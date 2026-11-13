package inbox

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Handler is the inbox's JSON API (D-88): a person's own, so
// project-scoped API keys never reach it (apikey's routes).
type Handler struct {
	svc *Service
	log *zap.Logger
}

func NewHandler(svc *Service, log *zap.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/inbox")
	g.GET("/needs-you", h.needsYou)
	g.GET("/updates", h.updates)
	g.GET("/counts", h.counts)
	g.POST("/done", h.mark(true))
	g.POST("/undone", h.mark(false))
	g.POST("/read", h.read)
}

func (h *Handler) fail(c *gin.Context, op string, err error) {
	switch {
	case errors.Is(err, ErrBadCursor):
		respond.ErrorData(c, http.StatusBadRequest, err.Error(), gin.H{"code": "bad_cursor"})
	case errors.Is(err, ErrBadInput):
		respond.ErrorData(c, http.StatusBadRequest, err.Error(), gin.H{"code": "bad_input"})
	default:
		h.log.Error(op, zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to "+op)
	}
}

func (h *Handler) needsYou(c *gin.Context) {
	which := Marks(c.DefaultQuery("done", string(MarksOpen)))
	if which != MarksOpen && which != MarksDone && which != MarksAny {
		respond.ErrorData(c, http.StatusBadRequest, "done must be true, false or any", gin.H{"code": "bad_input"})
		return
	}
	out, err := h.svc.NeedsYou(c.Request.Context(), middleware.OrgIDFrom(c), middleware.UserIDFrom(c), middleware.MemberRoleFrom(c), which)
	if err != nil {
		h.fail(c, "read the inbox", err)
		return
	}
	respond.JSON(c, http.StatusOK, out)
}

func (h *Handler) updates(c *gin.Context) {
	q := UpdateQuery{UserID: middleware.UserIDFrom(c), OrgID: middleware.OrgIDFrom(c), Unread: c.Query("unread") == "true", Done: c.Query("done") == "true"}
	if k := strings.TrimSpace(c.Query("kind")); k != "" {
		q.Kinds = strings.Split(k, ",")
	}
	q.Limit, _ = strconv.Atoi(c.DefaultQuery("limit", "50"))
	out, next, err := h.svc.Updates(c.Request.Context(), q, c.Query("cursor"))
	if err != nil {
		h.fail(c, "read the updates", err)
		return
	}
	respond.JSONPage(c, http.StatusOK, out, next)
}

func (h *Handler) counts(c *gin.Context) {
	out, err := h.svc.Counts(c.Request.Context(), middleware.OrgIDFrom(c), middleware.UserIDFrom(c), middleware.MemberRoleFrom(c))
	if err != nil {
		h.fail(c, "count the inbox", err)
		return
	}
	respond.JSON(c, http.StatusOK, out)
}

func (h *Handler) mark(done bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		var in MarkInput
		if err := c.ShouldBindJSON(&in); err != nil {
			respond.ErrorData(c, http.StatusBadRequest, "invalid request body", gin.H{"code": "bad_input"})
			return
		}
		if err := h.svc.Mark(c.Request.Context(), middleware.OrgIDFrom(c), middleware.UserIDFrom(c), in, done); err != nil {
			h.fail(c, "mark the inbox", err)
			return
		}
		respond.NoContent(c)
	}
}

func (h *Handler) read(c *gin.Context) {
	var in ReadInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, "invalid request body", gin.H{"code": "bad_input"})
		return
	}
	if err := h.svc.Read(c.Request.Context(), middleware.OrgIDFrom(c), middleware.UserIDFrom(c), in); err != nil {
		h.fail(c, "mark the updates read", err)
		return
	}
	respond.NoContent(c)
}
