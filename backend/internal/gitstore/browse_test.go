package gitstore

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/testenv"
)

// fixture is a bare repository at its executor path with three commits on
// main, a branch one commit ahead, an annotated and a lightweight tag, and
// a hidden ref, made with the real git client.
type fixture struct {
	dir                   string
	first, second, branch string
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com"}
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, b)
	}
	return strings.TrimSpace(string(b))
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	if _, err := CheckVersion(context.Background()); err != nil {
		testenv.Skip(t, "needs git %d.%d or newer: %v", MinVersion[0], MinVersion[1], err)
	}
	dir, _ := RepoPath(t.TempDir(), "0b6f9c4e-2a51-4f0e-9d6e-6f0c2a7b9d11")
	e := NewExecutor(filepath.Dir(dir))
	if err := e.init(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	w := t.TempDir()
	write := func(name, body string) {
		os.MkdirAll(filepath.Join(w, filepath.Dir(name)), 0o755)
		if err := os.WriteFile(filepath.Join(w, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, w, "init", "-q", "-b", "main")
	write("README.md", "# Shop\n\nSee [the docs](docs/a.md) and ![logo](img/logo.png) and ![badge](https://ci.example/badge.svg) and [bad](javascript:alert(1)).\n")
	write("docs/a.md", "# A\n\none\n")
	write("old.txt", strings.Repeat("line\n", 20))
	write("bin.dat", "a\x00b")
	write("with space.txt", "hi\n")
	gitIn(t, w, "add", ".")
	gitIn(t, w, "commit", "-qm", "first")
	first := gitIn(t, w, "rev-parse", "HEAD")
	gitIn(t, w, "tag", "-a", "v1", "-m", "Release one")
	gitIn(t, w, "tag", "v0")

	write("docs/a.md", "# A\n\ntwo\n")
	gitIn(t, w, "mv", "old.txt", "new.txt")
	write("new.txt", strings.Repeat("line\n", 20)+"more\n")
	gitIn(t, w, "rm", "-q", "bin.dat")
	if err := os.Symlink("docs/a.md", filepath.Join(w, "link")); err != nil {
		t.Fatal(err)
	}
	gitIn(t, w, "add", "-A")
	gitIn(t, w, "commit", "-qm", "second", "-m", "The body.")
	second := gitIn(t, w, "rev-parse", "HEAD")

	gitIn(t, w, "checkout", "-q", "-b", "feature")
	write("feature.txt", "f\n")
	gitIn(t, w, "add", ".")
	gitIn(t, w, "commit", "-qm", "feature work")
	branch := gitIn(t, w, "rev-parse", "HEAD")

	gitIn(t, w, "push", "-q", dir, "main", "feature", "v1", "v0")
	gitIn(t, dir, "update-ref", "refs/pull/1/head", branch)
	return fixture{dir: dir, first: first, second: second, branch: branch}
}

func TestResolve(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for ref, want := range map[string]Ref{
		"":          {Name: "main", Kind: "branch", SHA: f.second},
		"main":      {Name: "main", Kind: "branch", SHA: f.second},
		"feature":   {Name: "feature", Kind: "branch", SHA: f.branch},
		"v1":        {Name: "v1", Kind: "tag", SHA: f.first},
		"v0":        {Name: "v0", Kind: "tag", SHA: f.first},
		f.first[:8]: {Name: f.first[:8], Kind: "commit", SHA: f.first},
		f.second:    {Name: f.second, Kind: "commit", SHA: f.second},
	} {
		got, err := resolve(ctx, f.dir, ref)
		if err != nil || got != want {
			t.Errorf("resolve(%q) = %+v, %v; want %+v", ref, got, err, want)
		}
	}
	// Server-owned names are never resolved by name.
	for _, ref := range []string{"refs/pull/1/head", "pull/1/head", "refs/heads/main", "nope"} {
		if _, err := resolve(ctx, f.dir, ref); err != errNoRef {
			t.Errorf("resolve(%q) = %v, want ref_not_found", ref, err)
		}
	}
	for _, ref := range []string{"-x", "a..b", "a b", "a@{1}", "a:b", "x.lock", ".x", "a/", "@", "a\x00b", "a\nb"} {
		if _, err := resolve(ctx, f.dir, ref); err != errBadRef {
			t.Errorf("resolve(%q) = %v, want bad_ref", ref, err)
		}
	}
}

func TestSummaryBranchesTags(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, err := summary(ctx, f.dir)
	if err != nil || s != (Summary{DefaultBranch: "main", BranchCount: 2, TagCount: 2}) {
		t.Fatalf("summary = %+v, %v", s, err)
	}
	bs, err := branches(ctx, f.dir, url.Values{})
	if err != nil || len(bs.Items) != 2 {
		t.Fatalf("branches = %+v, %v", bs, err)
	}
	if b := bs.Items[0]; b.Name != "main" || !b.Default || *b.Ahead != 0 || *b.Behind != 0 {
		t.Errorf("main = %+v", b)
	}
	if b := bs.Items[1]; b.Name != "feature" || *b.Ahead != 1 || *b.Behind != 0 || b.Commit.SHA != f.branch || b.Commit.Subject != "feature work" || b.Commit.AuthorName != "Ada" {
		t.Errorf("feature = %+v", b)
	}
	p1, err := branches(ctx, f.dir, url.Values{"limit": {"1"}})
	if err != nil || len(p1.Items) != 1 || p1.Next == "" {
		t.Fatalf("page 1 = %+v, %v", p1, err)
	}
	p2, err := branches(ctx, f.dir, url.Values{"limit": {"1"}, "cursor": {p1.Next}})
	if err != nil || len(p2.Items) != 1 || p2.Items[0].Name != "feature" || p2.Next != "" {
		t.Fatalf("page 2 = %+v, %v", p2, err)
	}
	if _, err := branches(ctx, f.dir, url.Values{"cursor": {"!!"}}); err != errBadCursor {
		t.Errorf("bad cursor: %v", err)
	}

	ts, err := tags(ctx, f.dir, url.Values{})
	if err != nil || len(ts.Items) != 2 {
		t.Fatalf("tags = %+v, %v", ts, err)
	}
	for _, tg := range ts.Items {
		if tg.SHA != f.first || tg.Annotated != (tg.Name == "v1") {
			t.Errorf("tag %+v", tg)
		}
		if tg.Name == "v1" && tg.Message != "Release one" {
			t.Errorf("v1 message %q", tg.Message)
		}
	}
}

func TestTree(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tr, err := tree(ctx, f.dir, url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range tr.Entries {
		names = append(names, e.Name+":"+e.Kind)
		if e.LastCommit == nil {
			t.Errorf("%s has no last commit", e.Name)
		}
	}
	if got := strings.Join(names, " "); got != "docs:dir README.md:file link:symlink new.txt:file with space.txt:file" {
		t.Errorf("entries %s", got)
	}
	if tr.CommitCount != 2 || tr.LastCommit.SHA != f.second || tr.Ref.Name != "main" {
		t.Errorf("tree %+v", tr)
	}
	for _, e := range tr.Entries {
		if e.Name == "README.md" && e.LastCommit.SHA != f.first {
			t.Errorf("README last commit %s", e.LastCommit.SHA)
		}
		if e.Name == "docs" && e.LastCommit.SHA != f.second {
			t.Errorf("docs last commit %s", e.LastCommit.SHA)
		}
	}
	if tr.Readme == nil || !strings.Contains(tr.Readme.HTML, `data-toft-path="docs/a.md"`) ||
		!strings.Contains(tr.Readme.HTML, `<a href="https://ci.example/badge.svg"`) || strings.Contains(tr.Readme.HTML, "javascript") {
		t.Errorf("readme %+v", tr.Readme)
	}

	docs, err := tree(ctx, f.dir, url.Values{"path": {"docs"}, "ref": {"v1"}})
	if err != nil || len(docs.Entries) != 1 || docs.Entries[0].Path != "docs/a.md" || docs.Entries[0].LastCommit.SHA != f.first {
		t.Fatalf("docs at v1 = %+v, %v", docs, err)
	}
	for p, want := range map[string]error{"README.md": errNotDir, "nope": errNoPath, "../x": errBadPath, "docs/": errBadPath, "/docs": errBadPath} {
		if _, err := tree(ctx, f.dir, url.Values{"path": {p}}); err != want {
			t.Errorf("tree %q: %v, want %v", p, err, want)
		}
	}
}

func TestBlob(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	b, err := blob(ctx, f.dir, url.Values{"path": {"docs/a.md"}})
	if err != nil || b.Text != "# A\n\ntwo\n" || !strings.Contains(b.HTML, `id="user-content-a"`) || b.Binary || b.Size != 9 {
		t.Fatalf("docs/a.md = %+v, %v", b, err)
	}
	if b, err := blob(ctx, f.dir, url.Values{"path": {"bin.dat"}, "ref": {"v1"}}); err != nil || !b.Binary || b.Text != "" {
		t.Errorf("bin.dat = %+v, %v", b, err)
	}
	if b, err := blob(ctx, f.dir, url.Values{"path": {"link"}}); err != nil || b.Kind != "symlink" || b.Text != "docs/a.md" {
		t.Errorf("link = %+v, %v", b, err)
	}
	if b, err := blob(ctx, f.dir, url.Values{"path": {"with space.txt"}}); err != nil || b.Text != "hi\n" {
		t.Errorf("with space = %+v, %v", b, err)
	}
	for p, want := range map[string]error{"docs": errNotFile, "nope": errNoPath, "": errBadPath, "a/../b": errBadPath} {
		if _, err := blob(ctx, f.dir, url.Values{"path": {p}}); !errors.Is(err, want) {
			t.Errorf("blob %q: %v, want %v", p, err, want)
		}
	}
}

func TestCommitsAndDiff(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p1, err := commits(ctx, f.dir, url.Values{"limit": {"1"}})
	if err != nil || len(p1.Items) != 1 || p1.Items[0].SHA != f.second || p1.Next == "" {
		t.Fatalf("page 1 = %+v, %v", p1, err)
	}
	p2, err := commits(ctx, f.dir, url.Values{"limit": {"1"}, "cursor": {p1.Next}})
	if err != nil || len(p2.Items) != 1 || p2.Items[0].SHA != f.first || p2.Next != "" {
		t.Fatalf("page 2 = %+v, %v", p2, err)
	}
	if byPath, err := commits(ctx, f.dir, url.Values{"path": {"README.md"}}); err != nil || len(byPath.Items) != 1 || byPath.Items[0].SHA != f.first {
		t.Errorf("README's commits = %+v, %v", byPath, err)
	}

	d, err := commitDetail(ctx, f.dir, f.second[:10])
	if err != nil {
		t.Fatal(err)
	}
	if d.SHA != f.second || d.Body != "The body." || d.Parents[0] != f.first {
		t.Errorf("commit %+v", d.Commit)
	}
	got := map[string]FileChange{}
	for _, fc := range d.Files {
		got[fc.Path] = fc
	}
	if fc := got["new.txt"]; fc.Status != "renamed" || fc.OldPath != "old.txt" || fc.Additions != 1 {
		t.Errorf("new.txt %+v", fc)
	}
	if fc := got["bin.dat"]; fc.Status != "deleted" || !fc.Binary {
		t.Errorf("bin.dat %+v", fc)
	}
	if got["link"].Status != "added" || got["docs/a.md"].Status != "modified" || len(d.Files) != 4 || d.Stats.Files != 4 {
		t.Errorf("files %+v", d.Files)
	}

	all, err := diff(ctx, f.dir, f.second, url.Values{})
	if err != nil || len(all.Items) != 4 {
		t.Fatalf("diff = %+v, %v", all, err)
	}
	for _, fd := range all.Items {
		switch fd.Path {
		case "bin.dat":
			if fd.Patch != "" {
				t.Errorf("binary patch %q", fd.Patch)
			}
		case "docs/a.md":
			if !strings.HasPrefix(fd.Patch, "@@") || !strings.Contains(fd.Patch, "-one\n+two\n") {
				t.Errorf("docs/a.md patch %q", fd.Patch)
			}
		}
	}
	pg, err := diff(ctx, f.dir, f.second, url.Values{"limit": {"3"}})
	if err != nil || len(pg.Items) != 3 || pg.Next == "" {
		t.Fatalf("diff page = %+v, %v", pg, err)
	}
	rest, err := diff(ctx, f.dir, f.second, url.Values{"limit": {"3"}, "cursor": {pg.Next}})
	if err != nil || len(rest.Items) != 1 || rest.Items[0].Path != all.Items[3].Path {
		t.Fatalf("diff rest = %+v, %v", rest, err)
	}
	if root, err := commitDetail(ctx, f.dir, f.first); err != nil || len(root.Files) != 5 || root.Files[0].Status != "added" {
		t.Errorf("root commit %+v, %v", root, err)
	}
	if _, err := commitDetail(ctx, f.dir, "main"); err != errBadRef {
		t.Errorf("a branch name as sha: %v", err)
	}
}

// A push that leaves HEAD on a branch that doesn't exist points it at one.
func TestFixHead(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	gitIn(t, f.dir, "symbolic-ref", "HEAD", "refs/heads/trunk")
	if err := fixHead(ctx, f.dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := defaultBranch(ctx, f.dir); b != "main" {
		t.Errorf("HEAD → %s, want main", b)
	}
	gitIn(t, f.dir, "update-ref", "-d", "refs/heads/main")
	gitIn(t, f.dir, "symbolic-ref", "HEAD", "refs/heads/trunk")
	if err := fixHead(ctx, f.dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := defaultBranch(ctx, f.dir); b != "feature" {
		t.Errorf("HEAD → %s, want the newest branch", b)
	}
}

func TestMarkdownIsSafe(t *testing.T) {
	hostile := []string{
		"<script>alert(1)</script>",
		"<img src=x onerror=alert(1)>",
		"<iframe src=//evil.example></iframe>",
		"[a](javascript:alert(1))",
		"[a](JaVaScRiPt:alert(1))",
		"[a](<java\tscript:alert(1)>)",
		"[a](&#106;avascript:alert(1))",
		"[a](java&#x09;script:alert(1))",
		"[a](vbscript:msgbox(1))",
		"[a](data:text/html;base64,PHNjcmlwdD4=)",
		"[a](file:///etc/passwd)",
		"![a](javascript:alert(1))",
		"<javascript:alert(1)>",
		"# H {onclick=alert(1)}",
		"[a](//evil.example/x)",
		`[a](/\evil.example/x)`,
		"<a href=\"javascript:alert(1)\">x</a>",
		"[a]: javascript:alert(1)\n\n[a]",
	}
	// What would run or leave: a tag, an event attribute, or a URL attribute
	// with a script scheme or another host. The same words as text are fine.
	bad := regexp.MustCompile(`<(script|iframe|object|embed|style)|<[^>]*\son\w+=|(href|src)="[^"]*(javascript|vbscript|data|file):|(href|src)="[^"]*evil\.example`)
	for _, src := range hostile {
		out := strings.ToLower(Markdown([]byte(src), ""))
		if bad.MatchString(out) {
			t.Errorf("%q rendered %q", src, out)
		}
	}

	out := Markdown([]byte("# Title\n\n[t](#title) [dir](sub/) [d](b.md) [up](../../etc/passwd) [root](/README.md#x) [s](<my file.md>) [e](my%20file.md) ![i](../img.png) [w](https://example.com)"), "docs")
	for _, want := range []string{
		`id="user-content-title"`, `href="#user-content-title"`,
		`data-toft-path="docs/b.md"`, `data-toft-path="README.md"`, `data-toft-path="docs/my file.md"`,
		`<a href="" data-toft-dir="" data-toft-path="docs/sub">dir</a>`,
		`<img src="" alt="i" data-toft-path="img.png">`,
		`href="https://example.com" rel="nofollow noopener noreferrer" target="_blank"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in\n%s", want, out)
		}
	}
	if strings.Contains(out, "etc/passwd") {
		t.Errorf("a link out of the repository kept its path:\n%s", out)
	}
}

// Autolinks (<scheme:…> and GFM's bare links) follow the same rule as
// links: http, https and mailto stay links, any other scheme is text.
func TestMarkdownAutolinks(t *testing.T) {
	const rel = ` rel="nofollow noopener noreferrer" target="_blank"`
	for _, c := range []struct{ src, want string }{
		{"<ms-settings:privacy>", "<p>ms-settings:privacy</p>"},
		{"<intent://x#Intent;end>", "<p>intent://x#Intent;end</p>"},
		{"<javascript:alert(1)>", "<p>javascript:alert(1)</p>"},
		{"<data:text/html;base64,PHNjcmlwdD4=>", "<p>data:text/html;base64,PHNjcmlwdD4=</p>"},
		{"<file:///etc/passwd>", "<p>file:///etc/passwd</p>"},
		{"<vbscript:msgbox(1)>", "<p>vbscript:msgbox(1)</p>"},
		{"<MS-Settings:privacy>", "<p>MS-Settings:privacy</p>"},
		{"<http://example.com/a>", `<p><a href="http://example.com/a"` + rel + `>http://example.com/a</a></p>`},
		{"<https://example.com/a>", `<p><a href="https://example.com/a"` + rel + `>https://example.com/a</a></p>`},
		{"<mailto:a@example.com>", `<p><a href="mailto:a@example.com"` + rel + `>mailto:a@example.com</a></p>`},
		{"<a@example.com>", `<p><a href="mailto:a@example.com"` + rel + `>a@example.com</a></p>`},
		{"www.example.com/x", `<p><a href="http://www.example.com/x"` + rel + `>www.example.com/x</a></p>`},
		{"ftp://example.com/x", "<p>ftp://example.com/x</p>"},
	} {
		if got := strings.TrimSpace(Markdown([]byte(c.src), "")); got != c.want {
			t.Errorf("%q rendered %q, want %q", c.src, got, c.want)
		}
	}
}

// TestMarkdownRefs links the project's issue keys and, when asked, pull
// requests, but not inside code, links or other words (D-81).
func TestMarkdownRefs(t *testing.T) {
	out := MarkdownRefs([]byte("Closes STO-104 and !41.\nNot `STO-1`, [STO-2](https://x.test), XSTO-3, BIL-4 or a&#33;5.\n\nSee STO-7"), Refs{Keys: []string{"STO"}, Pulls: true})
	for _, want := range []string{
		`Closes <a href="" data-toft-issue="104">STO-104</a> and <a href="" data-toft-pull="41">!41</a>.`,
		"<code>STO-1</code>",
		`<a href="https://x.test" rel="nofollow noopener noreferrer" target="_blank">STO-2</a>`,
		"XSTO-3, BIL-4",
		`See <a href="" data-toft-issue="7">STO-7</a>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Count(out, "data-toft-") != 3 {
		t.Errorf("want 3 refs:\n%s", out)
	}
	if out := MarkdownRefs([]byte("STO-1 !2"), Refs{Keys: []string{"STO"}}); strings.Contains(out, "data-toft-pull") {
		t.Errorf("pulls linked without Pulls: %s", out)
	}
}

// TestMarkdownDoc gives a doc page its blocks in order, page links, notes
// and what it names (D-86).
func TestMarkdownDoc(t *testing.T) {
	src := "Restore STO-97 after !40.\n\n```toft-service 0d1f6f7e-1b2c-4d5e-8f90-a1b2c3d4e5f6\n```\n\n" +
		"> [!WARNING]\n> Writes are down.\n\n- [ ] one\n  ```toft-run 0d1f6f7e-1b2c-4d5e-8f90-a1b2c3d4e5f6 host\n  docker stop api\n  docker stop worker\n  ```\n\n" +
		"```sh\nnot a block\n```\n\nSee [the checklist](page:7a8b9c0d-1e2f-4a3b-8c4d-5e6f7a8b9c0d) and [x](page:nope).\n\n> plain quote\n"
	d := MarkdownDoc([]byte(src), Refs{Keys: []string{"STO"}, Pulls: true})
	if len(d.Fences) != 2 || d.Fences[0].Info != "toft-service 0d1f6f7e-1b2c-4d5e-8f90-a1b2c3d4e5f6" || d.Fences[0].Body != "" ||
		d.Fences[1].Info != "toft-run 0d1f6f7e-1b2c-4d5e-8f90-a1b2c3d4e5f6 host" || d.Fences[1].Body != "docker stop api\ndocker stop worker" {
		t.Errorf("fences: %+v", d.Fences)
	}
	if !slices.Equal(d.Issues, []int{97}) || !slices.Equal(d.Pulls, []int{40}) || !slices.Equal(d.Pages, []string{"7a8b9c0d-1e2f-4a3b-8c4d-5e6f7a8b9c0d"}) {
		t.Errorf("refs: %v %v %v", d.Issues, d.Pulls, d.Pages)
	}
	for _, want := range []string{
		`<blockquote data-toft-note="warning">`,
		`<a href="" data-toft-page="7a8b9c0d-1e2f-4a3b-8c4d-5e6f7a8b9c0d">the checklist</a>`,
		`class="language-toft-service"`,
		"<blockquote>\n<p>plain quote</p>",
	} {
		if !strings.Contains(d.HTML, want) {
			t.Errorf("missing %q in\n%s", want, d.HTML)
		}
	}
	if strings.Contains(d.HTML, "[!WARNING]") || strings.Contains(d.HTML, `href="page:`) {
		t.Errorf("marker or page: URL left in\n%s", d.HTML)
	}
}

// TestMarkdownMentions marks the workspace's handles, not emails, code or
// other words (D-88).
func TestMarkdownMentions(t *testing.T) {
	out := MarkdownRefs([]byte("@sam can you check? cc @jo.doe. Not sam@x.test, `@sam` or @samuel."), Refs{Mentions: []string{"sam", "jo.doe"}})
	for _, want := range []string{`<a href="" data-toft-user="sam">@sam</a> can`, `cc <a href="" data-toft-user="jo.doe">@jo.doe</a>.`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Count(out, "data-toft-user") != 2 {
		t.Errorf("want 2 mentions:\n%s", out)
	}
}
