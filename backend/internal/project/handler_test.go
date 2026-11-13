package project_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/project"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type fakeRepo struct{ projects map[string]*project.Project }

func (f *fakeRepo) List(context.Context, string) ([]project.Project, error) { return nil, nil }
func (f *fakeRepo) GetByID(_ context.Context, id string) (*project.Project, error) {
	return f.projects[id], nil
}
func (f *fakeRepo) Create(_ context.Context, orgID, _ string, in project.CreateProjectInput) (*project.Project, error) {
	for _, p := range f.projects {
		if p.Name == in.Name {
			return nil, project.ErrDuplicateName
		}
	}
	p := &project.Project{ID: "p-1", OrgID: orgID, Name: in.Name}
	f.projects[p.ID] = p
	return p, nil
}
func (f *fakeRepo) Update(context.Context, string, project.UpdateProjectInput) (*project.Project, error) {
	return nil, nil
}
func (f *fakeRepo) Delete(_ context.Context, id string) error { delete(f.projects, id); return nil }
func (f *fakeRepo) CheckOwnership(context.Context, string, string) (bool, error) {
	return true, nil
}

type entry struct {
	action, resource string
	detail           map[string]any
}

type fakeAudit struct{ entries []entry }

func (f *fakeAudit) WriteAuditLog(_ context.Context, _, _, action, resource string) {
	f.entries = append(f.entries, entry{action, resource, nil})
}
func (f *fakeAudit) WriteAuditLogDetail(_ context.Context, _, _, action, resource string, detail map[string]any) {
	f.entries = append(f.entries, entry{action, resource, detail})
}

// Creating and deleting a project are in the audit log, the deleted one
// with its name (Home's activity and the Audit log page name it).
func TestProjectCreateAndDeleteAreAudited(t *testing.T) {
	gin.SetMode(gin.TestMode)
	audit := &fakeAudit{}
	r := gin.New()
	g := r.Group("")
	g.Use(func(c *gin.Context) {
		c.Set("orgID", "org-1")
		c.Set("userID", "u-1")
		c.Set("memberRole", "owner")
		c.Set("reauthenticated", true)
		c.Next()
	})
	project.NewHandler(project.NewService(&fakeRepo{projects: map[string]*project.Project{}}), audit, zap.NewNop()).RegisterRoutes(g)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/projects", bytes.NewBufferString(`{"name":"storefront"}`)))
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/projects/p-1?confirm=storefront", nil))
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())

	assert.Equal(t, []entry{
		{"project.created", "project:p-1", map[string]any{"name": "storefront"}},
		{"project.deleted", "project:p-1", map[string]any{"name": "storefront"}},
	}, audit.entries)
}

// Deleting a project takes its repositories, issues and docs with it, so it
// asks what deleting one repository asks: an owner, the password, the name.
func TestProjectDeleteNeedsOwnerPasswordAndName(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name, role, confirm string
		reauth, viaKey      bool
		want                int
	}{
		{"developer", "developer", "storefront", true, false, http.StatusForbidden},
		{"owner without the password", "owner", "storefront", false, false, http.StatusForbidden},
		{"owner's API key", "owner", "storefront", false, true, http.StatusForbidden},
		{"owner, wrong name", "owner", "store", true, false, http.StatusBadRequest},
		{"owner, password and name", "owner", "storefront", true, false, http.StatusNoContent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepo{projects: map[string]*project.Project{"p-1": {ID: "p-1", OrgID: "org-1", Name: "storefront"}}}
			r := gin.New()
			g := r.Group("")
			g.Use(func(c *gin.Context) {
				c.Set("orgID", "org-1")
				c.Set("userID", "u-1")
				c.Set("memberRole", tc.role)
				c.Set("reauthenticated", tc.reauth)
				c.Set("apiKey", tc.viaKey)
				c.Next()
			})
			project.NewHandler(project.NewService(repo), &fakeAudit{}, zap.NewNop()).RegisterRoutes(g)

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/projects/p-1?confirm="+tc.confirm, nil))
			assert.Equal(t, tc.want, w.Code, w.Body.String())
			_, kept := repo.projects["p-1"]
			assert.Equal(t, tc.want != http.StatusNoContent, kept)
		})
	}
}

type listRepo struct{ fakeRepo }

func (listRepo) List(context.Context, string) ([]project.Project, error) {
	return []project.Project{{ID: "p-a", Name: "a"}, {ID: "p-b", Name: "b"}}, nil
}

// An agent connected to some projects lists those alone, over REST as
// through MCP (D-100); a session lists every project.
func TestProjectListNarrowsToTheAgentsProjects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	list := func(agent *middleware.AgentIdentity) string {
		r := gin.New()
		g := r.Group("")
		g.Use(func(c *gin.Context) {
			c.Set("orgID", "org-1")
			c.Set("userID", "u-1")
			if agent != nil {
				c.Set("agent", *agent)
			}
			c.Next()
		})
		project.NewHandler(project.NewService(&listRepo{}), &fakeAudit{}, zap.NewNop()).RegisterRoutes(g)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/projects", nil))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		return w.Body.String()
	}
	assert.Contains(t, list(nil), "p-b")
	assert.Contains(t, list(&middleware.AgentIdentity{AllProjects: true}), "p-b")
	got := list(&middleware.AgentIdentity{ProjectIDs: []string{"p-a"}})
	assert.Contains(t, got, "p-a")
	assert.NotContains(t, got, "p-b")
}

type fakeInline struct{ got []string }

func (f *fakeInline) CreateProjectWithService(c *gin.Context, in project.CreateProjectInput, raw json.RawMessage) {
	f.got = append(f.got, in.Name+" "+string(raw))
	c.Status(http.StatusCreated)
}

// A create with a first service (7g) goes to the service's handler, never
// for a viewer; a name the workspace has is 409 project_name_taken.
func TestProjectCreateWithServiceAndNameTaken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	call := func(role, body string, inline *fakeInline) *httptest.ResponseRecorder {
		r := gin.New()
		g := r.Group("")
		g.Use(func(c *gin.Context) {
			c.Set("orgID", "org-1")
			c.Set("userID", "u-1")
			c.Set("memberRole", role)
			c.Next()
		})
		repo := &fakeRepo{projects: map[string]*project.Project{"p-0": {ID: "p-0", Name: "storefront"}}}
		h := project.NewHandler(project.NewService(repo), &fakeAudit{}, zap.NewNop())
		h.WithInlineService(inline)
		h.RegisterRoutes(g)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/projects", bytes.NewBufferString(body)))
		return w
	}
	const withService = `{"name":"uptime-kuma","service":{"name":"uptime-kuma"}}`

	inline := &fakeInline{}
	require.Equal(t, http.StatusCreated, call("developer", withService, inline).Code)
	require.Equal(t, []string{`uptime-kuma {"name":"uptime-kuma"}`}, inline.got)

	inline = &fakeInline{}
	require.Equal(t, http.StatusForbidden, call("viewer", withService, inline).Code)
	require.Empty(t, inline.got)

	w := call("owner", `{"name":"storefront"}`, &fakeInline{})
	require.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), `"code":"project_name_taken"`)
}
