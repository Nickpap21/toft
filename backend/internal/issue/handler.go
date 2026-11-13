package issue

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Handler is the issues module's JSON API (D-81): reads for every member
// and issues_read keys, writes for owners, developers and issues_write keys
// (apikey's scopes), saved views for people.
type Handler struct {
	svc      *Service
	projects ProjectOwnershipChecker
	log      *zap.Logger
}

// ProjectOwnershipChecker says whether a project is in a workspace.
// Satisfied by *project.Service.
type ProjectOwnershipChecker interface {
	CheckOwnership(ctx context.Context, projectID, orgID string) (bool, error)
}

func NewHandler(svc *Service, projects ProjectOwnershipChecker, log *zap.Logger) *Handler {
	return &Handler{svc: svc, projects: projects, log: log}
}

func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	p := rg.Group("/projects/:projectID")
	p.GET("/issues", h.list)
	p.POST("/issues", h.create)
	p.GET("/issue-counts", h.counts)
	p.GET("/issue-labels", h.labels)
	p.GET("/issue-views", h.views)
	p.POST("/issue-views", h.addView)
	p.PATCH("/issue-views/:viewID", h.updateView)
	p.DELETE("/issue-views/:viewID", h.deleteView)
	i := p.Group("/issues/:issueNumber")
	i.GET("", h.get)
	i.PATCH("", h.update)
	i.GET("/timeline", h.timeline)
	i.POST("/comments", h.addComment)
	i.PATCH("/comments/:commentID", h.editComment)
	i.DELETE("/comments/:commentID", h.deleteComment)
	i.POST("/relations", h.relate)
	i.DELETE("/relations/:relationID", h.unrelate)
	i.POST("/clone", h.clone)
	p.GET("/issue-settings", h.projectSettings)
	p.PATCH("/issue-settings", h.saveProjectSettings)
	rg.GET("/workspace/search", h.search)
	rg.GET("/workspace/my-issues", h.myIssues)
	rg.POST("/services/:serviceID/deployments/:deploymentID/issue", h.openFromFailure)
	rg.GET("/workspace/issue-settings", h.workspaceSettings)
	rg.PATCH("/workspace/issue-settings", h.saveWorkspaceSettings)
	rg.GET("/workspace/issue-types", h.types)
	rg.POST("/workspace/issue-types", h.addType)
	rg.PATCH("/workspace/issue-types/:typeID", h.updateType)
	rg.DELETE("/workspace/issue-types/:typeID", h.archiveType)
	// Cycles and the Timeline (D-90).
	p.GET("/cycles", h.projectCycles)
	p.GET("/issue-timeline", h.issueTimeline)
	rg.GET("/workspace/cycles", h.cycles)
	rg.GET("/workspace/cycles/:cycleNumber", h.cycle)
	rg.PATCH("/workspace/cycles/:cycleNumber", h.renameCycle)
	rg.GET("/workspace/cycles/:cycleNumber/issues", h.cycleIssues)
}

// errorCodes are the errors a client can act on, by status and code.
var errorCodes = []struct {
	err    error
	status int
	code   string
}{
	{ErrBadTitle, http.StatusBadRequest, "bad_title"},
	{ErrBadDescription, http.StatusBadRequest, "bad_description"},
	{ErrBadLabel, http.StatusBadRequest, "bad_label"},
	{ErrBadStatus, http.StatusBadRequest, "bad_status"},
	{ErrBadPriority, http.StatusBadRequest, "bad_priority"},
	{ErrNotAssignable, http.StatusUnprocessableEntity, "not_assignable"},
	{ErrAgentField, http.StatusForbidden, "agent_cannot_assign"},
	{ErrBadCursor, http.StatusBadRequest, "bad_cursor"},
	{ErrEmptyComment, http.StatusBadRequest, "empty_comment"},
	{ErrNotYours, http.StatusForbidden, "not_yours"},
	{ErrBadName, http.StatusBadRequest, "bad_name"},
	{ErrNameTaken, http.StatusConflict, "name_taken"},
	{ErrBadFilters, http.StatusBadRequest, "bad_filters"},
	{ErrBadQuery, http.StatusBadRequest, "bad_query"},
	{ErrBadKey, http.StatusBadRequest, "bad_key"},
	{ErrKeyTaken, http.StatusConflict, "key_taken"},
	{ErrBadDigits, http.StatusBadRequest, "bad_digits"},
	{ErrBadType, http.StatusBadRequest, "bad_type"},
	{ErrBadTypeInput, http.StatusBadRequest, "bad_type"},
	{ErrTypeTaken, http.StatusConflict, "name_taken"},
	{ErrLastType, http.StatusConflict, "last_type"},
	{ErrBadParent, http.StatusUnprocessableEntity, "bad_parent"},
	{ErrNeedsParent, http.StatusUnprocessableEntity, "needs_parent"},
	{ErrIssueNotFound, http.StatusNotFound, "issue_not_found"},
	{ErrRelated, http.StatusConflict, "related"},
	{ErrBadRelation, http.StatusBadRequest, "bad_relation"},
	{ErrNotFailed, http.StatusConflict, "not_failed"},
	{ErrNoProject, http.StatusConflict, "no_project"},
	{ErrNotService, http.StatusNotFound, "not_found"},
	{ErrBadCycle, http.StatusUnprocessableEntity, "bad_cycle"},
	{ErrContainerCycle, http.StatusUnprocessableEntity, "container_not_in_cycle"},
	{ErrBadDates, http.StatusBadRequest, "bad_dates"},
	{ErrBadEstimate, http.StatusBadRequest, "bad_estimate"},
	{ErrEstimatesOff, http.StatusUnprocessableEntity, "estimates_off"},
	{ErrBadSchedule, http.StatusBadRequest, "bad_schedule"},
	{ErrBadTimeZone, http.StatusBadRequest, "bad_time_zone"},
	{ErrBadScale, http.StatusBadRequest, "bad_estimates"},
	{ErrBadCycleName, http.StatusBadRequest, "bad_name"},
}

func (h *Handler) fail(c *gin.Context, op string, err error) {
	if respond.Gate(c, err) || respond.VersionRefused(c, err) {
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
	respond.ErrorData(c, http.StatusForbidden, "viewers can read issues but not change them", gin.H{"code": "forbidden"})
	return false
}

func number(c *gin.Context) (int, bool) {
	n, err := strconv.Atoi(c.Param("issueNumber"))
	if err != nil || n <= 0 {
		respond.Error(c, http.StatusNotFound, "not found")
		return 0, false
	}
	return n, true
}

func (h *Handler) query(c *gin.Context) (ListQuery, bool) {
	lq, err := Query(c.Param("projectID"), middleware.UserIDFrom(c), c.Query("status"), c.Query("priority"),
		c.Query("assignee"), c.Query("label"), c.Query("q"), c.Query("order"), c.Query("type"), c.Query("parent"), c.Query("cycle"))
	if err != nil {
		h.fail(c, "list issues", err)
		return lq, false
	}
	return lq, true
}

func (h *Handler) list(c *gin.Context) {
	if !h.inProject(c) {
		return
	}
	lq, ok := h.query(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	out, next, err := h.svc.List(c.Request.Context(), lq, c.Query("cursor"), limit)
	if err != nil {
		h.fail(c, "list issues", err)
		return
	}
	respond.JSONPage(c, http.StatusOK, out, next)
}

func (h *Handler) counts(c *gin.Context) {
	if !h.inProject(c) {
		return
	}
	lq, ok := h.query(c)
	if !ok {
		return
	}
	out, err := h.svc.Counts(c.Request.Context(), lq)
	if err != nil {
		h.fail(c, "count issues", err)
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
	in.KeyProject, _, _ = middleware.APIKeyScopeFrom(c)
	if a, ok := middleware.AgentFrom(c); ok && !a.AllProjects {
		in.KeyProject = c.Param("projectID") // its parent stays in its projects (D-100)
	}
	d, err := h.svc.Create(c.Request.Context(), c.Param("projectID"), middleware.UserIDFrom(c), in)
	if err != nil {
		h.fail(c, "open the issue", err)
		return
	}
	respond.JSON(c, http.StatusCreated, d)
}

func (h *Handler) get(c *gin.Context) {
	n, ok := number(c)
	if !ok || !h.inProject(c) {
		return
	}
	d, err := h.svc.Get(c.Request.Context(), c.Param("projectID"), n)
	if err != nil {
		h.fail(c, "read the issue", err)
		return
	}
	respond.JSONVersioned(c, http.StatusOK, d, d.version)
}

func (h *Handler) update(c *gin.Context) {
	n, ok := number(c)
	if !ok || !h.inProject(c) || !canWrite(c) {
		return
	}
	var in UpdateInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.Error(c, http.StatusBadRequest, "title, description, status, priority, assignee_id or labels")
		return
	}
	in.KeyProject, _, _ = middleware.APIKeyScopeFrom(c)
	d, err := h.svc.Update(c.Request.Context(), c.Param("projectID"), n, middleware.UserIDFrom(c), in)
	if err != nil {
		h.fail(c, "change the issue", err)
		return
	}
	respond.JSONVersioned(c, http.StatusOK, d, d.version)
}

func (h *Handler) timeline(c *gin.Context) {
	n, ok := number(c)
	if !ok || !h.inProject(c) {
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	items, next, err := h.svc.Timeline(c.Request.Context(), c.Param("projectID"), n, c.Query("cursor"), limit)
	if err != nil {
		h.fail(c, "read the issue's history", err)
		return
	}
	respond.JSONPage(c, http.StatusOK, items, next)
}

type commentInput struct {
	Body string `json:"body"`
}

func (h *Handler) addComment(c *gin.Context) {
	n, ok := number(c)
	if !ok || !h.inProject(c) || !canWrite(c) {
		return
	}
	var in commentInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, ErrEmptyComment.Error(), gin.H{"code": "empty_comment"})
		return
	}
	it, err := h.svc.AddComment(c.Request.Context(), c.Param("projectID"), n, middleware.UserIDFrom(c), in.Body)
	if err != nil {
		h.fail(c, "comment", err)
		return
	}
	respond.JSON(c, http.StatusCreated, it)
}

func (h *Handler) editComment(c *gin.Context) {
	n, ok := number(c)
	if !ok || !h.inProject(c) || !canWrite(c) {
		return
	}
	var in commentInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, ErrEmptyComment.Error(), gin.H{"code": "empty_comment"})
		return
	}
	it, err := h.svc.EditComment(c.Request.Context(), c.Param("projectID"), n, c.Param("commentID"), middleware.UserIDFrom(c), in.Body)
	if err != nil {
		h.fail(c, "edit the comment", err)
		return
	}
	respond.JSON(c, http.StatusOK, it)
}

func (h *Handler) deleteComment(c *gin.Context) {
	n, ok := number(c)
	if !ok || !h.inProject(c) || !canWrite(c) {
		return
	}
	owner := middleware.MemberRoleFrom(c) == "owner"
	if err := h.svc.DeleteComment(c.Request.Context(), c.Param("projectID"), n, c.Param("commentID"), middleware.UserIDFrom(c), owner); err != nil {
		h.fail(c, "delete the comment", err)
		return
	}
	respond.NoContent(c)
}

func (h *Handler) labels(c *gin.Context) {
	if !h.inProject(c) {
		return
	}
	out, err := h.svc.Labels(c.Request.Context(), c.Param("projectID"))
	if err != nil {
		h.fail(c, "list labels", err)
		return
	}
	respond.JSON(c, http.StatusOK, out)
}

func (h *Handler) views(c *gin.Context) {
	if !h.inProject(c) {
		return
	}
	out, err := h.svc.Views(c.Request.Context(), c.Param("projectID"))
	if err != nil {
		h.fail(c, "list views", err)
		return
	}
	respond.JSON(c, http.StatusOK, out)
}

func (h *Handler) viewInput(c *gin.Context) (ViewInput, bool) {
	var in ViewInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, ErrBadFilters.Error(), gin.H{"code": "bad_filters"})
		return in, false
	}
	return in, true
}

func (h *Handler) addView(c *gin.Context) {
	if !h.inProject(c) || !canWrite(c) {
		return
	}
	in, ok := h.viewInput(c)
	if !ok {
		return
	}
	v, err := h.svc.AddView(c.Request.Context(), c.Param("projectID"), middleware.UserIDFrom(c), in)
	if err != nil {
		h.fail(c, "save the view", err)
		return
	}
	respond.JSON(c, http.StatusCreated, v)
}

func (h *Handler) updateView(c *gin.Context) {
	if !h.inProject(c) || !canWrite(c) {
		return
	}
	in, ok := h.viewInput(c)
	if !ok {
		return
	}
	owner := middleware.MemberRoleFrom(c) == "owner"
	v, err := h.svc.UpdateView(c.Request.Context(), c.Param("projectID"), c.Param("viewID"), middleware.UserIDFrom(c), owner, in)
	if err != nil {
		h.fail(c, "change the view", err)
		return
	}
	respond.JSON(c, http.StatusOK, v)
}

func (h *Handler) deleteView(c *gin.Context) {
	if !h.inProject(c) || !canWrite(c) {
		return
	}
	owner := middleware.MemberRoleFrom(c) == "owner"
	if err := h.svc.DeleteView(c.Request.Context(), c.Param("projectID"), c.Param("viewID"), middleware.UserIDFrom(c), owner); err != nil {
		h.fail(c, "delete the view", err)
		return
	}
	respond.NoContent(c)
}

func (h *Handler) search(c *gin.Context) {
	projectID := c.Query("project_id")
	if projectID != "" {
		if owned, err := h.projects.CheckOwnership(c.Request.Context(), projectID, middleware.OrgIDFrom(c)); err != nil || !owned {
			respond.Error(c, http.StatusNotFound, "project not found")
			return
		}
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	out, err := h.svc.Search(c.Request.Context(), middleware.OrgIDFrom(c), projectID, c.Query("q"), c.Query("types"), limit)
	if err != nil {
		h.fail(c, "search", err)
		return
	}
	respond.JSON(c, http.StatusOK, out)
}

// isOwner: workspace owners change types and settings (D-83).
func isOwner(c *gin.Context) bool {
	if middleware.MemberRoleFrom(c) == "owner" {
		return true
	}
	respond.ErrorData(c, http.StatusForbidden, "only owners change issue types and keys", gin.H{"code": "forbidden"})
	return false
}

func (h *Handler) relate(c *gin.Context) {
	n, ok := number(c)
	if !ok || !h.inProject(c) || !canWrite(c) {
		return
	}
	var in RelationInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, ErrBadRelation.Error(), gin.H{"code": "bad_relation"})
		return
	}
	in.KeyProject, _, _ = middleware.APIKeyScopeFrom(c)
	d, err := h.svc.Relate(c.Request.Context(), c.Param("projectID"), n, middleware.UserIDFrom(c), in)
	if err != nil {
		h.fail(c, "relate the issues", err)
		return
	}
	respond.JSON(c, http.StatusCreated, d)
}

func (h *Handler) unrelate(c *gin.Context) {
	n, ok := number(c)
	if !ok || !h.inProject(c) || !canWrite(c) {
		return
	}
	if err := h.svc.Unrelate(c.Request.Context(), c.Param("projectID"), n, middleware.UserIDFrom(c), c.Param("relationID")); err != nil {
		h.fail(c, "remove the relation", err)
		return
	}
	respond.NoContent(c)
}

func (h *Handler) clone(c *gin.Context) {
	n, ok := number(c)
	if !ok || !h.inProject(c) || !canWrite(c) {
		return
	}
	var in CloneInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, ErrBadTitle.Error(), gin.H{"code": "bad_title"})
		return
	}
	if in.ProjectID != "" {
		// A project-scoped key clones within its project.
		if keyProject, _, scoped := middleware.APIKeyScopeFrom(c); scoped && keyProject != in.ProjectID {
			respond.ErrorData(c, http.StatusForbidden, "this API key is limited to its scopes in one project", gin.H{"code": "insufficient_scope"})
			return
		}
		if owned, err := h.projects.CheckOwnership(c.Request.Context(), in.ProjectID, middleware.OrgIDFrom(c)); err != nil || !owned {
			respond.Error(c, http.StatusNotFound, "project not found")
			return
		}
	}
	d, err := h.svc.Clone(c.Request.Context(), c.Param("projectID"), n, middleware.UserIDFrom(c), in)
	if err != nil {
		h.fail(c, "clone the issue", err)
		return
	}
	respond.JSON(c, http.StatusCreated, d)
}

func (h *Handler) projectSettings(c *gin.Context) {
	if !h.inProject(c) {
		return
	}
	out, err := h.svc.ProjectSettings(c.Request.Context(), c.Param("projectID"))
	if err != nil {
		h.fail(c, "read the issue settings", err)
		return
	}
	respond.JSONVersioned(c, http.StatusOK, out, out.version)
}

func (h *Handler) saveProjectSettings(c *gin.Context) {
	if !h.inProject(c) || !isOwner(c) {
		return
	}
	var raw map[string]json.RawMessage
	var in ProjectSettingsInput
	body, err := c.GetRawData()
	if err == nil {
		err = json.Unmarshal(body, &raw)
	}
	if err == nil {
		err = json.Unmarshal(body, &in)
	}
	if err != nil {
		respond.ErrorData(c, http.StatusBadRequest, ErrBadKey.Error(), gin.H{"code": "bad_key"})
		return
	}
	// "digits": null inherits the workspace's.
	if d, ok := raw["digits"]; ok && string(d) == "null" {
		in.InheritDigits = true
	}
	out, err := h.svc.SaveProjectSettings(c.Request.Context(), c.Param("projectID"), in)
	if err != nil {
		h.fail(c, "save the issue settings", err)
		return
	}
	respond.JSONVersioned(c, http.StatusOK, out, out.version)
}

func (h *Handler) workspaceSettings(c *gin.Context) {
	out, err := h.svc.WorkspaceSettings(c.Request.Context(), middleware.OrgIDFrom(c))
	if err != nil {
		h.fail(c, "read the issue settings", err)
		return
	}
	respond.JSON(c, http.StatusOK, out)
}

func (h *Handler) saveWorkspaceSettings(c *gin.Context) {
	if !isOwner(c) {
		return
	}
	var in WorkspaceSettingsInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, ErrBadSchedule.Error(), gin.H{"code": "bad_schedule"})
		return
	}
	out, err := h.svc.SaveWorkspaceSettings(c.Request.Context(), middleware.OrgIDFrom(c), in)
	if err != nil {
		h.fail(c, "save the issue settings", err)
		return
	}
	respond.JSON(c, http.StatusOK, out)
}

func (h *Handler) types(c *gin.Context) {
	out, err := h.svc.Types(c.Request.Context(), middleware.OrgIDFrom(c))
	if err != nil {
		h.fail(c, "list issue types", err)
		return
	}
	respond.JSON(c, http.StatusOK, out)
}

func (h *Handler) addType(c *gin.Context) {
	if !isOwner(c) {
		return
	}
	var in TypeInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, ErrBadTypeInput.Error(), gin.H{"code": "bad_type"})
		return
	}
	t, err := h.svc.AddType(c.Request.Context(), middleware.OrgIDFrom(c), in)
	if err != nil {
		h.fail(c, "add the issue type", err)
		return
	}
	respond.JSON(c, http.StatusCreated, t)
}

func (h *Handler) updateType(c *gin.Context) {
	if !isOwner(c) {
		return
	}
	var in TypeUpdate
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, ErrBadTypeInput.Error(), gin.H{"code": "bad_type"})
		return
	}
	t, err := h.svc.UpdateType(c.Request.Context(), middleware.OrgIDFrom(c), c.Param("typeID"), in)
	if err != nil {
		h.fail(c, "change the issue type", err)
		return
	}
	respond.JSON(c, http.StatusOK, t)
}

func (h *Handler) archiveType(c *gin.Context) {
	if !isOwner(c) {
		return
	}
	if err := h.svc.ArchiveType(c.Request.Context(), middleware.OrgIDFrom(c), c.Param("typeID")); err != nil {
		h.fail(c, "archive the issue type", err)
		return
	}
	respond.NoContent(c)
}

func (h *Handler) myIssues(c *gin.Context) {
	out, err := h.svc.MyIssues(c.Request.Context(), middleware.OrgIDFrom(c), middleware.UserIDFrom(c))
	if err != nil {
		h.fail(c, "list your issues", err)
		return
	}
	respond.JSON(c, http.StatusOK, out)
}

// openFromFailure opens an issue from a failed deploy (D-88).
func (h *Handler) openFromFailure(c *gin.Context) {
	if !canWrite(c) {
		return
	}
	d, err := h.svc.OpenFromFailure(c.Request.Context(), middleware.OrgIDFrom(c), c.Param("serviceID"), c.Param("deploymentID"), middleware.UserIDFrom(c))
	var open *AlreadyOpen
	if errors.As(err, &open) {
		respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "already_open", "key": open.Key})
		return
	}
	if err != nil {
		h.fail(c, "open the issue", err)
		return
	}
	respond.JSON(c, http.StatusCreated, d)
}

// ─── cycles and the Timeline (D-90) ──────────────────────────────────────────

func cycleNumber(c *gin.Context) (int, bool) {
	n, err := strconv.Atoi(c.Param("cycleNumber"))
	if err != nil || n <= 0 {
		respond.Error(c, http.StatusNotFound, "not found")
		return 0, false
	}
	return n, true
}

func (h *Handler) cycles(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "25"))
	out, next, err := h.svc.Cycles(c.Request.Context(), middleware.OrgIDFrom(c), c.Query("state"), c.Query("cursor"), limit)
	if err != nil {
		h.fail(c, "list cycles", err)
		return
	}
	respond.JSONPage(c, http.StatusOK, out, next)
}

func (h *Handler) cycle(c *gin.Context) {
	n, ok := cycleNumber(c)
	if !ok {
		return
	}
	out, err := h.svc.Cycle(c.Request.Context(), middleware.OrgIDFrom(c), n)
	if err != nil {
		h.fail(c, "read the cycle", err)
		return
	}
	respond.JSON(c, http.StatusOK, out)
}

func (h *Handler) renameCycle(c *gin.Context) {
	n, ok := cycleNumber(c)
	if !ok || !canWrite(c) {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, ErrBadCycleName.Error(), gin.H{"code": "bad_name"})
		return
	}
	out, err := h.svc.RenameCycle(c.Request.Context(), middleware.OrgIDFrom(c), n, in.Name)
	if err != nil {
		h.fail(c, "rename the cycle", err)
		return
	}
	respond.JSON(c, http.StatusOK, out)
}

func (h *Handler) cycleIssues(c *gin.Context) {
	n, ok := cycleNumber(c)
	if !ok {
		return
	}
	projectID := c.Query("project_id")
	if projectID != "" {
		if owned, err := h.projects.CheckOwnership(c.Request.Context(), projectID, middleware.OrgIDFrom(c)); err != nil || !owned {
			respond.Error(c, http.StatusNotFound, "project not found")
			return
		}
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "200"))
	out, next, err := h.svc.CycleIssues(c.Request.Context(), middleware.OrgIDFrom(c), n, projectID, c.Query("cursor"), limit)
	if err != nil {
		h.fail(c, "list the cycle's issues", err)
		return
	}
	respond.JSONPage(c, http.StatusOK, out, next)
}

func (h *Handler) projectCycles(c *gin.Context) {
	if !h.inProject(c) {
		return
	}
	out, err := h.svc.ProjectCycles(c.Request.Context(), c.Param("projectID"))
	if err != nil {
		h.fail(c, "list cycles", err)
		return
	}
	respond.JSON(c, http.StatusOK, out)
}

func (h *Handler) issueTimeline(c *gin.Context) {
	if !h.inProject(c) {
		return
	}
	from, err := parseDate(c.Query("from"))
	if err != nil {
		h.fail(c, "read the timeline", ErrBadQuery)
		return
	}
	f := TimelineFilter{Assignee: c.Query("assignee"), Labels: splitList(strings.ToLower(c.Query("label"))), TypeIDs: splitList(c.Query("type"))}
	if f.Assignee == "me" {
		f.Assignee = middleware.UserIDFrom(c)
	}
	var since time.Time
	if from != nil {
		since = *from
	}
	out, err := h.svc.IssueTimeline(c.Request.Context(), c.Param("projectID"), since, f)
	if err != nil {
		h.fail(c, "read the timeline", err)
		return
	}
	respond.JSON(c, http.StatusOK, out)
}
