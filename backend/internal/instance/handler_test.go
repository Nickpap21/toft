package instance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/apispec"
	"github.com/Nickpap21/toft/backend/internal/instance"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// newRouter mounts the handler with a stand-in for middleware.AuthWithOrg
// that sets the literal "userID" key the real middleware uses.
func newRouter(repo *fakeRepo, actorID string) *gin.Engine {
	return newRouterVia(repo, actorID, false)
}

// newRouterVia also sets the literal "apiKey" key, as the middleware does for
// a request made with an API key.
func newRouterVia(repo *fakeRepo, actorID string, apiKey bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := instance.NewHandler(newService(repo), zap.NewNop())
	v1 := r.Group("/api/v1")
	h.RegisterPublicRoutes(v1)
	protected := v1.Group("")
	protected.Use(func(c *gin.Context) {
		c.Set("userID", actorID)
		if apiKey {
			c.Set("apiKey", true)
		}
		c.Next()
	})
	h.RegisterRoutes(protected)
	return r
}

func call(t *testing.T, r *gin.Engine, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	out := map[string]any{}
	if w.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out), w.Body.String())
		apispec.Check(t, method, path, w.Code, out)
	} else {
		apispec.Check(t, method, path, w.Code, nil)
	}
	return w.Code, out
}

func TestGetRegistration_Public(t *testing.T) {
	r := newRouter(newFakeRepo(), "")

	code, body := call(t, r, http.MethodGet, "/api/v1/instance/registration", nil)

	require.Equal(t, http.StatusOK, code)
	data := body["data"].(map[string]any)
	assert.Equal(t, "invite_only", data["mode"])
	assert.Equal(t, true, data["first_run"])
	assert.Equal(t, false, data["setup_token_required"])
}

func TestOperatorRoutes_NonOperator_403(t *testing.T) {
	repo := newFakeRepo(ada, bob)
	repo.operators[ada.id] = instance.SourceFirstRun
	r := newRouter(repo, bob.id)

	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/v1/instance/operators", nil},
		{http.MethodPost, "/api/v1/instance/operators", map[string]string{"email": bob.email, "password": pw}},
		{http.MethodDelete, "/api/v1/instance/operators/" + ada.id, map[string]string{"password": pw}},
		{http.MethodGet, "/api/v1/instance/users", nil},
		{http.MethodPatch, "/api/v1/instance/users/" + ada.id, map[string]any{"is_active": false, "password": pw}},
	} {
		code, body := call(t, r, tc.method, tc.path, tc.body)
		assert.Equal(t, http.StatusForbidden, code, "%s %s", tc.method, tc.path)
		assert.Contains(t, body["error"], "only instance operators")
	}
	assert.Empty(t, repo.grants)
	assert.True(t, repo.user(ada.id).active)
}

func TestGrantOperator_Routes(t *testing.T) {
	repo := newFakeRepo(ada, bob)
	repo.operators[ada.id] = instance.SourceFirstRun
	r := newRouter(repo, ada.id)

	code, _ := call(t, r, http.MethodPost, "/api/v1/instance/operators", map[string]string{})
	assert.Equal(t, http.StatusBadRequest, code, "neither field")

	code, _ = call(t, r, http.MethodPost, "/api/v1/instance/operators", map[string]string{"email": bob.email, "user_id": bob.id})
	assert.Equal(t, http.StatusBadRequest, code, "both fields")

	code, _ = call(t, r, http.MethodPost, "/api/v1/instance/operators", map[string]string{"email": bob.email})
	assert.Equal(t, http.StatusBadRequest, code, "no password")

	code, _ = call(t, r, http.MethodPost, "/api/v1/instance/operators", map[string]string{"email": "nobody@example.com", "password": pw})
	assert.Equal(t, http.StatusNotFound, code)

	code, body := call(t, r, http.MethodPost, "/api/v1/instance/operators", map[string]string{"email": bob.email, "password": pw})
	require.Equal(t, http.StatusCreated, code, body)
	assert.Equal(t, bob.id, body["data"].(map[string]any)["user_id"])

	code, body = call(t, r, http.MethodGet, "/api/v1/instance/operators", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Len(t, body["data"], 2)
}

func TestRevokeOperator_Routes(t *testing.T) {
	repo := newFakeRepo(ada, bob)
	repo.operators[ada.id] = instance.SourceFirstRun
	r := newRouter(repo, ada.id)

	password := map[string]string{"password": pw}
	code, _ := call(t, r, http.MethodDelete, "/api/v1/instance/operators/"+ada.id, nil)
	assert.Equal(t, http.StatusBadRequest, code, "no password")

	code, body := call(t, r, http.MethodDelete, "/api/v1/instance/operators/"+ada.id, password)
	assert.Equal(t, http.StatusConflict, code, "last operator")
	assert.Contains(t, body["error"], "without an active operator")

	code, _ = call(t, r, http.MethodDelete, "/api/v1/instance/operators/"+bob.id, password)
	assert.Equal(t, http.StatusNotFound, code, "bob is not an operator")

	repo.operators[bob.id] = instance.SourceAPI
	code, _ = call(t, r, http.MethodDelete, "/api/v1/instance/operators/"+bob.id, password)
	assert.Equal(t, http.StatusNoContent, code)
}

func TestUsersRoutes(t *testing.T) {
	repo := newFakeRepo(ada, bob)
	repo.operators[ada.id] = instance.SourceFirstRun
	r := newRouter(repo, ada.id)

	code, body := call(t, r, http.MethodGet, "/api/v1/instance/users", nil)
	require.Equal(t, http.StatusOK, code)
	page := body["data"].(map[string]any)
	assert.Equal(t, float64(0), page["deleted"])
	users := page["users"].([]any)
	require.Len(t, users, 2)
	first := users[0].(map[string]any)
	for _, k := range []string{"id", "email", "name", "is_active", "is_operator", "created_at", "deactivated_at", "deactivated_by", "workspaces", "last_login_at", "reset_expires_at"} {
		assert.Contains(t, first, k)
	}

	code, _ = call(t, r, http.MethodPatch, "/api/v1/instance/users/"+bob.id, map[string]any{"password": pw})
	assert.Equal(t, http.StatusBadRequest, code, "is_active is required")
	code, _ = call(t, r, http.MethodPatch, "/api/v1/instance/users/"+bob.id, map[string]any{"is_active": false})
	assert.Equal(t, http.StatusBadRequest, code, "the password is required")

	repo.invitesExposed = 2
	code, body = call(t, r, http.MethodPatch, "/api/v1/instance/users/"+bob.id, map[string]any{"is_active": false, "password": pw})
	require.Equal(t, http.StatusOK, code, body)
	data := body["data"].(map[string]any)
	assert.Equal(t, false, data["is_active"])
	assert.Equal(t, bob.email, data["email"], "the user fields stay at the top level")
	assert.Equal(t, float64(2), data["invites_revoked"])

	code, _ = call(t, r, http.MethodPatch, "/api/v1/instance/users/"+ada.id, map[string]any{"is_active": false, "password": pw})
	assert.Equal(t, http.StatusBadRequest, code, "cannot deactivate yourself")

	code, _ = call(t, r, http.MethodPatch, "/api/v1/instance/users/u-nobody", map[string]any{"is_active": true, "password": pw})
	assert.Equal(t, http.StatusNotFound, code)
}

// A wrong password is a 403 with a code, never a 401 (which signs the
// browser out), and an API key can't make these changes at all (D-50).
func TestChangeRoutes_Reauthentication(t *testing.T) {
	repo := newFakeRepo(ada, bob)
	repo.operators[ada.id] = instance.SourceFirstRun
	r := newRouter(repo, ada.id)

	code, body := call(t, r, http.MethodPatch, "/api/v1/instance/users/"+bob.id, map[string]any{"is_active": false, "password": "wrong"})
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "wrong_password", body["data"].(map[string]any)["code"])
	assert.True(t, repo.user(bob.id).active)

	viaKey := newRouterVia(repo, ada.id, true)
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/v1/instance/operators", map[string]string{"email": bob.email, "password": pw}},
		{http.MethodDelete, "/api/v1/instance/operators/" + ada.id, map[string]string{"password": pw}},
		{http.MethodPatch, "/api/v1/instance/users/" + bob.id, map[string]any{"is_active": false, "password": pw}},
	} {
		code, body := call(t, viaKey, tc.method, tc.path, tc.body)
		assert.Equal(t, http.StatusForbidden, code, "%s %s", tc.method, tc.path)
		assert.Equal(t, "session_required", body["data"].(map[string]any)["code"])
	}
	assert.Empty(t, repo.grants)
	assert.True(t, repo.user(bob.id).active)

	code, _ = call(t, viaKey, http.MethodGet, "/api/v1/instance/users", nil)
	assert.Equal(t, http.StatusOK, code, "reading stays open to an operator's key")
}

// A reset link (D-56): issued by an operator with their password, read and
// redeemed signed out, used once; a deactivated account gets none.
func TestPasswordReset_Routes(t *testing.T) {
	repo := newFakeRepo(ada, bob, carol)
	repo.operators[ada.id] = instance.SourceFirstRun
	r := newRouter(repo, ada.id)
	issue := "/api/v1/instance/users/" + bob.id + "/password-resets"

	code, _ := call(t, r, http.MethodPost, issue, map[string]any{})
	assert.Equal(t, http.StatusBadRequest, code, "the password is required")
	code, body := call(t, r, http.MethodPost, issue, map[string]any{"password": "wrong"})
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "wrong_password", body["data"].(map[string]any)["code"])
	code, _ = call(t, newRouter(repo, bob.id), http.MethodPost, issue, map[string]any{"password": pw})
	assert.Equal(t, http.StatusForbidden, code, "operators only")
	code, body = call(t, newRouterVia(repo, ada.id, true), http.MethodPost, issue, map[string]any{"password": pw})
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "session_required", body["data"].(map[string]any)["code"])

	code, body = call(t, r, http.MethodPost, issue, map[string]any{"password": pw})
	require.Equal(t, http.StatusCreated, code, body)
	token := body["data"].(map[string]any)["token"].(string)
	require.NotEmpty(t, token)
	assert.Contains(t, body["data"], "expires_at")
	require.Len(t, repo.resets, 1)
	for h := range repo.resets {
		assert.NotEqual(t, token, h, "only the token's hash is stored")
	}

	code, body = call(t, r, http.MethodPost, issue, map[string]any{"password": pw})
	require.Equal(t, http.StatusCreated, code)
	token = body["data"].(map[string]any)["token"].(string)
	assert.Len(t, repo.resets, 1, "a new link replaces the unused one")

	public := newRouter(repo, "")
	code, body = call(t, public, http.MethodGet, "/api/v1/password-resets/"+token, nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, bob.email, body["data"].(map[string]any)["email"])
	code, body = call(t, public, http.MethodGet, "/api/v1/password-resets/nope", nil)
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "reset_not_found", body["data"].(map[string]any)["code"])

	code, _ = call(t, public, http.MethodPost, "/api/v1/password-resets/"+token, map[string]any{"password": "elevenchars"})
	assert.Equal(t, http.StatusBadRequest, code, "min 12 chars")
	code, body = call(t, public, http.MethodPost, "/api/v1/password-resets/"+token, map[string]any{"password": "a new password"})
	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, bob.email, body["data"].(map[string]any)["email"])
	assert.NotEmpty(t, repo.passwords[bob.id])
	assert.NotEqual(t, "a new password", repo.passwords[bob.id], "stored hashed")

	code, body = call(t, public, http.MethodPost, "/api/v1/password-resets/"+token, map[string]any{"password": "another password"})
	assert.Equal(t, http.StatusBadRequest, code, "used once")
	assert.Equal(t, "reset_used", body["data"].(map[string]any)["code"])
	assert.Empty(t, repo.revoked, "keys and agents stay unless asked")

	_, body = call(t, r, http.MethodPost, issue, map[string]any{"password": pw})
	token = body["data"].(map[string]any)["token"].(string)
	code, _ = call(t, public, http.MethodPost, "/api/v1/password-resets/"+token, map[string]any{"password": "a third password", "revoke_access": true})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, []string{bob.id}, repo.revoked)
	assert.Equal(t, `{"revoked_access":true}`, repo.audit[len(repo.audit)-1].detail)

	repo.user(carol.id).active = false
	code, _ = call(t, r, http.MethodPost, "/api/v1/instance/users/"+carol.id+"/password-resets", map[string]any{"password": pw})
	assert.Equal(t, http.StatusConflict, code, "reactivate first")

	// The row carries the live link (D-57), and loses it on deactivation.
	code, body = call(t, r, http.MethodPost, "/api/v1/instance/users/"+bob.id+"/password-resets", map[string]any{"password": pw})
	require.Equal(t, http.StatusCreated, code)
	_, body = call(t, r, http.MethodGet, "/api/v1/instance/users", nil)
	rowOf := func(id string) map[string]any {
		for _, u := range body["data"].(map[string]any)["users"].([]any) {
			if u.(map[string]any)["id"] == id {
				return u.(map[string]any)
			}
		}
		return nil
	}
	assert.NotNil(t, rowOf(bob.id)["reset_expires_at"])
	call(t, r, http.MethodPatch, "/api/v1/instance/users/"+bob.id, map[string]any{"is_active": false, "password": pw})
	_, body = call(t, r, http.MethodGet, "/api/v1/instance/users", nil)
	assert.Nil(t, rowOf(bob.id)["reset_expires_at"])
	code, _ = call(t, r, http.MethodPost, "/api/v1/instance/users/u-nobody/password-resets", map[string]any{"password": pw})
	assert.Equal(t, http.StatusNotFound, code)
}

// Deleting an account (D-57): deactivated, non-operator, not yourself, with
// the password; the account leaves the list, its count shows, and the
// instance audit log has the trail.
func TestDeleteUser_Routes(t *testing.T) {
	repo := newFakeRepo(ada, bob, carol)
	repo.operators[ada.id] = instance.SourceFirstRun
	repo.operators[carol.id] = instance.SourceAPI
	r := newRouter(repo, ada.id)
	del := func(id string, body any) (int, map[string]any) {
		return call(t, r, http.MethodDelete, "/api/v1/instance/users/"+id, body)
	}

	code, _ := del(bob.id, map[string]any{})
	assert.Equal(t, http.StatusBadRequest, code, "the password is required")
	code, body := del(bob.id, map[string]any{"password": "wrong"})
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "wrong_password", body["data"].(map[string]any)["code"])
	code, _ = call(t, newRouter(repo, bob.id), http.MethodDelete, "/api/v1/instance/users/"+carol.id, map[string]any{"password": pw})
	assert.Equal(t, http.StatusForbidden, code, "operators only")
	code, body = call(t, newRouterVia(repo, ada.id, true), http.MethodDelete, "/api/v1/instance/users/"+bob.id, map[string]any{"password": pw})
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "session_required", body["data"].(map[string]any)["code"])

	code, _ = del(ada.id, map[string]any{"password": pw})
	assert.Equal(t, http.StatusBadRequest, code, "not yourself")
	code, _ = del(bob.id, map[string]any{"password": pw})
	assert.Equal(t, http.StatusConflict, code, "deactivate first")
	repo.user(carol.id).active = false
	code, _ = del(carol.id, map[string]any{"password": pw})
	assert.Equal(t, http.StatusConflict, code, "revoke operator first")
	code, _ = del("u-nobody", map[string]any{"password": pw})
	assert.Equal(t, http.StatusNotFound, code)
	assert.False(t, repo.user(bob.id).deleted)

	code, _ = call(t, r, http.MethodPatch, "/api/v1/instance/users/"+bob.id, map[string]any{"is_active": false, "password": pw})
	require.Equal(t, http.StatusOK, code)
	code, _ = del(bob.id, map[string]any{"password": pw})
	require.Equal(t, http.StatusNoContent, code)
	assert.True(t, repo.user(bob.id).deleted)
	assert.NotEqual(t, bob.email, repo.user(bob.id).email, "the email is freed")
	code, _ = del(bob.id, map[string]any{"password": pw})
	assert.Equal(t, http.StatusNotFound, code, "gone for every later change")

	_, body = call(t, r, http.MethodGet, "/api/v1/instance/users", nil)
	page := body["data"].(map[string]any)
	assert.Len(t, page["users"], 2)
	assert.Equal(t, float64(1), page["deleted"])

	code, body = call(t, r, http.MethodGet, "/api/v1/instance/audit-log", nil)
	require.Equal(t, http.StatusOK, code)
	entries := body["data"].([]any)
	require.GreaterOrEqual(t, len(entries), 2)
	newest := entries[0].(map[string]any)
	assert.Equal(t, "user.deleted", newest["action"])
	assert.Equal(t, bob.email, newest["detail"].(map[string]any)["email"], "the log keeps the email the tombstone lost")
	assert.Equal(t, "Deleted account", newest["target"].(map[string]any)["name"])
	assert.Equal(t, ada.name, newest["actor"].(map[string]any)["name"])
	assert.Equal(t, "user.deactivated", entries[1].(map[string]any)["action"])
	code, _ = call(t, newRouter(repo, bob.id), http.MethodGet, "/api/v1/instance/audit-log", nil)
	assert.Equal(t, http.StatusForbidden, code, "operators only")
}

// ?kind reaches the query; anything but panel or accounts is refused.
func TestListAudit_Kind(t *testing.T) {
	repo := newFakeRepo(ada)
	repo.operators[ada.id] = instance.SourceFirstRun
	r := newRouter(repo, ada.id)

	code, _ := call(t, r, http.MethodGet, "/api/v1/instance/audit-log?kind=accounts", nil)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "accounts", repo.auditKind)

	code, body := call(t, r, http.MethodGet, "/api/v1/instance/audit-log?kind=nope", nil)
	assert.Equal(t, http.StatusBadRequest, code)
	assert.NotEmpty(t, body["error"])
	assert.EqualValues(t, http.StatusBadRequest, body["status"], "the problem shape")
}

// The operator check comes before ?kind is looked at.
func TestListAudit_BadKind_NonOperator_403(t *testing.T) {
	repo := newFakeRepo(ada, bob)
	repo.operators[ada.id] = instance.SourceFirstRun

	code, _ := call(t, newRouter(repo, bob.id), http.MethodGet, "/api/v1/instance/audit-log?kind=nope", nil)
	assert.Equal(t, http.StatusForbidden, code)
}

type fakePanelHost struct{ host instance.PanelHost }

func (f fakePanelHost) PanelHost(context.Context) (instance.PanelHost, error) { return f.host, nil }

// Every member reads the panel host, not only operators: their services
// run there until their workspace adds a server.
func TestPanelHost_AnyMember(t *testing.T) {
	repo := newFakeRepo(ada, bob)
	repo.operators[ada.id] = instance.SourceFirstRun
	gin.SetMode(gin.TestMode)
	r := gin.New()
	svc := instance.NewService(repo, zap.NewNop()).WithPanelHost(fakePanelHost{instance.PanelHost{
		Address: "203.0.113.20", CPUCores: 4, MemoryBytes: 8 << 30, DiskBytes: 80 << 30, AppNetworks: "legacy",
	}})
	protected := r.Group("/api/v1")
	protected.Use(func(c *gin.Context) { c.Set("userID", bob.id); c.Next() })
	instance.NewHandler(svc, zap.NewNop()).RegisterRoutes(protected)

	code, body := call(t, r, http.MethodGet, "/api/v1/instance/panel-host", nil)

	require.Equal(t, http.StatusOK, code)
	data := body["data"].(map[string]any)
	assert.Equal(t, "203.0.113.20", data["address"])
	assert.Equal(t, float64(4), data["cpu_cores"])
	assert.Equal(t, "legacy", data["app_networks"])
}

// The panel's own health, server and logs (D-68): operators only, and every
// answer as the spec says.
func TestPanelRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	shellOn := false
	router := func(actorID string) *gin.Engine {
		repo := newFakeRepo(ada, bob)
		repo.operators[ada.id] = instance.SourceFirstRun
		r := gin.New()
		svc := instance.NewService(repo, zap.NewNop()).WithPanelMonitor(&fakeProbe{}, &fakeStore{}, t.TempDir(), t.TempDir()).WithPanelShell(shellOn)
		protected := r.Group("/api/v1")
		protected.Use(func(c *gin.Context) { c.Set("userID", actorID); c.Next() })
		instance.NewHandler(svc, zap.NewNop()).RegisterRoutes(protected)
		return r
	}

	code, body := call(t, router(ada.id), http.MethodGet, "/api/v1/instance/panel", nil)
	require.Equal(t, http.StatusOK, code)
	data := body["data"].(map[string]any)
	assert.Equal(t, true, data["healthy"])
	assert.Len(t, data["containers"], 4)

	code, _ = call(t, router(ada.id), http.MethodGet, "/api/v1/instance/panel/server?range=24h", nil)
	assert.Equal(t, http.StatusOK, code)
	code, _ = call(t, router(ada.id), http.MethodGet, "/api/v1/instance/panel/server?range=1y", nil)
	assert.Equal(t, http.StatusBadRequest, code)
	code, _ = call(t, router(ada.id), http.MethodGet, "/api/v1/instance/panel/containers/storefront-web/logs", nil)
	assert.Equal(t, http.StatusNotFound, code, "only the panel's own containers")

	// The shell is off until TOFT_PANEL_HOST_SHELL allows it (the router
	// above never turns it on).
	code, body = call(t, router(ada.id), http.MethodGet, "/api/v1/instance/panel/pty", nil)
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "panel_shell_off", body["data"].(map[string]any)["code"])

	// On, it still wants the password confirmed first (D-53).
	shellOn = true
	code, body = call(t, router(ada.id), http.MethodGet, "/api/v1/instance/panel/pty", nil)
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "reauth_required", body["data"].(map[string]any)["code"])

	for _, path := range []string{"/api/v1/instance/panel", "/api/v1/instance/panel/server", "/api/v1/instance/panel/containers/paas-backend/logs", "/api/v1/instance/panel/pty"} {
		code, _ = call(t, router(bob.id), http.MethodGet, path, nil)
		assert.Equal(t, http.StatusForbidden, code, path)
	}
}

// Upgrades from the panel (D-68): operators, sessions and the password; a
// deploy.sh install says so.
func TestUpgradeRoutes(t *testing.T) {
	repo := newFakeRepo(ada, bob)
	repo.operators[ada.id] = instance.SourceFirstRun

	code, body := call(t, newRouter(repo, ada.id), http.MethodGet, "/api/v1/instance/upgrade", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "deploy_sh", body["data"].(map[string]any)["install"], "no toft files here")

	code, _ = call(t, newRouter(repo, bob.id), http.MethodGet, "/api/v1/instance/upgrade", nil)
	assert.Equal(t, http.StatusForbidden, code)

	code, body = call(t, newRouterVia(repo, ada.id, true), http.MethodPost, "/api/v1/instance/upgrade", map[string]string{"to": "v0.1.3", "password": pw})
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "session_required", body["data"].(map[string]any)["code"])

	code, _ = call(t, newRouter(repo, ada.id), http.MethodPost, "/api/v1/instance/upgrade", map[string]string{"to": "v0.1.3"})
	assert.Equal(t, http.StatusBadRequest, code, "the password is required")

	code, body = call(t, newRouter(repo, ada.id), http.MethodPost, "/api/v1/instance/upgrade", map[string]string{"to": "v0.1.3", "password": pw})
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "deploy_sh", body["data"].(map[string]any)["code"])

	code, body = call(t, newRouter(repo, ada.id), http.MethodPost, "/api/v1/instance/upgrade/check", nil)
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "deploy_sh", body["data"].(map[string]any)["code"])
}
