package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/deploypolicy"
	serverPkg "github.com/Nickpap21/toft/backend/internal/server"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// ─── Fakes ────────────────────────────────────────────────────────────────────

// fakeRepo implements only what these tests call; anything else panics on
// the nil embedded interface.
type fakeRepo struct {
	Repository
	svc       *Service
	created   int
	updated   int
	createdIn CreateInput // the last create's input, as stored
	// The stored container check (ContainerHealth).
	checkMode, checkCommand string
}

func (f *fakeRepo) AliasUserInWorkspace(context.Context, string, string, string) (string, error) {
	return "", nil
}
func (f *fakeRepo) PidsLimit(context.Context, string) (int, error)           { return 0, nil }
func (f *fakeRepo) SetPidsLimit(context.Context, string, int) error          { return nil }
func (f *fakeRepo) DeployTimeout(context.Context, string) (int, error)       { return 0, nil }
func (f *fakeRepo) SetDeployTimeout(context.Context, string, int) error      { return nil }
func (f *fakeRepo) RollbackImagesKept(context.Context, string) (int, error)  { return 0, nil }
func (f *fakeRepo) SetRollbackImagesKept(context.Context, string, int) error { return nil }
func (f *fakeRepo) ContainerHealth(context.Context, string) (string, string, error) {
	return f.checkMode, f.checkCommand, nil
}
func (f *fakeRepo) SetContainerHealth(_ context.Context, _, mode, command string) error {
	f.checkMode, f.checkCommand = mode, command
	return nil
}
func (f *fakeRepo) Isolation(context.Context, string) (string, string, error) {
	return "", "standard", nil
}
func (f *fakeRepo) SetIsolation(context.Context, string, string) error         { return nil }
func (f *fakeRepo) IsPreviewCopy(context.Context, string) (bool, error)        { return false, nil }
func (f *fakeRepo) IsPreviewEnvironment(context.Context, string) (bool, error) { return false, nil }

func (f *fakeRepo) GetByID(_ context.Context, id string) (*Service, error) {
	if f.svc == nil || f.svc.ID != id {
		return nil, errors.New("not found")
	}
	return f.svc, nil
}

func (f *fakeRepo) ListByEnvironment(context.Context, string) ([]Service, error) { return nil, nil }

func (f *fakeRepo) Create(_ context.Context, _ string, in CreateInput) (*Service, error) {
	f.created++
	f.createdIn = in
	return &Service{ID: "new", Name: in.Name, SourceType: in.SourceType}, nil
}

func (f *fakeRepo) UpdateConfig(_ context.Context, id string, _ UpdateConfigInput) (*Service, error) {
	f.updated++
	return f.svc, nil
}

func (f *fakeRepo) CheckOwnership(_ context.Context, id, orgID string) (bool, error) {
	return f.svc != nil && f.svc.ID == id && orgID == "org-a", nil
}

type fakePolicy struct {
	decision deploypolicy.Decision
	seen     []deploypolicy.SaveCheck
}

func (f *fakePolicy) CheckSave(_ context.Context, in deploypolicy.SaveCheck) deploypolicy.Decision {
	f.seen = append(f.seen, in)
	return f.decision
}

type fakeOperators map[string]bool

func (f fakeOperators) IsOperator(_ context.Context, id string) (bool, error) { return f[id], nil }

type fakeServers struct{}

func (fakeServers) GetServer(context.Context, string) (*serverPkg.Server, error) {
	return &serverPkg.Server{}, nil
}

type fakeOrch struct {
	mu         sync.Mutex
	containers []string
	execTarget string
	hostCmds   []string
	logsFrom   string
}

func (o *fakeOrch) Stop(context.Context, string) error                        { return nil }
func (o *fakeOrch) StopCompose(context.Context, string, string, string) error { return nil }
func (o *fakeOrch) StreamLogs(_ context.Context, _ string, _ int, name string, logFn func(string)) error {
	o.mu.Lock()
	o.logsFrom = name
	o.mu.Unlock()
	logFn("line")
	return nil
}
func (o *fakeOrch) ExecInContainer(_ context.Context, _, name, _ string, logFn func(string)) error {
	o.mu.Lock()
	o.execTarget = name
	o.mu.Unlock()
	logFn("ok")
	return nil
}
func (o *fakeOrch) ListContainers(context.Context, string, string) ([]string, error) {
	return o.containers, nil
}
func (o *fakeOrch) ExecCommands(_ context.Context, cmds []string, _ func(string)) error {
	o.mu.Lock()
	o.hostCmds = append(o.hostCmds, cmds...)
	o.mu.Unlock()
	return nil
}

type fakeFactory struct{ orch *fakeOrch }

func (f fakeFactory) ForServer(*serverPkg.Server) (ContainerOrchestrator, func(), error) {
	return f.orch, func() {}, nil
}

func blockedDecision() deploypolicy.Decision {
	return deploypolicy.Decision{
		Mode:       deploypolicy.ModeEnforce,
		Violations: []composespec.Violation{{Rule: composespec.RulePrivileged, Field: "services.web.privileged", Message: "no"}},
		Blocked:    true,
	}
}

// ─── Save-time policy ─────────────────────────────────────────────────────────

func TestCreate_PolicyViolationRefusesSave(t *testing.T) {
	repo := &fakeRepo{}
	m := NewServiceManager(repo, zap.NewNop())
	policy := &fakePolicy{decision: blockedDecision()}
	m.WithPolicyChecker(policy)

	_, err := m.Create(context.Background(), "env-1", "", CreateInput{
		Name: "web", SourceType: SourceTypeDockerCompose, ComposeContent: "services: {}",
		Volumes: []VolumeMount{{Name: "x", Target: "/x", HostPath: "/etc"}}, PreDeployCommands: "make",
	})
	var pv *composespec.ViolationsError
	require.True(t, errors.As(err, &pv))
	assert.Equal(t, 0, repo.created)
	require.Len(t, policy.seen, 1)
	assert.Equal(t, "", policy.seen[0].ServiceID)
	assert.Equal(t, "services: {}", policy.seen[0].ComposeContent)
	assert.Equal(t, "/etc", policy.seen[0].Volumes[0].HostPath)
	assert.Equal(t, "make", policy.seen[0].PreDeployCommands)
}

func TestCreate_WarnModeSavesAndReturnsViolations(t *testing.T) {
	repo := &fakeRepo{}
	m := NewServiceManager(repo, zap.NewNop())
	d := blockedDecision()
	d.Mode, d.Blocked = deploypolicy.ModeWarn, false
	m.WithPolicyChecker(&fakePolicy{decision: d})

	saved, err := m.Create(context.Background(), "env-1", "", CreateInput{Name: "web", SourceType: SourceTypeGit})
	require.NoError(t, err)
	assert.Equal(t, 1, repo.created)
	assert.Len(t, saved.PolicyViolations, 1)

	b, err := json.Marshal(saved)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"policy_violations"`)
	assert.Contains(t, string(b), `"id":"new"`, "service fields stay at the top level")
}

func TestUpdateConfig_ChecksWithServiceIDAndStoredSourceType(t *testing.T) {
	repo := &fakeRepo{svc: &Service{ID: "s1", Name: "web", SourceType: SourceTypeDockerCompose}}
	m := NewServiceManager(repo, zap.NewNop())
	policy := &fakePolicy{decision: blockedDecision()}
	m.WithPolicyChecker(policy)

	_, err := m.UpdateConfig(context.Background(), "s1", UpdateConfigInput{ComposeContent: "x"})
	var pv *composespec.ViolationsError
	require.True(t, errors.As(err, &pv))
	assert.Equal(t, 0, repo.updated)
	assert.Equal(t, "s1", policy.seen[0].ServiceID)
	assert.Equal(t, "web", policy.seen[0].ServiceName)
	assert.Equal(t, string(SourceTypeDockerCompose), policy.seen[0].SourceType)
}

// ─── Exec, logs and stats stay inside the service ────────────────────────────

func execManager(orch *fakeOrch, ops OperatorChecker) *ServiceManager {
	repo := &fakeRepo{svc: &Service{ID: "s1", Name: "web"}}
	m := NewServiceManager(repo, zap.NewNop())
	m.WithOrchestration(fakeServers{}, fakeFactory{orch: orch})
	if ops != nil {
		m.WithOperatorChecker(ops)
	}
	return m
}

func TestExecInService_HostShellIsOperatorOnly(t *testing.T) {
	orch := &fakeOrch{}
	ctx := context.Background()

	err := execManager(orch, nil).ExecInService(ctx, "op", "s1", HostContainer, "id", func(string) {})
	assert.ErrorIs(t, err, ErrHostExecOperatorOnly, "no checker configured: refused")

	m := execManager(orch, fakeOperators{"op": true})
	assert.ErrorIs(t, m.ExecInService(ctx, "dev", "s1", HostContainer, "id", func(string) {}), ErrHostExecOperatorOnly)
	assert.Empty(t, orch.hostCmds)

	require.NoError(t, m.ExecInService(ctx, "op", "s1", HostContainer, "id", func(string) {}))
	assert.Equal(t, []string{"id"}, orch.hostCmds)
}

func TestExecInService_OnlyOwnContainers(t *testing.T) {
	orch := &fakeOrch{containers: []string{"paas-web-s1-blue"}}
	m := execManager(orch, nil)
	ctx := context.Background()

	for _, name := range []string{"paas-postgres", "paas-web-s1-blue; rm -rf /", "other"} {
		assert.ErrorIs(t, m.ExecInService(ctx, "dev", "s1", name, "ls", func(string) {}), ErrContainerNotInService, name)
	}
	assert.Empty(t, orch.execTarget)

	require.NoError(t, m.ExecInService(ctx, "dev", "s1", "paas-web-s1-blue", "ls", func(string) {}))
	assert.Equal(t, "paas-web-s1-blue", orch.execTarget)

	orch.execTarget = ""
	require.NoError(t, m.ExecInService(ctx, "dev", "s1", "", "ls", func(string) {}))
	assert.Equal(t, "paas-web-s1-blue", orch.execTarget, "empty picks the service's own container")
}

func TestStreamLogs_OnlyOwnContainers(t *testing.T) {
	orch := &fakeOrch{containers: []string{"paas-web-s1-blue"}}
	m := execManager(orch, nil)
	ctx := context.Background()

	assert.ErrorIs(t, m.StreamLogs(ctx, "s1", LogStreamOptions{Tail: 10, Container: "paas-postgres"}, func(string) {}), ErrContainerNotInService)
	assert.Empty(t, orch.logsFrom)
	require.NoError(t, m.StreamLogs(ctx, "s1", LogStreamOptions{Tail: 10, Container: "paas-web-s1-blue"}, func(string) {}))
	require.NoError(t, m.StreamLogs(ctx, "s1", LogStreamOptions{Tail: 10}, func(string) {}))
}

// stampedOrch writes Docker-timestamped lines, as both orchestrators do,
// and names the container a stream with no name follows.
type stampedOrch struct{ fakeOrch }

func (o *stampedOrch) ActiveContainer(context.Context, string, string) (string, bool) {
	return "paas-web-s1-blue", true
}
func (o *stampedOrch) StreamLogs(_ context.Context, _ string, _ int, name string, logFn func(string)) error {
	o.logsFrom = name
	logFn("2026-10-14T14:02:11.204123456Z GET /health 200")
	logFn("no timestamp here")
	return nil
}

func TestStreamLogs_TimestampsAndContainer(t *testing.T) {
	orch := &stampedOrch{fakeOrch{containers: []string{"paas-web-s1-blue"}}}
	m := execManager(&orch.fakeOrch, nil)
	m.WithOrchestration(fakeServers{}, reconcilerFactory{orch})
	ctx := context.Background()

	var lines []string
	var followed string
	opts := LogStreamOptions{Tail: 10, OnContainer: func(n string, running bool) {
		followed = n
		assert.True(t, running)
	}}
	require.NoError(t, m.StreamLogs(ctx, "s1", opts, func(l string) { lines = append(lines, l) }))
	assert.Equal(t, []string{"GET /health 200", "no timestamp here"}, lines, "untimed by default, as before")
	assert.Equal(t, "paas-web-s1-blue", followed)
	assert.Equal(t, "paas-web-s1-blue", orch.logsFrom, "the stream follows the container it named")

	lines = nil
	opts.Timestamps = true
	require.NoError(t, m.StreamLogs(ctx, "s1", opts, func(l string) { lines = append(lines, l) }))
	assert.Equal(t, "2026-10-14T14:02:11.204123456Z GET /health 200", lines[0])
}

// exitedOrch is a one-shot job: it runs when the stream opens and has
// exited by the time its log ends.
type exitedOrch struct{ fakeOrch }

func (o *exitedOrch) ContainerRunning(context.Context, string, string, string) bool { return false }

func TestStreamLogs_ExitedContainerIsToldNotRunning(t *testing.T) {
	orch := &exitedOrch{fakeOrch{containers: []string{"umami-2"}}}
	m := execManager(&orch.fakeOrch, nil)
	m.WithOrchestration(fakeServers{}, reconcilerFactory{orch})

	var told []bool
	opts := LogStreamOptions{Tail: 10, Container: "umami-2", OnContainer: func(_ string, running bool) { told = append(told, running) }}
	require.NoError(t, m.StreamLogs(context.Background(), "s1", opts, func(string) {}))
	assert.Equal(t, []bool{true, false}, told, "followed, then finished when the log ended")
}

func TestStripLogTimestamp(t *testing.T) {
	assert.Equal(t, "msg", StripLogTimestamp("2026-10-14T14:02:11Z msg"))
	assert.Equal(t, "a b", StripLogTimestamp("2026-10-14T14:02:11.000000001Z a b"))
	assert.Equal(t, "2026/10/14 14:02:01 [warn] x", StripLogTimestamp("2026/10/14 14:02:01 [warn] x"), "an app's own time stays")
	assert.Equal(t, "", StripLogTimestamp(""))
}

// ─── Handler ──────────────────────────────────────────────────────────────────

func newTestRouter(m *ServiceManager, role string) *gin.Engine {
	return newTestRouterIn(m, role, "org-a")
}

// newTestRouterIn is newTestRouter with orgID as the caller's active workspace.
func newTestRouterIn(m *ServiceManager, role, orgID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("userID", "dev")
		c.Set("orgID", orgID)
		c.Set("memberRole", role)
		c.Next()
	})
	h := NewHandler(m, nil, nil, nil, nil, zap.NewNop())
	h.RegisterRoutes(g)
	return r
}

func TestHandler_UpdateConfig_PolicyViolationIs422(t *testing.T) {
	repo := &fakeRepo{svc: &Service{ID: "s1", Name: "web", SourceType: SourceTypeDockerCompose}}
	m := NewServiceManager(repo, zap.NewNop())
	m.WithPolicyChecker(&fakePolicy{decision: blockedDecision()})
	r := newTestRouter(m, "developer")

	body, _ := json.Marshal(UpdateConfigInput{ComposeContent: "services: {}"})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/services/s1/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	var resp struct {
		Error string                     `json:"error"`
		Data  composespec.ViolationsBody `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, composespec.ViolationCode, resp.Data.Code)
	assert.Equal(t, "services.web.privileged", resp.Data.Violations[0].Field)
	assert.Contains(t, resp.Error, "services.web.privileged")
}

func TestHandler_ExecHostShellIs403ForNonOperators(t *testing.T) {
	orch := &fakeOrch{}
	m := execManager(orch, fakeOperators{"op": true})
	r := newTestRouter(m, "owner")

	body, _ := json.Marshal(map[string]string{"container": HostContainer, "command": "id"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/services/s1/exec", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Contains(t, w.Header().Get("Content-Type"), "application/json")
	assert.Empty(t, orch.hostCmds)
}

// The host's shell needs the operator's password confirmed (D-53); a
// container's shell does not.
func TestHandler_ExecHostShellNeedsTheConfirmedPassword(t *testing.T) {
	orch := &fakeOrch{}
	m := execManager(orch, fakeOperators{"op": true})
	serve := func(container string, reauthenticated bool) *httptest.ResponseRecorder {
		gin.SetMode(gin.TestMode)
		r := gin.New()
		g := r.Group("/api/v1")
		g.Use(func(c *gin.Context) {
			c.Set("userID", "op")
			c.Set("orgID", "org-a")
			c.Set("memberRole", "owner")
			if reauthenticated {
				c.Set("reauthenticated", true)
			}
			c.Next()
		})
		NewHandler(m, nil, nil, nil, nil, zap.NewNop()).RegisterRoutes(g)
		body, _ := json.Marshal(map[string]string{"container": container, "command": "id"})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/services/s1/exec", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	w := serve(HostContainer, false)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), `"code":"reauth_required"`)
	assert.Empty(t, orch.hostCmds)

	w = serve(HostContainer, true)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, []string{"id"}, orch.hostCmds)

	w = serve("", false)
	assert.Equal(t, http.StatusOK, w.Code, "a container's shell needs no confirmation")
	assert.NotContains(t, w.Body.String(), "reauth_required")
}

// activeOrch picks the container a stream with no name follows; "" is none.
type activeOrch struct {
	fakeOrch
	active string
}

func (o *activeOrch) ActiveContainer(context.Context, string, string) (string, bool) {
	return o.active, o.active != ""
}

type downFactory struct{}

func (downFactory) ForServer(*serverPkg.Server) (ContainerOrchestrator, func(), error) {
	return nil, nil, errors.New(`SSH connect to server "edge" (203.0.113.7): i/o timeout`)
}

// The page needs to tell "no container" and "can't reach the server" from a
// failed read, with no server address in the event (D-39).
func TestHandler_LiveLogsSaysWhyItCantFollow(t *testing.T) {
	repo := &fakeRepo{svc: &Service{ID: "s1", Name: "web"}}
	serve := func(f ContainerOrchestratorFactory) string {
		m := NewServiceManager(repo, zap.NewNop())
		m.WithOrchestration(fakeServers{}, f)
		w := httptest.NewRecorder()
		newTestRouter(m, "viewer").ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/services/s1/logs/live", nil))
		return w.Body.String()
	}

	none := &activeOrch{}
	body := serve(reconcilerFactory{none})
	assert.Contains(t, body, "event: error\ndata: no_container: ")
	assert.Contains(t, body, "event: done")
	assert.Empty(t, none.logsFrom, "nothing to follow")

	body = serve(downFactory{})
	assert.Contains(t, body, "event: error\ndata: server_unreachable: ")
	assert.NotContains(t, body, "203.0.113.7", "a workspace using a shared server is not shown its address")
}

func TestHandler_LiveLogsForeignContainerIs403(t *testing.T) {
	orch := &fakeOrch{containers: []string{"paas-web-s1-blue"}}
	r := newTestRouter(execManager(orch, nil), "viewer")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/services/s1/logs/live?container=paas-postgres", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Empty(t, orch.logsFrom)

	req = httptest.NewRequest(http.MethodGet, "/api/v1/services/s1/logs/live?container=paas-web-s1-blue", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "data: line")
	assert.Equal(t, "paas-web-s1-blue", orch.logsFrom)
}

// Another workspace's member, owner or not, reads none of the service's logs.
func TestHandler_LiveLogsOfAnotherWorkspaceIs404(t *testing.T) {
	orch := &fakeOrch{containers: []string{"paas-web-s1-blue"}}
	r := newTestRouterIn(execManager(orch, nil), "owner", "org-b")

	for _, path := range []string{"/api/v1/services/s1/logs/live", "/api/v1/services/s1/logs/live?container=paas-web-s1-blue"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, http.StatusNotFound, w.Code, path)
		assert.NotContains(t, w.Body.String(), "data: line", path)
	}
	assert.Empty(t, orch.logsFrom)
}

type healthTestOrch struct {
	fakeOrch
	asked []string
}

func (o *healthTestOrch) TestHealthCheck(_ context.Context, name, path string, port, _ int) (HealthTestResult, error) {
	o.asked = append(o.asked, fmt.Sprintf("%s %d%s", name, port, path))
	return HealthTestResult{Status: 200, Millis: 4}, nil
}

func TestHandler_HealthCheckTest(t *testing.T) {
	orch := &healthTestOrch{fakeOrch: fakeOrch{containers: []string{"paas-web-s1-blue"}}}
	m := NewServiceManager(&fakeRepo{svc: &Service{ID: "s1", Name: "web"}}, zap.NewNop())
	m.WithOrchestration(fakeServers{}, reconcilerFactory{orch})
	post := func(r http.Handler, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/services/s1/health-check/test", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	w := post(newTestRouter(m, "developer"), `{"path":"/api/health","port":4000}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.JSONEq(t, `{"data":{"status":200,"ms":4}}`, w.Body.String())
	assert.Equal(t, []string{"paas-web-s1-blue 4000/api/health"}, orch.asked)

	assert.Equal(t, http.StatusBadRequest, post(newTestRouter(m, "developer"), `{"path":"health","port":4000}`).Code, "a path without /")
	assert.Equal(t, http.StatusBadRequest, post(newTestRouter(m, "developer"), `{"path":"/","port":70000}`).Code)
	assert.Equal(t, http.StatusForbidden, post(newTestRouter(m, "viewer"), `{"path":"/","port":80}`).Code)
	assert.Equal(t, http.StatusNotFound, post(newTestRouterIn(m, "owner", "org-b"), `{"path":"/","port":80}`).Code, "another workspace")
	assert.Len(t, orch.asked, 1, "nothing refused reached the container")
}
