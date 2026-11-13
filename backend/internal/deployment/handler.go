package deployment

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/internal/composespec"
	serverPkg "github.com/Nickpap21/toft/backend/internal/server"
	serviceManager "github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/pkg/actor"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

// OrgAuditWriter is satisfied by org.Service; defined here to avoid a circular import.
type OrgAuditWriter interface {
	WriteAuditLog(ctx context.Context, orgID, actorID, action, resource string)
}

// VersionCacheInvalidator is satisfied by version.Service; defined here to avoid a circular import.
type VersionCacheInvalidator interface {
	InvalidateCache(serviceID string)
}

// UserNamer gives a user's display name, for a deployment's "triggered by".
type UserNamer interface {
	UserName(ctx context.Context, userID string) (string, error)
}

// ServiceManagerOps is the subset of *service.ServiceManager the deploy routes
// and their use case (Service.Deploy) call —
// ownership checks, service lookups, dependency checks, and version-override writes.
// A narrow interface here keeps them from depending on ServiceManager's full surface.
type ServiceManagerOps interface {
	CheckOwnership(ctx context.Context, id, orgID string) (bool, error)
	Get(ctx context.Context, id string) (*serviceManager.Service, error)
	CheckDependencies(ctx context.Context, svc *serviceManager.Service) error
	UpdateDockerImage(ctx context.Context, id, image string) (clearedRegistryPassword bool, err error)
	PutBackDockerImage(ctx context.Context, id, from, image, registryPassword string) error
	UpdateGitBranch(ctx context.Context, id, branch string) error
	CheckMove(ctx context.Context, serviceID string) error
}

// EnvVarSetter creates or updates one plain env var of a service, saying
// what of the service's own it replaced; Swap changes the service's own
// only while it is still from, never creating it; Unset deletes the
// service's own only while it is still value.
// Satisfied by envvar.Service.
type EnvVarSetter interface {
	Set(ctx context.Context, serviceID, key, value string) (prev string, had bool, err error)
	Swap(ctx context.Context, serviceID, key, from, value string) error
	Unset(ctx context.Context, serviceID, key, value string) error
}

// Handler is a thin HTTP adapter for the deployment domain.
type Handler struct {
	service        *Service
	serviceManager ServiceManagerOps
	log            *zap.Logger
}

func NewHandler(service *Service, sm ServiceManagerOps, log *zap.Logger) *Handler {
	return &Handler{
		service:        service,
		serviceManager: sm,
		log:            log,
	}
}

// RegisterRoutes mounts deployment routes under /services/:serviceID.
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	// 15 deploys per service per 10 minutes (burst of 15).
	deployRL := middleware.KeyedRateLimiter(
		rate.Every(2*time.Minute), 15,
		func(c *gin.Context) string { return "deploy:" + c.Param("serviceID") },
	)

	services := rg.Group("/services/:serviceID")
	{
		services.POST("/deployments", deployRL, h.trigger)
		services.GET("/deployments", h.list)
		services.GET("/deployments/:deploymentID", h.get)
		services.GET("/deployments/:deploymentID/logs", h.logs)
		services.GET("/deployments/:deploymentID/logs/stream", h.logsStream)
		services.POST("/deployments/:deploymentID/rollback", deployRL, h.rollback)
	}
}

// answer is how the route answers a deploy that did not start.
type answer struct {
	status int
	msg    string
	data   any
}

func callerOf(c *gin.Context) serviceManager.DeployCaller {
	return serviceManager.DeployCaller{OrgID: middleware.OrgIDFrom(c), UserID: middleware.UserIDFrom(c),
		Role: middleware.MemberRoleFrom(c), ViaAPIKey: middleware.ViaAPIKey(c)}
}

func (h *Handler) trigger(c *gin.Context) {
	var body DeployOverrides
	_ = c.ShouldBindJSON(&body)
	d, err := h.service.Deploy(c.Request.Context(), callerOf(c), c.Param("serviceID"), body, c.Query("force") == "true", false)
	if err != nil {
		h.writeTriggerError(c, err)
		return
	}
	respond.JSON(c, http.StatusAccepted, d)
}

// DeployCreated starts a new service's first deploy for its create
// (service.FirstDeployer): the deploy route's use case with no body. Its
// error is the message and the code that route would answer with.
func (h *Handler) DeployCreated(ctx context.Context, who serviceManager.DeployCaller, serviceID string, force bool) (any, error) {
	d, err := h.service.Deploy(ctx, who, serviceID, DeployOverrides{}, force, true)
	if err != nil {
		var ge *actor.GateError
		if errors.As(err, &ge) {
			return nil, &serviceManager.DeployRefused{Message: cmp.Or(ge.Message, ge.Code), Code: ge.Code}
		}
		p := h.problem(err)
		return nil, &serviceManager.DeployRefused{Message: p.msg, Code: codeOf(p.data)}
	}
	return d, nil
}

// codeOf is the code a refusal's data carries, "" when it has none.
func codeOf(data any) string {
	switch d := data.(type) {
	case gin.H:
		code, _ := d["code"].(string)
		return code
	case *serverPkg.CapacityExceededError:
		return d.Code
	case composespec.ViolationsBody:
		return d.Code
	}
	return ""
}

func (h *Handler) rollback(c *gin.Context) {
	// A rollback an approval covered (D-146) that fails before it runs gives
	// the approval's one rollback back.
	ctx, claims := actor.WithClaims(c.Request.Context())
	c.Request = c.Request.WithContext(ctx)
	defer func() {
		if claims.Rollback != "" && c.Writer.Status() >= http.StatusBadRequest {
			h.service.ReleaseAgentRollback(context.WithoutCancel(ctx), claims.Rollback)
		}
	}()
	orgID := middleware.OrgIDFrom(c)
	userID := middleware.UserIDFrom(c)
	serviceID := c.Param("serviceID")
	deploymentID := c.Param("deploymentID")

	if owned, err := h.serviceManager.CheckOwnership(c.Request.Context(), serviceID, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}
	// Viewers read; they don't roll back (the panel hides the button, the API refuses).
	if middleware.MemberRoleFrom(c) == "viewer" {
		respond.Error(c, http.StatusForbidden, "viewers cannot roll back")
		return
	}

	svc, err := h.serviceManager.Get(c.Request.Context(), serviceID)
	if err != nil {
		h.log.Error("failed to fetch service for rollback", zap.Error(err), zap.String("service_id", serviceID))
		respond.Error(c, http.StatusInternalServerError, "failed to load service configuration")
		return
	}
	if err := h.service.previewCopy(c.Request.Context(), serviceID); err != nil {
		h.writeTriggerError(c, err)
		return
	}
	if err := h.service.agentGate(c.Request.Context(), serviceID, c.Param("deploymentID")); err != nil {
		h.writeTriggerError(c, err)
		return
	}
	// The deployment must be this service's own, and have something to go
	// back to: a built image, or for a compose service the IMAGE_TAG it ran
	// with and/or the commit its repository was checked out at.
	oldDep, err := h.service.GetDeploymentForService(c.Request.Context(), serviceID, deploymentID)
	if err != nil {
		h.writeTriggerError(c, err)
		return
	}
	isCompose := svc.SourceType == serviceManager.SourceTypeDockerCompose
	rollbackTag, rollbackCommit := oldDep.ImageTag, ""
	if isCompose {
		if !imageTagPattern.MatchString(rollbackTag) {
			rollbackTag = ""
		}
		rollbackCommit = composeRollbackCommit(svc, oldDep)
		if rollbackTag == "" && rollbackCommit == "" {
			respond.Error(c, http.StatusBadRequest, "deployment has no IMAGE_TAG or repository commit to roll back to")
			return
		}
	} else if rollbackTag == "" {
		respond.Error(c, http.StatusBadRequest, "deployment has no built image to roll back to")
		return
	}
	if oldDep.ImageRemoved {
		respond.ErrorData(c, http.StatusConflict, "this deployment's image was removed to free disk space: deploy its commit again to run it", gin.H{"code": "image_removed"})
		return
	}

	targetServer, err := h.service.targetServer(c.Request.Context(), orgID, svc)
	if err != nil {
		h.writeTriggerError(c, err)
		return
	}

	hostnames, certified := h.service.hostnamesForService(c.Request.Context(), serviceID)
	access := svc.ToAccessControl()
	access.Certified = certified

	envVars, err := h.service.envVarProvider.ListAllForDeployment(c.Request.Context(), serviceID)
	if err != nil {
		h.log.Warn("could not fetch env vars for rollback", zap.Error(err), zap.String("service_id", serviceID))
		envVars = []string{}
	}

	envName := ""
	if svc.EnvironmentID != "" {
		if env, err := h.service.envSvc.Get(c.Request.Context(), svc.EnvironmentID); err == nil {
			envName = env.Name
		}
	}

	// The use case passes it on only with an image on the service's own
	// registry (rolloutAuth).
	var regAuth *RegistryAuth
	if svc.RegistryPassword != "" {
		regAuth = &RegistryAuth{
			Server:   svc.RegistryServer,
			Username: svc.RegistryUsername,
			Password: svc.RegistryPassword,
		}
	}

	kind := rollbackKind(svc, oldDep.ID)
	in := TriggerInput{
		ServiceID:           serviceID,
		OrgID:               orgID,
		ProjectID:           svc.ProjectID,
		ServerID:            svc.ServerID,
		Server:              targetServer,
		SourceType:          string(svc.SourceType),
		ComposeRouteService: svc.ComposeRouteService,
		RollbackImageTag:    oldDep.ImageTag,
		CommitSHA:           oldDep.CommitSHA, // it runs what that deployment ran
		CommitMessage:       oldDep.CommitMessage,
		RegistryAuth:        regAuth,
		Domains:             hostnames,
		EnvVars:             envVars,
		Port:                svc.Port,
		CPULimit:            svc.CPULimit,
		MemoryLimit:         svc.MemoryLimit,
		Volumes:             svc.Volumes,
		NetworkAlias:        svc.NetworkAlias,
		StartCommand:        svc.StartCommand,
		HealthCheckPath:     svc.HealthCheckPath,
		HealthCheckTimeout:  svc.HealthCheckTimeout,
		HealthCheckRetries:  svc.HealthCheckRetries,
		PostDeployCommands:  ParsePostDeployCommands(svc.PostDeployCommands),
		RequiredEnvVars:     SplitLines(svc.RequiredEnvVars),
		ServiceName:         svc.Name,
		EnvironmentName:     envName,
		MigrateCommand:      svc.MigrateCommand,
		AccessControl:       access,
		Recreate:            svc.Recreates(),
		Origin:              OriginInteractive,
		ActorID:             userID,
		TriggeredBy:         h.service.triggeredBy(c.Request.Context(), callerOf(c), kind),
		Settings:            svc,
	}
	savedTag := "" // the service's IMAGE_TAG the rollback replaced
	if isCompose {
		// The stack goes back to the deployment's images (IMAGE_TAG) and, for
		// a repository stack, to its compose files at that commit. The
		// service's own IMAGE_TAG becomes the rollback's, so Deploy runs it
		// next; one only the environment sets is left as it is (Swap never
		// creates one). A rollback that fails puts it back (followLive).
		in.RollbackImageTag = ""
		if rollbackTag != "" {
			if t := kvsToMap(envVars)["IMAGE_TAG"]; t != "" && t != rollbackTag {
				if err := h.service.envVarSetter.Swap(c.Request.Context(), serviceID, "IMAGE_TAG", t, rollbackTag); err != nil {
					h.log.Warn("failed to save the rollback's IMAGE_TAG", zap.Error(err), zap.String("service_id", serviceID))
				} else {
					savedTag = t
				}
			}
			in.EnvVars = withEnv(envVars, "IMAGE_TAG", rollbackTag)
		}
		in.CheckoutSHA = rollbackCommit
		in.ComposeContent = svc.ComposeContent
		in.ComposeRepoURL = svc.ComposeRepoURL
		in.RepositoryID = svc.RepositoryID
		in.ComposeFilePath = svc.ComposeFilePath
		in.GitBranch = svc.GitBranch
		in.GitUsername = svc.GitUsername
		in.GitToken = svc.GitToken
		in.PreDeployCommands = SplitLines(svc.PreDeployCommands)
	} else {
		// Not built again, but what the deploy policy pins a git
		// service's exception to (K3).
		in.GitRepoURL = svc.GitRepoURL
		in.RepositoryID = svc.RepositoryID
	}
	d, err := h.service.Trigger(c.Request.Context(), in)
	if err != nil {
		if savedTag != "" {
			if serr := h.service.envVarSetter.Swap(context.WithoutCancel(c.Request.Context()), serviceID, "IMAGE_TAG", rollbackTag, savedTag); serr != nil {
				h.log.Warn("failed to put the previous IMAGE_TAG back", zap.Error(serr), zap.String("service_id", serviceID))
			}
		}
		h.writeTriggerError(c, err)
		return
	}

	// deployment.rollback, or deployment.redeploy to the live one.
	h.service.orgSvc.WriteAuditLog(c.Request.Context(), orgID, userID, "deployment."+strings.ToLower(kind), "deployment:"+deploymentID)
	respond.JSON(c, http.StatusAccepted, d)
}

// writeTriggerError maps Trigger's refusals: the deploy policy (422 with
// the violations), the control-plane stopgap (403), an unknown deployment
// (404), a compose rollback, the panel host under Control plane only or a
// host updating Docker or rebooting (409), a server that could not be loaded (503),
// anything else (500).
func (h *Handler) writeTriggerError(c *gin.Context, err error) {
	if respond.Gate(c, err) {
		return
	}
	p := h.problem(err)
	respond.ErrorData(c, p.status, p.msg, p.data)
}

// problem is how the route answers a deploy that did not start, but for
// the agent policy's answer (respond.Gate).
func (h *Handler) problem(err error) *answer {
	var r *refusal
	var capErr *serverPkg.CapacityExceededError
	var pv *composespec.ViolationsError
	switch {
	case errors.As(err, &r):
		return &answer{refusalStatus(r.kind), r.msg, nil}
	case errors.Is(err, ErrPreviewService):
		return &answer{http.StatusConflict, err.Error(), gin.H{"code": "preview_service"}}
	case errors.As(err, &capErr):
		return &answer{http.StatusConflict, capErr.Error(), capErr}
	case errors.As(err, &pv):
		return &answer{http.StatusUnprocessableEntity, pv.Error(), pv.Body()}
	case errors.Is(err, ErrControlPlaneOperatorOnly), errors.Is(err, ErrControlPlaneWebhook):
		return &answer{http.StatusForbidden, err.Error(), nil}
	case errors.Is(err, ErrDeploymentNotFound):
		return &answer{http.StatusNotFound, err.Error(), nil}
	case errors.Is(err, ErrWildcardNeedsDNS), errors.Is(err, ErrPanelHostNoApps):
		return &answer{http.StatusConflict, err.Error(), nil}
	case errors.Is(err, ErrSandboxUnavailable):
		return &answer{http.StatusConflict, err.Error(), gin.H{"code": "sandbox_unavailable"}}
	case errors.Is(err, serverPkg.ErrServerFrozen):
		return &answer{http.StatusConflict, err.Error(), gin.H{"code": "server_frozen"}}
	case errors.Is(err, serverPkg.ErrServerNotReady):
		return &answer{http.StatusConflict, err.Error(), gin.H{"code": "server_not_ready"}}
	case errors.Is(err, ErrServerUpdating):
		return &answer{http.StatusConflict, err.Error(), gin.H{"code": "server_updating"}}
	case errors.Is(err, ErrDiskCleaning):
		return &answer{http.StatusConflict, err.Error(), gin.H{"code": "disk_clean_running"}}
	case errors.Is(err, ErrServiceMoving):
		return &answer{http.StatusConflict, err.Error(), gin.H{"code": "service_moving"}}
	case errors.Is(err, ErrRollbackUnsupported):
		return &answer{http.StatusConflict, err.Error(), nil}
	case errors.Is(err, ErrServerUnavailable):
		return &answer{http.StatusServiceUnavailable, err.Error(), nil}
	default:
		h.log.Error("trigger deployment", zap.Error(err))
		return &answer{http.StatusInternalServerError, "failed to trigger deployment", nil}
	}
}

// refusalStatus is the status the use case's refusal answers with.
func refusalStatus(k refusalKind) int {
	switch k {
	case refusedNotFound:
		return http.StatusNotFound
	case refusedForbidden:
		return http.StatusForbidden
	case refusedInvalid:
		return http.StatusBadRequest
	case refusedConflict:
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func (h *Handler) list(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	serviceID := c.Param("serviceID")

	if owned, err := h.serviceManager.CheckOwnership(c.Request.Context(), serviceID, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}

	// The page convention (D-59): ?limit (50 by default, 200 at most) and
	// ?cursor from the previous page's next.
	limit, _ := strconv.Atoi(c.Query("limit"))
	switch {
	case limit <= 0:
		limit = 50
	case limit > 200:
		limit = 200
	}
	at, beforeID, err := respond.ParseCursor(c.Query("cursor"))
	if err != nil {
		respond.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	var before *time.Time
	if beforeID != "" {
		before = &at
	}

	deployments, err := h.service.ListDeploymentsPaginated(c.Request.Context(), serviceID, int32(limit), before, beforeID)
	if err != nil {
		respond.Error(c, http.StatusInternalServerError, "failed to list deployments")
		return
	}
	var next string
	if n := len(deployments); n > 0 && n == limit {
		next = respond.Cursor(deployments[n-1].CreatedAt, deployments[n-1].ID)
	}
	respond.JSONPage(c, http.StatusOK, deployments, next)
}

func (h *Handler) get(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	serviceID := c.Param("serviceID")

	if owned, err := h.serviceManager.CheckOwnership(c.Request.Context(), serviceID, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}

	dep, err := h.service.GetDeploymentForService(c.Request.Context(), serviceID, c.Param("deploymentID"))
	if err != nil {
		respond.Error(c, http.StatusNotFound, ErrDeploymentNotFound.Error())
		return
	}
	respond.JSON(c, http.StatusOK, dep)
}

func (h *Handler) logs(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	serviceID := c.Param("serviceID")

	if owned, err := h.serviceManager.CheckOwnership(c.Request.Context(), serviceID, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}

	if _, err := h.service.GetDeploymentForService(c.Request.Context(), serviceID, c.Param("deploymentID")); err != nil {
		respond.Error(c, http.StatusNotFound, ErrDeploymentNotFound.Error())
		return
	}

	search := c.Query("search")
	logs, err := h.service.GetLogsFiltered(c.Request.Context(), c.Param("deploymentID"), search)
	if err != nil {
		respond.Error(c, http.StatusInternalServerError, "failed to fetch logs")
		return
	}
	respond.JSON(c, http.StatusOK, logs)
}

func (h *Handler) logsStream(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	serviceID := c.Param("serviceID")

	if owned, err := h.serviceManager.CheckOwnership(c.Request.Context(), serviceID, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}

	deploymentID := c.Param("deploymentID")
	if _, err := h.service.GetDeploymentForService(c.Request.Context(), serviceID, deploymentID); err != nil {
		respond.Error(c, http.StatusNotFound, ErrDeploymentNotFound.Error())
		return
	}

	respond.Stream(c)
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Transfer-Encoding", "chunked")

	clientGone := c.Request.Context().Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var lastLogID int64

	for {
		select {
		case <-clientGone:
			return
		case <-ticker.C:
			newLogs, err := h.service.GetLogsAfter(c.Request.Context(), deploymentID, lastLogID)
			if err != nil {
				continue
			}
			for _, entry := range newLogs {
				lastLogID = entry.ID
				event, err := logEvent(entry)
				if err != nil {
					h.log.Warn("skipping a log line that cannot be encoded", zap.Error(err), zap.Int64("log_id", entry.ID))
					continue
				}
				_, _ = c.Writer.WriteString(event)
				c.Writer.Flush()
			}

			dep, err := h.service.GetDeployment(c.Request.Context(), deploymentID)
			if err == nil {
				switch dep.Status {
				case StatusRunning, StatusFailed, StatusStopped:
					runtimeStatus := ""
					if svc, svcErr := h.serviceManager.Get(c.Request.Context(), serviceID); svcErr == nil {
						runtimeStatus = string(svc.RuntimeStatus)
					}
					doneData := fmt.Sprintf(`{"status":%q,"runtime_status":%q}`, string(dep.Status), runtimeStatus)
					_, _ = c.Writer.WriteString("event: done\ndata: " + doneData + "\n\n")
					c.Writer.Flush()
					return
				}
			}
		}
	}
}

// logEvent is one log line as an SSE event: its data is the Log as JSON, the
// shape GET .../logs returns. fmt's %q is not JSON: it writes control
// characters, such as ANSI colour codes, as \x1b.
func logEvent(entry Log) (string, error) {
	data, err := json.Marshal(entry)
	if err != nil {
		return "", fmt.Errorf("encode log %d: %w", entry.ID, err)
	}
	return fmt.Sprintf("id: %d\ndata: %s\n\n", entry.ID, data), nil
}

// imageTagPattern is Docker's tag grammar.
var imageTagPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// isFullSHA reports whether s is a full 40-character hex commit SHA.
func isFullSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

// composeCheckout returns the commit a deploy of svc checks its repository
// out at: sha, for a compose service deployed from a repository and for a
// service building from the project's own repository (D-77); "" for
// everything else.
func composeCheckout(svc *serviceManager.Service, sha string) string {
	if svc.RepositoryID != "" {
		return sha
	}
	if svc.SourceType != serviceManager.SourceTypeDockerCompose || svc.ComposeRepoURL == "" {
		return ""
	}
	return sha
}

// rollbackKind names a rollback in triggered_by: to the live deployment it
// runs the same version with today's settings, a Redeploy (7g step 8).
func rollbackKind(svc *serviceManager.Service, deploymentID string) string {
	if deploymentID == svc.ActiveDeploymentID {
		return "Redeploy"
	}
	return "Rollback"
}

// composeRollbackCommit returns the commit a rollback of compose service svc
// to dep checks its repository out at: the one dep recorded checking out,
// or "" (the branch tip) when it recorded none. Never dep.CommitSHA: for a
// push to git_repo_url that is a commit of another repository.
func composeRollbackCommit(svc *serviceManager.Service, dep *Deployment) string {
	sha := composeCheckout(svc, dep.ComposeCommitSHA)
	if !isFullSHA(sha) {
		return ""
	}
	return sha
}

// withEnv returns envs (KEY=VALUE pairs) with key set to value, replacing
// any earlier value.
func withEnv(envs []string, key, value string) []string {
	out := make([]string, 0, len(envs)+1)
	for _, kv := range envs {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return append(out, key+"="+value)
}
