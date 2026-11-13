package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// ownsP1 owns project p1 for org-1 and counts its checks.
type ownsP1 struct{ checks int }

func (o *ownsP1) CheckOwnership(_ context.Context, id, orgID string) (bool, error) {
	o.checks++
	return id == "p1" && orgID == "org-1", nil
}

func (o *ownsP1) IsWorkspaceService(context.Context, string) (bool, error) { return true, nil }

func router(role string, projects ProjectChecker) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) { c.Set("orgID", "org-1"); c.Set("memberRole", role); c.Next() })
	NewHandler(nil, projects, zap.NewNop()).RegisterRoutes(g)
	return r
}

func call(r *gin.Engine, method, path string) int {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w.Code
}

// The service is nil: a request that reached it would panic. An empty body
// stops a developer at binding (400), past the role and ownership checks;
// the workspace's own stack is the owners'.
func TestViewersCannotChangeObservabilitySettings(t *testing.T) {
	paths := []string{
		"/api/v1/observability/loki",
		"/api/v1/observability/grafana",
		"/api/v1/observability/prometheus",
		"/api/v1/projects/p1/observability/loki",
		"/api/v1/projects/p1/observability/grafana",
	}
	for role, want := range map[string]int{"viewer": http.StatusForbidden, "developer": http.StatusBadRequest} {
		projects := &ownsP1{}
		r := router(role, projects)
		for _, p := range paths {
			if role == "developer" && !strings.Contains(p, "/projects/") {
				assert.Equal(t, http.StatusForbidden, call(r, http.MethodPut, p), "the workspace's stack is the owners': PUT %s", p)
				continue
			}
			assert.Equal(t, want, call(r, http.MethodPut, p), "%s PUT %s", role, p)
		}
		if role == "viewer" {
			assert.Zero(t, projects.checks, "a viewer is refused before anything is looked up")
		}
	}
}

func TestProjectObservabilityIsForTheProjectsOwnWorkspace(t *testing.T) {
	projects := &ownsP1{}
	r := router("owner", projects)
	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/projects/p2/observability"},
		{http.MethodPut, "/api/v1/projects/p2/observability/loki"},
		{http.MethodPut, "/api/v1/projects/p2/observability/grafana"},
	} {
		assert.Equal(t, http.StatusNotFound, call(r, rt.method, rt.path), "%s %s: another workspace's project", rt.method, rt.path)
	}
	assert.Equal(t, 3, projects.checks)
}
