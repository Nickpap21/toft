package reveal

import (
	"errors"
	"net/http"
	"time"

	"github.com/Nickpap21/toft/backend/internal/catalog"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type Handler struct {
	svc *Service
	log *zap.Logger
}

func NewHandler(svc *Service, log *zap.Logger) *Handler { return &Handler{svc: svc, log: log} }

// RegisterRoutes: every route needs a signed-in session (never an API key
// or an agent), and the value also a confirmed password (D-160).
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/services/:serviceID/admin-password")
	g.GET("", h.pending)
	g.GET("/value", h.value)
	g.DELETE("", h.dismiss)
	g.POST("", h.reset)
}

type pendingResponse struct {
	Pending bool `json:"pending"`
	// Reset is how the service's admin login is reset: app, deploy, or
	// absent for none; User its login's user. Both also without a copy.
	Reset     string     `json:"reset,omitempty"`
	User      string     `json:"user,omitempty"`
	What      string     `json:"what,omitempty"`
	Key       string     `json:"key,omitempty"`
	Reason    string     `json:"reason,omitempty"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type valueResponse struct {
	What     string `json:"what"`
	Key      string `json:"key,omitempty"`
	User     string `json:"user,omitempty"`
	Password string `json:"password"`
	// Deploy: a reset that takes effect at the service's next deploy.
	Deploy bool `json:"deploy,omitempty"`
}

// what names a copy for the screen: the service's admin password, or its
// temporary login (no key).
func what(r *Reveal) string {
	if r.Key == "" {
		return "temporary_login"
	}
	return "admin_password"
}

func session(c *gin.Context) bool {
	if _, ok := middleware.SessionFrom(c); !ok {
		respond.ErrorData(c, http.StatusForbidden, "this needs a signed-in session, not an API key or an agent", gin.H{"code": "session_required"})
		return false
	}
	return true
}

func (h *Handler) pending(c *gin.Context) {
	if !session(c) {
		return
	}
	ctx, org, role, id := c.Request.Context(), middleware.OrgIDFrom(c), middleware.MemberRoleFrom(c), c.Param("serviceID")
	r, err := h.svc.Pending(ctx, org, middleware.UserIDFrom(c), role, id)
	if err != nil && !errors.Is(err, ErrNone) {
		h.fail(c, err)
		return
	}
	login, lerr := h.svc.Login(ctx, org, role, id)
	if lerr != nil {
		h.fail(c, lerr)
		return
	}
	out := pendingResponse{Reset: login.Reset, User: login.User}
	if err == nil {
		created, expires := r.CreatedAt, r.ExpiresAt()
		out.Pending, out.What, out.Key, out.Reason, out.CreatedAt, out.ExpiresAt = true, what(r), r.Key, r.Reason, &created, &expires
		if r.Key == "" {
			out.User = catalog.TemporaryLoginUser
		}
	}
	respond.JSON(c, http.StatusOK, out)
}

func (h *Handler) value(c *gin.Context) {
	if !session(c) {
		return
	}
	// Who may and whether there is one before the password (RequireReauth).
	if _, err := h.svc.Pending(c.Request.Context(), middleware.OrgIDFrom(c), middleware.UserIDFrom(c), middleware.MemberRoleFrom(c), c.Param("serviceID")); err != nil {
		h.fail(c, err)
		return
	}
	if !middleware.RequireReauth(c) {
		return
	}
	r, err := h.svc.Open(c.Request.Context(), middleware.OrgIDFrom(c), middleware.UserIDFrom(c), middleware.MemberRoleFrom(c), c.Param("serviceID"))
	if err != nil {
		h.fail(c, err)
		return
	}
	out := valueResponse{What: what(r), Key: r.Key, Password: r.Password, User: catalog.TemporaryLoginUser}
	if r.Key != "" {
		login, err := h.svc.Login(c.Request.Context(), middleware.OrgIDFrom(c), middleware.MemberRoleFrom(c), c.Param("serviceID"))
		if err != nil {
			h.fail(c, err)
			return
		}
		out.User = login.User
	}
	c.Header("Cache-Control", "no-store")
	respond.JSON(c, http.StatusOK, out)
}

func (h *Handler) dismiss(c *gin.Context) {
	if !session(c) {
		return
	}
	if err := h.svc.Dismiss(c.Request.Context(), middleware.OrgIDFrom(c), middleware.UserIDFrom(c), c.Param("serviceID")); err != nil {
		h.fail(c, err)
		return
	}
	respond.NoContent(c)
}

// reset gives the app login a new password, returned once and kept like a
// created one. The protected confirm is the screen's (D-143).
func (h *Handler) reset(c *gin.Context) {
	if !session(c) {
		return
	}
	ctx, org, role, id := c.Request.Context(), middleware.OrgIDFrom(c), middleware.MemberRoleFrom(c), c.Param("serviceID")
	// Who may and whether this app has Reset before the password.
	login, err := h.svc.Login(ctx, org, role, id)
	if err != nil {
		h.fail(c, err)
		return
	}
	if login.Reset == "" {
		h.fail(c, service.ErrNoReset)
		return
	}
	if !middleware.RequireReauth(c) {
		return
	}
	r, err := h.svc.Reset(ctx, org, middleware.UserIDFrom(c), role, id)
	if err != nil {
		h.fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	respond.JSON(c, http.StatusOK, valueResponse{What: "admin_password", Key: r.Key, User: r.User, Password: r.Password, Deploy: r.Deploy})
}

func (h *Handler) fail(c *gin.Context, err error) {
	var refused *service.ResetFailedError
	switch {
	case errors.As(err, &refused):
		respond.ErrorData(c, http.StatusBadGateway, "the app refused the new password, so nothing changed", gin.H{"code": "reset_failed", "output": refused.Output})
	case errors.Is(err, service.ErrResetUnconfirmed):
		respond.ErrorData(c, http.StatusBadGateway, "the app's answer didn't arrive: it most likely has the new password, which waits on Overview; if it doesn't sign in, reset again", gin.H{"code": "reset_unconfirmed"})
	case errors.Is(err, service.ErrNoReset):
		respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "no_reset"})
	case errors.Is(err, service.ErrNotRunning):
		respond.ErrorData(c, http.StatusConflict, "the app isn't running: deploy it, then reset its password", gin.H{"code": "not_running"})
	case errors.Is(err, ErrNotFound):
		respond.ErrorData(c, http.StatusNotFound, "service not found", gin.H{"code": "not_found"})
	case errors.Is(err, ErrForbidden):
		respond.ErrorData(c, http.StatusForbidden, err.Error(), gin.H{"code": "forbidden"})
	case errors.Is(err, ErrNone):
		respond.ErrorData(c, http.StatusNotFound, err.Error(), gin.H{"code": "no_admin_password"})
	default:
		h.log.Error("admin password", zap.Error(err), zap.String("service_id", c.Param("serviceID")))
		respond.Error(c, http.StatusInternalServerError, "failed to read the admin password")
	}
}
