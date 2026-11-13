package domain

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/Nickpap21/toft/backend/internal/testdb"
)

// denyAll counts ownership checks and owns nothing.
type denyAll struct{ checks int }

func (d *denyAll) CheckOwnership(context.Context, string, string) (bool, error) {
	d.checks++
	return false, nil
}

func TestViewersCannotChangeDomains(t *testing.T) {
	gin.SetMode(gin.TestMode)
	routes := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/services/s1/domains"},
		{http.MethodDelete, "/api/v1/services/s1/domains/d1"},
	}
	for _, role := range []string{"viewer", "developer"} {
		for _, rt := range routes {
			owner := &denyAll{}
			r := gin.New()
			g := r.Group("/api/v1")
			g.Use(func(c *gin.Context) { c.Set("orgID", "org-1"); c.Set("memberRole", role); c.Next() })
			NewHandler(nil, nil, owner, zap.NewNop()).RegisterRoutes(g)

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(rt.method, rt.path, strings.NewReader(`{}`)))
			want := http.StatusNotFound // another workspace's, as an unknown one
			if role == "viewer" {
				want = http.StatusForbidden
			}
			assert.Equal(t, want, w.Code, "%s %s %s", role, rt.method, rt.path)
			if role == "viewer" {
				assert.Zero(t, owner.checks, "a viewer is refused before anything is looked up: %s %s", rt.method, rt.path)
			} else {
				assert.Equal(t, 1, owner.checks, "a developer gets past the role check: %s %s", rt.method, rt.path)
			}
		}
	}
}

type ownAll struct{}

func (ownAll) CheckOwnership(context.Context, string, string) (bool, error) { return true, nil }

// A hostname another service already has is a 409, not a 500, whatever its
// case: names are saved in lower case.
func TestCreateTakenHostnameConflicts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.New(t)
	ctx := context.Background()
	var web, api string
	require.NoError(t, db.QueryRow(ctx, `WITH u AS (INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id),
		p AS (INSERT INTO projects (user_id, name) SELECT id, 'Shop' FROM u RETURNING id),
		e AS (INSERT INTO environments (project_id, name) SELECT id, 'production' FROM p RETURNING id),
		s AS (INSERT INTO services (environment_id, name, type, source_type, port) SELECT id, n, 'web', 'docker-image', 80 FROM e, (VALUES ('web'), ('api')) v(n) RETURNING id, name)
		SELECT (SELECT id::text FROM s WHERE name = 'web'), (SELECT id::text FROM s WHERE name = 'api')`).Scan(&web, &api))

	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) { c.Set("orgID", "org-1"); c.Set("memberRole", "developer"); c.Next() })
	NewHandler(NewService(NewPostgresRepository(db), routing{port: 80}, zap.NewNop()), nil, ownAll{}, zap.NewNop()).RegisterRoutes(g)
	post := func(serviceID, hostname string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/services/"+serviceID+"/domains", strings.NewReader(`{"hostname":"`+hostname+`"}`)))
		return w
	}

	require.Equal(t, http.StatusCreated, post(web, "app.example.com").Code)
	for _, c := range []struct{ service, hostname string }{{web, "app.example.com"}, {api, "App.Example.com"}} {
		w := post(c.service, c.hostname)
		assert.Equal(t, http.StatusConflict, w.Code, "%+v: %s", c, w.Body)
		assert.Contains(t, w.Body.String(), ErrHostnameTaken.Error())
		assert.Contains(t, w.Body.String(), `"code":"hostname_taken"`)
	}
}
