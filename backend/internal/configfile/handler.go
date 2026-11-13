package configfile

import (
	"context"
	"errors"
	"net/http"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ServiceOwnershipChecker verifies that a service belongs to the requesting org.
// Satisfied by *service.ServiceManager — defined here to avoid a circular import.
type ServiceOwnershipChecker interface {
	CheckOwnership(ctx context.Context, id, orgID string) (bool, error)
}

// AuditWriter records a change in the workspace's audit log; satisfied by
// org.Service.
type AuditWriter interface {
	WriteAuditLogDetail(ctx context.Context, orgID, actorID, action, resource string, detail map[string]any)
}

// Handler is the HTTP adapter for the configfile domain.
type Handler struct {
	svc    *Service
	svcSvc ServiceOwnershipChecker
	audit  AuditWriter
	log    *zap.Logger
}

func NewHandler(svc *Service, svcSvc ServiceOwnershipChecker, log *zap.Logger) *Handler {
	return &Handler{svc: svc, svcSvc: svcSvc, log: log}
}

// WithAudit records config files saved and deleted in the audit log, by
// path: a file's content may hold secrets and is never recorded.
func (h *Handler) WithAudit(a AuditWriter) *Handler {
	h.audit = a
	return h
}

// RegisterRoutes mounts config file routes under /services/:serviceID.
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/services/:serviceID/config-files")
	{
		g.GET("", h.handleList)
		g.PUT("", h.handleUpsert)
		g.DELETE("/:fileID", h.handleDelete)
	}
}

func (h *Handler) handleList(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	serviceID := c.Param("serviceID")

	if owned, err := h.svcSvc.CheckOwnership(c.Request.Context(), serviceID, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}

	files, err := h.svc.List(c.Request.Context(), serviceID)
	if err != nil {
		h.log.Error("failed to list config files", zap.Error(err), zap.String("service_id", serviceID))
		respond.Error(c, http.StatusInternalServerError, "Failed to list config files")
		return
	}

	respond.JSON(c, http.StatusOK, files)
}

func (h *Handler) handleUpsert(c *gin.Context) {
	if middleware.MemberRoleFrom(c) == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	serviceID := c.Param("serviceID")

	if owned, err := h.svcSvc.CheckOwnership(c.Request.Context(), serviceID, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}

	var input UpsertInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "Invalid request body")
		return
	}

	cf, err := h.svc.Upsert(c.Request.Context(), serviceID, input)
	if errors.Is(err, ErrInvalidPath) {
		respond.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		h.log.Error("failed to upsert config file", zap.Error(err), zap.String("service_id", serviceID))
		respond.Error(c, http.StatusInternalServerError, "Failed to save config file")
		return
	}
	h.record(c, "service.config_file_saved", serviceID, cf.Path)

	respond.JSON(c, http.StatusOK, cf)
}

func (h *Handler) handleDelete(c *gin.Context) {
	if middleware.MemberRoleFrom(c) == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	serviceID := c.Param("serviceID")
	fileID := c.Param("fileID")

	if owned, err := h.svcSvc.CheckOwnership(c.Request.Context(), serviceID, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}

	path, err := h.svc.Delete(c.Request.Context(), fileID, serviceID)
	if err != nil {
		h.log.Error("failed to delete config file", zap.Error(err),
			zap.String("service_id", serviceID),
			zap.String("file_id", fileID),
		)
		respond.Error(c, http.StatusInternalServerError, "Failed to delete config file")
		return
	}
	if path != "" {
		h.record(c, "service.config_file_deleted", serviceID, path)
	}

	respond.NoContent(c)
}

// record writes a config file change to the audit log, against its service:
// the path only.
func (h *Handler) record(c *gin.Context, action, serviceID, path string) {
	if h.audit != nil {
		h.audit.WriteAuditLogDetail(c.Request.Context(), middleware.OrgIDFrom(c), middleware.UserIDFrom(c), action,
			"service:"+serviceID, map[string]any{"path": path})
	}
}
