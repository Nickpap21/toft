package preview

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
)

// ProjectChecker verifies that a project belongs to a workspace. Satisfied
// by *project.Service.
type ProjectChecker interface {
	CheckOwnership(ctx context.Context, id, orgID string) (bool, error)
}

// Handler serves previews over HTTP.
type Handler struct {
	svc      *Service
	projects ProjectChecker
	envs     EnvironmentChecker
	log      *zap.Logger
}

// NewHandler returns the previews' handler.
func NewHandler(svc *Service, projects ProjectChecker, envs EnvironmentChecker, log *zap.Logger) *Handler {
	return &Handler{svc: svc, projects: projects, envs: envs, log: log}
}

// RegisterRoutes mounts the previews' endpoints on the protected group.
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	r.GET("/projects/:projectID/previews", h.list)
	r.GET("/projects/:projectID/previews/settings", h.getSettings)
	r.PUT("/projects/:projectID/previews/settings", h.putSettings)
	r.GET("/projects/:projectID/previews/domain-check", h.domainCheck)
	pull := r.Group("/projects/:projectID/repositories/:repositoryID/pulls/:pullNumber/preview")
	pull.GET("", h.get)
	pull.POST("", h.start)
	pull.POST("/approve", h.approve)
	pull.POST("/stop", h.stop)
	pull.POST("/redeploy", h.redeploy)
}

func (h *Handler) inProject(c *gin.Context) bool {
	owned, err := h.projects.CheckOwnership(c.Request.Context(), c.Param("projectID"), middleware.OrgIDFrom(c))
	if err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "project not found")
		return false
	}
	return true
}

// canWrite: owners and developers start, stop, redeploy and approve.
func canWrite(c *gin.Context) bool {
	if r := middleware.MemberRoleFrom(c); r == "owner" || r == "developer" {
		return true
	}
	respond.ErrorData(c, http.StatusForbidden, "owners and developers act on previews; viewers read them", gin.H{"code": "forbidden"})
	return false
}

func actor(c *gin.Context) Actor { return Actor{UserID: middleware.UserIDFrom(c)} }

// fail answers err: the previews' errors a client can act on by status and
// code, anything else as a 500.
func (h *Handler) fail(c *gin.Context, what string, err error) {
	if respond.Gate(c, err) {
		return
	}
	for _, e := range []struct {
		err    error
		status int
		code   string
	}{
		{ErrNotFound, http.StatusNotFound, "no_preview"},
		{ErrNoPull, http.StatusNotFound, "not_found"},
		{ErrOff, http.StatusConflict, "previews_off"},
		{ErrPullNotOpen, http.StatusConflict, "pull_not_open"},
		{ErrNotUntrusted, http.StatusConflict, "not_waiting"},
		{ErrStaleSHA, http.StatusConflict, "stale_sha"},
		{ErrNotStarted, http.StatusConflict, "preview_not_started"},
		{ErrBadEnvironment, http.StatusBadRequest, "bad_environment"},
		{ErrBadDomain, http.StatusBadRequest, "bad_domain"},
		{ErrBadMax, http.StatusBadRequest, "bad_max"},
		{ErrLegacyNetworks, http.StatusConflict, "legacy_networks"},
		{ErrBadCursor, http.StatusBadRequest, "bad_cursor"},
	} {
		if errors.Is(err, e.err) {
			respond.ErrorData(c, e.status, e.err.Error(), gin.H{"code": e.code})
			return
		}
	}
	h.log.Error("previews: "+what, zap.Error(err))
	respond.Error(c, http.StatusInternalServerError, "failed to "+what)
}

func (h *Handler) list(c *gin.Context) {
	if !h.inProject(c) {
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit < 1 || limit > 200 {
		respond.ErrorData(c, http.StatusBadRequest, "limit is 1 to 200", gin.H{"code": "bad_limit"})
		return
	}
	views, next, err := h.svc.List(c.Request.Context(), c.Param("projectID"), c.Query("cursor"), limit)
	if err != nil {
		h.fail(c, "list previews", err)
		return
	}
	respond.JSONPage(c, http.StatusOK, views, next)
}

// SettingsView is a project's previews settings as the API shows them.
type SettingsView struct {
	EnvironmentID   *string `json:"environment_id"`
	EnvironmentName string  `json:"environment_name,omitempty"`
	Domain          *string `json:"domain"`
	MaxPerProject   int     `json:"max_per_project"`
}

func settingsView(s Settings) SettingsView {
	v := SettingsView{EnvironmentName: s.EnvironmentName, MaxPerProject: s.Max}
	if s.EnvironmentID != "" {
		v.EnvironmentID = &s.EnvironmentID
	}
	if s.Domain != "" {
		v.Domain = &s.Domain
	}
	return v
}

func (h *Handler) getSettings(c *gin.Context) {
	if !h.inProject(c) {
		return
	}
	s, err := h.svc.GetSettings(c.Request.Context(), c.Param("projectID"))
	if err != nil {
		h.fail(c, "read the previews settings", err)
		return
	}
	respond.JSON(c, http.StatusOK, settingsView(s))
}

// SettingsInput is PUT …/previews/settings: the environment previews copy
// (null turns them off), their domain (null for sslip.io names) and how
// many run at once (omitted keeps the project's).
type SettingsInput struct {
	EnvironmentID *string `json:"environment_id"`
	Domain        *string `json:"domain" binding:"omitempty,max=200"`
	MaxPerProject *int    `json:"max_per_project"`
}

// putSettings: owners, with a session (an agent never turns its own
// previews on or points them elsewhere).
func (h *Handler) putSettings(c *gin.Context) {
	respond.Long(c, respond.LongWork) // turning previews off removes them
	if !h.inProject(c) {
		return
	}
	if middleware.MemberRoleFrom(c) != "owner" {
		respond.ErrorData(c, http.StatusForbidden, "only owners change a project's previews", gin.H{"code": "forbidden"})
		return
	}
	if middleware.ViaAPIKey(c) {
		respond.ErrorData(c, http.StatusForbidden, "changing previews needs a signed-in session, not an API key", gin.H{"code": "session_required"})
		return
	}
	var in SettingsInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, "environment_id and domain are a string or null, max_per_project a number", gin.H{"code": "bad_request"})
		return
	}
	env, domain := "", ""
	if in.EnvironmentID != nil {
		env = *in.EnvironmentID
	}
	if in.Domain != nil {
		domain = *in.Domain
	}
	s, err := h.svc.SetSettings(c.Request.Context(), h.envs, c.Param("projectID"), env, domain, in.MaxPerProject, actor(c))
	if err != nil {
		h.fail(c, "save the previews settings", err)
		return
	}
	respond.JSON(c, http.StatusOK, settingsView(s))
}

// DomainCheck is whether the preview domain's wildcard record resolves to
// the server of the environment previews copy.
type DomainCheck struct {
	Domain    string    `json:"domain"`
	Record    string    `json:"record"`
	Want      string    `json:"want"`
	Got       []string  `json:"got"`
	OK        bool      `json:"ok"`
	CheckedAt time.Time `json:"checked_at"`
}

func (h *Handler) domainCheck(c *gin.Context) {
	if !h.inProject(c) {
		return
	}
	ctx := c.Request.Context()
	s, err := h.svc.GetSettings(ctx, c.Param("projectID"))
	if err != nil {
		h.fail(c, "check the preview domain", err)
		return
	}
	if s.Domain == "" || s.EnvironmentID == "" {
		respond.ErrorData(c, http.StatusConflict, "set previews on and a domain first", gin.H{"code": "no_domain"})
		return
	}
	server := ""
	if sources, err := h.svc.store.SourceServices(ctx, s.EnvironmentID); err == nil && len(sources) > 0 {
		server = sources[0].ServerID
	}
	want, _ := h.svc.hosts.Address(ctx, server)
	host := "toft-check-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "." + s.Domain
	got := []string{}
	if h.svc.lookup != nil {
		lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		if addrs, err := h.svc.lookup(lctx, host); err == nil {
			got = addrs
		}
		cancel()
	}
	ok := false
	for _, a := range got {
		ok = ok || a == want
	}
	respond.JSON(c, http.StatusOK, DomainCheck{Domain: s.Domain, Record: "*." + s.Domain + "  A  " + want, Want: want,
		Got: got, OK: ok && want != "", CheckedAt: time.Now().UTC()})
}

func (h *Handler) pull(c *gin.Context) (int, bool) {
	if !h.inProject(c) {
		return 0, false
	}
	n, err := strconv.Atoi(c.Param("pullNumber"))
	if err != nil || n < 1 {
		respond.ErrorData(c, http.StatusNotFound, "no such pull request", gin.H{"code": "not_found"})
		return 0, false
	}
	return n, true
}

func (h *Handler) get(c *gin.Context) {
	n, ok := h.pull(c)
	if !ok {
		return
	}
	v, err := h.svc.Of(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), n)
	if err != nil {
		h.fail(c, "read the preview", err)
		return
	}
	respond.JSON(c, http.StatusOK, v)
}

func (h *Handler) start(c *gin.Context) {
	n, ok := h.pull(c)
	if !ok || !canWrite(c) {
		return
	}
	v, err := h.svc.Start(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), n, actor(c))
	if err != nil {
		h.fail(c, "start the preview", err)
		return
	}
	respond.JSON(c, http.StatusAccepted, v)
}

// ApproveInput is POST …/preview/approve: the commit approved, which must
// be the pull request's head.
type ApproveInput struct {
	SHA string `json:"sha" binding:"required,max=64"`
}

// approve: owners and developers with a session. An API key never
// approves, an owner's included: an agent can't approve its own preview.
func (h *Handler) approve(c *gin.Context) {
	n, ok := h.pull(c)
	if !ok || !canWrite(c) {
		return
	}
	if middleware.ViaAPIKey(c) {
		respond.ErrorData(c, http.StatusForbidden, "approving a preview needs a signed-in session, not an API key", gin.H{"code": "session_required"})
		return
	}
	var in ApproveInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, "sha is required", gin.H{"code": "bad_request"})
		return
	}
	v, err := h.svc.Approve(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), n, strings.ToLower(strings.TrimSpace(in.SHA)), actor(c))
	if err != nil {
		h.fail(c, "approve the preview", err)
		return
	}
	respond.JSON(c, http.StatusAccepted, v)
}

func (h *Handler) stop(c *gin.Context) {
	respond.Long(c, respond.LongWork) // stops containers
	n, ok := h.pull(c)
	if !ok || !canWrite(c) {
		return
	}
	v, err := h.svc.Stop(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), n, actor(c))
	if err != nil {
		h.fail(c, "stop the preview", err)
		return
	}
	respond.JSON(c, http.StatusOK, v)
}

func (h *Handler) redeploy(c *gin.Context) {
	n, ok := h.pull(c)
	if !ok || !canWrite(c) {
		return
	}
	v, err := h.svc.Redeploy(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), n, actor(c))
	if err != nil {
		h.fail(c, "redeploy the preview", err)
		return
	}
	respond.JSON(c, http.StatusAccepted, v)
}
