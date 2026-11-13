package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// workspaceRepo lists services per workspace.
type workspaceRepo struct {
	Repository
	byOrg map[string][]WorkspaceService
	asked string
}

func (r *workspaceRepo) ListWorkspaceServices(_ context.Context, orgID string) ([]WorkspaceService, error) {
	r.asked = orgID
	if s, ok := r.byOrg[orgID]; ok {
		return s, nil
	}
	return []WorkspaceService{}, nil
}

func getWorkspaceServices(t *testing.T, repo *workspaceRepo, orgID string) (int, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := NewHandler(NewServiceManager(repo, zap.NewNop()), nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) { c.Set("orgID", orgID); c.Set("memberRole", "viewer"); c.Next() })
	h.RegisterRoutes(g)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/workspace/services", nil))
	return w.Code, w.Body.String()
}

// The caller's own workspace only, any member role, and [] (not null) for a
// workspace without services.
func TestListWorkspaceServices(t *testing.T) {
	repo := &workspaceRepo{byOrg: map[string][]WorkspaceService{"org-a": {
		{ID: "s1", Name: "web", Scope: ScopeEnvironment, ProjectID: "p1", EnvironmentID: "e1", ServerID: "srv-1", Port: 80, RuntimeStatus: "running", SourceType: "docker-image", DockerImage: "nginx", GitBranch: "main", EnvironmentName: "production"},
		{ID: "s2", Name: "shared", Scope: ScopeOrg, RuntimeStatus: "idle", SourceType: "docker-image"},
	}}}

	code, body := getWorkspaceServices(t, repo, "org-a")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "org-a", repo.asked, "the active workspace, never a parameter")
	assert.JSONEq(t, `{"data":[
		{"id":"s1","name":"web","scope":"environment","project_id":"p1","environment_id":"e1","server_id":"srv-1","port":80,"runtime_status":"running",
		 "source_type":"docker-image","addon_slug":"","docker_image":"nginx","git_repo_url":"","git_branch":"main","environment_name":"production"},
		{"id":"s2","name":"shared","scope":"org","project_id":"","environment_id":"","server_id":"","port":0,"runtime_status":"idle",
		 "source_type":"docker-image","addon_slug":"","docker_image":"","git_repo_url":"","git_branch":"","environment_name":""}]}`, body)

	code, body = getWorkspaceServices(t, repo, "org-empty")
	assert.Equal(t, http.StatusOK, code)
	assert.JSONEq(t, `{"data":[]}`, body)
}

// An agent connected to some projects lists their services alone: neither
// another project's nor the workspace's own (D-100).
func TestListWorkspaceServicesNarrowsToTheAgentsProjects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &workspaceRepo{byOrg: map[string][]WorkspaceService{"org-a": {
		{ID: "s1", ProjectID: "p1"}, {ID: "s2", ProjectID: "p2"}, {ID: "s3", Scope: ScopeOrg},
	}}}
	h := NewHandler(NewServiceManager(repo, zap.NewNop()), nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("orgID", "org-a")
		c.Set("agent", middleware.AgentIdentity{ProjectIDs: []string{"p1"}})
		c.Next()
	})
	h.RegisterRoutes(g)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/workspace/services", nil))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"s1"`)
	assert.NotContains(t, w.Body.String(), `"s2"`)
	assert.NotContains(t, w.Body.String(), `"s3"`)
}
