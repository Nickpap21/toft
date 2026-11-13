package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Nickpap21/toft/backend/internal/advisory"
	"github.com/Nickpap21/toft/backend/internal/agenthub"
	"github.com/Nickpap21/toft/backend/internal/rollout"

	"github.com/docker/docker/errdefs"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"

	"github.com/Nickpap21/toft/backend/api"
	"github.com/Nickpap21/toft/backend/db"
	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/internal/agent"
	"github.com/Nickpap21/toft/backend/internal/alert"
	"github.com/Nickpap21/toft/backend/internal/apikey"
	"github.com/Nickpap21/toft/backend/internal/auth"
	"github.com/Nickpap21/toft/backend/internal/catalog"
	"github.com/Nickpap21/toft/backend/internal/code"
	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/configfile"
	"github.com/Nickpap21/toft/backend/internal/dashboard"
	"github.com/Nickpap21/toft/backend/internal/dbtx"
	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/deploypolicy"
	"github.com/Nickpap21/toft/backend/internal/disk"
	"github.com/Nickpap21/toft/backend/internal/doc"
	"github.com/Nickpap21/toft/backend/internal/docker"
	"github.com/Nickpap21/toft/backend/internal/domain"
	"github.com/Nickpap21/toft/backend/internal/environment"
	"github.com/Nickpap21/toft/backend/internal/envvar"
	"github.com/Nickpap21/toft/backend/internal/events"
	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"github.com/Nickpap21/toft/backend/internal/inbox"
	"github.com/Nickpap21/toft/backend/internal/instance"
	"github.com/Nickpap21/toft/backend/internal/issue"
	"github.com/Nickpap21/toft/backend/internal/mcp"
	"github.com/Nickpap21/toft/backend/internal/metrics"
	"github.com/Nickpap21/toft/backend/internal/netplan"
	"github.com/Nickpap21/toft/backend/internal/observability"
	"github.com/Nickpap21/toft/backend/internal/org"
	"github.com/Nickpap21/toft/backend/internal/preview"
	"github.com/Nickpap21/toft/backend/internal/project"
	"github.com/Nickpap21/toft/backend/internal/reseal"
	"github.com/Nickpap21/toft/backend/internal/resources"
	"github.com/Nickpap21/toft/backend/internal/reveal"
	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/internal/sharedvolume"
	"github.com/Nickpap21/toft/backend/internal/user"
	"github.com/Nickpap21/toft/backend/internal/version"
	"github.com/Nickpap21/toft/backend/internal/webhook"
	"github.com/Nickpap21/toft/backend/pkg/broker"
	"github.com/Nickpap21/toft/backend/pkg/buildinfo"
	"github.com/Nickpap21/toft/backend/pkg/config"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/Nickpap21/toft/backend/pkg/logger"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/netcheck"
	"github.com/Nickpap21/toft/backend/pkg/reauth"
	"github.com/Nickpap21/toft/backend/pkg/registry"
	"github.com/Nickpap21/toft/backend/pkg/reservation"
	"github.com/Nickpap21/toft/backend/pkg/scm"
	"github.com/Nickpap21/toft/backend/pkg/sshpool"
	"github.com/Nickpap21/toft/backend/pkg/wspty"
)

// deploymentEventPublisher adapts broker.Broker to deployment.EventPublisher.
// The broker accepts broker.Event; this wrapper converts DeploymentEvent → broker.Event.
type deploymentEventPublisher struct {
	b *broker.Broker
}

func (a *deploymentEventPublisher) Publish(orgID string, e deployment.DeploymentEvent) {
	a.b.Publish(orgID, broker.Event{Type: e.Type, Payload: e})
}

// userNames adapts user.Service to deployment.UserNamer.
type userNames struct{ users *user.Service }

// credentials lets the auth middleware take API keys and agents' access
// tokens (D-100).
type credentials struct {
	middleware.APIKeyValidator
	middleware.AgentTokenValidator
}

func (u userNames) UserName(ctx context.Context, userID string) (string, error) {
	usr, err := u.users.GetByID(ctx, userID)
	if err != nil {
		return "", err
	}
	return usr.Name, nil
}

// serviceOrchestratorFactory adapts docker.Factory to service.ContainerOrchestratorFactory.
// deployment.Orchestrator (returned by docker.Factory) is a superset of
// service.ContainerOrchestrator, so the assignment in ForServer is valid.
type serviceOrchestratorFactory struct {
	inner *docker.Factory
}

func (a *serviceOrchestratorFactory) ForServer(srv *server.Server) (service.ContainerOrchestrator, func(), error) {
	orch, cleanup, err := a.inner.ForServer(srv)
	if err != nil {
		return nil, cleanup, err
	}
	return orch, cleanup, nil
}

// sharedVolumeHosts reaches the Docker volumes of shared volumes (D-38) on
// a server through its orchestrator. Satisfies sharedvolume.Hosts.
type sharedVolumeHosts struct {
	servers *server.Service
	factory *docker.Factory
}

// volumeHost is the part of both orchestrators shared volumes use.
type volumeHost interface {
	PruneVolume(ctx context.Context, volumeName string) error
	VolumeSizes(ctx context.Context, names []string) (map[string]int64, error)
}

func (h *sharedVolumeHosts) host(ctx context.Context, serverID string) (volumeHost, func(), error) {
	var srv *server.Server
	if serverID != "" {
		s, err := h.servers.GetServer(ctx, serverID)
		if err != nil {
			return nil, nil, fmt.Errorf("server %s: %w", serverID, err)
		}
		srv = s
	}
	orch, cleanup, err := h.factory.ForServer(srv)
	if err != nil {
		return nil, nil, err
	}
	vh, ok := orch.(volumeHost)
	if !ok {
		cleanup()
		return nil, nil, errors.New("this orchestrator cannot manage volumes")
	}
	return vh, cleanup, nil
}

func (h *sharedVolumeHosts) Remove(ctx context.Context, serverID, name string) error {
	vh, cleanup, err := h.host(ctx, serverID)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := vh.PruneVolume(ctx, name); err != nil && !errdefs.IsNotFound(err) {
		return err
	}
	return nil
}

func (h *sharedVolumeHosts) Sizes(ctx context.Context, serverID string, names []string) (map[string]int64, error) {
	vh, cleanup, err := h.host(ctx, serverID)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return vh.VolumeSizes(ctx, names)
}

// hostUpdates is deployment.HostUpdateGate: a Docker update or a reboot on
// a server, or on the panel's own (serverID ""), holds its deploys (D-134).
type hostUpdates struct {
	servers *server.Service
	panel   *instance.Service
}

func (h hostUpdates) HostUpdating(ctx context.Context, serverID string) (bool, error) {
	if serverID == "" {
		return h.panel.PanelUpdating(ctx)
	}
	return h.servers.HostUpdating(ctx, serverID)
}

// diskHosts opens a host's Docker for the disk advisor (D-135): "" is the
// panel's own server.
type diskHosts struct {
	factory *docker.Factory
	servers *server.Service
}

func (h diskHosts) Host(ctx context.Context, serverID string) (disk.Host, error) {
	if serverID == "" {
		return h.factory.DiskHost(ctx, nil)
	}
	srv, err := h.servers.GetServer(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("find the server: %w", err)
	}
	return h.factory.DiskHost(ctx, srv)
}

// diskDeploys is disk.DeployGate on the deployment service.
type diskDeploys struct{ deployments *deployment.Service }

func (d diskDeploys) DeployRunning(ctx context.Context, serverID string) (bool, error) {
	return d.deployments.DeployRunning(ctx, serverID)
}

// diskAudit writes the advisor's audit entries: a server's to its
// workspace's log, the panel's own server's to the instance's.
type diskAudit struct {
	orgs     *org.Service
	instance *instance.Service
}

func (a diskAudit) Audit(ctx context.Context, serverID, orgID, actorID, action string, detail map[string]any) {
	if serverID == "" {
		a.instance.WriteAudit(ctx, actorID, action, detail)
		return
	}
	if actorID == "" {
		actorID = org.ToftActor // the advisor's own clean
	}
	a.orgs.WriteAuditLogDetail(ctx, orgID, actorID, action, "server:"+serverID, detail)
}

// diskPeople answers the advisor's questions about people.
type diskPeople struct {
	instance *instance.Service
	users    *user.Service
}

func (p diskPeople) IsOperator(ctx context.Context, userID string) (bool, error) {
	return p.instance.IsOperator(ctx, userID)
}

func (p diskPeople) Name(ctx context.Context, userID string) string {
	if u, err := p.users.GetByID(ctx, userID); err == nil && u != nil {
		return u.Name
	}
	return ""
}

// volumeMeter adapts sharedvolume.Service to alert.VolumeMeter.
type volumeMeter struct{ svc *sharedvolume.Service }

func (m volumeMeter) Measure(ctx context.Context) ([]alert.VolumeUsage, error) {
	usage, err := m.svc.Measure(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]alert.VolumeUsage, 0, len(usage))
	for _, u := range usage {
		out = append(out, alert.VolumeUsage{ID: u.ID, Name: u.Name, SizeLimitBytes: u.SizeLimitBytes, UsedBytes: u.UsedBytes, Measured: u.Measured})
	}
	return out, nil
}

// configFileWriterAdapter adapts configfile.Service to deployment.ConfigFileWriter.
// configfile.VolumeMount and deployment.ConfigFileMount have the same fields but are
// different types; this wrapper converts between them without importing deployment
// from the configfile package.
// domainServers gives the domain check the facts of a service's server:
// its public address, which a workspace using a shared server is told here
// only (D-45), and its DNS provider and provisioning for its owners. The
// panel host's address is what the panel's own domain resolves to, or with
// no domain the IP install.sh recorded.
type domainServers struct {
	servers *server.Service
	domain  func() string
	panelIP string
}

func (d domainServers) DomainServer(ctx context.Context, serverID, orgID string) (domain.ServerFacts, error) {
	if serverID == "" {
		facts := domain.ServerFacts{Name: "the panel host"}
		if d.domain() == "" {
			facts.Address = d.panelIP
			return facts, nil
		}
		lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if addrs, err := net.DefaultResolver.LookupIP(lctx, "ip4", d.domain()); err == nil && len(addrs) > 0 {
			facts.Address = addrs[0].String()
		}
		return facts, nil
	}
	srv, err := d.servers.GetServer(ctx, serverID)
	if err != nil {
		return domain.ServerFacts{}, fmt.Errorf("get server: %w", err)
	}
	facts := domain.ServerFacts{Name: srv.Name, Address: srv.IPAddress, Owner: srv.OrgID == orgID}
	dns, err := d.servers.GetDNSProvider(ctx, serverID)
	switch {
	case err == nil:
		facts.WildcardsOK = true
		if facts.Owner {
			facts.DNSProvider = dns.Provider
			saved := dns.UpdatedAt
			facts.DNSProviderSavedAt = &saved
			facts.ProvisionedAt = srv.ProvisionedAt
		}
	case !errors.Is(err, server.ErrNoDNSProvider):
		return domain.ServerFacts{}, fmt.Errorf("get the DNS provider: %w", err)
	}
	return facts, nil
}

// panelHost reads the panel host's facts for the Welcome page: the
// panel's own domain, as the domain checks resolve it, the local Docker
// host's size, and the app network mode.
// panelKitBackup reads the panel host's backup destination for the
// recovery kit (instance.KitBackupReader).
type panelKitBackup struct{ servers *server.Service }

func (p panelKitBackup) PanelKitBackup(ctx context.Context) (*instance.KitBackup, error) {
	repo, password, access, ok, err := p.servers.PanelBackupKit(ctx)
	if err != nil || !ok {
		return nil, err
	}
	return &instance.KitBackup{Repository: repo, Password: password, AccessKey: access}, nil
}

type panelHost struct {
	local   *docker.LocalOrchestrator
	servers *server.Service
	domain  func() string
	// network is the control plane's network; slice the apps' systemd
	// slice ("" when apps aren't capped).
	network, slice string
}

func (p panelHost) PanelHost(ctx context.Context) (instance.PanelHost, error) {
	cores, mem, disk, os, err := p.local.HostSize(ctx)
	if err != nil {
		return instance.PanelHost{}, err
	}
	host := instance.PanelHost{CPUCores: cores, MemoryBytes: mem, DiskBytes: disk, AppNetworks: p.servers.AppNetworks(), OS: os}
	host.ControlPlaneSubnet, _ = p.local.NetworkSubnet(ctx, p.network)
	// What Docker here uses, not the pool the panel writes on fresh servers;
	// nil (not known) when Docker didn't say.
	host.AppAddressPools, _ = p.local.AddressPools(ctx)
	if len(host.AppAddressPools) > 0 {
		host.AppAddressPool = host.AppAddressPools[0].Base
	}
	appsCPU, appsMem := reservation.Apps(cores, mem)
	host.Reservation = instance.Reservation{
		MinCPU: reservation.MinCPUs, MinMemoryBytes: reservation.MinMemory,
		SystemCPU: reservation.SystemCPU, SystemMemoryBytes: reservation.SystemMemory,
		PanelCPU: reservation.PanelCPU, PanelMemoryBytes: reservation.PanelMemory,
		AppsCPU: appsCPU, AppsMemoryBytes: appsMem, Enforced: p.slice != "",
	}
	host.Version = buildinfo.Version
	// A second at most: Home waits on this, and a name that resolves at all
	// resolves well within it.
	lctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if addrs, err := net.DefaultResolver.LookupIP(lctx, "ip4", p.domain()); err == nil && len(addrs) > 0 {
		host.Address = addrs[0].String()
	}
	return host, nil
}

type configFileWriterAdapter struct {
	svc *configfile.Service
}

func (a *configFileWriterAdapter) WriteToHost(ctx context.Context, serviceID string, srv *server.Server) ([]deployment.ConfigFileMount, []string, error) {
	files, err := a.svc.List(ctx, serviceID)
	if err != nil {
		return nil, nil, err
	}
	mounts, err := a.svc.WriteFiles(ctx, serviceID, srv, files)
	if err != nil {
		return nil, configContents(files), err
	}
	result := make([]deployment.ConfigFileMount, len(mounts))
	for i, m := range mounts {
		result[i] = deployment.ConfigFileMount{HostPath: m.HostPath, Target: m.Target, ReadOnly: m.ReadOnly}
	}
	return result, configContents(files), nil
}

func (a *configFileWriterAdapter) Contents(ctx context.Context, serviceID string) ([]string, error) {
	files, err := a.svc.List(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	return configContents(files), nil
}

// configContents are config files as the settings fingerprint reads them.
func configContents(files []configfile.ConfigFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path+"\x00"+f.Content)
	}
	return out
}

// projectEnvCreator adapts environment.Service to project.DefaultEnvEnsurer:
// the environment's ID alone.
type projectEnvCreator struct {
	envSvc *environment.Service
}

func (a *projectEnvCreator) EnsureDefault(ctx context.Context, projectID string) (string, error) {
	env, err := a.envSvc.EnsureDefault(ctx, projectID)
	if err != nil {
		return "", err
	}
	return env.ID, nil
}

// alertServerLister lists the remote servers for the alert monitor's disk
// check.
type alertServerLister struct{ servers *server.Service }

func (l alertServerLister) ListServerIDs(ctx context.Context) ([]string, error) {
	list, err := l.servers.ListAllServers(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(list))
	for _, s := range list {
		ids = append(ids, s.ID)
	}
	return ids, nil
}

// apiKeyProjects resolves services and environments to their project for
// apikey.EnforceScopes.
type apiKeyProjects struct {
	services     *service.ServiceManager
	environments *environment.Service
}

func (p apiKeyProjects) ServiceProject(ctx context.Context, serviceID string) (string, error) {
	svc, err := p.services.Get(ctx, serviceID)
	if err != nil {
		return "", err
	}
	return svc.ProjectID, nil
}

func (p apiKeyProjects) EnvironmentProject(ctx context.Context, environmentID string) (string, error) {
	env, err := p.environments.Get(ctx, environmentID)
	if err != nil {
		return "", err
	}
	return env.ProjectID, nil
}

// scopedServiceRemover deletes every service of an environment, a project or
// a workspace (service.ServiceManager.DeleteIn) for the packages that cannot
// import service, and turns a teardown failure into their own sentinel, with
// the message a client is shown.
type scopedServiceRemover struct {
	svc *service.ServiceManager
}

func (a scopedServiceRemover) remove(ctx context.Context, scope service.Scope, id string, notRemoved error) error {
	err := a.svc.DeleteIn(ctx, scope, id)
	if errors.Is(err, service.ErrTeardown) {
		return fmt.Errorf("%w: %s", notRemoved, service.TeardownMessage(err))
	}
	return err
}

func (a scopedServiceRemover) DeleteEnvironmentServices(ctx context.Context, id string) error {
	return a.remove(ctx, service.ScopeEnvironment, id, environment.ErrServicesNotRemoved)
}

func (a scopedServiceRemover) DeleteProjectServices(ctx context.Context, id string) error {
	return a.remove(ctx, service.ScopeProject, id, project.ErrServicesNotRemoved)
}

// workspaceContents deletes what a workspace has besides its own rows, for
// org.DeleteWorkspace, which cannot import these modules. The order leaves
// nothing behind: a server shared with a workspace that uses it refuses the
// whole delete up front; then the services, with their containers and data;
// the shared volumes they no longer mount; the servers they no longer run
// on; the projects they no longer belong to.
type workspaceContents struct {
	services *service.ServiceManager
	volumes  *sharedvolume.Service
	servers  *server.Service
	projects *project.Service
}

func (w workspaceContents) DeleteAll(ctx context.Context, orgID string) error {
	servers, err := w.servers.ListServers(ctx, orgID)
	if err != nil {
		return err
	}
	for _, srv := range servers {
		if inUse, err := w.servers.SharedInUse(ctx, srv.ID); err != nil {
			return err
		} else if inUse {
			return fmt.Errorf("%w (%s)", org.ErrWorkspaceInUse, srv.Name)
		}
	}
	if err := w.services.DeleteIn(ctx, service.ScopeOrg, orgID); err != nil {
		if errors.Is(err, service.ErrTeardown) {
			return fmt.Errorf("%w: %s", org.ErrContentsNotRemoved, service.TeardownMessage(err))
		}
		return err
	}
	volumes, err := w.volumes.List(ctx, orgID)
	if err != nil {
		return err
	}
	for _, v := range volumes {
		if err := w.volumes.Delete(ctx, orgID, v.ID); err != nil {
			return fmt.Errorf("%w: shared volume %s: %v", org.ErrContentsNotRemoved, v.Name, err)
		}
	}
	for _, srv := range servers {
		if err := w.servers.DeleteServer(ctx, srv.ID, false); err != nil {
			return fmt.Errorf("delete server %s: %w", srv.Name, err)
		}
	}
	projects, err := w.projects.ListProjects(ctx, orgID)
	if err != nil {
		return err
	}
	for _, p := range projects {
		if err := w.projects.DeleteProject(ctx, p.ID); err != nil {
			return fmt.Errorf("delete project %s: %w", p.Name, err)
		}
	}
	return nil
}

func main() {
	_ = godotenv.Load()

	cfg := config.Load()
	logger.Init(cfg.LogLevel, cfg.IsProduction())
	defer logger.Sync()

	appLog := logger.Get()
	// keys seal and open every encrypted column (D-126). The previous key is
	// set only while a rotation runs.
	keys := crypto.NewKeys(cfg.EncryptionKey, cfg.EncryptionKeyPrevious)
	if cfg.EncryptionKeyPrevious != "" {
		appLog.Warn("ENCRYPTION_KEY_PREVIOUS is set: values sealed with it still open, until a rotation finishes")
	}

	if cfg.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}

	// ── Database ─────────────────────────────────────────────────────────────
	pool, err := db.New(context.Background(), cfg)
	if err != nil {
		appLog.Fatal("failed to connect to database", zap.Error(err))
	}
	defer pool.Close()

	// `server basic-auth-plaintext` stores the basic auth passwords in plain
	// text again, for a rollback to a backend from before 0019, and exits.
	if len(os.Args) > 1 && os.Args[1] == "basic-auth-plaintext" {
		n, err := service.NewPostgresRepository(pool, keys).StoreBasicAuthPasswords(context.Background(), false)
		if err != nil {
			appLog.Fatal("basic auth passwords not stored in plain text", zap.Error(err))
		}
		appLog.Info("basic auth passwords stored in plain text", zap.Int64("services", n))
		return
	}

	// `server reseal [--check]` runs the start-up pass that seals every
	// secret under the current key, or with --check only counts what isn't,
	// and exits non-zero while a key opens any of those (toft
	// rotate-encryption-key). Values no key opens are named, not counted.
	if len(os.Args) > 1 && os.Args[1] == "reseal" {
		check := len(os.Args) > 2 && os.Args[2] == "--check"
		res, err := reseal.Run(context.Background(), sqlcdb.New(pool), keys, check)
		if err != nil {
			appLog.Fatal("reseal", zap.Error(err))
		}
		logRows(appLog, "value under another key", res.Left)
		logRows(appLog, "value no key opens", res.Lost)
		appLog.Info("reseal", zap.Int64("resealed", res.Resealed), zap.Int("left", len(res.Left)), zap.Int("lost", len(res.Lost)), zap.String("key_id", keys.CurrentID()))
		if len(res.Left) > 0 {
			logger.Sync()
			os.Exit(1)
		}
		return
	}

	// `server answer-on-ip` makes a restored panel wait for its domain again
	// and exits (toft answer-on-ip, which then restarts the backend). Its
	// one line is what the operator reads.
	if len(os.Args) > 1 && os.Args[1] == "answer-on-ip" {
		already, err := instance.AnswerOnIP(context.Background(), instance.NewPostgresRepository(pool))
		switch {
		case errors.Is(err, instance.ErrNeverRestored):
			fmt.Println("never-restored")
		case err != nil:
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		case already:
			fmt.Println("already")
		default:
			fmt.Println("ok")
		}
		return
	}

	// ── App networks (D-19, D-26, D-31; package C) ──────────────────────────
	// Under TOFT_APP_NETWORKS=isolated every environment has its own network
	// and apps never join the control plane's (TRAEFIK_NETWORK); it is the
	// default (D-51). Under legacy deploys behave as before Phase 0 and the
	// control-plane stopgap stays on.
	appNetworks, appNetworksOK := cfg.EffectiveAppNetworks()
	if !appNetworksOK {
		appLog.Warn("unknown TOFT_APP_NETWORKS value; using legacy (apps share the control plane's network, and deploys to "+
			"the control-plane server stay operator-only). Use legacy or isolated", zap.String("value", cfg.AppNetworks))
	}
	isolated := appNetworks == config.AppNetworksIsolated
	netMigration := netplan.MigrationOff
	addressPool := ""
	if isolated {
		if netMigration, err = netplan.ParseMigrationMode(cfg.AppNetworkMigration); err != nil {
			appLog.Fatal("invalid app network migration mode", zap.Error(err))
		}
		if addressPool, err = netplan.ParseAddressPool(cfg.AppNetworkPool); err != nil {
			appLog.Fatal("invalid app network address pool", zap.Error(err))
		}
		appLog.Info("app networks are isolated", zap.String("migration", string(netMigration)), zap.String("address_pool", addressPool))
	} else if cfg.AppNetworkMigration != "" {
		appLog.Info("APP_NETWORK_MIGRATION is ignored under TOFT_APP_NETWORKS=legacy")
	}
	netCfg := rollout.NetConfig{ProxyContainer: cfg.ProxyContainer, LegacyNetwork: cfg.TraefikNetwork, HostKey: "local", AppsSlice: cfg.AppsSlice}

	// ── Docker / Orchestration ────────────────────────────────────────────────
	// The data dir is shared with the host (TOFT_DATA_DIR): keep it to root,
	// since config files in it can hold secrets. A bind-mounted file stays
	// readable inside the app's container whatever its host parents allow.
	if !filepath.IsAbs(cfg.DataDir) {
		appLog.Fatal("TOFT_DATA_DIR must be an absolute path", zap.String("value", cfg.DataDir))
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		// Not fatal: only deploys to this server need it, and they fail with
		// the reason. Outside Docker, set TOFT_DATA_DIR (make does on macOS).
		appLog.Warn("cannot create the data directory; compose and config files for apps on this server will fail",
			zap.String("dir", cfg.DataDir), zap.Error(err))
	} else if err := os.Chmod(cfg.DataDir, 0o700); err != nil {
		appLog.Warn("could not restrict the data directory to its owner", zap.String("dir", cfg.DataDir), zap.Error(err))
	}
	localOrchestrator, err := docker.NewLocalOrchestrator(logger.Named("docker.local"), netCfg, cfg.DataDir)
	if err != nil {
		appLog.Fatal("failed to connect to local Docker daemon", zap.Error(err))
	}

	// ── SSH to managed servers ───────────────────────────────────────────────
	// One pooled connection per server (D-19), with each server's host key
	// pinned on first use. Every SSH user goes through serverSvc.SSH.
	sshPool := sshpool.New(sshpool.Options{Logger: logger.Named("ssh.pool")})

	// ── Repositories ──────────────────────────────────────────────────────────
	userRepo := user.NewPostgresRepository(pool)
	orgRepo := org.NewPostgresRepository(pool, keys)
	serverRepo := server.NewPostgresRepository(pool, keys)
	projectRepo := project.NewPostgresRepository(pool)
	environmentRepo := environment.NewPostgresRepository(pool)
	serviceRepo := service.NewPostgresRepository(pool, keys)
	deploymentRepo := deployment.NewPostgresRepository(pool)
	domainRepo := domain.NewPostgresRepository(pool)
	envvarRepo := envvar.NewPostgresRepository(pool, keys)
	apiKeyRepo := apikey.NewPostgresRepository(pool)
	observabilityRepo := observability.NewPostgresRepository(pool)
	configFileRepo := configfile.NewPostgresRepository(pool)
	instanceRepo := instance.NewPostgresRepository(pool)

	// First, as the check that ENCRYPTION_KEY is this database's: the basic
	// auth pass below would seal what a wrong key can't open as plain text.
	hashKey, err := instanceHashKey(context.Background(), instanceRepo, keys)
	if err != nil {
		appLog.Fatal("the instance's HMAC key", zap.Error(err))
	}

	// ── Basic auth passwords at rest (0019) ──────────────────────────────────
	// Encrypts those still in plain text: from before the upgrade, or saved by
	// an older backend after a rollback. Reads take both, so a failure only
	// leaves them in plain text until the next start.
	if n, err := serviceRepo.StoreBasicAuthPasswords(context.Background(), true); err != nil {
		appLog.Error("basic auth passwords not encrypted; retried at the next start", zap.Error(err))
	} else if n > 0 {
		appLog.Info("basic auth passwords encrypted", zap.Int64("services", n))
	}

	// ── SSH keys at rest ─────────────────────────────────────────────────────
	// Encrypts the servers' private keys still in plain text from before
	// encryption was added. Reads take both, as every earlier release's do.
	if n, err := serverRepo.EncryptLegacySSHKeys(context.Background()); err != nil {
		appLog.Error("SSH keys not encrypted; retried at the next start", zap.Error(err))
	} else if n > 0 {
		appLog.Info("SSH keys encrypted", zap.Int64("servers", n))
	}

	// ── Invite links at rest ─────────────────────────────────────────────────
	// Invites from before 0054 hold the link itself; lookups now use its
	// sha256, so they stop working until this converts them.
	if n, err := orgRepo.HashUnhashedInvites(context.Background()); err != nil {
		appLog.Error("invite links not hashed; retried at the next start", zap.Error(err))
	} else if n > 0 {
		appLog.Info("invite links hashed", zap.Int64("invites", n))
	}

	// ── Secrets under the current key (D-126) ──────────────────────────────
	// After the passes above, which seal what is still in plain text: every
	// value in the format before v0.28.0, or under ENCRYPTION_KEY_PREVIOUS,
	// is sealed again before anything reads one. A failure is fatal: Open
	// reads neither, and toft upgrade rolls a failed start back to a release
	// that does.
	if res, err := reseal.Run(context.Background(), sqlcdb.New(pool), keys, false); err != nil {
		appLog.Fatal("secrets not sealed under the current key", zap.Error(err))
	} else {
		if res.Resealed > 0 {
			appLog.Info("secrets sealed under the current key", zap.Int64("values", res.Resealed))
		}
		logRows(appLog, "value no key opens", res.Lost)
	}

	authRepo := auth.NewPostgresRepository(pool)
	deployPolicyRepo := deploypolicy.NewPostgresRepository(pool)

	// ── Services ──────────────────────────────────────────────────────────────
	// Instance operators and registration (D-21). instanceSvc satisfies the
	// small operator/registration interfaces declared by user, org and auth.
	instanceSvc := instance.NewService(instanceRepo, logger.Named("instance")).WithSetupToken(cfg.SetupToken).
		WithSetupTokenExpiry(cfg.SetupTokenExpires)
	userSvc := user.NewService(userRepo, logger.Named("user")).WithOperatorChecker(instanceSvc)
	instanceSvc.WithPasswordVerifier(userSvc)
	orgSvc := org.NewService(orgRepo, logger.Named("org")).WithOperatorChecker(instanceSvc)
	serverSvc := server.NewService(serverRepo, cfg.ACMEEmail)
	// toft upgrade links the panel host's /var/lib/toft/backup here (D-43).
	serverSvc.WithPanelBackupDir(filepath.Join(cfg.DataDir, "backup"))
	serverSvc.WithPanelRestore(instanceSvc) // paused since the restore (D-163)
	serverSvc.WithAllocReader(serviceRepo)
	serverSvc.WithOrgRoles(orgSvc) // sharing a server needs owning both workspaces
	serverSvc.WithSSH(sshPool, logger.Named("server"))
	// Servers' agents (D-106): each dials the panel and the pool runs SSH
	// inside its WebSocket.
	agentHub := agenthub.New(serverSvc, logger.Named("agent.hub"))
	serverSvc.WithAgents(server.NewPostgresAgentStore(pool, keys), agentHub, orgSvc, cfg.AgentDir)
	serverSvc.WithDesired(serviceRepo)
	// The capability probe's store (D-93); serverRepo is the Postgres one.
	if caps, ok := serverRepo.(server.CapabilityStore); ok {
		serverSvc.WithCapabilities(caps)
	}
	// Hosts' updates (D-133), judged against Docker's advisories: the list
	// shipped with the release and those GitHub published since.
	dockerAdvisories := advisory.New(advisory.FeedURL, logger.Named("advisory"))
	if updates, ok := serverRepo.(server.UpdateStore); ok {
		serverSvc.WithUpdates(updates, dockerAdvisories)
	}
	instanceSvc.WithAdvisories(dockerAdvisories)
	// The gVisor installs' records (D-95), in the same repository.
	if runs, ok := serverRepo.(server.GvisorRunStore); ok {
		serverSvc.WithGvisorRuns(runs)
	}
	// The provisioner writes the app address pool on fresh servers and
	// attaches the proxy to app networks only under isolated app networks.
	serverSvc.WithAppNetworks(isolated, addressPool)
	orgSvc.WithServerSandboxes(serverSvc)        // where Sandboxed can run (D-95)
	serverSvc.WithPanelCapabilities(instanceSvc) // the panel's own server too (D-97)
	orchestratorFactory := docker.NewFactory(localOrchestrator, netCfg, serverSvc, logger.Named("docker.factory"))
	instanceSvc.WithPanelHost(panelHost{local: localOrchestrator, servers: serverSvc, domain: instanceSvc.PanelDomain,
		network: cfg.TraefikNetwork, slice: cfg.AppsSlice})
	// The panel's own containers and server (D-68): the host's /proc, which
	// a container shares, and / inside the backend, which is on Docker's
	// filesystem.
	instanceSvc.WithPanelMonitor(panelProbe{local: localOrchestrator, proxy: netCfg.ProxyContainer}, instanceRepo, "/proc", "/").
		WithPanelShell(cfg.PanelHostShell).WithGit(cfg.Git).
		// toft's upgrade files, mounted read-only (TOFT_UPGRADE_DIR).
		WithUpgrades("/var/lib/toft/upgrade")
	// The panel's own routes (D-62), written now so Traefik matches the
	// stored domain before the API serves. Outside docker compose (no
	// routes directory) nothing routes the panel, so nothing is written.
	var routes instance.PanelRoutesWriter
	if _, err := os.Stat(panelRoutesDir); err == nil {
		routes = panelRoutesFile{dir: panelRoutesDir}
	} else {
		appLog.Info("panel routes not managed: no " + panelRoutesDir)
	}
	instanceSvc.WithPanelRoutes(routes, cfg.BaseDomain, cfg.TraefikDashboard).
		WithSetup(domainProber{}, cfg.SetupApp).
		WithRecoveryKit(cfg.EncryptionKey, buildinfo.Version, panelKitBackup{serverSvc})
	if err := instanceSvc.SyncPanelRoutes(context.Background()); err != nil {
		appLog.Warn("panel routes not written; Traefik keeps the last ones", zap.Error(err))
	}
	projectSvc := project.NewService(projectRepo)
	environmentSvc := environment.NewService(environmentRepo)
	environmentSvc.WithIsolatedNetworks(appNetworks == config.AppNetworksIsolated)
	projectSvc.WithEnvCreator(&projectEnvCreator{envSvc: environmentSvc})
	serviceSvc := service.NewServiceManager(serviceRepo, logger.Named("service"))
	domainSvc := domain.NewService(domainRepo, serviceSvc, logger.Named("domain"))
	domainChecker := domain.NewChecker(domainRepo, serviceSvc, domainServers{servers: serverSvc, domain: instanceSvc.PanelDomain, panelIP: cfg.PanelIP},
		domain.NetProber{}, logger.Named("domain.check"))
	envvarSvc := envvar.NewService(envvarRepo, logger.Named("envvar"))
	// A project with its first service, in one transaction (7g).
	projectSvc.WithTransactor(dbtx.NewRunner(pool))
	serviceSvc.WithProjects(projectSvc)
	serviceSvc.WithVariables(envvarSvc)
	serviceSvc.WithTransactor(dbtx.NewRunner(pool))
	// A catalog app's admin password, kept for its creator (D-160).
	revealRepo := reveal.NewPostgresRepository(pool, keys)
	serviceSvc.WithReveals(revealRepo)
	envvarSvc.WithReveals(revealRepo)
	authSvc := auth.NewService(userRepo, authRepo, instanceSvc, logger.Named("auth"), cfg.JWTSecret)
	apiKeySvc := apikey.NewService(apiKeyRepo, logger.Named("apikey"))
	observabilitySvc := observability.NewService(observabilityRepo, serviceSvc, logger.Named("observability"))
	configFileSvc := configfile.NewService(configFileRepo, logger.Named("configfile"))
	configFileSvc.WithSSH(serverSvc)
	configFileSvc.WithDataDir(cfg.DataDir)

	serviceSvc.WithOrchestration(serverSvc, &serviceOrchestratorFactory{inner: orchestratorFactory})
	serviceSvc.WithConfigFileSeeder(configFileSvc)
	serviceSvc.WithEnvVarReader(envvarSvc) // network preflight; compose files' own memory limits at save
	if isolated && netMigration != netplan.MigrationOff {
		serviceSvc.WithNetworkCollection() // deletes remove the app networks left unused
	}
	serverSvc.WithServiceForgetter(serviceSvc)
	projectSvc.WithServiceRemover(scopedServiceRemover{svc: serviceSvc})
	environmentSvc.WithServiceRemover(scopedServiceRemover{svc: serviceSvc})

	// ── Git hosting (D-71) ────────────────────────────────────────────────────
	// git runs in the paas-git container, reached on its socket; an install
	// without git (TOFT_GIT=off) has neither, nor the SSH listener.
	// Import and mirrors (D-79) reach other hosts only through the egress,
	// a proxy on a socket beside the executor's that dials public addresses
	// only; paas-git itself has no network.
	var gitClient *gitstore.Client
	var egress *gitstore.Egress
	if cfg.Git {
		gitClient = gitstore.NewClient(gitstore.Socket)
		egress = gitstore.NewEgress(netcheck.NewPublicOnlyClient(0, false).Transport)
	}
	codeSvc := code.NewService(code.NewPostgresRepository(pool), gitClient, userSvc, instanceSvc.PanelDomain, cfg.GitSSHPort, logger.Named("code")).
		WithEgress(egress, keys)
	projectSvc.WithRepositories(codeSvc)
	var gitSSH *code.SSHServer
	if cfg.Git {
		if err := codeSvc.EnsureRepositories(context.Background()); err != nil {
			appLog.Error("projects without a repository get one when it is first read", zap.Error(err))
		}
		if hostKey, err := gitHostKey(context.Background(), instanceRepo, keys); err != nil {
			appLog.Error("git over SSH is off: no host key", zap.Error(err))
		} else {
			codeSvc.WithHostKey(ssh.FingerprintSHA256(hostKey.PublicKey()))
			gitSSH = code.NewSSHServer(codeSvc, hostKey, logger.Named("code.ssh"))
		}
	}

	// Deploy policy (D-19): checked when a service's config is saved and on
	// every deploy. Exceptions and the violations report are operator-only.
	policyMode, policyModeOK := deploypolicy.ParseMode(cfg.DeployPolicy)
	if !policyModeOK {
		appLog.Warn("unknown TOFT_DEPLOY_POLICY value; enforcing the deploy policy", zap.String("value", cfg.DeployPolicy))
	}
	if policyMode == deploypolicy.ModeWarn {
		appLog.Warn("deploy policy is in WARN mode (TOFT_DEPLOY_POLICY=warn): violations are logged and deploys go ahead. " +
			"Use it only to migrate existing services; review Settings → Deploy policy report (GET /api/v1/deploy-policy/violations)")
	}
	deployPolicySvc := deploypolicy.NewService(deployPolicyRepo, policyMode, logger.Named("deploypolicy")).
		WithOperatorChecker(instanceSvc).
		WithOwnershipChecker(serviceSvc).
		WithEnvProvider(envvarSvc).
		WithAppNetworks(composespec.AppNetworkPolicy{Isolated: isolated, Legacy: legacyNetworkNames(cfg.TraefikNetwork)}).
		WithHashKey(hashKey)
	serviceSvc.WithPolicyChecker(deployPolicySvc)
	// Host shells (service exec on @server, server exec, PTY) are operator-only.
	serviceSvc.WithOperatorChecker(instanceSvc)
	serverSvc.WithOperatorChecker(instanceSvc)

	eventBroker := broker.New()

	deploymentSvc := deployment.NewService(deploymentRepo, orchestratorFactory, serviceSvc, logger.Named("deployment"))
	deploymentSvc.WithBuildSlots(cfg.MaxConcurrentBuilds)
	// Shared volumes (D-38): a service that mounts one cannot move server,
	// and its deploys mount them.
	sharedVolumeSvc := sharedvolume.NewService(sharedvolume.NewPostgresRepository(pool), serviceSvc, serverSvc,
		&sharedVolumeHosts{servers: serverSvc, factory: orchestratorFactory}, logger.Named("sharedvolume"))
	serviceSvc.WithSharedMounts(sharedVolumeSvc)
	orgSvc.WithWorkspaceContents(workspaceContents{services: serviceSvc, volumes: sharedVolumeSvc, servers: serverSvc, projects: projectSvc})
	deploymentSvc.WithSharedMounts(sharedVolumeSvc)
	deploymentSvc.WithServiceMover(serviceSvc)
	deploymentSvc.WithDomainActivator(domainSvc)
	deploymentSvc.WithObservabilityRegistrar(observabilitySvc)
	deploymentSvc.WithEventPublisher(&deploymentEventPublisher{b: eventBroker})
	alertRepo := alert.NewPostgresRepository(pool)
	alertSvc := alert.NewService(alertRepo, keys, logger.Named("alert")).WithPublisher(eventBroker)
	instanceSvc.WithAlerts(alertSvc)
	deploymentSvc.WithAlerts(alertSvc)
	deploymentSvc.WithDNSChecker(serverSvc)
	deploymentSvc.WithSandboxChecker(serverSvc) // Sandboxed services need gVisor there (D-95)
	deploymentSvc.WithConfigFileWriter(&configFileWriterAdapter{svc: configFileSvc})
	deploymentSvc.WithPolicyChecker(deployPolicySvc)
	// Memory limits (D-41): a new service gets its kind's default, saves and
	// deploys need one (after the grace period), compose runs with the
	// panel's.
	resourcesSvc := resources.NewService(resources.NewPostgresRepository(pool), logger.Named("resources"))
	resourcesSvc.WithLookups(serviceSvc, serverSvc)
	serviceSvc.WithResources(resourcesSvc)
	serviceSvc.WithSandboxes(serverSvc) // new tools and panel-server apps start Sandboxed (D-99)
	deploymentSvc.WithResources(resourcesSvc)
	// Under isolated app networks every deploy computes its network plan;
	// under legacy it joins TRAEFIK_NETWORK with its alias, as before.
	deploymentSvc.WithAppNetworks(isolated, cfg.TraefikNetwork, serviceSvc)
	// D-19 stopgap: while apps on the control-plane server share its network
	// (legacy), interactive deploys there are operator-only and git-push
	// (webhook) deploys there are refused, unless an operator granted the
	// service the control_plane exception. The policy view reports it per
	// service. Isolated app networks lift it.
	controlPlaneStopgap := !isolated
	deploymentSvc.WithControlPlaneStopgap(controlPlaneStopgap, instanceSvc)
	deploymentSvc.WithPanelRole(instanceSvc)
	serviceSvc.WithPanelRole(instanceSvc) // and new services there are refused at create
	// Server updates' runs (D-134): a Docker update or a reboot waits for
	// the host's deploys, and its deploys wait for it.
	if runs, ok := serverRepo.(server.UpdateRunStore); ok {
		serverSvc.WithUpdateRuns(runs, deploymentSvc, orgSvc)
	}
	instanceSvc.WithDeploys(deploymentSvc)
	deploymentSvc.WithHostUpdates(hostUpdates{servers: serverSvc, panel: instanceSvc})
	// The disk advisor (D-135): deploys wait for a host's clean, and each
	// build trims its host's build cache to the limit.
	diskSvc := disk.NewService(disk.NewPostgresRepository(pool), diskHosts{factory: orchestratorFactory, servers: serverSvc},
		diskDeploys{deploymentSvc}, diskAudit{orgs: orgSvc, instance: instanceSvc}, diskPeople{instance: instanceSvc, users: userSvc},
		instanceSvc.PanelReleases, logger.Named("disk"))
	deploymentSvc.WithDiskCleans(diskSvc)
	deploymentSvc.WithBuildCacheTrim(diskSvc)
	diskHandler := disk.NewHandler(diskSvc, logger.Named("disk.handler"))
	deployPolicySvc.WithControlPlaneStopgap(controlPlaneStopgap)

	regClient := registry.NewClient()
	scmClient := scm.NewClient()
	versionSvc := version.NewService(serviceSvc, regClient, scmClient, logger.Named("version"))

	// ── Handlers ──────────────────────────────────────────────────────────────
	authHandler := auth.NewHandler(authSvc, logger.Named("auth.handler"))
	userHandler := user.NewHandler(userSvc, logger.Named("user.handler")).WithTokenIssuer(authSvc)
	instanceHandler := instance.NewHandler(instanceSvc, logger.Named("instance.handler")).WithAllowedOrigins(allowedOrigins(cfg, instanceSvc.PanelDomain))
	orgHandler := org.NewHandler(orgSvc, logger.Named("org.handler"))
	serverHandler := server.NewHandler(serverSvc, orgSvc, logger.Named("server.handler")).WithAllowedOrigins(allowedOrigins(cfg, instanceSvc.PanelDomain)).
		WithAgentOrigin(agentOrigin(instanceSvc.PanelDomain, net.JoinHostPort(cfg.ProxyContainer, "443")))
	projectHandler := project.NewHandler(projectSvc, orgSvc, logger.Named("project.handler"))
	environmentHandler := environment.NewHandler(environmentSvc, projectSvc, logger.Named("environment.handler")).WithAudit(orgSvc)
	serviceHandler := service.NewHandler(serviceSvc, projectSvc, environmentSvc, orgSvc, versionSvc, logger.Named("service.handler"))
	serviceHandler.WithServerChecker(serverSvc)
	projectHandler.WithInlineService(serviceHandler)
	domainHandler := domain.NewHandler(domainSvc, domainChecker, serviceSvc, logger.Named("domain.handler")).WithAudit(orgSvc)
	revealSvc := reveal.NewService(revealRepo, serviceSvc, orgSvc, logger.Named("reveal"))
	revealSvc.WithLogins(serviceSvc)
	revealHandler := reveal.NewHandler(revealSvc, logger.Named("reveal.handler"))
	envvarHandler := envvar.NewHandler(envvarSvc, serviceSvc, environmentSvc, orgSvc, logger.Named("envvar.handler"))
	observabilityHandler := observability.NewHandler(observabilitySvc, projectSvc, logger.Named("observability.handler")).WithAudit(orgSvc)
	sharedVolumeHandler := sharedvolume.NewHandler(sharedVolumeSvc, logger.Named("sharedvolume.handler")).WithAudit(orgSvc)
	resourcesHandler := resources.NewHandler(resourcesSvc, orgSvc, logger.Named("resources.handler"))
	versionHandler := version.NewHandler(versionSvc, logger.Named("version.handler"))
	eventsHandler := events.NewHandler(eventBroker, logger.Named("events.handler"))
	deploymentHandler := deployment.NewHandler(deploymentSvc, serviceSvc, logger.Named("deployment.handler"))
	deploymentSvc.WithDeployUseCase(serviceSvc, serverSvc, domainSvc, envvarSvc, envvarSvc, environmentSvc, orgSvc)
	deploymentSvc.WithObservabilityQuerier(observabilitySvc)
	deploymentSvc.WithLoopbackPorts(serviceRepo)
	deploymentSvc.WithVersionCache(versionSvc)
	deploymentSvc.WithUserNames(userNames{userSvc})
	serverSvc.WithAdopter(deploymentSvc)
	deploymentSvc.WithOrgSettingsSvc(orgSvc)
	deploymentSvc.WithCapacitySvc(serverSvc)
	deploymentSvc.WithServiceReader(serviceSvc)
	deploymentSvc.WithEnvVarSwapper(envvarSvc)
	serviceSvc.WithCapacityGate(deploymentSvc) // Create and deploy is refused, not created, when it won't fit
	serviceHandler.WithFirstDeploys(deploymentHandler)
	deploymentSvc.WithHashKey(hashKey)
	deploymentSvc.WithServerReader(serverSvc)
	webhookHandler := webhook.NewHandler(serviceSvc, deploymentSvc, serverSvc, domainSvc, envvarSvc, environmentSvc, projectSvc,
		webhook.NewSecrets(webhook.NewPostgresRepository(pool), keys), logger.Named("webhook.handler"))
	webhookHandler.WithAudit(orgSvc)
	// A temporary name's first certificate redeploys its service (D-150).
	domainSvc.WithRedeployer(webhookHandler)
	domainChecker.WithRedeployer(webhookHandler)
	if cfg.Git {
		// Deploy on push from the project's own repositories (D-77): builds
		// read them through git archive, and the events outbox's pushes and
		// merges deploy through the webhook's loop.
		deploymentSvc.WithRepositories(codeSvc)
		codeSvc.WithPushConsumer(webhookHandler)
	}
	apiKeyHandler := apikey.NewHandler(apiKeySvc, projectSvc, logger.Named("apikey.handler")).WithAudit(orgSvc)
	dashboardHandler := dashboard.NewHandler(dashboard.NewPostgresRepository(pool), logger.Named("dashboard.handler"))
	catalogHandler := catalog.NewHandler()
	configFileHandler := configfile.NewHandler(configFileSvc, serviceSvc, logger.Named("configfile.handler")).WithAudit(orgSvc)
	deployPolicyHandler := deploypolicy.NewHandler(deployPolicySvc, logger.Named("deploypolicy.handler"))
	alertHandler := alert.NewHandler(alertSvc, orgSvc, logger.Named("alert.handler"))
	codeHandler := code.NewHandler(codeSvc, projectSvc, orgSvc, logger.Named("code.handler"))
	// Issues (D-81): linked and moved by the events outbox's pushes, pull
	// requests and merges, shipped by the deploys that go live.
	codeSvc.WithPublisher(eventBroker)
	issueSvc := issue.NewService(issue.NewPostgresRepository(pool), codeSvc, logger.Named("issue")).WithPublisher(eventBroker)
	codeSvc.WithIssues(issueSvc)
	deploymentSvc.WithShipper(issueSvc)
	issueHandler := issue.NewHandler(issueSvc, projectSvc, logger.Named("issue.handler"))
	// Docs (D-86): pages connected to the project's services, issues and
	// pull requests; a page's commands run through the service's exec.
	docSvc := doc.NewService(doc.NewPostgresRepository(pool), issueSvc, codeSvc, serviceSvc, logger.Named("doc"))
	docSvc.WithPublisher(eventBroker)
	codeSvc.WithDocs(docSvc)
	issueSvc.WithDocs(docSvc)
	docHandler := doc.NewHandler(docSvc, projectSvc, orgSvc, logger.Named("doc.handler"))
	// The inbox (D-88): Needs you computed when read, updates written by a
	// notifier from the issues', pull requests' and docs' records.
	inboxSvc := inbox.NewService(inbox.NewPostgresRepository(pool), docSvc, instanceSvc, eventBroker, logger.Named("inbox"))
	issueSvc.WithMentions(inboxSvc)
	inboxHandler := inbox.NewHandler(inboxSvc, logger.Named("inbox.handler"))
	// Previews (D-97): a pull request's preview environment, moved by the
	// events outbox; a deploy that doesn't fit stops the oldest first.
	previewSvc := preview.NewService(preview.NewPostgresStore(pool), environmentSvc, previewServices{serviceSvc},
		previewDeployer{webhookHandler}, previewCode{codeSvc}, previewVars{envvarSvc},
		previewHosts{servers: serverSvc, domain: instanceSvc.PanelDomain, panelIP: cfg.PanelIP}, orgSvc, lookupIPv4,
		appNetworks == config.AppNetworksIsolated, logger.Named("preview"))
	environmentSvc.WithPreviews(previewRemovals{previewSvc})
	serviceSvc.WithAddresser(serviceAddresses{hosts: previewHosts{servers: serverSvc, domain: instanceSvc.PanelDomain, panelIP: cfg.PanelIP}, envs: environmentSvc, projects: projectSvc, previews: previewSvc, domains: domainSvc})
	codeSvc.WithPreviews(previewRemovals{previewSvc})
	deploymentSvc.WithPreviews(previewEvictor{previewSvc}, panelLedger{local: localOrchestrator, services: serviceRepo}, serviceSvc)
	inboxSvc.WithPreviews(previewApprovals{previewSvc})
	previewHandler := preview.NewHandler(previewSvc, projectSvc, environmentSvc, logger.Named("preview.handler"))
	gitHTTP := code.NewGitHTTP(codeSvc, apiKeySvc, logger.Named("code.http"))

	// ── Agents (D-100) ────────────────────────────────────────────────────────
	agentSvc := agent.NewService(agent.NewPostgresRepository(pool), orgSvc, instanceSvc, orgSvc, logger.Named("agent"))
	agentSvc.WithKeys(keys, hashKey)
	envvarSvc.WithAgentGate(agentSvc)
	envvarSvc.WithPreviewCopies(serviceSvc)
	issueSvc.WithAgentGate(agentSvc)
	codeSvc.WithAgents(agentSvc, orgSvc)
	docSvc.WithAgentGate(agentSvc)
	previewSvc.WithAgentGate(agentSvc)
	deploymentSvc.WithAgentGate(agentSvc)
	inboxSvc.WithAgentApprovals(agentSvc)
	agentHandler := agent.NewHandler(agentSvc, instanceSvc.PanelDomain, userNames{userSvc}, logger.Named("agent.handler"))
	toolSpec, err := mcp.LoadSpec(api.Spec)
	if err != nil {
		appLog.Fatal("the MCP tools don't match the API spec", zap.Error(err))
	}
	// The MCP server and Approve run operations through the router; it is
	// built just below, so they hold it through engine.
	var engine http.Handler
	viaRouter := mcp.Router{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { engine.ServeHTTP(w, r) })}
	agentSvc.WithDispatcher(viaRouter)
	toolActions := make(map[string]string, len(toolSpec.Tools))
	for _, t := range toolSpec.Tools {
		toolActions[t.Name] = t.Policy
	}
	agentSvc.WithToolActions(toolActions)
	mcpServer := mcp.NewServer(toolSpec, viaRouter, agentSvc, orgSvc, buildinfo.Version, logger.Named("mcp"))

	// ── Router ────────────────────────────────────────────────────────────────
	r := newRouter(cfg, appLog, handlers{
		auth: authHandler, user: userHandler, instance: instanceHandler, org: orgHandler, server: serverHandler,
		project: projectHandler, environment: environmentHandler, service: serviceHandler, domain: domainHandler,
		envvar: envvarHandler, reveal: revealHandler, observability: observabilityHandler, sharedVolume: sharedVolumeHandler,
		resources: resourcesHandler, version: versionHandler, events: eventsHandler, deployment: deploymentHandler,
		webhook: webhookHandler, apiKey: apiKeyHandler, dashboard: dashboardHandler, catalog: catalogHandler,
		configFile: configFileHandler, deployPolicy: deployPolicyHandler, alert: alertHandler,
		code: codeHandler, gitHTTP: gitHTTP, issue: issueHandler, doc: docHandler, inbox: inboxHandler,
		preview: previewHandler, agent: agentHandler, agentHub: agentHub, mcp: mcpServer, disk: diskHandler,
	}, routerDeps{apiKeys: credentials{apiKeySvc, agentSvc}, orgs: orgSvc, users: userSvc, services: serviceSvc, environments: environmentSvc, origins: allowedOrigins(cfg, instanceSvc.PanelDomain), db: pool.Ping})

	engine = r

	// ── Graceful shutdown ─────────────────────────────────────────────────────
	// Every request's context, ended once requests in flight have had their
	// moment at shutdown (below).
	baseCtx, endRequests := context.WithCancelCause(context.Background())
	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      r,
		BaseContext:  func(net.Listener) context.Context { return baseCtx },
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// ── Deploys interrupted by the last restart (D-19) ───────────────────────
	// Phase 1, before the API serves requests: deployments a restart left
	// pending or building are marked failed ("interrupted: ..."), so nothing
	// shows them as live. Phase 2, in the background: the newest one of each
	// service has its containers checked, and the service's runtime status
	// is set to what runs.
	markCtx, cancelMark := context.WithTimeout(context.Background(), 30*time.Second)
	interrupted, err := deploymentSvc.MarkInterrupted(markCtx)
	cancelMark()
	if err != nil {
		appLog.Error("could not mark deployments interrupted by the last restart", zap.Error(err))
	} else if len(interrupted) > 0 {
		appLog.Warn("deployments interrupted by the last restart were marked failed; checking their services",
			zap.Int("count", len(interrupted)))
	}
	recoverCtx, cancelRecover := context.WithCancel(context.Background())
	// Provisioning runs in this process, so no run survives a restart (D-48).
	if n, err := serverSvc.CloseUnfinishedProvisionRuns(recoverCtx); err != nil {
		appLog.Warn("close unfinished provision runs", zap.Error(err))
	} else if n > 0 {
		appLog.Info("closed provision runs the restart cut short", zap.Int64("runs", n))
	}
	if n, err := serverSvc.CloseUnfinishedGvisorRuns(recoverCtx); err != nil {
		appLog.Warn("close unfinished gvisor installs", zap.Error(err))
	} else if n > 0 {
		appLog.Info("closed gvisor installs the restart cut short", zap.Int64("runs", n))
	}
	if err := instanceSvc.CloseUnfinishedPanelGvisorRun(recoverCtx); err != nil {
		appLog.Warn("close an unfinished gvisor install on the panel's server", zap.Error(err))
	}
	// Deploys a server's agent was running are followed to their end, and
	// the agents are kept up to date with what should run (D-111). Their
	// services' turns are taken here, before the API and the workers below
	// can ask for a deploy.
	followHandedOff := deploymentSvc.FollowHandedOff(recoverCtx)
	go followHandedOff()
	go serverSvc.SyncDesired(recoverCtx)
	go func() {
		deploymentSvc.ReconcileAfterRestart(recoverCtx, interrupted)
		// App networks after recovery (isolated only): the control-plane
		// server first, then each remote server, best effort.
		if isolated && netMigration != netplan.MigrationOff {
			reconcileAppNetworks(recoverCtx, serviceSvc, serverSvc, netMigration, logger.Named("netplan"))
		}
	}()

	// ── Proxy on every app network (isolated only) ──────────────────────────
	// docker compose up can recreate paas-traefik, which drops the app
	// networks it was connected to; re-attach on its start and every minute.
	proxyCtx, cancelProxy := context.WithCancel(context.Background())
	if isolated {
		go localOrchestrator.WatchProxy(proxyCtx)
	}

	// ── Memory limit grace dates (D-41) ─────────────────────────────────────
	// Before the API serves deploys: once per instance, existing services
	// without a memory limit get 7 days before deploys need one. A fresh local
	// stack starts the backend before `make migrate`, so a first attempt
	// that fails is retried in the background until it succeeds; meanwhile
	// services without a grace date get the provisional one (they warn).
	graceCtx, cancelGrace := context.WithCancel(context.Background())
	if err := resourcesSvc.EnsureGrace(graceCtx); err != nil {
		appLog.Error("memory limit grace dates not set yet; retrying every 15s. Until they are, "+
			"deploys without a memory limit warn and new compose services get no default", zap.Error(err))
		go resourcesSvc.RetryGrace(graceCtx, 15*time.Second)
	}

	go func() {
		appLog.Info("server starting", zap.String("addr", srv.Addr), zap.String("version", buildinfo.Version))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			appLog.Fatal("server error", zap.Error(err))
		}
	}()
	// git over SSH (D-28, D-71): the container's port 2222, which compose
	// publishes on TOFT_GIT_SSH_PORT.
	if gitSSH != nil {
		go func() {
			ln, err := net.Listen("tcp", ":2222")
			if err != nil {
				appLog.Error("git over SSH is off", zap.Error(err))
				return
			}
			appLog.Info("git ssh starting", zap.String("addr", ":2222"))
			if err := gitSSH.Serve(ln); err != nil {
				appLog.Error("git over SSH stopped", zap.Error(err))
			}
		}()
	}

	// ── Instance operator bootstrap ──────────────────────────────────────────
	// Break-glass only: while there is no active operator, grants
	// TOFT_OPERATOR_EMAILS (or, when unset and no operator exists, promotes the
	// earliest user). Retried because a fresh local stack's backend starts
	// before `make migrate` creates the instance tables: every 15s for up
	// to 10 minutes.
	bootstrapCtx, cancelBootstrap := context.WithCancel(context.Background())
	go instanceSvc.RunBootstrap(bootstrapCtx, cfg.OperatorEmails, 15*time.Second, 40)

	// ── Audit log retention (TOFT_AUDIT_RETENTION_DAYS) and pool stats ──────
	gcCtx, cancelGC := context.WithCancel(context.Background())
	go orgSvc.RunAuditRetention(gcCtx, time.Duration(cfg.AuditRetentionDays)*24*time.Hour)
	go db.LogPoolStats(gcCtx, pool, logger.Named("db.pool"), time.Minute)
	// The events outbox's dispatcher (D-77), and the mirrors' and webhooks'
	// workers (D-79): each returns at once without git.
	go codeSvc.RunEvents(gcCtx)
	go previewSvc.Run(gcCtx)
	go domainSvc.RunCertificateChecks(gcCtx) // D-150: certificate pending until one answers
	go agentSvc.RunCleaner(gcCtx)
	go revealSvc.RunCleaner(gcCtx) // D-160: deleted after 7 days
	go codeSvc.RunMirrors(gcCtx)
	go codeSvc.RunWebhooks(gcCtx)
	// What a restart lost of the deploys that ship issues (D-81).
	go issueSvc.Run(gcCtx)
	go serverSvc.RunCapabilityProbes(gcCtx)
	go serverSvc.RunUpdateReads(gcCtx)
	go dockerAdvisories.Run(gcCtx)
	go docSvc.RunPurge(gcCtx)
	go inboxSvc.RunNotifier(gcCtx)
	if egress != nil {
		go func() {
			if err := egress.Serve(gcCtx, gitstore.EgressSocket); err != nil {
				appLog.Error("import and mirrors can't reach other hosts", zap.Error(err))
			}
		}()
	}
	// The panel's own server, every 5 seconds; a sample a minute kept 7 days.
	go instanceSvc.RunPanelMonitor(gcCtx)
	// What it can run, through the host helper: at start, then daily (D-97).
	go instanceSvc.RunPanelProbes(gcCtx)
	// Its updates, the same way: at start, then every 6 hours (D-133).
	go instanceSvc.RunPanelUpdateReads(gcCtx)

	// Servers Add a server made that nobody joined go 24 h after their last
	// command expired (D-153), checked every hour.
	go serverSvc.RunUnjoinedSweep(gcCtx)

	// ── Deployment log GC ────────────────────────────────────────────────────
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		gcLog := logger.Named("deployment.gc")
		for {
			select {
			case <-gcCtx.Done():
				return
			case <-ticker.C:
				gcLog.Info("running deployment GC")
				deploymentSvc.RunGC(gcCtx, 200)
			}
		}
	}()

	// ── Metrics collector ────────────────────────────────────────────────────
	// serviceSvc is the metrics.Source: every 60s it lists the running
	// services and samples each server once (one command over its pooled
	// SSH connection, or the local daemon), 8 servers at a time with a 20s
	// budget each, so a dead server costs one warning, not the whole cycle.
	// resourcesSvc stores memory kills and servers' CPU waiting, and folds the
	// samples into daily rows at start and every hour (D-41); each new kill
	// also goes to alertSvc.
	// ── Alerts: one goroutine samples, one delivers; both stop at shutdown ──
	alertCtx, cancelAlerts := context.WithCancel(context.Background())
	go alertSvc.RunSender(alertCtx)
	monitor := alert.NewMonitor(serviceSvc, alertServerLister{serverSvc}, alertRepo, alertSvc, logger.Named("alert.monitor")).
		WithVolumes(volumeMeter{sharedVolumeSvc}).WithBackups(serverSvc).WithPanel(instanceSvc).WithMirrors(codeSvc).WithAgents(serverSvc).
		WithUpdates(serverSvc, instanceSvc).WithDisk(diskSvc).WithAnnouncer(alertSvc)
	diskSvc.WithAlerts(monitor)
	serviceHandler.WithHealthAnswers(monitor)
	go monitor.Run(alertCtx)
	go diskSvc.RunAutomatic(alertCtx)

	metricsCtx, cancelMetrics := context.WithCancel(context.Background())
	go metrics.RunCollector(metricsCtx, serviceSvc, alertSvc, resourcesSvc, logger.Named("metrics.collector"))

	// ── Server health polling ─────────────────────────────────────────────────
	pollCtx, cancelPoll := context.WithCancel(context.Background())
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		pollLog := logger.Named("health.poller")
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
				servers, err := serverSvc.ListAllServers(pollCtx)
				if err != nil {
					pollLog.Warn("health poll: failed to list servers", zap.Error(err))
					continue
				}
				for _, s := range servers {
					go func(srv server.Server) {
						if _, err := serverSvc.TestConnection(pollCtx, srv.ID); err != nil {
							pollLog.Warn("health poll: server unreachable",
								zap.String("server_id", srv.ID),
								zap.String("name", srv.Name),
								zap.Error(err),
							)
							return
						}
						// A Traefik from before 7f gets the HTTP router order
						// temporary addresses need, without a Re-provision (D-150).
						// Detached from shutdown: a Traefik restart stops halfway otherwise.
						tctx, cancel := context.WithTimeout(context.WithoutCancel(pollCtx), 5*time.Minute)
						defer cancel()
						if err := serverSvc.KeepTraefikCurrent(tctx, srv.ID); err != nil {
							pollLog.Warn("health poll: Traefik", zap.String("server_id", srv.ID), zap.Error(err))
						}
					}(s)
				}
			}
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	appLog.Info("shutting down...")
	// A deploy that fails from now on may be failing because of the
	// shutdown: it is left for the next start's recovery.
	deploymentSvc.BeginShutdown()
	cancelRecover()
	cancelProxy()
	cancelBootstrap()
	cancelGrace()
	cancelGC()
	cancelMetrics()
	cancelAlerts()
	cancelPoll()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Requests in flight get a moment to finish. Streams (SSE) and shells
	// never do, and Shutdown doesn't end a shell at all (its connection is
	// hijacked), so then every request is ended: a server's shell writes its
	// closing row, and the panel's shell is left to what runs in it
	// (wspty.Stopping tells them from a closed tab).
	first, cancelFirst := context.WithTimeout(ctx, 3*time.Second)
	shutErr := srv.Shutdown(first)
	cancelFirst()
	endRequests(wspty.ErrStopping)
	serverHandler.EndShells(ctx)
	if gitSSH != nil {
		gitSSH.Close()
	}
	if shutErr != nil {
		shutErr = srv.Shutdown(ctx)
	}
	if shutErr != nil {
		appLog.Fatal("forced shutdown", zap.Error(shutErr))
	}
	if n := deploymentSvc.InFlightCount(); n > 0 {
		appLog.Warn("deploys still running at shutdown are recovered on the next start", zap.Int("count", n))
	}
	// After the HTTP server: requests in flight (log tails, PTYs) were
	// still using their connections.
	_ = sshPool.Close()
	appLog.Info("stopped")
}

// allowedOrigins are the browser origins the API answers (CORS) and accepts
// a WebSocket from besides its own (D-62): local development's, and the
// panel's domain, for a page there that still calls api.<domain>.
func allowedOrigins(cfg *config.Config, panelDomain func() string) func() []string {
	return func() []string {
		if !cfg.IsProduction() {
			return []string{"http://localhost:3000"}
		}
		if d := panelDomain(); d != "" {
			return []string{"https://" + d}
		}
		return nil
	}
}

func corsMiddleware(origins func() []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")
		var allowedOrigins []string
		if origins != nil {
			allowedOrigins = origins()
		}
		for _, allowed := range allowedOrigins {
			if origin == allowed {
				c.Header("Access-Control-Allow-Origin", origin)
				break
			}
		}
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID, X-Setup-Token, X-Toft-Setup-Token, "+middleware.OrgHeader+", "+reauth.Header)
		c.Header("Access-Control-Allow-Credentials", "true")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// agentOrigin is how servers' agents reach the panel (D-106): its domain
// over verified TLS, or, before it has one, the address the request came
// to, whose self-signed certificate the agent doesn't verify (the pinned
// SSH keys carry the trust then). Until the join the agent pins that
// certificate, which Traefik (at proxy, on the control plane's network)
// is asked for, by the name the request came to, as the agent will ask.
func agentOrigin(panelDomain func() string, proxy string) func(r *http.Request) (server.AgentOrigin, error) {
	return func(r *http.Request) (server.AgentOrigin, error) {
		if d := panelDomain(); d != "" {
			return server.AgentOrigin{URL: "https://" + d}, nil
		}
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		o := server.AgentOrigin{URL: scheme + "://" + r.Host, InsecureTLS: scheme == "https"}
		if !o.InsecureTLS {
			return o, nil
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		// Nothing is sent: the certificate is read, not trusted.
		d := tls.Dialer{Config: &tls.Config{ServerName: (&url.URL{Host: r.Host}).Hostname(), InsecureSkipVerify: true}} //nolint:gosec // read-only, see above
		conn, err := d.DialContext(ctx, "tcp", proxy)
		if err != nil {
			return o, fmt.Errorf("read the panel's certificate from Traefik: %w", err)
		}
		defer conn.Close()
		sum := sha256.Sum256(conn.(*tls.Conn).ConnectionState().PeerCertificates[0].RawSubjectPublicKeyInfo)
		o.CertPin = base64.StdEncoding.EncodeToString(sum[:])
		return o, nil
	}
}

// logRows names up to 20 values a reseal pass left as they are. No key
// opens a lost one: a key nobody has, or plain text a pass couldn't seal.
func logRows(log *zap.Logger, msg string, rows []reseal.Row) {
	for i, r := range rows {
		if i == 20 {
			log.Warn(msg, zap.Int("more", len(rows)-i))
			return
		}
		log.Warn(msg, zap.String("column", r.Column), zap.String("row", r.ID), zap.Error(r.Err))
	}
}
