package deployment

import (
	"context"
	"errors"

	domainPkg "github.com/Nickpap21/toft/backend/internal/domain"
	envPkg "github.com/Nickpap21/toft/backend/internal/environment"
	serverPkg "github.com/Nickpap21/toft/backend/internal/server"
	serviceManager "github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/pkg/actor"
	"go.uber.org/zap"
)

// WithDeployUseCase gives Deploy the other modules it calls: the service's
// settings and gates, its server, domains, variables and environment, and
// the workspace's audit log.
func (s *Service) WithDeployUseCase(sm ServiceManagerOps, serverSvc *serverPkg.Service, ds *domainPkg.Service, evp EnvVarProvider, evs EnvVarSetter, envSvc *envPkg.Service, orgSvc OrgAuditWriter) {
	s.serviceManager, s.serverSvc, s.domainSvc = sm, serverSvc, ds
	s.envVarProvider, s.envVarSetter, s.envSvc, s.orgSvc = evp, evs, envSvc, orgSvc
}

// WithVersionCache injects a version cache invalidator called after version-override deploys.
func (s *Service) WithVersionCache(v VersionCacheInvalidator) {
	s.versionCache = v
}

// refusal is a deploy the use case turned down before Trigger: what to tell
// the caller, and what kind of refusal it is. The route picks its status
// from the kind (Handler.problem); nothing here knows HTTP.
type refusal struct {
	kind refusalKind
	msg  string
}

func (r *refusal) Error() string { return r.msg }

type refusalKind int

const (
	refusedFailed    refusalKind = iota // something of ours failed; msg says nothing of what
	refusedNotFound                     // not a service of the caller's workspace
	refusedForbidden                    // the caller may not deploy
	refusedInvalid                      // the request itself is wrong
	refusedConflict                     // the service's state does not allow it
)

// DeployOverrides is a deploy's body: what it deploys instead of what the
// service's settings say.
type DeployOverrides struct {
	CommitSHA  string `json:"commit_sha"`   // recorded; a compose deploy also checks its repository out at it (full SHA)
	ImageTag   string `json:"image_tag"`    // overrides docker_image (and persists); for compose, sets the IMAGE_TAG env var
	GitRef     string `json:"git_ref"`      // overrides git ref for this deploy
	GitRefType string `json:"git_ref_type"` // "branch" | "tag"
	// ServerID, when given and not the service's server, moves the
	// service there ("" is the control-plane server): see move.go.
	ServerID *string `json:"server_id"`
}

// Deploy is the deploy route's use case: every gate, then Trigger. What
// refuses it is a *refusal, the capacity gate's error, the agent policy's or
// one of Trigger's (Handler.problem answers each). fromCreate marks the
// first deploy a create starts (TriggerInput.FromCreate).
func (s *Service) Deploy(ctx context.Context, who serviceManager.DeployCaller, serviceID string, body DeployOverrides, force, fromCreate bool) (*Deployment, error) {
	orgID, userID := who.OrgID, who.UserID

	if owned, err := s.serviceManager.CheckOwnership(ctx, serviceID, orgID); err != nil || !owned {
		return nil, &refusal{refusedNotFound, "service not found"}
	}
	// Viewers read; they don't deploy (the panel hides the button, the API refuses).
	if who.Role == "viewer" {
		return nil, &refusal{refusedForbidden, "viewers cannot deploy"}
	}

	svc, err := s.serviceManager.Get(ctx, serviceID)
	if err != nil {
		s.log.Error("failed to fetch service for deployment", zap.Error(err), zap.String("service_id", serviceID))
		return nil, &refusal{refusedFailed, "failed to load service configuration"}
	}
	if err := s.previewCopy(ctx, serviceID); err != nil {
		return nil, err
	}
	if err := s.agentGate(ctx, serviceID, ""); err != nil {
		return nil, err
	}

	// Block deploy if any declared dependencies are not running.
	if err := s.serviceManager.CheckDependencies(ctx, svc); err != nil {
		if errors.Is(err, serviceManager.ErrDependenciesNotReady) {
			return nil, &refusal{refusedConflict, err.Error()}
		}
		// The lookup failed: its text (a database error) stays in the log.
		s.log.Error("failed to check dependencies for deployment", zap.Error(err), zap.String("service_id", serviceID))
		return nil, &refusal{refusedFailed, "failed to check the service's dependencies"}
	}

	var moveFrom *MoveSource
	if body.ServerID != nil && *body.ServerID != svc.ServerID {
		if moveFrom, err = s.startMove(ctx, svc, *body.ServerID); err != nil {
			return nil, err
		}
	}

	// A deploy that doesn't fit stops the server's oldest previews first
	// (D-15, D-97), then meets the gate.
	envNameForRoom := ""
	if svc.EnvironmentID != "" {
		if env, err := s.envSvc.Get(ctx, svc.EnvironmentID); err == nil {
			envNameForRoom = env.Name
		}
	}
	s.MakeRoom(ctx, TriggerInput{ServiceID: svc.ID, ServerID: svc.ServerID, OrgID: orgID,
		ServiceName: svc.Name, EnvironmentName: envNameForRoom, ActorID: userID})

	// Capacity gate: reject if the service's resource requests would exceed
	// server capacity. ?force=true deploys past it only for a caller allowed
	// to bypass (checkCapacity's CanBypass); anyone else gets the 409 again.
	reqCPU, reqMemMB := s.requestedLimits(ctx, svc)
	capErr, err := s.checkCapacity(
		ctx,
		svc.ServerID, svc.ID, userID,
		who.Role, orgID,
		reqCPU, reqMemMB,
	)
	if err != nil {
		s.log.Error("capacity check error", zap.Error(err))
		// non-fatal: proceed with deployment
	} else if capErr != nil {
		if !force || !capErr.CanBypass {
			return nil, capErr
		}
		s.orgSvc.WriteAuditLog(ctx, orgID, userID, "deployment.capacity_bypassed", "service:"+serviceID)
	}

	// Resolve the target server (nil = use local Docker socket). A server
	// that cannot be loaded refuses the deploy; it never falls back to the
	// control-plane server.
	targetServer, err := s.targetServer(ctx, orgID, svc)
	if err != nil {
		return nil, err
	}

	// D-19 stopgap: refuse before anything is persisted (Trigger checks again).
	if err := s.AuthorizeTarget(ctx, OriginInteractive, userID, serviceID, targetServer); err != nil {
		return nil, err
	}

	hostnames, certified := s.hostnamesForService(ctx, serviceID)
	access := svc.ToAccessControl()
	access.Certified = certified

	envVars, err := s.envVarProvider.ListAllForDeployment(ctx, serviceID)
	if err != nil {
		s.log.Warn("could not fetch env vars for deployment", zap.Error(err), zap.String("service_id", serviceID))
		envVars = []string{}
	}

	if body.CommitSHA != "" && svc.RepositoryID != "" && !isFullSHA(body.CommitSHA) {
		// A build from the project's own repository builds commit_sha (D-77).
		return nil, &refusal{refusedInvalid, "commit_sha must be a full 40-character commit SHA"}
	}
	// What the override replaced (the image and a registry password it
	// cleared), put back if no deploy starts.
	previousImage, clearedPassword := "", ""
	// A compose stack's own IMAGE_TAG the override replaced (hadTag false:
	// it had none), put back or removed if no deploy starts.
	tagSaved, previousTag, hadTag := false, "", false
	if svc.SourceType == serviceManager.SourceTypeDockerCompose {
		// A compose deploy checks its repository out at commit_sha, and git
		// fetches a commit only by its full SHA.
		if body.CommitSHA != "" && !isFullSHA(body.CommitSHA) {
			return nil, &refusal{refusedInvalid, "commit_sha must be a full 40-character commit SHA"}
		}
		// For a compose service image_tag is IMAGE_TAG, which the compose file
		// interpolates (image: ghcr.io/acme/api:${IMAGE_TAG}). It is saved as
		// the service's IMAGE_TAG env var, so later deploys (a redeploy from the
		// panel) use the same tag and it shows in the Env tab.
		if body.ImageTag != "" {
			if !imageTagPattern.MatchString(body.ImageTag) {
				return nil, &refusal{refusedInvalid, "image_tag must be an image tag: letters, digits, '_', '.' and '-', at most 128 characters"}
			}
			prev, had, err := s.envVarSetter.Set(ctx, serviceID, "IMAGE_TAG", body.ImageTag)
			if err != nil {
				s.log.Error("failed to save IMAGE_TAG", zap.Error(err), zap.String("service_id", serviceID))
				return nil, &refusal{refusedFailed, "failed to save image_tag as IMAGE_TAG"}
			}
			tagSaved, previousTag, hadTag = true, prev, had
			envVars = withEnv(envVars, "IMAGE_TAG", body.ImageTag)
		}
	} else if body.ImageTag != "" {
		// Version override: update service config when caller specifies an explicit version.
		if cleared, err := s.serviceManager.UpdateDockerImage(ctx, serviceID, body.ImageTag); err != nil {
			s.log.Warn("failed to persist docker image override", zap.Error(err), zap.String("service_id", serviceID))
		} else {
			previousImage = svc.DockerImage
			svc.DockerImage = body.ImageTag
			if cleared {
				clearedPassword = svc.RegistryPassword // put back with the image
				svc.RegistryPassword = ""              // never sent to the new image's registry
			}
		}
		if s.versionCache != nil {
			s.versionCache.InvalidateCache(serviceID)
		}
	}
	if body.GitRef != "" && body.GitRefType == "branch" {
		if err := serviceManager.ValidateGitBranch(body.GitRef); err != nil {
			return nil, &refusal{refusedInvalid, err.Error()}
		}
		if err := s.serviceManager.UpdateGitBranch(ctx, serviceID, body.GitRef); err != nil {
			s.log.Warn("failed to persist git branch override", zap.Error(err), zap.String("service_id", serviceID))
		} else {
			svc.GitBranch = body.GitRef
		}
		if s.versionCache != nil {
			s.versionCache.InvalidateCache(serviceID)
		}
	}
	if body.GitRef != "" && body.GitRefType == "tag" {
		// Tags are immutable; don't update the stored branch — just deploy from the tag ref.
		svc.GitBranch = body.GitRef
	}

	// Resolve the environment name for human-readable container naming.
	envName := ""
	if svc.EnvironmentID != "" {
		if env, err := s.envSvc.Get(ctx, svc.EnvironmentID); err == nil {
			envName = env.Name
		}
	}

	var regAuth *RegistryAuth
	if svc.RegistryPassword != "" {
		regAuth = &RegistryAuth{
			Server:   svc.RegistryServer,
			Username: svc.RegistryUsername,
			Password: svc.RegistryPassword,
		}
	}

	input := TriggerInput{
		ServiceID:           serviceID,
		OrgID:               orgID,
		ProjectID:           svc.ProjectID,
		AddonSlug:           svc.AddonSlug,
		ServerID:            svc.ServerID,
		Server:              targetServer,
		SourceType:          string(svc.SourceType),
		GitRepoURL:          svc.GitRepoURL,
		GitBranch:           svc.GitBranch,
		RepositoryID:        svc.RepositoryID,
		BaseDirectory:       svc.BaseDirectory,
		DockerImage:         svc.DockerImage,
		ComposeContent:      svc.ComposeContent,
		ComposeRepoURL:      svc.ComposeRepoURL,
		ComposeFilePath:     svc.ComposeFilePath,
		ComposeRouteService: svc.ComposeRouteService,
		RegistryAuth:        regAuth,
		CommitSHA:           body.CommitSHA,
		CheckoutSHA:         composeCheckout(svc, body.CommitSHA),
		Domains:             hostnames,
		EnvVars:             envVars,
		Port:                svc.Port,
		CPULimit:            svc.CPULimit,
		MemoryLimit:         svc.MemoryLimit,
		Volumes:             svc.Volumes,
		NetworkAlias:        svc.NetworkAlias,
		StartCommand:        svc.StartCommand,
		BuildPack:           svc.BuildPack,
		Nixpacks:            NixpacksCommands{Install: svc.InstallCommand, Build: svc.BuildCommand},
		HealthCheckPath:     svc.HealthCheckPath,
		HealthCheckTimeout:  svc.HealthCheckTimeout,
		HealthCheckRetries:  svc.HealthCheckRetries,
		GitUsername:         svc.GitUsername,
		GitToken:            svc.GitToken,
		PreDeployCommands:   SplitLines(svc.PreDeployCommands),
		PostDeployCommands:  ParsePostDeployCommands(svc.PostDeployCommands),
		RequiredEnvVars:     SplitLines(svc.RequiredEnvVars),
		ServiceName:         svc.Name,
		EnvironmentName:     envName,
		MigrateCommand:      svc.MigrateCommand,
		AccessControl:       access,
		Recreate:            svc.Recreates(),
		Origin:              OriginInteractive,
		ActorID:             userID,
		TriggeredBy:         s.triggeredBy(ctx, who, ""),
		MoveFrom:            moveFrom,
		Settings:            svc,
		FromCreate:          fromCreate,
	}
	// A client that created the service with deploy and now deploys it
	// itself (a tab loaded before the create did): one deploy, not two.
	if !fromCreate && body == (DeployOverrides{}) {
		if d := s.FirstDeploy(ctx, input); d != nil {
			return d, nil
		}
	}
	d, err := s.Trigger(ctx, input)
	if err != nil {
		if previousImage != "" && previousImage != body.ImageTag {
			if serr := s.serviceManager.PutBackDockerImage(context.WithoutCancel(ctx), serviceID, body.ImageTag, previousImage, clearedPassword); serr != nil {
				s.log.Warn("failed to put the previous docker image back", zap.Error(serr), zap.String("service_id", serviceID))
			}
		}
		if tagSaved && (!hadTag || previousTag != body.ImageTag) {
			ctx := context.WithoutCancel(ctx)
			var serr error
			if hadTag {
				serr = s.envVarSetter.Swap(ctx, serviceID, "IMAGE_TAG", body.ImageTag, previousTag)
			} else {
				serr = s.envVarSetter.Unset(ctx, serviceID, "IMAGE_TAG", body.ImageTag)
			}
			if serr != nil {
				s.log.Warn("failed to put the previous IMAGE_TAG back", zap.Error(serr), zap.String("service_id", serviceID))
			}
		}
		return nil, err
	}

	s.orgSvc.WriteAuditLog(ctx, orgID, userID, "deployment.triggered", "service:"+serviceID)
	if moveFrom != nil {
		s.orgSvc.WriteAuditLog(ctx, orgID, userID, "service.move_started", "service:"+serviceID)
	}
	return d, nil
}

// startMove checks a move of svc to the server to ("" the control-plane
// server) and returns the server it moves from. svc.ServerID becomes to, so
// the capacity check and the deploy's target are the new server's
// (targetServer checks the workspace may use it).
func (s *Service) startMove(ctx context.Context, svc *serviceManager.Service, to string) (*MoveSource, error) {
	if svc.Deploying {
		return nil, &refusal{refusedConflict, "a deploy of this service is in progress: move it once it has finished"}
	}
	if err := s.serviceManager.CheckMove(ctx, svc.ID); err != nil {
		if errors.Is(err, serviceManager.ErrMountsSharedVolume) {
			return nil, &refusal{refusedConflict, err.Error()}
		}
		s.log.Error("move check failed", zap.Error(err), zap.String("service_id", svc.ID))
		return nil, &refusal{refusedFailed, "could not check the move"}
	}
	from := &MoveSource{ServerID: svc.ServerID}
	if svc.ServerID != "" {
		srv, err := s.serverSvc.GetServer(ctx, svc.ServerID)
		if err != nil || srv == nil {
			s.log.Error("failed to fetch the server a move leaves", zap.Error(err), zap.String("server_id", svc.ServerID))
			return nil, ErrServerUnavailable
		}
		from.Server = srv
	}
	svc.ServerID = to
	return from, nil
}

// targetServer loads the service's server (nil for the control-plane
// server). When it cannot be loaded it answers with ErrServerUnavailable and
// reports false: a deploy never falls back to the control-plane server.
func (s *Service) targetServer(ctx context.Context, orgID string, svc *serviceManager.Service) (*serverPkg.Server, error) {
	if svc.ServerID == "" {
		return nil, nil
	}
	srv, err := s.serverSvc.GetServer(ctx, svc.ServerID)
	if err != nil || srv == nil {
		s.log.Error("failed to fetch server for deployment", zap.Error(err), zap.String("server_id", svc.ServerID))
		return nil, ErrServerUnavailable
	}
	// A service deploys only to a server its workspace owns or has a share
	// of (the service's workspace is the caller's: its ownership was
	// checked).
	if ok, err := s.serverSvc.CanUse(ctx, srv.ID, orgID); err != nil || !ok {
		s.log.Error("service assigned to another workspace's server", zap.String("service_id", svc.ID), zap.String("server_id", svc.ServerID))
		return nil, ErrServerUnavailable
	}
	return srv, nil
}

// agentGate runs the agent policy (D-100): its refusal, or the 202 of a
// call waiting for a person (*actor.GateError).
func (s *Service) agentGate(ctx context.Context, serviceID, rollbackTo string) error {
	err := s.CheckAgent(ctx, serviceID, rollbackTo)
	var ge *actor.GateError
	if err == nil || errors.As(err, &ge) {
		return err
	}
	s.log.Error("agent policy check failed", zap.Error(err), zap.String("service_id", serviceID))
	return &refusal{refusedFailed, "failed to check the agent policy"}
}

// previewCopy refuses one of a preview's services, which deploys from its
// preview only (D-97): ErrPreviewService.
func (s *Service) previewCopy(ctx context.Context, serviceID string) error {
	isCopy, err := s.IsPreviewCopy(ctx, serviceID)
	if err != nil {
		return &refusal{refusedFailed, "failed to load service configuration"}
	}
	if isCopy {
		return ErrPreviewService
	}
	return nil
}

// hostnamesForService returns the service's hostnames, and those a
// certificate has answered for (service.AccessControl.Certified).
func (s *Service) hostnamesForService(ctx context.Context, serviceID string) ([]string, []string) {
	domains, err := s.domainSvc.List(ctx, serviceID)
	if err != nil {
		s.log.Warn("could not fetch domains for deployment", zap.Error(err), zap.String("service_id", serviceID))
		return []string{}, nil
	}
	hostnames := make([]string, len(domains))
	var certified []string
	for i, d := range domains {
		hostnames[i] = d.Hostname
		if d.CertificateAt != nil {
			certified = append(certified, d.Hostname)
		}
	}
	return hostnames, certified
}

// triggeredBy is a deployment's "triggered by": how ("Manual", "API" for an
// API key, or the given kind) and, when known, who. An agent is who, acting
// for its person: "Agent · Claude Code for admin", "Rollback · Agent · …"
// (D-100).
func (s *Service) triggeredBy(ctx context.Context, caller serviceManager.DeployCaller, kind string) string {
	if a, ok := actor.AgentFrom(ctx); ok {
		who := "Agent · " + a.Name
		if s.users != nil {
			if name, err := s.users.UserName(ctx, a.UserID); err == nil && name != "" {
				who += " for " + name
			}
		}
		if kind == "" {
			return who
		}
		return kind + " · " + who
	}
	if kind == "" {
		kind = "Manual"
		if caller.ViaAPIKey {
			kind = "API"
		}
	}
	if s.users != nil {
		if name, err := s.users.UserName(ctx, caller.UserID); err == nil && name != "" {
			return kind + " · " + name
		}
	}
	return kind
}
