package events

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/broker"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Handler streams org-scoped events to browser clients via SSE.
type Handler struct {
	broker *broker.Broker
	log    *zap.Logger
}

func NewHandler(b *broker.Broker, log *zap.Logger) *Handler {
	return &Handler{broker: b, log: log}
}

func (h *Handler) RegisterRoutes(r gin.IRouter) {
	r.GET("/events", h.handleStream)
}

// handleStream opens a long-lived SSE connection scoped to the caller's org.
func (h *Handler) handleStream(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)

	respond.Stream(c)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no") // disable nginx/Traefik proxy buffering
	// Do NOT set Connection: keep-alive — it is illegal in HTTP/2 (RFC 7540 §8.1.2.2)
	// and causes ERR_HTTP2_PROTOCOL_ERROR when the stream goes through Traefik.

	ch, unsub := h.broker.Subscribe(orgID)
	defer unsub()

	// Send an initial ping so the client knows the stream is alive.
	fmt.Fprintf(c.Writer, "event: connected\ndata: {}\n\n")
	c.Writer.Flush()

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-c.Request.Context().Done():
			return

		case <-heartbeat.C:
			fmt.Fprintf(c.Writer, ": heartbeat\n\n")
			c.Writer.Flush()

		case e, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(e.Payload)
			if err != nil {
				h.log.Warn("failed to marshal event payload", zap.Error(err))
				continue
			}
			fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", e.Type, data)
			c.Writer.Flush()
		}
	}
}
