package deploypolicy_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/deploypolicy"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// newRouter mounts the handler behind a stand-in for middleware.AuthWithOrg
// that sets the literal context keys the real middleware uses.
func newRouter(svc *deploypolicy.Service, userID, orgID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("userID", userID)
		c.Set("orgID", orgID)
		c.Set("reauthenticated", true)
		c.Next()
	})
	deploypolicy.NewHandler(svc, zap.NewNop()).RegisterRoutes(g)
	return r
}

func call(t *testing.T, r *gin.Engine, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	out := map[string]any{}
	if w.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out), w.Body.String())
	}
	return w.Code, out
}

func handlerService() (*deploypolicy.Service, *fakeRepo) {
	repo := newFakeRepo()
	repo.services["s1"] = &deploypolicy.StoredService{ID: "s1", Name: "web", SourceType: "git"}
	svc := deploypolicy.NewService(repo, deploypolicy.ModeEnforce, zap.NewNop()).
		WithOperatorChecker(fakeOperators{"op": true}).
		WithOwnershipChecker(fakeOwnership{"s1": "org-a"})
	return svc, repo
}

func TestHandler_GrantRequiresOperator(t *testing.T) {
	svc, repo := handlerService()
	body := map[string]any{"allowed_rules": []string{"docker_socket"}, "reason": "monitoring agent", "values_digest": digestOf(t, svc, "s1"), "version": "v"}

	code, resp := call(t, newRouter(svc, "owner", "org-a"), http.MethodPost, "/api/v1/services/s1/deploy-policy/exception", body)
	assert.Equal(t, http.StatusForbidden, code)
	assert.Contains(t, resp["error"], "instance operators")
	assert.Empty(t, repo.grants)

	code, resp = call(t, newRouter(svc, "op", "org-z"), http.MethodPost, "/api/v1/services/s1/deploy-policy/exception", body)
	require.Equal(t, http.StatusOK, code, resp)
	data := resp["data"].(map[string]any)
	assert.Equal(t, []any{"docker_socket"}, data["allowed_rules"])
}

// An operator without a recent password confirmation is asked for one
// (D-53); a non-operator is refused as such first.
func TestHandler_GrantAsksForThePassword(t *testing.T) {
	svc, repo := handlerService()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) { c.Set("userID", "op"); c.Set("orgID", "org-a"); c.Next() })
	deploypolicy.NewHandler(svc, zap.NewNop()).RegisterRoutes(g)
	body := map[string]any{"allowed_rules": []string{"docker_socket"}, "reason": "monitoring agent"}

	code, resp := call(t, r, http.MethodPost, "/api/v1/services/s1/deploy-policy/exception", body)
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "reauth_required", resp["data"].(map[string]any)["code"])
	assert.Empty(t, repo.grants)
	code, resp = call(t, r, http.MethodDelete, "/api/v1/services/s1/deploy-policy/exception", nil)
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "reauth_required", resp["data"].(map[string]any)["code"])
}

func TestHandler_GrantRejectsNonGrantableRules(t *testing.T) {
	svc, _ := handlerService()
	code, resp := call(t, newRouter(svc, "op", "org-a"), http.MethodPost, "/api/v1/services/s1/deploy-policy/exception",
		map[string]any{"allowed_rules": []string{"reserved_label"}, "reason": "x", "values_digest": "x", "version": "v"})
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, resp["error"], "can never be granted")
}

// A grant of values that changed since the operator opened the dialog
// is 409 values_changed, so the screen reloads them.
func TestHandler_GrantRefusesChangedValues(t *testing.T) {
	svc, repo := handlerService()
	code, resp := call(t, newRouter(svc, "op", "org-a"), http.MethodPost, "/api/v1/services/s1/deploy-policy/exception",
		map[string]any{"allowed_rules": []string{"cap_add"}, "reason": "x", "values_digest": "stale", "version": "v"})
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "values_changed", resp["data"].(map[string]any)["code"])
	assert.Empty(t, repo.grants)
}

func TestHandler_Revoke(t *testing.T) {
	svc, repo := handlerService()
	repo.exceptions["s1"] = &deploypolicy.Exception{ServiceID: "s1", AllowedRules: []composespec.Rule{composespec.RuleCapAdd}}

	code, _ := call(t, newRouter(svc, "owner", "org-a"), http.MethodDelete, "/api/v1/services/s1/deploy-policy/exception", nil)
	assert.Equal(t, http.StatusForbidden, code)

	code, _ = call(t, newRouter(svc, "op", "org-a"), http.MethodDelete, "/api/v1/services/s1/deploy-policy/exception",
		map[string]string{"reason": "done"})
	assert.Equal(t, http.StatusNoContent, code)

	code, _ = call(t, newRouter(svc, "op", "org-a"), http.MethodDelete, "/api/v1/services/s1/deploy-policy/exception", nil)
	assert.Equal(t, http.StatusNotFound, code)
}

func TestHandler_GetServicePolicy(t *testing.T) {
	svc, _ := handlerService()
	code, _ := call(t, newRouter(svc, "stranger", "org-b"), http.MethodGet, "/api/v1/services/s1/deploy-policy", nil)
	assert.Equal(t, http.StatusNotFound, code)

	code, resp := call(t, newRouter(svc, "member", "org-a"), http.MethodGet, "/api/v1/services/s1/deploy-policy", nil)
	require.Equal(t, http.StatusOK, code)
	data := resp["data"].(map[string]any)
	assert.Equal(t, "enforce", data["mode"])
	assert.Equal(t, false, data["can_manage_exceptions"])
	assert.NotNil(t, data["violations"])
}

func TestHandler_ReportIsOperatorOnly(t *testing.T) {
	svc, _ := handlerService()
	code, _ := call(t, newRouter(svc, "owner", "org-a"), http.MethodGet, "/api/v1/deploy-policy/violations", nil)
	assert.Equal(t, http.StatusForbidden, code)

	code, resp := call(t, newRouter(svc, "op", ""), http.MethodGet, "/api/v1/deploy-policy/violations", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "enforce", resp["data"].(map[string]any)["mode"])
}
