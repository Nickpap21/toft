package service_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/Nickpap21/toft/backend/internal/apispec"
	"github.com/Nickpap21/toft/backend/internal/dbtx"
	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/environment"
	"github.com/Nickpap21/toft/backend/internal/envvar"
	orgPkg "github.com/Nickpap21/toft/backend/internal/org"
	"github.com/Nickpap21/toft/backend/internal/project"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
)

// firstDeploys answers the first deploy a create asks for, and records it.
type firstDeploys struct {
	asked  []string // service IDs
	who    service.DeployCaller
	forced bool
	refuse error
}

func (f *firstDeploys) DeployCreated(_ context.Context, who service.DeployCaller, id string, force bool) (any, error) {
	f.asked, f.who, f.forced = append(f.asked, id), who, force
	if f.refuse != nil {
		return nil, f.refuse
	}
	return &deployment.Deployment{ID: "0b0f3a6e-0000-4000-8000-000000000001", ServiceID: id,
		Status: deployment.StatusPending, CreatedAt: time.Now()}, nil
}

// Create and deploy (D-164, finding 11). On every create route a catalog
// service is stored with its template's plain settings, one sent empty kept
// empty and those left out at their defaults, and the create starts the
// first deploy itself when asked to: once, as its caller, never otherwise. A
// deploy that is refused leaves the service created and says why.
func TestCreateSetsVariablesAndStartsTheFirstDeploy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.New(t)
	ctx := context.Background()
	keys := crypto.NewKeys("test-encryption-key", "")
	var orgID, userID, projectID, envID string
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('Founders') RETURNING id::text`).Scan(&orgID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`).Scan(&userID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO projects (org_id, user_id, name) VALUES ($1, $2, 'shop') RETURNING id::text`, orgID, userID).Scan(&projectID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, projectID).Scan(&envID))

	projects := project.NewService(project.NewPostgresRepository(db))
	projects.WithEnvCreator(defaultEnv{environment.NewService(environment.NewPostgresRepository(db))})
	projects.WithTransactor(dbtx.NewRunner(db))
	m := service.NewServiceManager(service.NewPostgresRepository(db, keys), zap.NewNop())
	m.WithAddresser(namedAddresser{})
	m.WithProjects(projects)
	m.WithVariables(envvar.NewService(envvar.NewPostgresRepository(db, keys), zap.NewNop()))
	m.WithTransactor(dbtx.NewRunner(db))
	deploys := &firstDeploys{}
	h := service.NewHandler(m, projects, ownEnv{}, noAudit{}, nil, zap.NewNop())
	h.WithFirstDeploys(deploys)
	ph := project.NewHandler(projects, noAudit{}, zap.NewNop())
	ph.WithInlineService(h)

	viaKey := false // the caller's API key, where one is used
	post := func(role, path, body string) (int, map[string]any) {
		t.Helper()
		r := gin.New()
		g := r.Group("/api/v1")
		g.Use(func(c *gin.Context) {
			c.Set("orgID", orgID)
			c.Set("userID", userID)
			c.Set("memberRole", role)
			c.Set("apiKey", viaKey)
			c.Next()
		})
		h.RegisterRoutes(g)
		ph.RegisterRoutes(g)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1"+path, strings.NewReader(body)))
		var out map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out), w.Body.String())
		apispec.Check(t, http.MethodPost, "/api/v1"+path, w.Code, out)
		return w.Code, out
	}
	// A Postgres from the catalog: POSTGRES_USER (postgres) and POSTGRES_DB
	// (app) are its plain settings, POSTGRES_PASSWORD its secret.
	postgres := func(name, extra string) string {
		return `{"name":"` + name + `","type":"web","source_type":"docker-image","docker_image":"postgres:16","port":5432,"addon_slug":"postgres"` + extra + `}`
	}
	variable := func(serviceID, key string) (value string, secret bool) {
		t.Helper()
		require.NoError(t, db.QueryRow(ctx, `SELECT value, is_secret FROM env_vars WHERE service_id = $1 AND key = $2`, serviceID, key).Scan(&value, &secret),
			"%s is stored", key)
		return value, secret
	}
	const cleared = `,"variables":[{"key":"POSTGRES_USER","value":""}]`

	routes := []struct {
		name, path string
		body       func(name, extra string) string
		service    func(out map[string]any) map[string]any
	}{
		{"environment", "/environments/" + envID + "/services", postgres, func(out map[string]any) map[string]any { return out["data"].(map[string]any) }},
		{"project", "/projects/" + projectID + "/shared-services", postgres, func(out map[string]any) map[string]any { return out["data"].(map[string]any) }},
		{"workspace", "/shared-services", postgres, func(out map[string]any) map[string]any { return out["data"].(map[string]any) }},
		{"new project", "/projects", func(name, extra string) string {
			return `{"name":"` + name + `","service":` + postgres(name, extra) + `}`
		},
			func(out map[string]any) map[string]any {
				return out["data"].(map[string]any)["service"].(map[string]any)
			}},
	}
	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			slug := strings.ReplaceAll(route.name, " ", "-")

			*deploys = firstDeploys{}
			code, out := post(orgPkg.RoleDeveloper, route.path, route.body(slug+"-deployed", cleared+`,"deploy":true`))
			require.Equal(t, http.StatusCreated, code, out)
			svc := route.service(out)
			id := svc["id"].(string)
			require.Equal(t, []string{id}, deploys.asked, "the create starts the first deploy, once")
			assert.Equal(t, service.DeployCaller{OrgID: orgID, UserID: userID, Role: orgPkg.RoleDeveloper}, deploys.who)
			assert.False(t, deploys.forced)
			require.Contains(t, svc, "deployment")
			assert.Equal(t, id, svc["deployment"].(map[string]any)["service_id"])
			assert.NotContains(t, svc, "deploy_error")
			user, secret := variable(id, "POSTGRES_USER")
			assert.Equal(t, "", user, "a setting sent empty stays empty")
			assert.False(t, secret)
			name, _ := variable(id, "POSTGRES_DB")
			assert.Equal(t, "app", name, "a setting left out gets its default")
			_, secret = variable(id, "POSTGRES_PASSWORD")
			assert.True(t, secret, "its secret is generated as before")

			*deploys = firstDeploys{}
			code, out = post(orgPkg.RoleDeveloper, route.path, route.body(slug+"-created", ``))
			require.Equal(t, http.StatusCreated, code, out)
			svc = route.service(out)
			assert.Empty(t, deploys.asked, "without deploy nothing is deployed")
			assert.NotContains(t, svc, "deployment")
			user, _ = variable(svc["id"].(string), "POSTGRES_USER")
			assert.Equal(t, "postgres", user, "the defaults are set without a deploy too")

			*deploys = firstDeploys{refuse: &service.DeployRefused{Message: "Its server is frozen", Code: "server_frozen"}}
			code, out = post(orgPkg.RoleOwner, route.path, route.body(slug+"-refused", `,"deploy":true,"force":true,"variables":[]`))
			require.Equal(t, http.StatusCreated, code, "a refused first deploy leaves the service created: %v", out)
			svc = route.service(out)
			assert.Equal(t, "Its server is frozen", svc["deploy_error"])
			assert.Equal(t, "server_frozen", svc["deploy_error_code"])
			assert.NotContains(t, svc, "deployment")
			assert.True(t, deploys.forced, "force goes to the first deploy")
			var n int
			require.NoError(t, db.QueryRow(ctx, `SELECT count(*) FROM services WHERE id = $1`, svc["id"]).Scan(&n))
			assert.Equal(t, 1, n)

			*deploys = firstDeploys{}
			code, out = post(orgPkg.RoleDeveloper, route.path, route.body(slug+"-twice",
				`,"deploy":true,"variables":[{"key":"POSTGRES_PASSWORD","value":"x"}],"secrets":[{"key":"POSTGRES_PASSWORD","value":"y"}]`))
			require.Equal(t, http.StatusBadRequest, code, "a key given as a variable and a secret is refused: %v", out)
			assert.Empty(t, deploys.asked)
			require.NoError(t, db.QueryRow(ctx, `SELECT count(*) FROM services WHERE name = $1`, slug+"-twice").Scan(&n))
			assert.Zero(t, n, "and nothing is created")

			// A panel tab loaded before the create deployed sends a catalog
			// service without its settings, saves them and deploys it itself:
			// that one is left to it. An API key's create is deployed.
			*deploys = firstDeploys{}
			code, out = post(orgPkg.RoleDeveloper, route.path, route.body(slug+"-old-tab", `,"deploy":true`))
			require.Equal(t, http.StatusCreated, code, out)
			svc = route.service(out)
			assert.Empty(t, deploys.asked, "the old tab deploys it")
			assert.NotContains(t, svc, "deployment")
			assert.NotContains(t, svc, "deploy_error")
			viaKey = true
			code, out = post(orgPkg.RoleDeveloper, route.path, route.body(slug+"-key", `,"deploy":true`))
			viaKey = false
			require.Equal(t, http.StatusCreated, code, out)
			assert.Equal(t, []string{route.service(out)["id"].(string)}, deploys.asked, "an API key's create is deployed")

			*deploys = firstDeploys{}
			code, _ = post(orgPkg.RoleViewer, route.path, route.body(slug+"-viewer", `,"deploy":true,"variables":[]`))
			require.Equal(t, http.StatusForbidden, code)
			assert.Empty(t, deploys.asked, "a viewer creates and deploys nothing")
		})
	}
}
