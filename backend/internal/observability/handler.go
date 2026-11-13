package observability

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ProjectChecker verifies that a project belongs to an org. Satisfied by
// project.Service; defined here to avoid a circular import.
type ProjectChecker interface {
	CheckOwnership(ctx context.Context, id, orgID string) (bool, error)
}

// AuditWriter records a change in the workspace's audit log; satisfied by
// org.Service.
type AuditWriter interface {
	WriteAuditLogDetail(ctx context.Context, orgID, actorID, action, resource string, detail map[string]any)
}

// Handler is the HTTP adapter for the observability domain.
type Handler struct {
	svc      *Service
	projects ProjectChecker
	audit    AuditWriter
	log      *zap.Logger
}

func NewHandler(svc *Service, projects ProjectChecker, log *zap.Logger) *Handler {
	return &Handler{svc: svc, projects: projects, log: log}
}

// WithAudit records changes of the workspace's stack in the audit log.
func (h *Handler) WithAudit(a AuditWriter) *Handler {
	h.audit = a
	return h
}

// workspaceOwner refuses all but owners: the workspace's Loki is where
// every container of the workspace ships its logs.
func workspaceOwner(c *gin.Context) bool {
	if middleware.MemberRoleFrom(c) != "owner" {
		respond.Error(c, http.StatusForbidden, "only owners can change the workspace's logs and metrics stack")
		return false
	}
	return true
}

// redacted is raw for the audit log, without a password it may carry.
func redacted(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	// A token can sit in the query string as well as in the userinfo.
	u.RawQuery, u.Fragment = "", ""
	return u.Redacted()
}

func (h *Handler) audited(c *gin.Context, orgID, kind string, detail map[string]any) {
	if h.audit != nil {
		h.audit.WriteAuditLogDetail(c.Request.Context(), orgID, middleware.UserIDFrom(c), "observability."+kind+"_changed", "observability:"+kind, detail)
	}
}

// ownProject returns the route's project if it belongs to the caller's
// workspace; otherwise it writes the 403.
func (h *Handler) ownProject(c *gin.Context) (orgID, projectID string, ok bool) {
	orgID, projectID = middleware.OrgIDFrom(c), c.Param("projectID")
	if owned, err := h.projects.CheckOwnership(c.Request.Context(), projectID, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "project not found")
		return "", "", false
	}
	return orgID, projectID, true
}

// RegisterRoutes mounts observability routes.
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	// Org-scoped
	r.GET("/observability", h.handleGetOrg)
	r.GET("/observability/projects", h.handleListProjects)
	r.PUT("/observability/loki", h.handleUpsertLoki)
	r.PUT("/observability/grafana", h.handleUpsertGrafana)
	r.PUT("/observability/prometheus", h.handleUpsertPrometheus)
	r.DELETE("/observability/loki", h.handleUnregister("loki"))
	r.DELETE("/observability/grafana", h.handleUnregister("grafana"))
	r.DELETE("/observability/prometheus", h.handleUnregister("prometheus"))

	// Project-scoped
	r.GET("/projects/:projectID/observability", h.handleGetProject)
	r.PUT("/projects/:projectID/observability/loki", h.handleUpsertProjectLoki)
	r.PUT("/projects/:projectID/observability/grafana", h.handleUpsertProjectGrafana)
}

// handleUnregister forgets the workspace's Loki, Grafana or Prometheus
// (D-162): the service keeps running, Toft stops treating it as the
// workspace's. Owners, as setting it.
func (h *Handler) handleUnregister(kind string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !workspaceOwner(c) {
			return
		}
		orgID := middleware.OrgIDFrom(c)
		if err := h.svc.Unregister(c.Request.Context(), orgID, kind); err != nil {
			h.log.Error("unregister observability service", zap.Error(err), zap.String("kind", kind))
			respond.Error(c, http.StatusInternalServerError, "failed to unregister it")
			return
		}
		h.audited(c, orgID, kind, gin.H{"unregistered": true})
		respond.NoContent(c)
	}
}

func (h *Handler) handleGetOrg(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	cfg, err := h.svc.GetForOrg(c.Request.Context(), orgID)
	if err != nil {
		h.log.Error("get org observability config", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to get observability config")
		return
	}
	if cfg == nil {
		respond.JSON(c, http.StatusOK, gin.H{})
		return
	}
	respond.JSON(c, http.StatusOK, cfg)
}

// handleListProjects lists each project's own Loki and Grafana for the
// Observability page's By project card. Every member may.
func (h *Handler) handleListProjects(c *gin.Context) {
	list, err := h.svc.ProjectSettings(c.Request.Context(), middleware.OrgIDFrom(c))
	if err != nil {
		h.log.Error("list project observability", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to list the projects' observability settings")
		return
	}
	respond.JSON(c, http.StatusOK, list)
}

func (h *Handler) handleGetProject(c *gin.Context) {
	orgID, projectID, ok := h.ownProject(c)
	if !ok {
		return
	}
	cfg, err := h.svc.GetForProject(c.Request.Context(), orgID, projectID)
	if err != nil {
		h.log.Error("get project observability config", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to get observability config")
		return
	}
	if cfg == nil {
		respond.JSON(c, http.StatusOK, gin.H{})
		return
	}
	respond.JSON(c, http.StatusOK, cfg)
}

func (h *Handler) handleUpsertLoki(c *gin.Context) {
	if !workspaceOwner(c) {
		return
	}
	orgID := middleware.OrgIDFrom(c)
	var input UpsertLokiInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	cfg, err := h.svc.UpsertLoki(c.Request.Context(), orgID, "", input)
	if errors.Is(err, ErrInvalidURL) {
		respond.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if errors.Is(err, ErrServiceNotFound) {
		respond.Error(c, http.StatusNotFound, err.Error())
		return
	}
	if errors.Is(err, ErrNotWorkspaceService) {
		respond.ErrorData(c, http.StatusBadRequest, err.Error(), gin.H{"code": "not_workspace_service"})
		return
	}
	if err != nil {
		h.log.Error("upsert loki", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to register Loki")
		return
	}
	h.audited(c, orgID, "loki", gin.H{"service_id": input.ServiceID, "url": redacted(input.LokiURL)})
	respond.JSON(c, http.StatusOK, cfg)
}

func (h *Handler) handleUpsertGrafana(c *gin.Context) {
	if !workspaceOwner(c) {
		return
	}
	orgID := middleware.OrgIDFrom(c)
	var input UpsertGrafanaInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	cfg, err := h.svc.UpsertGrafana(c.Request.Context(), orgID, "", input)
	if errors.Is(err, ErrInvalidURL) {
		respond.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if errors.Is(err, ErrServiceNotFound) {
		respond.Error(c, http.StatusNotFound, err.Error())
		return
	}
	if errors.Is(err, ErrNotWorkspaceService) {
		respond.ErrorData(c, http.StatusBadRequest, err.Error(), gin.H{"code": "not_workspace_service"})
		return
	}
	if err != nil {
		h.log.Error("upsert grafana", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to register Grafana")
		return
	}
	h.audited(c, orgID, "grafana", gin.H{"service_id": input.ServiceID, "url": redacted(input.GrafanaURL)})
	respond.JSON(c, http.StatusOK, cfg)
}

func (h *Handler) handleUpsertPrometheus(c *gin.Context) {
	if !workspaceOwner(c) {
		return
	}
	orgID := middleware.OrgIDFrom(c)
	var input UpsertPrometheusInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	cfg, err := h.svc.UpsertPrometheus(c.Request.Context(), orgID, "", input)
	if errors.Is(err, ErrServiceNotFound) {
		respond.Error(c, http.StatusNotFound, err.Error())
		return
	}
	if errors.Is(err, ErrNotWorkspaceService) {
		respond.ErrorData(c, http.StatusBadRequest, err.Error(), gin.H{"code": "not_workspace_service"})
		return
	}
	if err != nil {
		h.log.Error("upsert prometheus", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to register Prometheus")
		return
	}
	h.audited(c, orgID, "prometheus", gin.H{"service_id": input.ServiceID})
	respond.JSON(c, http.StatusOK, cfg)
}

func (h *Handler) handleUpsertProjectLoki(c *gin.Context) {
	if middleware.MemberRoleFrom(c) == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	orgID, projectID, ok := h.ownProject(c)
	if !ok {
		return
	}
	var input UpsertLokiInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	cfg, err := h.svc.UpsertLoki(c.Request.Context(), orgID, projectID, input)
	if errors.Is(err, ErrInvalidURL) {
		respond.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if errors.Is(err, ErrServiceNotFound) {
		respond.Error(c, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		h.log.Error("upsert project loki", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to register Loki")
		return
	}
	respond.JSON(c, http.StatusOK, cfg)
}

func (h *Handler) handleUpsertProjectGrafana(c *gin.Context) {
	if middleware.MemberRoleFrom(c) == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	orgID, projectID, ok := h.ownProject(c)
	if !ok {
		return
	}
	var input UpsertGrafanaInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	cfg, err := h.svc.UpsertGrafana(c.Request.Context(), orgID, projectID, input)
	if errors.Is(err, ErrInvalidURL) {
		respond.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if errors.Is(err, ErrServiceNotFound) {
		respond.Error(c, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		h.log.Error("upsert project grafana", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to register Grafana")
		return
	}
	respond.JSON(c, http.StatusOK, cfg)
}
