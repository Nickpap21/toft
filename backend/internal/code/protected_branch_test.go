package code

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/Nickpap21/toft/backend/internal/apispec"
	"github.com/Nickpap21/toft/backend/internal/gitstore"
)

// TestRepositoryDetailShowsProtectedBranch: the detail carries the name the
// hook protects, which differs from default_branch after a rollback moved
// the default (L2); null while protection is off or has no name yet.
// The git executor is a stub that answers the summary, so no git is needed.
func TestRepositoryDetailShowsProtectedBranch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir, err := os.MkdirTemp("", "pb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "g.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	stub := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"default_branch":"new","empty":false,"branch_count":2,"tag_count":0}`))
	})}
	go stub.Serve(ln)
	t.Cleanup(func() { stub.Close() })

	const repoID = "0b6f9c4e-2a51-4f0e-9d6e-6f0c2a7b9d11"
	repo := &fakeRepo{stored: []StoredRepo{{ID: repoID, Name: "shop", OrgSlug: "acme", OrgID: "o1", CreatedAt: time.Now(), Protect: true, ProtectedBranch: "old"}}}
	svc := NewService(repo, gitstore.NewClient(sock), fakeMembers{"own": {"o1": "owner"}}, func() string { return "panel.test" }, 2222, zap.NewNop())
	r := gin.New()
	NewHandler(svc, projectsIn{"p1": "o1"}, nil, zap.NewNop()).RegisterRoutes(r.Group("/api/v1", func(c *gin.Context) {
		c.Set("userID", "own")
		c.Set("orgID", "o1")
		c.Set("memberRole", "owner")
		c.Next()
	}))

	path := "/api/v1/projects/p1/repositories/" + repoID
	protected := func() any {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		out := map[string]any{}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		apispec.Check(t, "GET", path, w.Code, out)
		d, _ := out["data"].(map[string]any)
		if _, ok := d["protected_branch"]; !ok {
			t.Fatalf("no protected_branch in %v", d)
		}
		return d["protected_branch"]
	}

	if got := protected(); got != "old" {
		t.Errorf("protected_branch = %v, want old (default_branch is new)", got)
	}
	repo.stored[0].ProtectedBranch = ""
	if got := protected(); got != nil {
		t.Errorf("protection on with no name stored: protected_branch = %v, want null", got)
	}
	repo.stored[0].Protect, repo.stored[0].ProtectedBranch = false, "old"
	if got := protected(); got != nil {
		t.Errorf("protection off: protected_branch = %v, want null", got)
	}
}
