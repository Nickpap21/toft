package gitstore

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Pull requests' git (D-75): comparing two branches, merging one into the
// other, the default branch, refs/pull/<n>/head, and the push hooks. As
// with the browser's reads, the executor builds every command: what comes
// over the socket is branch names, SHAs, a number, identities and a
// message, each checked here.

// HooksDir holds the hooks the paas-git image ships (pre-receive and
// post-receive, both toft-git); receive-pack runs with core.hooksPath set
// to it, every other git program with no hooks at all.
const HooksDir = "/usr/local/lib/toft-git/hooks"

const (
	maxPullCommits = 250 // a comparison's commits, as GitHub shows
	statusBudget   = 5 * time.Second
	zeroSHA        = "0000000000000000000000000000000000000000"
)

var (
	errUnrelated      = browseErr(http.StatusUnprocessableEntity, "unrelated_histories", "these branches share no history, so one can't be merged into the other")
	errConflicts      = browseErr(http.StatusConflict, "conflicts", "the branches conflict: merge the base into the head branch, resolve the files and push")
	errHeadMoved      = browseErr(http.StatusConflict, "head_moved", "the branch got new commits since you looked: check them, then merge again")
	errBaseMoved      = browseErr(http.StatusConflict, "base_moved", "the base branch moved while merging: try again")
	errNotFastForward = browseErr(http.StatusConflict, "not_fast_forward", "the base branch has commits the head branch doesn't: merge or squash instead, or bring the head up to date")
	errNothingToMerge = browseErr(http.StatusUnprocessableEntity, "no_commits", "the base branch already has every commit of the head branch")
	errBadIdentity    = browseErr(http.StatusBadRequest, "bad_identity", "a name or email can't hold a line break, NUL or angle bracket, and has 255 characters at most")
	errBadMethod      = browseErr(http.StatusBadRequest, "bad_method", "method is merge, squash or fast_forward")
	errBadMessage     = browseErr(http.StatusBadRequest, "bad_message", "the commit message is empty or longer than 64 KiB")
)

// Compare is what merging head into base would bring.
type Compare struct {
	Base      Ref    `json:"base"`
	Head      Ref    `json:"head"`
	MergeBase string `json:"merge_base"`
	// Ahead counts head's commits base lacks; Behind base's head lacks.
	Ahead  int `json:"ahead"`
	Behind int `json:"behind"`
	// Mergeable is false with the conflicting paths when merge-tree finds
	// conflicts; FastForward when base is where head left it.
	Mergeable        bool         `json:"mergeable"`
	Conflicts        []string     `json:"conflicts"`
	FastForward      bool         `json:"fast_forward"`
	Commits          []Commit     `json:"commits"`
	CommitsTruncated bool         `json:"commits_truncated"`
	Stats            Stats        `json:"stats"`
	Files            []FileChange `json:"files"`
	FilesTruncated   bool         `json:"files_truncated"`
}

// compare is head against base: base and head are branch names, tags or
// SHAs, as the browser's ref; mergeBase, when set, is where head left base
// (a merged pull request's, whose base now holds its head).
func compare(ctx context.Context, dir string, q url.Values) (*Compare, error) {
	base, err := resolve(ctx, dir, q.Get("base"))
	if err != nil {
		return nil, err
	}
	head, err := resolve(ctx, dir, q.Get("head"))
	if err != nil {
		return nil, err
	}
	c := &Compare{Base: base, Head: head, Conflicts: []string{}, Commits: []Commit{}}
	if mb := q.Get("merge_base"); mb != "" {
		if !isHex(mb, 40, 64) {
			return nil, errBadRef
		}
		c.MergeBase = mb
	} else if c.MergeBase, err = mergeBase(ctx, dir, base.SHA, head.SHA); err != nil {
		return nil, err
	}
	if c.Ahead, c.Behind, err = aheadBehind(ctx, dir, base.SHA, head.SHA); err != nil {
		return nil, err
	}
	c.FastForward = c.MergeBase == base.SHA
	if q.Get("merge_base") == "" {
		if c.Conflicts, err = conflicts(ctx, dir, base.SHA, head.SHA); err != nil {
			return nil, err
		}
		c.Mergeable = len(c.Conflicts) == 0
	}
	// The commits head adds, oldest first: log can't page oldest first, so
	// a comparison shows its first 250, as GitHub does.
	b, err := out(ctx, dir, nil, "log", "--reverse", logFormat, c.MergeBase+".."+head.SHA)
	if err != nil {
		return nil, err
	}
	c.Commits = parseCommits(b)
	if len(c.Commits) > maxPullCommits {
		c.Commits, c.CommitsTruncated = c.Commits[:maxPullCommits], true
	}
	if c.Files, err = changes(ctx, dir, []string{c.MergeBase, head.SHA}); err != nil {
		return nil, err
	}
	c.Stats.Files = len(c.Files)
	for _, f := range c.Files {
		c.Stats.Additions += f.Additions
		c.Stats.Deletions += f.Deletions
	}
	if len(c.Files) > maxFiles {
		c.Files, c.FilesTruncated = c.Files[:maxFiles], true
	}
	return c, nil
}

// compareDiff pages the patches of head against where it left base.
func compareDiff(ctx context.Context, dir string, q url.Values) (Page[FileDiff], error) {
	base, err := resolve(ctx, dir, q.Get("base"))
	if err != nil {
		return Page[FileDiff]{}, err
	}
	head, err := resolve(ctx, dir, q.Get("head"))
	if err != nil {
		return Page[FileDiff]{}, err
	}
	mb := q.Get("merge_base")
	if mb != "" && !isHex(mb, 40, 64) {
		return Page[FileDiff]{}, errBadRef
	}
	if mb == "" {
		if mb, err = mergeBase(ctx, dir, base.SHA, head.SHA); err != nil {
			return Page[FileDiff]{}, err
		}
	}
	return patches(ctx, dir, []string{mb, head.SHA}, q)
}

func mergeBase(ctx context.Context, dir, a, b string) (string, error) {
	o, err := out(ctx, dir, nil, "merge-base", a, b)
	if exited1(err) {
		return "", errUnrelated
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(o)), nil
}

// aheadBehind is how many commits head has that base lacks, and base has
// that head lacks.
func aheadBehind(ctx context.Context, dir, base, head string) (ahead, behind int, err error) {
	o, err := out(ctx, dir, nil, "rev-list", "--left-right", "--count", base+"..."+head)
	if err != nil {
		return 0, 0, err
	}
	f := strings.Fields(string(o))
	if len(f) != 2 {
		return 0, 0, fmt.Errorf("rev-list --count: %q", o)
	}
	behind, _ = strconv.Atoi(f[0])
	ahead, _ = strconv.Atoi(f[1])
	return ahead, behind, nil
}

// mergeTree merges head into base without touching a ref: the merged tree,
// or the paths that conflict. Its exit status, never an empty list, says
// which (the spec's rule).
func mergeTree(ctx context.Context, dir, base, head string) (tree string, conflicted []string, err error) {
	o, err := out(ctx, dir, nil, "merge-tree", "--write-tree", "--name-only", "--no-messages", "-z", base, head)
	var g *gitFailed
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &g) && errors.As(g.err, &exit) && exit.ExitCode() == 1:
		// Conflicts: the tree is still printed, then the paths.
		fields := strings.Split(strings.TrimRight(g.stdout, "\x00"), "\x00")
		conflicted = make([]string, 0, len(fields))
		for _, f := range fields[1:] {
			if f != "" {
				conflicted = append(conflicted, f)
			}
		}
		return "", conflicted, nil
	case errors.As(err, &g) && strings.Contains(g.stderr, "unrelated histories"):
		return "", nil, errUnrelated
	default:
		return "", nil, err
	}
	return strings.TrimRight(string(o), "\x00\n"), nil, nil
}

func conflicts(ctx context.Context, dir, base, head string) ([]string, error) {
	_, c, err := mergeTree(ctx, dir, base, head)
	if c == nil {
		c = []string{}
	}
	if len(c) > 100 {
		c = c[:100]
	}
	return c, err
}

// PullStatus is a pull request's head against its base, for the list.
type PullStatus struct {
	Ahead  int `json:"ahead"`
	Behind int `json:"behind"`
	// Conflicts is null when the branches are missing or the list's time
	// ran out before this one.
	Conflicts *bool `json:"conflicts"`
}

// pullsStatus answers each "base..head" pair in q's pair values, within
// statusBudget; a pair whose branch is gone gets a zero status.
func pullsStatus(ctx context.Context, dir string, q url.Values) ([]PullStatus, error) {
	pairs := q["pair"]
	if len(pairs) > maxPage {
		return nil, errBadLimit
	}
	bctx, cancel := context.WithTimeout(ctx, statusBudget)
	defer cancel()
	res := make([]PullStatus, len(pairs))
	for i, p := range pairs {
		base, head, ok := strings.Cut(p, "..")
		if !ok || !ValidRef(base) || !ValidRef(head) {
			return nil, errBadRef
		}
		if bctx.Err() != nil {
			continue
		}
		b, err1 := revParse(bctx, dir, "refs/heads/"+base)
		h, err2 := revParse(bctx, dir, "refs/heads/"+head)
		if err1 != nil || err2 != nil || b == "" || h == "" {
			continue
		}
		if res[i].Ahead, res[i].Behind, err1 = aheadBehind(bctx, dir, b, h); err1 != nil {
			continue
		}
		if _, c, err := mergeTree(bctx, dir, b, h); err == nil {
			has := len(c) > 0
			res[i].Conflicts = &has
		}
	}
	return res, nil
}

// pushRange says how many commits a push added to a branch (old to new),
// the newest 20 of them oldest first, and whether it rewrote the branch
// (old is not in new's history).
func pushRange(ctx context.Context, dir string, q url.Values) (map[string]any, error) {
	from, to := q.Get("from"), q.Get("to")
	if !isHex(from, 40, 64) || !isHex(to, 40, 64) {
		return nil, errBadRef
	}
	if from == zeroSHA {
		return map[string]any{"count": 0, "forced": false, "commits": []Commit{}}, nil
	}
	_, err := out(ctx, dir, nil, "merge-base", "--is-ancestor", from, to)
	forced := exited1(err)
	if err != nil && !forced {
		return nil, err
	}
	n, err := out(ctx, dir, nil, "rev-list", "--count", from+".."+to)
	if err != nil {
		return nil, err
	}
	count, _ := strconv.Atoi(strings.TrimSpace(string(n)))
	b, err := out(ctx, dir, nil, "log", "-20", "--reverse", logFormat, from+".."+to)
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": count, "forced": forced, "commits": parseCommits(b)}, nil
}

// ─── writes ──────────────────────────────────────────────────────────────────

// Identity is a commit's author or committer.
type Identity struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

func (i Identity) valid() bool {
	ok := func(s string) bool {
		return s != "" && len(s) <= 255 && !strings.ContainsAny(s, "\n\r\x00<>")
	}
	return ok(i.Name) && ok(i.Email)
}

// MergeRequest is POST /v1/repos/{id}/merge.
type MergeRequest struct {
	Method string `json:"method"` // merge, squash, fast_forward
	Base   string `json:"base"`   // branch names
	Head   string `json:"head"`
	// ExpectedHead is the head the person merging saw: a merge of anything
	// newer is refused.
	ExpectedHead string `json:"expected_head"`
	// PullNumber's refs/pull/<n>/head is set to the merged head.
	PullNumber   int      `json:"pull_number"`
	DeleteBranch bool     `json:"delete_branch"`
	Message      string   `json:"message"`
	Author       Identity `json:"author"`
	Committer    Identity `json:"committer"`
}

// MergeResult is the commit the base moved to, where it moved from, where
// the head left it, and the head that was merged.
type MergeResult struct {
	SHA       string `json:"sha"`
	Before    string `json:"before"`
	MergeBase string `json:"merge_base"`
	Head      string `json:"head"`
}

// merge merges Head into Base by Method in one ref transaction that checks
// the head is ExpectedHead and the base is where the merge started.
func merge(ctx context.Context, dir string, m MergeRequest) (*MergeResult, error) {
	switch {
	case m.Method != "merge" && m.Method != "squash" && m.Method != "fast_forward":
		return nil, errBadMethod
	case !ValidRef(m.Base) || !ValidRef(m.Head) || m.Base == m.Head || !isHex(m.ExpectedHead, 40, 64) || m.PullNumber <= 0:
		return nil, errBadRef
	case m.Method != "fast_forward" && (strings.TrimSpace(m.Message) == "" || len(m.Message) > 64<<10 || strings.ContainsRune(m.Message, 0)):
		return nil, errBadMessage
	case !m.Author.valid() || !m.Committer.valid():
		return nil, errBadIdentity
	}
	base, err := revParse(ctx, dir, "refs/heads/"+m.Base)
	if err != nil {
		return nil, err
	}
	head, err := revParse(ctx, dir, "refs/heads/"+m.Head)
	if err != nil {
		return nil, err
	}
	switch {
	case base == "" || head == "":
		return nil, errNoRef
	case head != m.ExpectedHead:
		return nil, errHeadMoved
	}
	mb, err := mergeBase(ctx, dir, base, head)
	if err != nil {
		return nil, err
	}
	if mb == head {
		return nil, errNothingToMerge
	}
	res := &MergeResult{Before: base, MergeBase: mb, Head: head}
	switch m.Method {
	case "fast_forward":
		if mb != base {
			return nil, errNotFastForward
		}
		res.SHA = head
	default:
		tree, conflicted, err := mergeTree(ctx, dir, base, head)
		if err != nil {
			return nil, err
		}
		if len(conflicted) > 0 {
			return nil, errConflicts
		}
		args := []string{"commit-tree", "--no-gpg-sign", tree, "-p", base}
		if m.Method == "merge" {
			args = append(args, "-p", head)
		}
		now := strconv.FormatInt(time.Now().Unix(), 10) + " +0000"
		cmd := command(ctx, "", append([]string{"--git-dir=" + dir}, append(args, "-F", "-")...)...)
		cmd.Env = append(cmd.Env,
			"GIT_AUTHOR_NAME="+m.Author.Name, "GIT_AUTHOR_EMAIL="+m.Author.Email, "GIT_AUTHOR_DATE="+now,
			"GIT_COMMITTER_NAME="+m.Committer.Name, "GIT_COMMITTER_EMAIL="+m.Committer.Email, "GIT_COMMITTER_DATE="+now,
		)
		cmd.Stdin = strings.NewReader(m.Message)
		stderr := &capped{max: 4096}
		cmd.Stderr = stderr
		o, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("git commit-tree: %w: %s", err, stderr.String())
		}
		res.SHA = strings.TrimSpace(string(o))
	}
	// One transaction: the head is still what was seen (or goes, when
	// asked), the base moves from where it was, and refs/pull/<n>/head keeps
	// the merged head.
	headRef := "refs/heads/" + m.Head
	tx := "start\n"
	if m.DeleteBranch {
		tx += "delete " + headRef + " " + head + "\n"
	} else {
		tx += "verify " + headRef + " " + head + "\n"
	}
	tx += "update refs/heads/" + m.Base + " " + res.SHA + " " + base + "\n"
	tx += "update refs/pull/" + strconv.Itoa(m.PullNumber) + "/head " + head + "\n"
	tx += "commit\n"
	if _, err := out(ctx, dir, strings.NewReader(tx), "update-ref", "--stdin"); err != nil {
		now, rerr := revParse(ctx, dir, "refs/heads/"+m.Base)
		if rerr == nil && now != base {
			return nil, errBaseMoved
		}
		var g *gitFailed
		if errors.As(err, &g) {
			return nil, errHeadMoved
		}
		return nil, err
	}
	return res, nil
}

// setHead makes branch, which must exist, the default branch.
func setHead(ctx context.Context, dir, branch string) error {
	if !ValidRef(branch) {
		return errBadRef
	}
	sha, err := revParse(ctx, dir, "refs/heads/"+branch)
	if err != nil {
		return err
	}
	if sha == "" {
		return errNoRef
	}
	_, err = out(ctx, dir, nil, "symbolic-ref", "HEAD", "refs/heads/"+branch)
	return err
}

// setPullRef points refs/pull/<number>/head at sha, a commit in the
// repository.
func setPullRef(ctx context.Context, dir string, number int, sha string) error {
	if number <= 0 || !isHex(sha, 40, 64) {
		return errBadRef
	}
	if got, err := revParse(ctx, dir, sha); err != nil {
		return err
	} else if got == "" {
		return errNoRef
	}
	_, err := out(ctx, dir, nil, "update-ref", "refs/pull/"+strconv.Itoa(number)+"/head", sha)
	return err
}

// CreateBranchRequest makes Branch at From (a branch, tag or commit).
type CreateBranchRequest struct {
	Branch string `json:"branch"`
	From   string `json:"from"`
}

// CreatedBranch is a branch create-branch made.
type CreatedBranch struct {
	Branch string `json:"branch"`
	SHA    string `json:"sha"`
}

var errBranchExists = browseErr(http.StatusConflict, "branch_exists", "a branch by that name exists")

// createBranch makes a branch at from's commit, refusing one that exists:
// update-ref with an all-zero old value only creates.
func createBranch(ctx context.Context, dir string, b CreateBranchRequest) (*CreatedBranch, error) {
	if !ValidRef(b.Branch) || b.Branch == "HEAD" || !ValidRef(b.From) {
		return nil, errBadRef
	}
	sha, err := revParse(ctx, dir, b.From)
	if err != nil {
		return nil, err
	}
	if sha == "" {
		return nil, errNoRef
	}
	if got, err := revParse(ctx, dir, "refs/heads/"+b.Branch); err != nil {
		return nil, err
	} else if got != "" {
		return nil, errBranchExists
	}
	if _, err := out(ctx, dir, nil, "update-ref", "refs/heads/"+b.Branch, sha, strings.Repeat("0", len(sha))); err != nil {
		return nil, errBranchExists
	}
	return &CreatedBranch{Branch: b.Branch, SHA: sha}, nil
}

// handleWrite serves POST /v1/repos/{id}/{op} for merge, head and pull-ref,
// a JSON body in and a JSON answer out, within the executor's limits.
func (e *Executor) handleWrite(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		e.browse(w, r, func(ctx context.Context, dir string, _ url.Values) (any, error) {
			body := io.LimitReader(r.Body, 128<<10)
			switch op {
			case "merge":
				var m MergeRequest
				if err := json.NewDecoder(body).Decode(&m); err != nil {
					return nil, browseErr(http.StatusBadRequest, "bad_request", "unreadable merge request")
				}
				return merge(ctx, dir, m)
			case "head":
				var h struct{ Branch string }
				if err := json.NewDecoder(body).Decode(&h); err != nil {
					return nil, browseErr(http.StatusBadRequest, "bad_request", "unreadable request")
				}
				return map[string]string{"branch": h.Branch}, setHead(ctx, dir, h.Branch)
			case "pull-ref":
				var p struct {
					Number int
					SHA    string
				}
				if err := json.NewDecoder(body).Decode(&p); err != nil {
					return nil, browseErr(http.StatusBadRequest, "bad_request", "unreadable request")
				}
				return map[string]string{"sha": p.SHA}, setPullRef(ctx, dir, p.Number, p.SHA)
			case "create-branch":
				var b CreateBranchRequest
				if err := json.NewDecoder(body).Decode(&b); err != nil {
					return nil, browseErr(http.StatusBadRequest, "bad_request", "unreadable request")
				}
				return createBranch(ctx, dir, b)
			}
			return nil, browseErr(http.StatusNotFound, "unknown_op", "unknown write")
		})
	}
}

// ─── push hooks ──────────────────────────────────────────────────────────────

// RefUpdate is one ref a push moved: Old or New is all zeros for a ref it
// created or deleted.
type RefUpdate struct {
	Ref string `json:"ref"`
	Old string `json:"old"`
	New string `json:"new"`
}

// The hooks' settings, in the environment receive-pack gives them.
const (
	envProtect  = "TOFT_PROTECT_BRANCH" // the protected branch: refuse its updates and deletion (by name, never HEAD's; L2)
	envPushLog  = "TOFT_PUSH_LOG"       // where post-receive writes what moved
	envReadOnly = "TOFT_READ_ONLY"      // the URL a pull mirror pulls from: refuse every push (D-79)
	envPushOnly = "TOFT_PUSH_ONLY"      // an agent's branch: refuse every other ref and its deletion (D-102)
)

// HookMain runs a hook when this program was started as one (git runs
// HooksDir/pre-receive or post-receive, links to toft-git) and exits with
// its status; otherwise it returns.
func HookMain() {
	name := filepath.Base(os.Args[0])
	if name != "pre-receive" && name != "post-receive" {
		return
	}
	os.Exit(runHook(name, os.Stdin, os.Stderr))
}

// runHook is git's pre-receive (refuse a branch named HEAD, and the
// protected branch's updates) or post-receive (note
// what moved for the executor). Each line on stdin is "<old> <new> <ref>".
func runHook(name string, stdin io.Reader, stderr io.Writer) int {
	var lines []RefUpdate
	sc := bufio.NewScanner(stdin)
	for sc.Scan() {
		// Split on single spaces, as git writes them: a ref name may hold
		// other whitespace (U+00A0), which strings.Fields would split on.
		f := strings.SplitN(sc.Text(), " ", 3)
		if len(f) != 3 {
			if name == "pre-receive" {
				fmt.Fprintf(stderr, "toft: couldn't read the update %q\n", sc.Text())
				return 1
			}
			continue
		}
		lines = append(lines, RefUpdate{Old: f[0], New: f[1], Ref: f[2]})
	}
	if name == "post-receive" {
		path := os.Getenv(envPushLog)
		if path == "" {
			return 0
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			fmt.Fprintln(stderr, "toft: couldn't note the push:", err)
			return 0
		}
		defer f.Close()
		_ = json.NewEncoder(f).Encode(lines)
		return 0
	}
	if from := os.Getenv(envReadOnly); from != "" && len(lines) > 0 {
		fmt.Fprintf(stderr, "toft: this repository mirrors %s, so it's read-only here.\n", from)
		fmt.Fprintf(stderr, "toft: push there instead (it arrives within 10 minutes), or stop mirroring under Code → Settings.\n")
		return 1
	}
	if only := os.Getenv(envPushOnly); only != "" {
		for _, u := range lines {
			if u.Ref != "refs/heads/"+only {
				fmt.Fprintf(stderr, "toft: this agent pushes only to %s, which it created; %s was refused.\n", only, u.Ref)
				return 1
			}
			if strings.Trim(u.New, "0") == "" {
				fmt.Fprintf(stderr, "toft: an agent can't delete its branch %s; a person can.\n", only)
				return 1
			}
		}
	}
	protected := ""
	if b := os.Getenv(envProtect); b != "" {
		protected = "refs/heads/" + b
	}
	refused := false
	for _, u := range lines {
		switch {
		case u.Ref == "refs/heads/HEAD":
			fmt.Fprintln(stderr, "toft: a branch can't be called HEAD")
			refused = true
		case protected != "" && u.Ref == protected && strings.Trim(u.Old, "0") != "":
			b := strings.TrimPrefix(protected, "refs/heads/")
			fmt.Fprintf(stderr, "toft: %s is protected: it changes only by merging a pull request.\n", b)
			fmt.Fprintf(stderr, "toft: push your work to another branch and open one, or ask an owner to turn protection off under Code → Settings.\n")
			refused = true
		}
	}
	if refused {
		return 1
	}
	return 0
}

// readPushLog is what post-receive wrote, nil when nothing moved.
func readPushLog(path string) []RefUpdate {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var all []RefUpdate
	dec := json.NewDecoder(strings.NewReader(string(b)))
	for {
		var batch []RefUpdate
		if err := dec.Decode(&batch); err != nil {
			break
		}
		all = append(all, batch...)
	}
	if len(all) > 1000 {
		all = all[:1000]
	}
	return all
}
