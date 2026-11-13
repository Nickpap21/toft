package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nickpap21/toft/backend/internal/catalog"
	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/configfile"
	"github.com/Nickpap21/toft/backend/internal/deploypolicy"
	"github.com/Nickpap21/toft/backend/internal/domain"
	"github.com/Nickpap21/toft/backend/internal/netplan"
	"github.com/Nickpap21/toft/backend/internal/resources"
	serverPkg "github.com/Nickpap21/toft/backend/internal/server"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// ServerFetcher fetches a server by ID. Satisfied by server.Service.
type ServerFetcher interface {
	GetServer(ctx context.Context, id string) (*serverPkg.Server, error)
}

// ContainerOrchestrator handles container operations for a service.
// deployment.Orchestrator is a superset of this interface.
type ContainerOrchestrator interface {
	Stop(ctx context.Context, serviceID string) error
	StopCompose(ctx context.Context, serviceID, serviceName, composeContent string) error
	StreamLogs(ctx context.Context, serviceID string, tailLines int, containerName string, logFn func(string)) error
	ExecInContainer(ctx context.Context, serviceID, containerName, command string, logFn func(string)) error
	ListContainers(ctx context.Context, serviceID, serviceName string) ([]string, error)
}

// ContainerOrchestratorFactory produces a ContainerOrchestrator for a given server.
type ContainerOrchestratorFactory interface {
	ForServer(srv *serverPkg.Server) (ContainerOrchestrator, func(), error)
}

// ConfigFileSeeder seeds default config files for a service created from a catalog addon.
// Satisfied by *configfile.Service — defined here to allow service to import configfile
// without creating a circular dependency.
type ConfigFileSeeder interface {
	Upsert(ctx context.Context, serviceID string, in configfile.UpsertInput) (configfile.ConfigFile, error)
}

// PolicyChecker checks a service configuration against the deploy policy
// (D-19) before it is saved. Satisfied by *deploypolicy.Service. Save-time
// checks are fast feedback; the deploy-time check is the authoritative one.
type PolicyChecker interface {
	CheckSave(ctx context.Context, in deploypolicy.SaveCheck) deploypolicy.Decision
}

// OperatorChecker reports whether a user is an instance operator. Satisfied
// by instance.Service.
type OperatorChecker interface {
	IsOperator(ctx context.Context, userID string) (bool, error)
}

// ErrInvalidConfig wraps every validation error in a service configuration
// (port, limits, the domain routing choice). The handlers answer it with 400
// and the message.
var ErrInvalidConfig = errors.New("invalid service configuration")

// ErrNoLoopbackPort: every 127.0.0.1 port Toft gives a catalog Loki on a
// server (39100-39199) is taken (D-148).
var ErrNoLoopbackPort = errors.New("every port from 39100 to 39199 on this server is taken by another Loki")

// ErrHostExecOperatorOnly is returned when someone who is not an instance
// operator asks to run a command on a server's host shell.
var ErrHostExecOperatorOnly = errors.New("only instance operators can run commands on the host server")

// ErrContainerNotInService is returned when a container name does not belong
// to the service (its paas.service_id label or its compose project).
var ErrContainerNotInService = errors.New("that container does not belong to this service")

// HostContainer is the container name that asks ExecInService to run the
// command on the server's host shell instead of in a container.
const HostContainer = "@server"

// ServiceManager contains the business logic for managing Services.
type ServiceManager struct {
	repo             Repository
	log              *zap.Logger
	serverFetcher    ServerFetcher
	factory          ContainerOrchestratorFactory
	configFileSeeder ConfigFileSeeder
	policy           PolicyChecker
	operators        OperatorChecker
	envReader        EnvVarReader       // network preflight; optional
	sharedMounts     SharedMountChecker // optional; nil allows every move
	resources        ResourceLimits     // optional; nil checks no memory limits
	sandboxes        SandboxChecker     // optional; nil starts no new service Sandboxed
	collectNetworks  bool               // isolated app networks: deletes remove unused ones
	logs             logHub             // live logs shared among their viewers (K11)
	addresser        Addresser          // optional; nil names no address at creation
	projects         ProjectCreator     // CreateWithProject's (7g)
	variables        VariableWriter     // a create's secrets, CreateWithProject's variables (7g)
	reveals          RevealKeeper       // optional; a create's admin password for its creator (D-160)
	tx               Transactor         // a create with secrets
	panelRole        PanelRole          // optional; nil = the panel host runs apps
	capacity         CapacityGate       // optional; nil refuses no create for room
}

// PanelRole says whether the panel host runs apps (D-26, D-62). Satisfied by
// *instance.Service.
type PanelRole interface {
	PanelHostRunsApps(ctx context.Context) bool
}

// ErrPanelHostNoApps refuses a new service on the panel host while its
// server role is Only Toft: its first deploy would be refused there.
var ErrPanelHostNoApps = errors.New("this panel's server runs no apps: its role on Instance is Only Toft. " +
	"Create the service on another server, or change the role to Run apps here too")

// WithPanelRole refuses new services on the panel host under Only Toft.
func (s *ServiceManager) WithPanelRole(r PanelRole) { s.panelRole = r }

// checkPanelHost is ErrPanelHostNoApps for a service created on the panel
// host ("") while it runs no apps.
func (s *ServiceManager) checkPanelHost(ctx context.Context, serverID string) error {
	if serverID == "" && s.panelRole != nil && !s.panelRole.PanelHostRunsApps(ctx) {
		return ErrPanelHostNoApps
	}
	return nil
}

// Addresser names a new web service's address (D-150): preview.Service,
// through the environment's project, in main.go. ErrNoPublicAddress when
// the server has none.
type Addresser interface {
	Address(ctx context.Context, environmentID, serverID, name string) (string, error)
	// AddressIn names it in a project and environment that don't exist yet
	// (a new project's first service, 7g).
	AddressIn(ctx context.Context, project, environment, serverID, name string) (string, error)
	// ServerAddress is the public IPv4 a server's names point at, "" when it
	// has none.
	ServerAddress(ctx context.Context, serverID string) (string, error)
}

// ErrAddressTaken: another service took the address named for a new one
// between naming and creating it; creating again names the next.
var ErrAddressTaken = errors.New("its address was just taken by another service: create it again")

// ErrNoPublicAddress: the service's server has no public address to name.
var ErrNoPublicAddress = errors.New("the server has no public address")

// WithAddresser lets a create give a web service its address (D-150).
func (s *ServiceManager) WithAddresser(a Addresser) {
	s.addresser = a
}

// Address is the address a new service would get (D-150): Hostname, or ""
// with Reason.
type Address struct {
	Hostname  string `json:"hostname"`
	Temporary bool   `json:"temporary"`
	Reason    string `json:"reason"`
}

// AddressQuery is the new service AddressFor names.
type AddressQuery struct {
	Name, Type, ServerID, AddonSlug, DockerImage string
	Port                                         int
}

// WantsAddress reports whether a new service gets an address by default
// (D-150): a web service with a port that is an app. From the catalog, its
// "apps" (databases, caches, admin tools, Loki and the rest stay internal);
// otherwise by its image's kind.
func WantsAddress(typ, addonSlug, image string, port int) bool {
	if Type(typ) == TypeWorker || port <= 0 {
		return false
	}
	if addonSlug != "" {
		t, ok := catalog.Get(addonSlug)
		return ok && t.Category == "apps"
	}
	return resources.NewKindOf("", image) == resources.KindApp
}

// AddressFor is the address q would get in environmentID (D-150).
func (s *ServiceManager) AddressFor(ctx context.Context, environmentID string, q AddressQuery) (Address, error) {
	return s.address(q, func() (string, error) { return s.addresser.Address(ctx, environmentID, q.ServerID, q.Name) })
}

// address is the address q gets, named by name when it gets one.
func (s *ServiceManager) address(q AddressQuery, name func() (string, error)) (Address, error) {
	if !WantsAddress(q.Type, q.AddonSlug, q.DockerImage, q.Port) {
		return Address{Reason: "not_web"}, nil
	}
	if s.addresser == nil {
		return Address{Reason: "no_public_address"}, nil
	}
	h, err := name()
	if errors.Is(err, ErrNoPublicAddress) {
		return Address{Reason: "no_public_address"}, nil
	}
	if err != nil {
		return Address{}, err
	}
	return Address{Hostname: h, Temporary: domain.IsTemporary(h)}, nil
}

// WithNetworkCollection makes bulk deletes remove the app networks no
// service uses any more. Only under isolated app networks (main.go).
func (s *ServiceManager) WithNetworkCollection() {
	s.collectNetworks = true
}

// SavedService is a service as returned by a create or a config save. In
// warn mode (TOFT_DEPLOY_POLICY=warn) PolicyViolations lists what the deploy
// policy would refuse; in enforce mode such a save fails instead.
type SavedService struct {
	*Service
	PolicyViolations []composespec.Violation `json:"policy_violations,omitempty"`
	// What a create generated (withGenerated), shown this once.
	GeneratedSecrets []KeyValue `json:"generated_secrets,omitempty"`
	TemporaryLogin   *Login     `json:"temporary_login,omitempty"`
	// The first deploy a create started (CreateInput.Deploy), or why none
	// did (Handler.firstDeploy).
	Deployment      any    `json:"deployment,omitempty"`
	DeployError     string `json:"deploy_error,omitempty"`
	DeployErrorCode string `json:"deploy_error_code,omitempty"`
}

// MarshalJSON is needed because the embedded Service's MarshalJSON would
// otherwise be promoted and drop PolicyViolations.
func (s SavedService) MarshalJSON() ([]byte, error) {
	var svc serviceJSON
	if s.Service != nil {
		svc = s.Service.presented()
	}
	return json.Marshal(struct {
		serviceJSON
		PolicyViolations []composespec.Violation `json:"policy_violations,omitempty"`
		GeneratedSecrets []KeyValue              `json:"generated_secrets,omitempty"`
		TemporaryLogin   *Login                  `json:"temporary_login,omitempty"`
		Deployment       any                     `json:"deployment,omitempty"`
		DeployError      string                  `json:"deploy_error,omitempty"`
		DeployErrorCode  string                  `json:"deploy_error_code,omitempty"`
	}{svc, s.PolicyViolations, s.GeneratedSecrets, s.TemporaryLogin, s.Deployment, s.DeployError, s.DeployErrorCode})
}

func NewServiceManager(repo Repository, log *zap.Logger) *ServiceManager {
	return &ServiceManager{repo: repo, log: log}
}

// WithOrchestration injects the orchestration dependencies for container stop/log operations.
func (s *ServiceManager) WithOrchestration(sf ServerFetcher, f ContainerOrchestratorFactory) {
	s.serverFetcher = sf
	s.factory = f
}

// WithConfigFileSeeder injects the config file seeder for addon service creation.
func (s *ServiceManager) WithConfigFileSeeder(cs ConfigFileSeeder) {
	s.configFileSeeder = cs
}

// WithPolicyChecker enables the save-time deploy policy check.
func (s *ServiceManager) WithPolicyChecker(p PolicyChecker) {
	s.policy = p
}

// WithOperatorChecker enables host-shell exec for instance operators.
// Without one, host exec is refused for everyone (fail closed).
func (s *ServiceManager) WithOperatorChecker(c OperatorChecker) {
	s.operators = c
}

// SharedMountChecker says whether a service mounts a shared volume (D-38).
// Satisfied by sharedvolume.Service.
type SharedMountChecker interface {
	HasMounts(ctx context.Context, serviceID string) (bool, error)
}

// ErrMountsSharedVolume: a service that mounts a shared volume cannot move
// to another server, since the volume stays on its own.
var ErrMountsSharedVolume = errors.New("the service mounts a shared volume, which stays on its server: remove the mount under Settings → Volumes first")

// ErrMoveWhileRunning: assigning another server to a service that runs, or
// is deploying, would leave its containers on the old server, out of
// Stop's and Delete's reach. A running service moves with a deploy to the
// new server (POST /deployments with server_id), which removes it from the
// old one once it runs on the new one.
var ErrMoveWhileRunning = errors.New("the service runs on its current server: move it with a deploy to the new server, or stop it first")

// WithSharedMounts refuses moving a service that mounts a shared volume.
func (s *ServiceManager) WithSharedMounts(c SharedMountChecker) { s.sharedMounts = c }

// ResourceLimits is the memory rule's data (D-41). Satisfied by
// *resources.Service.
type ResourceLimits interface {
	MemoryCheckFor(ctx context.Context, ref resources.ServiceRef) (*resources.MemoryCheck, error)
	AddComposeDefaults(ctx context.Context, serviceID string, src composespec.Source) (map[string]resources.DefaultAdded, error)
	NewComposeDefaults(ctx context.Context, src composespec.Source) map[string]resources.DefaultAdded
}

// WithResources checks memory limits on config saves and gives new compose
// services their kind's default. Without it neither happens.
func (s *ServiceManager) WithResources(r ResourceLimits) { s.resources = r }

// SandboxChecker reads whether a server can run Sandboxed services, and
// Sandboxed with a strict network ("" is the panel's own). Satisfied by
// *server.Service.
type SandboxChecker interface {
	Sandboxed(ctx context.Context, serverID string) (serverPkg.Level, string, error)
	SandboxedStrict(ctx context.Context, serverID string) (serverPkg.Level, string, error)
}

// WithSandboxes starts new services Sandboxed where that is recommended
// and their server can run it (D-99).
func (s *ServiceManager) WithSandboxes(c SandboxChecker) { s.sandboxes = c }

// CapacityGate is the deploy's capacity gate, asked of a service not created
// yet. Satisfied by *deployment.Service.
type CapacityGate interface {
	CheckNewCapacity(ctx context.Context, svc *Service, composeMemoryMB float64, userID, memberRole, orgID string) (*serverPkg.CapacityExceededError, error)
}

// WithCapacityGate refuses creating a service to be deployed at once
// (CreateInput.Deploy) that won't fit its server (CheckRoom).
func (s *ServiceManager) WithCapacityGate(g CapacityGate) { s.capacity = g }

// CheckRoom refuses, before anything is created, a service to be deployed as
// soon as it is (in.Deploy) that won't fit its server: the deploy's own gate,
// with the limits it will be created with, so the deploy that follows isn't
// refused with the service left holding its reservation. in.Force lets past
// a caller the gate allows to bypass, as the deploy's ?force=true. A failed
// check refuses nothing, as at the deploy.
func (s *ServiceManager) CheckRoom(ctx context.Context, in CreateInput, userID, memberRole, orgID string) *serverPkg.CapacityExceededError {
	if !in.Deploy || s.capacity == nil {
		return nil
	}
	in = withDefaultMemory(in)
	var composeMB float64
	if src, ok := newInlineStack("", in.Name, in); ok && s.resources != nil {
		for _, d := range s.resources.NewComposeDefaults(ctx, src) {
			composeMB += float64(d.MemoryMB)
		}
	}
	capErr, err := s.capacity.CheckNewCapacity(ctx, &Service{ServerID: in.ServerID, SourceType: in.SourceType,
		CPULimit: in.CPULimit, MemoryLimit: in.MemoryLimit}, composeMB, userID, memberRole, orgID)
	if err != nil {
		s.log.Error("capacity check of a new service", zap.Error(err))
		return nil
	}
	if capErr != nil && in.Force && capErr.CanBypass {
		return nil
	}
	return capErr
}

// ResourceRef is what resources reads of a service for its usage and
// suggestions (D-41). With CheckOwnership and ResourceRefsOnServer it
// satisfies resources.Services.
func (s *ServiceManager) ResourceRef(ctx context.Context, id string) (*resources.ServiceRef, error) {
	svc, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	ref := resourceRef(svc)
	return &ref, nil
}

// ResourceRefsOnServer is ResourceRef of every service of the workspace on
// a server, the services GET /servers/:serverID/services lists, in one query.
func (s *ServiceManager) ResourceRefsOnServer(ctx context.Context, orgID, serverID string) ([]resources.ServiceRef, error) {
	svcs, err := s.repo.ListResourceFieldsOnServer(ctx, orgID, serverID)
	if err != nil {
		return nil, err
	}
	out := make([]resources.ServiceRef, 0, len(svcs))
	for i := range svcs {
		out = append(out, resourceRef(&svcs[i]))
	}
	return out, nil
}

// resourceRef carries an inline compose file, loaded without the service's
// env vars (reading them decrypts its secrets, and the server's summary
// would read them per service): a limit the file takes from a variable
// without a default shows as none. A repository's file is not known here.
func resourceRef(svc *Service) resources.ServiceRef {
	ref := resources.ServiceRef{
		ID: svc.ID, Name: svc.Name, SourceType: string(svc.SourceType),
		MemoryLimit: svc.MemoryLimit, CPULimit: svc.CPULimit, AddonSlug: svc.AddonSlug, DockerImage: svc.DockerImage,
	}
	if svc.SourceType == SourceTypeDockerCompose && svc.ComposeRepoURL == "" && strings.TrimSpace(svc.ComposeContent) != "" {
		src := deploypolicy.InlineComposeSource(svc.ID, svc.Name, svc.ComposeContent, nil)
		ref.Compose = &src
	}
	return ref
}

// checkPolicy runs the save-time policy check. In enforce mode a violation
// is returned as *composespec.ViolationsError; in warn mode the violations
// are returned for the response.
func (s *ServiceManager) checkPolicy(ctx context.Context, in deploypolicy.SaveCheck) ([]composespec.Violation, error) {
	if s.policy == nil {
		return nil, nil
	}
	d := s.policy.CheckSave(ctx, in)
	if err := d.Err(); err != nil {
		return nil, err
	}
	return d.Violations, nil
}

func volumeSpecs(vols []VolumeMount) []deploypolicy.VolumeSpec {
	out := make([]deploypolicy.VolumeSpec, len(vols))
	for i, v := range vols {
		out[i] = deploypolicy.VolumeSpec{Name: v.Name, Target: v.Target, HostPath: v.HostPath}
	}
	return out
}

func saveCheckFromCreate(input CreateInput) deploypolicy.SaveCheck {
	return deploypolicy.SaveCheck{
		ServiceName:       input.Name,
		SourceType:        string(input.SourceType),
		ComposeContent:    input.ComposeContent,
		ComposeRepoURL:    input.ComposeRepoURL,
		Volumes:           volumeSpecs(input.Volumes),
		PreDeployCommands: input.PreDeployCommands,
	}
}

// withDefaultMemory gives a new single-container service without a memory
// limit its kind's default (D-41).
func withDefaultMemory(in CreateInput) CreateInput {
	if in.SourceType != SourceTypeDockerCompose && in.MemoryLimit == "" {
		in.MemoryLimit = resources.FormatMB(resources.DefaultMemoryMB(resources.NewKindOf(in.AddonSlug, in.DockerImage), in.AddonSlug, in.DockerImage))
	}
	return in
}

// addComposeDefaults gives a new inline stack's compose services without a
// memory limit their kind's default (D-41). The service is created by then,
// so an error is logged: the first deploy names the limit to set.
func (s *ServiceManager) addComposeDefaults(ctx context.Context, svc *Service, in CreateInput) {
	src, ok := newInlineStack(svc.ID, svc.Name, in)
	if s.resources == nil || !ok {
		return
	}
	if _, err := s.resources.AddComposeDefaults(ctx, svc.ID, src); err != nil {
		s.log.Error("add default memory limits to a new compose stack", zap.String("service_id", svc.ID), zap.Error(err))
	}
}

// newInlineStack is the compose file a new inline stack gets its default
// limits from (addComposeDefaults, and CheckRoom before it exists); false
// for anything else, a repository's stack included: its file is not known
// before the first deploy clones it.
func newInlineStack(id, name string, in CreateInput) (composespec.Source, bool) {
	if in.SourceType != SourceTypeDockerCompose || in.ComposeRepoURL != "" || strings.TrimSpace(in.ComposeContent) == "" {
		return composespec.Source{}, false
	}
	return deploypolicy.InlineComposeSource(id, name, in.ComposeContent, nil), true
}

func (s *ServiceManager) Create(ctx context.Context, environmentID, userID string, input CreateInput) (*SavedService, error) {
	if err := validateCreateInput(input); err != nil {
		return nil, err
	}
	if err := checkKeysOnce(input); err != nil {
		return nil, err
	}
	if err := s.checkPanelHost(ctx, input.ServerID); err != nil {
		return nil, err
	}
	if err := s.checkRepository(ctx, input.RepositoryID, environmentID, ""); err != nil {
		return nil, err
	}
	input, generated, login := withGenerated(withDefaultMemory(input))
	nameAddress := func() error {
		a, err := s.AddressFor(ctx, environmentID, AddressQuery{Name: input.Name, Type: string(input.Type), ServerID: input.ServerID,
			AddonSlug: input.AddonSlug, DockerImage: input.DockerImage, Port: input.Port})
		if err != nil {
			return fmt.Errorf("name its address: %w", err)
		}
		input.AddressHostname = a.Hostname // "" when it gets none: the screen said why
		return nil
	}
	if input.Address {
		if err := nameAddress(); err != nil {
			return nil, err
		}
	}
	warnings, err := s.checkPolicy(ctx, saveCheckFromCreate(input))
	if err != nil {
		return nil, err
	}
	if input.NetworkAlias == "" {
		input.NetworkAlias = sanitizeNetworkAlias(input.Name)
	}
	if err := s.validateAliasUnique(ctx, environmentID, "", input.NetworkAlias); err != nil {
		return nil, err
	}
	svc, err := s.createWithSecrets(ctx, userID, input, func(ctx context.Context) (*Service, error) {
		svc, err := s.repo.Create(ctx, environmentID, input)
		if errors.Is(err, ErrAddressTaken) {
			// Another service took the name since it was named: name the next.
			if err := nameAddress(); err != nil {
				return nil, err
			}
			svc, err = s.repo.Create(ctx, environmentID, input)
		}
		return svc, err
	})
	if err != nil {
		return nil, err
	}
	s.addComposeDefaults(ctx, svc, input)
	s.seedConfigFiles(ctx, svc.ID, input.AddonSlug)
	s.startSandboxed(ctx, svc, input)
	return &SavedService{Service: svc, PolicyViolations: warnings, GeneratedSecrets: generated, TemporaryLogin: login}, nil
}

// startSandboxed gives a new service the Sandboxed level when that is
// recommended (an admin tool, or an app on the panel's own server) and its
// server can run it (D-99). Never a compose stack: it may hold a database,
// which Sandboxed slows most. Never below a workspace default above
// Sandboxed (D-115). The service is created by then, so an error is logged
// and the service follows the workspace's default.
func (s *ServiceManager) startSandboxed(ctx context.Context, svc *Service, in CreateInput) {
	if s.sandboxes == nil || in.SourceType == SourceTypeDockerCompose ||
		AdviseIsolation(in.AddonSlug, in.DockerImage, in.ServerID).Level != IsolationSandboxed {
		return
	}
	_, def, err := s.repo.Isolation(ctx, svc.ID)
	if err == nil && isolationRank[def] > isolationRank[IsolationSandboxed] {
		return
	}
	level, _, err := s.sandboxes.Sandboxed(ctx, in.ServerID)
	if err == nil && level.State != "available" {
		return
	}
	if err == nil {
		err = s.repo.SetIsolation(ctx, svc.ID, IsolationSandboxed)
	}
	if err != nil {
		s.log.Error("start a new service Sandboxed", zap.String("service_id", svc.ID), zap.Error(err))
		return
	}
	svc.Isolation, svc.EffectiveIsolation = IsolationSandboxed, IsolationSandboxed
}

// seedConfigFiles seeds default config files from the catalog template for a new addon service.
// Errors are non-fatal: logged as warnings so service creation is never blocked.
func (s *ServiceManager) seedConfigFiles(ctx context.Context, serviceID, addonSlug string) {
	if s.configFileSeeder == nil || addonSlug == "" {
		return
	}
	tmpl, ok := catalog.Get(addonSlug)
	if !ok || len(tmpl.ConfigFiles) == 0 {
		return
	}
	for _, cf := range tmpl.ConfigFiles {
		if _, err := s.configFileSeeder.Upsert(ctx, serviceID, configfile.UpsertInput{
			Path:    cf.Path,
			Content: cf.Content,
		}); err != nil {
			s.log.Warn("failed to seed config file",
				zap.String("service_id", serviceID),
				zap.String("addon_slug", addonSlug),
				zap.String("path", cf.Path),
				zap.Error(err),
			)
		}
	}
}

func (s *ServiceManager) Get(ctx context.Context, id string) (*Service, error) {
	svc, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if svc.PidsLimit, err = s.repo.PidsLimit(ctx, id); err != nil {
		return nil, err
	}
	if svc.DeployTimeoutMinutes, err = s.repo.DeployTimeout(ctx, id); err != nil {
		return nil, err
	}
	if svc.RollbackImagesKept, err = s.repo.RollbackImagesKept(ctx, id); err != nil {
		return nil, err
	}
	if svc.ContainerHealth, svc.ContainerHealthCommand, err = s.repo.ContainerHealth(ctx, id); err != nil {
		return nil, err
	}
	if err := s.readIsolation(ctx, svc); err != nil {
		return nil, err
	}
	return svc, nil
}

// Isolation levels (D-95, D-115), weakest first.
const (
	IsolationStandard        = "standard"
	IsolationSandboxed       = "sandboxed"
	IsolationSandboxedStrict = "sandboxed_strict"
	isolationDefault         = "default" // on input: follow the workspace's
)

// isolationRank orders the levels: a higher one is a stronger wall.
var isolationRank = map[string]int{IsolationStandard: 0, IsolationSandboxed: 1, IsolationSandboxedStrict: 2}

// ErrIsolationNeedsOwner refuses a level below the workspace's default to
// anyone but an owner (D-95).
var ErrIsolationNeedsOwner = errors.New("only owners can run a service below the workspace's default isolation")

// readIsolation fills the service's own isolation level and the one it runs at.
func (s *ServiceManager) readIsolation(ctx context.Context, svc *Service) error {
	own, def, err := s.repo.Isolation(ctx, svc.ID)
	if err != nil {
		return err
	}
	svc.Isolation, svc.EffectiveIsolation = own, own
	if own == "" {
		svc.EffectiveIsolation = def
	}
	return nil
}

// CheckIsolation refuses an isolation level role may not choose for the
// service: below the workspace's default is the owners' (D-95).
func (s *ServiceManager) CheckIsolation(ctx context.Context, id, level, role string) error {
	if level == isolationDefault || role == "owner" {
		return nil
	}
	_, def, err := s.repo.Isolation(ctx, id)
	if err != nil {
		return err
	}
	if isolationRank[level] < isolationRank[def] {
		return ErrIsolationNeedsOwner
	}
	return nil
}

func (s *ServiceManager) ListByProject(ctx context.Context, projectID string) ([]Service, error) {
	return s.repo.ListByProject(ctx, projectID)
}

func (s *ServiceManager) ListByEnvironment(ctx context.Context, environmentID string) ([]Service, error) {
	return s.repo.ListByEnvironment(ctx, environmentID)
}

// ListProjectScoped returns services with scope='project' for the given project.
func (s *ServiceManager) ListProjectScoped(ctx context.Context, projectID string) ([]Service, error) {
	return s.repo.ListProjectScoped(ctx, projectID)
}

// ListOrgScoped returns services with scope='org' for the given org.
func (s *ServiceManager) ListOrgScoped(ctx context.Context, orgID string) ([]Service, error) {
	return s.repo.ListOrgScoped(ctx, orgID)
}

// ListWorkspace returns every service of the workspace, for the shell.
func (s *ServiceManager) ListWorkspace(ctx context.Context, orgID string) ([]WorkspaceService, error) {
	return s.repo.ListWorkspaceServices(ctx, orgID)
}

// ServerLogs is where the workspace's containers on one server send their
// output (the Observability page, D-48). Error says why the server could
// not be read; Services is then empty.
type ServerLogs struct {
	ServerID string              `json:"server_id"` // "" is the panel host
	Error    string              `json:"error,omitempty"`
	Services []ServiceLogDrivers `json:"services"`
}

type ServiceLogDrivers struct {
	ServiceID  string         `json:"service_id"`
	Containers []ContainerLog `json:"containers"`
}

// LogDrivers reads, server by server, where each of the workspace's
// services' containers send their output: one read per server, and only
// this workspace's containers, attributed as the metrics are.
func (s *ServiceManager) LogDrivers(ctx context.Context, orgID string) ([]ServerLogs, error) {
	services, err := s.repo.ListWorkspaceServices(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list the workspace's services: %w", err)
	}
	byServer := map[string][]MetricsTarget{}
	order := []string{}
	for _, svc := range services {
		if _, seen := byServer[svc.ServerID]; !seen {
			order = append(order, svc.ServerID)
		}
		byServer[svc.ServerID] = append(byServer[svc.ServerID], MetricsTarget{ID: svc.ID, Name: svc.Name, ServerID: svc.ServerID})
	}
	out := make([]ServerLogs, 0, len(order))
	for _, serverID := range order {
		out = append(out, s.serverLogDrivers(ctx, serverID, byServer[serverID]))
	}
	return out, nil
}

func (s *ServiceManager) serverLogDrivers(ctx context.Context, serverID string, targets []MetricsTarget) ServerLogs {
	res := ServerLogs{ServerID: serverID, Services: make([]ServiceLogDrivers, 0, len(targets))}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	orch, cleanup, err := s.orchestratorFor(ctx, serverID)
	if err != nil {
		res.Error = "the server could not be reached"
		return res
	}
	defer cleanup()
	reader, ok := orch.(LogDriverReader)
	if !ok {
		res.Error = "the server's containers could not be read"
		return res
	}
	logs, err := reader.LogDrivers(ctx, targets)
	if err != nil {
		s.log.Warn("read log drivers", zap.String("server_id", serverID), zap.Error(err))
		res.Error = "the server's containers could not be read"
		return res
	}
	for _, t := range targets {
		if c, ok := logs[t.ID]; ok {
			res.Services = append(res.Services, ServiceLogDrivers{ServiceID: t.ID, Containers: c})
		}
	}
	return res
}

// CreateProjectScoped creates a service shared across all environments of a project.
func (s *ServiceManager) CreateProjectScoped(ctx context.Context, projectID, userID string, input CreateInput) (*SavedService, error) {
	if err := validateCreateInput(input); err != nil {
		return nil, err
	}
	if err := checkKeysOnce(input); err != nil {
		return nil, err
	}
	if err := s.checkPanelHost(ctx, input.ServerID); err != nil {
		return nil, err
	}
	if err := s.checkRepository(ctx, input.RepositoryID, "", projectID); err != nil {
		return nil, err
	}
	input, generated, login := withGenerated(withDefaultMemory(input))
	warnings, err := s.checkPolicy(ctx, saveCheckFromCreate(input))
	if err != nil {
		return nil, err
	}
	if input.NetworkAlias == "" {
		input.NetworkAlias = sanitizeNetworkAlias(input.Name)
	}
	input.Scope = ScopeProject
	svc, err := s.createWithSecrets(ctx, userID, input, func(ctx context.Context) (*Service, error) {
		return s.repo.CreateProjectScoped(ctx, projectID, input)
	})
	if err != nil {
		return nil, err
	}
	s.addComposeDefaults(ctx, svc, input)
	return &SavedService{Service: svc, PolicyViolations: warnings, GeneratedSecrets: generated, TemporaryLogin: login}, nil
}

// CreateOrgScoped creates a service shared across the entire organisation.
func (s *ServiceManager) CreateOrgScoped(ctx context.Context, orgID, userID string, input CreateInput) (*SavedService, error) {
	if err := validateCreateInput(input); err != nil {
		return nil, err
	}
	if err := checkKeysOnce(input); err != nil {
		return nil, err
	}
	if err := s.checkPanelHost(ctx, input.ServerID); err != nil {
		return nil, err
	}
	if input.RepositoryID != "" {
		return nil, fmt.Errorf("%w: a workspace service builds from a repository URL; a project's repositories are for its own services", ErrInvalidConfig)
	}
	input, generated, login := withGenerated(withDefaultMemory(input))
	warnings, err := s.checkPolicy(ctx, saveCheckFromCreate(input))
	if err != nil {
		return nil, err
	}
	if input.NetworkAlias == "" {
		input.NetworkAlias = sanitizeNetworkAlias(input.Name)
	}
	// It joins every network of the workspace, so its alias must be its own
	// there, as a moved one's (D-162).
	other, err := s.repo.AliasUserInWorkspace(ctx, orgID, uuid.Nil.String(), input.NetworkAlias)
	if err != nil {
		return nil, err
	}
	if other != "" {
		return nil, fmt.Errorf("%w: %s uses %q; pick another name or alias", ErrMoveAliasTaken, other, input.NetworkAlias)
	}
	input.Scope = ScopeOrg
	svc, err := s.createWithSecrets(ctx, userID, input, func(ctx context.Context) (*Service, error) {
		return s.repo.CreateOrgScoped(ctx, orgID, input)
	})
	if err != nil {
		return nil, err
	}
	s.addComposeDefaults(ctx, svc, input)
	return &SavedService{Service: svc, PolicyViolations: warnings, GeneratedSecrets: generated, TemporaryLogin: login}, nil
}

// configOver reads a config save's body (bind) over the service's stored
// settings, so a field the body leaves out, or sends as null, keeps its
// value. The write-only secrets stay empty and the pointer fields nil, which
// keep theirs. The lists are read into fresh slices, since encoding/json
// decodes an array over the elements already there (a stored volume's
// host_path would survive into the new list); one left out keeps the stored
// list, and [] clears it.
func configOver(s *Service, bind func(*UpdateConfigInput) error) (UpdateConfigInput, error) {
	in := UpdateConfigInput{
		Name:               s.Name,
		GitRepoURL:         s.GitRepoURL,
		GitBranch:          s.GitBranch,
		BaseDirectory:      s.BaseDirectory,
		DockerImage:        s.DockerImage,
		InstallCommand:     s.InstallCommand,
		BuildCommand:       s.BuildCommand,
		StartCommand:       s.StartCommand,
		BuildPack:          s.BuildPack,
		Port:               s.Port,
		CPULimit:           s.CPULimit,
		MemoryLimit:        s.MemoryLimit,
		ComposeContent:     s.ComposeContent,
		ComposeRepoURL:     s.ComposeRepoURL,
		ComposeFilePath:    s.ComposeFilePath,
		DeployStrategy:     s.DeployStrategy,
		RegistryServer:     s.RegistryServer,
		RegistryUsername:   s.RegistryUsername,
		NetworkAlias:       s.NetworkAlias,
		GitUsername:        s.GitUsername,
		PreDeployCommands:  s.PreDeployCommands,
		PostDeployCommands: s.PostDeployCommands,
		HealthCheckPath:    s.HealthCheckPath,
		HealthCheckTimeout: s.HealthCheckTimeout,
		HealthCheckRetries: s.HealthCheckRetries,
		RequiredEnvVars:    s.RequiredEnvVars,
		MigrateCommand:     s.MigrateCommand,
		BasicAuthUser:      s.BasicAuthUser,
	}
	if err := bind(&in); err != nil {
		return in, err
	}
	if in.Volumes == nil {
		in.Volumes = s.Volumes
	}
	if in.Dependencies == nil {
		in.Dependencies = s.Dependencies
	}
	if in.IPAllowlist == nil {
		in.IPAllowlist = s.IPAllowlist
	}
	return in, nil
}

// changedSettings names, by their API names, the settings a config save
// changed from before to after: the audit log's service.settings_changed.
// The credentials (registry_password, git_token, basic_auth_password) are
// compared but only named; their values never leave here. Isolation has its
// own entry, service.isolation_changed.
func changedSettings(before, after *Service) []string {
	fields := []struct {
		name string
		same bool
	}{
		{"name", before.Name == after.Name},
		{"git_repo_url", before.GitRepoURL == after.GitRepoURL},
		{"git_branch", before.GitBranch == after.GitBranch},
		{"repository_id", before.RepositoryID == after.RepositoryID},
		{"deploy_on_push", before.DeployOnPush == after.DeployOnPush},
		{"base_directory", before.BaseDirectory == after.BaseDirectory},
		{"docker_image", before.DockerImage == after.DockerImage},
		{"install_command", before.InstallCommand == after.InstallCommand},
		{"build_command", before.BuildCommand == after.BuildCommand},
		{"start_command", before.StartCommand == after.StartCommand},
		{"build_pack", before.BuildPack == after.BuildPack},
		{"port", before.Port == after.Port},
		{"cpu_limit", before.CPULimit == after.CPULimit},
		{"memory_limit", before.MemoryLimit == after.MemoryLimit},
		{"pids_limit", before.PidsLimit == after.PidsLimit},
		{"deploy_timeout_minutes", before.DeployTimeoutMinutes == after.DeployTimeoutMinutes},
		{"rollback_images_kept", before.RollbackImagesKept == after.RollbackImagesKept},
		{"container_health", before.ContainerHealth == after.ContainerHealth},
		{"container_health_command", before.ContainerHealthCommand == after.ContainerHealthCommand},
		{"compose_content", before.ComposeContent == after.ComposeContent},
		{"compose_repo_url", before.ComposeRepoURL == after.ComposeRepoURL},
		{"compose_file_path", before.ComposeFilePath == after.ComposeFilePath},
		{"compose_route_service", before.ComposeRouteService == after.ComposeRouteService},
		{"deploy_strategy", before.DeployStrategy == after.DeployStrategy},
		{"registry_server", before.RegistryServer == after.RegistryServer},
		{"registry_username", before.RegistryUsername == after.RegistryUsername},
		{"registry_password", before.RegistryPassword == after.RegistryPassword},
		{"network_alias", before.NetworkAlias == after.NetworkAlias},
		{"volumes", slices.Equal(before.Volumes, after.Volumes)},
		{"git_username", before.GitUsername == after.GitUsername},
		{"git_token", before.GitToken == after.GitToken},
		{"pre_deploy_commands", before.PreDeployCommands == after.PreDeployCommands},
		{"post_deploy_commands", before.PostDeployCommands == after.PostDeployCommands},
		{"health_check_path", before.HealthCheckPath == after.HealthCheckPath},
		{"health_check_timeout", before.HealthCheckTimeout == after.HealthCheckTimeout},
		{"health_check_retries", before.HealthCheckRetries == after.HealthCheckRetries},
		{"required_env_vars", before.RequiredEnvVars == after.RequiredEnvVars},
		{"migrate_command", before.MigrateCommand == after.MigrateCommand},
		{"dependencies", slices.Equal(before.Dependencies, after.Dependencies)},
		{"basic_auth_user", before.BasicAuthUser == after.BasicAuthUser},
		{"basic_auth_password", before.BasicAuthPassword == after.BasicAuthPassword},
		{"ip_allowlist", slices.Equal(before.IPAllowlist, after.IPAllowlist)},
	}
	changed := make([]string, 0, len(fields))
	for _, f := range fields {
		if !f.same {
			changed = append(changed, f.name)
		}
	}
	return changed
}

func (s *ServiceManager) UpdateConfig(ctx context.Context, id string, input UpdateConfigInput) (*SavedService, error) {
	if err := validateServiceInput(input.Port, input.CPULimit, input.MemoryLimit); err != nil {
		return nil, err
	}
	if err := validateGitSource(input.GitRepoURL, input.ComposeRepoURL, input.GitBranch); err != nil {
		return nil, err
	}
	if err := ValidateBaseDirectory(input.BaseDirectory); err != nil {
		return nil, err
	}
	if err := validateComposeContent(input.ComposeContent); err != nil {
		return nil, err
	}
	if err := validateAccessControl(input.BasicAuthUser, input.IPAllowlist); err != nil {
		return nil, err
	}
	if input.ComposeRouteService != nil {
		if err := validateComposeRouteService(*input.ComposeRouteService); err != nil {
			return nil, err
		}
	}
	if err := validateDeployStrategy(input.DeployStrategy); err != nil {
		return nil, err
	}
	if p := input.PidsLimit; p != nil && *p != 0 && (*p < 256 || *p > 32768) {
		return nil, fmt.Errorf("%w: pids_limit is 256 to 32768, or 0 for the default", ErrInvalidConfig)
	}
	if m := input.DeployTimeoutMinutes; m != nil && *m != 0 && (*m < 5 || *m > 240) {
		return nil, fmt.Errorf("%w: deploy_timeout_minutes is 5 to 240, or 0 for the default", ErrInvalidConfig)
	}
	if k := input.RollbackImagesKept; k != nil && (*k < 0 || *k > 100) {
		return nil, fmt.Errorf("%w: rollback_images_kept is 1 to 100, or 0 for the default", ErrInvalidConfig)
	}
	if l := input.Isolation; l != nil && *l != isolationDefault && *l != IsolationStandard && *l != IsolationSandboxed && *l != IsolationSandboxedStrict {
		return nil, fmt.Errorf("%w: isolation is default, standard, sandboxed or sandboxed_strict", ErrInvalidConfig)
	}
	existing, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get service: %w", err)
	}
	repositoryID := existing.RepositoryID
	if input.RepositoryID != nil {
		repositoryID = *input.RepositoryID
		if repositoryID != "" && existing.Scope == ScopeOrg {
			return nil, fmt.Errorf("%w: a workspace service builds from a repository URL; a project's repositories are for its own services", ErrInvalidConfig)
		}
		if repositoryID != "" && repositoryID != existing.RepositoryID {
			if err := s.checkRepository(ctx, repositoryID, existing.EnvironmentID, existing.ProjectID); err != nil {
				return nil, err
			}
		}
	}
	if err := validateRepositorySource(existing.SourceType, repositoryID, input.GitRepoURL, input.ComposeRepoURL); err != nil {
		return nil, err
	}
	// The container check: a field left out keeps its stored value.
	checkChanged := input.ContainerHealth != nil || input.ContainerHealthCommand != nil
	var checkMode, checkCommand string
	if checkChanged {
		if checkMode, checkCommand, err = s.repo.ContainerHealth(ctx, id); err != nil {
			return nil, err
		}
		if input.ContainerHealth != nil {
			checkMode = *input.ContainerHealth
		}
		if input.ContainerHealthCommand != nil {
			checkCommand = *input.ContainerHealthCommand
		}
		if checkMode == "" {
			checkMode = ContainerHealthImage
		}
		if err := validateContainerHealth(checkMode, checkCommand, existing.SourceType); err != nil {
			return nil, err
		}
	}
	name := input.Name
	if name == "" {
		name = existing.Name
	}
	memory, err := s.memoryCheck(ctx, existing, name, input)
	if err != nil {
		return nil, err
	}
	warnings, err := s.checkPolicy(ctx, deploypolicy.SaveCheck{
		ServiceID:         id,
		ServiceName:       name,
		SourceType:        string(existing.SourceType),
		ComposeContent:    input.ComposeContent,
		ComposeRepoURL:    input.ComposeRepoURL,
		Volumes:           volumeSpecs(input.Volumes),
		PreDeployCommands: input.PreDeployCommands,
		Memory:            memory,
	})
	if err != nil {
		return nil, err
	}
	if input.NetworkAlias != "" {
		if err := s.validateAliasUnique(ctx, existing.EnvironmentID, id, input.NetworkAlias); err != nil {
			return nil, err
		}
	}
	// The source's CHECK holds after each statement: leaving a repository
	// goes before the URLs are written, taking one after they are cleared.
	leaving := input.RepositoryID != nil && *input.RepositoryID == ""
	if leaving {
		if err := s.repo.SetSource(ctx, id, input.RepositoryID, nil); err != nil {
			return nil, err
		}
	}
	input.ClearGitToken, input.ClearRegistryPassword = credentialsMoved(existing, input)
	svc, err := s.repo.UpdateConfig(ctx, id, input)
	if err != nil {
		return nil, err
	}
	// A new or removed basic auth: the temporary login's kept copies no
	// longer open the app (D-160).
	if s.reveals != nil && (input.BasicAuthPassword != "" || (input.BasicAuthUser == "" && existing.BasicAuthUser != "")) {
		if err := s.reveals.Forget(ctx, id, ""); err != nil {
			return nil, fmt.Errorf("forget the temporary login: %w", err)
		}
	}
	if (input.RepositoryID != nil && !leaving) || input.DeployOnPush != nil {
		repo := input.RepositoryID
		if leaving {
			repo = nil
		}
		if err := s.repo.SetSource(ctx, id, repo, input.DeployOnPush); err != nil {
			return nil, err
		}
		if svc, err = s.repo.GetByID(ctx, id); err != nil {
			return nil, err
		}
	}
	if input.PidsLimit != nil {
		if err := s.repo.SetPidsLimit(ctx, id, *input.PidsLimit); err != nil {
			return nil, err
		}
	}
	if input.DeployTimeoutMinutes != nil {
		if err := s.repo.SetDeployTimeout(ctx, id, *input.DeployTimeoutMinutes); err != nil {
			return nil, err
		}
	}
	if input.RollbackImagesKept != nil {
		if err := s.repo.SetRollbackImagesKept(ctx, id, *input.RollbackImagesKept); err != nil {
			return nil, err
		}
	}
	if checkChanged {
		if err := s.repo.SetContainerHealth(ctx, id, checkMode, checkCommand); err != nil {
			return nil, err
		}
	}
	if input.Isolation != nil {
		level := *input.Isolation
		if level == isolationDefault {
			level = ""
		}
		if err := s.repo.SetIsolation(ctx, id, level); err != nil {
			return nil, err
		}
	}
	if svc.PidsLimit, err = s.repo.PidsLimit(ctx, id); err != nil {
		return nil, err
	}
	if svc.DeployTimeoutMinutes, err = s.repo.DeployTimeout(ctx, id); err != nil {
		return nil, err
	}
	if svc.RollbackImagesKept, err = s.repo.RollbackImagesKept(ctx, id); err != nil {
		return nil, err
	}
	if svc.ContainerHealth, svc.ContainerHealthCommand, err = s.repo.ContainerHealth(ctx, id); err != nil {
		return nil, err
	}
	if err := s.readIsolation(ctx, svc); err != nil {
		return nil, err
	}
	return &SavedService{Service: svc, PolicyViolations: warnings}, nil
}

// memoryCheck is the memory rule's input for a config save (D-41): the
// service's new limit, or a compose stack's panel limits (the policy merges
// an inline file's own). A new inline stack (no grace date) first gets the
// default for each compose service the file leaves without a limit, so
// adding one is never refused. nil without the resources service.
func (s *ServiceManager) memoryCheck(ctx context.Context, existing *Service, name string, input UpdateConfigInput) (*resources.MemoryCheck, error) {
	if s.resources == nil {
		return nil, nil
	}
	ref := resources.ServiceRef{
		ID: existing.ID, SourceType: string(existing.SourceType), MemoryLimit: input.MemoryLimit,
		AddonSlug: existing.AddonSlug, DockerImage: input.DockerImage,
	}
	if existing.SourceType == SourceTypeDockerCompose && input.ComposeRepoURL == "" && strings.TrimSpace(input.ComposeContent) != "" {
		src := deploypolicy.InlineComposeSource(existing.ID, name, input.ComposeContent, s.envValues(ctx, existing.ID))
		if _, err := s.resources.AddComposeDefaults(ctx, existing.ID, src); err != nil {
			return nil, fmt.Errorf("default memory limits: %w", err)
		}
		ref.Compose = &src
	}
	m, err := s.resources.MemoryCheckFor(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("memory limit check: %w", err)
	}
	return m, nil
}

// envValues are the service's env vars, which compose interpolates the file
// with; nil when they cannot be read (the file's own limits that read them
// are then unknown, as at create).
func (s *ServiceManager) envValues(ctx context.Context, serviceID string) map[string]string {
	if s.envReader == nil {
		return nil
	}
	kvs, err := s.envReader.ListAllForDeployment(ctx, serviceID)
	if err != nil {
		s.log.Warn("read env vars for the compose file's memory limits", zap.String("service_id", serviceID), zap.Error(err))
		return nil
	}
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		if k, v, ok := strings.Cut(kv, "="); ok {
			out[k] = v
		}
	}
	return out
}

// ErrTeardown wraps a failure to remove a service from the server it runs
// on. Nothing of the service is deleted then: its rows stay, so the delete
// can be retried, and nothing is left running that the panel no longer knows.
var ErrTeardown = errors.New("could not remove the service from its server")

// TeardownMessage is what a client is told when a delete stopped at
// ErrTeardown: what failed, that nothing was deleted, and the way out when
// the server is gone for good.
func TeardownMessage(err error) string {
	return err.Error() + ". Nothing was deleted: try again once the server is reachable. If the server is gone for good, remove it under Servers: that deletes its services with it."
}

// serviceDataRemover deletes a removed service's named volumes, compose
// volumes and built images from its host (docker orchestrators).
type serviceDataRemover interface {
	RemoveServiceData(ctx context.Context, serviceID, serviceName string, volumes []string) error
}

// configFileRemover deletes a service's config files from its host
// (*configfile.Service, the ConfigFileSeeder).
type configFileRemover interface {
	RemoveFromHost(ctx context.Context, serviceID string, srv *serverPkg.Server) error
}

// Delete removes a service and everything it has on its server: its
// containers, its volumes and its compose stack's (data included), the
// images built for it, its compose files and config files. Shared volumes
// it mounts belong to the workspace and stay. Its rows go last, and only
// once the server is clean (ErrTeardown otherwise).
func (s *ServiceManager) Delete(ctx context.Context, id string) error {
	svc, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("get service: %w", err)
	}
	// The previews' copies of it go first (D-97); each preview keeps its
	// other services.
	copies, err := s.repo.ListCopies(ctx, id)
	if err != nil {
		return err
	}
	for _, c := range copies {
		if err := s.Delete(ctx, c); err != nil {
			return err
		}
	}
	if err := s.teardown(ctx, svc); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrTeardown, svc.Name, err)
	}
	return s.repo.Delete(ctx, id)
}

// DeleteIn deletes every service of an environment (ScopeEnvironment), a
// project (ScopeProject) or a workspace (ScopeOrg), each as Delete does,
// then removes the app networks nothing uses any more from the servers they
// ran on. It stops at the first service it cannot remove; the ones deleted
// before it stay deleted, so running it again carries on.
func (s *ServiceManager) DeleteIn(ctx context.Context, scope Scope, id string) error {
	ids, err := s.repo.ListIDsIn(ctx, scope, id)
	if err != nil {
		return err
	}
	servers := map[string]bool{}
	for _, sid := range ids {
		svc, err := s.repo.GetByID(ctx, sid)
		if err != nil {
			return fmt.Errorf("get service: %w", err)
		}
		if err := s.Delete(ctx, sid); err != nil {
			return err
		}
		servers[svc.ServerID] = true
	}
	s.collectNetworksLater(ctx, servers)
	return nil
}

// collectNetworksLater removes, in the background, the app networks that no
// service uses any more on servers: after a delete, or on the server a move
// left. They hold no data, and a reconcile takes a while on a busy server;
// the networks of a server that cannot be reached go at the next startup.
// A network that is younger than the reconcile's grace period, or still has
// a container being removed, is kept by the first pass, so a second one runs
// once the grace period has passed.
func (s *ServiceManager) collectNetworksLater(ctx context.Context, servers map[string]bool) {
	if len(servers) == 0 || s.factory == nil || !s.collectNetworks {
		return
	}
	bg := context.WithoutCancel(ctx)
	go s.removeUnusedNetworks(bg, servers)
	time.AfterFunc(networkGCRetry, func() { s.removeUnusedNetworks(bg, servers) })
}

// networkGCRetry is when the second network pass runs: after the
// reconcile's grace period for new networks (docker.gcGrace, 15 minutes).
var networkGCRetry = 16 * time.Minute

// removeUnusedNetworks runs an attach-only network reconcile on each server,
// which removes app networks that no service uses any more (once they are
// older than the reconcile's grace period).
func (s *ServiceManager) removeUnusedNetworks(ctx context.Context, servers map[string]bool) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for serverID := range servers {
		if _, err := s.ReconcileNetworks(ctx, serverID, netplan.MigrationAttach, func(string) {}); err != nil {
			s.log.Warn("delete: app networks not collected", zap.String("server_id", serverID), zap.Error(err))
		}
	}
}

// ForgetServicesOnServer deletes the services assigned to a server from the
// panel without touching the server: for a server that is gone for good,
// whose services cannot be torn down. Satisfies server.ServiceForgetter.
func (s *ServiceManager) ForgetServicesOnServer(ctx context.Context, serverID string) error {
	if serverID == "" {
		return errors.New("forget services: the control-plane server is never gone")
	}
	ids, err := s.repo.ListIDsOnServer(ctx, serverID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.repo.Delete(ctx, id); err != nil {
			return err
		}
		s.log.Warn("service deleted with its server; nothing was removed from the server", zap.String("service_id", id), zap.String("server_id", serverID))
	}
	return nil
}

// teardown removes the service's containers and data from its server, and
// its config files from the host.
func (s *ServiceManager) teardown(ctx context.Context, svc *Service) error {
	var srv *serverPkg.Server
	if svc.ServerID != "" && s.serverFetcher != nil {
		got, err := s.serverFetcher.GetServer(ctx, svc.ServerID)
		if err != nil {
			return fmt.Errorf("get server: %w", err)
		}
		srv = got
	}
	if srv != nil && srv.AgentFrozenAt != nil {
		return serverPkg.ErrServerFrozen
	}
	if s.factory != nil {
		orch, cleanup, err := s.factory.ForServer(srv)
		if err != nil {
			return fmt.Errorf("reach the server: %w", err)
		}
		defer cleanup()
		if svc.SourceType == SourceTypeDockerCompose {
			err = orch.StopCompose(ctx, svc.ID, svc.Name, svc.ComposeContent)
		} else {
			err = orch.Stop(ctx, svc.ID)
		}
		if err != nil {
			return fmt.Errorf("remove containers: %w", err)
		}
		if r, ok := orch.(serviceDataRemover); ok {
			if err := r.RemoveServiceData(ctx, svc.ID, svc.Name, ownVolumes(svc.ID, svc.Volumes)); err != nil {
				return fmt.Errorf("remove data: %w", err)
			}
		}
	}
	if r, ok := s.configFileSeeder.(configFileRemover); ok {
		if err := r.RemoveFromHost(ctx, svc.ID, srv); err != nil {
			return err
		}
	}
	return nil
}

func (s *ServiceManager) FindByProjectRepoAndBranch(ctx context.Context, projectID, repoURL, repositoryID, branch string) ([]Service, error) {
	return s.repo.FindByProjectRepoAndBranch(ctx, projectID, repoURL, repositoryID, branch)
}

// ErrRepositoryNotInProject: a service builds only from its own project's
// repositories (D-77).
var ErrRepositoryNotInProject = errors.New("that repository isn't one of the project's")

// checkRepository refuses a repository that isn't the project's (the
// environment's project when environmentID is set); "" passes.
func (s *ServiceManager) checkRepository(ctx context.Context, repositoryID, environmentID, projectID string) error {
	if repositoryID == "" {
		return nil
	}
	ok, err := s.repo.RepositoryInScope(ctx, repositoryID, environmentID, projectID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %w", ErrInvalidConfig, ErrRepositoryNotInProject)
	}
	return nil
}

// validateRepositorySource: a repository source is a git build's or a
// compose stack's files, and takes the place of their URLs.
func validateRepositorySource(source SourceType, repositoryID, gitRepoURL, composeRepoURL string) error {
	if repositoryID == "" {
		return nil
	}
	if _, err := uuid.Parse(repositoryID); err != nil {
		return fmt.Errorf("%w: repository_id is not a repository's id", ErrInvalidConfig)
	}
	if source != SourceTypeGit && source != SourceTypeDockerCompose {
		return fmt.Errorf("%w: only a git or compose service builds from a repository", ErrInvalidConfig)
	}
	if gitRepoURL != "" || composeRepoURL != "" {
		return fmt.Errorf("%w: a service builds from one of the project's repositories or from a URL, not both", ErrInvalidConfig)
	}
	return nil
}

func (s *ServiceManager) CheckOwnership(ctx context.Context, id, orgID string) (bool, error) {
	return s.repo.CheckOwnership(ctx, id, orgID)
}

// IsWorkspaceService reports whether the service is its workspace's own
// (scope org), for observability's registration (D-162).
func (s *ServiceManager) IsWorkspaceService(ctx context.Context, id string) (bool, error) {
	svc, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return false, err
	}
	return svc.Scope == ScopeOrg, nil
}

// AssignServer links (or unlinks when serverID is empty) a server to a service.
// Only a service that runs nowhere (idle or stopped) is assigned here
// (ErrMoveWhileRunning). from is the server it had, for the audit log.
func (s *ServiceManager) AssignServer(ctx context.Context, serviceID, serverID string) (svc *Service, from string, err error) {
	current, err := s.repo.GetByID(ctx, serviceID)
	if err != nil {
		return nil, "", err
	}
	if current.ServerID != serverID {
		if current.Deploying || (current.RuntimeStatus != "idle" && current.RuntimeStatus != "stopped" && current.RuntimeStatus != "") {
			return nil, "", ErrMoveWhileRunning
		}
		if err := s.CheckMove(ctx, serviceID); err != nil {
			return nil, "", err
		}
	}
	from = current.ServerID
	svc, err = s.repo.AssignServer(ctx, serviceID, serverID)
	return svc, from, err
}

// CheckMove reports whether the service may move to another server: not
// when it mounts a shared volume (ErrMountsSharedVolume).
func (s *ServiceManager) CheckMove(ctx context.Context, serviceID string) error {
	if s.sharedMounts == nil {
		return nil
	}
	mounts, err := s.sharedMounts.HasMounts(ctx, serviceID)
	if err != nil {
		return fmt.Errorf("check shared mounts: %w", err)
	}
	if mounts {
		return ErrMountsSharedVolume
	}
	return nil
}

// CompleteMove records the server a move deployed the service to, once it
// runs there, and collects the app networks the server it left no longer
// uses (the pipeline removes the service from it next, so the second pass
// is the one that finds them unused). Satisfies deployment.ServiceMover.
func (s *ServiceManager) CompleteMove(ctx context.Context, serviceID, serverID string) error {
	current, err := s.repo.GetByID(ctx, serviceID)
	if err != nil {
		return fmt.Errorf("record the move: %w", err)
	}
	if _, err := s.repo.AssignServer(ctx, serviceID, serverID); err != nil {
		return fmt.Errorf("record the move: %w", err)
	}
	if current.ServerID != serverID {
		s.collectNetworksLater(ctx, map[string]bool{current.ServerID: true})
		s.moveTemporaryAddresses(ctx, serviceID, current.ServerID, serverID)
	}
	return nil
}

// moveAddresses is the old and new servers' public addresses for a move,
// "" for either when it has none or the lookup fails.
func (s *ServiceManager) moveAddresses(ctx context.Context, fromServerID, toServerID string) (string, string) {
	if s.addresser == nil {
		return "", ""
	}
	from, err := s.addresser.ServerAddress(ctx, fromServerID)
	if err != nil {
		s.log.Warn("a moved service's temporary address: the old server's address", zap.String("server_id", fromServerID), zap.Error(err))
		return "", ""
	}
	to, err := s.addresser.ServerAddress(ctx, toServerID)
	if err != nil {
		s.log.Warn("a moved service's temporary address: the new server's address", zap.String("server_id", toServerID), zap.Error(err))
		return "", ""
	}
	return from, to
}

// MovedHostnames is what a move deploy routes on the new server (D-150):
// the service's names, its temporary ones pointing at the new server.
// Satisfies deployment.ServiceMover.
func (s *ServiceManager) MovedHostnames(ctx context.Context, hostnames []string, fromServerID, toServerID string) []string {
	from, to := s.moveAddresses(ctx, fromServerID, toServerID)
	out := make([]string, 0, len(hostnames))
	for _, h := range hostnames {
		out = append(out, domain.MoveTemporary(h, from, to))
	}
	return out
}

// moveTemporaryAddresses records the names MovedHostnames gave the move's
// deploy (D-150). The move is done by then, so a failure is logged: the
// service runs under the new name, and its Domains tab shows the old one
// until the person adds a domain.
func (s *ServiceManager) moveTemporaryAddresses(ctx context.Context, serviceID, fromServerID, toServerID string) {
	from, to := s.moveAddresses(ctx, fromServerID, toServerID)
	if from == "" || to == "" {
		return
	}
	if _, err := s.repo.RenameTemporaryAddresses(ctx, serviceID, from, to); err != nil {
		s.log.Warn("a moved service's temporary address", zap.String("service_id", serviceID), zap.Error(err))
	}
}

// UpdateRuntimeStatus updates the service's runtime_status and active_deployment_id.
// Satisfies deployment.ServiceStatusUpdater.
func (s *ServiceManager) UpdateRuntimeStatus(ctx context.Context, id, status, activeDeploymentID string) error {
	return s.repo.UpdateRuntimeStatus(ctx, id, status, activeDeploymentID)
}

// SetComposeNetworkService records the compose service a successful compose
// deploy routed, "" for none: the one connect_to names. Satisfies
// deployment.ServiceStatusUpdater.
func (s *ServiceManager) SetComposeNetworkService(ctx context.Context, id, name string) error {
	return s.repo.SetComposeNetworkService(ctx, id, name)
}

// UpdateDockerImage persists a new docker_image value for the service (a
// deploy's override). An image from another registry clears the saved
// registry password, as a save does (credentialsMoved); cleared says so.
func (s *ServiceManager) UpdateDockerImage(ctx context.Context, id, image string) (cleared bool, err error) {
	existing, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return false, fmt.Errorf("get service: %w", err)
	}
	cleared = existing.HasRegistryPassword && imageRegistry(image) != imageRegistry(existing.DockerImage)
	return cleared, s.repo.UpdateDockerImage(ctx, id, image, cleared)
}

// SwapDockerImage sets the service's docker_image to image while it is
// still from (the image a deploy started with); a save made since stays.
// It never swaps to another registry's image: the saved registry server,
// username and password are from's, and image's are not known, so the
// saved settings stay as they are.
// Satisfies deployment.ServiceStatusUpdater.
func (s *ServiceManager) SwapDockerImage(ctx context.Context, id, from, image string) error {
	if imageRegistry(image) != imageRegistry(from) {
		return nil
	}
	return s.repo.SwapDockerImage(ctx, id, from, image, "")
}

// PutBackDockerImage undoes a deploy's override (UpdateDockerImage) when no
// deploy started: image again while docker_image is still from, with the
// registry password the override cleared (registryPassword, "" none), in
// one conditional update; a save made since stays.
func (s *ServiceManager) PutBackDockerImage(ctx context.Context, id, from, image, registryPassword string) error {
	return s.repo.SwapDockerImage(ctx, id, from, image, registryPassword)
}

// UpdateGitBranch persists a new git_branch value for the service.
func (s *ServiceManager) UpdateGitBranch(ctx context.Context, id, branch string) error {
	if err := ValidateGitBranch(branch); err != nil {
		return err
	}
	return s.repo.UpdateGitBranch(ctx, id, branch)
}

// Stop stops the container (if orchestrator is configured) then marks it stopped in DB.
func (s *ServiceManager) Stop(ctx context.Context, id string) error {
	if s.factory != nil && s.serverFetcher != nil {
		svc, err := s.repo.GetByID(ctx, id)
		if err != nil {
			s.log.Error("stop: failed to get service", zap.Error(err), zap.String("service_id", id))
		} else {
			var targetServer *serverPkg.Server
			if svc.ServerID != "" {
				srv, fetchErr := s.serverFetcher.GetServer(ctx, svc.ServerID)
				if fetchErr != nil {
					s.log.Error("stop: failed to get server", zap.Error(fetchErr), zap.String("server_id", svc.ServerID))
				} else {
					targetServer = srv
				}
			}
			orch, cleanup, orchErr := s.factory.ForServer(targetServer)
			if orchErr != nil {
				s.log.Error("stop: orchestrator init failed", zap.Error(orchErr))
			} else {
				defer cleanup()
				var stopErr error
				if svc.SourceType == SourceTypeDockerCompose {
					stopErr = orch.StopCompose(ctx, id, svc.Name, svc.ComposeContent)
				} else {
					stopErr = orch.Stop(ctx, id)
				}
				if stopErr != nil {
					s.log.Error("stop: failed", zap.Error(stopErr), zap.String("service_id", id))
					// Continue — still mark as stopped in DB even if container stop fails.
				}
			}
		}
	}
	return s.repo.UpdateRuntimeStatus(ctx, id, "stopped", "")
}

// LogStreamOptions say what a live log stream follows and how.
type LogStreamOptions struct {
	// Tail is how many earlier lines come first.
	Tail int
	// Container names the container; "" follows the active one.
	Container string
	// Timestamps keeps Docker's RFC 3339 timestamp and a space before each
	// line; without it lines come as the container wrote them.
	Timestamps bool
	// OnContainer, when set, is told the container followed before the
	// first line, and whether it runs: one that doesn't gives its last
	// lines and the stream ends.
	OnContainer func(name string, running bool)
}

// activeContainerFinder is satisfied by orchestrators that say which
// container a log stream with no name follows, and whether it runs.
type activeContainerFinder interface {
	ActiveContainer(ctx context.Context, serviceID, serviceName string) (string, bool)
}

// containerRunFinder is satisfied by orchestrators that say whether a named
// container runs, as opposed to having exited.
type containerRunFinder interface {
	ContainerRunning(ctx context.Context, serviceID, serviceName, name string) bool
}

// StreamLogs streams live container logs to logFn.
func (s *ServiceManager) StreamLogs(ctx context.Context, id string, opts LogStreamOptions, logFn func(string)) error {
	if s.factory == nil || s.serverFetcher == nil {
		return fmt.Errorf("orchestration not configured for service manager")
	}
	svc, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("get service: %w", err)
	}
	var targetServer *serverPkg.Server
	if svc.ServerID != "" {
		targetServer, err = s.serverFetcher.GetServer(ctx, svc.ServerID)
		if err != nil {
			return fmt.Errorf("get server: %w", err)
		}
	}
	orch, cleanup, err := s.factory.ForServer(targetServer)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrLogServerUnreachable, err)
	}
	defer cleanup()
	name, running := opts.Container, true
	if name != "" {
		if err := ensureOwnContainer(ctx, orch, svc, name); err != nil {
			return err
		}
	} else if f, ok := orch.(activeContainerFinder); ok {
		name, running = f.ActiveContainer(ctx, id, svc.Name) // the platform's own pick
		if name == "" {
			return ErrLogNoContainer
		}
	}
	if name != "" && opts.OnContainer != nil {
		opts.OnContainer(name, running)
	}
	// The upstream outlives the viewer who started it, so it takes its own
	// orchestrator.
	start := func(ctx context.Context, tail int, logFn func(string)) error {
		orch, cleanup, err := s.factory.ForServer(targetServer)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrLogServerUnreachable, err)
		}
		defer cleanup()
		return orch.StreamLogs(ctx, id, tail, name, logFn)
	}
	err = s.logs.follow(ctx, svc.ServerID+"/"+id+"/"+name, opts.Tail, start, func(line string) {
		if !opts.Timestamps {
			line = StripLogTimestamp(line)
		}
		logFn(line)
	})
	// The follow also ends when the container exits (a one-shot job, or the
	// named one was never running): say so, so the page shows it finished
	// rather than a lost connection.
	if runs, ok := orch.(containerRunFinder); ok && running && name != "" && opts.OnContainer != nil && ctx.Err() == nil && !runs.ContainerRunning(ctx, id, svc.Name, name) {
		opts.OnContainer(name, false)
	}
	return err
}

// StripLogTimestamp removes the RFC 3339 timestamp and space Docker puts
// before a log line when asked for timestamps. A line without one is
// returned as it is.
func StripLogTimestamp(line string) string {
	i := strings.IndexByte(line, ' ')
	if i < 20 || i > 40 {
		return line
	}
	if _, err := time.Parse(time.RFC3339Nano, line[:i]); err != nil {
		return line
	}
	return line[i+1:]
}

// CheckContainer returns ErrContainerNotInService unless name is one of the
// service's containers. An empty name (let the orchestrator pick the
// service's own container) is always fine.
func (s *ServiceManager) CheckContainer(ctx context.Context, id, name string) error {
	if name == "" {
		return nil
	}
	if s.factory == nil || s.serverFetcher == nil {
		return fmt.Errorf("orchestration not configured for service manager")
	}
	svc, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("get service: %w", err)
	}
	var targetServer *serverPkg.Server
	if svc.ServerID != "" {
		targetServer, err = s.serverFetcher.GetServer(ctx, svc.ServerID)
		if err != nil {
			return fmt.Errorf("get server: %w", err)
		}
	}
	orch, cleanup, err := s.factory.ForServer(targetServer)
	if err != nil {
		return fmt.Errorf("get orchestrator: %w", err)
	}
	defer cleanup()
	return ensureOwnContainer(ctx, orch, svc, name)
}

// ensureOwnContainer returns ErrContainerNotInService unless name is one of
// the service's containers: those with its paas.service_id label or in its
// compose project, as the orchestrator lists them. Names are matched exactly,
// so a name can neither reach another service's container nor carry shell
// syntax into the remote docker commands.
func ensureOwnContainer(ctx context.Context, orch ContainerOrchestrator, svc *Service, name string) error {
	names, err := orch.ListContainers(ctx, svc.ID, svc.Name)
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}
	for _, n := range names {
		if n == name {
			return nil
		}
	}
	return ErrContainerNotInService
}

// ExecInService runs a command inside one of the service's containers and
// streams output to logFn. containerName must be one of the service's
// containers (ErrContainerNotInService otherwise); "" picks the first one.
// HostContainer ("@server") runs the command on the server's host shell and
// is for instance operators only (ErrHostExecOperatorOnly).
func (s *ServiceManager) ExecInService(ctx context.Context, actorID, id, containerName, command string, logFn func(string)) error {
	if s.factory == nil || s.serverFetcher == nil {
		return fmt.Errorf("orchestration not configured for service manager")
	}
	if containerName == HostContainer {
		if err := s.AuthorizeHostExec(ctx, actorID); err != nil {
			return err
		}
	}
	svc, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("get service: %w", err)
	}
	var targetServer *serverPkg.Server
	if svc.ServerID != "" {
		targetServer, err = s.serverFetcher.GetServer(ctx, svc.ServerID)
		if err != nil {
			return fmt.Errorf("get server: %w", err)
		}
	}
	orch, cleanup, err := s.factory.ForServer(targetServer)
	if err != nil {
		return fmt.Errorf("get orchestrator: %w", err)
	}
	defer cleanup()
	// @server = run on the host, not inside a container (operators only,
	// checked above).
	if containerName == HostContainer {
		hostExec, ok := orch.(hostCommander)
		if !ok {
			return fmt.Errorf("orchestrator does not support host exec")
		}
		return hostExec.ExecCommands(ctx, []string{command}, logFn)
	}
	if containerName == "" {
		containers, _ := orch.ListContainers(ctx, id, svc.Name)
		if len(containers) == 0 {
			return fmt.Errorf("no container found for this service; deploy it first")
		}
		containerName = containers[0]
	} else if err := ensureOwnContainer(ctx, orch, svc, containerName); err != nil {
		return err
	}
	return orch.ExecInContainer(ctx, id, containerName, command, logFn)
}

// AuthorizeHostExec is ErrHostExecOperatorOnly unless actorID is an
// instance operator: only they run commands on a server's host (D-19).
func (s *ServiceManager) AuthorizeHostExec(ctx context.Context, actorID string) error {
	if s.operators == nil {
		return ErrHostExecOperatorOnly
	}
	ok, err := s.operators.IsOperator(ctx, actorID)
	if err != nil {
		return fmt.Errorf("check operator: %w", err)
	}
	if !ok {
		return ErrHostExecOperatorOnly
	}
	return nil
}

// ListContainersForService returns running container names on the server that hosts the service.
func (s *ServiceManager) ListContainersForService(ctx context.Context, id string) ([]string, error) {
	if s.factory == nil || s.serverFetcher == nil {
		return nil, fmt.Errorf("orchestration not configured for service manager")
	}
	svc, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get service: %w", err)
	}
	var targetServer *serverPkg.Server
	if svc.ServerID != "" {
		targetServer, err = s.serverFetcher.GetServer(ctx, svc.ServerID)
		if err != nil {
			return nil, fmt.Errorf("get server: %w", err)
		}
	}
	orch, cleanup, err := s.factory.ForServer(targetServer)
	if err != nil {
		return nil, fmt.Errorf("get orchestrator: %w", err)
	}
	defer cleanup()
	return orch.ListContainers(ctx, id, svc.Name)
}

// healthTester is satisfied by orchestrators that can ask a container for a
// health-check path once (the Settings test).
type healthTester interface {
	TestHealthCheck(ctx context.Context, containerName, path string, port, timeoutSec int) (HealthTestResult, error)
}

// ErrNotRunning: the service has no container to ask.
var ErrNotRunning = errors.New("the service is not running: deploy it first")

// TestHealthCheck asks the service's running container for path on port,
// once, as the deploy's health check does, before the settings are saved.
// path, port and timeoutSec come from the form, so they are checked here.
func (s *ServiceManager) TestHealthCheck(ctx context.Context, id, path string, port, timeoutSec int) (HealthTestResult, error) {
	if !strings.HasPrefix(path, "/") || len(path) > 512 || strings.ContainsAny(path, " \t\r\n") {
		return HealthTestResult{}, fmt.Errorf("%w: the path must start with / and have no spaces", ErrInvalidConfig)
	}
	if port < 1 || port > 65535 {
		return HealthTestResult{}, fmt.Errorf("%w: the port must be 1 to 65535", ErrInvalidConfig)
	}
	if timeoutSec < 1 || timeoutSec > 30 {
		return HealthTestResult{}, fmt.Errorf("%w: the timeout must be 1 to 30 seconds", ErrInvalidConfig)
	}
	if s.factory == nil || s.serverFetcher == nil {
		return HealthTestResult{}, fmt.Errorf("orchestration not configured for service manager")
	}
	svc, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return HealthTestResult{}, fmt.Errorf("get service: %w", err)
	}
	if svc.SourceType == SourceTypeDockerCompose {
		return HealthTestResult{}, fmt.Errorf("%w: a compose stack's health checks are in its file", ErrInvalidConfig)
	}
	var targetServer *serverPkg.Server
	if svc.ServerID != "" {
		if targetServer, err = s.serverFetcher.GetServer(ctx, svc.ServerID); err != nil {
			return HealthTestResult{}, fmt.Errorf("get server: %w", err)
		}
	}
	orch, cleanup, err := s.factory.ForServer(targetServer)
	if err != nil {
		return HealthTestResult{}, fmt.Errorf("get orchestrator: %w", err)
	}
	defer cleanup()
	tester, ok := orch.(healthTester)
	if !ok {
		return HealthTestResult{}, errors.New("orchestrator cannot test health checks")
	}
	containers, _ := orch.ListContainers(ctx, id, svc.Name)
	if len(containers) == 0 {
		return HealthTestResult{}, ErrNotRunning
	}
	return tester.TestHealthCheck(ctx, containers[0], path, port, timeoutSec)
}

// DomainTarget is where the service's domains go: the port Traefik routes
// them to (0: none) and its server ("" the panel host).
func (s *ServiceManager) DomainTarget(ctx context.Context, id string) (int, string, error) {
	svc, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return 0, "", fmt.Errorf("get service: %w", err)
	}
	return svc.Port, svc.ServerID, nil
}

// routedHostsReader is satisfied by orchestrators that can read what a
// service's running containers are routed for.
type routedHostsReader interface {
	RoutedHosts(ctx context.Context, serviceID, serviceName string) ([]string, bool, error)
}

// RoutedHosts are the hostnames the service's running containers are
// routed for now, from their labels; running is false when none runs.
// Domains change at a deploy, so this differs from the service's domains
// in between.
func (s *ServiceManager) RoutedHosts(ctx context.Context, id string) ([]string, bool, error) {
	if s.factory == nil || s.serverFetcher == nil {
		return nil, false, fmt.Errorf("orchestration not configured for service manager")
	}
	svc, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, false, fmt.Errorf("get service: %w", err)
	}
	var targetServer *serverPkg.Server
	if svc.ServerID != "" {
		if targetServer, err = s.serverFetcher.GetServer(ctx, svc.ServerID); err != nil {
			return nil, false, fmt.Errorf("get server: %w", err)
		}
	}
	orch, cleanup, err := s.factory.ForServer(targetServer)
	if err != nil {
		return nil, false, fmt.Errorf("get orchestrator: %w", err)
	}
	defer cleanup()
	reader, ok := orch.(routedHostsReader)
	if !ok {
		return nil, false, errors.New("orchestrator cannot read routing")
	}
	return reader.RoutedHosts(ctx, id, svc.Name)
}

// serviceRestarter is satisfied by orchestrators that can restart a
// service's running containers in place.
type serviceRestarter interface {
	RestartService(ctx context.Context, serviceID, serviceName string) (int, error)
}

// ErrDeployRunning: a deploy of the service is under way.
var ErrDeployRunning = errors.New("a deploy of this service is running: wait for it to finish")

// Restart restarts the service's running containers in place: the same
// image, settings and data, a few seconds of downtime. Settings saved since
// the live deploy need a deploy, not a restart. It refuses while a deploy
// runs (it would race the deploy's own container changes) and when nothing
// runs (ErrNotRunning: Deploy starts it).
func (s *ServiceManager) Restart(ctx context.Context, id string) error {
	if s.factory == nil || s.serverFetcher == nil {
		return fmt.Errorf("orchestration not configured for service manager")
	}
	svc, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("get service: %w", err)
	}
	if svc.Deploying {
		return ErrDeployRunning
	}
	var targetServer *serverPkg.Server
	if svc.ServerID != "" {
		if targetServer, err = s.serverFetcher.GetServer(ctx, svc.ServerID); err != nil {
			return fmt.Errorf("get server: %w", err)
		}
	}
	orch, cleanup, err := s.factory.ForServer(targetServer)
	if err != nil {
		return fmt.Errorf("get orchestrator: %w", err)
	}
	defer cleanup()
	restarter, ok := orch.(serviceRestarter)
	if !ok {
		return errors.New("orchestrator cannot restart services")
	}
	n, err := restarter.RestartService(ctx, id, svc.Name)
	if err != nil {
		return fmt.Errorf("restart %s: %w", svc.Name, err)
	}
	if n == 0 {
		return ErrNotRunning
	}
	return nil
}

// hostCommander is satisfied by orchestrators that can run arbitrary host commands.
type hostCommander interface {
	ExecCommands(ctx context.Context, commands []string, logFn func(string)) error
}

// statsGetter is the optional capability implemented by orchestrators that support stats.
type statsGetter interface {
	Stats(ctx context.Context, serviceID string, containerName string) (*ContainerStats, error)
}

// ServiceStatsCollector is the optional capability of orchestrators that
// sample every listed service on their host at once, for the metrics
// collector: each running container of a target (a service with none is
// left out), and the targets' memory kills since since (unix seconds on the
// host's clock; 0 for the last minute).
type ServiceStatsCollector interface {
	CollectServiceStats(ctx context.Context, targets []MetricsTarget, since int64) (HostSample, error)
}

// AgentStatsReader is the optional capability of a remote orchestrator
// whose server's agent samples it every minute and keeps the samples while
// the panel is away (D-113). KeepsStats says whether this one's does;
// AgentServiceStats returns those taken after after (unix seconds),
// oldest first, each at its own time (HostSample.At).
type AgentStatsReader interface {
	KeepsStats() bool
	AgentServiceStats(ctx context.Context, targets []MetricsTarget, after int64) ([]HostSample, error)
}

// ServiceStateProbe reports container states and disk usage for alerts.
// Both orchestrators implement it.
type ServiceStateProbe interface {
	// ServiceStates returns every container, running or not, of each
	// target, keyed by service ID; a target with none is left out.
	ServiceStates(ctx context.Context, targets []MetricsTarget) (map[string][]ContainerState, error)
	DiskUsage(ctx context.Context) ([]DiskUsage, error)
}

// LogDriverReader reports where containers send their output. Both
// orchestrators implement it.
type LogDriverReader interface {
	// LogDrivers returns each target's containers' log drivers, keyed by
	// service ID; a target with no container is left out.
	LogDrivers(ctx context.Context, targets []MetricsTarget) (map[string][]ContainerLog, error)
}

// volumeLister is satisfied by orchestrators that can inspect named volumes.
type volumeLister interface {
	ListVolumes(ctx context.Context, volumeNames []string) ([]VolumeInfo, error)
}

// volumeDiscoverer is satisfied by orchestrators that can discover volumes from running containers.
type volumeDiscoverer interface {
	DiscoverVolumes(ctx context.Context, serviceID string) ([]VolumeInfo, error)
}

// volumePruner is satisfied by orchestrators that can remove named volumes.
type volumePruner interface {
	PruneVolume(ctx context.Context, volumeName string) error
}

// volumeDockerName returns the full Docker volume name for a service volume mount.
func volumeDockerName(serviceID, mountName string) string {
	prefix := serviceID
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	return fmt.Sprintf("paas-vol-%s-%s", prefix, mountName)
}

// GetStats returns a one-shot CPU/memory snapshot for the running container.
// containerName overrides auto-resolution; pass "" to let the orchestrator find the active container.
// Returns an error if the orchestration layer is not configured or the container is not running.
func (s *ServiceManager) GetStats(ctx context.Context, id string, containerName string) (*ContainerStats, error) {
	if s.factory == nil || s.serverFetcher == nil {
		return nil, fmt.Errorf("orchestration not configured")
	}
	svc, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get service: %w", err)
	}
	var targetServer *serverPkg.Server
	if svc.ServerID != "" {
		targetServer, err = s.serverFetcher.GetServer(ctx, svc.ServerID)
		if err != nil {
			return nil, fmt.Errorf("get server: %w", err)
		}
	}
	orch, cleanup, err := s.factory.ForServer(targetServer)
	if err != nil {
		return nil, fmt.Errorf("get orchestrator: %w", err)
	}
	defer cleanup()

	getter, ok := orch.(statsGetter)
	if !ok {
		return nil, fmt.Errorf("orchestrator does not support stats")
	}
	if containerName != "" {
		if err := ensureOwnContainer(ctx, orch, svc, containerName); err != nil {
			return nil, err
		}
	}
	return getter.Stats(ctx, id, containerName)
}

// ListVolumes returns VolumeInfo for each volume defined on the service, enriched with
// Docker existence data from the host that runs the service.
// For services with no volumes configured in DB, it falls back to container inspection
// to discover volumes that were configured before the volumes feature was added.
func (s *ServiceManager) ListVolumes(ctx context.Context, serviceID string) ([]VolumeInfo, error) {
	svc, err := s.repo.GetByID(ctx, serviceID)
	if err != nil {
		return nil, fmt.Errorf("get service: %w", err)
	}

	if s.factory == nil || s.serverFetcher == nil {
		return buildBaseVolumeInfos(serviceID, svc.Volumes), nil
	}

	var targetServer *serverPkg.Server
	if svc.ServerID != "" {
		if srv, fetchErr := s.serverFetcher.GetServer(ctx, svc.ServerID); fetchErr == nil {
			targetServer = srv
		}
	}
	orch, cleanup, orchErr := s.factory.ForServer(targetServer)
	if orchErr != nil {
		return buildBaseVolumeInfos(serviceID, svc.Volumes), nil
	}
	defer cleanup()

	// No volumes in DB: discover from running container (covers pre-feature services).
	if len(svc.Volumes) == 0 {
		if discoverer, ok := orch.(volumeDiscoverer); ok {
			return discoverer.DiscoverVolumes(ctx, serviceID)
		}
		return []VolumeInfo{}, nil
	}

	// Build base infos from configured volumes then enrich named volumes with Docker data.
	infos := buildBaseVolumeInfos(serviceID, svc.Volumes)

	// Collect only named volume names for Docker inspection (bind mounts don't need it).
	namedVolNames := make([]string, 0, len(svc.Volumes))
	for _, v := range svc.Volumes {
		if v.HostPath == "" {
			namedVolNames = append(namedVolNames, volumeDockerName(serviceID, v.Name))
		}
	}
	if len(namedVolNames) == 0 {
		return infos, nil // all bind mounts — nothing to inspect
	}

	lister, ok := orch.(volumeLister)
	if !ok {
		return infos, nil
	}
	dockerInfos, err := lister.ListVolumes(ctx, namedVolNames)
	if err != nil {
		return infos, nil // best-effort
	}

	existsMap := make(map[string]VolumeInfo, len(dockerInfos))
	for _, di := range dockerInfos {
		existsMap[di.Name] = di
	}
	for i := range infos {
		if infos[i].Type == "bind" {
			continue // bind mounts don't get enriched via volumeLister
		}
		if di, found := existsMap[infos[i].Name]; found {
			infos[i].Exists = di.Exists
			infos[i].SizeBytes = di.SizeBytes
		}
	}
	return infos, nil
}

// buildBaseVolumeInfos builds VolumeInfo slice from the service's configured volumes.
// For named volumes it sets Type="volume" and computes the Docker volume name.
// For bind mounts it sets Type="bind", HostPath, ReadOnly, and Exists=true.
func buildBaseVolumeInfos(serviceID string, volumes []VolumeMount) []VolumeInfo {
	infos := make([]VolumeInfo, len(volumes))
	for i, v := range volumes {
		if v.HostPath != "" {
			infos[i] = VolumeInfo{
				Type:      "bind",
				Name:      v.HostPath,
				MountName: v.Name,
				Target:    v.Target,
				HostPath:  v.HostPath,
				ReadOnly:  v.ReadOnly,
				Exists:    true, // bind mount — existence is assumed; actual path checked at deploy time
			}
		} else {
			infos[i] = VolumeInfo{
				Type:      "volume",
				Name:      volumeDockerName(serviceID, v.Name),
				MountName: v.Name,
				Target:    v.Target,
			}
		}
	}
	return infos
}

// PruneVolume removes the named Docker volume for a service mount (best-effort, data is destroyed).
func (s *ServiceManager) PruneVolume(ctx context.Context, serviceID, mountName string) error {
	if s.factory == nil || s.serverFetcher == nil {
		return fmt.Errorf("orchestration not configured")
	}
	svc, err := s.repo.GetByID(ctx, serviceID)
	if err != nil {
		return fmt.Errorf("get service: %w", err)
	}
	var targetServer *serverPkg.Server
	if svc.ServerID != "" {
		if srv, fetchErr := s.serverFetcher.GetServer(ctx, svc.ServerID); fetchErr == nil {
			targetServer = srv
		}
	}
	orch, cleanup, err := s.factory.ForServer(targetServer)
	if err != nil {
		return fmt.Errorf("get orchestrator: %w", err)
	}
	defer cleanup()

	pruner, ok := orch.(volumePruner)
	if !ok {
		return fmt.Errorf("orchestrator does not support volume pruning")
	}
	return pruner.PruneVolume(ctx, volumeDockerName(serviceID, mountName))
}

// ErrDependenciesNotReady: a service this one depends on is not running.
// CheckDependencies wraps it with which ones; the text is the caller's to show.
var ErrDependenciesNotReady = errors.New("dependencies not ready")

// CheckDependencies verifies all declared dependency services are running.
// Returns ErrDependenciesNotReady, naming them, if any are not; any other
// error is the lookup failing, and is not for the client.
func (s *ServiceManager) CheckDependencies(ctx context.Context, svc *Service) error {
	if len(svc.Dependencies) == 0 {
		return nil
	}
	deps, err := s.repo.RuntimeStatuses(ctx, svc.ID, svc.Dependencies)
	if err != nil {
		return fmt.Errorf("check dependencies: %w", err)
	}
	notReady := make([]string, 0)
	for _, depID := range svc.Dependencies {
		dep, ok := deps[depID]
		if !ok {
			notReady = append(notReady, fmt.Sprintf("unknown (%s)", depID))
			continue
		}
		if dep.RuntimeStatus != "running" {
			notReady = append(notReady, fmt.Sprintf("%s (%s)", dep.Name, dep.RuntimeStatus))
		}
	}
	if len(notReady) > 0 {
		return fmt.Errorf("%w: %s", ErrDependenciesNotReady, strings.Join(notReady, ", "))
	}
	return nil
}

// sanitizeNetworkAlias produces a DNS-safe alias from an arbitrary service name.
// Rules: lowercase, only [a-z0-9-], trim leading/trailing hyphens, max 63 chars.
func sanitizeNetworkAlias(name string) string {
	re := regexp.MustCompile(`[^a-z0-9]+`)
	s := re.ReplaceAllString(strings.ToLower(name), "-")
	s = strings.Trim(s, "-")
	if len(s) > 63 {
		s = strings.TrimRight(s[:63], "-")
	}
	return s
}

// defaultNetworkAlias is the alias a new service named name gets when its
// creator sets none: with its environment's name in an environment, so the
// same name in two environments doesn't collide (api-staging,
// api-production); environment is "" for a shared service. The New service
// form names it before the service exists (frontend/src/lib/network-alias.ts,
// held to testdata/network_alias.json with this).
func defaultNetworkAlias(name, environment string) string {
	if environment != "" {
		name += "-" + environment
	}
	return sanitizeNetworkAlias(name)
}

// validateAliasUnique returns an error if another service in the same environment
// already owns the given alias. excludeID is empty for new services and the
// service's own ID for updates. Fails open on repo error to avoid blocking deploys.
func (s *ServiceManager) validateAliasUnique(ctx context.Context, environmentID, excludeID, alias string) error {
	if alias == "" {
		return nil
	}
	existing, err := s.repo.ListByEnvironment(ctx, environmentID)
	if err != nil {
		return nil // fail open
	}
	for _, svc := range existing {
		if svc.NetworkAlias == alias && svc.ID != excludeID {
			return fmt.Errorf("network alias %q is already used by service %q in this environment", alias, svc.Name)
		}
	}
	return nil
}

// validateCreateInput validates a new service's port, limits and domain
// routing choice.
func validateCreateInput(input CreateInput) error {
	if err := validateServiceInput(input.Port, input.CPULimit, input.MemoryLimit); err != nil {
		return err
	}
	if err := validateGitSource(input.GitRepoURL, input.ComposeRepoURL, input.GitBranch); err != nil {
		return err
	}
	if err := ValidateBaseDirectory(input.BaseDirectory); err != nil {
		return err
	}
	if err := validateRepositorySource(input.SourceType, input.RepositoryID, input.GitRepoURL, input.ComposeRepoURL); err != nil {
		return err
	}
	if err := validateComposeContent(input.ComposeContent); err != nil {
		return err
	}
	if (input.BasicAuthUser == "") != (input.BasicAuthPassword == "") {
		return fmt.Errorf("%w: basic auth needs both a user and a password", ErrInvalidConfig)
	}
	if err := validateAccessControl(input.BasicAuthUser, input.IPAllowlist); err != nil {
		return err
	}
	if err := validateContainerHealth(input.ContainerHealth, input.ContainerHealthCommand, input.SourceType); err != nil {
		return err
	}
	return validateComposeRouteService(input.ComposeRouteService)
}

// validateAccessControl checks what becomes Traefik labels: a basic auth
// user htpasswd can carry, and IP allowlist entries that are addresses or
// CIDR ranges (Traefik drops a router whose middleware does not load).
func validateAccessControl(user string, allowlist []string) error {
	if strings.ContainsAny(user, ":\r\n") {
		return fmt.Errorf("%w: the basic auth user cannot contain ':' or line breaks", ErrInvalidConfig)
	}
	for _, e := range allowlist {
		if net.ParseIP(e) != nil {
			continue
		}
		if _, _, err := net.ParseCIDR(e); err != nil {
			return fmt.Errorf("%w: IP allowlist entry %q is not an address or a range such as 10.0.0.0/8", ErrInvalidConfig, e)
		}
	}
	return nil
}

// validateServiceInput validates port, CPU limit, and memory limit values.
// Errors wrap ErrInvalidConfig.
func validateServiceInput(port int, cpuLimit, memLimit string) error {
	if port != 0 && (port < 1 || port > 65535) {
		return fmt.Errorf("%w: port must be between 1 and 65535", ErrInvalidConfig)
	}
	if cpuLimit != "" {
		var cpu float64
		if _, err := fmt.Sscanf(cpuLimit, "%f", &cpu); err != nil || cpu <= 0 || cpu > 64.0 {
			return fmt.Errorf("%w: cpu_limit must be a positive number (e.g. 0.5, 2.0)", ErrInvalidConfig)
		}
	}
	if memLimit != "" {
		if !isValidMemoryLimit(memLimit) {
			return fmt.Errorf("%w: memory_limit must be a positive number optionally followed by k/m/g (e.g. 512m, 1g)", ErrInvalidConfig)
		}
	}
	return nil
}

// validateComposeRouteService checks the compose service chosen to receive a
// service's domains. Empty is automatic. The name ends up as a key in the
// compose override file the platform generates, so it is held to the
// characters compose allows in service names (composespec.ValidServiceName).
func validateComposeRouteService(name string) error {
	if name == "" || composespec.ValidServiceName(name) {
		return nil
	}
	return fmt.Errorf("%w: compose_route_service must be the name of a service in the compose file "+
		"(letters, digits, '_', '.' and '-', starting with a letter or digit, at most 63 characters), or empty for automatic", ErrInvalidConfig)
}

// validateDeployStrategy accepts the three strategies, and "" for no change.
func validateDeployStrategy(strategy string) error {
	switch strategy {
	case "", DeployStrategyAuto, DeployStrategyRolling, DeployStrategyRecreate:
		return nil
	}
	return fmt.Errorf("%w: deploy_strategy must be auto, rolling or recreate", ErrInvalidConfig)
}

// maxContainerHealthCommand is the longest container check command, in
// characters, as the column's CHECK and the contract's maxLength count.
const maxContainerHealthCommand = 1024

// validateContainerHealth checks a container check: one of the three modes
// ("" is the image's), a command of one line within its length, and one
// for the command mode. A compose stack keeps its image's: its file sets
// its own.
func validateContainerHealth(mode, command string, source SourceType) error {
	switch mode {
	case "", ContainerHealthImage, ContainerHealthOff, ContainerHealthCommand:
	default:
		return fmt.Errorf("%w: container_health must be image, off or command", ErrInvalidConfig)
	}
	if utf8.RuneCountInString(command) > maxContainerHealthCommand {
		return fmt.Errorf("%w: container_health_command is at most %d characters", ErrInvalidConfig, maxContainerHealthCommand)
	}
	if strings.ContainsAny(command, "\x00\r\n") {
		return fmt.Errorf("%w: container_health_command must be one line", ErrInvalidConfig)
	}
	if mode == ContainerHealthCommand && strings.TrimSpace(command) == "" {
		return fmt.Errorf("%w: container_health command needs a container_health_command", ErrInvalidConfig)
	}
	if source == SourceTypeDockerCompose && mode != "" && mode != ContainerHealthImage {
		return fmt.Errorf("%w: a compose stack's file sets its containers' health checks, so container_health stays image", ErrInvalidConfig)
	}
	return nil
}

// GenerateComposeExport generates a docker-compose.yml for all services in a project.
// Volume names match the "paas-vol-{id[:8]}-{name}" convention used at runtime.
func (s *ServiceManager) GenerateComposeExport(ctx context.Context, projectID string) (string, error) {
	services, err := s.repo.ListByProject(ctx, projectID)
	if err != nil {
		return "", fmt.Errorf("list services: %w", err)
	}

	type composeNetwork struct{}
	type composeVolume struct {
		External bool `yaml:"external"`
	}
	type endpointCfg struct {
		Aliases []string `yaml:"aliases,omitempty"`
	}
	type composeSvc struct {
		Image       string                 `yaml:"image,omitempty"`
		Command     string                 `yaml:"command,omitempty"`
		Restart     string                 `yaml:"restart"`
		Environment []string               `yaml:"environment,omitempty"`
		Volumes     []string               `yaml:"volumes,omitempty"`
		Networks    map[string]endpointCfg `yaml:"networks,omitempty"`
		Labels      []string               `yaml:"labels,omitempty"`
	}

	// A stack-local network: the export runs standalone and never joins
	// the control plane's network. The services keep their aliases.
	const networkName = "app"

	svcMap := make(map[string]composeSvc, len(services))
	volMap := make(map[string]composeVolume)

	for _, svc := range services {
		if svc.SourceType == SourceTypeDockerCompose {
			continue // compose-in-compose not supported for export
		}

		idPrefix := svc.ID
		if len(idPrefix) > 8 {
			idPrefix = idPrefix[:8]
		}

		var volEntries []string
		for _, v := range svc.Volumes {
			volName := "paas-vol-" + idPrefix + "-" + v.Name
			volEntries = append(volEntries, volName+":"+v.Target)
			volMap[volName] = composeVolume{External: true}
		}

		nets := map[string]endpointCfg{}
		ep := endpointCfg{}
		if svc.NetworkAlias != "" {
			ep.Aliases = []string{svc.NetworkAlias}
		}
		nets[networkName] = ep

		csvc := composeSvc{
			Image:    svc.DockerImage,
			Restart:  "unless-stopped",
			Volumes:  volEntries,
			Networks: nets,
		}
		if svc.StartCommand != "" {
			csvc.Command = svc.StartCommand
		}

		svcMap[svc.Name] = csvc
	}

	doc := map[string]interface{}{
		"version":  "3.8",
		"services": svcMap,
		"networks": map[string]composeNetwork{
			networkName: {},
		},
	}
	if len(volMap) > 0 {
		doc["volumes"] = volMap
	}

	out, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("marshal compose: %w", err)
	}
	return string(out), nil
}

// ListMetricsTargets returns every service currently in "running" status,
// with its name and server. The metrics collector samples them.
func (s *ServiceManager) ListMetricsTargets(ctx context.Context) ([]MetricsTarget, error) {
	return s.repo.ListRunningMetricsTargets(ctx)
}

// CollectServerStats samples the targets running on one server ("" = the
// local Docker host) with a single call to that host's orchestrator, and
// reads their memory kills since since (ServiceStatsCollector). Targets on
// another server are ignored.
func (s *ServiceManager) CollectServerStats(ctx context.Context, serverID string, targets []MetricsTarget, since int64) (HostSample, error) {
	if s.factory == nil || s.serverFetcher == nil {
		return HostSample{}, errors.New("orchestration not configured")
	}
	onServer := make([]MetricsTarget, 0, len(targets))
	for _, t := range targets {
		if t.ServerID == serverID {
			onServer = append(onServer, t)
		}
	}
	if len(onServer) == 0 {
		return HostSample{}, nil
	}
	orch, cleanup, err := s.orchestratorFor(ctx, serverID)
	if err != nil {
		return HostSample{}, err
	}
	defer cleanup()
	collector, ok := orch.(ServiceStatsCollector)
	if !ok {
		return HostSample{}, errors.New("orchestrator does not support stats collection")
	}
	return collector.CollectServiceStats(ctx, onServer, since)
}

// CollectAgentStats returns the samples the server's agent took after after
// (zero: after the newest stored for the server's services), oldest first,
// each at its own time (HostSample.At), for the targets on that server
// (D-113). kept is false when the server's agent keeps none (the local
// Docker host, an SSH server, an older agent): the caller samples it with
// CollectServerStats.
func (s *ServiceManager) CollectAgentStats(ctx context.Context, serverID string, targets []MetricsTarget, after time.Time) (samples []HostSample, kept bool, err error) {
	if serverID == "" || s.factory == nil || s.serverFetcher == nil {
		return nil, false, nil
	}
	srv, err := s.serverFetcher.GetServer(ctx, serverID)
	if err != nil {
		return nil, false, fmt.Errorf("get server: %w", err)
	}
	if srv.Connection != serverPkg.ConnectionAgent {
		return nil, false, nil
	}
	orch, cleanup, err := s.factory.ForServer(srv)
	if err != nil {
		return nil, false, fmt.Errorf("get orchestrator: %w", err)
	}
	defer cleanup()
	reader, ok := orch.(AgentStatsReader)
	if !ok || !reader.KeepsStats() {
		return nil, false, nil
	}
	if after.IsZero() {
		if after, err = s.repo.NewestMetricOn(ctx, serverID); err != nil {
			return nil, true, err
		}
	}
	var since int64
	if !after.IsZero() {
		since = after.Unix()
	}
	onServer := make([]MetricsTarget, 0, len(targets))
	for _, t := range targets {
		if t.ServerID == serverID {
			onServer = append(onServer, t)
		}
	}
	samples, err = reader.AgentServiceStats(ctx, onServer, since)
	return samples, true, err
}

// orchestratorFor returns the orchestrator of a server ("" = the local
// Docker host). The caller must call cleanup.
func (s *ServiceManager) orchestratorFor(ctx context.Context, serverID string) (ContainerOrchestrator, func(), error) {
	if s.factory == nil || s.serverFetcher == nil {
		return nil, nil, errors.New("orchestration not configured")
	}
	var srv *serverPkg.Server
	if serverID != "" {
		var err error
		if srv, err = s.serverFetcher.GetServer(ctx, serverID); err != nil {
			return nil, nil, fmt.Errorf("get server: %w", err)
		}
	}
	orch, cleanup, err := s.factory.ForServer(srv)
	if err != nil {
		return nil, nil, fmt.Errorf("get orchestrator: %w", err)
	}
	return orch, cleanup, nil
}

// ProbeServerStates returns the container states of the targets running on
// one server ("" = the local Docker host), for alerts. Targets on another
// server are ignored.
func (s *ServiceManager) ProbeServerStates(ctx context.Context, serverID string, targets []MetricsTarget) (map[string][]ContainerState, error) {
	onServer := make([]MetricsTarget, 0, len(targets))
	for _, t := range targets {
		if t.ServerID == serverID {
			onServer = append(onServer, t)
		}
	}
	orch, cleanup, err := s.orchestratorFor(ctx, serverID)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	probe, ok := orch.(ServiceStateProbe)
	if !ok {
		return nil, errors.New("orchestrator does not report container states")
	}
	return probe.ServiceStates(ctx, onServer)
}

// ProbeServerDisk returns how full a server's filesystems are ("" = the
// local Docker host), for alerts.
func (s *ServiceManager) ProbeServerDisk(ctx context.Context, serverID string) ([]DiskUsage, error) {
	orch, cleanup, err := s.orchestratorFor(ctx, serverID)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	probe, ok := orch.(ServiceStateProbe)
	if !ok {
		return nil, errors.New("orchestrator does not report disk usage")
	}
	return probe.DiskUsage(ctx)
}

// InsertSample records one compose service's sample ("" for a
// single-container service) for the metrics collector, collected at the
// cycle's time at.
func (s *ServiceManager) InsertSample(ctx context.Context, serviceID, composeService string, at time.Time, m MetricSample) error {
	return s.repo.InsertSample(ctx, serviceID, composeService, at, m)
}

// ListMetrics returns historical metric points collected after `since`.
func (s *ServiceManager) ListMetrics(ctx context.Context, serviceID, composeService string, since time.Time) ([]MetricPoint, error) {
	return s.repo.ListMetrics(ctx, serviceID, composeService, since)
}

// ListDailyMetrics returns a service's week as of now, one point per UTC
// day (the days of SummariseDays): the 6 days before today, then today so
// far. composeService "" sums a stack's compose services.
func (s *ServiceManager) ListDailyMetrics(ctx context.Context, serviceID, composeService string, now time.Time) ([]MetricPoint, error) {
	return s.repo.ListDailyMetrics(ctx, serviceID, composeService, now.UTC().Truncate(24*time.Hour))
}

// DeleteOldMetrics removes metric rows older than `before` (used for GC).
func (s *ServiceManager) DeleteOldMetrics(ctx context.Context, before time.Time) error {
	return s.repo.DeleteOldMetrics(ctx, before)
}

// isValidMemoryLimit accepts exactly what resources.ParseMB reads, the one
// parser of memory limits (D-41).
func isValidMemoryLimit(limit string) bool {
	_, ok := resources.ParseMB(limit)
	return ok
}
