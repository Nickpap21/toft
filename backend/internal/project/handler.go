package project

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// AuditWriter is satisfied by org.Service; defined here to avoid a circular import.
type AuditWriter interface {
	WriteAuditLog(ctx context.Context, orgID, actorID, action, resource string)
	WriteAuditLogDetail(ctx context.Context, orgID, actorID, action, resource string, detail map[string]any)
}

// InlineServiceCreator answers a create that carries the project's first
// service (7g): the project and the service in one transaction, its
// response and audit. Satisfied by *service.Handler, which knows the
// service's input and errors.
type InlineServiceCreator interface {
	CreateProjectWithService(c *gin.Context, input CreateProjectInput, service json.RawMessage)
}

// Handler is a thin HTTP adapter for the project domain.
type Handler struct {
	service *Service
	audit   AuditWriter
	log     *zap.Logger
	inline  InlineServiceCreator
}

// WithInlineService lets a create carry the project's first service.
// Called from main.go.
func (h *Handler) WithInlineService(i InlineServiceCreator) { h.inline = i }

func NewHandler(service *Service, audit AuditWriter, log *zap.Logger) *Handler {
	return &Handler{service: service, audit: audit, log: log}
}

// RegisterRoutes mounts all project routes on the given Gin router group.
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	projects := rg.Group("/projects")
	{
		projects.GET("", h.list)
		projects.POST("", h.create)
		projects.GET("/:projectID", h.get)
		projects.PUT("/:projectID", h.update)
		projects.DELETE("/:projectID", h.delete)
	}
}

func (h *Handler) list(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	if orgID == "" {
		respond.Error(c, http.StatusForbidden, "no org context")
		return
	}
	projects, err := h.service.ListProjects(c.Request.Context(), orgID)
	if err != nil {
		h.log.Error("list projects", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to list projects")
		return
	}
	// An agent connected to some projects sees those alone (D-100).
	if a, ok := middleware.AgentFrom(c); ok && !a.AllProjects {
		projects = slices.DeleteFunc(projects, func(p Project) bool { return !slices.Contains(a.ProjectIDs, p.ID) })
	}
	respond.JSON(c, http.StatusOK, projects)
}

func (h *Handler) get(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	projectID := c.Param("projectID")

	owned, err := h.service.CheckOwnership(c.Request.Context(), projectID, orgID)
	if err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "project not found")
		return
	}
	p, err := h.service.GetProject(c.Request.Context(), projectID)
	if err != nil {
		respond.Error(c, http.StatusNotFound, "project not found")
		return
	}
	respond.JSON(c, http.StatusOK, p)
}

func (h *Handler) create(c *gin.Context) {
	if role := middleware.MemberRoleFrom(c); role == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	var body struct {
		CreateProjectInput
		Service json.RawMessage `json:"service"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		respond.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	input := body.CreateProjectInput
	orgID := middleware.OrgIDFrom(c)
	userID := middleware.UserIDFrom(c)
	if orgID == "" {
		respond.Error(c, http.StatusForbidden, "no org context")
		return
	}
	if len(body.Service) > 0 && string(body.Service) != "null" {
		if h.inline == nil {
			respond.Error(c, http.StatusBadRequest, "a project can't be created with a service here")
			return
		}
		h.inline.CreateProjectWithService(c, input, body.Service)
		return
	}
	p, err := h.service.CreateProject(c.Request.Context(), orgID, userID, input)
	if err != nil {
		if errors.Is(err, ErrDuplicateName) {
			WriteNameTaken(c)
			return
		}
		h.log.Error("create project", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	// The name too, so the entry still names the project once it is deleted.
	h.audit.WriteAuditLogDetail(c.Request.Context(), orgID, userID, "project.created", "project:"+p.ID, map[string]any{"name": p.Name})
	respond.JSON(c, http.StatusCreated, p)
}

func (h *Handler) update(c *gin.Context) {
	if role := middleware.MemberRoleFrom(c); role == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	projectID := c.Param("projectID")

	owned, err := h.service.CheckOwnership(c.Request.Context(), projectID, orgID)
	if err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "project not found")
		return
	}
	var input UpdateProjectInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	p, err := h.service.UpdateProject(c.Request.Context(), projectID, input)
	if err != nil {
		h.log.Error("update project", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	respond.JSON(c, http.StatusOK, p)
}

// delete removes a project with its services, repositories, issues and
// docs: owners, with the password and the project's name in ?confirm, as a
// repository's delete asks for one repository.
func (h *Handler) delete(c *gin.Context) {
	respond.Long(c, respond.LongWork) // stops and removes containers
	if middleware.MemberRoleFrom(c) != "owner" {
		respond.Error(c, http.StatusForbidden, "only owners can delete a project")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	projectID := c.Param("projectID")

	owned, err := h.service.CheckOwnership(c.Request.Context(), projectID, orgID)
	if err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "project not found")
		return
	}
	if !middleware.RequireReauth(c) {
		return
	}

	// The name, for the confirmation and for the audit log once the project
	// is gone.
	p, err := h.service.GetProject(c.Request.Context(), projectID)
	if err != nil {
		respond.Error(c, http.StatusNotFound, "project not found")
		return
	}
	if c.Query("confirm") != p.Name {
		respond.ErrorData(c, http.StatusBadRequest, "type the project's name to confirm", gin.H{"code": "confirm_name"})
		return
	}
	if err := h.service.DeleteProject(c.Request.Context(), projectID); err != nil {
		h.log.Error("delete project", zap.Error(err))
		if errors.Is(err, ErrServicesNotRemoved) {
			respond.Error(c, http.StatusBadGateway, err.Error())
			return
		}
		respond.Error(c, http.StatusInternalServerError, "failed to delete project")
		return
	}
	h.audit.WriteAuditLogDetail(c.Request.Context(), orgID, middleware.UserIDFrom(c), "project.deleted", "project:"+projectID, map[string]any{"name": p.Name})
	respond.NoContent(c)
}

// WriteNameTaken answers a project name the workspace already has: 409
// project_name_taken.
func WriteNameTaken(c *gin.Context) {
	respond.ErrorData(c, http.StatusConflict, ErrDuplicateName.Error(), gin.H{"code": "project_name_taken"})
}
