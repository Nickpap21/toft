package code

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Repositories' settings and pull requests over HTTP (D-75).

// canWrite: owners and developers write; viewers only read.
func canWrite(c *gin.Context) bool {
	if r := middleware.MemberRoleFrom(c); r == "owner" || r == "developer" {
		return true
	}
	respond.ErrorData(c, http.StatusForbidden, "viewers can read code but not change it", gin.H{"code": "forbidden"})
	return false
}

func isOwner(c *gin.Context) bool {
	if middleware.MemberRoleFrom(c) == "owner" {
		return true
	}
	respond.ErrorData(c, http.StatusForbidden, "only owners change a repository's settings", gin.H{"code": "forbidden"})
	return false
}

// codeErrors are the code module's errors a client can act on, by status
// and code.
var codeErrors = []struct {
	err    error
	status int
	code   string
}{
	{ErrNameTaken, http.StatusConflict, "name_taken"},
	{ErrBadName, http.StatusBadRequest, "bad_name"},
	{ErrLastRepo, http.StatusConflict, "last_repository"},
	{ErrConfirmName, http.StatusBadRequest, "confirm_name"},
	{ErrPullClosed, http.StatusConflict, "pull_closed"},
	{ErrSameBranch, http.StatusUnprocessableEntity, "same_branch"},
	{ErrNoChanges, http.StatusUnprocessableEntity, "no_commits"},
	{ErrNotBranches, http.StatusUnprocessableEntity, "not_branches"},
	{ErrBadTitle, http.StatusBadRequest, "bad_title"},
	{ErrEmptyComment, http.StatusBadRequest, "empty_comment"},
	{ErrNotYours, http.StatusForbidden, "not_yours"},
	{ErrHeadGone, http.StatusConflict, "head_gone"},
	{ErrHeadMoved, http.StatusConflict, "head_moved"},
	{ErrHeadKept, http.StatusConflict, "head_kept"},
	{ErrBadCursor, http.StatusBadRequest, "bad_cursor"},
	{ErrBadState, http.StatusBadRequest, "bad_state"},
	{ErrBadCheckName, http.StatusBadRequest, "bad_check_name"},
	{ErrReservedName, http.StatusBadRequest, "reserved_name"},
	{ErrBadCheckState, http.StatusBadRequest, "bad_state"},
	{ErrBadCheckURL, http.StatusBadRequest, "bad_url"},
	{ErrBadDescription, http.StatusBadRequest, "bad_description"},
	{ErrBadSHA, http.StatusBadRequest, "bad_sha"},
	{ErrNoCommit, http.StatusNotFound, "commit_not_found"},
	{ErrTooManyChecks, http.StatusUnprocessableEntity, "too_many_checks"},
	{ErrRepoInUse, http.StatusConflict, "repository_in_use"},
	{ErrBadRemote, http.StatusBadRequest, "bad_url"},
	{ErrBadHookURL, http.StatusBadRequest, "bad_url"},
	{ErrBadUsername, http.StatusBadRequest, "bad_username"},
	{ErrPrivateAddress, http.StatusBadRequest, "private_address"},
	{ErrBadEvents, http.StatusBadRequest, "bad_events"},
	{ErrNotEmpty, http.StatusConflict, "not_empty"},
	{ErrImportRunning, http.StatusConflict, "import_running"},
	{ErrMirrorLimit, http.StatusConflict, "mirror_limit"},
	{ErrSyncRunning, http.StatusConflict, "sync_running"},
	{ErrMirrorReadOnly, http.StatusConflict, "mirror_read_only"},
	{ErrNotYourMirror, http.StatusForbidden, "forbidden"},
	{ErrWebhookLimit, http.StatusConflict, "webhook_limit"},
	{ErrWebhookPaused, http.StatusConflict, "webhook_paused"},
	{ErrDeliveryPending, http.StatusConflict, "delivery_pending"},
	{ErrEgressOff, http.StatusServiceUnavailable, "egress_off"},
	{ErrBranchExists, http.StatusConflict, "branch_exists"},
	{ErrBranchDeployed, http.StatusForbidden, "branch_deployed"},
	{ErrNotAgentBranch, http.StatusForbidden, "not_agent_branch"},
}

// fail answers err: a pull request that exists already with its number,
// the code module's own errors, then git's (browseError).
func (h *Handler) fail(c *gin.Context, op string, err error) {
	if repoInUse(c, err) || respond.Gate(c, err) {
		return
	}
	var exists *PullExistsError
	if errors.As(err, &exists) {
		respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "pull_exists", "number": exists.Number})
		return
	}
	for _, e := range codeErrors {
		if errors.Is(err, e.err) {
			respond.ErrorData(c, e.status, err.Error(), gin.H{"code": e.code})
			return
		}
	}
	if errors.Is(err, ErrNotFound) && op != "repository" {
		respond.Error(c, http.StatusNotFound, "not found")
		return
	}
	h.browseError(c, op, err)
}

func (h *Handler) createRepository(c *gin.Context) {
	if !h.inProject(c) || !canWrite(c) {
		return
	}
	var in CreateRepositoryInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, ErrBadName.Error(), gin.H{"code": "bad_name"})
		return
	}
	r, err := h.svc.CreateRepository(c.Request.Context(), middleware.OrgIDFrom(c), c.Param("projectID"), in.Name, c.Request.Host)
	if err != nil {
		h.fail(c, "create repository", err)
		return
	}
	h.auditRepo(c, "repository.created", r.ID, map[string]any{"name": r.Name, "project_id": c.Param("projectID")})
	respond.JSON(c, http.StatusCreated, r)
}

// updateRepository changes the default branch and its protection (owners);
// turning protection off, or moving the default branch (protection moves
// with it, L2), asks for the password, since either lets anyone who can
// push rewrite the branch that was protected.
func (h *Handler) updateRepository(c *gin.Context) {
	if !h.inProject(c) || !isOwner(c) {
		return
	}
	var in UpdateRepositoryInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.Error(c, http.StatusBadRequest, "default_branch (a branch) or protect_default_branch (true or false)")
		return
	}
	if (in.DefaultBranch != nil || in.ProtectDefaultBranch != nil && !*in.ProtectDefaultBranch) && !middleware.RequireReauth(c) {
		return
	}
	d, changed, err := h.svc.UpdateRepository(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), c.Request.Host, in)
	if err != nil {
		h.fail(c, "repository", err)
		return
	}
	if len(changed) > 0 {
		changed["name"] = d.Name
		h.auditRepo(c, "repository.updated", d.ID, changed)
	}
	respond.JSON(c, http.StatusOK, d)
}

// deleteRepository removes a repository (owners, with the password and its
// name in ?confirm), never a project's last.
func (h *Handler) deleteRepository(c *gin.Context) {
	if !h.inProject(c) || !isOwner(c) || !middleware.RequireReauth(c) {
		return
	}
	r, err := h.svc.DeleteRepository(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), c.Query("confirm"))
	if err != nil {
		h.fail(c, "repository", err)
		return
	}
	h.auditRepo(c, "repository.deleted", r.ID, map[string]any{"name": r.Name, "project_id": c.Param("projectID")})
	respond.NoContent(c)
}

func (h *Handler) auditRepo(c *gin.Context, action, id string, detail map[string]any) {
	if h.audit != nil {
		h.audit.WriteAuditLogDetail(c.Request.Context(), middleware.OrgIDFrom(c), middleware.UserIDFrom(c), action, "repository:"+id, detail)
	}
}

func (h *Handler) compare(c *gin.Context) {
	if !h.inProject(c) {
		return
	}
	cmp, err := h.svc.Compare(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), c.Query("base"), c.Query("head"))
	if err != nil {
		h.fail(c, "compare", err)
		return
	}
	respond.JSON(c, http.StatusOK, cmp)
}

func (h *Handler) compareDiff(c *gin.Context) {
	if !h.inProject(c) {
		return
	}
	q := url.Values{}
	for _, k := range []string{"base", "head", "limit", "cursor"} {
		if v, ok := c.GetQuery(k); ok {
			q.Set(k, v)
		}
	}
	h.page(c, "compare diff", func() ([]byte, error) {
		return h.svc.CompareDiff(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), q)
	})
}

// page answers an executor's page ({items, next}) as the API's.
func (h *Handler) page(c *gin.Context, op string, read func() ([]byte, error)) {
	out, err := read()
	if err != nil {
		h.fail(c, op, err)
		return
	}
	var pg struct {
		Items json.RawMessage `json:"items"`
		Next  string          `json:"next"`
	}
	if err := json.Unmarshal(out, &pg); err != nil {
		h.fail(c, op, err)
		return
	}
	respond.JSONPage(c, http.StatusOK, pg.Items, pg.Next)
}

func pullNumber(c *gin.Context) (int, bool) {
	n, err := strconv.Atoi(c.Param("pullNumber"))
	if err != nil || n <= 0 {
		respond.Error(c, http.StatusNotFound, "not found")
		return 0, false
	}
	return n, true
}

func (h *Handler) listPulls(c *gin.Context) {
	if !h.inProject(c) {
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	pulls, next, err := h.svc.ListPulls(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), c.Query("state"), c.Query("cursor"), limit)
	if err != nil {
		h.fail(c, "pull requests", err)
		return
	}
	respond.JSONPage(c, http.StatusOK, pulls, next)
}

func (h *Handler) openPull(c *gin.Context) {
	if !h.inProject(c) || !canWrite(c) {
		return
	}
	var in OpenPullInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.Error(c, http.StatusBadRequest, "title (1 to 256 characters), base and head are required; body has 64 KiB at most")
		return
	}
	in.ViaKey, in.APIKeyID = middleware.ViaAPIKey(c), middleware.APIKeyIDFrom(c)
	p, err := h.svc.OpenPull(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), middleware.UserIDFrom(c), in)
	if err != nil {
		h.fail(c, "open pull request", err)
		return
	}
	respond.JSON(c, http.StatusCreated, p)
}

// createBranch makes a branch; an agent gets the credential that pushes to
// it alone (D-102).
func (h *Handler) createBranch(c *gin.Context) {
	if !h.inProject(c) || !canWrite(c) {
		return
	}
	var in CreateBranchInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, "name (a branch name, 255 characters at most) is required", gin.H{"code": "bad_name"})
		return
	}
	in.ViaKey = middleware.ViaAPIKey(c)
	b, err := h.svc.CreateBranch(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), middleware.UserIDFrom(c), in)
	if err != nil {
		h.fail(c, "create branch", err)
		return
	}
	respond.JSON(c, http.StatusCreated, b)
}

func (h *Handler) getPull(c *gin.Context) {
	n, ok := pullNumber(c)
	if !ok || !h.inProject(c) {
		return
	}
	d, err := h.svc.Pull(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), n)
	if err != nil {
		h.fail(c, "pull request", err)
		return
	}
	respond.JSON(c, http.StatusOK, d)
}

func (h *Handler) updatePull(c *gin.Context) {
	n, ok := pullNumber(c)
	if !ok || !h.inProject(c) || !canWrite(c) {
		return
	}
	var in UpdatePullInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.Error(c, http.StatusBadRequest, "title (1 to 256 characters), body (64 KiB at most) or state (open or closed)")
		return
	}
	p, err := h.svc.UpdatePull(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), n, middleware.UserIDFrom(c), in)
	if err != nil {
		h.fail(c, "update pull request", err)
		return
	}
	respond.JSON(c, http.StatusOK, p)
}

func (h *Handler) pullDiff(c *gin.Context) {
	n, ok := pullNumber(c)
	if !ok || !h.inProject(c) {
		return
	}
	q := url.Values{"limit": {c.Query("limit")}, "cursor": {c.Query("cursor")}}
	h.page(c, "pull request diff", func() ([]byte, error) {
		return h.svc.PullDiff(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), n, q)
	})
}

func (h *Handler) timeline(c *gin.Context) {
	n, ok := pullNumber(c)
	if !ok || !h.inProject(c) {
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	items, next, err := h.svc.Timeline(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), n, c.Query("cursor"), limit)
	if err != nil {
		h.fail(c, "timeline", err)
		return
	}
	respond.JSONPage(c, http.StatusOK, items, next)
}

func (h *Handler) addComment(c *gin.Context) {
	n, ok := pullNumber(c)
	if !ok || !h.inProject(c) || !canWrite(c) {
		return
	}
	var in CommentInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, "a comment has 1 character to 64 KiB", gin.H{"code": "empty_comment"})
		return
	}
	it, err := h.svc.AddComment(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), n, middleware.UserIDFrom(c), in.Body)
	if err != nil {
		h.fail(c, "comment", err)
		return
	}
	respond.JSON(c, http.StatusCreated, it)
}

func (h *Handler) editComment(c *gin.Context) {
	n, ok := pullNumber(c)
	if !ok || !h.inProject(c) || !canWrite(c) {
		return
	}
	var in CommentInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, "a comment has 1 character to 64 KiB", gin.H{"code": "empty_comment"})
		return
	}
	it, err := h.svc.EditComment(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), n, c.Param("commentID"), middleware.UserIDFrom(c), in.Body)
	if err != nil {
		h.fail(c, "comment", err)
		return
	}
	respond.JSON(c, http.StatusOK, it)
}

func (h *Handler) deleteComment(c *gin.Context) {
	n, ok := pullNumber(c)
	if !ok || !h.inProject(c) || !canWrite(c) {
		return
	}
	err := h.svc.DeleteComment(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), n, c.Param("commentID"),
		middleware.UserIDFrom(c), middleware.MemberRoleFrom(c) == "owner")
	if err != nil {
		h.fail(c, "comment", err)
		return
	}
	respond.NoContent(c)
}

func (h *Handler) merge(c *gin.Context) {
	n, ok := pullNumber(c)
	if !ok || !h.inProject(c) || !canWrite(c) {
		return
	}
	// A person merges (D-102): never an API key, nor an agent (whose calls
	// carry the key mark too).
	if middleware.ViaAPIKey(c) {
		respond.ErrorData(c, http.StatusForbidden, "merging needs a signed-in session, not an API key or an agent", gin.H{"code": "session_required"})
		return
	}
	var in MergeInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.Error(c, http.StatusBadRequest, "method (merge, squash or fast_forward) and expected_head_sha (the head you saw) are required")
		return
	}
	p, err := h.svc.MergePull(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), n, middleware.UserIDFrom(c), in)
	if err != nil {
		h.log.Info("merge refused", zap.Error(err), zap.Int("number", n))
		h.fail(c, "merge", err)
		return
	}
	respond.JSON(c, http.StatusOK, p)
}
