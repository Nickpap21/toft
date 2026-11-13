package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"
)

// The capability probe (D-93): what a server can run, read over SSH on Scan
// again, after provisioning and daily. It records the kernel, architecture,
// cgroups, virtualisation, /dev/kvm, Docker and its security, and each
// runtime Docker lists with a smoke test, and says which isolation levels
// the server can run.

// Runtime is one of Docker's runtimes and its smoke test.
type Runtime struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Version is runsc's release (20261012.0), and Args the runtime's
	// arguments in Docker's configuration (D-95).
	Version string   `json:"version,omitempty"`
	Args    []string `json:"args,omitempty"`
}

// Level is an isolation level and whether the server can run it: State is
// available, not_installed (gVisor could run here), failing (installed, its
// smoke test fails) or unavailable.
type Level struct {
	ID     string `json:"id"` // standard, sandboxed, sandboxed_strict, microvm, dedicated
	State  string `json:"state"`
	Reason string `json:"reason"`
}

// Capabilities is what the probe last read of a server.
type Capabilities struct {
	Kernel         string    `json:"kernel"`
	Arch           string    `json:"arch"`
	CgroupV2       bool      `json:"cgroup_v2"`
	Virtualization string    `json:"virtualization"`
	KVM            bool      `json:"kvm"`
	DockerVersion  string    `json:"docker_version"`
	AppArmor       bool      `json:"apparmor"`
	Seccomp        bool      `json:"seccomp"`
	Runtimes       []Runtime `json:"runtimes"`
	// PreviewFirewall: the host firewall's chain for untrusted previews'
	// networks is loaded (D-97).
	PreviewFirewall bool `json:"preview_firewall"`
	// DNSRelay: the agent's DNS relay answered (D-115).
	DNSRelay bool `json:"dns_relay"`
	// AddressPools are Docker's default-address-pools, which app networks
	// take their subnets from: empty when Docker has none (its own ranges),
	// nil (null) until a probe read them.
	AddressPools []AddressPool `json:"address_pools"`
	// Agent: the panel reaches the server through its agent, so the strict
	// network can run there (D-115). Set from the server row on read, never
	// stored.
	Agent    bool      `json:"-"`
	Error    string    `json:"error"`
	ProbedAt time.Time `json:"probed_at"`
	Levels   []Level   `json:"levels"`
}

// AddressPool is one of Docker's default-address-pools: networks of Size
// bits from Base.
type AddressPool struct {
	Base string `json:"base"`
	Size int    `json:"size"`
}

// capabilityScript prints one key:value line a fact, then one runtime line
// per runtime Docker lists (runc's containerd alias left out), each after
// a smoke test: the pinned busybox, without a network, exiting 0.
func capabilityScript() string {
	return `printf 'kernel:%s\n' "$(uname -r)"
printf 'arch:%s\n' "$(uname -m)"
if [ -f /sys/fs/cgroup/cgroup.controllers ]; then echo cgroup:2; else echo cgroup:1; fi
v=$(systemd-detect-virt 2>/dev/null); printf 'virt:%s\n' "${v:-unknown}"
if [ -c /dev/kvm ]; then echo kvm:1; else echo kvm:0; fi
printf 'docker:%s\n' "$(docker version --format '{{.Server.Version}}' 2>/dev/null)"
printf 'security:%s\n' "$(docker info --format '{{json .SecurityOptions}}' 2>/dev/null)"
printf 'pools:%s\n' "$(docker info --format '{{json .DefaultAddressPools}}' 2>/dev/null)"
printf 'runtimeargs:%s\n' "$(docker info --format '{{json .Runtimes}}' 2>/dev/null)"
printf 'runsc:%s\n' "$(runsc --version 2>/dev/null | head -n 1)"
if nft list chain ip toft from_untrusted >/dev/null 2>&1; then echo previewfw:1; else echo previewfw:0; fi
if ip -o addr show dev ` + agentproto.DNSRelayInterface + ` 2>/dev/null | grep -q ' ` + agentproto.DNSRelayAddr + `/' &&
  timeout 20 docker run --rm --network bridge --pull missing ` + rollout.ProbeToolImage + ` nslookup ` + agentproto.DNSRelayCheckName + ` ` + agentproto.DNSRelayAddr + ` >/dev/null 2>&1; then echo dnsrelay:1; else echo dnsrelay:0; fi
for r in $(docker info --format '{{range $k, $v := .Runtimes}}{{$k}} {{end}}' 2>/dev/null); do
  case "$r" in io.containerd.runc.v2) continue ;; esac
  if out=$(docker run --rm --runtime="$r" --network none --pull missing ` + rollout.ProbeToolImage + ` true 2>&1); then
    echo "runtime:$r:ok"
  else
    printf 'runtime:%s:fail:%s\n' "$r" "$(printf '%s' "$out" | tail -n 1 | cut -c1-200)"
  fi
done
`
}

// parseCapabilities reads capabilityScript's output.
func parseCapabilities(out string) (Capabilities, error) {
	c := Capabilities{Runtimes: []Runtime{}}
	seen := false
	var configured map[string]dockerRuntime
	runscVersion := ""
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		switch key {
		case "kernel":
			c.Kernel, seen = val, true
		case "arch":
			c.Arch = val
		case "cgroup":
			c.CgroupV2 = val == "2"
		case "virt":
			c.Virtualization = val
		case "kvm":
			c.KVM = val == "1"
		case "previewfw":
			c.PreviewFirewall = val == "1"
		case "dnsrelay":
			c.DNSRelay = val == "1"
		case "docker":
			c.DockerVersion = val
		case "security":
			var opts []string
			if json.Unmarshal([]byte(val), &opts) == nil {
				for _, o := range opts {
					c.AppArmor = c.AppArmor || strings.HasPrefix(o, "name=apparmor")
					c.Seccomp = c.Seccomp || strings.HasPrefix(o, "name=seccomp")
				}
			}
		case "pools":
			// null when Docker has none; nothing when it didn't answer.
			var pools []AddressPool
			if json.Unmarshal([]byte(val), &pools) == nil {
				c.AddressPools = make([]AddressPool, 0, len(pools))
				for _, p := range pools {
					if len(p.Base) <= 43 { // a CIDR, IPv6's longest
						c.AddressPools = append(c.AddressPools, p)
					}
				}
			}
		case "runtimeargs":
			_ = json.Unmarshal([]byte(val), &configured)
		case "runsc":
			// "runsc version release-20261012.0"
			if i := strings.LastIndex(val, "release-"); i >= 0 {
				runscVersion = val[i+len("release-"):]
			}
		case "runtime":
			name, rest, _ := strings.Cut(val, ":")
			result, msg, _ := strings.Cut(rest, ":")
			if name != "" {
				c.Runtimes = append(c.Runtimes, Runtime{Name: name, OK: result == "ok", Error: msg})
			}
		}
	}
	if !seen {
		return c, errors.New("the probe printed nothing it knows")
	}
	for i := range c.Runtimes {
		c.Runtimes[i].Args = configured[c.Runtimes[i].Name].Args
		if n := c.Runtimes[i].Name; n == rollout.RuntimeSandboxed || n == rollout.RuntimeStrict {
			c.Runtimes[i].Version = runscVersion
		}
	}
	if c.DockerVersion == "" {
		c.Error = "Docker isn't answering on this server"
	}
	return c, nil
}

// kernelAtLeast reports whether release (6.8.0-146-generic) is at least
// major.minor.
func kernelAtLeast(release string, major, minor int) bool {
	parts := strings.SplitN(release, ".", 3)
	if len(parts) < 2 {
		return false
	}
	maj, err1 := strconv.Atoi(parts[0])
	min, err2 := strconv.Atoi(strings.TrimFunc(parts[1], func(r rune) bool { return r < '0' || r > '9' }))
	if err1 != nil || err2 != nil {
		return false
	}
	return maj > major || maj == major && min >= minor
}

func (c Capabilities) runtime(name string) *Runtime {
	for i := range c.Runtimes {
		if c.Runtimes[i].Name == name {
			return &c.Runtimes[i]
		}
	}
	return nil
}

// bareMetalKVM is where gVisor uses its KVM platform: /dev/kvm on a server
// that isn't a virtual machine (D-95); systrap everywhere else.
func (c Capabilities) bareMetalKVM() bool { return c.KVM && c.Virtualization == "none" }

func gvisorPlatform(args []string) string {
	for _, a := range args {
		if a == "--platform=kvm" {
			return "the KVM platform"
		}
	}
	return "systrap"
}

// levels says which isolation levels the server can run, and why not.
func (c Capabilities) levels() []Level {
	std := Level{ID: "standard", State: "failing"}
	switch r := c.runtime("runc"); {
	case c.DockerVersion == "":
		std.Reason = "Docker isn't answering on this server."
	case r == nil:
		std.Reason = "Docker lists no runc runtime."
	case !r.OK:
		std.Reason = "runc's smoke test failed: " + r.Error
	default:
		std.State, std.Reason = "available", "Every service: no new privileges, raw sockets off, a process limit, Docker's default seccomp and AppArmor. Smoke test passed."
		switch {
		case !c.AppArmor && !c.Seccomp:
			std.Reason += " AppArmor and seccomp are off in this server's Docker, so that part doesn't hold."
		case !c.AppArmor:
			std.Reason += " AppArmor is off in this server's Docker, so that part doesn't hold."
		case !c.Seccomp:
			std.Reason += " seccomp is off in this server's Docker, so that part doesn't hold."
		}
	}
	sb := Level{ID: "sandboxed"}
	switch r := c.runtime("runsc"); {
	case !kernelAtLeast(c.Kernel, 5, 6):
		sb.State, sb.Reason = "unavailable", "gVisor needs Linux 5.6 or newer; this server runs "+c.Kernel+"."
	case r == nil:
		sb.State, sb.Reason = "not_installed", "Each container gets its own kernel in user space. The kernel here is new enough (5.6+)"
		if c.KVM {
			sb.Reason += "; with /dev/kvm here it would use the KVM platform."
		} else {
			sb.Reason += "; this server has no /dev/kvm, so it would use systrap, slower for databases."
		}
	case !r.OK:
		sb.State, sb.Reason = "failing", "gVisor's smoke test failed: "+r.Error
	case strings.Join(r.Args, " ") != strings.Join(GvisorArgs(c.bareMetalKVM()), " "):
		sb.State, sb.Reason = "failing", "runsc is registered with Docker without Toft's settings (D-95), so Sandboxed services can't find each other by name. Install gVisor again to fix it."
	default:
		sb.State, sb.Reason = "available", "Each container gets its own kernel in user space, "+gvisorPlatform(r.Args)+" here. Smoke test passed."
	}
	strict := Level{ID: "sandboxed_strict"}
	switch r := c.runtime(rollout.RuntimeStrict); {
	case !kernelAtLeast(c.Kernel, 5, 6):
		strict.State, strict.Reason = "unavailable", sb.Reason
	case !c.Agent:
		strict.State, strict.Reason = "unavailable", "Needs Toft's agent on the server: it answers the name lookups of gVisor's own network stack, which can't reach Docker's."
	case r == nil && c.runtime(rollout.RuntimeSandboxed) == nil:
		strict.State, strict.Reason = "not_installed", "gVisor's own network stack too, with names answered by Toft's agent here. Install gVisor adds it."
	case r == nil:
		strict.State, strict.Reason = "not_installed", "This server's gVisor came before the strict network. Install gVisor again to add it: Docker reloads, and no container restarts."
	case !r.OK:
		strict.State, strict.Reason = "failing", "runsc-strict's smoke test failed: "+r.Error
	case strings.Join(r.Args, " ") != strings.Join(GvisorStrictArgs(c.bareMetalKVM()), " "):
		strict.State, strict.Reason = "failing", "runsc-strict is registered with Docker without Toft's settings (D-115). Install gVisor again to fix it."
	case !c.DNSRelay:
		strict.State, strict.Reason = "failing", "Containers here get no answer from Toft's agent's name lookups: Install gVisor again to let them through the firewall, or Check again once the agent has updated itself with the panel."
	default:
		strict.State, strict.Reason = "available", "gVisor's own network stack too, "+gvisorPlatform(r.Args)+" here, with names answered by Toft's agent. Smoke test passed."
	}
	mv := Level{ID: "microvm", State: "unavailable", Reason: "Toft doesn't build MicroVMs yet."}
	if !c.KVM {
		mv.Reason = "Needs /dev/kvm"
		if c.Virtualization != "" && c.Virtualization != "none" && c.Virtualization != "unknown" {
			mv.Reason += ": this server runs under " + c.Virtualization + " without nested virtualisation"
		}
		mv.Reason += ". Toft doesn't build MicroVMs yet."
	}
	return []Level{std, sb, strict, mv, {ID: "dedicated", State: "available", Reason: "Give a workspace a server nobody else uses: the hardest wall there is."}}
}

// ProbeCapabilities reads what the server can run and stores it; a probe
// that fails keeps what the last one read, with its error.
func (s *Service) ProbeCapabilities(ctx context.Context, serverID string) (*Capabilities, error) {
	srv, err := s.repo.GetByID(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("get server: %w", err)
	}
	if s.caps == nil {
		return nil, errors.New("no capability store")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	c, err := ProbeOn(shellFor(ctx, s.SSH(srv)))
	if err != nil {
		msg := "The probe couldn't run: " + err.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		if serr := s.caps.SetCapabilitiesError(ctx, serverID, msg); serr != nil {
			return nil, serr
		}
		return s.Capabilities(ctx, serverID)
	}
	if err := s.caps.SaveCapabilities(ctx, serverID, c); err != nil {
		return nil, err
	}
	return s.Capabilities(ctx, serverID)
}

// Capabilities is what the probe last read of the server; nil when it never
// ran, without levels until one probe read the server.
func (s *Service) Capabilities(ctx context.Context, serverID string) (*Capabilities, error) {
	if s.caps == nil {
		return nil, nil
	}
	srv, err := s.repo.GetByID(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("get server: %w", err)
	}
	return s.capabilitiesOf(ctx, srv)
}

// FirstAddressPool is the deprecated address_pool (v0.36.0): the first
// pool's base the probe read, "" when none or not read.
func (s *Service) FirstAddressPool(ctx context.Context, srv *Server) string {
	if s.caps == nil {
		return ""
	}
	c, err := s.caps.GetCapabilities(ctx, srv.ID)
	if err != nil || c == nil || len(c.AddressPools) == 0 {
		return ""
	}
	return c.AddressPools[0].Base
}

// capabilitiesOf is Capabilities for a server already read.
func (s *Service) capabilitiesOf(ctx context.Context, srv *Server) (*Capabilities, error) {
	if s.caps == nil {
		return nil, nil
	}
	c, err := s.caps.GetCapabilities(ctx, srv.ID)
	if err != nil || c == nil {
		return c, err
	}
	c.Agent = srv.Connection == ConnectionAgent
	c.SetLevels()
	return c, nil
}

// SetLevels says which isolation levels c's server can run. They need one
// probe that read the server; before it there are none, only its error.
func (c *Capabilities) SetLevels() {
	c.Levels = []Level{}
	if c.Kernel != "" {
		c.Levels = c.levels()
	}
}

// ProbeOn reads what a host can run through sc, as ProbeCapabilities does
// over SSH: the panel's own server runs it through its host helper (D-97).
func ProbeOn(sc Shell) (Capabilities, error) {
	encoded := base64.StdEncoding.EncodeToString([]byte(capabilityScript()))
	out, err := sc.Exec("echo " + encoded + " | base64 -d | sh")
	if err != nil {
		return Capabilities{}, err
	}
	return parseCapabilities(out)
}

// RunCapabilityProbes probes, at start and every hour, the servers whose
// probe is a day old or never ran, until ctx ends.
func (s *Service) RunCapabilityProbes(ctx context.Context) {
	if s.caps == nil {
		return
	}
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		ids, err := s.caps.StaleCapabilityServers(ctx)
		if err != nil && ctx.Err() == nil {
			s.log.Warn("list servers to probe", zap.Error(err))
		}
		for _, id := range ids {
			if _, err := s.ProbeCapabilities(ctx, id); err != nil && ctx.Err() == nil {
				s.log.Warn("probe capabilities", zap.Error(err), zap.String("server_id", id))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// CapabilityStore keeps what the probe read. Satisfied by the Postgres
// repository.
type CapabilityStore interface {
	SaveCapabilities(ctx context.Context, serverID string, c Capabilities) error
	SetCapabilitiesError(ctx context.Context, serverID, msg string) error
	GetCapabilities(ctx context.Context, serverID string) (*Capabilities, error)
	StaleCapabilityServers(ctx context.Context) ([]string, error)
}

// WithCapabilities stores what the probe reads.
func (s *Service) WithCapabilities(c CapabilityStore) { s.caps = c }

func (r *postgresRepository) SaveCapabilities(ctx context.Context, serverID string, c Capabilities) error {
	rt, err := json.Marshal(c.Runtimes)
	if err != nil {
		return fmt.Errorf("encode runtimes: %w", err)
	}
	var pools []byte // NULL: not read
	if c.AddressPools != nil {
		// Within the column's 4096 bytes, which its CHECK measures as
		// address_pools::text: jsonb writes a space after each ':' and ','
		// (`{"base": "…", "size": 24}, `), at most 5 bytes a pool more than
		// Marshal's. The pools that fit are kept, the rest of the probe saved.
		n, size := 0, len("[]")
		for _, p := range c.AddressPools {
			b, err := json.Marshal(p)
			if err != nil {
				return fmt.Errorf("encode address pool: %w", err)
			}
			if size += len(b) + 5; size > 4096 {
				break
			}
			n++
		}
		if pools, err = json.Marshal(c.AddressPools[:n]); err != nil {
			return fmt.Errorf("encode address pools: %w", err)
		}
	}
	cut := func(s string, n int) string {
		if len(s) > n {
			return s[:n]
		}
		return s
	}
	if err := r.q.UpsertServerCapabilities(ctx, sqlcdb.UpsertServerCapabilitiesParams{
		ServerID: pgUUID(serverID), Kernel: cut(c.Kernel, 200), Arch: cut(c.Arch, 40), CgroupV2: c.CgroupV2,
		Virtualization: cut(c.Virtualization, 40), Kvm: c.KVM, DockerVersion: cut(c.DockerVersion, 40),
		Apparmor: c.AppArmor, Seccomp: c.Seccomp, Runtimes: rt, Error: cut(c.Error, 500), PreviewFirewall: c.PreviewFirewall,
		DnsRelay: c.DNSRelay, AddressPools: pools,
	}); err != nil {
		return fmt.Errorf("UpsertServerCapabilities: %w", err)
	}
	return nil
}

func (r *postgresRepository) SetCapabilitiesError(ctx context.Context, serverID, msg string) error {
	if err := r.q.SetServerCapabilitiesError(ctx, sqlcdb.SetServerCapabilitiesErrorParams{ServerID: pgUUID(serverID), Error: msg}); err != nil {
		return fmt.Errorf("SetServerCapabilitiesError: %w", err)
	}
	return nil
}

func (r *postgresRepository) GetCapabilities(ctx context.Context, serverID string) (*Capabilities, error) {
	row, err := r.q.GetServerCapabilities(ctx, pgUUID(serverID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("GetServerCapabilities: %w", err)
	}
	c := &Capabilities{Kernel: row.Kernel, Arch: row.Arch, CgroupV2: row.CgroupV2, Virtualization: row.Virtualization, KVM: row.Kvm,
		DockerVersion: row.DockerVersion, AppArmor: row.Apparmor, Seccomp: row.Seccomp, Error: row.Error, ProbedAt: row.ProbedAt.Time,
		Runtimes: []Runtime{}, PreviewFirewall: row.PreviewFirewall, DNSRelay: row.DnsRelay}
	if err := json.Unmarshal(row.Runtimes, &c.Runtimes); err != nil {
		return nil, fmt.Errorf("decode runtimes: %w", err)
	}
	if row.AddressPools != nil {
		c.AddressPools = []AddressPool{}
		if err := json.Unmarshal(row.AddressPools, &c.AddressPools); err != nil {
			return nil, fmt.Errorf("decode address pools: %w", err)
		}
	}
	return c, nil
}

func (r *postgresRepository) StaleCapabilityServers(ctx context.Context) ([]string, error) {
	rows, err := r.q.ListStaleCapabilityServers(ctx)
	if err != nil {
		return nil, fmt.Errorf("ListStaleCapabilityServers: %w", err)
	}
	out := make([]string, 0, len(rows))
	for _, id := range rows {
		out = append(out, uuidString(id))
	}
	return out, nil
}

func pgUUID(s string) pgtype.UUID {
	var u pgtype.UUID
	_ = u.Scan(s)
	return u
}
