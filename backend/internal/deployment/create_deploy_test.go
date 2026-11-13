package deployment_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/Nickpap21/toft/backend/internal/apispec"
	"github.com/Nickpap21/toft/backend/internal/dbtx"
	"github.com/Nickpap21/toft/backend/internal/deployment"
	domainPkg "github.com/Nickpap21/toft/backend/internal/domain"
	envPkg "github.com/Nickpap21/toft/backend/internal/environment"
	"github.com/Nickpap21/toft/backend/internal/envvar"
	orgPkg "github.com/Nickpap21/toft/backend/internal/org"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
)

// heldRollout holds every deploy at its rollout: a send on release lets one
// through, closing it lets all.
type heldRollout struct {
	mockOrchestrator
	rolling chan struct{}
	release chan struct{}
}

func (o *heldRollout) Rollout(ctx context.Context, spec rollout.Spec, routing deployment.Routing, hooks rollout.Hooks) rollout.Result {
	o.rolling <- struct{}{}
	<-o.release
	return o.mockOrchestrator.Rollout(ctx, spec, routing, hooks)
}

// heldLive holds a deploy's pipeline after it went live, in its last step.
type heldLive chan struct{}

func (h heldLive) DeployLive(context.Context, string)           { <-h }
func (h heldLive) DeployFailed(context.Context, string, string) {}

// audited records the audit log's actions.
type audited struct {
	mu      sync.Mutex
	actions []string
}

func (a *audited) WriteAuditLog(_ context.Context, _, _, action, _ string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.actions = append(a.actions, action)
}
func (a *audited) WriteAuditLogDetail(ctx context.Context, orgID, actorID, action, resource string, _ map[string]any) {
	a.WriteAuditLog(ctx, orgID, actorID, action, resource)
}
func (a *audited) count(action string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, got := range a.actions {
		if got == action {
			n++
		}
	}
	return n
}

type anyEnvironment struct{}

func (anyEnvironment) CheckOwnership(context.Context, string, string) (bool, error) { return true, nil }
func (anyEnvironment) GetName(context.Context, string) (string, error)              { return "production", nil }

// Create and deploy starts the deploy itself (D-164, finding 11), and a tab
// loaded before it did still deploys after its create, as that screen always
// has. While the create's deploy is pending or building and nothing it ships
// changed, that second call is answered with the same deployment: the
// service is deployed once. A deploy asked for once it went live, one that
// overrides something and one after a setting changed are deploys of their
// own.
func TestDeployAfterCreateAndDeployIsOneDeploy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.New(t)
	ctx := context.Background()
	keys := crypto.NewKeys("test-encryption-key", "")
	var orgID, userID, envID string
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('Founders') RETURNING id::text`).Scan(&orgID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`).Scan(&userID))
	require.NoError(t, db.QueryRow(ctx, `WITH p AS (INSERT INTO projects (org_id, user_id, name) VALUES ($1, $2, 'shop') RETURNING id)
		INSERT INTO environments (project_id, name) SELECT id, 'production' FROM p RETURNING id::text`, orgID, userID).Scan(&envID))

	vars := envvar.NewService(envvar.NewPostgresRepository(db, keys), zap.NewNop())
	mgr := service.NewServiceManager(service.NewPostgresRepository(db, keys), zap.NewNop())
	mgr.WithVariables(vars)
	mgr.WithTransactor(dbtx.NewRunner(db))
	orch := &heldRollout{rolling: make(chan struct{}, 8), release: make(chan struct{})}
	live := heldLive(make(chan struct{}))
	deploys := deployment.NewService(deployment.NewPostgresRepository(db), &mockFactory{orch: orch}, &mockStatusUpdater{}, zap.NewNop())
	deploys.WithHashKey([]byte("k"))
	deploys.WithShipper(live)
	// Before the database is dropped: every pipeline has ended.
	t.Cleanup(func() {
		close(orch.release)
		close(live)
		require.Eventually(t, func() bool { return deploys.InFlightCount() == 0 }, 10*time.Second, 10*time.Millisecond)
	})
	audit := &audited{}
	deploys.WithDeployUseCase(mgr, nil, domainPkg.NewService(domainPkg.NewPostgresRepository(db), nil, zap.NewNop()), vars, vars,
		envPkg.NewService(envPkg.NewPostgresRepository(db)), audit)
	dh := deployment.NewHandler(deploys, mgr, zap.NewNop())
	sh := service.NewHandler(mgr, nil, anyEnvironment{}, audit, nil, zap.NewNop())
	sh.WithFirstDeploys(dh)

	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("orgID", orgID)
		c.Set("userID", userID)
		c.Set("memberRole", orgPkg.RoleOwner)
		c.Next()
	})
	sh.RegisterRoutes(g)
	dh.RegisterRoutes(g)
	call := func(method, path, body string, want int) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body)))
		require.Equal(t, want, w.Code, w.Body.String())
		var out map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out), w.Body.String())
		apispec.Check(t, method, "/api/v1"+path, w.Code, out)
		return out["data"].(map[string]any)
	}
	// created is a service created with deploy: its ID and its first deploy's.
	created := func(name string) (id, first string) {
		t.Helper()
		svc := call(http.MethodPost, "/environments/"+envID+"/services",
			`{"name":"`+name+`","type":"web","source_type":"docker-image","docker_image":"nginx:1.27","port":80,"deploy":true}`, http.StatusCreated)
		return svc["id"].(string), svc["deployment"].(map[string]any)["id"].(string)
	}
	running := func() {
		t.Helper()
		select {
		case <-orch.rolling:
		case <-time.After(10 * time.Second):
			t.Fatal("the deploy never reached its rollout")
		}
	}
	deployments := func(serviceID string) int {
		t.Helper()
		var n int
		require.NoError(t, db.QueryRow(ctx, `SELECT count(*) FROM deployments WHERE service_id = $1`, serviceID).Scan(&n))
		return n
	}
	// The body the web client's deploy has always sent.
	const noOverrides = `{"commit_sha":"","image_tag":"","git_ref":"","git_ref_type":""}`

	// The old tab creates and deploys, then deploys.
	id, first := created("web")
	running()
	again := call(http.MethodPost, "/services/"+id+"/deployments?force=true", noOverrides, http.StatusAccepted)
	assert.Equal(t, first, again["id"], "the deploy after the create is the create's")
	assert.Equal(t, 1, deployments(id), "the service is deployed once")
	assert.Equal(t, 1, audit.count("deployment.triggered"))

	// Once that deploy is live, a deploy is a new one, though the first
	// one's pipeline still runs its last step (held in live), which the new
	// one's waits for: a service's deploys run one after the other.
	orch.release <- struct{}{}
	require.Eventually(t, func() bool {
		var status string
		require.NoError(t, db.QueryRow(ctx, `SELECT status FROM deployments WHERE id = $1`, first).Scan(&status))
		return status == string(deployment.StatusRunning)
	}, 10*time.Second, 10*time.Millisecond, "the first deploy goes live")
	later := call(http.MethodPost, "/services/"+id+"/deployments", noOverrides, http.StatusAccepted)
	assert.NotEqual(t, first, later["id"], "the first deploy is over: a new one")
	live <- struct{}{}
	running()

	// While the create's deploy runs, a body that overrides something is a
	// deploy of its own, which waits for the create's.
	id, first = created("api")
	running()
	other := call(http.MethodPost, "/services/"+id+"/deployments", `{"commit_sha":"0123456789abcdef0123456789abcdef01234567"}`, http.StatusAccepted)
	assert.NotEqual(t, first, other["id"])

	// So is one after its settings changed: it ships them.
	require.NoError(t, vars.Seed(ctx, id, userID, []envvar.UpsertInput{{Key: "LOG_LEVEL", Value: "debug"}}))
	changed := call(http.MethodPost, "/services/"+id+"/deployments", noOverrides, http.StatusAccepted)
	assert.NotEqual(t, first, changed["id"], "what ships changed: a new deploy")
	assert.Equal(t, 3, deployments(id))
	assert.Equal(t, 5, audit.count("deployment.triggered"))

	// What refuses the create's deploy is the deploy route's answer, with
	// its code.
	_, err := dh.DeployCreated(ctx, service.DeployCaller{OrgID: orgID, UserID: userID, Role: orgPkg.RoleViewer}, id, false)
	assert.Equal(t, &service.DeployRefused{Message: "viewers cannot deploy"}, err)
	assert.Equal(t, 3, deployments(id))
}
