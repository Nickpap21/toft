package disk

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
)

// Handler serves the disk advisor's endpoints (D-135).
type Handler struct {
	svc *Service
	log *zap.Logger
}

// NewHandler returns the advisor's HTTP handler.
func NewHandler(svc *Service, log *zap.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

// RegisterRoutes adds the endpoints for servers and the panel's own server.
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	for _, base := range []string{"/servers/:serverID/disk", "/instance/panel/disk"} {
		rg.GET(base, h.report)
		rg.GET(base+"/clean", h.cleanState)
		rg.POST(base+"/clean", h.startClean)
		rg.POST(base+"/volumes/remove", h.removeVolume)
		rg.GET(base+"/policy", h.policy)
		rg.PUT(base+"/policy", h.setPolicy)
	}
}

// host authorizes the caller for the route's host. write takes a session;
// password takes the re-auth proof as well.
func (h *Handler) host(c *gin.Context, write, password bool) (HostRow, bool) {
	if write && middleware.ViaAPIKey(c) {
		respond.ErrorData(c, http.StatusForbidden, "this needs a signed-in session, not an API key", gin.H{"code": "session_required"})
		return HostRow{}, false
	}
	row, err := h.svc.Authorize(c.Request.Context(), c.Param("serverID"), middleware.OrgIDFrom(c), middleware.UserIDFrom(c), middleware.MemberRoleFrom(c), write)
	if err != nil {
		h.fail(c, err, "authorize")
		return HostRow{}, false
	}
	if password && !middleware.RequireReauth(c) {
		return HostRow{}, false
	}
	return row, true
}

// fail answers err as the contract says.
func (h *Handler) fail(c *gin.Context, err error, what string) {
	type mapped struct {
		target error
		status int
		code   string
	}
	for _, m := range []mapped{
		{ErrNotFound, http.StatusNotFound, ""},
		{ErrForbidden, http.StatusForbidden, ""},
		{ErrCleanRunning, http.StatusConflict, "disk_clean_running"},
		{ErrDeployRunning, http.StatusConflict, "deploy_running"},
		{ErrVolumeInUse, http.StatusConflict, "volume_in_use"},
		{ErrVolumeNotRemovable, http.StatusConflict, "volume_not_removable"},
		{ErrInvalidPolicy, http.StatusBadRequest, "invalid_policy"},
		{ErrNothingToClean, http.StatusBadRequest, ""},
		{ErrKindNotHere, http.StatusBadRequest, ""},
		{ErrUnreachable, http.StatusBadGateway, "server_unreachable"},
	} {
		if !errors.Is(err, m.target) {
			continue
		}
		msg := err.Error()
		if m.target == ErrUnreachable {
			msg = ErrUnreachable.Error() // never the transport's own message
			h.log.Warn("disk: "+what, zap.Error(err), zap.String("server_id", c.Param("serverID")))
		}
		if m.code == "" {
			respond.Error(c, m.status, msg)
		} else {
			respond.ErrorData(c, m.status, msg, gin.H{"code": m.code})
		}
		return
	}
	h.log.Error("disk: "+what, zap.Error(err), zap.String("server_id", c.Param("serverID")))
	respond.Error(c, http.StatusInternalServerError, "failed to "+what)
}

func (h *Handler) report(c *gin.Context) {
	row, ok := h.host(c, false, false)
	if !ok {
		return
	}
	r, err := h.svc.Report(c.Request.Context(), row, c.Query("fresh") == "true")
	if err != nil {
		h.fail(c, err, "read the disk")
		return
	}
	respond.JSON(c, http.StatusOK, r)
}

func (h *Handler) cleanState(c *gin.Context) {
	row, ok := h.host(c, false, false)
	if !ok {
		return
	}
	respond.JSON(c, http.StatusOK, h.svc.CleanState(row))
}

// CleanInput is POST …/disk/clean's body.
type CleanInput struct {
	Kinds         []Kind `json:"kinds"`
	BuildCacheAll bool   `json:"build_cache_all"`
}

func (h *Handler) startClean(c *gin.Context) {
	row, ok := h.host(c, true, true)
	if !ok {
		return
	}
	var in CleanInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.Error(c, http.StatusBadRequest, "kinds is a list of untagged, old_builds, old_panel_releases and unused_pulled")
		return
	}
	cl, err := h.svc.StartClean(c.Request.Context(), row, middleware.UserIDFrom(c), in.Kinds, in.BuildCacheAll)
	if err != nil {
		h.fail(c, err, "start the clean")
		return
	}
	respond.JSON(c, http.StatusAccepted, cl)
}

// VolumeInput is POST …/disk/volumes/remove's body.
type VolumeInput struct {
	Name string `json:"name" binding:"required"`
}

func (h *Handler) removeVolume(c *gin.Context) {
	row, ok := h.host(c, true, true)
	if !ok {
		return
	}
	var in VolumeInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.Error(c, http.StatusBadRequest, "name is required")
		return
	}
	if err := h.svc.RemoveVolume(c.Request.Context(), row, middleware.UserIDFrom(c), in.Name); err != nil {
		h.fail(c, err, "delete the volume")
		return
	}
	respond.NoContent(c)
}

func (h *Handler) policy(c *gin.Context) {
	row, ok := h.host(c, false, false)
	if !ok {
		return
	}
	respond.JSON(c, http.StatusOK, h.svc.Policy(c.Request.Context(), row))
}

// PolicyInput is PUT …/disk/policy's body: every field, null the default.
type PolicyInput struct {
	BuildCacheLimitBytes *int64  `json:"build_cache_limit_bytes"`
	AutoClean            *[]Kind `json:"auto_clean"`
	AlertPercent         *int    `json:"alert_percent"`
}

func (h *Handler) setPolicy(c *gin.Context) {
	row, ok := h.host(c, true, false)
	if !ok {
		return
	}
	var in PolicyInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, "build_cache_limit_bytes, auto_clean and alert_percent are each a value or null", gin.H{"code": "invalid_policy"})
		return
	}
	p := Policy{BuildCacheLimitBytes: in.BuildCacheLimitBytes, AlertPercent: in.AlertPercent}
	if in.AutoClean != nil {
		p.AutoClean = append([]Kind{}, *in.AutoClean...)
	}
	v, err := h.svc.SetPolicy(c.Request.Context(), row, middleware.UserIDFrom(c), p)
	if err != nil {
		h.fail(c, err, "save the disk policy")
		return
	}
	respond.JSON(c, http.StatusOK, v)
}
