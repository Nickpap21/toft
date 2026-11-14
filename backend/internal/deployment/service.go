package deployment

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/deploypolicy"
	domainPkg "github.com/Nickpap21/toft/backend/internal/domain"
	envPkg "github.com/Nickpap21/toft/backend/internal/environment"
	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"github.com/Nickpap21/toft/backend/internal/netplan"
	"github.com/Nickpap21/toft/backend/internal/observability"
	orgPkg "github.com/Nickpap21/toft/backend/internal/org"
	"github.com/Nickpap21/toft/backend/internal/resources"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/pkg/actor"
	"github.com/Nickpap21/toft/backend/pkg/gitcred"
	"github.com/moby/go-archive"
	"go.uber.org/zap"
)

// PostDeployCommand is a single post-deploy exec targeting a named container/service.
// ServiceName is the compose service name (e.g. "web"); empty = exec into the main service container.
type PostDeployCommand struct {
	ServiceName string
	Command     string
}

// ParsePostDeployCommands parses a newline-delimited string of "service:command" pairs.
// Lines without a colon are treated as commands with an empty ServiceName.
func ParsePostDeployCommands(s string) []PostDeployCommand {
	lines := SplitLines(s)
	result := make([]PostDeployCommand, 0, len(lines))
	for _, line := range lines {
		if idx := strings.IndexByte(line, ':'); idx >= 0 {
			result = append(result, PostDeployCommand{
				ServiceName: strings.TrimSpace(line[:idx]),
				Command:     strings.TrimSpace(line[idx+1:]),
			})
		} else {
			result = append(result, PostDeployCommand{Command: line})
		}
	}
	return result
}

// RegistryAuth holds credentials for a private container registry.
type RegistryAuth = rollout.RegistryAuth

// Commit is the commit a git build checked out: its full SHA and subject.
type Commit struct {
	SHA     string
	Subject string
}

// Orchestrator handles building and running containers.
// Implementations: docker.LocalOrchestrator, docker.RemoteOrchestrator.
type Orchestrator interface {
	// buildArgs are forwarded to `docker build --build-arg KEY=VALUE` so that
	// build-time variables (e.g. NEXT_PUBLIC_* in Next.js) are available during
	// the image build step, not just at container runtime.
	//
	// The Commit is what the clone checked out (the branch's tip), returned
	// once the clone succeeded, also when the build then fails; it is empty
	// when the clone failed or the commit could not be read.
	BuildFromGit(ctx context.Context, repoURL, branch, baseDir, imageTag string, buildArgs map[string]*string, logFn func(string)) (Commit, error)
	BuildFromGitNixpacks(ctx context.Context, repoURL, branch, baseDir, imageTag string, cmds NixpacksCommands, buildArgs map[string]*string, logFn func(string)) (Commit, error)
	// BuildFromArchive builds imageTag from source, a commit's tree as a tar
	// (the project's own repository, D-77): with the Dockerfile at
	// baseDir/Dockerfile and the tree as the context, or with nixpacks in
	// baseDir when nixpacks is set. The build host never sees a URL or a token.
	BuildFromArchive(ctx context.Context, source io.Reader, baseDir, imageTag string, nixpacks *NixpacksCommands, buildArgs map[string]*string, logFn func(string)) error
	// Rollout runs a single-container deploy's rollout on the server
	// (rollout.Run): pull, migrate, the new blue/green slot, its checks and
	// the switch. route is the new slot's routing, which the orchestrator
	// turns into its Traefik labels.
	Rollout(ctx context.Context, spec rollout.Spec, route Routing, hooks rollout.Hooks) rollout.Result
	// RunCompose runs inline compose content. envVars are the only variables
	// docker compose may interpolate besides the platform's own (see
	// composespec.IsPlatformEnv); route, when not nil, adds the platform's
	// routing for the service's domains.
	// nets says which network the routed service joins and, under isolated
	// app networks, carries the network override.
	RunCompose(ctx context.Context, deploymentID string, serviceID, serviceName, composeContent string, envVars map[string]string, route *ComposeRoute, nets ComposeNetworks, limits map[string]ComposeLimit, auth *RegistryAuth, hooks rollout.Hooks) error
	// RunComposeFromRepo runs docker compose from a full local repo directory.
	// The entire directory is uploaded to the remote so relative file paths work.
	RunComposeFromRepo(ctx context.Context, deploymentID string, serviceID, serviceName, repoDir, composeFilePath string, envVars map[string]string, route *ComposeRoute, nets ComposeNetworks, limits map[string]ComposeLimit, auth *RegistryAuth, hooks rollout.Hooks) error
	Stop(ctx context.Context, serviceID string) error
	StopCompose(ctx context.Context, serviceID, serviceName, composeContent string) error
	StreamLogs(ctx context.Context, serviceID string, tailLines int, containerName string, logFn func(string)) error
	// ExecCommands runs arbitrary shell commands on the target host before the main pipeline.
	// Each command is run in sequence; on first failure the remaining commands are skipped.
	ExecCommands(ctx context.Context, commands []string, logFn func(string)) error
	// ExecInContainer runs a command inside a running container.
	// containerName is the actual container name as shown by `docker ps`.
	ExecInContainer(ctx context.Context, serviceID, containerName, command string, logFn func(string)) error
	// ListContainers returns the names of running containers that belong to this service.
	ListContainers(ctx context.Context, serviceID, serviceName string) ([]string, error)
	// PrepareNetworks makes the host ready for a deploy under plan: its home
	// network exists, the proxy and the service's own and attached peers'
	// containers are on it. A no-op under legacy app networks.
	PrepareNetworks(ctx context.Context, plan netplan.Plan, logFn func(string)) error
}

// Routing is how a rollout's new slot is routed.
type Routing struct {
	Domains []string
	Access  service.AccessControl
}

// OrchestratorFactory selects the right Orchestrator for a given server.
// ForServer(nil) → local Docker socket; ForServer(srv) → SSH-based remote.
// The returned cleanup function must always be called.
type OrchestratorFactory interface {
	ForServer(srv *server.Server) (Orchestrator, func(), error)
}

// ServiceStatusUpdater updates the runtime state of a service after a
// deployment, and an image service's saved image (followLive).
// Satisfied by service.ServiceManager.
type ServiceStatusUpdater interface {
	UpdateRuntimeStatus(ctx context.Context, serviceID, status, activeDeploymentID string) error
	// SetComposeNetworkService records the compose service a successful
	// compose deploy routed, "" for none (composeRoute's pick).
	SetComposeNetworkService(ctx context.Context, serviceID, name string) error
	// SwapDockerImage sets the service's docker_image to image only while
	// it is still from, and never to another registry's image.
	SwapDockerImage(ctx context.Context, serviceID, from, image string) error
}

// SharedMountProvider returns a service's shared volume mounts (D-38) for a
// deploy to serverID ("" for the control-plane server). Satisfied by
// sharedvolume.Service.
type SharedMountProvider interface {
	MountsForDeploy(ctx context.Context, serviceID, serverID string) ([]service.VolumeMount, error)
}

// EnvVarProvider fetches all env vars (including decrypted secrets) for a service.
// Satisfied by envvar.Service.
type EnvVarProvider interface {
	ListAllForDeployment(ctx context.Context, serviceID string) ([]string, error)
}

// DomainActivator marks all domains for a service as verified after a successful deployment.
// Satisfied by domain.Service.
type DomainActivator interface {
	ActivateForService(ctx context.Context, serviceID string) error
}

// ConfigFileMount is a bind-mount produced by the config file service.
// Mirrors configfile.VolumeMount to avoid a circular import.
type ConfigFileMount struct {
	HostPath string
	Target   string
	ReadOnly bool
}

// ConfigFileWriter writes config files to the host before container start and returns bind mounts.
// Satisfied by *configfile.Service — defined here to avoid a circular import.
type ConfigFileWriter interface {
	// WriteToHost also returns the files it wrote, each its path and
	// content, as Contents gives them.
	WriteToHost(ctx context.Context, serviceID string, srv *server.Server) ([]ConfigFileMount, []string, error)
	// Contents are the service's config files, each its path and content,
	// for its settings fingerprint (D-159).
	Contents(ctx context.Context, serviceID string) ([]string, error)
}

// ObservabilityQuerier gives the Loki a service's logs go to: its
// project's, else its workspace's (D-48, D-148). Satisfied by
// observability.Service.
type ObservabilityQuerier interface {
	LokiFor(ctx context.Context, orgID, projectID string) (observability.LokiTarget, error)
}

// LoopbackPorts gives the catalog Loki its 127.0.0.1 port on its server
// (D-148). Satisfied by service.PostgresRepository.
type LoopbackPorts interface {
	AssignLoopbackPort(ctx context.Context, serviceID string) (int, error)
}

// LokiDriverChecker says whether a server's Docker has the Loki log driver
// plugin; a container started with a driver Docker lacks fails to start.
// Both orchestrators implement it.
type LokiDriverChecker interface {
	HasLokiDriver(ctx context.Context) (bool, error)
}

// ObservabilityRegistrar auto-registers a service in the observability config
// when it is a PLG catalog addon (loki|grafana|prometheus).
// Satisfied by observability.Service — defined here to avoid a circular import.
type ObservabilityRegistrar interface {
	RegisterIfObservability(ctx context.Context, orgID, serviceID, addonSlug string) error
}

// EventPublisher publishes domain events to the org-scoped SSE bus.
// Satisfied by broker.Broker — defined here to avoid a circular import.
type EventPublisher interface {
	Publish(orgID string, event DeploymentEvent)
}

// OrgSettingsReader is satisfied by org.Service; defined here to avoid a circular import.
type OrgSettingsReader interface {
	GetSettings(ctx context.Context, orgID string) (*orgPkg.OrgSettings, error)
	// RoleIn is the user's role in another workspace: who may deploy past a
	// share is decided by the server's owner.
	RoleIn(ctx context.Context, userID, orgID string) (string, error)
}

// CapacitySummarizer is satisfied by server.Service; defined here to avoid a circular import.
type CapacitySummarizer interface {
	// GetCapacitySummaryFor is a workspace's capacity on a server: its
	// share of a shared server, or for the owner the server less the
	// shares, with only its own services allocated.
	GetCapacitySummaryFor(ctx context.Context, serverID, orgID string) (*server.CapacitySummary, error)
}

// ServiceReader fetches a service by ID for resource-limit inspection during capacity checks.
// Satisfied by service.ServiceManager — defined here to avoid a circular import.
type ServiceReader interface {
	Get(ctx context.Context, id string) (*service.Service, error)
}

// ComposeRoute is the routing the platform adds to a compose deployment: the
// Traefik labels for the service's domains, on one compose service and
// port, and that service's attachment to the Traefik network. Compose files
// cannot set traefik.* labels themselves (D-19).
type ComposeRoute struct {
	// Service is the compose service that receives the domains.
	Service string
	// Port is its container port.
	Port    int
	Domains []string
	Access  service.AccessControl
}

// ComposeLimit is the memory and CPU limit the panel sets on one compose
// service (D-41). Zero values mean no panel value. DeployLimits is set when
// the file declares deploy.resources.limits for the service: the override
// then sets the limits there too, as compose refuses the two forms with
// different values.
type ComposeLimit struct {
	MemoryMB     int64
	CPUCores     float64
	DeployLimits bool
}

// limitLogLines describes the memory limits a deploy runs with, one line per
// container, for the deploy log. fileMB maps each compose service to the
// limit its file sets (0 for none); its keys are the services listed.
// defaults are the panel limits this deploy added (AddComposeDefaults).
func limitLogLines(input TriggerInput, fileMB map[string]int64, defaults map[string]resources.DefaultAdded) []string {
	if input.SourceType != "docker-compose" {
		if m, ok := resources.ParseMB(input.MemoryLimit); ok {
			line := "→ memory limit " + resources.FormatMB(m) + " (panel)"
			if input.CPULimit != "" {
				line += ", cpu " + input.CPULimit
			}
			return []string{line}
		}
		return []string{"→ memory limit none"}
	}
	names := make([]string, 0, len(fileMB))
	for n := range fileMB {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, n := range names {
		line := "→ " + n + ": memory limit none"
		l := input.ComposeLimits[n]
		switch {
		case l.MemoryMB > 0:
			from := "(panel)"
			if d, ok := defaults[n]; ok {
				from = "(default for " + string(d.Kind) + "); change it on the service page"
			}
			line = "→ " + n + ": memory limit " + resources.FormatMB(l.MemoryMB) + " " + from
		case fileMB[n] > 0:
			line = "→ " + n + ": memory limit " + resources.FormatMB(fileMB[n]) + " (compose file)"
		}
		if l.CPUCores > 0 {
			line += ", cpu " + strconv.FormatFloat(l.CPUCores, 'f', -1, 64)
		}
		out = append(out, line)
	}
	return out
}

// ComposeNetworks is how a compose deploy uses networks.
type ComposeNetworks struct {
	// Isolated is TOFT_APP_NETWORKS=isolated. Under legacy the routed
	// service joins the control plane's network, as before.
	Isolated bool
	// Security is how the hardened runtime walls in Services (D-93).
	Security rollout.Security
	// Home is the service's environment (or shared) network, which the
	// routed service joins under isolation.
	Home string
	// Override is the network override composespec.IsolateNetworks
	// generated and re-checked (nil when the file names no legacy network).
	Override []byte
	// RouteKey is the stack's own key for Home, when it has one (a network
	// it named like the control plane's), so the routed service is not
	// attached to Home twice.
	RouteKey string
	// Services are the compose file's services. Every one but the routed
	// service gets traefik.enable=false, so routers its image defines never
	// go live on a network Traefik joins (the environment network, or the
	// control plane's under legacy). Nil when the file could not be loaded;
	// the check after up still takes such a stack down.
	Services []string
}

// PolicyChecker is the deploy policy (D-19), checked on every deploy.
// Satisfied by *deploypolicy.Service.
type PolicyChecker interface {
	CheckDeploy(ctx context.Context, in deploypolicy.DeployCheck) deploypolicy.Decision
	CheckComposeRepo(ctx context.Context, in deploypolicy.RepoCheck) deploypolicy.Decision
	// AllowsControlPlane reports whether an operator granted the service the
	// control_plane exception, which lifts the stopgap for it.
	AllowsControlPlane(ctx context.Context, serviceID string) bool
	// AllowsNetRaw reports whether an operator granted the service the
	// net_raw exception, which keeps raw sockets (D-93).
	AllowsNetRaw(ctx context.Context, serviceID string) bool
}

// OperatorChecker reports whether a user is an instance operator. Satisfied
// by instance.Service.
type OperatorChecker interface {
	IsOperator(ctx context.Context, userID string) (bool, error)
}

// ErrControlPlaneOperatorOnly refuses an interactive deploy to the
// control-plane server by someone who is not an instance operator, while
// apps there still share the control plane's network (D-19 stopgap).
var ErrControlPlaneOperatorOnly = errors.New("deploys to the control-plane server are limited to instance operators " +
	"until app network isolation is enabled (D-19). Assign this service to another server, or ask an instance operator " +
	"to deploy it or to allow it there (Settings → Deploy Policy, control_plane)")

// ErrControlPlaneWebhook refuses a git-push deploy to the control-plane
// server while the stopgap is on: a webhook has no person to check, and the
// webhook secret is shared by the whole instance.
var ErrControlPlaneWebhook = errors.New("git-push deploys to the control-plane server are refused until app network isolation " +
	"is enabled (D-19), unless an instance operator allows this service there (Settings → Deploy Policy, control_plane). " +
	"Deploy it from the panel as an operator, or assign it to another server")

// PanelRole says whether the panel host runs apps: its server role is
// "Control plane and apps" (D-26, D-62). Satisfied by *instance.Service.
type PanelRole interface {
	PanelHostRunsApps(ctx context.Context) bool
}

// ErrPanelHostNoApps refuses any deploy to the panel host while its server
// role is "Control plane only" (D-62).
var ErrPanelHostNoApps = errors.New("this panel's server runs no apps: its role on Instance is Only Toft. " +
	"Assign the service to another server, or change the role to Run apps here too")

// ErrServerUnavailable refuses a deploy whose server could not be loaded,
// rather than deploying to the control-plane server instead.
var ErrServerUnavailable = errors.New("the service's server could not be loaded, so the deploy was not started")

// ErrWildcardNeedsDNS refuses a deploy with a wildcard domain
// ("*.example.com") to a server that can't get a wildcard certificate: the
// control-plane server, or a remote server without a DNS provider.
var ErrWildcardNeedsDNS = errors.New("wildcard domains need the service on a remote server with a DNS provider (Servers → server → DNS provider, then Provision)")

// DNSProviderChecker says whether a server has a DNS provider. Satisfied by
// *server.Service.
type DNSProviderChecker interface {
	HasDNSProvider(ctx context.Context, serverID string) (bool, error)
}

// WithDNSChecker lets deploys with wildcard domains through to servers
// that have a DNS provider. Without it every such deploy is refused.
func (s *Service) WithDNSChecker(c DNSProviderChecker) { s.dns = c }

// AgentGate is the workspace's agent policy (D-100): nil for a request no
// agent makes or one it allows, else an *actor.GateError (refused, or
// waiting for a person). Satisfied by *agent.Service.
type AgentGate interface {
	Check(ctx context.Context, t actor.Target) error
	// ReleaseRollback gives back a rollback an approval covered when the
	// rollback failed before it ran (D-146).
	ReleaseRollback(ctx context.Context, approvalID string)
}

// WithAgentGate puts deploys and rollbacks under the agent policy.
func (s *Service) WithAgentGate(g AgentGate) { s.agents = g }

// CheckAgent is the agent policy for deploying or rolling back serviceID,
// before anything else happens: the deploy and rollback use cases run it
// first, so the API with an agent's token meets the policy the MCP tools
// do. rollbackTo is a rollback's target ("" for a deploy), which an
// approval of the agent's deploy may cover (D-146).
func (s *Service) CheckAgent(ctx context.Context, serviceID, rollbackTo string) error {
	if s.agents == nil {
		return nil
	}
	tool := "deploy"
	if rollbackTo != "" {
		tool = "rollback"
	}
	return s.agents.Check(ctx, actor.Target{Action: "deploy", Tool: toolOf(ctx, tool), ServiceID: serviceID, DeploymentID: rollbackTo})
}

// ReleaseAgentRollback gives back the covered rollback a failed request
// used (D-146).
func (s *Service) ReleaseAgentRollback(ctx context.Context, approvalID string) {
	if s.agents != nil {
		s.agents.ReleaseRollback(ctx, approvalID)
	}
}

// toolOf is the MCP tool behind a call, or def over REST, for the policy's
// rate limits.
func toolOf(ctx context.Context, def string) string {
	if i, ok := actor.IntentFrom(ctx); ok && i.Tool != "" {
		return i.Tool
	}
	return def
}

// SandboxChecker says whether a server can run Sandboxed services (D-95),
// and Sandboxed with a strict network (D-115), as its probe last read it.
// Satisfied by *server.Service.
type SandboxChecker interface {
	Sandboxed(ctx context.Context, serverID string) (server.Level, string, error)
	SandboxedStrict(ctx context.Context, serverID string) (server.Level, string, error)
}

// WithSandboxChecker lets Sandboxed services deploy to servers that can run
// them. Without it every such deploy is refused.
func (s *Service) WithSandboxChecker(c SandboxChecker) { s.sandboxes = c }

// WithUserNames names the people who adopt a server's state in its
// deployments' "triggered by".
func (s *Service) WithUserNames(u UserNamer) { s.users = u }

// EnvVarSwapper sets a service's own plain variable only while it is
// still from, never creating one. Satisfied by envvar.Service.
type EnvVarSwapper interface {
	Swap(ctx context.Context, serviceID, key, from, value string) error
}

// WithEnvVarSwapper lets a compose stack's own IMAGE_TAG follow what went
// live (followLive).
func (s *Service) WithEnvVarSwapper(e EnvVarSwapper) { s.envVars = e }

// AdoptDeployment makes deploymentID, which a frozen server's agent kept
// running (D-113), serviceID's active deployment, recording it when the
// restored panel has no record of it ("Adopted · Alex"). It does nothing
// for a service that isn't on serverID.
func (s *Service) AdoptDeployment(ctx context.Context, actorID, serverID, serviceID, deploymentID, imageTag string, startedAt time.Time) error {
	by := "Adopted"
	if s.users != nil {
		if name, err := s.users.UserName(ctx, actorID); err == nil && name != "" {
			by += " · " + name
		}
	}
	ok, err := s.repo.Adopt(ctx, serverID, serviceID, deploymentID, imageTag, by, startedAt)
	if err != nil || !ok {
		return err
	}
	return s.serviceSvc.UpdateRuntimeStatus(ctx, serviceID, string(StatusRunning), deploymentID)
}

// ErrSandboxUnavailable refuses a deploy of a Sandboxed service (D-95) to a
// server that can't run it, before anything starts.
var ErrSandboxUnavailable = errors.New("this service runs Sandboxed, and its server can't run it")

// ErrRollbackUnsupported refuses RollbackImageTag for a docker-compose
// service: compose deploys build no image to go back to. A compose rollback
// replays the deployment's IMAGE_TAG and commit instead (the rollback handler).
var ErrRollbackUnsupported = errors.New("docker-compose services roll back through IMAGE_TAG and the commit, not a built image")

// ErrDeploymentNotFound is returned when a deployment does not exist or does
// not belong to the service named in the request.
var ErrDeploymentNotFound = errors.New("deployment not found")

// Origin says what started a deployment.
type Origin string

const (
	// OriginInteractive is a person or API client (the zero value).
	OriginInteractive Origin = ""
	// OriginWebhook is a git push webhook.
	OriginWebhook Origin = "webhook"
	// OriginSystem is Toft on its own, with no person: a redeploy once a
	// temporary name's certificate is out (D-150). The control-plane
	// stopgap and a refusal treat it as a push.
	OriginSystem Origin = "system"
)

// DeploymentEvent is the payload published for deployment status changes.
type DeploymentEvent struct {
	Type         string `json:"type"` // "deployment.building" | "deployment.running" | "deployment.failed"
	DeploymentID string `json:"deployment_id"`
	ServiceID    string `json:"service_id"`
	ServiceName  string `json:"service_name"`
	ProjectID    string `json:"project_id"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
}

// NixpacksCommands replace the install and build commands Nixpacks
// detects; "" keeps the detected one. They run inside the build, as a
// Dockerfile's RUN steps do.
type NixpacksCommands struct {
	Install, Build string
}

// TriggerInput carries all the information needed to perform a full build+run pipeline.
type TriggerInput struct {
	ServiceID string
	OrgID     string // used to look up the active Loki URL + event publishing
	// TriggeredBy is how and by whom, as shown: "Manual · Alex", "API · Alex",
	// "Rollback · Alex", "Redeploy · Alex", "Push · alex"; an agent's "Agent · Claude Code
	// for Alex", "Rollback · Agent · Claude Code for Alex" (D-100). CommitMessage is a push's head commit.
	TriggeredBy   string
	CommitMessage string
	ProjectID     string // included in SSE events so the frontend can invalidate project queries
	AddonSlug     string // catalog slug (e.g. "loki", "grafana") — empty for custom services
	// ServerID is the service's assigned server; Server is that server, or
	// nil for the control-plane server (local Docker socket). A ServerID
	// with a nil Server means the server could not be loaded: the deploy is
	// refused rather than sent to the control plane.
	ServerID        string
	Server          *server.Server
	SourceType      string // "git" | "docker-image" | "docker-compose"
	GitRepoURL      string
	GitBranch       string
	BaseDirectory   string
	DockerImage     string
	ComposeContent  string
	ComposeRepoURL  string
	ComposeFilePath string
	// ComposeRouteService is the compose service that receives the service's
	// Domains on Port. Empty means automatic: the compose file's only
	// service, or no routing when it has several (see composeRoute).
	ComposeRouteService string
	// ComposeLimits are the panel's limits per compose service (D-41).
	ComposeLimits map[string]ComposeLimit
	RegistryAuth  *RegistryAuth
	CommitSHA     string
	// CheckoutSHA is the commit a docker-compose deploy checks its repository
	// out at, and a build from the project's own repository builds (empty:
	// the tip of GitBranch). Only API deploys and rollbacks set it: a
	// webhook's CommitSHA is a commit of GitRepoURL, which need not be the
	// compose repository, and a push deploy builds the branch's tip.
	CheckoutSHA string
	// RepositoryID is the project's own repository the service builds from,
	// or takes its compose files from, through git archive (D-77);
	// GitRepoURL and ComposeRepoURL are empty then. GitEventID is the push
	// or merge that started the deploy (one deployment per service each).
	RepositoryID string
	GitEventID   int64
	Domains      []string // hostnames the container should be accessible on
	EnvVars      []string // KEY=VALUE pairs injected into the container
	Port         int
	CPULimit     string
	MemoryLimit  string
	Volumes      []service.VolumeMount // named volumes to create and mount
	// NetworkAlias is the service's DNS alias. Under legacy app networks it
	// is used as is; under isolated ones the network plan reads it from the
	// database.
	NetworkAlias string
	StartCommand string // override container CMD (e.g. MinIO's "server /data ...")
	BuildPack    string // "auto" | "nixpacks" | "dockerfile"
	// Nixpacks replaces the install and build steps Nixpacks detects when
	// set (BuildPack nixpacks only).
	Nixpacks           NixpacksCommands
	HealthCheckPath    string                // e.g. "/health"
	HealthCheckTimeout int                   // seconds; 0 = skip health check
	HealthCheckRetries int                   // number of attempts before failing
	GitUsername        string                // HTTP basic auth username for private repos
	GitToken           string                // HTTP basic auth token/PAT (never logged)
	PreDeployCommands  []string              // shell commands to run on the target host before the pipeline
	PostDeployCommands []PostDeployCommand   // container exec commands to run after the pipeline succeeds
	RequiredEnvVars    []string              // env var keys that must be present before the container starts
	ServiceName        string                // human-readable name for container naming
	EnvironmentName    string                // e.g. "production", "staging" — prefixed on container names
	MigrateCommand     string                // run --rm before container start; empty = skip
	AccessControl      service.AccessControl // Traefik-level auth + IP allowlist
	// RollbackImageTag, when non-empty, skips clone+build and re-uses this pre-built image tag.
	RollbackImageTag string
	// LokiURL, when non-empty, configures the Loki Docker log driver on the container.
	LokiURL string
	// Recreate: stop the previous container before the new one starts
	// (service.EffectiveDeployStrategy is recreate, D-42).
	Recreate bool
	// Origin and ActorID say who started the deploy. ActorID is empty for
	// webhooks.
	Origin  Origin
	ActorID string
	// MoveFrom is set when the deploy moves the service to Server from the
	// server it runs on (move.go); nil for every other deploy.
	MoveFrom *MoveSource
	// Preview: one of a pull request's preview's services (D-97). It never
	// stops previews for room, and is refused when it doesn't fit; its
	// check on the commit is toft/preview/<service>.
	Preview bool
	// FromCreate: the first deploy, started by the service's create
	// (FirstDeploy).
	FromCreate bool

	// netPlan is set by the pipeline (runPipeline): where the service's
	// containers go.
	netPlan netplan.Plan
	// security is set by the pipeline too: how its containers are walled in.
	security rollout.Security
	// check is set by the pipeline too: its container check (containerCheckFor).
	check rollout.ContainerCheck
	// composeNetwork is set by a compose deploy once it picked its route
	// (composeNetworkOf): what its success records; nil for other deploys.
	composeNetwork *string
	// clock is the deploy's time limit (D-125), set by runPipeline.
	clock *deployClock
	// Settings is the service the caller built the input from: what the
	// deploy's settings fingerprint is of (D-159); nil reads it again.
	Settings *service.Service
	// settings is Trigger's read of what the deploy ships (shipped).
	settings *shipped
	// reservedMB is Trigger's read of the memory the service's limits
	// reserve (requestedLimits): what its success records (D-166); nil when
	// the service couldn't be read.
	reservedMB *float64
	// roomChecked says the deploy met the room check (Deploy), or an
	// operator deployed past it: reservedBy.
	roomChecked bool
}

// Service contains the business logic for triggering and managing deployments.
type Service struct {
	users                  UserNamer // optional; nil = "Adopted" without a name
	repo                   Repository
	factory                OrchestratorFactory
	serviceSvc             ServiceStatusUpdater
	domainActivator        DomainActivator        // optional; nil = skip domain activation
	observabilityRegistrar ObservabilityRegistrar // optional; nil = skip auto-registration
	observabilityQuerier   ObservabilityQuerier   // optional; nil = logs stay on the server
	loopbackPorts          LoopbackPorts          // optional; nil = Loki is published nowhere
	eventPublisher         EventPublisher         // optional; nil = skip event broadcasting
	alerts                 AlertNotifier          // optional; nil = no deploy-failed alerts
	shipper                Shipper                // optional; nil = issues are shipped by their reconcile only
	dns                    DNSProviderChecker     // nil = wildcard domains refused
	sandboxes              SandboxChecker         // nil = Sandboxed services refused
	agents                 AgentGate              // nil = no agent policy (tests)
	configFileWriter       ConfigFileWriter       // optional; nil = skip config file injection
	orgSettingsSvc         OrgSettingsReader      // optional; nil = skip capacity gate
	capacitySvc            CapacitySummarizer     // optional; nil = skip capacity gate
	serviceFetcher         ServiceReader          // optional; nil = skip existing-alloc subtraction
	policy                 PolicyChecker          // optional in tests; main always wires it
	resources              ResourceLimits         // optional in tests; main always wires it
	sharedMounts           SharedMountProvider    // optional; nil mounts none
	envVars                EnvVarSwapper          // nil = a compose stack's IMAGE_TAG is left as it is
	hashKey                []byte                 // nil: no settings fingerprints (D-159)
	operators              OperatorChecker        // used by the control-plane stopgap
	panelRole              PanelRole              // nil = the panel host runs apps
	hostUpdates            HostUpdateGate         // nil = deploys don't wait for host updates (D-134)
	diskCleans             DiskCleanGate          // nil = deploys don't wait for disk cleans (D-135)
	buildCache             BuildCacheTrimmer      // nil = nothing trims the build cache after a build (D-135)
	controlPlaneStopgap    bool                   // D-19: interactive control-plane deploys are operator-only
	serverReader           ServerReader           // recovery: loads a service's server
	repos                  RepoSource             // nil: no git hosting, so no builds from its repositories
	mover                  ServiceMover           // nil = move deploys refused
	evictor                Evictor                // nil = no previews to stop for room
	panelCapacity          PanelCapacity          // nil = the panel's server has no ledger
	copies                 PreviewCopies          // nil = no service is a preview's
	// App networks (TOFT_APP_NETWORKS). Under legacy every deploy joins
	// controlPlaneNetwork with its alias, as before; under isolated the
	// planner decides and a missing planner fails the deploy.
	appNetworksIsolated bool
	controlPlaneNetwork string
	legacyNetworks      []string
	netPlanner          NetworkPlanner
	log                 *zap.Logger

	// What the deploy use case (Deploy) calls in other modules
	// (WithDeployUseCase); versionCache is optional, nil = no cache to clear.
	serviceManager ServiceManagerOps
	serverSvc      *server.Service
	domainSvc      *domainPkg.Service
	envVarProvider EnvVarProvider
	envVarSetter   EnvVarSetter
	envSvc         *envPkg.Service
	orgSvc         OrgAuditWriter
	versionCache   VersionCacheInvalidator

	// buildSlots bounds how many deploys clone and build at once
	// (WithBuildSlots); nil means no bound.
	buildSlots chan struct{}
	// inflight holds the IDs of the deployments whose pipeline runs in this
	// process, which recovery must not mark as interrupted.
	inflight sync.Map
	// moving holds, per deployment that moves its service, the server it
	// moves to ("" the panel's): services.server_id changes only once the
	// move succeeds, so DeployRunning reads the target here (D-135).
	moving sync.Map
	// created holds, per service, the first deploy its create started
	// (firstDeploy), while that pipeline runs: see FirstDeploy.
	// ponytail: in memory, so a panel restart mid-deploy forgets it and such
	// a client's deploy starts a second one; a column on deployments if
	// that ever matters.
	created sync.Map
	// turns holds, per service, the last deploy in its line (takeTurn).
	turnMu sync.Mutex
	turns  map[string]*turn
	// shuttingDown is set by BeginShutdown: a pipeline that fails from then
	// on is left for the next start's recovery (leaveForRecovery).
	shuttingDown atomic.Bool
}

// RepoSource is the project's own repositories (D-77): a commit's tree for
// a build, and the check each deploy reports on the commit it built.
// Satisfied by *code.Service.
type RepoSource interface {
	Archive(ctx context.Context, projectID, repositoryID, ref string) (*gitstore.Archive, error)
	ReportDeploy(ctx context.Context, repositoryID, sha, environment, service, state, description, deploymentID string, preview bool)
	FailDeployChecks(ctx context.Context, deploymentIDs []string, description string)
}

// WithRepositories lets services build from the project's own repositories.
func (s *Service) WithRepositories(r RepoSource) { s.repos = r }

// ErrAlreadyDeployed: this push or merge already started a deployment of
// the service (a dispatch run twice).
var ErrAlreadyDeployed = errors.New("this push already deployed the service")

func NewService(repo Repository, factory OrchestratorFactory, serviceSvc ServiceStatusUpdater, log *zap.Logger) *Service {
	return &Service{repo: repo, factory: factory, serviceSvc: serviceSvc, log: log}
}

// WithBuildSlots lets at most n deploys clone and build at the same time.
// Clones and builds run inside the backend's container, under its memory
// limit; without a bound a push to a project with several services, or a
// few members deploying together, ran them all at once.
func (s *Service) WithBuildSlots(n int) {
	if n < 1 {
		s.buildSlots = nil
		return
	}
	s.buildSlots = make(chan struct{}, n)
}

// acquireBuildSlot waits for a build slot, telling the deploy log when it
// has to wait, and returns the release. false when ctx ended first. The
// wait doesn't count toward the deploy's time limit (clock, D-125): it is
// bounded by the deploys ahead, which have limits of their own.
func (s *Service) acquireBuildSlot(ctx context.Context, clock *deployClock, appendLog func(string)) (release func(), ok bool) {
	if s.buildSlots == nil {
		return func() {}, true
	}
	select {
	case s.buildSlots <- struct{}{}:
	default:
		appendLog(fmt.Sprintf("Waiting for a build slot (%d builds running)...", len(s.buildSlots)))
		clock.pause()
		defer clock.resume()
		started := time.Now()
		select {
		case s.buildSlots <- struct{}{}:
			appendLog(fmt.Sprintf("Got a build slot after %s; the wait doesn't count toward the deploy time limit", time.Since(started).Round(time.Second)))
		case <-ctx.Done():
			return func() {}, false
		}
	}
	return func() { <-s.buildSlots }, true
}

// turn is a deploy's place in its service's line.
type turn struct {
	done chan struct{} // closed when its pipeline has ended
	move bool          // it moves the service to another server
}

// ErrServiceMoving refuses a deploy while the service moves to another
// server: it was aimed at the server the service is leaving.
var ErrServiceMoving = errors.New("the service is moving to another server: deploy it again once the move has finished")

// takeTurn puts a deploy of serviceID in line (move: it moves the service).
// A service's deploys run one after the other, so two never switch its
// containers or run its migrations at the same time: ahead is closed once
// the deploy before this one has ended (nil when none is under way), and
// the caller calls done when this one has. A deploy behind a move is
// refused (ErrServiceMoving); a move waits like any other deploy. A turn
// given up while the deploy ahead still runs (a deploy refused after it
// took its turn) stays in the line until that one has ended, so the next
// deploy still waits for it.
// ponytail: the line is in this process's memory, which recovery already
// assumes is the only one (recovery.go); a row lock or an advisory lock per
// service if the panel ever runs as more than one instance.
func (s *Service) takeTurn(serviceID string, move bool) (ahead <-chan struct{}, done func(), err error) {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	last := s.turns[serviceID]
	if last != nil {
		if last.move {
			return nil, nil, ErrServiceMoving
		}
		ahead = last.done
	}
	mine := &turn{done: make(chan struct{}), move: move}
	if s.turns == nil {
		s.turns = make(map[string]*turn)
	}
	s.turns[serviceID] = mine
	end := func() {
		s.turnMu.Lock()
		defer s.turnMu.Unlock()
		if s.turns[serviceID] == mine {
			delete(s.turns, serviceID)
		}
		close(mine.done)
	}
	return ahead, func() {
		if ahead != nil {
			select {
			case <-ahead:
			default:
				go func() { // ends with the deploy ahead
					<-ahead
					end()
				}()
				return
			}
		}
		end()
	}, nil
}

// waitTurn waits for the deploy of the service ahead of this one (takeTurn)
// to end, telling the deploy log when it has to wait. The deployment stays
// pending and its time limit hasn't started (D-125): the wait is as long
// as the deploy ahead takes. false when it is not to run: the deployment is
// gone by then (its service was deleted while it waited), or the panel is
// shutting down, which may be what ended the deploy ahead. It then stays
// pending, and the next start records it as never started.
func (s *Service) waitTurn(deploymentID string, ahead <-chan struct{}) bool {
	if ahead == nil {
		return true
	}
	select {
	case <-ahead:
	default:
		if _, err := s.repo.InsertLog(context.Background(), deploymentID, "Waiting for this service's earlier deploy to finish..."); err != nil {
			s.log.Warn("failed to insert deployment log", zap.Error(err))
		}
		<-ahead
	}
	if s.shuttingDown.Load() {
		s.log.Info("deploy not started: the panel is shutting down", zap.String("deployment_id", deploymentID))
		return false
	}
	_, err := s.repo.GetByID(context.Background(), deploymentID)
	if errors.Is(err, ErrDeploymentNotFound) {
		s.log.Info("deploy not started: its service was deleted while it waited", zap.String("deployment_id", deploymentID))
		return false
	}
	if err != nil {
		s.log.Warn("could not read a deployment after its wait; deploying it", zap.String("deployment_id", deploymentID), zap.Error(err))
	}
	return true
}

// WithDomainActivator injects the domain activator (called after successful deployments).
func (s *Service) WithDomainActivator(da DomainActivator) {
	s.domainActivator = da
}

// WithObservabilityQuerier gives deploys the Loki their containers log to.
func (s *Service) WithObservabilityQuerier(oq ObservabilityQuerier) {
	s.observabilityQuerier = oq
}

// WithLoopbackPorts lets the catalog Loki's deploy publish it on 127.0.0.1
// of its server (D-148); without it Loki is published nowhere.
func (s *Service) WithLoopbackPorts(lp LoopbackPorts) {
	s.loopbackPorts = lp
}

// WithObservabilityRegistrar injects the observability registrar for auto-wiring PLG services.
func (s *Service) WithObservabilityRegistrar(or ObservabilityRegistrar) {
	s.observabilityRegistrar = or
}

// WithEventPublisher injects the SSE event broadcaster.
// AlertNotifier is told about every failed deploy. Satisfied by
// *alert.Service, which queues and never blocks.
type AlertNotifier interface {
	DeployFailed(serviceID, deploymentID, errMsg string)
}

// WithAlerts sends a deploy-failed alert for every failed deploy.
func (s *Service) WithAlerts(a AlertNotifier) { s.alerts = a }

// Shipper hears of every deploy that went live, to ship the issues its
// merge closes (D-81), and of every deploy that failed, for the issues a
// project opens from them (D-88). Satisfied by *issue.Service; its own
// reconcile catches what a restart loses of the first.
type Shipper interface {
	DeployLive(ctx context.Context, serviceID string)
	DeployFailed(ctx context.Context, serviceID, deploymentID string)
}

// WithShipper tells the issues module of every deploy that goes live.
func (s *Service) WithShipper(sh Shipper) { s.shipper = sh }

func (s *Service) WithEventPublisher(ep EventPublisher) {
	s.eventPublisher = ep
}

// WithConfigFileWriter injects the config file writer for pre-deploy file injection.
func (s *Service) WithConfigFileWriter(cfw ConfigFileWriter) {
	s.configFileWriter = cfw
}

// WithOrgSettingsSvc injects the org settings reader for the capacity gate.
func (s *Service) WithOrgSettingsSvc(r OrgSettingsReader) { s.orgSettingsSvc = r }

// WithCapacitySvc injects the capacity summarizer for the capacity gate.
func (s *Service) WithCapacitySvc(r CapacitySummarizer) { s.capacitySvc = r }

// WithServiceReader injects the service fetcher used to subtract a service's
// existing allocation before evaluating the capacity gate.
func (s *Service) WithServiceReader(r ServiceReader) { s.serviceFetcher = r }

// NetworkPlanner computes a service's network plan. Satisfied by
// *service.ServiceManager.
type NetworkPlanner interface {
	NetworkPlan(ctx context.Context, serviceID string) (netplan.Plan, error)
}

// WithAppNetworks sets the app network mode. controlPlaneNetwork is
// TRAEFIK_NETWORK, which legacy deploys join; under isolated app networks
// deploys use planner's plans and compose references to the control plane's
// network names (netplan.LegacyNetwork and controlPlaneNetwork) are pointed
// at the environment's network.
func (s *Service) WithAppNetworks(isolated bool, controlPlaneNetwork string, planner NetworkPlanner) {
	if controlPlaneNetwork == "" {
		controlPlaneNetwork = netplan.LegacyNetwork
	}
	s.appNetworksIsolated = isolated
	s.controlPlaneNetwork = controlPlaneNetwork
	s.legacyNetworks = []string{netplan.LegacyNetwork}
	if controlPlaneNetwork != netplan.LegacyNetwork {
		s.legacyNetworks = append(s.legacyNetworks, controlPlaneNetwork)
	}
	s.netPlanner = planner
}

// networkPlan returns where input's containers go. Under legacy it is the
// control plane's network with the service's alias, exactly as before app
// networks; under isolated it is the planner's plan, and without a planner
// or a plan the deploy fails: nothing falls back to the control plane's
// network.
func (s *Service) networkPlan(ctx context.Context, input TriggerInput) (netplan.Plan, error) {
	if !s.appNetworksIsolated {
		network := s.controlPlaneNetwork
		if network == "" {
			network = netplan.LegacyNetwork
		}
		return netplan.LegacyPlan(input.ServiceID, input.ServiceName, input.NetworkAlias, network), nil
	}
	if s.netPlanner == nil {
		return netplan.Plan{}, errors.New("network planner not configured")
	}
	plan, err := s.netPlanner.NetworkPlan(ctx, input.ServiceID)
	if err != nil {
		return netplan.Plan{}, err
	}
	if plan.Legacy {
		return netplan.Plan{}, errors.New("the network planner returned a legacy plan under isolated app networks")
	}
	if err := plan.Valid(); err != nil {
		return netplan.Plan{}, err
	}
	return plan, nil
}

// ResourceLimits is the memory rule's data (D-41). Satisfied by
// *resources.Service.
type ResourceLimits interface {
	MemoryCheckFor(ctx context.Context, ref resources.ServiceRef) (*resources.MemoryCheck, error)
	AddComposeDefaults(ctx context.Context, serviceID string, src composespec.Source) (map[string]resources.DefaultAdded, error)
	ComposeLimits(ctx context.Context, serviceID string) (map[string]resources.ComposeLimit, error)
}

// WithResources checks memory limits on every deploy, gives a new
// repository stack's compose services their default, and runs compose with
// the panel's limits. Without it none of that happens.
func (s *Service) WithResources(r ResourceLimits) { s.resources = r }

// WithPolicyChecker enables the deploy policy on every deploy.
func (s *Service) WithPolicyChecker(p PolicyChecker) { s.policy = p }

// WithSharedMounts mounts services' shared volumes at deploy.
func (s *Service) WithSharedMounts(p SharedMountProvider) { s.sharedMounts = p }

// WithControlPlaneStopgap limits deploys to the control-plane server
// (services with no server) while enabled, that is while app network
// isolation is not on (TOFT_APP_NETWORKS != isolated): interactive deploys
// are operator-only and webhook deploys are refused, unless an operator
// granted the service the control_plane exception. Without an operator
// checker every interactive deploy there is refused (fail closed).
func (s *Service) WithControlPlaneStopgap(enabled bool, ops OperatorChecker) {
	s.controlPlaneStopgap = enabled
	s.operators = ops
}

// WithPanelRole refuses deploys to the panel host while its role is
// Control plane only.
func (s *Service) WithPanelRole(r PanelRole) { s.panelRole = r }

// HostUpdateGate says whether a Docker update or a reboot runs on a host,
// "" the panel's own (D-134): deploys there wait for it.
type HostUpdateGate interface {
	HostUpdating(ctx context.Context, serverID string) (bool, error)
}

// ErrServerUpdating refuses a deploy while the server's Docker updates or
// the server reboots (D-134).
var ErrServerUpdating = errors.New("the server's Docker is updating or the server is rebooting: deploy again once that's done")

// WithHostUpdates has deploys wait for a host's Docker update or reboot.
func (s *Service) WithHostUpdates(g HostUpdateGate) { s.hostUpdates = g }

// checkHostUpdates refuses a deploy to, or a move from, a host whose Docker
// updates or which reboots, or which a disk clean is cleaning (D-135).
func (s *Service) checkHostUpdates(ctx context.Context, input TriggerInput) error {
	ids := []string{input.ServerID}
	if input.MoveFrom != nil {
		ids = append(ids, input.MoveFrom.ServerID)
	}
	for _, id := range ids {
		if s.hostUpdates != nil {
			updating, err := s.hostUpdates.HostUpdating(ctx, id)
			if err != nil {
				return fmt.Errorf("check the server's updates: %w", err)
			}
			if updating {
				return ErrServerUpdating
			}
		}
		if s.diskCleans != nil {
			cleaning, err := s.diskCleans.Cleaning(ctx, id)
			if err != nil {
				return fmt.Errorf("check the server's disk clean: %w", err)
			}
			if cleaning {
				return ErrDiskCleaning
			}
		}
	}
	return nil
}

// DiskCleanGate says whether a disk clean runs on a host, "" the panel's
// own (D-135): deploys there wait for it, as it may remove an image a
// deploy is about to start.
type DiskCleanGate interface {
	Cleaning(ctx context.Context, serverID string) (bool, error)
}

// ErrDiskCleaning refuses a deploy while a disk clean runs on its server.
var ErrDiskCleaning = errors.New("a disk clean is running on the server: deploy again once it's done")

// WithDiskCleans has deploys wait for a host's disk clean.
func (s *Service) WithDiskCleans(g DiskCleanGate) { s.diskCleans = g }

// BuildCacheTrimmer trims a host's build cache to its limit (D-135).
type BuildCacheTrimmer interface {
	AfterBuild(serverID string)
}

// WithBuildCacheTrim trims a host's build cache after each deploy that
// built on it.
func (s *Service) WithBuildCacheTrim(t BuildCacheTrimmer) { s.buildCache = t }

// DeployRunning reports whether a deploy is under way to the server, "" the
// panel's own (D-134): a Docker update or a reboot waits for it, and a
// disk clean (D-135). A move counts on the server it moves to.
func (s *Service) DeployRunning(ctx context.Context, serverID string) (bool, error) {
	moving := false
	s.moving.Range(func(_, to any) bool {
		moving = to.(string) == serverID
		return !moving
	})
	if moving {
		return true, nil
	}
	return s.repo.DeployRunningOnServer(ctx, serverID)
}

// AuthorizeTarget applies the panel host's server role and the
// control-plane stopgap to a deploy of serviceID that would run on the
// control-plane server (srv == nil). Under Control plane only every such
// deploy gets ErrPanelHostNoApps. Otherwise it returns nil when the stopgap
// is off, the target is another server, or an operator granted the service
// the control_plane exception; a webhook deploy gets ErrControlPlaneWebhook,
// and an interactive deploy by someone who is not an instance operator gets
// ErrControlPlaneOperatorOnly.
func (s *Service) AuthorizeTarget(ctx context.Context, origin Origin, actorID, serviceID string, srv *server.Server) error {
	if srv == nil && s.panelRole != nil && !s.panelRole.PanelHostRunsApps(ctx) {
		return ErrPanelHostNoApps
	}
	if !s.controlPlaneStopgap || srv != nil {
		return nil
	}
	if s.policy != nil && serviceID != "" && s.policy.AllowsControlPlane(ctx, serviceID) {
		return nil
	}
	if origin == OriginWebhook || origin == OriginPreview || origin == OriginSystem {
		return ErrControlPlaneWebhook
	}
	if s.operators == nil || actorID == "" {
		return ErrControlPlaneOperatorOnly
	}
	ok, err := s.operators.IsOperator(ctx, actorID)
	if err != nil {
		return fmt.Errorf("check operator: %w", err)
	}
	if !ok {
		return ErrControlPlaneOperatorOnly
	}
	return nil
}

// policyCheck runs the deploy policy on everything known before the
// pipeline starts. A nil decision means no policy is configured. A
// repository's compose file is checked after the clone, its memory limits
// too.
func (s *Service) policyCheck(ctx context.Context, input TriggerInput) (*deploypolicy.Decision, error) {
	if s.policy == nil {
		return nil, nil
	}
	var memory *resources.MemoryCheck
	if input.SourceType != "docker-compose" || input.ComposeRepoURL == "" {
		var src *composespec.Source
		if input.SourceType == "docker-compose" {
			inline := deploypolicy.InlineComposeSource(input.ServiceID, input.ServiceName, input.ComposeContent, kvsToMap(input.EnvVars))
			src = &inline
		}
		var err error
		if memory, err = s.memoryCheck(ctx, input, src); err != nil {
			return nil, err
		}
	}
	vols := make([]deploypolicy.VolumeSpec, len(input.Volumes))
	for i, v := range input.Volumes {
		vols[i] = deploypolicy.VolumeSpec{Name: v.Name, Target: v.Target, HostPath: v.HostPath}
	}
	image := deploypolicy.ServiceImage(input.SourceType, input.DockerImage, input.GitRepoURL, input.RepositoryID)
	if input.SourceType == deploypolicy.SourceImage && input.RollbackImageTag != "" {
		image = input.RollbackImageTag // the image reference that deployment ran
	}
	d := s.policy.CheckDeploy(ctx, deploypolicy.DeployCheck{
		ServiceID:         input.ServiceID,
		ServiceName:       input.ServiceName,
		SourceType:        input.SourceType,
		Image:             image,
		ComposeContent:    input.ComposeContent,
		ComposeFromRepo:   input.ComposeRepoURL != "",
		EnvVars:           input.EnvVars,
		Volumes:           vols,
		PreDeployCommands: input.PreDeployCommands,
		Memory:            memory,
	})
	return &d, nil
}

// memoryCheck is the memory rule's input for a deploy of input (D-41); src
// is its compose file, when known. nil without the resources service.
func (s *Service) memoryCheck(ctx context.Context, input TriggerInput, src *composespec.Source) (*resources.MemoryCheck, error) {
	if s.resources == nil {
		return nil, nil
	}
	m, err := s.resources.MemoryCheckFor(ctx, resources.ServiceRef{
		ID: input.ServiceID, SourceType: input.SourceType, MemoryLimit: input.MemoryLimit,
		AddonSlug: input.AddonSlug, DockerImage: input.DockerImage, Compose: src,
	})
	if err != nil {
		return nil, fmt.Errorf("read the service's memory limits: %w", err)
	}
	return m, nil
}

// repoComposeMemory gives a new repository stack's compose services without
// a memory limit their kind's default (AddComposeDefaults: only without a
// grace date) and returns what it added and the memory rule's input for the
// file in src. Both load the file, so the repository is scanned first
// (composespec.ScanRepo: a symlink out of it would be followed); when the
// scan reports anything nothing is loaded, and CheckComposeRepo refuses it.
func (s *Service) repoComposeMemory(ctx context.Context, input TriggerInput, src composespec.Source) (map[string]resources.DefaultAdded, *resources.MemoryCheck, error) {
	if s.resources == nil {
		return nil, nil, nil
	}
	if vs, err := composespec.ScanRepo(src.Root); err != nil || len(vs) > 0 {
		return nil, nil, nil
	}
	added, err := s.resources.AddComposeDefaults(ctx, input.ServiceID, src)
	if err != nil {
		return nil, nil, fmt.Errorf("add default memory limits: %w", err)
	}
	m, err := s.memoryCheck(ctx, input, &src)
	return added, m, err
}

// composeLimits are the panel's limits per compose service, as compose runs
// with them (D-41). deployLimits are the services whose file declares
// deploy.resources.limits (the policy's load of the file).
func (s *Service) composeLimits(ctx context.Context, serviceID string, deployLimits map[string]bool) (map[string]ComposeLimit, error) {
	if s.resources == nil {
		return nil, nil
	}
	panel, err := s.resources.ComposeLimits(ctx, serviceID)
	if err != nil {
		return nil, fmt.Errorf("read the compose services' memory limits: %w", err)
	}
	out := make(map[string]ComposeLimit, len(panel))
	for name, l := range panel {
		if l.MemoryMB <= 0 {
			continue // the panel always has one (memory_mb >= 64); never write mem_limit 0
		}
		cl := ComposeLimit{MemoryMB: l.MemoryMB, DeployLimits: deployLimits[name]}
		if l.CPUCores != nil {
			cl.CPUCores = *l.CPUCores
		}
		out[name] = cl
	}
	// The process limit mirrors the file's form too (D-93), panel limit or not.
	for name, d := range deployLimits {
		if _, ok := out[name]; d && !ok {
			out[name] = ComposeLimit{DeployLimits: true}
		}
	}
	return out, nil
}

// roleRank returns a numeric rank for a member role so roles can be compared.
// Hierarchy: owner (2) > developer (1) > viewer / unknown (0).
func roleRank(r string) int {
	switch r {
	case "owner":
		return 2
	case "developer":
		return 1
	default:
		return 0
	}
}

// roleCanBypass returns true if memberRole is at least as privileged as bypassMinRole.
func roleCanBypass(memberRole, bypassMinRole string) bool {
	return roleRank(memberRole) >= roleRank(bypassMinRole)
}

// requestedLimits is what a service asks of its server: a single container's
// cpu and memory limit strings, or a compose stack's panel limits summed over
// its compose services (limits set only in the compose file count 0, as in
// the server totals). Both the gate's request and the existing allocation it
// subtracts come from here, so they always agree.
func (s *Service) requestedLimits(ctx context.Context, svc *service.Service) (cpuCores, memoryMB float64) {
	if svc.SourceType != service.SourceTypeDockerCompose {
		cpuCores, _ = strconv.ParseFloat(svc.CPULimit, 64)
		return cpuCores, service.ParseMemoryMB(svc.MemoryLimit)
	}
	if s.resources == nil {
		return 0, 0
	}
	limits, err := s.resources.ComposeLimits(ctx, svc.ID)
	if err != nil {
		return 0, 0 // unknown limits: the gate never blocks on a read failure
	}
	for _, l := range limits {
		memoryMB += float64(l.MemoryMB)
		if l.CPUCores != nil {
			cpuCores += *l.CPUCores
		}
	}
	return cpuCores, memoryMB
}

// checkCapacity verifies that deploying the service will not exceed the target server's
// effective resource limits. Returns a non-nil *server.CapacityExceededError when the gate trips,
// or a non-nil error when the check itself failed fatally. Either nil return means proceed.
// CanBypass says whether userID may deploy past it: on the workspace's own server, a
// member whose role reaches the workspace's bypass setting; on a share of another
// workspace's server, only an owner of that workspace, since the share protects its
// services. The panel's own server (serverID "") has its own gate, checkPanelCapacity.
func (s *Service) checkCapacity(
	ctx context.Context,
	serverID, currentServiceID, userID, memberRole, orgID string,
	requestedCPU, requestedMemMB float64,
) (*server.CapacityExceededError, error) {
	if serverID == "" {
		return s.checkPanelCapacity(ctx, currentServiceID, userID, requestedMemMB), nil
	}
	if s.capacitySvc == nil || s.orgSettingsSvc == nil {
		return nil, nil
	}

	summary, err := s.capacitySvc.GetCapacitySummaryFor(ctx, serverID, orgID)
	if err != nil {
		return nil, nil // capacity unknown — allow
	}
	if !summary.CapacityDiscovered {
		return nil, nil
	}

	settings, err := s.orgSettingsSvc.GetSettings(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("get org settings: %w", err)
	}

	// Subtract the current service's existing allocation (it's being redeployed),
	// floored at zero to prevent negative values from racing reads.
	existCPU, existMem := s.ownAllocation(ctx, currentServiceID)
	summary.AllocatedCPUCores = max(summary.AllocatedCPUCores-existCPU, 0)
	summary.AllocatedMemoryMB = max(summary.AllocatedMemoryMB-existMem, 0)

	if cpuOK, memOK := summary.Fits(requestedCPU, requestedMemMB); cpuOK && memOK {
		return nil, nil
	}

	canBypass := roleCanBypass(memberRole, settings.ResourceBypassMinRole)
	if summary.Share {
		role, err := s.orgSettingsSvc.RoleIn(ctx, userID, summary.OwnerOrgID)
		canBypass = err == nil && role == orgPkg.RoleOwner
	}
	return &server.CapacityExceededError{
		Code:              "capacity_exceeded",
		Server:            summary.ServerName,
		CanBypass:         canBypass,
		EffectiveCPUCores: float64(summary.EffectiveCPUCores),
		EffectiveMemoryMB: float64(summary.EffectiveMemoryMB),
		AllocatedCPUCores: summary.AllocatedCPUCores,
		AllocatedMemoryMB: summary.AllocatedMemoryMB,
		RequestedCPUCores: requestedCPU,
		RequestedMemoryMB: requestedMemMB,
	}, nil
}

// checkPanelCapacity is the gate on the panel's own server: the memory it
// leaves its apps (D-62) against what every workspace's services reserve
// there, the ledger previews are counted in (room). The apps share one
// memory cap, so past it a service is killed for memory before it reaches
// its own limit. CPU is not counted: the ledger is memory only. A running
// service deployed again with no more than its live deployment reserved
// (heldLive) is never refused: it takes no new room, and servers were
// filled past the ledger before this gate existed. A raised limit, or a
// service that never went live, is new room. Only an instance operator deploys past it, since the server
// is every workspace's. A ledger that can't say refuses nothing.
func (s *Service) checkPanelCapacity(ctx context.Context, currentServiceID, userID string, requestedMemMB float64) *server.CapacityExceededError {
	if s.panelCapacity == nil || requestedMemMB == 0 {
		return nil
	}
	apps, err := s.panelCapacity.AppsMemoryMB(ctx)
	if err != nil {
		s.log.Warn("the panel's server's room check is skipped: its apps' memory can't be read", zap.Error(err))
		return nil
	}
	if apps <= 0 {
		return nil
	}
	allocated, err := s.panelCapacity.AllocatedMemoryMB(ctx)
	if err != nil {
		s.log.Warn("the panel's server's room check is skipped: what is reserved there can't be read", zap.Error(err))
		return nil
	}
	_, own := s.ownAllocation(ctx, currentServiceID)
	allocated = max(allocated-own, 0)
	if allocated+requestedMemMB <= apps {
		return nil
	}
	if held, known := s.heldLive(ctx, currentServiceID); !known || requestedMemMB <= held {
		return nil
	}
	canBypass := false
	if s.operators != nil && userID != "" {
		canBypass, _ = s.operators.IsOperator(ctx, userID)
	}
	return &server.CapacityExceededError{
		Code: "capacity_exceeded", Server: "the panel's server", PanelHost: true, CanBypass: canBypass,
		EffectiveMemoryMB: apps, AllocatedMemoryMB: allocated, RequestedMemoryMB: requestedMemMB,
	}
}

// heldLive is the memory serviceID holds on its server through its live
// deployment: what that deploy reserved, or the service's limits as they
// are when it went live before deploys recorded it (0082). 0 for no service
// (a new one, or one moving in) and for one that is stopped or never went
// live. known is false when it can't be read: the check then refuses
// nothing, as with a ledger that can't say.
func (s *Service) heldLive(ctx context.Context, serviceID string) (memoryMB float64, known bool) {
	if serviceID == "" {
		return 0, true
	}
	if s.serviceFetcher == nil || s.repo == nil {
		return 0, false
	}
	svc, err := s.serviceFetcher.Get(ctx, serviceID)
	if err != nil || svc == nil {
		return 0, false
	}
	if svc.RuntimeStatus == "stopped" || svc.ActiveDeploymentID == "" {
		return 0, true
	}
	live, err := s.repo.GetByID(ctx, svc.ActiveDeploymentID)
	if err != nil || live == nil {
		return 0, false
	}
	if live.ReservedMemoryMB == nil {
		_, mem := s.requestedLimits(ctx, svc)
		return mem, true
	}
	return *live.ReservedMemoryMB, true
}

// reservedBy is the memory input's service reserves with its limits as
// they are, for its deployment to record once live; nil when the service
// can't be read. A deploy that met no room check (a rollback, a push)
// records no more than the service held when the panel's server has no
// room for the rest: a raise the check refused isn't granted by deploying
// another way.
func (s *Service) reservedBy(ctx context.Context, input TriggerInput) *float64 {
	svc := input.Settings
	if svc == nil {
		if s.serviceFetcher == nil || input.ServiceID == "" {
			return nil
		}
		var err error
		if svc, err = s.serviceFetcher.Get(ctx, input.ServiceID); err != nil || svc == nil {
			return nil
		}
	}
	_, mem := s.requestedLimits(ctx, svc)
	if !input.roomChecked && svc.ServerID == "" && s.checkPanelCapacity(ctx, svc.ID, "", mem) != nil {
		mem, _ = s.heldLive(ctx, svc.ID) // known, or the check had refused nothing
	}
	return &mem
}

// CheckNewCapacity is the capacity gate for svc, a service not created yet
// that is to be deployed once it is (service.CapacityGate). A compose
// stack's limits are stored once it exists, so it asks for composeMemoryMB,
// the default limits it will be given then (resources.NewComposeDefaults).
func (s *Service) CheckNewCapacity(ctx context.Context, svc *service.Service, composeMemoryMB float64, userID, memberRole, orgID string) (*server.CapacityExceededError, error) {
	cpu, mem := 0.0, composeMemoryMB
	if svc.SourceType != service.SourceTypeDockerCompose {
		cpu, mem = s.requestedLimits(ctx, svc)
	}
	return s.checkCapacity(ctx, svc.ServerID, "", userID, memberRole, orgID, cpu, mem)
}

// Trigger starts a new deployment for a service.
// The build runs in a goroutine so the HTTP request returns immediately (202 Accepted).
//
// Before anything runs it refuses a deploy whose server could not be loaded
// (ErrServerUnavailable), a rollback of a compose service
// (ErrRollbackUnsupported), a deploy the control-plane stopgap does not allow
// (AuthorizeTarget) and, in enforce mode, a deploy the policy refuses
// (*composespec.ViolationsError). An interactive deploy that is refused
// creates no deployment. A webhook deploy that is refused is recorded as a
// failed deployment whose log says why, so a push is never silently
// ignored; Trigger returns it with the error. A compose file from a
// repository is checked in the pipeline, after the clone.
func (s *Service) Trigger(ctx context.Context, input TriggerInput) (*Deployment, error) {
	// Before TOFT_URL, which is routing (D-159). Unknown, the deployment
	// records none, and Toft's redeploy for a certificate waits for a person.
	input.settings = s.shippedSettings(ctx, input)
	input.reservedMB = s.reservedBy(ctx, input)
	if input.SourceType == "docker-compose" {
		input.EnvVars = withServiceURL(input.EnvVars, input.Domains, input.AccessControl.Certified)
	}
	decision, refusal := s.precheck(ctx, input)
	var ahead <-chan struct{}
	var done func()
	if refusal == nil {
		ahead, done, refusal = s.takeTurn(input.ServiceID, input.MoveFrom != nil)
	}
	// A move that ended between the caller's read of the service and this
	// turn: the input still names the server the service has left, or
	// carries it (a push reads the server before it reads the service).
	if refusal == nil && input.MoveFrom == nil && s.serviceFetcher != nil {
		if svc, err := s.serviceFetcher.Get(ctx, input.ServiceID); err == nil &&
			(svc.ServerID != input.ServerID || input.Server != nil && input.Server.ID != svc.ServerID) {
			done()
			refusal = ErrServiceMoving
		}
	}
	if refusal != nil {
		if input.Origin == OriginWebhook || input.Origin == OriginPreview || input.Origin == OriginSystem {
			return s.recordRefusal(ctx, input, decision, refusal)
		}
		return nil, refusal
	}
	// Until the pipeline's goroutine has the turn, a return gives it up.
	started := false
	defer func() {
		if !started {
			done()
		}
	}()
	s.MakeRoom(ctx, input)
	d, err := s.repo.Create(ctx, input.ServiceID, input.TriggeredBy, input.CommitMessage, input.GitEventID)
	if errors.Is(err, ErrAlreadyDeployed) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("deployment.Service.Trigger: create record: %w", err)
	}
	if input.MoveFrom != nil {
		s.moving.Store(d.ID, input.ServerID) // before the check below: see DeployRunning
	}
	// A Docker update or a reboot that began between precheck and the
	// record: server.Service.StartUpdates checks the other way after its
	// own write, so one of the two sees the other.
	if err := s.checkHostUpdates(ctx, input); err != nil {
		s.moving.Delete(d.ID)
		if _, lerr := s.repo.InsertLog(ctx, d.ID, "✗ "+err.Error()); lerr != nil {
			s.log.Warn("failed to insert deployment log", zap.Error(lerr))
		}
		if _, uerr := s.repo.UpdateStatus(ctx, d.ID, StatusFailed, "", input.CommitSHA, err.Error()); uerr != nil {
			s.log.Error("failed to update deployment status to failed", zap.Error(uerr), zap.String("deployment_id", d.ID))
		}
		s.publishFailed(input, d.ID, err.Error())
		return nil, err
	}
	s.inflight.Store(d.ID, struct{}{})
	if input.FromCreate && input.settings != nil {
		s.created.Store(input.ServiceID, firstDeploy{d.ID, input.settings.parts})
	}
	started = true
	go func() {
		defer done()
		s.runPipeline(d.ID, input, decision, ahead)
	}()
	return d, nil
}

// firstDeploy is the deploy a create started and what it ships.
type firstDeploy struct {
	id    string
	parts map[string]string
}

// FirstDeploy is the first deploy the service's create started, while it
// is pending or building and input would ship what it ships; nil otherwise. A client
// that creates a service with deploy and then deploys it itself (a tab
// loaded before the create started the deploy) is answered with it, so the
// service is deployed once. Once that client changed what ships in between
// (it saved the catalog's settings after the create), its deploy is a new
// one, with them.
func (s *Service) FirstDeploy(ctx context.Context, input TriggerInput) *Deployment {
	v, ok := s.created.Load(input.ServiceID)
	if !ok {
		return nil
	}
	first := v.(firstDeploy)
	now := s.shippedSettings(ctx, input)
	if now == nil || !maps.Equal(first.parts, now.parts) {
		return nil
	}
	// Its pipeline outlives it: a deploy that went live or failed still has
	// its last steps to run, and a deploy asked for then is a new one.
	d, err := s.GetDeploymentForService(ctx, input.ServiceID, first.id)
	if err != nil || (d.Status != StatusPending && d.Status != StatusBuilding) {
		return nil
	}
	return d
}

// BeginShutdown is called when the control plane starts shutting down. A
// deploy that fails from then on is not recorded as failed: its failure may
// be the shutdown itself (the SSH pool closing under a remote deploy), so it
// is left in progress for the next start's recovery, the one place that
// reconciles interrupted deploys. Successes are still recorded.
func (s *Service) BeginShutdown() {
	s.shuttingDown.Store(true)
}

// InFlightCount returns how many deploy pipelines are running in this
// process.
func (s *Service) InFlightCount() int {
	n := 0
	s.inflight.Range(func(any, any) bool {
		n++
		return true
	})
	return n
}

// inflightIDs returns the IDs of the deployments whose pipeline is running
// in this process.
func (s *Service) inflightIDs() []string {
	var ids []string
	s.inflight.Range(func(k, _ any) bool {
		ids = append(ids, k.(string))
		return true
	})
	return ids
}

// precheck runs every check that must pass before a deployment starts. The
// decision is the policy's (nil when no policy is configured or it was not
// reached).
func (s *Service) precheck(ctx context.Context, input TriggerInput) (*deploypolicy.Decision, error) {
	if input.ServerID != "" && input.Server == nil {
		return nil, ErrServerUnavailable
	}
	if input.Server != nil && input.Server.WaitingForAgent() {
		return nil, server.ErrServerNotReady
	}
	if input.Server != nil && input.Server.AgentFrozenAt != nil ||
		input.MoveFrom != nil && input.MoveFrom.Server != nil && input.MoveFrom.Server.AgentFrozenAt != nil {
		return nil, server.ErrServerFrozen
	}
	if err := s.checkHostUpdates(ctx, input); err != nil {
		return nil, err
	}
	if input.MoveFrom != nil && s.mover == nil {
		return nil, errMovesNotConfigured
	}
	if input.RollbackImageTag != "" && input.SourceType == string(service.SourceTypeDockerCompose) {
		return nil, ErrRollbackUnsupported
	}
	if err := s.checkWildcardDomains(ctx, input); err != nil {
		return nil, err
	}
	if err := s.checkSandbox(ctx, input); err != nil {
		return nil, err
	}
	if input.Preview {
		if err := s.previewRoom(ctx, input); err != nil {
			return nil, err
		}
	}
	if err := s.AuthorizeTarget(ctx, input.Origin, input.ActorID, input.ServiceID, input.Server); err != nil {
		return nil, err
	}
	decision, err := s.policyCheck(ctx, input)
	if err != nil {
		return nil, err
	}
	if decision != nil {
		if err := decision.Err(); err != nil {
			return decision, err
		}
	}
	return decision, nil
}

// checkWildcardDomains refuses wildcard domains on a server that has no
// DNS-01 resolver (ErrWildcardNeedsDNS).
func (s *Service) checkWildcardDomains(ctx context.Context, input TriggerInput) error {
	wildcard := false
	for _, d := range input.Domains {
		if strings.HasPrefix(d, "*.") {
			wildcard = true
			break
		}
	}
	if !wildcard {
		return nil
	}
	if input.Server == nil || s.dns == nil {
		return ErrWildcardNeedsDNS
	}
	ok, err := s.dns.HasDNSProvider(ctx, input.Server.ID)
	if err != nil {
		return fmt.Errorf("check the server's DNS provider: %w", err)
	}
	if !ok {
		return ErrWildcardNeedsDNS
	}
	return nil
}

// checkSandbox refuses a Sandboxed service on a server that can't run it
// (ErrSandboxUnavailable, with why): a server, the panel's own included
// (D-97), never probed, or whose gVisor isn't installed or fails its smoke
// test; with a strict network (D-115) also one without the agent's DNS
// relay.
func (s *Service) checkSandbox(ctx context.Context, input TriggerInput) error {
	if s.serviceFetcher == nil || input.ServiceID == "" {
		return nil
	}
	svc, err := s.serviceFetcher.Get(ctx, input.ServiceID)
	if err != nil {
		return fmt.Errorf("read the service's isolation: %w", err)
	}
	strict := svc.EffectiveIsolation == service.IsolationSandboxedStrict
	if svc.EffectiveIsolation != service.IsolationSandboxed && !strict {
		return nil
	}
	if s.sandboxes == nil {
		return fmt.Errorf("%w: this panel can't check servers for gVisor", ErrSandboxUnavailable)
	}
	id, name, page := "", "the panel's own server", "Instance → Server"
	if input.Server != nil {
		id, name, page = input.Server.ID, input.Server.Name, "the server's page"
	}
	check := s.sandboxes.Sandboxed
	if strict {
		check = s.sandboxes.SandboxedStrict
	}
	level, _, err := check(ctx, id)
	if err != nil {
		return fmt.Errorf("check the server for gVisor: %w", err)
	}
	if level.State != "available" {
		return fmt.Errorf("%w: %s %s", ErrSandboxUnavailable, name, sandboxWhy(level, page))
	}
	return nil
}

// sandboxWhy is why a server can't run Sandboxed services, to follow its
// name; page is where its Isolation card is.
func sandboxWhy(l server.Level, page string) string {
	strict := l.ID == service.IsolationSandboxedStrict
	switch {
	case l.State == "not_installed" && strict:
		return "has no strict network yet: an instance operator installs gVisor again on " + page + " (Isolation, Install gVisor)."
	case l.State == "not_installed":
		return "has no gVisor: an instance operator installs it on " + page + " (Isolation, Install gVisor)."
	case l.State == "unknown":
		return "hasn't been checked yet: Check again under Isolation on " + page + "."
	case strict:
		return "can't run Sandboxed with a strict network: " + l.Reason
	}
	return "can't run gVisor: " + l.Reason
}

// recordRefusal records a refused webhook deploy as a failed deployment with
// the reason in its log. The service's runtime status is left alone: nothing
// was stopped or started.
func (s *Service) recordRefusal(ctx context.Context, input TriggerInput, decision *deploypolicy.Decision, refusal error) (*Deployment, error) {
	d, err := s.repo.Create(ctx, input.ServiceID, input.TriggeredBy, input.CommitMessage, input.GitEventID)
	if errors.Is(err, ErrAlreadyDeployed) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("record refused deploy: %w (refused: %w)", err, refusal)
	}
	appendLog := func(msg string) {
		if _, err := s.repo.InsertLog(ctx, d.ID, msg); err != nil {
			s.log.Warn("failed to insert deployment log", zap.Error(err))
		}
	}
	switch input.Origin {
	case OriginPreview:
		appendLog("✗ The preview's deploy was refused; nothing was stopped or started")
	case OriginSystem:
		appendLog("✗ Toft's redeploy was refused; nothing was stopped or started")
	default:
		appendLog("✗ Deploy triggered by a git push was refused; nothing was stopped or started")
	}
	if decision != nil && decision.Blocked {
		for _, line := range decision.LogLines() {
			appendLog(line)
		}
	} else {
		appendLog("✗ " + refusal.Error())
	}
	if _, err := s.repo.UpdateStatus(ctx, d.ID, StatusFailed, "", input.CommitSHA, refusal.Error()); err != nil {
		s.log.Error("failed to update deployment status to failed", zap.Error(err), zap.String("deployment_id", d.ID))
	}
	s.publishFailed(input, d.ID, refusal.Error())
	return d, refusal
}

// runPipeline executes the full build + run cycle in the background, once
// the service's deploy ahead of it has ended (ahead, waitTurn). decision is
// the policy check Trigger made (warnings and exceptions are logged here).
//
// The deploy runs under its service's time limit (D-125): every step gets a
// context the limit cancels, and what records how it ended runs without
// that cancellation (settled), as does the rollout's own clean-up.
func (s *Service) runPipeline(deploymentID string, input TriggerInput, decision *deploypolicy.Decision, ahead <-chan struct{}) {
	defer s.inflight.Delete(deploymentID)
	defer s.moving.Delete(deploymentID)
	if input.FromCreate {
		defer s.created.Delete(input.ServiceID)
	}
	if !s.waitTurn(deploymentID, ahead) {
		return
	}
	base := context.Background()
	ctx, clock := startClock(base, s.timeLimit(base, input.ServiceID))
	defer clock.stop()
	input.clock = clock

	appendLog := func(msg string) {
		if _, err := s.repo.InsertLog(base, deploymentID, msg); err != nil {
			s.log.Warn("failed to insert deployment log", zap.Error(err))
		}
	}

	if _, err := s.repo.UpdateStatus(ctx, deploymentID, StatusBuilding, "", input.CommitSHA, ""); err != nil {
		s.log.Error("failed to update deployment status to building", zap.Error(err), zap.String("deployment_id", deploymentID))
	}
	appendLog("Build started")
	if input.MoveFrom != nil {
		appendLog("→ Moving from " + serverLabel(input.MoveFrom.Server) + " to " + serverLabel(input.Server) +
			": it keeps running on " + serverLabel(input.MoveFrom.Server) + " until it runs on " + serverLabel(input.Server))
		moved := s.mover.MovedHostnames(ctx, input.Domains, input.MoveFrom.ServerID, input.ServerID)
		for i, h := range moved {
			if h != input.Domains[i] {
				appendLog("→ Its temporary address moves with it: " + h)
			}
		}
		input.Domains = moved
	}
	if s.eventPublisher != nil && input.OrgID != "" {
		s.eventPublisher.Publish(input.OrgID, DeploymentEvent{
			Type:         "deployment.building",
			DeploymentID: deploymentID,
			ServiceID:    input.ServiceID,
			ServiceName:  input.ServiceName,
			ProjectID:    input.ProjectID,
			Status:       string(StatusBuilding),
		})
	}
	if decision != nil {
		for _, line := range decision.LogLines() {
			appendLog(line)
		}
		if decision.Blocked {
			// Trigger refuses these; kept so the pipeline can never run one.
			s.failAndPublish(ctx, deploymentID, input, "", "refused by the deploy policy")
			return
		}
	}

	plan, err := s.networkPlan(ctx, input)
	if err != nil {
		msg := "network plan: " + err.Error()
		appendLog("✗ " + msg)
		s.failAndPublish(ctx, deploymentID, input, "", msg)
		return
	}
	input.netPlan = plan
	if input.security, err = s.securityFor(ctx, input.ServiceID); err != nil {
		appendLog("✗ " + err.Error())
		s.failAndPublish(ctx, deploymentID, input, "", err.Error())
		return
	}
	input.settings.check("security", fmt.Sprintf("%+v", input.security))
	if input.check, err = s.containerCheckFor(ctx, input); err != nil {
		appendLog("✗ " + err.Error())
		s.failAndPublish(ctx, deploymentID, input, "", err.Error())
		return
	}
	switch input.security.Runtime {
	case rollout.RuntimeStrict:
		appendLog("→ Sandboxed with a strict network: the service's containers run under gVisor (runsc-strict), with names answered by Toft's agent")
	case rollout.RuntimeSandboxed:
		appendLog("→ Sandboxed: the service's containers run under gVisor (runsc)")
	}

	// Obtain an orchestrator for the target server (local or remote).
	orch, cleanup, err := s.factory.ForServer(input.Server)
	if err != nil {
		s.log.Error("orchestrator init failed", zap.String("deployment_id", deploymentID), zap.Error(err))
		appendLog("Orchestrator error: " + err.Error())
		s.failAndPublish(ctx, deploymentID, input, "", err.Error())
		return
	}
	defer cleanup()

	if !plan.Legacy {
		appendLog("→ network: " + plan.Home.Name)
		if err := orch.PrepareNetworks(ctx, plan, appendLog); err != nil {
			msg := "prepare networks: " + err.Error()
			appendLog("✗ " + msg)
			s.failAndPublish(ctx, deploymentID, input, "", msg)
			return
		}
	}

	if s.sharedMounts != nil && input.SourceType != "docker-compose" {
		mounts, err := s.sharedMounts.MountsForDeploy(ctx, input.ServiceID, input.ServerID)
		if err != nil {
			msg := "shared volumes: " + err.Error()
			appendLog("✗ " + msg)
			s.failAndPublish(ctx, deploymentID, input, "", msg)
			return
		}
		input.settings.check("shared_mounts", fmt.Sprintf("%+v", mounts))
		for _, m := range mounts {
			appendLog(fmt.Sprintf("→ shared volume %s at %s", m.Name, m.Target))
		}
		input.Volumes = append(append([]service.VolumeMount(nil), input.Volumes...), mounts...)
	}

	switch input.SourceType {
	case "docker-image":
		s.runDockerImagePipeline(ctx, deploymentID, input, orch, appendLog)
	case "docker-compose":
		var inline deploypolicy.Decision
		if decision != nil {
			inline = *decision
		}
		s.runComposePipeline(ctx, deploymentID, input, orch, appendLog, inline)
	default:
		s.runGitPipeline(ctx, deploymentID, input, orch, appendLog)
	}
	if s.buildCache != nil && input.SourceType != deploypolicy.SourceImage && input.RollbackImageTag == "" {
		s.buildCache.AfterBuild(input.ServerID)
	}
}

func (s *Service) runGitPipeline(ctx context.Context, deploymentID string, input TriggerInput, orch Orchestrator, appendLog func(string)) {
	repoURL := injectGitCredentials(input.GitRepoURL, input.GitUsername, input.GitToken)

	var imageTag string
	if input.RollbackImageTag != "" {
		// Rollback: skip clone+build, re-use the pre-built image.
		imageTag = input.RollbackImageTag
		appendLog(fmt.Sprintf("→ rollback to image: %s", imageTag))
		appendLog(fmt.Sprintf("→ env vars: %d (current)", len(input.EnvVars)))
	} else {
		imageTag = gitImageTag(input.ServiceID, deploymentID)

		if input.RepositoryID != "" {
			appendLog(fmt.Sprintf("→ the project's repository, %s", archiveRef(input)))
		} else {
			appendLog(fmt.Sprintf("→ repo: %s  branch: %s", sanitizeRepoURL(input.GitRepoURL), input.GitBranch))
		}
		if input.BaseDirectory != "" {
			appendLog(fmt.Sprintf("→ base directory: %s", input.BaseDirectory))
		}
		if len(input.Domains) > 0 {
			appendLog(fmt.Sprintf("→ domains: %s", strings.Join(input.Domains, ", ")))
		}
		appendLog(fmt.Sprintf("→ env vars: %d", len(input.EnvVars)))
		appendLog(fmt.Sprintf("→ image tag: %s", imageTag))

		if !s.runPreDeployCommands(ctx, deploymentID, input, orch, appendLog) {
			return
		}

		// Pass all env vars as Docker build args so build-time variables
		// (e.g. NEXT_PUBLIC_* in Next.js) are baked into the image.
		buildArgs := envsToBuildArgs(input.EnvVars)

		releaseSlot, ok := s.acquireBuildSlot(ctx, input.clock, appendLog)
		if !ok {
			s.failAndPublish(ctx, deploymentID, input, imageTag, "cancelled while waiting for a build slot")
			return
		}
		var built Commit
		var buildErr error
		input.clock.at("build")
		if input.RepositoryID != "" {
			built, buildErr = s.buildFromRepository(ctx, deploymentID, input, orch, imageTag, buildArgs, appendLog)
		} else if input.BuildPack == "nixpacks" {
			appendLog("[nixpacks] Detecting language and building...")
			built, buildErr = orch.BuildFromGitNixpacks(ctx, repoURL, input.GitBranch, input.BaseDirectory, imageTag, input.Nixpacks, buildArgs, appendLog)
		} else {
			built, buildErr = orch.BuildFromGit(ctx, repoURL, input.GitBranch, input.BaseDirectory, imageTag, buildArgs, appendLog)
		}
		releaseSlot()
		// What was built, which the status updates below keep: a push's or
		// an API call's commit_sha is what was asked for, and the branch may
		// have moved since.
		if built.SHA != "" {
			input.CommitSHA = built.SHA
			s.recordCommit(context.WithoutCancel(ctx), deploymentID, built)
		}
		if buildErr != nil {
			s.log.Error("build failed", zap.String("deployment_id", deploymentID), zap.Error(buildErr))
			appendLog("✗ Build failed: " + buildErr.Error())
			s.failAndPublish(ctx, deploymentID, input, imageTag, buildErr.Error())
			return
		}
	}

	if missing := checkRequiredEnvVars(input.RequiredEnvVars, input.EnvVars); len(missing) > 0 {
		msg := "✗ missing required env vars: " + strings.Join(missing, ", ")
		appendLog(msg)
		s.failAndPublish(ctx, deploymentID, input, imageTag, msg)
		return
	}

	s.writeConfigFiles(ctx, &input, appendLog)
	s.runRollout(ctx, deploymentID, input, orch, imageTag, false, appendLog)
}

func (s *Service) runDockerImagePipeline(ctx context.Context, deploymentID string, input TriggerInput, orch Orchestrator, appendLog func(string)) {
	imageTag := input.DockerImage
	if input.RollbackImageTag != "" {
		// Rollback: skip pull, re-use the cached image.
		imageTag = input.RollbackImageTag
		appendLog(fmt.Sprintf("→ rollback to image: %s", imageTag))
		appendLog(fmt.Sprintf("→ env vars: %d (current)", len(input.EnvVars)))
	} else {
		appendLog(fmt.Sprintf("→ image: %s", imageTag))
		if len(input.Domains) > 0 {
			appendLog(fmt.Sprintf("→ domains: %s", strings.Join(input.Domains, ", ")))
		}
		appendLog(fmt.Sprintf("→ env vars: %d", len(input.EnvVars)))

		if !s.runPreDeployCommands(ctx, deploymentID, input, orch, appendLog) {
			return
		}
	}

	if missing := checkRequiredEnvVars(input.RequiredEnvVars, input.EnvVars); len(missing) > 0 {
		msg := "✗ missing required env vars: " + strings.Join(missing, ", ")
		appendLog(msg)
		s.failAndPublish(ctx, deploymentID, input, imageTag, msg)
		return
	}

	s.writeConfigFiles(ctx, &input, appendLog)
	s.runRollout(ctx, deploymentID, input, orch, imageTag, input.RollbackImageTag == "", appendLog)
}

// lokiFor is the Loki this container's output goes to (D-48): its
// project's, else its workspace's; "" keeps its logs on the server only. It
// is decided here, on every deploy path (panel, API, push, rollback), and
// says in the log why logs stay on the server when a Loki is set: an
// address that is a container alias, which Docker's log driver never
// resolves, or a server without the driver plugin.
func (s *Service) lokiFor(ctx context.Context, input TriggerInput, orch Orchestrator, appendLog func(string)) string {
	if s.observabilityQuerier == nil || input.OrgID == "" {
		return ""
	}
	t, err := s.observabilityQuerier.LokiFor(ctx, input.OrgID, input.ProjectID)
	if err != nil {
		s.log.Warn("look up Loki", zap.String("service_id", input.ServiceID), zap.Error(err))
		input.settings.check("loki", "\x00unread")
		return ""
	}
	addr := t.ShipTo(input.ServerID, input.ServiceID)
	input.settings.check("loki", addr)
	self := t.ServiceID != "" && t.ServiceID == input.ServiceID // Loki's own logs stay local, unsaid
	switch {
	case addr == "" && t.Port != 0 && !self:
		where := "the panel's server"
		if t.Server != "" {
			where = t.Server
		}
		appendLog("→ Logs stay on this server: Loki runs on " + where + ", and central logs from other servers need an external Loki address for now " +
			"(Workspace services → Logs and metrics → Set an external Loki address)")
		return ""
	case addr == "" && t.URL != "" && !self:
		appendLog("→ Logs stay on this server: Loki's address, " + t.URL + ", is a container alias, which Docker's log driver can't resolve. " +
			"Give Loki an address every server reaches (Workspace services → Logs and metrics → Set an external Loki address)")
		return ""
	case addr == "":
		return ""
	}
	if c, ok := orch.(LokiDriverChecker); ok {
		has, err := c.HasLokiDriver(ctx)
		if err != nil {
			s.log.Warn("check the Loki log driver", zap.String("service_id", input.ServiceID), zap.Error(err))
		} else if !has {
			appendLog("→ Logs stay on this server: it has no Loki log driver plugin (Re-provision installs it)")
			return ""
		}
	}
	if strings.HasPrefix(addr, "http://127.0.0.1:") {
		appendLog("→ Logs go to Loki on this server (127.0.0.1:" + strconv.Itoa(t.Port) + ")")
	}
	return addr
}

// IsContainerAlias reports whether a URL's host is a bare name such as
// "loki": a Docker network alias, which resolves only inside containers,
// never in the daemon's own log driver.
func IsContainerAlias(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host != "" && !strings.Contains(host, ".") && !strings.Contains(host, ":") && host != "localhost"
}

// runRollout hands the rollout of imageTag to the orchestrator (rollout.Run,
// D-111) and records how it ended. pull pulls the image first (an image
// deploy).
func (s *Service) runRollout(ctx context.Context, deploymentID string, input TriggerInput, orch Orchestrator, imageTag string, pull bool, appendLog func(string)) {
	for _, line := range limitLogLines(input, nil, nil) {
		appendLog(line)
	}
	input.LokiURL = s.lokiFor(ctx, input, orch, appendLog)
	spec := rolloutSpec(deploymentID, input, imageTag, pull)
	if s.loopbackPorts != nil {
		// The catalog's Loki takes its server's services' logs on a
		// 127.0.0.1 port, never on another address (D-148). Every deploy
		// asks, whatever started it (a rollback carries no addon slug); 0
		// for any other service.
		port, err := s.loopbackPorts.AssignLoopbackPort(ctx, input.ServiceID)
		if err != nil {
			s.log.Error("assign Loki's loopback port", zap.String("service_id", input.ServiceID), zap.Error(err))
			msg := "✗ Could not give Loki a 127.0.0.1 port on this server"
			if errors.Is(err, service.ErrNoLoopbackPort) {
				msg += ": " + err.Error()
			}
			appendLog(msg)
			s.failAndPublish(ctx, deploymentID, input, imageTag, msg)
			return
		}
		if port > 0 {
			spec.Slot.Loopback = rollout.Loopback{Port: port, Target: input.Port}
			appendLog(fmt.Sprintf("→ Loki takes this server's logs on 127.0.0.1:%d", port))
		}
	}
	spec.ActiveDeployment = s.activeDeploymentOf(ctx, input.ServiceID)
	res := orch.Rollout(ctx, spec, Routing{Domains: input.Domains, Access: input.AccessControl},
		rollout.Hooks{Log: appendLog, Abandon: s.shuttingDown.Load, HandOff: s.handOffHook(ctx, deploymentID, input, imageTag, false), Gone: s.agentGone(ctx, input.ServiceID),
			TimeLeft: input.clock.timeLeft, NoLimit: input.clock.stop})
	s.recordRollout(ctx, deploymentID, input, imageTag, res, appendLog)
}

// rolloutSpec is the rollout of imageTag for input.
func rolloutSpec(deploymentID string, input TriggerInput, imageTag string, pull bool) rollout.Spec {
	var cpu float64
	fmt.Sscanf(input.CPULimit, "%f", &cpu)
	var memory int64
	if mb, ok := resources.ParseMB(input.MemoryLimit); ok {
		memory = mb << 20
	}
	volumes := make([]rollout.Volume, 0, len(input.Volumes))
	for _, v := range input.Volumes {
		volumes = append(volumes, rollout.Volume{Name: v.Name, Target: v.Target, HostPath: v.HostPath, ReadOnly: v.ReadOnly, Shared: v.Shared})
	}
	spec := rollout.Spec{
		DeploymentID: deploymentID,
		Slot: rollout.Slot{
			ServiceID:   input.ServiceID,
			ServiceName: input.ServiceName,
			EnvName:     input.EnvironmentName,
			Image:       imageTag,
			Env:         input.EnvVars,
			Command:     input.StartCommand,
			CPUCores:    cpu,
			MemoryBytes: memory,
			Volumes:     volumes,
			LokiURL:     input.LokiURL,
			BasicAuth:   rollout.BasicAuth{User: input.AccessControl.BasicAuthUser, Password: input.AccessControl.BasicAuthPassword},
			Security:    input.security,
			Check:       input.check,
		},
		Plan: input.netPlan,
		Port: input.Port,
		Pull: pull,
		// A rollback of an image service pulls its image again when a disk
		// clean removed it (D-135); a registry that's down doesn't stop one
		// whose image is still there.
		PullMissing: !pull && input.RollbackImageTag != "" && input.SourceType == deploypolicy.SourceImage,
		Auth:        rolloutAuth(input),
		Migrate:     input.MigrateCommand,
		Recreate:    input.Recreate,
	}
	if input.HealthCheckRetries > 0 {
		spec.Health = &rollout.HealthCheck{Path: input.HealthCheckPath, TimeoutSec: input.HealthCheckTimeout, Retries: input.HealthCheckRetries}
	}
	for _, pd := range input.PostDeployCommands {
		spec.PostDeploy = append(spec.PostDeploy, rollout.PostDeploy{Container: pd.ServiceName, Command: pd.Command})
	}
	return spec
}

// rolloutAuth is the saved registry login input's rollout pulls with. A
// rollback's image is an old deployment's, from wherever that deployment
// pulled it: the login goes with it only when it is on the registry of the
// service's own image (input.Settings), the one the login is saved for
// (service.credentialsMoved), or a developer could roll back to a
// deployment of an image on a host of theirs, or have the certificate
// redeploy run it again, and have Docker send it a password that is never
// shown back (SEC-011). A build's tag names no registry, as its service
// names no image, and keeps the login.
func rolloutAuth(input TriggerInput) *RegistryAuth {
	if input.RollbackImageTag != "" && (input.Settings == nil || !service.SameRegistry(input.RollbackImageTag, input.Settings.DockerImage)) {
		return nil
	}
	return input.RegistryAuth
}

// recordRollout records how a rollout ended. One that failed before it
// changed containers (pull, migrate) is failAndPublish. One whose previous
// slot serves again leaves the service's runtime status and active
// deployment as they are: they still describe what runs. Otherwise (a
// first deploy, a previous slot that could not be brought back) it is
// failAfterChange.
func (s *Service) recordRollout(ctx context.Context, deploymentID string, input TriggerInput, imageTag string, res rollout.Result, appendLog func(string)) {
	ctx, res.Err = s.settled(ctx, deploymentID, res.Err, res.Step)
	if res.Step == "" {
		s.succeedDeployment(ctx, deploymentID, input, imageTag, input.CommitSHA, appendLog)
		return
	}
	s.log.Error("rollout failed", zap.String("deployment_id", deploymentID), zap.String("step", res.Step), zap.String("error", res.Err))
	if res.Step == rollout.StepPostDeploy {
		imageTag = ""
	}
	switch {
	case !res.Changed:
		s.failAndPublish(ctx, deploymentID, input, imageTag, res.Err)
	case !res.PreviousServes:
		s.failAfterChange(ctx, deploymentID, input, imageTag, res.Err)
	default:
		if _, err := s.repo.UpdateStatus(ctx, deploymentID, StatusFailed, imageTag, input.CommitSHA, res.Err); err != nil {
			s.log.Error("failed to update deployment status to failed", zap.Error(err), zap.String("deployment_id", deploymentID))
		}
		s.publishFailed(input, deploymentID, res.Err)
	}
}

// followLive makes what a service's next Deploy runs the version its live
// deployment ran, or else its newest that went live, once a deploy of it
// settles: an image service's saved image (the header's Source), and a
// compose stack's own IMAGE_TAG variable (never the environment's, nor one
// it doesn't have). A rollback's image becomes the saved one, and a version
// whose deploy failed doesn't stay it (Deploy a version still offers it).
// Nothing changes when nothing went live, or when what is saved is no
// longer what the deploy started with: a save made meanwhile stays. A
// deploy finished after a restart (input.Settings nil) reads what is saved
// now; a compose stack's IMAGE_TAG is then not known, and is left.
func (s *Service) followLive(ctx context.Context, input TriggerInput) {
	saved := input.Settings
	if saved == nil {
		if s.serviceFetcher == nil {
			return
		}
		var err error
		if saved, err = s.serviceFetcher.Get(ctx, input.ServiceID); err != nil || saved == nil {
			return
		}
	}
	compose := saved.SourceType == service.SourceTypeDockerCompose
	from := ""
	switch {
	case saved.SourceType == service.SourceTypeDockerImage:
		from = saved.DockerImage
	case compose && s.envVars != nil:
		from = kvsToMap(input.EnvVars)["IMAGE_TAG"]
	}
	if from == "" {
		return
	}
	// ponytail: the newest 25 deployments; one live before those keeps what is saved.
	deps, err := s.repo.ListByService(ctx, input.ServiceID)
	if err != nil {
		s.log.Warn("could not read deployments to restore the saved version", zap.Error(err), zap.String("service_id", input.ServiceID))
		return
	}
	// A deployment that went live stays running; recovery can make an
	// interrupted (failed) one the active one.
	for _, d := range deps {
		if d.Status != StatusRunning && d.ID != saved.ActiveDeploymentID {
			continue
		}
		if d.ImageTag == "" || d.ImageTag == from {
			return
		}
		if compose {
			err = s.envVars.Swap(ctx, input.ServiceID, "IMAGE_TAG", from, d.ImageTag)
		} else {
			err = s.serviceSvc.SwapDockerImage(ctx, input.ServiceID, from, d.ImageTag)
		}
		if err != nil {
			s.log.Warn("could not restore the saved version", zap.Error(err), zap.String("service_id", input.ServiceID))
		}
		return
	}
}

// activeDeploymentOf returns serviceID's active deployment, or "" when it
// cannot be read.
func (s *Service) activeDeploymentOf(ctx context.Context, serviceID string) string {
	if s.serviceFetcher == nil {
		return ""
	}
	svc, err := s.serviceFetcher.Get(ctx, serviceID)
	if err != nil || svc == nil {
		return ""
	}
	return svc.ActiveDeploymentID
}

// runComposePipeline runs a compose deploy. inline is the policy's decision
// on an inline file (its services and their limits in the file).
func (s *Service) runComposePipeline(ctx context.Context, deploymentID string, input TriggerInput, orch Orchestrator, appendLog func(string), inline deploypolicy.Decision) {
	appendLog("Starting docker compose deployment...")

	if !s.runPreDeployCommands(ctx, deploymentID, input, orch, appendLog) {
		return
	}

	// Exactly the env vars the policy checked the file with: docker compose
	// interpolates with these and nothing of the platform's that the check
	// allows (composespec.IsPlatformEnv). Basic auth and IP allowlists are
	// applied by the platform's routing (ComposeRoute), not through env vars.
	envMap := kvsToMap(input.EnvVars)

	// The deployment records the IMAGE_TAG it ran with, so a rollback can
	// replay it (the compose file pins its images with ${IMAGE_TAG}).
	imageTag := ""
	if t := envMap["IMAGE_TAG"]; imageTagPattern.MatchString(t) {
		imageTag = t
		appendLog("→ IMAGE_TAG=" + t)
	}

	appendLog(fmt.Sprintf("→ injecting %d env var(s)", len(envMap)))

	// Repo-based: clone locally so relative file paths (nginx configs, etc.) are preserved,
	// then hand the full directory to the orchestrator which uploads it to the remote.
	// The project's own repository is unpacked from git archive instead (D-77).
	if input.ComposeRepoURL != "" || input.RepositoryID != "" {
		branch := input.GitBranch
		if branch == "" {
			branch = "main"
		}
		ref := branch
		if input.CheckoutSHA != "" {
			ref = input.CheckoutSHA
		}
		if input.RepositoryID != "" {
			appendLog(fmt.Sprintf("[1/2] Reading the project's repository at %s...", ref))
		} else {
			appendLog(fmt.Sprintf("[1/2] Cloning infra repo %s @ %s...", sanitizeRepoURL(input.ComposeRepoURL), ref))
		}

		tmpDir, err := os.MkdirTemp("", "paas-infra-*")
		if err != nil {
			appendLog("✗ Failed to create temp dir: " + err.Error())
			s.failAndPublish(ctx, deploymentID, input, imageTag, err.Error())
			return
		}
		defer os.RemoveAll(tmpDir)

		releaseSlot, ok := s.acquireBuildSlot(ctx, input.clock, appendLog)
		if !ok {
			s.failAndPublish(ctx, deploymentID, input, imageTag, "cancelled while waiting for a build slot")
			return
		}
		input.clock.at("clone")
		var composeSHA string
		if input.RepositoryID != "" {
			c, err := s.unpackRepository(ctx, deploymentID, input, ref, tmpDir)
			releaseSlot()
			if err != nil {
				msg := "✗ " + err.Error()
				appendLog(msg)
				s.failAndPublish(ctx, deploymentID, input, imageTag, msg)
				return
			}
			input.CommitSHA = c.SHA
			composeSHA = c.SHA
			s.recordCommit(ctx, deploymentID, c)
			if err := s.repo.SetComposeCommit(ctx, deploymentID, c.SHA); err != nil {
				s.log.Warn("could not record the compose commit", zap.Error(err), zap.String("deployment_id", deploymentID))
			}
			appendLog("→ compose files at commit " + c.SHA)
		} else {
			composeCloneURL := injectGitCredentials(input.ComposeRepoURL, input.GitUsername, input.GitToken)
			out, err := checkoutRepo(ctx, composeCloneURL, branch, input.CheckoutSHA, tmpDir)
			releaseSlot()
			for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
				if line != "" {
					appendLog(line)
				}
			}
			if err != nil {
				msg := fmt.Sprintf("✗ git clone failed: %v", err)
				appendLog(msg)
				s.failAndPublish(ctx, deploymentID, input, imageTag, msg)
				return
			}
			composeSHA = s.recordComposeCommit(ctx, deploymentID, tmpDir, appendLog)
		}

		composeFilePath := input.ComposeFilePath
		if composeFilePath == "" {
			composeFilePath = "docker-compose.yml"
		}
		appendLog(fmt.Sprintf("Cloned repo — compose file: %s", composeFilePath))

		// On a remote server the deploy replaces the compose file directory's
		// .env with the service's env vars, and compose reads that file for
		// files included from the same directory too. Write it into the clone
		// now, so the check below, the uploaded repository and docker compose
		// all read the one file.
		if input.Server != nil {
			if err := composespec.StageDotEnv(tmpDir, composeFilePath, envMap); err != nil {
				msg := "✗ Could not write the compose .env file: " + err.Error()
				appendLog(msg)
				s.failAndPublish(ctx, deploymentID, input, imageTag, msg)
				return
			}
		}

		// The compose file and everything it references are only known now:
		// check them exactly as docker compose will read them. A new stack
		// (no grace date) first gets the default memory limit for each
		// compose service the file leaves without one.
		rc := deploypolicy.RepoCheck{
			ServiceID:       input.ServiceID,
			ServiceName:     input.ServiceName,
			RepoDir:         tmpDir,
			ComposeFilePath: composeFilePath,
			CommitSHA:       composeSHA,
			EnvVars:         input.EnvVars,
			Local:           input.Server == nil,
		}
		defaults, memory, err := s.repoComposeMemory(ctx, input, deploypolicy.RepoComposeSource(rc))
		if err != nil {
			appendLog("✗ " + err.Error())
			s.failAndPublish(ctx, deploymentID, input, imageTag, err.Error())
			return
		}
		rc.Memory = memory
		// The defaults are the stack's limits from here on: the next
		// deploy's room check compares with what this one records.
		if input.reservedMB != nil {
			for _, d := range defaults {
				*input.reservedMB += float64(d.MemoryMB)
			}
		}
		var (
			repoServices []string
			repoMB       map[string]int64
			repoDeploy   map[string]bool
		)
		if s.policy != nil {
			d := s.policy.CheckComposeRepo(ctx, rc)
			for _, line := range d.LogLines() {
				appendLog(line)
			}
			if d.Blocked {
				s.failAndPublish(ctx, deploymentID, input, imageTag, d.Err().Error())
				return
			}
			repoServices, repoMB, repoDeploy = d.ComposeServices, d.ComposeMemoryMB, d.ComposeDeployLimits
		}
		if input.ComposeLimits, err = s.composeLimits(ctx, input.ServiceID, repoDeploy); err != nil {
			appendLog("✗ " + err.Error())
			s.failAndPublish(ctx, deploymentID, input, imageTag, err.Error())
			return
		}
		input.settings.check("compose_limits", panelLimits(input.ComposeLimits))
		for _, line := range limitLogLines(input, repoMB, defaults) {
			appendLog(line)
		}

		route, err := composeRoute(input, repoServices, appendLog)
		if err != nil {
			appendLog("✗ " + err.Error())
			s.failAndPublish(ctx, deploymentID, input, imageTag, err.Error())
			return
		}
		input.composeNetwork = composeNetworkOf(route)

		nets, ok := s.composeNetworks(ctx, deploymentID, input, deploypolicy.RepoComposeSource(rc), appendLog)
		if !ok {
			return
		}
		warnUnlistedLimits(input, nets, appendLog)

		input.clock.at("compose up")
		appendLog("[2/2] Uploading repo and running docker compose up...")
		err = orch.RunComposeFromRepo(ctx, deploymentID, input.ServiceID, input.ServiceName, tmpDir, composeFilePath, envMap, route, nets, input.ComposeLimits, input.RegistryAuth,
			rollout.Hooks{Log: appendLog, HandOff: s.handOffHook(ctx, deploymentID, input, imageTag, true), Gone: s.agentGone(ctx, input.ServiceID),
				TimeLeft: input.clock.timeLeft, NoLimit: input.clock.stop})
		s.finishCompose(ctx, deploymentID, input, orch, imageTag, err, appendLog)
		return
	}

	// Inline compose content (no repo URL).
	composeYAML := input.ComposeContent
	appendLog(fmt.Sprintf("→ using inline compose content (%d bytes)", len(composeYAML)))
	limits, err := s.composeLimits(ctx, input.ServiceID, inline.ComposeDeployLimits)
	if err != nil {
		appendLog("✗ " + err.Error())
		s.failAndPublish(ctx, deploymentID, input, imageTag, err.Error())
		return
	}
	input.ComposeLimits = limits
	input.settings.check("compose_limits", panelLimits(limits))
	for _, line := range limitLogLines(input, inline.ComposeMemoryMB, nil) {
		appendLog(line)
	}
	route, err := composeRoute(input, inline.ComposeServices, appendLog)
	if err != nil {
		appendLog("✗ " + err.Error())
		s.failAndPublish(ctx, deploymentID, input, imageTag, err.Error())
		return
	}
	input.composeNetwork = composeNetworkOf(route)
	nets, ok := s.composeNetworks(ctx, deploymentID, input,
		deploypolicy.InlineComposeSource(input.ServiceID, input.ServiceName, composeYAML, envMap), appendLog)
	if !ok {
		return
	}
	warnUnlistedLimits(input, nets, appendLog)
	input.clock.at("compose up")
	err = orch.RunCompose(ctx, deploymentID, input.ServiceID, input.ServiceName, composeYAML, envMap, route, nets, input.ComposeLimits, input.RegistryAuth,
		rollout.Hooks{Log: appendLog, HandOff: s.handOffHook(ctx, deploymentID, input, imageTag, true), Gone: s.agentGone(ctx, input.ServiceID),
			TimeLeft: input.clock.timeLeft, NoLimit: input.clock.stop})
	s.finishCompose(ctx, deploymentID, input, orch, imageTag, err, appendLog)
}

// finishCompose records a compose deploy once its docker compose up ran
// (upErr its failure): the stack joins its networks, the post-deploy
// commands run, and the deploy succeeds. A deploy its server's agent ran
// up for is the panel's again first, so a restart from here does not run
// the post-deploy commands twice; one left for recovery stays with the
// agent, whose result the next start reads.
func (s *Service) finishCompose(ctx context.Context, deploymentID string, input TriggerInput, orch Orchestrator, imageTag string, upErr error, appendLog func(string)) {
	if upErr == nil || !s.shuttingDown.Load() {
		if err := s.repo.ClearHandOff(context.WithoutCancel(ctx), deploymentID); err != nil {
			s.log.Error("could not take a handed-off deploy back", zap.String("deployment_id", deploymentID), zap.Error(err))
		}
	}
	if upErr != nil {
		s.log.Error("compose up failed", zap.String("deployment_id", deploymentID), zap.Error(upErr))
		appendLog("✗ Compose failed: " + upErr.Error())
		s.failAfterChange(ctx, deploymentID, input, imageTag, upErr.Error())
		return
	}
	s.publishCompose(ctx, input, orch, appendLog)
	if err := s.runPostDeployCommands(ctx, deploymentID, input, orch, appendLog); err != nil {
		s.failAfterChange(ctx, deploymentID, input, imageTag, err.Error())
		return
	}
	s.succeedDeployment(ctx, deploymentID, input, imageTag, input.CommitSHA, appendLog)
}

// composeNetworks returns how a compose deploy uses networks. Under legacy
// app networks the routed service joins the control plane's network, as
// before, and the file's services are listed for the routing guard. Under
// isolated ones the compose file in src is loaded, its
// references to the control plane's network are pointed at the service's
// home network by an override, and the result is re-checked
// (composespec.IsolateNetworks). A file the re-check refuses fails the
// deploy (ok false, already recorded); only field names are logged, since
// messages can quote interpolated values.
func (s *Service) composeNetworks(ctx context.Context, deploymentID string, input TriggerInput, src composespec.Source, appendLog func(string)) (nets ComposeNetworks, ok bool) {
	plan := input.netPlan
	if plan.Legacy {
		_, services := composespec.Inspect(ctx, src)
		if services == nil {
			// Without the list no override names the stack's services, so
			// none would get gVisor (D-95).
			if input.security.Runtime != "" {
				msg := "the compose file's services could not be listed, so they can't be given gVisor"
				appendLog("✗ " + msg)
				s.failAndPublish(ctx, deploymentID, input, "", msg)
				return ComposeNetworks{}, false
			}
			appendLog("→ the compose file's services could not be listed; routes its images define are checked after up")
		}
		return ComposeNetworks{Home: plan.Home.Name, Services: services, Security: input.security}, true
	}
	override, keys, services, vs, err := composespec.IsolateNetworks(ctx, src, plan.Home.Name, s.legacyNetworks)
	if err != nil || len(vs) > 0 {
		msg := "the compose file's networks could not be pointed at the environment network"
		if err != nil {
			appendLog("✗ " + msg + ": the file could not be loaded")
		}
		for _, v := range vs {
			appendLog(fmt.Sprintf("✗ compose rejected: %s (%s)", v.Field, v.Rule))
		}
		s.failAndPublish(ctx, deploymentID, input, "", msg)
		return ComposeNetworks{}, false
	}
	nets = ComposeNetworks{Isolated: true, Home: plan.Home.Name, Override: override, Services: services, Security: input.security}
	if len(keys) > 0 {
		nets.RouteKey = keys[0]
		appendLog(fmt.Sprintf("→ compose network %s points at %s", strings.Join(keys, ", "), plan.Home.Name))
	}
	return nets, true
}

// warnUnlistedLimits says so when the panel's limits cannot be applied
// because the file's services are unknown (composeOverrides writes none).
func warnUnlistedLimits(input TriggerInput, nets ComposeNetworks, appendLog func(string)) {
	if len(input.ComposeLimits) > 0 && nets.Services == nil {
		appendLog("warning: the compose file's services could not be listed; the memory limits set in the panel are not applied")
	}
}

// publishCompose runs PrepareNetworks again once a compose stack is up, so
// its containers on the home network join the proxy and, for a shared
// service, its consumer networks. A failure is only a warning: the stack
// runs.
func (s *Service) publishCompose(ctx context.Context, input TriggerInput, orch Orchestrator, appendLog func(string)) {
	if input.netPlan.Legacy {
		return
	}
	if err := orch.PrepareNetworks(ctx, input.netPlan, appendLog); err != nil {
		appendLog("⚠ Could not attach the stack to its networks: " + err.Error())
	}
}

// composeRoute returns the platform routing for a compose deployment, or nil
// when the service has no domains or no compose service receives them
// (logged). services are the compose file's services when known (nil when
// the file was not loaded by the policy check).
//
// The compose service that receives the domains is input.ComposeRouteService
// or, when that is empty (automatic), the file's only service. A file with
// several services and no choice, or whose only service has a name the
// routing override cannot carry (composespec.ValidServiceName), is deployed
// without routing. A chosen service that is not in the file is an error.
func composeRoute(input TriggerInput, services []string, appendLog func(string)) (*ComposeRoute, error) {
	if len(input.Domains) == 0 {
		return nil, nil
	}
	notRouted := plural(len(input.Domains), "domain is", "domains are") + " not routed"
	target := input.ComposeRouteService
	if target == "" {
		if len(services) != 1 {
			var reason string
			switch {
			case services == nil:
				reason = "the compose file's services are not known"
			case len(services) == 0:
				reason = "the compose file has no services"
			default:
				reason = "the compose file has several services (" + strings.Join(services, ", ") + ")"
			}
			appendLog(fmt.Sprintf("⚠ %s: %s; choose the one that receives them in Settings → Domain Routing", notRouted, reason))
			return nil, nil
		}
		target = services[0]
		if !composespec.ValidServiceName(target) {
			appendLog(fmt.Sprintf("⚠ %s: the platform cannot route to compose service %q (letters, digits, '_', '.' and '-', "+
				"starting with a letter or digit, at most 63 characters); rename it", notRouted, target))
			return nil, nil
		}
	}
	if input.Port <= 0 {
		appendLog(fmt.Sprintf("⚠ %s: set the port compose service %s listens on in Settings → Domain Routing", notRouted, target))
		return nil, nil
	}
	if services != nil && !containsString(services, target) {
		return nil, fmt.Errorf("domain routing names compose service %q, which is not in the compose file (it has: %s); "+
			"change it in Settings → Domain Routing", target, strings.Join(services, ", "))
	}
	how := "chosen in Settings → Domain Routing"
	if input.ComposeRouteService == "" {
		how = "the file's only service"
	}
	appendLog(fmt.Sprintf("→ routing %s to compose service %s (%s), port %d", strings.Join(input.Domains, ", "), target, how, input.Port))
	return &ComposeRoute{
		Service: target,
		Port:    input.Port,
		Domains: input.Domains,
		Access:  input.AccessControl,
	}, nil
}

// composeNetworkOf is the compose service route puts on the app network,
// where other services reach the stack: route's, or "" when the deploy
// routes none.
func composeNetworkOf(route *ComposeRoute) *string {
	name := ""
	if route != nil {
		name = route.Service
	}
	return &name
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// writeConfigFiles writes config files to the host and appends bind mounts to input.Volumes.
// Errors are non-fatal: logged as warnings so a missing config file never blocks a deploy.
func (s *Service) writeConfigFiles(ctx context.Context, input *TriggerInput, appendLog func(string)) {
	if s.configFileWriter == nil {
		return
	}
	mounts, written, err := s.configFileWriter.WriteToHost(ctx, input.ServiceID, input.Server)
	if written == nil && err != nil {
		input.settings.check("config_files", "\x00unread")
	} else {
		input.settings.check("config_files", filesPart(written))
	}
	if err != nil {
		s.log.Warn("config file write failed", zap.String("service_id", input.ServiceID), zap.Error(err))
		appendLog("⚠ Config file write partial failure: " + err.Error())
	}
	for _, m := range mounts {
		input.Volumes = append(input.Volumes, service.VolumeMount{
			HostPath: m.HostPath,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
		})
	}
}

// runPreDeployCommands executes pre-deploy commands on the target host.
// Returns false (and logs the error) if any command fails.
func (s *Service) runPreDeployCommands(ctx context.Context, deploymentID string, input TriggerInput, orch Orchestrator, appendLog func(string)) bool {
	if len(input.PreDeployCommands) == 0 {
		return true
	}
	input.clock.at("pre-deploy")
	appendLog(fmt.Sprintf("→ running %d pre-deploy command(s)...", len(input.PreDeployCommands)))
	if err := orch.ExecCommands(ctx, input.PreDeployCommands, appendLog); err != nil {
		s.log.Error("pre-deploy command failed", zap.String("deployment_id", deploymentID), zap.Error(err))
		appendLog("✗ Pre-deploy command failed: " + err.Error())
		s.failAndPublish(ctx, deploymentID, input, "", err.Error())
		return false
	}
	return true
}

// runPostDeployCommands executes a compose deploy's post-deploy exec
// commands inside its running containers (a single-container deploy's run
// in its rollout). A command with no target runs in the service's first
// container. It returns the first failure, already logged; the caller
// records it.
func (s *Service) runPostDeployCommands(ctx context.Context, deploymentID string, input TriggerInput, orch Orchestrator, appendLog func(string)) error {
	if len(input.PostDeployCommands) == 0 {
		return nil
	}
	appendLog(fmt.Sprintf("→ running %d post-deploy command(s)...", len(input.PostDeployCommands)))
	for _, pdc := range input.PostDeployCommands {
		containerName := pdc.ServiceName
		containers, _ := orch.ListContainers(ctx, input.ServiceID, input.ServiceName)
		if containerName == "" {
			// Resolve the active container name (blue/green or compose service).
			if len(containers) > 0 {
				containerName = containers[0]
			} else {
				containerName = rollout.BlueContainerName(input.ServiceID, input.ServiceName, input.EnvironmentName) // safe fallback
			}
		} else if !containsString(containers, containerName) {
			// A named container must be one of this service's own: the
			// name is user input and must not reach another service's
			// container (or the control plane's).
			msg := fmt.Sprintf("post-deploy command targets %q, which is not one of this service's containers", containerName)
			appendLog("✗ " + msg)
			return errors.New(msg)
		}
		appendLog(fmt.Sprintf("$ exec [%s] %s", containerName, pdc.Command))
		if err := orch.ExecInContainer(ctx, input.ServiceID, containerName, pdc.Command, appendLog); err != nil {
			s.log.Error("post-deploy command failed", zap.String("deployment_id", deploymentID), zap.Error(err))
			appendLog("✗ Post-deploy command failed: " + err.Error())
			return err
		}
	}
	return nil
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// ─── Pipeline helpers ─────────────────────────────────────────────────────────

func (s *Service) failDeployment(ctx context.Context, deploymentID, serviceID, imageTag, commitSHA, errMsg string) {
	if _, err := s.repo.UpdateStatus(ctx, deploymentID, StatusFailed, imageTag, commitSHA, errMsg); err != nil {
		s.log.Error("failed to update deployment status to failed", zap.Error(err), zap.String("deployment_id", deploymentID))
	}
	if err := s.serviceSvc.UpdateRuntimeStatus(ctx, serviceID, "error", ""); err != nil {
		s.log.Error("failed to update service runtime status", zap.Error(err), zap.String("service_id", serviceID))
	}
}

// failAndPublish records a deploy that failed before it changed what runs
// (the policy, networks, a build or pull, pre-deploy or migrate commands, a
// compose file's checks): when the service was live, its previous version
// still serves, so its runtime status and active deployment are left as they
// are, and the metrics and alerts keep watching it. A service that was not
// live goes to error. The deploy itself is failed and announced either way.
// Failures once containers were changed go through failAfterChange.
func (s *Service) failAndPublish(ctx context.Context, deploymentID string, input TriggerInput, imageTag, errMsg string) {
	ctx, errMsg = s.settled(ctx, deploymentID, errMsg, input.clock.current())
	if s.leaveForRecovery(ctx, deploymentID, errMsg) {
		return
	}
	if input.MoveFrom != nil {
		s.abortMove(ctx, deploymentID, input, imageTag, errMsg)
		return
	}
	if s.isLive(ctx, input.ServiceID) {
		if _, err := s.repo.UpdateStatus(ctx, deploymentID, StatusFailed, imageTag, input.CommitSHA, errMsg); err != nil {
			s.log.Error("failed to update deployment status to failed", zap.Error(err), zap.String("deployment_id", deploymentID))
		}
	} else {
		s.failDeployment(ctx, deploymentID, input.ServiceID, imageTag, input.CommitSHA, errMsg)
	}
	s.publishFailed(input, deploymentID, errMsg)
}

// failAfterChange records a deploy that failed once it changed containers
// (a first deploy's Run, an orchestrator that switches inside Run, docker
// compose up and what follows it): what runs is not known to be the previous
// version, so the service goes to error.
func (s *Service) failAfterChange(ctx context.Context, deploymentID string, input TriggerInput, imageTag, errMsg string) {
	ctx, errMsg = s.settled(ctx, deploymentID, errMsg, input.clock.current())
	if s.leaveForRecovery(ctx, deploymentID, errMsg) {
		return
	}
	if input.MoveFrom != nil {
		s.abortMove(ctx, deploymentID, input, imageTag, errMsg)
		return
	}
	s.failDeployment(ctx, deploymentID, input.ServiceID, imageTag, input.CommitSHA, errMsg)
	s.publishFailed(input, deploymentID, errMsg)
}

// isLive reports whether the service is running a deployment: running, with
// an active deployment. False when it cannot be read.
func (s *Service) isLive(ctx context.Context, serviceID string) bool {
	if s.serviceFetcher == nil {
		return false
	}
	svc, err := s.serviceFetcher.Get(ctx, serviceID)
	return err == nil && svc != nil && svc.RuntimeStatus == "running" && svc.ActiveDeploymentID != ""
}

// leaveForRecovery reports whether the control plane is shutting down
// (BeginShutdown). If so it only logs the failure on the deployment, which
// stays in progress: the next start's recovery marks it interrupted and
// reconciles the service with what runs.
func (s *Service) leaveForRecovery(ctx context.Context, deploymentID, errMsg string) bool {
	if !s.shuttingDown.Load() {
		return false
	}
	msg := "⚠ The control plane is shutting down; this deploy is recovered when it restarts (" + errMsg + ")"
	if handed, err := s.repo.IsHandedOff(ctx, deploymentID); err == nil && handed {
		msg = rollout.PanelLinePrefix + msg // not one of the agent's lines
	}
	if _, err := s.repo.InsertLog(ctx, deploymentID, msg); err != nil {
		s.log.Warn("failed to insert deployment log", zap.Error(err))
	}
	s.log.Info("deploy failed during shutdown; left for recovery", zap.String("deployment_id", deploymentID), zap.String("error", errMsg))
	return true
}

// publishFailed emits a deployment.failed event if a publisher is wired, and
// a deploy-failed alert. Every failed deploy passes here, webhook ones too,
// so it is also where an image service's saved image goes back to what was
// live (followLive).
func (s *Service) publishFailed(input TriggerInput, deploymentID, errMsg string) {
	s.followLive(context.Background(), input)
	s.reportDeploy(context.Background(), input, deploymentID, input.CommitSHA, "failure", errMsg)
	if s.alerts != nil {
		s.alerts.DeployFailed(input.ServiceID, deploymentID, errMsg)
	}
	if s.shipper != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		s.shipper.DeployFailed(ctx, input.ServiceID, deploymentID)
		cancel()
	}
	if s.eventPublisher == nil || input.OrgID == "" {
		return
	}
	s.eventPublisher.Publish(input.OrgID, DeploymentEvent{
		Type:         "deployment.failed",
		DeploymentID: deploymentID,
		ServiceID:    input.ServiceID,
		ServiceName:  input.ServiceName,
		ProjectID:    input.ProjectID,
		Status:       string(StatusFailed),
		Error:        errMsg,
	})
}

func (s *Service) succeedDeployment(ctx context.Context, deploymentID string, input TriggerInput, imageTag, commitSHA string, appendLog func(string)) {
	ctx, _ = s.settled(ctx, deploymentID, "", "")
	if input.MoveFrom != nil && !s.completeMove(ctx, deploymentID, input, imageTag, appendLog) {
		return
	}
	if _, err := s.repo.UpdateStatus(ctx, deploymentID, StatusRunning, imageTag, commitSHA, ""); err != nil {
		s.log.Error("failed to update deployment status to running", zap.Error(err), zap.String("deployment_id", deploymentID))
	}
	if input.reservedMB != nil {
		if err := s.repo.SetReservedMemory(ctx, deploymentID, *input.reservedMB); err != nil {
			s.log.Warn("record the memory the deploy reserved", zap.String("deployment_id", deploymentID), zap.Error(err))
		}
	}
	s.recordShipped(ctx, deploymentID, input)
	s.reportDeploy(ctx, input, deploymentID, commitSHA, "success", "Live")
	if err := s.serviceSvc.UpdateRuntimeStatus(ctx, input.ServiceID, "running", deploymentID); err != nil {
		s.log.Error("failed to update service runtime status", zap.Error(err), zap.String("service_id", input.ServiceID))
	}
	if input.composeNetwork != nil {
		if err := s.serviceSvc.SetComposeNetworkService(ctx, input.ServiceID, *input.composeNetwork); err != nil {
			s.log.Error("failed to record the stack's routed compose service", zap.Error(err), zap.String("service_id", input.ServiceID))
		}
	}
	s.followLive(ctx, input)
	if s.domainActivator != nil {
		if err := s.domainActivator.ActivateForService(ctx, input.ServiceID); err != nil {
			s.log.Warn("failed to activate domains", zap.Error(err), zap.String("service_id", input.ServiceID))
		}
	}
	if s.observabilityRegistrar != nil && input.AddonSlug != "" && input.OrgID != "" {
		if err := s.observabilityRegistrar.RegisterIfObservability(ctx, input.OrgID, input.ServiceID, input.AddonSlug); err != nil {
			s.log.Warn("failed to register observability service", zap.Error(err), zap.String("addon_slug", input.AddonSlug))
		}
	}
	if s.eventPublisher != nil && input.OrgID != "" {
		s.eventPublisher.Publish(input.OrgID, DeploymentEvent{
			Type:         "deployment.running",
			DeploymentID: deploymentID,
			ServiceID:    input.ServiceID,
			ServiceName:  input.ServiceName,
			ProjectID:    input.ProjectID,
			Status:       string(StatusRunning),
		})
	}
	appendLog("✓ Deployment complete")
	if s.shipper != nil {
		shipCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		s.shipper.DeployLive(shipCtx, input.ServiceID)
		cancel()
	}
}

// archiveRef is what a deploy from the project's repository takes: the
// commit asked for, else the branch's tip.
func archiveRef(input TriggerInput) string {
	if input.CheckoutSHA != "" {
		return input.CheckoutSHA
	}
	return input.GitBranch
}

// openArchive starts the tar of ref in the project's repository and
// reports the deploy as running on the commit it resolved to.
func (s *Service) openArchive(ctx context.Context, deploymentID string, input TriggerInput, ref string) (*gitstore.Archive, error) {
	if s.repos == nil {
		return nil, errors.New("this panel doesn't host git, so it can't read the project's repository")
	}
	a, err := s.repos.Archive(ctx, input.ProjectID, input.RepositoryID, ref)
	if err != nil {
		return nil, fmt.Errorf("read the project's repository at %s: %w", ref, err)
	}
	s.reportDeploy(ctx, input, deploymentID, a.SHA, "running", "Deploying")
	return a, nil
}

// closeArchive reads what the build left of a and says whether git wrote
// the whole tar.
func closeArchive(a *gitstore.Archive) error {
	_, _ = io.Copy(io.Discard, a)
	a.Close()
	return a.Err()
}

// buildFromRepository builds imageTag from the project's own repository
// (D-77): git archive's tar of the commit, streamed from paas-git to the
// build host, which needs no URL and no token. A .git directory isn't in
// it, so TOFT_GIT_SHA and TOFT_GIT_BRANCH are build arguments.
func (s *Service) buildFromRepository(ctx context.Context, deploymentID string, input TriggerInput, orch Orchestrator, imageTag string, buildArgs map[string]*string, appendLog func(string)) (Commit, error) {
	ref := archiveRef(input)
	a, err := s.openArchive(ctx, deploymentID, input, ref)
	if err != nil {
		return Commit{}, err
	}
	c := Commit{SHA: a.SHA, Subject: a.Subject}
	appendLog(fmt.Sprintf("Building %s at %s", ref, a.SHA[:7]))
	args := make(map[string]*string, len(buildArgs)+2)
	maps.Copy(args, buildArgs)
	sha, branch := a.SHA, input.GitBranch
	args["TOFT_GIT_SHA"], args["TOFT_GIT_BRANCH"] = &sha, &branch
	var nix *NixpacksCommands
	if input.BuildPack == "nixpacks" {
		nix = &input.Nixpacks
	}
	buildErr := orch.BuildFromArchive(ctx, a, input.BaseDirectory, imageTag, nix, args, appendLog)
	if err := closeArchive(a); err != nil {
		return c, err
	}
	return c, buildErr
}

// unpackRepository writes the tree of ref in the project's repository into
// dir, for a compose deploy's files (D-77).
func (s *Service) unpackRepository(ctx context.Context, deploymentID string, input TriggerInput, ref, dir string) (Commit, error) {
	a, err := s.openArchive(ctx, deploymentID, input, ref)
	if err != nil {
		return Commit{}, err
	}
	c := Commit{SHA: a.SHA, Subject: a.Subject}
	untarErr := untar(a, dir)
	if err := closeArchive(a); err != nil {
		return c, err
	}
	if untarErr != nil {
		return c, fmt.Errorf("unpack the repository: %w", untarErr)
	}
	return c, nil
}

// untar writes the tar r into dir, owned by this process.
func untar(r io.Reader, dir string) error {
	return archive.Untar(r, dir, &archive.TarOptions{NoLchown: true})
}

// reportDeploy records the deploy as the check of the commit it built, for
// a service building from the project's own repository (D-77).
func (s *Service) reportDeploy(ctx context.Context, input TriggerInput, deploymentID, sha, state, description string) {
	if s.repos == nil || input.RepositoryID == "" || sha == "" || input.RollbackImageTag != "" {
		return
	}
	s.repos.ReportDeploy(ctx, input.RepositoryID, sha, input.EnvironmentName, input.ServiceName, state, description, deploymentID, input.Preview)
}

// checkoutRepo shallow-clones repoURL into dir: at commitSHA when one is
// given, so the deploy gets exactly that commit's files, otherwise at the
// tip of branch. Fetching a commit needs its full SHA (GitHub, GitLab and
// Gitea serve one that is reachable from a ref).
func checkoutRepo(ctx context.Context, repoURL, branch, commitSHA, dir string) ([]byte, error) {
	cloneURL, gitArgs, forget, err := gitcred.Local(repoURL)
	if err != nil {
		return nil, err
	}
	defer forget()
	if commitSHA == "" {
		args := append(gitArgs, "clone", "--depth", "1", "--branch", branch, "--progress", "--", cloneURL, dir)
		return exec.CommandContext(ctx, "git", args...).CombinedOutput()
	}
	var all []byte
	for _, args := range [][]string{
		{"init", "--quiet", dir},
		append(append([]string{}, gitArgs...), "-C", dir, "fetch", "--depth", "1", "--progress", "--", cloneURL, commitSHA),
		{"-C", dir, "checkout", "--quiet", "--detach", "FETCH_HEAD"},
	} {
		out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput()
		all = append(all, out...)
		if err != nil {
			return all, err
		}
	}
	return all, nil
}

// recordCommit records the commit a git build checked out and its subject.
// Not fatal: the deployment then shows what it was started with.
func (s *Service) recordCommit(ctx context.Context, deploymentID string, c Commit) {
	if err := s.repo.SetCommit(ctx, deploymentID, c.SHA, c.Subject); err != nil {
		s.log.Warn("could not record the built commit", zap.Error(err), zap.String("deployment_id", deploymentID))
	}
}

// recordComposeCommit records the commit checked out in dir, so a rollback
// to this deployment gets the same compose files. Not fatal: without it, a
// rollback uses the branch tip's files. It returns the commit, "" when
// unknown.
func (s *Service) recordComposeCommit(ctx context.Context, deploymentID, dir string, appendLog func(string)) string {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	sha := strings.TrimSpace(string(out))
	if err == nil && !isFullSHA(sha) {
		err = fmt.Errorf("unexpected commit %q", sha)
	}
	if err == nil {
		err = s.repo.SetComposeCommit(ctx, deploymentID, sha)
	}
	if err != nil {
		s.log.Warn("could not record the compose commit", zap.Error(err), zap.String("deployment_id", deploymentID))
		appendLog("⚠ Could not record the commit checked out; a rollback to this deployment will use the branch tip's files")
		if !isFullSHA(sha) {
			return ""
		}
		return sha
	}
	appendLog("→ compose files at commit " + sha)
	return sha
}

// injectGitCredentials embeds HTTP credentials into a repo URL so git clone can
// authenticate without an interactive prompt.
// GitHub PAT-only style:  https://{token}@github.com/org/repo.git
// Username + token style: https://{user}:{token}@github.com/org/repo.git
func injectGitCredentials(rawURL, username, token string) string {
	if token == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	if username != "" {
		u.User = url.UserPassword(username, token)
	} else {
		u.User = url.User(token)
	}
	return u.String()
}

// sanitizeRepoURL strips embedded credentials from a repo URL before logging.
func sanitizeRepoURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	u.User = nil
	return u.String()
}

// SplitLines splits a newline-separated string into non-empty trimmed lines.
func SplitLines(s string) []string {
	raw := strings.Split(s, "\n")
	result := make([]string, 0, len(raw))
	for _, line := range raw {
		if t := strings.TrimSpace(line); t != "" {
			result = append(result, t)
		}
	}
	return result
}

func kvsToMap(kvs []string) map[string]string {
	m := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		idx := len(kv)
		for i, c := range kv {
			if c == '=' {
				idx = i
				break
			}
		}
		if idx < len(kv) {
			m[kv[:idx]] = kv[idx+1:]
		}
	}
	return m
}

// buildTimePublicPrefixes lists the well-known framework prefixes whose values are
// intentionally public (baked into the browser bundle at build time). Only these are
// forwarded as --build-arg; everything else is runtime-only and must not appear in
// image layers where `docker history --no-trunc` would expose it.
var buildTimePublicPrefixes = []string{
	"NEXT_PUBLIC_", // Next.js
	"VITE_",        // Vite
	"REACT_APP_",   // Create React App
	"NG_APP_",      // Angular
}

func isBuildTimeVar(key string) bool {
	for _, prefix := range buildTimePublicPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// envsToBuildArgs converts a KEY=VALUE slice into a Docker build-args map.
// Only variables with well-known public prefixes (NEXT_PUBLIC_, VITE_, REACT_APP_,
// NG_APP_) are forwarded as --build-arg. Runtime secrets and other env vars are
// injected at container start via docker run --env, not at build time — this
// prevents sensitive values from being visible in image layer metadata.
func envsToBuildArgs(envVars []string) map[string]*string {
	args := make(map[string]*string)
	for _, kv := range envVars {
		if idx := strings.IndexByte(kv, '='); idx > 0 {
			k := kv[:idx]
			if isBuildTimeVar(k) {
				v := kv[idx+1:]
				args[k] = &v
			}
		}
	}
	return args
}

// checkRequiredEnvVars returns the names of any required keys absent from the env var list.
func checkRequiredEnvVars(required []string, envVars []string) []string {
	if len(required) == 0 {
		return nil
	}
	present := make(map[string]struct{}, len(envVars))
	for _, kv := range envVars {
		if idx := strings.IndexByte(kv, '='); idx > 0 {
			present[kv[:idx]] = struct{}{}
		}
	}
	var missing []string
	for _, key := range required {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, ok := present[key]; !ok {
			missing = append(missing, key)
		}
	}
	return missing
}

func (s *Service) ListDeployments(ctx context.Context, serviceID string) ([]Deployment, error) {
	return s.repo.ListByService(ctx, serviceID)
}

func (s *Service) ListDeploymentsPaginated(ctx context.Context, serviceID string, limit int32, before *time.Time, beforeID string) ([]Deployment, error) {
	ds, err := s.repo.ListByServicePaginated(ctx, serviceID, limit, before, beforeID)
	if err != nil {
		return nil, err
	}
	return ds, s.withPulls(ctx, ds)
}

// withPulls names the pull request each deployment's merge was (D-77).
func (s *Service) withPulls(ctx context.Context, ds []Deployment) error {
	ids := make([]string, 0, len(ds))
	for _, d := range ds {
		ids = append(ids, d.ID)
	}
	pulls, err := s.repo.ListPulls(ctx, ids)
	if err != nil {
		return err
	}
	for i := range ds {
		if p, ok := pulls[ds[i].ID]; ok {
			ds[i].PullRequest = &p
		}
	}
	return nil
}

func (s *Service) GetDeployment(ctx context.Context, id string) (*Deployment, error) {
	return s.repo.GetByID(ctx, id)
}

// GetDeploymentForService returns deployment id only when it belongs to
// serviceID, and ErrDeploymentNotFound otherwise (or when it does not exist),
// so a deployment of another service, possibly another workspace's, can never
// be read or rolled back to through a service the caller owns.
func (s *Service) GetDeploymentForService(ctx context.Context, serviceID, id string) (*Deployment, error) {
	d, err := s.repo.GetByID(ctx, id)
	if err != nil || d == nil || d.ServiceID != serviceID {
		return nil, ErrDeploymentNotFound
	}
	one := []Deployment{*d}
	if err := s.withPulls(ctx, one); err != nil {
		return nil, err
	}
	return &one[0], nil
}

func (s *Service) GetLogs(ctx context.Context, deploymentID string) ([]Log, error) {
	return s.repo.ListLogs(ctx, deploymentID)
}

func (s *Service) GetLogsFiltered(ctx context.Context, deploymentID, search string) ([]Log, error) {
	return s.repo.ListLogsFiltered(ctx, deploymentID, search)
}

func (s *Service) GetLogsAfter(ctx context.Context, deploymentID string, afterID int64) ([]Log, error) {
	return s.repo.ListLogsAfter(ctx, deploymentID, afterID)
}

// RunGC deletes deployment records (and their logs via CASCADE) beyond the most
// recent keepLast per service. Safe to call concurrently.
func (s *Service) RunGC(ctx context.Context, keepLast int) {
	serviceIDs, err := s.repo.ListServiceIDs(ctx)
	if err != nil {
		s.log.Warn("deployment GC: failed to list service IDs", zap.Error(err))
		return
	}
	for _, id := range serviceIDs {
		if err := s.repo.DeleteOldDeploymentsForService(ctx, id, keepLast); err != nil {
			s.log.Warn("deployment GC: failed to prune service", zap.String("service_id", id), zap.Error(err))
		}
	}
	s.log.Info("deployment GC complete", zap.Int("services_checked", len(serviceIDs)), zap.Int("keep_last", keepLast))
}

// securityFor is how a service's containers are walled in: its own process
// limit, raw sockets only by an operator's exception (D-93), and gVisor
// when it runs Sandboxed, where raw sockets are never kept (D-95). A
// service it can't read fails the deploy, so a Sandboxed one never runs
// under runc by mistake.
func (s *Service) securityFor(ctx context.Context, serviceID string) (rollout.Security, error) {
	var sec rollout.Security
	if s.serviceFetcher != nil && serviceID != "" {
		svc, err := s.serviceFetcher.Get(ctx, serviceID)
		if err != nil {
			return sec, fmt.Errorf("read the service's isolation: %w", err)
		}
		sec.PidsLimit = int64(svc.PidsLimit)
		switch svc.EffectiveIsolation {
		case service.IsolationSandboxed:
			sec.Runtime = rollout.RuntimeSandboxed
		case service.IsolationSandboxedStrict:
			sec.Runtime = rollout.RuntimeStrict
		}
	}
	if s.policy != nil && serviceID != "" && sec.Runtime == "" {
		sec.NetRaw = s.policy.AllowsNetRaw(ctx, serviceID)
	}
	return sec, nil
}

// containerCheckFor is the container check a deploy creates the service's
// container with: of the service the deploy was built from, as its settings
// fingerprint (D-159), or of the service now. Settings without it (read by
// a list, not Get, which always fills it) are read again. A service it can't
// read fails the deploy, so a check turned off never comes back by mistake.
func (s *Service) containerCheckFor(ctx context.Context, input TriggerInput) (rollout.ContainerCheck, error) {
	svc := input.Settings
	if svc == nil || svc.ContainerHealth == "" {
		if s.serviceFetcher == nil || input.ServiceID == "" {
			return rollout.ContainerCheck{}, nil
		}
		var err error
		if svc, err = s.serviceFetcher.Get(ctx, input.ServiceID); err != nil {
			return rollout.ContainerCheck{}, fmt.Errorf("read the service's container check: %w", err)
		}
	}
	switch svc.ContainerHealth {
	case service.ContainerHealthOff:
		return rollout.ContainerCheck{Off: true}, nil
	case service.ContainerHealthCommand:
		return rollout.ContainerCheck{Command: svc.ContainerHealthCommand}, nil
	}
	return rollout.ContainerCheck{}, nil
}

// withServiceURL adds TOFT_URL (deploypolicy.ServiceURL) to a compose
// stack's variables. A TOFT_URL of the service's own wins; with no domain
// there is none, and a template that needs one says so (${TOFT_URL:?…}).
func withServiceURL(kvs, domains, certified []string) []string {
	u := deploypolicy.ServiceURL(domains, certified)
	if u == "" {
		return kvs
	}
	for _, kv := range kvs {
		if strings.HasPrefix(kv, "TOFT_URL=") {
			return kvs
		}
	}
	return append(slices.Clip(kvs), "TOFT_URL="+u)
}
