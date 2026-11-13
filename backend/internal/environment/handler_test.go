package environment

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
)

// denyAll counts ownership checks and owns nothing. Embedding Repository
// leaves every other method nil: reaching one fails the test.
type denyAll struct {
	Repository
	checks int
}

func (d *denyAll) CheckOwnership(context.Context, string, string) (bool, error) {
	d.checks++
	return false, nil
}

func TestViewersCannotChangeEnvironments(t *testing.T) {
	gin.SetMode(gin.TestMode)
	routes := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/projects/p1/environments"},
		{http.MethodPatch, "/api/v1/environments/e1"},
	}
	for _, role := range []string{"viewer", "developer"} {
		for _, rt := range routes {
			owner := &denyAll{}
			r := gin.New()
			g := r.Group("/api/v1")
			g.Use(func(c *gin.Context) { c.Set("orgID", "org-1"); c.Set("memberRole", role); c.Next() })
			NewHandler(NewService(owner), owner, zap.NewNop()).RegisterRoutes(g)

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(rt.method, rt.path, strings.NewReader(`{"name":"x"}`)))
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

func TestProjectOverviewChecksTheProjectsWorkspace(t *testing.T) {
	gin.SetMode(gin.TestMode)
	owner := &denyAll{}
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) { c.Set("orgID", "org-1"); c.Set("memberRole", "viewer"); c.Next() })
	NewHandler(NewService(owner), owner, zap.NewNop()).RegisterRoutes(g)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/projects/p1/overview", nil))
	assert.Equal(t, http.StatusNotFound, w.Code, "another workspace's project is refused before it is read")
	assert.Equal(t, 1, owner.checks)
}

// allowAll owns everything and records what Create and Update receive; a
// preview's environment when preview is set.
type allowAll struct {
	Repository
	created   CreateInput
	updated   UpdateInput
	preview   bool
	protected bool
	deleted   bool
	// raced: the protection changed after the handler read it.
	raced bool
}

func (a *allowAll) GetByID(_ context.Context, id string) (*Environment, error) {
	e := &Environment{ID: id, Name: "staging", Protected: a.protected}
	if a.preview {
		e.Preview = &PreviewOf{PullNumber: 42}
	}
	return e, nil
}
func (a *allowAll) Delete(context.Context, string) error { a.deleted = true; return nil }

func (a *allowAll) CheckOwnership(context.Context, string, string) (bool, error) { return true, nil }
func (a *allowAll) Create(_ context.Context, _ string, in CreateInput) (*Environment, error) {
	a.created = in
	return &Environment{Name: in.Name, Color: *in.Color, Protected: *in.Protected}, nil
}
func (a *allowAll) Update(_ context.Context, _ string, in UpdateInput) (*Environment, error) {
	a.updated = in
	if a.raced && in.WasProtected != nil {
		return nil, ErrProtectionChanged
	}
	return &Environment{}, nil
}

func TestEnvironmentColourAndProtection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	serve := func(repo Repository, method, path, body string) *httptest.ResponseRecorder {
		r := gin.New()
		g := r.Group("/api/v1")
		g.Use(func(c *gin.Context) {
			c.Set("orgID", "org-1")
			c.Set("memberRole", "owner")
			c.Set("reauthenticated", true)
			c.Next()
		})
		NewHandler(NewService(repo), repo.(*allowAll), zap.NewNop()).RegisterRoutes(g)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}

	t.Run("red is refused, saying which colours remain", func(t *testing.T) {
		repo := &allowAll{}
		w := serve(repo, http.MethodPost, "/api/v1/projects/p1/environments", `{"name":"ops","color":"red"}`)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "use amber, violet, blue, teal or gray")
		assert.Nil(t, repo.created.Color)
	})

	t.Run("a new environment is coloured and protected by its name", func(t *testing.T) {
		for name, want := range map[string]struct {
			color     string
			protected bool
		}{"production": {"gray", true}, "Prod": {"gray", true}, "staging": {"amber", false}, "qa": {"violet", false}, "dev": {"teal", false}, "preview": {"blue", false}, "ops": {"gray", false}} {
			repo := &allowAll{}
			w := serve(repo, http.MethodPost, "/api/v1/projects/p1/environments", `{"name":"`+name+`"}`)
			assert.Equal(t, http.StatusCreated, w.Code, name)
			assert.Equal(t, want.color, *repo.created.Color, name)
			assert.Equal(t, want.protected, *repo.created.Protected, name)
		}
	})

	t.Run("a colour given wins over the name", func(t *testing.T) {
		repo := &allowAll{}
		serve(repo, http.MethodPost, "/api/v1/projects/p1/environments", `{"name":"production","color":"blue","protected":false}`)
		assert.Equal(t, "blue", *repo.created.Color)
		assert.False(t, *repo.created.Protected)
	})

	t.Run("an unknown colour is refused", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, serve(&allowAll{}, http.MethodPost, "/api/v1/projects/p1/environments", `{"name":"x","color":"pink"}`).Code)
		assert.Equal(t, http.StatusBadRequest, serve(&allowAll{}, http.MethodPatch, "/api/v1/environments/e1", `{"color":"pink"}`).Code)
		w := serve(&allowAll{}, http.MethodPatch, "/api/v1/environments/e1", `{"color":"red"}`)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "use amber, violet, blue, teal or gray")
	})

	t.Run("a PATCH with nothing to change is refused, one field alone is applied", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, serve(&allowAll{}, http.MethodPatch, "/api/v1/environments/e1", `{}`).Code)
		repo := &allowAll{}
		assert.Equal(t, http.StatusOK, serve(repo, http.MethodPatch, "/api/v1/environments/e1", `{"protected":true}`).Code)
		assert.Nil(t, repo.updated.Name)
		assert.Nil(t, repo.updated.Color)
		assert.True(t, *repo.updated.Protected)
	})

	t.Run("a preview's environment is neither changed nor deleted by hand", func(t *testing.T) {
		repo := &allowAll{preview: true}
		assert.Equal(t, http.StatusConflict, serve(repo, http.MethodPatch, "/api/v1/environments/e1", `{"name":"x"}`).Code)
		w := serve(repo, http.MethodDelete, "/api/v1/environments/e1", "")
		assert.Equal(t, http.StatusConflict, w.Code)
		assert.Contains(t, w.Body.String(), "preview_environment")
		assert.Nil(t, repo.updated.Name)
		assert.False(t, repo.deleted)
	})
}

// noEnvironments is a project with no environment yet.
type noEnvironments struct{ allowAll }

func (*noEnvironments) ListByProject(context.Context, string) ([]Environment, error) { return nil, nil }

func TestANewProjectsProductionIsProtected(t *testing.T) {
	repo := &noEnvironments{}
	env, err := NewService(repo).EnsureDefault(context.Background(), "p1")
	require.NoError(t, err)
	assert.Equal(t, "production", env.Name)
	assert.Equal(t, "gray", env.Color)
	assert.True(t, env.Protected)
}

type auditLog []string

func (a *auditLog) WriteAuditLogDetail(_ context.Context, _, _, action, _ string, _ map[string]any) {
	*a = append(*a, action)
}

func TestLiftingProtectionIsTheOwnersWithThePassword(t *testing.T) {
	gin.SetMode(gin.TestMode)
	serve := func(role string, reauth bool, body string) (*httptest.ResponseRecorder, *auditLog) {
		repo := &allowAll{protected: true}
		audit := &auditLog{}
		r := gin.New()
		g := r.Group("/api/v1")
		g.Use(func(c *gin.Context) {
			c.Set("orgID", "org-1")
			c.Set("memberRole", role)
			c.Set("reauthenticated", reauth)
			c.Next()
		})
		NewHandler(NewService(repo), repo, zap.NewNop()).WithAudit(audit).RegisterRoutes(g)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPatch, "/api/v1/environments/e1", strings.NewReader(body)))
		return w, audit
	}

	w, audit := serve("developer", true, `{"protected":false}`)
	assert.Equal(t, http.StatusForbidden, w.Code, "a developer can't lift protection")
	assert.Empty(t, *audit)

	w, _ = serve("owner", false, `{"protected":false}`)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "reauth_required")

	w, audit = serve("owner", true, `{"protected":false}`)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, auditLog{"environment.protection_changed"}, *audit)

	w, _ = serve("developer", false, `{"color":"blue"}`)
	assert.Equal(t, http.StatusOK, w.Code, "other edits stay a developer's")
}

// Deleting an environment removes its services and their volumes: owners
// only, with the password and its name typed, as deleting a project
// (security close-out F5).
func TestDeletingAnEnvironmentIsTheOwnersWithThePasswordAndName(t *testing.T) {
	gin.SetMode(gin.TestMode)
	serve := func(role string, reauth bool, confirm string) (*httptest.ResponseRecorder, *allowAll, *auditLog) {
		repo, audit := &allowAll{}, &auditLog{}
		r := gin.New()
		g := r.Group("/api/v1")
		g.Use(func(c *gin.Context) {
			c.Set("orgID", "org-1")
			c.Set("memberRole", role)
			c.Set("reauthenticated", reauth)
			c.Next()
		})
		NewHandler(NewService(repo), repo, zap.NewNop()).WithAudit(audit).RegisterRoutes(g)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/v1/environments/e1?confirm="+confirm, nil))
		return w, repo, audit
	}

	for _, role := range []string{"viewer", "developer"} {
		w, repo, _ := serve(role, true, "staging")
		assert.Equal(t, http.StatusForbidden, w.Code, role)
		assert.False(t, repo.deleted, role)
	}
	// Another workspace's environment is not found, before the password is
	// asked for: reauth_required would say it exists.
	{
		owner := &denyAll{}
		r := gin.New()
		g := r.Group("/api/v1")
		g.Use(func(c *gin.Context) { c.Set("orgID", "org-1"); c.Set("memberRole", "owner"); c.Next() })
		NewHandler(NewService(owner), owner, zap.NewNop()).RegisterRoutes(g)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/v1/environments/e1?confirm=staging", nil))
		assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
		assert.Equal(t, 1, owner.checks)
	}

	w, repo, _ := serve("owner", false, "staging")
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "reauth_required")
	assert.False(t, repo.deleted)

	w, repo, _ = serve("owner", true, "Staging")
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "confirm_name")
	assert.False(t, repo.deleted)

	w, repo, audit := serve("owner", true, "staging")
	assert.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	assert.True(t, repo.deleted)
	assert.Equal(t, auditLog{"environment.deleted"}, *audit)
}

func TestAProtectionChangeThatRacesAnotherIsRefused(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &allowAll{raced: true}
	audit := &auditLog{}
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) { c.Set("orgID", "org-1"); c.Set("memberRole", "developer"); c.Next() })
	NewHandler(NewService(repo), repo, zap.NewNop()).WithAudit(audit).RegisterRoutes(g)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPatch, "/api/v1/environments/e1", strings.NewReader(`{"protected":true}`)))

	assert.Equal(t, http.StatusConflict, w.Code)
	require.NotNil(t, repo.updated.WasProtected, "the update applies only while the protection is what the handler read")
	assert.False(t, *repo.updated.WasProtected)
	assert.Empty(t, *audit, "nothing changed, so nothing is audited")
}
