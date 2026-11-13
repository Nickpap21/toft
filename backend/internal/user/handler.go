package user

import (
	"errors"
	"net/http"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

type Handler struct {
	svc    *Service
	tokens TokenIssuer
	log    *zap.Logger
}

// TokenIssuer mints a session token for a user. Satisfied by auth.Service.
type TokenIssuer interface {
	IssueToken(userID string) (string, error)
}

func NewHandler(svc *Service, log *zap.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

// WithTokenIssuer lets a password change answer with a fresh token: the
// change revokes every token issued before it, the caller's included.
func (h *Handler) WithTokenIssuer(t TokenIssuer) *Handler {
	h.tokens = t
	return h
}

// RegisterRoutes adds /me. passwordLimit is the per-account bucket every
// password check shares (sign-in's and re-authentication's).
func (h *Handler) RegisterRoutes(r *gin.RouterGroup, passwordLimit gin.HandlerFunc) {
	me := r.Group("/me")
	{
		me.GET("", h.getMe)
		me.PUT("", h.updateProfile)
		me.PUT("/password", passwordLimit, h.updatePassword)
		me.POST("/sessions/sign-out", h.signOut)
		me.POST("/sessions/sign-out-others", h.signOutOthers)
		me.PUT("/theme", h.setTheme)
	}
}

func (h *Handler) getMe(c *gin.Context) {
	userID := middleware.UserIDFrom(c)

	p, err := h.svc.GetMe(c.Request.Context(), userID)
	if err != nil {
		h.log.Error("get me failed", zap.Error(err), zap.String("user_id", userID))
		respond.Error(c, http.StatusInternalServerError, "failed to fetch user")
		return
	}

	respond.JSON(c, http.StatusOK, p)
}

type UpdateProfileInput struct {
	Name string `json:"name" binding:"required,min=2,max=100"`
}

func (h *Handler) updateProfile(c *gin.Context) {
	userID := middleware.UserIDFrom(c)

	var input UpdateProfileInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "name is required (min 2 characters)")
		return
	}

	p, err := h.svc.UpdateProfile(c.Request.Context(), userID, input.Name)
	if err != nil {
		h.log.Error("update profile failed", zap.Error(err), zap.String("user_id", userID))
		respond.Error(c, http.StatusInternalServerError, "failed to update profile")
		return
	}

	respond.JSON(c, http.StatusOK, p)
}

type UpdatePasswordInput struct {
	CurrentPassword string `json:"current_password" binding:"required,max=256"`
	NewPassword     string `json:"new_password" binding:"required,min=12"` // at most 72 bytes, bcrypt's: password_too_long
}

func (h *Handler) updatePassword(c *gin.Context) {
	if !sessionOnly(c) {
		return
	}
	userID := middleware.UserIDFrom(c)

	var input UpdatePasswordInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "current_password and new_password (min 8 chars) are required")
		return
	}

	err := h.svc.UpdatePassword(c.Request.Context(), userID, input.CurrentPassword, input.NewPassword)
	if err != nil {
		if errors.Is(err, ErrWrongPassword) {
			respond.Error(c, http.StatusUnauthorized, "current password is incorrect")
			return
		}
		if errors.Is(err, bcrypt.ErrPasswordTooLong) {
			respond.ErrorData(c, http.StatusBadRequest, "a password is at most 72 bytes: 72 letters, fewer with accents, non-Latin letters or emoji", gin.H{"code": "password_too_long", "field": "new_password"})
			return
		}
		h.log.Error("update password failed", zap.Error(err), zap.String("user_id", userID))
		respond.Error(c, http.StatusInternalServerError, "failed to update password")
		return
	}

	respond.JSON(c, http.StatusOK, h.withFreshToken(userID, gin.H{"message": "password updated"}))
}

// PUT /me/theme — saves the Settings → Appearance choice to the account.
func (h *Handler) setTheme(c *gin.Context) {
	userID := middleware.UserIDFrom(c)
	var in struct {
		Theme string `json:"theme" binding:"required,max=40"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.Error(c, http.StatusBadRequest, "theme is required")
		return
	}
	if err := h.svc.SetTheme(c.Request.Context(), userID, in.Theme); err != nil {
		if errors.Is(err, ErrUnknownTheme) {
			respond.Error(c, http.StatusBadRequest, err.Error())
			return
		}
		h.log.Error("set theme failed", zap.Error(err), zap.String("user_id", userID))
		respond.Error(c, http.StatusInternalServerError, "failed to save the theme")
		return
	}
	respond.NoContent(c)
}

// POST /me/sessions/sign-out — this session ends: its token is refused
// from now on, wherever a copy of it is.
func (h *Handler) signOut(c *gin.Context) {
	session, ok := middleware.SessionFrom(c)
	if !ok {
		respond.ErrorData(c, http.StatusForbidden, "this needs a signed-in session, not an API key", gin.H{"code": "session_required"})
		return
	}
	if err := h.svc.SignOut(c.Request.Context(), session.Hash, session.Expires); err != nil {
		h.log.Error("sign out failed", zap.Error(err), zap.String("user_id", middleware.UserIDFrom(c)))
		respond.Error(c, http.StatusInternalServerError, "failed to sign out")
		return
	}
	respond.NoContent(c)
}

// POST /me/sessions/sign-out-others — every other session of the account
// ends; the answer carries a fresh token for this one.
func (h *Handler) signOutOthers(c *gin.Context) {
	if !sessionOnly(c) {
		return
	}
	userID := middleware.UserIDFrom(c)
	if err := h.svc.SignOutOthers(c.Request.Context(), userID); err != nil {
		h.log.Error("sign out other sessions failed", zap.Error(err), zap.String("user_id", userID))
		respond.Error(c, http.StatusInternalServerError, "failed to sign out the other sessions")
		return
	}
	respond.JSON(c, http.StatusOK, h.withFreshToken(userID, gin.H{"message": "other sessions signed out"}))
}

// sessionOnly refuses API keys and agents: both routes answer with a fresh
// session token, which would turn a key into a session that outlives it and
// passes every session_required check.
func sessionOnly(c *gin.Context) bool {
	if middleware.ViaAPIKey(c) {
		respond.ErrorData(c, http.StatusForbidden, "this needs a signed-in session, not an API key", gin.H{"code": "session_required"})
		return false
	}
	return true
}

// withFreshToken adds a new session token to out: revoking the account's
// sessions revoked the caller's too.
func (h *Handler) withFreshToken(userID string, out gin.H) gin.H {
	if h.tokens == nil {
		return out
	}
	token, err := h.tokens.IssueToken(userID)
	if err != nil {
		h.log.Error("issue token after revoking sessions", zap.Error(err), zap.String("user_id", userID))
		return out
	}
	out["token"] = token
	return out
}
