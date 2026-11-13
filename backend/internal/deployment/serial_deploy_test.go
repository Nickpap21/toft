package deployment_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/Nickpap21/toft/backend/internal/dbtx"
	"github.com/Nickpap21/toft/backend/internal/deployment"
	domainPkg "github.com/Nickpap21/toft/backend/internal/domain"
	envPkg "github.com/Nickpap21/toft/backend/internal/environment"
	"github.com/Nickpap21/toft/backend/internal/envvar"
	orgPkg "github.com/Nickpap21/toft/backend/internal/org"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
)

// A deploy refused after it took its turn doesn't end the line for the
// deploy that still runs, and a deploy that waited is not started once the
// panel is shutting down: its deployment stays pending.
func TestTheLineHoldsBehindARefusedDeployAndAtShutdown(t *testing.T) {
	repo := &mockRepo{createdID: "dep-1"}
	orch := &heldRollout{rolling: make(chan struct{}, 8), release: make(chan struct{})}
	svc := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
	in := deployment.TriggerInput{ServiceID: "svc-1", ServiceName: "web", SourceType: "docker-image", DockerImage: "nginx"}
	_, err := svc.Trigger(context.Background(), in)
	require.NoError(t, err)
	<-orch.rolling

	repo.createErr = deployment.ErrAlreadyDeployed
	_, err = svc.Trigger(context.Background(), in)
	require.ErrorIs(t, err, deployment.ErrAlreadyDeployed)
	repo.createErr = nil

	// An ID of its own, so that it counts as in flight until it has left its wait.
	repo.createdID = "dep-2"
	_, err = svc.Trigger(context.Background(), in)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		repo.mu.Lock()
		defer repo.mu.Unlock()
		return slices.Contains(repo.logMessages, "Waiting for this service's earlier deploy to finish...")
	}, 10*time.Second, 10*time.Millisecond, "the next deploy waits for the one that still runs")
	assert.Empty(t, orch.rolling, "only one deploy of the service rolls out at a time")

	svc.BeginShutdown()
	close(orch.release)
	require.Eventually(t, func() bool { return svc.InFlightCount() == 0 }, 10*time.Second, 10*time.Millisecond)
	assert.Empty(t, orch.rolling, "the deploy that waited was not started")
	repo.mu.Lock()
	defer repo.mu.Unlock()
	assert.Equal(t, []deployment.Status{deployment.StatusBuilding, deployment.StatusRunning}, repo.statusHistory, "only the first deploy ran")
}

// A service's deploys run one after the other: of two asked for at the same
// time, both are accepted, one runs and the other stays pending, its log
// saying what it waits for, until the first one's pipeline has ended.
func TestTwoDeploysOfAServiceRunOneAfterTheOther(t *testing.T) {
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
	deploys := deployment.NewService(deployment.NewPostgresRepository(db), &mockFactory{orch: orch}, &mockStatusUpdater{}, zap.NewNop())
	deploys.WithHashKey([]byte("k"))
	// Before the database is dropped: every pipeline has ended.
	t.Cleanup(func() {
		close(orch.release)
		require.Eventually(t, func() bool { return deploys.InFlightCount() == 0 }, 10*time.Second, 10*time.Millisecond)
	})
	audit := &audited{}
	deploys.WithDeployUseCase(mgr, nil, domainPkg.NewService(domainPkg.NewPostgresRepository(db), nil, zap.NewNop()), vars, vars,
		envPkg.NewService(envPkg.NewPostgresRepository(db)), audit)
	dh := deployment.NewHandler(deploys, mgr, zap.NewNop())
	sh := service.NewHandler(mgr, nil, anyEnvironment{}, audit, nil, zap.NewNop())

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
	post := func(path, body string) (int, map[string]any) {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1"+path, strings.NewReader(body)))
		var out struct {
			Data map[string]any `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out.Data
	}
	code, svc := post("/environments/"+envID+"/services",
		`{"name":"web","type":"web","source_type":"docker-image","docker_image":"nginx:1.27","port":80}`)
	require.Equal(t, http.StatusCreated, code)
	id := svc["id"].(string)

	// Two requests at the same time.
	ids := make([]string, 2)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code, d := post("/services/"+id+"/deployments", `{}`); assert.Equal(t, http.StatusAccepted, code) {
				ids[i] = d["id"].(string)
			}
		}()
	}
	wg.Wait()
	require.NotEmpty(t, ids[0])
	require.NotEmpty(t, ids[1])
	require.NotEqual(t, ids[0], ids[1], "each is a deployment of its own")

	rolling := func() {
		t.Helper()
		select {
		case <-orch.rolling:
		case <-time.After(10 * time.Second):
			t.Fatal("no deploy reached its rollout")
		}
	}
	count := func(status string) int {
		t.Helper()
		var n int
		require.NoError(t, db.QueryRow(ctx, `SELECT count(*) FROM deployments WHERE service_id = $1 AND status = $2`, id, status).Scan(&n))
		return n
	}

	// One is at its rollout; the other waits for it, and says so.
	rolling()
	var waiting string
	require.Eventually(t, func() bool {
		err := db.QueryRow(ctx, `SELECT d.id::text FROM deployments d JOIN deployment_logs l ON l.deployment_id = d.id
			WHERE d.service_id = $1 AND d.status = 'pending' AND l.message LIKE 'Waiting for %'`, id).Scan(&waiting)
		return err == nil
	}, 10*time.Second, 10*time.Millisecond, "the second deploy stays pending and its log says it waits")
	assert.Equal(t, 1, count("building"))
	assert.Empty(t, orch.rolling, "only one deploy of the service rolls out at a time")

	// The first one ends, and the one that waited runs.
	orch.release <- struct{}{}
	rolling()
	assert.Equal(t, 1, count("running"), "the first went live before the second began")
	var status string
	require.NoError(t, db.QueryRow(ctx, `SELECT status FROM deployments WHERE id = $1`, waiting).Scan(&status))
	assert.Equal(t, "building", status)
	orch.release <- struct{}{}
	require.Eventually(t, func() bool { return count("running") == 2 }, 10*time.Second, 10*time.Millisecond)

	// While the service moves to another server, a deploy and a rollback
	// of it are refused: either was aimed at the server it leaves.
	ended := deploys.HoldMove(id)
	for _, path := range []string{"/services/" + id + "/deployments", "/services/" + id + "/deployments/" + ids[0] + "/rollback"} {
		code, refusal := post(path, `{}`)
		assert.Equal(t, http.StatusConflict, code, path)
		assert.Equal(t, "service_moving", refusal["code"], path)
	}
	ended()
	assert.Equal(t, 2, count("running"), "neither refusal left a deployment")
}
