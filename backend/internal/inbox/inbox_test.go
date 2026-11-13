package inbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/agent"
	"github.com/Nickpap21/toft/backend/internal/alert"
	"github.com/Nickpap21/toft/backend/internal/apispec"
	"github.com/Nickpap21/toft/backend/internal/instance"
	"github.com/Nickpap21/toft/backend/pkg/broker"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type fakeRepo struct {
	failed, alerts, assigned, due []Item
	https                         []Item
	public                        []PublicService
	unbacked                      []UnbackedService
	panelBacked                   bool
	done                          map[string]map[string]time.Time // user → key → when
	notes                         []Notification
	read, doneIDs                 map[int]bool
	members                       []Member
	cursors                       map[string][2]string
	rows                          map[string][]Row
	who                           map[string][]string
	myIssues                      int
}

func newFake() *fakeRepo {
	return &fakeRepo{done: map[string]map[string]time.Time{}, read: map[int]bool{}, doneIDs: map[int]bool{}, cursors: map[string][2]string{},
		rows: map[string][]Row{}, who: map[string][]string{}}
}

func (f *fakeRepo) FailedDeploys(context.Context, string) ([]Item, error) { return f.failed, nil }
func (f *fakeRepo) OpenAlerts(context.Context, string) ([]Item, error)    { return f.alerts, nil }
func (f *fakeRepo) UnprotectedPublic(context.Context, string) ([]PublicService, error) {
	return f.public, nil
}
func (f *fakeRepo) UnbackedServices(context.Context, string) ([]UnbackedService, error) {
	return f.unbacked, nil
}
func (f *fakeRepo) PanelHostBackedUp(context.Context) (bool, error)        { return f.panelBacked, nil }
func (f *fakeRepo) HTTPSRedeploys(context.Context, string) ([]Item, error) { return f.https, nil }
func (f *fakeRepo) TemporaryLogins(context.Context, string, []string) ([]PublicService, error) {
	return []PublicService{}, nil
}
func (f *fakeRepo) AssignedUrgent(_ context.Context, _, user string) ([]Item, error) {
	if user == "ann" {
		return f.assigned, nil
	}
	return []Item{}, nil
}
func (f *fakeRepo) AssignedDue(_ context.Context, _, user string) ([]Item, error) {
	if user == "ann" {
		return f.due, nil
	}
	return []Item{}, nil
}
func (f *fakeRepo) DoneKeys(_ context.Context, user string) (map[string]time.Time, error) {
	if f.done[user] == nil {
		return map[string]time.Time{}, nil
	}
	return f.done[user], nil
}
func (f *fakeRepo) MarkDone(_ context.Context, user string, keys []string, done bool) error {
	if f.done[user] == nil {
		f.done[user] = map[string]time.Time{}
	}
	for _, k := range keys {
		if done {
			f.done[user][k] = time.Now()
		} else {
			delete(f.done[user], k)
		}
	}
	return nil
}
func (f *fakeRepo) CountMyIssues(context.Context, string, string) (int, error) {
	return f.myIssues, nil
}
func (f *fakeRepo) Updates(_ context.Context, q UpdateQuery) ([]Update, error) {
	out := []Update{}
	for i := len(f.notes) - 1; i >= 0; i-- {
		n := f.notes[i]
		id := int64(i + 1)
		if n.UserID != q.UserID || n.OrgID != q.OrgID || f.doneIDs[i+1] != q.Done || (q.Unread && f.read[i+1]) ||
			(len(q.Kinds) > 0 && !slices.Contains(q.Kinds, n.Kind)) || (q.Before != nil && id >= q.BeforeID) {
			continue
		}
		u := Update{ID: fmt.Sprint(id), id: id, Kind: n.Kind, ProjectID: n.ProjectID, Detail: n.Detail, CreatedAt: n.CreatedAt,
			Read: f.read[i+1], Done: f.doneIDs[i+1]}
		if n.ActorID != nil {
			u.Actor = &Person{ID: *n.ActorID, Name: *n.ActorID}
		}
		if n.IssueID != nil {
			u.Issue = &Issue{Key: "STO-1", Number: 1, Title: "Retry", Status: "in_progress"}
		}
		if n.PullID != nil {
			u.Pull = &Pull{Repository: "storefront", Number: 4, Title: "Faster", State: "merged"}
		}
		if n.PageID != nil {
			u.Page = &Page{ID: *n.PageID, Title: "Runbook"}
		}
		u.Category = categoryOf(u.Kind, u.Pull != nil, u.Page != nil)
		out = append(out, u)
		if len(out) == q.Limit {
			break
		}
	}
	return out, nil
}
func (f *fakeRepo) CountUnread(_ context.Context, user, org string) (int, error) {
	n := 0
	for i, x := range f.notes {
		if x.UserID == user && x.OrgID == org && !f.read[i+1] && !f.doneIDs[i+1] {
			n++
		}
	}
	return n, nil
}
func (f *fakeRepo) MarkRead(_ context.Context, user, _ string, ids []int64, all bool) error {
	for i, x := range f.notes {
		if x.UserID == user && (all || slices.Contains(ids, int64(i+1))) {
			f.read[i+1] = true
		}
	}
	return nil
}
func (f *fakeRepo) SetDone(_ context.Context, user, _ string, ids []int64, done bool) error {
	for _, id := range ids {
		if int(id) <= len(f.notes) && f.notes[id-1].UserID == user {
			f.doneIDs[int(id)] = done
			f.read[int(id)] = true
		}
	}
	return nil
}
func (f *fakeRepo) Purge(context.Context) error                         { return nil }
func (f *fakeRepo) Members(context.Context, []string) ([]Member, error) { return f.members, nil }
func (f *fakeRepo) Cursor(_ context.Context, src string) (time.Time, string, bool, error) {
	c, ok := f.cursors[src]
	if !ok {
		return time.Time{}, "", false, nil
	}
	at, _ := time.Parse(time.RFC3339Nano, c[0])
	return at, c[1], true, nil
}
func (f *fakeRepo) SetCursor(_ context.Context, src string, at time.Time, id string) error {
	f.cursors[src] = [2]string{at.Format(time.RFC3339Nano), id}
	return nil
}
func (f *fakeRepo) Rows(_ context.Context, src string, _ time.Time, after string) ([]Row, error) {
	out := []Row{}
	for _, r := range f.rows[src] {
		if r.RowID > after {
			out = append(out, r)
		}
	}
	return out, nil
}
func (f *fakeRepo) Participants(_ context.Context, _ string, ids []string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, id := range ids {
		out[id] = f.who[id]
	}
	return out, nil
}
func (f *fakeRepo) Insert(_ context.Context, ns []Notification) error {
	f.notes = append(f.notes, ns...)
	return nil
}

type fakePages struct{}

func (fakePages) StalePagesFor(_ context.Context, _, user string) ([]StalePage, error) {
	if user != "ann" {
		return []StalePage{}, nil
	}
	return []StalePage{{ID: "page1", ProjectID: "p1", Project: "storefront", Title: "Restore Postgres", Since: time.Unix(1700000000, 0),
		Change: "postgres changed from postgres:16.2 to postgres:16.4 after this page was last verified"}}, nil
}

type fakeInstance struct {
	upgrade     *instance.UpgradeStatus
	noWatchdog  bool
	watchdogErr error
	setup       *instance.Setup
	setupErr    error
}

func (f fakeInstance) Setup(context.Context, string) (*instance.Setup, error) {
	if f.setupErr != nil {
		return nil, f.setupErr
	}
	if f.setup == nil {
		return &instance.Setup{}, nil
	}
	return f.setup, nil
}

func (f fakeInstance) WatchdogStatus(context.Context, string) (*instance.Watchdog, error) {
	if f.watchdogErr != nil {
		return nil, f.watchdogErr
	}
	if f.noWatchdog {
		return &instance.Watchdog{}, nil
	}
	return &instance.Watchdog{URL: "https://hc-ping.example/x"}, nil
}

func (fakeInstance) IsOperator(_ context.Context, user string) (bool, error) {
	return user == "op", nil
}

func (f fakeInstance) UpgradeStatus(context.Context, string) (*instance.UpgradeStatus, error) {
	if f.upgrade == nil {
		return &instance.UpgradeStatus{}, nil
	}
	return f.upgrade, nil
}

type pubs struct{ events []broker.Event }

func (p *pubs) Publish(_ string, e broker.Event) { p.events = append(p.events, e) }

func users(ns []Notification, kind string) []string {
	var out []string
	for _, n := range ns {
		if n.Kind == kind {
			out = append(out, n.UserID)
		}
	}
	sort.Strings(out)
	return out
}

// TestNotifier: who hears of what (D-88), never their own actions, and
// mentions over plain comments.
func TestNotifier(t *testing.T) {
	repo := newFake()
	pub := &pubs{}
	svc := NewService(repo, nil, nil, pub, zap.NewNop())
	ctx := context.Background()
	repo.members = []Member{{OrgID: "o1", UserID: "ann", Email: "ann@shop.test"}, {OrgID: "o1", UserID: "bob", Email: "bob@shop.test"},
		{OrgID: "o1", UserID: "cy", Email: "cy@shop.test"}, {OrgID: "o1", UserID: "jo1", Email: "jo@a.test"}, {OrgID: "o1", UserID: "jo2", Email: "jo@b.test"}}

	// The first read starts now: history isn't sent.
	repo.rows["issue_events"] = []Row{{RowID: "0001", Kind: "status", ActorID: "bob", IssueID: "i1", OrgID: "o1"}}
	if err := svc.Notify(ctx, "issue_events"); err != nil || len(repo.notes) != 0 {
		t.Fatalf("first read: %v %v", err, repo.notes)
	}
	repo.cursors["issue_events"] = [2]string{time.Now().Format(time.RFC3339Nano), "0001"}

	repo.who["i1"] = []string{"ann", "bob", "cy"}
	repo.rows["issue_events"] = append(repo.rows["issue_events"],
		Row{RowID: "0002", Kind: "status", ActorID: "bob", From: "todo", To: "in_progress", IssueID: "i1", ProjectID: "p1", OrgID: "o1"},
		Row{RowID: "0003", Kind: "assigned", ActorID: "bob", To: "cy", IssueID: "i1", ProjectID: "p1", OrgID: "o1"},
		Row{RowID: "0004", Kind: "assigned", ActorID: "cy", To: "cy", IssueID: "i1", ProjectID: "p1", OrgID: "o1"},
		Row{RowID: "0005", Kind: "opened", ActorID: "bob", Body: "for @ann, not `@cy`, and @jo (two of them)", IssueID: "i1", ProjectID: "p1", OrgID: "o1"})
	if err := svc.Notify(ctx, "issue_events"); err != nil {
		t.Fatal(err)
	}
	if got := users(repo.notes, "issue_status"); !slices.Equal(got, []string{"ann", "cy"}) {
		t.Errorf("status: %v", got)
	}
	if got := users(repo.notes, "issue_assigned"); !slices.Equal(got, []string{"cy"}) {
		t.Errorf("assigned (not to oneself): %v", got)
	}
	if got := users(repo.notes, "mention"); !slices.Equal(got, []string{"ann"}) {
		t.Errorf("mentions in a description: %v", got)
	}
	if repo.cursors["issue_events"][1] != "0005" || len(pub.events) != 1 {
		t.Errorf("cursor %v, events %v", repo.cursors["issue_events"], pub.events)
	}

	// A comment: a mention, not also a comment; never the author.
	repo.notes = nil
	repo.cursors["issue_comments"] = [2]string{time.Now().Format(time.RFC3339Nano), ""}
	repo.rows["issue_comments"] = []Row{{RowID: "c1", ActorID: "cy", Body: "@ann can you look? @cy here", IssueID: "i1", ProjectID: "p1", OrgID: "o1"}}
	if err := svc.Notify(ctx, "issue_comments"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(users(repo.notes, "mention"), []string{"ann"}) || !slices.Equal(users(repo.notes, "issue_comment"), []string{"bob"}) {
		t.Errorf("comment: %+v", repo.notes)
	}
	if ex := repo.notes[0].Detail["excerpt"]; ex != "@ann can you look? @cy here" {
		t.Errorf("excerpt: %v", ex)
	}

	// A merge and a page edit.
	repo.notes = nil
	repo.who["pr4"], repo.who["page1"] = []string{"ann", "bob"}, []string{"ann", "bob"}
	repo.cursors["pull_request_events"] = [2]string{time.Now().Format(time.RFC3339Nano), ""}
	repo.rows["pull_request_events"] = []Row{{RowID: "e1", Kind: "merged", ActorID: "ann", PullID: "pr4", ProjectID: "p1", OrgID: "o1"}}
	repo.cursors["doc_page_versions"] = [2]string{time.Now().Format(time.RFC3339Nano), ""}
	repo.rows["doc_page_versions"] = []Row{{RowID: "v2", Kind: "edited", ActorID: "bob", Version: 2, PageID: "page1", ProjectID: "p1", OrgID: "o1"}}
	for _, src := range []string{"pull_request_events", "doc_page_versions"} {
		if err := svc.Notify(ctx, src); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(users(repo.notes, "pull_merged"), []string{"bob"}) || !slices.Equal(users(repo.notes, "page_edited"), []string{"ann"}) {
		t.Errorf("merge and edit: %+v", repo.notes)
	}

	if h := Handles(repo.members); h["ann"] != "ann" || h["jo"] != "" {
		t.Errorf("handles: %v", h)
	}
}

func TestDueDetail(t *testing.T) {
	d := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	for today, want := range map[int]string{-1: "Due tomorrow", 0: "Due today", 1: "1 day late", 3: "3 days late"} {
		if got := dueDetail(d, d.AddDate(0, 0, today)); got != want {
			t.Errorf("%d days on: %q, want %q", today, got, want)
		}
	}
}

// TestInboxAPI: Needs you by role, marked done and back; updates paged,
// read and done; the counts.
func TestInboxAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := newFake()
	svc := NewService(repo, fakePages{}, fakeInstance{}, nil, zap.NewNop())
	now := time.Now()
	repo.failed = []Item{{Key: "deploy:d1", Kind: "deploy_failed", Category: "deploys", Title: "billing-api / worker", Detail: "Deploy failed", Error: "missing go.sum entry",
		At: now, Project: "billing-api", ServiceID: "s1", DeploymentID: "d1"}}
	repo.alerts = []Item{{Key: "alert:a1", Kind: "alert", Category: "servers", Title: "fsn1", Detail: "Disk 86% used", At: now.Add(-time.Hour), AlertKind: "disk_full", ServerID: "v1"}}
	repo.assigned = []Item{{Key: "assigned:i1", Kind: "assigned", Category: "issues", Title: "Retry", Detail: "Assigned to you", At: now.Add(-2 * time.Hour),
		Issue: &Issue{Key: "STO-118", Number: 118, Title: "Retry", Status: "todo", Priority: "urgent"}}}
	repo.due = []Item{{Key: "due:i2:2026-10-20", Kind: "due", Category: "issues", Title: "Prorate", Detail: "Due today", At: now.Add(-150 * time.Minute),
		Issue: &Issue{Key: "STO-119", Number: 119, Title: "Prorate", Status: "in_progress", Priority: "high"}}}
	repo.myIssues = 7
	p1 := "p1"
	for i := 0; i < 3; i++ {
		repo.notes = append(repo.notes, Notification{UserID: "ann", OrgID: "o1", Kind: "issue_comment", ProjectID: &p1, IssueID: &p1,
			Detail: map[string]any{"excerpt": "hi"}, CreatedAt: now.Add(time.Duration(i) * time.Minute)})
	}
	router := func(user, role string) http.Handler {
		r := gin.New()
		NewHandler(svc, zap.NewNop()).RegisterRoutes(r.Group("/api/v1", func(c *gin.Context) {
			c.Set("userID", user)
			c.Set("orgID", "o1")
			c.Set("memberRole", role)
			c.Next()
		}))
		return r
	}
	ann, viewer, op := router("ann", "developer"), router("vic", "viewer"), router("op", "owner")
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
	kinds := func(out map[string]any) []string {
		var ks []string
		for _, x := range out["data"].([]any) {
			ks = append(ks, x.(map[string]any)["kind"].(string))
		}
		return ks
	}

	_, out := call(ann, "GET", "/api/v1/inbox/needs-you", nil)
	if got := kinds(out); !slices.Equal(got, []string{"deploy_failed", "alert", "assigned", "due", "stale_page"}) {
		t.Errorf("a developer's: %v", got)
	}
	_, out = call(viewer, "GET", "/api/v1/inbox/needs-you", nil)
	if got := kinds(out); len(got) != 0 {
		t.Errorf("a viewer's: %v", got)
	}
	_, out = call(op, "GET", "/api/v1/inbox/needs-you", nil)
	// An operator hears that Toft's own database has no backup; a release
	// is the Instance chip's, not Needs you's (D-140).
	if got := kinds(out); slices.Contains(got, "release") || !slices.Contains(got, "no_backup") {
		t.Errorf("an operator's: %v", got)
	}

	if code, _ := call(ann, "POST", "/api/v1/inbox/done", map[string]any{"keys": []string{"deploy:d1"}, "updates": []string{"1"}}); code != 204 {
		t.Fatalf("done: %d", code)
	}
	_, out = call(ann, "GET", "/api/v1/inbox/needs-you", nil)
	if slices.Contains(kinds(out), "deploy_failed") {
		t.Errorf("done stays: %v", kinds(out))
	}
	_, out = call(ann, "GET", "/api/v1/inbox/needs-you?done=true", nil)
	if !slices.Equal(kinds(out), []string{"deploy_failed"}) {
		t.Errorf("Done: %v", kinds(out))
	}
	_, out = call(ann, "GET", "/api/v1/inbox/counts", nil)
	if c := out["data"].(map[string]any); c["needs_you"].(float64) != 4 || c["unread"].(float64) != 2 || c["my_issues"].(float64) != 7 {
		t.Errorf("counts: %v", c)
	}
	if code, out := call(ann, "GET", "/api/v1/inbox/needs-you?done=any", nil); code != 200 || len(kinds(out)) != 5 || !slices.Contains(kinds(out), "deploy_failed") {
		t.Errorf("done=any: %d %v", code, out)
	}
	call(ann, "POST", "/api/v1/inbox/undone", map[string]any{"keys": []string{"deploy:d1"}})

	_, out = call(ann, "GET", "/api/v1/inbox/updates?limit=1", nil)
	if len(out["data"].([]any)) != 1 || out["next"] == nil {
		t.Fatalf("a page: %v", out)
	}
	_, out = call(ann, "GET", "/api/v1/inbox/updates?limit=1&cursor="+out["next"].(string), nil)
	if len(out["data"].([]any)) != 1 || out["data"].([]any)[0].(map[string]any)["id"] != "2" {
		t.Errorf("the next page: %v", out)
	}
	if code, _ := call(ann, "POST", "/api/v1/inbox/read", map[string]any{"all": true}); code != 204 {
		t.Errorf("read all: %d", code)
	}
	_, out = call(ann, "GET", "/api/v1/inbox/updates?unread=true", nil)
	if len(out["data"].([]any)) != 0 {
		t.Errorf("unread after read all: %v", out)
	}
	if code, _ := call(ann, "GET", "/api/v1/inbox/updates?cursor=nope", nil); code != 400 {
		t.Errorf("a bad cursor: %d", code)
	}
	code, out := call(ann, "GET", "/api/v1/inbox/needs-you?done=maybe", nil)
	if code != 400 || out["data"].(map[string]any)["code"] != "bad_input" {
		t.Errorf("an unknown done: %d %v", code, out)
	}
}

// Every Needs you kind says on purpose whether done only puts it off: a
// condition that a mark hid for good is how Health and Needs you disagreed.
func TestSnoozes(t *testing.T) {
	for kind, want := range map[string]bool{
		"no_backup": true, "no_watchdog": true, "kit_out_of_date": true, "exposed_tool": true, "setup_login": true, "https_redeploy": true, "alert": true,
		"deploy_failed": false, "upgrade_failed": false, "assigned": false, "due": false, "stale_page": false,
		"preview_approval": false, "agent_approval": false,
	} {
		if got := snoozes(kind); got != want {
			t.Errorf("%s: %v", kind, got)
		}
	}
}

// A condition marked done is put off for a week, and comes back by itself
// if it still holds; an event stays done however long ago it was marked.
func TestNeedsYouSnooze(t *testing.T) {
	repo := newFake()
	now := time.Now()
	repo.alerts = []Item{
		{Key: "alert:recent", Kind: "alert", Category: "servers", At: now},
		{Key: "alert:old", Kind: "alert", Category: "servers", At: now},
	}
	repo.failed = []Item{{Key: "deploy:old", Kind: "deploy_failed", Category: "deploys", At: now}}
	day := 24 * time.Hour
	recent := now.Add(-day)
	repo.done["ann"] = map[string]time.Time{"alert:recent": recent, "alert:old": now.Add(-8 * day), "deploy:old": now.Add(-8 * day)}
	svc := NewService(repo, nil, nil, nil, zap.NewNop())
	read := func(which Marks) map[string]Item {
		t.Helper()
		items, err := svc.NeedsYou(context.Background(), "o1", "ann", "owner", which)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]Item{}
		for _, it := range items {
			out[it.Key] = it
		}
		return out
	}

	open := read(MarksOpen)
	if _, ok := open["alert:recent"]; ok {
		t.Error("marked a day ago: still open")
	}
	if it, ok := open["alert:old"]; !ok || !it.Snoozes || it.SnoozedUntil != nil {
		t.Errorf("marked 8 days ago: back in the open list, not put off: %v %+v", ok, it)
	}
	if _, ok := open["deploy:old"]; ok {
		t.Error("an event marked 8 days ago: open again")
	}

	done := read(MarksDone)
	it, ok := done["alert:recent"]
	if !ok || !it.Snoozes || it.SnoozedUntil == nil || !it.SnoozedUntil.Equal(recent.Add(7*day)) {
		t.Errorf("marked a day ago, in done: %v %+v", ok, it)
	}
	if _, ok := done["alert:old"]; ok {
		t.Error("marked 8 days ago: still in done")
	}
	if ev, ok := done["deploy:old"]; !ok || ev.Snoozes || ev.SnoozedUntil != nil {
		t.Errorf("an event in done: %v %+v", ok, ev)
	}

	if all := read(MarksAny); len(all) != 3 {
		t.Errorf("any: %d items, want 3: %v", len(all), all)
	}
}

type fakeAgents struct{ waiting []agent.Waiting }

func (f fakeAgents) Waiting(context.Context, string) ([]agent.Waiting, error) { return f.waiting, nil }

// A waiting request's title says what it asks for, in its page's words;
// Home splits the verb off it (home.ts).
func TestAgentRequestTitles(t *testing.T) {
	want := map[string]string{
		"deploy": "Deploy t", "rollback": "Roll back t", "set_secret": "Set a secret on t", "set_env_var": "Set a variable on t",
		"run_runbook_step": "Run a runbook step in t", "create_branch": "Make a branch in t", "open_pr": "Open a pull request in t",
		"create_issue": "Open an issue in t", "comment": "Comment in t", "create_preview": "Start a preview in t", "a_new_tool": "t",
	}
	now := time.Now()
	for tool, title := range want {
		svc := NewService(newFake(), fakePages{}, fakeInstance{}, nil, zap.NewNop())
		svc.WithAgentApprovals(fakeAgents{waiting: []agent.Waiting{{ID: "w1", Tool: tool, Target: "t", ExpiresAt: now.Add(time.Hour), CreatedAt: now}}})
		items, err := svc.NeedsYou(context.Background(), "o1", "ann", "owner", MarksOpen)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, it := range items {
			if it.Key == "agent:w1" {
				found = true
				if it.Title != title {
					t.Errorf("%s: title %q, want %q", tool, it.Title, title)
				}
			}
		}
		if !found {
			t.Errorf("%s: no row", tool)
		}
	}
}

// Needs you comes in severity order whatever the times (D-140): problems,
// an agent's request (it expires), warnings, then requests; newest first
// within one. The sidebar's count is the same list's length.
func TestNeedsYouSeverity(t *testing.T) {
	repo := newFake()
	now := time.Now()
	repo.failed = []Item{{Key: "deploy:d1", Kind: "deploy_failed", Category: "deploys", At: now.Add(-5 * time.Hour)}}
	repo.alerts = []Item{
		{Key: "alert:disk", Kind: "alert", AlertKind: "disk_full", Category: "servers", At: now.Add(-time.Minute)},
		{Key: "alert:backup", Kind: "alert", AlertKind: "backup_failed", Category: "servers", At: now.Add(-4 * time.Hour)},
		{Key: "alert:new", Kind: "alert", AlertKind: "something_new", Category: "deploys", At: now.Add(-2 * time.Minute)},
	}
	repo.assigned = []Item{{Key: "assigned:i1", Kind: "assigned", Category: "issues", At: now}}
	svc := NewService(repo, fakePages{}, fakeInstance{}, nil, zap.NewNop())
	svc.WithAgentApprovals(fakeAgents{waiting: []agent.Waiting{{ID: "w1", AgentName: "Claude Code", UserName: "sam", Tool: "deploy",
		Target: "storefront / api", ExpiresAt: now.Add(30 * time.Minute), CreatedAt: now.Add(-3 * time.Hour)}}})

	items, err := svc.NeedsYou(context.Background(), "o1", "ann", "owner", MarksOpen)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range items {
		got = append(got, it.Key+"="+it.Severity)
	}
	want := []string{"alert:backup=problem", "deploy:d1=problem", "agent:w1=waiting", "alert:disk=warning", "alert:new=warning", "assigned:i1=request",
		"stale:page1:1700000000=request"}
	if !slices.Equal(got, want) {
		t.Errorf("order:\n got %v\nwant %v", got, want)
	}
	c, err := svc.Counts(context.Background(), "o1", "ann", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if c.NeedsYou != len(items) {
		t.Errorf("the sidebar's count %d, the list's %d", c.NeedsYou, len(items))
	}
}

// Every alert kind and item kind has its severity on purpose (D-140): a
// renamed or misspelled kind would turn an offline server into a warning,
// and Home's cap would hide it.
func TestSeverityOf(t *testing.T) {
	alerts := map[alert.Kind]string{
		alert.KindServerUnreachable: "problem", alert.KindAgentOffline: "problem", alert.KindBackupFailed: "problem",
		alert.KindServiceDown: "problem", alert.KindContainerExited: "problem", alert.KindPanelContainer: "problem",
		alert.KindMirrorFailing: "problem", alert.KindAgentFrozen: "problem", alert.KindServiceRestarting: "problem",
		alert.KindDiskFull: "warning", alert.KindVolumeFull: "warning",
		alert.KindServiceUnhealthy: "warning", alert.KindPanelCertificate: "warning",
		alert.KindSecurityUpdates: "warning", alert.KindRebootRequired: "warning", alert.KindDockerAdvisory: "warning",
		// One-offs, stored resolved: never an open alert, so never here.
		alert.KindDeployFailed: "", alert.KindOOMKilled: "", alert.KindPanelUpgrade: "",
		"a_kind_from_later": "warning",
	}
	for k, want := range alerts {
		if want == "" {
			continue
		}
		if got := severityOf(Item{Kind: "alert", AlertKind: string(k)}); got != want {
			t.Errorf("alert %s: %s, want %s", k, got, want)
		}
	}
	items := map[string]string{
		"deploy_failed": "problem", "upgrade_failed": "problem", "agent_approval": "waiting",
		"exposed_tool": "warning", "setup_login": "warning", "https_redeploy": "warning", "no_backup": "warning", "no_watchdog": "warning", "kit_out_of_date": "warning",
		"preview_approval": "request", "assigned": "request", "due": "request", "stale_page": "request",
	}
	for k, want := range items {
		if got := severityOf(Item{Kind: k}); got != want {
			t.Errorf("%s: %s, want %s", k, got, want)
		}
	}
}

// A panel with no watchdog is an operator's to fix, nobody else's; set up, it
// is gone. A read that failed says nothing rather than claiming there is none.
func TestNoWatchdog(t *testing.T) {
	find := func(inst fakeInstance, user string) (Item, bool) {
		t.Helper()
		items, err := NewService(newFake(), nil, inst, nil, zap.NewNop()).NeedsYou(context.Background(), "o1", user, "owner", MarksOpen)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range items {
			if it.Kind == "no_watchdog" {
				return it, true
			}
		}
		return Item{}, false
	}
	it, ok := find(fakeInstance{noWatchdog: true}, "op")
	if !ok {
		t.Fatal("an operator with no watchdog: no row")
	}
	if it.Key != "no_watchdog" || it.Category != "servers" || it.Title != "The panel host" || it.Detail != "Nobody hears if this server goes down" ||
		it.Severity != "warning" || !it.Snoozes || it.At.IsZero() {
		t.Errorf("row: %+v", it)
	}
	if _, ok := find(fakeInstance{noWatchdog: true}, "ann"); ok {
		t.Error("someone who isn't an operator got the row")
	}
	if _, ok := find(fakeInstance{}, "op"); ok {
		t.Error("a watchdog is set up, and the row is there")
	}
	if _, ok := find(fakeInstance{watchdogErr: errors.New("settings unreadable")}, "op"); ok {
		t.Error("a failed read claimed there is no watchdog")
	}
}

// The recovery kit an operator stored no longer restores the panel (D-163):
// every operator has the row until the current kit is stored, and putting it
// off holds only for the kit it was about.
func TestKitOutOfDate(t *testing.T) {
	stored := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	find := func(inst fakeInstance, user string) (Item, bool) {
		t.Helper()
		items, err := NewService(newFake(), nil, inst, nil, zap.NewNop()).NeedsYou(context.Background(), "o1", user, "owner", MarksOpen)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range items {
			if it.Kind == "kit_out_of_date" {
				return it, true
			}
		}
		return Item{}, false
	}
	stale := fakeInstance{setup: &instance.Setup{KitAcknowledgedAt: &stored, KitOutOfDate: true}}
	it, ok := find(stale, "op")
	if !ok {
		t.Fatal("an operator whose kit is out of date: no row")
	}
	if it.Key != "kit_out_of_date:1792314000" || it.Category != "servers" || it.Title != "This panel" ||
		it.Detail != "The encryption key or the panel host’s backup settings changed after the recovery kit was stored. A restore with that kit fails." ||
		it.Severity != "warning" || !it.Snoozes || it.At.IsZero() {
		t.Errorf("row: %+v", it)
	}
	if _, ok := find(stale, "ann"); ok {
		t.Error("someone who isn't an operator got the row")
	}
	if _, ok := find(fakeInstance{setup: &instance.Setup{KitAcknowledgedAt: &stored}}, "op"); ok {
		t.Error("the stored kit is current, and the row is there")
	}
	if _, ok := find(fakeInstance{setupErr: errors.New("settings unreadable")}, "op"); ok {
		t.Error("a failed read claimed the kit is out of date")
	}

	// Put off, then a kit stored later goes out of date: its row is open.
	repo := newFake()
	repo.done["op"] = map[string]time.Time{it.Key: time.Now()}
	open := func(inst fakeInstance) int {
		t.Helper()
		items, err := NewService(repo, nil, inst, nil, zap.NewNop()).NeedsYou(context.Background(), "o1", "op", "owner", MarksOpen)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, it := range items {
			if it.Kind == "kit_out_of_date" {
				n++
			}
		}
		return n
	}
	if open(stale) != 0 {
		t.Error("put off a moment ago: still open")
	}
	later := stored.Add(24 * time.Hour)
	if open(fakeInstance{setup: &instance.Setup{KitAcknowledgedAt: &later, KitOutOfDate: true}}) != 1 {
		t.Error("the mark on one kit put off the next kit's row")
	}
}

// A panel upgrade that failed is an operator's problem until a later
// upgrade succeeds (its run replaces the failed one) or they mark it done;
// one running or succeeded isn't, nor is it anyone else's.
func TestUpgradeFailed(t *testing.T) {
	started := time.Date(2026, 11, 7, 14, 0, 0, 0, time.UTC)
	ended := started.Add(3 * time.Minute)
	run := func(outcome string) fakeInstance {
		return fakeInstance{upgrade: &instance.UpgradeStatus{Install: instance.InstallInstaller, Current: "v0.39.0",
			Run: &instance.UpgradeRun{To: "v0.40.0", From: "v0.39.0", StartedAt: started, FinishedAt: &ended, Outcome: outcome, Error: "v0.40.0 not healthy: rolled back"}}}
	}
	needs := func(inst fakeInstance, user string) []Item {
		t.Helper()
		items, err := NewService(newFake(), nil, inst, nil, zap.NewNop()).NeedsYou(context.Background(), "o1", user, "owner", MarksOpen)
		if err != nil {
			t.Fatal(err)
		}
		var out []Item
		for _, it := range items {
			if it.Kind == "upgrade_failed" {
				out = append(out, it)
			}
		}
		return out
	}
	got := needs(run("rolled_back"), "op")
	if len(got) != 1 {
		t.Fatalf("rolled back: %d items, want 1", len(got))
	}
	want := Item{Key: "upgrade_failed:v0.40.0:" + strconv.FormatInt(started.Unix(), 10), Kind: "upgrade_failed", Category: "servers",
		Title: "Upgrade to v0.40.0 failed and rolled back", Detail: "v0.39.0 is running again", Error: "v0.40.0 not healthy: rolled back", At: ended, Severity: "problem"}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("rolled back:\n got %+v\nwant %+v", got[0], want)
	}
	if got := needs(run("failed"), "op"); len(got) != 1 || got[0].Title != "Upgrade to v0.40.0 failed" || got[0].Detail != "v0.39.0 kept running" {
		t.Errorf("stopped: %+v", got)
	}
	for _, outcome := range []string{"running", "succeeded"} {
		if got := needs(run(outcome), "op"); len(got) != 0 {
			t.Errorf("%s: %+v, want none", outcome, got)
		}
	}
	if got := needs(run("rolled_back"), "ann"); len(got) != 0 {
		t.Errorf("not an operator: %+v, want none", got)
	}
}

// An admin tool anyone can open at its address needs someone (phpMyAdmin,
// MailHog by image or catalog); an app with an address doesn't, and the
// query already leaves out what basic auth or an allowlist protects.
func TestExposedTools(t *testing.T) {
	p1 := "p1"
	got := exposedTools([]PublicService{
		{ID: "s1", Name: "pma", Image: "phpmyadmin/phpmyadmin:5", ProjectID: &p1, Project: "acme", Environment: "staging", Hostname: "pma.example.com"},
		{ID: "s2", Name: "mail", AddonSlug: "mailhog", Image: "mailhog/mailhog", ProjectID: &p1, Project: "acme", Hostname: "mail.example.com"},
		{ID: "s3", Name: "web", Image: "nginx:alpine", ProjectID: &p1, Project: "acme", Hostname: "www.example.com"},
		{ID: "s4", Name: "db", Image: "mysql:8.0", ProjectID: &p1, Project: "acme", Hostname: "db.example.com"},
		{ID: "s5", Name: "mailpit", AddonSlug: "mailpit", Image: "axllent/mailpit:v1.33.0", ProjectID: &p1, Project: "acme", Hostname: "mailpit.example.com"},
	})
	if len(got) != 3 {
		t.Fatalf("got %d items, want the three tools: %+v", len(got), got)
	}
	if got[2].ServiceID != "s5" {
		t.Errorf("Mailpit: %+v", got[2])
	}
	want := "Admin tool open to anyone at pma.example.com · staging: no basic auth or IP allowlist"
	if got[0].Key != "exposed_tool:s1" || got[0].Title != "acme / pma" || got[0].Detail != want {
		t.Errorf("phpMyAdmin: %+v", got[0])
	}
	if got[1].ServiceID != "s2" || got[1].Category != "deploys" {
		t.Errorf("MailHog: %+v", got[1])
	}
}

// An app whoever opens first becomes the admin of, with the temporary login
// still on, asks to set up its admin account and turn the login off; the
// query names the apps by the catalog's SetupLogin.
func TestTemporaryLogins(t *testing.T) {
	if got := setupLoginSlugs(); !slices.Equal(got, []string{"uptime-kuma", "plausible", "n8n", "ghost"}) {
		t.Errorf("setup login apps: %v", got)
	}
	p1 := "p1"
	got := temporaryLogins([]PublicService{
		{ID: "s1", Name: "blog", AddonSlug: "ghost", ProjectID: &p1, Project: "acme", Environment: "production"},
	})
	want := Item{Key: "setup_login:s1", Kind: "setup_login", Category: "deploys",
		Title:     "Set up blog’s admin account, then turn off its temporary login",
		Detail:    "acme · Ghost · production: whoever opens it first becomes its admin; the temporary login keeps others out until then",
		ProjectID: &p1, Project: "acme", ServiceID: "s1"}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// A server nothing backs up needs someone when it holds data: its
// databases, the other services' volumes and the shared volumes they mount
// (once each). A cache counts by its volumes; an app without one doesn't
// count. The panel host's row names Toft's database for an operator.
func TestNoBackups(t *testing.T) {
	since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	ctx := context.Background()
	got := noBackups(ctx, []UnbackedService{
		{ID: "s1", Image: "postgres:16", ServerID: "v1", Server: "fsn1", Volumes: 1, Since: since},
		{ID: "s2", AddonSlug: "mysql", ServerID: "v1", Server: "fsn1", Volumes: 1, Since: since.Add(time.Hour)},
		{ID: "s3", Image: "ghcr.io/acme/web", ServerID: "v1", Server: "fsn1", Volumes: 2, SharedVolumes: []string{"sv1"}},
		{ID: "s4", Image: "ghcr.io/acme/worker", ServerID: "v1", Server: "fsn1", SharedVolumes: []string{"sv1"}},
		{ID: "s5", Image: "redis:7", ServerID: "v2", Server: "hel1", Volumes: 1},
		{ID: "s6", Image: "nginx:alpine", ServerID: "v3", Server: "ams1"},
		{ID: "s7", Image: "mongo:7", Server: "", Since: since},
		{ID: "s8", ServerID: "v4", Server: "lon1", Compose: true, ComposeContent: ghostStack},
		{ID: "s9", ServerID: "v4", Server: "lon1", Compose: true, ComposeFromRepo: true},
		{ID: "s10", ServerID: "v4", Server: "lon1", Compose: true, ComposeContent: "services: [not a map"},
		{ID: "s11", ServerID: "v5", Server: "par1", Compose: true, ComposeContent: "services:\n  web:\n    image: nginx:alpine\n"},
	}, false)
	if len(got) != 4 {
		t.Fatalf("got %d items, want fsn1, hel1, the panel host and lon1: %+v", len(got), got)
	}
	if got[0].Title != "fsn1" || got[0].Detail != "2 databases, 3 volumes, no backups" || got[0].ServerID != "v1" ||
		got[0].Kind != "no_backup" || got[0].Category != "servers" || !got[0].At.Equal(since) || got[0].Key != "no_backup:v1:false:2:3:0" {
		t.Errorf("fsn1: %+v", got[0])
	}
	if got[1].Title != "hel1" || got[1].Detail != "1 volume, no backups" {
		t.Errorf("a cache with a volume: %+v", got[1])
	}
	if got[2].Title != "The panel host" || got[2].ServerID != "" || got[2].Detail != "1 database, no backups" {
		t.Errorf("the panel host: %+v", got[2])
	}
	// The inline stack: MySQL, and the content volume its two other services
	// share and an uploads bind mount; the repository's stack and the one
	// that doesn't load may hold anything.
	if got[3].Title != "lon1" || got[3].Detail != "1 database, 2 volumes, 2 compose stacks, no backups" {
		t.Errorf("compose stacks: %+v", got[3])
	}

	op := noBackups(ctx, nil, true)
	if len(op) != 1 || op[0].Detail != "Toft’s database, no backups" || op[0].Key != "no_backup:panel:true:0:0:0" || op[0].At.IsZero() {
		t.Errorf("an operator, nothing of the workspace's on the panel host: %+v", op)
	}
	if got := noBackups(ctx, []UnbackedService{{ID: "s6", Image: "nginx", ServerID: "v3", Server: "ams1"}}, false); len(got) != 0 {
		t.Errorf("no data: %+v", got)
	}
}

// ghostStack is an inline compose stack with a database, a named volume two
// services share, and a bind mount.
const ghostStack = `services:
  ghost:
    image: ghost:5
    volumes:
      - content:/var/lib/ghost/content
      - /srv/ghost/uploads:/uploads
  worker:
    image: ghost:5
    volumes:
      - content:/content
  db:
    image: mysql:8
    volumes:
      - db:/var/lib/mysql
volumes:
  content:
  db:
`
