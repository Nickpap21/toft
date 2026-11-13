package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/Nickpap21/toft/backend/pkg/sshclient"
	"github.com/Nickpap21/toft/backend/pkg/sshpool"
	"github.com/Nickpap21/toft/backend/pkg/wspty"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

// OrgAuditWriter is satisfied by org.Service; defined here to avoid a circular import.
type OrgAuditWriter interface {
	WriteAuditLog(ctx context.Context, orgID, actorID, action, resource string)
	WriteAuditLogDetail(ctx context.Context, orgID, actorID, action, resource string, detail map[string]any)
}

// Handler is the thin HTTP adapter for the server domain.
type Handler struct {
	// wsUpgrader is set by WithAllowedOrigins; the zero value refuses every
	// cross-origin browser request (gorilla's default check).
	wsUpgrader websocket.Upgrader
	service    *Service
	orgSvc     OrgAuditWriter
	log        *zap.Logger
	// The open host shells, which EndShells waits for when the backend
	// stops so that each one's audit row closes.
	shells sync.WaitGroup
	// agentOrigin: see WithAgentOrigin.
	agentOrigin func(r *http.Request) (AgentOrigin, error)
}

func NewHandler(s *Service, orgSvc OrgAuditWriter, log *zap.Logger) *Handler {
	return &Handler{service: s, orgSvc: orgSvc, log: log}
}

// EndShells waits, until ctx ends, for the open host shells to close and
// write their audit rows, once the backend has ended their requests.
func (h *Handler) EndShells(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		h.shells.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// RegisterRoutes sets up the endpoints for the server domain.
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	servers := rg.Group("/servers")
	{
		servers.GET("", h.listServers)
		servers.POST("", h.createServer)
		servers.GET("/:serverID", h.requireOwnership(), h.getServer)
		servers.DELETE("/:serverID", h.requireOwnership(), h.deleteServer)
		servers.PATCH("/:serverID", h.requireOwnership(), h.renameServer)
		servers.POST("/:serverID/ssh-key", h.requireOwnership(), h.serverSSHKey)
		servers.PUT("/:serverID/ssh", h.requireOwnership(), h.useSSH)
		servers.GET("/:serverID/test-connection", h.requireOwnership(), h.testConnection)
		servers.POST("/:serverID/provision", h.requireOwnership(), h.provision)
		servers.POST("/:serverID/exec", h.requireOwnership(), h.execOnServer)
		servers.GET("/:serverID/system-stats", h.requireOwnership(), h.getSystemStats)
		servers.GET("/:serverID/pty", h.requireOwnership(), h.handlePTY)
		// Owners see the server; a workspace with a share sees its share.
		servers.GET("/:serverID/capacity-summary", h.handleGetCapacitySummary)
		servers.GET("/:serverID/fit", h.handleFit)
		servers.PUT("/:serverID/capacity", h.requireOwnership(), h.handleUpdateCapacity)
		servers.POST("/:serverID/discover-capacity", h.requireOwnership(), h.handleDiscoverCapacity)
		servers.GET("/:serverID/capabilities", h.requireOwnership(), h.getCapabilities)
		servers.POST("/:serverID/capabilities", h.requireOwnership(), h.probeCapabilities)
		servers.GET("/:serverID/updates", h.requireOwnership(), h.getUpdates)
		servers.POST("/:serverID/updates", h.requireOwnership(), h.readUpdates)
		servers.GET("/:serverID/updates/run", h.requireOwnership(), h.getUpdateRun)
		servers.POST("/:serverID/updates/run", h.requireOwnership(), h.startUpdateRun)
		servers.GET("/:serverID/gvisor", h.requireOwnership(), h.getGvisorRun)
		servers.POST("/:serverID/gvisor", h.requireOwnership(), h.installGvisor)
		servers.GET("/:serverID/services", h.handleListServices)
		servers.GET("/:serverID/provisioning", h.requireOwnership(), h.getProvisionRun)
		servers.GET("/:serverID/requirements", h.requireOwnership(), h.getRequirements)
		servers.POST("/:serverID/host-key/scan", h.requireOwnership(), h.scanHostKey)
		servers.PUT("/:serverID/host-key", h.requireOwnership(), h.resetHostKey)
		servers.GET("/:serverID/dns-provider", h.requireOwnership(), h.getDNSProvider)
		servers.PUT("/:serverID/dns-provider", h.requireOwnership(), h.putDNSProvider)
		servers.DELETE("/:serverID/dns-provider", h.requireOwnership(), h.deleteDNSProvider)
		servers.GET("/:serverID/backup", h.requireOwnership(), h.getBackup)
		servers.PUT("/:serverID/backup", h.requireOwnership(), h.putBackup)
		servers.DELETE("/:serverID/backup", h.requireOwnership(), h.deleteBackup)
		servers.GET("/:serverID/shares", h.requireOwnership(), h.listShares)
		servers.PUT("/:serverID/shares/:orgID", h.requireOwnership(), h.putShare)
		servers.DELETE("/:serverID/shares/:orgID", h.requireOwnership(), h.deleteShare)
		// The node agent (D-106).
		servers.POST("/:serverID/agent/join-token", h.requireOwnership(), h.createJoinToken)
		servers.POST("/:serverID/agent/install", h.requireOwnership(), h.installAgent)
		servers.POST("/:serverID/agent/rotate", h.requireOwnership(), h.rotateAgentKey)
		servers.DELETE("/:serverID/agent", h.requireOwnership(), h.revokeAgent)
		servers.POST("/:serverID/agent/adopt", h.requireOwnership(), h.chooseAgentState(true))
		servers.POST("/:serverID/agent/use-panel-state", h.requireOwnership(), h.chooseAgentState(false))
		servers.POST("/:serverID/connection", h.requireOwnership(), h.moveConnection)
	}
	// The panel host's backup: instance operators (checked in the service).
	panel := rg.Group("/instance/backup")
	{
		panel.GET("", h.getPanelBackup)
		panel.PUT("", h.putPanelBackup)
		panel.DELETE("", h.deletePanelBackup)
	}
}

// requireOwnership verifies the authenticated org owns the server before
// proceeding: 404 for a server the workspace can't see, 403 for one shared
// with it.
func (h *Handler) requireOwnership() gin.HandlerFunc {
	return func(c *gin.Context) {
		owns, ok := h.ownerOrShare(c, c.Param("serverID"))
		if ok && !owns {
			respond.Error(c, http.StatusForbidden, "only the workspace that owns this server can do this")
		}
		if !owns {
			c.Abort()
			return
		}
		c.Next()
	}
}

func (h *Handler) listServers(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	if orgID == "" {
		respond.Error(c, http.StatusForbidden, "no org context")
		return
	}
	servers, err := h.service.ListServers(c.Request.Context(), orgID)
	if err != nil {
		h.log.Error("list servers failed", zap.Error(err), zap.String("org_id", orgID))
		respond.Error(c, http.StatusInternalServerError, "failed to list servers")
		return
	}
	shared, err := h.service.ListSharedWithOrg(c.Request.Context(), orgID)
	if err != nil {
		h.log.Error("list shared servers failed", zap.Error(err), zap.String("org_id", orgID))
		respond.Error(c, http.StatusInternalServerError, "failed to list servers")
		return
	}
	// The workspace's own servers, then those shared with it (no address).
	out := make([]any, 0, len(servers)+len(shared))
	own := toResponses(servers)
	h.service.WithAgentViews(c.Request.Context(), orgID, own)
	h.service.WithProvisionWarnings(c.Request.Context(), orgID, own)
	for _, r := range own {
		out = append(out, r)
	}
	for _, sh := range shared {
		out = append(out, toSharedResponse(sh))
	}
	respond.JSON(c, http.StatusOK, out)
}

func (h *Handler) createServer(c *gin.Context) {
	// A server is shared infrastructure every project here can deploy to.
	if middleware.MemberRoleFrom(c) != "owner" {
		respond.Error(c, http.StatusForbidden, "only owners can add servers")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	userID := middleware.UserIDFrom(c)
	if orgID == "" {
		respond.Error(c, http.StatusForbidden, "no org context")
		return
	}
	var mode struct {
		Connection string `json:"connection"`
	}
	if err := c.ShouldBindBodyWith(&mode, binding.JSON); err != nil {
		respond.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	if mode.Connection == ConnectionAgent {
		h.createAgentServer(c, orgID, userID)
		return
	}
	var input CreateServerInput
	if err := c.ShouldBindBodyWith(&input, binding.JSON); err != nil || (mode.Connection != "" && mode.Connection != ConnectionSSH) {
		respond.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	srv, err := h.service.CreateServer(c.Request.Context(), orgID, userID, input)
	if errors.Is(err, ErrInvalidServerName) || errors.Is(err, ErrServerNameTaken) {
		h.agentError(c, "create server", err)
		return
	}
	if err != nil {
		h.log.Error("create server failed", zap.Error(err), zap.String("org_id", orgID))
		respond.Error(c, http.StatusInternalServerError, "failed to create server")
		return
	}
	h.orgSvc.WriteAuditLog(c.Request.Context(), orgID, userID, "server.created", "server:"+srv.ID)
	resp := toResponse(srv)
	h.service.WithAgentView(c.Request.Context(), &resp)
	respond.JSON(c, http.StatusCreated, resp)
}

func (h *Handler) getServer(c *gin.Context) {
	serverID := c.Param("serverID")
	srv, err := h.service.GetServer(c.Request.Context(), serverID)
	if err != nil {
		h.log.Error("get server failed", zap.Error(err), zap.String("server_id", serverID))
		respond.Error(c, http.StatusNotFound, "server not found")
		return
	}
	resp := toResponse(srv)
	resp.AppNetworks = h.service.AppNetworks()
	resp.AddressPool = h.service.FirstAddressPool(c.Request.Context(), srv)
	h.service.WithAgentView(c.Request.Context(), &resp)
	respond.JSON(c, http.StatusOK, resp)
}

// testConnection checks the server answers over SSH (its pooled connection)
// and reports the host key it presented.
func (h *Handler) testConnection(c *gin.Context) {
	serverID := c.Param("serverID")
	result, err := h.service.TestConnection(c.Request.Context(), serverID)
	if err != nil {
		h.log.Warn("test connection failed", zap.Error(err), zap.String("server_id", serverID))
		respond.Error(c, http.StatusBadGateway, "connection failed: "+err.Error())
		return
	}
	respond.JSON(c, http.StatusOK, result)
}

// isLocalAddress returns true if the host resolves to the loopback interface.
func isLocalAddress(host string) bool {
	local := []string{"localhost", "127.0.0.1", "::1", "0.0.0.0"}
	for _, l := range local {
		if host == l {
			return true
		}
	}
	return false
}

// provision runs the idempotent server setup and streams progress via SSE.
func (h *Handler) provision(c *gin.Context) {
	if middleware.MemberRoleFrom(c) == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	serverID := c.Param("serverID")

	srv, err := h.service.GetServer(c.Request.Context(), serverID)
	if err != nil {
		respond.Error(c, http.StatusNotFound, "server not found")
		return
	}
	if isLocalAddress(srv.IPAddress) {
		respond.Error(c, http.StatusBadRequest, "cannot provision the local server — it already runs Docker and Traefik via Docker Compose")
		return
	}

	respond.Stream(c)
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Transfer-Encoding", "chunked")

	logFn := func(msg string) {
		fmt.Fprintf(c.Writer, "data: %s\n\n", msg)
		c.Writer.Flush()
	}

	if err := h.service.Provision(c.Request.Context(), serverID, middleware.UserIDFrom(c), logFn); err != nil {
		h.log.Error("provision failed", zap.Error(err), zap.String("server_id", serverID))
		fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", err.Error())
	} else {
		orgID := middleware.OrgIDFrom(c)
		userID := middleware.UserIDFrom(c)
		h.orgSvc.WriteAuditLog(c.Request.Context(), orgID, userID, "server.provisioned", "server:"+serverID)
		fmt.Fprintf(c.Writer, "event: done\ndata: provisioned\n\n")
	}
	c.Writer.Flush()
}

func (h *Handler) deleteServer(c *gin.Context) {
	if middleware.MemberRoleFrom(c) == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	userID := middleware.UserIDFrom(c)
	serverID := c.Param("serverID")
	withServices := c.Query("with_services") == "true"
	// Taking its services (their deployments, variables and secrets) with it
	// is the owners', with the password, as a project's delete is.
	if withServices {
		if middleware.MemberRoleFrom(c) != "owner" {
			respond.Error(c, http.StatusForbidden, "only owners can remove a server with its services")
			return
		}
		if !middleware.RequireReauth(c) {
			return
		}
	}
	// Its name, for the audit log once it is gone.
	var name string
	if srv, err := h.service.GetServer(c.Request.Context(), serverID); err == nil {
		name = srv.Name
	}
	if err := h.service.DeleteServer(c.Request.Context(), serverID, withServices); errors.Is(err, ErrServerInUse) {
		respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "server_in_use"})
		return
	} else if errors.Is(err, ErrServerHoldsVolumes) || errors.Is(err, ErrServerShared) {
		respond.Error(c, http.StatusConflict, err.Error())
		return
	} else if err != nil {
		h.log.Error("delete server failed", zap.Error(err), zap.String("server_id", serverID))
		respond.Error(c, http.StatusInternalServerError, "failed to delete server")
		return
	}
	h.orgSvc.WriteAuditLogDetail(c.Request.Context(), orgID, userID, "server.deleted", "server:"+serverID, map[string]any{"name": name})
	respond.NoContent(c)
}

type serverExecRequest struct {
	Command string `json:"command" binding:"required,max=4000"`
}

// execOnServer streams the output of an arbitrary shell command run on the
// remote server via SSH. Output is delivered as SSE lines.
func (h *Handler) execOnServer(c *gin.Context) {
	serverID := c.Param("serverID")
	userID := middleware.UserIDFrom(c)
	if err := h.service.AuthorizeHostShell(c.Request.Context(), userID); err != nil {
		h.writeHostShellError(c, err)
		return
	}
	if !middleware.RequireReauth(c) {
		return
	}
	var body serverExecRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		respond.Error(c, http.StatusBadRequest, "command is required")
		return
	}
	command := body.Command
	if r := []rune(command); len(r) > 500 {
		command = string(r[:500]) + "…"
	}
	h.orgSvc.WriteAuditLogDetail(c.Request.Context(), middleware.OrgIDFrom(c), userID, "server.command_run", "server:"+serverID, map[string]any{"command": command})

	respond.Stream(c)
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Transfer-Encoding", "chunked")

	logFn := func(line string) {
		line = strings.ReplaceAll(line, "\n", " ")
		fmt.Fprintf(c.Writer, "data: %s\n\n", line)
		c.Writer.Flush()
	}

	if err := h.service.ExecOnServer(c.Request.Context(), userID, serverID, body.Command, logFn); err != nil {
		if !errors.Is(err, context.Canceled) {
			h.log.Error("server exec failed", zap.Error(err), zap.String("server_id", serverID))
			logFn("ERROR: " + err.Error())
		}
	}

	fmt.Fprint(c.Writer, "event: done\ndata: {}\n\n")
	c.Writer.Flush()
}

// getSystemStats returns a one-shot resource snapshot (CPU, memory, disk, load,
// uptime, docker version) collected from the remote server via SSH.
func (h *Handler) getSystemStats(c *gin.Context) {
	serverID := c.Param("serverID")
	stats, err := h.service.GetSystemStats(c.Request.Context(), serverID)
	if err != nil {
		h.log.Error("get system stats failed", zap.Error(err), zap.String("server_id", serverID))
		respond.Error(c, http.StatusBadGateway, "failed to collect stats: "+err.Error())
		return
	}
	respond.JSON(c, http.StatusOK, stats)
}

// WithAllowedOrigins limits the PTY WebSocket to the API's own origin and
// to origins (the same list as CORS); see wspty.Upgrader.
func (h *Handler) WithAllowedOrigins(origins func() []string) *Handler {
	h.wsUpgrader = wspty.Upgrader(origins)
	return h
}

// handlePTY upgrades the connection to WebSocket and bridges it to an SSH PTY
// session on the remote server (pkg/wspty has the messages). The browser
// passes the JWT as ?token=... because the WebSocket API cannot set custom
// headers.
func (h *Handler) handlePTY(c *gin.Context) {
	serverID := c.Param("serverID")
	userID := middleware.UserIDFrom(c)

	// Refuse before the WebSocket upgrade so the client gets a plain 403.
	if err := h.service.AuthorizeHostShell(c.Request.Context(), userID); err != nil {
		h.writeHostShellError(c, err)
		return
	}
	if !middleware.RequireReauth(c) {
		return
	}
	cols, rows := wspty.Size(c.Request)

	// Counted before the upgrade: the HTTP server's Shutdown stops waiting
	// for a connection once it is hijacked, and EndShells takes over.
	h.shells.Add(1)
	defer h.shells.Done()
	conn, err := h.wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.log.Error("PTY WebSocket upgrade failed", zap.Error(err), zap.String("server_id", serverID))
		return
	}
	defer conn.Close()

	pty, err := h.service.OpenPTY(c.Request.Context(), userID, serverID, cols, rows)
	if err != nil {
		h.log.Error("open PTY failed", zap.Error(err), zap.String("server_id", serverID))
		wspty.Fail(conn, err.Error())
		return
	}
	defer pty.Close()
	orgID, opened := middleware.OrgIDFrom(c), time.Now()
	h.orgSvc.WriteAuditLogDetail(c.Request.Context(), orgID, userID, "server.shell_opened", "server:"+serverID, map[string]any{"from": c.ClientIP()})

	// The auth middleware cancels the request when the user is deactivated,
	// which closes the socket and, through the defer, the SSH session; so
	// does the backend when it stops.
	ctx := c.Request.Context()
	wspty.Serve(ctx, conn, pty)

	detail := map[string]any{"seconds": int(time.Since(opened).Seconds())}
	if wspty.Stopping(ctx) {
		detail["restart"] = true
	}
	h.orgSvc.WriteAuditLogDetail(context.WithoutCancel(ctx), orgID, userID, "server.shell_closed", "server:"+serverID, detail)
}

// writeHostShellError answers a refused host shell: 403 for non-operators,
// 500 when the operator check itself failed.
func (h *Handler) writeHostShellError(c *gin.Context, err error) {
	if errors.Is(err, ErrHostShellOperatorOnly) {
		respond.Error(c, http.StatusForbidden, err.Error())
		return
	}
	h.log.Error("host shell authorization failed", zap.Error(err))
	respond.Error(c, http.StatusInternalServerError, "failed to verify permissions")
}

// ownerOrShare reports whether the caller's workspace owns the server, and
// whether it may see it at all (it owns it or has a share). It writes the
// error response when the check fails or the answer is no.
func (h *Handler) ownerOrShare(c *gin.Context, serverID string) (owns, ok bool) {
	ctx, orgID := c.Request.Context(), middleware.OrgIDFrom(c)
	owns, err := h.service.CheckOwnership(ctx, serverID, orgID)
	if err != nil {
		h.log.Error("server ownership check failed", zap.Error(err), zap.String("server_id", serverID))
		respond.Error(c, http.StatusInternalServerError, "failed to verify ownership")
		return false, false
	}
	if owns {
		return true, true
	}
	share, err := h.service.CanUse(ctx, serverID, orgID)
	if err != nil {
		h.log.Error("server share check failed", zap.Error(err), zap.String("server_id", serverID))
		respond.Error(c, http.StatusInternalServerError, "failed to verify ownership")
		return false, false
	}
	if !share {
		respond.Error(c, http.StatusNotFound, "server not found")
		return false, false
	}
	return false, true
}

// GET /servers/:serverID/capacity-summary
func (h *Handler) handleGetCapacitySummary(c *gin.Context) {
	id := c.Param("serverID")
	ctx := c.Request.Context()

	owns, ok := h.ownerOrShare(c, id)
	if !ok {
		return
	}
	var summary *CapacitySummary
	var err error
	if owns {
		summary, err = h.service.GetCapacitySummary(ctx, id)
	} else {
		// A guest sees its share only: no live figures for the whole server.
		summary, err = h.service.GetCapacitySummaryFor(ctx, id, middleware.OrgIDFrom(c))
	}
	if err != nil {
		h.log.Error("get capacity summary", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to get capacity summary")
		return
	}
	respond.JSON(c, http.StatusOK, summary)
}

// GET /servers/:serverID/fit?cpu_cores=&memory_mb= — whether a service with
// these limits fits on the server for the caller's workspace, as the deploy
// capacity gate would decide. Without them it is the room left.
func (h *Handler) handleFit(c *gin.Context) {
	id := c.Param("serverID")
	if _, ok := h.ownerOrShare(c, id); !ok {
		return
	}
	cpu, err := queryAmount(c, "cpu_cores", 1024)
	if err != nil {
		respond.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	mem, err := queryAmount(c, "memory_mb", 16<<20)
	if err != nil {
		respond.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	res, err := h.service.Fit(c.Request.Context(), id, middleware.OrgIDFrom(c), cpu, mem)
	if errors.Is(err, ErrServerNotReady) {
		respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "server_not_ready"})
		return
	}
	if err != nil {
		h.log.Error("capacity fit", zap.Error(err), zap.String("server_id", id))
		respond.Error(c, http.StatusInternalServerError, "failed to check capacity")
		return
	}
	respond.JSON(c, http.StatusOK, res)
}

// queryAmount reads an optional non-negative number from the query; absent is 0.
func queryAmount(c *gin.Context, name string, max float64) (float64, error) {
	raw := c.Query(name)
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(v) || v < 0 || v > max {
		return 0, fmt.Errorf("%s must be a number from 0 to %g", name, max)
	}
	return v, nil
}

// PUT /servers/:serverID/capacity
func (h *Handler) handleUpdateCapacity(c *gin.Context) {
	id := c.Param("serverID")

	if middleware.MemberRoleFrom(c) != "owner" {
		respond.Error(c, http.StatusForbidden, "only owners can override server capacity")
		return
	}

	var input UpdateCapacityInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "invalid input")
		return
	}

	srv, err := h.service.UpdateCapacity(c.Request.Context(), id, input)
	if err != nil {
		h.log.Error("update server capacity", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to update capacity")
		return
	}
	respond.JSON(c, http.StatusOK, toResponse(srv))
}

// GET /servers/:serverID/services
// GET /servers/:serverID/services — the caller's workspace's services on
// the server, for its owner and for a workspace it is shared with.
func (h *Handler) handleListServices(c *gin.Context) {
	id := c.Param("serverID")
	if _, ok := h.ownerOrShare(c, id); !ok {
		return
	}
	services, err := h.service.ListServicesOnServer(c.Request.Context(), id, middleware.OrgIDFrom(c))
	if err != nil {
		h.log.Error("list services on server", zap.Error(err), zap.String("server_id", id))
		respond.Error(c, http.StatusInternalServerError, "failed to list services")
		return
	}
	respond.JSON(c, http.StatusOK, services)
}

// GET /servers/:serverID/requirements — the server checked against D-29's
// requirements over SSH (the first connection pins its host key). Read-only.
func (h *Handler) getRequirements(c *gin.Context) {
	req, err := h.service.CheckRequirements(c.Request.Context(), c.Param("serverID"))
	if err != nil {
		h.log.Warn("check requirements", zap.Error(err), zap.String("server_id", c.Param("serverID")))
		respond.Error(c, http.StatusBadGateway, "could not check the server: "+err.Error())
		return
	}
	respond.JSON(c, http.StatusOK, req)
}

// GET /servers/:serverID/provisioning — the last provisioning run (D-48),
// or null when none is recorded.
func (h *Handler) getProvisionRun(c *gin.Context) {
	run, err := h.service.GetProvisionRun(c.Request.Context(), c.Param("serverID"))
	if errors.Is(err, ErrNoProvisionRun) {
		respond.JSON(c, http.StatusOK, json.RawMessage("null"))
		return
	}
	if err != nil {
		h.log.Error("get provision run", zap.Error(err), zap.String("server_id", c.Param("serverID")))
		respond.Error(c, http.StatusInternalServerError, "failed to read the last provisioning run")
		return
	}
	respond.JSON(c, http.StatusOK, run)
}

// POST /servers/:serverID/discover-capacity
func (h *Handler) handleDiscoverCapacity(c *gin.Context) {
	if middleware.MemberRoleFrom(c) == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	id := c.Param("serverID")
	srv, err := h.service.DiscoverCapacity(c.Request.Context(), id)
	if err != nil {
		h.log.Error("discover capacity failed", zap.Error(err), zap.String("server_id", id))
		respond.Error(c, http.StatusInternalServerError, "capacity discovery failed: "+err.Error())
		return
	}
	respond.JSON(c, http.StatusOK, toResponse(srv))
}

// POST /servers/:serverID/host-key/scan
//
// scanHostKey runs an SSH handshake without credentials and reports the host
// key the server presents next to the pinned one. Developers and up: a
// viewer never makes the panel dial out.
func (h *Handler) scanHostKey(c *gin.Context) {
	if middleware.MemberRoleFrom(c) == "viewer" {
		respond.Error(c, http.StatusForbidden, "viewers can't scan a server's host key")
		return
	}
	serverID := c.Param("serverID")
	result, err := h.service.ScanHostKey(c.Request.Context(), serverID)
	if err != nil {
		h.writeHostKeyError(c, serverID, err)
		return
	}
	respond.JSON(c, http.StatusOK, result)
}

// PUT /servers/:serverID/host-key {fingerprint}
//
// resetHostKey pins the key the server presents now, after the operator
// compared its fingerprint on the server's console. Instance operators only.
func (h *Handler) resetHostKey(c *gin.Context) {
	ctx := c.Request.Context()
	serverID := c.Param("serverID")
	orgID := middleware.OrgIDFrom(c)
	userID := middleware.UserIDFrom(c)

	// Refuse before reading the body so non-operators always get a 403.
	if err := h.service.AuthorizeHostKeyReset(ctx, userID); err != nil {
		h.writeHostKeyError(c, serverID, err)
		return
	}
	if !middleware.RequireReauth(c) {
		return
	}
	var input ResetHostKeyInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "fingerprint is required")
		return
	}
	srv, err := h.service.ResetHostKey(ctx, userID, serverID, input.Fingerprint)
	if err != nil {
		h.writeHostKeyError(c, serverID, err)
		return
	}
	h.orgSvc.WriteAuditLogDetail(ctx, orgID, userID, "server.host_key_reset", "server:"+serverID, map[string]any{"fingerprint": input.Fingerprint})
	respond.JSON(c, http.StatusOK, toResponse(srv))
}

// hostKeyChangedBody is the data of a 409 from resetHostKey.
type hostKeyChangedBody struct {
	KeyType              string `json:"key_type"`
	PresentedFingerprint string `json:"presented_fingerprint"`
}

// writeHostKeyError answers a failed host-key scan or reset: 403 for
// non-operators, 400 for a malformed fingerprint, 409 when the server now
// presents another key than the confirmed one, 502 when the server could
// not be reached over SSH, 500 otherwise.
func (h *Handler) writeHostKeyError(c *gin.Context, serverID string, err error) {
	var changed *HostKeyChangedError
	switch {
	case errors.Is(err, ErrHostKeyResetOperatorOnly):
		respond.Error(c, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrInvalidFingerprint):
		respond.Error(c, http.StatusBadRequest, err.Error())
	case errors.As(err, &changed):
		respond.ErrorData(c, http.StatusConflict, err.Error(), hostKeyChangedBody{
			KeyType:              changed.KeyType,
			PresentedFingerprint: changed.Presented,
		})
	case errors.Is(err, ErrSSHFailed), errors.Is(err, sshclient.ErrHostKeyMismatch), errors.Is(err, sshpool.ErrUnreachable):
		h.log.Warn("SSH host key scan failed", zap.Error(err), zap.String("server_id", serverID))
		respond.Error(c, http.StatusBadGateway, err.Error())
	default:
		h.log.Error("SSH host key request failed", zap.Error(err), zap.String("server_id", serverID))
		respond.Error(c, http.StatusInternalServerError, "failed to check the SSH host key")
	}
}

// ─── DNS provider (wildcard certificates) ─────────────────────────────────────

// GET /servers/:serverID/dns-provider — the provider and its variable names,
// never their values; 404 when there is none.
func (h *Handler) getDNSProvider(c *gin.Context) {
	dto, err := h.service.GetDNSProvider(c.Request.Context(), c.Param("serverID"))
	if errors.Is(err, ErrNoDNSProvider) {
		respond.Error(c, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		respond.Error(c, http.StatusInternalServerError, "failed to load the DNS provider")
		return
	}
	respond.JSON(c, http.StatusOK, dto)
}

// PUT /servers/:serverID/dns-provider — owners only. Replaces every
// variable; takes effect at the server's next Provision.
func (h *Handler) putDNSProvider(c *gin.Context) {
	if middleware.MemberRoleFrom(c) != "owner" {
		respond.Error(c, http.StatusForbidden, "only owners can set a server's DNS provider")
		return
	}
	var body struct {
		Provider    string            `json:"provider" binding:"required,max=40"`
		Credentials map[string]string `json:"credentials" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		respond.Error(c, http.StatusBadRequest, ErrInvalidDNSProvider.Error())
		return
	}
	dto, err := h.service.SetDNSProvider(c.Request.Context(), c.Param("serverID"), body.Provider, body.Credentials)
	if errors.Is(err, ErrInvalidDNSProvider) {
		respond.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		respond.Error(c, http.StatusInternalServerError, "failed to save the DNS provider")
		return
	}
	respond.JSON(c, http.StatusOK, dto)
}

// DELETE /servers/:serverID/dns-provider — owners only.
func (h *Handler) deleteDNSProvider(c *gin.Context) {
	if middleware.MemberRoleFrom(c) != "owner" {
		respond.Error(c, http.StatusForbidden, "only owners can remove a server's DNS provider")
		return
	}
	if err := h.service.DeleteDNSProvider(c.Request.Context(), c.Param("serverID")); err != nil {
		respond.Error(c, http.StatusInternalServerError, "failed to remove the DNS provider")
		return
	}
	respond.NoContent(c)
}

// ─── Backups (D-43) ───────────────────────────────────────────────────────────

// GET /servers/:serverID/backup — the destination and its last backup,
// never the secret key or the repository password; null when there is none.
// Every member of the workspace sees it.
func (h *Handler) getBackup(c *gin.Context) {
	dto, err := h.service.GetBackup(c.Request.Context(), c.Param("serverID"))
	h.respondBackupGet(c, dto, err)
}

// respondBackupGet answers {"data": null} for a server with no destination:
// not backing up is a state the page shows, not an error.
func (h *Handler) respondBackupGet(c *gin.Context, dto *BackupDestinationDTO, err error) {
	if errors.Is(err, ErrNoBackupDestination) {
		respond.JSON(c, http.StatusOK, json.RawMessage("null"))
		return
	}
	h.respondBackup(c, http.StatusOK, dto, err)
}

// PUT /servers/:serverID/backup — owners only. Creates or replaces the
// destination and writes it to the server; the answer that creates it is
// the only one with the repository password.
func (h *Handler) putBackup(c *gin.Context) {
	if middleware.MemberRoleFrom(c) != "owner" {
		respond.Error(c, http.StatusForbidden, "only owners can set a server's backup destination")
		return
	}
	var in BackupInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.Error(c, http.StatusBadRequest, ErrInvalidBackupDestination.Error())
		return
	}
	serverID := c.Param("serverID")
	dto, err := h.service.SetBackup(c.Request.Context(), serverID, in)
	if err == nil {
		h.orgSvc.WriteAuditLog(c.Request.Context(), middleware.OrgIDFrom(c), middleware.UserIDFrom(c), "server.backup_set", "server:"+serverID)
	}
	h.respondBackup(c, http.StatusOK, dto, err)
}

// DELETE /servers/:serverID/backup — owners only. The server stops backing
// up; the backups in the bucket stay.
func (h *Handler) deleteBackup(c *gin.Context) {
	if middleware.MemberRoleFrom(c) != "owner" {
		respond.Error(c, http.StatusForbidden, "only owners can remove a server's backup destination")
		return
	}
	serverID := c.Param("serverID")
	err := h.service.DeleteBackup(c.Request.Context(), serverID)
	if err == nil || errors.Is(err, ErrBackupRemovedNotApplied) {
		h.orgSvc.WriteAuditLog(c.Request.Context(), middleware.OrgIDFrom(c), middleware.UserIDFrom(c), "server.backup_removed", "server:"+serverID)
	}
	h.respondBackup(c, http.StatusNoContent, nil, err)
}

// GET /instance/backup — the panel host's destination; operators only.
func (h *Handler) getPanelBackup(c *gin.Context) {
	dto, err := h.service.GetPanelBackup(c.Request.Context(), middleware.UserIDFrom(c))
	h.respondBackupGet(c, dto, err)
}

// PUT /instance/backup — operators only; as PUT /servers/:serverID/backup.
func (h *Handler) putPanelBackup(c *gin.Context) {
	var in BackupInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.Error(c, http.StatusBadRequest, ErrInvalidBackupDestination.Error())
		return
	}
	dto, err := h.service.SetPanelBackup(c.Request.Context(), middleware.UserIDFrom(c), in)
	if err == nil {
		h.orgSvc.WriteAuditLog(c.Request.Context(), middleware.OrgIDFrom(c), middleware.UserIDFrom(c), "instance.backup_set", "instance:panel-host")
	}
	h.respondBackup(c, http.StatusOK, dto, err)
}

// DELETE /instance/backup — operators only.
func (h *Handler) deletePanelBackup(c *gin.Context) {
	err := h.service.DeletePanelBackup(c.Request.Context(), middleware.UserIDFrom(c))
	if err == nil || errors.Is(err, ErrBackupRemovedNotApplied) {
		h.orgSvc.WriteAuditLog(c.Request.Context(), middleware.OrgIDFrom(c), middleware.UserIDFrom(c), "instance.backup_removed", "instance:panel-host")
	}
	h.respondBackup(c, http.StatusNoContent, nil, err)
}

// respondBackup maps the backup errors. A delete that removed the row but
// could not reach the server answers 502 with what is left to do.
func (h *Handler) respondBackup(c *gin.Context, status int, dto *BackupDestinationDTO, err error) {
	switch {
	case err == nil && dto == nil:
		respond.NoContent(c)
	case err == nil:
		respond.JSON(c, status, dto)
	case errors.Is(err, ErrNoBackupDestination):
		respond.Error(c, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrInvalidBackupDestination):
		respond.Error(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrSameMachineBackup):
		respond.Error(c, http.StatusConflict, err.Error())
	case errors.Is(err, ErrPanelBackupOperatorOnly):
		respond.Error(c, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrBackupRemovedNotApplied):
		respond.Error(c, http.StatusBadGateway, err.Error())
	default:
		h.log.Error("backup destination request failed", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "the backup destination request failed")
	}
}

// ─── Shares ───────────────────────────────────────────────────────────────────

// GET /servers/:serverID/shares — the workspaces the server is shared with.
func (h *Handler) listShares(c *gin.Context) {
	shares, err := h.service.ListShares(c.Request.Context(), middleware.OrgIDFrom(c), c.Param("serverID"))
	if err != nil {
		h.writeShareError(c, err)
		return
	}
	respond.JSON(c, http.StatusOK, shares)
}

type shareInput struct {
	CPUCores float64 `json:"cpu_cores"`
	MemoryMB int64   `json:"memory_mb"`
}

// PUT /servers/:serverID/shares/:orgID — share the server with a workspace
// the caller also owns, or change the share. Owners only.
func (h *Handler) putShare(c *gin.Context) {
	if middleware.MemberRoleFrom(c) != "owner" {
		respond.Error(c, http.StatusForbidden, "only owners can share a server")
		return
	}
	var in shareInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	orgID, serverID := middleware.OrgIDFrom(c), c.Param("serverID")
	if err := h.service.ShareWith(c.Request.Context(), middleware.UserIDFrom(c), orgID, serverID, c.Param("orgID"), in.CPUCores, in.MemoryMB); err != nil {
		h.writeShareError(c, err)
		return
	}
	h.orgSvc.WriteAuditLog(c.Request.Context(), orgID, middleware.UserIDFrom(c), "server.shared", "server:"+serverID+" org:"+c.Param("orgID"))
	shares, err := h.service.ListShares(c.Request.Context(), orgID, serverID)
	if err != nil {
		h.writeShareError(c, err)
		return
	}
	respond.JSON(c, http.StatusOK, shares)
}

// DELETE /servers/:serverID/shares/:orgID — owners only; refused while that
// workspace has services on the server.
func (h *Handler) deleteShare(c *gin.Context) {
	if middleware.MemberRoleFrom(c) != "owner" {
		respond.Error(c, http.StatusForbidden, "only owners can remove a share")
		return
	}
	orgID, serverID := middleware.OrgIDFrom(c), c.Param("serverID")
	if err := h.service.Unshare(c.Request.Context(), orgID, serverID, c.Param("orgID")); err != nil {
		h.writeShareError(c, err)
		return
	}
	h.orgSvc.WriteAuditLog(c.Request.Context(), orgID, middleware.UserIDFrom(c), "server.unshared", "server:"+serverID+" org:"+c.Param("orgID"))
	respond.NoContent(c)
}

func (h *Handler) writeShareError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrServerNotFound), errors.Is(err, ErrNoShare):
		respond.Error(c, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrNotTargetOwner):
		respond.Error(c, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrSharingNeedsIsolation), errors.Is(err, ErrShareInUse), errors.Is(err, ErrShareTooLarge):
		respond.Error(c, http.StatusConflict, err.Error())
	case errors.Is(err, ErrServerNotReady):
		respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "server_not_ready"})
	case errors.Is(err, ErrInvalidShare):
		respond.Error(c, http.StatusBadRequest, err.Error())
	default:
		h.log.Error("server shares", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to update the server's shares")
	}
}

// GET /servers/:serverID/capabilities: what the probe last read (D-93);
// null data when it never ran.
func (h *Handler) getCapabilities(c *gin.Context) {
	out, err := h.service.Capabilities(c.Request.Context(), c.Param("serverID"))
	if err != nil {
		h.log.Error("read capabilities", zap.Error(err), zap.String("server_id", c.Param("serverID")))
		respond.Error(c, http.StatusInternalServerError, "failed to read what the server can run")
		return
	}
	respond.JSON(c, http.StatusOK, out)
}

// POST /servers/:serverID/capabilities: check again. Owners and developers.
func (h *Handler) probeCapabilities(c *gin.Context) {
	if middleware.MemberRoleFrom(c) == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	out, err := h.service.ProbeCapabilities(c.Request.Context(), c.Param("serverID"))
	if err != nil {
		h.log.Error("probe capabilities", zap.Error(err), zap.String("server_id", c.Param("serverID")))
		respond.Error(c, http.StatusInternalServerError, "failed to check what the server can run")
		return
	}
	respond.JSON(c, http.StatusOK, out)
}

// GET /servers/:serverID/updates: the last updates read (D-133); null
// data before the first.
func (h *Handler) getUpdates(c *gin.Context) {
	out, err := h.service.Updates(c.Request.Context(), c.Param("serverID"))
	if err != nil {
		h.log.Error("read updates", zap.Error(err), zap.String("server_id", c.Param("serverID")))
		respond.Error(c, http.StatusInternalServerError, "failed to read the server's updates")
		return
	}
	respond.JSON(c, http.StatusOK, out)
}

// POST /servers/:serverID/updates: read again now. Owners and developers.
func (h *Handler) readUpdates(c *gin.Context) {
	if middleware.MemberRoleFrom(c) == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return
	}
	out, err := h.service.ReadUpdates(c.Request.Context(), c.Param("serverID"))
	if err != nil {
		h.log.Error("read updates", zap.Error(err), zap.String("server_id", c.Param("serverID")))
		respond.Error(c, http.StatusInternalServerError, "failed to read the server's updates")
		return
	}
	respond.JSON(c, http.StatusOK, out)
}

// UpdateRunInput is POST /servers/:serverID/updates/run and the panel's.
type UpdateRunInput struct {
	Action string `json:"action" binding:"required,oneof=install docker reboot"`
}

// UpdateRunStatus is the status and code of a refused run (D-134); 0 for
// an error that isn't one.
func UpdateRunStatus(err error) (int, string) {
	switch {
	case errors.Is(err, ErrUpdateRunning):
		return http.StatusConflict, "update_running"
	case errors.Is(err, ErrDeployRunning):
		return http.StatusConflict, "deploy_running"
	case errors.Is(err, ErrNothingPending):
		return http.StatusConflict, "nothing_pending"
	case errors.Is(err, ErrDockerNotPackaged):
		return http.StatusConflict, "docker_not_packaged"
	case errors.Is(err, ErrAgentOutdated):
		return http.StatusConflict, "agent_outdated"
	case errors.Is(err, ErrHostUnreachable):
		return http.StatusBadGateway, "server_unreachable"
	}
	return 0, ""
}

// GET /servers/:serverID/updates/run: the run of updates in progress,
// followed on the server, or the last; null data before the first (D-134).
func (h *Handler) getUpdateRun(c *gin.Context) {
	run, err := h.service.UpdateRun(c.Request.Context(), c.Param("serverID"))
	if err != nil {
		h.log.Error("read update run", zap.Error(err), zap.String("server_id", c.Param("serverID")))
		respond.Error(c, http.StatusInternalServerError, "failed to read the run of updates")
		return
	}
	respond.JSON(c, http.StatusOK, run)
}

// POST /servers/:serverID/updates/run: install updates, update Docker or
// reboot (D-134). Owners and operators, sessions only, with the password.
func (h *Handler) startUpdateRun(c *gin.Context) {
	if !h.adminGate(c, true) {
		return
	}
	var in UpdateRunInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.Error(c, http.StatusBadRequest, "action must be install, docker or reboot")
		return
	}
	run, err := h.service.StartUpdates(c.Request.Context(), c.Param("serverID"), middleware.UserIDFrom(c), in.Action)
	if err != nil {
		if status, code := UpdateRunStatus(err); status != 0 {
			respond.ErrorData(c, status, err.Error(), gin.H{"code": code})
			return
		}
		h.log.Error("start update run", zap.Error(err), zap.String("server_id", c.Param("serverID")))
		respond.Error(c, http.StatusInternalServerError, "failed to start the run of updates")
		return
	}
	respond.JSON(c, http.StatusAccepted, run)
}

// GET /servers/:serverID/gvisor: the last gVisor install (D-95); null data
// before the first.
func (h *Handler) getGvisorRun(c *gin.Context) {
	run, err := h.service.GetGvisorRun(c.Request.Context(), c.Param("serverID"))
	if err != nil {
		h.log.Error("read gvisor run", zap.Error(err), zap.String("server_id", c.Param("serverID")))
		respond.Error(c, http.StatusInternalServerError, "failed to read the gVisor install")
		return
	}
	respond.JSON(c, http.StatusOK, run)
}

// POST /servers/:serverID/gvisor: install gVisor (D-95). Instance operators
// with the password, as for a host shell; server-sent events as Provision's.
func (h *Handler) installGvisor(c *gin.Context) {
	ctx := c.Request.Context()
	serverID := c.Param("serverID")
	userID := middleware.UserIDFrom(c)
	if err := h.service.AuthorizeGvisorInstall(ctx, userID); err != nil {
		if errors.Is(err, ErrGvisorOperatorOnly) {
			respond.Error(c, http.StatusForbidden, err.Error())
			return
		}
		h.log.Error("check operator", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to check who may install gVisor")
		return
	}
	if !middleware.RequireReauth(c) {
		return
	}
	srv, err := h.service.GetServer(ctx, serverID)
	if err != nil {
		respond.Error(c, http.StatusNotFound, "server not found")
		return
	}
	why := h.service.GvisorUnsupported(ctx, serverID)
	if isLocalAddress(srv.IPAddress) {
		why = "this is the panel's own server: install gVisor there from Instance → Server"
	}
	if why != "" {
		respond.ErrorData(c, http.StatusBadRequest, why, gin.H{"code": "gvisor_unsupported"})
		return
	}
	if h.service.GvisorInstalling(serverID) {
		respond.ErrorData(c, http.StatusConflict, ErrGvisorRunning.Error(), gin.H{"code": "install_running"})
		return
	}

	respond.Stream(c)
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Transfer-Encoding", "chunked")
	step := ""
	logFn := func(msg string) {
		if strings.HasPrefix(msg, "[") {
			step = msg
		}
		fmt.Fprintf(c.Writer, "data: %s\n\n", strings.ReplaceAll(msg, "\n", " "))
		c.Writer.Flush()
	}
	orgID := middleware.OrgIDFrom(c)
	platform, err := h.service.InstallGvisor(ctx, serverID, userID, logFn)
	if err != nil {
		h.log.Warn("gvisor install failed", zap.Error(err), zap.String("server_id", serverID))
		h.orgSvc.WriteAuditLogDetail(ctx, orgID, userID, "server.gvisor_install_failed", "server:"+serverID,
			map[string]any{"release": GvisorRelease, "step": step, "error": err.Error()})
		fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", strings.ReplaceAll(err.Error(), "\n", " "))
	} else {
		h.orgSvc.WriteAuditLogDetail(ctx, orgID, userID, "server.gvisor_installed", "server:"+serverID,
			map[string]any{"release": GvisorRelease, "platform": platform})
		fmt.Fprintf(c.Writer, "event: done\ndata: installed\n\n")
	}
	c.Writer.Flush()
}
