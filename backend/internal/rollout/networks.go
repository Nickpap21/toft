package rollout

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"

	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/netplan"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

// ─── App networks (package C; D-19, D-26, D-31) ──────────────────────────────
//
// Under TOFT_APP_NETWORKS=isolated every environment has its own bridge
// network (netplan), created on the host that runs it. The proxy (Traefik)
// stays on the control plane's network and is connected to every app
// network on its host. Shared services join their consumers' networks too.
// Under legacy (netplan.Plan.Legacy) none of this runs.
//
// Which containers are attached is decided from what the platform can
// trust: a slot's reserved name together with its paas.service_id label
// (SlotsOf), or the platform-derived compose project name. A container that
// only carries a label because its image does is never attached anywhere.

// NetConfig is how the orchestrators run app networks on a host.
type NetConfig struct {
	// ProxyContainer is the Traefik container on every host
	// (TRAEFIK_CONTAINER, default paas-traefik).
	ProxyContainer string
	// LegacyNetwork is the control plane's network (TRAEFIK_NETWORK).
	LegacyNetwork string
	// InternalNetwork is Postgres's network on the control-plane server
	// (paas-internal); apps are detached from it too.
	InternalNetwork string
	// HostKey tells hosts apart for the per-network create locks.
	HostKey string
	// AppsSlice is the systemd slice apps run under on this host: on the
	// panel's server, toft-apps.slice, which caps them at what the panel's
	// reservation leaves (TOFT_APPS_SLICE, D-26, D-62); "" elsewhere.
	AppsSlice string
	// LocalLogs is set on agent servers (docker.Factory.ForServer): every
	// container logs to Docker's local driver (LocalLogConfig) and the
	// agent ships the lines to Loki, not the Loki plugin (D-113).
	LocalLogs bool
}

// panelContainers are the control plane's own containers: never detached
// from the control plane's network, whatever they carry.
var panelContainers = map[string]bool{"paas-backend": true, "paas-postgres": true, "paas-frontend": true}

// netLocks serialises the creation of one network on one host within this
// process (Docker 25+ also refuses duplicate names).
var netLocks sync.Map // HostKey + "\x00" + name → *sync.Mutex

func lockNetwork(hostKey, name string) func() {
	m, _ := netLocks.LoadOrStore(hostKey+"\x00"+name, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// netOps applies network changes on one host, or, when dry, only records
// what it would change.
type netOps struct {
	dc     *client.Client
	cfg    NetConfig
	dry    bool
	report *netplan.Report
	logFn  func(string)
	// created are the networks this run created (or, dry, would create).
	created map[string]bool
}

func newNetOps(dc *client.Client, cfg NetConfig, dry bool, logFn func(string)) *netOps {
	if logFn == nil {
		logFn = func(string) {}
	}
	return &netOps{dc: dc, cfg: cfg.WithDefaults(), dry: dry, report: &netplan.Report{}, logFn: logFn, created: map[string]bool{}}
}

// addressPoolHint is added to an error creating a network when the
// daemon has run out of address space.
const addressPoolHint = "; Docker has no free address pool for another network: set default-address-pools in /etc/docker/daemon.json " +
	"(the panel uses " + netplan.DefaultAddressPool + " in /24s on fresh servers) and restart Docker"

// containerRef is a running container and the networks it is on.
type containerRef struct {
	id       string
	name     string
	labels   map[string]string
	networks map[string]bool
}

func refOf(c container.Summary) containerRef {
	nets := map[string]bool{}
	if c.NetworkSettings != nil {
		for n := range c.NetworkSettings.Networks {
			nets[n] = true
		}
	}
	return containerRef{id: c.ID, name: SummaryName(c), labels: c.Labels, networks: nets}
}

// endpointContainer is one of a service's running containers and the
// aliases it answers to.
type endpointContainer struct {
	ref     containerRef
	aliases []string
}

// gcGrace is how old an app network must be before collectGarbage may
// remove it: a deploy that just created it has not created its container
// yet.
var gcGrace = 15 * time.Minute

// ReconcileHost converges a host on plans in mode: every service with
// running containers gets its networks prepared (created, attached,
// published), the proxy joins every app network, unused empty app networks
// are removed and, in isolate mode, app containers leave the control
// plane's network. Report mode records the same without changing anything.
//
// serviceIDs are every service that exists (a slot of any other is an
// orphan), plans those of the host's services whose plan could be built.
func ReconcileHost(ctx context.Context, dc *client.Client, cfg NetConfig, plans []netplan.Plan, serviceIDs []string, mode netplan.MigrationMode, logFn func(string)) (netplan.Report, error) {
	if mode == netplan.MigrationOff {
		return netplan.Report{}, nil
	}
	o := newNetOps(dc, cfg, !mode.Changes(), logFn)
	var errs []error
	active := make([]netplan.Plan, 0, len(plans))
	for _, p := range plans {
		if p.Legacy {
			continue
		}
		active = append(active, p)
		own, err := o.ownContainers(ctx, p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if len(own) == 0 {
			continue // nothing runs here: its networks come with its next deploy
		}
		o.reportForeignNetworks(p, own)
		if err := o.prepare(ctx, p); err != nil {
			o.report.Errors = append(o.report.Errors, fmt.Sprintf("%s (%s): %v", p.ServiceName, p.ServiceID, err))
		}
	}
	if !o.dry {
		if _, err := o.attachProxyToManaged(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if err := o.collectGarbage(ctx, active); err != nil {
		errs = append(errs, err)
	}
	if mode.Detaches() || mode == netplan.MigrationReport {
		if err := o.isolate(ctx, active, serviceIDs); err != nil {
			errs = append(errs, err)
		}
	}
	for _, err := range errs {
		o.report.Errors = append(o.report.Errors, err.Error())
	}
	o.report.Sort()
	return *o.report, nil
}

func (c NetConfig) WithDefaults() NetConfig {
	if c.ProxyContainer == "" {
		c.ProxyContainer = "paas-traefik"
	}
	if c.LegacyNetwork == "" {
		c.LegacyNetwork = netplan.LegacyNetwork
	}
	if c.InternalNetwork == "" {
		c.InternalNetwork = "paas-internal"
	}
	if c.HostKey == "" {
		c.HostKey = "local"
	}
	return c
}

// ensureNetwork makes sure n exists on the host as the platform created it.
// A network of that name without the platform's labels for n is refused,
// never joined.
func (o *netOps) ensureNetwork(ctx context.Context, n netplan.Network) error {
	if err := n.Valid(); err != nil {
		return err
	}
	if o.dry && o.created[n.Name] {
		return nil
	}
	unlock := lockNetwork(o.cfg.HostKey, n.Name)
	defer unlock()

	ok, err := o.inspectOwned(ctx, n)
	if err == nil {
		if !ok {
			return fmt.Errorf("network %s exists but was not created by the panel for this %s; remove or rename it", n.Name, n.Kind)
		}
		return nil
	}
	if !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("inspect network %s: %w", n.Name, err)
	}
	o.report.Created = append(o.report.Created, n.Name)
	o.created[n.Name] = true
	if o.dry {
		return nil
	}
	opts := network.CreateOptions{Driver: "bridge", Labels: n.Labels()}
	if b := n.BridgeName(); b != "" {
		opts.Options = map[string]string{"com.docker.network.bridge.name": b}
	}
	if _, cerr := o.dc.NetworkCreate(ctx, n.Name, opts); cerr != nil {
		// Another creator may have won the race.
		if ok, err := o.inspectOwned(ctx, n); err == nil && ok {
			return nil
		}
		msg := cerr.Error()
		if strings.Contains(msg, "non-overlapping") || strings.Contains(msg, "address pool") {
			msg += addressPoolHint
		}
		return fmt.Errorf("create network %s: %s", n.Name, msg)
	}
	o.logFn("→ created network " + n.Name)
	return nil
}

// inspectOwned inspects network n.Name: ok reports that it exists as the
// platform created it for n (labels, bridge driver, exact name). A NotFound
// error means it does not exist.
func (o *netOps) inspectOwned(ctx context.Context, n netplan.Network) (bool, error) {
	res, err := o.dc.NetworkInspect(ctx, n.Name, network.InspectOptions{})
	if err != nil {
		return false, err
	}
	return res.Name == n.Name && res.Driver == "bridge" && n.Matches(res.Labels), nil
}

// connect connects container c to networkName with aliases unless it is
// already connected. A failed connect is re-checked by inspecting the
// container rather than by matching error strings.
func (o *netOps) connect(ctx context.Context, networkName string, c containerRef, aliases []string) error {
	if c.networks[networkName] {
		return nil
	}
	o.report.Attached = append(o.report.Attached, c.name+" → "+networkName)
	if o.dry {
		c.networks[networkName] = true
		return nil
	}
	// A connected network never becomes the container's default gateway:
	// the proxy keeps its published ports and outbound route on the control
	// plane's network, and an app keeps its home's (Docker 28+).
	err := o.dc.NetworkConnect(ctx, networkName, c.id, &network.EndpointSettings{Aliases: aliases, GwPriority: -1})
	if err != nil {
		ins, ierr := o.dc.ContainerInspect(ctx, c.id)
		if cerrdefs.IsNotFound(ierr) {
			// Removed meanwhile (a concurrent deploy retired it).
			o.logFn(fmt.Sprintf("→ %s is gone; not connected to %s", c.name, networkName))
			return nil
		}
		if ierr == nil && ins.NetworkSettings != nil {
			if _, ok := ins.NetworkSettings.Networks[networkName]; ok {
				return nil
			}
		}
		return fmt.Errorf("connect %s to %s: %w", c.name, networkName, err)
	}
	c.networks[networkName] = true
	return nil
}

// onAny reports whether c is on one of names, or, with managed, on any
// network named like an app network.
func (c containerRef) onAny(managed bool, names ...string) bool {
	for _, n := range names {
		if n != "" && c.networks[n] {
			return true
		}
	}
	if managed {
		for n := range c.networks {
			if netplan.IsManagedName(n) {
				return true
			}
		}
	}
	return false
}

// endpointContainers returns ep's running containers: its slots (label and
// reserved name) with ep.Alias, or the containers of its compose project
// with their compose service name. keep filters compose containers (a stack
// keeps its private containers off app networks): only those it accepts
// are returned.
func (o *netOps) endpointContainers(ctx context.Context, ep netplan.Endpoint, keep func(containerRef) bool) ([]endpointContainer, error) {
	var out []endpointContainer
	if !ep.Compose {
		list, err := o.dc.ContainerList(ctx, container.ListOptions{
			Filters: filters.NewArgs(filters.Arg("label", LabelServiceID+"="+ep.ServiceID)),
		})
		if err != nil {
			return nil, fmt.Errorf("ContainerList: %w", err)
		}
		var aliases []string
		if ep.Alias != "" {
			aliases = []string{ep.Alias}
		}
		for _, c := range SlotsOf(list, ep.ServiceID) {
			out = append(out, endpointContainer{ref: refOf(c), aliases: aliases})
		}
		return out, nil
	}
	for _, proj := range ComposeProjectNames(ep.ServiceID, ep.ServiceName) {
		list, err := o.dc.ContainerList(ctx, container.ListOptions{
			Filters: filters.NewArgs(filters.Arg("label", LabelComposeProject+"="+proj)),
		})
		if err != nil {
			return nil, fmt.Errorf("ContainerList: %w", err)
		}
		for _, c := range list {
			if c.Labels[LabelComposeProject] != proj || IsPlatformSlot(c.Labels, SummaryName(c)) {
				continue
			}
			ref := refOf(c)
			if keep != nil && !keep(ref) {
				continue
			}
			var aliases []string
			if svc := c.Labels["com.docker.compose.service"]; composespec.ValidServiceName(svc) {
				aliases = []string{svc}
			}
			out = append(out, endpointContainer{ref: ref, aliases: aliases})
		}
	}
	return out, nil
}

// ownContainers returns plan's service's own running containers that
// belong on its networks: every slot, and the compose containers already on
// the control plane's network (from before isolation) or on an app network
// (the routed service, and services that named the control plane's
// network). Stack-private compose containers are left alone (D-31).
func (o *netOps) ownContainers(ctx context.Context, plan netplan.Plan) ([]endpointContainer, error) {
	ep := netplan.Endpoint{ServiceID: plan.ServiceID, ServiceName: plan.ServiceName, Alias: plan.Alias, Compose: plan.Compose}
	return o.endpointContainers(ctx, ep, func(c containerRef) bool {
		return c.onAny(true, o.cfg.LegacyNetwork)
	})
}

// proxy returns the proxy container, or ok false when there is none.
func (o *netOps) proxy(ctx context.Context) (containerRef, bool, error) {
	ins, err := o.dc.ContainerInspect(ctx, o.cfg.ProxyContainer)
	if cerrdefs.IsNotFound(err) {
		return containerRef{}, false, nil
	}
	if err != nil {
		return containerRef{}, false, fmt.Errorf("inspect %s: %w", o.cfg.ProxyContainer, err)
	}
	if strings.TrimPrefix(ins.Name, "/") != o.cfg.ProxyContainer {
		return containerRef{}, false, nil
	}
	ref := containerRef{id: ins.ID, name: o.cfg.ProxyContainer, networks: map[string]bool{}}
	if ins.Config != nil {
		ref.labels = ins.Config.Labels
	}
	if ins.NetworkSettings != nil {
		for n := range ins.NetworkSettings.Networks {
			ref.networks[n] = true
		}
	}
	return ref, true, nil
}

// prepare makes the host ready for plan: Home exists, the proxy is on it,
// the service's own containers are on it (and published into its consumer
// networks), and the Attach services' containers are on it. A missing proxy
// is a warning: the service runs, unrouted.
func (o *netOps) prepare(ctx context.Context, plan netplan.Plan) error {
	if err := plan.Valid(); err != nil {
		return err
	}
	if plan.Legacy {
		return nil
	}
	home := plan.Home.Name
	if err := o.ensureNetwork(ctx, plan.Home); err != nil {
		return err
	}

	px, ok, err := o.proxy(ctx)
	switch {
	case err != nil:
		return err
	case !ok:
		o.logFn(fmt.Sprintf("⚠ proxy container %s not found: %s is not routed until it runs", o.cfg.ProxyContainer, home))
	default:
		if err := o.connect(ctx, home, px, nil); err != nil {
			return err
		}
	}

	var errs []error
	own, err := o.ownContainers(ctx, plan)
	if err != nil {
		return err
	}
	for _, c := range own {
		if err := o.connect(ctx, home, c.ref, c.aliases); err != nil {
			errs = append(errs, err)
			continue
		}
		o.publish(ctx, plan, c.ref, c.aliases)
	}
	for _, ep := range plan.Attach {
		cs, err := o.endpointContainers(ctx, ep, func(c containerRef) bool {
			// A compose peer's containers that serve outside its stack.
			return c.onAny(true, o.cfg.LegacyNetwork)
		})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, c := range cs {
			if err := o.connect(ctx, home, c.ref, c.aliases); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// publish connects a shared service's container c into each of plan's
// consumer networks that exists on this host as the platform created it.
// Failures are warnings: the service itself runs.
func (o *netOps) publish(ctx context.Context, plan netplan.Plan, c containerRef, aliases []string) {
	for _, n := range plan.Publish {
		ok, err := o.inspectOwned(ctx, n)
		switch {
		case cerrdefs.IsNotFound(err):
			continue // nothing consumes it on this host
		case err != nil:
			o.logFn(fmt.Sprintf("⚠ could not inspect %s: %v", n.Name, err))
			continue
		case !ok:
			o.logFn(fmt.Sprintf("⚠ %s exists but was not created by the panel; %s is not published into it", n.Name, c.name))
			continue
		}
		if err := o.connect(ctx, n.Name, c, aliases); err != nil {
			o.logFn("⚠ " + err.Error())
		}
	}
}

// attachProxyToManaged connects the proxy to every app network on the host
// it is not on yet. It returns how many it connected.
func (o *netOps) attachProxyToManaged(ctx context.Context) (int, error) {
	px, ok, err := o.proxy(ctx)
	if err != nil || !ok {
		return 0, err
	}
	nets, err := o.dc.NetworkList(ctx, network.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", netplan.LabelManaged+"=true")),
	})
	if err != nil {
		return 0, fmt.Errorf("NetworkList: %w", err)
	}
	n := 0
	var errs []error
	for _, nw := range nets {
		if !netplan.IsManagedName(nw.Name) || nw.Labels[netplan.LabelManaged] != "true" || px.networks[nw.Name] {
			continue
		}
		if err := o.connect(ctx, nw.Name, px, nil); err != nil {
			errs = append(errs, err)
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

// collectGarbage removes app networks that no plan uses as its home, that
// are older than gcGrace and that no container but the proxy uses, running
// or not. It holds the network's create lock, so a deploy preparing it
// waits.
func (o *netOps) collectGarbage(ctx context.Context, plans []netplan.Plan) error {
	homes := map[string]bool{}
	for _, p := range plans {
		homes[p.Home.Name] = true
	}
	nets, err := o.dc.NetworkList(ctx, network.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", netplan.LabelManaged+"=true")),
	})
	if err != nil {
		return fmt.Errorf("NetworkList: %w", err)
	}
	var errs []error
	for _, nw := range nets {
		if !netplan.IsManagedName(nw.Name) || homes[nw.Name] {
			continue
		}
		if err := o.collectNetwork(ctx, nw.Name); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (o *netOps) collectNetwork(ctx context.Context, name string) error {
	unlock := lockNetwork(o.cfg.HostKey, name)
	defer unlock()
	ins, err := o.dc.NetworkInspect(ctx, name, network.InspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect %s: %w", name, err)
	}
	if !ins.Created.IsZero() && time.Since(ins.Created) < gcGrace {
		return nil
	}
	// Members, stopped ones included (inspect lists running endpoints only).
	members, err := o.dc.ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("network", name))})
	if err != nil {
		return fmt.Errorf("list members of %s: %w", name, err)
	}
	var proxyID string
	for _, c := range members {
		if SummaryName(c) == o.cfg.ProxyContainer {
			proxyID = c.ID
			continue
		}
		return nil // in use
	}
	o.report.Removed = append(o.report.Removed, name)
	if o.dry {
		return nil
	}
	if proxyID != "" {
		if err := o.dc.NetworkDisconnect(ctx, name, proxyID, false); err != nil && !cerrdefs.IsNotFound(err) {
			return fmt.Errorf("disconnect the proxy from %s: %w", name, err)
		}
	}
	if err := o.dc.NetworkRemove(ctx, name); err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("remove %s: %w", name, err)
	}
	return nil
}

// isolate detaches app containers from the control plane's networks: the
// legacy network and Postgres's internal one. App containers are
// identified positively: a slot (reserved name and paas.service_id label)
// or a container of a known compose service's project. The proxy and the
// panel's own containers are never touched; anything else is reported as
// unknown. A container is never detached unless it is on an app network
// already, so nothing is cut off from every network: a known service
// whose home could not be prepared, a service on this host without a plan
// (serviceIDs that plans lacks) and an orphan (a slot of a service that no
// longer exists) stay attached and are reported.
func (o *netOps) isolate(ctx context.Context, plans []netplan.Plan, serviceIDs []string) error {
	planned := map[string]bool{}
	projects := map[string]bool{}
	for _, p := range plans {
		planned[p.ServiceID] = true
		if p.Compose {
			for _, proj := range ComposeProjectNames(p.ServiceID, p.ServiceName) {
				projects[proj] = true
			}
		}
	}
	exists := map[string]bool{}
	for _, id := range serviceIDs {
		exists[id] = true
	}
	var errs []error
	for _, netName := range []string{o.cfg.LegacyNetwork, o.cfg.InternalNetwork} {
		if err := o.isolateFrom(ctx, netName, planned, exists, projects); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (o *netOps) isolateFrom(ctx context.Context, netName string, planned, exists, projects map[string]bool) error {
	ins, err := o.dc.NetworkInspect(ctx, netName, network.InspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect %s: %w", netName, err)
	}
	ids := make([]string, 0, len(ins.Containers))
	for id := range ins.Containers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var errs []error
	for _, id := range ids {
		name := ins.Containers[id].Name
		if name == o.cfg.ProxyContainer || panelContainers[name] {
			continue
		}
		c, err := o.dc.ContainerInspect(ctx, id)
		if err != nil {
			if !cerrdefs.IsNotFound(err) {
				errs = append(errs, fmt.Errorf("inspect %s: %w", name, err))
			}
			continue
		}
		name = strings.TrimPrefix(c.Name, "/")
		var labels map[string]string
		if c.Config != nil {
			labels = c.Config.Labels
		}
		onApp := false
		if c.NetworkSettings != nil {
			for n := range c.NetworkSettings.Networks {
				if netplan.IsManagedName(n) {
					onApp = true
				}
			}
		}
		svcID := labels[LabelServiceID]
		slot := svcID != "" && IsSlotName(name, svcID)
		switch {
		case slot && !exists[svcID] && !planned[svcID]:
			o.report.Orphans = append(o.report.Orphans, name+" (on "+netName+"; remove it with docker rm -f)")
			continue
		case slot && !planned[svcID]:
			// Another server entry for this machine (a server shared by two
			// workspaces before shared servers), or a plan that could not
			// be built: that entry's reconcile, or the next deploy, moves it.
			o.report.Errors = append(o.report.Errors, name+": its service is not reconciled with this server entry (it is assigned to another entry for this machine, or its plan failed), so it was left on "+netName)
			continue
		case slot || projects[labels[LabelComposeProject]]:
			if !onApp && !o.dry {
				o.report.Errors = append(o.report.Errors, name+" is on no app network yet, so it was left on "+netName)
				continue
			}
		default:
			o.report.Unknown = append(o.report.Unknown, name+" (on "+netName+")")
			continue
		}
		o.report.Detached = append(o.report.Detached, name+" ✕ "+netName)
		if o.dry {
			continue
		}
		if err := o.dc.NetworkDisconnect(ctx, netName, id, false); err != nil && !cerrdefs.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("disconnect %s from %s: %w", name, netName, err))
		}
	}
	return errors.Join(errs...)
}

// reportForeignNetworks reports app containers that are on networks other
// than app networks, the control plane's and their own stack's (joined
// under legacy, for example another stack's network). They are not
// disconnected: the platform cannot tell what still needs them.
func (o *netOps) reportForeignNetworks(plan netplan.Plan, own []endpointContainer) {
	for _, c := range own {
		var foreign []string
		for n := range c.ref.networks {
			if netplan.IsManagedName(n) || n == o.cfg.LegacyNetwork || n == o.cfg.InternalNetwork {
				continue
			}
			if proj := c.ref.labels[LabelComposeProject]; plan.Compose && proj != "" && strings.HasPrefix(n, proj+"_") {
				continue
			}
			foreign = append(foreign, n)
		}
		if len(foreign) > 0 {
			sort.Strings(foreign)
			o.report.Errors = append(o.report.Errors, fmt.Sprintf("%s is also on %s, joined before isolation; disconnect it by hand if nothing needs it",
				c.ref.name, strings.Join(foreign, ", ")))
		}
	}
}

// PrepareNetworks makes the host ready for plan: its networks created and
// the proxy and shared services attached. A no-op under legacy.
func PrepareNetworks(ctx context.Context, dc *client.Client, cfg NetConfig, plan netplan.Plan, logFn func(string)) error {
	return newNetOps(dc, cfg, false, logFn).prepare(ctx, plan)
}

// AttachProxy connects the proxy to every managed app network on the host
// and returns how many it joined.
func AttachProxy(ctx context.Context, dc *client.Client, cfg NetConfig) (int, error) {
	return newNetOps(dc, cfg, false, nil).attachProxyToManaged(ctx)
}
