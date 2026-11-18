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

	domainPkg "github.com/Nickpap21/toft/backend/internal/domain"
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

// askPolicy is an agent policy that holds every deploy for a person, as
// Ask does, and counts the approvals it opened.
type askPolicy struct{ opened *int }

func (p askPolicy) Check(context.Context, actor.Target) error {
	*p.opened++
	return &actor.GateError{Status: http.StatusAccepted, Code: "approval_pending", Message: "waiting for a person", ApprovalID: "appr-1"}
}
func (askPolicy) ReleaseRollback(context.Context, string) {}

// An agent's deploy that can only fail is refused before the policy sees
// it, so no person is asked to approve a request that would then fail.
func TestTrigger_AnInvalidBodyNeverBecomesAnApproval(t *testing.T) {
	gin.SetMode(gin.TestMode)
	opened := 0
	s := NewService(nil, nil, nil, zap.NewNop())
	s.WithAgentGate(askPolicy{&opened})
	s.WithDeployUseCase(guestService{}, nil, nil, nil, nil, nil, &auditLog{})
	h := NewHandler(s, guestService{}, zap.NewNop())
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("orgID", "guest")
		c.Set("userID", "bob")
		c.Set("memberRole", orgPkg.RoleOwner)
		c.Request = c.Request.WithContext(actor.WithAgent(c.Request.Context(), actor.Agent{ConnectionID: "conn-1", Name: "Claude Code", UserID: "bob", OrgID: "guest"}))
		c.Next()
	})
	h.RegisterRoutes(g)

	for _, body := range []string{`{"git_ref":"main"}`, `{"git_ref":"-x","git_ref_type":"tag"}`} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/services/svc-1/deployments", strings.NewReader(body)))
		assert.Equal(t, http.StatusBadRequest, w.Code, "%s: %s", body, w.Body.String())
	}
	assert.Zero(t, opened, "no approval opened")

	// A valid body still meets the policy.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/services/svc-1/deployments", strings.NewReader(`{"git_ref":"main","git_ref_type":"branch"}`)))
	assert.Contains(t, w.Body.String(), "approval_pending")
	assert.Equal(t, 1, opened)
}

// composeStack is guestService as a compose stack built from a repository.
type composeStack struct{ guestService }

func (composeStack) Get(_ context.Context, id string) (*service.Service, error) {
	return &service.Service{ID: id, SourceType: service.SourceTypeDockerCompose}, nil
}

// The deploy's invalid fields answer 400 invalid_config, each named.
func TestTrigger_InvalidFieldsAreInvalidConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := NewService(nil, nil, nil, zap.NewNop())
	s.WithDeployUseCase(composeStack{}, nil, domainPkg.NewService(noDomains{}, nil, zap.NewNop()), noVars{}, nil, nil, &auditLog{})
	h := NewHandler(s, composeStack{}, zap.NewNop())
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("orgID", "guest")
		c.Set("userID", "bob")
		c.Set("memberRole", orgPkg.RoleOwner)
		c.Next()
	})
	h.RegisterRoutes(g)

	for body, msg := range map[string]string{
		`{"commit_sha":"abc123"}`:                 "commit_sha must be a full 40-character commit SHA",
		`{"image_tag":"1.2; rm -rf /"}`:           "image_tag must be an image tag",
		`{"git_ref":"a..b","git_ref_type":"tag"}`: `git_ref "a..b" is not a valid ref name`,
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/services/svc-1/deployments", strings.NewReader(body)))
		require.Equal(t, http.StatusBadRequest, w.Code, "%s: %s", body, w.Body.String())
		var out map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
		assert.Equal(t, "invalid_config", out["code"], body)
		assert.Contains(t, out["error"], msg, body)
	}
}

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
