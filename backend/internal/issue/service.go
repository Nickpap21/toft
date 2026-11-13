package issue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Nickpap21/toft/backend/internal/code"
	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"github.com/Nickpap21/toft/backend/pkg/actor"
	"github.com/Nickpap21/toft/backend/pkg/broker"
	"github.com/Nickpap21/toft/backend/pkg/listversion"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"go.uber.org/zap"
)

// Code is what issues ask of the code module. Satisfied by *code.Service.
type Code interface {
	BranchExists(ctx context.Context, repoID, branch string) bool
	CommitContains(ctx context.Context, repoID, ancestor, descendant string) (bool, error)
}

// Service is the issues module.
type Service struct {
	repo Repository
	code Code
	log  *zap.Logger
	// failures counts each outbox event's failed applications; after 5 it
	// is marked applied and logged. Only ApplyEvents (the events
	// dispatcher's goroutine) touches it.
	// ponytail: in memory, so a restart gives a failing event 5 more tries.
	failures map[int64]int
	docs     Docs
	mentions Mentions
	// clock is now's source; tests set it.
	clock func() time.Time
	// agents is the agent policy (D-102).
	agents AgentGate
	pub    Publisher
}

// AgentGate is the workspace's agent policy (D-100, D-102). Satisfied by
// *agent.Service.
type AgentGate interface {
	Check(ctx context.Context, t actor.Target) error
}

// WithAgentGate puts agents' issues and comments under the policy.
func (s *Service) WithAgentGate(g AgentGate) *Service {
	s.agents = g
	return s
}

// checkAgent runs the agent policy for tool in a project; nil for people.
func (s *Service) checkAgent(ctx context.Context, projectID, tool string) error {
	if s.agents == nil {
		return nil
	}
	return s.agents.Check(ctx, actor.Target{Action: "issues", Tool: tool, ProjectID: projectID})
}

// Docs is what issues and ⌘K show of the docs module (D-86). Satisfied by
// *doc.Service.
type Docs interface {
	IssueDocs(ctx context.Context, issueID string) ([]code.DocRef, error)
	// SearchDocs finds the workspace's pages by tsq, a to_tsquery.
	SearchDocs(ctx context.Context, orgID, projectID, tsq string, limit int) ([]DocHit, error)
}

// Mentions are the workspace's @handles (D-88). Satisfied by
// *inbox.Service.
type Mentions interface {
	Handles(ctx context.Context, orgID string) []string
}

// WithMentions renders @handle in issues' text.
func (s *Service) WithMentions(m Mentions) *Service {
	s.mentions = m
	return s
}

// ProjectHandles are a project's workspace's @handles. Satisfies code.Issues.
func (s *Service) ProjectHandles(ctx context.Context, projectID string) []string {
	p, err := s.repo.Project(ctx, projectID)
	if err != nil {
		return nil
	}
	return s.handles(ctx, p.OrgID)
}

func (s *Service) handles(ctx context.Context, orgID string) []string {
	if s.mentions == nil {
		return nil
	}
	return s.mentions.Handles(ctx, orgID)
}

// Publisher tells a workspace's open tabs. Satisfied by *broker.Broker.
type Publisher interface {
	Publish(orgID string, e broker.Event)
}

// WithPublisher tells the workspace's tabs when an issue is created or
// changes, so the counts that read them refresh.
func (s *Service) WithPublisher(p Publisher) *Service {
	s.pub = p
	return s
}

// changed tells the workspace's tabs that a project's issues changed. Call
// it once a change has committed.
func (s *Service) changed(orgID, projectID string) {
	if s.pub != nil {
		s.pub.Publish(orgID, broker.Changed(projectID, "issues"))
	}
}

// WithDocs lists the pages that name an issue on it, and pages in ⌘K.
func (s *Service) WithDocs(d Docs) *Service {
	s.docs = d
	return s
}

func NewService(repo Repository, c Code, log *zap.Logger) *Service {
	return &Service{repo: repo, code: c, log: log, failures: map[int64]int{}}
}

// ─── validation ──────────────────────────────────────────────────────────────

func checkTitle(t string) (string, error) {
	t = strings.TrimSpace(t)
	if n := utf8.RuneCountInString(t); n < 1 || n > 256 || strings.IndexFunc(t, unicode.IsControl) >= 0 {
		return "", ErrBadTitle
	}
	return t, nil
}

func checkLabels(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, l := range in {
		l = strings.ToLower(strings.TrimSpace(l))
		if n := utf8.RuneCountInString(l); n < 1 || n > 40 || strings.ContainsRune(l, ',') || strings.IndexFunc(l, unicode.IsControl) >= 0 {
			return nil, ErrBadLabel
		}
		if !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	if len(out) > 10 {
		return nil, ErrBadLabel
	}
	slices.Sort(out)
	return out, nil
}

// assignable is nil when userID is an owner or developer of the workspace.
func (s *Service) assignable(ctx context.Context, orgID, userID string) error {
	role, err := s.repo.MemberRole(ctx, orgID, userID)
	if err != nil {
		return err
	}
	if role != "owner" && role != "developer" {
		return ErrNotAssignable
	}
	return nil
}

// ─── keys ────────────────────────────────────────────────────────────────────

// IssueKeys are the project's key and its former ones, none before its
// first issue. Satisfies code.Issues.
func (s *Service) IssueKeys(ctx context.Context, projectID string) []string {
	p, err := s.repo.Project(ctx, projectID)
	if err != nil {
		return nil
	}
	keys, err := s.keys(ctx, p)
	if err != nil {
		return nil
	}
	return keys
}

// ensureKey gives the project its key at its first issue (D-81).
func (s *Service) ensureKey(ctx context.Context, p *Project) (string, error) {
	if p.Key != "" {
		return p.Key, nil
	}
	taken, err := s.repo.OrgKeys(ctx, p.OrgID)
	if err != nil {
		return "", err
	}
	for _, k := range keyCandidates(p.Name) {
		if slices.Contains(taken, k) {
			continue
		}
		ok, err := s.repo.SetKey(ctx, p.ID, k)
		if err != nil {
			return "", err
		}
		if ok {
			return k, nil
		}
		// Another project took it, or this one got a key meanwhile.
		if again, err := s.repo.Project(ctx, p.ID); err == nil && again.Key != "" {
			return again.Key, nil
		}
	}
	return "", fmt.Errorf("no issue key left for project %q", p.Name)
}

// ─── issues ──────────────────────────────────────────────────────────────────

// Create opens an issue; the project's first sets its key.
func (s *Service) Create(ctx context.Context, projectID, actorID string, in CreateInput) (*Detail, error) {
	p, err := s.repo.Project(ctx, projectID)
	if err != nil {
		return nil, err
	}
	n := NewIssue{ProjectID: projectID, Description: in.Description, Status: in.Status, Priority: in.Priority,
		AssigneeID: in.AssigneeID, CreatedBy: actorID}
	if n.Title, err = checkTitle(in.Title); err != nil {
		return nil, err
	}
	if len(n.Description) > 65536 {
		return nil, ErrBadDescription
	}
	if n.Status == "" {
		n.Status = StatusBacklog
	}
	if !slices.Contains(statuses, n.Status) {
		return nil, ErrBadStatus
	}
	if n.Priority == "" {
		n.Priority = "none"
	}
	if !slices.Contains(priorities, n.Priority) {
		return nil, ErrBadPriority
	}
	if n.Labels, err = checkLabels(in.Labels); err != nil {
		return nil, err
	}
	if n.AssigneeID != "" {
		if err := s.assignable(ctx, p.OrgID, n.AssigneeID); err != nil {
			return nil, err
		}
	}
	types, err := s.types(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	t, err := s.usable(ctx, p, types, in.TypeID)
	if err != nil {
		return nil, err
	}
	n.TypeID = t.ID
	parent, err := s.parentFor(ctx, p.OrgID, in.KeyProject, "", t, in.Parent, types)
	if err != nil {
		return nil, err
	}
	if parent != nil {
		n.ParentID = parent.ID
	}
	// Cycle, dates and estimate (D-90): a sub-issue takes its parent's
	// cycle unless it names one.
	if in.Cycle != nil {
		if n.CycleID, err = s.cycleFor(ctx, p, t, *in.Cycle); err != nil {
			return nil, err
		}
	} else if parent != nil && parent.CycleID != "" && t.Kind != "container" {
		n.CycleID = s.openCycle(ctx, p, parent.CycleID)
	}
	if n.StartOn, err = parseDate(in.StartOn); err != nil {
		return nil, err
	}
	if n.DueOn, err = parseDate(in.DueOn); err != nil {
		return nil, err
	}
	if err := inOrder(n.StartOn, n.DueOn); err != nil {
		return nil, err
	}
	if n.Estimate, err = checkEstimate(p, t, in.Estimate); err != nil {
		return nil, err
	}
	// Agents don't assign people or plan cycles (D-102).
	if _, ok := actor.AgentFrom(ctx); ok && (in.AssigneeID != "" || in.Cycle != nil) {
		return nil, ErrAgentField
	}
	if err := s.checkAgent(ctx, projectID, "create_issue"); err != nil {
		return nil, err
	}
	if p.Key, err = s.ensureKey(ctx, p); err != nil {
		return nil, err
	}
	number, err := s.repo.Create(ctx, n)
	if err != nil {
		return nil, err
	}
	s.changed(p.OrgID, projectID)
	return s.Get(ctx, projectID, number)
}

// Update changes what in sends, each change a history row.
func (s *Service) Update(ctx context.Context, projectID string, number int, actorID string, in UpdateInput) (*Detail, error) {
	p, err := s.repo.Project(ctx, projectID)
	if err != nil {
		return nil, err
	}
	st, err := s.repo.Get(ctx, projectID, number)
	if err != nil {
		return nil, err
	}
	var events []Event
	change := func(kind, from, to string) {
		if from != to {
			events = append(events, Event{IssueID: st.ID, Kind: kind, ActorID: actorID, From: from, To: to})
		}
	}
	if in.Title != nil {
		t, err := checkTitle(*in.Title)
		if err != nil {
			return nil, err
		}
		change("title", st.Title, t)
		st.Title = t
	}
	if in.Description != nil {
		if len(*in.Description) > 65536 {
			return nil, ErrBadDescription
		}
		st.Description = *in.Description
	}
	if in.Status != nil {
		if !slices.Contains(statuses, *in.Status) {
			return nil, ErrBadStatus
		}
		change("status", st.Status, *in.Status)
		st.Status = *in.Status
	}
	if in.Priority != nil {
		if !slices.Contains(priorities, *in.Priority) {
			return nil, ErrBadPriority
		}
		change("priority", st.Priority, *in.Priority)
		st.Priority = *in.Priority
	}
	if in.AssigneeID != nil {
		if *in.AssigneeID != "" && *in.AssigneeID != st.AssigneeID {
			if err := s.assignable(ctx, p.OrgID, *in.AssigneeID); err != nil {
				return nil, err
			}
		}
		change("assigned", st.AssigneeID, *in.AssigneeID)
		st.AssigneeID = *in.AssigneeID
	}
	labelsChanged, labelsVersion := false, ""
	if in.Labels != nil {
		if in.Version == "" {
			return nil, listversion.ErrRequired
		}
		labelsVersion = in.Version
		l, err := checkLabels(*in.Labels)
		if err != nil {
			return nil, err
		}
		if labelsChanged = !slices.Equal(l, st.Labels); labelsChanged {
			change("labels", strings.Join(st.Labels, ", "), strings.Join(l, ", "))
		}
		st.Labels = l
	}
	// Type and parent (D-83): a sub-task keeps a parent; a parent is never
	// the issue itself or one of its children.
	types, err := s.types(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	t := typeOf(types, st.TypeID)
	if in.TypeID != nil && *in.TypeID != st.TypeID {
		if t, err = s.usable(ctx, p, types, *in.TypeID); err != nil {
			return nil, err
		}
		change("type", typeOf(types, st.TypeID).Name, t.Name)
		st.TypeID = t.ID
	}
	if in.Parent != nil {
		parent, err := s.parentFor(ctx, p.OrgID, in.KeyProject, st.ID, t, *in.Parent, types)
		if err != nil {
			return nil, err
		}
		before, after, next := "", "", ""
		if st.ParentID != "" {
			if old, err := s.repo.IssueRef(ctx, st.ParentID); err == nil {
				before = KeyOf(old.Key, old.Number, old.Digits)
			}
		}
		if parent != nil {
			next, after = parent.ID, KeyOf(parent.Key, parent.Number, parent.Digits)
		}
		change("parent", before, after)
		st.ParentID = next
	}
	if t.Kind == "subtask" && st.ParentID == "" {
		return nil, ErrNeedsParent
	}
	// Cycle, dates and estimate (D-90).
	if in.Cycle != nil {
		cid, err := s.cycleFor(ctx, p, t, *in.Cycle)
		if err != nil {
			return nil, err
		}
		if cid != st.CycleID {
			change("cycle", itoa0(st.CycleNumber), itoa0(*in.Cycle))
		}
		st.CycleID = cid
	} else if t.Kind == "container" && st.CycleID != "" {
		return nil, ErrContainerCycle
	}
	if in.StartOn != nil {
		d, err := parseDate(*in.StartOn)
		if err != nil {
			return nil, err
		}
		change("start", dateOr(st.StartOn), dateOr(d))
		st.StartOn = d
	}
	if in.DueOn != nil {
		d, err := parseDate(*in.DueOn)
		if err != nil {
			return nil, err
		}
		change("due", dateOr(st.DueOn), dateOr(d))
		st.DueOn = d
	}
	if err := inOrder(st.StartOn, st.DueOn); err != nil {
		return nil, err
	}
	if in.Estimate != nil {
		e, err := checkEstimate(p, t, *in.Estimate)
		if err != nil {
			return nil, err
		}
		change("estimate", itoa0(intOr0(st.Estimate)), itoa0(*in.Estimate))
		st.Estimate = e
	} else if t.Kind == "container" {
		st.Estimate = nil
	}
	from := st.Status
	if in.Status != nil {
		from = statusBefore(events, st.Status)
	}
	if err := s.repo.Update(ctx, st, labelsChanged, labelsVersion, events); err != nil {
		return nil, err
	}
	if from != st.Status {
		s.afterStatus(ctx, st.ID, st.Status) // tells the workspace
	} else {
		s.changed(p.OrgID, projectID)
	}
	return s.Get(ctx, projectID, number)
}

// Get is one issue with its linked work.
func (s *Service) Get(ctx context.Context, projectID string, number int) (*Detail, error) {
	p, err := s.repo.Project(ctx, projectID)
	if err != nil {
		return nil, err
	}
	st, err := s.repo.Get(ctx, projectID, number)
	if err != nil {
		return nil, err
	}
	types, err := s.types(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	keys, err := s.keys(ctx, p)
	if err != nil {
		return nil, err
	}
	d := &Detail{
		version: listversion.Of(st.Labels),
		Issue: Issue{Key: KeyOf(p.Key, st.Number, p.Digits), Number: st.Number, Title: st.Title, Status: st.Status, Priority: st.Priority,
			Assignee: person(st.AssigneeID, st.AssigneeName), Labels: st.Labels, CreatedAt: st.CreatedAt, UpdatedAt: st.UpdatedAt,
			Type: typeOf(types, st.TypeID), BlockedBy: []string{},
			Cycle:   refOf(st.CycleNumber, st.CycleName, st.CycleStarts, st.CycleEnds, todayIn(p.Zone, s.now())),
			StartOn: optDate(st.StartOn), DueOn: optDate(st.DueOn), Estimate: st.Estimate},
		Description: st.Description, CreatedBy: withAgent(person(st.CreatedByID, st.CreatedByName), st.CreatedByAgent), StatusChangedAt: st.StatusAt,
		Comments: st.Comments, Links: []Link{}, Relations: []Relation{}, Children: []Related{}, Docs: []code.DocRef{}, Cycles: []int{},
	}
	if st.CarriedFrom > 0 {
		d.CarriedFrom = &st.CarriedFrom
	}
	if d.Type.Kind == "container" {
		d.Estimate = nil
		if st.ChildrenPoints > 0 {
			d.Estimate = &st.ChildrenPoints
		}
	}
	if s.docs != nil {
		if d.Docs, err = s.docs.IssueDocs(ctx, st.ID); err != nil {
			return nil, err
		}
	}
	d.HTML = gitstore.MarkdownRefs([]byte(st.Description), gitstore.Refs{Keys: keys, Pulls: true, Mentions: s.handles(ctx, p.OrgID)})
	if st.ParentID != "" {
		if par, err := s.repo.IssueRef(ctx, st.ParentID); err == nil {
			d.Parent = &ParentRef{Key: KeyOf(par.Key, par.Number, par.Digits), Title: par.Title}
		}
	}
	related := func(r RefRow) Related {
		return Related{Key: KeyOf(r.Key, r.Number, r.Digits), Number: r.Number, Title: r.Title, Status: r.Status,
			Type: typeOf(types, r.TypeID), ProjectID: r.ProjectID, Assignee: person(r.AssigneeID, r.AssigneeName)}
	}
	rels, err := s.repo.Relations(ctx, st.ID)
	if err != nil {
		return nil, err
	}
	for _, r := range rels {
		side := sideOf(r.Kind, r.Outward)
		d.Relations = append(d.Relations, Relation{ID: r.ID, Kind: side, Issue: related(r.Other)})
		if side == "blocked_by" && r.Other.Status != StatusShipped && r.Other.Status != StatusCancelled {
			d.BlockedBy = append(d.BlockedBy, KeyOf(r.Other.Key, r.Other.Number, r.Other.Digits))
		}
	}
	kids, err := s.repo.Children(ctx, st.ID)
	if err != nil {
		return nil, err
	}
	if len(kids) > 0 {
		pr := Progress{Total: len(kids)}
		for _, k := range kids {
			d.Children = append(d.Children, related(k))
			if k.CycleNumber > 0 && !slices.Contains(d.Cycles, k.CycleNumber) {
				d.Cycles = append(d.Cycles, k.CycleNumber)
			}
			if k.Status == StatusShipped {
				pr.Shipped++
			}
			if k.Status == StatusShipped || k.Status == StatusCancelled {
				pr.Done++
			}
		}
		d.Progress = &pr
		slices.Sort(d.Cycles)
	}
	stored, err := s.repo.Links(ctx, st.ID)
	if err != nil {
		return nil, err
	}
	var heads []string
	for _, l := range stored {
		if l.PullState == "open" {
			heads = append(heads, l.HeadSHA)
		}
	}
	checks, err := s.checks(ctx, heads)
	if err != nil {
		return nil, err
	}
	for _, l := range stored {
		if l.PullID == "" {
			exists := s.code.BranchExists(ctx, l.RepositoryID, l.Branch)
			d.Links = append(d.Links, Link{Kind: "branch", Repository: l.Repository, RepositoryID: l.RepositoryID, Branch: l.Branch, Exists: &exists})
			continue
		}
		link := Link{Kind: "pull_request", Repository: l.Repository, RepositoryID: l.RepositoryID, Number: l.PullNumber,
			Title: l.PullTitle, State: l.PullState, HeadBranch: l.Head, BaseBranch: l.Base, MergeSHA: l.MergeSHA}
		if l.PullState == "open" {
			link.Checks = checks[l.RepositoryID+" "+l.HeadSHA]
		}
		if l.PullState == "merged" && l.MergedAt != nil {
			if link.Deploys, err = s.deploys(ctx, l.RepositoryID, l.Base, l.MergeSHA, *l.MergedAt); err != nil {
				return nil, err
			}
		}
		d.Links = append(d.Links, link)
	}
	return d, nil
}

// checks are the summaries of these commits' checks, by "<repository> <sha>".
func (s *Service) checks(ctx context.Context, shas []string) (map[string]*code.CheckSummary, error) {
	states, err := s.repo.CheckStates(ctx, shas)
	if err != nil {
		return nil, err
	}
	by := map[string][]string{}
	for _, c := range states {
		k := c.RepositoryID + " " + c.SHA
		by[k] = append(by[k], c.State)
	}
	out := make(map[string]*code.CheckSummary, len(by))
	for k, v := range by {
		out[k] = code.SummarizeStates(v)
	}
	return out, nil
}

// deploys are the services a merge of mergeSHA into base deploys to, and
// whether each runs it: live when its live commit holds the merge, else
// its newest deploy since the merge.
func (s *Service) deploys(ctx context.Context, repoID, base, mergeSHA string, mergedAt time.Time) ([]Deploy, error) {
	targets, err := s.repo.BranchTargets(ctx, repoID, base)
	if err != nil || len(targets) == 0 {
		return nil, err
	}
	sids := make([]string, 0, len(targets))
	for _, t := range targets {
		sids = append(sids, t.ServiceID)
	}
	latest, err := s.repo.LatestSince(ctx, sids, mergedAt)
	if err != nil {
		return nil, err
	}
	out := make([]Deploy, 0, len(targets))
	for _, t := range targets {
		d := Deploy{ServiceID: t.ServiceID, Service: t.Service, Environment: t.Environment, State: "waiting"}
		if s.runs(ctx, repoID, mergeSHA, t.LiveSHA) {
			d.State, d.SHA, d.DeploymentID = "live", t.LiveSHA, t.LiveDeploymentID
		} else if l, ok := latest[t.ServiceID]; ok {
			d.SHA, d.DeploymentID = l.SHA, l.ID
			switch l.Status {
			case "pending", "building":
				d.State = "building"
			case "failed":
				d.State = "failed"
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// runs says whether live (a service's live commit) holds mergeSHA.
func (s *Service) runs(ctx context.Context, repoID, mergeSHA, live string) bool {
	if live == "" || mergeSHA == "" {
		return false
	}
	ok, err := s.code.CommitContains(ctx, repoID, mergeSHA, live)
	if err != nil {
		s.log.Warn("is the merge live", zap.Error(err), zap.String("repository_id", repoID))
		return false
	}
	return ok
}

// ─── the list ────────────────────────────────────────────────────────────────

// tsqWords are the words of a search, by prefix, for to_tsquery.
var tsqWords = regexp.MustCompile(`[\p{L}\p{N}]+`)

// parseQ reads a search: STO-12 or 12 (or !12) is a number, the words of
// anything else match titles by prefix.
func parseQ(q string) (hasQ bool, tsq, key string, number int) {
	q = strings.TrimSpace(q)
	if q == "" {
		return false, "", "", 0
	}
	if m := regexp.MustCompile(`^(?i)([a-z][a-z0-9]{1,9})-(\d{1,9})$`).FindStringSubmatch(q); m != nil {
		number, _ = strconv.Atoi(m[2])
		return true, "", strings.ToUpper(m[1]), number
	}
	if m := regexp.MustCompile(`^[!#]?(\d{1,9})$`).FindStringSubmatch(q); m != nil {
		number, _ = strconv.Atoi(m[1])
	}
	words := tsqWords.FindAllString(strings.ToLower(q), 8)
	for i, w := range words {
		words[i] = w + ":*"
	}
	return true, strings.Join(words, " & "), "", number
}

func splitList(v string) []string {
	out := []string{}
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Query reads the list's filters; me is the caller's account.
func Query(projectID, me string, status, priority, assignee, label, q, order, typ, parent, cycle string) (ListQuery, error) {
	lq := ListQuery{ProjectID: projectID, Statuses: splitList(status), Priorities: splitList(priority),
		Labels: splitList(strings.ToLower(label)), Assignee: assignee, Order: order, TypeIDs: splitList(typ), ParentKey: strings.TrimSpace(parent),
		CycleKey: cycle}
	if cycle != "" && !validCycleFilter(cycle) {
		return lq, ErrBadQuery
	}
	for _, st := range lq.Statuses {
		if !slices.Contains(statuses, st) {
			return lq, ErrBadQuery
		}
	}
	for _, p := range lq.Priorities {
		if !slices.Contains(priorities, p) {
			return lq, ErrBadQuery
		}
	}
	if lq.Assignee == "me" {
		lq.Assignee = me
	}
	if lq.Order == "" {
		lq.Order = "priority"
	}
	if !slices.Contains([]string{"priority", "updated", "created", "due"}, lq.Order) || len(q) > 200 {
		return lq, ErrBadQuery
	}
	lq.HasQ, lq.TSQ, _, lq.QNumber = parseQ(q)
	return lq, nil
}

// List is a page of issues; the cursor is the last row's sort keys.
func (s *Service) List(ctx context.Context, lq ListQuery, cursor string, limit int) ([]Issue, string, error) {
	p, err := s.repo.Project(ctx, lq.ProjectID)
	if err != nil {
		return nil, "", err
	}
	types, err := s.prepare(ctx, p, &lq)
	if err != nil {
		return nil, "", err
	}
	if cursor != "" {
		at, rest, err := respond.ParseCursor(cursor)
		rank, rid, ok := strings.Cut(rest, ":")
		r, convErr := strconv.Atoi(rank)
		if err != nil || !ok || convErr != nil {
			return nil, "", ErrBadCursor
		}
		lq.AfterAt, lq.AfterRank, lq.AfterID = at, r, rid
	}
	lq.Limit = min(max(limit, 1), 200) + 1
	rows, err := s.repo.List(ctx, lq)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) == lq.Limit {
		rows = rows[:lq.Limit-1]
		last := rows[len(rows)-1]
		next = respond.Cursor(last.SortAt, strconv.Itoa(last.SortRank)+":"+last.ID)
	}
	var heads []string
	for _, r := range rows {
		if r.PullState == "open" {
			heads = append(heads, r.PullHeadSHA)
		}
	}
	checks, err := s.checks(ctx, heads)
	if err != nil {
		return nil, "", err
	}
	today := todayIn(p.Zone, s.now())
	out := make([]Issue, 0, len(rows))
	for _, r := range rows {
		it := Issue{Key: KeyOf(p.Key, r.Number, p.Digits), Number: r.Number, Title: r.Title, Status: r.Status, Priority: r.Priority,
			Assignee: person(r.AssigneeID, r.AssigneeName), Labels: r.Labels, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
			Type: typeOf(types, r.TypeID), BlockedBy: r.BlockedBy,
			Cycle:   refOf(r.CycleNumber, r.CycleName, r.CycleStarts, r.CycleEnds, today),
			StartOn: optDate(r.StartOn), DueOn: optDate(r.DueOn), Estimate: r.Estimate}
		if it.Type.Kind == "container" {
			it.Estimate = nil
			if r.ChildrenPoints > 0 {
				it.Estimate = &r.ChildrenPoints
			}
		}
		if it.BlockedBy == nil {
			it.BlockedBy = []string{}
		}
		if r.ParentKey != "" {
			it.Parent = &ParentRef{Key: r.ParentKey, Title: r.ParentTitle}
		}
		if r.Children > 0 {
			it.Progress = &Progress{Total: r.Children, Shipped: r.ChildrenShipped, Done: r.ChildrenDone}
		}
		switch {
		case r.PullNumber > 0:
			it.Work = &Work{Kind: "pull_request", Number: r.PullNumber, State: r.PullState, RepositoryID: r.PullRepoID}
			if r.PullState == "open" {
				it.Work.Checks = checks[r.PullRepoID+" "+r.PullHeadSHA]
			}
			if r.PullState == "merged" {
				it.Work.SHA, it.Work.Deploy = r.PullMergeSHA, deployState(r.PullDeploys)
			}
		case r.Branch != "":
			it.Work = &Work{Kind: "branch", Branch: r.Branch}
		}
		out = append(out, it)
	}
	return out, next, nil
}

// deployState sums up a merge's deployments' statuses.
func deployState(statuses string) string {
	switch {
	case statuses == "":
		return ""
	case strings.Contains(statuses, "failed"):
		return "failed"
	case strings.Contains(statuses, "pending"), strings.Contains(statuses, "building"):
		return "building"
	}
	return "live"
}

// Counts is how many issues each status has under lq's other filters.
func (s *Service) Counts(ctx context.Context, lq ListQuery) (map[string]int, error) {
	p, err := s.repo.Project(ctx, lq.ProjectID)
	if err != nil {
		return nil, err
	}
	if _, err := s.prepare(ctx, p, &lq); err != nil {
		return nil, err
	}
	return s.repo.Count(ctx, lq)
}

// noIssue matches no issue: a parent key that names none.
const noIssue = "00000000-0000-0000-0000-000000000000"

// prepare resolves lq's parent key and says whether issues without a type
// match its types (when the fallback type is one of them).
func (s *Service) prepare(ctx context.Context, p *Project, lq *ListQuery) ([]Type, error) {
	types, err := s.types(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	lq.Untyped = slices.Contains(lq.TypeIDs, fallback(types).ID)
	if lq.ParentKey != "" {
		pid, err := s.byKey(ctx, p.OrgID, lq.ParentKey)
		if errors.Is(err, ErrIssueNotFound) {
			pid = noIssue
		} else if err != nil {
			return nil, err
		}
		lq.ParentID = pid
	}
	switch lq.CycleKey {
	case "":
	case "none":
		lq.NoCycle = true
	default:
		lq.CycleID = noIssue
		if c, err := s.cycleByKey(ctx, p, lq.CycleKey); err != nil {
			return nil, err
		} else if c != nil {
			lq.CycleID = c.ID
		}
	}
	return types, nil
}

// ─── history and comments ────────────────────────────────────────────────────

func (s *Service) issueOf(ctx context.Context, projectID string, number int) (*Project, *Stored, error) {
	p, err := s.repo.Project(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	st, err := s.repo.Get(ctx, projectID, number)
	if err != nil {
		return nil, nil, err
	}
	return p, st, nil
}

// Timeline pages an issue's history and comments, oldest first.
func (s *Service) Timeline(ctx context.Context, projectID string, number int, cursor string, limit int) ([]TimelineItem, string, error) {
	p, st, err := s.issueOf(ctx, projectID, number)
	if err != nil {
		return nil, "", err
	}
	afterAt, afterID, err := respond.ParseCursor(cursor)
	if err != nil {
		return nil, "", ErrBadCursor
	}
	limit = min(max(limit, 1), 200)
	items, err := s.repo.Timeline(ctx, st.ID, afterAt, afterID, limit+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		last := items[limit-1]
		next = respond.Cursor(last.CreatedAt, last.ID)
	}
	keys, err := s.keys(ctx, p)
	if err != nil {
		return nil, "", err
	}
	refs := gitstore.Refs{Keys: keys, Pulls: true, Mentions: s.handles(ctx, p.OrgID)}
	out := make([]TimelineItem, 0, len(items))
	for _, it := range items {
		out = append(out, toItem(it, refs))
	}
	return out, next, nil
}

func toItem(it StoredItem, refs gitstore.Refs) TimelineItem {
	t := TimelineItem{Type: it.Item, ID: it.ID, Actor: withAgent(person(it.ActorID, it.ActorName), it.AgentName), CreatedAt: it.CreatedAt, EditedAt: it.EditedAt}
	if it.Item == "comment" {
		t.Body, t.HTML = it.Body, gitstore.MarkdownRefs([]byte(it.Body), refs)
		return t
	}
	t.Kind, t.Automatic, t.From, t.To, t.Branch = it.Kind, it.Automatic, it.From, it.To, it.Branch
	if it.Kind == "assigned" {
		t.Assignee = person(it.To, it.AssigneeName)
	}
	if it.PullNumber > 0 {
		t.PullRequest = &PullRef{Number: it.PullNumber, Title: it.PullTitle, Repository: it.Repository}
	}
	if it.Service != "" {
		t.Deploy = &DeployRef{Service: it.Service, Environment: it.Environment, SHA: it.CommitSHA}
	}
	return t
}

func checkBody(b string) (string, error) {
	if strings.TrimSpace(b) == "" || len(b) > 65536 {
		return "", ErrEmptyComment
	}
	return b, nil
}

// AddComment comments on an issue.
func (s *Service) AddComment(ctx context.Context, projectID string, number int, authorID, body string) (*TimelineItem, error) {
	p, st, err := s.issueOf(ctx, projectID, number)
	if err != nil {
		return nil, err
	}
	if body, err = checkBody(body); err != nil {
		return nil, err
	}
	if err := s.checkAgent(ctx, projectID, "comment"); err != nil {
		return nil, err
	}
	c, err := s.repo.AddComment(ctx, st.ID, authorID, body)
	if err != nil {
		return nil, err
	}
	c2, err := s.repo.GetComment(ctx, st.ID, c.ID)
	if err != nil {
		return nil, err
	}
	return s.commentItem(ctx, c2, p)
}

func (s *Service) commentItem(ctx context.Context, c *StoredComment, p *Project) (*TimelineItem, error) {
	keys, err := s.keys(ctx, p)
	if err != nil {
		return nil, err
	}
	t := toItem(StoredItem{Item: "comment", ID: c.ID, ActorID: c.AuthorID, ActorName: c.AuthorName, AgentName: c.AgentName, Body: c.Body,
		CreatedAt: c.CreatedAt, EditedAt: c.EditedAt}, gitstore.Refs{Keys: keys, Pulls: true, Mentions: s.handles(ctx, p.OrgID)})
	return &t, nil
}

// EditComment changes a comment; only its author may.
func (s *Service) EditComment(ctx context.Context, projectID string, number int, commentID, actorID, body string) (*TimelineItem, error) {
	p, st, err := s.issueOf(ctx, projectID, number)
	if err != nil {
		return nil, err
	}
	c, err := s.repo.GetComment(ctx, st.ID, commentID)
	if err != nil {
		return nil, err
	}
	if c.AuthorID != actorID {
		return nil, ErrNotYours
	}
	if body, err = checkBody(body); err != nil {
		return nil, err
	}
	if err := s.repo.EditComment(ctx, c.ID, body); err != nil {
		return nil, err
	}
	if c, err = s.repo.GetComment(ctx, st.ID, commentID); err != nil {
		return nil, err
	}
	return s.commentItem(ctx, c, p)
}

// DeleteComment removes a comment: its author's, or anyone's for an owner.
func (s *Service) DeleteComment(ctx context.Context, projectID string, number int, commentID, actorID string, owner bool) error {
	_, st, err := s.issueOf(ctx, projectID, number)
	if err != nil {
		return err
	}
	c, err := s.repo.GetComment(ctx, st.ID, commentID)
	if err != nil {
		return err
	}
	if c.AuthorID != actorID && !owner {
		return ErrNotYours
	}
	return s.repo.DeleteComment(ctx, c.ID)
}

// ─── labels and views ────────────────────────────────────────────────────────

// Labels are the project's labels and its services' names (D-81).
func (s *Service) Labels(ctx context.Context, projectID string) ([]Label, error) {
	labels, err := s.repo.Labels(ctx, projectID)
	if err != nil {
		return nil, err
	}
	names, err := s.repo.ServiceNames(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for i := range labels {
		labels[i].Service = slices.Contains(names, labels[i].Name)
	}
	for _, n := range names {
		n = strings.ToLower(n)
		if !slices.ContainsFunc(labels, func(l Label) bool { return l.Name == n }) {
			labels = append(labels, Label{Name: n, Service: true})
		}
	}
	slices.SortFunc(labels, func(a, b Label) int { return strings.Compare(a.Name, b.Name) })
	return labels, nil
}

// checkFilters canonicalises a view's filters.
func checkFilters(f Filters) ([]byte, error) {
	for _, st := range f.Status {
		if !slices.Contains(statuses, st) {
			return nil, ErrBadFilters
		}
	}
	for _, p := range f.Priority {
		if !slices.Contains(priorities, p) {
			return nil, ErrBadFilters
		}
	}
	if f.Order != "" && !slices.Contains([]string{"priority", "updated", "created", "due"}, f.Order) ||
		f.Group != "" && !slices.Contains([]string{"status", "priority", "assignee", "type", "cycle"}, f.Group) ||
		f.Cycle != "" && !validCycleFilter(f.Cycle) ||
		len(f.Label) > 10 || len(f.Q) > 200 || len(f.Assignee) > 64 {
		return nil, ErrBadFilters
	}
	b, err := json.Marshal(f)
	if err != nil || len(b) > 4096 {
		return nil, ErrBadFilters
	}
	return b, nil
}

func checkViewName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if c := utf8.RuneCountInString(n); c < 1 || c > 60 || strings.IndexFunc(n, unicode.IsControl) >= 0 {
		return "", ErrBadName
	}
	return n, nil
}

func toViewOut(v StoredView) View {
	out := View{ID: v.ID, Name: v.Name, Owner: person(v.OwnerID, v.OwnerName), CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}
	d := json.NewDecoder(bytes.NewReader(v.Filters))
	_ = d.Decode(&out.Filters)
	return out
}

// Views are the project's saved views.
func (s *Service) Views(ctx context.Context, projectID string) ([]View, error) {
	stored, err := s.repo.Views(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]View, 0, len(stored))
	for _, v := range stored {
		out = append(out, toViewOut(v))
	}
	return out, nil
}

// AddView saves a view.
func (s *Service) AddView(ctx context.Context, projectID, ownerID string, in ViewInput) (*View, error) {
	name, err := checkViewName(in.Name)
	if err != nil {
		return nil, err
	}
	f, err := checkFilters(in.Filters)
	if err != nil {
		return nil, err
	}
	vid, err := s.repo.AddView(ctx, projectID, ownerID, name, f)
	if err != nil {
		return nil, err
	}
	v, err := s.repo.GetView(ctx, projectID, vid)
	if err != nil {
		return nil, err
	}
	out := toViewOut(*v)
	return &out, nil
}

// viewFor is a view its owner or a workspace owner may change.
func (s *Service) viewFor(ctx context.Context, projectID, viewID, actorID string, owner bool) (*StoredView, error) {
	v, err := s.repo.GetView(ctx, projectID, viewID)
	if err != nil {
		return nil, err
	}
	if v.OwnerID != actorID && !owner {
		return nil, ErrNotYours
	}
	return v, nil
}

// UpdateView renames a view or changes its filters.
func (s *Service) UpdateView(ctx context.Context, projectID, viewID, actorID string, owner bool, in ViewInput) (*View, error) {
	if _, err := s.viewFor(ctx, projectID, viewID, actorID, owner); err != nil {
		return nil, err
	}
	name, err := checkViewName(in.Name)
	if err != nil {
		return nil, err
	}
	f, err := checkFilters(in.Filters)
	if err != nil {
		return nil, err
	}
	if err := s.repo.UpdateView(ctx, viewID, name, f); err != nil {
		return nil, err
	}
	v, err := s.repo.GetView(ctx, projectID, viewID)
	if err != nil {
		return nil, err
	}
	out := toViewOut(*v)
	return &out, nil
}

// DeleteView deletes a view.
func (s *Service) DeleteView(ctx context.Context, projectID, viewID, actorID string, owner bool) error {
	if _, err := s.viewFor(ctx, projectID, viewID, actorID, owner); err != nil {
		return err
	}
	return s.repo.DeleteView(ctx, viewID)
}

// ─── ⌘K and the rest of the panel ────────────────────────────────────────────

// Search is ⌘K's: the workspace's issues and pull requests (D-81).
func (s *Service) Search(ctx context.Context, orgID, projectID, q, types string, limit int) (*Search, error) {
	if len(q) > 200 {
		return nil, ErrBadQuery
	}
	sq := SearchQuery{OrgID: orgID, ProjectID: projectID, Limit: min(max(limit, 1), 20)}
	if limit == 0 {
		sq.Limit = 8
	}
	sq.HasQ, sq.TSQ, sq.QKey, sq.QNumber = parseQ(q)
	if k := strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(q), "-")); validKey.MatchString(k) {
		sq.QPrefix = k
	}
	want := splitList(types)
	all := len(want) == 0
	out := &Search{Issues: []SearchIssue{}, Pulls: []SearchPull{}, Docs: []DocHit{}}
	var err error
	if all || slices.Contains(want, "issues") {
		if out.Issues, err = s.repo.SearchIssues(ctx, sq); err != nil {
			return nil, err
		}
		types, err := s.types(ctx, orgID)
		if err != nil {
			return nil, err
		}
		for i := range out.Issues {
			out.Issues[i].Type = typeOf(types, out.Issues[i].typeID)
		}
	}
	if (all || slices.Contains(want, "pulls")) && sq.QKey == "" {
		if out.Pulls, err = s.repo.SearchPulls(ctx, sq); err != nil {
			return nil, err
		}
	}
	if (all || slices.Contains(want, "docs")) && sq.QKey == "" && s.docs != nil && (sq.TSQ != "" || !sq.HasQ) {
		if out.Docs, err = s.docs.SearchDocs(ctx, orgID, projectID, sq.TSQ, sq.Limit); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// PullIssues are the issues a pull request names. Satisfies code.Issues.
func (s *Service) PullIssues(ctx context.Context, pullID string) ([]code.IssueRef, error) {
	m, err := s.repo.PullIssues(ctx, []string{pullID})
	if err != nil {
		return nil, err
	}
	if m[pullID] == nil {
		return []code.IssueRef{}, nil
	}
	return m[pullID], nil
}

// BranchIssues are the issues each branch names. Satisfies code.Issues.
func (s *Service) BranchIssues(ctx context.Context, repoID string, branches []string) (map[string][]code.IssueRef, error) {
	if len(branches) == 0 {
		return map[string][]code.IssueRef{}, nil
	}
	return s.repo.BranchIssues(ctx, repoID, branches)
}

// statusBefore is the status a change in events moved from, else now.
func statusBefore(events []Event, now string) string {
	for _, e := range events {
		if e.Kind == "status" {
			return e.From
		}
	}
	return now
}
