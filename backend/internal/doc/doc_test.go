package doc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/apispec"
	"github.com/Nickpap21/toft/backend/internal/code"
	"github.com/Nickpap21/toft/backend/internal/inbox"
	"github.com/Nickpap21/toft/backend/internal/issue"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const (
	api    = "aaaaaaaa-0000-4000-8000-000000000001" // p1's
	db     = "aaaaaaaa-0000-4000-8000-000000000002" // p1's
	other  = "aaaaaaaa-0000-4000-8000-000000000003" // p2's
	issue7 = "bbbbbbbb-0000-4000-8000-000000000007"
	pull3  = "cccccccc-0000-4000-8000-000000000003"
)

// fakeRepo keeps pages in memory, with the services, issues and pull
// requests they name.
type fakeRepo struct {
	n        int
	pages    map[string]*stored
	versions map[string][]VersionDetail
	seen     map[string]map[string][]byte // page → service → what it saw
	refs     map[string]links
	services map[string]serviceNow // as they are now, by id
	owner    map[string]string     // service → project
}

func newFake() *fakeRepo {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	return &fakeRepo{pages: map[string]*stored{}, versions: map[string][]VersionDetail{}, seen: map[string]map[string][]byte{}, refs: map[string]links{},
		services: map[string]serviceNow{
			api: {ServiceID: api, Name: "api", Status: "running", SourceType: "git", Port: 8080, ServerID: "s1", ServerName: "fsn1",
				Vars: map[string]varSeen{"STRIPE_KEY": {At: now, Secret: true}}},
			db:    {ServiceID: db, Name: "postgres", Status: "running", SourceType: "docker-image", DockerImage: "postgres:16.2", Vars: map[string]varSeen{}},
			other: {ServiceID: other, Name: "web", SourceType: "git", Vars: map[string]varSeen{}},
		},
		owner: map[string]string{api: "p1", db: "p1", other: "p2"}}
}

func (f *fakeRepo) CountPages(_ context.Context, project string) (int, error) {
	n := 0
	for _, p := range f.pages {
		if p.ProjectID == project && p.DeletedAt == nil {
			n++
		}
	}
	return n, nil
}
func (f *fakeRepo) Tree(_ context.Context, project string, deleted bool) ([]TreePage, error) {
	out := []TreePage{}
	for _, p := range f.pages {
		if p.ProjectID == project && (p.DeletedAt != nil) == deleted {
			t := TreePage{ID: p.ID, Title: p.Title, Position: p.Position, UpdatedAt: p.UpdatedAt, DeletedAt: p.DeletedAt,
				Runbook: strings.Contains(p.Body, "```toft-run")}
			if p.ParentID != "" {
				parent := p.ParentID
				t.ParentID = &parent
			}
			out = append(out, t)
		}
	}
	slices.SortFunc(out, func(a, b TreePage) int { return a.Position - b.Position })
	return out, nil
}
func (f *fakeRepo) Page(_ context.Context, project, id string) (*stored, error) {
	p, ok := f.pages[id]
	if !ok || p.ProjectID != project {
		return nil, ErrNotFound
	}
	c := *p
	return &c, nil
}
func (f *fakeRepo) Path(_ context.Context, id string) ([]code.DocRef, error) {
	out := []code.DocRef{}
	for p := f.pages[f.pages[id].ParentID]; p != nil; p = f.pages[p.ParentID] {
		out = append([]code.DocRef{{ID: p.ID, Title: p.Title, ProjectID: p.ProjectID}}, out...)
	}
	return out, nil
}
func (f *fakeRepo) link(page string, l links) {
	keep := map[string][]byte{}
	for _, s := range l.Services {
		if b, ok := f.seen[page][s]; ok {
			keep[s] = b
		} else {
			keep[s], _ = json.Marshal(f.services[s].seen())
		}
	}
	f.seen[page] = keep
	f.refs[page] = l
}
func (f *fakeRepo) Create(_ context.Context, p newPage) (string, error) {
	f.n++
	id := fmt.Sprintf("dddddddd-0000-4000-8000-%012d", f.n)
	pos := 0
	for _, o := range f.pages {
		if o.ProjectID == p.ProjectID && o.ParentID == p.ParentID {
			pos = max(pos, o.Position+1)
		}
	}
	now := time.Now()
	f.pages[id] = &stored{ID: id, ProjectID: p.ProjectID, ParentID: p.ParentID, Title: p.Title, Body: p.Body, Version: 1, Position: pos,
		CreatedByID: p.ActorID, CreatedBy: p.ActorID, UpdatedByID: p.ActorID, UpdatedBy: p.ActorID, CreatedAt: now, UpdatedAt: now}
	f.versions[id] = []VersionDetail{{Version: Version{Version: 1, Title: p.Title, CreatedAt: now}, Body: p.Body}}
	f.link(id, p.Links)
	return id, nil
}
func (f *fakeRepo) Save(_ context.Context, s savePage) (int, error) {
	p := f.pages[s.ID]
	if p.Version != s.Base {
		return 0, ErrConflict
	}
	p.Version++
	p.Title, p.Body, p.UpdatedByID, p.UpdatedBy = s.Title, s.Body, s.ActorID, s.ActorID
	f.versions[s.ID] = append(f.versions[s.ID], VersionDetail{Version: Version{Version: p.Version, Title: s.Title, CreatedAt: time.Now()}, Body: s.Body})
	f.link(s.ID, s.Links)
	return p.Version, nil
}
func (f *fakeRepo) Move(_ context.Context, project, id, parent string, position int) error {
	sibs := []*stored{}
	for _, o := range f.pages {
		if o.ProjectID == project && o.ParentID == parent && o.ID != id && o.DeletedAt == nil {
			sibs = append(sibs, o)
		}
	}
	slices.SortFunc(sibs, func(a, b *stored) int { return a.Position - b.Position })
	position = min(max(position, 0), len(sibs))
	sibs = slices.Insert(sibs, position, f.pages[id])
	for i, o := range sibs {
		o.ParentID, o.Position = parent, i
	}
	return nil
}
func (f *fakeRepo) LiveChildren(_ context.Context, id string) (int, error) {
	n := 0
	for _, o := range f.pages {
		if o.ParentID == id && o.DeletedAt == nil {
			n++
		}
	}
	return n, nil
}
func (f *fakeRepo) Delete(_ context.Context, id string) (bool, error) {
	now := time.Now()
	f.pages[id].DeletedAt = &now
	return true, nil
}
func (f *fakeRepo) Restore(_ context.Context, id string) (bool, error) {
	p := f.pages[id]
	if p.DeletedAt == nil {
		return false, nil
	}
	p.DeletedAt = nil
	if par, ok := f.pages[p.ParentID]; !ok || par.DeletedAt != nil {
		p.ParentID = ""
	}
	return true, nil
}
func (f *fakeRepo) Purge(context.Context) (int, error) { return 0, nil }
func (f *fakeRepo) Verify(_ context.Context, id, actor string) error {
	for s := range f.seen[id] {
		f.seen[id][s], _ = json.Marshal(f.services[s].seen())
	}
	now := time.Now()
	f.pages[id].VerifiedAt, f.pages[id].VerifiedByID, f.pages[id].Verifier = &now, actor, actor
	return nil
}
func (f *fakeRepo) Versions(_ context.Context, id string, before, limit int) ([]Version, error) {
	out := []Version{}
	vs := f.versions[id]
	for i := len(vs) - 1; i >= 0 && len(out) < limit; i-- {
		if before == 0 || vs[i].Version.Version < before {
			out = append(out, vs[i].Version)
		}
	}
	return out, nil
}
func (f *fakeRepo) Version(_ context.Context, id string, n int) (*VersionDetail, error) {
	for _, v := range f.versions[id] {
		if v.Version.Version == n {
			return &v, nil
		}
	}
	return nil, ErrNotFound
}
func (f *fakeRepo) ProjectServices(_ context.Context, project string, svcs []string) ([]string, error) {
	out := []string{}
	for _, s := range svcs {
		if f.owner[s] == project {
			out = append(out, s)
		}
	}
	return out, nil
}
func (f *fakeRepo) ServiceStates(_ context.Context, pageIDs []string, project string) ([]serviceNow, error) {
	out := []serviceNow{}
	for page, m := range f.seen {
		if !slices.Contains(pageIDs, page) && (project == "" || f.pages[page].ProjectID != project) {
			continue
		}
		for s, b := range m {
			sn := f.services[s]
			sn.PageID, sn.Seen = page, b
			out = append(out, sn)
		}
	}
	return out, nil
}
func (f *fakeRepo) ResolveIssues(_ context.Context, _ string, refs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, r := range refs {
		if r == "STO-7" || r == "OLD-7" {
			out[r] = issue7
		}
	}
	return out, nil
}
func (f *fakeRepo) ResolvePulls(_ context.Context, _ string, refs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, r := range refs {
		if r == "storefront!3" {
			out[r] = pull3
		}
	}
	return out, nil
}
func (f *fakeRepo) RepositoryNames(context.Context, string) ([]string, error) {
	return []string{"storefront"}, nil
}
func (f *fakeRepo) ExistingPages(_ context.Context, project string, pageIDs []string) ([]string, error) {
	out := []string{}
	for _, id := range pageIDs {
		if p, ok := f.pages[id]; ok && p.ProjectID == project {
			out = append(out, id)
		}
	}
	return out, nil
}
func (f *fakeRepo) Issues(_ context.Context, issueIDs []string) (map[string]Issue, error) {
	out := map[string]Issue{}
	if slices.Contains(issueIDs, issue7) {
		out[issue7] = Issue{Key: "STO-7", Number: 7, Title: "Restore drill", Status: "in_progress", ProjectID: "p1", Assignee: &Person{ID: "dev", Name: "Dev"}}
	}
	return out, nil
}
func (f *fakeRepo) Pulls(_ context.Context, pullIDs []string) (map[string]Pull, error) {
	out := map[string]Pull{}
	if slices.Contains(pullIDs, pull3) {
		out[pull3] = Pull{Repository: "storefront", RepositoryID: "r1", Number: 3, Title: "Faster restore", State: "open", Checks: "success"}
	}
	return out, nil
}
func (f *fakeRepo) Referencing(_ context.Context, issueID, pullID, pageID string) ([]code.DocRef, error) {
	out := []code.DocRef{}
	for page, l := range f.refs {
		p := f.pages[page]
		if p.DeletedAt == nil && (issueID != "" && slices.Contains(l.Issues, issueID) || pullID != "" && slices.Contains(l.Pulls, pullID) ||
			pageID != "" && slices.Contains(l.Pages, pageID)) {
			out = append(out, code.DocRef{ID: p.ID, Title: p.Title, ProjectID: p.ProjectID})
		}
	}
	return out, nil
}
func (f *fakeRepo) Search(_ context.Context, _, project, tsq string, _ int) ([]issue.DocHit, error) {
	out := []issue.DocHit{}
	for _, p := range f.pages {
		if p.DeletedAt == nil && (project == "" || p.ProjectID == project) && strings.Contains(strings.ToLower(p.Title+" "+p.Body), strings.TrimSuffix(tsq, ":*")) {
			out = append(out, issue.DocHit{ID: p.ID, ProjectID: p.ProjectID, Project: p.ProjectID, Title: p.Title, Snippet: p.Body})
		}
	}
	return out, nil
}
func (f *fakeRepo) ServiceForWork(_ context.Context, id string) (*workService, error) {
	s, ok := f.services[id]
	if !ok {
		return nil, ErrNotFound
	}
	return &workService{ID: id, Name: s.Name, ProjectID: f.owner[id], OrgID: "o1", RepositoryID: "r1", Branch: "main", Dir: "services/api"}, nil
}
func (f *fakeRepo) ServiceIssues(_ context.Context, _, label string) ([]Issue, error) {
	if label == "api" {
		return []Issue{{Key: "STO-9", Number: 9, Title: "Webhook retries", Status: "todo", Priority: "urgent", ProjectID: "p1"}}, nil
	}
	return []Issue{}, nil
}
func (f *fakeRepo) ServicePulls(context.Context, string, string) ([]workPull, error) {
	return []workPull{{Pull: Pull{Repository: "storefront", RepositoryID: "r1", Number: 3, Title: "Faster restore", State: "open"}, HeadSHA: "touches"},
		{Pull: Pull{Repository: "storefront", RepositoryID: "r1", Number: 4, Title: "Docs only", State: "open"}, HeadSHA: "elsewhere"}}, nil
}
func (f *fakeRepo) ServicePages(_ context.Context, svc string) ([]WorkPage, error) {
	out := []WorkPage{}
	for page, m := range f.seen {
		if _, ok := m[svc]; ok && f.pages[page].DeletedAt == nil {
			out = append(out, WorkPage{ID: page, Title: f.pages[page].Title, VerifiedAt: f.pages[page].VerifiedAt})
		}
	}
	return out, nil
}

func (f *fakeRepo) PagesBy(_ context.Context, _, user string) ([]inbox.StalePage, error) {
	out := []inbox.StalePage{}
	for _, p := range f.pages {
		if p.DeletedAt == nil && p.CreatedByID == user {
			since := p.CreatedAt
			if p.VerifiedAt != nil {
				since = *p.VerifiedAt
			}
			out = append(out, inbox.StalePage{ID: p.ID, ProjectID: p.ProjectID, Project: p.ProjectID, Title: p.Title, Since: since})
		}
	}
	return out, nil
}

type fakeKeys struct{}

func (fakeKeys) IssueKeys(context.Context, string) []string { return []string{"STO", "OLD"} }

type fakeChanges struct{}

func (fakeChanges) ChangesUnder(_ context.Context, _, _, head, dir string) (bool, error) {
	return head == "touches" && dir == "services/api", nil
}

// fakeExec runs nothing: it says what it would have, and only operators
// reach a host.
type fakeExec struct{ ran *[]string }

func (fakeExec) AuthorizeHostExec(_ context.Context, actor string) error {
	if actor != "op" {
		return fmt.Errorf("not an operator")
	}
	return nil
}
func (e fakeExec) ExecInService(_ context.Context, _, id, container, command string, logFn func(string)) error {
	*e.ran = append(*e.ran, id+"|"+container+"|"+command)
	logFn("ran " + command)
	return nil
}

type projectsIn map[string]string

func (p projectsIn) CheckOwnership(_ context.Context, projectID, orgID string) (bool, error) {
	return p[projectID] == orgID, nil
}

type audits struct{ actions *[]string }

func (a audits) WriteAuditLogDetail(_ context.Context, _, _, action, _ string, d map[string]any) {
	*a.actions = append(*a.actions, fmt.Sprintf("%s %v", action, d["outcome"]))
}

func TestDocsAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := newFake()
	var ran, actions []string
	svc := NewService(repo, fakeKeys{}, fakeChanges{}, fakeExec{&ran}, zap.NewNop())
	router := func(user, role string, apiKey, reauth bool) http.Handler {
		r := gin.New()
		NewHandler(svc, projectsIn{"p1": "o1", "p2": "o1"}, audits{&actions}, zap.NewNop()).RegisterRoutes(r.Group("/api/v1", func(c *gin.Context) {
			c.Set("userID", user)
			c.Set("orgID", "o1")
			c.Set("memberRole", role)
			if apiKey {
				c.Set("apiKey", true)
			}
			if reauth {
				c.Set("reauthenticated", true)
			}
			c.Next()
		}))
		return r
	}
	dev, viewer, op := router("dev", "developer", false, false), router("viewer", "viewer", false, false), router("op", "owner", false, true)
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
		if strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
			out["stream"] = w.Body.String()
			return w.Code, out
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s %s: %v\n%s", method, path, err, w.Body)
		}
		apispec.Check(t, method, strings.SplitN(path, "?", 2)[0], w.Code, out)
		return w.Code, out
	}
	data := func(m map[string]any) map[string]any { d, _ := m["data"].(map[string]any); return d }
	base := "/api/v1/projects/p1/docs"

	// A folder page, and a runbook in it naming two services, an issue by a
	// former key, a pull request and the folder.
	code, out := call(dev, "POST", base, map[string]any{"title": "Runbooks"})
	if code != 201 {
		t.Fatalf("create: %d %v", code, out)
	}
	folder := data(out)["id"].(string)
	body := "Restore after OLD-7 and !3.\n\n```toft-service " + db + "\n```\n\n```toft-run " + api + "\ndocker stop api\n```\n\n" +
		"```toft-run " + db + " host\nls /backups\n```\n\n```toft-run " + other + "\nnot ours\n```\n\n```toft-issue STO-7\n```\n\n" +
		"```toft-pull storefront!3\n```\n\nBack to [the folder](page:" + folder + ").\n"
	code, out = call(dev, "POST", base, map[string]any{"title": "Restore Postgres", "body": body, "parent_id": folder})
	if code != 201 {
		t.Fatalf("create the runbook: %d %v", code, out)
	}
	page := data(out)["id"].(string)
	p := data(out)
	if p["version"].(float64) != 1 || p["stale"] != false || len(p["services"].([]any)) != 2 || len(p["path"].([]any)) != 1 {
		t.Errorf("the runbook: %v", p)
	}
	blocks := p["blocks"].([]any)
	if len(blocks) != 6 || blocks[4].(map[string]any)["issue"].(map[string]any)["status"] != "in_progress" ||
		blocks[5].(map[string]any)["pull"].(map[string]any)["checks"] != "success" || blocks[2].(map[string]any)["host"] != true {
		t.Errorf("blocks: %v", blocks)
	}
	if code, _ := call(viewer, "POST", base, map[string]any{"title": "x"}); code != 403 {
		t.Errorf("a viewer wrote: %d", code)
	}
	if code, _ := call(dev, "POST", base, map[string]any{"title": "x", "parent_id": "dddddddd-0000-4000-8000-999999999999"}); code != 400 {
		t.Errorf("a missing parent: %d", code)
	}

	// What names what.
	if refs, _ := svc.IssueDocs(context.Background(), issue7); len(refs) != 1 || refs[0].ID != page {
		t.Errorf("the issue's docs: %v", refs)
	}
	if refs, _ := svc.PullDocs(context.Background(), pull3); len(refs) != 1 {
		t.Errorf("the pull request's docs: %v", refs)
	}
	_, out = call(viewer, "GET", base+"/"+folder, nil)
	if by := data(out)["referenced_by"].([]any); len(by) != 1 || by[0].(map[string]any)["id"] != page {
		t.Errorf("referenced by: %v", by)
	}

	// The image changes and a secret is rewritten: the page is out of date
	// until verified.
	s := repo.services[db]
	s.DockerImage = "postgres:16.4"
	repo.services[db] = s
	a := repo.services[api]
	a.Vars = map[string]varSeen{"STRIPE_KEY": {At: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC), Secret: true}}
	repo.services[api] = a
	_, out = call(viewer, "GET", base+"/"+page, nil)
	if data(out)["stale"] != true {
		t.Fatalf("not stale: %v", data(out))
	}
	var fields []string
	for _, sv := range data(out)["services"].([]any) {
		for _, ch := range sv.(map[string]any)["changes"].([]any) {
			fields = append(fields, ch.(map[string]any)["field"].(string))
		}
	}
	slices.Sort(fields)
	if !slices.Equal(fields, []string{"image", "secret"}) {
		t.Errorf("changes: %v", fields)
	}
	_, out = call(viewer, "GET", base, nil)
	for _, tp := range out["data"].([]any) {
		if m := tp.(map[string]any); m["id"] == page && (m["stale"] != true || m["runbook"] != true) {
			t.Errorf("the tree's runbook: %v", m)
		}
	}
	_, out = call(viewer, "GET", "/api/v1/services/"+db+"/work", nil)
	if pages := data(out)["pages"].([]any); len(pages) != 1 || pages[0].(map[string]any)["stale"] != true {
		t.Errorf("the service's runbooks: %v", data(out))
	}
	if code, out := call(dev, "POST", base+"/"+page+"/verify", nil); code != 200 || data(out)["stale"] != false || data(out)["verified_by"] == nil {
		t.Errorf("verify: %d %v", code, out)
	}

	// Saving: the next version, from the current one only.
	if code, out := call(dev, "PATCH", base+"/"+page, map[string]any{"body": body + "\nMore."}); code != 400 || data(out)["code"] != "bad_version" {
		t.Errorf("a save without base_version: %d %v", code, out)
	}
	if code, out := call(dev, "PATCH", base+"/"+page, map[string]any{"body": body + "\nMore.", "base_version": 1}); code != 200 || data(out)["version"].(float64) != 2 {
		t.Fatalf("save: %d %v", code, out)
	}
	if code, out := call(dev, "PATCH", base+"/"+page, map[string]any{"title": "Late", "base_version": 1}); code != 409 || data(out)["code"] != "version_conflict" {
		t.Errorf("a stale save: %d %v", code, out)
	}
	_, out = call(viewer, "GET", base+"/"+page+"/versions?limit=1", nil)
	if vs := out["data"].([]any); len(vs) != 1 || vs[0].(map[string]any)["version"].(float64) != 2 || out["next"] != "2" {
		t.Errorf("versions: %v", out)
	}
	if code, out := call(viewer, "GET", base+"/"+page+"/versions/1", nil); code != 200 || data(out)["body"] != body {
		t.Errorf("version 1: %d %v", code, out)
	}

	// Runs: the saved version's commands, by number.
	if code, out := call(viewer, "POST", base+"/"+page+"/steps/0/run", map[string]any{"version": 2}); code != 403 {
		t.Errorf("a viewer ran: %d %v", code, out)
	}
	if code, _ := call(router("dev", "developer", true, false), "POST", base+"/"+page+"/steps/0/run", map[string]any{"version": 2}); code != 403 {
		t.Errorf("an API key ran: %d", code)
	}
	if code, out := call(dev, "POST", base+"/"+page+"/steps/0/run", map[string]any{"version": 1}); code != 409 {
		t.Errorf("an old version ran: %d %v", code, out)
	}
	if code, out := call(dev, "POST", base+"/"+page+"/steps/0/run", map[string]any{"version": 2}); code != 200 || !strings.Contains(out["stream"].(string), "ran docker stop api") {
		t.Errorf("run: %d %v", code, out)
	}
	if code, out := call(dev, "POST", base+"/"+page+"/steps/1/run", map[string]any{"version": 2}); code != 403 || data(out)["code"] != "operators_only" {
		t.Errorf("a developer on the host: %d %v", code, out)
	}
	if code, _ := call(router("op", "owner", false, false), "POST", base+"/"+page+"/steps/1/run", map[string]any{"version": 2}); code != 403 {
		t.Errorf("an operator on the host without the password: %d", code)
	}
	if code, out := call(op, "POST", base+"/"+page+"/steps/1/run", map[string]any{"version": 2}); code != 200 || !strings.Contains(out["stream"].(string), "ran ls /backups") {
		t.Errorf("an operator on the host: %d %v", code, out)
	}
	for _, step := range []string{"2", "9"} { // another project's service; none
		if code, _ := call(dev, "POST", base+"/"+page+"/steps/"+step+"/run", map[string]any{"version": 2}); code != 404 {
			t.Errorf("step %s: %d", step, code)
		}
	}
	if !slices.Equal(ran, []string{api + "||docker stop api", db + "|@server|ls /backups"}) {
		t.Errorf("ran: %v", ran)
	}
	if !slices.Equal(actions, []string{"doc.step_run ok", "doc.step_run ok"}) {
		t.Errorf("audit: %v", actions)
	}

	// Moving, deleting and restoring.
	code, out = call(dev, "POST", base, map[string]any{"title": "Onboarding"})
	onboarding := data(out)["id"].(string)
	if code, out := call(dev, "PATCH", base+"/"+folder, map[string]any{"parent_id": page}); code != 400 || data(out)["code"] != "bad_parent" {
		t.Errorf("a page into its own page: %d %v", code, out)
	}
	if code, out := call(dev, "PATCH", base+"/"+onboarding, map[string]any{"parent_id": folder, "position": 0}); code != 200 || parentOf(out) != folder {
		t.Errorf("move: %d %v", code, out)
	}
	if repo.pages[onboarding].Position != 0 || repo.pages[page].Position != 1 {
		t.Errorf("positions: %d %d", repo.pages[onboarding].Position, repo.pages[page].Position)
	}
	if code, out := call(dev, "DELETE", base+"/"+folder, nil); code != 409 || data(out)["code"] != "has_pages" {
		t.Errorf("delete a page with pages: %d %v", code, out)
	}
	if code, _ := call(dev, "DELETE", base+"/"+onboarding, nil); code != 204 {
		t.Errorf("delete: %d", code)
	}
	_, out = call(viewer, "GET", base+"?deleted=true", nil)
	if len(out["data"].([]any)) != 1 {
		t.Errorf("deleted pages: %v", out)
	}
	if code, out := call(dev, "POST", base+"/"+onboarding+"/restore", nil); code != 200 || data(out)["deleted_at"] != nil {
		t.Errorf("restore: %d %v", code, out)
	}

	// Connected work and search.
	_, out = call(viewer, "GET", "/api/v1/services/"+api+"/work", nil)
	w := data(out)
	if len(w["pulls"].([]any)) != 1 || len(w["issues"].([]any)) != 1 || len(w["pages"].([]any)) != 1 {
		t.Errorf("work: %v", w)
	}
	if hits, _ := svc.SearchDocs(context.Background(), "o1", "p1", "postgres:*", 8); len(hits) != 1 || hits[0].ID != page {
		t.Errorf("search: %v", hits)
	}
	if code, _ := call(viewer, "GET", "/api/v1/projects/p3/docs", nil); code != 404 {
		t.Errorf("another workspace's project: %d", code)
	}
}

func parentOf(out map[string]any) string {
	d, _ := out["data"].(map[string]any)
	s, _ := d["parent_id"].(string)
	return s
}

// TestChanges says what counts as a service changing (D-86).
func TestChanges(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	base := serviceNow{SourceType: "git", Port: 8080, ServerID: "s1", ServerName: "fsn1", LiveCommit: "abc", Vars: map[string]varSeen{"A": {At: at}}}
	was := base.seen()
	moved := base
	moved.LiveCommit = "def" // a push: not a change
	if c := changes(was, moved.seen()); len(c) != 0 {
		t.Errorf("a commit changed it: %v", c)
	}
	moved.Port, moved.ServerID, moved.ServerName = 9090, "", ""
	moved.Vars = map[string]varSeen{"B": {At: at, Secret: true}}
	c := changes(was, moved.seen())
	if len(c) != 4 || c[0].Field != "build" || c[1].Field != "server" || *c[1].To != "the panel's server" ||
		c[2].Name != "A" || c[2].To != nil || c[3].Field != "secret" || c[3].From != nil {
		t.Errorf("changes: %+v", c)
	}
	if b := parseBlock("toft-run not-a-uuid", "ls"); b.Kind != "unknown" {
		t.Errorf("a bad run block: %+v", b)
	}
	if b := parseBlock("toft-issue sto-012", ""); b.ref != "STO-12" {
		t.Errorf("an issue block: %+v", b)
	}
}
