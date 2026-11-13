package sharedvolume

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Handler is the HTTP adapter for shared volumes. Managing volumes is for
// workspace owners (a volume's limit is a share of a server's disk);
// mounting one is service configuration, for owners and developers.
type Handler struct {
	svc   *Service
	audit AuditWriter
	log   *zap.Logger
}

// AuditWriter records a change in the workspace's audit log; satisfied by
// org.Service.
type AuditWriter interface {
	WriteAuditLogDetail(ctx context.Context, orgID, actorID, action, resource string, detail map[string]any)
}

func NewHandler(svc *Service, log *zap.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

// WithAudit records a service's shared mounts changing in the audit log.
func (h *Handler) WithAudit(a AuditWriter) *Handler {
	h.audit = a
	return h
}

// RegisterRoutes mounts the routes on the protected group. Project-scoped
// API keys reach none of them (apikey.routeScopes).
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/shared-volumes", h.list)
	rg.POST("/shared-volumes", h.create)
	rg.PATCH("/shared-volumes/:volumeID", h.updateLimit)
	rg.DELETE("/shared-volumes/:volumeID", h.delete)
	rg.GET("/services/:serviceID/shared-volumes", h.serviceMounts)
	rg.PUT("/services/:serviceID/shared-volumes", h.setServiceMounts)
}

func (h *Handler) list(c *gin.Context) {
	vols, err := h.svc.List(c.Request.Context(), middleware.OrgIDFrom(c))
	if err != nil {
		h.fail(c, err)
		return
	}
	respond.JSON(c, http.StatusOK, vols)
}

// owner writes the 403 unless the caller owns the workspace.
func owner(c *gin.Context) bool {
	if middleware.MemberRoleFrom(c) != "owner" {
		respond.Error(c, http.StatusForbidden, "only workspace owners manage shared volumes")
		return false
	}
	return true
}

func (h *Handler) create(c *gin.Context) {
	if !owner(c) {
		return
	}
	var in CreateInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	v, err := h.svc.Create(c.Request.Context(), middleware.OrgIDFrom(c), in)
	if err != nil {
		h.fail(c, err)
		return
	}
	respond.JSON(c, http.StatusCreated, v)
}

func (h *Handler) updateLimit(c *gin.Context) {
	if !owner(c) {
		return
	}
	var in struct {
		SizeLimitBytes int64 `json:"size_limit_bytes"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	v, err := h.svc.UpdateLimit(c.Request.Context(), middleware.OrgIDFrom(c), c.Param("volumeID"), in.SizeLimitBytes)
	if err != nil {
		h.fail(c, err)
		return
	}
	respond.JSON(c, http.StatusOK, v)
}

func (h *Handler) delete(c *gin.Context) {
	if !owner(c) {
		return
	}
	if err := h.svc.Delete(c.Request.Context(), middleware.OrgIDFrom(c), c.Param("volumeID")); err != nil {
		h.fail(c, err)
		return
	}
	respond.NoContent(c)
}

func (h *Handler) serviceMounts(c *gin.Context) {
	mounts, version, err := h.svc.ServiceMountsVersioned(c.Request.Context(), middleware.OrgIDFrom(c), c.Param("serviceID"))
	if err != nil {
		h.fail(c, err)
		return
	}
	respond.JSONVersioned(c, http.StatusOK, mounts, version)
}

func (h *Handler) setServiceMounts(c *gin.Context) {
	if middleware.MemberRoleFrom(c) == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	var in struct {
		Mounts []Mount `json:"mounts"`
		// Version is the one from the list it replaces; no binding tag, so a
		// missing one is its own refusal, version_required.
		Version string `json:"version"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	orgID, serviceID := middleware.OrgIDFrom(c), c.Param("serviceID")
	// What it mounted, to audit only a change; a failed read audits the save.
	before, _ := h.svc.ServiceMounts(c.Request.Context(), orgID, serviceID)
	mounts, version, err := h.svc.SetServiceMounts(c.Request.Context(), orgID, serviceID, in.Version, in.Mounts)
	if err != nil {
		h.fail(c, err)
		return
	}
	same := slices.EqualFunc(before, mounts, func(a, b Mount) bool {
		return a.VolumeID == b.VolumeID && a.Target == b.Target && a.ReadOnly == b.ReadOnly
	})
	if !same && h.audit != nil {
		h.audit.WriteAuditLogDetail(c.Request.Context(), orgID, middleware.UserIDFrom(c), "service.shared_volumes_changed",
			"service:"+serviceID, map[string]any{"mounts": mounts})
	}
	respond.JSONVersioned(c, http.StatusOK, mounts, version)
}

// fail answers an error: 404 for anything outside the workspace (never 403,
// so IDs reveal nothing), 409 for conflicts, 400 for invalid input.
func (h *Handler) fail(c *gin.Context, err error) {
	if respond.VersionRefused(c, err) {
		return
	}
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrUnknownServer):
		respond.Error(c, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrNameTaken), errors.Is(err, ErrInUse), errors.Is(err, ErrWrongServer), errors.Is(err, ErrCompose):
		respond.Error(c, http.StatusConflict, err.Error())
	case errors.Is(err, ErrInvalid):
		respond.Error(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, server.ErrServerNotReady):
		respond.ErrorData(c, http.StatusConflict, server.ErrServerNotReady.Error(), gin.H{"code": "server_not_ready"})
	default:
		h.log.Error("shared volumes", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "shared volumes: something went wrong")
	}
}
