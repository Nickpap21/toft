package code

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/testenv"

	"github.com/Nickpap21/toft/backend/internal/apispec"
	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"github.com/Nickpap21/toft/backend/pkg/actor"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type allowAll struct{ calls []actor.Target }

func (a *allowAll) Check(_ context.Context, t actor.Target) error {
	a.calls = append(a.calls, t)
	return nil
}

// create_branch (D-102): a person gets a branch; an agent gets a branch and
// a credential for it alone; calling again on its own branch gives a new
// credential without asking the policy; a branch a service deploys from,
// another's branch or one that exists is refused.
func TestCreateBranch(t *testing.T) {
	if _, err := gitstore.CheckVersion(context.Background()); err != nil {
		testenv.Skip(t, "needs git: %v", err)
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
	repo := &fakeRepo{stored: []StoredRepo{{ID: repoID, Name: "shop", OrgSlug: "acme", CreatedAt: time.Now()}}, deployedBranches: []string{"release"},
		targets: map[string]*Target{"acme/shop": {ID: repoID, OrgID: "o1", ProjectID: "p1"}}}
	gate := &allowAll{}
	svc := NewService(repo, gitstore.NewClient(sock), fakeMembers{"dev": {"o1": "developer"}}, func() string { return "panel.test" }, 2222, zap.NewNop()).WithAgents(gate, nil)
	env := gitEnv(t)
	work := t.TempDir()
	dir, _ := gitstore.RepoPath(root, repoID)
	if err := gitstore.NewClient(sock).Init(context.Background(), repoID); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(work, "a.txt"), []byte("a"), 0o644)
	for _, args := range [][]string{{"git", "init", "-q", "-b", "main"}, {"git", "add", "."}, {"git", "commit", "-qm", "first"}, {"git", "push", "-q", dir, "main"}} {
		if out, err := run(t, env, work, args...); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}

	var agentConn string
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		c.Set("userID", "dev")
		c.Set("orgID", "o1")
		c.Set("memberRole", "developer")
		if agentConn != "" {
			c.Set("apiKey", true)
			c.Request = c.Request.WithContext(actor.WithAgent(c.Request.Context(), actor.Agent{ConnectionID: agentConn, Name: "Claude Code", UserID: "dev", OrgID: "o1"}))
		}
		c.Next()
	})
	NewHandler(svc, projectsIn{"p1": "o1"}, nil, zap.NewNop()).RegisterRoutes(v1)
	path := "/api/v1/projects/p1/repositories/" + repoID + "/branches"
	post := func(body string) (int, map[string]any) {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body)))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		apispec.Check(t, http.MethodPost, path, w.Code, out)
		return w.Code, out
	}

	if code, out := post(`{"name":"sam/copy"}`); code != 201 || out["data"].(map[string]any)["push"] != nil {
		t.Fatalf("a person's branch: %d %v", code, out)
	}
	agentConn = "conn-1"
	code, out := post(`{"name":"agent/fix","from":"main"}`)
	push, _ := out["data"].(map[string]any)["push"].(map[string]any)
	if code != 201 || !strings.HasPrefix(push["password"].(string), AgentBranchPrefix) || !strings.Contains(push["command"].(string), "HEAD:agent/fix") ||
		push["url"] != "https://panel.test/acme/shop.git" {
		t.Fatalf("an agent's branch: %d %v", code, out)
	}
	if len(gate.calls) != 1 || gate.calls[0].Tool != "create_branch" || gate.calls[0].ProjectID != "p1" {
		t.Fatalf("the policy saw %+v", gate.calls)
	}
	// Each new branch is a push to it; the agent's a key's (D-97).
	if len(repo.events) != 2 || repo.events[0].Branch != "sam/copy" || repo.events[0].WithKey ||
		repo.events[1].Branch != "agent/fix" || !repo.events[1].WithKey || repo.events[1].AgentID != "conn-1" || repo.events[1].Kind != "push" {
		t.Fatalf("the pushes recorded: %+v", repo.events)
	}
	first := push["password"].(string)
	if b, err := svc.AgentCredential(context.Background(), first); err != nil || b.Name != "agent/fix" {
		t.Fatalf("the credential: %+v %v", b, err)
	}
	// Again on its own branch: a new credential, no new branch, no policy.
	code, out = post(`{"name":"agent/fix"}`)
	if code != 201 {
		t.Fatalf("again: %d %v", code, out)
	}
	again := out["data"].(map[string]any)["push"].(map[string]any)["password"].(string)
	if code != 201 || again == first || len(gate.calls) != 1 {
		t.Fatalf("a new credential: %d %v %+v", code, out, gate.calls)
	}
	for body, want := range map[string]string{
		`{"name":"release"}`: "branch_deployed",
		`{"name":"main"}`:    "branch_exists",
		`{"name":"a..b"}`:    "bad_name",
	} {
		if code, out := post(body); code < 400 || out["code"] != want {
			t.Errorf("%s: %d %v", body, code, out)
		}
	}
	// Paused, the agent's git push says why: git shows a refusal only from
	// its first request, and only in text/plain.
	repo.agentBranches[repoID+"/agent/fix"].Paused = true
	gr := gin.New()
	NewGitHTTP(svc, fakeKeys{}, zap.NewNop()).RegisterRoutes(gr, func(c *gin.Context) { c.Next() })
	srv := httptest.NewServer(gr)
	t.Cleanup(srv.Close)
	os.WriteFile(filepath.Join(work, "b.txt"), []byte("b"), 0o644)
	if out, err := run(t, env, work, "git", "add", "."); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if out, err := run(t, env, work, "git", "commit", "-qm", "second"); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	pushURL := strings.Replace(srv.URL, "http://", "http://agent:"+again+"@", 1) + "/acme/shop.git"
	if out, err := run(t, env, work, "git", "push", pushURL, "HEAD:agent/fix"); err == nil || !strings.Contains(out, "remote: "+ErrAgentsPaused.Error()) {
		t.Errorf("a paused agent's push: %v\n%s", err, out)
	}

	agentConn = "conn-2"
	if code, out := post(`{"name":"agent/fix"}`); code != 403 || out["code"] != "not_agent_branch" {
		t.Errorf("another agent's branch: %d %v", code, out)
	}
}
