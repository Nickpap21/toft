package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// restartOrch counts restarts; running is how many containers run.
type restartOrch struct {
	fakeOrch
	running  int
	restarts int
}

func (o *restartOrch) RestartService(context.Context, string, string) (int, error) {
	o.restarts++
	return o.running, nil
}

type auditLog struct{ actions []string }

func (a *auditLog) WriteAuditLog(_ context.Context, _, _, action, resource string) {
	a.actions = append(a.actions, action+" "+resource)
}

func (a *auditLog) WriteAuditLogDetail(ctx context.Context, orgID, actorID, action, resource string, _ map[string]any) {
	a.WriteAuditLog(ctx, orgID, actorID, action, resource)
}

func TestServiceManager_Restart(t *testing.T) {
	newManager := func(svc *Service, orch *restartOrch) *ServiceManager {
		m := NewServiceManager(&fakeRepo{svc: svc}, zap.NewNop())
		m.WithOrchestration(fakeServers{}, reconcilerFactory{orch})
		return m
	}

	orch := &restartOrch{running: 1}
	require.NoError(t, newManager(&Service{ID: "s1", Name: "web"}, orch).Restart(context.Background(), "s1"))
	assert.Equal(t, 1, orch.restarts)

	orch = &restartOrch{running: 0}
	err := newManager(&Service{ID: "s1", Name: "web"}, orch).Restart(context.Background(), "s1")
	assert.ErrorIs(t, err, ErrNotRunning, "nothing running: Deploy starts it")

	orch = &restartOrch{running: 1}
	err = newManager(&Service{ID: "s1", Name: "web", Deploying: true}, orch).Restart(context.Background(), "s1")
	assert.ErrorIs(t, err, ErrDeployRunning)
	assert.Zero(t, orch.restarts, "a deploy under way is not raced")
}

func TestHandler_Restart(t *testing.T) {
	gin.SetMode(gin.TestMode)
	orch := &restartOrch{running: 2}
	m := NewServiceManager(&fakeRepo{svc: &Service{ID: "s1", Name: "web"}}, zap.NewNop())
	m.WithOrchestration(fakeServers{}, reconcilerFactory{orch})
	audit := &auditLog{}
	router := func(role, orgID string) *gin.Engine {
		r := gin.New()
		g := r.Group("/api/v1")
		g.Use(func(c *gin.Context) {
			c.Set("userID", "dev")
			c.Set("orgID", orgID)
			c.Set("memberRole", role)
			c.Next()
		})
		NewHandler(m, nil, nil, audit, nil, zap.NewNop()).RegisterRoutes(g)
		return r
	}
	post := func(r http.Handler) int {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/services/s1/restart", nil))
		return w.Code
	}

	assert.Equal(t, http.StatusForbidden, post(router("viewer", "org-a")))
	assert.Equal(t, http.StatusNotFound, post(router("owner", "org-b")), "another workspace")
	assert.Zero(t, orch.restarts)

	assert.Equal(t, http.StatusNoContent, post(router("developer", "org-a")))
	assert.Equal(t, 1, orch.restarts)
	assert.Equal(t, []string{"service.restarted service:s1"}, audit.actions)

	orch.running = 0
	assert.Equal(t, http.StatusConflict, post(router("owner", "org-a")))
	assert.Len(t, audit.actions, 1, "a refused restart is not audited")
}
