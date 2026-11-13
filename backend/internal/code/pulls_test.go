package code

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/testenv"

	"github.com/Nickpap21/toft/backend/internal/apispec"
	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"github.com/Nickpap21/toft/backend/pkg/broker"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// git runs this test binary as its push hooks (the links hooksDir makes),
// as paas-git runs toft-git.
func TestMain(m *testing.M) {
	gitstore.HookMain()
	os.Exit(m.Run())
}

// hooksDir is a hooks directory whose pre-receive and post-receive are this
// test binary.
func hooksDir(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, h := range []string{"pre-receive", "post-receive"} {
		if err := os.Symlink(self, filepath.Join(dir, h)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// fakeAudit keeps each entry as "<actor> <action> <resource> <detail JSON>".
type fakeAudit struct{ entries []string }

func (f *fakeAudit) WriteAuditLogDetail(_ context.Context, _, actor, action, resource string, detail map[string]any) {
	b, _ := json.Marshal(detail)
	f.entries = append(f.entries, actor+" "+action+" "+resource+" "+string(b))
}

// TestPullRequests pushes with the real git client and works a pull request
// through the API: open, push to it, protection, conflicts, a moved head,
// squash and merge, comments; every JSON answer is checked against
// openapi.yaml.
func TestPullRequests(t *testing.T) {
	if _, err := gitstore.CheckVersion(context.Background()); err != nil {
		testenv.Skip(t, "needs git %d.%d or newer: %v", gitstore.MinVersion[0], gitstore.MinVersion[1], err)
	}
	gin.SetMode(gin.TestMode)
	sockDir, err := os.MkdirTemp("", "tp")
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
	target := &Target{ID: repoID, OrgID: "o1", ProjectID: "p1"}
	repo := &fakeRepo{
		targets: map[string]*Target{"acme/shop": target},
		stored:  []StoredRepo{{ID: repoID, Name: "shop", OrgSlug: "acme", OrgID: "o1", CreatedAt: time.Now()}},
		items:   map[string][]StoredItem{},
	}
	pub := &tellings{}
	svc := NewService(repo, gitstore.NewClient(sock), fakeMembers{"dev": {"o1": "developer"}, "own": {"o1": "owner"}}, func() string { return "panel.test" }, 2222, zap.NewNop()).WithPublisher(pub)

	r := gin.New()
	NewGitHTTP(svc, fakeKeys{"paas_dev": {UserID: "dev"}}, zap.NewNop()).RegisterRoutes(r, func(c *gin.Context) { c.Next() })
	as := func(userID, role string) gin.HandlerFunc {
		return func(c *gin.Context) {
			c.Set("userID", userID)
			c.Set("orgID", "o1")
			c.Set("memberRole", role)
			c.Next()
		}
	}
	NewHandler(svc, projectsIn{"p1": "o1"}, nil, zap.NewNop()).RegisterRoutes(r.Group("/api/v1", as("dev", "developer")))
	owner := gin.New()
	NewHandler(svc, projectsIn{"p1": "o1"}, nil, zap.NewNop()).RegisterRoutes(owner.Group("/api/v1", as("own", "owner")))
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
			apispec.Check(t, method, strings.SplitN(path, "?", 2)[0], w.Code, out)
		} else {
			apispec.Check(t, method, strings.SplitN(path, "?", 2)[0], w.Code, nil)
		}
		return w.Code, out
	}
	data := func(m map[string]any) map[string]any { d, _ := m["data"].(map[string]any); return d }

	env := gitEnv(t)
	work := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := run(t, env, dir, append([]string{"git"}, args...)...)
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(out)
	}
	gitFails := func(dir string, args ...string) string {
		t.Helper()
		out, err := run(t, env, dir, append([]string{"git"}, args...)...)
		if err == nil {
			t.Fatalf("git %v succeeded:\n%s", args, out)
		}
		return out
	}
	remote := strings.Replace(srv.URL, "http://", "http://git:paas_dev@", 1) + "/acme/shop.git"
	git(work, "clone", "-q", remote, "w")
	w := filepath.Join(work, "w")
	write := func(name, body string) { os.WriteFile(filepath.Join(w, name), []byte(body), 0o644) }
	write("a.txt", "1\n2\n3\n")
	git(w, "add", ".")
	git(w, "commit", "-qm", "base")
	git(w, "push", "-q", "origin", "HEAD:main")
	git(w, "switch", "-qc", "feat")
	write("b.txt", "b\n")
	git(w, "add", ".")
	git(w, "commit", "-qm", "add b")
	git(w, "push", "-q", "origin", "feat")

	// Open: refused for the same branch, a missing one, nothing to merge.
	for _, in := range []map[string]any{
		{"title": "x", "base": "main", "head": "main"},
		{"title": "x", "base": "main", "head": "nope"},
		{"title": " ", "base": "main", "head": "feat"},
	} {
		if code, out := call(r, "POST", base+"/pulls", in); code < 400 {
			t.Errorf("opened %v: %d %v", in, code, out)
		}
	}
	code, out := call(r, "POST", base+"/pulls", map[string]any{"title": "Add b", "body": "Adds **b**.", "base": "main", "head": "feat"})
	if code != 201 || data(out)["number"] != 1.0 || !strings.Contains(data(out)["html"].(string), "<strong>b</strong>") {
		t.Fatalf("open: %d %v", code, out)
	}
	if code, out := call(r, "POST", base+"/pulls", map[string]any{"title": "Again", "base": "main", "head": "feat"}); code != 409 || out["code"] != "pull_exists" {
		t.Errorf("a second open one for the same branches: %d %v", code, out)
	}
	if !strings.Contains(git(w, "ls-remote", "origin"), "refs/pull/1/head") {
		t.Error("refs/pull/1/head was not set")
	}

	// A push to the branch moves the pull request.
	write("b.txt", "b2\n")
	git(w, "commit", "-qam", "b2")
	git(w, "push", "-q", "origin", "feat")
	feat := git(w, "rev-parse", "HEAD")
	code, out = call(r, "GET", base+"/pulls/1", nil)
	cmp, _ := data(out)["compare"].(map[string]any)
	if code != 200 || data(out)["head_sha"] != feat || cmp["ahead"] != 2.0 || cmp["mergeable"] != true {
		t.Fatalf("after a push: %d %v", code, out)
	}
	if _, ok := data(out)["agent_branch"]; ok {
		t.Errorf("a person's branch has an agent: %v", data(out)["agent_branch"])
	}
	// An agent's head branch names the agent, its person and when the
	// branch's push password expires (an expired one still says when).
	expires := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	repo.agentBranches = map[string]*AgentBranch{repoID + "/feat": {RepoID: repoID, Name: "feat", AgentName: "Claude Code", UserName: "sam", ExpiresAt: expires}}
	_, out = call(r, "GET", base+"/pulls/1", nil)
	if ab, _ := data(out)["agent_branch"].(map[string]any); ab["agent"] != "Claude Code" || ab["person"] != "sam" || ab["password_expires_at"] != "2026-01-02T03:04:05Z" {
		t.Errorf("an agent's branch: %v", data(out)["agent_branch"])
	}
	repo.agentBranches = nil
	_, out = call(r, "GET", base+"/pulls/1/timeline", nil)
	if items, _ := out["data"].([]any); len(items) != 2 {
		t.Errorf("timeline after a push: %v", out)
	}
	// The push is the pull request's too, for webhooks and its preview.
	found := false
	for _, e := range repo.events {
		found = found || e.Kind == "pull_request" && e.Action == "pushed" && e.After == feat && e.Branch == "main" && e.Subject != ""
	}
	if !found {
		t.Errorf("no pull_request pushed event for %s: %+v", feat, repo.events)
	}

	// Protection: the owner turns it on; a direct push to main is refused,
	// another branch isn't.
	if code, out := call(r, "PATCH", base, map[string]any{"protect_default_branch": true}); code != 403 {
		t.Errorf("a developer changed protection: %d %v", code, out)
	}
	if code, out := call(owner, "PATCH", base, map[string]any{"protect_default_branch": true}); code != 200 || data(out)["protect_default_branch"] != true {
		t.Fatalf("protect: %d %v", code, out)
	}
	git(w, "switch", "-q", "main")
	write("a.txt", "1\nmain\n3\n")
	git(w, "commit", "-qam", "direct")
	if msg := gitFails(w, "push", "origin", "HEAD:main"); !strings.Contains(msg, "main is protected") {
		t.Errorf("a direct push to a protected main:\n%s", msg)
	}
	git(w, "push", "-q", "origin", "HEAD:clash")
	if msg := gitFails(w, "push", "origin", "HEAD:refs/heads/HEAD"); !strings.Contains(msg, "can't be called HEAD") {
		t.Errorf("a branch named HEAD:\n%s", msg)
	}

	// Protection is stored by the branch's name (L2). Moving the default
	// branch away, rewriting main and moving it back is refused at the first
	// step: it needs an owner's session and the password, so the owner's
	// API key is refused, and so is a session without the password.
	ownerKey := gin.New()
	NewHandler(svc, projectsIn{"p1": "o1"}, nil, zap.NewNop()).RegisterRoutes(ownerKey.Group("/api/v1", as("own", "owner"), func(c *gin.Context) { c.Set("apiKey", true); c.Next() }))
	if code, out := call(ownerKey, "PATCH", base, map[string]any{"default_branch": "clash"}); code != 403 || out["code"] != "session_required" {
		t.Errorf("an owner's API key moved the default branch: %d %v", code, out)
	}
	if code, out := call(owner, "PATCH", base, map[string]any{"default_branch": "clash"}); code != 403 || out["code"] != "reauth_required" {
		t.Errorf("an owner without the password moved the default branch: %d %v", code, out)
	}
	// HEAD moved by any other means: main still refuses a force-push and a
	// delete.
	ctx := context.Background()
	rewrite := git(w, "commit-tree", "-m", "rewrite", "HEAD^{tree}")
	moveHead := func(b string) {
		t.Helper()
		if err := svc.git.Write(ctx, repoID, "head", map[string]string{"branch": b}, nil); err != nil {
			t.Fatal(err)
		}
	}
	moveHead("clash")
	if msg := gitFails(w, "push", "--force", "origin", rewrite+":refs/heads/main"); !strings.Contains(msg, "main is protected") {
		t.Errorf("a force-push to main with HEAD moved:\n%s", msg)
	}
	if msg := gitFails(w, "push", "origin", ":main"); !strings.Contains(msg, "main is protected") {
		t.Errorf("deleting main with HEAD moved:\n%s", msg)
	}
	moveHead("main")
	// An owner who confirmed the password moves it, in the audit log, and
	// protection moves with it; then back.
	audit := &fakeAudit{}
	confirmed := gin.New()
	NewHandler(svc, projectsIn{"p1": "o1"}, audit, zap.NewNop()).RegisterRoutes(confirmed.Group("/api/v1", as("own", "owner"), func(c *gin.Context) { c.Set("reauthenticated", true); c.Next() }))
	if code, out := call(confirmed, "PATCH", base, map[string]any{"default_branch": "clash"}); code != 200 || data(out)["default_branch"] != "clash" || data(out)["protect_default_branch"] != true {
		t.Fatalf("a reauthenticated owner moving the default branch: %d %v", code, out)
	}
	want := `own repository.updated repository:` + repoID + ` {"default_branch":{"from":"main","to":"clash"},"name":"shop","protected_branch":"clash"}`
	if len(audit.entries) != 1 || audit.entries[0] != want {
		t.Errorf("audit log:\n%q\nwant\n%q", audit.entries, want)
	}
	if msg := gitFails(w, "push", "--force", "origin", rewrite+":refs/heads/clash"); !strings.Contains(msg, "clash is protected") {
		t.Errorf("a force-push to the protected default branch clash:\n%s", msg)
	}
	if code, out := call(confirmed, "PATCH", base, map[string]any{"default_branch": "main"}); code != 200 || data(out)["default_branch"] != "main" {
		t.Fatalf("moving it back: %d %v", code, out)
	}
	if msg := gitFails(w, "push", "--force", "origin", rewrite+":refs/heads/main"); !strings.Contains(msg, "main is protected") {
		t.Errorf("a force-push to main, protected again:\n%s", msg)
	}
	// Protected before 0080 (no name stored): a push reads HEAD's, as
	// before, and stores it. Served here, on the test's goroutine, which
	// owns target.
	target.ProtectedBranch = ""
	if _, err := svc.Serve(ctx, target, Actor{UserID: "dev"}, "receive-pack", "advertise", "", nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	if target.ProtectedBranch != "main" {
		t.Errorf("the backfill stored %q, want main", target.ProtectedBranch)
	}

	// A conflicting pull request can't merge.
	git(w, "switch", "-q", "feat")
	write("a.txt", "1\nfeat\n3\n")
	git(w, "commit", "-qam", "feat edits a")
	git(w, "push", "-q", "origin", "feat:edit-a")
	_, out = call(r, "POST", base+"/pulls", map[string]any{"title": "Edit a", "base": "clash", "head": "edit-a"})
	_, out = call(r, "GET", base+"/pulls/2", nil)
	cmp, _ = data(out)["compare"].(map[string]any)
	if cmp["mergeable"] != false || len(cmp["conflicts"].([]any)) != 1 {
		t.Errorf("conflicts: %v", out)
	}
	if code, out := call(r, "POST", base+"/pulls/2/merge", map[string]any{"method": "squash", "expected_head_sha": data(out)["head_sha"]}); code != 409 || out["code"] != "conflicts" {
		t.Errorf("merged a conflict: %d %v", code, out)
	}
	_, out = call(r, "GET", base+"/pulls?state=open", nil)
	if list, _ := out["data"].([]any); len(list) != 2 || list[0].(map[string]any)["conflicts"] != true {
		t.Errorf("open list: %v", out)
	}

	// Only a person merges (D-102): an owner's full-access API key is refused,
	// and an agent's calls carry the same mark.
	keyed := gin.New()
	NewHandler(svc, projectsIn{"p1": "o1"}, nil, zap.NewNop()).RegisterRoutes(keyed.Group("/api/v1", as("own", "owner"), func(c *gin.Context) { c.Set("apiKey", true); c.Next() }))
	if code, out := call(keyed, "POST", base+"/pulls/1/merge", map[string]any{"method": "squash", "expected_head_sha": feat}); code != 403 || out["code"] != "session_required" {
		t.Errorf("an API key merged: %d %v", code, out)
	}

	// Merge: a stale head is refused; then squash, deleting the branch.
	if code, out := call(r, "POST", base+"/pulls/1/merge", map[string]any{"method": "squash", "expected_head_sha": strings.Repeat("a", 40)}); code != 409 || out["code"] != "head_moved" {
		t.Errorf("a stale head: %d %v", code, out)
	}
	code, out = call(r, "POST", base+"/pulls/1/merge", map[string]any{"method": "squash", "expected_head_sha": feat, "delete_branch": true})
	if code != 200 || data(out)["state"] != "merged" || data(out)["merge_method"] != "squash" {
		t.Fatalf("merge: %d %v", code, out)
	}
	git(w, "fetch", "-q", "--prune", "origin")
	if log := git(w, "log", "-1", "--format=%an|%s", "origin/main"); log != "dev|Add b (!1)" {
		t.Errorf("the squash commit: %q", log)
	}
	if strings.Contains(git(w, "branch", "-r"), "origin/feat") {
		t.Error("feat was not deleted")
	}
	if e := repo.events[len(repo.events)-1]; e.Kind != "push" || e.Branch != "feat" || e.Before != feat || strings.Trim(e.After, "0") != "" || e.Deploys {
		t.Errorf("the deleted head's event: %+v", e.GitEvent)
	}
	code, out = call(r, "GET", base+"/pulls/1", nil)
	cmp, _ = data(out)["compare"].(map[string]any)
	if code != 200 || cmp == nil || len(cmp["files"].([]any)) != 1 {
		t.Errorf("a merged pull request's changes: %d %v", code, out)
	}
	if code, out := call(r, "GET", base+"/pulls/1/diff", nil); code != 200 || len(out["data"].([]any)) != 1 {
		t.Errorf("a merged pull request's diff: %d %v", code, out)
	}
	if code, out := call(r, "POST", base+"/pulls/1/merge", map[string]any{"method": "squash", "expected_head_sha": feat}); code != 409 || out["code"] != "pull_closed" {
		t.Errorf("merged twice: %d %v", code, out)
	}

	// The conversation: comment, edit your own, someone else's refused,
	// an owner deletes it; close and reopen.
	code, out = call(r, "POST", base+"/pulls/2/comments", map[string]any{"body": "Can we *fix* this?"})
	if code != 201 || !strings.Contains(data(out)["html"].(string), "<em>fix</em>") {
		t.Fatalf("comment: %d %v", code, out)
	}
	cid := data(out)["id"].(string)
	if code, _ := call(r, "PATCH", base+"/pulls/2/comments/"+cid, map[string]any{"body": "Fixed?"}); code != 200 {
		t.Errorf("edit own comment: %d", code)
	}
	if code, out := call(owner, "PATCH", base+"/pulls/2/comments/"+cid, map[string]any{"body": "x"}); code != 403 || out["code"] != "not_yours" {
		t.Errorf("edited someone else's comment: %d %v", code, out)
	}
	if code, _ := call(owner, "DELETE", base+"/pulls/2/comments/"+cid, nil); code != 204 {
		t.Errorf("an owner deletes a comment: %d", code)
	}
	if code, out := call(r, "PATCH", base+"/pulls/2", map[string]any{"state": "closed"}); code != 200 || data(out)["state"] != "closed" {
		t.Errorf("close: %d %v", code, out)
	}
	if code, out := call(r, "PATCH", base+"/pulls/2", map[string]any{"state": "open", "title": "Edit a, again"}); code != 200 || data(out)["state"] != "open" {
		t.Errorf("reopen: %d %v", code, out)
	}
	// Two opened, one merged, one closed and reopened: the workspace's tabs
	// heard each once, and nothing of the refused ones.
	if got, want := pub.take(), slices.Repeat([]broker.Event{broker.Changed("p1", "pulls")}, 5); !reflect.DeepEqual(got, want) {
		t.Errorf("told %+v, want %+v", got, want)
	}
	_, out = call(r, "GET", base+"/pulls/2/timeline", nil)
	kinds := []string{}
	for _, it := range out["data"].([]any) {
		kinds = append(kinds, it.(map[string]any)["kind"].(string))
	}
	if strings.Join(kinds, ",") != "pushed,closed,title_changed,reopened" {
		t.Errorf("timeline: %v", kinds)
	}

	// Repositories: create, a taken name, delete with the name; never the last.
	if code, out := call(r, "POST", "/api/v1/projects/p1/repositories", map[string]any{"name": "Shop-API.git"}); code != 201 || data(out)["name"] != "shop-api" {
		t.Errorf("create: %d %v", code, out)
	}
	if code, out := call(r, "POST", "/api/v1/projects/p1/repositories", map[string]any{"name": "shop"}); code != 409 || out["code"] != "name_taken" {
		t.Errorf("a taken name: %d %v", code, out)
	}
	if code, out := call(r, "POST", "/api/v1/projects/p1/repositories", map[string]any{"name": "-bad"}); code != 400 || out["code"] != "bad_name" {
		t.Errorf("a bad name: %d %v", code, out)
	}
	if code, _ := call(owner, "DELETE", base+"?confirm=shop", nil); code != 403 {
		t.Errorf("deleted without the password: %d", code)
	}
	_, out = call(r, "GET", base, nil)
	if p := data(out)["pulls"].(map[string]any); p["open"] != 1.0 || p["merged"] != 1.0 {
		t.Errorf("counts: %v", p)
	}
	if code, out := call(r, "POST", "/api/v1/markdown", map[string]any{"text": "**hi** <script>x</script>"}); code != 200 || strings.Contains(data(out)["html"].(string), "<script") {
		t.Errorf("markdown preview: %d %v", code, out)
	}
}

// A merge keeps the repository's default branch and any branch a service
// deploys from: delete_branch on one is refused before anything merges,
// and the pull request says it stays. The executor is a stub, so this runs
// without a new enough git; TestPullRequests merges for real.
func TestMergeKeepsBranches(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sockDir, err := os.MkdirTemp("", "mk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "g.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("a", 40)
	var merges, deletions atomic.Int32
	executor := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/browse/summary"):
			json.NewEncoder(w).Encode(gitstore.Summary{DefaultBranch: "main"})
		case strings.HasSuffix(r.URL.Path, "/browse/compare"):
			json.NewEncoder(w).Encode(gitstore.Compare{Base: gitstore.Ref{Name: "release", Kind: "branch", SHA: head}, Head: gitstore.Ref{Kind: "branch", SHA: head}, Ahead: 1, Mergeable: true,
				Conflicts: []string{}, Commits: []gitstore.Commit{}, Files: []gitstore.FileChange{}})
		case strings.HasSuffix(r.URL.Path, "/merge"):
			var m gitstore.MergeRequest
			json.NewDecoder(r.Body).Decode(&m)
			merges.Add(1)
			if m.DeleteBranch {
				deletions.Add(1)
			}
			json.NewEncoder(w).Encode(gitstore.MergeResult{SHA: strings.Repeat("b", 40), Before: strings.Repeat("c", 40), MergeBase: strings.Repeat("c", 40), Head: head})
		default:
			http.NotFound(w, r)
		}
	})}
	go executor.Serve(ln)
	t.Cleanup(func() { executor.Close() })

	const repoID = "0b6f9c4e-2a51-4f0e-9d6e-6f0c2a7b9d11"
	pull := func(n int, head0 string) *StoredPull {
		return &StoredPull{ID: uuid.NewString(), RepoID: repoID, Number: n, Title: head0, AuthorID: "dev", AuthorName: "dev", Base: "release", Head: head0, HeadSHA: head, State: "open", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	}
	repo := &fakeRepo{
		stored:           []StoredRepo{{ID: repoID, Name: "shop", OrgSlug: "acme", CreatedAt: time.Now()}},
		pulls:            []*StoredPull{pull(1, "main"), pull(2, "staging"), pull(3, "feat")},
		items:            map[string][]StoredItem{},
		deployedBranches: []string{"staging"},
	}
	svc := NewService(repo, gitstore.NewClient(sock), fakeMembers{"dev": {"o1": "developer"}}, func() string { return "panel.test" }, 2222, zap.NewNop())
	r := gin.New()
	NewHandler(svc, projectsIn{"p1": "o1"}, nil, zap.NewNop()).RegisterRoutes(r.Group("/api/v1", func(c *gin.Context) {
		c.Set("userID", "dev")
		c.Set("orgID", "o1")
		c.Set("memberRole", "developer")
		c.Next()
	}))
	base := "/api/v1/projects/p1/repositories/" + repoID + "/pulls/"
	call := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		b, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, bytes.NewReader(b))
		r.ServeHTTP(w, req)
		out := map[string]any{}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s %s: %v\n%s", method, path, err, w.Body)
		}
		apispec.Check(t, method, path, w.Code, out)
		return w.Code, out
	}
	kept := func(n string) any {
		t.Helper()
		code, out := call("GET", base+n, nil)
		if code != 200 {
			t.Fatalf("GET %s: %d %v", n, code, out)
		}
		return out["data"].(map[string]any)["head_kept"]
	}
	merge := func(n string, del bool) (int, map[string]any) {
		t.Helper()
		return call("POST", base+n+"/merge", map[string]any{"method": "squash", "expected_head_sha": head, "delete_branch": del})
	}

	for _, n := range []string{"1", "2"} {
		if got := kept(n); got != true {
			t.Errorf("!%s head_kept = %v", n, got)
		}
		if code, out := merge(n, true); code != 409 || out["code"] != "head_kept" {
			t.Errorf("!%s merged and deleted: %d %v", n, code, out)
		}
	}
	if merges.Load() != 0 || repo.pulls[0].State != "open" || repo.pulls[1].State != "open" {
		t.Fatalf("a refused merge reached git: %d merges, states %s %s", merges.Load(), repo.pulls[0].State, repo.pulls[1].State)
	}

	// An ordinary head still deletes, and a kept one merges when it stays.
	if got := kept("3"); got != false {
		t.Errorf("!3 head_kept = %v", got)
	}
	if code, out := merge("3", true); code != 200 || deletions.Load() != 1 {
		t.Errorf("an ordinary head: %d %v, %d deleted", code, out, deletions.Load())
	}
	if code, out := merge("1", false); code != 200 || merges.Load() != 2 || deletions.Load() != 1 {
		t.Errorf("keeping main: %d %v, %d merges, %d deleted", code, out, merges.Load(), deletions.Load())
	}
}

// tellings records the events told to workspace o1's tabs.
type tellings struct {
	mu  sync.Mutex
	got []broker.Event
}

func (p *tellings) Publish(orgID string, e broker.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if orgID != "o1" {
		e.Type = "told to " + orgID
	}
	p.got = append(p.got, e)
}

func (p *tellings) take() []broker.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.got
	p.got = nil
	return out
}
