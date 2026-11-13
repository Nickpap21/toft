package instance

import (
	"errors"
	"net/http"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
)

// registerSetupRoutes mounts the setup wizard's endpoints (D-62) on the
// operator group. The domain and the recovery kit need a recent password
// confirmation (D-23); every change needs a signed-in session.
func (h *Handler) registerSetupRoutes(g *gin.RouterGroup) {
	g.GET("/setup", h.getSetup)
	g.PATCH("/setup", h.patchSetup)
	g.PUT("/setup/domain", h.putSetupDomain)
	g.POST("/setup/domain-check", h.checkSetupDomain)
	g.PUT("/setup/role", h.putSetupRole)
	g.GET("/recovery-kit", h.getRecoveryKit)
}

func (h *Handler) getProbe(c *gin.Context) {
	respond.JSON(c, http.StatusOK, gin.H{"id": h.svc.ProbeID()})
}

func (h *Handler) getSetup(c *gin.Context) {
	st, err := h.svc.Setup(c.Request.Context(), middleware.UserIDFrom(c))
	if err != nil {
		h.writeSetupError(c, err, "failed to read setup")
		return
	}
	respond.JSON(c, http.StatusOK, st)
}

func (h *Handler) patchSetup(c *gin.Context) {
	if !sessionOnly(c) {
		return
	}
	var input SetupPatch
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, ErrInvalidPatch.Error())
		return
	}
	st, err := h.svc.PatchSetup(c.Request.Context(), middleware.UserIDFrom(c), input)
	if err != nil {
		h.writeSetupError(c, err, "failed to update setup")
		return
	}
	respond.JSON(c, http.StatusOK, st)
}

// PanelDomainInput is the body of POST /instance/setup/domain-check.
type PanelDomainInput struct {
	Domain string `json:"domain"`
}

// SetPanelDomainInput is the body of PUT /instance/setup/domain.
type SetPanelDomainInput struct {
	Domain string `json:"domain"`
	// DomainChecked: the operator says the domain works, so it isn't probed.
	DomainChecked bool `json:"domain_checked"`
}

func (h *Handler) putSetupDomain(c *gin.Context) {
	var input SetPanelDomainInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, ErrInvalidDomain.Error())
		return
	}
	ctx, actor := c.Request.Context(), middleware.UserIDFrom(c)
	// The operator check first, so someone who may not change it is never
	// asked for a password.
	if err := h.svc.requireOperator(ctx, actor); err != nil {
		h.writeSetupError(c, err, "failed to set the panel's domain")
		return
	}
	if !middleware.RequireReauth(c) {
		return
	}
	st, err := h.svc.SetPanelDomain(ctx, actor, input.Domain, input.DomainChecked)
	if err != nil {
		h.writeSetupError(c, err, "failed to set the panel's domain")
		return
	}
	respond.JSON(c, http.StatusOK, st)
}

func (h *Handler) checkSetupDomain(c *gin.Context) {
	var input PanelDomainInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, ErrInvalidDomain.Error())
		return
	}
	check, err := h.svc.CheckPanelDomain(c.Request.Context(), middleware.UserIDFrom(c), input.Domain)
	if err != nil {
		h.writeSetupError(c, err, "failed to check the domain")
		return
	}
	respond.JSON(c, http.StatusOK, check)
}

// ServerRoleInput is the body of PUT /instance/setup/role.
type ServerRoleInput struct {
	Role string `json:"role"`
}

func (h *Handler) putSetupRole(c *gin.Context) {
	if !sessionOnly(c) {
		return
	}
	var input ServerRoleInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, ErrInvalidRole.Error())
		return
	}
	st, err := h.svc.SetServerRole(c.Request.Context(), middleware.UserIDFrom(c), input.Role)
	if err != nil {
		h.writeSetupError(c, err, "failed to set the server role")
		return
	}
	respond.JSON(c, http.StatusOK, st)
}

func (h *Handler) getRecoveryKit(c *gin.Context) {
	ctx, actor := c.Request.Context(), middleware.UserIDFrom(c)
	if err := h.svc.requireOperator(ctx, actor); err != nil {
		h.writeSetupError(c, err, "failed to read the recovery kit")
		return
	}
	if !middleware.RequireReauth(c) {
		return
	}
	kit, err := h.svc.RecoveryKit(ctx, actor)
	if err != nil {
		h.writeSetupError(c, err, "failed to read the recovery kit")
		return
	}
	c.Header("Cache-Control", "no-store")
	respond.JSON(c, http.StatusOK, kit)
}

func (h *Handler) writeSetupError(c *gin.Context, err error, internalMsg string) {
	var inUse *PanelHostInUseError
	var tooSmall *PanelHostTooSmallError
	switch {
	case errors.Is(err, ErrNotOperator):
		respond.Error(c, http.StatusForbidden, "only instance operators can set up the instance")
	case errors.Is(err, ErrInvalidDomain), errors.Is(err, ErrInvalidRole), errors.Is(err, ErrInvalidPatch), errors.Is(err, ErrKitFingerprintRequired):
		respond.Error(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrSetupIncomplete):
		respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "setup_incomplete"})
	case errors.Is(err, ErrKitChanged):
		respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "kit_changed"})
	case errors.Is(err, ErrPanelDomainUnreachable):
		respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "panel_domain_unreachable"})
	case errors.As(err, &inUse):
		respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "panel_host_in_use"})
	case errors.As(err, &tooSmall):
		respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "panel_host_too_small"})
	default:
		h.writeError(c, err, "only instance operators can set up the instance", internalMsg)
	}
}
