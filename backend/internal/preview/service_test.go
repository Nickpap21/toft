package preview

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// fake is every dependency of Service in memory: one project copying
// staging, whose web and api build from the repository and postgres
// doesn't.
type fake struct {
	set       Settings
	dns       bool
	pulls     map[string]*Pull
	previews  map[string]*Preview // by pull
	sources   []SourceService
	copies    map[string][]CopyService // by environment
	envs      []string
	deploys   []DeployInput
	checks    []string
	events    []string
	stopped   []string
	files     map[string]string
	ready     string
	nextID    int
	outbox    []Event
	certCount int
	changed   map[string][]ChangedFile // by head
	truncated map[string]bool          // by head: the diff was too large to list
}

func newFake() *fake {
	return &fake{
		set:      Settings{ProjectID: "proj", OrgID: "org", EnvironmentID: "staging", EnvironmentName: "staging", Max: 5},
		pulls:    map[string]*Pull{},
		previews: map[string]*Preview{},
		copies:   map[string][]CopyService{},
		files:    map[string]string{},
		sources: []SourceService{
			{ID: "web", Name: "web", SourceType: "git", RepositoryID: "repo", ServerID: "srv", Volumes: "[]", BaseDirectory: "/",
				MemoryLimit: "512m", Hostnames: []string{"web.staging.example.com"}},
			{ID: "api", Name: "api", SourceType: "git", RepositoryID: "repo", ServerID: "srv", Volumes: "[]", BaseDirectory: "/api",
				Hostnames: []string{"api.staging.example.com"}},
			{ID: "postgres", Name: "postgres", SourceType: "docker-image", ServerID: "srv", Volumes: "[]"},
		},
	}
}

func (f *fake) id(p string) string { f.nextID++; return fmt.Sprintf("%s-%d", p, f.nextID) }

// Store.
func (f *fake) Settings(context.Context, string) (Settings, error) { return f.set, nil }
func (f *fake) SetSettings(_ context.Context, _, env, domain string, max *int) error {
	f.set.EnvironmentID, f.set.Domain = env, domain
	if max != nil {
		f.set.Max = *max
	}
	return nil
}
func (f *fake) Pull(_ context.Context, id string) (Pull, error) {
	p, ok := f.pulls[id]
	if !ok {
		return Pull{}, ErrPullGone
	}
	return *p, nil
}
func (f *fake) PullID(_ context.Context, _, _ string, n int) (string, error) {
	for id, p := range f.pulls {
		if p.Number == n {
			return id, nil
		}
	}
	return "", ErrNoPull
}
func (f *fake) ByPull(_ context.Context, id string) (*Preview, error) {
	if p, ok := f.previews[id]; ok {
		c := *p
		return &c, nil
	}
	return nil, nil
}
func (f *fake) Insert(_ context.Context, p *Preview) error {
	p.ID, p.CreatedAt = f.id("pv"), time.Now()
	c := *p
	f.previews[p.PullID] = &c
	return nil
}
func (f *fake) Update(_ context.Context, p *Preview) error {
	if _, ok := f.previews[p.PullID]; !ok {
		return nil
	}
	c := *p
	f.previews[p.PullID] = &c
	return nil
}
func (f *fake) SetMigrationFiles(_ context.Context, id string, files []string, unchecked bool) error {
	for _, p := range f.previews {
		if p.ID == id {
			p.MigrationFiles, p.MigrationsUnchecked = files, unchecked
		}
	}
	return nil
}
func (f *fake) Delete(_ context.Context, id string) error {
	for k, p := range f.previews {
		if p.ID == id {
			delete(f.previews, k)
		}
	}
	return nil
}
func (f *fake) CountHolding(context.Context, string) (int, error) {
	n := 0
	for _, p := range f.previews {
		if p.State == StateActive || p.State == StateStopped || p.State == StateRemoving {
			n++
		}
	}
	return n, nil
}
func (f *fake) WaitingRoom(ctx context.Context, project string) ([]string, error) {
	return f.PullsOf(ctx, ScopeWaitingRoom, project)
}
func (f *fake) PullsOf(_ context.Context, scope Scope, _ string) ([]string, error) {
	var out []string
	for k, p := range f.previews {
		if scope != ScopeWaitingRoom || p.State == StateWaitingRoom {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out, nil
}
func (f *fake) UnappliedEvents(context.Context, int) ([]Event, error) { return f.outbox, nil }
func (f *fake) MarkApplied(_ context.Context, ids []int64) error {
	f.outbox = slices.DeleteFunc(f.outbox, func(e Event) bool { return slices.Contains(ids, e.ID) })
	return nil
}
func (f *fake) SourceServices(context.Context, string) ([]SourceService, error) {
	return f.sources, nil
}
func (f *fake) Copies(_ context.Context, env string) ([]CopyService, error) {
	return f.copies[env], nil
}
func (f *fake) List(context.Context, string, string, int) ([]Listed, string, error) {
	return nil, "", nil
}
func (f *fake) CertificatesSince(context.Context, string, time.Time) (int, error) {
	return f.certCount, nil
}
func (f *fake) RoomCandidates(context.Context, string, string) ([]RoomCandidate, error) {
	return nil, nil
}
func (f *fake) ApprovalsNeeded(context.Context, string) ([]Approval, error) { return nil, nil }

// Environments.
func (f *fake) CreatePreview(_ context.Context, _, name string) (string, error) {
	f.envs = append(f.envs, name)
	return "env-" + name, nil
}
func (f *fake) deleteEnv(env string) {
	delete(f.copies, env)
	for k, p := range f.previews {
		if p.EnvID == env {
			delete(f.previews, k)
		}
	}
	f.envs = slices.DeleteFunc(f.envs, func(n string) bool { return "env-"+n == env })
}

// Services.
func (f *fake) CopyForPreview(_ context.Context, in CopyInput) (string, error) {
	var name string
	for _, s := range f.sources {
		if s.ID == in.OriginalID {
			name = s.Name
		}
	}
	id := f.id("copy-" + name)
	f.copies[in.EnvironmentID] = append(f.copies[in.EnvironmentID], CopyService{ID: id, Name: name, PreviewOf: in.OriginalID,
		ServerID: "srv", Hostnames: in.Hostnames, RuntimeStatus: "idle"})
	f.events = append(f.events, fmt.Sprintf("copy %s extra=%d untrusted=%v", name, in.ExtraMemoryMB, in.Untrusted))
	return id, nil
}
func (f *fake) Stop(_ context.Context, id string) error {
	f.stopped = append(f.stopped, id)
	return nil
}

// Deployer: a deploy is live at once.
func (f *fake) DeployPreview(_ context.Context, in DeployInput) error {
	f.deploys = append(f.deploys, in)
	for env, cs := range f.copies {
		for i := range cs {
			if cs[i].ID == in.ServiceID {
				f.copies[env][i].DeploymentSHA, f.copies[env][i].DeploymentStatus, f.copies[env][i].RuntimeStatus = in.SHA, "running", "running"
			}
		}
	}
	return nil
}

// Code.
func (f *fake) AddEvent(_ context.Context, _, _, kind string, data map[string]any) {
	if files, ok := data["files"].([]string); ok {
		kind += " " + strings.Join(files, ",")
	}
	f.events = append(f.events, kind)
}
func (f *fake) ChangedFiles(_ context.Context, _, _, base, head string) ([]ChangedFile, bool, error) {
	files, ok := f.changed[head]
	if !ok || base != "main" {
		return nil, false, fmt.Errorf("no compare of %s with %s", head, base)
	}
	return files, f.truncated[head], nil
}
func (f *fake) ReportPreviewCheck(_ context.Context, _, sha, service, state, _ string) {
	f.checks = append(f.checks, fmt.Sprintf("%s %s %s", service, state, sha[:3]))
}
func (f *fake) ReadFile(_ context.Context, _, _, _, p string) ([]byte, error) {
	if b, ok := f.files[p]; ok {
		return []byte(b), nil
	}
	return nil, fmt.Errorf("no %s", p)
}

// Vars.
func (f *fake) All(_ context.Context, id string) ([]string, error) {
	return []string{"API=https://api.staging.example.com", "SECRET=s3cret"}, nil
}
func (f *fake) Plain(_ context.Context, id string) ([]string, error) {
	return []string{"API=https://api.staging.example.com"}, nil
}

// Hosts.
func (f *fake) Address(context.Context, string) (string, error)      { return "203.0.113.10", nil }
func (f *fake) HasDNSProvider(context.Context, string) (bool, error) { return f.dns, nil }
func (f *fake) UntrustedReady(context.Context, string) string        { return f.ready }

type envs struct{ f *fake }

func (e envs) CreatePreview(ctx context.Context, p, n string) (string, error) {
	return e.f.CreatePreview(ctx, p, n)
}
func (e envs) Delete(_ context.Context, id string) error { e.f.deleteEnv(id); return nil }

type svcs struct{ f *fake }

func (s svcs) CopyForPreview(ctx context.Context, in CopyInput) (string, error) {
	return s.f.CopyForPreview(ctx, in)
}
func (s svcs) AddPreviewAddress(_ context.Context, id, host string) error {
	for env, cs := range s.f.copies {
		for i := range cs {
			if cs[i].ID == id {
				s.f.copies[env][i].Hostnames = append(s.f.copies[env][i].Hostnames, host)
			}
		}
	}
	return nil
}
func (s svcs) Delete(context.Context, string) error      { return nil }
func (s svcs) Stop(ctx context.Context, id string) error { return s.f.Stop(ctx, id) }

func newSvc(f *fake) *Service {
	s := NewService(f, envs{f}, svcs{f}, f, f, f, f, nil, nil, true, zap.NewNop())
	s.now = func() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) }
	return s
}

func sha(c byte) string { return strings.Repeat(string(c), 40) }

func (f *fake) open(id string, number int, key bool) {
	f.pulls[id] = &Pull{ID: id, RepoID: "repo", RepoName: "storefront", ProjectID: "proj", ProjectName: "storefront", OrgID: "org",
		Number: number, Title: "t", State: "open", Base: "main", Head: "feat", HeadSHA: sha('a'), AuthorID: "alex", OpenedWithKey: key, FirstRepository: true}
}

func (f *fake) emit(id string) {
	f.outbox = append(f.outbox, Event{ID: int64(len(f.outbox) + 100 + f.nextID), PullID: id})
}

func TestWriterPreviewLifecycle(t *testing.T) {
	f := newFake()
	s := newSvc(f)
	ctx := context.Background()
	f.open("p1", 42, false)
	f.emit("p1")
	s.ApplyEvents(ctx)

	pv := f.previews["p1"]
	if pv == nil || pv.State != StateActive || pv.EnvID != "env-pr-42" || pv.Untrusted {
		t.Fatalf("preview after open: %+v", pv)
	}
	if got := len(f.copies["env-pr-42"]); got != 2 {
		t.Fatalf("copies = %d, want web and api", got)
	}
	if len(f.deploys) != 2 || f.deploys[0].SHA != sha('a') {
		t.Fatalf("deploys = %+v", f.deploys)
	}
	vars := strings.Join(f.deploys[0].EnvVars, " ")
	for _, want := range []string{"SECRET=s3cret", "TOFT_PULL_REQUEST=42", "https://web-pr42-storefront.203-0-113-10.sslip.io"} {
		if !strings.Contains(vars+" "+strings.Join(f.deploys[1].EnvVars, " "), want) {
			t.Fatalf("variables %q lack %q", vars, want)
		}
	}
	if !strings.Contains(strings.Join(f.deploys[1].EnvVars, " "), "API=https://api-pr42-storefront.203-0-113-10.sslip.io") &&
		!strings.Contains(vars, "API=https://api-pr42-storefront.203-0-113-10.sslip.io") {
		t.Fatalf("staging's api address not rewritten: %v", f.deploys)
	}
	if len(f.outbox) != 0 {
		t.Fatalf("events left unapplied: %v", f.outbox)
	}

	// The same event again redeploys nothing.
	f.emit("p1")
	s.ApplyEvents(ctx)
	if len(f.deploys) != 2 {
		t.Fatalf("a repeated event deployed again: %d", len(f.deploys))
	}

	// A push updates both.
	f.pulls["p1"].HeadSHA = sha('b')
	f.emit("p1")
	s.ApplyEvents(ctx)
	if len(f.deploys) != 4 || f.deploys[3].SHA != sha('b') || !slices.Contains(f.events, "preview_updated") {
		t.Fatalf("after push: deploys %d, events %v", len(f.deploys), f.events)
	}

	// Merged: removed.
	f.pulls["p1"].State, f.pulls["p1"].MergedBy = "merged", "sam"
	f.emit("p1")
	s.ApplyEvents(ctx)
	if f.previews["p1"] != nil || len(f.envs) != 0 || !slices.Contains(f.events, "preview_removed") {
		t.Fatalf("after merge: previews %v envs %v events %v", f.previews, f.envs, f.events)
	}
}

// A copy made while its original had no domain gets its address at the next
// deploy after the original gets one. A copy that isn't deployed is left
// alone: an address nothing routes would be shown as its own.
func TestLateDomainNamesTheCopy(t *testing.T) {
	f := newFake()
	s := newSvc(f)
	ctx := context.Background()
	f.sources[0].Hostnames = nil
	f.open("p1", 42, false)
	f.emit("p1")
	s.ApplyEvents(ctx)
	web := func() CopyService {
		for _, c := range f.copies["env-pr-42"] {
			if c.PreviewOf == "web" {
				return c
			}
		}
		t.Fatal("no copy of web")
		return CopyService{}
	}
	if got := web().Hostnames; len(got) != 0 {
		t.Fatalf("a copy of a service with no domain has addresses: %v", got)
	}

	f.sources[0].Hostnames = []string{"web.staging.example.com"}
	f.emit("p1")
	s.ApplyEvents(ctx)
	if got := web().Hostnames; len(got) != 0 {
		t.Fatalf("a copy that wasn't deployed got addresses: %v", got)
	}

	before := len(f.deploys)
	if err := s.reconcile(ctx, "p1", Actor{}, cause{kind: causeRedeploy}); err != nil {
		t.Fatal(err)
	}
	want := "web-pr42-storefront.203-0-113-10.sslip.io"
	if got := web().Hostnames; !slices.Equal(got, []string{want}) {
		t.Fatalf("addresses after a redeploy = %v, want %s", got, want)
	}
	vars := ""
	for _, d := range f.deploys[before:] {
		if d.ServiceID == web().ID {
			vars = strings.Join(d.EnvVars, " ")
		}
	}
	if !strings.Contains(vars, "TOFT_PREVIEW_URL=https://"+want) {
		t.Fatalf("web's redeploy doesn't know its address: %q", vars)
	}
}

// A late name that falls outside the preview domain (its check failed that
// day) isn't under the wildcard: the certificate names every address.
func TestLateNameOutsideTheWildcard(t *testing.T) {
	f := newFake()
	s := newSvc(f)
	ctx := context.Background()
	f.set.Domain, f.dns = "preview.example.com", true
	s.lookup = func(context.Context, string) ([]string, error) { return []string{"203.0.113.10"}, nil }
	f.sources[1].Hostnames = nil
	f.open("p1", 42, false)
	f.emit("p1")
	s.ApplyEvents(ctx)
	if got := f.deploys[0].CertDomains; !slices.Equal(got, []string{"*.preview.example.com"}) {
		t.Fatalf("certificate names at the start = %v", got)
	}

	f.sources[1].Hostnames = []string{"api.staging.example.com"}
	s.lookup = func(context.Context, string) ([]string, error) { return nil, nil }
	before := len(f.deploys)
	if err := s.reconcile(ctx, "p1", Actor{}, cause{kind: causeRedeploy}); err != nil {
		t.Fatal(err)
	}
	want := []string{"pr-42-web.preview.example.com", "api-pr42-storefront.203-0-113-10.sslip.io"}
	for _, d := range f.deploys[before:] {
		if !slices.Equal(d.CertDomains, want) || d.CertResolver != "" {
			t.Fatalf("certificate = %v by %q, want %v", d.CertDomains, d.CertResolver, want)
		}
	}
	if len(f.deploys) == before {
		t.Fatal("the redeploy deployed nothing")
	}
}

// A preview keeps the migrations its diff touches, at its start and each
// push it deploys, and names them when they change (D-144). A diff it
// can't read changes nothing and doesn't stop the deploy.
func TestPreviewNotesSkippedMigrations(t *testing.T) {
	f := newFake()
	s := newSvc(f)
	ctx := context.Background()
	migration := ChangedFile{Path: "db/migrations/0001_init.sql"}
	f.changed = map[string][]ChangedFile{
		sha('a'): {migration, {Path: "main.go"}},
		sha('b'): {migration, {Path: "main.go"}, {Path: "README.md"}},
		sha('c'): {migration, {Path: "prisma/schema.prisma"}},
		sha('e'): {{Path: "main.go"}},
	}
	f.open("p1", 42, false)
	push := func(c byte) {
		f.pulls["p1"].HeadSHA = sha(c)
		f.emit("p1")
		s.ApplyEvents(ctx)
	}
	check := func(step string, files []string, events int) {
		t.Helper()
		got := slices.DeleteFunc(slices.Clone(f.events), func(e string) bool { return !strings.HasPrefix(e, "preview_migrations_skipped") })
		if pv := f.previews["p1"]; !slices.Equal(pv.MigrationFiles, files) || len(got) != events {
			t.Fatalf("%s: kept %v, events %v", step, pv.MigrationFiles, got)
		}
	}

	f.emit("p1")
	s.ApplyEvents(ctx)
	check("at the start", []string{"db/migrations/0001_init.sql"}, 1)
	if !slices.Contains(f.events, "preview_migrations_skipped db/migrations/0001_init.sql") {
		t.Fatalf("events = %v", f.events)
	}
	push('b')
	check("a push touching the same migrations", []string{"db/migrations/0001_init.sql"}, 1)
	push('c')
	check("a push touching another", []string{"db/migrations/0001_init.sql", "prisma/schema.prisma"}, 2)
	push('d')
	check("a diff that can't be read", []string{"db/migrations/0001_init.sql", "prisma/schema.prisma"}, 2)
	if f.deploys[len(f.deploys)-1].SHA != sha('d') {
		t.Fatalf("the push wasn't deployed: %+v", f.deploys)
	}
	push('e')
	check("a push touching none", []string{}, 2)

	// A diff too large to check is kept as unchecked, with the migrations
	// among the files it lists (D-147); one that fits clears it.
	f.changed[sha('f')], f.truncated = []ChangedFile{migration}, map[string]bool{sha('f'): true}
	push('f')
	check("a diff too large to check", []string{"db/migrations/0001_init.sql"}, 3)
	if !f.previews["p1"].MigrationsUnchecked {
		t.Fatal("a truncated diff wasn't kept as unchecked")
	}
	f.changed[sha('g')], f.truncated[sha('g')] = []ChangedFile{migration}, false
	push('g')
	check("the same migrations in a diff that fits", []string{"db/migrations/0001_init.sql"}, 3)
	if f.previews["p1"].MigrationsUnchecked {
		t.Fatal("a diff that fits left the preview unchecked")
	}
}

func TestUntrustedPreviewWaitsForApproval(t *testing.T) {
	f := newFake()
	s := newSvc(f)
	ctx := context.Background()
	f.open("p1", 45, true)
	f.emit("p1")
	s.ApplyEvents(ctx)

	pv := f.previews["p1"]
	if pv.State != StateWaitingApproval || pv.EnvID != "" || len(f.deploys) != 0 {
		t.Fatalf("untrusted preview started without approval: %+v, deploys %d", pv, len(f.deploys))
	}
	if !slices.Equal(f.checks, []string{"web pending aaa", "api pending aaa"}) {
		t.Fatalf("checks = %v", f.checks)
	}

	// Approving another commit is refused.
	if _, err := s.Approve(ctx, "proj", "repo", 45, sha('z'), Actor{UserID: "sam"}); err != ErrStaleSHA {
		t.Fatalf("approve a stale sha: %v", err)
	}
	if _, err := s.Approve(ctx, "proj", "repo", 45, sha('a'), Actor{UserID: "sam"}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	pv = f.previews["p1"]
	if pv.State != StateActive || len(f.deploys) != 2 {
		t.Fatalf("after approval: %+v, deploys %d", pv, len(f.deploys))
	}
	for _, d := range f.deploys {
		if strings.Contains(strings.Join(d.EnvVars, " "), "SECRET") {
			t.Fatalf("untrusted preview got a secret: %v", d.EnvVars)
		}
	}
	if !slices.Contains(f.events, "copy web extra=64 untrusted=true") {
		t.Fatalf("copies = %v", f.events)
	}

	// A key's push: the approved commit keeps running; the new one waits.
	f.pulls["p1"].HeadSHA, f.pulls["p1"].KeyPushed = sha('c'), true
	f.emit("p1")
	s.ApplyEvents(ctx)
	pv = f.previews["p1"]
	if pv.ApprovedSHA != sha('a') || pv.HeadSHA != sha('c') || len(f.deploys) != 2 {
		t.Fatalf("after a push: %+v, deploys %d", pv, len(f.deploys))
	}
	if _, err := s.Approve(ctx, "proj", "repo", 45, sha('c'), Actor{UserID: "sam"}); err != nil {
		t.Fatalf("approve the push: %v", err)
	}
	if len(f.deploys) != 4 || f.deploys[3].SHA != sha('c') {
		t.Fatalf("approved push not deployed: %+v", f.deploys)
	}
}

func TestUntrustedRefusals(t *testing.T) {
	f := newFake()
	s := newSvc(f)
	ctx := context.Background()
	f.ready = "fsn1-prod-01 can't run Sandboxed (gVisor isn't installed)"
	f.open("p1", 45, true)
	f.emit("p1")
	s.ApplyEvents(ctx)
	if _, err := s.Approve(ctx, "proj", "repo", 45, sha('a'), Actor{UserID: "sam"}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if pv := f.previews["p1"]; pv.EnvID != "" || pv.Note != f.ready {
		t.Fatalf("started on a server without gVisor: %+v", pv)
	}

	f.ready = ""
	f.files["api/Dockerfile"] = "FROM golang\nRUN --mount=type=cache,target=/go go build\n"
	if _, err := s.Start(ctx, "proj", "repo", 45, Actor{UserID: "sam"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(f.deploys) != 1 || !slices.Contains(f.checks, "api failure aaa") {
		t.Fatalf("api's cache mount not refused: deploys %d, checks %v", len(f.deploys), f.checks)
	}
}

// An untrusted preview that waited for approval while a server had no gVisor
// reads as blocked; once the server can run it, it asks for approval again
// without a push.
func TestBlockedForGvisorUnblocksWhenReady(t *testing.T) {
	f := newFake()
	s := newSvc(f)
	ctx := context.Background()
	f.ready = "fsn1-prod-01 can't run Sandboxed (gVisor isn't installed)"
	f.open("p1", 45, true)
	f.emit("p1")
	s.ApplyEvents(ctx)

	v, err := s.Of(ctx, "proj", "repo", 45)
	if err != nil || v.Status != "blocked" || v.Note != f.ready || v.CanStart != f.ready {
		t.Fatalf("while gVisor is missing: %+v, %v", v, err)
	}

	f.ready = ""
	v, err = s.Of(ctx, "proj", "repo", 45)
	if err != nil || v.Status != "needs_approval" || v.Note != "" || v.CanStart != "" || !v.NeedsApprove {
		t.Fatalf("after gVisor is ready: %+v, %v", v, err)
	}
}

// A commit refused for its Dockerfile (D-97) reads as failed, with the
// reason: it created no deployment, so nothing else would ever end
// "starting".
func TestRefusedCommitReadsFailed(t *testing.T) {
	f := newFake()
	s := newSvc(f)
	ctx := context.Background()
	f.open("p1", 45, true)
	refused := "FROM golang\nRUN --mount=type=cache,target=/go go build\n"
	f.files["Dockerfile"], f.files["api/Dockerfile"] = refused, refused
	f.emit("p1")
	s.ApplyEvents(ctx)
	if _, err := s.Approve(ctx, "proj", "repo", 45, sha('a'), Actor{UserID: "sam"}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	v, err := s.Of(ctx, "proj", "repo", 45)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != "failed" || len(f.deploys) != 0 {
		t.Fatalf("a refused commit: status %q, deploys %d", v.Status, len(f.deploys))
	}
	for _, sv := range v.Services {
		if sv.Status != "failed" || !strings.Contains(sv.Error, "RUN steps") {
			t.Errorf("%s: %+v", sv.Name, sv)
		}
	}

	// A commit it can build clears the refusal.
	delete(f.files, "Dockerfile")
	delete(f.files, "api/Dockerfile")
	f.pulls["p1"].HeadSHA, f.pulls["p1"].KeyPushed = sha('c'), true
	f.emit("p1")
	s.ApplyEvents(ctx)
	if _, err := s.Approve(ctx, "proj", "repo", 45, sha('c'), Actor{UserID: "sam"}); err != nil {
		t.Fatalf("approve the fix: %v", err)
	}
	if v, err = s.Of(ctx, "proj", "repo", 45); err != nil || v.Status != "running" || v.Note != "" {
		t.Fatalf("after the fix: %+v %v", v, err)
	}
}

// An untrusted preview already running a commit: the next one refused leaves
// that one running, and says so.
func TestRefusedSecondCommitKeepsRunningTheFirst(t *testing.T) {
	f := newFake()
	s := newSvc(f)
	ctx := context.Background()
	f.open("p1", 45, true)
	f.emit("p1")
	s.ApplyEvents(ctx)
	if _, err := s.Approve(ctx, "proj", "repo", 45, sha('a'), Actor{UserID: "sam"}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	refused := "FROM golang\nRUN --mount=type=cache,target=/go go build\n"
	f.files["Dockerfile"], f.files["api/Dockerfile"] = refused, refused
	deploys := len(f.deploys)
	f.pulls["p1"].HeadSHA, f.pulls["p1"].KeyPushed = sha('c'), true
	f.emit("p1")
	s.ApplyEvents(ctx)
	if _, err := s.Approve(ctx, "proj", "repo", 45, sha('c'), Actor{UserID: "sam"}); err != nil {
		t.Fatalf("approve the second: %v", err)
	}
	v, err := s.Of(ctx, "proj", "repo", 45)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != "running" || v.ApprovedSHA != sha('c') || !strings.Contains(v.Note, "RUN steps") || len(f.deploys) != deploys {
		t.Fatalf("status %q, approved %q, note %q, deploys %d", v.Status, v.ApprovedSHA, v.Note, len(f.deploys)-deploys)
	}
	for _, sv := range v.Services {
		if sv.Status != "running" || sv.SHA != sha('a') || !strings.Contains(sv.Error, "RUN steps") {
			t.Errorf("%s: %+v", sv.Name, sv)
		}
	}
}

func TestKeyPushSandboxesAWritersPreview(t *testing.T) {
	f := newFake()
	s := newSvc(f)
	ctx := context.Background()
	f.open("p1", 42, false)
	f.emit("p1")
	s.ApplyEvents(ctx)
	f.pulls["p1"].HeadSHA, f.pulls["p1"].KeyPushed = sha('b'), true
	f.emit("p1")
	s.ApplyEvents(ctx)
	pv := f.previews["p1"]
	if pv == nil || !pv.Untrusted || pv.EnvID != "" || pv.State != StateWaitingApproval || len(f.envs) != 0 {
		t.Fatalf("after a key's push: %+v envs %v", pv, f.envs)
	}
}

func TestLimitAndWaitingRoom(t *testing.T) {
	f := newFake()
	s := newSvc(f)
	ctx := context.Background()
	for i := 1; i <= 6; i++ {
		id := fmt.Sprintf("p%d", i)
		f.open(id, i, false)
		f.emit(id)
	}
	s.ApplyEvents(ctx)
	if pv := f.previews["p6"]; pv.State != StateWaitingRoom || pv.EnvID != "" {
		t.Fatalf("sixth preview: %+v", pv)
	}
	f.pulls["p1"].State = "closed"
	f.emit("p1")
	s.ApplyEvents(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		pv := f.previews["p6"]
		started := pv != nil && pv.State == StateActive
		s.mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the waiting preview didn't start: %+v", pv)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The project's own limit (0077): a third waits under 2, starts when an
// owner raises it to 3, and lowering it to 1 stops none.
func TestProjectLimit(t *testing.T) {
	f := newFake()
	f.set.Max = 2
	s := newSvc(f)
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("p%d", i)
		f.open(id, i, false)
		f.emit(id)
	}
	s.ApplyEvents(ctx)
	if pv := f.previews["p3"]; pv.State != StateWaitingRoom {
		t.Fatalf("third preview under 2: %+v", pv)
	}
	for _, bad := range []int{0, 21} {
		if _, err := s.SetSettings(ctx, ownEnv{}, "proj", "staging", "", &bad, Actor{UserID: "nikos"}); !errors.Is(err, ErrBadMax) {
			t.Fatalf("max %d: %v", bad, err)
		}
	}
	three := 3
	if _, err := s.SetSettings(ctx, ownEnv{}, "proj", "staging", "", &three, Actor{UserID: "nikos"}); err != nil {
		t.Fatal(err)
	}
	// It starts in the background.
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		started := f.previews["p3"].State == StateActive
		s.mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("third preview after raising to 3: %+v", f.previews["p3"])
		}
		time.Sleep(10 * time.Millisecond)
	}
	one := 1
	if _, err := s.SetSettings(ctx, ownEnv{}, "proj", "staging", "", &one, Actor{UserID: "nikos"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"p1", "p2", "p3"} {
		if pv := f.previews[id]; pv.State != StateActive {
			t.Fatalf("%s after lowering to 1: %+v", id, pv)
		}
	}
}

func TestStoppedForRoom(t *testing.T) {
	f := newFake()
	s := newSvc(f)
	ctx := context.Background()
	f.open("p1", 42, false)
	f.emit("p1")
	s.ApplyEvents(ctx)
	if err := s.StopForRoom(ctx, "p1", "production's api needed the memory", Actor{UserID: "sam"}); err != nil {
		t.Fatal(err)
	}
	for env, cs := range f.copies {
		for i := range cs {
			f.copies[env][i].RuntimeStatus = "stopped"
		}
	}
	// An event that doesn't move the head leaves it stopped.
	f.emit("p1")
	s.ApplyEvents(ctx)
	if pv := f.previews["p1"]; pv.State != StateStopped || len(f.deploys) != 2 {
		t.Fatalf("restarted by a reopen: %+v, deploys %d", pv, len(f.deploys))
	}
	// A push starts it again.
	f.pulls["p1"].HeadSHA = sha('b')
	f.emit("p1")
	s.ApplyEvents(ctx)
	if pv := f.previews["p1"]; pv.State != StateActive || len(f.deploys) != 4 {
		t.Fatalf("not started by a push: %+v, deploys %d", pv, len(f.deploys))
	}
}

func TestOffRemovesAndDomain(t *testing.T) {
	f := newFake()
	s := newSvc(f)
	ctx := context.Background()
	f.set.Domain = "preview.example.com"
	s.lookup = func(context.Context, string) ([]string, error) { return []string{"203.0.113.10"}, nil }
	f.open("p1", 42, false)
	f.emit("p1")
	s.ApplyEvents(ctx)
	if cs := f.copies["env-pr-42"]; len(cs) != 2 || cs[0].Hostnames[0] != "pr-42-web.preview.example.com" {
		t.Fatalf("addresses = %+v", cs)
	}
	if got := f.deploys[0].CertDomains; !slices.Equal(got, []string{"pr-42-web.preview.example.com", "pr-42-api.preview.example.com"}) {
		t.Fatalf("certificate names = %v", got)
	}
	if _, err := s.SetSettings(ctx, ownEnv{}, "proj", "", "", nil, Actor{UserID: "nikos"}); err != nil {
		t.Fatal(err)
	}
	if len(f.previews) != 0 {
		t.Fatalf("previews left after turning off: %v", f.previews)
	}
}

func TestCertificateGuard(t *testing.T) {
	f := newFake()
	s := newSvc(f)
	f.set.Domain = "preview.example.com"
	f.certCount = 40
	s.lookup = func(context.Context, string) ([]string, error) { return []string{"203.0.113.10"}, nil }
	f.open("p1", 42, false)
	f.emit("p1")
	s.ApplyEvents(context.Background())
	if cs := f.copies["env-pr-42"]; !strings.HasSuffix(cs[0].Hostnames[0], ".sslip.io") {
		t.Fatalf("past 40 certificates a preview still took one: %v", cs[0].Hostnames)
	}
}

type ownEnv struct{}

func (ownEnv) OwnEnvironment(context.Context, string, string) (bool, error) { return true, nil }

func TestRestartCatchesUp(t *testing.T) {
	f := newFake()
	s := newSvc(f)
	ctx := context.Background()
	f.open("p1", 42, false)
	f.emit("p1")
	s.ApplyEvents(ctx)
	// A restart with nothing new redeploys nothing.
	s.reconcileAll(ctx)
	if len(f.deploys) != 2 {
		t.Fatalf("a quiet restart deployed: %d", len(f.deploys))
	}
	// The head moved while an older release ran, and no event says so.
	f.pulls["p1"].HeadSHA = sha('d')
	s.reconcileAll(ctx)
	if len(f.deploys) != 4 || f.deploys[3].SHA != sha('d') {
		t.Fatalf("the moved head wasn't deployed at start: %+v", f.deploys)
	}
	// Closed meanwhile: removed at start.
	f.pulls["p1"].State = "closed"
	s.reconcileAll(ctx)
	if f.previews["p1"] != nil {
		t.Fatal("a closed pull request's preview survived a start")
	}
}
