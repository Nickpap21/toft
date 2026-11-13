package catalog

import (
	"net/http"

	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
)

// Handler serves the addon catalog over HTTP.
type Handler struct{}

func NewHandler() *Handler { return &Handler{} }

// RegisterRoutes mounts GET /catalog and GET /catalog/:slug on the given router group.
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	r.GET("/catalog", h.list)
	r.GET("/catalog/:slug", h.get)
}

func (h *Handler) list(c *gin.Context) {
	respond.JSON(c, http.StatusOK, All())
}

func (h *Handler) get(c *gin.Context) {
	tmpl, ok := Get(c.Param("slug"))
	if !ok {
		respond.Error(c, http.StatusNotFound, "addon template not found")
		return
	}
	respond.JSON(c, http.StatusOK, tmpl)
}
