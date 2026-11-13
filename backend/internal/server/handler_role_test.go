package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// roleRouterAs mounts the server routes for a user with a role in a workspace.
func roleRouterAs(s *Service, audit *fakeAudit, role, orgID, userID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("userID", userID)
		c.Set("orgID", orgID)
		c.Set("memberRole", role)
		c.Next()
	})
	NewHandler(s, audit, zap.NewNop()).RegisterRoutes(g)
	return r
}

func roleRouter(s *Service, audit *fakeAudit, role string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("userID", "u1")
		c.Set("orgID", "org-a")
		c.Set("memberRole", role)
		c.Next()
	})
	NewHandler(s, audit, zap.NewNop()).RegisterRoutes(g)
	return r
}

func TestViewersCannotChangeServers(t *testing.T) {
	const local = "srv-local"
	setup := func() (*fakeRepo, *fakeAudit) {
		return newFakeRepo(
			&Server{ID: testServerID, OrgID: "org-a", Name: "web-1", IPAddress: "203.0.113.7", Port: 22},
			&Server{ID: local, OrgID: "org-a", Name: "panel", IPAddress: "127.0.0.1", Port: 22},
		), &fakeAudit{}
	}
	call := func(r *gin.Engine, method, path string) int {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		return w.Code
	}

	t.Run("a viewer is refused every write", func(t *testing.T) {
		repo, audit := setup()
		r := roleRouter(NewService(repo, ""), audit, "viewer")
		for _, rt := range []struct{ method, path string }{
			{http.MethodPost, "/api/v1/servers"},
			{http.MethodDelete, "/api/v1/servers/" + testServerID},
			{http.MethodPost, "/api/v1/servers/" + testServerID + "/provision"},
			{http.MethodPost, "/api/v1/servers/" + testServerID + "/discover-capacity"},
		} {
			assert.Equal(t, http.StatusForbidden, call(r, rt.method, rt.path), "%s %s", rt.method, rt.path)
		}
		assert.Len(t, repo.servers, 2, "nothing was deleted")
		assert.Empty(t, repo.statuses, "nothing was provisioned or probed")
		assert.Empty(t, audit.entries)
	})

	t.Run("a developer gets past the role check", func(t *testing.T) {
		repo, audit := setup()
		r := roleRouter(NewService(repo, ""), audit, "developer")
		assert.Equal(t, http.StatusForbidden, call(r, http.MethodPost, "/api/v1/servers"), "adding a server is the owners'")
		assert.Equal(t, http.StatusBadRequest, call(r, http.MethodPost, "/api/v1/servers/"+local+"/provision"), "stopped at the local-server check")
		assert.Equal(t, http.StatusNoContent, call(r, http.MethodDelete, "/api/v1/servers/"+testServerID))
		_, still := repo.servers[testServerID]
		assert.False(t, still)
		require.Len(t, audit.entries, 1)
		assert.Equal(t, "server.deleted", audit.entries[0].action)
	})
}

// A first join is the owners' and instance operators' (D-128), whatever
// their workspace role; a developer or viewer who isn't an operator gets 403.
// Past the check the fake's empty panel key fails the join command: the
// role is what's under test.
func TestFirstJoinTokenIsForOwnersAndOperators(t *testing.T) {
	for _, tc := range []struct {
		role, user string
		allowed    bool
	}{
		{"owner", "u1", true},
		{"developer", "op", true},
		{"viewer", "op", true},
		{"developer", "u1", false},
		{"viewer", "u1", false},
	} {
		svc, _, _ := agentService(t)
		svc.WithOperatorChecker(fakeOperators{"op": true})
		r := roleRouterAs(svc, &fakeAudit{}, tc.role, "o1", tc.user)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/servers/s1/agent/join-token", nil))
		assert.Equal(t, tc.allowed, w.Code != http.StatusForbidden, "%s %s: %d %s", tc.role, tc.user, w.Code, w.Body.String())
	}
}
