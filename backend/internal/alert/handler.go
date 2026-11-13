package alert

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// AuditWriter records workspace actions. Satisfied by *org.Service.
type AuditWriter interface {
	WriteAuditLog(ctx context.Context, orgID, actorID, action, target string)
	WriteAuditLogDetail(ctx context.Context, orgID, actorID, action, target string, detail map[string]any)
}

// Handler serves the workspace's alert webhook. Workspace owners only.
type Handler struct {
	svc   *Service
	audit AuditWriter
	log   *zap.Logger
}

func NewHandler(svc *Service, audit AuditWriter, log *zap.Logger) *Handler {
	return &Handler{svc: svc, audit: audit, log: log}
}

func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	r.GET("/alerts", h.handleRecent)
	g := r.Group("/alerts/channel")
	{
		g.GET("", h.handleGet)
		g.PUT("", h.handlePut)
		g.DELETE("", h.handleDelete)
		g.POST("/rotate-secret", h.handleRotate)
		g.POST("/test", h.handleTest)
	}
}

// ownerOrg returns the caller's workspace if they own it; otherwise it
// writes the 403.
func ownerOrg(c *gin.Context) (string, bool) {
	orgID := middleware.OrgIDFrom(c)
	if orgID == "" || middleware.MemberRoleFrom(c) != "owner" {
		respond.Error(c, http.StatusForbidden, "only workspace owners manage alerts")
		return "", false
	}
	return orgID, true
}

func (h *Handler) writeError(c *gin.Context, err error, what string) {
	switch {
	case errors.Is(err, ErrNoChannel):
		respond.Error(c, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrInvalidURL), errors.Is(err, ErrPrivateAddress):
		respond.Error(c, http.StatusBadRequest, err.Error())
	default:
		h.log.Error("alerts: "+what, zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to "+what)
	}
}

func (h *Handler) handleGet(c *gin.Context) {
	orgID, ok := ownerOrg(c)
	if !ok {
		return
	}
	ch, err := h.svc.GetChannel(c.Request.Context(), orgID)
	if errors.Is(err, ErrNoChannel) {
		// No webhook is a state, not an error: alerts are off. {"data":
		// null}: a nil data would be left out of the envelope.
		respond.JSON(c, http.StatusOK, json.RawMessage("null"))
		return
	}
	if err != nil {
		h.writeError(c, err, "load the alert webhook")
		return
	}
	respond.JSON(c, http.StatusOK, ch)
}

// GET /alerts — the workspace's alerts open now or fired in the last 30
// days. Every member: Home shows the open ones to everyone too.
func (h *Handler) handleRecent(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	if orgID == "" {
		respond.Error(c, http.StatusForbidden, "no org context")
		return
	}
	list, err := h.svc.ListRecent(c.Request.Context(), orgID)
	if err != nil {
		h.writeError(c, err, "load the recent alerts")
		return
	}
	respond.JSON(c, http.StatusOK, list)
}

func (h *Handler) handlePut(c *gin.Context) {
	orgID, ok := ownerOrg(c)
	if !ok {
		return
	}
	var body struct {
		WebhookURL string `json:"webhook_url" binding:"required,max=2048"`
		Enabled    *bool  `json:"enabled"` // default true
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		respond.Error(c, http.StatusBadRequest, "webhook_url is required")
		return
	}
	enabled := body.Enabled == nil || *body.Enabled
	ch, err := h.svc.PutChannel(c.Request.Context(), orgID, body.WebhookURL, enabled)
	if err != nil {
		h.writeError(c, err, "save the alert webhook")
		return
	}
	detail := map[string]any{"enabled": ch.Enabled}
	if u, err := url.Parse(ch.WebhookURL); err == nil {
		detail["host"] = u.Host
	}
	h.audit.WriteAuditLogDetail(c.Request.Context(), orgID, middleware.UserIDFrom(c), "alerts.channel_updated", "org:"+orgID, detail)
	respond.JSON(c, http.StatusOK, ch)
}

func (h *Handler) handleRotate(c *gin.Context) {
	orgID, ok := ownerOrg(c)
	if !ok {
		return
	}
	ch, err := h.svc.RotateSecret(c.Request.Context(), orgID)
	if err != nil {
		h.writeError(c, err, "rotate the signing secret")
		return
	}
	h.audit.WriteAuditLog(c.Request.Context(), orgID, middleware.UserIDFrom(c), "alerts.secret_rotated", "org:"+orgID)
	respond.JSON(c, http.StatusOK, ch)
}

func (h *Handler) handleDelete(c *gin.Context) {
	orgID, ok := ownerOrg(c)
	if !ok {
		return
	}
	if err := h.svc.DeleteChannel(c.Request.Context(), orgID); err != nil {
		h.writeError(c, err, "delete the alert webhook")
		return
	}
	h.audit.WriteAuditLog(c.Request.Context(), orgID, middleware.UserIDFrom(c), "alerts.channel_deleted", "org:"+orgID)
	respond.NoContent(c)
}

// handleTest sends a test message now; 502 says why the receiver didn't
// take it.
func (h *Handler) handleTest(c *gin.Context) {
	orgID, ok := ownerOrg(c)
	if !ok {
		return
	}
	err := h.svc.SendTest(c.Request.Context(), orgID)
	switch {
	case err == nil:
		respond.NoContent(c)
	case errors.Is(err, ErrNoChannel):
		respond.Error(c, http.StatusNotFound, err.Error())
	default:
		respond.Error(c, http.StatusBadGateway, "the test message was not delivered: "+err.Error())
	}
}
