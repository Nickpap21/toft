package resources

import (
	"context"
	"errors"
	"net/http"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// AuditWriter is satisfied by org.Service.
type AuditWriter interface {
	WriteAuditLog(ctx context.Context, orgID, actorID, action, resource string)
}

// Handler is the HTTP adapter for usage, suggestions and compose limits
// (D-41). Every member of the workspace reads; owners and developers set
// compose limits.
type Handler struct {
	svc   *Service
	audit AuditWriter
	log   *zap.Logger
}

func NewHandler(svc *Service, audit AuditWriter, log *zap.Logger) *Handler {
	return &Handler{svc: svc, audit: audit, log: log}
}

// RegisterRoutes mounts the routes on the protected group. Project-scoped
// API keys reach none of them (apikey.routeScopes).
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/services/:serviceID/resources", h.serviceResources)
	rg.PUT("/services/:serviceID/compose-limits", h.setComposeLimits)
	rg.GET("/servers/:serverID/resources", h.serverResources)
	rg.GET("/resources/default-limit", h.defaultLimit)
}

// defaultLimit is the memory limit a new service gets (D-41), for the New
// service page: ?addon_slug= and ?image= name what it runs.
func (h *Handler) defaultLimit(c *gin.Context) {
	respond.JSON(c, http.StatusOK, h.svc.DefaultLimit(c.Query("addon_slug"), c.Query("image")))
}

func (h *Handler) serviceResources(c *gin.Context) {
	out, err := h.svc.ServiceResources(c.Request.Context(), middleware.OrgIDFrom(c), c.Param("serviceID"))
	if err != nil {
		h.fail(c, err)
		return
	}
	respond.JSONVersioned(c, http.StatusOK, out, out.version)
}

func (h *Handler) setComposeLimits(c *gin.Context) {
	if middleware.MemberRoleFrom(c) == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	var in struct {
		Limits map[string]ComposeLimit `json:"limits"`
		// Version is the one from the limits it replaces; no binding tag, so
		// a missing one is its own refusal, version_required.
		Version string `json:"version"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	orgID, serviceID := middleware.OrgIDFrom(c), c.Param("serviceID")
	out, err := h.svc.SetComposeLimits(c.Request.Context(), orgID, serviceID, in.Version, in.Limits)
	if err != nil {
		h.fail(c, err)
		return
	}
	h.audit.WriteAuditLog(c.Request.Context(), orgID, middleware.UserIDFrom(c), "service.compose_limits.updated", "service:"+serviceID)
	respond.JSONVersioned(c, http.StatusOK, out, out.version)
}

func (h *Handler) serverResources(c *gin.Context) {
	out, err := h.svc.ServerResources(c.Request.Context(), middleware.OrgIDFrom(c), c.Param("serverID"))
	if err != nil {
		h.fail(c, err)
		return
	}
	respond.JSON(c, http.StatusOK, out)
}

// fail answers an error: 404 for a service or server outside the workspace
// (so IDs reveal nothing), 400 for invalid limits.
func (h *Handler) fail(c *gin.Context, err error) {
	if respond.VersionRefused(c, err) {
		return
	}
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrServerNotFound):
		respond.Error(c, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrInvalid):
		respond.Error(c, http.StatusBadRequest, err.Error())
	default:
		h.log.Error("resources", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "resources: something went wrong")
	}
}
