package agent

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// UserNamer names a person for the pause notes. Satisfied by cmd/server's
// userNames.
type UserNamer interface {
	UserName(ctx context.Context, userID string) (string, error)
}

// Handler is agents' HTTP transport: OAuth's endpoints and the Agents
// page's API.
type Handler struct {
	svc    *Service
	domain func() string
	users  UserNamer
	log    *zap.Logger
}

func NewHandler(svc *Service, panelDomain func() string, users UserNamer, log *zap.Logger) *Handler {
	return &Handler{svc: svc, domain: panelDomain, users: users, log: log}
}

// Issuer is the panel's origin: its domain when one is set, otherwise the
// address the request came to (a local install).
func (h *Handler) Issuer(c *gin.Context) string { return h.IssuerOf(c.Request) }

// IssuerOf is Issuer for a plain request (the MCP endpoint's).
func (h *Handler) IssuerOf(r *http.Request) string {
	if d := h.domain(); d != "" {
		return "https://" + d
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// RegisterPublicRoutes mounts the metadata documents on the panel's root
// and OAuth's public endpoints, each behind limit (per address).
func (h *Handler) RegisterPublicRoutes(r *gin.Engine, v1 *gin.RouterGroup, limit gin.HandlerFunc) {
	r.GET("/.well-known/oauth-authorization-server", limit, func(c *gin.Context) {
		respond.Bare(c, http.StatusOK, ServerMetadata(h.Issuer(c)))
	})
	resource := func(c *gin.Context) { respond.Bare(c, http.StatusOK, ResourceMetadata(h.Issuer(c))) }
	r.GET("/.well-known/oauth-protected-resource", limit, resource)
	r.GET("/.well-known/oauth-protected-resource/mcp", limit, resource)
	o := v1.Group("/oauth", limit)
	o.POST("/register", h.register)
	o.POST("/token", h.token)
	o.POST("/revoke", h.revoke)
}

// RegisterRoutes mounts the routes that need a person: consent and the
// Agents page.
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/oauth/authorize/request", h.authorizeRequest)
	rg.POST("/oauth/authorize", h.authorize)
	a := rg.Group("/agents")
	a.GET("/connections", h.listConnections)
	a.DELETE("/connections/:connectionID", h.revokeConnection)
	a.GET("/policy", h.getPolicy)
	a.PUT("/policy", h.putPolicy)
	a.PUT("/policy/projects/:projectID", h.putProjectPolicy)
	a.DELETE("/policy/projects/:projectID", h.deleteProjectPolicy)
	a.GET("/pause", h.getPause)
	a.POST("/pause", h.pauseWorkspace)
	a.DELETE("/pause", h.resumeWorkspace)
	a.GET("/approvals", h.listWaiting)
	a.GET("/approvals/:approvalID", h.getApproval)
	a.POST("/approvals/:approvalID/approve", h.approve)
	a.POST("/approvals/:approvalID/deny", h.deny)
	rg.POST("/instance/agents/pause", h.pauseInstance)
	rg.DELETE("/instance/agents/pause", h.resumeInstance)
}

// sessionOnly refuses agents and API keys: a person decides here.
func sessionOnly(c *gin.Context) bool {
	if middleware.ViaAPIKey(c) {
		respond.ErrorData(c, http.StatusForbidden, "a person does this, signed in: not an agent or an API key", gin.H{"code": "session_required"})
		return false
	}
	return true
}

func (h *Handler) fail(c *gin.Context, err error, what string) {
	var oe *OAuthError
	var inv *ErrInvalid
	switch {
	case errors.As(err, &oe):
		respond.ErrorData(c, oe.Status, oe.Desc, gin.H{"code": oe.Code})
	case errors.As(err, &inv):
		respond.ErrorData(c, http.StatusBadRequest, inv.Msg, gin.H{"code": "invalid_input"})
	case errors.Is(err, ErrNotFound):
		respond.ErrorData(c, http.StatusNotFound, what+" not found", gin.H{"code": "not_found"})
	case errors.Is(err, ErrForbidden):
		respond.ErrorData(c, http.StatusForbidden, "your role can't do this", gin.H{"code": "forbidden"})
	case errors.Is(err, ErrConflict):
		respond.ErrorData(c, http.StatusConflict, what+" was already decided or has expired", gin.H{"code": "approval_closed"})
	case errors.Is(err, ErrReauthRequired):
		respond.ErrorData(c, http.StatusForbidden, "confirm your password to loosen the agent policy", gin.H{"code": "reauth_required"})
	default:
		h.log.Error("agents: "+what, zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed: "+what)
	}
}

// oauthFail writes an OAuth error response (RFC 6749 §5.2), not the API's.
func (h *Handler) oauthFail(c *gin.Context, err error) {
	var oe *OAuthError
	if errors.As(err, &oe) {
		if oe.RetryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(oe.RetryAfter))
		}
		respond.Bare(c, oe.Status, gin.H{"error": oe.Code, "error_description": oe.Desc})
		return
	}
	h.log.Error("oauth", zap.Error(err))
	respond.Bare(c, http.StatusInternalServerError, gin.H{"error": "server_error", "error_description": "the panel failed; try again"})
}

func (h *Handler) register(c *gin.Context) {
	var r Registration
	if err := c.ShouldBindJSON(&r); err != nil {
		respond.Bare(c, http.StatusBadRequest, gin.H{"error": "invalid_client_metadata", "error_description": "the body must be JSON client metadata"})
		return
	}
	out, err := h.svc.Register(c.Request.Context(), r)
	if err != nil {
		h.oauthFail(c, err)
		return
	}
	respond.Bare(c, http.StatusCreated, out)
}

func (h *Handler) token(c *gin.Context) {
	var r TokenRequest
	if err := c.ShouldBind(&r); err != nil || !strings.HasPrefix(c.ContentType(), "application/x-www-form-urlencoded") {
		respond.Bare(c, http.StatusBadRequest, gin.H{"error": "invalid_request", "error_description": "send the form as application/x-www-form-urlencoded"})
		return
	}
	out, err := h.svc.Token(c.Request.Context(), h.Issuer(c), r)
	if err != nil {
		h.oauthFail(c, err)
		return
	}
	respond.Bare(c, http.StatusOK, out)
}

func (h *Handler) revoke(c *gin.Context) {
	if err := h.svc.RevokeToken(c.Request.Context(), c.PostForm("token")); err != nil {
		h.oauthFail(c, err)
		return
	}
	c.Status(http.StatusOK)
}

func (h *Handler) authorizeRequest(c *gin.Context) {
	if !sessionOnly(c) {
		return
	}
	var r AuthRequest
	_ = c.ShouldBindQuery(&r)
	v, _, err := h.svc.CheckRequest(c.Request.Context(), h.Issuer(c), r)
	if err != nil {
		h.fail(c, err, "authorization request")
		return
	}
	respond.JSON(c, http.StatusOK, v)
}

func (h *Handler) authorize(c *gin.Context) {
	if !sessionOnly(c) {
		return
	}
	var in Consent
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, "the body must be JSON", gin.H{"code": "invalid_input"})
		return
	}
	to, err := h.svc.Authorize(c.Request.Context(), h.Issuer(c), middleware.UserIDFrom(c), in)
	if err != nil {
		h.fail(c, err, "authorization")
		return
	}
	respond.JSON(c, http.StatusOK, gin.H{"redirect_to": to})
}

func (h *Handler) listConnections(c *gin.Context) {
	out, err := h.svc.ListConnections(c.Request.Context(), middleware.OrgIDFrom(c), middleware.UserIDFrom(c), middleware.MemberRoleFrom(c))
	if err != nil {
		h.fail(c, err, "list agents")
		return
	}
	respond.JSON(c, http.StatusOK, out)
}

func (h *Handler) revokeConnection(c *gin.Context) {
	if !sessionOnly(c) {
		return
	}
	if err := h.svc.Revoke(c.Request.Context(), middleware.OrgIDFrom(c), middleware.UserIDFrom(c), middleware.MemberRoleFrom(c), c.Param("connectionID")); err != nil {
		h.fail(c, err, "agent connection")
		return
	}
	respond.NoContent(c)
}

func (h *Handler) getPolicy(c *gin.Context) {
	p, err := h.svc.Policy(c.Request.Context(), middleware.OrgIDFrom(c))
	if err != nil {
		h.fail(c, err, "policy")
		return
	}
	respond.JSON(c, http.StatusOK, p)
}

func (h *Handler) putPolicy(c *gin.Context) {
	if !sessionOnly(c) {
		return
	}
	var body Policy
	if err := c.ShouldBindJSON(&body); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, "the body must be the policy's cells", gin.H{"code": "invalid_input"})
		return
	}
	p, err := h.svc.SetPolicy(c.Request.Context(), middleware.OrgIDFrom(c), middleware.UserIDFrom(c), middleware.MemberRoleFrom(c),
		body.Cells, middleware.Reauthenticated(c))
	if err != nil {
		h.fail(c, err, "policy")
		return
	}
	respond.JSON(c, http.StatusOK, p)
}

func (h *Handler) putProjectPolicy(c *gin.Context) {
	if !sessionOnly(c) {
		return
	}
	var body struct {
		Cells []PolicyCell `json:"cells" binding:"max=20"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, "the body must be the project's cells", gin.H{"code": "invalid_input"})
		return
	}
	p, err := h.svc.SetProjectPolicy(c.Request.Context(), middleware.OrgIDFrom(c), middleware.UserIDFrom(c), middleware.MemberRoleFrom(c),
		c.Param("projectID"), body.Cells, middleware.Reauthenticated(c))
	if err != nil {
		h.fail(c, err, "project")
		return
	}
	respond.JSON(c, http.StatusOK, p)
}

func (h *Handler) deleteProjectPolicy(c *gin.Context) {
	if !sessionOnly(c) {
		return
	}
	p, err := h.svc.ResetProjectPolicy(c.Request.Context(), middleware.OrgIDFrom(c), middleware.UserIDFrom(c), middleware.MemberRoleFrom(c),
		c.Param("projectID"), middleware.Reauthenticated(c))
	if err != nil {
		h.fail(c, err, "project")
		return
	}
	respond.JSON(c, http.StatusOK, p)
}

func (h *Handler) getPause(c *gin.Context) {
	p, err := h.svc.PauseState(c.Request.Context(), middleware.OrgIDFrom(c))
	if err != nil {
		h.fail(c, err, "pause")
		return
	}
	respond.JSON(c, http.StatusOK, p)
}

func (h *Handler) userName(c *gin.Context) string {
	if h.users != nil {
		if n, err := h.users.UserName(c.Request.Context(), middleware.UserIDFrom(c)); err == nil && n != "" {
			return n
		}
	}
	return "a person"
}

// Pausing is at once; resuming asks for the password (D-100).
func (h *Handler) pauseWorkspace(c *gin.Context)  { h.setPause(c, false, true) }
func (h *Handler) resumeWorkspace(c *gin.Context) { h.setPause(c, false, false) }
func (h *Handler) pauseInstance(c *gin.Context)   { h.setPause(c, true, true) }
func (h *Handler) resumeInstance(c *gin.Context)  { h.setPause(c, true, false) }

func (h *Handler) setPause(c *gin.Context, instance, paused bool) {
	if !sessionOnly(c) {
		return
	}
	if !paused && !middleware.RequireReauth(c) {
		return
	}
	ctx, org, user := c.Request.Context(), middleware.OrgIDFrom(c), middleware.UserIDFrom(c)
	var p *Pause
	var err error
	if instance {
		p, err = h.svc.PauseInstance(ctx, org, user, h.userName(c), paused)
	} else {
		p, err = h.svc.PauseWorkspace(ctx, org, user, h.userName(c), middleware.MemberRoleFrom(c), paused)
	}
	if err != nil {
		h.fail(c, err, "pause")
		return
	}
	respond.JSON(c, http.StatusOK, p)
}

func (h *Handler) listWaiting(c *gin.Context) {
	out, err := h.svc.Waiting(c.Request.Context(), middleware.OrgIDFrom(c))
	if err != nil {
		h.fail(c, err, "approvals")
		return
	}
	respond.JSON(c, http.StatusOK, out)
}

func (h *Handler) getApproval(c *gin.Context) {
	a, err := h.svc.Approval(c.Request.Context(), middleware.OrgIDFrom(c), c.Param("approvalID"))
	if err != nil {
		h.fail(c, err, "approval")
		return
	}
	respond.JSON(c, http.StatusOK, a)
}

func (h *Handler) approve(c *gin.Context) { h.decide(c, true) }
func (h *Handler) deny(c *gin.Context)    { h.decide(c, false) }

// decide is a person's call, signed in: never an agent's, so an agent never
// approves its own request (D-100).
func (h *Handler) decide(c *gin.Context, approve bool) {
	if !sessionOnly(c) {
		return
	}
	var body struct {
		Note   string `json:"note"`
		Commit string `json:"commit"`
	}
	_ = c.ShouldBindJSON(&body)
	respond.Long(c, respond.LongWork)
	a, err := h.svc.Decide(c.Request.Context(), middleware.OrgIDFrom(c), middleware.UserIDFrom(c), middleware.MemberRoleFrom(c),
		c.Param("approvalID"), approve, body.Note, body.Commit)
	if err != nil {
		h.fail(c, err, "approval")
		return
	}
	respond.JSON(c, http.StatusOK, a)
}
