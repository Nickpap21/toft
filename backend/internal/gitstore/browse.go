package gitstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The repository browser's reads (D-73). Each is a fixed set of git
// commands the executor builds itself: what crosses the socket is a ref, a
// path, a SHA, a page, never an argument. A ref is resolved once to a full
// SHA, which every later command takes; a path is used only after a SHA and
// a colon, or after "--" with GIT_LITERAL_PATHSPECS (command's environment).

// Caps on what a read returns.
const (
	maxBlob        = 1 << 20   // the text of a file shown
	maxEntries     = 1000      // entries of a directory
	maxFiles       = 3000      // files of a commit
	maxPatch       = 512 << 10 // the patch of one file
	maxOut         = 64 << 20  // anything git prints for one read
	browseTimeout  = 30 * time.Second
	lastModBudget  = 5 * time.Second // the tree's last commit per entry
	defaultPage    = 50
	maxPage        = 200
	defaultDiffs   = 20
	maxDiffs       = 50
	binarySniffLen = 8000 // git's own: a NUL in the first 8,000 bytes is binary
)

// Ref is what a client's ref resolved to.
type Ref struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // branch, tag, commit
	SHA  string `json:"sha"`
}

// Commit is a commit as the browser shows it. Author names and emails are
// git's, never matched to accounts: commit emails are unverified (D-73).
type Commit struct {
	SHA         string    `json:"sha"`
	Parents     []string  `json:"parents"`
	AuthorName  string    `json:"author_name"`
	AuthorEmail string    `json:"author_email"`
	AuthoredAt  time.Time `json:"authored_at"`
	CommittedAt time.Time `json:"committed_at"`
	Subject     string    `json:"subject"`
	Body        string    `json:"body,omitempty"`
}

type Summary struct {
	DefaultBranch string `json:"default_branch"`
	Empty         bool   `json:"empty"`
	BranchCount   int    `json:"branch_count"`
	TagCount      int    `json:"tag_count"`
}

type Branch struct {
	Name    string `json:"name"`
	Default bool   `json:"default"`
	Commit  Commit `json:"commit"`
	Ahead   *int   `json:"ahead"`
	Behind  *int   `json:"behind"`
}

type Tag struct {
	Name      string    `json:"name"`
	SHA       string    `json:"sha"`
	Annotated bool      `json:"annotated"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

type TreeEntry struct {
	Name       string  `json:"name"`
	Path       string  `json:"path"`
	Kind       string  `json:"kind"` // dir, file, symlink, submodule
	Size       *int64  `json:"size"`
	LastCommit *Commit `json:"last_commit"`
}

type Readme struct {
	Path string `json:"path"`
	HTML string `json:"html"`
}

type Tree struct {
	Ref         Ref         `json:"ref"`
	Path        string      `json:"path"`
	LastCommit  *Commit     `json:"last_commit"`
	CommitCount int         `json:"commit_count"`
	Entries     []TreeEntry `json:"entries"`
	Truncated   bool        `json:"truncated"`
	Readme      *Readme     `json:"readme"`
}

type Blob struct {
	Ref        Ref     `json:"ref"`
	Path       string  `json:"path"`
	Kind       string  `json:"kind"` // file, symlink
	Size       int64   `json:"size"`
	Binary     bool    `json:"binary"`
	Truncated  bool    `json:"truncated"`
	Text       string  `json:"text"`
	HTML       string  `json:"html,omitempty"`
	LastCommit *Commit `json:"last_commit"`
}

type FileChange struct {
	Path      string `json:"path"`
	OldPath   string `json:"old_path,omitempty"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary"`
}

type Stats struct {
	Files     int `json:"files"`
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
}

type CommitDetail struct {
	Commit
	Stats          Stats        `json:"stats"`
	Files          []FileChange `json:"files"`
	FilesTruncated bool         `json:"files_truncated"`
}

type FileDiff struct {
	FileChange
	Patch          string `json:"patch"`
	PatchTruncated bool   `json:"patch_truncated"`
}

// Page is a page of a list and the cursor of the next, "" on the last.
type Page[T any] struct {
	Items []T    `json:"items"`
	Next  string `json:"next,omitempty"`
}

// BrowseError is a read the client got wrong, or that found nothing. Code
// is the API's error code.
type BrowseError struct {
	Status int    `json:"-"`
	Code   string `json:"code"`
	Msg    string `json:"error"`
}

func (e *BrowseError) Error() string { return e.Msg }

func browseErr(status int, code, msg string) *BrowseError {
	return &BrowseError{Status: status, Code: code, Msg: msg}
}

var (
	errBadRef    = browseErr(http.StatusBadRequest, "bad_ref", "that is not a branch, tag or commit name")
	errBadPath   = browseErr(http.StatusBadRequest, "bad_path", "that is not a path in a repository: relative, with no '.', '..' or empty part")
	errBadCursor = browseErr(http.StatusBadRequest, "bad_cursor", "cursor is not one this API issued")
	errBadLimit  = browseErr(http.StatusBadRequest, "bad_limit", "limit must be a whole number")
	errEmpty     = browseErr(http.StatusNotFound, "empty", "this repository has no commits yet")
	errNoRef     = browseErr(http.StatusNotFound, "ref_not_found", "no branch, tag or commit by that name")
	errNoPath    = browseErr(http.StatusNotFound, "path_not_found", "nothing at that path")
	errNotDir    = browseErr(http.StatusNotFound, "not_a_directory", "that path is a file, not a directory")
	errNotFile   = browseErr(http.StatusNotFound, "not_a_file", "that path is not a file")
	errTooLarge  = browseErr(http.StatusUnprocessableEntity, "too_large", "git's answer is too large to show")
	errDiffSplit = errors.New("the diff's files and patches don't line up")
)

// ValidRef says whether s may name a branch or a tag, by git's rules
// (git-check-ref-format), or a SHA. Nothing else reaches a command.
func ValidRef(s string) bool {
	if s == "" || len(s) > 255 || s == "@" || s[0] == '-' || s[0] == '/' ||
		strings.HasSuffix(s, "/") || strings.HasSuffix(s, ".") ||
		strings.Contains(s, "..") || strings.Contains(s, "@{") || strings.Contains(s, "//") {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) {
			return false
		}
	}
	for _, part := range strings.Split(s, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

// ValidPath says whether p may be a path in a repository: relative, with no
// NUL and no ".", ".." or empty segment. "" is the root.
func ValidPath(p string) bool {
	if p == "" {
		return true
	}
	if len(p) > 4096 || strings.ContainsRune(p, 0) {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

func isHex(s string, min, max int) bool {
	if len(s) < min || len(s) > max {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// ─── running git ─────────────────────────────────────────────────────────────

// out runs git on the repository at dir and returns what it printed, or
// errTooLarge past maxOut. A git that fails is a *gitFailed with the start
// of its stderr.
func out(ctx context.Context, dir string, stdin io.Reader, args ...string) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var stdout bytes.Buffer
	stderr := &capped{max: 4096}
	lw := &limitWriter{w: &stdout, n: maxOut, over: cancel}
	cmd := command(ctx, "", append([]string{"--git-dir=" + dir}, args...)...)
	cmd.Stdin = stdin
	cmd.Stdout = lw
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if lw.n < 0 {
			return nil, errTooLarge
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("git %s: %w", args[0], ctx.Err())
		}
		return nil, &gitFailed{args: args[0], err: err, stderr: strings.TrimSpace(stderr.String()), stdout: stdout.String()}
	}
	return stdout.Bytes(), nil
}

// head is out for one command whose output may be cut: at most n bytes,
// and whether there was more.
func head(ctx context.Context, dir string, n int, args ...string) ([]byte, bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := command(ctx, "", append([]string{"--git-dir=" + dir}, args...)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	stderr := &capped{max: 4096}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, false, err
	}
	buf, rerr := io.ReadAll(io.LimitReader(stdout, int64(n)+1))
	cut := len(buf) > n
	if cut {
		buf = buf[:n]
		cancel()
	}
	werr := cmd.Wait()
	if rerr != nil {
		return nil, false, rerr
	}
	if werr != nil && !cut {
		return nil, false, &gitFailed{args: args[0], err: werr, stderr: strings.TrimSpace(stderr.String())}
	}
	return buf, cut, nil
}

type gitFailed struct {
	args   string
	err    error
	stderr string
	// stdout is what git printed before failing (merge-tree's conflicts).
	stdout string
}

func (g *gitFailed) Error() string { return "git " + g.args + ": " + g.err.Error() + ": " + g.stderr }

// exited1 says whether err is git exiting with status 1 (rev-parse
// --verify --quiet's "no such object").
func exited1(err error) bool {
	var g *gitFailed
	var exit *exec.ExitError
	return errors.As(err, &g) && errors.As(g.err, &exit) && exit.ExitCode() == 1
}

// limitWriter keeps n bytes; past them it stops git (over) and drops the
// rest, still draining the pipe so git never blocks on it. n < 0 after.
type limitWriter struct {
	w    io.Writer
	n    int
	over func()
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n < 0 {
		return len(p), nil
	}
	if len(p) > l.n {
		l.n = -1
		l.over()
		return len(p), nil
	}
	l.n -= len(p)
	return l.w.Write(p)
}

// revParse is the full SHA of the commit spec names, or "" when there is
// none.
func revParse(ctx context.Context, dir, spec string) (string, error) {
	b, err := out(ctx, dir, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", spec+"^{commit}")
	if exited1(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// defaultBranch is the branch HEAD names.
func defaultBranch(ctx context.Context, dir string) (string, error) {
	b, err := out(ctx, dir, nil, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(strings.TrimSpace(string(b)), "refs/heads/"), nil
}

// resolve is what ref names: a branch, then a tag, then a commit by SHA;
// the default branch when ref is "". Names under refs/pull or
// refs/keep-around are never looked up.
func resolve(ctx context.Context, dir, ref string) (Ref, error) {
	if ref == "" {
		name, err := defaultBranch(ctx, dir)
		if err != nil {
			return Ref{}, err
		}
		sha, err := revParse(ctx, dir, "refs/heads/"+name)
		if err != nil {
			return Ref{}, err
		}
		if sha == "" {
			return Ref{}, errEmpty
		}
		return Ref{Name: name, Kind: "branch", SHA: sha}, nil
	}
	if !ValidRef(ref) {
		return Ref{}, errBadRef
	}
	for _, k := range [][2]string{{"branch", "refs/heads/"}, {"tag", "refs/tags/"}} {
		sha, err := revParse(ctx, dir, k[1]+ref)
		if err != nil {
			return Ref{}, err
		}
		if sha != "" {
			return Ref{Name: ref, Kind: k[0], SHA: sha}, nil
		}
	}
	if isHex(ref, 4, 64) {
		sha, err := revParse(ctx, dir, ref)
		if err != nil {
			return Ref{}, err
		}
		if sha != "" {
			return Ref{Name: ref, Kind: "commit", SHA: sha}, nil
		}
	}
	return Ref{}, errNoRef
}

// ─── commits ─────────────────────────────────────────────────────────────────

// logFormat is a commit's fields, NUL-separated, each commit ended by RS.
const (
	logFormat     = "--format=%H%x00%P%x00%an%x00%ae%x00%at%x00%ct%x00%s%x1e"
	logFormatBody = "--format=%H%x00%P%x00%an%x00%ae%x00%at%x00%ct%x00%s%x00%b%x1e"
)

func parseCommits(b []byte) []Commit {
	recs := strings.Split(string(b), "\x1e")
	commits := make([]Commit, 0, len(recs))
	for _, rec := range recs {
		f := strings.Split(strings.TrimLeft(rec, "\n"), "\x00")
		if len(f) < 7 {
			continue
		}
		at, _ := strconv.ParseInt(f[4], 10, 64)
		ct, _ := strconv.ParseInt(f[5], 10, 64)
		c := Commit{
			SHA: f[0], Parents: strings.Fields(f[1]),
			AuthorName: f[2], AuthorEmail: f[3],
			AuthoredAt: time.Unix(at, 0).UTC(), CommittedAt: time.Unix(ct, 0).UTC(),
			Subject: f[6],
		}
		if len(f) > 7 {
			c.Body = strings.TrimSpace(f[7])
		}
		commits = append(commits, c)
	}
	return commits
}

// lastCommit is the newest commit from sha that touched p (sha itself at
// the root).
func lastCommit(ctx context.Context, dir, sha, p string) (*Commit, error) {
	args := []string{"log", "-1", logFormat, sha}
	if p != "" {
		args = append(args, "--", p)
	}
	b, err := out(ctx, dir, nil, args...)
	if err != nil {
		return nil, err
	}
	if cs := parseCommits(b); len(cs) > 0 {
		return &cs[0], nil
	}
	return nil, nil
}

// commitsByID is the commits named, by SHA.
func commitsByID(ctx context.Context, dir string, ids []string) (map[string]*Commit, error) {
	byID := make(map[string]*Commit, len(ids))
	if len(ids) == 0 {
		return byID, nil
	}
	b, err := out(ctx, dir, strings.NewReader(strings.Join(ids, "\n")+"\n"), "log", "--no-walk=unsorted", "--stdin", logFormat)
	if err != nil {
		return nil, err
	}
	for _, c := range parseCommits(b) {
		byID[c.SHA] = &c
	}
	return byID, nil
}

// ─── the reads ───────────────────────────────────────────────────────────────

func summary(ctx context.Context, dir string) (Summary, error) {
	name, err := defaultBranch(ctx, dir)
	if err != nil {
		return Summary{}, err
	}
	b, err := out(ctx, dir, nil, "for-each-ref", "--format=%(refname)", "refs/heads", "refs/tags")
	if err != nil {
		return Summary{}, err
	}
	s := Summary{DefaultBranch: name}
	for _, r := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(r, "refs/heads/"):
			s.BranchCount++
		case strings.HasPrefix(r, "refs/tags/"):
			s.TagCount++
		}
	}
	s.Empty = s.BranchCount == 0
	return s, nil
}

// page reads limit and cursor (an offset this API issued).
func page(q url.Values, def, most int) (offset, limit int, err error) {
	limit = def
	if v := q.Get("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil {
			return 0, 0, errBadLimit
		}
		limit = min(max(limit, 1), most)
	}
	if c := q.Get("cursor"); c != "" {
		raw, err := base64.RawURLEncoding.DecodeString(c)
		if err != nil {
			return 0, 0, errBadCursor
		}
		if offset, err = strconv.Atoi(string(raw)); err != nil || offset < 0 {
			return 0, 0, errBadCursor
		}
	}
	return offset, limit, nil
}

func offsetCursor(n int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(n)))
}

// pageOf is items[offset:offset+limit] and the next cursor.
func pageOf[T any](items []T, offset, limit int) Page[T] {
	if offset >= len(items) {
		return Page[T]{Items: []T{}}
	}
	end := min(offset+limit, len(items))
	p := Page[T]{Items: items[offset:end]}
	if end < len(items) {
		p.Next = offsetCursor(end)
	}
	return p
}

func branches(ctx context.Context, dir string, q url.Values) (Page[Branch], error) {
	offset, limit, err := page(q, defaultPage, maxPage)
	if err != nil {
		return Page[Branch]{}, err
	}
	def, err := defaultBranch(ctx, dir)
	if err != nil {
		return Page[Branch]{}, err
	}
	defSHA, err := revParse(ctx, dir, "refs/heads/"+def)
	if err != nil {
		return Page[Branch]{}, err
	}
	format := "--format=%(refname:lstrip=2)%00%(objectname)%00%(parent)%00%(authorname)%00%(authoremail:trim)%00%(authordate:unix)%00%(committerdate:unix)%00%(subject)"
	if defSHA != "" {
		// By SHA: a branch name may hold a ")" the format would trip on.
		format += "%00%(ahead-behind:" + defSHA + ")"
	}
	b, err := out(ctx, dir, nil, "for-each-ref", "--sort=-committerdate", format, "refs/heads")
	if err != nil {
		return Page[Branch]{}, err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	all := make([]Branch, 0, len(lines))
	for _, line := range lines {
		f := strings.Split(line, "\x00")
		if len(f) < 8 {
			continue
		}
		at, _ := strconv.ParseInt(f[5], 10, 64)
		ct, _ := strconv.ParseInt(f[6], 10, 64)
		br := Branch{Name: f[0], Default: f[0] == def, Commit: Commit{
			SHA: f[1], Parents: strings.Fields(f[2]), AuthorName: f[3], AuthorEmail: f[4],
			AuthoredAt: time.Unix(at, 0).UTC(), CommittedAt: time.Unix(ct, 0).UTC(), Subject: f[7],
		}}
		if len(f) > 8 {
			if ab := strings.Fields(f[8]); len(ab) == 2 {
				a, _ := strconv.Atoi(ab[0])
				bh, _ := strconv.Atoi(ab[1])
				br.Ahead, br.Behind = &a, &bh
			}
		}
		all = append(all, br)
	}
	// The default branch first, as the boards list it.
	if i := slices.IndexFunc(all, func(b Branch) bool { return b.Default }); i > 0 {
		d := all[i]
		all = append([]Branch{d}, slices.Delete(all, i, i+1)...)
	}
	return pageOf(all, offset, limit), nil
}

func tags(ctx context.Context, dir string, q url.Values) (Page[Tag], error) {
	offset, limit, err := page(q, defaultPage, maxPage)
	if err != nil {
		return Page[Tag]{}, err
	}
	b, err := out(ctx, dir, nil, "for-each-ref", "--sort=-creatordate",
		"--format=%(refname:lstrip=2)%00%(objecttype)%00%(objectname)%00%(*objecttype)%00%(*objectname)%00%(creatordate:unix)%00%(contents:subject)",
		"refs/tags")
	if err != nil {
		return Page[Tag]{}, err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	all := make([]Tag, 0, len(lines))
	for _, line := range lines {
		f := strings.Split(line, "\x00")
		if len(f) < 7 {
			continue
		}
		t := Tag{Name: f[0], Message: f[6]}
		switch {
		case f[1] == "commit":
			t.SHA = f[2]
		case f[1] == "tag" && f[3] == "commit":
			t.SHA, t.Annotated = f[4], true
		default:
			continue // a tag of a tree or a blob: nothing to browse
		}
		at, _ := strconv.ParseInt(f[5], 10, 64)
		t.CreatedAt = time.Unix(at, 0).UTC()
		all = append(all, t)
	}
	return pageOf(all, offset, limit), nil
}

// objectType is what is at sha:p ("tree", "blob", "commit" for a
// submodule), or "" when nothing is.
func objectType(ctx context.Context, dir, sha, p string) (string, error) {
	b, err := out(ctx, dir, nil, "cat-file", "-t", sha+":"+p)
	if err != nil {
		var g *gitFailed
		if errors.As(err, &g) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func tree(ctx context.Context, dir string, q url.Values) (*Tree, error) {
	p := q.Get("path")
	if !ValidPath(p) {
		return nil, errBadPath
	}
	ref, err := resolve(ctx, dir, q.Get("ref"))
	if err != nil {
		return nil, err
	}
	treeish := ref.SHA
	if p != "" {
		switch t, err := objectType(ctx, dir, ref.SHA, p); {
		case err != nil:
			return nil, err
		case t == "":
			return nil, errNoPath
		case t != "tree":
			return nil, errNotDir
		}
		treeish = ref.SHA + ":" + p
	}
	b, err := out(ctx, dir, nil, "ls-tree", "-z", "--format=%(objectmode)%x00%(objecttype)%x00%(objectsize)%x00%(path)", treeish)
	if err != nil {
		return nil, err
	}
	f := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
	t := &Tree{Ref: ref, Path: p, Entries: make([]TreeEntry, 0, len(f)/4)}
	for i := 0; i+3 < len(f); i += 4 {
		e := TreeEntry{Name: f[i+3], Path: path.Join(p, f[i+3])}
		switch {
		case f[i] == "120000":
			e.Kind = "symlink"
		case f[i+1] == "tree":
			e.Kind = "dir"
		case f[i+1] == "commit":
			e.Kind = "submodule"
		default:
			e.Kind = "file"
			if n, err := strconv.ParseInt(f[i+2], 10, 64); err == nil {
				e.Size = &n
			}
		}
		t.Entries = append(t.Entries, e)
	}
	// Directories first, then by name.
	slices.SortStableFunc(t.Entries, func(a, b TreeEntry) int {
		if (a.Kind == "dir") != (b.Kind == "dir") {
			if a.Kind == "dir" {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	if len(t.Entries) > maxEntries {
		t.Entries, t.Truncated = t.Entries[:maxEntries], true
	}

	if t.LastCommit, err = lastCommit(ctx, dir, ref.SHA, p); err != nil {
		return nil, err
	}
	n, err := out(ctx, dir, nil, "rev-list", "--count", ref.SHA)
	if err != nil {
		return nil, err
	}
	t.CommitCount, _ = strconv.Atoi(strings.TrimSpace(string(n)))
	if err := entriesLastCommit(ctx, dir, ref.SHA, p, t.Entries); err != nil {
		return nil, err
	}
	if t.Readme, err = readme(ctx, dir, ref, p, t.Entries); err != nil {
		return nil, err
	}
	return t, nil
}

// entriesLastCommit fills each entry's last commit with git last-modified,
// within lastModBudget: a deep history can take longer, and the entries
// then go without (the API's null).
func entriesLastCommit(ctx context.Context, dir, sha, p string, entries []TreeEntry) error {
	if len(entries) == 0 {
		return nil
	}
	lctx, cancel := context.WithTimeout(ctx, lastModBudget)
	defer cancel()
	args := []string{"last-modified", "-z", sha}
	if p != "" {
		args = append(args, "--", p+"/")
	}
	b, err := out(lctx, dir, nil, args...)
	if err != nil {
		if lctx.Err() != nil && ctx.Err() == nil {
			return nil
		}
		return err
	}
	byPath := map[string]string{}
	var ids []string
	for _, rec := range strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00") {
		id, pth, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		byPath[pth] = id
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	commits, err := commitsByID(ctx, dir, ids)
	if err != nil {
		return err
	}
	for i := range entries {
		entries[i].LastCommit = commits[byPath[entries[i].Path]]
	}
	return nil
}

// readme renders the directory's README, if it has one: README.md first,
// then any README.*, then README.
func readme(ctx context.Context, dir string, ref Ref, p string, entries []TreeEntry) (*Readme, error) {
	best, rank := "", 9
	for _, e := range entries {
		if e.Kind != "file" {
			continue
		}
		n := strings.ToLower(e.Name)
		r := 9
		switch {
		case n == "readme.md" || n == "readme.markdown":
			r = 0
		case strings.HasPrefix(n, "readme."):
			r = 1
		case n == "readme":
			r = 2
		}
		if r < rank {
			best, rank = e.Path, r
		}
	}
	if best == "" {
		return nil, nil
	}
	text, _, err := head(ctx, dir, maxBlob, "cat-file", "blob", ref.SHA+":"+best)
	if err != nil {
		return nil, err
	}
	if bytes.IndexByte(text[:min(len(text), binarySniffLen)], 0) >= 0 {
		return nil, nil
	}
	return &Readme{Path: best, HTML: renderText(best, text)}, nil
}

// isMarkdown says whether p is rendered as markdown.
func isMarkdown(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".md", ".markdown", ".mdown", ".mkd":
		return true
	}
	return false
}

// renderText is a README's HTML: markdown rendered, anything else as text.
func renderText(p string, text []byte) string {
	if isMarkdown(p) {
		return Markdown(text, path.Dir(p))
	}
	var b strings.Builder
	b.WriteString("<pre>")
	b.WriteString(html.EscapeString(string(text)))
	b.WriteString("</pre>")
	return b.String()
}

// entry is what ls-tree says of the single path p at sha: its mode, type,
// object and size.
func entry(ctx context.Context, dir, sha, p string) (mode, typ, oid string, size int64, err error) {
	b, err := out(ctx, dir, nil, "ls-tree", "-z", "--format=%(objectmode)%x00%(objecttype)%x00%(objectname)%x00%(objectsize)", sha, "--", p)
	if err != nil {
		return "", "", "", 0, err
	}
	f := strings.Split(string(b), "\x00")
	if len(f) < 4 {
		return "", "", "", 0, errNoPath
	}
	size, _ = strconv.ParseInt(f[3], 10, 64)
	return f[0], f[1], f[2], size, nil
}

func blob(ctx context.Context, dir string, q url.Values) (*Blob, error) {
	p := q.Get("path")
	if p == "" || !ValidPath(p) {
		return nil, errBadPath
	}
	ref, err := resolve(ctx, dir, q.Get("ref"))
	if err != nil {
		return nil, err
	}
	mode, typ, oid, size, err := entry(ctx, dir, ref.SHA, p)
	if err != nil {
		return nil, err
	}
	if typ != "blob" {
		return nil, errNotFile
	}
	text, cut, err := head(ctx, dir, maxBlob, "cat-file", "blob", oid)
	if err != nil {
		return nil, err
	}
	b := &Blob{Ref: ref, Path: p, Kind: "file", Size: size, Truncated: cut}
	if mode == "120000" {
		b.Kind = "symlink"
	}
	if bytes.IndexByte(text[:min(len(text), binarySniffLen)], 0) >= 0 {
		b.Binary = true
	} else {
		b.Text = string(text)
		if isMarkdown(p) && b.Kind == "file" {
			b.HTML = Markdown(text, path.Dir(p))
		}
	}
	if b.LastCommit, err = lastCommit(ctx, dir, ref.SHA, p); err != nil {
		return nil, err
	}
	return b, nil
}

// raw streams a file's bytes to w, with its size as Content-Length.
func raw(ctx context.Context, dir string, q url.Values, w http.ResponseWriter) error {
	p := q.Get("path")
	if p == "" || !ValidPath(p) {
		return errBadPath
	}
	ref, err := resolve(ctx, dir, q.Get("ref"))
	if err != nil {
		return err
	}
	_, typ, oid, size, err := entry(ctx, dir, ref.SHA, p)
	if err != nil {
		return err
	}
	if typ != "blob" {
		return errNotFile
	}
	cmd := command(ctx, "", "--git-dir="+dir, "cat-file", "blob", oid)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
	cmd.Stdout = w
	return cmd.Run()
}

// commits pages the history from ref (or from the commit a cursor kept),
// touching p when given.
func commits(ctx context.Context, dir string, q url.Values) (Page[Commit], error) {
	p := q.Get("path")
	if !ValidPath(p) {
		return Page[Commit]{}, errBadPath
	}
	_, limit, err := page(url.Values{"limit": {q.Get("limit")}}, defaultPage, maxPage)
	if err != nil {
		return Page[Commit]{}, err
	}
	var tip string
	offset := 0
	if c := q.Get("cursor"); c != "" {
		raw, err := base64.RawURLEncoding.DecodeString(c)
		sha, off, ok := strings.Cut(string(raw), "|")
		if err != nil || !ok || !isHex(sha, 40, 64) {
			return Page[Commit]{}, errBadCursor
		}
		if offset, err = strconv.Atoi(off); err != nil || offset < 0 {
			return Page[Commit]{}, errBadCursor
		}
		if tip, err = revParse(ctx, dir, sha); err != nil {
			return Page[Commit]{}, err
		} else if tip == "" {
			return Page[Commit]{}, errBadCursor
		}
	} else {
		ref, err := resolve(ctx, dir, q.Get("ref"))
		if err != nil {
			return Page[Commit]{}, err
		}
		tip = ref.SHA
	}
	args := []string{"log", logFormat, "--skip=" + strconv.Itoa(offset), "-n", strconv.Itoa(limit + 1), tip}
	if p != "" {
		args = append(args, "--", p)
	}
	b, err := out(ctx, dir, nil, args...)
	if err != nil {
		return Page[Commit]{}, err
	}
	cs := parseCommits(b)
	pg := Page[Commit]{Items: cs}
	if len(cs) > limit {
		pg.Items = cs[:limit]
		pg.Next = base64.RawURLEncoding.EncodeToString([]byte(tip + "|" + strconv.Itoa(offset+limit)))
	}
	return pg, nil
}

// commitAt is the commit sha names, with its message's body.
func commitAt(ctx context.Context, dir, sha string) (*Commit, error) {
	if !isHex(sha, 4, 64) {
		return nil, errBadRef
	}
	full, err := revParse(ctx, dir, sha)
	if err != nil {
		return nil, err
	}
	if full == "" {
		return nil, errNoRef
	}
	b, err := out(ctx, dir, nil, "log", "-1", logFormatBody, full)
	if err != nil {
		return nil, err
	}
	cs := parseCommits(b)
	if len(cs) == 0 {
		return nil, errNoRef
	}
	return &cs[0], nil
}

// against is diff-tree's arguments for c against its first parent, or as
// a root commit.
func against(c *Commit) []string {
	if len(c.Parents) == 0 {
		return []string{"--root", c.SHA}
	}
	return []string{c.Parents[0], c.SHA}
}

var statusNames = map[byte]string{'A': "added", 'M': "modified", 'D': "deleted", 'R': "renamed", 'C': "copied", 'T': "type_changed"}

// changes is every file that changed in rng (diff-tree's two commits, or
// --root and one), in git's order, from one diff-tree with --raw and
// --numstat (each file's raw record first, then each file's numbers, in the
// same order).
func changes(ctx context.Context, dir string, rng []string) ([]FileChange, error) {
	args := append([]string{"diff-tree", "-r", "-z", "--raw", "--numstat", "-M", "--no-commit-id", "--no-ext-diff", "--no-textconv"}, rng...)
	b, err := out(ctx, dir, nil, args...)
	if err != nil {
		return nil, err
	}
	tok := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
	files := make([]FileChange, 0, len(tok)/4)
	i := 0
	for i < len(tok) && strings.HasPrefix(tok[i], ":") {
		f := strings.Fields(tok[i])
		if len(f) < 5 || i+1 >= len(tok) {
			break
		}
		fc := FileChange{Status: statusNames[f[4][0]]}
		if fc.Status == "" {
			fc.Status = "modified"
		}
		if f[4][0] == 'R' || f[4][0] == 'C' {
			if i+2 >= len(tok) {
				break
			}
			fc.OldPath, fc.Path = tok[i+1], tok[i+2]
			i += 3
		} else {
			fc.Path = tok[i+1]
			i += 2
		}
		files = append(files, fc)
	}
	for n := 0; i < len(tok) && n < len(files); n++ {
		f := strings.SplitN(tok[i], "\t", 3)
		if len(f) < 3 {
			break
		}
		if f[0] == "-" {
			files[n].Binary = true
		} else {
			files[n].Additions, _ = strconv.Atoi(f[0])
			files[n].Deletions, _ = strconv.Atoi(f[1])
		}
		if f[2] == "" { // a rename or copy: old and new follow
			i += 3
		} else {
			i++
		}
	}
	return files, nil
}

func commitDetail(ctx context.Context, dir, sha string) (*CommitDetail, error) {
	c, err := commitAt(ctx, dir, sha)
	if err != nil {
		return nil, err
	}
	files, err := changes(ctx, dir, against(c))
	if err != nil {
		return nil, err
	}
	d := &CommitDetail{Commit: *c, Files: files}
	d.Stats.Files = len(files)
	for _, f := range files {
		d.Stats.Additions += f.Additions
		d.Stats.Deletions += f.Deletions
	}
	if len(d.Files) > maxFiles {
		d.Files, d.FilesTruncated = d.Files[:maxFiles], true
	}
	return d, nil
}

// diff pages c's patches against its first parent.
func diff(ctx context.Context, dir, sha string, q url.Values) (Page[FileDiff], error) {
	c, err := commitAt(ctx, dir, sha)
	if err != nil {
		return Page[FileDiff]{}, err
	}
	return patches(ctx, dir, against(c), q)
}

// patches pages rng's patches, file by file: one diff-tree for the page's
// files (and the old names of its renames), split at each "diff --git"
// header, which come in the order of changes.
func patches(ctx context.Context, dir string, rng []string, q url.Values) (Page[FileDiff], error) {
	offset, limit, err := page(q, defaultDiffs, maxDiffs)
	if err != nil {
		return Page[FileDiff]{}, err
	}
	files, err := changes(ctx, dir, rng)
	if err != nil {
		return Page[FileDiff]{}, err
	}
	pg := pageOf(files, offset, limit)
	res := Page[FileDiff]{Items: make([]FileDiff, 0, len(pg.Items)), Next: pg.Next}
	if len(pg.Items) == 0 {
		return res, nil
	}
	args := append([]string{"diff-tree", "-r", "-p", "-M", "--no-color", "--no-ext-diff", "--no-textconv", "--no-commit-id"}, rng...)
	args = append(args, "--")
	for _, f := range pg.Items {
		args = append(args, f.Path)
		if f.OldPath != "" {
			args = append(args, f.OldPath)
		}
	}
	b, err := out(ctx, dir, nil, args...)
	if err != nil {
		return Page[FileDiff]{}, err
	}
	sections := splitPatch(b)
	if len(sections) != len(pg.Items) {
		return Page[FileDiff]{}, errDiffSplit
	}
	for i, f := range pg.Items {
		patch := sections[i]
		fd := FileDiff{FileChange: f}
		if at := strings.Index(patch, "\n@@"); at >= 0 {
			patch = patch[at+1:]
		} else {
			patch = ""
		}
		if len(patch) > maxPatch {
			cut := strings.LastIndexByte(patch[:maxPatch], '\n')
			patch, fd.PatchTruncated = patch[:cut+1], true
		}
		fd.Patch = patch
		res.Items = append(res.Items, fd)
	}
	return res, nil
}

// splitPatch cuts diff-tree -p's output at each "diff --git" line. A line
// of a hunk starts with ' ', '+', '-' or '\', so it can't be one.
func splitPatch(b []byte) []string {
	s := string(b)
	var sections []string
	for {
		start := strings.Index(s, "diff --git ")
		if start < 0 {
			break
		}
		next := strings.Index(s[start+1:], "\ndiff --git ")
		if next < 0 {
			sections = append(sections, s[start:])
			break
		}
		sections = append(sections, s[start:start+1+next+1])
		s = s[start+1+next+1:]
	}
	return sections
}

// fixHead points a HEAD whose branch doesn't exist at main, then master,
// then the newest branch, so a clone checks something out and the browser
// opens on it. It runs after a push.
func fixHead(ctx context.Context, dir string) error {
	name, err := defaultBranch(ctx, dir)
	if err != nil {
		return err
	}
	if sha, err := revParse(ctx, dir, "refs/heads/"+name); err != nil || sha != "" {
		return err
	}
	pick := ""
	for _, n := range []string{"main", "master"} {
		if sha, err := revParse(ctx, dir, "refs/heads/"+n); err != nil {
			return err
		} else if sha != "" {
			pick = n
			break
		}
	}
	if pick == "" {
		b, err := out(ctx, dir, nil, "for-each-ref", "--sort=-committerdate", "--count=1", "--format=%(refname:lstrip=2)", "refs/heads")
		if err != nil {
			return err
		}
		pick = strings.TrimSpace(string(b))
	}
	if pick == "" {
		return nil
	}
	_, err = out(ctx, dir, nil, "symbolic-ref", "HEAD", "refs/heads/"+pick)
	return err
}

// ─── the executor's handlers ─────────────────────────────────────────────────

// handleBrowse serves GET /v1/repos/{id}/browse/{op}: summary, branches,
// tags, tree, blob, commits, commit, diff, and for pull requests compare,
// compare-diff, pulls-status and range, as JSON.
func (e *Executor) handleBrowse(w http.ResponseWriter, r *http.Request) {
	e.browse(w, r, func(ctx context.Context, dir string, q url.Values) (any, error) {
		switch r.PathValue("op") {
		case "summary":
			return summary(ctx, dir)
		case "branches":
			return branches(ctx, dir, q)
		case "tags":
			return tags(ctx, dir, q)
		case "tree":
			return tree(ctx, dir, q)
		case "blob":
			return blob(ctx, dir, q)
		case "commits":
			return commits(ctx, dir, q)
		case "commit":
			return commitDetail(ctx, dir, q.Get("sha"))
		case "diff":
			return diff(ctx, dir, q.Get("sha"), q)
		case "compare":
			return compare(ctx, dir, q)
		case "compare-diff":
			return compareDiff(ctx, dir, q)
		case "pulls-status":
			return pullsStatus(ctx, dir, q)
		case "range":
			return pushRange(ctx, dir, q)
		}
		return nil, browseErr(http.StatusNotFound, "unknown_op", "unknown read")
	})
}

// handleRaw serves GET /v1/repos/{id}/raw: a file's bytes.
func (e *Executor) handleRaw(w http.ResponseWriter, r *http.Request) {
	e.browse(w, r, func(ctx context.Context, dir string, q url.Values) (any, error) {
		return nil, raw(ctx, dir, q, w)
	})
}

// browse runs read on repository {id} within the executor's limits and
// writes its answer as JSON, or its error; a nil answer means read wrote
// its own.
func (e *Executor) browse(w http.ResponseWriter, r *http.Request, read func(context.Context, string, url.Values) (any, error)) {
	id := r.PathValue("id")
	dir, err := RepoPath(e.root, id)
	if err != nil {
		writeErr(w, browseErr(http.StatusBadRequest, "bad_repository", err.Error()))
		return
	}
	if err := e.init(r.Context(), dir); err != nil {
		writeErr(w, err)
		return
	}
	release, err := e.acquire(r.Context(), id, "", "")
	if err != nil {
		writeErr(w, browseErr(http.StatusServiceUnavailable, "git_busy", err.Error()))
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(r.Context(), browseTimeout)
	defer cancel()
	v, err := read(ctx, dir, r.URL.Query())
	if err != nil {
		writeErr(w, err)
		return
	}
	if v == nil {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	be, ok := err.(*BrowseError)
	switch {
	case ok:
	case errors.Is(err, context.DeadlineExceeded):
		be = browseErr(http.StatusServiceUnavailable, "git_busy", "git took too long to answer, try again")
	default:
		be = browseErr(http.StatusInternalServerError, "git_failed", err.Error())
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(be.Status)
	_ = json.NewEncoder(w).Encode(be)
}
