package environment

import (
	"context"
	"errors"
	"net/http"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ProjectChecker verifies that a project belongs to an org.
// Satisfied by project.Service — defined here to avoid a circular import.
type ProjectChecker interface {
	CheckOwnership(ctx context.Context, id, orgID string) (bool, error)
}

// AuditWriter records a change in the workspace's audit log; satisfied by
// org.Service.
type AuditWriter interface {
	WriteAuditLogDetail(ctx context.Context, orgID, actorID, action, resource string, detail map[string]any)
}

// Handler handles HTTP requests for the environment domain.
type Handler struct {
	svc        *Service
	projectSvc ProjectChecker
	audit      AuditWriter
	log        *zap.Logger
}

// WithAudit records protection changes and deletions in the audit log.
func (h *Handler) WithAudit(a AuditWriter) *Handler {
	h.audit = a
	return h
}

// NewHandler creates a new environment Handler.
func NewHandler(svc *Service, projectSvc ProjectChecker, log *zap.Logger) *Handler {
	return &Handler{svc: svc, projectSvc: projectSvc, log: log}
}

// RegisterRoutes mounts environment endpoints on the given router group.
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	projects := r.Group("/projects/:projectID/environments")
	{
		projects.GET("", h.handleList)
		projects.POST("", h.handleCreate)
	}

	r.GET("/projects/:projectID/overview", h.handleProjectOverview)
	r.GET("/workspace/environments", h.handleListWorkspace)

	envs := r.Group("/environments/:environmentID")
	{
		envs.GET("", h.handleGet)
		envs.GET("/overview", h.handleOverview)
		envs.PATCH("", h.handleUpdate)
		envs.DELETE("", h.handleDelete)
	}
}

func (h *Handler) handleList(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	projectID := c.Param("projectID")

	if owned, err := h.projectSvc.CheckOwnership(c.Request.Context(), projectID, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "project not found")
		return
	}

	envs, err := h.svc.ListByProject(c.Request.Context(), projectID)
	if err != nil {
		h.log.Error("failed to list environments", zap.Error(err), zap.String("project_id", projectID))
		respond.Error(c, http.StatusInternalServerError, "failed to list environments")
		return
	}

	respond.JSON(c, http.StatusOK, envs)
}

func (h *Handler) handleCreate(c *gin.Context) {
	if middleware.MemberRoleFrom(c) == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	projectID := c.Param("projectID")

	if owned, err := h.projectSvc.CheckOwnership(c.Request.Context(), projectID, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "project not found")
		return
	}

	var input CreateInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, badBody(input.Color))
		return
	}

	env, err := h.svc.Create(c.Request.Context(), projectID, input)
	if err != nil {
		h.log.Error("failed to create environment", zap.Error(err), zap.String("project_id", projectID))
		respond.Error(c, http.StatusInternalServerError, "failed to create environment")
		return
	}

	respond.JSON(c, http.StatusCreated, env)
}

func (h *Handler) handleGet(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	id := c.Param("environmentID")

	if owned, err := h.svc.CheckOwnership(c.Request.Context(), id, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "environment not found")
		return
	}

	env, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		h.log.Warn("environment not found", zap.Error(err), zap.String("environment_id", id))
		respond.Error(c, http.StatusNotFound, "environment not found")
		return
	}

	respond.JSON(c, http.StatusOK, env)
}

func (h *Handler) handleUpdate(c *gin.Context) {
	if middleware.MemberRoleFrom(c) == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	id := c.Param("environmentID")

	if owned, err := h.svc.CheckOwnership(c.Request.Context(), id, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "environment not found")
		return
	}

	if h.isPreview(c, id) {
		return
	}

	var input UpdateInput
	if err := c.ShouldBindJSON(&input); err != nil || (input.Name == nil && input.Color == nil && input.Protected == nil) {
		respond.Error(c, http.StatusBadRequest, badBody(input.Color))
		return
	}

	// Protection decides what agents may do there without asking (D-100):
	// lifting it is the owners', with the password, as loosening the agent
	// policy is, and any change of it is audited.
	var before *Environment
	if input.Protected != nil {
		b, err := h.svc.Get(c.Request.Context(), id)
		if err != nil {
			respond.Error(c, http.StatusNotFound, "environment not found")
			return
		}
		before = b
		input.WasProtected = &b.Protected
		if b.Protected && !*input.Protected {
			if middleware.MemberRoleFrom(c) != "owner" {
				respond.Error(c, http.StatusForbidden, "only owners can lift an environment's protection")
				return
			}
			if !middleware.RequireReauth(c) {
				return
			}
		}
	}

	env, err := h.svc.Update(c.Request.Context(), id, input)
	if errors.Is(err, ErrProtectionChanged) {
		respond.Error(c, http.StatusConflict, err.Error())
		return
	}
	if errors.Is(err, ErrNotFound) {
		respond.Error(c, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		h.log.Error("failed to update environment", zap.Error(err), zap.String("environment_id", id))
		respond.Error(c, http.StatusInternalServerError, "failed to update environment")
		return
	}
	if before != nil && before.Protected != env.Protected && h.audit != nil {
		h.audit.WriteAuditLogDetail(c.Request.Context(), orgID, middleware.UserIDFrom(c), "environment.protection_changed",
			"environment:"+id, map[string]any{"name": env.Name, "protected": env.Protected})
	}

	respond.JSON(c, http.StatusOK, env)
}

// handleDelete removes the environment's services from their servers, with
// their volumes, so it asks what deleting a project asks (D-120, 1): an
// owner, the password, and the environment's name typed.
func (h *Handler) handleDelete(c *gin.Context) {
	respond.Long(c, respond.LongWork) // stops and removes containers
	if middleware.MemberRoleFrom(c) != "owner" {
		respond.Error(c, http.StatusForbidden, "only owners can delete an environment")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	id := c.Param("environmentID")

	if owned, err := h.svc.CheckOwnership(c.Request.Context(), id, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "environment not found")
		return
	}
	if !middleware.RequireReauth(c) {
		return
	}

	env, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		respond.Error(c, http.StatusNotFound, "environment not found")
		return
	}
	if env.Preview != nil {
		respond.ErrorData(c, http.StatusConflict, ErrPreview.Error(), gin.H{"code": "preview_environment"})
		return
	}
	if c.Query("confirm") != env.Name {
		respond.ErrorData(c, http.StatusBadRequest, "type the environment's name to confirm", gin.H{"code": "confirm_name"})
		return
	}

	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		h.log.Error("failed to delete environment", zap.Error(err), zap.String("environment_id", id))
		if errors.Is(err, ErrServicesNotRemoved) {
			respond.Error(c, http.StatusBadGateway, err.Error())
			return
		}
		respond.Error(c, http.StatusInternalServerError, "failed to delete environment")
		return
	}
	if h.audit != nil {
		h.audit.WriteAuditLogDetail(c.Request.Context(), orgID, middleware.UserIDFrom(c), "environment.deleted",
			"environment:"+id, map[string]any{"name": env.Name})
	}
	respond.NoContent(c)
}

// handleOverview is what the Environment page shows of the environment's
// services and network. Every member may read it.
func (h *Handler) handleOverview(c *gin.Context) {
	id := c.Param("environmentID")
	if owned, err := h.svc.CheckOwnership(c.Request.Context(), id, middleware.OrgIDFrom(c)); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "environment not found")
		return
	}
	o, err := h.svc.Overview(c.Request.Context(), id)
	if err != nil {
		h.log.Error("failed to read the environment overview", zap.Error(err), zap.String("environment_id", id))
		respond.Error(c, http.StatusInternalServerError, "failed to read the environment")
		return
	}
	respond.JSON(c, http.StatusOK, o)
}

// handleProjectOverview is what a project's Overview shows beside its
// services. Every member may read it.
func (h *Handler) handleProjectOverview(c *gin.Context) {
	projectID := c.Param("projectID")
	if owned, err := h.projectSvc.CheckOwnership(c.Request.Context(), projectID, middleware.OrgIDFrom(c)); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "project not found")
		return
	}
	o, err := h.svc.ProjectOverview(c.Request.Context(), projectID)
	if err != nil {
		h.log.Error("failed to read the project overview", zap.Error(err), zap.String("project_id", projectID))
		respond.Error(c, http.StatusInternalServerError, "failed to read the project")
		return
	}
	respond.JSON(c, http.StatusOK, o)
}

// handleListWorkspace returns every environment of the caller's workspace.
func (h *Handler) handleListWorkspace(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	envs, err := h.svc.ListByWorkspace(c.Request.Context(), orgID)
	if err != nil {
		h.log.Error("failed to list workspace environments", zap.Error(err), zap.String("org_id", orgID))
		respond.Error(c, http.StatusInternalServerError, "failed to list environments")
		return
	}
	respond.JSON(c, http.StatusOK, envs)
}

// isPreview answers 409 preview_environment for a preview's environment,
// which changes with its pull request only (D-97).
func (h *Handler) isPreview(c *gin.Context, id string) bool {
	env, err := h.svc.Get(c.Request.Context(), id)
	if err != nil || env.Preview == nil {
		return false
	}
	respond.ErrorData(c, http.StatusConflict, ErrPreview.Error(), gin.H{"code": "preview_environment"})
	return true
}

// badBody says why a request body was refused. Red left the colours
// (D-142), so a client that still sends it is told which remain.
func badBody(color *string) string {
	if color != nil && *color == "red" {
		return "red is no longer an environment colour, since red means failing: use amber, violet, blue, teal or gray"
	}
	return "invalid request body"
}
