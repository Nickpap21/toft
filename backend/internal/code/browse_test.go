package code

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/testenv"

	"github.com/Nickpap21/toft/backend/internal/apispec"
	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// projectsIn: project → workspace.
type projectsIn map[string]string

func (p projectsIn) CheckOwnership(_ context.Context, projectID, orgID string) (bool, error) {
	return p[projectID] == orgID, nil
}

// TestBrowse reads a repository pushed with the real git client through the
// API, against the executor on a socket, and checks every answer against
// openapi.yaml.
func TestBrowse(t *testing.T) {
	if _, err := gitstore.CheckVersion(context.Background()); err != nil {
		testenv.Skip(t, "needs git %d.%d or newer: %v", gitstore.MinVersion[0], gitstore.MinVersion[1], err)
	}
	gin.SetMode(gin.TestMode)
	sockDir, err := os.MkdirTemp("", "tb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "g.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	executor := &http.Server{Handler: gitstore.NewExecutor(root).Handler()}
	go executor.Serve(ln)
	t.Cleanup(func() { executor.Close() })

	const repoID = "0b6f9c4e-2a51-4f0e-9d6e-6f0c2a7b9d11"
	repo := &fakeRepo{stored: []StoredRepo{{ID: repoID, Name: "shop", OrgSlug: "acme", CreatedAt: time.Now()}}}
	svc := NewService(repo, gitstore.NewClient(sock), fakeMembers{}, func() string { return "panel.test" }, 2222, zap.NewNop())
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) { c.Set("userID", "dev"); c.Set("orgID", "o1"); c.Next() })
	NewHandler(svc, projectsIn{"p1": "o1", "p2": "o2"}, nil, zap.NewNop()).RegisterRoutes(v1)
	base := "/api/v1/projects/p1/repositories/" + repoID

	get := func(path string) (int, map[string]any, *httptest.ResponseRecorder) {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		var out map[string]any
		if strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatalf("%s: %v\n%s", path, err, w.Body)
			}
			apispec.Check(t, http.MethodGet, strings.SplitN(path, "?", 2)[0], w.Code, out)
		}
		return w.Code, out, w
	}

	// Empty until the first push.
	if code, out, _ := get(base); code != 200 || out["data"].(map[string]any)["empty"] != true {
		t.Fatalf("empty repository: %d %v", code, out)
	}
	if code, out, _ := get(base + "/tree"); code != 404 || out["code"] != "empty" {
		t.Fatalf("tree of an empty repository: %d %v", code, out)
	}

	env := gitEnv(t)
	work := t.TempDir()
	write := func(name, body string) { os.WriteFile(filepath.Join(work, name), []byte(body), 0o644) }
	dir, _ := gitstore.RepoPath(root, repoID)
	write("README.md", "# Shop\n\n<script>alert(1)</script>\n\nSee [docs](docs.md).\n")
	write("logo.png", "\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR")
	write("page.svg", `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`)
	write("blob.bin", "a\x00b")
	for _, args := range [][]string{{"git", "init", "-q", "-b", "main"}, {"git", "add", "."}, {"git", "commit", "-qm", "first"}, {"git", "push", "-q", dir, "main"}} {
		if out, err := run(t, env, work, args...); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	sha, _ := run(t, env, work, "git", "rev-parse", "HEAD")
	sha = strings.TrimSpace(sha)

	checks := []struct {
		path string
		code int
	}{
		{base, 200},
		{base + "/branches", 200},
		{base + "/tags", 200},
		{base + "/tree", 200},
		{base + "/tree?ref=main&path=README.md", 404},
		{base + "/tree?path=../etc", 400},
		{base + "/tree?ref=a..b", 400},
		{base + "/blob?path=README.md", 200},
		{base + "/blob?path=nope", 404},
		{base + "/commits?limit=1", 200},
		{base + "/commits?cursor=xyz", 400},
		{base + "/commits/" + sha[:7], 200},
		{base + "/commits/" + sha + "/diff", 200},
		{base + "/commits/nothex", 400},
		{"/api/v1/projects/p2/repositories/" + repoID + "/tree", 404}, // another workspace's project
		{"/api/v1/projects/p1/repositories/0b6f9c4e-0000-4f0e-9d6e-6f0c2a7b9d11/tree", 404},
	}
	for _, c := range checks {
		if code, out, _ := get(c.path); code != c.code {
			t.Errorf("%s: %d, want %d: %v", c.path, code, c.code, out)
		}
	}
	_, tree, _ := get(base + "/tree")
	if html := tree["data"].(map[string]any)["readme"].(map[string]any)["html"].(string); strings.Contains(html, "<script") || !strings.Contains(html, `data-toft-path="docs.md"`) {
		t.Errorf("readme html %s", html)
	}
	_, commitsPage, _ := get(base + "/commits?limit=1")
	if len(commitsPage["data"].([]any)) != 1 {
		t.Errorf("commits %v", commitsPage)
	}

	// Raw: never something a browser runs.
	for p, want := range map[string]string{
		"README.md": "text/plain; charset=utf-8",
		"page.svg":  "text/plain; charset=utf-8",
		"logo.png":  "image/png",
		"blob.bin":  "application/octet-stream",
	} {
		code, _, w := get(base + "/raw?path=" + url.QueryEscape(p))
		h := w.Header()
		if code != 200 || h.Get("Content-Type") != want || h.Get("Content-Security-Policy") != "sandbox; default-src 'none'" ||
			h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Cross-Origin-Resource-Policy") != "same-origin" {
			t.Errorf("raw %s: %d %v", p, code, h)
		}
		if attach := strings.HasPrefix(h.Get("Content-Disposition"), "attachment"); attach != (want == "application/octet-stream") {
			t.Errorf("raw %s: Content-Disposition %q", p, h.Get("Content-Disposition"))
		}
	}
	if _, _, w := get(base + "/raw?path=page.svg"); !strings.Contains(w.Body.String(), "onload") {
		t.Errorf("raw page.svg body %q", w.Body)
	}
}
