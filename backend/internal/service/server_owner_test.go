package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	serverPkg "github.com/Nickpap21/toft/backend/internal/server"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// ownedRepo is assignRepo whose service belongs to org-a.
type ownedRepo struct{ assignRepo }

func (r *ownedRepo) CheckOwnership(_ context.Context, _, orgID string) (bool, error) {
	return orgID == "org-a", nil
}

// orgServers: srv-a is org-a's, srv-b org-b's, srv-w org-a's too but still
// waiting for its first agent.
type orgServers struct{}

func (orgServers) CanUse(_ context.Context, id, orgID string) (bool, error) {
	return ((id == "srv-a" || id == "srv-w") && orgID == "org-a") || (id == "srv-b" && orgID == "org-b"), nil
}

func (orgServers) CheckReady(_ context.Context, id string) error {
	if id == "srv-w" {
		return serverPkg.ErrServerNotReady
	}
	return nil
}

// A server still waiting for its first agent (D-153) is refused with 409
// server_not_ready wherever a service is given a server: moved onto it,
// created on it (the other create routes run the same check), or asked the address it would get. Nothing changes.
func TestWaitingServerIsNotAssignable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	do := func(method, path, body string) (int, string, *ownedRepo) {
		repo := &ownedRepo{}
		svc := NewServiceManager(repo, zap.NewNop())
		svc.WithAddresser(fakeAddresser{host: "web.203-0-113-10.sslip.io"})
		h := NewHandler(svc, nil, orgEnvs{}, &auditLog{}, nil, zap.NewNop())
		h.WithServerChecker(orgServers{})
		r := gin.New()
		g := r.Group("/api/v1")
		g.Use(func(c *gin.Context) { c.Set("orgID", "org-a"); c.Set("memberRole", "developer"); c.Next() })
		h.RegisterRoutes(g)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w.Code, w.Body.String(), repo
	}

	code, body, repo := do(http.MethodPut, "/api/v1/services/svc-1/server", `{"server_id":"srv-w"}`)
	assert.Equal(t, http.StatusConflict, code)
	assert.Contains(t, body, `"code":"server_not_ready"`)
	assert.Empty(t, repo.moved, "nothing moved")

	code, body, _ = do(http.MethodPost, "/api/v1/shared-services", `{"name":"web","type":"web","source_type":"docker-image","server_id":"srv-w"}`)
	assert.Equal(t, http.StatusConflict, code)
	assert.Contains(t, body, `"code":"server_not_ready"`)

	code, body, _ = do(http.MethodGet, "/api/v1/environments/env-a/address?name=web&port=80&server_id=srv-w", "")
	assert.Equal(t, http.StatusConflict, code)
	assert.Contains(t, body, `"code":"server_not_ready"`)

	code, _, repo = do(http.MethodPut, "/api/v1/services/svc-1/server", `{"server_id":"srv-a"}`)
	assert.Equal(t, http.StatusOK, code, "a server with an agent still takes it")
	assert.Equal(t, "srv-a", repo.moved)
}

// A service can be moved only onto its own workspace's servers, or the
// control-plane server; another workspace's server is refused before
// anything changes.
func TestAssignServerOnlyToTheWorkspacesOwnServers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	move := func(serverID string, withChecker bool) (int, *ownedRepo) {
		repo := &ownedRepo{}
		h := NewHandler(NewServiceManager(repo, zap.NewNop()), nil, nil, &auditLog{}, nil, zap.NewNop())
		if withChecker {
			h.WithServerChecker(orgServers{})
		}
		r := gin.New()
		g := r.Group("/api/v1")
		g.Use(func(c *gin.Context) { c.Set("orgID", "org-a"); c.Set("memberRole", "developer"); c.Next() })
		h.RegisterRoutes(g)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/v1/services/svc-1/server", strings.NewReader(`{"server_id":"`+serverID+`"}`))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w.Code, repo
	}

	code, repo := move("srv-b", true)
	assert.Equal(t, http.StatusForbidden, code)
	assert.Empty(t, repo.moved, "nothing moved")

	code, repo = move("srv-a", true)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "srv-a", repo.moved)

	code, _ = move("", true)
	assert.Equal(t, http.StatusOK, code, "the control-plane server")

	code, _ = move("srv-a", false)
	assert.Equal(t, http.StatusForbidden, code, "without a checker, every server is refused")
}

// orgEnvs: env-a is org-a's.
type orgEnvs struct{}

func (orgEnvs) CheckOwnership(_ context.Context, id, orgID string) (bool, error) {
	return id == "env-a" && orgID == "org-a", nil
}
func (orgEnvs) GetName(context.Context, string) (string, error) { return "production", nil }

// The address a new service would get (D-150): a developer of the
// environment's workspace, on one of its servers, asks; a viewer, another
// workspace's environment or server, and a bad query are refused.
func TestAddressEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	get := func(role, path string) (int, string) {
		svc := NewServiceManager(&ownedRepo{}, zap.NewNop())
		svc.WithAddresser(fakeAddresser{host: "web-production-shop.203-0-113-10.sslip.io"})
		h := NewHandler(svc, nil, orgEnvs{}, nil, nil, zap.NewNop())
		h.WithServerChecker(orgServers{})
		r := gin.New()
		g := r.Group("/api/v1")
		g.Use(func(c *gin.Context) { c.Set("orgID", "org-a"); c.Set("memberRole", role); c.Next() })
		h.RegisterRoutes(g)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/environments/"+path, nil))
		return w.Code, w.Body.String()
	}

	code, body := get("developer", "env-a/address?name=web&port=80&server_id=srv-a")
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, `"hostname":"web-production-shop.203-0-113-10.sslip.io"`)
	assert.Contains(t, body, `"temporary":true`)

	code, body = get("developer", "env-a/address?name=db&addon_slug=postgres&port=5432")
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, `"reason":"not_web"`)

	code, _ = get("viewer", "env-a/address?name=web&port=80")
	assert.Equal(t, http.StatusForbidden, code)
	code, _ = get("developer", "env-b/address?name=web&port=80")
	assert.Equal(t, http.StatusNotFound, code)
	code, _ = get("developer", "env-a/address?name=web&port=80&server_id=srv-b")
	assert.Equal(t, http.StatusForbidden, code)
	for _, q := range []string{"port=80", "name=web&type=cron", "name=web&port=70000"} {
		code, _ = get("developer", "env-a/address?"+q)
		assert.Equal(t, http.StatusBadRequest, code, q)
	}
}
