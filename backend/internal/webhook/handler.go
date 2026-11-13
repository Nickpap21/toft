package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/internal/code"
	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/deploypolicy"
	domainPkg "github.com/Nickpap21/toft/backend/internal/domain"
	envPkg "github.com/Nickpap21/toft/backend/internal/environment"
	"github.com/Nickpap21/toft/backend/internal/project"
	serverPkg "github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ServiceFinder is satisfied by *service.ServiceManager: the project's
// services that deploy from the pushed repository and branch.
type ServiceFinder interface {
	FindByProjectRepoAndBranch(ctx context.Context, projectID, repoURL, repositoryID, branch string) ([]service.Service, error)
	Get(ctx context.Context, id string) (*service.Service, error)
}

// ProjectReader is satisfied by *project.Service.
type ProjectReader interface {
	GetProject(ctx context.Context, id string) (*project.Project, error)
	CheckOwnership(ctx context.Context, id, orgID string) (bool, error)
}

// SecretStore is satisfied by *Secrets.
type SecretStore interface {
	Get(ctx context.Context, projectID string) (string, error)
	Ensure(ctx context.Context, projectID string) (string, error)
	Rotate(ctx context.Context, projectID string) (string, error)
	LastPush(ctx context.Context, projectID string) (LastPush, error)
	MarkPushed(ctx context.Context, projectID, repo string) error
}

// Handler handles inbound GitHub webhook push events, and deploys pushes to
// the project's own repositories (DeployPush, code's events outbox, D-77).
type Handler struct {
	serviceSvc     ServiceFinder
	deploymentSvc  *deployment.Service
	serverSvc      *serverPkg.Service
	domainSvc      *domainPkg.Service
	envVarProvider deployment.EnvVarProvider
	envSvc         *envPkg.Service
	projects       ProjectReader
	secrets        SecretStore
	audit          AuditWriter // nil: Toft's redeploys aren't audited (tests)
	log            *zap.Logger
}

// AuditWriter records what Toft does on its own. Satisfied by org.Service.
type AuditWriter interface {
	WriteAuditLogDetail(ctx context.Context, orgID, actorID, action, resource string, detail map[string]any)
}

// WithAudit records Toft's redeploys in the audit log.
func (h *Handler) WithAudit(a AuditWriter) { h.audit = a }

func NewHandler(
	serviceSvc ServiceFinder,
	deploymentSvc *deployment.Service,
	serverSvc *serverPkg.Service,
	domainSvc *domainPkg.Service,
	evp deployment.EnvVarProvider,
	envSvc *envPkg.Service,
	projects ProjectReader,
	secrets SecretStore,
	log *zap.Logger,
) *Handler {
	return &Handler{
		serviceSvc:     serviceSvc,
		deploymentSvc:  deploymentSvc,
		serverSvc:      serverSvc,
		domainSvc:      domainSvc,
		envVarProvider: evp,
		envSvc:         envSvc,
		projects:       projects,
		secrets:        secrets,
		log:            log,
	}
}

// RegisterRoutes mounts the delivery routes on a public (no-JWT) group.
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.POST("/webhooks/github/:projectID", h.handleGitHub)
	rg.POST("/webhooks/github", h.handleRetiredRoute)
}

// RegisterProtectedRoutes mounts the routes that show and rotate a
// project's webhook, on the protected group.
func (h *Handler) RegisterProtectedRoutes(rg *gin.RouterGroup) {
	rg.GET("/projects/:projectID/webhook", h.handleInfo)
	rg.POST("/projects/:projectID/webhook/rotate", h.handleRotate)
}

// handleRetiredRoute answers deliveries to the instance-wide URL, which no
// longer deploys anything. GitHub shows the message in the delivery log.
func (h *Handler) handleRetiredRoute(c *gin.Context) {
	respond.Error(c, http.StatusGone, "this webhook URL is retired: use the project's webhook URL and secret from a service's Webhook card")
}

// pushedBy is a push deployment's "triggered by": "Push · <pusher>".
func pushedBy(pusher string) string {
	if pusher = strings.TrimSpace(pusher); pusher != "" {
		return "Push · " + pusher
	}
	return "Push"
}

// firstLine is a commit message's subject, at most 200 characters.
func firstLine(msg string) string {
	msg, _, _ = strings.Cut(strings.TrimSpace(msg), "\n")
	if r := []rune(msg); len(r) > 200 {
		msg = string(r[:200])
	}
	return strings.TrimSpace(msg)
}

// githubPushPayload is a minimal subset of the GitHub push event.
type githubPushPayload struct {
	Ref        string `json:"ref"` // e.g. "refs/heads/main"
	Repository struct {
		CloneURL string `json:"clone_url"`
		FullName string `json:"full_name"`
	} `json:"repository"`
	HeadCommit struct {
		ID      string `json:"id"`
		Message string `json:"message"`
	} `json:"head_commit"`
	Pusher struct {
		Name string `json:"name"`
	} `json:"pusher"`
}

// @Summary     GitHub push webhook
// @Description Receives a GitHub push event, validates the HMAC signature, and triggers an auto-deployment.
// @Tags        Webhooks
// @Accept      json
// @Produce     json
// @Success     202 {object} map[string]string
// @Router      /api/v1/webhooks/github [post]
func (h *Handler) handleGitHub(c *gin.Context) {
	projectID := c.Param("projectID")
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		respond.Error(c, http.StatusBadRequest, "failed to read request body")
		return
	}

	secret, err := h.secrets.Get(c.Request.Context(), projectID)
	if errors.Is(err, ErrUnknownWebhook) {
		respond.Error(c, http.StatusNotFound, "unknown webhook")
		return
	}
	if err != nil {
		h.log.Error("could not read the project's webhook secret", zap.Error(err), zap.String("project_id", projectID))
		respond.Error(c, http.StatusInternalServerError, "could not check the signature")
		return
	}
	if !verifySignature(secret, body, c.GetHeader("X-Hub-Signature-256")) {
		h.log.Warn("invalid github webhook signature")
		respond.Error(c, http.StatusUnauthorized, "invalid signature")
		return
	}

	var payload githubPushPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		respond.Error(c, http.StatusBadRequest, "invalid payload")
		return
	}

	// Only act on branch push events, not tag pushes or deletions.
	if !strings.HasPrefix(payload.Ref, "refs/heads/") {
		respond.JSON(c, http.StatusOK, gin.H{"message": "ignored (not a branch push)"})
		return
	}

	// Settings shows the last one; not recording it never stops the deploy.
	if err := h.secrets.MarkPushed(c.Request.Context(), projectID, payload.Repository.FullName); err != nil {
		h.log.Warn("could not record the push", zap.Error(err), zap.String("project_id", projectID))
	}

	branch := strings.TrimPrefix(payload.Ref, "refs/heads/")
	repoURL := payload.Repository.CloneURL
	commitSHA := payload.HeadCommit.ID

	h.log.Info("github push received",
		zap.String("repo", repoURL),
		zap.String("branch", branch),
		zap.String("commit", commitSHA),
	)

	// Fire and forget — find matching services and trigger deployments.
	go func() {
		if err := h.deploy(context.Background(), pushed{projectID: projectID, repoURL: repoURL, branch: branch, sha: commitSHA,
			triggeredBy: pushedBy(payload.Pusher.Name), message: firstLine(payload.HeadCommit.Message)}); err != nil {
			h.log.Error("failed to find matching services", zap.Error(err))
		}
	}()

	respond.JSON(c, http.StatusAccepted, gin.H{"message": "webhook accepted"})
}

// pushed is a branch a push moved: of repoURL (GitHub's webhook) or of the
// project's own repository repositoryID, which eventID records (D-77).
type pushed struct {
	projectID, repoURL, repositoryID, branch, sha, triggeredBy, message string
	eventID                                                             int64
}

// DeployPush deploys a push or a merge to one of the project's own
// repositories (code's events outbox, D-77): the services building from
// its branch with deploy on push. A second call for one event deploys
// nothing new (deployment.ErrAlreadyDeployed).
func (h *Handler) DeployPush(ctx context.Context, p code.Push) error {
	kind := "Push"
	if p.Kind == "merge" {
		kind = "Merge"
	}
	by := kind
	if p.ActorName != "" {
		by = kind + " · " + p.ActorName
	}
	return h.deploy(ctx, pushed{projectID: p.ProjectID, repositoryID: p.RepositoryID, branch: p.Branch, sha: p.SHA,
		triggeredBy: by, message: firstLine(p.Subject), eventID: p.EventID})
}

// deploy deploys projectID's services that deploy on push from the pushed
// repository and branch. Only finding them fails it: a refused deploy is
// recorded as a failed deployment.
func (h *Handler) deploy(ctx context.Context, p pushed) error {
	projectID, repoURL, branch, commitSHA, triggeredBy, commitMessage := p.projectID, p.repoURL, p.branch, p.sha, p.triggeredBy, p.message
	services, err := h.serviceSvc.FindByProjectRepoAndBranch(ctx, projectID, repoURL, p.repositoryID, branch)
	if err != nil {
		return err
	}

	if len(services) == 0 {
		h.log.Info("no services matched push event", zap.String("repo", repoURL), zap.String("repository_id", p.repositoryID), zap.String("branch", branch))
		return nil
	}

	// The workspace, so the panel's event stream reports these deploys.
	orgID := ""
	if p, err := h.projects.GetProject(ctx, projectID); err == nil {
		orgID = p.OrgID
	}

	// Pre-fetch servers and environment names, deduplicated by ID.
	// Multiple services often share the same server or environment, so this
	// collapses what would be N×2 queries down to unique-server + unique-env queries.
	serverCache := make(map[string]*serverPkg.Server)
	for _, svc := range services {
		if svc.ServerID == "" {
			continue
		}
		if _, ok := serverCache[svc.ServerID]; ok {
			continue
		}
		srv, err := h.serverSvc.GetServer(ctx, svc.ServerID)
		if err != nil {
			// Cached as nil so it is not retried. Trigger refuses the deploy
			// (ServerID set, Server nil) and records it as failed: it never
			// falls back to the control-plane server.
			h.log.Error("could not fetch server for webhook deployment", zap.Error(err), zap.String("server_id", svc.ServerID))
			serverCache[svc.ServerID] = nil
		} else {
			serverCache[svc.ServerID] = srv
		}
	}

	envNameCache := make(map[string]string)
	for _, svc := range services {
		if svc.EnvironmentID == "" {
			continue
		}
		if _, ok := envNameCache[svc.EnvironmentID]; ok {
			continue
		}
		if env, err := h.envSvc.Get(ctx, svc.EnvironmentID); err == nil {
			envNameCache[svc.EnvironmentID] = env.Name
		} else {
			envNameCache[svc.EnvironmentID] = ""
		}
	}

	for _, svc := range services {
		h.log.Info("auto-triggering deployment",
			zap.String("service_id", svc.ID),
			zap.String("service_name", svc.Name),
		)

		targetServer := serverCache[svc.ServerID] // nil for the control-plane server, or a failed fetch
		if ok, err := h.serverSvc.CanUse(ctx, svc.ServerID, orgID); targetServer != nil && (err != nil || !ok) {
			// Only servers the project's workspace owns or has a share of;
			// Trigger refuses a service whose server is missing and records
			// why.
			h.log.Error("service assigned to another workspace's server", zap.String("service_id", svc.ID), zap.String("server_id", svc.ServerID))
			targetServer = nil
		}

		// The deploy and its settings fingerprint are of the service as Get
		// reads it (D-159): the find leaves its isolation and process limit
		// out. Unread, the fingerprint never matches, which is safe.
		if full, err := h.serviceSvc.Get(ctx, svc.ID); err == nil {
			svc = *full
		}
		envVars, err := h.envVarProvider.ListAllForDeployment(ctx, svc.ID)
		if err != nil {
			h.log.Warn("could not fetch env vars for webhook deployment", zap.Error(err), zap.String("service_id", svc.ID))
			envVars = []string{}
		}
		input := h.inputFor(ctx, &svc, orgID, targetServer, envNameCache[svc.EnvironmentID])
		input.GitEventID = p.eventID
		input.CommitSHA = commitSHA
		input.TriggeredBy = triggeredBy
		input.CommitMessage = commitMessage
		input.EnvVars = envVars
		// Trigger applies the deploy policy and the D-19 stopgap: while
		// it is on, a push deploy to the control-plane server is refused
		// unless an operator granted the service the control_plane
		// exception. A refused push is recorded as a failed deployment
		// whose log says why.
		input.Origin = deployment.OriginWebhook
		if _, err := h.deploymentSvc.Trigger(ctx, input); errors.Is(err, deployment.ErrAlreadyDeployed) {
			h.log.Info("this push already deployed the service", zap.String("service_id", svc.ID), zap.Int64("event_id", p.eventID))
		} else if err != nil {
			h.log.Warn("auto-deploy refused or failed",
				zap.String("service_id", svc.ID),
				zap.Error(err),
			)
		}
	}
	return nil
}

// inputFor is what a deploy of svc runs from its configuration: the parts a
// push deploy and a preview's share.
func (h *Handler) inputFor(ctx context.Context, svc *service.Service, orgID string, srv *serverPkg.Server, envName string) deployment.TriggerInput {
	var regAuth *deployment.RegistryAuth
	if svc.RegistryUsername != "" {
		regAuth = &deployment.RegistryAuth{
			Server:   svc.RegistryServer,
			Username: svc.RegistryUsername,
			Password: svc.RegistryPassword,
		}
	}
	hostnames, certified := h.hostnamesForService(ctx, svc.ID)
	access := svc.ToAccessControl()
	access.Certified = certified
	return deployment.TriggerInput{
		ServiceID:           svc.ID,
		OrgID:               orgID,
		ProjectID:           svc.ProjectID,
		ServerID:            svc.ServerID,
		Server:              srv,
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
		Domains:             hostnames,
		Port:                svc.Port,
		CPULimit:            svc.CPULimit,
		MemoryLimit:         svc.MemoryLimit,
		Volumes:             svc.Volumes,
		NetworkAlias:        svc.NetworkAlias,
		StartCommand:        svc.StartCommand,
		BuildPack:           svc.BuildPack,
		Nixpacks:            deployment.NixpacksCommands{Install: svc.InstallCommand, Build: svc.BuildCommand},
		HealthCheckPath:     svc.HealthCheckPath,
		HealthCheckTimeout:  svc.HealthCheckTimeout,
		HealthCheckRetries:  svc.HealthCheckRetries,
		GitUsername:         svc.GitUsername,
		GitToken:            svc.GitToken,
		PreDeployCommands:   deployment.SplitLines(svc.PreDeployCommands),
		PostDeployCommands:  deployment.ParsePostDeployCommands(svc.PostDeployCommands),
		RequiredEnvVars:     deployment.SplitLines(svc.RequiredEnvVars),
		ServiceName:         svc.Name,
		EnvironmentName:     envName,
		MigrateCommand:      svc.MigrateCommand,
		AccessControl:       access,
		Recreate:            svc.Recreates(),
		Settings:            svc,
	}
}

// PreviewDeploy is one of a preview's services to deploy at sha (D-97):
// with the variables the preview resolved and its certificate's names.
type PreviewDeploy struct {
	ServiceID, SHA, TriggeredBy, Message, ActorID string
	EventID                                       int64
	EnvVars                                       []string
	CertDomains                                   []string
	CertResolver                                  string
}

// DeployPreview deploys one of a preview's services at exactly its commit.
// A refused one is recorded as a failed deployment (its check says why);
// a second call for one event deploys nothing new.
func (h *Handler) DeployPreview(ctx context.Context, p PreviewDeploy) error {
	svc, err := h.serviceSvc.Get(ctx, p.ServiceID)
	if err != nil {
		return fmt.Errorf("get service: %w", err)
	}
	orgID := ""
	if pr, err := h.projects.GetProject(ctx, svc.ProjectID); err == nil {
		orgID = pr.OrgID
	}
	var srv *serverPkg.Server
	if svc.ServerID != "" {
		if s, err := h.serverSvc.GetServer(ctx, svc.ServerID); err == nil {
			srv = s
		}
	}
	envName := ""
	if env, err := h.envSvc.Get(ctx, svc.EnvironmentID); err == nil {
		envName = env.Name
	}
	input := h.inputFor(ctx, svc, orgID, srv, envName)
	input.GitEventID = p.EventID
	input.CommitSHA, input.CheckoutSHA = p.SHA, p.SHA
	input.TriggeredBy, input.CommitMessage = p.TriggeredBy, firstLine(p.Message)
	input.EnvVars = p.EnvVars
	input.AccessControl.TLSDomains, input.AccessControl.TLSResolver = p.CertDomains, p.CertResolver
	input.Origin, input.ActorID, input.Preview = deployment.OriginPreview, p.ActorID, true
	if _, err := h.deploymentSvc.Trigger(ctx, input); err != nil && !errors.Is(err, deployment.ErrAlreadyDeployed) {
		return err
	}
	return nil
}

// certificateReason is what the redeploy for a certificate records as why.
const certificateReason = "certificate issued: switch to HTTPS"

// RedeployForCertificate redeploys a running service on the image it runs
// now (domain.Redeployer, D-150): its routes and TOFT_URL were set while a
// temporary name of its served plain HTTP. It is a rollback to the live
// deployment, by Toft. In the background, so the check that saw the
// certificate isn't held up: the goroutine ends when Trigger has started
// the deploy, or refused it.
func (h *Handler) RedeployForCertificate(ctx context.Context, serviceID string) {
	ctx = context.WithoutCancel(ctx)
	go func() {
		input, skip := h.certificateRedeploy(ctx, serviceID)
		if input == nil {
			h.log.Info("no redeploy for a certificate", zap.String("service_id", serviceID), zap.String("reason", skip))
			return
		}
		if _, err := h.deploymentSvc.Trigger(ctx, *input); err != nil {
			h.log.Warn("redeploy for a certificate refused", zap.String("service_id", serviceID), zap.Error(err))
			return
		}
		if h.audit != nil {
			// "toft" is org.ToftActor: the entry lists as Toft.
			h.audit.WriteAuditLogDetail(ctx, input.OrgID, "toft", "deployment.triggered", "service:"+serviceID,
				map[string]any{"reason": certificateReason})
		}
	}()
}

// pinnedStack is why input's inline compose stack can't be brought up again
// without its images changing ("" when it can): compose up pulls every
// image, so each must be pinned by digest. Loaded as the deploy loads it,
// with the TOFT_URL the deploy adds.
func pinnedStack(ctx context.Context, input deployment.TriggerInput) string {
	env := make(map[string]string, len(input.EnvVars)+1)
	for _, kv := range input.EnvVars {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	if _, ok := env["TOFT_URL"]; !ok {
		if u := deploypolicy.ServiceURL(input.Domains, input.AccessControl.Certified); u != "" {
			env["TOFT_URL"] = u
		}
	}
	p, violations, err := composespec.Load(ctx, deploypolicy.InlineComposeSource(input.ServiceID, input.ServiceName, input.ComposeContent, env))
	if err != nil || len(violations) > 0 || p == nil {
		return "its compose file doesn't load"
	}
	if names := composespec.NotPinned(p); len(names) > 0 {
		return "a compose stack with images not pinned by digest: " + strings.Join(names, ", ")
	}
	return ""
}

// certificateRedeploy is the redeploy RedeployForCertificate runs, or nil
// and why there is none: the service isn't running a live deployment, a
// deploy is under way (it routes the name over HTTPS already), it is a
// preview's copy, its image is gone, a compose stack whose up could pull
// other images (from a repository, or an image not pinned by digest), or
// its server can't take a deploy now. Nor when a person has to decide
// (D-159): a protected environment, whose deploys a person confirms, or
// settings saved since the live deployment, which only a person's deploy
// ships; the redeploy is the live deployment's exact settings with only its
// routing changed. The next deploy routes the name over HTTPS in every one
// of those cases, and Needs attention asks for it.
func (h *Handler) certificateRedeploy(ctx context.Context, serviceID string) (*deployment.TriggerInput, string) {
	svc, err := h.serviceSvc.Get(ctx, serviceID)
	if err != nil {
		return nil, "the service: " + err.Error()
	}
	compose := svc.SourceType == service.SourceTypeDockerCompose
	switch {
	case svc.RuntimeStatus != "running" || svc.ActiveDeploymentID == "":
		return nil, "not running"
	case svc.Deploying:
		return nil, "a deploy is under way"
	case compose && (svc.ComposeRepoURL != "" || svc.RepositoryID != ""):
		return nil, "a compose stack from a repository: its file isn't stored"
	}
	if isCopy, err := h.deploymentSvc.IsPreviewCopy(ctx, serviceID); err != nil || isCopy {
		return nil, "a preview's copy"
	}
	live, err := h.deploymentSvc.GetDeploymentForService(ctx, serviceID, svc.ActiveDeploymentID)
	if err != nil || !compose && (live.ImageTag == "" || live.ImageRemoved) {
		return nil, "no image to run again"
	}
	orgID := svc.ScopedOrgID
	if svc.ProjectID != "" {
		if pr, err := h.projects.GetProject(ctx, svc.ProjectID); err == nil {
			orgID = pr.OrgID
		}
	}
	var srv *serverPkg.Server
	if svc.ServerID != "" {
		if srv, err = h.serverSvc.GetServer(ctx, svc.ServerID); err != nil {
			return nil, "its server: " + err.Error()
		}
		if srv.WaitingForAgent() || srv.AgentFrozenAt != nil {
			return nil, "its server is not ready or frozen"
		}
	}
	if err := h.deploymentSvc.AuthorizeTarget(ctx, deployment.OriginSystem, "", serviceID, srv); err != nil {
		return nil, err.Error()
	}
	envName := ""
	if svc.EnvironmentID != "" {
		env, err := h.envSvc.Get(ctx, svc.EnvironmentID)
		if err != nil {
			return nil, "its environment: " + err.Error()
		}
		if env.Protected {
			return nil, "a protected environment: a person confirms its deploys"
		}
		envName = env.Name
	}
	envVars, err := h.envVarProvider.ListAllForDeployment(ctx, serviceID)
	if err != nil {
		return nil, "its variables: " + err.Error()
	}
	if same, err := h.deploymentSvc.SettingsDeployed(ctx, live, orgID, envVars); err != nil {
		return nil, "its settings: " + err.Error()
	} else if !same {
		return nil, "settings saved since its live deployment, or not recorded with it"
	}
	input := h.inputFor(ctx, svc, orgID, srv, envName)
	input.EnvVars = envVars
	if compose {
		// As a compose rollback of an inline stack: its file again, whose
		// up pulls only what its digests already name.
		if why := pinnedStack(ctx, input); why != "" {
			return nil, why
		}
	} else {
		// As a rollback to the live deployment: its image, no build, no
		// pull, no pre-deploy commands.
		input.RollbackImageTag = live.ImageTag
		input.PreDeployCommands = nil
	}
	input.CommitSHA, input.CommitMessage = live.CommitSHA, live.CommitMessage
	input.Origin = deployment.OriginSystem
	input.TriggeredBy = "Toft · " + certificateReason
	return &input, ""
}

// hostnamesForService returns the domain hostnames attached to a service,
// and those a certificate has answered for (service.AccessControl.Certified).
func (h *Handler) hostnamesForService(ctx context.Context, serviceID string) ([]string, []string) {
	domains, err := h.domainSvc.List(ctx, serviceID)
	if err != nil {
		h.log.Warn("could not fetch domains for webhook deployment", zap.Error(err), zap.String("service_id", serviceID))
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

// verifySignature validates the X-Hub-Signature-256 header sent by GitHub.
func verifySignature(secret string, body []byte, signatureHeader string) bool {
	if secret == "" {
		// Fail closed: without a secret anyone could trigger deploys.
		return false
	}
	const prefix = "sha256="
	if !strings.HasPrefix(signatureHeader, prefix) {
		return false
	}
	expected := strings.TrimPrefix(signatureHeader, prefix)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	computed := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(computed), []byte(expected))
}

// ─── The project's webhook, for its owners and developers ─────────────────────

type infoResponse struct {
	URL          string     `json:"url"`
	Secret       string     `json:"secret"`
	LastPushAt   *time.Time `json:"last_push_at"`
	LastPushRepo string     `json:"last_push_repo"`
}

// info is the URL and secret to give GitHub, with the last push it sent.
func (h *Handler) info(c *gin.Context, projectID, secret string) {
	last, err := h.secrets.LastPush(c.Request.Context(), projectID)
	if err != nil {
		h.log.Error("could not read the project's last push", zap.Error(err), zap.String("project_id", projectID))
		respond.Error(c, http.StatusInternalServerError, "failed to load the webhook")
		return
	}
	respond.JSON(c, http.StatusOK, infoResponse{URL: webhookURL(c, projectID), Secret: secret, LastPushAt: last.At, LastPushRepo: last.Repo})
}

// writable checks that the caller may see or change the project's webhook:
// the project is in their workspace and they are not a viewer (the secret
// lets anyone who has it deploy). On failure it writes the 403.
func (h *Handler) writable(c *gin.Context) (string, bool) {
	projectID := c.Param("projectID")
	if middleware.MemberRoleFrom(c) == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return "", false
	}
	if owned, err := h.projects.CheckOwnership(c.Request.Context(), projectID, middleware.OrgIDFrom(c)); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "project not found")
		return "", false
	}
	return projectID, true
}

// webhookURL is the project's delivery URL as the caller reached the API.
// Behind Traefik the request is plain HTTP, so X-Forwarded-Proto says what
// the caller used.
func webhookURL(c *gin.Context, projectID string) string {
	scheme := "http"
	if proto := c.GetHeader("X-Forwarded-Proto"); proto == "https" || proto == "http" {
		scheme = proto
	} else if c.Request.TLS != nil {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/api/v1/webhooks/github/%s", scheme, c.Request.Host, projectID)
}

// GET /projects/:projectID/webhook: the URL and secret to give GitHub,
// created the first time.
func (h *Handler) handleInfo(c *gin.Context) {
	projectID, ok := h.writable(c)
	if !ok {
		return
	}
	secret, err := h.secrets.Ensure(c.Request.Context(), projectID)
	if err != nil {
		h.log.Error("could not create the project's webhook secret", zap.Error(err), zap.String("project_id", projectID))
		respond.Error(c, http.StatusInternalServerError, "failed to load the webhook")
		return
	}
	h.info(c, projectID, secret)
}

// POST /projects/:projectID/webhook/rotate: a new secret; GitHub needs it
// before the next push deploys.
func (h *Handler) handleRotate(c *gin.Context) {
	projectID, ok := h.writable(c)
	if !ok {
		return
	}
	secret, err := h.secrets.Rotate(c.Request.Context(), projectID)
	if err != nil {
		h.log.Error("could not rotate the project's webhook secret", zap.Error(err), zap.String("project_id", projectID))
		respond.Error(c, http.StatusInternalServerError, "failed to rotate the webhook secret")
		return
	}
	h.info(c, projectID, secret)
}
