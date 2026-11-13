package code

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"github.com/Nickpap21/toft/backend/pkg/actor"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"go.uber.org/zap"
)

// Several repositories per project, their settings, and pull requests
// (D-75). git's side of each is an executor operation (gitstore); this is
// who may do what, what is stored, and what the conversation records.

var repoName = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,98}[a-z0-9])?$`)

// CreateRepository adds an empty repository to a project. The caller has
// checked the project is in orgID and the caller may write there.
func (s *Service) CreateRepository(ctx context.Context, orgID, projectID, name, requestHost string) (*Repo, error) {
	if !s.Enabled() {
		return nil, ErrGitOff
	}
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".git")
	if !repoName.MatchString(name) || strings.HasSuffix(name, ".git") {
		return nil, ErrBadName
	}
	stored, err := s.repo.CreateRepo(ctx, orgID, projectID, name)
	if err != nil {
		return nil, err
	}
	// The executor would make it on first use; making it now catches a
	// broken executor here rather than at the first push.
	if err := s.git.Init(ctx, stored.ID); err != nil {
		s.log.Warn("init repository", zap.Error(err), zap.String("repository_id", stored.ID))
	}
	r, err := s.repoOf(ctx, projectID, stored.ID)
	if err != nil {
		return nil, err
	}
	repo := s.toRepo(*r, s.hostFor(requestHost))
	return &repo, nil
}

// DeleteRepository removes one of a project's repositories, never its last,
// from the database and then from disk; confirm must be its name.
func (s *Service) DeleteRepository(ctx context.Context, projectID, repoID, confirm string) (*StoredRepo, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, err
	}
	if confirm != r.Name {
		return nil, ErrConfirmName
	}
	// A repository services build from stays (D-77; the database refuses
	// it too): say which.
	targets, err := s.repo.DeployTargets(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	if len(targets) > 0 {
		names := make([]string, 0, len(targets))
		for _, t := range targets {
			names = append(names, t.ServiceName)
		}
		return nil, &RepoInUseError{Services: names}
	}
	// Its pull requests' previews go first (D-97).
	if s.previews != nil {
		if err := s.previews.RemoveForRepository(ctx, r.ID); err != nil {
			return nil, fmt.Errorf("remove its previews: %w", err)
		}
	}
	ok, err := s.repo.DeleteRepo(ctx, projectID, repoID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrLastRepo
	}
	// The row is gone, so nothing reaches the files; if removing them fails
	// they stay on the volume, unreferenced.
	if err := s.git.Delete(ctx, r.ID); err != nil {
		s.log.Error("delete repository files", zap.Error(err), zap.String("repository_id", r.ID))
	}
	return r, nil
}

// UpdateRepository changes a repository's default branch and its
// protection, and says what changed (for the audit log). Protection is
// stored by the branch's name (L2) and moves with the default branch: the
// caller is an owner who confirmed the password.
func (s *Service) UpdateRepository(ctx context.Context, projectID, repoID, requestHost string, in UpdateRepositoryInput) (*RepositoryDetail, map[string]any, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, nil, err
	}
	changed := map[string]any{}
	protect := r.Protect
	if in.ProtectDefaultBranch != nil {
		protect = *in.ProtectDefaultBranch
	}
	var sum gitstore.Summary
	if in.DefaultBranch != nil || protect != r.Protect {
		if err := s.git.Browse(ctx, r.ID, "summary", nil, &sum); err != nil {
			return nil, nil, err
		}
	}
	head := sum.DefaultBranch
	if in.DefaultBranch != nil {
		if b := strings.TrimSpace(*in.DefaultBranch); b != head {
			// HEAD first: if storing the name fails, the branch that was
			// protected stays so.
			if err := s.git.Write(ctx, r.ID, "head", map[string]string{"branch": b}, nil); err != nil {
				return nil, nil, err
			}
			changed["default_branch"] = map[string]string{"from": head, "to": b}
			head = b
		}
	}
	if protect != r.Protect || protect && changed["default_branch"] != nil {
		// A repository with no branch yet stores none: its first push
		// names it (Serve).
		name := head
		if !protect || sum.Empty {
			name = ""
		}
		if err := s.repo.SetProtection(ctx, r.ID, protect, name); err != nil {
			return nil, nil, err
		}
		if protect != r.Protect {
			changed["protect_default_branch"] = protect
		}
		if name != "" {
			changed["protected_branch"] = name
		}
	}
	d, err := s.Repository(ctx, projectID, repoID, requestHost)
	return d, changed, err
}

// ─── pull requests ───────────────────────────────────────────────────────────

func person(id, name string) *Person {
	if id == "" {
		return nil
	}
	if name == "" {
		name = "a deleted account"
	}
	return &Person{ID: id, Name: name}
}

func withAgent(p *Person, agent string) *Person {
	if p != nil {
		p.Agent = agent
	}
	return p
}

func toPullRequest(p *StoredPull) PullRequest {
	return PullRequest{
		Number: p.Number, Title: p.Title, Body: p.Body, HTML: gitstore.Markdown([]byte(p.Body), ""), State: p.State,
		Author: withAgent(person(p.AuthorID, p.AuthorName), p.AgentName), BaseBranch: p.Base, HeadBranch: p.Head, HeadSHA: p.HeadSHA,
		MergeMethod: p.MergeMethod, MergeSHA: p.MergeSHA, MergedBy: person(p.MergedByID, p.MergedByName),
		MergedAt: p.MergedAt, ClosedAt: p.ClosedAt, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, Comments: p.Comments,
	}
}

func numberCursor(n int) string {
	return base64.RawURLEncoding.EncodeToString([]byte("n" + strconv.Itoa(n)))
}

func parseNumberCursor(c string) (int, error) {
	if c == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil || len(b) < 2 || b[0] != 'n' {
		return 0, ErrBadCursor
	}
	n, err := strconv.Atoi(string(b[1:]))
	if err != nil || n <= 0 {
		return 0, ErrBadCursor
	}
	return n, nil
}

// ListPulls pages a repository's pull requests in one state, newest first;
// open ones carry how far their branches are apart and whether they
// conflict.
func (s *Service) ListPulls(ctx context.Context, projectID, repoID, state, cursor string, limit int) ([]PullRequest, string, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, "", err
	}
	if state == "" {
		state = "open"
	}
	if state != "open" && state != "merged" && state != "closed" {
		return nil, "", ErrBadState
	}
	before, err := parseNumberCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	limit = min(max(limit, 1), 100)
	stored, err := s.repo.ListPulls(ctx, r.ID, state, before, limit+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(stored) > limit {
		stored = stored[:limit]
		next = numberCursor(stored[limit-1].Number)
	}
	out := make([]PullRequest, 0, len(stored))
	q := url.Values{}
	for i := range stored {
		out = append(out, toPullRequest(&stored[i]))
		q.Add("pair", stored[i].Base+".."+stored[i].Head)
	}
	heads := make([]string, 0, len(out))
	for _, p := range out {
		heads = append(heads, p.HeadSHA)
	}
	checks, err := s.checksOf(ctx, r.ID, heads)
	if err != nil {
		return nil, "", err
	}
	for i := range out {
		out[i].Checks = checks[out[i].HeadSHA].Summary
	}
	if state == "open" && len(out) > 0 {
		var status []gitstore.PullStatus
		if err := s.git.Browse(ctx, r.ID, "pulls-status", q, &status); err != nil {
			s.log.Warn("pull requests' status", zap.Error(err), zap.String("repository_id", r.ID))
		} else {
			for i := range out {
				if i < len(status) && status[i].Conflicts != nil {
					a, b := status[i].Ahead, status[i].Behind
					out[i].Ahead, out[i].Behind, out[i].Conflicts = &a, &b, status[i].Conflicts
				}
			}
		}
	}
	return out, next, nil
}

// browseError is a *gitstore.BrowseError with code, if err is one.
func browseCode(err error) string {
	var be *gitstore.BrowseError
	if errors.As(err, &be) {
		return be.Code
	}
	return ""
}

// Pull is one pull request with its comparison. An open pull request
// whose branch moved without a push the backend saw (one served by an
// older release, say) is brought up to date first.
func (s *Service) Pull(ctx context.Context, projectID, repoID string, number int) (*PullRequestDetail, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, err
	}
	p, err := s.repo.GetPull(ctx, r.ID, number)
	if err != nil {
		return nil, err
	}
	d := &PullRequestDetail{PullRequest: toPullRequest(p), Issues: []IssueRef{}, Docs: []DocRef{}}
	if s.docs != nil {
		if d.Docs, err = s.docs.PullDocs(ctx, p.ID); err != nil {
			return nil, err
		}
	}
	d.HTML = gitstore.MarkdownRefs([]byte(p.Body), gitstore.Refs{Keys: s.issueKeys(ctx, projectID), Pulls: true, Mentions: s.handles(ctx, projectID)})
	if s.issues != nil {
		if d.Issues, err = s.issues.PullIssues(ctx, p.ID); err != nil {
			return nil, err
		}
	}
	var cmp gitstore.Compare
	if p.State == "merged" {
		// The base now holds the head: compare against where it left.
		q := url.Values{"base": {p.MergeBaseSHA}, "head": {p.HeadSHA}, "merge_base": {p.MergeBaseSHA}}
		if err := s.git.Browse(ctx, r.ID, "compare", q, &cmp); err != nil {
			return nil, err
		}
		d.Compare = &cmp
		return s.withPullChecks(ctx, r.ID, d)
	}
	err = s.git.Browse(ctx, r.ID, "compare", url.Values{"base": {p.Base}, "head": {p.Head}}, &cmp)
	if browseCode(err) == "ref_not_found" {
		// The head branch (or the base) is gone; show what it was.
		err = s.git.Browse(ctx, r.ID, "compare", url.Values{"base": {p.Base}, "head": {p.HeadSHA}}, &cmp)
		if browseCode(err) == "ref_not_found" || browseCode(err) == "unrelated_histories" {
			return s.withPullChecks(ctx, r.ID, d)
		}
		if err != nil {
			return nil, err
		}
		d.Compare = &cmp
		return s.withPullChecks(ctx, r.ID, d)
	}
	if err != nil && browseCode(err) != "unrelated_histories" {
		return nil, err
	}
	if err == nil {
		d.HeadExists, d.Compare = true, &cmp
		if p.State == "open" && cmp.Head.SHA != p.HeadSHA {
			s.recordPush(ctx, r.ID, *p, "", true, p.HeadSHA, cmp.Head.SHA, "")
			d.HeadSHA = cmp.Head.SHA
		}
	}
	return s.withPullChecks(ctx, r.ID, d)
}

// withPullChecks adds a pull request's head checks and what its merge
// deploys (D-77).
func (s *Service) withPullChecks(ctx context.Context, repoID string, d *PullRequestDetail) (*PullRequestDetail, error) {
	all, err := s.checksOf(ctx, repoID, []string{d.HeadSHA})
	if err != nil {
		return nil, err
	}
	d.HeadChecks = all[d.HeadSHA]
	d.Checks = d.HeadChecks.Summary
	files := []gitstore.FileChange{}
	if d.Compare != nil && d.Compare.Files != nil {
		files = d.Compare.Files
	}
	if d.Deploys, err = s.deployTargets(ctx, repoID, d.BaseBranch, files); err != nil {
		return nil, err
	}
	b, err := s.repo.AgentBranch(ctx, repoID, d.HeadBranch)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if b != nil {
		d.AgentBranch = &PullAgentBranch{Agent: b.AgentName, Person: b.UserName, PasswordExpiresAt: b.ExpiresAt}
	}
	if d.HeadKept, err = s.headKept(ctx, repoID, d.HeadBranch); err != nil {
		return nil, err
	}
	return d, nil
}

// headKept says whether a merge must leave branch in place: a service
// deploys from it, or it is the repository's default branch.
func (s *Service) headKept(ctx context.Context, repoID, branch string) (bool, error) {
	deployed, err := s.repo.BranchDeployed(ctx, repoID, branch)
	if err != nil {
		return false, fmt.Errorf("services deploying from the head: %w", err)
	}
	if deployed {
		return true, nil
	}
	var sum gitstore.Summary
	if err := s.git.Browse(ctx, repoID, "summary", nil, &sum); err != nil {
		return false, fmt.Errorf("the repository's default branch: %w", err)
	}
	return sum.DefaultBranch == branch, nil
}

// PullDiff pages a pull request's changes, file by file.
func (s *Service) PullDiff(ctx context.Context, projectID, repoID string, number int, q url.Values) (json.RawMessage, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, err
	}
	p, err := s.repo.GetPull(ctx, r.ID, number)
	if err != nil {
		return nil, err
	}
	qq := url.Values{"limit": q["limit"], "cursor": q["cursor"], "head": {p.HeadSHA}}
	if p.State == "merged" {
		qq.Set("base", p.MergeBaseSHA)
		qq.Set("merge_base", p.MergeBaseSHA)
	} else {
		qq.Set("base", p.Base)
		if s.branchExists(ctx, r.ID, p.Head) {
			qq.Set("head", p.Head)
		}
	}
	var out json.RawMessage
	if err := s.git.Browse(ctx, r.ID, "compare-diff", qq, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// BranchExists says whether repository repoID has the branch.
func (s *Service) BranchExists(ctx context.Context, repoID, branch string) bool {
	return s.git != nil && s.branchExists(ctx, repoID, branch)
}

// CommitContains says whether descendant's history holds ancestor (a
// commit holds itself), in repository repoID (D-81).
func (s *Service) CommitContains(ctx context.Context, repoID, ancestor, descendant string) (bool, error) {
	if s.git == nil {
		return false, ErrGitOff
	}
	if ancestor == descendant {
		return true, nil
	}
	var info struct {
		Forced bool `json:"forced"`
	}
	if err := s.git.Browse(ctx, repoID, "range", url.Values{"from": {ancestor}, "to": {descendant}}, &info); err != nil {
		return false, err
	}
	return !info.Forced, nil
}

func (s *Service) branchExists(ctx context.Context, repoID, branch string) bool {
	var tree json.RawMessage
	return s.git.Browse(ctx, repoID, "commits", url.Values{"ref": {branch}, "limit": {"1"}}, &tree) == nil
}

// Compare is what a pull request from head into base would bring, before
// one is opened.
func (s *Service) Compare(ctx context.Context, projectID, repoID, base, head string) (*gitstore.Compare, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, err
	}
	var cmp gitstore.Compare
	if err := s.git.Browse(ctx, r.ID, "compare", url.Values{"base": {base}, "head": {head}}, &cmp); err != nil {
		return nil, err
	}
	return &cmp, nil
}

// CompareDiff pages the patches of Compare.
func (s *Service) CompareDiff(ctx context.Context, projectID, repoID string, q url.Values) (json.RawMessage, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, err
	}
	var out json.RawMessage
	if err := s.git.Browse(ctx, r.ID, "compare-diff", q, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func checkTitle(t string) (string, error) {
	t = strings.TrimSpace(t)
	if t == "" || utf8.RuneCountInString(t) > 256 || strings.ContainsAny(t, "\n\r\x00") {
		return "", ErrBadTitle
	}
	return t, nil
}

// OpenPull opens a pull request from in.Head into in.Base, both branches of
// the repository.
func (s *Service) OpenPull(ctx context.Context, projectID, repoID, authorID string, in OpenPullInput) (*PullRequest, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, err
	}
	if r.Mirrored {
		return nil, ErrMirrorReadOnly
	}
	title, err := checkTitle(in.Title)
	if err != nil {
		return nil, err
	}
	base, head := strings.TrimSpace(in.Base), strings.TrimSpace(in.Head)
	if base == head {
		return nil, ErrSameBranch
	}
	var cmp gitstore.Compare
	if err := s.git.Browse(ctx, r.ID, "compare", url.Values{"base": {base}, "head": {head}}, &cmp); err != nil {
		return nil, err
	}
	if cmp.Base.Kind != "branch" || cmp.Head.Kind != "branch" {
		return nil, ErrNotBranches
	}
	if cmp.Ahead == 0 {
		return nil, ErrNoChanges
	}
	if err := s.checkAgent(ctx, actor.Target{Action: "code", Tool: "open_pr", ProjectID: projectID}); err != nil {
		return nil, err
	}
	agent, _ := actor.AgentFrom(ctx)
	p, err := s.repo.OpenPull(ctx, NewPull{RepoID: r.ID, AuthorID: authorID, Title: title, Body: strings.TrimRight(in.Body, " \n\t"), Base: base, Head: head, HeadSHA: cmp.Head.SHA,
		WithKey: in.ViaKey, APIKeyID: in.APIKeyID, AgentID: agent.ConnectionID})
	if errors.Is(err, ErrPullExists) {
		if open, lerr := s.repo.OpenPullsByHead(ctx, r.ID, []string{head}); lerr == nil {
			for _, o := range open {
				if o.Head == head {
					return nil, &PullExistsError{Number: o.Number}
				}
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if err := s.git.Write(ctx, r.ID, "pull-ref", map[string]any{"number": p.Number, "sha": cmp.Head.SHA}, nil); err != nil {
		s.log.Warn("refs/pull head", zap.Error(err), zap.String("repository_id", r.ID))
	}
	pushed := map[string]any{
		"before": cmp.MergeBase, "after": cmp.Head.SHA, "count": cmp.Ahead, "forced": false, "commits": commitList(cmp.Commits), "with_key": in.ViaKey,
	}
	if agent.Name != "" {
		pushed["agent"] = agent.Name
	}
	s.event(ctx, p.ID, authorID, "pushed", pushed)
	s.recordPull(ctx, r.ID, authorID, p, "opened", cmp.Head.SHA, in.ViaKey)
	s.pullsChanged(r, projectID)
	out := toPullRequest(p)
	return &out, nil
}

// commitList is the newest 20 of commits (oldest first), as an event keeps
// them.
func commitList(cs []gitstore.Commit) []map[string]string {
	if len(cs) > 20 {
		cs = cs[len(cs)-20:]
	}
	out := make([]map[string]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, map[string]string{"sha": c.SHA, "subject": c.Subject})
	}
	return out
}

// event writes an event in a pull request's conversation. One an agent
// caused names it (data.agent, D-100), unless the caller already did.
func (s *Service) event(ctx context.Context, pullID, actorID, kind string, data map[string]any) {
	if a, ok := actor.AgentFrom(ctx); ok && data["agent"] == nil {
		if data == nil {
			data = map[string]any{}
		}
		data["agent"] = a.Name
	}
	if err := s.repo.AddEvent(ctx, pullID, actorID, kind, data); err != nil {
		s.log.Error("pull request event", zap.Error(err), zap.String("kind", kind))
	}
}

// UpdatePull edits a pull request's title and description, or closes or
// reopens it.
func (s *Service) UpdatePull(ctx context.Context, projectID, repoID string, number int, actorID string, in UpdatePullInput) (*PullRequest, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, err
	}
	p, err := s.repo.GetPull(ctx, r.ID, number)
	if err != nil {
		return nil, err
	}
	if in.Title != nil || in.Body != nil {
		title, body := p.Title, p.Body
		if in.Title != nil {
			if title, err = checkTitle(*in.Title); err != nil {
				return nil, err
			}
		}
		if in.Body != nil {
			body = strings.TrimRight(*in.Body, " \n\t")
		}
		if err := s.repo.UpdatePullText(ctx, p.ID, title, body); err != nil {
			return nil, err
		}
		if title != p.Title {
			s.event(ctx, p.ID, actorID, "title_changed", map[string]any{"from": p.Title, "to": title})
		}
	}
	if in.State != nil && *in.State != p.State {
		if p.State == "merged" {
			return nil, ErrPullClosed
		}
		if *in.State == "open" && !s.branchExists(ctx, r.ID, p.Head) {
			return nil, ErrHeadGone
		}
		ok, err := s.repo.SetPullState(ctx, p.ID, p.State, *in.State)
		if err != nil {
			return nil, err
		}
		if ok {
			kind := map[string]string{"closed": "closed", "open": "reopened"}[*in.State]
			s.event(ctx, p.ID, actorID, kind, nil)
			s.recordPull(ctx, r.ID, actorID, p, kind, p.HeadSHA, false)
			s.pullsChanged(r, projectID)
		}
	}
	if p, err = s.repo.GetPull(ctx, r.ID, number); err != nil {
		return nil, err
	}
	out := toPullRequest(p)
	return &out, nil
}

// MergePull merges a pull request by method (D-75): the head must be the
// commit the person merging saw. A squash is authored by the pull
// request's author and committed by the person merging.
func (s *Service) MergePull(ctx context.Context, projectID, repoID string, number int, actorID string, in MergeInput) (*PullRequest, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, err
	}
	p, err := s.repo.GetPull(ctx, r.ID, number)
	if err != nil {
		return nil, err
	}
	if p.State != "open" {
		return nil, ErrPullClosed
	}
	if r.Mirrored {
		return nil, ErrMirrorReadOnly
	}
	if in.DeleteBranch {
		kept, err := s.headKept(ctx, r.ID, p.Head)
		if err != nil {
			return nil, err
		}
		if kept {
			return nil, ErrHeadKept
		}
	}
	committer, err := s.members.GetByID(ctx, actorID)
	if err != nil {
		return nil, fmt.Errorf("the merging account: %w", err)
	}
	me := gitstore.Identity{Name: committer.Name, Email: committer.Email}
	author := me
	if in.Method == "squash" && p.AuthorID != "" && p.AuthorID != actorID {
		if a, err := s.members.GetByID(ctx, p.AuthorID); err == nil {
			author = gitstore.Identity{Name: a.Name, Email: a.Email}
		}
	}
	// What would be merged, and that it is what the person merging saw.
	var cmp gitstore.Compare
	err = s.git.Browse(ctx, r.ID, "compare", url.Values{"base": {p.Base}, "head": {p.Head}}, &cmp)
	if browseCode(err) == "ref_not_found" {
		return nil, ErrHeadGone
	}
	if err != nil {
		return nil, err
	}
	if cmp.Head.SHA != in.ExpectedHeadSHA {
		return nil, ErrHeadMoved
	}
	msg := ""
	switch in.Method {
	case "merge":
		msg = fmt.Sprintf("Merge pull request !%d from %s\n\n%s\n", p.Number, p.Head, p.Title)
	case "squash":
		var b strings.Builder
		fmt.Fprintf(&b, "%s (!%d)\n", p.Title, p.Number)
		if len(cmp.Commits) > 1 {
			b.WriteString("\n")
			for _, c := range cmp.Commits {
				fmt.Fprintf(&b, "* %s\n", c.Subject)
			}
		}
		msg = b.String()
	}
	var res gitstore.MergeResult
	err = s.git.Write(ctx, r.ID, "merge", gitstore.MergeRequest{
		Method: in.Method, Base: p.Base, Head: p.Head, ExpectedHead: in.ExpectedHeadSHA, PullNumber: p.Number,
		DeleteBranch: in.DeleteBranch, Message: msg, Author: author, Committer: me,
	}, &res)
	if err != nil {
		return nil, err
	}
	ok, err := s.repo.MarkMerged(ctx, p.ID, Merged{Method: in.Method, SHA: res.SHA, MergeBase: res.MergeBase, Head: res.Head, By: actorID})
	if err != nil || !ok {
		// git merged it; the row says open until this is fixed by hand.
		s.log.Error("merged in git but not recorded", zap.Error(err), zap.String("repository_id", r.ID), zap.Int("number", p.Number), zap.String("sha", res.SHA))
		if err == nil {
			err = ErrPullClosed
		}
		return nil, err
	}
	s.event(ctx, p.ID, actorID, "merged", map[string]any{"method": in.Method, "sha": res.SHA, "base": p.Base, "deleted_branch": in.DeleteBranch})
	// The merge's update-ref ran no hook: it records its own push (D-77).
	subject, commits := firstLine(msg), 1
	switch in.Method {
	case "merge":
		commits = cmp.Ahead + 1
	case "fast_forward":
		commits = cmp.Ahead
		if n := len(cmp.Commits); n > 0 {
			subject = cmp.Commits[n-1].Subject
		}
	}
	s.recordMerge(ctx, r.ID, actorID, p, in.Method, res.Before, res.SHA, subject, commits)
	s.pullsChanged(r, projectID)
	if in.DeleteBranch {
		// The same transaction deleted the head: a push webhook says so.
		s.recordMoves(ctx, r.ID, actorID, "push", false, []gitstore.RefUpdate{{Ref: "refs/heads/" + p.Head, Old: res.Head, New: strings.Repeat("0", 40)}}, "")
	}
	if p, err = s.repo.GetPull(ctx, r.ID, number); err != nil {
		return nil, err
	}
	out := toPullRequest(p)
	return &out, nil
}

// ─── the conversation ────────────────────────────────────────────────────────

func toItem(it StoredItem) TimelineItem {
	t := TimelineItem{Type: it.Item, ID: it.ID, Actor: withAgent(person(it.ActorID, it.ActorName), it.AgentName), CreatedAt: it.CreatedAt}
	if it.Item == "comment" {
		t.Body, t.HTML = it.Body, gitstore.Markdown([]byte(it.Body), "")
		if it.UpdatedAt.Sub(it.CreatedAt) > time.Second {
			at := it.UpdatedAt
			t.EditedAt = &at
		}
		return t
	}
	t.Kind = it.Kind
	if len(it.Data) > 0 {
		_ = json.Unmarshal(it.Data, &t.Data)
	}
	return t
}

// Timeline pages a pull request's conversation after its description,
// oldest first.
func (s *Service) Timeline(ctx context.Context, projectID, repoID string, number int, cursor string, limit int) ([]TimelineItem, string, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, "", err
	}
	p, err := s.repo.GetPull(ctx, r.ID, number)
	if err != nil {
		return nil, "", err
	}
	var afterAt time.Time
	var afterID string
	if cursor != "" {
		if afterAt, afterID, err = respond.ParseCursor(cursor); err != nil {
			return nil, "", ErrBadCursor
		}
	}
	limit = min(max(limit, 1), 200)
	items, err := s.repo.Timeline(ctx, p.ID, afterAt, afterID, limit+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		last := items[limit-1]
		next = respond.Cursor(last.CreatedAt, last.ID)
	}
	refs := gitstore.Refs{Keys: s.issueKeys(ctx, projectID), Pulls: true, Mentions: s.handles(ctx, projectID)}
	out := make([]TimelineItem, 0, len(items))
	for _, it := range items {
		t := toItem(it)
		if t.Type == "comment" {
			t.HTML = gitstore.MarkdownRefs([]byte(t.Body), refs)
		}
		out = append(out, t)
	}
	return out, next, nil
}

func (s *Service) pullOf(ctx context.Context, projectID, repoID string, number int) (*StoredPull, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, err
	}
	return s.repo.GetPull(ctx, r.ID, number)
}

func checkBody(b string) (string, error) {
	b = strings.TrimRight(b, " \n\t")
	if strings.TrimSpace(b) == "" {
		return "", ErrEmptyComment
	}
	return b, nil
}

// AddComment adds a comment to a pull request's conversation.
func (s *Service) AddComment(ctx context.Context, projectID, repoID string, number int, authorID, body string) (*TimelineItem, error) {
	p, err := s.pullOf(ctx, projectID, repoID, number)
	if err != nil {
		return nil, err
	}
	if body, err = checkBody(body); err != nil {
		return nil, err
	}
	if err := s.checkAgent(ctx, actor.Target{Action: "issues", Tool: "comment", ProjectID: projectID}); err != nil {
		return nil, err
	}
	c, err := s.repo.AddComment(ctx, p.ID, authorID, body)
	if err != nil {
		return nil, err
	}
	agent, _ := actor.AgentFrom(ctx)
	it := toItem(StoredItem{Item: "comment", ID: c.ID, ActorID: c.AuthorID, ActorName: c.AuthorName, AgentName: agent.Name, Body: c.Body, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt})
	return &it, nil
}

// EditComment changes a comment's text: only its author may.
func (s *Service) EditComment(ctx context.Context, projectID, repoID string, number int, commentID, actorID, body string) (*TimelineItem, error) {
	p, err := s.pullOf(ctx, projectID, repoID, number)
	if err != nil {
		return nil, err
	}
	c, err := s.repo.GetComment(ctx, p.ID, commentID)
	if err != nil {
		return nil, err
	}
	if c.AuthorID != actorID {
		return nil, ErrNotYours
	}
	if body, err = checkBody(body); err != nil {
		return nil, err
	}
	if err := s.repo.UpdateComment(ctx, c.ID, body); err != nil {
		return nil, err
	}
	if c, err = s.repo.GetComment(ctx, p.ID, commentID); err != nil {
		return nil, err
	}
	it := toItem(StoredItem{Item: "comment", ID: c.ID, ActorID: c.AuthorID, ActorName: c.AuthorName, Body: c.Body, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt})
	return &it, nil
}

// DeleteComment removes a comment: its author, or an owner of the
// workspace.
func (s *Service) DeleteComment(ctx context.Context, projectID, repoID string, number int, commentID, actorID string, owner bool) error {
	p, err := s.pullOf(ctx, projectID, repoID, number)
	if err != nil {
		return err
	}
	c, err := s.repo.GetComment(ctx, p.ID, commentID)
	if err != nil {
		return err
	}
	if c.AuthorID != actorID && !owner {
		return ErrNotYours
	}
	return s.repo.DeleteComment(ctx, c.ID)
}

// ─── after a push ────────────────────────────────────────────────────────────

// afterPush moves the open pull requests whose branches a push moved: a
// pushed event with the new commits and refs/pull/<n>/head, or
// head_deleted when the branch went. Errors are logged: the push itself
// already succeeded.
func (s *Service) afterPush(ctx context.Context, t *Target, actor Actor, updates []gitstore.RefUpdate) {
	actorID := actor.UserID
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	byBranch := map[string]gitstore.RefUpdate{}
	branches := make([]string, 0, len(updates))
	for _, u := range updates {
		if b, ok := strings.CutPrefix(u.Ref, "refs/heads/"); ok {
			byBranch[b] = u
			branches = append(branches, b)
		}
	}
	if len(branches) == 0 {
		return
	}
	pulls, err := s.repo.OpenPullsByHead(ctx, t.ID, branches)
	if err != nil {
		s.log.Error("after push: pull requests", zap.Error(err), zap.String("repository_id", t.ID))
		return
	}
	for _, p := range pulls {
		u := byBranch[p.Head]
		if strings.Trim(u.New, "0") == "" {
			s.event(ctx, p.ID, actorID, "head_deleted", map[string]any{"branch": p.Head, "sha": p.HeadSHA})
			continue
		}
		if u.New != p.HeadSHA {
			s.recordPush(ctx, t.ID, p, actorID, actor.ViaKey, u.Old, u.New, actor.AgentName)
		}
	}
}

// recordPush notes that p's head moved from before to after, pushed with
// an API key or by someone unknown when withKey (D-97).
func (s *Service) recordPush(ctx context.Context, repoID string, p StoredPull, actorID string, withKey bool, before, after, agentName string) {
	var info struct {
		Count   int               `json:"count"`
		Forced  bool              `json:"forced"`
		Commits []gitstore.Commit `json:"commits"`
	}
	if err := s.git.Browse(ctx, repoID, "range", url.Values{"from": {before}, "to": {after}}, &info); err != nil {
		s.log.Warn("pushed range", zap.Error(err), zap.String("repository_id", repoID))
	}
	data := map[string]any{
		"before": before, "after": after, "count": info.Count, "forced": info.Forced, "commits": commitList(info.Commits), "with_key": withKey,
	}
	if agentName != "" {
		data["agent"] = agentName
	}
	s.event(ctx, p.ID, actorID, "pushed", data)
	if err := s.repo.SetPullHead(ctx, p.ID, after); err != nil {
		s.log.Error("pull request head", zap.Error(err))
	}
	s.recordPull(ctx, repoID, actorID, &p, "pushed", after, withKey)
	if err := s.git.Write(ctx, repoID, "pull-ref", map[string]any{"number": p.Number, "sha": after}, nil); err != nil {
		s.log.Warn("refs/pull head", zap.Error(err), zap.String("repository_id", repoID))
	}
}

// firstLine is a message's subject.
func firstLine(msg string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(msg), "\n")
	return strings.TrimSpace(line)
}

// AddPullEvent writes an event in a pull request's conversation for
// another module: a preview's started, waiting, approved, updated, stopped
// and removed (D-97).
func (s *Service) AddPullEvent(ctx context.Context, pullID, actorID, kind string, data map[string]any) {
	s.event(ctx, pullID, actorID, kind, data)
}

// maxPreviewFile caps what ReadFile returns.
const maxPreviewFile = 1 << 20

// ReadFile is a file of the project's repository at a commit, at most
// 1 MiB (a preview reads an untrusted Dockerfile before it builds; D-97).
func (s *Service) ReadFile(ctx context.Context, projectID, repoID, sha, path string) ([]byte, error) {
	rc, _, err := s.Raw(ctx, projectID, repoID, url.Values{"ref": {sha}, "path": {path}})
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, maxPreviewFile))
}
