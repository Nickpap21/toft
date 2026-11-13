package version

import (
	"errors"
	"net/http"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Handler exposes version endpoints.
type Handler struct {
	svc *Service
	log *zap.Logger
}

// NewHandler returns a version Handler.
func NewHandler(svc *Service, log *zap.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

// RegisterRoutes mounts the versions endpoint under /services/:serviceID.
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/services/:serviceID/versions", h.getVersions)
}

func (h *Handler) getVersions(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	serviceID := c.Param("serviceID")

	info, err := h.svc.GetVersionInfo(c.Request.Context(), serviceID, orgID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			respond.Error(c, http.StatusNotFound, err.Error())
			return
		}
		h.log.Error("get version info", zap.Error(err), zap.String("service_id", serviceID))
		respond.Error(c, http.StatusInternalServerError, "failed to fetch version info")
		return
	}

	respond.JSON(c, http.StatusOK, info)
}
