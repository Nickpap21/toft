package apikey

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Handler is the HTTP adapter for the apikey domain.
type Handler struct {
	svc      *Service
	projects ProjectOwnershipChecker
	audit    AuditWriter
	log      *zap.Logger
}

// AuditWriter records an action in a workspace's audit log. Satisfied by
// *org.Service.
type AuditWriter interface {
	WriteAuditLogDetail(ctx context.Context, orgID, actorID, action, resource string, detail map[string]any)
}

// WithAudit records key creations and revocations in the audit log.
func (h *Handler) WithAudit(a AuditWriter) *Handler {
	h.audit = a
	return h
}

// record writes a key's event to orgID's audit log, with what the Details
// column shows: its name, project and scopes.
func (h *Handler) record(c *gin.Context, orgID, action string, k APIKey) {
	if h.audit == nil || orgID == "" {
		return
	}
	detail := map[string]any{"name": k.Name, "scopes": k.Scopes, "created_at": k.CreatedAt, "expires_at": k.ExpiresAt}
	if k.ProjectName != "" {
		detail["project_name"] = k.ProjectName
	}
	if len(k.ServiceIDs) > 0 {
		names := make([]string, 0, len(k.Services))
		for _, svc := range k.Services {
			names = append(names, svc.Name)
		}
		detail["service_ids"], detail["services"] = k.ServiceIDs, names
	}
	h.audit.WriteAuditLogDetail(c.Request.Context(), orgID, middleware.UserIDFrom(c), action, "apikey:"+k.ID, detail)
}

// ProjectOwnershipChecker verifies that a project belongs to the requesting
// org. Satisfied by *project.Service.
type ProjectOwnershipChecker interface {
	CheckOwnership(ctx context.Context, projectID, orgID string) (bool, error)
}

func NewHandler(svc *Service, projects ProjectOwnershipChecker, log *zap.Logger) *Handler {
	return &Handler{svc: svc, projects: projects, log: log}
}

func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	keys := rg.Group("/user/api-keys")
	{
		keys.GET("", h.handleList)
		keys.POST("", h.handleCreate)
		keys.DELETE("/:keyID", h.handleDelete)
	}
	rg.GET("/projects/:projectID/api-keys", h.handleListForProject)
}

// GET /projects/:projectID/api-keys: the keys scoped to a project, a page
// at a time; an owner sees every member's.
func (h *Handler) handleListForProject(c *gin.Context) {
	projectID := c.Param("projectID")
	if owned, err := h.projects.CheckOwnership(c.Request.Context(), projectID, middleware.OrgIDFrom(c)); err != nil || !owned {
		respond.ErrorData(c, http.StatusNotFound, "project not found", gin.H{"code": "not_found"})
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit < 1 || limit > 200 {
		respond.ErrorData(c, http.StatusBadRequest, "limit is 1 to 200", gin.H{"code": "bad_limit"})
		return
	}
	at, id, err := respond.ParseCursor(c.Query("cursor"))
	if err == nil && id != "" {
		_, err = uuid.Parse(id)
	}
	if err != nil {
		respond.ErrorData(c, http.StatusBadRequest, "cursor is not one this API issued", gin.H{"code": "bad_cursor"})
		return
	}
	keys, more, err := h.svc.ListForProject(c.Request.Context(), projectID, middleware.UserIDFrom(c), middleware.MemberRoleFrom(c), at, id, limit)
	if err != nil {
		h.log.Error("failed to list the project's api keys", zap.Error(err), zap.String("project_id", projectID))
		respond.Error(c, http.StatusInternalServerError, "failed to list api keys")
		return
	}
	next := ""
	if more {
		last := keys[len(keys)-1]
		next = respond.Cursor(last.CreatedAt, last.ID)
	}
	respond.JSONPage(c, http.StatusOK, keys, next)
}

func (h *Handler) handleList(c *gin.Context) {
	userID := middleware.UserIDFrom(c)
	keys, err := h.svc.List(c.Request.Context(), userID)
	if err != nil {
		h.log.Error("failed to list api keys", zap.Error(err), zap.String("user_id", userID))
		respond.Error(c, http.StatusInternalServerError, "failed to list api keys")
		return
	}
	respond.JSON(c, http.StatusOK, keys)
}

func (h *Handler) handleCreate(c *gin.Context) {
	// A key never mints another: a leaked one would outlive its own revocation.
	if middleware.ViaAPIKey(c) {
		respond.ErrorData(c, http.StatusForbidden, "creating an API key needs a signed-in session, not an API key", gin.H{"code": "session_required"})
		return
	}
	userID := middleware.UserIDFrom(c)

	var input CreateInput
	if err := c.ShouldBindJSON(&input); errors.Is(err, ErrInvalidExpiry) {
		respond.ErrorData(c, http.StatusBadRequest, err.Error(), gin.H{"code": "invalid_expiry"})
		return
	} else if err != nil {
		respond.Error(c, http.StatusBadRequest, "name is required (1–64 chars)")
		return
	}

	// A project-scoped key: the project must be in the caller's workspace,
	// and only someone who may deploy there can mint a key that deploys.
	if input.ProjectID != "" {
		if middleware.MemberRoleFrom(c) == "viewer" {
			respond.Error(c, http.StatusForbidden, "viewers cannot create project API keys")
			return
		}
		if owned, err := h.projects.CheckOwnership(c.Request.Context(), input.ProjectID, middleware.OrgIDFrom(c)); err != nil || !owned {
			respond.Error(c, http.StatusNotFound, "project not found")
			return
		}
	}

	created, err := h.svc.Create(c.Request.Context(), userID, input)
	if errors.Is(err, ErrInvalidScope) || errors.Is(err, ErrInvalidServices) {
		respond.ErrorData(c, http.StatusBadRequest, err.Error(), gin.H{"code": "invalid_scope"})
		return
	}
	if errors.Is(err, ErrInvalidExpiry) {
		respond.ErrorData(c, http.StatusBadRequest, err.Error(), gin.H{"code": "invalid_expiry"})
		return
	}
	if err != nil {
		h.log.Error("failed to create api key", zap.Error(err), zap.String("user_id", userID))
		respond.Error(c, http.StatusInternalServerError, "failed to create api key — name may already be taken")
		return
	}

	// A project key's project is in the open workspace (checked above); a
	// full account key acts in it.
	h.record(c, middleware.OrgIDFrom(c), "apikey.created", created.APIKey)
	respond.JSON(c, http.StatusCreated, created)
}

func (h *Handler) handleDelete(c *gin.Context) {
	userID := middleware.UserIDFrom(c)
	keyID := c.Param("keyID")

	revoked, err := h.svc.Delete(c.Request.Context(), keyID, userID)
	if errors.Is(err, ErrNotFound) {
		respond.Error(c, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		h.log.Error("failed to delete api key", zap.Error(err), zap.String("key_id", keyID))
		respond.Error(c, http.StatusInternalServerError, "failed to delete api key")
		return
	}
	// A project key's revocation goes to its project's workspace; a full
	// account key's to the open one, where it was acting.
	orgID := revoked.ProjectOrgID
	if orgID == "" {
		orgID = middleware.OrgIDFrom(c)
	}
	h.record(c, orgID, "apikey.revoked", revoked.APIKey)

	respond.NoContent(c)
}
