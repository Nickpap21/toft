package server

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"maps"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nickpap21/toft/backend/internal/advisory"
	"github.com/Nickpap21/toft/backend/pkg/sshclient"
	"github.com/Nickpap21/toft/backend/pkg/sshpool"
	"go.uber.org/zap"
)

const (
	// provisionTimeout bounds a whole provision run.
	provisionTimeout = 30 * time.Minute
	// systemStatsTimeout bounds the one-shot system stats script (it
	// samples the CPU for a second).
	systemStatsTimeout = 15 * time.Second
)

// SystemStats holds a one-shot resource snapshot of the remote server.
type SystemStats struct {
	CPUPercent    float64 `json:"cpu_percent"`
	MemUsedMB     int64   `json:"mem_used_mb"`
	MemTotalMB    int64   `json:"mem_total_mb"`
	DiskUsedMB    int64   `json:"disk_used_mb"`
	DiskTotalMB   int64   `json:"disk_total_mb"`
	Load1         string  `json:"load_1"`
	Load5         string  `json:"load_5"`
	Load15        string  `json:"load_15"`
	Uptime        string  `json:"uptime"`
	DockerVersion string  `json:"docker_version,omitempty"`
	// UptimeSeconds since the machine booted (0 when not read).
	UptimeSeconds int64 `json:"uptime_seconds,omitempty"`
	// Traefik is the server's paas-traefik container: its state ("running",
	// "exited", …; "" when there is none) and image.
	TraefikStatus string `json:"traefik_status,omitempty"`
	TraefikImage  string `json:"traefik_image,omitempty"`
}

// OperatorChecker reports whether a user is an instance operator. Satisfied
// by instance.Service.
type OperatorChecker interface {
	IsOperator(ctx context.Context, userID string) (bool, error)
}

// ErrHostShellOperatorOnly is returned when someone who is not an instance
// operator asks for a shell or a command on a server's host (D-19).
var ErrHostShellOperatorOnly = errors.New("only instance operators can run commands or open a shell on a server's host")

// Service contains the business logic for the server domain.
type Service struct {
	repo          Repository
	acmeEmail     string
	allocReader   ServiceAllocReader // injected via WithAllocReader; may be nil
	forgetter     ServiceForgetter   // injected via WithServiceForgetter; nil refuses servers in use
	orgRoles      OrgRoleReader      // injected via WithOrgRoles; nil refuses sharing
	operators     OperatorChecker    // injected via WithOperatorChecker; nil refuses host shells and host-key resets
	conn          SSHConnector       // injected via WithSSH; nil fails every SSH call
	scan          hostKeyScanner
	log           *zap.Logger
	caps          CapabilityStore   // injected via WithCapabilities (D-93)
	updates       UpdateStore       // injected via WithUpdates (D-133)
	advisories    *advisory.Book    // injected via WithUpdates (D-133)
	updateRuns    UpdateRunStore    // injected via WithUpdateRuns (D-134)
	updateDeploys DeployChecker     // injected via WithUpdateRuns (D-134)
	updateAudit   AgentAuditor      // injected via WithUpdateRuns (D-134)
	updateFollow  sync.Map          // server ID → *followedRun (updaterun.go)
	gvisorRuns    GvisorRunStore    // injected via WithGvisorRuns (D-95)
	gvisorBusy    sync.Map          // server ID → an install running there
	panelCaps     PanelCapabilities // injected via WithPanelCapabilities (D-97)
	agents        *agentDeps        // injected via WithAgents (D-106); nil: no agents
	cutovers      sync.Map          // server IDs whose Traefik is being switched (cutOverRoutes)
	firstJoins    sync.Map          // server IDs provisioning on their first join (provisionOnFirstJoin)
	provisioning  sync.Map          // server IDs Provision runs on now
	// App networks (package C): under isolated app networks the
	// provisioner writes addressPool on fresh servers and attaches the
	// proxy to every app network.
	appNetworksIsolated bool
	addressPool         string
	// panelBackupDir: see WithPanelBackupDir.
	panelBackupDir string
	panelRestore   PanelRestore // injected via WithPanelRestore (D-163)
}

func NewService(repo Repository, acmeEmail string) *Service {
	return &Service{repo: repo, acmeEmail: acmeEmail, scan: sshclient.ScanHostKey, log: zap.NewNop()}
}

// WithAppNetworks tells the provisioner whether app networks are isolated
// and which address pool fresh servers get (netplan.ParseAddressPool).
// Under legacy the provisioner does what it did before.
// AppNetworks is how provisioning sets up app networks: "isolated" or
// "legacy" (one shared network). The address pool a server's Docker uses
// is what its capability probe read (Capabilities.AddressPools).
func (s *Service) AppNetworks() string {
	if s.appNetworksIsolated {
		return "isolated"
	}
	return "legacy"
}

func (s *Service) WithAppNetworks(isolated bool, addressPool string) {
	s.appNetworksIsolated = isolated
	s.addressPool = addressPool
}

// WithAllocReader injects the allocation reader after construction (called from main.go).
func (s *Service) WithAllocReader(r ServiceAllocReader) {
	s.allocReader = r
}

// WithOperatorChecker enables host shells (exec, PTY) for instance
// operators. Without one they are refused for everyone (fail closed).
func (s *Service) WithOperatorChecker(c OperatorChecker) {
	s.operators = c
}

// AuthorizeHostShell returns ErrHostShellOperatorOnly unless actorID is an
// instance operator. A host shell is root on the server, which no workspace
// role should grant.
func (s *Service) AuthorizeHostShell(ctx context.Context, actorID string) error {
	return s.requireOperator(ctx, actorID, ErrHostShellOperatorOnly)
}

// requireOperator returns denied unless actorID is an instance operator,
// and fails closed without an operator checker.
func (s *Service) requireOperator(ctx context.Context, actorID string, denied error) error {
	if s.operators == nil || actorID == "" {
		return denied
	}
	ok, err := s.operators.IsOperator(ctx, actorID)
	if err != nil {
		return fmt.Errorf("check operator: %w", err)
	}
	if !ok {
		return denied
	}
	return nil
}

func (s *Service) CreateServer(ctx context.Context, orgID, userID string, input CreateServerInput) (*Server, error) {
	if input.Name == "" || input.IPAddress == "" {
		return nil, fmt.Errorf("name and ip address are required")
	}
	if !ValidServerName(input.Name) {
		return nil, ErrInvalidServerName
	}
	return s.repo.Create(ctx, orgID, userID, input)
}

func (s *Service) GetServer(ctx context.Context, id string) (*Server, error) {
	return s.repo.GetByID(ctx, id)
}

func (s *Service) ListServers(ctx context.Context, orgID string) ([]Server, error) {
	return s.repo.ListByOrg(ctx, orgID)
}

// ListAllServers returns all servers regardless of org. Used for health polling.
func (s *Service) ListAllServers(ctx context.Context) ([]Server, error) {
	return s.repo.ListAll(ctx)
}

func (s *Service) UpdateServerStatus(ctx context.Context, id, status string) (*Server, error) {
	return s.repo.UpdateStatus(ctx, id, status)
}

// DeleteServer deletes the server and closes its pooled SSH connections.
// ErrServerInUse: services are assigned to the server. Deleting it would
// leave their containers running there with nothing to manage them.
var ErrServerInUse = errors.New("services run on this server")

// ServiceForgetter deletes the services on a server from the panel without
// touching the server, for a server that is gone. Satisfied by
// *service.ServiceManager via main.go.
type ServiceForgetter interface {
	ForgetServicesOnServer(ctx context.Context, serverID string) error
}

// WithServiceForgetter lets DeleteServer take the server's services with it.
func (s *Service) WithServiceForgetter(f ServiceForgetter) {
	s.forgetter = f
}

// SharedInUse reports whether workspaces the server is shared with run
// services on it (DeleteServer refuses then, with ErrServerShared).
func (s *Service) SharedInUse(ctx context.Context, id string) (bool, error) {
	n, err := s.repo.CountSharedServicesOnServer(ctx, id)
	return n > 0, err
}

// DeleteServer removes the server from the panel; the machine is not
// touched, except that a connected agent is asked to remove itself. It refuses while services run on it (ErrServerInUse), unless
// withServices: then those services are deleted from the panel too, for a
// server that is gone for good. Services of workspaces it is shared with
// always refuse it (ErrServerShared).
func (s *Service) DeleteServer(ctx context.Context, id string, withServices bool) error {
	if n, err := s.repo.CountSharedServicesOnServer(ctx, id); err != nil {
		return err
	} else if n > 0 {
		return ErrServerShared
	}
	n, err := s.repo.CountServicesOnServer(ctx, id)
	if err != nil {
		return err
	}
	if n > 0 {
		if !withServices || s.forgetter == nil {
			return fmt.Errorf("%w: %d. Delete them first, or remove the server with them if it is gone for good", ErrServerInUse, n)
		}
		if err := s.forgetter.ForgetServicesOnServer(ctx, id); err != nil {
			return fmt.Errorf("delete the server's services: %w", err)
		}
	}
	// Its agent would knock forever: a connected one removes itself, and
	// one that connects later is refused and stops. Before the delete, as
	// the agent's connection reads its pinned key; a delete that then fails
	// leaves a server without an agent, which a second delete removes.
	if s.agents != nil {
		srv, err := s.repo.GetByID(ctx, id)
		if err != nil {
			return err
		}
		if srv.AgentKey != "" {
			s.uninstallAgent(ctx, srv)
		}
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	if s.agents != nil {
		s.agents.hub.Drop(id)
	}
	if s.conn != nil {
		s.conn.Invalidate(id)
	}
	return nil
}

func (s *Service) CheckOwnership(ctx context.Context, id, orgID string) (bool, error) {
	return s.repo.CheckOwnership(ctx, id, orgID)
}

// TestConnection checks that the server answers over SSH, dialling even
// while the pool is backing off after failures, and updates the server
// status: "active" on success, "offline" on failure (including a changed
// host key). The latency is a keepalive round trip on the connection. The
// first connection to a server pins its host key (HostKeyNewlyPinned).
func (s *Service) TestConnection(ctx context.Context, id string) (*ConnectionResult, error) {
	srv, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get server: %w", err)
	}

	h := s.SSH(srv)
	info, err := h.Ensure(ctx, sshpool.EnsureOpts{Force: true})
	if err != nil {
		_, _ = s.repo.UpdateStatus(ctx, id, StatusOffline)
		return nil, fmt.Errorf("SSH connect: %w", err)
	}
	rtt, err := h.Ping(ctx)
	if err != nil {
		_, _ = s.repo.UpdateStatus(ctx, id, StatusOffline)
		return nil, fmt.Errorf("SSH connect: %w", err)
	}

	_, _ = s.repo.UpdateStatus(ctx, id, StatusActive)

	// Fire-and-forget: capacity discovery runs with its own 30s budget,
	// detached from the request context, over the same pooled connection.
	// Failure is non-fatal.
	go func() {
		discoverCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := s.DiscoverCapacity(discoverCtx, id); err != nil {
			log.Printf("server %s: capacity discovery failed: %v", id, err)
		}
	}()

	fp, keyType, _ := sshclient.Fingerprint(info.HostKey)
	host := fmt.Sprintf("%s@%s:%d", srv.User, srv.IPAddress, srv.Port)
	if srv.Connection == ConnectionAgent {
		host = "the agent on " + srv.Name
	}
	return &ConnectionResult{
		Host:               host,
		LatencyMS:          rtt.Milliseconds(),
		HostKeyType:        keyType,
		HostKeyFingerprint: fp,
		HostKeyNewlyPinned: info.NewlyPinned,
	}, nil
}

// Provision runs the idempotent server setup (Docker + paas-network + Traefik) via SSH.
// Progress lines are delivered to logFn for live streaming. On success, status is set to "active".
//
// The setup keeps running when the caller stops following it (the browser
// closes the stream; the page promises the install carries on), so it is
// detached from ctx's cancellation and bounded by provisionTimeout instead.
func (s *Service) Provision(ctx context.Context, id, actorID string, logFn func(string)) error {
	// One run at a time: two would fight over apt's lock, and the second
	// would replace the run the first one's page follows.
	if _, busy := s.provisioning.LoadOrStore(id, true); busy {
		return ErrProvisionRunning
	}
	defer s.provisioning.Delete(id)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), provisionTimeout)
	defer cancel()
	// No routes cutover starts while it runs (cutOverRoutes).
	s.cutovers.Store(id, true)
	defer s.cutovers.Delete(id)

	// The run is recorded as it goes (D-48), so the server's page shows it
	// to whoever opens it, during the run and after. A failed write is
	// logged and never stops the provisioning.
	if err := s.repo.StartProvisionRun(ctx, id, actorID); err != nil {
		s.log.Warn("record provision run", zap.Error(err), zap.String("server_id", id))
	}
	warnings, err := s.provision(ctx, id, func(line string) {
		if err := s.repo.AppendProvisionLog(ctx, id, line); err != nil {
			s.log.Warn("record provision log", zap.Error(err), zap.String("server_id", id))
		}
		logFn(line)
	})
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if ferr := s.repo.FinishProvisionRun(ctx, id, err == nil, msg, warnings); ferr != nil {
		s.log.Warn("record provision outcome", zap.Error(ferr), zap.String("server_id", id))
	}
	return err
}

// GetProvisionRun returns the server's last provisioning run, or
// ErrNoProvisionRun.
func (s *Service) GetProvisionRun(ctx context.Context, id string) (*ProvisionRun, error) {
	run, err := s.repo.GetProvisionRun(ctx, id)
	if err != nil {
		return nil, err
	}
	run.AddressPoolFix = addressPoolFix(run)
	return run, nil
}

// WithProvisionWarnings fills each response's provision warnings (one
// query for the workspace). A failed read leaves them out.
func (s *Service) WithProvisionWarnings(ctx context.Context, orgID string, rs []ServerResponse) {
	byServer, err := s.repo.ListProvisionWarnings(ctx, orgID)
	if err != nil {
		s.log.Warn("could not read the servers' provisioning warnings", zap.Error(err))
		return
	}
	for i := range rs {
		rs[i].ProvisionWarnings = byServer[rs[i].ID]
	}
}

// CloseUnfinishedProvisionRuns closes, at startup, the runs a restart cut
// short: provisioning runs in the backend, so none survives it.
func (s *Service) CloseUnfinishedProvisionRuns(ctx context.Context) (int64, error) {
	return s.repo.CloseUnfinishedProvisionRuns(ctx)
}

// provision returns what the run left out (provision's warnings).
func (s *Service) provision(ctx context.Context, id string, logFn func(string)) ([]string, error) {

	srv, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get server: %w", err)
	}

	opts := provisionOpts{ACMEEmail: s.acmeEmail, SSHPort: srv.Port, AppNetworksIsolated: s.appNetworksIsolated, AddressPool: s.addressPool,
		LocalLogs: srv.Connection == ConnectionAgent}
	if dns, err := s.repo.GetDNSProvider(ctx, id); err == nil {
		opts.DNS = dns
	} else if !errors.Is(err, ErrNoDNSProvider) {
		return nil, fmt.Errorf("load DNS provider: %w", err)
	}
	if b, err := s.repo.GetBackupDestination(ctx, id); err == nil {
		opts.BackupEnv = b.envFile()
	} else if !errors.Is(err, ErrNoBackupDestination) {
		return nil, fmt.Errorf("load the backup destination: %w", err)
	}
	// Before anything changes on the machine.
	if err := s.checkSameMachineDNS(ctx, srv, opts.DNS); err != nil {
		return nil, err
	}
	if opts.DNS != nil {
		logFn("DNS provider " + opts.DNS.Provider + ": Traefik gets the letsencrypt-dns resolver for wildcard domains")
	}

	if _, err := s.repo.UpdateStatus(ctx, id, StatusProvisioning); err != nil {
		return nil, fmt.Errorf("update status: %w", err)
	}

	logFn("Connecting via SSH...")
	h := s.SSH(srv)
	info, err := h.Ensure(ctx, sshpool.EnsureOpts{Force: true})
	if err != nil {
		_, _ = s.repo.UpdateStatus(ctx, id, StatusOffline)
		return nil, fmt.Errorf("SSH connect: %w", err)
	}
	if info.NewlyPinned {
		fp, keyType, _ := sshclient.Fingerprint(info.HostKey)
		logFn(fmt.Sprintf("Pinned SSH host key %s %s (first connection)", keyType, fp))
	}

	opts.SyncRoutes = s.routesSync(ctx, srv)
	sc := shellFor(ctx, h)
	s.nameFromHost(ctx, srv, sc, logFn)
	warnings, err := provision(sc, opts, logFn)
	if err != nil {
		_, _ = s.repo.UpdateStatus(ctx, id, StatusOffline)
		return nil, err
	}
	// Its size before it shows as provisioned, for Add a server's joined
	// line and placement (D-153); DiscoverCapacity probes what it can run
	// too.
	if _, err := s.DiscoverCapacity(ctx, id); err != nil {
		s.log.Warn("read capacity after provisioning", zap.Error(err), zap.String("server_id", id))
		if s.caps != nil {
			if _, perr := s.ProbeCapabilities(ctx, id); perr != nil {
				s.log.Warn("probe capabilities after provisioning", zap.Error(perr), zap.String("server_id", id))
			}
		}
	}

	if _, err := s.repo.MarkProvisioned(ctx, id); err != nil {
		return nil, fmt.Errorf("mark provisioned: %w", err)
	}
	if _, err = s.repo.UpdateStatus(ctx, id, StatusActive); err != nil {
		return nil, err
	}
	return warnings, nil
}

// ErrSameMachineDNS: another server entry is the same machine (a server
// belongs to one workspace, so a machine two workspaces use is two entries)
// with a different DNS provider setting. Both entries run the machine's one
// Traefik, so provisioning from this one would replace the other's wildcard
// certificate setup.
var ErrSameMachineDNS = errors.New("this machine is also another server entry (in another workspace) with a different DNS provider setting; " +
	"both entries run its one Traefik, so provisioning from here would replace that wildcard certificate setup. " +
	"Give both entries the same DNS provider (Servers → server → DNS provider), then provision")

// checkSameMachineDNS refuses provisioning srv when another server entry is
// the same machine (the same pinned host key, or the same address and port)
// and has a different DNS provider setting than dns.
func (s *Service) checkSameMachineDNS(ctx context.Context, srv *Server, dns *DNSConfig) error {
	all, err := s.repo.ListAll(ctx)
	if err != nil {
		return fmt.Errorf("list servers: %w", err)
	}
	for _, other := range all {
		if other.ID == srv.ID || !sameMachine(*srv, other) {
			continue
		}
		theirs, err := s.repo.GetDNSProvider(ctx, other.ID)
		if errors.Is(err, ErrNoDNSProvider) {
			theirs, err = nil, nil
		}
		if err != nil {
			return fmt.Errorf("load the DNS provider of server %s: %w", other.ID, err)
		}
		if !sameDNS(dns, theirs) {
			return ErrSameMachineDNS
		}
	}
	return nil
}

// sameMachine reports whether two server entries reach one machine: the
// same pinned host key, or the same address and SSH port.
func sameMachine(a, b Server) bool {
	if a.HostKey != "" && a.HostKey == b.HostKey {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(a.IPAddress), strings.TrimSpace(b.IPAddress)) && a.Port == b.Port
}

// sameDNS reports whether two DNS provider settings give Traefik the same
// configuration (nil is none).
func sameDNS(a, b *DNSConfig) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Provider == b.Provider && maps.Equal(a.Credentials, b.Credentials)
}

// ExecOnServer runs command on the server over its pooled SSH connection and
// streams the output line-by-line to logFn. When ctx ends (the caller went
// away), the remote command is stopped. Instance operators only.
func (s *Service) ExecOnServer(ctx context.Context, actorID, id, command string, logFn func(string)) error {
	if err := s.AuthorizeHostShell(ctx, actorID); err != nil {
		return err
	}
	srv, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("get server: %w", err)
	}
	return s.SSH(srv).ExecStream(ctx, command+" 2>&1", logFn)
}

// OpenPTY allocates a PTY with the given terminal dimensions on the server's
// pooled SSH connection and returns a ready PTYSession. The session lives
// until PTYSession.Close or until ctx ends; closing it leaves the
// connection to other users. Instance operators only.
func (s *Service) OpenPTY(ctx context.Context, actorID, id string, cols, rows int) (*sshclient.PTYSession, error) {
	if err := s.AuthorizeHostShell(ctx, actorID); err != nil {
		return nil, err
	}
	srv, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get server: %w", err)
	}
	return s.SSH(srv).NewPTY(ctx, cols, rows)
}

// GetSystemStats collects a one-shot resource snapshot from the remote server
// via a small shell script executed over SSH.
func (s *Service) GetSystemStats(ctx context.Context, id string) (*SystemStats, error) {
	srv, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get server: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, systemStatsTimeout)
	defer cancel()

	const script = `CPU_A=$(awk 'NR==1{idle=$5+$6; total=0; for(i=2;i<=NF;i++) total+=$i; print idle" "total}' /proc/stat)
sleep 1
CPU_B=$(awk 'NR==1{idle=$5+$6; total=0; for(i=2;i<=NF;i++) total+=$i; print idle" "total}' /proc/stat)
IDLE_D=$(echo "$CPU_A $CPU_B" | awk '{print $3-$1}')
TOTAL_D=$(echo "$CPU_A $CPU_B" | awk '{print $4-$2}')
CPU=$(echo "$IDLE_D $TOTAL_D" | awk '{if($2>0) printf "%.1f", (1-$1/$2)*100; else print "0"}')
MEM=$(free -m | awk 'NR==2{print $3" "$2}')
DISK=$(df -BM / | awk 'NR==2{gsub(/M/,"",$3); gsub(/M/,"",$2); print $3" "$2}')
LOAD=$(awk '{print $1" "$2" "$3}' /proc/loadavg)
UPTIME=$(uptime -p 2>/dev/null | sed 's/^up //' || uptime | awk -F'up ' '{sub(/,.*/,"",$2); print $2}')
DOCKER=$(docker --version 2>/dev/null | awk '{print $3}' | tr -d ',' || echo "")
UPTIME_S=$(cut -d. -f1 /proc/uptime 2>/dev/null)
TRAEFIK=$(docker inspect -f '{{.State.Status}} {{.Config.Image}}' paas-traefik 2>/dev/null || echo "")
printf 'cpu:%s\nmem:%s\ndisk:%s\nload:%s\nuptime:%s\ndocker:%s\nuptime_s:%s\ntraefik:%s\n' "$CPU" "$MEM" "$DISK" "$LOAD" "$UPTIME" "$DOCKER" "$UPTIME_S" "$TRAEFIK"`

	encoded := base64.StdEncoding.EncodeToString([]byte(script))
	out, err := s.SSH(srv).Exec(ctx, "echo "+encoded+" | base64 -d | sh")
	if err != nil {
		return nil, fmt.Errorf("collect stats: %w", err)
	}
	return parseSystemStats(out)
}

func parseSystemStats(raw string) (*SystemStats, error) {
	stats := &SystemStats{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		idx := strings.IndexByte(line, ':')
		if idx < 0 {
			continue
		}
		key := line[:idx]
		val := strings.TrimSpace(line[idx+1:])
		switch key {
		case "cpu":
			cpu, _ := strconv.ParseFloat(val, 64)
			stats.CPUPercent = cpu
		case "mem":
			parts := strings.Fields(val)
			if len(parts) == 2 {
				stats.MemUsedMB, _ = strconv.ParseInt(parts[0], 10, 64)
				stats.MemTotalMB, _ = strconv.ParseInt(parts[1], 10, 64)
			}
		case "disk":
			parts := strings.Fields(val)
			if len(parts) == 2 {
				stats.DiskUsedMB, _ = strconv.ParseInt(parts[0], 10, 64)
				stats.DiskTotalMB, _ = strconv.ParseInt(parts[1], 10, 64)
			}
		case "load":
			parts := strings.Fields(val)
			if len(parts) >= 3 {
				stats.Load1, stats.Load5, stats.Load15 = parts[0], parts[1], parts[2]
			}
		case "uptime":
			stats.Uptime = val
		case "docker":
			stats.DockerVersion = val
		case "uptime_s":
			stats.UptimeSeconds, _ = strconv.ParseInt(val, 10, 64)
		case "traefik":
			if parts := strings.Fields(val); len(parts) == 2 {
				stats.TraefikStatus, stats.TraefikImage = parts[0], parts[1]
			}
		}
	}
	return stats, nil
}
