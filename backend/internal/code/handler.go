package code

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"path"

	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Handler is the code module's JSON API.
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

// AuditWriter records an action in a workspace's audit log. Satisfied by
// *org.Service.
type AuditWriter interface {
	WriteAuditLogDetail(ctx context.Context, orgID, actorID, action, resource string, detail map[string]any)
}

func NewHandler(svc *Service, projects ProjectOwnershipChecker, audit AuditWriter, log *zap.Logger) *Handler {
	return &Handler{svc: svc, projects: projects, audit: audit, log: log}
}

func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/git", h.getInfo)
	rg.POST("/markdown", h.renderMarkdown)
	rg.GET("/projects/:projectID/repositories", h.listRepositories)
	keys := rg.Group("/user/ssh-keys")
	keys.GET("", h.listKeys)
	keys.POST("", h.addKey)
	keys.DELETE("/:keyID", h.deleteKey)

	// The repository browser (D-73): read-only, every member of the
	// workspace; project keys with code_read or code_write (apikey).
	rg.POST("/projects/:projectID/repositories", h.createRepository)
	repo := rg.Group("/projects/:projectID/repositories/:repositoryID")
	repo.GET("", h.getRepository)
	repo.PATCH("", h.updateRepository)
	repo.DELETE("", h.deleteRepository)
	repo.GET("/branches", h.browse("branches", true, "limit", "cursor"))
	repo.POST("/branches", h.createBranch)
	repo.GET("/tags", h.browse("tags", true, "limit", "cursor"))
	repo.GET("/tree", h.browse("tree", false, "ref", "path"))
	repo.GET("/blob", h.browse("blob", false, "ref", "path"))
	repo.GET("/raw", h.raw)
	repo.GET("/commits", h.browse("commits", true, "ref", "path", "limit", "cursor"))
	repo.GET("/commits/:sha", h.browse("commit", false))
	repo.GET("/commits/:sha/diff", h.browse("diff", true, "limit", "cursor"))

	// Pull requests (D-75): reads for every member, writes for owners and
	// developers (and code_write keys, which may open and comment only).
	repo.GET("/compare", h.compare)
	repo.GET("/compare/diff", h.compareDiff)
	repo.GET("/pulls", h.listPulls)
	repo.POST("/pulls", h.openPull)
	pull := repo.Group("/pulls/:pullNumber")
	pull.GET("", h.getPull)
	pull.PATCH("", h.updatePull)
	pull.GET("/diff", h.pullDiff)
	pull.GET("/timeline", h.timeline)
	pull.POST("/comments", h.addComment)
	pull.PATCH("/comments/:commentID", h.editComment)
	pull.DELETE("/comments/:commentID", h.deleteComment)
	pull.POST("/merge", h.merge)

	h.registerChecks(repo)
	h.registerMirrors(repo)
}

func gitOff(c *gin.Context) {
	respond.ErrorData(c, http.StatusNotFound, ErrGitOff.Error(), gin.H{"code": "git_off"})
}

func (h *Handler) getInfo(c *gin.Context) {
	respond.JSON(c, http.StatusOK, h.svc.Info(c.Request.Host))
}

func (h *Handler) listRepositories(c *gin.Context) {
	projectID := c.Param("projectID")
	if owned, err := h.projects.CheckOwnership(c.Request.Context(), projectID, middleware.OrgIDFrom(c)); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "project not found")
		return
	}
	repos, err := h.svc.ProjectRepositories(c.Request.Context(), projectID, c.Request.Host)
	switch {
	case errors.Is(err, ErrGitOff):
		gitOff(c)
	case err != nil:
		h.log.Error("list repositories", zap.Error(err), zap.String("project_id", projectID))
		respond.Error(c, http.StatusInternalServerError, "failed to list repositories")
	default:
		respond.JSON(c, http.StatusOK, repos)
	}
}

func (h *Handler) listKeys(c *gin.Context) {
	keys, err := h.svc.ListKeys(c.Request.Context(), middleware.UserIDFrom(c))
	switch {
	case errors.Is(err, ErrGitOff):
		gitOff(c)
	case err != nil:
		h.log.Error("list ssh keys", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to list SSH keys")
	default:
		respond.JSON(c, http.StatusOK, keys)
	}
}

// addKey asks for the password first (D-71): a key outlives the session
// that added it, so a stolen session must not be able to leave one behind.
func (h *Handler) addKey(c *gin.Context) {
	if !h.svc.Enabled() {
		gitOff(c)
		return
	}
	if !middleware.RequireReauth(c) {
		return
	}
	var in AddSSHKeyInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.Error(c, http.StatusBadRequest, "title (1–100 characters) and public_key are required")
		return
	}
	key, err := h.svc.AddKey(c.Request.Context(), middleware.UserIDFrom(c), in)
	switch {
	case errors.Is(err, ErrInvalidKey):
		respond.ErrorData(c, http.StatusBadRequest, err.Error(), gin.H{"code": "invalid_ssh_key"})
	case errors.Is(err, ErrWeakKey):
		respond.ErrorData(c, http.StatusBadRequest, err.Error(), gin.H{"code": "weak_ssh_key"})
	case errors.Is(err, ErrKeyInUse):
		respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "ssh_key_in_use"})
	case errors.Is(err, ErrTooManyKeys):
		respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "too_many_ssh_keys"})
	case err != nil:
		h.log.Error("add ssh key", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to add the SSH key")
	default:
		h.record(c, "ssh_key.added", key)
		respond.JSON(c, http.StatusCreated, key)
	}
}

func (h *Handler) deleteKey(c *gin.Context) {
	key, err := h.svc.DeleteKey(c.Request.Context(), middleware.UserIDFrom(c), c.Param("keyID"))
	switch {
	case errors.Is(err, ErrGitOff):
		gitOff(c)
	case errors.Is(err, ErrNotFound):
		respond.Error(c, http.StatusNotFound, "SSH key not found")
	case err != nil:
		h.log.Error("delete ssh key", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to remove the SSH key")
	default:
		h.record(c, "ssh_key.removed", key)
		respond.NoContent(c)
	}
}

// record writes a key's event to the open workspace's audit log, as API
// keys do: the account was working there when it added or removed it.
func (h *Handler) record(c *gin.Context, action string, k *SSHKey) {
	if h.audit == nil {
		return
	}
	h.audit.WriteAuditLogDetail(c.Request.Context(), middleware.OrgIDFrom(c), middleware.UserIDFrom(c), action, "ssh_key:"+k.ID,
		map[string]any{"title": k.Title, "fingerprint": k.Fingerprint, "key_type": k.KeyType})
}

// inProject answers 404 unless the route's project is in the caller's
// workspace.
func (h *Handler) inProject(c *gin.Context) bool {
	owned, err := h.projects.CheckOwnership(c.Request.Context(), c.Param("projectID"), middleware.OrgIDFrom(c))
	if err != nil || !owned {
		respond.Error(c, http.StatusNotFound, "project not found")
		return false
	}
	return true
}

func (h *Handler) getRepository(c *gin.Context) {
	if !h.inProject(c) {
		return
	}
	d, err := h.svc.Repository(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), c.Request.Host)
	if err != nil {
		h.browseError(c, "repository", err)
		return
	}
	respond.JSON(c, http.StatusOK, d)
}

// browse serves one of the executor's reads with the query parameters
// named (and the route's :sha); a paged read answers its items and next.
func (h *Handler) browse(op string, paged bool, params ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !h.inProject(c) {
			return
		}
		q := url.Values{}
		for _, p := range params {
			if v, ok := c.GetQuery(p); ok {
				q.Set(p, v)
			}
		}
		if sha := c.Param("sha"); sha != "" {
			q.Set("sha", sha)
		}
		out, err := h.svc.Browse(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), op, q)
		if err != nil {
			h.browseError(c, op, err)
			return
		}
		if !paged {
			respond.JSON(c, http.StatusOK, out)
			return
		}
		var pg struct {
			Items json.RawMessage `json:"items"`
			Next  string          `json:"next"`
		}
		if err := json.Unmarshal(out, &pg); err != nil {
			h.browseError(c, op, err)
			return
		}
		respond.JSONPage(c, http.StatusOK, pg.Items, pg.Next)
	}
}

// raw serves a file's bytes so that no browser runs them (D-71, D-73): a
// sandbox CSP, nosniff and same-origin CORP always; raster images as
// themselves, other text as text/plain (SVG and HTML included), anything
// else as an octet-stream attachment.
func (h *Handler) raw(c *gin.Context) {
	if !h.inProject(c) {
		return
	}
	p := c.Query("path")
	body, size, err := h.svc.Raw(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), url.Values{"ref": {c.Query("ref")}, "path": {p}})
	if err != nil {
		h.browseError(c, "raw", err)
		return
	}
	defer body.Close()
	br := bufio.NewReaderSize(body, 8192)
	head, _ := br.Peek(8000)
	ctype, inline := rawType(head)
	disposition := "attachment"
	if inline {
		disposition = "inline"
	}
	c.Header("Content-Security-Policy", "sandbox; default-src 'none'")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cross-Origin-Resource-Policy", "same-origin")
	c.Header("Cache-Control", "private, no-cache")
	c.DataFromReader(http.StatusOK, size, ctype, br, map[string]string{
		"Content-Disposition": mime.FormatMediaType(disposition, map[string]string{"filename": path.Base(p)}),
	})
}

// rawType is the type a file starting with head is served as, and whether
// a browser may show it in place.
func rawType(head []byte) (string, bool) {
	switch ct := http.DetectContentType(head); ct {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return ct, true
	}
	if bytes.IndexByte(head, 0) < 0 {
		return "text/plain; charset=utf-8", true
	}
	return "application/octet-stream", false
}

// browseError answers a read's error: the executor's own codes for what the
// client asked wrong, 503 git_busy with Retry-After when git is full,
// 503 git_unavailable when it isn't listening.
func (h *Handler) browseError(c *gin.Context, op string, err error) {
	var be *gitstore.BrowseError
	switch {
	case errors.As(err, &be):
		respond.ErrorData(c, be.Status, be.Msg, gin.H{"code": be.Code})
	case errors.Is(err, gitstore.ErrBusy):
		c.Header("Retry-After", "5")
		respond.ErrorData(c, http.StatusServiceUnavailable, err.Error(), gin.H{"code": "git_busy"})
	case errors.Is(err, gitstore.ErrUnavailable):
		h.log.Error("browse repository: git server unreachable", zap.Error(err), zap.String("op", op))
		respond.ErrorData(c, http.StatusServiceUnavailable, gitstore.ErrUnavailable.Error(), gin.H{"code": "git_unavailable"})
	case errors.Is(err, ErrGitOff):
		gitOff(c)
	case errors.Is(err, ErrNotFound):
		respond.Error(c, http.StatusNotFound, "repository not found")
	default:
		h.log.Error("browse repository", zap.Error(err), zap.String("op", op), zap.String("repository_id", c.Param("repositoryID")))
		respond.Error(c, http.StatusInternalServerError, "failed to read the repository")
	}
}

// renderMarkdown is a comment's or a description's preview, rendered as
// it will be shown (gitstore.Markdown).
func (h *Handler) renderMarkdown(c *gin.Context) {
	var in struct {
		Text      string `json:"text" binding:"max=65536"`
		ProjectID string `json:"project_id"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.Error(c, http.StatusBadRequest, "text has 64 KiB at most")
		return
	}
	// A project's previews link its issue keys, as its issues and pull
	// requests do (D-81).
	var refs gitstore.Refs
	if in.ProjectID != "" {
		if owned, err := h.projects.CheckOwnership(c.Request.Context(), in.ProjectID, middleware.OrgIDFrom(c)); err != nil || !owned {
			respond.Error(c, http.StatusNotFound, "project not found")
			return
		}
		refs = gitstore.Refs{Keys: h.svc.issueKeys(c.Request.Context(), in.ProjectID), Pulls: true, Mentions: h.svc.handles(c.Request.Context(), in.ProjectID)}
	}
	respond.JSON(c, http.StatusOK, gin.H{"html": gitstore.MarkdownRefs([]byte(in.Text), refs)})
}
