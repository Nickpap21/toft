package rollout

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/Nickpap21/toft/backend/internal/netplan"
	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockerimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/versions"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

// Slot is the container a deploy starts for a service: what it runs, and
// the labels the panel gives it (its routing and identity).
type Slot struct {
	ServiceID   string
	ServiceName string
	EnvName     string
	Image       string
	Labels      map[string]string
	Env         []string
	Command     string // split on spaces; "" runs the image's
	CPUCores    float64
	MemoryBytes int64
	Volumes     []Volume
	LokiURL     string
	// Loopback publishes the slot's port on 127.0.0.1 of its host, and on
	// no other address: the catalog Loki's, for that server's log shippers
	// (D-148). The zero value publishes nothing, as for every other service.
	Loopback Loopback
	// BasicAuth is the service's basic-auth user and password, so the new
	// slot can keep a serving slot's entry (reuseBasicAuth).
	BasicAuth BasicAuth
	Security  Security
	// Check is the service's own Docker health check, in place of its
	// image's HEALTHCHECK; the zero value keeps the image's. An agent
	// announces that it applies it (agentproto.CapContainerCheck).
	Check ContainerCheck
}

// ContainerCheck is a service's own Docker health check: Off turns the
// image's HEALTHCHECK off, Command replaces it with a shell command run at
// the image's interval, timeout and retries (Docker's defaults without
// one). The zero value keeps the image's.
type ContainerCheck struct {
	Off     bool   `json:",omitempty"`
	Command string `json:",omitempty"`
}

// override is the HEALTHCHECK the container is created with in place of
// image, the image's own (nil: none); nil keeps the image's.
func (c ContainerCheck) override(image *container.HealthConfig) *container.HealthConfig {
	switch {
	case c.Off:
		return &container.HealthConfig{Test: []string{"NONE"}}
	case c.Command != "":
		hc := &container.HealthConfig{}
		if image != nil {
			*hc = *image
		}
		hc.Test = []string{"CMD-SHELL", c.Command}
		return hc
	}
	return nil
}

// Loopback is a slot's Target port published on 127.0.0.1:Port.
type Loopback struct {
	Port   int `json:"port,omitempty"`
	Target int `json:"target,omitempty"`
}

// Volume is a volume mounted into a slot: a shared volume (Shared), a bind
// mount (HostPath) or the service's named volume (Name).
type Volume struct {
	Name     string
	Target   string
	HostPath string
	ReadOnly bool
	Shared   string
}

// BasicAuth is a service's basic-auth user and password.
type BasicAuth struct {
	User     string
	Password string
}

// StartSlot starts the inactive slot ("blue" on a first deploy)
// and leaves the service's serving containers alone.
//
// It records the new container and the containers that were serving in
// sw, and the caller completes the switch once the new slot is healthy:
// FinishSwitch retires the others, or AbortSwitch removes the new one so the
// previous one keeps serving.
//
// Until then both slots run. With the same Traefik labels (see
// reuseBasicAuth) Traefik serves the service from both; with labels that
// define a router, middleware or service differently, it drops those, which
// the SlotSwitch reports as RoutingChanged.
//
// The container joins plan.Home with the service's alias. Under isolated
// app networks it is labelled with its network (paas.network and, when
// routed, traefik.docker.network) and, once started, published into the
// plan's consumer networks; a publish failure is only a warning (warn).
func StartSlot(ctx context.Context, dc *client.Client, cfg NetConfig, plan netplan.Plan, s Slot, sw *SlotSwitch, warn func(string)) error {
	serviceID, serviceName, envName := s.ServiceID, s.ServiceName, s.EnvName
	if err := plan.Valid(); err != nil {
		return err
	}
	if plan.ServiceID != serviceID {
		return fmt.Errorf("network plan is for service %s, not %s", plan.ServiceID, serviceID)
	}
	// A resumed rollout (sw.New set) keeps the slot it chose before the
	// restart: the previous one may be stopped by now.
	newName := sw.New
	if newName == "" {
		if PickActiveSlot(RunningSlots(ctx, dc, serviceID, serviceName, envName), sw.ActiveDeployment) == "blue" {
			newName = GreenContainerName(serviceID, serviceName, envName)
		} else {
			newName = BlueContainerName(serviceID, serviceName, envName) // first deploy OR previous was green
		}
	}

	// Remove the container with the new slot's name: a stopped leftover of an
	// earlier attempt or, when both slots run, the one that does not serve
	// (PickActiveSlot), left by a switch that could not complete. One this
	// deployment created before a restart is kept and reused.
	staleContainers, _ := dc.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("name", "/"+newName)),
	})
	reuse := ""
	for _, c := range staleContainers {
		if SummaryName(c) != newName { // the name filter also matches longer names
			continue
		}
		if dep := s.Labels[LabelDeploymentID]; dep != "" && c.Labels[LabelDeploymentID] == dep && c.Labels[LabelServiceID] == serviceID {
			reuse = c.ID
			continue
		}
		timeout := 5
		_ = dc.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &timeout})
		_ = dc.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true})
	}

	// What serves the service now: the active slot, or containers left by a
	// rename of the service. They keep running until the switch completes.
	list, err := dc.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", LabelServiceID+"="+serviceID)),
	})
	if err != nil {
		return fmt.Errorf("ContainerList: %w", err)
	}
	var serving []container.Summary
	for _, c := range SlotsOf(list, serviceID) {
		if SummaryName(c) != newName {
			serving = append(serving, c)
		}
	}

	labels := make(map[string]string, len(s.Labels)+2)
	for k, v := range s.Labels {
		labels[k] = v
	}
	fresh := labels[BasicAuthUsersLabel(serviceID)]
	weak := reuseBasicAuth(labels, serviceID, serving, s.BasicAuth)
	if !plan.Legacy {
		labels[netplan.LabelContainerNetwork] = plan.Home.Name
		if labels["traefik.enable"] == "true" {
			labels["traefik.docker.network"] = plan.Home.Name
		}
	}

	// What served before a restart stays in Previous and Stopped: a slot
	// stopped then is no longer running now.
	sw.New = newName
	for _, c := range serving {
		sw.Previous = appendNew(sw.Previous, SummaryName(c))
		if sw.Recreate {
			sw.Stopped = appendNew(sw.Stopped, SummaryName(c))
		}
	}
	sw.RoutingChanged = routingConflicts(labels, serving)
	// An entry at an old, lower cost gets the platform's where the new
	// entry costs no time without routes: the previous slots stop first, or
	// Traefik reads route files. Elsewhere it stays until one of those, or a
	// new password: even beside another routing change it would take down
	// the routers that change leaves serving.
	if weak && (sw.Recreate || sw.FileRoutes) {
		labels[BasicAuthUsersLabel(serviceID)] = fresh
	}
	if sw.Chosen != nil {
		sw.Chosen()
	}
	if sw.Recreate {
		if err := stopPrevious(ctx, dc, sw, serving); err != nil {
			return err
		}
	}

	// Start the new slot.
	id := reuse
	if id != "" {
		if err := dc.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
			return fmt.Errorf("ContainerStart %s: %w", newName, err)
		}
	} else if id, err = runContainerWithName(ctx, dc, cfg, plan, newName, s, labels, !sw.FileRoutes, warn); err != nil {
		return err
	}
	if !plan.Legacy && len(plan.Publish) > 0 {
		ops := newNetOps(dc, cfg, false, warn)
		var aliases []string
		if plan.Alias != "" {
			aliases = []string{plan.Alias}
		}
		ops.publish(ctx, plan, containerRef{id: id, name: newName, networks: map[string]bool{plan.Home.Name: true}}, aliases)
	}
	return nil
}

// runContainerWithName creates and starts s's container, named name, with
// labels and the log driver logConfig picks. It joins plan.Home with
// plan.Alias and returns the container's ID. A routed container gets the
// drain health check (drain.go) when Traefik reads labels (labelRoutes).
func runContainerWithName(ctx context.Context, dc *client.Client, cfg NetConfig, plan netplan.Plan, name string, s Slot, labels map[string]string, labelRoutes bool, warn func(string)) (string, error) {
	containerName, serviceID, imageTag, sec := name, s.ServiceID, s.Image, s.Security
	// Create named volumes / register bind mounts and build mount list.
	mounts := make([]mount.Mount, 0, len(s.Volumes))
	idPrefix := serviceID
	if len(idPrefix) > 8 {
		idPrefix = idPrefix[:8]
	}
	for _, v := range s.Volumes {
		if v.Shared != "" {
			// Shared volume (D-38): the workspace's, named by the platform;
			// created the first time a service that mounts it is deployed.
			if _, err := dc.VolumeCreate(ctx, volume.CreateOptions{Name: v.Shared}); err != nil {
				return "", fmt.Errorf("VolumeCreate %s: %w", v.Shared, err)
			}
			mounts = append(mounts, mount.Mount{
				Type:     mount.TypeVolume,
				Source:   v.Shared,
				Target:   v.Target,
				ReadOnly: v.ReadOnly,
			})
			continue
		}
		if v.HostPath != "" {
			// Bind mount: map a host directory directly into the container.
			mounts = append(mounts, mount.Mount{
				Type:     mount.TypeBind,
				Source:   v.HostPath,
				Target:   v.Target,
				ReadOnly: v.ReadOnly,
			})
		} else {
			// Named volume: create if it doesn't exist, then mount.
			volName := "paas-vol-" + idPrefix + "-" + v.Name
			if _, err := dc.VolumeCreate(ctx, volume.CreateOptions{Name: volName}); err != nil {
				return "", fmt.Errorf("VolumeCreate %s: %w", volName, err)
			}
			mounts = append(mounts, mount.Mount{
				Type:   mount.TypeVolume,
				Source: volName,
				Target: v.Target,
			})
		}
	}

	// The health check probe's tool (probe.go). Without it the container is
	// not probed, which is no reason to fail the deploy.
	probe := true
	if m, err := probeToolMount(ctx, dc); err != nil {
		probe = false
		if warn != nil {
			warn("health check probe not mounted, so the service is checked at deploy only: " + err.Error())
		}
	} else {
		mounts = append(mounts, m)
	}
	mounts = append(mounts, sec.Mounts()...)

	hostConfig := &container.HostConfig{
		NetworkMode:   container.NetworkMode(plan.Home.Name),
		RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
		SecurityOpt:   []string{"no-new-privileges:true"},
		CapDrop:       sec.CapDrop(),
		Mounts:        mounts,
		Runtime:       sec.Runtime, // gVisor for a Sandboxed service (D-95)
	}
	pids := sec.Pids()
	hostConfig.PidsLimit = &pids
	hostConfig.CgroupParent = cfg.AppsSlice // "" outside the panel's server
	applyResourceLimits(hostConfig, s.CPUCores, s.MemoryBytes)
	hostConfig.LogConfig = logConfig(cfg.LocalLogs, s.LokiURL, containerName, serviceID)

	var netConfig *network.NetworkingConfig
	if plan.Alias != "" || !plan.Legacy {
		ep := &network.EndpointSettings{}
		if plan.Alias != "" {
			ep.Aliases = []string{plan.Alias}
		}
		netConfig = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{plan.Home.Name: ep},
		}
	}

	img, err := dc.ImageInspect(ctx, imageTag)
	if err != nil {
		return "", fmt.Errorf("ImageInspect %s: %w", imageTag, err)
	}
	routed := labels["traefik.enable"] == "true"
	labels, err = imageLabelsBlanked(img, imageTag, labels, routed)
	if err != nil {
		return "", err
	}
	containerCfg := &container.Config{Image: imageTag, Labels: labels, Env: s.Env}
	if l := s.Loopback; l.Port > 0 && l.Target > 0 {
		p := nat.Port(strconv.Itoa(l.Target) + "/tcp")
		containerCfg.ExposedPorts = nat.PortSet{p: {}}
		hostConfig.PortBindings = nat.PortMap{p: {{HostIP: "127.0.0.1", HostPort: strconv.Itoa(l.Port)}}}
	}
	var own *container.HealthConfig
	var shell []string
	if img.Config != nil {
		own, shell = img.Config.Healthcheck, img.Config.Shell
	}
	// The service's own check replaces the image's, drained or not.
	override := s.Check.override(own)
	if override != nil {
		own = override
	}
	if probe && labelRoutes && routed {
		if hc, ok := drainHealthcheck(own, shell); ok {
			containerCfg.Healthcheck = hc
			hostConfig.Tmpfs = map[string]string{drainDir: "mode=1777,size=64k"}
		}
	}
	if containerCfg.Healthcheck == nil {
		containerCfg.Healthcheck = override
	}
	if s.Command != "" {
		containerCfg.Cmd = strings.Fields(s.Command)
	}

	resp, err := dc.ContainerCreate(ctx, containerCfg, hostConfig, netConfig, nil, containerName)
	if err != nil {
		return "", fmt.Errorf("ContainerCreate: %w", err)
	}
	if err := dc.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return resp.ID, fmt.Errorf("ContainerStart: %w", err)
	}
	return resp.ID, nil
}

// LocalLogConfig is Docker's local log driver capped at 20 MiB × 5 files
// per container: on agent servers, the spool the agent ships to Loki from
// (D-113). Slots and compose stacks (the platform's override) get it.
func LocalLogConfig() container.LogConfig {
	return container.LogConfig{Type: "local", Config: map[string]string{"max-size": "20m", "max-file": "5"}}
}

// logConfig is a slot's log driver: the local one on an agent server
// (local), whose agent ships the lines; elsewhere, with a Loki URL, the
// Loki plugin (ensureLokiDriver installs it at provisioning), with the
// labels the agent ships with too; else Docker's default.
func logConfig(local bool, lokiURL, containerName, serviceID string) container.LogConfig {
	switch {
	case local:
		return LocalLogConfig()
	case lokiURL != "":
		return container.LogConfig{
			Type: "loki",
			Config: map[string]string{
				"loki-url":             lokiURL + "/loki/api/v1/push",
				"loki-retries":         "5",
				"loki-batch-size":      "400",
				"loki-external-labels": "container_name=" + containerName + ",service_id=" + serviceID,
				"loki-timeout":         "1s",
				"keep-file":            "true", // keep local journal so docker logs still works
				// While Loki is away (its stop-then-start deploy, D-148) the
				// driver retries; non-blocking keeps the app's writes from
				// waiting on it, dropping lines past the buffer instead.
				"mode":            "non-blocking",
				"max-buffer-size": "4m",
			},
		}
	}
	return container.LogConfig{}
}

// applyResourceLimits sets a CPU limit (cores, 0: none) and a memory limit
// (bytes, 0: none). Swap equals memory: the container is stopped at its
// limit rather than spilling into swap (D-41).
func applyResourceLimits(hc *container.HostConfig, cpuCores float64, memoryBytes int64) {
	if cpuCores > 0 {
		hc.CPUQuota = int64(cpuCores * 1e5)
		hc.CPUPeriod = 1e5
	}
	hc.Memory = memoryBytes
	hc.MemorySwap = hc.Memory
}

// ProbeToolImage is busybox 1.37-musl, a static binary, pinned by digest (a
// multi-arch index, amd64 and arm64): the health check probe's tool, and
// the image server checks a runtime with.
const ProbeToolImage = "busybox@sha256:5cec3fc171c87218698e85a52af7087de727372aae264a787b8112901a5b0092"

const ProbeMountPath = "/.toft-probe"

// probeMinAPI is the first Docker API with image mounts (Engine 28; D-29
// requires 29).
const probeMinAPI = "1.48"

// probeToolMount is the mount that puts ProbeToolImage in a container,
// pulling the image first when the server lacks it. It fails on a Docker API
// without image mounts or when the image cannot be had: the container is
// then created without it and is not probed.
func probeToolMount(ctx context.Context, dc *client.Client) (mount.Mount, error) {
	if v := dc.ClientVersion(); !versions.GreaterThanOrEqualTo(v, probeMinAPI) {
		return mount.Mount{}, fmt.Errorf("docker API %s has no image mounts (needs %s)", v, probeMinAPI)
	}
	if _, err := dc.ImageInspect(ctx, ProbeToolImage); err != nil {
		if !client.IsErrNotFound(err) {
			return mount.Mount{}, fmt.Errorf("inspect the probe image: %w", err)
		}
		rc, err := dc.ImagePull(ctx, ProbeToolImage, dockerimage.PullOptions{})
		if err != nil {
			return mount.Mount{}, fmt.Errorf("pull the probe image: %w", err)
		}
		_, err = io.Copy(io.Discard, rc)
		rc.Close()
		if err != nil {
			return mount.Mount{}, fmt.Errorf("pull the probe image: %w", err)
		}
	}
	return mount.Mount{Type: mount.TypeImage, Source: ProbeToolImage, Target: ProbeMountPath, ReadOnly: true}, nil
}

// identityLabelPrefixes are the label prefixes that say which service,
// deployment, network or compose project a container belongs to. A
// container inherits every label of its image, and tenants choose their
// images.
var identityLabelPrefixes = []string{"paas.", "com.docker.compose."}

// blankedIdentityLabels returns labels plus an empty value for every
// identity label (identityLabelPrefixes) the image carries that labels does
// not set, so a container the platform creates from a tenant's image never
// inherits a paas.* or com.docker.compose.* label it did not set itself.
// labels is not changed.
func blankedIdentityLabels(imageLabels, labels map[string]string) map[string]string {
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		out[k] = v
	}
	for k := range imageLabels {
		if _, set := out[k]; set {
			continue
		}
		for _, p := range identityLabelPrefixes {
			if strings.HasPrefix(strings.ToLower(k), p) {
				out[k] = ""
				break
			}
		}
	}
	return out
}

// WithImageLabelsBlanked inspects image and returns blankedIdentityLabels.
// It fails when the image cannot be inspected: the container is not created
// without knowing what it would inherit.
//
// routed is set for a container Traefik routes (traefik.enable=true): an
// image that carries traefik.* labels is then refused, because the platform
// sets traefik.enable=true and every extra router the image defines would go
// live (another domain, Traefik's API or metrics). This holds under both app
// network modes: Traefik joins every app network, and under legacy the
// control plane's network, which every app is on.
func WithImageLabelsBlanked(ctx context.Context, dc *client.Client, image string, labels map[string]string, routed bool) (map[string]string, error) {
	img, err := dc.ImageInspect(ctx, image)
	if err != nil {
		return nil, fmt.Errorf("ImageInspect %s: %w", image, err)
	}
	return imageLabelsBlanked(img, image, labels, routed)
}

// imageLabelsBlanked is WithImageLabelsBlanked for an image already inspected.
func imageLabelsBlanked(img dockerimage.InspectResponse, image string, labels map[string]string, routed bool) (map[string]string, error) {
	var imageLabels map[string]string
	if img.Config != nil {
		imageLabels = img.Config.Labels
	}
	if routed {
		if keys := TraefikKeys(imageLabels); len(keys) > 0 {
			return nil, fmt.Errorf("image %s carries traefik.* labels (%s), which would add routes of their own; "+
				"the platform routes the service's domains itself: remove them from the image", image, strings.Join(keys, ", "))
		}
	}
	return blankedIdentityLabels(imageLabels, labels), nil
}

// TraefikKeys returns the traefik.* keys of labels, sorted.
func TraefikKeys(labels map[string]string) []string {
	var keys []string
	for k := range labels {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(k)), "traefik.") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// Security is what the hardened Standard runtime (D-93) sets on every
// container the platform makes for a service, besides no-new-privileges and
// Docker's default seccomp and AppArmor: a process limit, and NET_RAW
// dropped unless an operator granted the net_raw exception. A Sandboxed
// service (D-95) also runs under gVisor.
type Security struct {
	PidsLimit int64 // 0: DefaultPidsLimit
	NetRaw    bool
	Runtime   string // "" is Docker's default (runc); RuntimeSandboxed or RuntimeStrict
}

// RuntimeSandboxed is the Docker runtime of a Sandboxed service: gVisor's
// runsc, as the gVisor install registers it (D-95).
const RuntimeSandboxed = "runsc"

// RuntimeStrict is the Docker runtime of a Sandboxed service with a strict
// network: runsc with gVisor's own network stack, resolving through the
// agent's DNS relay (D-115).
const RuntimeStrict = "runsc-strict"

// StrictResolvConf is where a strict container's /etc/resolv.conf comes
// from: Docker's 127.0.0.11 is out of gVisor's network stack's reach, so it
// names the agent's DNS relay instead (D-115).
const StrictResolvConf = agentproto.StrictResolvConf + ":/etc/resolv.conf:ro"

// Mounts are the mounts the runtime needs: the relay's resolv.conf under
// RuntimeStrict, none otherwise.
func (s Security) Mounts() []mount.Mount {
	if s.Runtime != RuntimeStrict {
		return nil
	}
	return []mount.Mount{{Type: mount.TypeBind, Source: agentproto.StrictResolvConf, Target: "/etc/resolv.conf", ReadOnly: true}}
}

// DefaultPidsLimit is a container's process limit (threads count too)
// unless its service sets one.
const DefaultPidsLimit = 2048

// Pids is the process limit to set.
func (s Security) Pids() int64 {
	if s.PidsLimit > 0 {
		return s.PidsLimit
	}
	return DefaultPidsLimit
}

// CapDrop is the capabilities to drop.
func (s Security) CapDrop() []string {
	if s.NetRaw {
		return nil
	}
	return []string{"NET_RAW"}
}
