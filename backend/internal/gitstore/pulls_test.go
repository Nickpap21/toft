package gitstore

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/testenv"
)

// prFixture is a bare repository with main, a feature branch two commits
// ahead, and a conflicting branch, made with the real git client.
func prFixture(t *testing.T) (dir string) {
	t.Helper()
	if _, err := CheckVersion(context.Background()); err != nil {
		testenv.Skip(t, "needs git %d.%d or newer: %v", MinVersion[0], MinVersion[1], err)
	}
	dir, _ = RepoPath(t.TempDir(), "0b6f9c4e-2a51-4f0e-9d6e-6f0c2a7b9d11")
	if err := NewExecutor(filepath.Dir(dir)).init(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	w := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(w, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, w, "init", "-q", "-b", "main")
	write("a.txt", "1\n2\n3\n")
	gitIn(t, w, "add", ".")
	gitIn(t, w, "commit", "-qm", "base")
	gitIn(t, w, "switch", "-qc", "feat")
	write("b.txt", "new\n")
	gitIn(t, w, "add", ".")
	gitIn(t, w, "commit", "-qm", "add b")
	write("b.txt", "newer\n")
	gitIn(t, w, "commit", "-qam", "change b")
	gitIn(t, w, "switch", "-q", "main")
	gitIn(t, w, "switch", "-qc", "clash")
	write("a.txt", "1\nclash\n3\n")
	gitIn(t, w, "commit", "-qam", "clash")
	gitIn(t, w, "switch", "-q", "main")
	write("a.txt", "1\nmain\n3\n")
	gitIn(t, w, "commit", "-qam", "main moves")
	gitIn(t, w, "push", "-q", dir, "main", "feat", "clash")
	return dir
}

var ada = Identity{Name: "Ada", Email: "ada@example.com"}

func TestCompare(t *testing.T) {
	dir := prFixture(t)
	ctx := context.Background()
	c, err := compare(ctx, dir, url.Values{"base": {"main"}, "head": {"feat"}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Ahead != 2 || c.Behind != 1 || !c.Mergeable || c.FastForward || len(c.Commits) != 2 || c.Commits[0].Subject != "add b" {
		t.Errorf("compare feat: %+v", c)
	}
	if len(c.Files) != 1 || c.Files[0].Path != "b.txt" || c.Files[0].Status != "added" || c.Stats.Additions != 1 {
		t.Errorf("compare feat files: %+v %+v", c.Files, c.Stats)
	}
	c, err = compare(ctx, dir, url.Values{"base": {"main"}, "head": {"clash"}})
	if err != nil || c.Mergeable || len(c.Conflicts) != 1 || c.Conflicts[0] != "a.txt" {
		t.Errorf("compare clash: %+v, %v", c, err)
	}
	pg, err := compareDiff(ctx, dir, url.Values{"base": {"main"}, "head": {"feat"}})
	if err != nil || len(pg.Items) != 1 || !strings.Contains(pg.Items[0].Patch, "+newer") {
		t.Errorf("compare diff: %+v, %v", pg, err)
	}
	st, err := pullsStatus(ctx, dir, url.Values{"pair": {"main..feat", "main..clash", "main..gone"}})
	if err != nil || st[0].Ahead != 2 || *st[0].Conflicts || !*st[1].Conflicts || st[2].Conflicts != nil {
		t.Errorf("status: %+v, %v", st, err)
	}
}

func TestMergeMethods(t *testing.T) {
	ctx := context.Background()
	req := func(dir, method string) MergeRequest {
		head, _ := revParse(ctx, dir, "refs/heads/feat")
		return MergeRequest{Method: method, Base: "main", Head: "feat", ExpectedHead: head, PullNumber: 1,
			Message: "Merge feat\n", Author: Identity{Name: "Bo", Email: "bo@example.com"}, Committer: ada}
	}

	// Squash: one commit on main with the merged tree, authored by the
	// pull request's author; the branch goes and refs/pull/1/head keeps it.
	dir := prFixture(t)
	r := req(dir, "squash")
	r.DeleteBranch = true
	res, err := merge(ctx, dir, r)
	if err != nil {
		t.Fatal(err)
	}
	if f := strings.Split(gitIn(t, dir, "log", "-1", "--format=%an|%cn|%P|%s", "main"), "|"); f[0] != "Bo" || f[1] != "Ada" || len(strings.Fields(f[2])) != 1 || f[3] != "Merge feat" {
		t.Errorf("squash commit %q", f)
	}
	if gitIn(t, dir, "show", "main:b.txt") != "newer" || gitIn(t, dir, "show", "main:a.txt") != "1\nmain\n3" {
		t.Error("squash tree")
	}
	if sha, _ := revParse(ctx, dir, "refs/heads/feat"); sha != "" {
		t.Error("feat was not deleted")
	}
	if got, _ := revParse(ctx, dir, "refs/pull/1/head"); got != res.Head {
		t.Errorf("refs/pull/1/head = %s, want %s", got, res.Head)
	}

	// A merge commit has both parents.
	dir = prFixture(t)
	if res, err = merge(ctx, dir, req(dir, "merge")); err != nil {
		t.Fatal(err)
	}
	if parents := strings.Fields(gitIn(t, dir, "log", "-1", "--format=%P", "main")); len(parents) != 2 || parents[1] != res.Head {
		t.Errorf("merge parents %v", parents)
	}

	// Fast-forward needs main where feat left it.
	dir = prFixture(t)
	if _, err := merge(ctx, dir, req(dir, "fast_forward")); !errors.Is(err, errNotFastForward) {
		t.Errorf("fast-forward over a moved base: %v", err)
	}
	gitIn(t, dir, "update-ref", "refs/heads/main", "refs/heads/main~1")
	if res, err = merge(ctx, dir, req(dir, "fast_forward")); err != nil || res.SHA != res.Head {
		t.Errorf("fast-forward: %+v, %v", res, err)
	}
	if _, err := merge(ctx, dir, req(dir, "squash")); !errors.Is(err, errNothingToMerge) {
		t.Errorf("merging again: %v", err)
	}
}

func TestMergeRefuses(t *testing.T) {
	ctx := context.Background()
	dir := prFixture(t)
	head, _ := revParse(ctx, dir, "refs/heads/clash")
	base := MergeRequest{Method: "squash", Base: "main", Head: "clash", ExpectedHead: head, PullNumber: 2, Message: "m", Author: ada, Committer: ada}
	if _, err := merge(ctx, dir, base); !errors.Is(err, errConflicts) {
		t.Errorf("conflicts: %v", err)
	}
	moved := base
	moved.Head, moved.ExpectedHead = "feat", head
	if _, err := merge(ctx, dir, moved); !errors.Is(err, errHeadMoved) {
		t.Errorf("a head that moved: %v", err)
	}
	for name, m := range map[string]MergeRequest{
		"method":    {Method: "rebase", Base: "main", Head: "feat", ExpectedHead: head, PullNumber: 1, Message: "m", Author: ada, Committer: ada},
		"same":      {Method: "squash", Base: "main", Head: "main", ExpectedHead: head, PullNumber: 1, Message: "m", Author: ada, Committer: ada},
		"ref":       {Method: "squash", Base: "main", Head: "-x", ExpectedHead: head, PullNumber: 1, Message: "m", Author: ada, Committer: ada},
		"identity":  {Method: "squash", Base: "main", Head: "feat", ExpectedHead: head, PullNumber: 1, Message: "m", Author: Identity{Name: "a\nb", Email: "x@y"}, Committer: ada},
		"brackets":  {Method: "squash", Base: "main", Head: "feat", ExpectedHead: head, PullNumber: 1, Message: "m", Author: Identity{Name: "a", Email: "<x@y>"}, Committer: ada},
		"message":   {Method: "squash", Base: "main", Head: "feat", ExpectedHead: head, PullNumber: 1, Message: " ", Author: ada, Committer: ada},
		"no number": {Method: "squash", Base: "main", Head: "feat", ExpectedHead: head, Message: "m", Author: ada, Committer: ada},
	} {
		if _, err := merge(ctx, dir, m); err == nil {
			t.Errorf("%s: merged", name)
		}
	}
	if err := setHead(ctx, dir, "feat"); err != nil {
		t.Fatal(err)
	}
	if b, _ := defaultBranch(ctx, dir); b != "feat" {
		t.Errorf("HEAD → %s", b)
	}
	if err := setHead(ctx, dir, "nope"); !errors.Is(err, errNoRef) {
		t.Errorf("a missing default branch: %v", err)
	}
}

func TestPreReceive(t *testing.T) {
	dir := prFixture(t)
	t.Setenv("GIT_DIR", dir)
	main := gitIn(t, dir, "rev-parse", "main")
	run := func(protect bool, lines ...string) (int, string) {
		if protect {
			t.Setenv(envProtect, "main")
		} else {
			t.Setenv(envProtect, "")
		}
		var stderr bytes.Buffer
		code := runHook("pre-receive", strings.NewReader(strings.Join(lines, "\n")+"\n"), &stderr)
		return code, stderr.String()
	}
	if code, _ := run(false, main+" "+zeroSHA+" refs/heads/main"); code != 0 {
		t.Error("unprotected: deleting main was refused")
	}
	if code, msg := run(true, main+" "+zeroSHA+" refs/heads/main"); code != 1 || !strings.Contains(msg, "main is protected") {
		t.Errorf("protected: %d %q", code, msg)
	}
	if code, _ := run(true, zeroSHA+" "+main+" refs/heads/main"); code != 0 {
		t.Error("protected: the first push of main was refused")
	}
	if code, _ := run(true, main+" "+main+" refs/heads/feat"); code != 0 {
		t.Error("protected: another branch was refused")
	}
	// The hook protects the branch it is named, never what HEAD names (L2):
	// with HEAD moved to feat, main still refuses a force-push and a delete,
	// and feat takes pushes.
	if err := setHead(context.Background(), dir, "feat"); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{main + " " + zeroSHA + " refs/heads/main", main + " " + strings.Repeat("a", 40) + " refs/heads/main"} {
		if code, msg := run(true, line); code != 1 || !strings.Contains(msg, "main is protected") {
			t.Errorf("protected, HEAD at feat: %q: %d %q", line, code, msg)
		}
	}
	if code, _ := run(true, main+" "+zeroSHA+" refs/heads/feat"); code != 0 {
		t.Error("protected main, HEAD at feat: deleting feat was refused")
	}
	if code, msg := run(false, zeroSHA+" "+main+" refs/heads/HEAD"); code != 1 || !strings.Contains(msg, "HEAD") {
		t.Errorf("a branch named HEAD: %d %q", code, msg)
	}
	// A pull mirror's repository refuses every push, naming the original.
	t.Setenv(envReadOnly, "https://github.com/founders/shop")
	if code, msg := run(false, main+" "+main+" refs/heads/feat"); code != 1 || !strings.Contains(msg, "mirrors https://github.com/founders/shop") {
		t.Errorf("read-only: %d %q", code, msg)
	}
	t.Setenv(envReadOnly, "")
	// An agent's credential pushes to its branch alone, and never deletes it (D-102).
	t.Setenv(envPushOnly, "agent/x")
	if code, _ := run(false, zeroSHA+" "+main+" refs/heads/agent/x"); code != 0 {
		t.Error("push-only: the agent's own branch was refused")
	}
	for _, line := range []string{main + " " + main + " refs/heads/feat", zeroSHA + " " + main + " refs/tags/v9", main + " " + zeroSHA + " refs/heads/agent/x",
		zeroSHA + " " + main + " refs/heads/agent/x\n" + main + " " + main + " refs/heads/main"} {
		if code, _ := run(false, line); code != 1 {
			t.Errorf("push-only: %q was let through", line)
		}
	}
	t.Setenv(envPushOnly, "")

	log := filepath.Join(t.TempDir(), "push.log")
	os.WriteFile(log, nil, 0o600)
	t.Setenv(envPushLog, log)
	runHook("post-receive", strings.NewReader(zeroSHA+" "+main+" refs/heads/x\n"), &bytes.Buffer{})
	if got := readPushLog(log); len(got) != 1 || got[0].Ref != "refs/heads/x" || got[0].New != main {
		t.Errorf("push log %+v", got)
	}
}

func TestCreateBranch(t *testing.T) {
	dir := prFixture(t)
	ctx := context.Background()
	main := gitIn(t, dir, "rev-parse", "main")
	b, err := createBranch(ctx, dir, CreateBranchRequest{Branch: "agent/fix", From: "main"})
	if err != nil || b.SHA != main {
		t.Fatalf("create: %+v %v", b, err)
	}
	if got := gitIn(t, dir, "rev-parse", "agent/fix"); got != main {
		t.Fatalf("the branch is at %s", got)
	}
	// It only creates: an existing branch, a bad name or an unknown start are refused.
	for name, req := range map[string]CreateBranchRequest{
		"exists":  {Branch: "main", From: "agent/fix"},
		"HEAD":    {Branch: "HEAD", From: "main"},
		"bad":     {Branch: "a..b", From: "main"},
		"unknown": {Branch: "x", From: "nope"},
	} {
		if _, err := createBranch(ctx, dir, req); err == nil {
			t.Errorf("%s: created", name)
		}
	}
}

// A push-only or read-only limit the hook can't be given refuses the push
// instead of being dropped (an agent's branch named fix/ä pushed anywhere).
func TestReceivePackRefusesALimitItCantPass(t *testing.T) {
	h := NewExecutor(t.TempDir()).Handler()
	for _, header := range []string{"Toft-Push-Only", "Toft-Read-Only"} {
		req := httptest.NewRequest(http.MethodPost, "/v1/repos/0b6f9c4e-2a51-4f0e-9d6e-6f0c2a7b9d11/receive-pack?mode=stateless", http.NoBody)
		req.Header.Set(header, "fix/ä")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %q, want 400", header, rec.Code, rec.Body.String())
		}
	}
	// Protection without the branch's name (the previous release's backend,
	// while an upgrade restarts the two) is refused, not let through.
	req := httptest.NewRequest(http.MethodPost, "/v1/repos/0b6f9c4e-2a51-4f0e-9d6e-6f0c2a7b9d11/receive-pack?mode=stateless", http.NoBody)
	req.Header.Set("Toft-Protect-Default", "1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("Toft-Protect-Default without Toft-Protect-Branch: %d %q, want 400", rec.Code, rec.Body.String())
	}
}

// The hooks read "<old> <new> <ref>" on single spaces: a ref holding other
// whitespace is still checked, and a line they can't read refuses the push.
func TestPreReceiveReadsEveryRef(t *testing.T) {
	sha := strings.Repeat("a", 40)
	t.Setenv(envProtect, "")
	t.Setenv(envReadOnly, "")
	t.Setenv(envPushOnly, "agent/x")
	for _, in := range []string{sha + " " + sha + " refs/heads/ma in", sha + " " + sha} {
		var stderr bytes.Buffer
		if code := runHook("pre-receive", strings.NewReader(in+"\n"), &stderr); code != 1 {
			t.Errorf("push-only: %q was let through", in)
		}
	}
	t.Setenv(envPushOnly, "")
	t.Setenv(envReadOnly, "https://github.com/founders/shop")
	if code := runHook("pre-receive", strings.NewReader(zeroSHA+" "+sha+" refs/heads/a b\n"), &bytes.Buffer{}); code != 1 {
		t.Error("read-only: a ref with U+00A0 was let through")
	}
}
