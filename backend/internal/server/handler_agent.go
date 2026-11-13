package server

import (
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/internal/agenthub"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"go.uber.org/zap"
)

// The node agent's routes (D-106).

// WithAgentOrigin tells the handler how agents reach the panel, for join
// commands and installs.
func (h *Handler) WithAgentOrigin(origin func(r *http.Request) (AgentOrigin, error)) *Handler {
	h.agentOrigin = origin
	return h
}

func (h *Handler) originOf(r *http.Request) (AgentOrigin, error) {
	if h.agentOrigin != nil {
		return h.agentOrigin(r)
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return AgentOrigin{URL: scheme + "://" + r.Host, InsecureTLS: scheme == "https"}, nil
}

// certChanged is what a join command says when the panel's certificate is
// no longer the one it pinned: Traefik makes a new one each time it starts.
const certChanged = "The panel's certificate changed since this command was made (the panel restarted): make a new join command in the panel."

// JoinResponse is a join token with the command that uses it, shown once.
type JoinResponse struct {
	Token               string    `json:"token"`
	Command             string    `json:"command"`
	ExpiresAt           time.Time `json:"expires_at"`
	PanelKeyFingerprint string    `json:"panel_key_fingerprint"`
}

func (h *Handler) joinResponse(c *gin.Context, tok *JoinToken) (*JoinResponse, error) {
	fp, err := h.service.PanelKeyFingerprint(c.Request.Context())
	if err != nil {
		return nil, err
	}
	o, err := h.originOf(c.Request)
	if err != nil {
		return nil, err
	}
	// The token reaches curl on stdin from printf, a shell builtin, and the
	// panel writes it into the script: no command line `ps` shows on the
	// server carries it (D-123, measured).
	auth := fmt.Sprintf(`printf 'Authorization: Bearer %%s\n' %s`, tok.Token)
	command := fmt.Sprintf("%s | curl -fsSL -H @- %s/api/v1/agent/install.sh | sudo sh", auth, o.URL)
	if o.InsecureTLS {
		// TLS can't vouch for the script (the panel has no domain, so no
		// certificate), so the command carries the script's checksum and
		// the script the agent's and the panel key's. The token goes only
		// to the certificate the panel serves now (curl's 90: another).
		script, err := h.service.InstallScript(c.Request.Context(), o, tok.Token)
		if err != nil {
			return nil, err
		}
		command = fmt.Sprintf(`f=$(mktemp) && (trap 'rm -f "$f"' EXIT; %s | curl -fsSLk --pinnedpubkey sha256//%s -H @- %s/api/v1/agent/install.sh -o "$f" || { [ $? = 90 ] && echo "%s" >&2; exit 1; }; echo "%x  $f" | sha256sum -c && sudo sh "$f")`,
			auth, o.CertPin, o.URL, certChanged, sha256.Sum256([]byte(script)))
	}
	return &JoinResponse{
		Token:               tok.Token,
		Command:             command,
		ExpiresAt:           tok.ExpiresAt,
		PanelKeyFingerprint: fp,
	}, nil
}

// createAgentServerInput is POST /servers with connection "agent".
type createAgentServerInput struct {
	Connection string `json:"connection" binding:"required,eq=agent"`
	Name       string `json:"name" binding:"max=63"`
	IPAddress  string `json:"ip_address" binding:"omitempty,ip|hostname"`
}

// AgentServerResponse is a server made to join by token, with its token.
type AgentServerResponse struct {
	ServerResponse
	Join *JoinResponse `json:"join"`
}

func (h *Handler) createAgentServer(c *gin.Context, orgID, userID string) {
	var in createAgentServerInput
	if err := c.ShouldBindBodyWith(&in, binding.JSON); err != nil {
		respond.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	srv, tok, reused, err := h.service.CreateAgentServer(c.Request.Context(), orgID, userID, strings.TrimSpace(in.Name), in.IPAddress)
	if err != nil {
		h.agentError(c, "create server", err)
		return
	}
	join, err := h.joinResponse(c, tok)
	if err != nil {
		h.agentError(c, "join token", err)
		return
	}
	if !reused {
		h.orgSvc.WriteAuditLogDetail(c.Request.Context(), orgID, userID, "server.created", "server:"+srv.ID, map[string]any{"name": srv.Name, "connection": ConnectionAgent})
	}
	h.service.AuditJoinToken(c.Request.Context(), orgID, userID, srv.ID, srv.Name, false)
	resp := toResponse(srv)
	h.service.WithAgentView(c.Request.Context(), &resp)
	respond.JSON(c, http.StatusCreated, AgentServerResponse{ServerResponse: resp, Join: join})
}

// createJoinToken makes a new join token: owners and operators. On a server
// whose agent key is pinned it is a rejoin, with the password too.
func (h *Handler) createJoinToken(c *gin.Context) {
	ctx, serverID, userID := c.Request.Context(), c.Param("serverID"), middleware.UserIDFrom(c)
	pinned, err := h.service.AgentKeyPinned(ctx, serverID)
	if err != nil {
		h.agentError(c, "join token", err)
		return
	}
	if err := h.service.AuthorizeAgentAdmin(ctx, userID, middleware.MemberRoleFrom(c)); err != nil {
		h.agentError(c, "join token", err)
		return
	}
	if pinned && !middleware.RequireReauth(c) {
		return
	}
	tok, rejoin, err := h.service.CreateJoinToken(ctx, serverID, userID)
	if err != nil {
		h.agentError(c, "join token", err)
		return
	}
	join, err := h.joinResponse(c, tok)
	if err != nil {
		h.agentError(c, "join token", err)
		return
	}
	name := ""
	if srv, err := h.service.GetServer(ctx, serverID); err == nil {
		name = srv.Name
	}
	h.service.AuditJoinToken(ctx, middleware.OrgIDFrom(c), userID, serverID, name, rejoin)
	respond.JSON(c, http.StatusCreated, join)
}

// adminGate: owners and operators, never an API key, with the password.
func (h *Handler) adminGate(c *gin.Context, password bool) bool {
	if middleware.ViaAPIKey(c) {
		respond.ErrorData(c, http.StatusForbidden, "this needs a signed-in session, not an API key", gin.H{"code": "session_required"})
		return false
	}
	if err := h.service.AuthorizeAgentAdmin(c.Request.Context(), middleware.UserIDFrom(c), middleware.MemberRoleFrom(c)); err != nil {
		h.agentError(c, "agent", err)
		return false
	}
	return !password || middleware.RequireReauth(c)
}

// installAgent moves an SSH server to the agent, streaming progress (SSE).
func (h *Handler) installAgent(c *gin.Context) {
	if !h.adminGate(c, true) {
		return
	}
	serverID, userID := c.Param("serverID"), middleware.UserIDFrom(c)
	keep := c.Query("keep_ssh_key") != "false"
	origin, err := h.originOf(c.Request)
	if err != nil {
		h.agentError(c, "install agent", err)
		return
	}
	respond.Stream(c)
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	logFn := func(msg string) {
		for _, line := range strings.Split(msg, "\n") {
			fmt.Fprintf(c.Writer, "data: %s\n\n", line)
		}
		c.Writer.Flush()
	}
	if err := h.service.InstallAgent(c.Request.Context(), userID, serverID, origin, keep, logFn); err != nil {
		h.log.Warn("installing the agent failed", zap.Error(err), zap.String("server_id", serverID))
		fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", strings.ReplaceAll(err.Error(), "\n", " "))
	} else {
		fmt.Fprintf(c.Writer, "event: done\ndata: moved\n\n")
	}
	c.Writer.Flush()
}

// MoveConnectionInput is POST /servers/:serverID/connection.
type MoveConnectionInput struct {
	Mode   string `json:"mode" binding:"required,oneof=ssh agent"`
	User   string `json:"user" binding:"omitempty,max=64"`
	Port   int    `json:"port" binding:"omitempty,min=1,max=65535"`
	SSHKey string `json:"ssh_key" binding:"omitempty,max=16384"`
}

func (h *Handler) moveConnection(c *gin.Context) {
	if !h.adminGate(c, true) {
		return
	}
	var in MoveConnectionInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	if in.Mode == ConnectionAgent {
		respond.ErrorData(c, http.StatusBadRequest, "moving to the agent installs it: use POST /servers/{serverID}/agent/install", gin.H{"code": "use_install"})
		return
	}
	ctx, serverID := c.Request.Context(), c.Param("serverID")
	if err := h.service.MoveToSSH(ctx, middleware.UserIDFrom(c), serverID, in.User, in.Port, in.SSHKey); err != nil {
		h.agentError(c, "move to SSH", err)
		return
	}
	h.respondServer(c, serverID)
}

func (h *Handler) rotateAgentKey(c *gin.Context) {
	if middleware.ViaAPIKey(c) {
		respond.ErrorData(c, http.StatusForbidden, "this needs a signed-in session, not an API key", gin.H{"code": "session_required"})
		return
	}
	if middleware.MemberRoleFrom(c) != "owner" {
		respond.Error(c, http.StatusForbidden, ErrAgentOwnerOnly.Error())
		return
	}
	serverID := c.Param("serverID")
	if _, err := h.service.RotateAgentKey(c.Request.Context(), middleware.UserIDFrom(c), serverID); err != nil {
		h.agentError(c, "rotate", err)
		return
	}
	h.respondServer(c, serverID)
}

func (h *Handler) revokeAgent(c *gin.Context) {
	if !h.adminGate(c, false) {
		return
	}
	serverID := c.Param("serverID")
	if err := h.service.RevokeAgent(c.Request.Context(), middleware.UserIDFrom(c), serverID, c.Query("uninstall") == "true"); err != nil {
		h.agentError(c, "revoke", err)
		return
	}
	h.respondServer(c, serverID)
}

// chooseAgentState ends a frozen server's freeze (D-113): adopt what runs
// there, or use the panel's state.
func (h *Handler) chooseAgentState(adopt bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !h.adminGate(c, true) {
			return
		}
		serverID := c.Param("serverID")
		if err := h.service.ChooseAgentState(c.Request.Context(), middleware.UserIDFrom(c), serverID, adopt); err != nil {
			h.agentError(c, "choose the server's state", err)
			return
		}
		h.respondServer(c, serverID)
	}
}

func (h *Handler) respondServer(c *gin.Context, serverID string) {
	srv, err := h.service.GetServer(c.Request.Context(), serverID)
	if err != nil {
		respond.Error(c, http.StatusNotFound, "server not found")
		return
	}
	resp := toResponse(srv)
	h.service.WithAgentView(c.Request.Context(), &resp)
	respond.JSON(c, http.StatusOK, resp)
}

// agentError maps the agent's errors to answers with stable codes.
func (h *Handler) agentError(c *gin.Context, what string, err error) {
	var strict *StrictInUseError
	if errors.As(err, &strict) {
		respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "strict_network_in_use", "services": strict.Services})
		return
	}
	code, status := "", http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrAgentOperatorOrOwner), errors.Is(err, ErrAgentOwnerOnly):
		respond.Error(c, http.StatusForbidden, err.Error())
		return
	case errors.Is(err, ErrAgentsNotConfigured), errors.Is(err, ErrAgentBinaries):
		code, status = "agent_unavailable", http.StatusServiceUnavailable
	case errors.Is(err, ErrAgentOffline):
		code, status = "agent_offline", http.StatusConflict
	case errors.Is(err, ErrNotFrozen):
		code, status = "not_frozen", http.StatusConflict
	case errors.Is(err, ErrNoAgent):
		code, status = "no_agent", http.StatusConflict
	case errors.Is(err, ErrAlreadyAgent), errors.Is(err, ErrAlreadySSH):
		code, status = "connection_unchanged", http.StatusConflict
	case errors.Is(err, ErrNoSSHKey):
		code, status = "ssh_key_required", http.StatusBadRequest
	case errors.Is(err, ErrSSHFailed):
		code, status = "ssh_failed", http.StatusBadGateway
	case errors.Is(err, ErrServerNotFound):
		respond.Error(c, http.StatusNotFound, "server not found")
		return
	case errors.Is(err, ErrInvalidServerName):
		code, status = "invalid_server_name", http.StatusBadRequest
	case errors.Is(err, ErrServerNameTaken):
		code, status = "server_name_taken", http.StatusConflict
	case errors.Is(err, ErrServerJoined):
		code, status = "server_joined", http.StatusConflict
	}
	if code == "" {
		h.log.Error(what+" failed", zap.Error(err), zap.String("server_id", c.Param("serverID")))
		respond.Error(c, status, what+" failed")
		return
	}
	respond.ErrorData(c, status, err.Error(), gin.H{"code": code})
}

//go:embed agent-install.sh
var agentInstallScript string

// ─── Public: the install script, the binary, the connection ────────────────

// RegisterAgentRoutes mounts the agent's public routes on /api/v1, each
// behind limit (per address). The connection authenticates itself: a join
// token or the agent's pinned key, then SSH.
func (h *Handler) RegisterAgentRoutes(v1 *gin.RouterGroup, hub *agenthub.Hub, limit gin.HandlerFunc) {
	a := v1.Group("/agent", limit)
	a.GET("/install.sh", h.installScript)
	a.GET("/binary", h.agentBinary)
	a.GET("/connect", func(c *gin.Context) { hub.Connect(c.Writer, c.Request, c.ClientIP()) })
}

func (h *Handler) installScript(c *gin.Context) {
	tok, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
	if !ok {
		tok = "" // a proxy's basic auth, say: no join token
	}
	o, err := h.originOf(c.Request)
	if err != nil {
		h.agentError(c, "install script", err)
		return
	}
	script, err := h.service.InstallScript(c.Request.Context(), o, strings.TrimSpace(tok))
	if errors.Is(err, ErrUnknownToken) {
		respond.ErrorData(c, http.StatusBadRequest, "the bearer token is not a join token", gin.H{"code": "token_invalid"})
		return
	}
	if err != nil {
		h.agentError(c, "install script", err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/x-shellscript; charset=utf-8", []byte(script))
}

func (h *Handler) agentBinary(c *gin.Context) {
	tok, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
	if !ok {
		respond.Error(c, http.StatusUnauthorized, "a join token is required")
		return
	}
	serverID, bin, err := h.service.AgentBinary(c.Request.Context(), strings.TrimSpace(tok), c.Query("arch"))
	if errors.Is(err, ErrUnknownToken) {
		respond.ErrorData(c, http.StatusUnauthorized, "the join token is unknown, used or expired: make a new one in the panel", gin.H{"code": "token_invalid"})
		return
	}
	if err != nil {
		h.agentError(c, "agent download", err)
		return
	}
	c.Header("X-Toft-Server", serverID)
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "application/octet-stream", bin)
}

// ─── Add a server (D-153) ────────────────────────────────────────────────────

type renameServerInput struct {
	Name string `json:"name" binding:"required,max=63"`
}

// renameServer gives a server a new name: owners.
func (h *Handler) renameServer(c *gin.Context) {
	if middleware.MemberRoleFrom(c) != "owner" {
		respond.Error(c, http.StatusForbidden, "only owners can rename servers")
		return
	}
	var in renameServerInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	ctx, id := c.Request.Context(), c.Param("serverID")
	before, err := h.service.GetServer(ctx, id)
	if err != nil {
		h.agentError(c, "rename server", err)
		return
	}
	srv, err := h.service.RenameServer(ctx, id, in.Name)
	if err != nil {
		h.agentError(c, "rename server", err)
		return
	}
	if before.Name != srv.Name {
		h.orgSvc.WriteAuditLogDetail(ctx, middleware.OrgIDFrom(c), middleware.UserIDFrom(c), "server.renamed", "server:"+id, map[string]any{"from": before.Name, "to": srv.Name})
	}
	resp := toResponse(srv)
	h.service.WithAgentView(ctx, &resp)
	respond.JSON(c, http.StatusOK, resp)
}

// ServerSSHKeyResponse is the public line of the key Toft made.
type ServerSSHKeyResponse struct {
	PublicKey string `json:"public_key"`
}

// serverSSHKey makes, once, the key a waiting server is reached with over
// SSH: owners.
func (h *Handler) serverSSHKey(c *gin.Context) {
	if middleware.MemberRoleFrom(c) != "owner" {
		respond.Error(c, http.StatusForbidden, "only owners can add servers")
		return
	}
	pub, err := h.service.WaitingServerSSHKey(c.Request.Context(), c.Param("serverID"))
	if err != nil {
		h.agentError(c, "make the key", err)
		return
	}
	respond.JSON(c, http.StatusOK, ServerSSHKeyResponse{PublicKey: pub})
}

type useSSHInput struct {
	IPAddress string `json:"ip_address" binding:"required,ip|hostname"`
	Port      int    `json:"port" binding:"required,min=1,max=65535"`
	SSHKey    string `json:"ssh_key" binding:"max=16384"`
}

// useSSH reaches a waiting server over SSH instead, as root: owners.
func (h *Handler) useSSH(c *gin.Context) {
	if middleware.MemberRoleFrom(c) != "owner" {
		respond.Error(c, http.StatusForbidden, "only owners can add servers")
		return
	}
	var in useSSHInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	ctx, id := c.Request.Context(), c.Param("serverID")
	srv, err := h.service.UseSSH(ctx, id, strings.TrimSpace(in.IPAddress), in.Port, in.SSHKey)
	if err != nil {
		h.agentError(c, "use SSH", err)
		return
	}
	h.orgSvc.WriteAuditLogDetail(ctx, middleware.OrgIDFrom(c), middleware.UserIDFrom(c), "server.connection_moved", "server:"+id,
		map[string]any{"name": srv.Name, "to": ConnectionSSH, "own_key": in.SSHKey != ""})
	resp := toResponse(srv)
	h.service.WithAgentView(ctx, &resp)
	respond.JSON(c, http.StatusOK, resp)
}
