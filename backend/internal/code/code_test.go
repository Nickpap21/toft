package code

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"github.com/Nickpap21/toft/backend/internal/user"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
)

func TestSlugsAndNames(t *testing.T) {
	cases := map[string]string{
		"Founders":            "founders",
		"Personal Workspace":  "personal-workspace",
		"Ada's workspace":     "adas-workspace",
		"Ada’s workspace":     "adas-workspace",
		"  Ünïcode -- shop! ": "n-code-shop",
		"!!!":                 "fallback",
		"nginx_booking.v2":    "nginx-booking-v2",
	}
	for in, want := range cases {
		if got := slugify(in, maxOrgSlug, "fallback"); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
	if got := slugify(strings.Repeat("ab-", 30), maxOrgSlug, "x"); len(got) > maxOrgSlug || strings.HasSuffix(got, "-") {
		t.Errorf("long slug %q", got)
	}
	taken := map[string]bool{"shop": true, "shop-2": true}
	if got := freeName("shop", maxOrgSlug, taken); got != "shop-3" || !taken["shop-3"] {
		t.Errorf("freeName = %q", got)
	}
	long := strings.Repeat("a", maxOrgSlug)
	if got := freeName(long, maxOrgSlug, map[string]bool{long: true}); len(got) != maxOrgSlug || !strings.HasSuffix(got, "-2") {
		t.Errorf("freeName at the limit = %q", got)
	}
}

func TestSplitPathAndSSHCommand(t *testing.T) {
	for in, want := range map[string][2]string{
		"founders/shop.git":   {"founders", "shop"},
		"/Founders/Shop":      {"founders", "shop"},
		"founders/shop/x":     {"", ""},
		"founders":            {"", ""},
		"/founders/.git":      {"", ""},
		"founders/sh\\op.git": {"", ""},
	} {
		ws, name, _ := SplitPath(in)
		if ws != want[0] || name != want[1] {
			t.Errorf("SplitPath(%q) = %q %q", in, ws, name)
		}
	}
	for in, want := range map[string][2]string{
		"git-upload-pack '/founders/shop.git'": {"upload-pack", "/founders/shop.git"},
		"git-receive-pack 'founders/shop.git'": {"receive-pack", "founders/shop.git"},
		"git-upload-pack founders/shop":        {"upload-pack", "founders/shop"},
		"git-upload-archive 'founders/shop'":   {"", ""},
		"sh -c id":                             {"", ""},
		"git-upload-pack 'a/b'; rm -rf /'":     {"", ""},
		"git-upload-pack":                      {"", ""},
	} {
		service, path, _ := ParseSSHCommand(in)
		if service != want[0] || path != want[1] {
			t.Errorf("ParseSSHCommand(%q) = %q %q", in, service, path)
		}
	}
}

func TestParseKey(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	sp, _ := ssh.NewPublicKey(pub)
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp)))
	norm, fp, typ, err := parseKey(`command="x" ` + line + " me@laptop\n")
	if err != nil || norm != line || fp != ssh.FingerprintSHA256(sp) || typ != ssh.KeyAlgoED25519 {
		t.Fatalf("ed25519: %q %q %q %v", norm, fp, typ, err)
	}
	if _, _, _, err := parseKey("not a key"); err != ErrInvalidKey {
		t.Errorf("garbage: %v", err)
	}
	if _, _, _, err := parseKey(line + "\n" + line); err != ErrInvalidKey {
		t.Errorf("two keys: %v", err)
	}
	small, _ := rsa.GenerateKey(rand.Reader, 1024)
	sr, _ := ssh.NewPublicKey(&small.PublicKey)
	if _, _, _, err := parseKey(string(ssh.MarshalAuthorizedKey(sr))); err != ErrWeakKey {
		t.Errorf("rsa 1024: %v", err)
	}
}

// fakeRepo is the code module's data in memory.
type fakeRepo struct {
	unnamed []Unnamed
	slugs   []string
	names   map[string][]string
	named   map[string]string
	repos   []NewRepo
	targets map[string]*Target // "<slug>/<name>"
	keys    map[string][2]string
	stored  []StoredRepo // every project's, for ListProjectRepos
	pulls   []*StoredPull
	items   map[string][]StoredItem // pull request id → conversation
	// D-77: the outbox, checks by "<sha>\x00<name>", and what builds from
	// the repository.
	events   []fakeEvent
	checks   map[string]StoredCheck
	order    []string
	deployTo []DeployTarget
	// D-79: mirrors, webhooks and their deliveries.
	mirrors    []*fakeMirror
	hooks      []*StoredWebhook
	deliveries []*fakeDelivery
	// D-102: agents' branches, and the branches services deploy from.
	agentBranches    map[string]*AgentBranch
	deployedBranches []string
}

type fakeEvent struct {
	GitEvent
	id         int64
	dispatched bool
	msg        string
	attempts   int
	due        time.Time
	at         time.Time
}

func (f *fakeRepo) Unnamed(context.Context) ([]Unnamed, error)   { return f.unnamed, nil }
func (f *fakeRepo) TakenSlugs(context.Context) ([]string, error) { return f.slugs, nil }
func (f *fakeRepo) TakenNames(context.Context, []string) (map[string][]string, error) {
	return f.names, nil
}
func (f *fakeRepo) Name(_ context.Context, slugs map[string]string, repos []NewRepo) error {
	f.named, f.repos, f.unnamed = slugs, repos, nil
	return nil
}
func (f *fakeRepo) ListProjectRepos(context.Context, string) ([]StoredRepo, error) {
	return f.stored, nil
}
func (f *fakeRepo) RepoByPath(_ context.Context, slug, name string) (*Target, error) {
	if t, ok := f.targets[slug+"/"+name]; ok {
		return t, nil
	}
	return nil, ErrNotFound
}
func (f *fakeRepo) ListKeys(context.Context, string) ([]SSHKey, error) { return nil, nil }
func (f *fakeRepo) CountKeys(context.Context, string) (int64, error)   { return 0, nil }
func (f *fakeRepo) AddKey(context.Context, string, string, string, string, string) (*SSHKey, error) {
	return &SSHKey{}, nil
}
func (f *fakeRepo) DeleteKey(context.Context, string, string) (*SSHKey, error) {
	return nil, ErrNotFound
}
func (f *fakeRepo) KeyOwner(_ context.Context, fp string) (string, string, error) {
	if k, ok := f.keys[fp]; ok {
		return k[0], k[1], nil
	}
	return "", "", ErrNotFound
}
func (f *fakeRepo) TouchKey(context.Context, string) error { return nil }

func (f *fakeRepo) CreateRepo(_ context.Context, _, _, name string) (*StoredRepo, error) {
	for _, r := range f.stored {
		if r.Name == name {
			return nil, ErrNameTaken
		}
	}
	r := StoredRepo{ID: uuid.NewString(), Name: name, OrgSlug: "acme", CreatedAt: time.Now()}
	f.stored = append(f.stored, r)
	return &r, nil
}
func (f *fakeRepo) DeleteRepo(_ context.Context, _, id string) (bool, error) {
	if len(f.stored) < 2 {
		return false, nil
	}
	for i, r := range f.stored {
		if r.ID == id {
			f.stored = append(f.stored[:i], f.stored[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeRepo) SetProtection(_ context.Context, id string, on bool, branch string) error {
	for i := range f.stored {
		if f.stored[i].ID == id {
			f.stored[i].Protect, f.stored[i].ProtectedBranch = on, branch
		}
	}
	for _, t := range f.targets {
		if t.ID == id {
			t.ProtectDefault, t.ProtectedBranch = on, branch
		}
	}
	return nil
}
func (f *fakeRepo) FillProtectedBranch(_ context.Context, id, branch string) error {
	for _, t := range f.targets {
		if t.ID == id && t.ProtectDefault && t.ProtectedBranch == "" {
			t.ProtectedBranch = branch
		}
	}
	return nil
}

// Pull requests in memory, in the order they were opened.
func (f *fakeRepo) OpenPull(_ context.Context, p NewPull) (*StoredPull, error) {
	for _, o := range f.pulls {
		if o.State == "open" && o.Base == p.Base && o.Head == p.Head {
			return nil, ErrPullExists
		}
	}
	sp := &StoredPull{ID: uuid.NewString(), RepoID: p.RepoID, Number: len(f.pulls) + 1, Title: p.Title, Body: p.Body,
		AuthorID: p.AuthorID, AuthorName: p.AuthorID, Base: p.Base, Head: p.Head, HeadSHA: p.HeadSHA, State: "open",
		CreatedAt: time.Now(), UpdatedAt: time.Now()}
	f.pulls = append(f.pulls, sp)
	return sp, nil
}
func (f *fakeRepo) GetPull(_ context.Context, _ string, number int) (*StoredPull, error) {
	if number < 1 || number > len(f.pulls) {
		return nil, ErrNotFound
	}
	cp := *f.pulls[number-1]
	return &cp, nil
}
func (f *fakeRepo) ListPulls(_ context.Context, _, state string, before, limit int) ([]StoredPull, error) {
	out := []StoredPull{}
	for i := len(f.pulls) - 1; i >= 0 && len(out) < limit; i-- {
		if p := f.pulls[i]; p.State == state && (before == 0 || p.Number < before) {
			out = append(out, *p)
		}
	}
	return out, nil
}
func (f *fakeRepo) CountPulls(context.Context, string) (map[string]int, error) {
	out := map[string]int{}
	for _, p := range f.pulls {
		out[p.State]++
	}
	return out, nil
}
func (f *fakeRepo) OpenPullsByHead(_ context.Context, _ string, branches []string) ([]StoredPull, error) {
	out := []StoredPull{}
	for _, p := range f.pulls {
		if p.State == "open" && slices.Contains(branches, p.Head) {
			// What ListOpenPullRequestsByHead selects, no more.
			out = append(out, StoredPull{ID: p.ID, RepoID: p.RepoID, Number: p.Number, Title: p.Title, Base: p.Base,
				Head: p.Head, HeadSHA: p.HeadSHA, State: "open"})
		}
	}
	return out, nil
}
func (f *fakeRepo) pull(id string) *StoredPull {
	for _, p := range f.pulls {
		if p.ID == id {
			return p
		}
	}
	return nil
}
func (f *fakeRepo) UpdatePullText(_ context.Context, id, title, body string) error {
	p := f.pull(id)
	p.Title, p.Body = title, body
	return nil
}
func (f *fakeRepo) SetPullState(_ context.Context, id, from, to string) (bool, error) {
	if p := f.pull(id); p.State == from {
		p.State = to
		return true, nil
	}
	return false, nil
}
func (f *fakeRepo) MarkMerged(_ context.Context, id string, m Merged) (bool, error) {
	p := f.pull(id)
	if p.State != "open" {
		return false, nil
	}
	now := time.Now()
	p.State, p.MergeMethod, p.MergeSHA, p.MergeBaseSHA, p.HeadSHA, p.MergedByID, p.MergedAt = "merged", m.Method, m.SHA, m.MergeBase, m.Head, m.By, &now
	return true, nil
}
func (f *fakeRepo) SetPullHead(_ context.Context, id, sha string) error {
	f.pull(id).HeadSHA = sha
	return nil
}
func (f *fakeRepo) AddComment(_ context.Context, pullID, authorID, body string) (*StoredComment, error) {
	c := StoredItem{Item: "comment", ID: uuid.NewString(), ActorID: authorID, ActorName: authorID, Body: body, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	f.items[pullID] = append(f.items[pullID], c)
	return &StoredComment{ID: c.ID, AuthorID: authorID, AuthorName: authorID, Body: body, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}, nil
}
func (f *fakeRepo) GetComment(_ context.Context, pullID, id string) (*StoredComment, error) {
	for _, it := range f.items[pullID] {
		if it.ID == id && it.Item == "comment" {
			return &StoredComment{ID: it.ID, AuthorID: it.ActorID, AuthorName: it.ActorName, Body: it.Body, CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt}, nil
		}
	}
	return nil, ErrNotFound
}
func (f *fakeRepo) UpdateComment(_ context.Context, id, body string) error {
	for _, items := range f.items {
		for i := range items {
			if items[i].ID == id {
				items[i].Body, items[i].UpdatedAt = body, time.Now().Add(2*time.Second)
			}
		}
	}
	return nil
}
func (f *fakeRepo) DeleteComment(_ context.Context, id string) error {
	for k, items := range f.items {
		f.items[k] = slices.DeleteFunc(items, func(it StoredItem) bool { return it.ID == id })
	}
	return nil
}
func (f *fakeRepo) AddEvent(_ context.Context, pullID, actorID, kind string, data map[string]any) error {
	b, _ := json.Marshal(data)
	f.items[pullID] = append(f.items[pullID], StoredItem{Item: "event", ID: uuid.NewString(), ActorID: actorID, ActorName: actorID, Kind: kind, Data: b, CreatedAt: time.Now()})
	return nil
}
func (f *fakeRepo) Timeline(_ context.Context, pullID string, _ time.Time, afterID string, limit int) ([]StoredItem, error) {
	items := f.items[pullID]
	if afterID != "" {
		i := slices.IndexFunc(items, func(it StoredItem) bool { return it.ID == afterID })
		items = items[i+1:]
	}
	return items[:min(limit, len(items))], nil
}

// fakeMembers: user → workspace → role; "inactive" users are deactivated.
type fakeMembers map[string]map[string]string

func (m fakeMembers) RequestIdentity(_ context.Context, userID, orgID, _ string) (middleware.Identity, error) {
	return middleware.Identity{Active: userID != "inactive", OrgID: orgID, Role: m[userID][orgID]}, nil
}

// GetByID: every account is called after its id, at example.com.
func (m fakeMembers) GetByID(_ context.Context, id string) (*user.User, error) {
	return &user.User{ID: id, Name: id, Email: id + "@example.com"}, nil
}

func TestEnsureRepositoriesNamesFromProjects(t *testing.T) {
	repo := &fakeRepo{
		unnamed: []Unnamed{
			{ProjectID: "p1", OrgID: "o1", ProjectName: "Shop", OrgName: "Founders"},
			{ProjectID: "p2", OrgID: "o1", ProjectName: "shop!", OrgName: "Founders"},
			{ProjectID: "p3", OrgID: "o2", ProjectName: "Blog", OrgName: "Founders"},
			{ProjectID: "p4", OrgID: "o3", ProjectName: "Api", OrgName: "Northwind", OrgSlug: "northwind"},
		},
		slugs: []string{"northwind"},
		names: map[string][]string{"o3": {"api"}},
	}
	// A client that is never called: naming touches only the database.
	svc := NewService(repo, gitstore.NewClient("/nonexistent"), nil, nil, 2222, zap.NewNop())
	if err := svc.EnsureRepositories(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.named["o1"] != "founders" || repo.named["o2"] != "founders-2" || len(repo.named) != 2 {
		t.Errorf("slugs: %v", repo.named)
	}
	want := map[string]string{"p1": "shop", "p2": "shop-2", "p3": "blog", "p4": "api-2"}
	for _, r := range repo.repos {
		if want[r.ProjectID] != r.Name {
			t.Errorf("%s named %q, want %q", r.ProjectID, r.Name, want[r.ProjectID])
		}
	}
}

func TestAuthorize(t *testing.T) {
	members := fakeMembers{
		"owner":    {"o1": "owner"},
		"dev":      {"o1": "developer"},
		"viewer":   {"o1": "viewer"},
		"inactive": {"o1": "owner"},
	}
	svc := NewService(&fakeRepo{}, nil, members, nil, 2222, zap.NewNop())
	target := &Target{ID: "r1", OrgID: "o1", ProjectID: "p1"}
	cases := []struct {
		name        string
		a           Actor
		read, write error
	}{
		{"owner", Actor{UserID: "owner"}, nil, nil},
		{"developer", Actor{UserID: "dev"}, nil, nil},
		{"viewer reads", Actor{UserID: "viewer"}, nil, ErrForbidden},
		{"not a member", Actor{UserID: "stranger"}, ErrNotFound, ErrNotFound},
		{"deactivated", Actor{UserID: "inactive"}, ErrNotFound, ErrNotFound},
		{"key with code_read", Actor{UserID: "dev", ProjectID: "p1", Scopes: []string{ScopeCodeRead}}, nil, ErrForbidden},
		{"key with code_write", Actor{UserID: "dev", ProjectID: "p1", Scopes: []string{ScopeCodeWrite}}, nil, nil},
		{"viewer's code_write key", Actor{UserID: "viewer", ProjectID: "p1", Scopes: []string{ScopeCodeWrite}}, nil, ErrForbidden},
		{"key for deploys only", Actor{UserID: "dev", ProjectID: "p1", Scopes: []string{"deploy"}}, ErrNotFound, ErrNotFound},
		{"key for another project", Actor{UserID: "dev", ProjectID: "p2", Scopes: []string{ScopeCodeWrite}}, ErrNotFound, ErrNotFound},
	}
	for _, c := range cases {
		if err := svc.Authorize(context.Background(), c.a, target, false); err != c.read {
			t.Errorf("%s reading: %v, want %v", c.name, err, c.read)
		}
		if err := svc.Authorize(context.Background(), c.a, target, true); err != c.write {
			t.Errorf("%s pushing: %v, want %v", c.name, err, c.write)
		}
	}
}

func (f *fakeRepo) InsertEvent(_ context.Context, e GitEvent) (int64, error) {
	id := int64(len(f.events) + 1)
	f.events = append(f.events, fakeEvent{GitEvent: e, id: id, at: time.Now(), dispatched: !e.Deploys})
	// As InsertGitEvent's statement: a delivery per subscribed webhook,
	// and the push mirrors due.
	for _, w := range f.hooks {
		if w.Active {
			for _, ev := range e.hookEvents() {
				if slices.Contains(w.Events, ev) {
					f.deliveries = append(f.deliveries, &fakeDelivery{webhookID: w.ID, gitEvent: id, StoredDelivery: StoredDelivery{
						WebhookDelivery: WebhookDelivery{ID: fakeID(len(f.deliveries) + 1), Event: ev, State: "pending", CreatedAt: time.Now()}}})
				}
			}
		}
	}
	for _, m := range f.mirrors {
		if m.Direction == "push" && e.Kind != "pull_request" {
			due := time.Now()
			m.NextSyncAt = &due
		}
	}
	return id, nil
}
func (f *fakeRepo) ClaimEvents(_ context.Context, n int) ([]ClaimedEvent, error) {
	var out []ClaimedEvent
	for i := range f.events {
		e := &f.events[i]
		if e.dispatched || time.Now().Before(e.due) || len(out) == n {
			continue
		}
		e.attempts++
		e.due = time.Now().Add(5 * time.Minute)
		number := 0
		if p := f.pull(e.PullID); p != nil {
			number = p.Number
		}
		out = append(out, ClaimedEvent{ID: e.id, RepoID: e.RepoID, ProjectID: "p1", OrgID: "o1", Kind: e.Kind, Branch: e.Branch,
			After: e.After, Subject: e.Subject, ActorID: e.ActorID, ActorName: e.ActorID, PullNumber: number, Attempts: e.attempts, CreatedAt: e.at})
	}
	return out, nil
}
func (f *fakeRepo) FinishEvent(_ context.Context, id int64, msg string) error {
	f.events[id-1].dispatched, f.events[id-1].msg = true, msg
	return nil
}
func (f *fakeRepo) RetryEvent(_ context.Context, id int64, at time.Time, msg string) error {
	f.events[id-1].due, f.events[id-1].msg = at, msg
	return nil
}
func (f *fakeRepo) DeleteOldEvents(context.Context, time.Time) (int64, error) { return 0, nil }
func (f *fakeRepo) ListEvents(_ context.Context, _, branch string, beforeID int64, n int) ([]Pipeline, error) {
	out := []Pipeline{}
	for i := len(f.events) - 1; i >= 0 && len(out) < n; i-- {
		e := f.events[i]
		// As ListGitEvents: only what moved a branch (D-79).
		if (branch != "" && e.Branch != branch) || (beforeID > 0 && e.id >= beforeID) || !e.Deploys {
			continue
		}
		p := Pipeline{ID: e.id, Kind: e.Kind, Branch: e.Branch, Before: e.Before, After: e.After, Subject: e.Subject,
			Commits: e.Commits, Forced: e.Forced, Actor: person(e.ActorID, e.ActorID), CreatedAt: e.at}
		if pr := f.pull(e.PullID); pr != nil {
			n := pr.Number
			p.PullNumber = &n
		}
		if e.dispatched {
			at := e.at
			p.DispatchedAt = &at
			if e.msg != "" {
				m := e.msg
				p.Error = &m
			}
		}
		out = append(out, p)
	}
	return out, nil
}
func (f *fakeRepo) UpsertCheck(_ context.Context, c NewCheck) error {
	if f.checks == nil {
		f.checks = map[string]StoredCheck{}
	}
	k := c.SHA + "\x00" + c.Name
	now := time.Now()
	old, ok := f.checks[k]
	if !ok {
		f.order = append(f.order, k)
		old.CreatedAt = now
	}
	sc := StoredCheck{SHA: c.SHA, CommitCheck: CommitCheck{Name: c.Name, State: c.State, Description: c.Description,
		Reporter: person(c.ReporterID, c.ReporterID), CreatedAt: old.CreatedAt, UpdatedAt: now}}
	if c.DetailsURL != "" {
		u := c.DetailsURL
		sc.DetailsURL = &u
	}
	if c.DeploymentID != "" {
		sc.Deployment = &CheckDeployment{ID: c.DeploymentID, ServiceID: "svc"}
	}
	f.checks[k] = sc
	return nil
}
func (f *fakeRepo) CheckExists(_ context.Context, _, sha, name string) (bool, error) {
	_, ok := f.checks[sha+"\x00"+name]
	return ok, nil
}
func (f *fakeRepo) CountChecks(_ context.Context, _, sha string) (int, error) {
	n := 0
	for _, c := range f.checks {
		if c.SHA == sha {
			n++
		}
	}
	return n, nil
}
func (f *fakeRepo) ListChecks(_ context.Context, _ string, shas []string) ([]StoredCheck, error) {
	out := []StoredCheck{}
	for _, k := range f.order {
		if c := f.checks[k]; slices.Contains(shas, c.SHA) {
			out = append(out, c)
		}
	}
	// As the query orders them: Toft's own first, then by name.
	slices.SortStableFunc(out, func(a, b StoredCheck) int {
		at, bt := strings.HasPrefix(a.Name, "toft/"), strings.HasPrefix(b.Name, "toft/")
		if at != bt {
			if at {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out, nil
}
func (f *fakeRepo) FailDeployChecks(context.Context, []string, string) error { return nil }
func (f *fakeRepo) DeployTargets(context.Context, string) ([]DeployTarget, error) {
	return append([]DeployTarget{}, f.deployTo...), nil
}

func (f *fakeRepo) BranchDeployed(_ context.Context, _, branch string) (bool, error) {
	return slices.Contains(f.deployedBranches, branch), nil
}
func (f *fakeRepo) AgentBranch(_ context.Context, repoID, name string) (*AgentBranch, error) {
	if b, ok := f.agentBranches[repoID+"/"+name]; ok {
		return b, nil
	}
	return nil, ErrNotFound
}
func (f *fakeRepo) AgentBranchByToken(_ context.Context, hash string) (*AgentBranch, error) {
	for _, b := range f.agentBranches {
		if b.OrgID == hash {
			return b, nil
		}
	}
	return nil, ErrNotFound
}
func (f *fakeRepo) UpsertAgentBranch(_ context.Context, repoID, name, conn, user, hash string, _ time.Time) error {
	if f.agentBranches == nil {
		f.agentBranches = map[string]*AgentBranch{}
	}
	// ponytail: the fake keeps the hash in OrgID, for AgentBranchByToken.
	f.agentBranches[repoID+"/"+name] = &AgentBranch{RepoID: repoID, Name: name, ConnectionID: conn, UserID: user, OrgID: hash}
	return nil
}
func (f *fakeRepo) AgentBranches(context.Context, string) (map[string]string, error) {
	return map[string]string{}, nil
}
