package instance

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/netcheck"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/Nickpap21/toft/backend/pkg/wspty"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

// Handler is the thin HTTP adapter for instance-level endpoints.
type Handler struct {
	svc *Service
	log *zap.Logger
	// ws upgrades the panel shell's WebSocket; WithAllowedOrigins sets it.
	ws websocket.Upgrader
}

// WithAllowedOrigins limits the panel shell's WebSocket to the API's own
// origin and to origins (the same list as CORS); see wspty.Upgrader.
func (h *Handler) WithAllowedOrigins(origins func() []string) *Handler {
	h.ws = wspty.Upgrader(origins)
	return h
}

// NewHandler creates an instance Handler.
func NewHandler(svc *Service, log *zap.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

// RegisterPublicRoutes mounts unauthenticated endpoints. Keep this off the
// /auth rate limiter: the sign-up page reads it on every load.
//
// The reset link's endpoints (D-56) are public too: its holder is signed
// out. redeemLimits (a per-token rate limiter) runs before the POST.
func (h *Handler) RegisterPublicRoutes(rg *gin.RouterGroup, redeemLimits ...gin.HandlerFunc) {
	rg.GET("/instance/registration", h.getRegistration)
	// What the setup wizard's domain check fetches through a domain (D-62).
	rg.GET("/instance/probe", h.getProbe)
	rg.GET("/password-resets/:token", h.getPasswordReset)
	rg.POST("/password-resets/:token", append(redeemLimits, h.redeemPasswordReset)...)
}

// RegisterRoutes mounts the protected endpoints: operator-only ones, whose
// operator check happens in the Service, and the panel host's facts.
//
// Grant, revoke and (de)activation are D-23's re-authentication (D-50): a
// signed-in session only, never an API key, and the operator's password in
// the body every time. changeLimits (a per-account rate limiter) runs before
// them, so the password can't be guessed at speed with a stolen session.
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup, changeLimits ...gin.HandlerFunc) {
	g := rg.Group("/instance")
	{
		g.GET("/operators", h.listOperators)
		g.POST("/operators", append(changeLimits, h.grantOperator)...)
		g.DELETE("/operators/:userID", append(changeLimits, h.revokeOperator)...)

		g.GET("/users", h.listUsers)
		g.PATCH("/users/:userID", append(changeLimits, h.updateUser)...)
		g.POST("/users/:userID/password-resets", append(changeLimits, h.issuePasswordReset)...)
		g.DELETE("/users/:userID", append(changeLimits, h.deleteUser)...)
		g.GET("/audit-log", h.listAudit)
		h.registerSetupRoutes(g)

		// Not operator-only: every member's services may run there.
		g.GET("/panel-host", h.getPanelHost)

		// The panel itself (D-68), operators only.
		g.GET("/panel", h.getPanel)
		g.GET("/panel/server", h.getPanelServer)
		g.GET("/panel/containers/:container/logs", h.streamPanelLogs)
		g.GET("/panel/pty", h.panelPTY)
		g.GET("/panel/capabilities", h.getPanelCapabilities)
		g.POST("/panel/capabilities", h.probePanelCapabilities)
		g.GET("/panel/updates", h.getPanelUpdates)
		g.POST("/panel/updates", h.checkPanelUpdates)
		g.GET("/panel/updates/run", h.getPanelUpdateRun)
		g.POST("/panel/updates/run", h.startPanelUpdateRun)
		g.GET("/panel/gvisor", h.getPanelGvisorRun)
		g.POST("/panel/gvisor", h.installPanelGvisor)
		g.GET("/upgrade", h.getUpgrade)
		g.POST("/upgrade", append(changeLimits, h.startUpgrade)...)
		g.POST("/upgrade/check", h.checkRelease)
		g.GET("/watchdog", h.getWatchdog)
		g.PUT("/watchdog", h.putWatchdog)
		g.POST("/watchdog/test", h.testWatchdog)
	}
}

func (h *Handler) getPanelHost(c *gin.Context) {
	host, err := h.svc.PanelHost(c.Request.Context())
	if err != nil {
		h.log.Error("panel host failed", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to read the panel host")
		return
	}
	respond.JSON(c, http.StatusOK, host)
}

func (h *Handler) getPanel(c *gin.Context) {
	st, err := h.svc.PanelStatus(c.Request.Context(), middleware.UserIDFrom(c))
	if err != nil {
		h.writeError(c, err, "only instance operators see the panel's health", "failed to read the panel's health")
		return
	}
	respond.JSON(c, http.StatusOK, st)
}

func (h *Handler) getPanelServer(c *gin.Context) {
	srv, err := h.svc.PanelServer(c.Request.Context(), middleware.UserIDFrom(c), c.DefaultQuery("range", "live"))
	if err != nil {
		h.writeError(c, err, "only instance operators see the panel's server", "failed to read the panel's server")
		return
	}
	respond.JSON(c, http.StatusOK, srv)
}

// streamPanelLogs follows one of the panel's containers' logs as
// server-sent events, as a service's live log does.
func (h *Handler) streamPanelLogs(c *gin.Context) {
	userID, name := middleware.UserIDFrom(c), c.Param("container")
	// Refused before the stream starts, so the client gets a JSON error.
	if err := h.svc.CheckPanelContainer(c.Request.Context(), userID, name); err != nil {
		h.writeError(c, err, "only instance operators read the panel's logs", "failed to read the panel's logs")
		return
	}
	tail, err := strconv.Atoi(c.DefaultQuery("tail", "200"))
	if err != nil || tail <= 0 {
		tail = 200
	}
	tail = min(tail, 2000)

	respond.Stream(c)
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Flush()
	line := func(l string) {
		_, _ = c.Writer.WriteString("data: " + strings.ReplaceAll(l, "\n", " ") + "\n\n")
		c.Writer.Flush()
	}
	if err := h.svc.PanelLogs(c.Request.Context(), userID, name, tail, line); err != nil && !errors.Is(err, context.Canceled) {
		h.log.Error("panel log stream failed", zap.Error(err), zap.String("container", name))
		_, _ = c.Writer.WriteString("event: error\ndata: the log could not be followed\n\n")
	}
	_, _ = c.Writer.WriteString("event: done\ndata: {}\n\n")
	c.Writer.Flush()
}

// panelPTY opens a root shell on the panel's server over a WebSocket (D-68;
// pkg/wspty has the messages): operators only, with the password confirmed
// (D-53), unless TOFT_PANEL_HOST_SHELL=off. Opening and closing are in the
// instance audit log.
func (h *Handler) panelPTY(c *gin.Context) {
	ctx, userID := c.Request.Context(), middleware.UserIDFrom(c)
	// Refused before the upgrade, so the client gets a plain 403.
	if err := h.svc.CheckPanelShell(ctx, userID); err != nil {
		h.writeError(c, err, "only instance operators open a shell on the panel's server", "failed to open the shell")
		return
	}
	if !middleware.RequireReauth(c) {
		return
	}
	cols, rows := wspty.Size(c.Request)
	conn, err := h.ws.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.log.Error("panel shell WebSocket upgrade failed", zap.Error(err))
		return
	}
	defer conn.Close()
	sh, err := h.svc.OpenPanelShell(ctx, userID, c.ClientIP(), cols, rows)
	if err != nil {
		h.log.Error("opening the panel's shell failed", zap.Error(err))
		wspty.Fail(conn, "The shell could not open: "+err.Error())
		return
	}
	opened := time.Now()
	defer func() {
		if wspty.Stopping(ctx) {
			// The backend is stopping, perhaps for an upgrade started from
			// this shell: the shell stays so that runs on, and the next
			// backend removes it once it is idle and writes its row.
			_ = sh.Detach()
			return
		}
		_ = sh.Close()
		h.svc.ClosedPanelShell(context.WithoutCancel(ctx), userID, opened)
	}()
	wspty.Serve(ctx, conn, sh)
}

// GET /instance/panel/capabilities: what the panel's server can run (D-97);
// null data before the first probe.
func (h *Handler) getPanelCapabilities(c *gin.Context) {
	caps, err := h.svc.PanelCapabilities(c.Request.Context(), middleware.UserIDFrom(c))
	if err != nil {
		h.writeError(c, err, "only instance operators see what the panel's server can run", "failed to read what the panel's server can run")
		return
	}
	respond.JSON(c, http.StatusOK, caps)
}

// POST /instance/panel/capabilities: check again, through the host helper.
func (h *Handler) probePanelCapabilities(c *gin.Context) {
	caps, err := h.svc.ProbePanel(c.Request.Context(), middleware.UserIDFrom(c))
	if err != nil {
		h.writeError(c, err, "only instance operators check the panel's server", "failed to check what the panel's server can run")
		return
	}
	respond.JSON(c, http.StatusOK, caps)
}

// GET /instance/panel/updates: the panel's server's last updates read
// (D-133); null data before the first.
func (h *Handler) getPanelUpdates(c *gin.Context) {
	u, err := h.svc.PanelUpdates(c.Request.Context(), middleware.UserIDFrom(c))
	if err != nil {
		h.writeError(c, err, "only instance operators see the panel's server's updates", "failed to read the panel's server's updates")
		return
	}
	respond.JSON(c, http.StatusOK, u)
}

// GET /instance/panel/updates/run: the panel's server's run of updates,
// followed on the host while it lasts; null data before the first (D-134).
func (h *Handler) getPanelUpdateRun(c *gin.Context) {
	run, err := h.svc.PanelUpdateRun(c.Request.Context(), middleware.UserIDFrom(c))
	if err != nil {
		h.writeError(c, err, "only instance operators see the panel's server's updates", "failed to read the run of updates")
		return
	}
	respond.JSON(c, http.StatusOK, run)
}

// POST /instance/panel/updates/run: install updates, update Docker or
// reboot the panel's server (D-134). Operators, with the password.
func (h *Handler) startPanelUpdateRun(c *gin.Context) {
	ctx, userID := c.Request.Context(), middleware.UserIDFrom(c)
	if err := h.svc.AuthorizePanelUpdates(ctx, userID); err != nil {
		h.writeError(c, err, "only instance operators run updates on the panel's server", "failed to check who may run updates")
		return
	}
	if !middleware.RequireReauth(c) {
		return
	}
	var in server.UpdateRunInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.Error(c, http.StatusBadRequest, "action must be install, docker or reboot")
		return
	}
	run, err := h.svc.StartPanelUpdates(ctx, userID, in.Action)
	if err != nil {
		if status, code := server.UpdateRunStatus(err); status != 0 {
			respond.ErrorData(c, status, err.Error(), gin.H{"code": code})
			return
		}
		h.writeError(c, err, "only instance operators run updates on the panel's server", "failed to start the run of updates")
		return
	}
	respond.JSON(c, http.StatusAccepted, run)
}

// POST /instance/panel/updates: read again now, through the host helper.
func (h *Handler) checkPanelUpdates(c *gin.Context) {
	u, err := h.svc.CheckPanelUpdates(c.Request.Context(), middleware.UserIDFrom(c))
	if err != nil {
		h.writeError(c, err, "only instance operators check the panel's server's updates", "failed to read the panel's server's updates")
		return
	}
	respond.JSON(c, http.StatusOK, u)
}

// GET /instance/panel/gvisor: the last gVisor install on the panel's server;
// null data before the first.
func (h *Handler) getPanelGvisorRun(c *gin.Context) {
	run, err := h.svc.PanelGvisorRun(c.Request.Context(), middleware.UserIDFrom(c))
	if err != nil {
		h.writeError(c, err, "only instance operators see the panel's gVisor install", "failed to read the gVisor install")
		return
	}
	respond.JSON(c, http.StatusOK, run)
}

// POST /instance/panel/gvisor: install gVisor on the panel's server (D-97),
// as POST /servers/:serverID/gvisor does on a server: operators with the
// password, server-sent events.
func (h *Handler) installPanelGvisor(c *gin.Context) {
	ctx, userID := c.Request.Context(), middleware.UserIDFrom(c)
	if err := h.svc.AuthorizePanelGvisor(ctx, userID); err != nil {
		h.writeError(c, err, "only instance operators install gVisor on the panel's server", "failed to check who may install gVisor")
		return
	}
	if !middleware.RequireReauth(c) {
		return
	}
	switch err := h.svc.CheckPanelGvisor(ctx); {
	case errors.Is(err, ErrPanelNoApps):
		respond.ErrorData(c, http.StatusBadRequest, err.Error(), gin.H{"code": "panel_control_plane_only"})
		return
	case errors.Is(err, ErrPanelGvisorUnsupported):
		respond.ErrorData(c, http.StatusBadRequest, err.Error(), gin.H{"code": "gvisor_unsupported"})
		return
	case errors.Is(err, ErrPanelGvisorRunning):
		respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "install_running"})
		return
	case err != nil:
		h.writeError(c, err, "", "failed to check the panel's server")
		return
	}

	respond.Stream(c)
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Transfer-Encoding", "chunked")
	logFn := func(msg string) {
		fmt.Fprintf(c.Writer, "data: %s\n\n", strings.ReplaceAll(msg, "\n", " "))
		c.Writer.Flush()
	}
	if err := h.svc.InstallPanelGvisor(ctx, userID, logFn); err != nil {
		h.log.Warn("gvisor install on the panel's server failed", zap.Error(err))
		fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", strings.ReplaceAll(err.Error(), "\n", " "))
	} else {
		fmt.Fprintf(c.Writer, "event: done\ndata: installed\n\n")
	}
	c.Writer.Flush()
}

func (h *Handler) getUpgrade(c *gin.Context) {
	st, err := h.svc.UpgradeStatus(c.Request.Context(), middleware.UserIDFrom(c))
	if err != nil {
		h.writeError(c, err, "only instance operators upgrade the panel", "failed to read the upgrade")
		return
	}
	respond.JSON(c, http.StatusOK, st)
}

// StartUpgradeInput is the POST /instance/upgrade body: the release, which
// must be the newest, and the operator's password.
type StartUpgradeInput struct {
	To       string `json:"to" binding:"required,max=64"`
	Password string `json:"password" binding:"required,max=256"`
}

// startUpgrade starts toft upgrade on the host and answers 202 at once: the
// upgrade restarts the panel, and the page follows it with GET.
func (h *Handler) startUpgrade(c *gin.Context) {
	if !sessionOnly(c) {
		return
	}
	var input StartUpgradeInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "to (a release such as v0.2.0) and your password are required")
		return
	}
	st, err := h.svc.StartUpgrade(c.Request.Context(), middleware.UserIDFrom(c), input.Password, input.To)
	if err != nil {
		h.writeError(c, err, "only instance operators upgrade the panel", "failed to start the upgrade")
		return
	}
	respond.JSON(c, http.StatusAccepted, st)
}

func (h *Handler) checkRelease(c *gin.Context) {
	st, err := h.svc.CheckRelease(c.Request.Context(), middleware.UserIDFrom(c))
	if err != nil {
		h.writeError(c, err, "only instance operators upgrade the panel", "failed to check for a release")
		return
	}
	respond.JSON(c, http.StatusOK, st)
}

func (h *Handler) getWatchdog(c *gin.Context) {
	w, err := h.svc.WatchdogStatus(c.Request.Context(), middleware.UserIDFrom(c))
	if err != nil {
		h.writeError(c, err, "only instance operators set the watchdog", "failed to read the watchdog")
		return
	}
	respond.JSON(c, http.StatusOK, w)
}

// WatchdogInput is the PUT /instance/watchdog body: the ping URL, "" to
// turn the watchdog off.
type WatchdogInput struct {
	URL *string `json:"url" binding:"required"`
}

func (h *Handler) putWatchdog(c *gin.Context) {
	var input WatchdogInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "url is required (an https:// URL, or empty to turn the watchdog off)")
		return
	}
	w, err := h.svc.SetWatchdog(c.Request.Context(), middleware.UserIDFrom(c), *input.URL)
	if err != nil {
		h.writeError(c, err, "only instance operators set the watchdog", "failed to set the watchdog")
		return
	}
	respond.JSON(c, http.StatusOK, w)
}

func (h *Handler) testWatchdog(c *gin.Context) {
	w, err := h.svc.TestWatchdog(c.Request.Context(), middleware.UserIDFrom(c))
	if err != nil {
		h.writeError(c, err, "only instance operators set the watchdog", "failed to ping the watchdog")
		return
	}
	respond.JSON(c, http.StatusOK, w)
}

func (h *Handler) getRegistration(c *gin.Context) {
	status, err := h.svc.RegistrationStatus(c.Request.Context())
	if err != nil {
		h.log.Error("registration status failed", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to read registration status")
		return
	}
	respond.JSON(c, http.StatusOK, status)
}

func (h *Handler) listOperators(c *gin.Context) {
	ops, err := h.svc.ListOperators(c.Request.Context(), middleware.UserIDFrom(c))
	if err != nil {
		h.writeError(c, err, "only instance operators can manage operators", "failed to list operators")
		return
	}
	respond.JSON(c, http.StatusOK, ops)
}

// GrantOperatorInput identifies the user to grant by email or by ID; exactly
// one is required. Password is the operator's own.
type GrantOperatorInput struct {
	UserID   string `json:"user_id"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// sessionOnly refuses API keys: changes to accounts and operators need a
// person signed in (D-50).
func sessionOnly(c *gin.Context) bool {
	if middleware.ViaAPIKey(c) {
		respond.ErrorData(c, http.StatusForbidden, "changes to accounts and operators need a signed-in session, not an API key",
			gin.H{"code": "session_required"})
		return false
	}
	return true
}

func (h *Handler) grantOperator(c *gin.Context) {
	if !sessionOnly(c) {
		return
	}
	var input GrantOperatorInput
	if err := c.ShouldBindJSON(&input); err != nil || (input.UserID == "") == (input.Email == "") {
		respond.Error(c, http.StatusBadRequest, "provide exactly one of user_id or email")
		return
	}
	if input.Password == "" {
		respond.Error(c, http.StatusBadRequest, "your password is required")
		return
	}
	op, err := h.svc.GrantOperator(c.Request.Context(), middleware.UserIDFrom(c), input.Password, input.UserID, input.Email)
	if err != nil {
		h.writeError(c, err, "only instance operators can manage operators", "failed to grant operator")
		return
	}
	respond.JSON(c, http.StatusCreated, op)
}

// PasswordInput is the body of DELETE /instance/operators/:userID and of
// POST /instance/users/:userID/password-resets: the operator's own password.
type PasswordInput struct {
	Password string `json:"password" binding:"required,max=256"`
}

func (h *Handler) revokeOperator(c *gin.Context) {
	if !sessionOnly(c) {
		return
	}
	var input PasswordInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "your password is required")
		return
	}
	err := h.svc.RevokeOperator(c.Request.Context(), middleware.UserIDFrom(c), input.Password, c.Param("userID"))
	if err != nil {
		h.writeError(c, err, "only instance operators can manage operators", "failed to revoke operator")
		return
	}
	respond.NoContent(c)
}

// listUsers answers {users, deleted}: the accounts, and how many deleted
// ones the list leaves out (D-57).
func (h *Handler) listUsers(c *gin.Context) {
	users, deleted, err := h.svc.ListUsers(c.Request.Context(), middleware.UserIDFrom(c))
	if err != nil {
		h.writeError(c, err, "only instance operators can manage users", "failed to list users")
		return
	}
	respond.JSON(c, http.StatusOK, gin.H{"users": users, "deleted": deleted})
}

func (h *Handler) deleteUser(c *gin.Context) {
	if !sessionOnly(c) {
		return
	}
	var input PasswordInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "your password is required")
		return
	}
	if err := h.svc.DeleteUser(c.Request.Context(), middleware.UserIDFrom(c), input.Password, c.Param("userID")); err != nil {
		h.writeError(c, err, "only instance operators can delete accounts", "failed to delete the account")
		return
	}
	respond.NoContent(c)
}

func (h *Handler) listAudit(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	entries, next, err := h.svc.ListAudit(c.Request.Context(), middleware.UserIDFrom(c), limit, c.Query("cursor"), c.Query("kind"))
	if err != nil {
		h.writeError(c, err, "only instance operators see the instance audit log", "failed to list the audit log")
		return
	}
	respond.JSONPage(c, http.StatusOK, entries, next)
}

// UpdateUserInput is the PATCH /instance/users/:userID body. Password is
// the operator's own.
type UpdateUserInput struct {
	IsActive *bool  `json:"is_active" binding:"required"`
	Password string `json:"password" binding:"required,max=256"`
}

func (h *Handler) updateUser(c *gin.Context) {
	if !sessionOnly(c) {
		return
	}
	var input UpdateUserInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "is_active (true or false) and your password are required")
		return
	}
	u, err := h.svc.SetUserActive(c.Request.Context(), middleware.UserIDFrom(c), input.Password, c.Param("userID"), *input.IsActive)
	if err != nil {
		h.writeError(c, err, "only instance operators can manage users", "failed to update user")
		return
	}
	respond.JSON(c, http.StatusOK, u)
}

func (h *Handler) issuePasswordReset(c *gin.Context) {
	if !sessionOnly(c) {
		return
	}
	var input PasswordInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "your password is required")
		return
	}
	reset, err := h.svc.IssuePasswordReset(c.Request.Context(), middleware.UserIDFrom(c), input.Password, c.Param("userID"))
	if err != nil {
		h.writeError(c, err, "only instance operators can reset passwords", "failed to issue password reset")
		return
	}
	respond.JSON(c, http.StatusCreated, reset)
}

func (h *Handler) getPasswordReset(c *gin.Context) {
	target, err := h.svc.GetPasswordReset(c.Request.Context(), c.Param("token"))
	if err != nil {
		h.writeResetError(c, err, "failed to read password reset")
		return
	}
	respond.JSON(c, http.StatusOK, target)
}

// RedeemPasswordResetInput is the body of POST /password-resets/:token.
type RedeemPasswordResetInput struct {
	Password     string `json:"password" binding:"required,min=12"` // at most 72 bytes, bcrypt's: password_too_long
	RevokeAccess bool   `json:"revoke_access"`
}

func (h *Handler) redeemPasswordReset(c *gin.Context) {
	var input RedeemPasswordResetInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "password (min 8 chars) is required")
		return
	}
	email, err := h.svc.RedeemPasswordReset(c.Request.Context(), c.Param("token"), input.Password, input.RevokeAccess)
	if errors.Is(err, bcrypt.ErrPasswordTooLong) {
		respond.ErrorData(c, http.StatusBadRequest, "a password is at most 72 bytes: 72 letters, fewer with accents, non-Latin letters or emoji", gin.H{"code": "password_too_long", "field": "password"})
		return
	}
	if err != nil {
		h.writeResetError(c, err, "failed to reset password")
		return
	}
	respond.JSON(c, http.StatusOK, gin.H{"email": email})
}

// writeResetError answers a dead link with one wording and its code, as the
// invite endpoints do, and never says which account it was for.
func (h *Handler) writeResetError(c *gin.Context, err error, internalMsg string) {
	if code := ResetErrorCode(err); code != "" {
		respond.ErrorData(c, http.StatusBadRequest, "this link has been used or has expired — ask an instance operator for a new one", gin.H{"code": code})
		return
	}
	h.log.Error(internalMsg, zap.Error(err))
	respond.Error(c, http.StatusInternalServerError, internalMsg)
}

// writeError maps service errors to responses. forbiddenMsg names the
// operator-only action; internalMsg is used for unexpected errors.
func (h *Handler) writeError(c *gin.Context, err error, forbiddenMsg, internalMsg string) {
	switch {
	case errors.Is(err, ErrNotOperator):
		respond.Error(c, http.StatusForbidden, forbiddenMsg)
	case errors.Is(err, ErrWrongPassword):
		// 403, not 401: a 401 signs the browser out.
		respond.ErrorData(c, http.StatusForbidden, "your password is incorrect", gin.H{"code": "wrong_password"})
	case errors.Is(err, ErrUserNotFound):
		respond.Error(c, http.StatusNotFound, "no user with that id or email")
	case errors.Is(err, ErrNotFound):
		respond.Error(c, http.StatusNotFound, "that user is not an instance operator")
	case errors.Is(err, ErrAmbiguousEmail):
		respond.Error(c, http.StatusConflict, "more than one account matches this email (case-insensitive); use user_id instead")
	case errors.Is(err, ErrLastOperator):
		respond.Error(c, http.StatusConflict, "this would leave the instance without an active operator — grant another operator first")
	case errors.Is(err, ErrCannotDeactivateSelf):
		respond.Error(c, http.StatusBadRequest, "you cannot deactivate your own account")
	case errors.Is(err, ErrUserInactive):
		respond.Error(c, http.StatusConflict, "the account is deactivated — reactivate it first, then issue a reset link")
	case errors.Is(err, ErrUserDeleted):
		respond.Error(c, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrUserActive), errors.Is(err, ErrIsOperator):
		respond.Error(c, http.StatusConflict, err.Error())
	case errors.Is(err, ErrWatchdogURL), errors.Is(err, netcheck.ErrPrivateAddress):
		respond.ErrorData(c, http.StatusBadRequest, err.Error(), gin.H{"code": "invalid_url"})
	case errors.Is(err, ErrUpgradeDeploySh):
		respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "deploy_sh"})
	case errors.Is(err, ErrUpgradeRunning):
		respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "upgrade_running"})
	case errors.Is(err, server.ErrUpdateRunning):
		respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "update_running"})
	case errors.Is(err, ErrBadRelease):
		respond.ErrorData(c, http.StatusBadRequest, err.Error(), gin.H{"code": "bad_release"})
	case errors.Is(err, ErrPanelShellOff):
		respond.ErrorData(c, http.StatusForbidden, err.Error(), gin.H{"code": "panel_shell_off"})
	case errors.Is(err, ErrUnknownContainer):
		respond.Error(c, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrCannotDeleteSelf), errors.Is(err, ErrBadCursor), errors.Is(err, ErrBadKind), errors.Is(err, ErrBadRange):
		respond.Error(c, http.StatusBadRequest, err.Error())
	default:
		h.log.Error(internalMsg, zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, internalMsg)
	}
}
