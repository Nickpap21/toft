package service_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/Nickpap21/toft/backend/internal/apispec"
	"github.com/Nickpap21/toft/backend/internal/deployment"
	orgPkg "github.com/Nickpap21/toft/backend/internal/org"
	"github.com/Nickpap21/toft/backend/internal/resources"
	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
)

// nearlyFull: qa-node's 3.7 GiB, 3328 MiB of it reserved by the workspace.
type nearlyFull struct{}

func (nearlyFull) GetCapacitySummaryFor(_ context.Context, serverID, _ string) (*server.CapacitySummary, error) {
	return &server.CapacitySummary{ServerID: serverID, ServerName: "qa-node", CapacityDiscovered: true,
		EffectiveCPUCores: 2, EffectiveMemoryMB: 3788, AllocatedMemoryMB: 3328}, nil
}

// ownersBypass: owners may deploy past the server's capacity.
type ownersBypass struct{}

func (ownersBypass) GetSettings(context.Context, string) (*orgPkg.OrgSettings, error) {
	return &orgPkg.OrgSettings{ResourceBypassMinRole: orgPkg.RoleOwner}, nil
}
func (ownersBypass) RoleIn(context.Context, string, string) (string, error) {
	return "", orgPkg.ErrNotFound
}

type ownEnv struct{}

func (ownEnv) CheckOwnership(context.Context, string, string) (bool, error) { return true, nil }
func (ownEnv) GetName(context.Context, string) (string, error)              { return "production", nil }

type ownServers struct{}

func (ownServers) CanUse(context.Context, string, string) (bool, error) { return true, nil }
func (ownServers) CheckReady(context.Context, string) error             { return nil }

type noAudit struct{}

func (noAudit) WriteAuditLog(context.Context, string, string, string, string) {}
func (noAudit) WriteAuditLogDetail(context.Context, string, string, string, string, map[string]any) {
}

// Create and deploy of a service that won't fit its server is refused before
// anything is created, with the deploy's readable problem, so a refused
// attempt never leaves a service reserving memory it never ran with. Force
// creates it for a role that may deploy past the gate; Create only is never
// refused for room.
func TestCreateAndDeployOverCapacityCreatesNothing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.New(t)
	ctx := context.Background()
	var orgID, userID, envID, serverID string
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('Founders') RETURNING id::text`).Scan(&orgID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`).Scan(&userID))
	require.NoError(t, db.QueryRow(ctx, `WITH p AS (INSERT INTO projects (org_id, user_id, name) VALUES ($1, $2, 'shop') RETURNING id)
		INSERT INTO environments (project_id, name) SELECT id, 'production' FROM p RETURNING id::text`, orgID, userID).Scan(&envID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO servers (user_id, org_id, name, ip_address, ssh_key) VALUES ($1, $2, 'qa-node', '203.0.113.10', 'k') RETURNING id::text`,
		userID, orgID).Scan(&serverID))

	m := service.NewServiceManager(service.NewPostgresRepository(db, crypto.NewKeys("test-encryption-key", "")), zap.NewNop())
	gate := deployment.NewService(nil, nil, nil, zap.NewNop())
	gate.WithCapacitySvc(nearlyFull{})
	gate.WithOrgSettingsSvc(ownersBypass{})
	m.WithCapacityGate(gate)
	res := resources.NewService(resources.NewPostgresRepository(db), zap.NewNop())
	require.NoError(t, res.EnsureGrace(ctx))
	m.WithResources(res)
	h := service.NewHandler(m, nil, ownEnv{}, noAudit{}, nil, zap.NewNop())
	h.WithServerChecker(ownServers{})

	post := func(role, body string) (int, map[string]any) {
		t.Helper()
		r := gin.New()
		g := r.Group("/api/v1")
		g.Use(func(c *gin.Context) {
			c.Set("orgID", orgID)
			c.Set("userID", userID)
			c.Set("memberRole", role)
			c.Next()
		})
		h.RegisterRoutes(g)
		w := httptest.NewRecorder()
		path := "/api/v1/environments/" + envID + "/services"
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		var out map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out), w.Body.String())
		apispec.Check(t, http.MethodPost, path, w.Code, out)
		return w.Code, out
	}
	create := func(role, extra string) (int, map[string]any) {
		t.Helper()
		return post(role, `{"name":"probe","type":"web","source_type":"docker-image","docker_image":"nginx:alpine","port":80,"memory_limit":"512m","server_id":"`+serverID+`"`+extra+`}`)
	}
	services := func() int {
		t.Helper()
		var n int
		require.NoError(t, db.QueryRow(ctx, `SELECT count(*) FROM services WHERE environment_id = $1`, envID).Scan(&n))
		return n
	}

	code, body := create(orgPkg.RoleOwner, `,"deploy":true`)
	require.Equal(t, http.StatusConflict, code, body)
	const msg = "Not enough room on qa-node: needs 512 MiB of memory, 460 MiB left of 3.7 GiB."
	assert.Equal(t, msg, body["detail"])
	assert.Equal(t, msg, body["error"])
	assert.Equal(t, "Conflict", body["title"])
	data := body["data"].(map[string]any)
	assert.Equal(t, "capacity_exceeded", data["code"])
	assert.Equal(t, true, data["can_bypass"])
	assert.Equal(t, 512.0, data["requested_memory_mb"])
	assert.Zero(t, services(), "a refused Create and deploy creates nothing")

	code, body = create(orgPkg.RoleDeveloper, `,"deploy":true,"force":true`)
	require.Equal(t, http.StatusConflict, code, body)
	assert.Equal(t, false, body["data"].(map[string]any)["can_bypass"])
	assert.Zero(t, services(), "force is no bypass for a role the workspace doesn't allow")

	code, body = create(orgPkg.RoleOwner, `,"deploy":true,"force":true`)
	require.Equal(t, http.StatusCreated, code, body)
	assert.Equal(t, 1, services(), "an owner creates it anyway")

	_, err := db.Exec(ctx, `DELETE FROM services WHERE environment_id = $1`, envID)
	require.NoError(t, err)
	code, body = create(orgPkg.RoleDeveloper, ``)
	require.Equal(t, http.StatusCreated, code, body)
	assert.Equal(t, 1, services(), "Create only is never refused for room")

	// A stack is refused for the default limits its compose services get
	// once it is created (nginx 512 MiB, postgres 1024 MiB), not let through
	// asking for none and then refused at its deploy.
	_, err = db.Exec(ctx, `DELETE FROM services WHERE environment_id = $1`, envID)
	require.NoError(t, err)
	code, body = post(orgPkg.RoleOwner, `{"name":"stack","type":"web","source_type":"docker-compose","port":80,"deploy":true,"server_id":"`+serverID+`",`+
		`"compose_content":"services:\n  web:\n    image: nginx:alpine\n  db:\n    image: postgres:16\n"}`)
	require.Equal(t, http.StatusConflict, code, body)
	assert.Equal(t, 1536.0, body["data"].(map[string]any)["requested_memory_mb"])
	assert.Zero(t, services(), "a stack that won't fit its defaults creates nothing")
}
