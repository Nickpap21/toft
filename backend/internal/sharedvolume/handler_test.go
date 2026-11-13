package sharedvolume

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

func call(role, method, path, body string) int {
	gin.SetMode(gin.TestMode)
	s, _, _ := newSvc()
	_, _ = s.Create(context.Background(), "org-a", CreateInput{Name: "images", ServerID: "srv-1"})
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) { c.Set("orgID", "org-a"); c.Set("memberRole", role); c.Next() })
	NewHandler(s, zap.NewNop()).RegisterRoutes(g)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w.Code
}

// A volume can't be created on a server still waiting for its first agent
// (D-153): 409 server_not_ready, and nothing is stored.
func TestCreateOnWaitingServer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, repo, _ := newSvc()
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) { c.Set("orgID", "org-a"); c.Set("memberRole", "owner"); c.Next() })
	NewHandler(s, zap.NewNop()).RegisterRoutes(g)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/shared-volumes", strings.NewReader(`{"name":"more","server_id":"srv-w"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), `"code":"server_not_ready"`)
	assert.Empty(t, repo.vols)
}

// Owners manage volumes; developers mount them; viewers only read.
func TestRoles(t *testing.T) {
	create := `{"name":"more","server_id":"srv-1"}`
	assert.Equal(t, http.StatusCreated, call("owner", http.MethodPost, "/api/v1/shared-volumes", create))
	assert.Equal(t, http.StatusForbidden, call("developer", http.MethodPost, "/api/v1/shared-volumes", create))
	assert.Equal(t, http.StatusForbidden, call("developer", http.MethodDelete, "/api/v1/shared-volumes/vol-images", ""))
	assert.Equal(t, http.StatusForbidden, call("viewer", http.MethodPatch, "/api/v1/shared-volumes/vol-images", `{"size_limit_bytes":1}`))
	assert.Equal(t, http.StatusOK, call("viewer", http.MethodGet, "/api/v1/shared-volumes", ""))

	mount := `{"version":"v","mounts":[{"shared_volume_id":"vol-images","target":"/data"}]}`
	assert.Equal(t, http.StatusOK, call("developer", http.MethodPut, "/api/v1/services/svc-a/shared-volumes", mount))
	assert.Equal(t, http.StatusForbidden, call("viewer", http.MethodPut, "/api/v1/services/svc-a/shared-volumes", mount))
	assert.Equal(t, http.StatusNotFound, call("owner", http.MethodPut, "/api/v1/services/svc-b/shared-volumes", mount), "another workspace's service is not found")
	assert.Equal(t, http.StatusConflict, call("owner", http.MethodPut, "/api/v1/services/svc-compose/shared-volumes", mount))
	assert.Equal(t, http.StatusConflict, call("owner", http.MethodPost, "/api/v1/shared-volumes", `{"name":"images"}`), "name taken")
	assert.Equal(t, http.StatusBadRequest, call("owner", http.MethodPost, "/api/v1/shared-volumes", `{"name":"Bad Name"}`))
}
