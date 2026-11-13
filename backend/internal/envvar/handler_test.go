package envvar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/pkg/listversion"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// fakeVars records which scope ID each call reached and returns err.
type fakeVars struct {
	calls []string // "method id"
	err   error
}

func (f *fakeVars) rec(method, id string) { f.calls = append(f.calls, method+" "+id) }
func (f *fakeVars) ListVersioned(_ context.Context, id string) ([]EnvVar, string, error) {
	f.rec("List", id)
	return []EnvVar{}, "v1", f.err
}
func (f *fakeVars) Upsert(_ context.Context, id, _ string, _ UpsertInput) (*EnvVar, error) {
	f.rec("Upsert", id)
	return &EnvVar{}, f.err
}
func (f *fakeVars) BulkUpsert(_ context.Context, id, _, _ string, _ []UpsertInput) ([]EnvVar, string, error) {
	f.rec("BulkUpsert", id)
	return []EnvVar{}, "v2", f.err
}
func (f *fakeVars) Delete(_ context.Context, id, _ string) error { f.rec("Delete", id); return f.err }
func (f *fakeVars) ListSecrets(_ context.Context, id string) ([]SecretDTO, error) {
	f.rec("ListSecrets", id)
	return []SecretDTO{}, nil
}
func (f *fakeVars) CreateSecret(_ context.Context, id, _ string, _ CreateSecretInput) (*SecretDTO, error) {
	f.rec("CreateSecret", id)
	return &SecretDTO{}, f.err
}
func (f *fakeVars) ReplaceSecret(_ context.Context, id, _, _, _ string) error {
	f.rec("ReplaceSecret", id)
	return f.err
}
func (f *fakeVars) DeleteSecret(_ context.Context, id, _ string) error {
	f.rec("DeleteSecret", id)
	return f.err
}

// owns accepts exactly the IDs in it, for org "org-1".
type owns map[string]bool

func (o owns) CheckOwnership(_ context.Context, id, orgID string) (bool, error) {
	return orgID == "org-1" && o[id], nil
}

func newTestHandler(svcVars, envVars *fakeVars) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := &Handler{
		services:     scope{param: "serviceID", logKey: "service_id", owner: owns{"svc-1": true, "preview-copy": true}, vars: svcVars, log: zap.NewNop()},
		environments: scope{param: "environmentID", logKey: "environment_id", owner: owns{"env-1": true}, vars: envVars, log: zap.NewNop()},
	}
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("orgID", "org-1")
		c.Set("memberRole", c.GetHeader("X-Test-Role"))
		c.Next()
	})
	h.RegisterRoutes(g)
	return r
}

func do(r *gin.Engine, method, path, role, body string) int {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Role", role)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

// writes are every mutating route, relative to a scope's prefix.
var writes = []struct{ method, path, body string }{
	{http.MethodPost, "/env-vars", `{"key":"A","value":"1"}`},
	{http.MethodPut, "/env-vars", `{"vars":[{"key":"A","value":"1"}],"version":"v1"}`},
	{http.MethodDelete, "/env-vars/A", ""},
	{http.MethodPost, "/secrets", `{"key":"S","value":"x"}`},
	{http.MethodPut, "/secrets/S", `{"value":"y"}`},
	{http.MethodDelete, "/secrets/S", ""},
}

func TestEachScopeReachesItsOwnVariables(t *testing.T) {
	svcVars, envVars := &fakeVars{}, &fakeVars{}
	r := newTestHandler(svcVars, envVars)

	assert.Equal(t, http.StatusOK, do(r, http.MethodGet, "/api/v1/services/svc-1/env-vars", "developer", ""))
	assert.Equal(t, http.StatusOK, do(r, http.MethodGet, "/api/v1/environments/env-1/secrets", "developer", ""))
	assert.Equal(t, http.StatusOK, do(r, http.MethodPost, "/api/v1/environments/env-1/env-vars", "owner", `{"key":"A","value":"1"}`))

	assert.Equal(t, []string{"List svc-1"}, svcVars.calls)
	assert.Equal(t, []string{"ListSecrets env-1", "Upsert env-1"}, envVars.calls)
}

func TestAnotherOrgsEnvironmentIsNotFound(t *testing.T) {
	envVars := &fakeVars{}
	r := newTestHandler(&fakeVars{}, envVars)
	assert.Equal(t, http.StatusNotFound, do(r, http.MethodGet, "/api/v1/environments/env-2/env-vars", "owner", ""))
	assert.Equal(t, http.StatusNotFound, do(r, http.MethodPost, "/api/v1/environments/env-2/secrets", "owner", `{"key":"S","value":"x"}`))
	assert.Empty(t, envVars.calls)
}

func TestViewersReadButNeverWrite(t *testing.T) {
	for _, prefix := range []string{"/api/v1/services/svc-1", "/api/v1/environments/env-1"} {
		svcVars, envVars := &fakeVars{}, &fakeVars{}
		r := newTestHandler(svcVars, envVars)

		assert.Equal(t, http.StatusOK, do(r, http.MethodGet, prefix+"/env-vars", "viewer", ""), prefix)
		assert.Equal(t, http.StatusOK, do(r, http.MethodGet, prefix+"/secrets", "viewer", ""), prefix)
		for _, w := range writes {
			assert.Equal(t, http.StatusForbidden, do(r, w.method, prefix+w.path, "viewer", w.body), "%s %s%s", w.method, prefix, w.path)
		}
		assert.Len(t, append(svcVars.calls, envVars.calls...), 2, "only the two reads reached a scope")

		for _, w := range writes {
			code := do(r, w.method, prefix+w.path, "developer", w.body)
			assert.Less(t, code, 300, "a developer may %s %s%s", w.method, prefix, w.path)
		}
	}
}

func TestScopeErrorsMapToStatusCodes(t *testing.T) {
	cases := []struct {
		err                error
		method, path, body string
		want               int
	}{
		{ErrKeyIsSecret, http.MethodPost, "/env-vars", `{"key":"S","value":"1"}`, http.StatusConflict},
		{ErrKeyIsSecret, http.MethodPut, "/env-vars", `{"vars":[{"key":"S","value":"1"}],"version":"v1"}`, http.StatusConflict},
		{listversion.ErrChanged, http.MethodPut, "/env-vars", `{"vars":[],"version":"old"}`, http.StatusConflict},
		{listversion.ErrRequired, http.MethodPut, "/env-vars", `{"vars":[]}`, http.StatusBadRequest},
		{ErrInvalidKey, http.MethodPost, "/env-vars", `{"key":"1A","value":"1"}`, http.StatusBadRequest},
		{ErrNotFound, http.MethodPut, "/secrets/NOPE", `{"value":"y"}`, http.StatusNotFound},
	}
	for _, tc := range cases {
		r := newTestHandler(&fakeVars{}, &fakeVars{err: tc.err})
		assert.Equal(t, tc.want, do(r, tc.method, "/api/v1/environments/env-1"+tc.path, "owner", tc.body), "%v on %s %s", tc.err, tc.method, tc.path)
	}
}

func (f *fakeVars) CheckAgent(context.Context, string, bool) error { return nil }

func (f *fakeVars) Writable(_ context.Context, id string) error {
	if id == "preview-copy" {
		return ErrPreviewService
	}
	return nil
}

// A preview's copy reads its variables from its original: writes on it are
// refused, reads aren't (D-104).
func TestPreviewCopiesRefuseWrites(t *testing.T) {
	r := newTestHandler(&fakeVars{}, &fakeVars{})
	for _, w := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/services/preview-copy/env-vars", `{"key":"A","value":"b"}`},
		{http.MethodPost, "/api/v1/services/preview-copy/secrets", `{"key":"A","value":"b"}`},
		{http.MethodDelete, "/api/v1/services/preview-copy/env-vars/A", ""},
	} {
		if got := do(r, w.method, w.path, "owner", w.body); got != http.StatusConflict {
			t.Errorf("%s %s = %d, want 409", w.method, w.path, got)
		}
	}
	if got := do(r, http.MethodGet, "/api/v1/services/preview-copy/env-vars", "owner", ""); got != http.StatusOK {
		t.Errorf("reading a preview copy's variables = %d", got)
	}
}
