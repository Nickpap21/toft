package domain

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

// ─── Handler ──────────────────────────────────────────────────────────────────

type Handler struct {
	svc            *Service
	checker        *Checker
	serviceManager ServiceOwnershipChecker
	audit          AuditWriter
	log            *zap.Logger
}

func NewHandler(svc *Service, checker *Checker, sm ServiceOwnershipChecker, log *zap.Logger) *Handler {
	return &Handler{svc: svc, checker: checker, serviceManager: sm, log: log}
}

// WithAudit records domains added and removed in the audit log.
func (h *Handler) WithAudit(a AuditWriter) *Handler {
	h.audit = a
	return h
}

func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	r.GET("/domains", h.handleListWorkspace)
	serviceDomains := r.Group("/services/:serviceID/domains")
	{
		serviceDomains.POST("", h.handleCreate)
		serviceDomains.GET("", h.handleList)
		serviceDomains.GET("/check", h.handleCheck)
		serviceDomains.DELETE("/:id", h.handleDelete)
	}
}

// handleListWorkspace lists every domain of the active workspace's services
// (the Domains page, D-48). Every member may, viewers included; DNS and
// certificates come from each service's check.
func (h *Handler) handleListWorkspace(c *gin.Context) {
	domains, err := h.svc.ListForOrg(c.Request.Context(), middleware.OrgIDFrom(c))
	if err != nil {
		h.log.Error("failed to list the workspace's domains", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "Failed to list domains")
		return
	}
	respond.JSON(c, http.StatusOK, domains)
}

// handleCheck looks up the service's domains' DNS, certificates and
// routing (Checker.Check). Every member may, viewers included.
func (h *Handler) handleCheck(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	serviceID := c.Param("serviceID")
	if owned, err := h.serviceManager.CheckOwnership(c.Request.Context(), serviceID, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}
	check, err := h.checker.Check(c.Request.Context(), serviceID, orgID)
	if err != nil {
		h.log.Error("failed to check domains", zap.Error(err), zap.String("service_id", serviceID))
		respond.Error(c, http.StatusInternalServerError, "failed to check the domains")
		return
	}
	respond.JSON(c, http.StatusOK, check)
}

func (h *Handler) handleCreate(c *gin.Context) {
	if middleware.MemberRoleFrom(c) == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	serviceID := c.Param("serviceID")

	if owned, err := h.serviceManager.CheckOwnership(c.Request.Context(), serviceID, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}

	var input CreateInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "Invalid request body")
		return
	}

	domain, err := h.svc.Create(c.Request.Context(), serviceID, middleware.UserIDFrom(c), input)
	if errors.Is(err, ErrInvalidHostname) {
		respond.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if errors.Is(err, ErrHostnameTaken) {
		respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "hostname_taken"})
		return
	}
	if errors.Is(err, ErrNoPort) {
		respond.Error(c, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		h.log.Error("failed to create domain", zap.Error(err), zap.String("service_id", serviceID))
		respond.Error(c, http.StatusInternalServerError, "Failed to create domain")
		return
	}
	h.record(c, "service.domain_added", serviceID, domain.Hostname)

	respond.JSON(c, http.StatusCreated, domain)
}

// record writes a domain change to the audit log, against its service.
func (h *Handler) record(c *gin.Context, action, serviceID, hostname string) {
	if h.audit != nil {
		h.audit.WriteAuditLogDetail(c.Request.Context(), middleware.OrgIDFrom(c), middleware.UserIDFrom(c), action,
			"service:"+serviceID, map[string]any{"hostname": hostname})
	}
}

func (h *Handler) handleList(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	serviceID := c.Param("serviceID")

	if owned, err := h.serviceManager.CheckOwnership(c.Request.Context(), serviceID, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}

	domains, err := h.svc.List(c.Request.Context(), serviceID)
	if err != nil {
		h.log.Error("failed to list domains", zap.Error(err), zap.String("service_id", serviceID))
		respond.Error(c, http.StatusInternalServerError, "Failed to list domains")
		return
	}

	respond.JSON(c, http.StatusOK, domains)
}

func (h *Handler) handleDelete(c *gin.Context) {
	if middleware.MemberRoleFrom(c) == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	serviceID := c.Param("serviceID")
	id := c.Param("id")

	if owned, err := h.serviceManager.CheckOwnership(c.Request.Context(), serviceID, orgID); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "service not found")
		return
	}

	hostname, err := h.svc.Delete(c.Request.Context(), serviceID, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			respond.Error(c, http.StatusNotFound, err.Error())
			return
		}
		h.log.Error("failed to delete domain", zap.Error(err), zap.String("domain_id", id))
		respond.Error(c, http.StatusInternalServerError, "Failed to delete domain")
		return
	}
	h.record(c, "service.domain_removed", serviceID, hostname)

	respond.NoContent(c)
}
