package issue

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/apispec"
	"github.com/Nickpap21/toft/backend/internal/code"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ─── an in-memory Repository ─────────────────────────────────────────────────

type fakeLink struct{ issueID, repoID, branch, pullID string }

type fakePull struct {
	id, repoID, title, state, base, mergeSHA, mergedBy string
	number                                             int
	mergedAt                                           time.Time
}

type fakeRelation struct{ id, from, to, kind string }

type fakeRepo struct {
	types      map[string][]Type // org → types
	aliases    map[string]string // key → project
	hidden     map[string][]string
	relations  []fakeRelation
	orgDigits  int
	now        time.Time
	projects   map[string]*Project
	roles      map[string]string
	issues     []*Stored
	events     []Event
	comments   []*StoredComment
	cOwner     map[string]string // comment → issue
	views      []*StoredView
	vProject   map[string]string
	links      []fakeLink
	pulls      map[string]*fakePull
	targets    map[string][]Target // "<repo> <branch>"
	services   map[string]string   // service → repository
	names      []string            // the project's services
	git        []GitEvent
	applied    map[int64]bool
	deploys    map[string]*FailedDeployment // D-88
	fromDeploy map[string]string            // issue → deployment
	live       map[string]string            // service → live deployment
	// D-90: o1's settings and cycles, the cycle each carried issue came
	// from, and the days written.
	cyc     OrgSettings
	cycles  []StoredCycle
	carried map[string]string // issue → the cycle that carried it
	days    map[string][]CycleDay
}

func newFake() *fakeRepo {
	return &fakeRepo{
		now:      time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC),
		projects: map[string]*Project{"p1": {ID: "p1", OrgID: "o1", Name: "storefront"}, "p2": {ID: "p2", OrgID: "o1", Name: "store-ops"}},
		roles:    map[string]string{"dev": "developer", "own": "owner", "eve": "viewer"},
		cOwner:   map[string]string{}, vProject: map[string]string{}, pulls: map[string]*fakePull{},
		targets: map[string][]Target{}, services: map[string]string{}, names: []string{"api", "web"}, applied: map[int64]bool{},
		types: map[string][]Type{}, aliases: map[string]string{}, hidden: map[string][]string{},
		deploys: map[string]*FailedDeployment{}, fromDeploy: map[string]string{}, live: map[string]string{},
		cyc: OrgSettings{StartDay: 1, Zone: "UTC", Estimates: "off"}, carried: map[string]string{}, days: map[string][]CycleDay{},
	}
}

func (f *fakeRepo) SetFailedDeployIssues(_ context.Context, project string, on bool) error {
	f.projects[project].FailedDeployIssues = on
	return nil
}
func (f *fakeRepo) FailedDeployment(_ context.Context, id string) (*FailedDeployment, error) {
	d, ok := f.deploys[id]
	if !ok {
		return nil, ErrNotFound
	}
	c := *d
	if p, ok := f.projects[c.ProjectID]; ok {
		c.FailedDeployIssues = p.FailedDeployIssues
	}
	return &c, nil
}
func (f *fakeRepo) OpenFailureIssues(_ context.Context, service string) ([]FailureIssue, error) {
	out := []FailureIssue{}
	for i := len(f.issues) - 1; i >= 0; i-- {
		s := f.issues[i]
		if d, ok := f.deploys[f.fromDeploy[s.ID]]; ok && d.ServiceID == service && s.Status != StatusShipped && s.Status != StatusCancelled {
			out = append(out, FailureIssue{ID: s.ID, Number: s.Number})
		}
	}
	return out, nil
}
func (f *fakeRepo) SetFromDeployment(_ context.Context, issue, deployment string) error {
	f.fromDeploy[issue] = deployment
	return nil
}
func (f *fakeRepo) LiveDeployment(_ context.Context, service string) (string, error) {
	return f.live[service], nil
}
func (f *fakeRepo) MyIssues(_ context.Context, org, user string) ([]MyIssue, error) {
	out := []MyIssue{}
	for _, s := range f.issues {
		p := f.projects[s.ProjectID]
		if p.OrgID == org && s.AssigneeID == user && s.Status != StatusShipped && s.Status != StatusCancelled {
			out = append(out, MyIssue{Key: KeyOf(p.Key, s.Number, p.Digits), Number: s.Number, Title: s.Title, Status: s.Status,
				Priority: s.Priority, ProjectID: p.ID, Project: p.Name, UpdatedAt: s.UpdatedAt, typeID: s.TypeID})
		}
	}
	return out, nil
}

// tick is the fake's clock: every write is a second later.
func (f *fakeRepo) tick() time.Time { f.now = f.now.Add(time.Second); return f.now }

func (f *fakeRepo) issue(id string) *Stored {
	for _, s := range f.issues {
		if s.ID == id {
			return s
		}
	}
	return nil
}

func (f *fakeRepo) Project(_ context.Context, id string) (*Project, error) {
	if p, ok := f.projects[id]; ok {
		c := *p
		c.OrgDigits, c.Digits = f.orgDigits, f.orgDigits
		if c.OwnDigits != nil {
			c.Digits = *c.OwnDigits
		}
		for _, s := range f.issues {
			if s.ProjectID == id && s.Number >= c.NextNumber {
				c.NextNumber = s.Number + 1
			}
		}
		if c.NextNumber == 0 {
			c.NextNumber = 1
		}
		c.Cycles, c.Zone, c.Estimates = f.cyc.Weeks > 0, f.cyc.Zone, f.cyc.Estimates
		return &c, nil
	}
	return nil, ErrNotFound
}

// ─── types, keys, relations (D-83) ───

func (f *fakeRepo) Types(_ context.Context, org string) ([]Type, error) {
	return append([]Type{}, f.types[org]...), nil
}
func (f *fakeRepo) AddType(_ context.Context, org string, t TypeRef, pos int) (string, error) {
	for _, x := range f.types[org] {
		if x.Name == t.Name {
			return "", ErrTypeTaken
		}
	}
	t.ID = "t-" + strings.ToLower(t.Name)
	f.types[org] = append(f.types[org], Type{TypeRef: t, Position: pos})
	return t.ID, nil
}
func (f *fakeRepo) UpdateType(_ context.Context, org string, t Type) (bool, error) {
	for i, x := range f.types[org] {
		if x.ID == t.ID {
			t.Archived = x.Archived
			f.types[org][i] = t
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeRepo) SetTypeArchived(_ context.Context, org, id string, archived bool) (bool, error) {
	for i, x := range f.types[org] {
		if x.ID == id {
			f.types[org][i].Archived = archived
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeRepo) OrgDigits(context.Context, string) (int, error) { return f.orgDigits, nil }
func (f *fakeRepo) SetOrgDigits(_ context.Context, _ string, d int) error {
	f.orgDigits = d
	return nil
}
func (f *fakeRepo) Aliases(_ context.Context, project string) ([]string, error) {
	out := []string{}
	for k, p := range f.aliases {
		if p == project {
			out = append(out, k)
		}
	}
	return out, nil
}
func (f *fakeRepo) KeyHolder(_ context.Context, _, key string) (string, error) {
	for _, p := range f.projects {
		if p.Key == key {
			return p.ID, nil
		}
	}
	return f.aliases[key], nil
}
func (f *fakeRepo) ReplaceKey(_ context.Context, _, project, old, key string) error {
	if old != "" {
		f.aliases[old] = project
	}
	delete(f.aliases, key)
	f.projects[project].Key = key
	return nil
}
func (f *fakeRepo) HiddenTypes(_ context.Context, project string) ([]string, error) {
	return append([]string{}, f.hidden[project]...), nil
}
func (f *fakeRepo) SaveSettings(_ context.Context, project string, digits *int, def, version string, hidden []string) error {
	f.projects[project].OwnDigits, f.projects[project].DefaultTypeID = digits, def
	if version != "" {
		f.hidden[project] = hidden
	}
	return nil
}
func (f *fakeRepo) refOf(s *Stored) RefRow {
	p := f.projects[s.ProjectID]
	d := f.orgDigits
	if p.OwnDigits != nil {
		d = *p.OwnDigits
	}
	return RefRow{ID: s.ID, ProjectID: s.ProjectID, OrgID: p.OrgID, Key: p.Key, Number: s.Number, Digits: d, Title: s.Title, Status: s.Status,
		TypeID: s.TypeID, ParentID: s.ParentID, AssigneeID: s.AssigneeID, AssigneeName: s.AssigneeName,
		CycleID: s.CycleID, Estimate: s.Estimate, CycleNumber: f.cycleNumber(s.CycleID)}
}

func (f *fakeRepo) cycleNumber(id string) int {
	if c := f.cycle(id); c != nil {
		return c.Number
	}
	return 0
}
func (f *fakeRepo) IssueRef(_ context.Context, id string) (*RefRow, error) {
	s := f.issue(id)
	if s == nil {
		return nil, ErrNotFound
	}
	r := f.refOf(s)
	return &r, nil
}
func (f *fakeRepo) IssueByKey(_ context.Context, org, key string, n int) (string, error) {
	for _, s := range f.issues {
		p := f.projects[s.ProjectID]
		if p.OrgID == org && s.Number == n && (p.Key == key || f.aliases[key] == p.ID) {
			return s.ID, nil
		}
	}
	return "", ErrIssueNotFound
}
func (f *fakeRepo) Children(_ context.Context, id string) ([]RefRow, error) {
	var out []RefRow
	for _, s := range f.issues {
		if s.ParentID == id {
			out = append(out, f.refOf(s))
		}
	}
	return out, nil
}
func (f *fakeRepo) ChildCounts(ctx context.Context, id string) (Progress, error) {
	kids, _ := f.Children(ctx, id)
	p := Progress{Total: len(kids)}
	for _, k := range kids {
		if k.Status == StatusShipped {
			p.Shipped++
		}
		if k.Status == StatusShipped || k.Status == StatusCancelled {
			p.Done++
		}
	}
	return p, nil
}
func (f *fakeRepo) Relations(_ context.Context, id string) ([]RelationRow, error) {
	var out []RelationRow
	for _, r := range f.relations {
		if r.from == id || r.to == id {
			other := r.to
			if r.to == id {
				other = r.from
			}
			out = append(out, RelationRow{ID: r.id, Kind: r.kind, Outward: r.from == id, Other: f.refOf(f.issue(other))})
		}
	}
	return out, nil
}
func (f *fakeRepo) AddRelation(_ context.Context, from, to, kind, _ string) (bool, error) {
	for _, r := range f.relations {
		if r.kind == kind && (r.from == from && r.to == to || kind == "relates" && r.from == to && r.to == from) {
			return false, nil
		}
	}
	f.relations = append(f.relations, fakeRelation{id: "r" + strconv.Itoa(len(f.relations)+1), from: from, to: to, kind: kind})
	return true, nil
}
func (f *fakeRepo) GetRelation(_ context.Context, issue, id string) (*RelationRow, error) {
	for _, r := range f.relations {
		if r.id == id && (r.from == issue || r.to == issue) {
			other := r.to
			if r.to == issue {
				other = r.from
			}
			return &RelationRow{ID: r.id, Kind: r.kind, Outward: r.from == issue, Other: RefRow{ID: other}}, nil
		}
	}
	return nil, ErrNotFound
}
func (f *fakeRepo) DeleteRelation(_ context.Context, id string) error {
	f.relations = slices.DeleteFunc(f.relations, func(r fakeRelation) bool { return r.id == id })
	return nil
}
func (f *fakeRepo) BlockedIssues(_ context.Context, blocker string) ([]string, error) {
	var out []string
	for _, r := range f.relations {
		if r.from == blocker && r.kind == "blocks" && !slices.Contains([]string{StatusShipped, StatusCancelled}, f.issue(r.to).Status) {
			out = append(out, r.to)
		}
	}
	return out, nil
}
func (f *fakeRepo) CopyLabels(_ context.Context, src, dst string) error {
	f.issue(dst).Labels = append([]string{}, f.issue(src).Labels...)
	return nil
}
func (f *fakeRepo) CopyRelations(ctx context.Context, src, dst, actor string) error {
	for _, r := range append([]fakeRelation{}, f.relations...) {
		if r.kind == "clones" {
			continue
		}
		if r.from == src {
			f.AddRelation(ctx, dst, r.to, r.kind, actor)
		} else if r.to == src {
			f.AddRelation(ctx, r.from, dst, r.kind, actor)
		}
	}
	return nil
}
func (f *fakeRepo) OrgKeys(context.Context, string) ([]string, error) {
	var out []string
	for _, p := range f.projects {
		if p.Key != "" {
			out = append(out, p.Key)
		}
	}
	return out, nil
}
func (f *fakeRepo) SetKey(_ context.Context, id, key string) (bool, error) {
	if f.projects[id].Key != "" {
		return false, nil
	}
	f.projects[id].Key = key
	return true, nil
}
func (f *fakeRepo) MemberRole(_ context.Context, _, user string) (string, error) {
	return f.roles[user], nil
}

func (f *fakeRepo) Create(_ context.Context, in NewIssue) (int, error) {
	n := 1
	for _, s := range f.issues {
		if s.ProjectID == in.ProjectID {
			n++
		}
	}
	at := f.tick()
	s := &Stored{ID: "i" + strconv.Itoa(len(f.issues)+1), ProjectID: in.ProjectID, Number: n, Title: in.Title, Description: in.Description,
		TypeID: in.TypeID, ParentID: in.ParentID,
		Status: in.Status, Priority: in.Priority, AssigneeID: in.AssigneeID, AssigneeName: in.AssigneeID, CreatedByID: in.CreatedBy,
		CreatedByName: in.CreatedBy, CreatedAt: at, UpdatedAt: at, StatusAt: at, Labels: append([]string{}, in.Labels...),
		CycleID: in.CycleID, StartOn: in.StartOn, DueOn: in.DueOn, Estimate: in.Estimate}
	f.issues = append(f.issues, s)
	f.events = append(f.events, Event{IssueID: s.ID, Kind: "opened", ActorID: in.CreatedBy})
	return n, nil
}
func (f *fakeRepo) Get(_ context.Context, projectID string, n int) (*Stored, error) {
	for _, s := range f.issues {
		if s.ProjectID == projectID && s.Number == n {
			c := *s
			f.fillCycle(&c)
			for _, k := range f.issues {
				if k.ParentID == s.ID {
					c.ChildrenPoints += intOr0(k.Estimate)
				}
			}
			c.Comments = 0
			for _, owner := range f.cOwner {
				if owner == s.ID {
					c.Comments++
				}
			}
			return &c, nil
		}
	}
	return nil, ErrNotFound
}
func (f *fakeRepo) Update(_ context.Context, s *Stored, _ bool, _ string, events []Event) error {
	c := *s
	c.UpdatedAt = f.tick()
	for i, old := range f.issues {
		if old.ID == s.ID {
			f.issues[i] = &c
		}
	}
	f.events = append(f.events, events...)
	return nil
}
func (f *fakeRepo) List(_ context.Context, q ListQuery) ([]Row, error) {
	var out []Row
	for i := len(f.issues) - 1; i >= 0; i-- {
		s := f.issues[i]
		if s.ProjectID != q.ProjectID || len(q.Statuses) > 0 && !slices.Contains(q.Statuses, s.Status) ||
			q.Assignee == "none" && s.AssigneeID != "" || q.Assignee != "" && q.Assignee != "none" && q.Assignee != s.AssigneeID {
			continue
		}
		r := Row{ID: s.ID, Number: s.Number, Title: s.Title, Status: s.Status, Priority: s.Priority, AssigneeID: s.AssigneeID,
			AssigneeName: s.AssigneeName, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Labels: s.Labels, SortAt: s.UpdatedAt}
		for _, l := range f.links {
			if l.issueID == s.ID && l.branch != "" {
				r.Branch = l.branch
			}
		}
		if q.CycleID != "" && s.CycleID != q.CycleID || q.NoCycle && s.CycleID != "" {
			continue
		}
		cs := *s
		f.fillCycle(&cs)
		r.CycleID, r.CycleNumber, r.CycleName, r.CycleStarts, r.CycleEnds = cs.CycleID, cs.CycleNumber, cs.CycleName, cs.CycleStarts, cs.CycleEnds
		r.StartOn, r.DueOn, r.Estimate = s.StartOn, s.DueOn, s.Estimate
		r.TypeID = s.TypeID
		if s.ParentID != "" {
			pr := f.refOf(f.issue(s.ParentID))
			r.ParentKey, r.ParentTitle = KeyOf(pr.Key, pr.Number, pr.Digits), pr.Title
		}
		c, _ := f.ChildCounts(context.Background(), s.ID)
		r.Children, r.ChildrenShipped, r.ChildrenDone = c.Total, c.Shipped, c.Done
		for _, rel := range f.relations {
			if rel.to == s.ID && rel.kind == "blocks" {
				b := f.refOf(f.issue(rel.from))
				if b.Status != StatusShipped && b.Status != StatusCancelled {
					r.BlockedBy = append(r.BlockedBy, KeyOf(b.Key, b.Number, b.Digits))
				}
			}
		}
		out = append(out, r)
	}
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}
func (f *fakeRepo) Count(_ context.Context, q ListQuery) (map[string]int, error) {
	out := map[string]int{}
	for _, st := range statuses {
		out[st] = 0
	}
	for _, s := range f.issues {
		if s.ProjectID == q.ProjectID && (q.CycleID == "" || s.CycleID == q.CycleID) && (!q.NoCycle || s.CycleID == "") {
			out[s.Status]++
		}
	}
	return out, nil
}
func (f *fakeRepo) CheckStates(context.Context, []string) ([]CheckState, error) { return nil, nil }

func (f *fakeRepo) Timeline(_ context.Context, issueID string, _ time.Time, _ string, limit int) ([]StoredItem, error) {
	var out []StoredItem
	for i, e := range f.events {
		if e.IssueID == issueID {
			out = append(out, StoredItem{Item: "event", ID: strconv.Itoa(i), ActorID: e.ActorID, ActorName: e.ActorID, Kind: e.Kind,
				Automatic: e.Automatic, From: e.From, To: e.To, Branch: e.Branch, CreatedAt: f.now})
		}
	}
	for _, c := range f.comments {
		if f.cOwner[c.ID] == issueID {
			out = append(out, StoredItem{Item: "comment", ID: c.ID, ActorID: c.AuthorID, ActorName: c.AuthorName, Body: c.Body,
				CreatedAt: c.CreatedAt, EditedAt: c.EditedAt})
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (f *fakeRepo) AddComment(_ context.Context, issueID, author, body string) (*StoredComment, error) {
	c := &StoredComment{ID: "c" + strconv.Itoa(len(f.comments)+1), AuthorID: author, AuthorName: author, Body: body, CreatedAt: f.tick()}
	f.comments = append(f.comments, c)
	f.cOwner[c.ID] = issueID
	return c, nil
}
func (f *fakeRepo) GetComment(_ context.Context, issueID, id string) (*StoredComment, error) {
	for _, c := range f.comments {
		if c.ID == id && f.cOwner[id] == issueID {
			cc := *c
			return &cc, nil
		}
	}
	return nil, ErrNotFound
}
func (f *fakeRepo) EditComment(_ context.Context, id, body string) error {
	for _, c := range f.comments {
		if c.ID == id {
			at := f.tick()
			c.Body, c.EditedAt = body, &at
		}
	}
	return nil
}
func (f *fakeRepo) DeleteComment(_ context.Context, id string) error {
	delete(f.cOwner, id)
	return nil
}
func (f *fakeRepo) Labels(_ context.Context, projectID string) ([]Label, error) {
	count := map[string]int{}
	for _, s := range f.issues {
		if s.ProjectID == projectID {
			for _, l := range s.Labels {
				count[l]++
			}
		}
	}
	out := []Label{}
	for n, c := range count {
		out = append(out, Label{Name: n, Open: c, Total: c})
	}
	return out, nil
}
func (f *fakeRepo) ServiceNames(context.Context, string) ([]string, error) { return f.names, nil }
func (f *fakeRepo) Views(_ context.Context, projectID string) ([]StoredView, error) {
	out := []StoredView{}
	for _, v := range f.views {
		if f.vProject[v.ID] == projectID {
			out = append(out, *v)
		}
	}
	return out, nil
}
func (f *fakeRepo) GetView(_ context.Context, projectID, id string) (*StoredView, error) {
	for _, v := range f.views {
		if v.ID == id && f.vProject[id] == projectID {
			c := *v
			return &c, nil
		}
	}
	return nil, ErrNotFound
}
func (f *fakeRepo) AddView(_ context.Context, projectID, owner, name string, filters []byte) (string, error) {
	for _, v := range f.views {
		if v.Name == name && f.vProject[v.ID] == projectID {
			return "", ErrNameTaken
		}
	}
	v := &StoredView{ID: "v" + strconv.Itoa(len(f.views)+1), Name: name, OwnerID: owner, OwnerName: owner, Filters: filters, CreatedAt: f.tick(), UpdatedAt: f.now}
	f.views = append(f.views, v)
	f.vProject[v.ID] = projectID
	return v.ID, nil
}
func (f *fakeRepo) UpdateView(_ context.Context, id, name string, filters []byte) error {
	for _, v := range f.views {
		if v.ID == id {
			v.Name, v.Filters = name, filters
		}
	}
	return nil
}
func (f *fakeRepo) DeleteView(_ context.Context, id string) error {
	delete(f.vProject, id)
	return nil
}
func (f *fakeRepo) Links(_ context.Context, issueID string) ([]StoredLink, error) {
	var out []StoredLink
	for _, l := range f.links {
		if l.issueID != issueID {
			continue
		}
		sl := StoredLink{RepositoryID: l.repoID, Repository: "storefront", Branch: l.branch, PullID: l.pullID}
		if p := f.pulls[l.pullID]; p != nil {
			sl.PullNumber, sl.PullTitle, sl.PullState, sl.Base, sl.MergeSHA = p.number, p.title, p.state, p.base, p.mergeSHA
			if p.state == "merged" {
				sl.MergedAt = &p.mergedAt
			}
		}
		out = append(out, sl)
	}
	return out, nil
}
func (f *fakeRepo) BranchTargets(_ context.Context, repo, branch string) ([]Target, error) {
	return f.targets[repo+" "+branch], nil
}
func (f *fakeRepo) LatestSince(context.Context, []string, time.Time) (map[string]StoredDeploy, error) {
	return map[string]StoredDeploy{}, nil
}
func (f *fakeRepo) Unapplied(_ context.Context, n int) ([]GitEvent, error) {
	var out []GitEvent
	for _, e := range f.git {
		if !f.applied[e.ID] && len(out) < n {
			out = append(out, e)
		}
	}
	return out, nil
}
func (f *fakeRepo) MarkApplied(_ context.Context, id int64) error { f.applied[id] = true; return nil }
func (f *fakeRepo) ByNumbers(_ context.Context, projectID string, numbers []int) ([]Candidate, error) {
	var out []Candidate
	for _, s := range f.issues {
		if s.ProjectID == projectID && slices.Contains(numbers, s.Number) {
			out = append(out, Candidate{ID: s.ID, Number: s.Number, Status: s.Status, CreatedAt: s.CreatedAt})
		}
	}
	return out, nil
}
func (f *fakeRepo) ForPull(_ context.Context, pullID string) ([]Candidate, error) {
	var out []Candidate
	for _, l := range f.links {
		if l.pullID == pullID {
			s := f.issue(l.issueID)
			out = append(out, Candidate{ID: s.ID, Number: s.Number, Status: s.Status, CreatedAt: s.CreatedAt})
		}
	}
	return out, nil
}
func (f *fakeRepo) link(l fakeLink) bool {
	if slices.Contains(f.links, l) {
		return false
	}
	f.links = append(f.links, l)
	return true
}
func (f *fakeRepo) LinkBranch(_ context.Context, issueID, repoID, branch string) (bool, error) {
	return f.link(fakeLink{issueID: issueID, repoID: repoID, branch: branch}), nil
}
func (f *fakeRepo) LinkPull(_ context.Context, issueID, repoID, pullID string) (bool, error) {
	return f.link(fakeLink{issueID: issueID, repoID: repoID, pullID: pullID}), nil
}
func (f *fakeRepo) recorded(e Event) bool {
	if e.GitEventID == 0 {
		return false
	}
	for _, old := range f.events {
		if old.IssueID == e.IssueID && old.GitEventID == e.GitEventID && old.Kind == e.Kind {
			return true
		}
	}
	return false
}
func (f *fakeRepo) Move(_ context.Context, m Move) (bool, error) {
	s := f.issue(m.Event.IssueID)
	if !slices.Contains(m.From, s.Status) {
		return false, nil
	}
	e := m.Event
	e.Kind, e.From, e.To = "status", s.Status, m.To
	if f.recorded(e) {
		return false, nil
	}
	f.events = append(f.events, e)
	s.Status = m.To
	return true, nil
}
func (f *fakeRepo) Record(_ context.Context, e Event) (bool, error) {
	if f.recorded(e) {
		return false, nil
	}
	f.events = append(f.events, e)
	return true, nil
}
func (f *fakeRepo) OtherOpenPulls(_ context.Context, issueID, pullID string) (int, error) {
	n := 0
	for _, l := range f.links {
		if l.issueID == issueID && l.pullID != "" && l.pullID != pullID && f.pulls[l.pullID].state == "open" {
			n++
		}
	}
	return n, nil
}
func (f *fakeRepo) ShipCandidates(_ context.Context, repoID string) ([]ShipCandidate, error) {
	var out []ShipCandidate
	for _, l := range f.links {
		p, s := f.pulls[l.pullID], f.issue(l.issueID)
		if p == nil || p.state != "merged" || !slices.Contains(fromForShipped, s.Status) || repoID != "" && p.repoID != repoID {
			continue
		}
		open, _ := f.OtherOpenPulls(context.Background(), s.ID, p.id)
		out = append(out, ShipCandidate{IssueID: s.ID, Status: s.Status, PullID: p.id, RepositoryID: p.repoID, Base: p.base,
			PullNumber: p.number, MergeSHA: p.mergeSHA, MergedBy: p.mergedBy, MergedAt: p.mergedAt, HasOpen: open > 0})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].IssueID < out[j].IssueID })
	return out, nil
}
func (f *fakeRepo) ServiceRepository(_ context.Context, id string) (string, error) {
	return f.services[id], nil
}
func (f *fakeRepo) PullIssues(context.Context, []string) (map[string][]code.IssueRef, error) {
	return map[string][]code.IssueRef{}, nil
}
func (f *fakeRepo) BranchIssues(context.Context, string, []string) (map[string][]code.IssueRef, error) {
	return map[string][]code.IssueRef{}, nil
}
func (f *fakeRepo) SearchIssues(_ context.Context, q SearchQuery) ([]SearchIssue, error) {
	out := []SearchIssue{}
	for _, s := range f.issues {
		p := f.projects[s.ProjectID]
		if p.OrgID == q.OrgID && (!q.HasQ || s.Number == q.QNumber || q.TSQ != "" && strings.Contains(strings.ToLower(s.Title), strings.TrimSuffix(q.TSQ, ":*")) ||
			q.QPrefix != "" && (p.Key == q.QPrefix || f.aliases[q.QPrefix] == p.ID)) {
			out = append(out, SearchIssue{ProjectID: p.ID, Project: p.Name, Key: KeyOf(p.Key, s.Number, 0), Number: s.Number, Title: s.Title, Status: s.Status, Priority: s.Priority})
		}
	}
	return out, nil
}
func (f *fakeRepo) SearchPulls(context.Context, SearchQuery) ([]SearchPull, error) {
	return []SearchPull{}, nil
}

// fakeCode: every branch exists; a commit holds those listed for it.
type fakeCode struct{ holds map[string][]string }

func (fakeCode) BranchExists(context.Context, string, string) bool { return true }
func (c fakeCode) CommitContains(_ context.Context, _, ancestor, descendant string) (bool, error) {
	return ancestor == descendant || slices.Contains(c.holds[descendant], ancestor), nil
}

type projectsIn map[string]string

func (p projectsIn) CheckOwnership(_ context.Context, projectID, orgID string) (bool, error) {
	return p[projectID] == orgID, nil
}

// ─── the API ─────────────────────────────────────────────────────────────────

// TestIssuesAPI works issues through the API (D-81): the first one sets
// the project's key, the list and its counts, changes with their history,
// comments, labels with services, saved views and ⌘K's search, and what
// viewers and other authors are refused; every answer is checked against
// openapi.yaml.
func TestIssuesAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := newFake()
	svc := NewService(repo, fakeCode{}, zap.NewNop())
	router := func(user, role string) http.Handler {
		r := gin.New()
		NewHandler(svc, projectsIn{"p1": "o1", "p2": "o1"}, zap.NewNop()).RegisterRoutes(r.Group("/api/v1", func(c *gin.Context) {
			c.Set("userID", user)
			c.Set("orgID", "o1")
			c.Set("memberRole", role)
			c.Next()
		}))
		return r
	}
	dev, own, viewer := router("dev", "developer"), router("own", "owner"), router("eve", "viewer")
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
		if w.Body.Len() == 0 {
			apispec.Check(t, method, strings.SplitN(path, "?", 2)[0], w.Code, nil)
			return w.Code, out
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s %s: %v\n%s", method, path, err, w.Body)
		}
		apispec.Check(t, method, strings.SplitN(path, "?", 2)[0], w.Code, out)
		return w.Code, out
	}
	data := func(m map[string]any) map[string]any { d, _ := m["data"].(map[string]any); return d }
	base := "/api/v1/projects/p1"

	code, out := call(dev, "POST", base+"/issues", map[string]any{"title": "  Rate-limit login attempts ", "priority": "high",
		"labels": []string{"API", "web"}, "assignee_id": "dev", "description": "Closes STO-9 and !4"})
	if code != 201 || data(out)["key"] != "STO-1" || data(out)["title"] != "Rate-limit login attempts" || data(out)["status"] != "backlog" {
		t.Fatalf("create: %d %v", code, out)
	}
	if html, _ := data(out)["html"].(string); !strings.Contains(html, `data-toft-issue="9"`) {
		t.Errorf("description's keys aren't linked: %s", html)
	}
	if repo.projects["p1"].Key != "STO" {
		t.Errorf("project key %q", repo.projects["p1"].Key)
	}
	call(dev, "POST", base+"/issues", map[string]any{"title": "Search ranks out-of-stock items first", "status": "todo"})
	if code, out = call(dev, "POST", "/api/v1/projects/p2/issues", map[string]any{"title": "Ops"}); data(out)["key"] != "SO-1" {
		t.Errorf("second project's key: %v", out) // STO is taken, so its initials
	}
	for _, bad := range []map[string]any{
		{"title": ""}, {"title": "x", "status": "done"}, {"title": "x", "priority": "p0"},
		{"title": "x", "labels": []string{"a,b"}}, {"title": "x", "assignee_id": "eve"},
	} {
		if code, _ := call(dev, "POST", base+"/issues", bad); code != 400 && code != 422 {
			t.Errorf("%v: %d", bad, code)
		}
	}
	if code, _ := call(viewer, "POST", base+"/issues", map[string]any{"title": "x"}); code != 403 {
		t.Errorf("viewer opened an issue: %d", code)
	}

	code, out = call(viewer, "GET", base+"/issues?status=backlog,todo&order=updated", nil)
	if items, _ := out["data"].([]any); code != 200 || len(items) != 2 {
		t.Fatalf("list: %d %v", code, out)
	}
	if code, _ := call(viewer, "GET", base+"/issues?status=done", nil); code != 400 {
		t.Errorf("a bad status filter: %d", code)
	}
	if _, out = call(viewer, "GET", base+"/issue-counts", nil); data(out)["todo"] != float64(1) || data(out)["shipped"] != float64(0) {
		t.Errorf("counts: %v", out)
	}

	code, out = call(dev, "PATCH", base+"/issues/1", map[string]any{"status": "todo", "assignee_id": "own", "labels": []string{"api"}, "version": "v"})
	if code != 200 || data(out)["status"] != "todo" || data(out)["assignee"].(map[string]any)["id"] != "own" {
		t.Fatalf("update: %d %v", code, out)
	}
	if code, out = call(dev, "PATCH", base+"/issues/1", map[string]any{"assignee_id": ""}); data(out)["assignee"] != nil {
		t.Errorf("unassign: %d %v", code, out)
	}
	if code, _ := call(dev, "PATCH", base+"/issues/99", map[string]any{"title": "x"}); code != 404 {
		t.Errorf("a missing issue: %d", code)
	}

	code, out = call(dev, "POST", base+"/issues/1/comments", map[string]any{"body": "See STO-2"})
	if code != 201 || !strings.Contains(data(out)["html"].(string), `data-toft-issue="2"`) {
		t.Fatalf("comment: %d %v", code, out)
	}
	cid := data(out)["id"].(string)
	if code, _ := call(own, "PATCH", base+"/issues/1/comments/"+cid, map[string]any{"body": "mine now"}); code != 403 {
		t.Errorf("someone else edited the comment: %d", code)
	}
	if code, _ := call(dev, "PATCH", base+"/issues/1/comments/"+cid, map[string]any{"body": "edited"}); code != 200 {
		t.Errorf("edit: %d", code)
	}
	code, out = call(viewer, "GET", base+"/issues/1/timeline", nil)
	kinds := []string{}
	for _, it := range out["data"].([]any) {
		m := it.(map[string]any)
		kinds = append(kinds, m["type"].(string)+":"+func() string { k, _ := m["kind"].(string); return k }())
	}
	if code != 200 || strings.Join(kinds, " ") != "event:opened event:status event:assigned event:labels event:assigned comment:" {
		t.Errorf("timeline: %d %v", code, kinds)
	}
	if code, _ := call(own, "DELETE", base+"/issues/1/comments/"+cid, nil); code != 204 {
		t.Errorf("an owner deletes a comment: %d", code)
	}

	code, out = call(viewer, "GET", base+"/issue-labels", nil)
	labels := map[string]bool{}
	for _, l := range out["data"].([]any) {
		labels[l.(map[string]any)["name"].(string)] = l.(map[string]any)["service"].(bool)
	}
	if code != 200 || !labels["api"] || !labels["web"] || len(labels) != 2 {
		t.Errorf("labels: %v", labels)
	}

	view := map[string]any{"name": "Payments", "filters": map[string]any{"label": []string{"payments"}, "status": []string{"todo"}}}
	code, out = call(dev, "POST", base+"/issue-views", view)
	if code != 201 || data(out)["owner"].(map[string]any)["id"] != "dev" {
		t.Fatalf("save a view: %d %v", code, out)
	}
	vid := data(out)["id"].(string)
	if code, _ := call(dev, "POST", base+"/issue-views", view); code != 409 {
		t.Errorf("a taken name: %d", code)
	}
	if code, _ := call(dev, "POST", base+"/issue-views", map[string]any{"name": "x", "filters": map[string]any{"order": "random"}}); code != 400 {
		t.Errorf("bad filters: %d", code)
	}
	if code, _ := call(viewer, "POST", base+"/issue-views", view); code != 403 {
		t.Errorf("viewer saved a view: %d", code)
	}
	view["name"] = "Payments team"
	if code, _ := call(own, "PATCH", base+"/issue-views/"+vid, view); code != 200 {
		t.Errorf("a workspace owner renames a view: %d", code)
	}
	if _, out = call(viewer, "GET", base+"/issue-views", nil); len(out["data"].([]any)) != 1 {
		t.Errorf("views: %v", out)
	}
	if code, _ := call(dev, "DELETE", base+"/issue-views/"+vid, nil); code != 204 {
		t.Errorf("delete a view: %d", code)
	}

	code, out = call(viewer, "GET", "/api/v1/workspace/search?q=rate", nil)
	if found := data(out)["issues"].([]any); code != 200 || len(found) != 1 || found[0].(map[string]any)["key"] != "STO-1" {
		t.Errorf("search: %d %v", code, out)
	}
	if _, out = call(viewer, "GET", "/api/v1/workspace/search?q=STO-2&types=issues", nil); len(data(out)["issues"].([]any)) != 1 {
		t.Errorf("search by key: %v", out)
	}
	if _, out = call(viewer, "GET", "/api/v1/workspace/search?q=sto&types=issues", nil); len(data(out)["issues"].([]any)) < 2 {
		t.Errorf("search by the key alone: %v", out)
	}
}

// ─── the mover ───────────────────────────────────────────────────────────────

// TestEventsMoveIssues follows D-81's rules through the outbox's events
// and the deploys: a pushed branch, a pull request opened, closed and
// reopened, a merge that ships when every service it deploys to runs it,
// one that deploys nowhere, a hand move undone by the next event, an issue
// opened after the event, and every event applied twice.
func TestEventsMoveIssues(t *testing.T) {
	repo := newFake()
	c := fakeCode{holds: map[string][]string{}}
	svc := NewService(repo, c, zap.NewNop())
	ctx := context.Background()
	open := func(title string) string {
		d, err := svc.Create(ctx, "p1", "dev", CreateInput{Title: title})
		if err != nil {
			t.Fatal(err)
		}
		return repo.issues[d.Number-1].ID
	}
	status := func(id string) string { return repo.issue(id).Status }
	var nextID int64
	event := func(e GitEvent) {
		nextID++
		e.ID, e.ProjectID, e.RepositoryID, e.CreatedAt = nextID, "p1", "r1", repo.tick()
		repo.git = append(repo.git, e)
		svc.ApplyEvents(ctx)
	}
	one, two, three := open("Webhook retries"), open("Restore drill"), open("Docs only")

	event(GitEvent{Kind: "push", Branch: "fix/STO-1-webhook-idem", AfterSHA: "a1", ActorID: "dev"})
	if status(one) != StatusInProgress || status(two) != StatusBacklog {
		t.Fatalf("a pushed branch: %s %s", status(one), status(two))
	}
	// Moved back by hand, the next push moves it again.
	if _, err := svc.Update(ctx, "p1", 1, "own", UpdateInput{Status: ptr(StatusTodo)}); err != nil {
		t.Fatal(err)
	}
	event(GitEvent{Kind: "push", Branch: "fix/sto-1-webhook-idem", AfterSHA: "a2"})
	if status(one) != StatusInProgress {
		t.Fatalf("the next push: %s", status(one))
	}

	repo.pulls["pr1"] = &fakePull{id: "pr1", repoID: "r1", number: 41, title: "Idempotent webhooks", state: "open", base: "main"}
	event(GitEvent{Kind: "pull_request", Action: "opened", PullID: "pr1", PullNumber: 41, PullTitle: "Idempotent webhooks",
		PullBody: "Closes STO-1, and sto-2.", PullHead: "fix/sto-1-webhook-idem"})
	if status(one) != StatusInReview || status(two) != StatusInReview {
		t.Fatalf("a pull request opened: %s %s", status(one), status(two))
	}
	repo.pulls["pr1"].state = "closed"
	event(GitEvent{Kind: "pull_request", Action: "closed", PullID: "pr1"})
	if status(one) != StatusInProgress {
		t.Fatalf("closed unmerged: %s", status(one))
	}
	repo.pulls["pr1"].state = "open"
	event(GitEvent{Kind: "pull_request", Action: "reopened", PullID: "pr1", PullTitle: "Idempotent webhooks", PullBody: "Closes STO-1, and sto-2."})
	if status(one) != StatusInReview {
		t.Fatalf("reopened: %s", status(one))
	}

	// The merge deploys to api and web: shipped once both run it.
	repo.targets["r1 main"] = []Target{{ServiceID: "s-api", Service: "api", LiveSHA: "old"}, {ServiceID: "s-web", Service: "web", LiveSHA: "old"}}
	repo.services["s-api"], repo.services["s-web"] = "r1", "r1"
	p := repo.pulls["pr1"]
	p.state, p.mergeSHA, p.mergedBy, p.mergedAt = "merged", "m1", "dev", repo.tick()
	event(GitEvent{Kind: "merge", Branch: "main", AfterSHA: "m1", PullID: "pr1", PullTitle: p.title, PullBody: "Closes STO-1, and sto-2."})
	if status(one) != StatusInReview {
		t.Fatalf("merged, nothing live yet: %s", status(one))
	}
	repo.targets["r1 main"][0].LiveSHA, repo.targets["r1 main"][0].LiveDeploymentID = "m1", "d-api"
	svc.DeployLive(ctx, "s-api")
	if status(one) != StatusInReview {
		t.Fatalf("live on api only: %s", status(one))
	}
	repo.targets["r1 main"][1].LiveSHA, repo.targets["r1 main"][1].LiveDeploymentID = "m2", "d-web"
	c.holds["m2"] = []string{"m1"} // web runs a later commit holding the merge
	svc.DeployLive(ctx, "s-web")
	if status(one) != StatusShipped || status(two) != StatusShipped {
		t.Fatalf("live on both: %s %s", status(one), status(two))
	}

	// A merge into a branch nothing deploys from ships at the merge.
	repo.pulls["pr2"] = &fakePull{id: "pr2", repoID: "r1", number: 42, title: "Docs STO-3", state: "merged", base: "docs", mergeSHA: "d1", mergedAt: repo.tick()}
	event(GitEvent{Kind: "pull_request", Action: "opened", PullID: "pr2", PullTitle: "Docs STO-3"})
	event(GitEvent{Kind: "merge", Branch: "docs", AfterSHA: "d1", PullID: "pr2", PullTitle: "Docs STO-3"})
	if status(three) != StatusShipped {
		t.Fatalf("merged where nothing deploys: %s", status(three))
	}

	// An event applied late (the backend was down) doesn't move an issue
	// opened after it happened.
	nextID++
	repo.git = append(repo.git, GitEvent{ID: nextID, ProjectID: "p1", RepositoryID: "r1", Kind: "push", Branch: "fix/sto-4", AfterSHA: "e1", CreatedAt: repo.tick()})
	four := open("Opened after the push")
	svc.ApplyEvents(ctx)
	if status(four) != StatusBacklog || !repo.applied[nextID] {
		t.Fatalf("an issue opened after the push moved: %s", status(four))
	}

	// Every event applied again adds nothing.
	before := len(repo.events)
	repo.applied = map[int64]bool{}
	svc.ApplyEvents(ctx)
	if len(repo.events) != before {
		t.Errorf("replaying the events added %d history rows", len(repo.events)-before)
	}
	if n := countAuto(repo.events, one); n != 7 {
		t.Errorf("STO-1's automatic history: %d rows", n)
	}
}

func countAuto(events []Event, issueID string) int {
	n := 0
	for _, e := range events {
		if e.IssueID == issueID && e.Automatic {
			n++
		}
	}
	return n
}

func ptr[T any](v T) *T { return &v }

func TestKeysAndNumbers(t *testing.T) {
	for name, want := range map[string]string{"storefront": "STO,STOR,STORE", "billing-api": "BIL,BA", "ui": "UI", "3d": "DX", "--": "PRJ"} {
		got := strings.Join(keyCandidates(name)[:len(strings.Split(want, ","))], ",")
		if got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
	if got := numbersIn([]string{"STO", "OLD"}, "fix/sto-118-idem", "old-7", "Closes STO-104 and STO-0104, not XSTO-5 or STO-x", "chore/sto-97"); !slices.Equal(got, []int{118, 7, 104, 97}) {
		t.Errorf("numbers: %v", got)
	}
}

// TestDeeperIssues works D-83 through the API: types, a project's key and
// digits with the old key kept, epics with their children and progress, a
// sub-task needing a parent, a cycle refused, blocks and the history line
// when the blocker ships, an epic shipping with its children, a duplicate
// cancelled, a clone keeping labels and relations, and owners alone
// changing types and keys; every answer checked against openapi.yaml.
func TestDeeperIssues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := newFake()
	svc := NewService(repo, fakeCode{}, zap.NewNop())
	router := func(user, role string) http.Handler {
		r := gin.New()
		NewHandler(svc, projectsIn{"p1": "o1", "p2": "o1"}, zap.NewNop()).RegisterRoutes(r.Group("/api/v1", func(c *gin.Context) {
			c.Set("userID", user)
			c.Set("orgID", "o1")
			c.Set("memberRole", role)
			c.Next()
		}))
		return r
	}
	dev, own := router("dev", "developer"), router("own", "owner")
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
		if w.Body.Len() == 0 {
			apispec.Check(t, method, strings.SplitN(path, "?", 2)[0], w.Code, nil)
			return w.Code, out
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s %s: %v\n%s", method, path, err, w.Body)
		}
		apispec.Check(t, method, strings.SplitN(path, "?", 2)[0], w.Code, out)
		return w.Code, out
	}
	data := func(m map[string]any) map[string]any { d, _ := m["data"].(map[string]any); return d }
	base := "/api/v1/projects/p1"

	_, out := call(dev, "GET", "/api/v1/workspace/issue-types", nil)
	typeID := map[string]string{}
	for _, x := range out["data"].([]any) {
		m := x.(map[string]any)
		typeID[m["name"].(string)] = m["id"].(string)
	}
	if len(typeID) != 6 || typeID["Epic"] == "" || typeID["Sub-task"] == "" {
		t.Fatalf("default types: %v", typeID)
	}
	if code, _ := call(dev, "POST", "/api/v1/workspace/issue-types", map[string]any{"name": "Incident", "kind": "standard", "icon": "incident", "color": "red"}); code != 403 {
		t.Errorf("a developer added a type: %d", code)
	}
	if code, _ := call(own, "POST", "/api/v1/workspace/issue-types", map[string]any{"name": "Incident", "kind": "standard", "icon": "incident", "color": "red"}); code != 201 {
		t.Errorf("an owner adds a type: %d", code)
	}
	if code, _ := call(own, "PATCH", "/api/v1/workspace/issue-settings", map[string]any{"digits": 3}); code != 200 {
		t.Errorf("workspace digits: %d", code)
	}
	if code, out := call(own, "PATCH", base+"/issue-settings", map[string]any{"key": "vlb"}); code != 200 || data(out)["next_key"] != "VLB-001" {
		t.Fatalf("project key: %d %v", code, out)
	}
	if code, _ := call(own, "PATCH", "/api/v1/projects/p2/issue-settings", map[string]any{"key": "VLB"}); code != 409 {
		t.Errorf("a taken key: %d", code)
	}

	open := func(body map[string]any) string {
		t.Helper()
		code, out := call(dev, "POST", base+"/issues", body)
		if code != 201 {
			t.Fatalf("open %v: %d %v", body, code, out)
		}
		return data(out)["key"].(string)
	}
	epic := open(map[string]any{"title": "Checkout v2", "type_id": typeID["Epic"]})
	if epic != "VLB-001" {
		t.Fatalf("key with 3 digits: %s", epic)
	}
	one := open(map[string]any{"title": "SEPA", "parent": epic, "labels": []string{"payments"}})
	two := open(map[string]any{"title": "Apple Pay", "parent": "vlb-1"})
	if code, _ := call(dev, "POST", base+"/issues", map[string]any{"title": "Orphan", "type_id": typeID["Sub-task"]}); code != 422 {
		t.Errorf("a sub-task without a parent: %d", code)
	}
	if code, _ := call(dev, "PATCH", base+"/issues/1", map[string]any{"parent": one}); code != 422 {
		t.Errorf("an epic under its own child: %d", code)
	}
	_, out = call(dev, "GET", base+"/issues/1", nil)
	if kids := data(out)["children"].([]any); len(kids) != 2 || data(out)["type"].(map[string]any)["kind"] != "container" {
		t.Fatalf("children: %v", data(out))
	}

	if code, _ := call(dev, "POST", base+"/issues/2/relations", map[string]any{"kind": "blocks", "issue": two}); code != 201 {
		t.Fatalf("blocks: %d", code)
	}
	if code, _ := call(dev, "POST", base+"/issues/3/relations", map[string]any{"kind": "blocked_by", "issue": one}); code != 409 {
		t.Errorf("the same relation again: %d", code)
	}
	_, out = call(dev, "GET", base+"/issues/3", nil)
	if b := data(out)["blocked_by"].([]any); len(b) != 1 || b[0] != one {
		t.Fatalf("blocked_by: %v", data(out)["blocked_by"])
	}
	_, out = call(dev, "GET", base+"/issues?status=backlog", nil)
	for _, x := range out["data"].([]any) {
		m := x.(map[string]any)
		if m["key"] == epic && m["progress"].(map[string]any)["total"] != float64(2) {
			t.Errorf("the epic's row: %v", m)
		}
		if m["key"] == two && (len(m["blocked_by"].([]any)) != 1 || m["parent"].(map[string]any)["key"] != epic) {
			t.Errorf("the blocked row: %v", m)
		}
	}

	// The key renamed: the old one still names its issues.
	if code, out := call(own, "PATCH", base+"/issue-settings", map[string]any{"key": "SHOP", "digits": nil}); code != 200 || data(out)["aliases"].([]any)[0] != "VLB" || data(out)["next_key"] != "SHOP-004" {
		t.Fatalf("rename: %d %v", code, out)
	}
	three := open(map[string]any{"title": "Pay by link"})
	if code, _ := call(dev, "POST", base+"/issues/4/relations", map[string]any{"kind": "duplicates", "issue": "VLB-2"}); code != 201 {
		t.Fatalf("duplicates by an old key: %d", code)
	}
	_, out = call(dev, "GET", base+"/issues/4", nil)
	if data(out)["status"] != "cancelled" || three != "SHOP-004" {
		t.Errorf("the duplicate: %s %v", three, data(out)["status"])
	}
	if st, c := call(dev, "POST", base+"/issues/4/comments", map[string]any{"body": "Same as VLB-2"}); st != 201 || !strings.Contains(data(c)["html"].(string), `data-toft-issue="2"`) {
		t.Errorf("a comment naming an old key: %d %v", st, c)
	}
	if keys := svc.IssueKeys(context.Background(), "p1"); !slices.Equal(keys, []string{"SHOP", "VLB"}) {
		t.Errorf("the keys pull requests link: %v", keys)
	}
	if _, out = call(dev, "GET", "/api/v1/workspace/search?q=VLB-&types=issues", nil); len(data(out)["issues"].([]any)) < 4 {
		t.Errorf("search by a former key alone: %v", out)
	}
	if _, _, key, n := parseQ("checkout-12"); key != "CHECKOUT" || n != 12 {
		t.Errorf("a long key: %s %d", key, n)
	}

	// The blocker ships: the blocked issue's history says so; both
	// children done ship the epic.
	call(dev, "PATCH", base+"/issues/2", map[string]any{"status": "shipped"})
	_, out = call(dev, "GET", base+"/issues/3/timeline", nil)
	unblocked := false
	for _, x := range out["data"].([]any) {
		unblocked = unblocked || x.(map[string]any)["kind"] == "unblocked"
	}
	if !unblocked {
		t.Errorf("no unblocked line: %v", out["data"])
	}
	call(dev, "PATCH", base+"/issues/3", map[string]any{"status": "cancelled"})
	if _, out = call(dev, "GET", base+"/issues/1", nil); data(out)["status"] != "shipped" {
		t.Errorf("the epic with its children done: %v", data(out)["status"])
	}

	code, out := call(dev, "POST", base+"/issues/2/clone", map[string]any{"project_id": "p2", "keep": []string{"labels", "relations"}})
	if code != 201 || data(out)["status"] != "backlog" || len(data(out)["labels"].([]any)) != 1 {
		t.Fatalf("clone: %d %v", code, out)
	}
	kinds := []string{}
	for _, r := range data(out)["relations"].([]any) {
		kinds = append(kinds, r.(map[string]any)["kind"].(string))
	}
	if !slices.Contains(kinds, "clones") || !slices.Contains(kinds, "blocks") {
		t.Errorf("the clone's relations: %v", kinds)
	}
	clone := fmt.Sprintf("/api/v1/projects/p2/issues/%v", data(out)["number"])
	_, out = call(dev, "GET", base+"/issues/3", nil)
	rel := data(out)["relations"].([]any)[0].(map[string]any)["id"].(string)
	if code, _ := call(dev, "DELETE", base+"/issues/3/relations/"+rel, nil); code != 204 {
		t.Errorf("unrelate: %d", code)
	}

	// A key scoped to p2 relates and parents within p2: p1's issues are
	// not found, so it can't cancel one as a duplicate.
	_, out = call(dev, "GET", base+"/issues/4", nil)
	before := len(data(out)["relations"].([]any))
	key := gin.New()
	NewHandler(svc, projectsIn{"p1": "o1", "p2": "o1"}, zap.NewNop()).RegisterRoutes(key.Group("/api/v1", func(c *gin.Context) {
		c.Set("userID", "dev")
		c.Set("orgID", "o1")
		c.Set("memberRole", "developer")
		c.Set("apiKey", true)
		c.Set("apiKeyProject", "p2")
		c.Next()
	}))
	if code, _ := call(key, "POST", clone+"/relations", map[string]any{"kind": "duplicated_by", "issue": three}); code != 404 {
		t.Errorf("a scoped key relates another project's issue: %d", code)
	}
	if code, _ := call(key, "PATCH", clone, map[string]any{"parent": epic}); code != 404 {
		t.Errorf("a scoped key parents under another project's issue: %d", code)
	}
	if code, _ := call(key, "POST", "/api/v1/projects/p2/issues", map[string]any{"title": "Refunds", "parent": epic}); code != 404 {
		t.Errorf("a scoped key opens under another project's issue: %d", code)
	}
	// It clones within its project only, and says why with a code.
	if code, out := call(key, "POST", clone+"/clone", map[string]any{"project_id": "p1"}); code != 403 || out["code"] != "insufficient_scope" || data(out)["code"] != "insufficient_scope" {
		t.Errorf("a scoped key clones into another project: %d %v", code, out)
	}
	// So does an agent connected to p2 alone (D-100).
	agent := gin.New()
	NewHandler(svc, projectsIn{"p1": "o1", "p2": "o1"}, zap.NewNop()).RegisterRoutes(agent.Group("/api/v1", func(c *gin.Context) {
		c.Set("userID", "dev")
		c.Set("orgID", "o1")
		c.Set("memberRole", "developer")
		c.Set("agent", middleware.AgentIdentity{ProjectIDs: []string{"p2"}})
		c.Next()
	}))
	if code, _ := call(agent, "POST", "/api/v1/projects/p2/issues", map[string]any{"title": "Refunds", "parent": epic}); code != 404 {
		t.Errorf("an agent opens under another project's issue: %d", code)
	}
	if _, out = call(dev, "GET", base+"/issues/4", nil); len(data(out)["relations"].([]any)) != before {
		t.Errorf("p1's issue was related: %v", data(out)["relations"])
	}
}

type mentionsOf []string

func (m mentionsOf) Handles(context.Context, string) []string { return m }

// TestFailedDeploys: an issue from a failed deploy, one per run of
// failures, shipped by the next deploy live; My issues; mentions (D-88).
func TestFailedDeploys(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := newFake()
	svc := NewService(repo, fakeCode{}, zap.NewNop()).WithMentions(mentionsOf{"ann"})
	router := func(user, role string) *gin.Engine {
		r := gin.New()
		NewHandler(svc, projectsIn{"p1": "o1", "p2": "o1"}, zap.NewNop()).RegisterRoutes(r.Group("/api/v1", func(c *gin.Context) {
			c.Set("userID", user)
			c.Set("orgID", "o1")
			c.Set("memberRole", role)
			c.Next()
		}))
		return r
	}
	r, own := router("dev", "developer"), router("own", "owner")
	call := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		var b []byte
		if body != nil {
			b, _ = json.Marshal(body)
		}
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
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
	at := time.Date(2026, 10, 20, 14, 0, 0, 0, time.UTC)
	repo.deploys["d1"] = &FailedDeployment{ID: "d1", Status: "failed", Error: "go: missing go.sum entry", CommitSHA: "e41b07c0ffee",
		At: at, ServiceID: "s1", ServiceName: "Worker", Environment: "production", ProjectID: "p1", OrgID: "o1"}
	repo.deploys["d2"] = &FailedDeployment{ID: "d2", Status: "running", ServiceID: "s1", ProjectID: "p1", OrgID: "o1"}
	base := "/api/v1/services/s1/deployments/"

	if code, out := call("POST", base+"d2/issue", nil); code != 409 || data(out)["code"] != "not_failed" {
		t.Errorf("a live deploy: %d %v", code, out)
	}
	code, out := call("POST", base+"d1/issue", nil)
	if code != 201 {
		t.Fatalf("open from failure: %d %v", code, out)
	}
	d := data(out)
	if d["title"] != "Deploy failed: Worker in production" || d["priority"] != "high" || d["type"].(map[string]any)["icon"] != "bug" ||
		!slices.Equal(toStrings(d["labels"]), []string{"worker"}) || !strings.Contains(d["description"].(string), "go: missing go.sum entry") {
		t.Errorf("the issue: %v", d)
	}
	key := d["key"].(string)
	if code, out := call("POST", base+"d1/issue", nil); code != 409 || data(out)["code"] != "already_open" || data(out)["key"] != key {
		t.Errorf("a second one: %d %v", code, out)
	}

	// The project opens them itself: a further failure notes the open one.
	r, own = own, r
	code, out = call("PATCH", "/api/v1/projects/p1/issue-settings", map[string]any{"failed_deploy_issues": true})
	r, own = own, r
	if code != 200 || data(out)["failed_deploy_issues"] != true {
		t.Fatalf("setting: %d %v", code, out)
	}
	repo.deploys["d3"] = &FailedDeployment{ID: "d3", Status: "failed", CommitSHA: "abcdef1234", ServiceID: "s1", ServiceName: "Worker", ProjectID: "p1", OrgID: "o1"}
	n := len(repo.issues)
	svc.DeployFailed(context.Background(), "s1", "d3")
	if len(repo.issues) != n || repo.events[len(repo.events)-1].Kind != "deploy_failed" || repo.events[len(repo.events)-1].To != "abcdef1" {
		t.Errorf("the run's next failure: %d issues, %+v", len(repo.issues), repo.events[len(repo.events)-1])
	}
	if code, _ := call("GET", "/api/v1/projects/p1/issues/"+strconv.Itoa(repo.issues[n-1].Number)+"/timeline", nil); code != 200 {
		t.Errorf("the history with a further failure: %d", code)
	}

	// The next deploy live ships it; the next failure opens a new one.
	repo.live["s1"] = "d4"
	svc.DeployLive(context.Background(), "s1")
	if st := repo.issues[n-1].Status; st != StatusShipped {
		t.Errorf("shipped by the live deploy: %s", st)
	}
	svc.DeployFailed(context.Background(), "s1", "d3")
	if len(repo.issues) != n+1 || repo.issues[n].CreatedByID != "" {
		t.Errorf("a new run's issue: %d", len(repo.issues))
	}
	// Off: nothing opens.
	r, own = own, r
	call("PATCH", "/api/v1/projects/p1/issue-settings", map[string]any{"failed_deploy_issues": false})
	r, own = own, r
	call("PATCH", "/api/v1/projects/p1/issues/"+strconv.Itoa(repo.issues[n].Number), map[string]any{"status": "cancelled"})
	svc.DeployFailed(context.Background(), "s1", "d3")
	if len(repo.issues) != n+1 {
		t.Errorf("opened with the setting off")
	}

	// My issues; a mention renders.
	call("POST", "/api/v1/projects/p1/issues", map[string]any{"title": "Mine", "assignee_id": "dev"})
	_, out = call("GET", "/api/v1/workspace/my-issues", nil)
	if mine := out["data"].([]any); len(mine) != 1 || mine[0].(map[string]any)["title"] != "Mine" {
		t.Errorf("my issues: %v", mine)
	}
	_, out = call("POST", "/api/v1/projects/p1/issues/1/comments", map[string]any{"body": "@ann see this"})
	if !strings.Contains(data(out)["html"].(string), `data-toft-user="ann"`) {
		t.Errorf("a mention: %v", data(out)["html"])
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

// ─── cycles (D-90) ───────────────────────────────────────────────────────────

func (f *fakeRepo) cycle(id string) *StoredCycle {
	for i := range f.cycles {
		if f.cycles[i].ID == id {
			return &f.cycles[i]
		}
	}
	return nil
}

func (f *fakeRepo) fillCycle(s *Stored) {
	if c := f.cycle(s.CycleID); c != nil {
		s.CycleNumber, s.CycleName, s.CycleStarts, s.CycleEnds = c.Number, c.Name, c.StartsOn, c.EndsOn
	}
	if from := f.cycle(f.carried[s.ID]); from != nil {
		s.CarriedFrom = from.Number
	}
}

func (f *fakeRepo) OrgSettings(context.Context, string) (OrgSettings, error) {
	o := f.cyc
	o.Digits = f.orgDigits
	return o, nil
}
func (f *fakeRepo) SetCycleSchedule(_ context.Context, _ string, weeks *int, startDay int, zone string) error {
	f.cyc.Weeks, f.cyc.StartDay, f.cyc.Zone = intOr0(weeks), startDay, zone
	return nil
}
func (f *fakeRepo) SetEstimates(_ context.Context, _ string, scale string) error {
	f.cyc.Estimates = scale
	return nil
}
func (f *fakeRepo) CycleSchedules(context.Context) ([]OrgSchedule, error) {
	if f.cyc.Weeks == 0 {
		return nil, nil
	}
	return []OrgSchedule{{OrgID: "o1", Zone: f.cyc.Zone, Weeks: f.cyc.Weeks, StartDay: f.cyc.StartDay}}, nil
}
func (f *fakeRepo) Cycles(context.Context, string) ([]StoredCycle, error) {
	return slices.Clone(f.cycles), nil
}
func (f *fakeRepo) AddCycle(_ context.Context, _ string, c StoredCycle) error {
	if !slices.ContainsFunc(f.cycles, func(x StoredCycle) bool { return x.Number == c.Number }) {
		c.ID = "c" + strconv.Itoa(c.Number)
		f.cycles = append(f.cycles, c)
	}
	return nil
}
func (f *fakeRepo) MoveCycle(_ context.Context, c StoredCycle, today time.Time) error {
	if x := f.cycle(c.ID); x != nil && x.StartsOn.After(today) {
		x.StartsOn, x.EndsOn = c.StartsOn, c.EndsOn
	}
	return nil
}
func (f *fakeRepo) RenameCycle(_ context.Context, _ string, number int, name string) (bool, error) {
	for i := range f.cycles {
		if f.cycles[i].Number == number {
			f.cycles[i].Name = name
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeRepo) CycleByNumber(_ context.Context, _ string, number int) (*StoredCycle, error) {
	for _, c := range f.cycles {
		if c.Number == number {
			return &c, nil
		}
	}
	return nil, ErrNotFound
}
func (f *fakeRepo) CloseCycle(_ context.Context, _ string, c StoredCycle) error {
	x := f.cycle(c.ID)
	if x == nil || x.Closed {
		return nil
	}
	next, _ := f.CycleByNumber(context.Background(), "", c.Number+1)
	counts, _ := f.CycleCounts(context.Background(), "", "")
	final := counts[c.ID]
	final.Started = 0
	x.Closed, x.Final = true, &final
	for _, s := range f.issues {
		if s.CycleID == c.ID && s.Status != StatusShipped && s.Status != StatusCancelled {
			s.CycleID = next.ID
			f.carried[s.ID] = c.ID
			x.Final.Carried++
			f.events = append(f.events, Event{IssueID: s.ID, Kind: "cycle", Automatic: true, From: strconv.Itoa(c.Number), To: strconv.Itoa(next.Number)})
		}
	}
	return nil
}
func (f *fakeRepo) SnapshotCycle(_ context.Context, id string, d time.Time) error {
	counts, _ := f.CycleCounts(context.Background(), "", "")
	c := counts[id]
	day := CycleDay{Day: fmtDate(d), Scope: c.Scope, Started: c.Started, Shipped: c.Shipped}
	ds := f.days[id]
	if n := len(ds); n > 0 && ds[n-1].Day == day.Day {
		ds[n-1] = day
	} else {
		f.days[id] = append(ds, day)
	}
	return nil
}
func (f *fakeRepo) CycleCounts(_ context.Context, _, project string) (map[string]CycleCounts, error) {
	out := map[string]CycleCounts{}
	for _, s := range f.issues {
		if s.CycleID == "" || project != "" && s.ProjectID != project {
			continue
		}
		c, pts := out[s.CycleID], intOr0(s.Estimate)
		if s.Status == StatusCancelled {
			c.Cancelled++
		} else {
			c.Scope++
			c.Points += pts
			if s.Status == StatusInProgress || s.Status == StatusInReview || s.Status == StatusShipped {
				c.Started++
				c.StartedPoints += pts
			}
			if s.Status == StatusShipped {
				c.Shipped++
				c.ShippedPoints += pts
			}
		}
		out[s.CycleID] = c
	}
	return out, nil
}
func (f *fakeRepo) CycleDays(_ context.Context, id string) ([]CycleDay, error) {
	return append([]CycleDay{}, f.days[id]...), nil
}
func (f *fakeRepo) CycleShares(_ context.Context, id string) ([]CycleShare, []CycleShare, error) {
	byProject, byWho := map[string]*CycleShare{}, map[string]*CycleShare{}
	add := func(m map[string]*CycleShare, k string, shipped bool) {
		if m[k] == nil {
			m[k] = &CycleShare{ID: k, Name: k}
		}
		m[k].Total++
		if shipped {
			m[k].Shipped++
		}
	}
	for _, s := range f.issues {
		if s.CycleID != id && f.carried[s.ID] != id || s.Status == StatusCancelled {
			continue
		}
		add(byProject, s.ProjectID, s.Status == StatusShipped)
		add(byWho, s.AssigneeID, s.Status == StatusShipped)
	}
	flat := func(m map[string]*CycleShare) []CycleShare {
		out := []CycleShare{}
		for _, v := range m {
			out = append(out, *v)
		}
		slices.SortFunc(out, func(a, b CycleShare) int { return strings.Compare(a.ID, b.ID) })
		return out
	}
	return flat(byProject), flat(byWho), nil
}
func (f *fakeRepo) CycleMoves(context.Context, StoredCycle, time.Time) (int, int, error) {
	return 0, 0, nil
}
func (f *fakeRepo) CycleIssues(_ context.Context, id, project, after string, limit int) ([]CycleIssueRow, error) {
	out := []CycleIssueRow{}
	for _, s := range f.issues {
		if s.CycleID != id && f.carried[s.ID] != id || project != "" && s.ProjectID != project || s.ID <= after {
			continue
		}
		r := CycleIssueRow{ID: s.ID, ProjectID: s.ProjectID, Project: f.projects[s.ProjectID].Name, Key: KeyOf(f.projects[s.ProjectID].Key, s.Number, 0),
			Number: s.Number, Title: s.Title, Status: s.Status, Priority: s.Priority, TypeID: s.TypeID, AssigneeID: s.AssigneeID,
			AssigneeName: s.AssigneeName, Estimate: s.Estimate, DueOn: s.DueOn, Labels: s.Labels}
		if c := f.cycle(f.carried[s.ID]); c != nil && s.CycleID == id {
			r.CarriedFrom = c.Number
		}
		if f.carried[s.ID] == id {
			r.CarriedTo = f.cycle(s.CycleID).Number
		}
		out = append(out, r)
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (f *fakeRepo) TimelineIssues(_ context.Context, project string, _ time.Time) ([]TimelineRow, error) {
	out := []TimelineRow{}
	for _, s := range f.issues {
		if s.ProjectID != project {
			continue
		}
		r := TimelineRow{ID: s.ID, ParentID: s.ParentID, Number: s.Number, Title: s.Title, Status: s.Status, Priority: s.Priority, TypeID: s.TypeID,
			StartOn: s.StartOn, DueOn: s.DueOn, Estimate: s.Estimate, AssigneeID: s.AssigneeID, AssigneeName: s.AssigneeName,
			StatusAt: s.StatusAt, Labels: s.Labels}
		for _, k := range f.issues {
			if k.ParentID == s.ID {
				r.Children++
				r.ChildrenPoints += intOr0(k.Estimate)
				if k.Status == StatusShipped {
					r.Shipped++
				}
			}
		}
		out = append(out, r)
	}
	return out, nil
}
func (f *fakeRepo) ProjectBlocks(context.Context, string) ([][2]string, error) {
	out := [][2]string{}
	for _, r := range f.relations {
		if r.kind == "blocks" {
			out = append(out, [2]string{r.from, r.to})
		}
	}
	return out, nil
}
