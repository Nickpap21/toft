package doc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Handler is the docs module's JSON API (D-86): reads for every member and
// docs_read keys, writes for owners, developers and docs_write keys, runs
// for people only.
type Handler struct {
	svc      *Service
	projects ProjectOwnershipChecker
	audit    AuditWriter
	log      *zap.Logger
}

// ProjectOwnershipChecker says whether a project is in a workspace.
// Satisfied by *project.Service.
type ProjectOwnershipChecker interface {
	CheckOwnership(ctx context.Context, projectID, orgID string) (bool, error)
}

// AuditWriter records deletes and runs. Satisfied by *org.Service.
type AuditWriter interface {
	WriteAuditLogDetail(ctx context.Context, orgID, actorID, action, resource string, detail map[string]any)
}

func NewHandler(svc *Service, projects ProjectOwnershipChecker, audit AuditWriter, log *zap.Logger) *Handler {
	return &Handler{svc: svc, projects: projects, audit: audit, log: log}
}

func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	p := rg.Group("/projects/:projectID/docs")
	p.GET("", h.tree)
	p.POST("", h.create)
	p.GET("/:pageID", h.get)
	p.PATCH("/:pageID", h.update)
	p.DELETE("/:pageID", h.remove)
	p.POST("/:pageID/restore", h.restore)
	p.POST("/:pageID/verify", h.verify)
	p.GET("/:pageID/versions", h.versions)
	p.GET("/:pageID/versions/:version", h.version)
	p.POST("/:pageID/steps/:step/run", h.run)
	rg.GET("/services/:serviceID/work", h.work)
}

var errorCodes = []struct {
	err    error
	status int
	code   string
}{
	{ErrBadTitle, http.StatusBadRequest, "bad_title"},
	{ErrBadBody, http.StatusBadRequest, "bad_body"},
	{ErrBadParent, http.StatusBadRequest, "bad_parent"},
	{ErrBadVersion, http.StatusBadRequest, "bad_version"},
	{ErrBadCursor, http.StatusBadRequest, "bad_cursor"},
	{ErrConflict, http.StatusConflict, "version_conflict"},
	{ErrTooMany, http.StatusConflict, "too_many_pages"},
	{ErrHasPages, http.StatusConflict, "has_pages"},
	{ErrDeleted, http.StatusConflict, "deleted"},
	{ErrNoStep, http.StatusNotFound, "no_step"},
	{ErrHostCommand, http.StatusForbidden, "operators_only"},
	{ErrAgentHostStep, http.StatusForbidden, "host_step_people_only"},
}

func (h *Handler) fail(c *gin.Context, op string, err error) {
	if respond.Gate(c, err) {
		return
	}
	for _, e := range errorCodes {
		if errors.Is(err, e.err) {
			respond.ErrorData(c, e.status, err.Error(), gin.H{"code": e.code})
			return
		}
	}
	if errors.Is(err, ErrNotFound) {
		respond.Error(c, http.StatusNotFound, "not found")
		return
	}
	h.log.Error(op, zap.Error(err), zap.String("project_id", c.Param("projectID")))
	respond.Error(c, http.StatusInternalServerError, "failed to "+op)
}

// inProject answers 404 unless the project is the workspace's.
func (h *Handler) inProject(c *gin.Context) bool {
	if owned, err := h.projects.CheckOwnership(c.Request.Context(), c.Param("projectID"), middleware.OrgIDFrom(c)); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "project not found")
		return false
	}
	return true
}

// canWrite: owners and developers write; viewers read.
func canWrite(c *gin.Context) bool {
	if r := middleware.MemberRoleFrom(c); r == "owner" || r == "developer" {
		return true
	}
	respond.ErrorData(c, http.StatusForbidden, "viewers can read the docs but not change them", gin.H{"code": "forbidden"})
	return false
}

func (h *Handler) tree(c *gin.Context) {
	if !h.inProject(c) {
		return
	}
	out, err := h.svc.Tree(c.Request.Context(), c.Param("projectID"), c.Query("deleted") == "true")
	if err != nil {
		h.fail(c, "list the pages", err)
		return
	}
	respond.JSON(c, http.StatusOK, out)
}

func (h *Handler) create(c *gin.Context) {
	if !h.inProject(c) || !canWrite(c) {
		return
	}
	var in CreateInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, ErrBadTitle.Error(), gin.H{"code": "bad_title"})
		return
	}
	p, err := h.svc.Create(c.Request.Context(), middleware.OrgIDFrom(c), c.Param("projectID"), middleware.UserIDFrom(c), in)
	if err != nil {
		h.fail(c, "write the page", err)
		return
	}
	respond.JSON(c, http.StatusCreated, p)
}

func (h *Handler) get(c *gin.Context) {
	if !h.inProject(c) {
		return
	}
	p, err := h.svc.Get(c.Request.Context(), middleware.OrgIDFrom(c), c.Param("projectID"), c.Param("pageID"))
	if err != nil {
		h.fail(c, "read the page", err)
		return
	}
	respond.JSON(c, http.StatusOK, p)
}

func (h *Handler) update(c *gin.Context) {
	if !h.inProject(c) || !canWrite(c) {
		return
	}
	var in UpdateInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, "invalid request body", gin.H{"code": "bad_body"})
		return
	}
	p, err := h.svc.Update(c.Request.Context(), middleware.OrgIDFrom(c), c.Param("projectID"), c.Param("pageID"), middleware.UserIDFrom(c), in)
	if err != nil {
		h.fail(c, "save the page", err)
		return
	}
	respond.JSON(c, http.StatusOK, p)
}

func (h *Handler) remove(c *gin.Context) {
	if !h.inProject(c) || !canWrite(c) {
		return
	}
	if err := h.svc.Delete(c.Request.Context(), middleware.OrgIDFrom(c), c.Param("projectID"), c.Param("pageID")); err != nil {
		h.fail(c, "delete the page", err)
		return
	}
	h.audit.WriteAuditLogDetail(c.Request.Context(), middleware.OrgIDFrom(c), middleware.UserIDFrom(c), "doc.deleted", "doc_page:"+c.Param("pageID"),
		map[string]any{"project_id": c.Param("projectID")})
	respond.NoContent(c)
}

func (h *Handler) restore(c *gin.Context) {
	if !h.inProject(c) || !canWrite(c) {
		return
	}
	p, err := h.svc.Restore(c.Request.Context(), middleware.OrgIDFrom(c), c.Param("projectID"), c.Param("pageID"))
	if err != nil {
		h.fail(c, "restore the page", err)
		return
	}
	respond.JSON(c, http.StatusOK, p)
}

func (h *Handler) verify(c *gin.Context) {
	if !h.inProject(c) || !canWrite(c) {
		return
	}
	p, err := h.svc.Verify(c.Request.Context(), middleware.OrgIDFrom(c), c.Param("projectID"), c.Param("pageID"), middleware.UserIDFrom(c))
	if err != nil {
		h.fail(c, "verify the page", err)
		return
	}
	respond.JSON(c, http.StatusOK, p)
}

func (h *Handler) versions(c *gin.Context) {
	if !h.inProject(c) {
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "30"))
	out, next, err := h.svc.Versions(c.Request.Context(), c.Param("projectID"), c.Param("pageID"), c.Query("cursor"), limit)
	if err != nil {
		h.fail(c, "list the versions", err)
		return
	}
	respond.JSONPage(c, http.StatusOK, out, next)
}

func (h *Handler) version(c *gin.Context) {
	if !h.inProject(c) {
		return
	}
	n, err := strconv.Atoi(c.Param("version"))
	if err != nil || n <= 0 {
		respond.Error(c, http.StatusNotFound, "not found")
		return
	}
	v, err := h.svc.Version(c.Request.Context(), c.Param("projectID"), c.Param("pageID"), n)
	if err != nil {
		h.fail(c, "read the version", err)
		return
	}
	respond.JSON(c, http.StatusOK, v)
}

type runInput struct {
	Version int `json:"version" binding:"required"`
}

// run streams one of the page's saved commands (D-86, Q-12): owners and
// developers; a host step for operators with the password. An agent runs a
// container step under the policy and gets the output as JSON (D-102); API
// keys never run one.
func (h *Handler) run(c *gin.Context) {
	_, isAgent := middleware.AgentFrom(c)
	if middleware.ViaAPIKey(c) && !isAgent {
		respond.ErrorData(c, http.StatusForbidden, "running a page's commands needs a signed-in session, not an API key", gin.H{"code": "session_required"})
		return
	}
	if !h.inProject(c) || !canWrite(c) {
		return
	}
	var in runInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, ErrBadVersion.Error(), gin.H{"code": "bad_version"})
		return
	}
	step, err := strconv.Atoi(c.Param("step"))
	if err != nil || step < 0 {
		respond.ErrorData(c, http.StatusNotFound, ErrNoStep.Error(), gin.H{"code": "no_step"})
		return
	}
	ctx := c.Request.Context()
	userID := middleware.UserIDFrom(c)
	st, err := h.svc.Step(ctx, c.Param("projectID"), c.Param("pageID"), step, in.Version)
	if err != nil {
		h.fail(c, "run the step", err)
		return
	}
	if err := h.svc.CheckAgent(ctx, st); err != nil {
		h.fail(c, "run the step", err)
		return
	}
	if isAgent {
		h.runForAgent(c, st, step, in.Version)
		return
	}
	if st.Host {
		if err := h.svc.AuthorizeHost(ctx, userID); err != nil {
			h.fail(c, "run the step", err)
			return
		}
		if !middleware.RequireReauth(c) {
			return
		}
	}

	write, started := lazySSE(c)
	runErr := h.svc.Run(ctx, userID, st, write)
	outcome := "ok"
	if runErr != nil {
		outcome = "failed"
		if !errors.Is(runErr, context.Canceled) {
			h.log.Warn("doc step failed", zap.Error(runErr), zap.String("page_id", c.Param("pageID")))
			write("ERROR: " + runErr.Error())
		}
	}
	h.audit.WriteAuditLogDetail(context.WithoutCancel(ctx), middleware.OrgIDFrom(c), userID, "doc.step_run", "doc_page:"+c.Param("pageID"),
		map[string]any{"project_id": c.Param("projectID"), "version": in.Version, "step": step, "service_id": st.ServiceID, "host": st.Host, "outcome": outcome})
	if !started() {
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
	}
	fmt.Fprint(c.Writer, "event: done\ndata: {}\n\n")
	c.Writer.Flush()
}

// agentLines caps the output an agent's step returns (D-102).
const agentLines = 1000

// runForAgent runs a container step for an agent, for at most
// AgentStepTime, and answers with its output's last lines as JSON.
func (h *Handler) runForAgent(c *gin.Context, st *Step, step, version int) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), AgentStepTime)
	defer cancel()
	respond.Long(c, AgentStepTime+time.Minute)
	lines := make([]string, 0, 64)
	runErr := h.svc.Run(ctx, middleware.UserIDFrom(c), st, func(l string) {
		if len(lines) == agentLines {
			lines = lines[1:]
		}
		lines = append(lines, l)
	})
	out := gin.H{"outcome": "ok", "lines": lines, "service_id": st.ServiceID, "command": st.Command}
	if runErr != nil {
		out["outcome"], out["error"] = "failed", runErr.Error()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			out["error"] = "the step ran past " + AgentStepTime.String() + " and was stopped"
		}
	}
	h.audit.WriteAuditLogDetail(context.WithoutCancel(c.Request.Context()), middleware.OrgIDFrom(c), middleware.UserIDFrom(c), "doc.step_run",
		"doc_page:"+c.Param("pageID"), map[string]any{"project_id": c.Param("projectID"), "version": version, "step": step,
			"service_id": st.ServiceID, "host": false, "outcome": out["outcome"]})
	respond.JSON(c, http.StatusOK, out)
}

// lazySSE starts the event stream at the first line, as a service's exec.
func lazySSE(c *gin.Context) (write func(string), started func() bool) {
	respond.Stream(c)
	sent := false
	write = func(line string) {
		if !sent {
			c.Writer.Header().Set("Content-Type", "text/event-stream")
			c.Writer.Header().Set("Cache-Control", "no-cache")
			sent = true
		}
		fmt.Fprintf(c.Writer, "data: %s\n\n", strings.ReplaceAll(line, "\n", " "))
		c.Writer.Flush()
	}
	return write, func() bool { return sent }
}

func (h *Handler) work(c *gin.Context) {
	w, err := h.svc.Work(c.Request.Context(), middleware.OrgIDFrom(c), c.Param("serviceID"))
	if err != nil {
		h.fail(c, "read the service's work", err)
		return
	}
	respond.JSON(c, http.StatusOK, w)
}
