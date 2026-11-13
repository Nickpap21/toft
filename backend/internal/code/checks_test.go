package code

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type fakeConsumer struct {
	pushes []Push
	err    error
}

func (f *fakeConsumer) DeployPush(_ context.Context, p Push) error {
	f.pushes = append(f.pushes, p)
	return f.err
}

// TestDeployOnPushAndChecks pushes with the real git client and follows
// what D-77 adds: each moved branch in the events outbox, the dispatcher
// handing the newest per branch to the consumer and retrying a failure, a
// merge recording its own event, the checks API with its refusals, and the
// checks, deploys and pipelines the API shows, each answer checked against
// openapi.yaml.
func TestDeployOnPushAndChecks(t *testing.T) {
	if _, err := gitstore.CheckVersion(context.Background()); err != nil {
		testenv.Skip(t, "needs git %d.%d or newer: %v", gitstore.MinVersion[0], gitstore.MinVersion[1], err)
	}
	gin.SetMode(gin.TestMode)
	sockDir, err := os.MkdirTemp("", "tc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "g.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	executor := &http.Server{Handler: gitstore.NewExecutor(t.TempDir()).WithHooks(hooksDir(t)).Handler()}
	go executor.Serve(ln)
	t.Cleanup(func() { executor.Close() })

	const repoID = "0b6f9c4e-2a51-4f0e-9d6e-6f0c2a7b9d11"
	production := "production"
	repo := &fakeRepo{
		targets: map[string]*Target{"acme/shop": {ID: repoID, OrgID: "o1", ProjectID: "p1"}},
		stored:  []StoredRepo{{ID: repoID, Name: "shop", OrgSlug: "acme", CreatedAt: time.Now()}},
		items:   map[string][]StoredItem{},
		deployTo: []DeployTarget{
			{ServiceID: "s-web", ServiceName: "web", EnvironmentName: &production, Branch: "main", BaseDirectory: "web", DeployOnPush: true},
			{ServiceID: "s-api", ServiceName: "api", EnvironmentName: &production, Branch: "main", BaseDirectory: "/", DeployOnPush: true},
		},
	}
	consumer := &fakeConsumer{}
	svc := NewService(repo, gitstore.NewClient(sock), fakeMembers{"dev": {"o1": "developer"}, "own": {"o1": "owner"}, "eve": {"o1": "viewer"}},
		func() string { return "panel.test" }, 2222, zap.NewNop()).WithPushConsumer(consumer)

	r := gin.New()
	NewGitHTTP(svc, fakeKeys{"paas_dev": {UserID: "dev"}}, zap.NewNop()).RegisterRoutes(r, func(c *gin.Context) { c.Next() })
	router := func(userID, role string) http.Handler {
		g := r
		if userID != "dev" {
			g = gin.New()
		}
		NewHandler(svc, projectsIn{"p1": "o1"}, nil, zap.NewNop()).RegisterRoutes(g.Group("/api/v1", func(c *gin.Context) {
			c.Set("userID", userID)
			c.Set("orgID", "o1")
			c.Set("memberRole", role)
			c.Next()
		}))
		return g
	}
	dev, viewer := router("dev", "developer"), router("eve", "viewer")
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	base := "/api/v1/projects/p1/repositories/" + repoID
	call := func(h http.Handler, method, path string, body any) (int, map[string]any) {
		t.Helper()
		var b []byte
		if body != nil {
			b, _ = json.Marshal(body)
		}
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(w, req)
		out := map[string]any{}
		if w.Body.Len() > 0 {
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatalf("%s %s: %v\n%s", method, path, err, w.Body)
			}
		}
		apispec.Check(t, method, strings.SplitN(path, "?", 2)[0], w.Code, out)
		return w.Code, out
	}
	data := func(m map[string]any) map[string]any { d, _ := m["data"].(map[string]any); return d }

	env := gitEnv(t)
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := run(t, env, dir, append([]string{"git"}, args...)...)
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(out)
	}
	work := t.TempDir()
	git(work, "clone", "-q", strings.Replace(srv.URL, "http://", "http://git:paas_dev@", 1)+"/acme/shop.git", "w")
	w := filepath.Join(work, "w")
	write := func(name, body string) {
		os.MkdirAll(filepath.Join(w, filepath.Dir(name)), 0o755)
		os.WriteFile(filepath.Join(w, name), []byte(body), 0o644)
	}
	commit := func(msg string) string {
		git(w, "add", "-A")
		git(w, "commit", "-qm", msg)
		return git(w, "rev-parse", "HEAD")
	}
	ctx := context.Background()

	// A push writes one event per branch it moved, and the dispatcher hands
	// it to the consumer once.
	write("web/index.html", "hi\n")
	write("api/main.go", "package main\n")
	first := commit("First")
	git(w, "push", "-q", "origin", "HEAD:main")
	if len(repo.events) != 1 {
		t.Fatalf("events after the first push: %+v", repo.events)
	}
	if e := repo.events[0]; e.Kind != "push" || e.Branch != "main" || e.After != first || strings.Trim(e.Before, "0") != "" || e.Subject != "First" || e.ActorID != "dev" {
		t.Errorf("the first push's event: %+v", e)
	}
	svc.dispatch(ctx)
	if len(consumer.pushes) != 1 || consumer.pushes[0].SHA != first || consumer.pushes[0].Branch != "main" || consumer.pushes[0].ProjectID != "p1" {
		t.Fatalf("dispatched: %+v", consumer.pushes)
	}
	if !repo.events[0].dispatched || repo.events[0].msg != "" {
		t.Errorf("the first event after its dispatch: %+v", repo.events[0])
	}
	svc.dispatch(ctx)
	if len(consumer.pushes) != 1 {
		t.Errorf("a dispatched event went out again: %+v", consumer.pushes)
	}

	// Two pushes before a dispatch: only the newer deploys.
	write("web/index.html", "two\n")
	commit("Second")
	git(w, "push", "-q", "origin", "HEAD:main")
	write("web/index.html", "three\n")
	third := commit("Third")
	git(w, "push", "-q", "origin", "HEAD:main")
	if e := repo.events[2]; e.Commits != 1 || e.Forced {
		t.Errorf("the third push's event: %+v", e)
	}
	svc.dispatch(ctx)
	if len(consumer.pushes) != 2 || consumer.pushes[1].SHA != third {
		t.Fatalf("after two pushes: %+v", consumer.pushes)
	}
	if !strings.HasPrefix(repo.events[1].msg, "superseded") {
		t.Errorf("the older push wasn't superseded: %+v", repo.events[1])
	}

	// A consumer that fails is retried later, with its error kept meanwhile.
	consumer.err = errors.New("the database is down")
	write("api/main.go", "package main // 4\n")
	commit("Fourth")
	git(w, "push", "-q", "origin", "HEAD:main")
	svc.dispatch(ctx)
	if e := repo.events[3]; e.dispatched || e.msg != "the database is down" || e.attempts != 1 || !e.due.After(time.Now()) {
		t.Fatalf("a failed dispatch: %+v", e)
	}
	consumer.err, repo.events[3].due = nil, time.Time{}
	svc.dispatch(ctx)
	if e := repo.events[3]; !e.dispatched || e.msg != "" || len(consumer.pushes) != 4 {
		t.Errorf("the retry: %+v, %d pushes", e, len(consumer.pushes))
	}

	// The checks API.
	head := git(w, "rev-parse", "HEAD")
	checks := base + "/commits/" + head + "/checks"
	code, out := call(dev, "POST", checks, map[string]any{"name": "ci/test", "state": "running", "details_url": "https://ci.example/runs/1"})
	if code != 201 || data(out)["state"] != "running" || data(out)["name"] != "ci/test" {
		t.Fatalf("report a check: %d %v", code, out)
	}
	if code, out = call(dev, "POST", checks, map[string]any{"name": "ci/test", "state": "success", "description": "42 tests"}); code != 200 || data(out)["state"] != "success" {
		t.Errorf("report it again: %d %v", code, out)
	}
	for _, tc := range []struct {
		path string
		body map[string]any
		code int
		want string
	}{
		{checks, map[string]any{"name": "toft/deploy/production/web", "state": "success"}, 400, "reserved_name"},
		{checks, map[string]any{"name": "Toft/x", "state": "success"}, 400, "reserved_name"},
		{checks, map[string]any{"name": "ci", "state": "done"}, 400, "bad_state"},
		{checks, map[string]any{"name": "ci\nx", "state": "success"}, 400, "bad_check_name"},
		{checks, map[string]any{"name": "ci", "state": "success", "details_url": "javascript:alert(1)"}, 400, "bad_url"},
		{checks, map[string]any{"name": "ci", "state": "success", "description": strings.Repeat("x", 301)}, 400, "bad_description"},
		{base + "/commits/" + head[:7] + "/checks", map[string]any{"name": "ci", "state": "success"}, 400, "bad_sha"},
		{base + "/commits/" + strings.Repeat("ab", 20) + "/checks", map[string]any{"name": "ci", "state": "success"}, 404, "commit_not_found"},
	} {
		if code, out := call(dev, "POST", tc.path, tc.body); code != tc.code || out["code"] != tc.want {
			t.Errorf("report %v: %d %v, want %d %s", tc.body, code, out, tc.code, tc.want)
		}
	}
	if code, _ := call(viewer, "POST", checks, map[string]any{"name": "ci", "state": "success"}); code != 403 {
		t.Errorf("a viewer reported a check: %d", code)
	}
	svc.ReportDeploy(ctx, repoID, head, "production", "web", "running", "Deploying", "d-1", false)
	code, out = call(viewer, "GET", base+"/commits/"+head[:10]+"/checks", nil)
	cs, _ := data(out)["checks"].([]any)
	sum, _ := data(out)["summary"].(map[string]any)
	if code != 200 || data(out)["sha"] != head || len(cs) != 2 || sum["state"] != "pending" || sum["passed"] != 1.0 {
		t.Fatalf("a commit's checks: %d %v", code, out)
	}
	if c0, _ := cs[0].(map[string]any); c0["name"] != "toft/deploy/production/web" || c0["deployment"] == nil {
		t.Errorf("Toft's own check first: %v", cs[0])
	}
	_, out = call(viewer, "GET", base+"/commits?ref=main", nil)
	if items, _ := out["data"].([]any); len(items) == 0 || items[0].(map[string]any)["checks"].(map[string]any)["state"] != "pending" {
		t.Errorf("commits with their checks: %v", out)
	}
	_, out = call(viewer, "GET", base+"/branches", nil)
	if items, _ := out["data"].([]any); len(items) == 0 || items[0].(map[string]any)["checks"] == nil {
		t.Errorf("branches with their checks: %v", out)
	}
	_, out = call(viewer, "GET", base, nil)
	if ds, _ := data(out)["deploys"].([]any); len(ds) != 2 {
		t.Errorf("the repository's deploys: %v", out)
	}

	// A pull request shows what its merge deploys, and the merge records
	// its own event (its update-ref ran no hook).
	git(w, "switch", "-qc", "feat")
	write("web/about.html", "about\n")
	feat := commit("Add about")
	git(w, "push", "-q", "origin", "feat")
	svc.dispatch(ctx)
	if code, out = call(dev, "POST", base+"/pulls", map[string]any{"title": "About page", "base": "main", "head": "feat"}); code != 201 {
		t.Fatalf("open: %d %v", code, out)
	}
	_, out = call(viewer, "GET", base+"/pulls/1", nil)
	ds, _ := data(out)["deploys"].([]any)
	if len(ds) != 2 || ds[0].(map[string]any)["files_changed"] != 1.0 || ds[1].(map[string]any)["files_changed"] != 1.0 || data(out)["head_checks"] == nil {
		t.Fatalf("the pull request's deploys: %v", out)
	}
	n := len(repo.events)
	if code, out = call(dev, "POST", base+"/pulls/1/merge", map[string]any{"method": "squash", "expected_head_sha": feat, "delete_branch": true}); code != 200 {
		t.Fatalf("merge: %d %v", code, out)
	}
	// The merge's event, then the deleted head's, which deploys nothing.
	if len(repo.events) != n+2 || repo.events[n].Kind != "merge" || repo.events[n].Branch != "main" || repo.events[n].Before != head || repo.events[n].Subject != "About page (!1)" ||
		repo.events[n+1].Branch != "feat" || repo.events[n+1].Deploys {
		t.Fatalf("the merge's event: %+v", repo.events[n:])
	}
	svc.dispatch(ctx)
	if last := consumer.pushes[len(consumer.pushes)-1]; last.Kind != "merge" || last.PullNumber != 1 || last.ActorID != "dev" {
		t.Errorf("the merge dispatched: %+v", last)
	}

	// Pipelines: newest first, paged, with their checks.
	code, out = call(viewer, "GET", base+"/pipelines?limit=2", nil)
	items, _ := out["data"].([]any)
	if code != 200 || len(items) != 2 || out["next"] == nil || items[0].(map[string]any)["kind"] != "merge" || items[0].(map[string]any)["pull_number"] != 1.0 {
		t.Fatalf("pipelines: %d %v", code, out)
	}
	_, out = call(viewer, "GET", base+"/pipelines?branch=main&cursor="+out["next"].(string), nil)
	if items, _ := out["data"].([]any); len(items) == 0 || items[0].(map[string]any)["branch"] != "main" {
		t.Errorf("the next page: %v", out)
	}
	if code, out := call(viewer, "GET", base+"/pipelines?cursor=x", nil); code != 400 || out["code"] != "bad_cursor" {
		t.Errorf("a bad cursor: %d %v", code, out)
	}

	// A repository services build from stays.
	var inUse *RepoInUseError
	if _, err := svc.DeleteRepository(ctx, "p1", repoID, "shop"); !errors.As(err, &inUse) || strings.Join(inUse.Services, ",") != "web,api" {
		t.Errorf("deleting a repository in use: %v", err)
	}
}

func TestFilesUnder(t *testing.T) {
	files := []gitstore.FileChange{{Path: "web/a"}, {Path: "web2/b"}, {Path: "api/c", OldPath: "web/c"}, {Path: "README.md"}}
	for dir, want := range map[string]int{"": 4, "/": 4, "web": 2, "/web/": 2, "web2": 1, "api": 1, "nope": 0} {
		if got := filesUnder(files, dir); got != want {
			t.Errorf("filesUnder(%q) = %d, want %d", dir, got, want)
		}
	}
}

func TestSummarize(t *testing.T) {
	cs := func(states ...string) []CommitCheck {
		out := make([]CommitCheck, 0, len(states))
		for _, s := range states {
			out = append(out, CommitCheck{State: s})
		}
		return out
	}
	if summarize(nil) != nil {
		t.Error("no checks have a summary")
	}
	for _, tc := range []struct {
		states []string
		want   CheckSummary
	}{
		{[]string{"success", "skipped"}, CheckSummary{State: "success", Total: 2, Passed: 2}},
		{[]string{"success", "running", "pending"}, CheckSummary{State: "pending", Total: 3, Passed: 1, Pending: 2}},
		{[]string{"success", "running", "cancelled"}, CheckSummary{State: "failure", Total: 3, Passed: 1, Failed: 1, Pending: 1}},
		{[]string{"failure"}, CheckSummary{State: "failure", Total: 1, Failed: 1}},
	} {
		if got := summarize(cs(tc.states...)); *got != tc.want {
			t.Errorf("summarize(%v) = %+v, want %+v", tc.states, *got, tc.want)
		}
	}
	if n := DeployCheckName("", strings.Repeat("s", 120)); len([]rune(n)) != 100 || !strings.HasPrefix(n, "toft/deploy/s") {
		t.Errorf("a long deploy check name: %q", n)
	}
	if n := DeployCheckName("staging", "web"); n != "toft/deploy/staging/web" {
		t.Errorf("DeployCheckName = %q", n)
	}
}
