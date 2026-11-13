package auth

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/internal/org"
	"github.com/Nickpap21/toft/backend/internal/user"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

// SetupTokenHeader carries TOFT_SETUP_TOKEN as an alternative to the
// setup_token body field.
const SetupTokenHeader = "X-Setup-Token"

// SetupTokenHeaderToft is the header the API reference names; both are read.
const SetupTokenHeaderToft = "X-Toft-Setup-Token"

// Handler maps HTTP requests to the Auth Service.
type Handler struct {
	svc *Service
	log *zap.Logger
}

func NewHandler(svc *Service, log *zap.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

// RegisterRoutes registers the auth endpoints on the provided group. The
// loginLimits run before sign-in only (a per-account limiter, keyed by
// LoginAccountKey, next to the group's per-address one).
func (h *Handler) RegisterRoutes(r *gin.RouterGroup, loginLimits ...gin.HandlerFunc) {
	r.POST("/register", h.handleRegister)
	r.POST("/login", append(loginLimits, h.handleLogin)...)
}

// RegisterProtectedRoutes registers the endpoints that need a signed-in
// session. passwordLimits run before them (the per-account limiter on
// password confirmations, shared with operator changes).
func (h *Handler) RegisterProtectedRoutes(r *gin.RouterGroup, passwordLimits ...gin.HandlerFunc) {
	r.POST("/auth/reauth", append(passwordLimits, h.handleReauth)...)
}

// ReauthInput is the POST /auth/reauth body.
type ReauthInput struct {
	Password string `json:"password" binding:"required,max=256"`
}

// ReauthResponse carries the proof for the X-Toft-Reauth header and when it
// stops working, also in seconds from now, so a client times it by its own
// clock.
type ReauthResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	ExpiresIn int       `json:"expires_in"`
}

// POST /auth/reauth {password}: the session confirms its password, and gets
// a proof for host shells and host-key replacement (D-53). Sessions only.
func (h *Handler) handleReauth(c *gin.Context) {
	if middleware.ViaAPIKey(c) {
		respond.ErrorData(c, http.StatusForbidden, "confirming a password needs a signed-in session, not an API key", gin.H{"code": "session_required"})
		return
	}
	var input ReauthInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "password is required")
		return
	}
	session := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	token, expires, err := h.svc.Reauthenticate(c.Request.Context(), middleware.UserIDFrom(c), session, input.Password)
	if errors.Is(err, ErrWrongPassword) {
		respond.ErrorData(c, http.StatusForbidden, err.Error(), gin.H{"code": "wrong_password"})
		return
	}
	if err != nil {
		h.log.Error("reauthenticate failed", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to confirm the password")
		return
	}
	respond.JSON(c, http.StatusOK, ReauthResponse{Token: token, ExpiresAt: expires, ExpiresIn: int(time.Until(expires).Seconds())})
}

// LoginAccountKey is the account a sign-in attempt names, lowercased, for a
// per-account rate limiter; "" (no limit) when the body has none. The body
// is bound once and kept on the context (ShouldBindBodyWith), so the
// handler reads the same bytes.
func LoginAccountKey(c *gin.Context) string {
	var input LoginInput
	if err := c.ShouldBindBodyWith(&input, binding.JSON); err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(input.Email))
}

type RegisterInput struct {
	Email    string `json:"email" binding:"required,email,max=254"`
	Name     string `json:"name" binding:"required,min=2,max=100"`
	Password string `json:"password" binding:"required,min=12"` // at most 72 bytes, bcrypt's: password_too_long
	// OrgName names the first workspace; ignored unless this is the first
	// account on an empty instance.
	OrgName     string `json:"org_name" binding:"max=100"`
	InviteToken string `json:"invite_token" binding:"max=256"`
	// SetupToken is required for the first account when TOFT_SETUP_TOKEN is
	// set. It may be sent in the X-Setup-Token header instead.
	SetupToken string `json:"setup_token" binding:"max=256"`
}

// AuthResponse is the body of a successful register or login.
type AuthResponse struct {
	User  user.Profile `json:"user"`
	Token string       `json:"token"`
	// Reauth: a fresh sign-in counts as a password confirmation for a few
	// minutes (D-161), for the X-Toft-Reauth header.
	Reauth  ReauthResponse `json:"reauth"`
	Message string         `json:"message,omitempty"`
}

func (h *Handler) handleRegister(c *gin.Context) {
	var input RegisterInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	setupToken := input.SetupToken
	if setupToken == "" {
		setupToken = c.GetHeader(SetupTokenHeaderToft)
	}
	if setupToken == "" {
		setupToken = c.GetHeader(SetupTokenHeader)
	}

	result, err := h.svc.Register(c.Request.Context(), RegisterParams{
		Email:       input.Email,
		Name:        input.Name,
		Password:    input.Password,
		OrgName:     input.OrgName,
		InviteToken: input.InviteToken,
		SetupToken:  setupToken,
	})
	if err != nil {
		if msg, ok := org.InviteErrorMessage(err); ok {
			respond.ErrorData(c, http.StatusBadRequest, msg, gin.H{"code": org.InviteErrorCode(err)})
			return
		}
		switch {
		case errors.Is(err, bcrypt.ErrPasswordTooLong):
			respond.ErrorData(c, http.StatusBadRequest, "a password is at most 72 bytes: 72 letters, fewer with accents, non-Latin letters or emoji", gin.H{"code": "password_too_long", "field": "password"})
		case errors.Is(err, ErrRegistrationClosed):
			respond.ErrorData(c, http.StatusForbidden, "registration is invite-only — ask a workspace owner for an invite link",
				gin.H{"code": "registration_closed"})
		case errors.Is(err, ErrSetupTokenExpired):
			respond.ErrorData(c, http.StatusForbidden, "the setup code has expired — run the installer again on the server for a new one",
				gin.H{"code": "setup_token_expired"})
		case errors.Is(err, ErrSetupTokenInvalid):
			respond.Error(c, http.StatusForbidden, "a valid setup code is required to set up this instance — use the one the installer printed (TOFT_SETUP_TOKEN)")
		case errors.Is(err, ErrUserExists):
			respond.Error(c, http.StatusConflict, "an account with this email already exists — sign in instead")
		default:
			h.log.Error("registration failed", zap.Error(err))
			respond.Error(c, http.StatusInternalServerError, "Failed to register user")
		}
		return
	}

	respond.JSON(c, http.StatusCreated, AuthResponse{
		User:    user.NewProfile(result.User, result.IsOperator),
		Token:   result.Token,
		Reauth:  ReauthResponse{Token: result.Reauth, ExpiresAt: result.ReauthExpires, ExpiresIn: int(time.Until(result.ReauthExpires).Seconds())},
		Message: "User registered successfully",
	})
}

type LoginInput struct {
	Email    string `json:"email" binding:"required,email,max=254"`
	Password string `json:"password" binding:"required,max=256"`
}

func (h *Handler) handleLogin(c *gin.Context) {
	var input LoginInput
	if err := c.ShouldBindBodyWith(&input, binding.JSON); err != nil {
		respond.Error(c, http.StatusBadRequest, "Invalid request body")
		return
	}

	result, err := h.svc.Login(c.Request.Context(), input.Email, input.Password)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidCredentials):
			respond.Error(c, http.StatusUnauthorized, "Invalid email or password")
		case errors.Is(err, ErrAccountDisabled):
			respond.Error(c, http.StatusForbidden, "this account has been deactivated — ask an instance operator to reactivate it")
		default:
			h.log.Error("login failed", zap.Error(err))
			respond.Error(c, http.StatusInternalServerError, "Failed to connect to server")
		}
		return
	}

	respond.JSON(c, http.StatusOK, AuthResponse{
		User:    user.NewProfile(result.User, result.IsOperator),
		Token:   result.Token,
		Reauth:  ReauthResponse{Token: result.Reauth, ExpiresAt: result.ReauthExpires, ExpiresIn: int(time.Until(result.ReauthExpires).Seconds())},
		Message: "Login successful",
	})
}
