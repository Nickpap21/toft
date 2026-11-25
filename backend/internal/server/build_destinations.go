package server

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"go.uber.org/zap"
)

// Build destinations (D-170): the private addresses the builds on a server
// may reach. The host firewall keeps a build out of the private ranges
// (firewall.nft, from_untrusted); an instance operator lists what it may
// reach all the same, a registry or a mirror of their own. The panel
// writes a server's whole list into the firewall's build_destinations
// chain, which only the builder's network jumps to, never an untrusted
// preview's builder.

const (
	// buildDestinationsPath is the list as nft rules. toft-firewall.service
	// loads it after the table and apart from it: a file nft refuses leaves
	// the table loaded and the list empty.
	buildDestinationsPath = "/etc/toft/firewall.d/build-destinations.nft"
	// MaxBuildDestinations is how many entries a server's list holds.
	MaxBuildDestinations = 100
	// buildDestinationsTimeout bounds one write of a list to its server:
	// a save waits for it with a transaction open.
	buildDestinationsTimeout = 30 * time.Second
)

// buildDestinationsDelay is how long after a start the stored lists are
// written: toft upgrade loads the panel host's firewall once the new
// release is healthy, after the backend started (as panelProbeDelay).
var buildDestinationsDelay = 2 * time.Minute

// privateRanges are where a build destination may lie: the ranges the
// firewall closes to builds. build_destinations' CHECK holds the same four.
var privateRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("100.64.0.0/10"),
}

// BuildDestination is one entry of a server's list.
type BuildDestination struct {
	ID        string
	Prefix    netip.Prefix
	Port      *int32 // nil: every TCP port
	Note      string
	CreatedAt time.Time
}

// Address is the entry as an operator writes it: one address without /32.
func (d BuildDestination) Address() string { return prefixText(d.Prefix) }

func prefixText(p netip.Prefix) string {
	if p.IsSingleIP() {
		return p.Addr().String()
	}
	return p.String()
}

// BuildDestinationInput is a new entry.
type BuildDestinationInput struct {
	Address string `json:"address"`
	Port    *int   `json:"port"`
	Note    string `json:"note"`
}

// BuildDestinationResponse is an entry on the API.
type BuildDestinationResponse struct {
	ID        string    `json:"id"`
	Address   string    `json:"address"`
	Port      *int32    `json:"port"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

func toBuildDestinationResponse(d BuildDestination) BuildDestinationResponse {
	return BuildDestinationResponse{ID: d.ID, Address: d.Address(), Port: d.Port, Note: d.Note, CreatedAt: d.CreatedAt}
}

// BuildDestinationError is an entry the list doesn't take, with the API's
// code for the refusal and, for a malformed entry, its field.
type BuildDestinationError struct{ Code, Field, Message string }

func (e *BuildDestinationError) Error() string { return e.Message }

const (
	codeBuildDestinationInvalid = "build_destination_invalid"
	codeBuildDestinationExists  = "build_destination_exists"
	codeBuildDestinationPanel   = "build_destination_panel_address"
	codeBuildDestinationServer  = "build_destination_server_address"
	codeBuildDestinationDocker  = "build_destination_docker_network"
	codeBuildDestinationsFull   = "build_destinations_full"
	codeBuildDestinationsUnset  = "build_destinations_not_applied"
)

var (
	// ErrBuildDestinationsOperatorOnly: the list opens a server's private
	// network to every build on it, which no workspace role should grant.
	ErrBuildDestinationsOperatorOnly = errors.New("only instance operators can see or change what builds may reach")
	ErrBuildDestinationNotFound      = errors.New("no such build destination")
	// ErrBuildDestinationsNotApplied: the server didn't take the list (it
	// can't be reached, or its firewall refuses the rules), so the change
	// wasn't saved either.
	ErrBuildDestinationsNotApplied = errors.New("the server's firewall didn't take the list, so nothing was saved")
)

// parseBuildDestination checks a new entry: an IPv4 address or CIDR inside
// the private ranges, a port or none, a short note.
func parseBuildDestination(in BuildDestinationInput) (netip.Prefix, *int32, string, error) {
	invalid := func(field, msg string) (netip.Prefix, *int32, string, error) {
		return netip.Prefix{}, nil, "", &BuildDestinationError{Code: codeBuildDestinationInvalid, Field: field, Message: msg}
	}
	text := strings.TrimSpace(in.Address)
	p, err := netip.ParsePrefix(text)
	if err != nil {
		a, err := netip.ParseAddr(text)
		if err != nil {
			return invalid("address", "write an IPv4 address or a CIDR, like 10.0.5.20 or 10.0.5.0/24")
		}
		p = netip.PrefixFrom(a, a.BitLen())
	}
	if !p.Addr().Is4() {
		return invalid("address", "IPv4 only: the builders' networks have no IPv6")
	}
	if p != p.Masked() {
		return invalid("address", fmt.Sprintf("%s has address bits beyond its prefix: write %s for the network or %s for the one address", p, p.Masked(), p.Addr()))
	}
	if !slices.ContainsFunc(privateRanges, func(r netip.Prefix) bool { return r.Bits() <= p.Bits() && r.Contains(p.Addr()) }) {
		return invalid("address", "only addresses inside 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16 and 100.64.0.0/10: builds reach public addresses already, and loopback and link-local stay closed")
	}
	var port *int32
	if in.Port != nil {
		if *in.Port < 1 || *in.Port > 65535 {
			return invalid("port", "a port is 1 to 65535; leave it empty for every TCP port")
		}
		if *in.Port == 25 {
			return invalid("port", "port 25 stays closed: the firewall lets no build or app send mail on it")
		}
		n := int32(*in.Port)
		port = &n
	}
	note := strings.TrimSpace(in.Note)
	if utf8.RuneCountInString(note) > 200 || strings.ContainsFunc(note, unicode.IsControl) {
		return invalid("note", "a note is one line of up to 200 characters")
	}
	return p, port, note, nil
}

// hostNetworks is what a host says of itself: its own addresses (those
// inside a network of its Docker's left out: a bridge's gateway there) and
// its Docker's networks and address pools, where other apps' containers
// live.
type hostNetworks struct {
	Own    []netip.Addr
	Docker []netip.Prefix
}

// hostNetworksScript prints one own:, net: or pool: line a fact and "read"
// last: an answer without it is no answer. Every address is named, a
// bridge's too (a host may have its private address on one). A Docker with
// no default-address-pools set takes its networks from the pools built into
// it, so those are named. Each read is a command of its own, never the
// left side of a pipe: set -e sees it fail, and a host whose addresses
// couldn't be read gives no answer rather than a list that refuses nothing.
const hostNetworksScript = `exec 2>/dev/null
set -e
command -v ip >/dev/null
addrs=$(ip -4 -o addr show scope global)
printf '%s\n' "$addrs" | awk 'NF {print "own:" $4}'
nets=$(docker network ls -q)
[ -z "$nets" ] || docker network inspect --format '{{range .IPAM.Config}}net:{{.Subnet}}{{println}}{{end}}' $nets
pools=$(docker info --format '{{range .DefaultAddressPools}}pool:{{.Base}}{{println}}{{end}}')
case "$pools" in
*.*) printf '%s\n' "$pools" ;;
*) printf 'pool:%s\n' 172.17.0.0/16 172.18.0.0/16 172.19.0.0/16 172.20.0.0/14 172.24.0.0/14 172.28.0.0/14 192.168.0.0/16 ;;
esac
echo read`

func readHostNetworks(sc shell) (hostNetworks, error) {
	var h hostNetworks
	out, err := sc.Exec(hostNetworksScript)
	if err != nil {
		return h, fmt.Errorf("read the host's addresses and Docker networks: %w", err)
	}
	read := false
	var nets []netip.Prefix
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		kind, value, _ := strings.Cut(line, ":")
		if line == "read" {
			read = true
		}
		if kind != "own" && kind != "net" && kind != "pool" {
			continue
		}
		p, err := netip.ParsePrefix(value)
		if a, aerr := netip.ParseAddr(value); err != nil && kind == "own" && aerr == nil {
			p, err = netip.PrefixFrom(a, a.BitLen()), nil // an address with a peer has no prefix
		}
		if err != nil {
			return h, fmt.Errorf("read the host's addresses and Docker networks: %q: %w", line, err)
		}
		switch {
		case !p.Addr().Is4():
		case kind == "own":
			h.Own = append(h.Own, p.Addr())
		default:
			if kind == "net" {
				nets = append(nets, p.Masked())
			}
			if !slices.Contains(h.Docker, p.Masked()) { // a network is often a pool's base too
				h.Docker = append(h.Docker, p.Masked())
			}
		}
	}
	if !read {
		return h, errors.New("read the host's addresses and Docker networks: the answer is cut short")
	}
	h.Own = slices.DeleteFunc(h.Own, func(a netip.Addr) bool {
		return slices.ContainsFunc(nets, func(n netip.Prefix) bool { return n.Contains(a) })
	})
	return h, nil
}

// refuses says why p can't be opened to this host's builds, nil when it
// can. panel are the panel host's own addresses; panelHost says this host
// is the panel's.
func (h hostNetworks) refuses(p netip.Prefix, panel []netip.Addr, panelHost bool) *BuildDestinationError {
	for _, a := range panel {
		if p.Contains(a) {
			return &BuildDestinationError{Code: codeBuildDestinationPanel,
				Message: fmt.Sprintf("%s holds %s, one of the panel's own addresses: builds never reach the panel this way", prefixText(p), a)}
		}
	}
	for _, a := range h.Own {
		if !p.Contains(a) {
			continue
		}
		if panelHost {
			return &BuildDestinationError{Code: codeBuildDestinationPanel,
				Message: fmt.Sprintf("%s holds %s, one of the panel's own addresses: builds never reach the panel this way", prefixText(p), a)}
		}
		return &BuildDestinationError{Code: codeBuildDestinationServer,
			Message: fmt.Sprintf("%s holds %s, one of this server's own addresses: builds reach the server on 80, 443 and DNS only", prefixText(p), a)}
	}
	for _, n := range h.Docker {
		if p.Overlaps(n) {
			return &BuildDestinationError{Code: codeBuildDestinationDocker,
				Message: fmt.Sprintf("%s overlaps %s, a network or the address pool of this server's Docker: other apps' containers live there", prefixText(p), n)}
		}
	}
	return nil
}

// never is what the list's rules drop ahead of every entry: the panel's
// private addresses, which this host's kernel can't know, and its Docker's
// networks. An entry holding one was refused; this is for one that came to
// hold one since (a network Docker made later).
func (h hostNetworks) never(panel []netip.Addr) []netip.Prefix {
	var out []netip.Prefix
	private := func(p netip.Prefix) bool {
		return slices.ContainsFunc(privateRanges, p.Overlaps)
	}
	for _, a := range panel {
		if p := netip.PrefixFrom(a, a.BitLen()); a.Is4() && private(p) {
			out = append(out, p)
		}
	}
	for _, n := range h.Docker {
		if private(n) {
			out = append(out, n)
		}
	}
	return out
}

// buildDestinationRules is the list as nft rules, from typed values only:
// an address is netip's text, a port a number, and a note never gets here.
// The flush fails, and with it the whole load, where firewall.nft's chain
// isn't loaded. The verdict is accept: back in from_untrusted the next
// rule drops the private ranges.
func buildDestinationRules(list []BuildDestination, never []netip.Prefix) string {
	var b strings.Builder
	b.WriteString("# The private addresses builds on this server may reach (Toft, build\n" +
		"# destinations). The panel writes this file whole; an edit is lost.\n" +
		"flush chain ip toft build_destinations\n" +
		"table ip toft {\n\tchain build_destinations {\n")
	for _, n := range never {
		fmt.Fprintf(&b, "\t\tip daddr %s counter drop\n", prefixText(n))
	}
	for _, d := range list {
		if d.Port != nil {
			fmt.Fprintf(&b, "\t\tip daddr %s tcp dport %d counter accept\n", prefixText(d.Prefix), *d.Port)
		} else {
			fmt.Fprintf(&b, "\t\tip daddr %s meta l4proto tcp counter accept\n", prefixText(d.Prefix))
		}
	}
	b.WriteString("\t}\n}\n")
	return b.String()
}

// clearBuildDestinationsCmd leaves a host with no list: no file, and an
// empty chain where the firewall has one. It installs nothing.
const clearBuildDestinationsCmd = "rm -f " + buildDestinationsPath +
	"; ! nft list chain ip toft build_destinations >/dev/null 2>&1 || nft flush chain ip toft build_destinations"

// loadBuildDestinationsCmd takes the rules on stdin: the file is in place
// only once nft loaded it, in one transaction of the kernel's.
const loadBuildDestinationsCmd = "mkdir -p /etc/toft/firewall.d && cat > " + buildDestinationsPath + ".new && nft -f " + buildDestinationsPath + ".new && mv " +
	buildDestinationsPath + ".new " + buildDestinationsPath + " || { rm -f " + buildDestinationsPath + ".new; exit 1; }"

// installBuildDestinations makes list the whole of what the host behind sc
// opens to its builds; the caller loaded the firewall there. An entry the
// host refuses now is left out and logged. The rules are checked by nft
// before anything is written.
func installBuildDestinations(sc shell, list []BuildDestination, h hostNetworks, panel []netip.Addr, panelHost bool, logFn func(string)) error {
	kept := make([]BuildDestination, 0, len(list))
	for _, d := range list {
		if refused := h.refuses(d.Prefix, panel, panelHost); refused != nil {
			logFn("Build destination left out: " + refused.Message)
			continue
		}
		kept = append(kept, d)
	}
	if len(kept) == 0 {
		if _, err := sc.Exec(clearBuildDestinationsCmd); err != nil {
			return fmt.Errorf("empty the build destinations: %w", err)
		}
		return nil
	}
	rules := buildDestinationRules(kept, h.never(panel))
	if _, err := sc.ExecInput("nft -c -f -", strings.NewReader(rules)); err != nil {
		return fmt.Errorf("the firewall refuses the build destinations, nothing was changed: %w", err)
	}
	if _, err := sc.ExecInput(loadBuildDestinationsCmd, strings.NewReader(rules)); err != nil {
		return fmt.Errorf("load the build destinations: %w", err)
	}
	logFn(fmt.Sprintf("Build destinations loaded: %d", len(kept)))
	return nil
}

// BuildDestinationStore keeps the lists. Its methods run in the
// transaction of their context (dbtx), so a change and its audit rows are
// saved together or not at all. serverID "" is the panel's server.
type BuildDestinationStore interface {
	// LockBuildDestinations lets one change at a time through to a
	// server's list, until the transaction ends.
	LockBuildDestinations(ctx context.Context, serverID string) error
	ListBuildDestinations(ctx context.Context, serverID string) ([]BuildDestination, error)
	// AddBuildDestination: a *BuildDestinationError when the list has it.
	AddBuildDestination(ctx context.Context, serverID string, prefix netip.Prefix, port *int32, note, actorID string) (BuildDestination, error)
	// RemoveBuildDestination: ErrBuildDestinationNotFound when it hasn't.
	RemoveBuildDestination(ctx context.Context, serverID, id string) (BuildDestination, error)
}

// Transactor runs a use case's writes in one transaction (dbtx.Runner).
type Transactor interface {
	InTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// PanelHost reaches the panel's own host (instance.Service, through the
// host helper); a nil shell where it can't.
type PanelHost interface {
	PanelHostShell(ctx context.Context) Shell
}

// InstanceAuditRecorder and WorkspaceAuditRecorder write an audit row in
// the context's transaction and report a failed write.
type InstanceAuditRecorder interface {
	RecordAudit(ctx context.Context, actorID, action string, detail map[string]any) error
}

type WorkspaceAuditRecorder interface {
	RecordAuditLogDetail(ctx context.Context, orgID, actorID, action, resource string, detail map[string]any) error
}

type buildDestinationDeps struct {
	store     BuildDestinationStore
	tx        Transactor
	panel     PanelHost
	instance  InstanceAuditRecorder
	workspace WorkspaceAuditRecorder
}

// WithBuildDestinations enables the lists. Without it every change is
// refused and nothing is written to a server.
func (s *Service) WithBuildDestinations(store BuildDestinationStore, tx Transactor, panel PanelHost, instance InstanceAuditRecorder, workspace WorkspaceAuditRecorder) {
	s.dests = &buildDestinationDeps{store: store, tx: tx, panel: panel, instance: instance, workspace: workspace}
}

// AuthorizeBuildDestinations returns ErrBuildDestinationsOperatorOnly
// unless actorID is an instance operator.
func (s *Service) AuthorizeBuildDestinations(ctx context.Context, actorID string) error {
	return s.requireOperator(ctx, actorID, ErrBuildDestinationsOperatorOnly)
}

// ListBuildDestinations is a server's list ("" is the panel's server), as
// saved. Operators only.
func (s *Service) ListBuildDestinations(ctx context.Context, actorID, serverID string) ([]BuildDestination, error) {
	if err := s.AuthorizeBuildDestinations(ctx, actorID); err != nil {
		return nil, err
	}
	if s.dests == nil {
		return []BuildDestination{}, nil
	}
	return s.dests.store.ListBuildDestinations(ctx, serverID)
}

// AddBuildDestination adds an entry to a server's list and opens it there.
// Operators only.
func (s *Service) AddBuildDestination(ctx context.Context, actorID, serverID string, in BuildDestinationInput) (BuildDestination, error) {
	prefix, port, note, err := parseBuildDestination(in)
	if err != nil {
		return BuildDestination{}, err
	}
	return s.changeBuildDestinations(ctx, actorID, serverID, "added", &prefix, func(ctx context.Context, serverID string) (BuildDestination, error) {
		list, err := s.dests.store.ListBuildDestinations(ctx, serverID)
		if err != nil {
			return BuildDestination{}, err
		}
		if len(list) >= MaxBuildDestinations {
			return BuildDestination{}, &BuildDestinationError{Code: codeBuildDestinationsFull,
				Message: fmt.Sprintf("a server's list holds %d build destinations: remove one, or write a CIDR for several", MaxBuildDestinations)}
		}
		return s.dests.store.AddBuildDestination(ctx, serverID, prefix, port, note, actorID)
	})
}

// RemoveBuildDestination takes an entry off a server's list and closes it
// there. Operators only.
func (s *Service) RemoveBuildDestination(ctx context.Context, actorID, serverID, id string) error {
	_, err := s.changeBuildDestinations(ctx, actorID, serverID, "removed", nil, func(ctx context.Context, serverID string) (BuildDestination, error) {
		return s.dests.store.RemoveBuildDestination(ctx, serverID, id)
	})
	return err
}

// changeBuildDestinations is one change to a server's list: the change,
// its audit rows and the list on the server are saved together. In one
// transaction, under the list's lock: change, audit, write the whole list
// to the server, commit. A server that doesn't take the list (it can't be
// reached, its firewall refuses it) rolls the change back. added is the
// entry an add brings: the server's own refusal of it is the answer. verb
// is "added" or "removed": build_destination.<verb> in the instance's audit
// log, server.build_destination_<verb> in the workspace's of a server.
//
// The request's end doesn't cut the save short: a list the server took is
// committed. Where the change reached the server's firewall and wasn't
// saved (its answer was lost, the commit failed), the saved list is written
// back at once; a server that can't be reached then keeps the change until
// the next save there or the hourly pass writes the list.
func (s *Service) changeBuildDestinations(ctx context.Context, actorID, serverID, verb string, added *netip.Prefix,
	change func(ctx context.Context, serverID string) (BuildDestination, error)) (BuildDestination, error) {
	var out BuildDestination
	if err := s.AuthorizeBuildDestinations(ctx, actorID); err != nil {
		return out, err
	}
	if s.dests == nil {
		return out, ErrBuildDestinationsNotApplied
	}
	ctx = context.WithoutCancel(ctx)
	hostCtx, cancel := context.WithTimeout(ctx, buildDestinationsTimeout)
	defer cancel()
	sc, srv, err := s.buildDestinationsHost(hostCtx, serverID)
	if err != nil {
		return out, err
	}
	if srv != nil {
		serverID = srv.ID // one spelling of the ID, so one lock and one list
	}
	touched := false
	err = s.dests.tx.InTx(ctx, func(ctx context.Context) error {
		// The wait for another change's lock is part of the same budget.
		deadline, _ := hostCtx.Deadline()
		lockCtx, unlock := context.WithDeadline(ctx, deadline)
		err := s.dests.store.LockBuildDestinations(lockCtx, serverID)
		unlock()
		if err != nil {
			return fmt.Errorf("%w: waiting for another change to this list: %w", ErrBuildDestinationsNotApplied, err)
		}
		if out, err = change(ctx, serverID); err != nil {
			return err
		}
		detail := map[string]any{"address": out.Address(), "note": out.Note}
		if out.Port != nil {
			detail["port"] = *out.Port
		}
		if srv != nil {
			if err := s.dests.workspace.RecordAuditLogDetail(ctx, srv.OrgID, actorID, "server.build_destination_"+verb, "server:"+srv.ID, detail); err != nil {
				return err
			}
			detail["server_id"], detail["server"] = srv.ID, srv.Name
		}
		if err := s.dests.instance.RecordAudit(ctx, actorID, "build_destination."+verb, detail); err != nil {
			return err
		}
		list, err := s.dests.store.ListBuildDestinations(ctx, serverID)
		if err != nil {
			return err
		}
		touched, err = s.writeBuildDestinations(hostCtx, serverID, sc, list, added, func(line string) {
			s.log.Info("build destinations", zap.String("server_id", serverID), zap.String("line", line))
		})
		var refused *BuildDestinationError
		if err != nil && !errors.As(err, &refused) {
			return fmt.Errorf("%w: %w", ErrBuildDestinationsNotApplied, err)
		}
		return err
	})
	if err != nil && touched {
		s.restoreBuildDestinations(ctx, serverID)
	}
	return out, err
}

// restoreBuildDestinations writes a server's saved list back to it after a
// change that reached its firewall wasn't saved.
func (s *Service) restoreBuildDestinations(ctx context.Context, serverID string) {
	hostCtx, cancel := context.WithTimeout(ctx, buildDestinationsTimeout)
	defer cancel()
	sc, _, err := s.buildDestinationsHost(hostCtx, serverID)
	if err == nil {
		err = s.applyStoredBuildDestinations(hostCtx, serverID, sc, func(line string) {
			s.log.Info("build destinations", zap.String("server_id", serverID), zap.String("line", line))
		})
	}
	if err != nil {
		s.log.Warn("build destinations: an unsaved change may be in the server's firewall until its list is next written",
			zap.String("server_id", serverID), zap.Error(err))
	}
}

// buildDestinationsHost is the shell on a server's host under ctx, with
// the server; the panel's host ("") has no server.
func (s *Service) buildDestinationsHost(ctx context.Context, serverID string) (shell, *Server, error) {
	if serverID == "" {
		sc := s.dests.panel.PanelHostShell(ctx)
		if sc == nil {
			return nil, nil, fmt.Errorf("%w: the panel's own host can't be reached from here", ErrBuildDestinationsNotApplied)
		}
		return sc, nil, nil
	}
	srv, err := s.repo.GetByID(ctx, serverID)
	if err != nil {
		return nil, nil, fmt.Errorf("get server: %w", err)
	}
	return shellFor(ctx, s.SSH(srv)), srv, nil
}

// writeBuildDestinations writes list, a server's whole stored list, to its
// host through sc, loading the firewall first where the list opens
// anything: a server set up by an older release gets the chain here, with
// no Provision. Every write asks the host again what it must never open
// (hostNetworks.refuses), and a server other than the panel's is told the
// panel's own addresses; when either can't be read nothing is written.
// added is refused with an error; any other entry refused now is left out.
// touched reports that the host's firewall may have changed, on a failure
// too.
func (s *Service) writeBuildDestinations(ctx context.Context, serverID string, sc shell, list []BuildDestination, added *netip.Prefix, logFn func(string)) (touched bool, err error) {
	if len(list) == 0 {
		if _, err := sc.Exec("true"); err != nil { // a host that doesn't answer wasn't touched
			return false, fmt.Errorf("reach the host: %w", err)
		}
		return true, installBuildDestinations(sc, nil, hostNetworks{}, nil, false, logFn)
	}
	h, err := readHostNetworks(sc)
	if err != nil {
		return false, err
	}
	var panel []netip.Addr
	if serverID != "" {
		psc := s.dests.panel.PanelHostShell(ctx)
		if psc == nil {
			return false, errors.New("the panel's own addresses can't be read: its host can't be reached from here")
		}
		p, err := readHostNetworks(psc)
		if err != nil {
			return false, fmt.Errorf("the panel's own addresses: %w", err)
		}
		panel = p.Own
	}
	if added != nil {
		if refused := h.refuses(*added, panel, serverID == ""); refused != nil {
			return false, refused
		}
	}
	if err := ensureHostFirewall(sc, logFn); err != nil {
		return true, fmt.Errorf("host firewall: %w", err)
	}
	return true, installBuildDestinations(sc, list, h, panel, serverID == "", logFn)
}

// applyStoredBuildDestinations writes a server's stored list to its host
// through sc, under the list's lock: for what loads a firewall without a
// save (Provision, the gVisor install, the pass after a start).
func (s *Service) applyStoredBuildDestinations(ctx context.Context, serverID string, sc shell, logFn func(string)) error {
	return s.dests.tx.InTx(ctx, func(tx context.Context) error {
		if err := s.dests.store.LockBuildDestinations(tx, serverID); err != nil {
			return err
		}
		list, err := s.dests.store.ListBuildDestinations(tx, serverID)
		if err != nil {
			return err
		}
		_, err = s.writeBuildDestinations(ctx, serverID, sc, list, nil, logFn)
		return err
	})
}

// BuildDestinationsApplier returns what writes a server's stored list
// ("": the panel's server) through the shell of a run that has just loaded
// the firewall there; nil when the lists aren't enabled.
func (s *Service) BuildDestinationsApplier(ctx context.Context, serverID string) func(sc Shell, logFn func(string)) error {
	if s.dests == nil {
		return nil
	}
	return func(sc Shell, logFn func(string)) error {
		return s.applyStoredBuildDestinations(ctx, serverID, sc, logFn)
	}
}

// RunBuildDestinations writes every server's stored list to it shortly
// after a start and every hour after that, until ctx ends. It is how a
// list reaches a host that lost its file (the panel's after a restore,
// which doesn't bring /etc/toft back), and how a server whose list was
// changed outside the panel (an answer that was lost, an edit by hand, a
// Docker network made since) has the stored one again within the hour. A
// server with no stored entries only loses a list it still has; nothing is
// installed there.
func (s *Service) RunBuildDestinations(ctx context.Context) {
	if s.dests == nil {
		return
	}
	wait := buildDestinationsDelay
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = time.Hour
		s.applyAllBuildDestinations(ctx)
	}
}

// applyAllBuildDestinations writes every server's stored list to it.
// ponytail: one server after another, each up to buildDestinationsTimeout;
// in parallel if a fleet of unreachable servers makes the pass too slow.
func (s *Service) applyAllBuildDestinations(ctx context.Context) {
	servers, err := s.repo.ListAll(ctx)
	if err != nil {
		s.log.Warn("build destinations: list servers", zap.Error(err))
		return
	}
	ids := make([]string, 0, len(servers)+1)
	if s.dests.panel.PanelHostShell(ctx) != nil { // nil without the host helper
		ids = append(ids, "")
	}
	for _, srv := range servers {
		ids = append(ids, srv.ID)
	}
	for _, id := range ids {
		hostCtx, cancel := context.WithTimeout(ctx, buildDestinationsTimeout)
		sc, _, err := s.buildDestinationsHost(hostCtx, id)
		if err == nil {
			err = s.applyStoredBuildDestinations(hostCtx, id, sc, func(line string) {
				s.log.Info("build destinations", zap.String("server_id", id), zap.String("line", line))
			})
		}
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.log.Warn("build destinations not written, trying again in an hour", zap.String("server_id", id), zap.Error(err))
		}
	}
}
