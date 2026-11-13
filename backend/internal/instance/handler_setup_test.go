package instance_test

import (
	"net/http"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/instance"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// setupRouter mounts the handler for actorID; reauth stands in for a
// recent password confirmation (the literal key the middleware sets).
func setupRouter(svc *instance.Service, actorID string, reauth, apiKey bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := instance.NewHandler(svc, zap.NewNop())
	v1 := r.Group("/api/v1")
	h.RegisterPublicRoutes(v1)
	protected := v1.Group("")
	protected.Use(func(c *gin.Context) {
		c.Set("userID", actorID)
		c.Set("reauthenticated", reauth)
		if apiKey {
			c.Set("apiKey", true)
		}
		c.Next()
	})
	h.RegisterRoutes(protected)
	return r
}

func code(body map[string]any) any {
	d, _ := body["data"].(map[string]any)
	return d["code"]
}

func TestPatchSetup_DomainThatDoesntReachThePanel(t *testing.T) {
	svc, _, _ := readyToFinish(t, "panel.example", &probeSpy{unreachable: true})
	op := setupRouter(svc, "op", false, false)

	c, body := call(t, op, http.MethodPatch, "/api/v1/instance/setup", map[string]bool{"finished": true})
	assert.Equal(t, http.StatusConflict, c)
	assert.Equal(t, "panel_domain_unreachable", code(body))

	c, body = call(t, op, http.MethodPatch, "/api/v1/instance/setup", map[string]bool{"finished": true, "domain_checked": true})
	require.Equal(t, http.StatusOK, c)
	assert.Equal(t, false, body["data"].(map[string]any)["in_progress"])
}

func TestPutSetupDomain_DomainThatDoesntReachThePanel(t *testing.T) {
	svc, _, _ := readyToFinish(t, "", &probeSpy{unreachable: true})
	op := setupRouter(svc, "op", true, false)
	c, _ := call(t, op, http.MethodPatch, "/api/v1/instance/setup", map[string]bool{"finished": true})
	require.Equal(t, http.StatusOK, c)

	c, body := call(t, op, http.MethodPut, "/api/v1/instance/setup/domain", map[string]any{"domain": "panel.example"})
	assert.Equal(t, http.StatusConflict, c)
	assert.Equal(t, "panel_domain_unreachable", code(body))

	c, body = call(t, op, http.MethodPut, "/api/v1/instance/setup/domain", map[string]any{"domain": "panel.example", "domain_checked": true})
	require.Equal(t, http.StatusOK, c)
	assert.Equal(t, "panel.example", body["data"].(map[string]any)["domain"])
}

func TestSetupHandlers(t *testing.T) {
	repo := setupRepo()
	repo.panelHostServices = 1
	spy := &routesSpy{}
	svc := instance.NewService(repo, zap.NewNop()).WithPanelRoutes(spy, "", false).
		WithSetup(&probeSpy{}, "typesense").
		WithRecoveryKit("enc-key-hex", "v0.1.0", kitBackups{nil})
	op := setupRouter(svc, "op", false, false)
	opReauth := setupRouter(svc, "op", true, false)

	c, body := call(t, op, http.MethodGet, "/api/v1/instance/setup", nil)
	require.Equal(t, http.StatusOK, c)
	assert.Equal(t, true, body["data"].(map[string]any)["in_progress"])
	assert.Equal(t, "typesense", body["data"].(map[string]any)["app"])

	c, _ = call(t, setupRouter(svc, "dev", true, false), http.MethodGet, "/api/v1/instance/setup", nil)
	assert.Equal(t, http.StatusForbidden, c, "operators only")

	// The domain and the kit ask for the password again; a non-operator is
	// refused as one, never asked for a password.
	c, body = call(t, op, http.MethodPut, "/api/v1/instance/setup/domain", map[string]string{"domain": "panel.example"})
	assert.Equal(t, http.StatusForbidden, c)
	assert.Equal(t, "reauth_required", code(body))
	c, body = call(t, setupRouter(svc, "dev", false, false), http.MethodPut, "/api/v1/instance/setup/domain", map[string]string{"domain": "panel.example"})
	assert.Equal(t, http.StatusForbidden, c)
	assert.Nil(t, code(body))
	c, _ = call(t, opReauth, http.MethodPut, "/api/v1/instance/setup/domain", map[string]string{"domain": "panel..example"})
	assert.Equal(t, http.StatusBadRequest, c)
	c, body = call(t, opReauth, http.MethodPut, "/api/v1/instance/setup/domain", map[string]string{"domain": "panel.example"})
	require.Equal(t, http.StatusOK, c)
	assert.Equal(t, "panel.example", body["data"].(map[string]any)["domain"])

	c, body = call(t, op, http.MethodPost, "/api/v1/instance/setup/domain-check", map[string]string{"domain": "panel.example"})
	require.Equal(t, http.StatusOK, c)
	assert.Equal(t, true, body["data"].(map[string]any)["reaches_panel"])

	c, body = call(t, op, http.MethodGet, "/api/v1/instance/recovery-kit", nil)
	assert.Equal(t, http.StatusForbidden, c)
	assert.Equal(t, "reauth_required", code(body))
	c, body = call(t, setupRouter(svc, "op", true, true), http.MethodGet, "/api/v1/instance/recovery-kit", nil)
	assert.Equal(t, http.StatusForbidden, c)
	assert.Equal(t, "session_required", code(body), "an API key never reads the kit")
	c, body = call(t, opReauth, http.MethodGet, "/api/v1/instance/recovery-kit", nil)
	require.Equal(t, http.StatusOK, c)
	assert.Equal(t, "enc-key-hex", body["data"].(map[string]any)["encryption_key"])
	fingerprint := body["data"].(map[string]any)["fingerprint"]

	c, body = call(t, op, http.MethodPut, "/api/v1/instance/setup/role", map[string]string{"role": "control_plane"})
	assert.Equal(t, http.StatusConflict, c)
	assert.Equal(t, "panel_host_in_use", code(body))
	c, _ = call(t, op, http.MethodPut, "/api/v1/instance/setup/role", map[string]string{"role": "apps"})
	assert.Equal(t, http.StatusOK, c)

	c, body = call(t, op, http.MethodPatch, "/api/v1/instance/setup", map[string]bool{"finished": true})
	assert.Equal(t, http.StatusConflict, c)
	assert.Equal(t, "setup_incomplete", code(body))
	c, _ = call(t, setupRouter(svc, "op", false, true), http.MethodPatch, "/api/v1/instance/setup", map[string]bool{"kit_acknowledged": true})
	assert.Equal(t, http.StatusForbidden, c, "sessions only")
	c, _ = call(t, op, http.MethodPatch, "/api/v1/instance/setup", map[string]bool{"kit_acknowledged": true})
	assert.Equal(t, http.StatusBadRequest, c, "which kit was stored: its fingerprint")
	c, body = call(t, op, http.MethodPatch, "/api/v1/instance/setup", map[string]any{"kit_acknowledged": true, "fingerprint": "not-this-kit"})
	assert.Equal(t, http.StatusConflict, c)
	assert.Equal(t, "kit_changed", code(body))
	c, body = call(t, op, http.MethodPatch, "/api/v1/instance/setup", map[string]any{"kit_acknowledged": true, "fingerprint": fingerprint})
	require.Equal(t, http.StatusOK, c)
	assert.Equal(t, false, body["data"].(map[string]any)["kit_out_of_date"])
	c, body = call(t, op, http.MethodPatch, "/api/v1/instance/setup", map[string]bool{"finished": true})
	require.Equal(t, http.StatusOK, c)
	assert.Equal(t, false, body["data"].(map[string]any)["in_progress"])
	assert.Equal(t, instance.PanelRoutes{Domain: "panel.example"}, spy.got[len(spy.got)-1], "finished: the IP stops answering")

	c, body = call(t, setupRouter(svc, "", false, false), http.MethodGet, "/api/v1/instance/probe", nil)
	require.Equal(t, http.StatusOK, c)
	assert.Equal(t, svc.ProbeID(), body["data"].(map[string]any)["id"])
}
