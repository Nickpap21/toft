package doc

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/internal/code"
	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"github.com/Nickpap21/toft/backend/internal/inbox"
	"github.com/Nickpap21/toft/backend/internal/issue"
	"github.com/Nickpap21/toft/backend/pkg/actor"
	"github.com/Nickpap21/toft/backend/pkg/broker"
	"go.uber.org/zap"
)

// Keys are a project's issue keys, its current one and its former ones.
// Satisfied by *issue.Service.
type Keys interface {
	IssueKeys(ctx context.Context, projectID string) []string
}

// Changes says whether a pull request changes files under a directory.
// Satisfied by *code.Service.
type Changes interface {
	ChangesUnder(ctx context.Context, repoID, base, head, dir string) (bool, error)
}

// Exec runs a command in a service's container, or on its server's host.
// Satisfied by *service.ServiceManager.
type Exec interface {
	AuthorizeHostExec(ctx context.Context, actorID string) error
	ExecInService(ctx context.Context, actorID, id, containerName, command string, logFn func(string)) error
}

// hostContainer is service.HostContainer: a command on the server.
const hostContainer = "@server"

// Publisher tells a workspace's open tabs. Satisfied by *broker.Broker.
type Publisher interface {
	Publish(orgID string, e broker.Event)
}

// Service is the docs module (D-86).
type Service struct {
	repo    Repository
	keys    Keys
	changes Changes
	exec    Exec
	log     *zap.Logger
	agents  AgentGate
	pub     Publisher
}

func NewService(repo Repository, keys Keys, changes Changes, exec Exec, log *zap.Logger) *Service {
	return &Service{repo: repo, keys: keys, changes: changes, exec: exec, log: log}
}

// AgentGate is the workspace's agent policy (D-100, D-102). Satisfied by
// *agent.Service.
type AgentGate interface {
	Check(ctx context.Context, t actor.Target) error
}

// WithAgentGate lets agents run a page's container steps, under the policy.
func (s *Service) WithAgentGate(g AgentGate) { s.agents = g }

// WithPublisher tells the workspace's tabs when a page is created, saved,
// moved, deleted, restored or verified, so the counts that read them refresh.
func (s *Service) WithPublisher(p Publisher) { s.pub = p }

// changed tells the workspace's tabs that a project's pages changed. Call
// it once a change has committed.
func (s *Service) changed(orgID, projectID string) {
	if s.pub != nil {
		s.pub.Publish(orgID, broker.Changed(projectID, "docs"))
	}
}

// AgentStepTime is how long an agent's step may run (D-102).
const AgentStepTime = 10 * time.Minute

// CheckAgent is the agent policy for running st (D-102, Q-12): a step on a
// server's host is never an agent's; a container step is run_runbook_step
// on its service. nil for people.
func (s *Service) CheckAgent(ctx context.Context, st *Step) error {
	if _, ok := actor.AgentFrom(ctx); !ok {
		return nil
	}
	if st.Host {
		return ErrAgentHostStep
	}
	if s.agents == nil {
		return ErrAgentHostStep
	}
	return s.agents.Check(ctx, actor.Target{Action: "runbooks", Tool: "run_runbook_step", ServiceID: st.ServiceID})
}

// ─── pages ───────────────────────────────────────────────────────────────────

// Tree is the project's pages, each out of date or not; deleted, those
// deleted in the last 30 days.
func (s *Service) Tree(ctx context.Context, projectID string, deleted bool) ([]TreePage, error) {
	pages, err := s.repo.Tree(ctx, projectID, deleted)
	if err != nil {
		return nil, err
	}
	if deleted {
		return pages, nil
	}
	states, err := s.repo.ServiceStates(ctx, nil, projectID)
	if err != nil {
		return nil, err
	}
	stale := staleness(states)
	for i := range pages {
		pages[i].Stale = stale[pages[i].ID]
	}
	return pages, nil
}

// staleness is, by page, whether a service it names changed since.
func staleness(states []serviceNow) map[string]bool {
	out := map[string]bool{}
	for _, st := range states {
		if len(changes(was(st), st.seen())) > 0 {
			out[st.PageID] = true
		}
	}
	return out
}

func was(st serviceNow) seen {
	var w seen
	_ = json.Unmarshal(st.Seen, &w)
	if w.Vars == nil {
		w.Vars = map[string]varSeen{}
	}
	return w
}

// Create writes a page.
func (s *Service) Create(ctx context.Context, orgID, projectID, actorID string, in CreateInput) (*Page, error) {
	title, err := checkTitle(in.Title)
	if err != nil {
		return nil, err
	}
	if err := checkBody(in.Body); err != nil {
		return nil, err
	}
	if in.ParentID != "" {
		if err := s.parentOK(ctx, projectID, "", in.ParentID); err != nil {
			return nil, err
		}
	}
	n, err := s.repo.CountPages(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if n >= maxPages {
		return nil, ErrTooMany
	}
	l, err := s.links(ctx, orgID, projectID, in.Body)
	if err != nil {
		return nil, err
	}
	pageID, err := s.repo.Create(ctx, newPage{ProjectID: projectID, ParentID: in.ParentID, Title: title, Body: in.Body, ActorID: actorID, Links: l})
	if err != nil {
		return nil, err
	}
	s.changed(orgID, projectID)
	return s.Get(ctx, orgID, projectID, pageID)
}

// parentOK is ErrBadParent unless parentID is a live page of the project
// that isn't pageID or one of its pages.
func (s *Service) parentOK(ctx context.Context, projectID, pageID, parentID string) error {
	if parentID == pageID {
		return ErrBadParent
	}
	p, err := s.repo.Page(ctx, projectID, parentID)
	if errors.Is(err, ErrNotFound) || err == nil && p.DeletedAt != nil {
		return ErrBadParent
	}
	if err != nil {
		return err
	}
	if pageID == "" {
		return nil
	}
	up, err := s.repo.Path(ctx, parentID)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(up, func(r code.DocRef) bool { return r.ID == pageID }) {
		return ErrBadParent
	}
	return nil
}

// Update saves a page's title and text as the next version, and moves it.
func (s *Service) Update(ctx context.Context, orgID, projectID, pageID, actorID string, in UpdateInput) (*Page, error) {
	p, err := s.repo.Page(ctx, projectID, pageID)
	if err != nil {
		return nil, err
	}
	if p.DeletedAt != nil {
		return nil, ErrDeleted
	}
	wrote := false // the save, the move, or both: one telling
	defer func() {
		if wrote {
			s.changed(orgID, projectID)
		}
	}()
	if in.Title != nil || in.Body != nil {
		if in.BaseVersion <= 0 {
			return nil, ErrBadVersion
		}
		title, body := p.Title, p.Body
		if in.Title != nil {
			if title, err = checkTitle(*in.Title); err != nil {
				return nil, err
			}
		}
		if in.Body != nil {
			if err := checkBody(*in.Body); err != nil {
				return nil, err
			}
			body = *in.Body
		}
		if in.BaseVersion != p.Version {
			return nil, ErrConflict
		}
		l, err := s.links(ctx, orgID, projectID, body)
		if err != nil {
			return nil, err
		}
		if _, err := s.repo.Save(ctx, savePage{ID: pageID, Title: title, Body: body, ActorID: actorID, Base: in.BaseVersion, Links: l}); err != nil {
			return nil, err
		}
		wrote = true
	}
	if in.ParentID != nil || in.Position != nil {
		parent := p.ParentID
		if in.ParentID != nil {
			parent = *in.ParentID
			if parent != "" {
				if err := s.parentOK(ctx, projectID, pageID, parent); err != nil {
					return nil, err
				}
			}
		}
		pos := 1 << 30 // last
		if in.Position != nil {
			pos = *in.Position
		}
		if err := s.repo.Move(ctx, projectID, pageID, parent, pos); err != nil {
			return nil, err
		}
		wrote = true
	}
	return s.Get(ctx, orgID, projectID, pageID)
}

// Delete deletes a page for 30 days: ErrHasPages while pages are in it.
func (s *Service) Delete(ctx context.Context, orgID, projectID, pageID string) error {
	p, err := s.repo.Page(ctx, projectID, pageID)
	if err != nil {
		return err
	}
	if p.DeletedAt != nil {
		return ErrNotFound
	}
	n, err := s.repo.LiveChildren(ctx, pageID)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrHasPages
	}
	if ok, err := s.repo.Delete(ctx, pageID); err != nil || !ok {
		return firstErr(err, ErrNotFound)
	}
	s.changed(orgID, projectID)
	return nil
}

// Restore brings a deleted page back.
func (s *Service) Restore(ctx context.Context, orgID, projectID, pageID string) (*Page, error) {
	if _, err := s.repo.Page(ctx, projectID, pageID); err != nil {
		return nil, err
	}
	if ok, err := s.repo.Restore(ctx, pageID); err != nil || !ok {
		return nil, firstErr(err, ErrNotFound)
	}
	s.changed(orgID, projectID)
	return s.Get(ctx, orgID, projectID, pageID)
}

// Verify marks the page verified against its services as they are now.
func (s *Service) Verify(ctx context.Context, orgID, projectID, pageID, actorID string) (*Page, error) {
	p, err := s.repo.Page(ctx, projectID, pageID)
	if err != nil {
		return nil, err
	}
	if p.DeletedAt != nil {
		return nil, ErrDeleted
	}
	if err := s.repo.Verify(ctx, pageID, actorID); err != nil {
		return nil, err
	}
	s.changed(orgID, projectID)
	return s.Get(ctx, orgID, projectID, pageID)
}

func firstErr(err, otherwise error) error {
	if err != nil {
		return err
	}
	return otherwise
}

// Versions pages a page's versions, newest first; the cursor is the last
// version listed.
func (s *Service) Versions(ctx context.Context, projectID, pageID, cursor string, limit int) ([]Version, string, error) {
	if _, err := s.repo.Page(ctx, projectID, pageID); err != nil {
		return nil, "", err
	}
	before := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n <= 1 {
			return nil, "", ErrBadCursor
		}
		before = n
	}
	limit = min(max(limit, 1), 100)
	vs, err := s.repo.Versions(ctx, pageID, before, limit+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(vs) > limit {
		vs = vs[:limit]
		next = strconv.Itoa(vs[limit-1].Version)
	}
	return vs, next, nil
}

// Version is one version, rendered.
func (s *Service) Version(ctx context.Context, projectID, pageID string, version int) (*VersionDetail, error) {
	if _, err := s.repo.Page(ctx, projectID, pageID); err != nil {
		return nil, err
	}
	v, err := s.repo.Version(ctx, pageID, version)
	if err != nil {
		return nil, err
	}
	v.HTML = s.render(ctx, projectID, v.Body).HTML
	return v, nil
}

// ─── reading a page ──────────────────────────────────────────────────────────

func (s *Service) render(ctx context.Context, projectID, body string) gitstore.Doc {
	repos, err := s.repo.RepositoryNames(ctx, projectID)
	if err != nil {
		s.log.Warn("repository names", zap.Error(err))
	}
	var keys []string
	if s.keys != nil {
		keys = s.keys.IssueKeys(ctx, projectID)
	}
	return gitstore.MarkdownDoc([]byte(body), gitstore.Refs{Keys: keys, Pulls: len(repos) == 1})
}

// Get is a page with its blocks' live state.
func (s *Service) Get(ctx context.Context, orgID, projectID, pageID string) (*Page, error) {
	st, err := s.repo.Page(ctx, projectID, pageID)
	if err != nil {
		return nil, err
	}
	d := s.render(ctx, projectID, st.Body)
	p := &Page{ID: st.ID, ProjectID: st.ProjectID, Title: st.Title, Body: st.Body, HTML: d.HTML, Version: st.Version, Position: st.Position,
		CreatedBy: person(st.CreatedByID, st.CreatedBy), UpdatedBy: person(st.UpdatedByID, st.UpdatedBy),
		CreatedAt: st.CreatedAt, UpdatedAt: st.UpdatedAt, VerifiedAt: st.VerifiedAt, VerifiedBy: person(st.VerifiedByID, st.Verifier),
		DeletedAt: st.DeletedAt, Blocks: []Block{}, Services: []ServiceState{}}
	if st.ParentID != "" {
		p.ParentID = &st.ParentID
	}
	if p.Path, err = s.repo.Path(ctx, pageID); err != nil {
		return nil, err
	}
	if p.ReferencedBy, err = s.repo.Referencing(ctx, "", "", pageID); err != nil {
		return nil, err
	}
	var issueRefs, pullRefs []string
	for _, f := range d.Fences {
		b := parseBlock(f.Info, f.Body)
		switch b.Kind {
		case "issue":
			issueRefs = append(issueRefs, b.ref)
		case "pull":
			pullRefs = append(pullRefs, b.ref)
		}
		p.Blocks = append(p.Blocks, b)
	}
	issueIDs, err := s.repo.ResolveIssues(ctx, orgID, issueRefs)
	if err != nil {
		return nil, err
	}
	pullIDs, err := s.repo.ResolvePulls(ctx, projectID, pullRefs)
	if err != nil {
		return nil, err
	}
	issues, err := s.repo.Issues(ctx, values(issueIDs))
	if err != nil {
		return nil, err
	}
	pulls, err := s.repo.Pulls(ctx, values(pullIDs))
	if err != nil {
		return nil, err
	}
	for i, b := range p.Blocks {
		switch b.Kind {
		case "issue":
			if is, ok := issues[issueIDs[b.ref]]; ok {
				p.Blocks[i].Issue = &is
			}
		case "pull":
			if pr, ok := pulls[pullIDs[b.ref]]; ok {
				p.Blocks[i].Pull = &pr
			}
		}
	}
	states, err := s.repo.ServiceStates(ctx, []string{pageID}, "")
	if err != nil {
		return nil, err
	}
	for _, sn := range states {
		ss := serviceState(sn)
		p.Stale = p.Stale || len(ss.Changes) > 0
		p.Services = append(p.Services, ss)
	}
	return p, nil
}

func values(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// serviceState is a service as a page shows it, with what changed since
// the page last saw it.
func serviceState(sn serviceNow) ServiceState {
	ss := ServiceState{ID: sn.ServiceID, Name: sn.Name, Status: sn.Status, Environment: opt(sn.Environment), Server: opt(sn.ServerName),
		Commit: opt(sn.LiveCommit), DeployedAt: sn.LiveAt, Changes: changes(was(sn), sn.seen())}
	if sn.SourceType == "docker-image" {
		ss.Image = sn.DockerImage
	}
	for k, v := range sn.Vars {
		if v.Secret && (ss.SecretSet == nil || v.At.After(ss.SecretSet.At)) {
			ss.SecretSet = &SecretSet{Key: k, At: v.At}
		}
	}
	if len(ss.Changes) > 0 && sn.SinceID != "" && sn.SinceAt != nil {
		ss.Deployment = &Deploy{ID: sn.SinceID, Commit: opt(sn.SinceCommit), FinishedAt: *sn.SinceAt}
	}
	return ss
}

// links is what a body names: the project's services its blocks name, and
// the issues, pull requests and pages of the project its blocks and text
// name (issues of the whole workspace in blocks).
func (s *Service) links(ctx context.Context, orgID, projectID, body string) (links, error) {
	d := s.render(ctx, projectID, body)
	l := links{Services: []string{}, Issues: []string{}, Pulls: []string{}, Pages: []string{}}
	var svcs, issueRefs, pullRefs []string
	for _, f := range d.Fences {
		b := parseBlock(f.Info, f.Body)
		switch b.Kind {
		case "service", "run":
			svcs = append(svcs, b.ServiceID)
		case "issue":
			issueRefs = append(issueRefs, b.ref)
		case "pull":
			pullRefs = append(pullRefs, b.ref)
		}
	}
	if len(d.Issues) > 0 && s.keys != nil {
		if keys := s.keys.IssueKeys(ctx, projectID); len(keys) > 0 {
			for _, n := range d.Issues {
				issueRefs = append(issueRefs, keys[0]+"-"+strconv.Itoa(n))
			}
		}
	}
	if len(d.Pulls) > 0 {
		repos, err := s.repo.RepositoryNames(ctx, projectID)
		if err != nil {
			return l, err
		}
		if len(repos) == 1 {
			for _, n := range d.Pulls {
				pullRefs = append(pullRefs, repos[0]+"!"+strconv.Itoa(n))
			}
		}
	}
	var err error
	if len(svcs) > 0 {
		if l.Services, err = s.repo.ProjectServices(ctx, projectID, uniq(svcs)); err != nil {
			return l, err
		}
	}
	issueIDs, err := s.repo.ResolveIssues(ctx, orgID, uniq(issueRefs))
	if err != nil {
		return l, err
	}
	pullIDs, err := s.repo.ResolvePulls(ctx, projectID, uniq(pullRefs))
	if err != nil {
		return l, err
	}
	l.Issues, l.Pulls = uniq(values(issueIDs)), uniq(values(pullIDs))
	if len(d.Pages) > 0 {
		if l.Pages, err = s.repo.ExistingPages(ctx, projectID, uniq(d.Pages)); err != nil {
			return l, err
		}
	}
	return l, nil
}

func uniq(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// ─── runs ────────────────────────────────────────────────────────────────────

// Step is the saved page's command number step: ErrConflict unless version
// is the current one.
func (s *Service) Step(ctx context.Context, projectID, pageID string, step, version int) (*Step, error) {
	p, err := s.repo.Page(ctx, projectID, pageID)
	if err != nil {
		return nil, err
	}
	if p.DeletedAt != nil {
		return nil, ErrNotFound
	}
	if version != p.Version {
		return nil, ErrConflict
	}
	i := 0
	for _, f := range gitstore.MarkdownDoc([]byte(p.Body), gitstore.Refs{}).Fences {
		b := parseBlock(f.Info, f.Body)
		if b.Kind != "run" {
			continue
		}
		if i == step {
			own, err := s.repo.ProjectServices(ctx, projectID, []string{b.ServiceID})
			if err != nil {
				return nil, err
			}
			if len(own) == 0 {
				return nil, ErrNoStep
			}
			return &Step{ServiceID: b.ServiceID, Host: b.Host, Command: b.Command}, nil
		}
		i++
	}
	return nil, ErrNoStep
}

// AuthorizeHost is ErrHostCommand unless actorID is an instance operator.
func (s *Service) AuthorizeHost(ctx context.Context, actorID string) error {
	if err := s.exec.AuthorizeHostExec(ctx, actorID); err != nil {
		return ErrHostCommand
	}
	return nil
}

// Run runs a step, its output to logFn.
func (s *Service) Run(ctx context.Context, actorID string, st *Step, logFn func(string)) error {
	container := ""
	if st.Host {
		container = hostContainer
	}
	return s.exec.ExecInService(ctx, actorID, st.ServiceID, container, st.Command, logFn)
}

// ─── what links here ─────────────────────────────────────────────────────────

// IssueDocs are the pages that name an issue. Satisfies issue.Docs.
func (s *Service) IssueDocs(ctx context.Context, issueID string) ([]code.DocRef, error) {
	return s.repo.Referencing(ctx, issueID, "", "")
}

// PullDocs are the pages that name a pull request. Satisfies code.Docs.
func (s *Service) PullDocs(ctx context.Context, pullID string) ([]code.DocRef, error) {
	return s.repo.Referencing(ctx, "", pullID, "")
}

// SearchDocs is ⌘K's. Satisfies issue.Docs.
func (s *Service) SearchDocs(ctx context.Context, orgID, projectID, tsq string, limit int) ([]issue.DocHit, error) {
	return s.repo.Search(ctx, orgID, projectID, tsq, limit)
}

// Work is a service's Connected work: ErrNotFound unless it is the
// workspace's.
func (s *Service) Work(ctx context.Context, orgID, serviceID string) (*Work, error) {
	svc, err := s.repo.ServiceForWork(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	if svc.OrgID != orgID {
		return nil, ErrNotFound
	}
	w := &Work{Pulls: []Pull{}, Issues: []Issue{}, Pages: []WorkPage{}}
	if svc.RepositoryID != "" && svc.Branch != "" {
		pulls, err := s.repo.ServicePulls(ctx, svc.RepositoryID, svc.Branch)
		if err != nil {
			return nil, err
		}
		dir := strings.Trim(path.Clean("/"+svc.Dir), "/")
		for _, pr := range pulls {
			if dir != "" && s.changes != nil {
				// ponytail: one compare per open pull request (10 at most).
				touches, err := s.changes.ChangesUnder(ctx, svc.RepositoryID, svc.Branch, pr.HeadSHA, dir)
				if err != nil {
					s.log.Warn("connected work: compare", zap.Error(err), zap.Int("pull", pr.Number))
					continue
				}
				if !touches {
					continue
				}
			}
			w.Pulls = append(w.Pulls, pr.Pull)
		}
	}
	if svc.ProjectID != "" {
		if w.Issues, err = s.repo.ServiceIssues(ctx, svc.ProjectID, svc.Name); err != nil {
			return nil, err
		}
	}
	if w.Pages, err = s.repo.ServicePages(ctx, serviceID); err != nil {
		return nil, err
	}
	if len(w.Pages) > 0 {
		pageIDs := make([]string, 0, len(w.Pages))
		for _, p := range w.Pages {
			pageIDs = append(pageIDs, p.ID)
		}
		states, err := s.repo.ServiceStates(ctx, pageIDs, "")
		if err != nil {
			return nil, err
		}
		stale := staleness(states)
		for i := range w.Pages {
			w.Pages[i].Stale = stale[w.Pages[i].ID]
		}
	}
	return w, nil
}

// RunPurge purges pages deleted 30 days ago, hourly, until ctx ends.
func (s *Service) RunPurge(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if n, err := s.repo.Purge(ctx); err != nil {
			s.log.Warn("purge deleted pages", zap.Error(err))
		} else if n > 0 {
			s.log.Info("purged deleted pages", zap.Int("pages", n))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// StalePagesFor are the out-of-date pages a person wrote or edited, each
// with its first change. Satisfies inbox.Pages.
func (s *Service) StalePagesFor(ctx context.Context, orgID, userID string) ([]inbox.StalePage, error) {
	pages, err := s.repo.PagesBy(ctx, orgID, userID)
	if err != nil || len(pages) == 0 {
		return []inbox.StalePage{}, err
	}
	ids := make([]string, 0, len(pages))
	for _, p := range pages {
		ids = append(ids, p.ID)
	}
	states, err := s.repo.ServiceStates(ctx, ids, "")
	if err != nil {
		return nil, err
	}
	first := map[string]string{}
	for _, st := range states {
		if _, ok := first[st.PageID]; ok {
			continue
		}
		if cs := changes(was(st), st.seen()); len(cs) > 0 {
			first[st.PageID] = changeSentence(st.Name, cs[0])
		}
	}
	out := []inbox.StalePage{}
	for _, p := range pages {
		if c, ok := first[p.ID]; ok {
			p.Change = c
			out = append(out, p)
		}
	}
	return out, nil
}

// changeSentence says what changed, as the banner does.
func changeSentence(svc string, c Change) string {
	val := func(p *string) string {
		if p == nil {
			return "nothing"
		}
		return *p
	}
	switch c.Field {
	case "image":
		return svc + " changed from " + val(c.From) + " to " + val(c.To) + " after this page was last verified"
	case "build":
		return svc + "’s build or start settings changed after this page was last verified"
	case "server":
		return svc + " moved from " + val(c.From) + " to " + val(c.To) + " after this page was last verified"
	}
	what := c.Field
	switch {
	case c.From == nil:
		return svc + "’s " + what + " " + c.Name + " was added after this page was last verified"
	case c.To == nil:
		return svc + "’s " + what + " " + c.Name + " was removed after this page was last verified"
	}
	return svc + "’s " + what + " " + c.Name + " was rewritten after this page was last verified"
}
