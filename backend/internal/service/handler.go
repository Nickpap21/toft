package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/envvar"
	"github.com/Nickpap21/toft/backend/internal/project"
	serverPkg "github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"go.uber.org/zap"
)

// OrgAuditWriter is satisfied by org.Service; defined here to avoid a circular import.
type OrgAuditWriter interface {
	WriteAuditLog(ctx context.Context, orgID, actorID, action, resource string)
	WriteAuditLogDetail(ctx context.Context, orgID, actorID, action, resource string, detail map[string]any)
}

// VersionCacheInvalidator is satisfied by version.Service; defined here to avoid a circular import.
type VersionCacheInvalidator interface {
	InvalidateCache(serviceID string)
}

// EnvironmentOwnershipChecker verifies that an environment belongs to the requesting org
// and can resolve the environment name. Satisfied by environment.Service — defined here
// to avoid a circular import.
type EnvironmentOwnershipChecker interface {
	CheckOwnership(ctx context.Context, id, orgID string) (bool, error)
	GetName(ctx context.Context, id string) (string, error)
}

type Handler struct {
	svc          *ServiceManager
	projectSvc   *project.Service
	envChecker   EnvironmentOwnershipChecker
	orgSvc       OrgAuditWriter
	versionCache VersionCacheInvalidator
	servers      ServerOwnershipChecker
	health       HealthAnswers
	deploys      FirstDeployer
	log          *zap.Logger
}

// DeployCaller is who a create's first deploy is started for: the create's
// caller, whom its route refused if a viewer.
type DeployCaller struct {
	OrgID, UserID, Role string
	ViaAPIKey           bool
}

// FirstDeployer starts a new service's first deploy behind every gate of
// POST /services/{serviceID}/deployments and returns the deployment as that
// route answers it (any: deployment imports this package). Its error is a
// *DeployRefused. Satisfied by *deployment.Handler.
type FirstDeployer interface {
	DeployCreated(ctx context.Context, who DeployCaller, serviceID string, force bool) (any, error)
}

// DeployRefused is why a create's first deploy did not start: the message
// the deploy route answers with, and its problem's code when it has one.
type DeployRefused struct{ Message, Code string }

func (e *DeployRefused) Error() string { return e.Message }

// WithFirstDeploys lets a create that asks for it (CreateInput.Deploy)
// start the service's first deploy. Called from main.go.
func (h *Handler) WithFirstDeploys(d FirstDeployer) { h.deploys = d }

// firstDeploy starts the first deploy of the service a create just stored,
// when the create asked for it, and puts it in the create's answer, or why
// it did not start: the service stays created either way.
func (h *Handler) firstDeploy(c *gin.Context, svc *SavedService, in CreateInput) {
	if !in.Deploy {
		return
	}
	// A panel tab loaded before 0.45.0 creates a catalog service without
	// its settings, saves them, then deploys it itself: a deploy started
	// here would ship the defaults first, to a database that keeps them.
	// Only a browser session creates that way; the form now sends variables.
	// ponytail: for tabs that outlive the upgrade, gone in 0.46.0 (the
	// contract's deploy says so).
	if in.AddonSlug != "" && in.Variables == nil && !middleware.ViaAPIKey(c) {
		return
	}
	if h.deploys == nil {
		svc.DeployError = "It was created but not deployed: deploy it from its page."
		return
	}
	d, err := h.deploys.DeployCreated(c.Request.Context(), DeployCaller{OrgID: middleware.OrgIDFrom(c), UserID: middleware.UserIDFrom(c),
		Role: middleware.MemberRoleFrom(c), ViaAPIKey: middleware.ViaAPIKey(c)}, svc.ID, in.Force)
	if err != nil {
		svc.DeployError = err.Error()
		var refused *DeployRefused
		if errors.As(err, &refused) {
			svc.DeployErrorCode = refused.Code
		}
		return
	}
	svc.Deployment = d
}

// HealthAnswers gives a service's last health-check answer. Satisfied by
// *alert.Monitor, which probes every minute.
type HealthAnswers interface {
	LastHealthCheck(serviceID string) (HealthAnswer, bool)
}

// WithHealthAnswers adds the last health-check answer to GET
// /services/:serviceID. Called from main.go.
func (h *Handler) WithHealthAnswers(a HealthAnswers) { h.health = a }

func NewHandler(
	svc *ServiceManager,
	projectSvc *project.Service,
	envChecker EnvironmentOwnershipChecker,
	orgSvc OrgAuditWriter,
	versionCache VersionCacheInvalidator,
	log *zap.Logger,
) *Handler {
	return &Handler{
		svc:          svc,
		projectSvc:   projectSvc,
		envChecker:   envChecker,
		orgSvc:       orgSvc,
		versionCache: versionCache,
		log:          log,
	}
}

// ServerOwnershipChecker says whether a workspace may put services on a
// server: it owns it, or the server is shared with it. Satisfied by
// server.Service.
type ServerOwnershipChecker interface {
	CanUse(ctx context.Context, serverID, orgID string) (bool, error)
	// CheckReady refuses (serverPkg.ErrServerNotReady) a server still
	// waiting for its first agent.
	CheckReady(ctx context.Context, serverID string) error
}

// WithServerChecker refuses creating or moving a service onto a server the
// workspace neither owns nor has a share of. Without it, any server ID is
// refused.
func (h *Handler) WithServerChecker(sc ServerOwnershipChecker) { h.servers = sc }

// ownServer reports whether serverID ("" is the control-plane server) may be
// used by the caller's workspace; if not, it writes the 403. A server still
// waiting for its agent is refused with 409 server_not_ready.
func (h *Handler) ownServer(c *gin.Context, serverID string) bool {
	if serverID == "" {
		return true
	}
	if h.servers != nil {
		if ok, err := h.servers.CanUse(c.Request.Context(), serverID, middleware.OrgIDFrom(c)); err == nil && ok {
			err := h.servers.CheckReady(c.Request.Context(), serverID)
			if errors.Is(err, serverPkg.ErrServerNotReady) {
				respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "server_not_ready"})
				return false
			}
			if err != nil {
				h.log.Error("check the server is ready", zap.Error(err), zap.String("server_id", serverID))
				respond.Error(c, http.StatusInternalServerError, "failed to check the server")
				return false
			}
			return true
		}
	}
	respond.Error(c, http.StatusForbidden, "the server is not in this workspace")
	return false
}

// room refuses a service to be deployed at once that won't fit its server
// (409 capacity_exceeded, as the deploy's), before it is created.
func (h *Handler) room(c *gin.Context, in CreateInput) bool {
	capErr := h.svc.CheckRoom(c.Request.Context(), in, middleware.UserIDFrom(c), middleware.MemberRoleFrom(c), middleware.OrgIDFrom(c))
	if capErr != nil {
		respond.ErrorData(c, http.StatusConflict, capErr.Error(), capErr)
		return false
	}
	return true
}

func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	// Environment-scoped routes for creating and listing services.
	envs := r.Group("/environments/:environmentID/services")
	{
		envs.POST("", h.handleCreate)
		envs.GET("", h.handleListByEnv)
	}
	r.GET("/environments/:environmentID/address", h.handleAddress)
	r.GET("/environments/:environmentID/data", h.handleEnvironmentData)
	r.GET("/projects/:projectID/data", h.handleProjectData)
	r.GET("/services/address", h.handleNewProjectAddress)

	// Project-level list is kept for compose export (joins through environments).
	r.Group("/projects/:projectID/services").GET("", h.handleList)
	r.Group("/projects/:projectID").GET("/compose-export", h.handleComposeExport)

	// Project-scoped shared services (visible across all environments of the project).
	sharedProject := r.Group("/projects/:projectID/shared-services")
	{
		sharedProject.POST("", h.handleCreateProjectScoped)
		sharedProject.GET("", h.handleListProjectScoped)
	}

	// Every service of the workspace, for the sidebar and the command palette.
	// Not in a project-scoped API key's routes, so those keys get 403.
	r.GET("/workspace/services", h.handleListWorkspace)
	r.GET("/observability/logs", h.handleLogDrivers)

	// Org-scoped shared services (visible across the entire organisation).
	sharedOrg := r.Group("/shared-services")
	{
		sharedOrg.POST("", h.handleCreateOrgScoped)
		sharedOrg.GET("", h.handleListOrgScoped)
	}

	// Direct service routes for getters/updates/lifecycle.
	services := r.Group("/services/:serviceID")
	{
		services.GET("", h.handleGet)
		services.PUT("/config", h.handleUpdateConfig)
		services.PUT("/server", h.handleAssignServer)
		services.POST("/move-to-workspace", h.handleMoveToWorkspace)
		services.POST("/stop", h.handleStop)
		services.POST("/restart", h.handleRestart)
		services.DELETE("", h.handleDelete)
		services.GET("/logs/live", h.handleStreamLogs)
		services.GET("/stats", h.handleGetStats)
		services.GET("/metrics", h.handleGetMetrics)
		services.GET("/containers", h.handleListContainers)
		services.POST("/exec", h.handleExec)
		services.POST("/health-check/test", h.handleTestHealthCheck)
		services.GET("/volumes", h.handleListVolumes)
		services.GET("/data", h.handleServiceData)
		services.DELETE("/volumes/:mount", h.handlePruneVolume)
	}
}

// handleAddress names the address a new service would get (D-150), for the
// New service screen's summary; owners and developers, as creating one.
func (h *Handler) handleAddress(c *gin.Context) {
	if role := middleware.MemberRoleFrom(c); role == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	environmentID := c.Param("environmentID")
	if owned, err := h.envChecker.CheckOwnership(c.Request.Context(), environmentID, middleware.OrgIDFrom(c)); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "environment not found")
		return
	}
	q, ok := h.bindAddressQuery(c)
	if !ok {
		return
	}
	a, err := h.svc.AddressFor(c.Request.Context(), environmentID, q)
	if err != nil {
		h.log.Error("name a new service's address", zap.Error(err), zap.String("environment_id", environmentID))
		respond.Error(c, http.StatusInternalServerError, "could not name its address")
		return
	}
	respond.JSON(c, http.StatusOK, a)
}

// bindAddressQuery reads an address query (both address routes take the
// same one) and checks its server is the workspace's.
func (h *Handler) bindAddressQuery(c *gin.Context) (AddressQuery, bool) {
	var q struct {
		Name        string `form:"name" binding:"required,max=100"`
		Type        string `form:"type" binding:"omitempty,oneof=web worker"`
		ServerID    string `form:"server_id"`
		AddonSlug   string `form:"addon_slug"`
		DockerImage string `form:"docker_image"`
		Port        int    `form:"port" binding:"min=0,max=65535"`
	}
	if err := c.ShouldBindQuery(&q); err != nil {
		respond.Error(c, http.StatusBadRequest, "name is required; type is web or worker; port is 0 to 65535")
		return AddressQuery{}, false
	}
	if !h.ownServer(c, q.ServerID) {
		return AddressQuery{}, false
	}
	if q.Type == "" {
		q.Type = string(TypeWeb)
	}
	return AddressQuery{Name: q.Name, Type: q.Type, ServerID: q.ServerID, AddonSlug: q.AddonSlug, DockerImage: q.DockerImage, Port: q.Port}, true
}

// handleNewProjectAddress names the address a new project's first service
// would get (7g), for the first app's screen; owners and developers.
func (h *Handler) handleNewProjectAddress(c *gin.Context) {
	if role := middleware.MemberRoleFrom(c); role == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	q, ok := h.bindAddressQuery(c)
	if !ok {
		return
	}
	a, err := h.svc.AddressForNewProject(c.Request.Context(), q.Name, q)
	if err != nil {
		h.log.Error("name a new project's service's address", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "could not name its address")
		return
	}
	respond.JSON(c, http.StatusOK, a)
}

// CreateProjectWithService creates a project with its first service
// (project.InlineServiceCreator, 7g); the project handler checked the
// caller may create projects.
func (h *Handler) CreateProjectWithService(c *gin.Context, p project.CreateProjectInput, raw json.RawMessage) {
	var in CreateInput
	if err := binding.JSON.BindBody(raw, &in); err != nil {
		respond.Error(c, http.StatusBadRequest, "Invalid service")
		return
	}
	if !h.ownServer(c, in.ServerID) || !h.room(c, in) {
		return
	}
	orgID, userID := middleware.OrgIDFrom(c), middleware.UserIDFrom(c)
	created, svc, err := h.svc.CreateWithProject(c.Request.Context(), orgID, userID, p, in)
	if err != nil {
		switch {
		case errors.Is(err, project.ErrDuplicateName):
			project.WriteNameTaken(c)
		case errors.Is(err, ErrDuplicateName), errors.Is(err, ErrAddressTaken):
			respond.Error(c, http.StatusConflict, err.Error())
		case errors.Is(err, envvar.ErrInvalidKey), errors.Is(err, envvar.ErrKeyIsSecret):
			respond.Error(c, http.StatusBadRequest, err.Error())
		default:
			if !writePolicyError(c, err) && !writeConfigError(c, err) && !writePanelHostError(c, err) {
				h.log.Error("create a project with its first service", zap.Error(err))
				respond.Error(c, http.StatusInternalServerError, "Failed to create the project and its service")
			}
		}
		return
	}
	ctx := c.Request.Context()
	h.orgSvc.WriteAuditLogDetail(ctx, orgID, userID, "project.created", "project:"+created.ID, map[string]any{"name": created.Name})
	h.orgSvc.WriteAuditLog(ctx, orgID, userID, "service.created", "service:"+svc.ID)
	h.firstDeploy(c, svc, in)
	respond.JSON(c, http.StatusCreated, struct {
		*project.Project
		Service *SavedService `json:"service"`
	}{created, svc})
}

func (h *Handler) handleCreate(c *gin.Context) {
	if role := middleware.MemberRoleFrom(c); role == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	userID := middleware.UserIDFrom(c)
	environmentID := c.Param("environmentID")

	// Verify the environment belongs to the authenticated org before creating a service inside it.
	if owned, err := h.envChecker.CheckOwnership(c.Request.Context(), environmentID, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "environment not found")
		return
	}

	if preview, err := h.svc.IsPreviewEnvironment(c.Request.Context(), environmentID); err != nil || preview {
		refusePreview(c, err, ErrPreviewEnvironment, "preview_environment")
		return
	}

	var input CreateInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Default network alias includes the environment name to avoid collisions when the
	// same service name is reused across environments (e.g. "api-staging", "api-production").
	// Only applied when the caller hasn't explicitly set an alias.
	if input.NetworkAlias == "" {
		envName, err := h.envChecker.GetName(c.Request.Context(), environmentID)
		if err == nil && envName != "" {
			input.NetworkAlias = defaultNetworkAlias(input.Name, envName)
		}
	}

	if !h.ownServer(c, input.ServerID) || !h.room(c, input) {
		return
	}
	svc, err := h.svc.Create(c.Request.Context(), environmentID, userID, input)
	if err != nil {
		if errors.Is(err, ErrDuplicateName) || errors.Is(err, ErrAddressTaken) {
			respond.Error(c, http.StatusConflict, err.Error())
			return
		}
		if errors.Is(err, envvar.ErrInvalidKey) || errors.Is(err, envvar.ErrKeyIsSecret) {
			respond.Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if writePolicyError(c, err) || writeConfigError(c, err) || writePanelHostError(c, err) {
			return
		}
		h.log.Error("failed to create service", zap.Error(err), zap.String("environment_id", environmentID))
		respond.Error(c, http.StatusInternalServerError, "Failed to create service")
		return
	}

	h.orgSvc.WriteAuditLog(c.Request.Context(), orgID, userID, "service.created", "service:"+svc.ID)
	h.firstDeploy(c, svc, input)
	respond.JSON(c, http.StatusCreated, svc)
}

// handleList returns all services for a project (joins through environments).
// Used primarily for compose export and project-level service inspection.
func (h *Handler) handleList(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	projectID := c.Param("projectID")

	if owned, err := h.projectSvc.CheckOwnership(c.Request.Context(), projectID, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "project not found")
		return
	}

	services, err := h.svc.ListByProject(c.Request.Context(), projectID)
	if err != nil {
		h.log.Error("failed to list services", zap.Error(err), zap.String("project_id", projectID))
		respond.Error(c, http.StatusInternalServerError, "Failed to list services")
		return
	}

	respond.JSON(c, http.StatusOK, services)
}

// handleListByEnv returns all services belonging to a single environment.
func (h *Handler) handleListByEnv(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	environmentID := c.Param("environmentID")

	if owned, err := h.envChecker.CheckOwnership(c.Request.Context(), environmentID, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "environment not found")
		return
	}

	services, err := h.svc.ListByEnvironment(c.Request.Context(), environmentID)
	if err != nil {
		h.log.Error("failed to list services by environment", zap.Error(err), zap.String("environment_id", environmentID))
		respond.Error(c, http.StatusInternalServerError, "Failed to list services")
		return
	}

	respond.JSON(c, http.StatusOK, services)
}

func (h *Handler) handleGet(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	id := c.Param("serviceID")

	svc, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		h.log.Warn("service not found", zap.Error(err), zap.String("service_id", id))
		respond.Error(c, http.StatusNotFound, "Service not found")
		return
	}

	if owned, err := h.svc.CheckOwnership(c.Request.Context(), id, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}

	if h.health != nil {
		if a, ok := h.health.LastHealthCheck(id); ok {
			svc.LastHealthCheck = &a
		}
	}
	respond.JSON(c, http.StatusOK, svc)
}

func (h *Handler) handleUpdateConfig(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	id := c.Param("serviceID")

	// Viewers may read but not modify service configuration.
	if role := middleware.MemberRoleFrom(c); role == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden: viewer role cannot modify service configuration")
		return
	}

	// Check ownership via the GET lookup first
	existing, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		respond.Error(c, http.StatusNotFound, "Service not found")
		return
	}
	if owned, err := h.svc.CheckOwnership(c.Request.Context(), id, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}

	if preview, err := h.svc.IsPreviewCopy(c.Request.Context(), id); err != nil || preview {
		refusePreview(c, err, ErrPreviewService, "preview_service")
		return
	}

	// A field the body leaves out keeps its stored value.
	input, err := configOver(existing, func(in *UpdateConfigInput) error { return c.ShouldBindJSON(in) })
	if err != nil {
		respond.Error(c, http.StatusBadRequest, "Invalid request body")
		return
	}

	// The isolation level (D-95): API keys never change it, so an agent
	// can't lower its own sandbox; below the workspace's default is the
	// owners'.
	// A form sends every field: the level it already has is no change.
	if l := input.Isolation; l != nil && (*l == existing.Isolation || *l == isolationDefault && existing.Isolation == "") {
		input.Isolation = nil
	}
	if input.Isolation != nil {
		if middleware.ViaAPIKey(c) {
			respond.ErrorData(c, http.StatusForbidden, "changing a service's isolation needs a signed-in session, not an API key",
				gin.H{"code": "session_required"})
			return
		}
		if err := h.svc.CheckIsolation(c.Request.Context(), id, *input.Isolation, middleware.MemberRoleFrom(c)); err != nil {
			if errors.Is(err, ErrIsolationNeedsOwner) {
				respond.ErrorData(c, http.StatusForbidden, err.Error(), gin.H{"code": "isolation_needs_owner"})
				return
			}
			h.log.Error("check isolation", zap.Error(err), zap.String("service_id", id))
			respond.Error(c, http.StatusInternalServerError, "Failed to update service config")
			return
		}
	}

	svc, err := h.svc.UpdateConfig(c.Request.Context(), id, input)
	if err != nil {
		if errors.Is(err, ErrDuplicateName) {
			respond.Error(c, http.StatusConflict, err.Error())
			return
		}
		if writePolicyError(c, err) || writeConfigError(c, err) {
			return
		}
		h.log.Error("failed to update service config", zap.Error(err), zap.String("service_id", id))
		respond.Error(c, http.StatusInternalServerError, "Failed to update service config")
		return
	}

	if h.versionCache != nil {
		h.versionCache.InvalidateCache(id)
	}
	if svc.Service != nil {
		if fields := changedSettings(existing, svc.Service); len(fields) > 0 {
			h.orgSvc.WriteAuditLogDetail(c.Request.Context(), orgID, middleware.UserIDFrom(c), "service.settings_changed", "service:"+id,
				map[string]any{"fields": fields})
		}
	}
	if svc.Service != nil && svc.Isolation != existing.Isolation {
		levelName := func(l string) string {
			if l == "" {
				return isolationDefault
			}
			return l
		}
		h.orgSvc.WriteAuditLogDetail(c.Request.Context(), orgID, middleware.UserIDFrom(c), "service.isolation_changed", "service:"+id,
			map[string]any{"from": levelName(existing.Isolation), "to": levelName(svc.Isolation)})
	}

	respond.JSON(c, http.StatusOK, svc)
}

func (h *Handler) handleAssignServer(c *gin.Context) {
	if role := middleware.MemberRoleFrom(c); role == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	id := c.Param("serviceID")

	if owned, err := h.svc.CheckOwnership(c.Request.Context(), id, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}

	var body struct {
		ServerID string `json:"server_id"` // empty string to unassign
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		respond.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}

	if !h.ownServer(c, body.ServerID) {
		return
	}
	svc, from, err := h.svc.AssignServer(c.Request.Context(), id, body.ServerID)
	if errors.Is(err, ErrMountsSharedVolume) || errors.Is(err, ErrMoveWhileRunning) {
		respond.Error(c, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		h.log.Error("failed to assign server", zap.Error(err), zap.String("service_id", id))
		respond.Error(c, http.StatusInternalServerError, "failed to assign server")
		return
	}
	// "" is the control-plane server. A running service moves by a deploy,
	// audited as service.move_started.
	if svc.ServerID != from {
		h.orgSvc.WriteAuditLogDetail(c.Request.Context(), orgID, middleware.UserIDFrom(c), "service.server_changed", "service:"+id,
			map[string]any{"from": from, "to": svc.ServerID})
	}

	respond.JSON(c, http.StatusOK, svc)
}

func (h *Handler) handleStop(c *gin.Context) {
	respond.Long(c, respond.LongWork) // stops and removes containers
	if role := middleware.MemberRoleFrom(c); role == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	userID := middleware.UserIDFrom(c)
	id := c.Param("serviceID")

	if owned, err := h.svc.CheckOwnership(c.Request.Context(), id, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}

	if err := h.svc.Stop(c.Request.Context(), id); err != nil {
		h.log.Error("failed to stop service", zap.Error(err), zap.String("service_id", id))
		respond.Error(c, http.StatusInternalServerError, "Failed to stop service")
		return
	}

	h.orgSvc.WriteAuditLog(c.Request.Context(), orgID, userID, "service.stopped", "service:"+id)
	respond.NoContent(c)
}

// handleRestart restarts the service's running containers in place. Not in
// a project-scoped API key's routes, as Stop is not.
func (h *Handler) handleRestart(c *gin.Context) {
	respond.Long(c, respond.LongWork) // each container gets 10 s to stop
	if role := middleware.MemberRoleFrom(c); role == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	userID := middleware.UserIDFrom(c)
	id := c.Param("serviceID")

	if owned, err := h.svc.CheckOwnership(c.Request.Context(), id, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}

	switch err := h.svc.Restart(c.Request.Context(), id); {
	case err == nil:
	case errors.Is(err, ErrNotRunning):
		respond.Error(c, http.StatusConflict, "nothing of this service is running: deploy it to start it")
		return
	case errors.Is(err, ErrDeployRunning):
		respond.Error(c, http.StatusConflict, ErrDeployRunning.Error())
		return
	default:
		h.log.Error("failed to restart service", zap.Error(err), zap.String("service_id", id))
		respond.Error(c, http.StatusInternalServerError, "failed to restart the service")
		return
	}

	h.orgSvc.WriteAuditLog(c.Request.Context(), orgID, userID, "service.restarted", "service:"+id)
	respond.NoContent(c)
}

func (h *Handler) handleDelete(c *gin.Context) {
	respond.Long(c, respond.LongWork) // stops and removes containers
	if role := middleware.MemberRoleFrom(c); role == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	userID := middleware.UserIDFrom(c)
	id := c.Param("serviceID")

	svc, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		respond.Error(c, http.StatusNotFound, "Service not found")
		return
	}
	if owned, err := h.svc.CheckOwnership(c.Request.Context(), id, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}

	if preview, err := h.svc.IsPreviewCopy(c.Request.Context(), id); err != nil || preview {
		refusePreview(c, err, ErrPreviewService, "preview_service")
		return
	}
	if err := h.svc.DeleteConfirmed(c.Request.Context(), id, c.Query("confirm")); err != nil {
		if errors.Is(err, ErrConfirmName) {
			respond.ErrorData(c, http.StatusBadRequest, ErrConfirmName.Error(), gin.H{"code": "confirm_name"})
			return
		}
		h.log.Error("failed to delete service", zap.Error(err), zap.String("service_id", id))
		if errors.Is(err, serverPkg.ErrServerFrozen) {
			respond.ErrorData(c, http.StatusConflict, serverPkg.ErrServerFrozen.Error(), gin.H{"code": "server_frozen"})
			return
		}
		if errors.Is(err, ErrTeardown) {
			respond.Error(c, http.StatusBadGateway, TeardownMessage(err))
			return
		}
		respond.Error(c, http.StatusInternalServerError, "Failed to delete service")
		return
	}

	h.orgSvc.WriteAuditLogDetail(c.Request.Context(), orgID, userID, "service.deleted", "service:"+id, map[string]any{"name": svc.Name})
	respond.NoContent(c)
}

func (h *Handler) handleStreamLogs(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	id := c.Param("serviceID")

	if owned, err := h.svc.CheckOwnership(c.Request.Context(), id, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}

	tailStr := c.DefaultQuery("tail", "100")
	tail, err := strconv.Atoi(tailStr)
	if err != nil || tail <= 0 {
		tail = 100
	}
	if tail > 1000 {
		tail = 1000
	}

	// Refuse a container that is not this service's before the stream starts,
	// so the client gets a JSON 403 (StreamLogs checks again).
	containerName := c.Query("container")
	if err := h.svc.CheckContainer(c.Request.Context(), id, containerName); err != nil {
		if !writeContainerError(c, err) {
			h.log.Error("check container failed", zap.Error(err), zap.String("service_id", id))
			respond.Error(c, http.StatusInternalServerError, "failed to check the container")
		}
		return
	}

	respond.Stream(c)
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Transfer-Encoding", "chunked")
	c.Writer.Flush()

	logFn := func(line string) {
		msg := fmt.Sprintf("data: %s\n\n", strings.ReplaceAll(line, "\n", " "))
		_, _ = c.Writer.WriteString(msg)
		c.Writer.Flush()
	}

	opts := LogStreamOptions{
		Tail:       tail,
		Container:  containerName,
		Timestamps: c.Query("timestamps") == "1",
		// The container followed, so the page can name it and say whether
		// it runs.
		OnContainer: func(name string, running bool) {
			b, _ := json.Marshal(map[string]any{"name": name, "running": running})
			_, _ = c.Writer.WriteString("event: container\ndata: " + string(b) + "\n\n")
			c.Writer.Flush()
		},
	}
	if err := h.svc.StreamLogs(c.Request.Context(), id, opts, logFn); err != nil {
		switch {
		case errors.Is(err, ErrLogFellBehind), errors.Is(err, ErrLogNoContainer):
			_, _ = c.Writer.WriteString("event: error\ndata: " + err.Error() + "\n\n")
		case !errors.Is(err, context.Canceled):
			h.log.Error("live log stream error", zap.Error(err), zap.String("service_id", id))
			// A fixed message: the error can name a server's address, which
			// a workspace using a shared server is not shown (D-39).
			msg := "the log could not be followed"
			if errors.Is(err, ErrLogServerUnreachable) {
				msg = ErrLogServerUnreachable.Error()
			}
			_, _ = c.Writer.WriteString("event: error\ndata: " + msg + "\n\n")
		}
	}

	_, _ = c.Writer.WriteString("event: done\ndata: {}\n\n")
	c.Writer.Flush()
}

func (h *Handler) handleGetStats(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	id := c.Param("serviceID")

	if owned, err := h.svc.CheckOwnership(c.Request.Context(), id, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}

	containerName := c.Query("container")
	stats, err := h.svc.GetStats(c.Request.Context(), id, containerName)
	if err != nil && writeContainerError(c, err) {
		return
	}
	if err != nil {
		// Container may be starting, stopping, or stats temporarily unavailable — not an error.
		h.log.Debug("stats unavailable", zap.Error(err), zap.String("service_id", id))
		respond.NoContent(c)
		return
	}

	respond.JSON(c, http.StatusOK, stats)
}

func (h *Handler) handleGetMetrics(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	id := c.Param("serviceID")

	if owned, err := h.svc.CheckOwnership(c.Request.Context(), id, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}

	var since time.Time
	daily := false
	switch c.DefaultQuery("range", "1h") {
	case "6h":
		since = time.Now().Add(-6 * time.Hour)
	case "24h":
		since = time.Now().Add(-24 * time.Hour)
	case "7d":
		daily = true
	default:
		since = time.Now().Add(-1 * time.Hour)
	}

	// One compose service's points; empty sums the stack.
	var points []MetricPoint
	var err error
	if daily {
		points, err = h.svc.ListDailyMetrics(c.Request.Context(), id, c.Query("compose_service"), time.Now())
	} else {
		points, err = h.svc.ListMetrics(c.Request.Context(), id, c.Query("compose_service"), since)
	}
	if err != nil {
		h.log.Error("failed to fetch metrics", zap.Error(err), zap.String("service_id", id))
		respond.Error(c, http.StatusInternalServerError, "failed to fetch metrics")
		return
	}

	respond.JSON(c, http.StatusOK, points)
}

func (h *Handler) handleComposeExport(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	projectID := c.Param("projectID")

	if owned, err := h.projectSvc.CheckOwnership(c.Request.Context(), projectID, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "project not found")
		return
	}

	yamlStr, err := h.svc.GenerateComposeExport(c.Request.Context(), projectID)
	if err != nil {
		h.log.Error("failed to generate compose export", zap.Error(err), zap.String("project_id", projectID))
		respond.Error(c, http.StatusInternalServerError, "failed to generate compose export")
		return
	}

	c.Header("Content-Type", "application/x-yaml")
	c.Header("Content-Disposition", `attachment; filename="docker-compose.yml"`)
	c.String(http.StatusOK, yamlStr)
}

// writePolicyError answers a deploy policy refusal with 422 and the
// violations ({"error": ..., "data": {"code": "deploy_policy_violation",
// "violations": [...]}}). It reports whether it wrote a response.
func writePolicyError(c *gin.Context, err error) bool {
	var pv *composespec.ViolationsError
	if !errors.As(err, &pv) {
		return false
	}
	respond.ErrorData(c, http.StatusUnprocessableEntity, pv.Error(), pv.Body())
	return true
}

// writeConfigError answers a configuration validation error
// (ErrInvalidConfig) with 400 and its message. It reports whether it wrote a
// response.
func writeConfigError(c *gin.Context, err error) bool {
	if !errors.Is(err, ErrInvalidConfig) {
		return false
	}
	respond.Error(c, http.StatusBadRequest, err.Error())
	return true
}

// writePanelHostError answers a create on the panel host under Only Toft
// (ErrPanelHostNoApps) with 409 panel_host_no_apps. It reports whether it
// wrote a response.
func writePanelHostError(c *gin.Context, err error) bool {
	if !errors.Is(err, ErrPanelHostNoApps) {
		return false
	}
	respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "panel_host_no_apps"})
	return true
}

// lazySSE returns a line writer that sends the SSE headers on its first
// line, and a function that reports whether anything was sent, so a handler
// can still answer with a JSON error when the stream never started.
func lazySSE(c *gin.Context) (write func(string), started func() bool) {
	respond.Stream(c)
	sent := false
	write = func(line string) {
		if !sent {
			c.Writer.Header().Set("Content-Type", "text/event-stream")
			c.Writer.Header().Set("Cache-Control", "no-cache")
			c.Writer.Header().Set("Transfer-Encoding", "chunked")
			sent = true
		}
		fmt.Fprintf(c.Writer, "data: %s\n\n", strings.ReplaceAll(line, "\n", " "))
		c.Writer.Flush()
	}
	return write, func() bool { return sent }
}

type healthTestRequest struct {
	Path    string `json:"path" binding:"required,max=4096"`
	Port    int    `json:"port" binding:"required"`
	Timeout int    `json:"timeout"`
}

// handleTestHealthCheck asks the service's container for a health-check path
// once, with the form's values, so Settings can show what it answers before
// the check is saved. Owners and developers only: it makes a request inside
// the service's network.
func (h *Handler) handleTestHealthCheck(c *gin.Context) {
	if role := middleware.MemberRoleFrom(c); role == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	id := c.Param("serviceID")
	if owned, err := h.svc.CheckOwnership(c.Request.Context(), id, middleware.OrgIDFrom(c)); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}
	var body healthTestRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		respond.Error(c, http.StatusBadRequest, "path and port are required")
		return
	}
	if body.Timeout == 0 {
		body.Timeout = 5
	}
	result, err := h.svc.TestHealthCheck(c.Request.Context(), id, body.Path, body.Port, body.Timeout)
	switch {
	case err == nil:
		respond.JSON(c, http.StatusOK, result)
	case writeConfigError(c, err):
	case errors.Is(err, ErrNotRunning):
		respond.Error(c, http.StatusConflict, err.Error())
	default:
		h.log.Error("health check test failed", zap.Error(err), zap.String("service_id", id))
		respond.Error(c, http.StatusInternalServerError, "could not test the health check")
	}
}

// writeContainerError answers the container and host-exec refusals with a
// JSON error. It reports whether err was one of them.
func writeContainerError(c *gin.Context, err error) bool {
	switch {
	case errors.Is(err, ErrHostExecOperatorOnly):
		respond.Error(c, http.StatusForbidden, ErrHostExecOperatorOnly.Error())
	case errors.Is(err, ErrContainerNotInService):
		respond.Error(c, http.StatusForbidden, ErrContainerNotInService.Error())
	default:
		return false
	}
	return true
}

type execRequest struct {
	Container string `json:"container"` // compose service name, or empty for single-container exec
	Command   string `json:"command" binding:"required,max=4000"`
}

func (h *Handler) handleExec(c *gin.Context) {
	if role := middleware.MemberRoleFrom(c); role == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	id := c.Param("serviceID")

	if owned, err := h.svc.CheckOwnership(c.Request.Context(), id, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}

	var body execRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		respond.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}

	userID := middleware.UserIDFrom(c)
	// The host's shell: operators only, with their password confirmed (D-53).
	if body.Container == HostContainer {
		if err := h.svc.AuthorizeHostExec(c.Request.Context(), userID); err != nil {
			if !writeContainerError(c, err) {
				h.log.Error("host exec authorization failed", zap.Error(err))
				respond.Error(c, http.StatusInternalServerError, "failed to verify permissions")
			}
			return
		}
		if !middleware.RequireReauth(c) {
			return
		}
	}

	logFn, started := lazySSE(c)

	if err := h.svc.ExecInService(c.Request.Context(), userID, id, body.Container, body.Command, logFn); err != nil {
		if !started() && writeContainerError(c, err) {
			return
		}
		if !errors.Is(err, context.Canceled) {
			h.log.Error("exec in service failed", zap.Error(err), zap.String("service_id", id))
			logFn("ERROR: " + err.Error())
		}
	}

	if !started() {
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
	}
	fmt.Fprint(c.Writer, "event: done\ndata: {}\n\n")
	c.Writer.Flush()
}

func (h *Handler) handleListContainers(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	id := c.Param("serviceID")

	if owned, err := h.svc.CheckOwnership(c.Request.Context(), id, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}

	names, err := h.svc.ListContainersForService(c.Request.Context(), id)
	if err != nil {
		h.log.Error("list containers failed", zap.Error(err), zap.String("service_id", id))
		respond.Error(c, http.StatusInternalServerError, "failed to list containers")
		return
	}

	respond.JSON(c, http.StatusOK, names)
}

func (h *Handler) handleListVolumes(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	id := c.Param("serviceID")

	if owned, err := h.svc.CheckOwnership(c.Request.Context(), id, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}

	volumes, err := h.svc.ListVolumes(c.Request.Context(), id)
	if err != nil {
		h.log.Error("list volumes failed", zap.Error(err), zap.String("service_id", id))
		respond.Error(c, http.StatusInternalServerError, "failed to list volumes")
		return
	}

	respond.JSON(c, http.StatusOK, volumes)
}

// handleServiceData names what deleting the service removes (its delete
// dialog); owners and developers, as deleting it.
func (h *Handler) handleServiceData(c *gin.Context) {
	if role := middleware.MemberRoleFrom(c); role == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	id := c.Param("serviceID")
	if owned, err := h.svc.CheckOwnership(c.Request.Context(), id, middleware.OrgIDFrom(c)); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}
	h.respondData(c, "", id)
}

// handleEnvironmentData names what deleting the environment removes.
func (h *Handler) handleEnvironmentData(c *gin.Context) {
	if role := middleware.MemberRoleFrom(c); role == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	id := c.Param("environmentID")
	if owned, err := h.envChecker.CheckOwnership(c.Request.Context(), id, middleware.OrgIDFrom(c)); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "environment not found")
		return
	}
	h.respondData(c, ScopeEnvironment, id)
}

// handleProjectData names what deleting the project removes.
func (h *Handler) handleProjectData(c *gin.Context) {
	if role := middleware.MemberRoleFrom(c); role == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	id := c.Param("projectID")
	if owned, err := h.projectSvc.CheckOwnership(c.Request.Context(), id, middleware.OrgIDFrom(c)); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "project not found")
		return
	}
	h.respondData(c, ScopeProject, id)
}

func (h *Handler) respondData(c *gin.Context, scope Scope, id string) {
	sizes := true
	if v := c.Query("sizes"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			respond.Error(c, http.StatusBadRequest, "sizes is true or false")
			return
		}
		sizes = b
	}
	data, err := h.svc.Data(c.Request.Context(), scope, id, sizes)
	if err != nil {
		h.log.Error("read what a delete removes", zap.Error(err), zap.String("scope", string(scope)), zap.String("id", id))
		respond.Error(c, http.StatusInternalServerError, "could not read its data")
		return
	}
	respond.JSON(c, http.StatusOK, data)
}

func (h *Handler) handlePruneVolume(c *gin.Context) {
	if role := middleware.MemberRoleFrom(c); role == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	id := c.Param("serviceID")
	mountName := c.Param("mount")

	if owned, err := h.svc.CheckOwnership(c.Request.Context(), id, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}

	if err := h.svc.PruneVolume(c.Request.Context(), id, mountName); err != nil {
		h.log.Error("prune volume failed", zap.Error(err), zap.String("service_id", id), zap.String("mount", mountName))
		respond.Error(c, http.StatusInternalServerError, "failed to prune volume")
		return
	}
	h.orgSvc.WriteAuditLogDetail(c.Request.Context(), orgID, middleware.UserIDFrom(c), "service.volume_purged", "service:"+id,
		map[string]any{"mount": mountName})

	respond.NoContent(c)
}

// ── Scoped shared service handlers ───────────────────────────────────────────

func (h *Handler) handleCreateProjectScoped(c *gin.Context) {
	if role := middleware.MemberRoleFrom(c); role == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	userID := middleware.UserIDFrom(c)
	projectID := c.Param("projectID")

	if owned, err := h.projectSvc.CheckOwnership(c.Request.Context(), projectID, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "project not found")
		return
	}

	var input CreateInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}

	if !h.ownServer(c, input.ServerID) || !h.room(c, input) {
		return
	}
	svc, err := h.svc.CreateProjectScoped(c.Request.Context(), projectID, userID, input)
	if err != nil {
		if errors.Is(err, ErrDuplicateName) {
			respond.Error(c, http.StatusConflict, err.Error())
			return
		}
		if errors.Is(err, envvar.ErrInvalidKey) || errors.Is(err, envvar.ErrKeyIsSecret) {
			respond.Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if writePolicyError(c, err) || writeConfigError(c, err) || writePanelHostError(c, err) {
			return
		}
		h.log.Error("failed to create project-scoped service", zap.Error(err), zap.String("project_id", projectID))
		respond.Error(c, http.StatusInternalServerError, "failed to create service")
		return
	}

	h.orgSvc.WriteAuditLog(c.Request.Context(), orgID, userID, "service.created.project_scoped", "service:"+svc.ID)
	h.firstDeploy(c, svc, input)
	respond.JSON(c, http.StatusCreated, svc)
}

func (h *Handler) handleListProjectScoped(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	projectID := c.Param("projectID")

	if owned, err := h.projectSvc.CheckOwnership(c.Request.Context(), projectID, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "project not found")
		return
	}

	services, err := h.svc.ListProjectScoped(c.Request.Context(), projectID)
	if err != nil {
		h.log.Error("failed to list project-scoped services", zap.Error(err), zap.String("project_id", projectID))
		respond.Error(c, http.StatusInternalServerError, "failed to list services")
		return
	}

	respond.JSON(c, http.StatusOK, services)
}

func (h *Handler) handleCreateOrgScoped(c *gin.Context) {
	if role := middleware.MemberRoleFrom(c); role == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	userID := middleware.UserIDFrom(c)

	var input CreateInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}

	if !h.ownServer(c, input.ServerID) || !h.room(c, input) {
		return
	}
	svc, err := h.svc.CreateOrgScoped(c.Request.Context(), orgID, userID, input)
	if err != nil {
		if errors.Is(err, ErrDuplicateName) {
			respond.Error(c, http.StatusConflict, err.Error())
			return
		}
		if errors.Is(err, ErrMoveAliasTaken) {
			respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "alias_taken"})
			return
		}
		if errors.Is(err, envvar.ErrInvalidKey) || errors.Is(err, envvar.ErrKeyIsSecret) {
			respond.Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if writePolicyError(c, err) || writeConfigError(c, err) || writePanelHostError(c, err) {
			return
		}
		h.log.Error("failed to create org-scoped service", zap.Error(err), zap.String("org_id", orgID))
		respond.Error(c, http.StatusInternalServerError, "failed to create service")
		return
	}

	h.orgSvc.WriteAuditLog(c.Request.Context(), orgID, userID, "service.created.org_scoped", "service:"+svc.ID)
	h.firstDeploy(c, svc, input)
	respond.JSON(c, http.StatusCreated, svc)
}

// handleMoveToWorkspace makes a project's or environment's service the
// workspace's own (D-162); owners and developers, as creating one. It runs
// on the workspace's network from its next deploy, which the client starts.
func (h *Handler) handleMoveToWorkspace(c *gin.Context) {
	if role := middleware.MemberRoleFrom(c); role == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	id := c.Param("serviceID")
	if owned, err := h.svc.CheckOwnership(c.Request.Context(), id, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}
	svc, before, err := h.svc.MoveToWorkspace(c.Request.Context(), orgID, id)
	switch {
	case errors.Is(err, ErrDuplicateName):
		respond.ErrorData(c, http.StatusConflict, "the workspace has a service by this name already: rename one of them first", gin.H{"code": "name_taken"})
		return
	case errors.Is(err, ErrAlreadyWorkspace):
		respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "already_workspace"})
		return
	case errors.Is(err, ErrMoveAliasTaken):
		respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "alias_taken"})
		return
	case errors.Is(err, ErrMoveDeploysOnPush):
		respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "deploys_on_push"})
		return
	case errors.Is(err, ErrMoveCompose):
		respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "compose_stack"})
		return
	case errors.Is(err, ErrMoveRepositoryService):
		respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "builds_from_repository"})
		return
	case errors.Is(err, ErrPreviewService):
		refusePreview(c, nil, ErrPreviewService, "preview_service")
		return
	case err != nil:
		h.log.Error("failed to move a service to the workspace", zap.Error(err), zap.String("service_id", id))
		respond.Error(c, http.StatusInternalServerError, "failed to move the service")
		return
	}
	h.orgSvc.WriteAuditLogDetail(c.Request.Context(), orgID, middleware.UserIDFrom(c), "service.moved_to_workspace", "service:"+id,
		map[string]any{"project_id": before.ProjectID, "environment_id": before.EnvironmentID})
	respond.JSON(c, http.StatusOK, svc)
}

// handleListWorkspace returns every service of the caller's workspace.
func (h *Handler) handleListWorkspace(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	services, err := h.svc.ListWorkspace(c.Request.Context(), orgID)
	if err != nil {
		h.log.Error("failed to list workspace services", zap.Error(err), zap.String("org_id", orgID))
		respond.Error(c, http.StatusInternalServerError, "failed to list services")
		return
	}
	// An agent connected to some projects sees their services alone, not
	// the workspace's (D-100), whether through MCP or REST.
	if a, ok := middleware.AgentFrom(c); ok && !a.AllProjects {
		services = slices.DeleteFunc(services, func(s WorkspaceService) bool { return !slices.Contains(a.ProjectIDs, s.ProjectID) })
	}
	respond.JSON(c, http.StatusOK, services)
}

// handleLogDrivers answers where the active workspace's containers send
// their output, server by server (the Observability page, D-48). Every
// member may; it reads, never changes.
func (h *Handler) handleLogDrivers(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	logs, err := h.svc.LogDrivers(c.Request.Context(), orgID)
	if err != nil {
		h.log.Error("failed to read log drivers", zap.Error(err), zap.String("org_id", orgID))
		respond.Error(c, http.StatusInternalServerError, "failed to read where logs go")
		return
	}
	respond.JSON(c, http.StatusOK, logs)
}

func (h *Handler) handleListOrgScoped(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)

	services, err := h.svc.ListOrgScoped(c.Request.Context(), orgID)
	if err != nil {
		h.log.Error("failed to list org-scoped services", zap.Error(err), zap.String("org_id", orgID))
		respond.Error(c, http.StatusInternalServerError, "failed to list services")
		return
	}

	respond.JSON(c, http.StatusOK, services)
}

// refusePreview answers 409 with code for a preview's environment or
// service (D-97), or 500 when checking failed.
func refusePreview(c *gin.Context, err, refusal error, code string) {
	if err != nil {
		respond.Error(c, http.StatusInternalServerError, "Failed to read the service")
		return
	}
	respond.ErrorData(c, http.StatusConflict, refusal.Error(), gin.H{"code": code})
}
