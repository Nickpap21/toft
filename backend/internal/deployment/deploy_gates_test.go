package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	orgPkg "github.com/Nickpap21/toft/backend/internal/org"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/pkg/actor"
)

// everyCopy says every service is a preview's.
type everyCopy struct{}

func (everyCopy) IsPreviewCopy(context.Context, string) (bool, error) { return true, nil }

// brokenPolicy is an agent policy whose check fails.
type brokenPolicy struct{}

func (brokenPolicy) Check(context.Context, actor.Target) error {
	return errors.New(`ERROR: relation "agent_policies" does not exist (SQLSTATE 42P01)`)
}
func (brokenPolicy) ReleaseRollback(context.Context, string) {}

// deployRoute posts a deploy of svc-1 as an owner and asks for the same
// deploy as a create would (DeployCreated), on a Service set up by with.
func deployRoute(t *testing.T, with func(*Service)) (w *httptest.ResponseRecorder, body map[string]any, refused *service.DeployRefused, audit *auditLog, logs *observer.ObservedLogs) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	core, logs := observer.New(zap.ErrorLevel)
	s := NewService(nil, nil, nil, zap.New(core))
	with(s)
	audit = &auditLog{}
	s.WithDeployUseCase(guestService{}, nil, nil, nil, nil, nil, audit)
	h := NewHandler(s, guestService{}, zap.New(core))
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("orgID", "guest")
		c.Set("userID", "bob")
		c.Set("memberRole", orgPkg.RoleOwner)
		c.Next()
	})
	h.RegisterRoutes(g)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/services/svc-1/deployments", strings.NewReader(`{}`)))
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))

	_, err := h.DeployCreated(context.Background(), service.DeployCaller{OrgID: "guest", UserID: "bob", Role: orgPkg.RoleOwner}, "svc-1", false)
	require.ErrorAs(t, err, &refused)
	return w, body, refused, audit, logs
}

// One of a preview's services deploys from its preview only: the deploy
// route refuses it with 409 preview_service, and nothing is deployed.
func TestTrigger_PreviewServiceIsRefused(t *testing.T) {
	w, body, refused, audit, _ := deployRoute(t, func(s *Service) { s.WithPreviews(nil, nil, everyCopy{}) })
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Equal(t, ErrPreviewService.Error(), body["error"])
	assert.Equal(t, "preview_service", body["data"].(map[string]any)["code"])
	assert.Equal(t, service.DeployRefused{Message: ErrPreviewService.Error(), Code: "preview_service"}, *refused, "a create's deploy_error")
	assert.Empty(t, audit.actions)
}

// The agent policy's check failing is ours: a 500 that says only that, the
// policy's own text in the log, and nothing deployed.
func TestTrigger_AgentPolicyCheckFailing(t *testing.T) {
	const msg = "failed to check the agent policy"
	w, body, refused, audit, logs := deployRoute(t, func(s *Service) { s.WithAgentGate(brokenPolicy{}) })
	require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	assert.Equal(t, msg, body["error"])
	assert.NotContains(t, w.Body.String(), "SQLSTATE")
	assert.Equal(t, service.DeployRefused{Message: msg}, *refused, "a create's deploy_error")
	assert.Empty(t, audit.actions)

	require.Equal(t, 2, logs.Len(), "once for the route, once for the create")
	for _, e := range logs.All() {
		assert.Contains(t, fmt.Sprint(e.ContextMap()["error"]), "SQLSTATE 42P01")
		assert.Equal(t, "svc-1", e.ContextMap()["service_id"])
	}
}
