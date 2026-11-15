package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// aliasRepo lists a second service of the environment, which owns the
// alias "web-production" (the default of a service named web in production).
type aliasRepo struct{ fakeRepo }

func (aliasRepo) ListByEnvironment(context.Context, string) ([]Service, error) {
	return []Service{{ID: "other", Name: "web", NetworkAlias: "web-production"}}, nil
}

// A network alias another service of the environment has is a conflict the
// client can fix: 409 alias_taken, on create and on a settings save, with
// the readable message; nothing is saved.
func TestHandler_AliasTakenIs409(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &aliasRepo{fakeRepo{svc: &Service{
		ID: "svc-1", EnvironmentID: "env-a", Name: "api", SourceType: SourceTypeDockerImage, DockerImage: "nginx", Port: 80,
	}}}
	h := NewHandler(NewServiceManager(repo, zap.NewNop()), nil, orgEnvs{}, &auditLog{}, nil, zap.NewNop())
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) { c.Set("orgID", "org-a"); c.Set("memberRole", "developer"); c.Next() })
	h.RegisterRoutes(g)
	do := func(method, path, body string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var out map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out), w.Body.String())
		return w.Code, out
	}

	code, body := do(http.MethodPost, "/api/v1/environments/env-a/services",
		`{"name":"web","type":"web","source_type":"docker-image","docker_image":"nginx:alpine","port":80}`)
	require.Equal(t, http.StatusConflict, code, body)
	assert.Equal(t, "alias_taken", body["code"])
	assert.Contains(t, body["error"], `"web"`)
	assert.Zero(t, repo.created)

	code, body = do(http.MethodPut, "/api/v1/services/svc-1/config", `{"network_alias":"web-production"}`)
	require.Equal(t, http.StatusConflict, code, body)
	assert.Equal(t, "alias_taken", body["code"])
	assert.Zero(t, repo.updated)
}
