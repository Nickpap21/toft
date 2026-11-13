package server

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"
)

// gVisor (D-95): the Sandboxed level's runtime, installed on a server on
// demand by an instance operator. Toft pins one release; installing again
// moves a server to it. Each step checks first and changes only what is
// missing, and Docker is reloaded, never restarted, so nothing running is
// touched.

const (
	// GvisorRelease is the apt suite Toft installs from, and gvisorVersion
	// the runsc package in it.
	GvisorRelease = "20261012"
	gvisorVersion = GvisorRelease + ".0"

	gvisorKeyPath  = "/usr/share/keyrings/gvisor-archive-keyring.asc"
	gvisorListPath = "/etc/apt/sources.list.d/gvisor.list"
	runscPath      = "/usr/bin/runsc"
	daemonBackup   = daemonConfigPath + ".toft-backup"

	// GvisorTimeout ends an install that hangs.
	GvisorTimeout = 20 * time.Minute
)

// gvisorKey is gVisor's apt signing key, "The gVisor Authors
// <gvisor-bot@google.com>", fingerprint 6F1D F85E 3A71 C249 18E7 27D5 6FC6
// D554 E32B D943 (from https://gvisor.dev/archive.key; a test checks the
// fingerprint). It is built in, so nothing fetched at install time decides
// what apt trusts.
//
//go:embed gvisor-archive-key.pub
var gvisorKey string

// GvisorSteps are the install's steps; each logs "[n/6] …" as it starts.
var GvisorSteps = []string{"Checking the server", "Adding gVisor's apt repository", "Installing runsc", "Registering runsc with Docker", "Testing a sandboxed container", "Testing the strict network"}

// ErrGvisorOperatorOnly refuses an install by someone who isn't an instance
// operator.
var ErrGvisorOperatorOnly = errors.New("only instance operators can install gVisor on a server")

// ErrGvisorRunning is a second install on a server while one runs.
var ErrGvisorRunning = errors.New("gVisor is being installed on this server already")

// GvisorArgs are runsc's arguments in Docker's configuration (D-95): the
// host kernel's network stack inside the container's own network namespace,
// so Docker's DNS, aliases, Traefik and the health check work as for runc;
// Docker's seccomp profile applied inside the sandbox too; and, on bare
// metal with /dev/kvm, the KVM platform instead of systrap.
func GvisorArgs(kvm bool) []string {
	args := []string{"--network=host", "--oci-seccomp"}
	if kvm {
		args = append(args, "--platform=kvm")
	}
	return args
}

// GvisorStrictArgs are runsc-strict's (D-115): GvisorArgs without the host's
// network stack, so network calls stay in gVisor too; names come from the
// agent's DNS relay.
func GvisorStrictArgs(kvm bool) []string {
	args := []string{"--oci-seccomp"}
	if kvm {
		args = append(args, "--platform=kvm")
	}
	return args
}

// gvisorRuntimes are the runtimes the install registers, by name.
func gvisorRuntimes(kvm bool) map[string][]string {
	return map[string][]string{rollout.RuntimeSandboxed: GvisorArgs(kvm), rollout.RuntimeStrict: GvisorStrictArgs(kvm)}
}

// gvisorHost is what the check step read.
type gvisorHost struct {
	kernel, arch, os, docker string
	kvm                      bool // bare metal with /dev/kvm: the KVM platform
}

func (h gvisorHost) platform() string {
	if h.kvm {
		return "kvm"
	}
	return "systrap"
}

// installGvisor installs runsc on the server behind sc and registers it
// with Docker; logFn gets the progress. agent is a server whose agent
// answers names (D-115): there the strict network is opened and tested. It
// returns what it read of the host.
func installGvisor(sc shell, agent bool, logFn func(string)) (gvisorHost, error) {
	step := func(n int) { logFn(fmt.Sprintf("[%d/%d] %s", n, len(GvisorSteps), GvisorSteps[n-1])) }

	step(1)
	h, err := checkGvisorHost(sc)
	if err != nil {
		return h, err
	}
	logFn(fmt.Sprintf("  ✓ %s (%s), kernel %s, Docker %s; gVisor will use %s", h.os, h.arch, h.kernel, h.docker, h.platform()))

	step(2)
	if _, err := writeIfChanged(sc, gvisorKeyPath, gvisorKey); err != nil {
		return h, err
	}
	list := fmt.Sprintf("deb [arch=%s signed-by=%s] https://storage.googleapis.com/gvisor/releases %s main\n", h.arch, gvisorKeyPath, GvisorRelease)
	if _, err := writeIfChanged(sc, gvisorListPath, list); err != nil {
		return h, err
	}
	logFn("  ✓ storage.googleapis.com/gvisor/releases " + GvisorRelease + ", signed by the gVisor Authors' key")

	step(3)
	if have, _ := sc.Exec("dpkg-query -W -f='${Version}' runsc 2>/dev/null"); strings.TrimSpace(have) == gvisorVersion {
		logFn("  ✓ runsc " + gvisorVersion + " already installed")
	} else {
		// Only gVisor's list is updated, so a broken source elsewhere can't
		// stop it; the lock wait covers unattended-upgrades. needrestart's
		// apt hook is suspended: set to restart services by itself, it
		// would restart Docker, and with it every container (found on
		// production, where it only listed docker.service).
		apt := "apt-get -o DPkg::Lock::Timeout=120 "
		cmd := "export DEBIAN_FRONTEND=noninteractive NEEDRESTART_SUSPEND=1; " +
			apt + "update -q -o Dir::Etc::sourcelist=" + gvisorListPath + " -o Dir::Etc::sourceparts=- -o APT::Get::List-Cleanup=0 2>&1 && " +
			apt + "install -yq --no-install-recommends --allow-downgrades runsc=" + gvisorVersion + " 2>&1"
		if err := sc.ExecStream(cmd, logFn); err != nil {
			return h, fmt.Errorf("apt-get install runsc=%s: %w", gvisorVersion, err)
		}
		logFn("  ✓ runsc " + gvisorVersion + " installed")
	}

	step(4)
	want := gvisorRuntimes(h.kvm)
	if registered(sc, want) {
		logFn("  ✓ Docker already runs runsc and runsc-strict with Toft's settings")
	} else if err := registerRunsc(sc, want, logFn); err != nil {
		return h, err
	}
	// Untrusted previews run Sandboxed behind the host firewall's chain for
	// their networks (D-97): an older server gets it here, as at Provision.
	if err := ensureHostFirewall(sc, logFn); err != nil {
		return h, fmt.Errorf("host firewall: %w", err)
	}

	step(5)
	if err := testRunsc(sc, gvisorCheckScript, "a sandboxed test container"); err != nil {
		return h, err
	}
	logFn("  ✓ A sandboxed container started and found another by name")

	step(6)
	if !agent {
		logFn("  · Skipped: the strict network needs Toft's agent on the server to answer names")
		return h, nil
	}
	if err := allowDNSRelay(sc); err != nil {
		return h, err
	}
	if err := testRunsc(sc, strictCheckScript, "a strict test container"); err != nil {
		return h, err
	}
	logFn("  ✓ A strict container found another by name through the agent, and the health probe reached it")
	return h, nil
}

// InstallGvisorOn installs gVisor through sc, the steps InstallGvisor runs
// over SSH, and returns the platform it uses: the panel's own server runs
// them through its host helper (D-97).
func InstallGvisorOn(sc Shell, logFn func(string)) (platform string, err error) {
	h, err := installGvisor(sc, false, logFn)
	return h.platform(), err
}

// checkGvisorHost reads what gVisor needs and refuses a host it can't run
// on, before anything changes.
func checkGvisorHost(sc shell) (gvisorHost, error) {
	out, err := sc.Exec(`. /etc/os-release 2>/dev/null; printf '%s\n%s\n%s\n%s\n%s\n%s\n' "$(uname -r)" "$(dpkg --print-architecture 2>/dev/null)" "$(command -v apt-get)" "$(systemd-detect-virt 2>/dev/null)" "$([ -c /dev/kvm ] && echo kvm)" "${PRETTY_NAME:-Linux}"`)
	if err != nil {
		return gvisorHost{}, fmt.Errorf("read the server: %w", err)
	}
	f := strings.Split(out, "\n")
	for len(f) < 6 {
		f = append(f, "")
	}
	h := gvisorHost{kernel: f[0], arch: f[1], os: f[5], kvm: f[4] == "kvm" && f[3] == "none"}
	h.docker, _ = sc.Exec("docker version --format '{{.Server.Version}}' 2>/dev/null")
	switch {
	case !kernelAtLeast(h.kernel, 5, 6):
		return h, fmt.Errorf("gVisor needs Linux 5.6 or newer; this server runs %s", h.kernel)
	case h.arch != "amd64" && h.arch != "arm64":
		return h, fmt.Errorf("gVisor runs on amd64 and arm64; this server is %q", h.arch)
	case f[2] == "":
		return h, errors.New("gVisor is installed with apt, and this server has no apt-get (Debian or Ubuntu)")
	case strings.TrimSpace(h.docker) == "":
		return h, errors.New("docker isn't answering on this server")
	}
	h.docker = strings.TrimSpace(h.docker)
	return h, nil
}

// dockerRuntime is a runtime as docker info reports it.
type dockerRuntime struct {
	Path string   `json:"path"`
	Args []string `json:"runtimeArgs"`
}

// registered reports whether Docker runs each of want's runtimes from
// runscPath with its args.
func registered(sc shell, want map[string][]string) bool {
	out, err := sc.Exec("docker info --format '{{json .Runtimes}}' 2>/dev/null")
	if err != nil {
		return false
	}
	var rts map[string]dockerRuntime
	if json.Unmarshal([]byte(out), &rts) != nil {
		return false
	}
	for name, args := range want {
		r, ok := rts[name]
		if !ok || r.Path != runscPath || strings.Join(r.Args, " ") != strings.Join(args, " ") {
			return false
		}
	}
	return true
}

// registerRunsc writes want's runtimes into daemon.json with runsc's own
// installer, which keeps every other setting, checks the file with dockerd
// and puts the old one back if it fails, then reloads Docker once: running
// containers keep running.
func registerRunsc(sc shell, want map[string][]string, logFn func(string)) error {
	if _, err := sc.Exec("mkdir -p /etc/docker && if [ -f " + daemonConfigPath + " ]; then cp -p " + daemonConfigPath + " " + daemonBackup + "; else rm -f " + daemonBackup + "; fi"); err != nil {
		return fmt.Errorf("keep a copy of %s: %w", daemonConfigPath, err)
	}
	restore := "if [ -f " + daemonBackup + " ]; then mv " + daemonBackup + " " + daemonConfigPath + "; else rm -f " + daemonConfigPath + "; fi"
	for _, name := range []string{rollout.RuntimeSandboxed, rollout.RuntimeStrict} {
		args := strings.Join(want[name], " ")
		if out, err := sc.Exec(runscPath + " install --runtime=" + name + " -- " + args + " 2>&1"); err != nil {
			_, _ = sc.Exec(restore)
			return fmt.Errorf("runsc install --runtime=%s: %s: %w", name, lastLine(out), err)
		}
		logFn("  ✓ " + daemonConfigPath + ": " + name + " " + args)
	}
	if out, err := sc.Exec("dockerd --validate --config-file " + daemonConfigPath + " 2>&1"); err != nil {
		_, _ = sc.Exec(restore)
		return fmt.Errorf("docker refused the new configuration, so it was put back as it was: %s: %w", lastLine(out), err)
	}
	if _, err := sc.Exec("systemctl reload docker"); err != nil {
		return fmt.Errorf("systemctl reload docker: %w", err)
	}
	for i := 0; i < 15; i++ {
		if registered(sc, want) {
			logFn("  ✓ Docker reloaded; no container was restarted")
			return nil
		}
		_, _ = sc.Exec("sleep 1")
	}
	return errors.New("docker was reloaded but doesn't list runsc and runsc-strict yet (journalctl -u docker)")
}

// ufwAllowDNSRelay lets containers reach the agent's DNS relay through UFW,
// whose default denies what bridges send the host (firewall.nft already
// allows DNS). ufw skips a rule it has.
const ufwAllowDNSRelay = "ufw allow to " + agentproto.DNSRelayAddr + " port 53 comment 'Toft DNS relay (D-115)'"

// allowDNSRelay adds ufwAllowDNSRelay where UFW is installed.
func allowDNSRelay(sc shell) error {
	if out, _ := sc.Exec("command -v ufw >/dev/null 2>&1 && echo yes || echo no"); strings.TrimSpace(out) != "yes" {
		return nil
	}
	if out, err := sc.Exec(ufwAllowDNSRelay + " 2>&1"); err != nil {
		return fmt.Errorf("let containers reach the DNS relay: %s: %w", lastLine(out), err)
	}
	return nil
}

// gvisorCheckScript starts the pinned busybox under runsc, then has a
// sandboxed one find another container by name on a network of its own.
const gvisorCheckScript = `set -e
clean() { docker rm -f toft-gvisor-check-a >/dev/null 2>&1 || true; docker network rm toft-gvisor-check >/dev/null 2>&1 || true; }
clean; trap clean EXIT
docker run --rm --runtime=runsc --network none --pull missing ` + rollout.ProbeToolImage + ` true
docker network create toft-gvisor-check >/dev/null
docker run -d --name toft-gvisor-check-a --network toft-gvisor-check ` + rollout.ProbeToolImage + ` sleep 60 >/dev/null
docker run --rm --runtime=runsc --network toft-gvisor-check ` + rollout.ProbeToolImage + ` nslookup toft-gvisor-check-a >/dev/null
`

// strictCheckScript has a strict container serve on a network of its own,
// another find it by name through the agent's relay, and a runc probe reach
// it by its address, as the deploy's health check does (D-115).
const strictCheckScript = `set -e
clean() { docker rm -f toft-gvisor-check-a >/dev/null 2>&1 || true; docker network rm toft-gvisor-check >/dev/null 2>&1 || true; }
clean; trap clean EXIT
docker network create toft-gvisor-check >/dev/null
docker run -d --name toft-gvisor-check-a --runtime=` + rollout.RuntimeStrict + ` -v ` + agentproto.StrictResolvConf + `:/etc/resolv.conf:ro --network toft-gvisor-check --pull missing ` + rollout.ProbeToolImage + ` httpd -f -p 8080 -h /etc >/dev/null
docker run --rm --runtime=` + rollout.RuntimeStrict + ` -v ` + agentproto.StrictResolvConf + `:/etc/resolv.conf:ro --network toft-gvisor-check ` + rollout.ProbeToolImage + ` nslookup toft-gvisor-check-a >/dev/null ||
  { echo "it found no container by name through Toft's agent (` + agentproto.DNSRelayAddr + `)"; exit 1; }
ip=$(docker inspect -f '{{(index .NetworkSettings.Networks "toft-gvisor-check").IPAddress}}' toft-gvisor-check-a)
i=0; until docker run --rm --network toft-gvisor-check ` + rollout.ProbeToolImage + ` wget -q -O /dev/null -T 3 "http://$ip:8080/hostname"; do
  i=$((i+1)); [ $i -lt 10 ] || { echo "the health probe couldn't reach it at $ip"; exit 1; }; sleep 1
done
`

// testRunsc runs script, a smoke test named what for its error, which
// carries the script's last line rather than the command (its base64).
func testRunsc(sc shell, script, what string) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(script))
	if out, err := sc.Exec("echo " + encoded + " | base64 -d | sh 2>&1"); err != nil {
		why := lastLine(out)
		if why == "" {
			why = "it exited without output"
		}
		return fmt.Errorf("%s failed: %s", what, why)
	}
	return nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// GvisorRun is a server's last gVisor install (D-95). FinishedAt and OK are
// nil while it runs.
type GvisorRun struct {
	Release    string     `json:"release"`
	StartedAt  time.Time  `json:"started_at"`
	StartedBy  string     `json:"started_by"`
	FinishedAt *time.Time `json:"finished_at"`
	OK         *bool      `json:"ok"`
	Error      string     `json:"error"`
	Log        string     `json:"log"`
}

// GvisorRunStore records the installs. Satisfied by the Postgres repository.
type GvisorRunStore interface {
	StartGvisorRun(ctx context.Context, serverID, release, startedBy string) error
	AppendGvisorLog(ctx context.Context, serverID, line string) error
	FinishGvisorRun(ctx context.Context, serverID string, ok bool, errMsg string) error
	CloseUnfinishedGvisorRuns(ctx context.Context) (int64, error)
	GetGvisorRun(ctx context.Context, serverID string) (*GvisorRun, error)
}

// WithGvisorRuns records the installs.
func (s *Service) WithGvisorRuns(r GvisorRunStore) { s.gvisorRuns = r }

// AuthorizeGvisorInstall returns ErrGvisorOperatorOnly unless actorID is an
// instance operator: the install is root on the server.
func (s *Service) AuthorizeGvisorInstall(ctx context.Context, actorID string) error {
	return s.requireOperator(ctx, actorID, ErrGvisorOperatorOnly)
}

// PanelCapabilities reads what the panel's own server's probe last read
// (D-97); the instance package keeps it.
type PanelCapabilities interface {
	StoredPanelCapabilities(ctx context.Context) (*Capabilities, error)
}

// WithPanelCapabilities lets Sandboxed answer for the panel's own server.
func (s *Service) WithPanelCapabilities(p PanelCapabilities) { s.panelCaps = p }

// Sandboxed is the server's Sandboxed level as its probe last read it, with
// runsc's release; State "unknown" before the first probe that read it.
// serverID "" is the panel's own server.
func (s *Service) Sandboxed(ctx context.Context, serverID string) (Level, string, error) {
	return s.isolationLevel(ctx, serverID, "sandboxed")
}

// SandboxedStrict is the server's Sandboxed, strict network level (D-115),
// as Sandboxed reads Sandboxed's.
func (s *Service) SandboxedStrict(ctx context.Context, serverID string) (Level, string, error) {
	return s.isolationLevel(ctx, serverID, "sandboxed_strict")
}

func (s *Service) isolationLevel(ctx context.Context, serverID, id string) (Level, string, error) {
	var (
		c   *Capabilities
		err error
	)
	switch {
	case serverID != "":
		c, err = s.Capabilities(ctx, serverID)
	case s.panelCaps != nil:
		c, err = s.panelCaps.StoredPanelCapabilities(ctx)
	}
	if err != nil {
		return Level{}, "", err
	}
	if serverID == "" && (c == nil || c.Kernel == "") {
		return Level{ID: id, State: "unknown", Reason: "The panel's own server hasn't been checked yet: Check again under Isolation on Instance → Server."}, "", nil
	}
	return c.level(id)
}

// level is c's level id with runsc's release; c nil is a server never
// probed.
func (c *Capabilities) level(id string) (Level, string, error) {
	if c != nil {
		for _, l := range c.Levels {
			if l.ID == id {
				version := ""
				if r := c.runtime(rollout.RuntimeSandboxed); r != nil {
					version = r.Version
				}
				return l, version, nil
			}
		}
	}
	return Level{ID: id, State: "unknown", Reason: "This server hasn't been checked yet: Check again on its Server page."}, "", nil
}

// SandboxedLevel is whether one of a workspace's servers can run Sandboxed
// services (D-95), and Sandboxed with a strict network (D-115).
type SandboxedLevel struct {
	ServerID, ServerName, State, Reason, GvisorVersion string
	StrictState, StrictReason                          string
}

// SandboxedLevels lists the workspace's servers with their Sandboxed levels.
func (s *Service) SandboxedLevels(ctx context.Context, orgID string) ([]SandboxedLevel, error) {
	srvs, err := s.repo.ListByOrg(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}
	out := make([]SandboxedLevel, 0, len(srvs))
	for i := range srvs {
		c, err := s.capabilitiesOf(ctx, &srvs[i])
		if err != nil {
			return nil, err
		}
		l, version, _ := c.level("sandboxed")
		strict, _, _ := c.level("sandboxed_strict")
		out = append(out, SandboxedLevel{ServerID: srvs[i].ID, ServerName: srvs[i].Name, State: l.State, Reason: l.Reason, GvisorVersion: version,
			StrictState: strict.State, StrictReason: strict.Reason})
	}
	return out, nil
}

// GvisorUnsupported says why the server's last probe rules gVisor out, or
// "" when it doesn't (or never ran).
func (s *Service) GvisorUnsupported(ctx context.Context, serverID string) string {
	c, err := s.Capabilities(ctx, serverID)
	if err != nil || c == nil {
		return ""
	}
	for _, l := range c.Levels {
		if l.ID == "sandboxed" && l.State == "unavailable" {
			return l.Reason
		}
	}
	return ""
}

// InstallGvisor installs gVisor on the server, records the run as it goes
// and probes the server again afterwards. Like Provision, it carries on
// when the caller stops following it.
func (s *Service) InstallGvisor(ctx context.Context, id, actorID string, logFn func(string)) (platform string, err error) {
	if _, busy := s.gvisorBusy.LoadOrStore(id, struct{}{}); busy {
		return "", ErrGvisorRunning
	}
	defer s.gvisorBusy.Delete(id)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), GvisorTimeout)
	defer cancel()
	srv, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return "", fmt.Errorf("get server: %w", err)
	}
	record := s.gvisorRuns != nil
	if record {
		if err := s.gvisorRuns.StartGvisorRun(ctx, id, GvisorRelease, actorID); err != nil {
			s.log.Warn("record gvisor run", zap.Error(err), zap.String("server_id", id))
		}
	}
	logFn("Installing gVisor " + GvisorRelease + " on " + srv.Name)
	host, err := installGvisor(shellFor(ctx, s.SSH(srv)), s.agentDNS(srv), func(line string) {
		if record {
			if err := s.gvisorRuns.AppendGvisorLog(ctx, id, line); err != nil {
				s.log.Warn("record gvisor log", zap.Error(err), zap.String("server_id", id))
			}
		}
		logFn(line)
	})
	if err != nil {
		line := "  ✗ " + err.Error()
		if record {
			_ = s.gvisorRuns.AppendGvisorLog(ctx, id, line)
		}
		logFn(line)
	}
	if record {
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if ferr := s.gvisorRuns.FinishGvisorRun(ctx, id, err == nil, msg); ferr != nil {
			s.log.Warn("record gvisor outcome", zap.Error(ferr), zap.String("server_id", id))
		}
	}
	if s.caps != nil {
		if _, perr := s.ProbeCapabilities(ctx, id); perr != nil {
			s.log.Warn("probe capabilities after installing gVisor", zap.Error(perr), zap.String("server_id", id))
		}
	}
	return host.platform(), err
}

// GvisorInstalling reports whether an install runs on the server.
func (s *Service) GvisorInstalling(id string) bool {
	_, busy := s.gvisorBusy.Load(id)
	return busy
}

// GetGvisorRun returns the server's last gVisor install, nil before the
// first.
func (s *Service) GetGvisorRun(ctx context.Context, id string) (*GvisorRun, error) {
	if s.gvisorRuns == nil {
		return nil, nil
	}
	return s.gvisorRuns.GetGvisorRun(ctx, id)
}

// CloseUnfinishedGvisorRuns closes, at startup, the installs a restart cut
// short.
func (s *Service) CloseUnfinishedGvisorRuns(ctx context.Context) (int64, error) {
	if s.gvisorRuns == nil {
		return 0, nil
	}
	return s.gvisorRuns.CloseUnfinishedGvisorRuns(ctx)
}

func (r *postgresRepository) StartGvisorRun(ctx context.Context, serverID, release, startedBy string) error {
	by := pgtype.UUID{}
	if u, err := uuid.Parse(startedBy); err == nil {
		by = pgtype.UUID{Bytes: u, Valid: true}
	}
	if err := r.q.StartGvisorRun(ctx, sqlcdb.StartGvisorRunParams{ServerID: pgUUID(serverID), Release: release, StartedBy: by}); err != nil {
		return fmt.Errorf("start gvisor run: %w", err)
	}
	return nil
}

func (r *postgresRepository) AppendGvisorLog(ctx context.Context, serverID, line string) error {
	if err := r.q.AppendGvisorLog(ctx, sqlcdb.AppendGvisorLogParams{ServerID: pgUUID(serverID), Line: line}); err != nil {
		return fmt.Errorf("append gvisor log: %w", err)
	}
	return nil
}

func (r *postgresRepository) FinishGvisorRun(ctx context.Context, serverID string, ok bool, errMsg string) error {
	if err := r.q.FinishGvisorRun(ctx, sqlcdb.FinishGvisorRunParams{ServerID: pgUUID(serverID), Ok: &ok, Error: errMsg}); err != nil {
		return fmt.Errorf("finish gvisor run: %w", err)
	}
	return nil
}

func (r *postgresRepository) CloseUnfinishedGvisorRuns(ctx context.Context) (int64, error) {
	n, err := r.q.CloseUnfinishedGvisorRuns(ctx)
	if err != nil {
		return 0, fmt.Errorf("close unfinished gvisor runs: %w", err)
	}
	return n, nil
}

func (r *postgresRepository) GetGvisorRun(ctx context.Context, serverID string) (*GvisorRun, error) {
	row, err := r.q.GetGvisorRun(ctx, pgUUID(serverID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get gvisor run: %w", err)
	}
	run := &GvisorRun{Release: row.Release, StartedAt: row.StartedAt.Time, StartedBy: row.StartedByName, OK: row.Ok, Error: row.Error, Log: row.Log}
	if row.FinishedAt.Valid {
		t := row.FinishedAt.Time
		run.FinishedAt = &t
	}
	return run, nil
}

// UntrustedReady is "" when serverID ("" the panel's own) runs untrusted
// previews (D-97): Sandboxed available and the host firewall's chain for
// their networks loaded; else why not, in words.
func (s *Service) UntrustedReady(ctx context.Context, serverID string) string {
	name := "The panel's server"
	if serverID != "" {
		if srv, err := s.repo.GetByID(ctx, serverID); err == nil {
			name = srv.Name
		}
	}
	level, _, err := s.Sandboxed(ctx, serverID)
	if err != nil {
		return name + " couldn't be checked for gVisor: " + err.Error()
	}
	switch level.State {
	case "available":
	case "not_installed", "unknown":
		return name + " can't run Sandboxed yet: an instance operator installs gVisor from its page (Isolation)."
	default:
		return name + " can't run Sandboxed: " + level.Reason
	}
	var c *Capabilities
	switch {
	case serverID != "":
		c, err = s.Capabilities(ctx, serverID)
	case s.panelCaps != nil:
		c, err = s.panelCaps.StoredPanelCapabilities(ctx)
	}
	if err != nil || c == nil || !c.PreviewFirewall {
		return name + "'s host firewall doesn't keep previews off private addresses yet: Provision it again, or Install gVisor again."
	}
	return ""
}
