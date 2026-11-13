package service

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/Nickpap21/toft/backend/internal/catalog"
	"github.com/Nickpap21/toft/backend/internal/resources"
)

// ErrDuplicateName is returned when a service name already exists within the same scope.
var ErrDuplicateName = errors.New("a service with this name already exists in this scope")

type Type string
type SourceType string

// Scope controls which context a service belongs to.
type Scope string

const (
	// ScopeEnvironment is a classic per-environment service (default).
	ScopeEnvironment Scope = "environment"
	// ScopeProject is a service shared across all environments of a project.
	ScopeProject Scope = "project"
	// ScopeOrg is a service shared across the entire organisation (e.g. an org-wide Grafana).
	ScopeOrg Scope = "org"
)

// AccessControl holds Traefik-level per-service protection settings.
type AccessControl struct {
	BasicAuthUser     string
	BasicAuthPassword string
	IPAllowlist       []string
	// TLSDomains, when set, are the names of the certificate every one of
	// the service's routers uses (the first is its main name): a preview's
	// one certificate for all its addresses, or its wildcard (D-97), from
	// TLSResolver ("" is HTTP-01's).
	TLSDomains  []string
	TLSResolver string
	// Certified are the service's names a valid certificate has answered
	// for (domain.CertificateAt, D-150): a temporary one among them is
	// served over HTTPS like any other, no longer over plain HTTP.
	Certified []string
}

// VolumeMount describes a volume mounted into a container.
// When HostPath is set, a bind mount is created from the host directory.
// When HostPath is empty, a named Docker volume is created and managed by the PaaS.
type VolumeMount struct {
	Name     string `json:"name"`                // named-volume suffix ("paas-vol-{id8}-{Name}") OR display label for bind mounts
	Target   string `json:"target"`              // container path e.g. "/var/lib/postgresql/data"
	HostPath string `json:"host_path,omitempty"` // if set, bind-mount this host directory instead of a named volume
	ReadOnly bool   `json:"read_only,omitempty"` // bind mounts and shared volumes: mount read-only
	// Shared is the Docker volume of a shared volume (D-38). Set only for a
	// deploy, from the service's shared mounts; never stored with the
	// service's own volumes.
	Shared string `json:"-"`
}

// ServiceStatus is a service's name and stored runtime status.
type ServiceStatus struct {
	Name          string
	RuntimeStatus string
}

// VolumeInfo describes a volume along with its current state on the Docker host.
type VolumeInfo struct {
	Name      string `json:"name"`                // Docker volume name (named) or host path (bind)
	MountName string `json:"mount_name"`          // short display name
	Target    string `json:"target"`              // container mount path
	Exists    bool   `json:"exists"`              // true if the volume/path exists on the host
	SizeBytes int64  `json:"size_bytes"`          // 0 if unknown
	Type      string `json:"type,omitempty"`      // "volume" | "bind" | "" (empty = named volume, legacy)
	HostPath  string `json:"host_path,omitempty"` // set for bind mounts
	ReadOnly  bool   `json:"read_only,omitempty"` // set for read-only bind mounts
}

const (
	TypeWeb    Type = "web"
	TypeWorker Type = "worker"

	SourceTypeGit           SourceType = "git"
	SourceTypeDockerImage   SourceType = "docker-image"
	SourceTypeDockerCompose SourceType = "docker-compose"
)

// WorkspaceService is a service as the shell lists it: the sidebar's health
// dots and the command palette's search. ProjectID is "" for a
// workspace-shared service, EnvironmentID "" for a shared one.
type WorkspaceService struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Scope         Scope  `json:"scope"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	ServerID      string `json:"server_id"`      // "" is the panel host
	Port          int    `json:"port"`           // 0: no port, so no domain
	RuntimeStatus string `json:"runtime_status"` // as shown: PresentedStatus
	SourceType    string `json:"source_type"`
	AddonSlug     string `json:"addon_slug"`
	DockerImage   string `json:"docker_image"`
	GitRepoURL    string `json:"git_repo_url"`
	// GitBranch is what a deploy builds from (⌘K's "Deploy … from main").
	GitBranch string `json:"git_branch"`
	// EnvironmentName is "" for project and workspace services.
	EnvironmentName string `json:"environment_name"`
}

// Service represents a deployable unit within a project environment or shared scope.
type Service struct {
	ID            string     `json:"id"`
	Scope         Scope      `json:"scope"`                    // environment | project | org
	ProjectID     string     `json:"project_id"`               // populated via JOIN (env scope) or direct FK (project/org scope)
	EnvironmentID string     `json:"environment_id,omitempty"` // direct FK to environments table; empty for project/org scope
	ScopedOrgID   string     `json:"scoped_org_id,omitempty"`  // set only for org-scoped services
	ServerID      string     `json:"server_id,omitempty"`      // optional: empty = local Docker socket
	Name          string     `json:"name"`
	Type          Type       `json:"type"`
	SourceType    SourceType `json:"source_type"`
	GitRepoURL    string     `json:"git_repo_url,omitempty"`
	GitBranch     string     `json:"git_branch,omitempty"`
	// RepositoryID is one of the project's repositories the service builds
	// from, or takes its compose files from, instead of a URL ("" for a URL,
	// D-77). DeployOnPush: a push to GitBranch deploys it.
	RepositoryID   string `json:"repository_id"`
	DeployOnPush   bool   `json:"deploy_on_push"`
	BaseDirectory  string `json:"base_directory,omitempty"`
	DockerImage    string `json:"docker_image,omitempty"`
	InstallCommand string `json:"install_command,omitempty"`
	BuildCommand   string `json:"build_command,omitempty"`
	StartCommand   string `json:"start_command,omitempty"`
	BuildPack      string `json:"build_pack"`
	Port           int    `json:"port"`
	Replicas       int    `json:"replicas"`
	CPULimit       string `json:"cpu_limit,omitempty"`
	MemoryLimit    string `json:"memory_limit,omitempty"`
	// PidsLimit is the most processes each container may run (D-93); 0 is
	// the platform's default (rollout.DefaultPidsLimit). Read by Get.
	PidsLimit int `json:"pids_limit"`
	// DeployTimeoutMinutes is how long a deploy may run before it fails as
	// timed out (D-125); 0 is the platform's default (deployment.DefaultTimeout).
	// Read by Get.
	DeployTimeoutMinutes int `json:"deploy_timeout_minutes"`
	// RollbackImagesKept is how many of its last live deployments keep
	// their image so they can be rolled back to (D-135); 0 is the default,
	// 10. Read by Get.
	RollbackImagesKept int `json:"rollback_images_kept"`
	// ContainerHealth is the Docker health check its containers are created
	// with (ContainerHealthImage, Off or Command); ContainerHealthCommand is
	// the command's shell line. Read by Get ("" otherwise).
	ContainerHealth        string `json:"container_health,omitempty"`
	ContainerHealthCommand string `json:"container_health_command,omitempty"`
	// Isolation is the service's own isolation level (D-95), "" when it
	// follows the workspace's default; EffectiveIsolation is the level its
	// next deploy runs at. Read by Get.
	Isolation          string `json:"isolation,omitempty"`
	EffectiveIsolation string `json:"effective_isolation,omitempty"`
	RuntimeStatus      string `json:"runtime_status"`
	ActiveDeploymentID string `json:"active_deployment_id,omitempty"`
	// LiveSince is when the live deployment finished, nil without one. Not
	// the container's uptime, which a restart resets.
	LiveSince *time.Time `json:"live_since"`
	// Compose / registry fields
	ComposeContent  string `json:"compose_content,omitempty"`
	ComposeRepoURL  string `json:"compose_repo_url,omitempty"`
	ComposeFilePath string `json:"compose_file_path,omitempty"`
	// ComposeRouteService is the compose service that receives the service's
	// domains, on Port (Settings → Domain Routing). Empty means automatic:
	// the compose file's only service, or no routing when it has several.
	ComposeRouteService string `json:"compose_route_service"`
	// ComposeNetworkService is the compose service the last successful
	// deploy routed, so put on the app network (deployment.composeRoute's
	// pick); "" when none, or none recorded yet. connectTo reads it.
	ComposeNetworkService string `json:"-"`
	// DeployStrategy is the service's choice of how a deploy replaces its
	// container (DeployStrategyAuto, Rolling or Recreate);
	// EffectiveDeployStrategy is what it comes to, set as the service is
	// shown (presented). Go code uses the function of that name.
	DeployStrategy          string `json:"deploy_strategy"`
	EffectiveDeployStrategy string `json:"effective_deploy_strategy"`
	// IsolationAdvice is the level recommended for the service (D-99), set
	// as the service is shown (presented).
	IsolationAdvice IsolationAdvice `json:"isolation_advice"`
	// Kind is what the service is, for showing it: a database, a cache, an
	// admin tool or an app, by its catalog entry or its image's name
	// (resources.NewKindOf), set as the service is shown (presented).
	Kind                resources.Kind `json:"kind"`
	RegistryServer      string         `json:"registry_server,omitempty"`
	RegistryUsername    string         `json:"registry_username,omitempty"`
	RegistryPassword    string         `json:"-"`                     // decrypted at DB read; never serialised
	HasRegistryPassword bool           `json:"has_registry_password"` // true when a password is stored
	// Addon catalog fields
	AddonSlug    string `json:"addon_slug,omitempty"`
	NetworkAlias string `json:"network_alias,omitempty"`
	// ConnectTo is the address other services on its network reach it at
	// (connectTo), set as the service is shown (presented).
	ConnectTo *ConnectAddress `json:"connect_to,omitempty"`
	Volumes   []VolumeMount   `json:"volumes,omitempty"`
	// Git credentials (token never returned from API)
	GitUsername        string    `json:"git_username,omitempty"`
	GitToken           string    `json:"-"`                              // decrypted at read; never serialised to JSON
	HasGitToken        bool      `json:"has_git_token"`                  // true when a token is stored
	PreDeployCommands  string    `json:"pre_deploy_commands,omitempty"`  // newline-separated shell commands
	PostDeployCommands string    `json:"post_deploy_commands,omitempty"` // "service:command" per line; runs inside container after deploy
	HealthCheckPath    string    `json:"health_check_path"`
	HealthCheckTimeout int       `json:"health_check_timeout"`
	HealthCheckRetries int       `json:"health_check_retries"`
	RequiredEnvVars    string    `json:"required_env_vars"`      // newline-separated list of required env var keys
	MigrateCommand     string    `json:"migrate_command"`        // docker run --rm command before container start
	Dependencies       []string  `json:"dependencies,omitempty"` // service IDs that must be running before deploy
	BasicAuthUser      string    `json:"basic_auth_user,omitempty"`
	BasicAuthPassword  string    `json:"-"` // decrypted at read; never serialised
	IPAllowlist        []string  `json:"ip_allowlist,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`

	// Deploying: a deployment of the service is pending or building. The API
	// shows it as runtime_status "deploying" (MarshalJSON); RuntimeStatus
	// keeps saying what runs, which the dependency check reads.
	Deploying bool `json:"-"`
	// Down and Unhealthy: what the alert monitor sees of a running service
	// (an open alert): no container serving or a crash loop, and a failing
	// health check or an exited compose container. See PresentedStatus.
	Down      bool `json:"-"`
	Unhealthy bool `json:"-"`
	// LastDeployFailed: the service's latest deployment failed. A deploy
	// that fails before it changes anything, or at its health check, leaves
	// the previous version serving, so the status can be running meanwhile.
	LastDeployFailed bool `json:"last_deploy_failed"`
	// LastHealthCheck is the minute probe's last answer to its health check
	// (GET /services/:id only); nil when none was taken.
	LastHealthCheck *HealthAnswer `json:"last_health_check,omitempty"`
}

// serviceJSON is a Service as the API shows it. It has no MarshalJSON, so a
// struct embedding it marshals its own fields too (see SavedService).
type serviceJSON Service

// PresentedStatus is the runtime status the API shows: "deploying" while a
// deploy is pending or building (the fix under way), then what the alert
// monitor sees of a running service, "error" when nothing serves or it
// crash-loops and "unhealthy" when it serves but fails its health check.
// Otherwise the stored status, which deploys, recovery and the dependency
// check go by; the monitor watches services stored as running, so these
// alerts resolve, and the status returns, when the service recovers.
func PresentedStatus(stored string, deploying, down, unhealthy bool) string {
	switch {
	case deploying:
		return "deploying"
	case stored != "running":
		return stored
	case down:
		return "error"
	case unhealthy:
		return "unhealthy"
	default:
		return stored
	}
}

// presented is the service as the API shows it (PresentedStatus).
func (s Service) presented() serviceJSON {
	p := serviceJSON(s)
	p.RuntimeStatus = PresentedStatus(s.RuntimeStatus, s.Deploying, s.Down, s.Unhealthy)
	p.EffectiveDeployStrategy = EffectiveDeployStrategy(s.DeployStrategy, s.AddonSlug, s.DockerImage)
	p.IsolationAdvice = AdviseIsolation(s.AddonSlug, s.DockerImage, s.ServerID)
	p.Kind = resources.NewKindOf(s.AddonSlug, s.DockerImage)
	p.ConnectTo = connectTo(s)
	return p
}

// ConnectAddress is a host and port other services connect to; Port 0 when
// it listens on none.
type ConnectAddress struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// connectTo is the address other services on s's network reach it at, in
// legacy and isolated app networks alike, or nil when the panel can't name
// one. A single container's slots answer to services.network_alias
// (rollout), on its port, which for a catalog image is the image's own. A
// compose stack's containers answer only to their compose service names:
// the stack joins the app network through the service its last successful
// deploy routed (ComposeNetworkService), on the port its domains go to; none
// when that deploy routed none (no domain) or no deploy recorded one.
func connectTo(s Service) *ConnectAddress {
	if s.SourceType == SourceTypeDockerCompose {
		if s.ComposeNetworkService == "" {
			return nil
		}
		return &ConnectAddress{Host: s.ComposeNetworkService, Port: s.Port}
	}
	if s.NetworkAlias == "" {
		return nil
	}
	port := s.Port
	if t, ok := catalog.Get(s.AddonSlug); ok && t.Kind == catalog.KindImage && t.DefaultPort > 0 {
		port = t.DefaultPort
	}
	return &ConnectAddress{Host: s.NetworkAlias, Port: port}
}

func (s Service) MarshalJSON() ([]byte, error) { return json.Marshal(s.presented()) }

type CreateInput struct {
	// Address asks for an address now (D-150), as AddressFor names it.
	Address bool `json:"address"`
	// AddressHostname is that address, stored with the service; set by
	// ServiceManager.Create, never by a caller.
	AddressHostname string `json:"-"`
	// Deploy starts its first deploy once it is created (Handler.firstDeploy):
	// one that won't fit its server is refused, not created (CheckRoom).
	// Force, with Deploy, creates and deploys it anyway for a caller who may
	// deploy past the server's capacity.
	Deploy bool `json:"deploy"`
	Force  bool `json:"force"`

	Name       string     `json:"name" binding:"required,max=100"`
	Type       Type       `json:"type" binding:"required,oneof=web worker"`
	SourceType SourceType `json:"source_type" binding:"required,oneof=git docker-image docker-compose"`
	Scope      Scope      `json:"scope"` // defaults to ScopeEnvironment; set by handler based on route
	ServerID   string     `json:"server_id"`
	GitRepoURL string     `json:"git_repo_url"`
	GitBranch  string     `json:"git_branch"`
	// RepositoryID: see Service.RepositoryID ("" for a URL). DeployOnPush
	// is true when left out.
	RepositoryID    string `json:"repository_id"`
	DeployOnPush    *bool  `json:"deploy_on_push"`
	BaseDirectory   string `json:"base_directory"`
	DockerImage     string `json:"docker_image"`
	InstallCommand  string `json:"install_command"`
	BuildCommand    string `json:"build_command"`
	StartCommand    string `json:"start_command"`
	BuildPack       string `json:"build_pack"`
	Port            int    `json:"port"`
	CPULimit        string `json:"cpu_limit"`
	MemoryLimit     string `json:"memory_limit"`
	ComposeContent  string `json:"compose_content"`
	ComposeRepoURL  string `json:"compose_repo_url"`
	ComposeFilePath string `json:"compose_file_path"`
	// ComposeRouteService: see Service.ComposeRouteService. Empty = automatic.
	ComposeRouteService string        `json:"compose_route_service"`
	RegistryServer      string        `json:"registry_server"`
	RegistryUsername    string        `json:"registry_username"`
	RegistryPassword    string        `json:"registry_password"` // write-only; encrypted before storage
	AddonSlug           string        `json:"addon_slug"`
	NetworkAlias        string        `json:"network_alias"`
	Volumes             []VolumeMount `json:"volumes"`
	GitUsername         string        `json:"git_username"`
	GitToken            string        `json:"git_token"` // write-only; encrypted before storage
	PreDeployCommands   string        `json:"pre_deploy_commands"`
	PostDeployCommands  string        `json:"post_deploy_commands"`
	HealthCheckPath     string        `json:"health_check_path"`
	HealthCheckTimeout  int           `json:"health_check_timeout"`
	HealthCheckRetries  int           `json:"health_check_retries"`
	RequiredEnvVars     string        `json:"required_env_vars"`
	MigrateCommand      string        `json:"migrate_command"`
	Dependencies        []string      `json:"dependencies"`
	BasicAuthUser       string        `json:"basic_auth_user,omitempty"`
	BasicAuthPassword   string        `json:"basic_auth_password,omitempty"`
	IPAllowlist         []string      `json:"ip_allowlist,omitempty"`
	// ContainerHealth: see Service.ContainerHealth; "" is the image's.
	ContainerHealth        string `json:"container_health"`
	ContainerHealthCommand string `json:"container_health_command"`
	// Variables are stored with the service, or not at all; a catalog
	// service gets its template's others at their defaults (withGenerated).
	Variables []Variable `json:"variables" binding:"dive"`
	// Secrets are stored write-once with the service, or not at all; a
	// catalog service gets its template's others generated (withGenerated).
	Secrets []KeyValue `json:"secrets" binding:"dive"`
}

// ContainerStats is a point-in-time resource snapshot for a running container.
type ContainerStats struct {
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryMB      float64 `json:"memory_mb"`
	MemoryLimitMB float64 `json:"memory_limit_mb"`
	// When the container last started (a deploy, a restart), and how many
	// times Docker restarted it after it exited. Only GET /stats reads them.
	StartedAt    *time.Time `json:"started_at,omitempty"`
	RestartCount int        `json:"restart_count"`

	// For the metrics collector (D-41), never in a response. The counters
	// are a remote server's cgroup v2 counters, cumulative since the
	// container started; zero (nil) when not read: an older kernel, cgroup
	// v1, the control-plane server, or a container that stopped first.
	ComposeService string   `json:"-"` // "" for a single-container service or a blue/green slot
	ContainerID    string   `json:"-"`
	NrPeriods      uint64   `json:"-"` // cpu.stat
	NrThrottled    uint64   `json:"-"` // cpu.stat
	CPUWaitPct     *float64 `json:"-"` // cpu.pressure "some avg60"
	RestartsRead   bool     `json:"-"` // RestartCount was read (an agent that predates it prints none)
}

// HostSample is one server's sample for the metrics collector.
type HostSample struct {
	// Containers are each target's running containers, keyed by service ID,
	// then compose service ("" for a single-container service).
	Containers map[string]map[string][]ContainerStats
	CPUWaitPct *float64 // /proc/pressure/cpu "some avg60"; nil when not read
	// OOMKills are the targets' memory kills from the window's start (the
	// since the caller passed) to Until.
	OOMKills []OOMEvent
	// Until is the window's end, in unix seconds on the server's clock: the
	// since of the server's next sample. 0 when the kills were not read
	// (the control-plane server, or docker events failed): the caller keeps
	// its since, so the next sample reads the window again.
	Until int64
	// At is when the sample was taken, for one a server's agent kept
	// (D-113); zero for one taken now.
	At time.Time
	// Latency is what the host's Traefik measured of the targets' requests
	// (D-156); nil when it was not read.
	Latency *LatencyReading
}

// LatencyReading is a host's Traefik's request duration histograms at one
// sample (hoststats.Latency).
type LatencyReading struct {
	// Unmeasured says why there are none (hoststats.Unmeasured*); "" when
	// they were read.
	Unmeasured string
	// Start is Traefik's process start: another means every count started
	// again.
	Start float64
	// Counts are the requests since Traefik started of every Toft service
	// on its page, targets or not, by Traefik service name (paas-svc-<id8>):
	// cumulative per bound of hoststats.LatencyBounds, then +Inf. One
	// Traefik has answered no request for since has none.
	Counts map[string][]uint64
	// Names are the Traefik service name each target routes to, by service
	// ID; a target whose name another target shares has none.
	Names map[string]string
}

// OOMEvent is a container of a service killed for memory.
type OOMEvent struct {
	ServiceID      string
	ComposeService string // as in HostSample.Containers
	Container      string // the container's name
	At             time.Time
}

// MetricSample is one stored service_metrics row: a service's compose
// service ("" for a single-container service) in one collector cycle, its
// containers' CPU and memory summed. The pointers are nil when not measured.
// The collector names it metrics.Sample; it lives here so that
// ServiceManager can store it without importing metrics.
type MetricSample struct {
	CPUPercent, MemoryMB float64
	MemoryPeakMB         *float64 // the largest single container's memory: the limit is per container
	ThrottledPct         *float64 // share of the minute held back by the CPU limit
	CPUWaitPct           *float64 // share of the minute waiting for a CPU
	Restarts             *int     // its containers' restarts since the previous sample; nil when one's count was not read
}

// ContainerState is one container of a service as Docker reports it, for
// alerts.
type ContainerState struct {
	Name     string
	State    string // running, restarting, exited, dead, paused, created
	Health   string // healthy, unhealthy, starting; "" without a health check
	ExitCode int    // set when State is exited
	// HealthDetail is why the platform's health check probe failed
	// ("GET /health: HTTP 500"); "" when it passed or did not run.
	HealthDetail string
	// HealthAnswer is what the probe measured; nil when it did not run.
	HealthAnswer *HealthAnswer
}

// HealthAnswer is the last answer to a service's health check, as the
// minute probe measured it: the status (0 when nothing answered), how long
// the request took, and why it failed.
type HealthAnswer struct {
	Status  int       `json:"status"`
	Millis  int       `json:"ms"`
	Healthy bool      `json:"healthy"`
	Detail  string    `json:"detail,omitempty"`
	At      time.Time `json:"at"`
	// Since is when the running container was first checked.
	Since time.Time `json:"since"`
}

// HealthTestResult is what a service's container answered one health-check
// request: Status 0 when nothing answered, with Detail saying why.
type HealthTestResult struct {
	Status int    `json:"status"`
	Millis int    `json:"ms"`
	Detail string `json:"detail,omitempty"`
}

// DiskUsage is how full one filesystem of a server is.
type DiskUsage struct {
	Mount       string
	UsedPercent int
}

// MetricsTarget is a running service the metrics collector samples. Name
// matters: a compose service's containers are found by the project name
// derived from its ID and name.
// ContainerLog is where one container sends its output: its Docker log
// driver ("json-file", "loki", …) and, for Loki's, the address it pushes to.
type ContainerLog struct {
	Name    string `json:"name"`
	Driver  string `json:"driver"`
	LokiURL string `json:"loki_url,omitempty"`
}

type MetricsTarget struct {
	ID       string
	Name     string
	ServerID string // "" = the control plane's own Docker host
	// HealthPath, Port and HealthTimeout: the health check the alert
	// monitor probes every minute; HealthPath is "" when the service has
	// none (no retries, no port, or a compose stack, whose file has its own).
	HealthPath    string
	Port          int
	HealthTimeout int
	// Preview: one of a preview's services, which raises no alerts (its
	// pull request's checks say how it does; D-97).
	Preview bool
	// Routed: it has a domain, so its server's Traefik measures its
	// requests (D-156).
	Routed bool
	// ContainerHealth is whose check Docker runs in its containers
	// (Service.ContainerHealth), for the unhealthy alert to name it; "" for
	// a compose stack, whose file may set its own.
	ContainerHealth string
}

// MetricPoint is a single recorded data point for service metrics history.
type MetricPoint struct {
	Timestamp  time.Time `json:"timestamp"`
	CPUPercent float64   `json:"cpu_percent"`
	MemoryMB   float64   `json:"memory_mb"`
	// ThrottledPct is the share of the minute the CPU limit held it back.
	ThrottledPct float64 `json:"throttled_pct"`
	// LargestContainer marks a 7d day summarised before 0070: its memory is
	// each compose service's largest container's peak, not the summed one.
	LargestContainer bool `json:"largest_container,omitempty"`
}

type UpdateConfigInput struct {
	Name       string `json:"name"` // empty = keep existing
	GitRepoURL string `json:"git_repo_url"`
	GitBranch  string `json:"git_branch"`
	// RepositoryID: absent (nil) keeps the source, "" switches to the URLs,
	// an id to that repository (D-77). DeployOnPush: absent keeps it.
	RepositoryID   *string `json:"repository_id"`
	DeployOnPush   *bool   `json:"deploy_on_push"`
	BaseDirectory  string  `json:"base_directory"`
	DockerImage    string  `json:"docker_image"`
	InstallCommand string  `json:"install_command"`
	BuildCommand   string  `json:"build_command"`
	StartCommand   string  `json:"start_command"`
	BuildPack      string  `json:"build_pack"`
	Port           int     `json:"port"`
	CPULimit       string  `json:"cpu_limit"`
	MemoryLimit    string  `json:"memory_limit"`
	// PidsLimit: absent keeps it, 0 goes back to the default, else 256 to
	// 32768 (D-93).
	PidsLimit *int `json:"pids_limit"`
	// DeployTimeoutMinutes: absent keeps it, 0 goes back to the default,
	// else 5 to 240 (D-125).
	DeployTimeoutMinutes *int `json:"deploy_timeout_minutes"`
	// RollbackImagesKept: absent keeps it, 0 goes back to the default,
	// else 1 to 100 (D-135).
	RollbackImagesKept *int `json:"rollback_images_kept"`
	// Isolation: absent keeps it; "default" follows the workspace's, else
	// "standard", "sandboxed" or "sandboxed_strict" (D-95, D-115).
	Isolation *string `json:"isolation"`
	// ContainerHealth and ContainerHealthCommand: see
	// Service.ContainerHealth; absent keeps each.
	ContainerHealth        *string `json:"container_health"`
	ContainerHealthCommand *string `json:"container_health_command"`
	ComposeContent         string  `json:"compose_content"`
	ComposeRepoURL         string  `json:"compose_repo_url"`
	ComposeFilePath        string  `json:"compose_file_path"`
	// ComposeRouteService: see Service.ComposeRouteService. Absent (nil)
	// keeps the stored value; "" switches back to automatic.
	ComposeRouteService *string `json:"compose_route_service"`
	// DeployStrategy: see Service.DeployStrategy. "" keeps the stored one.
	DeployStrategy     string        `json:"deploy_strategy"`
	RegistryServer     string        `json:"registry_server"`
	RegistryUsername   string        `json:"registry_username"`
	RegistryPassword   string        `json:"registry_password"` // write-only; empty = keep existing
	NetworkAlias       string        `json:"network_alias"`
	Volumes            []VolumeMount `json:"volumes"`
	GitUsername        string        `json:"git_username"`
	GitToken           string        `json:"git_token"` // write-only; empty = keep existing
	PreDeployCommands  string        `json:"pre_deploy_commands"`
	PostDeployCommands string        `json:"post_deploy_commands"`
	HealthCheckPath    string        `json:"health_check_path"`
	HealthCheckTimeout int           `json:"health_check_timeout"`
	HealthCheckRetries int           `json:"health_check_retries"`
	RequiredEnvVars    string        `json:"required_env_vars"`
	MigrateCommand     string        `json:"migrate_command"`
	Dependencies       []string      `json:"dependencies"`
	BasicAuthUser      string        `json:"basic_auth_user,omitempty"`
	BasicAuthPassword  string        `json:"basic_auth_password,omitempty"`
	IPAllowlist        []string      `json:"ip_allowlist,omitempty"`
	// Set by UpdateConfig, never by a request: the saved credential would
	// go somewhere new (credentialsMoved), so it is cleared.
	ClearGitToken         bool `json:"-"`
	ClearRegistryPassword bool `json:"-"`
}

// Container checks: the Docker health check a service's containers are
// created with. Not for compose stacks, whose file sets its own.
const (
	// ContainerHealthImage keeps the image's HEALTHCHECK, if it has one.
	ContainerHealthImage = "image"
	// ContainerHealthOff turns Docker's check off (NONE).
	ContainerHealthOff = "off"
	// ContainerHealthCommand runs ContainerHealthCommand in the container's
	// shell (CMD-SHELL), at the image's interval, timeout and retries.
	ContainerHealthCommand = "command"
)

// Deploy strategies (D-42): how a deploy replaces a service's container.
const (
	// DeployStrategyAuto is DeployStrategyRecreate for a database or a cache
	// and DeployStrategyRolling for anything else.
	DeployStrategyAuto = "auto"
	// DeployStrategyRolling starts the new container and health-checks it
	// while the old one serves: no downtime, but both run for a while.
	DeployStrategyRolling = "rolling"
	// DeployStrategyRecreate stops the old container before the new one
	// starts, and starts it again when the new one fails: a moment of
	// downtime, but never two writers of the same data.
	DeployStrategyRecreate = "recreate"
)

// EffectiveDeployStrategy is what a service's deploy strategy comes to:
// Rolling or Recreate. Auto (or "") is Recreate for a database or a cache,
// whose volume has a single writer: two MySQLs or Redises on the same files
// corrupt them, or survive only by a lock some engines do not take.
func EffectiveDeployStrategy(strategy, addonSlug, image string) string {
	if addonSlug == "loki" {
		// The catalog's Loki holds one 127.0.0.1 port on its server, which
		// two slots can't share (D-148): whatever it is set to.
		return DeployStrategyRecreate
	}
	switch strategy {
	case DeployStrategyRolling, DeployStrategyRecreate:
		return strategy
	}
	switch resources.KindOf(addonSlug, image) {
	case resources.KindDatabase, resources.KindCache:
		return DeployStrategyRecreate
	}
	return DeployStrategyRolling
}

// IsolationAdvice is the isolation level recommended for a service, and
// why (D-99): database, tool, panel_server or app.
type IsolationAdvice struct {
	Level  string `json:"level"`
	Reason string `json:"reason"`
}

// AdviseIsolation recommends Standard for a database or a cache, which
// gVisor slows most, and Sandboxed for an admin tool, which faces the
// internet holding databases' passwords and does too little work to feel
// gVisor. Anything else is Sandboxed on the panel's own server (serverID
// ""), where a container that breaks out reaches every server's SSH key
// and every secret, and Standard elsewhere. A tool typed in by its image
// counts (NewKindOf, D-150), so the advice names the level a new one starts at.
func AdviseIsolation(addonSlug, image, serverID string) IsolationAdvice {
	switch resources.NewKindOf(addonSlug, image) {
	case resources.KindDatabase, resources.KindCache:
		return IsolationAdvice{IsolationStandard, "database"}
	case resources.KindTool:
		return IsolationAdvice{IsolationSandboxed, "tool"}
	}
	if serverID == "" {
		return IsolationAdvice{IsolationSandboxed, "panel_server"}
	}
	return IsolationAdvice{IsolationStandard, "app"}
}

// Recreates reports whether the service's deploys stop then start
// (EffectiveDeployStrategy is DeployStrategyRecreate).
func (s *Service) Recreates() bool {
	return EffectiveDeployStrategy(s.DeployStrategy, s.AddonSlug, s.DockerImage) == DeployStrategyRecreate
}

// ToAccessControl extracts access control settings from the service.
func (s *Service) ToAccessControl() AccessControl {
	return AccessControl{
		BasicAuthUser:     s.BasicAuthUser,
		BasicAuthPassword: s.BasicAuthPassword,
		IPAllowlist:       s.IPAllowlist,
	}
}

// ParseMemoryMB converts a Docker memory string (e.g. "512m", "1g") to
// megabytes with resources.ParseMB, the one parser of memory limits.
// Returns 0 for empty or unparseable input.
func ParseMemoryMB(s string) float64 {
	mb, _ := resources.ParseMB(s)
	return float64(mb)
}
