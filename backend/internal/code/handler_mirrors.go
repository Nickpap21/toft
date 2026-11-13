package code

import (
	"net/http"
	"strconv"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
)

// Import, mirrors and outbound webhooks over HTTP (D-79). Owners and
// developers import and sync; owners add and remove mirrors (a push mirror
// with the password) and manage webhooks. Project-scoped API keys reach
// none of it (apikey's routeScopes lists none).

func (h *Handler) registerMirrors(repo *gin.RouterGroup) {
	repo.POST("/import", h.importRepository)
	repo.GET("/mirrors", h.listMirrors)
	repo.POST("/mirrors", h.addPushMirror)
	repo.DELETE("/mirrors/:mirrorID", h.deleteMirror)
	repo.POST("/mirrors/:mirrorID/sync", h.syncMirror)
	repo.GET("/webhooks", h.listWebhooks)
	repo.POST("/webhooks", h.createWebhook)
	hook := repo.Group("/webhooks/:webhookID")
	hook.GET("", h.getWebhook)
	hook.PATCH("", h.updateWebhook)
	hook.DELETE("", h.deleteWebhook)
	hook.POST("/secret", h.rotateWebhookSecret)
	hook.POST("/test", h.pingWebhook)
	hook.GET("/deliveries", h.listDeliveries)
	hook.GET("/deliveries/:deliveryID", h.getDelivery)
	hook.POST("/deliveries/:deliveryID/redeliver", h.redeliver)
}

func (h *Handler) importRepository(c *gin.Context) {
	if !h.inProject(c) || !canWrite(c) {
		return
	}
	var in ImportInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, ErrBadRemote.Error(), gin.H{"code": "bad_url"})
		return
	}
	m, err := h.svc.Import(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), middleware.UserIDFrom(c), in)
	if err != nil {
		h.fail(c, "import", err)
		return
	}
	h.auditRepo(c, "repository.import_started", c.Param("repositoryID"), map[string]any{"url": m.URL, "keep": m.Keep})
	respond.JSON(c, http.StatusAccepted, m)
}

func (h *Handler) listMirrors(c *gin.Context) {
	if !h.inProject(c) {
		return
	}
	ms, err := h.svc.Mirrors(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"))
	if err != nil {
		h.fail(c, "mirrors", err)
		return
	}
	respond.JSON(c, http.StatusOK, ms)
}

func (h *Handler) addPushMirror(c *gin.Context) {
	if !h.inProject(c) || !isOwner(c) || !middleware.RequireReauth(c) {
		return
	}
	var in MirrorInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, ErrBadRemote.Error(), gin.H{"code": "bad_url"})
		return
	}
	m, err := h.svc.AddPushMirror(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), middleware.UserIDFrom(c), in)
	if err != nil {
		h.fail(c, "mirror", err)
		return
	}
	h.auditRepo(c, "repository.mirror_added", c.Param("repositoryID"), map[string]any{"url": m.URL, "direction": "push"})
	respond.JSON(c, http.StatusCreated, m)
}

func (h *Handler) deleteMirror(c *gin.Context) {
	if !h.inProject(c) || !canWrite(c) {
		return
	}
	m, err := h.svc.DeleteMirror(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), c.Param("mirrorID"),
		middleware.MemberRoleFrom(c) == "owner")
	if err != nil {
		h.fail(c, "mirror", err)
		return
	}
	action := "repository.mirror_removed"
	if m.Direction == "pull" && m.LastOKAt == nil {
		action = "repository.import_cancelled"
	}
	h.auditRepo(c, action, c.Param("repositoryID"), map[string]any{"url": m.URL, "direction": m.Direction})
	respond.NoContent(c)
}

func (h *Handler) syncMirror(c *gin.Context) {
	if !h.inProject(c) || !canWrite(c) {
		return
	}
	m, err := h.svc.SyncMirror(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), c.Param("mirrorID"))
	if err != nil {
		h.fail(c, "mirror", err)
		return
	}
	respond.JSON(c, http.StatusAccepted, m)
}

func (h *Handler) listWebhooks(c *gin.Context) {
	if !h.inProject(c) || !isOwner(c) {
		return
	}
	ws, err := h.svc.Webhooks(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"))
	if err != nil {
		h.fail(c, "webhooks", err)
		return
	}
	respond.JSON(c, http.StatusOK, ws)
}

func (h *Handler) getWebhook(c *gin.Context) {
	if !h.inProject(c) || !isOwner(c) {
		return
	}
	w, err := h.svc.Webhook(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), c.Param("webhookID"))
	if err != nil {
		h.fail(c, "webhook", err)
		return
	}
	respond.JSON(c, http.StatusOK, w)
}

func (h *Handler) createWebhook(c *gin.Context) {
	if !h.inProject(c) || !isOwner(c) {
		return
	}
	var in WebhookInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, ErrBadHookURL.Error(), gin.H{"code": "bad_url"})
		return
	}
	w, err := h.svc.CreateWebhook(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), middleware.UserIDFrom(c), in)
	if err != nil {
		h.fail(c, "webhook", err)
		return
	}
	h.auditRepo(c, "repository.webhook_created", c.Param("repositoryID"), map[string]any{"url": w.URL, "events": w.Events})
	respond.JSON(c, http.StatusCreated, w)
}

func (h *Handler) updateWebhook(c *gin.Context) {
	if !h.inProject(c) || !isOwner(c) {
		return
	}
	var in WebhookUpdate
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, "url (https), events (push, pull_request) or active (true or false)", gin.H{"code": "bad_request"})
		return
	}
	w, changed, err := h.svc.UpdateWebhook(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), c.Param("webhookID"), in)
	if err != nil {
		h.fail(c, "webhook", err)
		return
	}
	if len(changed) > 0 {
		changed["webhook"] = w.URL
		h.auditRepo(c, "repository.webhook_updated", c.Param("repositoryID"), changed)
	}
	respond.JSON(c, http.StatusOK, w)
}

func (h *Handler) deleteWebhook(c *gin.Context) {
	if !h.inProject(c) || !isOwner(c) {
		return
	}
	w, err := h.svc.DeleteWebhook(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), c.Param("webhookID"))
	if err != nil {
		h.fail(c, "webhook", err)
		return
	}
	h.auditRepo(c, "repository.webhook_deleted", c.Param("repositoryID"), map[string]any{"url": w.URL})
	respond.NoContent(c)
}

func (h *Handler) rotateWebhookSecret(c *gin.Context) {
	if !h.inProject(c) || !isOwner(c) {
		return
	}
	secret, err := h.svc.RotateWebhookSecret(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), c.Param("webhookID"), middleware.UserIDFrom(c))
	if err != nil {
		h.fail(c, "webhook", err)
		return
	}
	h.auditRepo(c, "repository.webhook_secret_rotated", c.Param("repositoryID"), map[string]any{"webhook_id": c.Param("webhookID")})
	respond.JSON(c, http.StatusOK, gin.H{"secret": secret})
}

func (h *Handler) pingWebhook(c *gin.Context) {
	if !h.inProject(c) || !isOwner(c) {
		return
	}
	d, err := h.svc.PingWebhook(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), c.Param("webhookID"), middleware.UserIDFrom(c))
	if err != nil {
		h.fail(c, "webhook", err)
		return
	}
	respond.JSON(c, http.StatusAccepted, d)
}

func (h *Handler) listDeliveries(c *gin.Context) {
	if !h.inProject(c) || !isOwner(c) {
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit < 1 {
		respond.ErrorData(c, http.StatusBadRequest, "limit must be a whole number", gin.H{"code": "bad_limit"})
		return
	}
	page, next, err := h.svc.Deliveries(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), c.Param("webhookID"), min(limit, 200), c.Query("cursor"))
	if err != nil {
		h.fail(c, "deliveries", err)
		return
	}
	respond.JSONPage(c, http.StatusOK, page, next)
}

func (h *Handler) getDelivery(c *gin.Context) {
	if !h.inProject(c) || !isOwner(c) {
		return
	}
	d, err := h.svc.Delivery(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), c.Param("webhookID"), c.Param("deliveryID"))
	if err != nil {
		h.fail(c, "delivery", err)
		return
	}
	respond.JSON(c, http.StatusOK, d)
}

func (h *Handler) redeliver(c *gin.Context) {
	if !h.inProject(c) || !isOwner(c) {
		return
	}
	d, err := h.svc.Redeliver(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), c.Param("webhookID"), c.Param("deliveryID"), middleware.UserIDFrom(c))
	if err != nil {
		h.fail(c, "delivery", err)
		return
	}
	respond.JSON(c, http.StatusAccepted, d)
}
