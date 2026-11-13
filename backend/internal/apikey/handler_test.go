package apikey

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/apispec"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const projectA = "11111111-1111-1111-1111-111111111111"

type orgAProjects struct{}

func (orgAProjects) CheckOwnership(_ context.Context, id, orgID string) (bool, error) {
	return id == projectA && orgID == "org-a", nil
}

// projectKeys answers ListByProject; nothing else is called.
type projectKeys struct {
	Repository
	owner string // the userID filter it was asked for
}

func (p *projectKeys) ListByProject(_ context.Context, _, userID string, _ time.Time, _ string, limit int) ([]ProjectKey, error) {
	p.owner = userID
	keys := []ProjectKey{
		{ID: "22222222-2222-2222-2222-222222222222", Name: "ci-deploy", Prefix: "paas_abc", Scopes: []string{"deploy"}, OwnerID: "u-2", OwnerName: "sam", CreatedAt: time.Now()},
		{ID: "33333333-3333-3333-3333-333333333333", Name: "claude-code", Prefix: "paas_def", Scopes: []string{"code_read"}, OwnerID: "u-1", OwnerName: "alex", CreatedAt: time.Now().Add(-time.Hour)},
	}
	if limit < len(keys) {
		keys = keys[:limit]
	}
	return keys, nil
}

// GET /projects/{id}/api-keys: another workspace's project is not found;
// a bad limit or cursor is refused; an owner's list is every member's and
// matches the spec; anyone else's is filtered to their own.
func TestListForProjectRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	call := func(role, org, project, query string) (int, map[string]any, *projectKeys) {
		repo := &projectKeys{}
		h := NewHandler(NewService(repo, zap.NewNop()), orgAProjects{}, zap.NewNop())
		r := gin.New()
		g := r.Group("/api/v1")
		g.Use(func(c *gin.Context) { c.Set("orgID", org); c.Set("memberRole", role); c.Set("userID", "u-1"); c.Next() })
		h.RegisterRoutes(g)
		path := "/api/v1/projects/" + project + "/api-keys"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path+query, nil))
		var out map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out), w.Body.String())
		apispec.Check(t, http.MethodGet, path, w.Code, out)
		return w.Code, out, repo
	}
	code, _, _ := call("owner", "org-b", projectA, "")
	assert.Equal(t, http.StatusNotFound, code, "another workspace's project")
	code, out, _ := call("owner", "org-a", projectA, "?limit=0")
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "bad_limit", out["data"].(map[string]any)["code"])
	code, out, _ = call("owner", "org-a", projectA, "?cursor=nope")
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "bad_cursor", out["data"].(map[string]any)["code"])

	code, out, repo := call("owner", "org-a", projectA, "?limit=1")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "", repo.owner, "an owner sees every member's")
	assert.Len(t, out["data"], 1)
	assert.NotEmpty(t, out["next"], "a page follows")

	_, _, repo = call("developer", "org-a", projectA, "")
	assert.Equal(t, "u-1", repo.owner, "a developer sees their own")
}
