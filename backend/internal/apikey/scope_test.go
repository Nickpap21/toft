package apikey

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

type keysForTest map[string]middleware.APIKeyIdentity

func (k keysForTest) ValidateAPIKey(_ context.Context, raw string) (middleware.APIKeyIdentity, error) {
	if id, ok := k[raw]; ok {
		return id, nil
	}
	return middleware.APIKeyIdentity{}, errors.New("invalid")
}

type orgsForTest struct{}

func (orgsForTest) GetForUser(context.Context, string) (string, string, error) {
	return "org-1", "owner", nil
}
func (orgsForTest) RoleIn(context.Context, string, string) (string, error) { return "developer", nil }

// projectsForTest: svc-a and env-a are in p-1, svc-b and env-b in p-2, and
// svc-shared is workspace-shared (no project).
type projectsForTest struct{}

func (projectsForTest) ServiceProject(_ context.Context, id string) (string, error) {
	return map[string]string{"svc-a": "p-1", "svc-a2": "p-1", "svc-b": "p-2", "svc-shared": ""}[id], nil
}
func (projectsForTest) EnvironmentProject(_ context.Context, id string) (string, error) {
	return map[string]string{"env-a": "p-1", "env-b": "p-2"}[id], nil
}

func scopedRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	keys := keysForTest{
		"paas_deploy": {UserID: "u-1", ProjectID: "p-1", OrgID: "org-1", Scopes: []string{ScopeDeploy}},
		"paas_vars":   {UserID: "u-1", ProjectID: "p-1", OrgID: "org-1", Scopes: []string{ScopeVariables}},
		"paas_read":   {UserID: "u-1", ProjectID: "p-1", OrgID: "org-1", Scopes: []string{ScopeCodeRead}},
		"paas_write":  {UserID: "u-1", ProjectID: "p-1", OrgID: "org-1", Scopes: []string{ScopeCodeWrite}},
		"paas_checks": {UserID: "u-1", ProjectID: "p-1", OrgID: "org-1", Scopes: []string{ScopeChecks}},
		"paas_full":   {UserID: "u-1"},
		// K5: limited to svc-a; and to a service since deleted.
		"paas_svc":  {UserID: "u-1", ProjectID: "p-1", OrgID: "org-1", Scopes: []string{ScopeDeploy, ScopeVariables}, ServiceIDs: []string{"svc-a"}},
		"paas_gone": {UserID: "u-1", ProjectID: "p-1", OrgID: "org-1", Scopes: []string{ScopeDeploy, ScopeVariables}, ServiceIDs: []string{"svc-deleted"}},
	}
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(middleware.AuthWithOrg("secret", keys, orgsForTest{}, nil))
	v1.Use(EnforceScopes(projectsForTest{}))
	ok := func(c *gin.Context) { c.Status(http.StatusOK) }
	for route := range routeScopes {
		method, path, _ := strings.Cut(route, " ")
		v1.Handle(method, strings.TrimPrefix(path, "/api/v1"), ok)
	}
	// Routes no scoped key may call.
	v1.GET("/servers", ok)
	v1.GET("/workspace/services", ok)
	v1.DELETE("/projects/:projectID", ok)
	v1.PUT("/services/:serviceID", ok)
	v1.POST("/services/:serviceID/exec", ok)
	v1.GET("/environments/:environmentID", ok)
	v1.POST("/projects/:projectID/repositories/:repositoryID/pulls/:pullNumber/merge", ok)
	v1.PATCH("/projects/:projectID/repositories/:repositoryID", ok)
	v1.POST("/projects/:projectID/repositories", ok)
	return r
}

func call(r *gin.Engine, key, method, path string) int {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

func TestScopedKeys(t *testing.T) {
	r := scopedRouter()
	cases := []struct {
		key, method, path string
		want              int
	}{
		// deploy: its own project's services only
		{"paas_deploy", "POST", "/api/v1/services/svc-a/deployments", 200},
		{"paas_deploy", "GET", "/api/v1/services/svc-a/deployments/d-1/logs/stream", 200},
		{"paas_deploy", "POST", "/api/v1/services/svc-a/deployments/d-1/rollback", 200},
		{"paas_deploy", "GET", "/api/v1/services/svc-a", 200},
		{"paas_deploy", "GET", "/api/v1/services/svc-a/domains", 200},
		{"paas_deploy", "GET", "/api/v1/services/svc-b/domains", 403},           // another project
		{"paas_deploy", "POST", "/api/v1/services/svc-b/deployments", 403},      // another project
		{"paas_deploy", "POST", "/api/v1/services/svc-shared/deployments", 403}, // workspace-shared
		{"paas_deploy", "POST", "/api/v1/services/svc-nope/deployments", 403},   // unknown service
		{"paas_deploy", "PUT", "/api/v1/services/svc-a/env-vars", 403},          // lacks variables
		{"paas_deploy", "PUT", "/api/v1/services/svc-a", 403},                   // not listed
		{"paas_deploy", "POST", "/api/v1/services/svc-a/exec", 403},
		{"paas_deploy", "GET", "/api/v1/servers", 403},
		{"paas_deploy", "GET", "/api/v1/workspace/services", 403}, // the whole workspace
		{"paas_deploy", "DELETE", "/api/v1/projects/p-1", 403},
		// variables: services and environments of its project
		{"paas_vars", "PUT", "/api/v1/services/svc-a/env-vars", 200},
		{"paas_vars", "POST", "/api/v1/environments/env-a/secrets", 200},
		{"paas_vars", "DELETE", "/api/v1/environments/env-a/env-vars/A", 200},
		{"paas_vars", "PUT", "/api/v1/environments/env-b/env-vars", 403},
		{"paas_vars", "GET", "/api/v1/environments/env-a", 403},
		{"paas_vars", "POST", "/api/v1/services/svc-a/deployments", 403}, // lacks deploy
		// code: its own project's repositories, read-only; code_write reads too
		{"paas_read", "GET", "/api/v1/projects/p-1/repositories/r-1/tree", 200},
		{"paas_read", "GET", "/api/v1/projects/p-1/repositories/r-1/commits/abcd/diff", 200},
		{"paas_write", "GET", "/api/v1/projects/p-1/repositories/r-1/raw", 200},
		{"paas_write", "GET", "/api/v1/projects/p-1/repositories", 200},
		{"paas_read", "GET", "/api/v1/projects/p-2/repositories/r-2/tree", 403}, // another project
		{"paas_read", "DELETE", "/api/v1/projects/p-1", 403},
		{"paas_read", "GET", "/api/v1/services/svc-a", 403},
		{"paas_deploy", "GET", "/api/v1/projects/p-1/repositories/r-1/tree", 403}, // lacks code
		// pull requests: code_write opens and comments; nobody's key merges
		{"paas_read", "GET", "/api/v1/projects/p-1/repositories/r-1/pulls/3/timeline", 200},
		{"paas_read", "POST", "/api/v1/projects/p-1/repositories/r-1/pulls", 403},
		{"paas_write", "POST", "/api/v1/projects/p-1/repositories/r-1/pulls", 200},
		{"paas_write", "POST", "/api/v1/projects/p-1/repositories/r-1/pulls/3/comments", 200},
		{"paas_write", "POST", "/api/v1/projects/p-1/repositories/r-1/pulls/3/merge", 403},
		{"paas_write", "PATCH", "/api/v1/projects/p-1/repositories/r-1", 403},
		{"paas_write", "POST", "/api/v1/projects/p-1/repositories", 403},
		{"paas_write", "POST", "/api/v1/projects/p-2/repositories/r-2/pulls", 403}, // another project
		// checks (D-77): a CI key reads and reports a commit's checks, and
		// nothing else; code keys read them but don't report
		{"paas_checks", "POST", "/api/v1/projects/p-1/repositories/r-1/commits/abcd/checks", 200},
		{"paas_checks", "GET", "/api/v1/projects/p-1/repositories/r-1/commits/abcd/checks", 200},
		{"paas_checks", "POST", "/api/v1/projects/p-2/repositories/r-2/commits/abcd/checks", 403}, // another project
		{"paas_checks", "GET", "/api/v1/projects/p-1/repositories/r-1/tree", 403},
		{"paas_checks", "GET", "/api/v1/projects/p-1/repositories/r-1/pipelines", 403},
		{"paas_checks", "POST", "/api/v1/services/svc-a/deployments", 403},
		{"paas_read", "GET", "/api/v1/projects/p-1/repositories/r-1/commits/abcd/checks", 200},
		{"paas_read", "GET", "/api/v1/projects/p-1/repositories/r-1/pipelines", 200},
		{"paas_read", "POST", "/api/v1/projects/p-1/repositories/r-1/commits/abcd/checks", 403},
		{"paas_write", "POST", "/api/v1/projects/p-1/repositories/r-1/commits/abcd/checks", 403},
		{"paas_deploy", "GET", "/api/v1/projects/p-1/repositories/r-1/commits/abcd/checks", 403},
		// a key limited to services: those services, and nothing else in
		// its project
		{"paas_svc", "POST", "/api/v1/services/svc-a/deployments", 200},
		{"paas_svc", "PUT", "/api/v1/services/svc-a/env-vars", 200},
		{"paas_svc", "PUT", "/api/v1/services/svc-a/secrets/A", 200},
		{"paas_svc", "POST", "/api/v1/services/svc-a2/deployments", 403},                   // another service of its project
		{"paas_svc", "GET", "/api/v1/services/svc-a2/env-vars", 403},                       // its variables
		{"paas_svc", "PUT", "/api/v1/environments/env-a/env-vars", 403},                    // shared with other services
		{"paas_svc", "POST", "/api/v1/projects/p-1/repositories/r-1/pulls/3/preview", 403}, // a preview
		{"paas_svc", "POST", "/api/v1/services/svc-b/deployments", 403},                    // another project
		{"paas_svc", "PUT", "/api/v1/services/svc-a", 403},                                 // not listed
		{"paas_gone", "POST", "/api/v1/services/svc-a/deployments", 403},                   // its services were deleted
		{"paas_gone", "GET", "/api/v1/services/svc-deleted", 403},
		// full-access keys are unchanged
		{"paas_full", "GET", "/api/v1/servers", 200},
		{"paas_full", "POST", "/api/v1/services/svc-b/deployments", 200},
		{"paas_full", "DELETE", "/api/v1/projects/p-2", 200},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, call(r, tc.key, tc.method, tc.path), "%s %s %s", tc.key, tc.method, tc.path)
	}
}

// A project key's refusal carries the code the contract promises, as an
// agent's does: in the problem's code and in data.code.
func TestScopedKeyRefusalsCarryACode(t *testing.T) {
	r := scopedRouter()
	for _, tc := range []struct{ key, method, path string }{
		{"paas_deploy", "PUT", "/api/v1/services/svc-a"},                              // not listed
		{"paas_deploy", "GET", "/api/v1/services/svc-b/domains"},                      // another project
		{"paas_svc", "POST", "/api/v1/services/svc-a2/deployments"},                   // another service of its project
		{"paas_write", "POST", "/api/v1/projects/p-1/repositories/r-1/pulls/3/merge"}, // nobody's key merges
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+tc.key)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var body struct {
			Code string
			Data struct{ Code string }
		}
		assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Equal(t, http.StatusForbidden, w.Code, "%s %s", tc.method, tc.path)
		assert.Equal(t, "insufficient_scope", body.Code, "%s %s", tc.method, tc.path)
		assert.Equal(t, "insufficient_scope", body.Data.Code, "%s %s", tc.method, tc.path)
	}
}

// Every listed route names the service, environment or project
// EnforceScopes checks against the key's project. A route without one would
// skip that check.
func TestEveryScopedRouteNamesItsOwner(t *testing.T) {
	for route := range routeScopes {
		_, path, _ := strings.Cut(route, " ")
		assert.True(t, strings.Contains(route, "/:serviceID") || strings.Contains(route, "/:environmentID") ||
			strings.HasPrefix(path, "/api/v1/projects/:projectID/"), route)
	}
}

func TestValidateScope(t *testing.T) {
	for _, tc := range []struct {
		project string
		scopes  []string
		ok      bool
	}{
		{"", nil, true},
		{"p-1", []string{"deploy"}, true},
		{"p-1", []string{"deploy", "variables"}, true},
		{"p-1", nil, false},
		{"", []string{"deploy"}, false},
		{"p-1", []string{"admin"}, false},
		{"p-1", []string{"deploy", "deploy"}, false},
	} {
		err := validateScope(tc.project, tc.scopes)
		assert.Equal(t, tc.ok, err == nil, "%q %v", tc.project, tc.scopes)
	}
}

// expires_at left out, null and a date differ (K4).
func TestCreateInputExpiry(t *testing.T) {
	var in CreateInput
	assert.NoError(t, json.Unmarshal([]byte(`{"name": "ci"}`), &in))
	assert.False(t, in.ExpiresAt.Set)
	in = CreateInput{}
	assert.NoError(t, json.Unmarshal([]byte(`{"name": "ci", "expires_at": null}`), &in))
	assert.True(t, in.ExpiresAt.Set)
	assert.Nil(t, in.ExpiresAt.Time)
	in = CreateInput{}
	assert.NoError(t, json.Unmarshal([]byte(`{"name": "ci", "expires_at": "2030-01-02T03:04:05Z"}`), &in))
	assert.True(t, in.ExpiresAt.Set)
	assert.Equal(t, "2030-01-02T03:04:05Z", in.ExpiresAt.Time.Format(time.RFC3339))
	assert.ErrorIs(t, json.Unmarshal([]byte(`{"name": "ci", "expires_at": "soon"}`), &in), ErrInvalidExpiry)
}
