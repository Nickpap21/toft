package deploypolicy

import (
	"errors"
	"net/http"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Handler is the thin HTTP adapter for the deploy policy.
type Handler struct {
	svc *Service
	log *zap.Logger
}

// NewHandler creates a deploy policy Handler.
func NewHandler(svc *Service, log *zap.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

// RegisterRoutes mounts the deploy policy endpoints on a protected group.
// Exception changes and the report are operator-only; the check happens in
// the Service, against the database, on every request.
//
// Granting and revoking an exception ask for the operator's password (D-23's
// re-authentication, D-53, D-56): a session with a recent proof only, never
// an API key.
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	svc := rg.Group("/services/:serviceID/deploy-policy")
	{
		svc.GET("", h.getServicePolicy)
		svc.POST("/exception", h.grantException)
		svc.DELETE("/exception", h.revokeException)
	}
	rg.GET("/deploy-policy/violations", h.report)
}

func (h *Handler) getServicePolicy(c *gin.Context) {
	p, err := h.svc.ServicePolicy(c.Request.Context(), middleware.UserIDFrom(c), middleware.OrgIDFrom(c), c.Param("serviceID"))
	if err != nil {
		h.writeError(c, err, "failed to read the deploy policy")
		return
	}
	respond.JSONVersioned(c, http.StatusOK, p, p.version)
}

// GrantExceptionInput is the POST /services/:serviceID/deploy-policy/exception body.
type GrantExceptionInput struct {
	AllowedRules []string `json:"allowed_rules" binding:"required"`
	Reason       string   `json:"reason" binding:"required,max=1000"`
	ValuesDigest string   `json:"values_digest" binding:"required"`
	// Version has no binding tag: a missing one is its own refusal,
	// version_required.
	Version string `json:"version"`
}

func (h *Handler) grantException(c *gin.Context) {
	if !h.operatorWithPassword(c) {
		return
	}
	var in GrantExceptionInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.Error(c, http.StatusBadRequest, "allowed_rules, reason and values_digest are required")
		return
	}
	e, err := h.svc.GrantException(c.Request.Context(), middleware.UserIDFrom(c), c.Param("serviceID"), in.AllowedRules, in.Reason, in.ValuesDigest, in.Version)
	if err != nil {
		h.writeError(c, err, "failed to grant the exception")
		return
	}
	respond.JSONVersioned(c, http.StatusOK, e, e.version)
}

// RevokeExceptionInput is the optional DELETE body.
type RevokeExceptionInput struct {
	Reason string `json:"reason"`
}

func (h *Handler) revokeException(c *gin.Context) {
	if !h.operatorWithPassword(c) {
		return
	}
	var in RevokeExceptionInput
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&in); err != nil {
			respond.Error(c, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	if err := h.svc.RevokeException(c.Request.Context(), middleware.UserIDFrom(c), c.Param("serviceID"), in.Reason); err != nil {
		h.writeError(c, err, "failed to revoke the exception")
		return
	}
	respond.NoContent(c)
}

func (h *Handler) report(c *gin.Context) {
	r, err := h.svc.Report(c.Request.Context(), middleware.UserIDFrom(c))
	if err != nil {
		h.writeError(c, err, "failed to build the violations report")
		return
	}
	respond.JSON(c, http.StatusOK, r)
}

// operatorWithPassword refuses a non-operator as such, and then asks an
// operator for a recent password confirmation (middleware.RequireReauth).
func (h *Handler) operatorWithPassword(c *gin.Context) bool {
	if err := h.svc.RequireOperator(c.Request.Context(), middleware.UserIDFrom(c)); err != nil {
		h.writeError(c, err, "failed to check the operator")
		return false
	}
	return middleware.RequireReauth(c)
}

func (h *Handler) writeError(c *gin.Context, err error, internalMsg string) {
	if respond.VersionRefused(c, err) {
		return
	}
	switch {
	case errors.Is(err, ErrNotOperator):
		respond.Error(c, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrServiceNotFound):
		respond.Error(c, http.StatusNotFound, "service not found")
	case errors.Is(err, ErrNotFound):
		respond.Error(c, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrInvalidRules), errors.Is(err, ErrReasonRequired):
		respond.Error(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrNoWorkspace):
		respond.Error(c, http.StatusConflict, err.Error())
	case errors.Is(err, ErrValuesChanged):
		respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "values_changed"})
	default:
		h.log.Error(internalMsg, zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, internalMsg)
	}
}
