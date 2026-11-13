package disk

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

type fakeHost struct {
	mu       sync.Mutex
	usage    Usage
	inUse    map[string]bool // refs Docker refuses
	used     map[string]bool // image IDs a container uses
	removed  []string
	pruned   []int64 // maxUsed per prune; -1 for all
	volumes  map[string]Volume
	volGone  []string
	failNext error
}

func (h *fakeHost) Usage(context.Context) (Usage, error) { return h.usage, nil }
func (h *fakeHost) Filesystem(context.Context) (int64, int64, int64, error) {
	return h.usage.SizeBytes, h.usage.UsedBytes, h.usage.FreeBytes, nil
}
func (h *fakeHost) RemoveImage(_ context.Context, ref string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failNext != nil {
		err := h.failNext
		h.failNext = nil
		return err
	}
	if h.inUse[ref] {
		return ErrInUse
	}
	h.removed = append(h.removed, ref)
	return nil
}
func (h *fakeHost) ImageUsed(_ context.Context, id string) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.used[id], nil
}
func (h *fakeHost) PruneBuildCache(_ context.Context, maxUsed int64, all bool) (int64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if all {
		maxUsed = -1
	}
	h.pruned = append(h.pruned, maxUsed)
	return 7, nil
}
func (h *fakeHost) InspectVolume(_ context.Context, name string) (Volume, error) {
	v, ok := h.volumes[name]
	if !ok {
		return Volume{}, errors.New("no such volume")
	}
	return v, nil
}
func (h *fakeHost) RemoveVolume(_ context.Context, name string) error {
	h.volGone = append(h.volGone, name)
	return nil
}
func (h *fakeHost) Close() {}

type fakeHosts struct{ h *fakeHost }

func (f fakeHosts) Host(context.Context, string) (Host, error) { return f.h, nil }

type fakeRepo struct {
	mu        sync.Mutex
	row       HostRow
	svcs      []HostService
	deps      []LiveDeployment
	prefixes  map[string]bool
	marked    []string
	lastClean *Clean
	policy    *Policy
}

func (r *fakeRepo) Server(context.Context, string) (HostRow, error) { return r.row, nil }
func (r *fakeRepo) Servers(context.Context) ([]HostRow, error)      { return nil, nil }
func (r *fakeRepo) SetPolicy(_ context.Context, _, _ string, p Policy) error {
	r.policy = &p
	return nil
}
func (r *fakeRepo) SetLastClean(_ context.Context, _ string, c Clean) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastClean = &c
	return nil
}
func (r *fakeRepo) HostServices(context.Context, string) ([]HostService, error) { return r.svcs, nil }
func (r *fakeRepo) ServicePrefixes(context.Context) (map[string]bool, error)    { return r.prefixes, nil }
func (r *fakeRepo) LiveDeployments(context.Context, []string) ([]LiveDeployment, error) {
	return r.deps, nil
}
func (r *fakeRepo) MarkImagesRemoved(_ context.Context, ids []string) error {
	r.marked = append(r.marked, ids...)
	return nil
}

type fakeDeploys struct{ calls, busyFrom int }

// DeployRunning reports a deploy from the busyFrom-th call on (0: never).
func (d *fakeDeploys) DeployRunning(context.Context, string) (bool, error) {
	d.calls++
	return d.busyFrom > 0 && d.calls >= d.busyFrom, nil
}

type fakeAudit struct {
	mu      sync.Mutex
	actions []string
	actors  []string
}

func (a *fakeAudit) Audit(_ context.Context, _, _, actorID, action string, _ map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.actions = append(a.actions, action)
	a.actors = append(a.actors, actorID)
}

type fakePeople struct{}

func (fakePeople) IsOperator(context.Context, string) (bool, error) { return false, nil }
func (fakePeople) Name(context.Context, string) string              { return "Nikos" }

const srvID = "11111111-0000-4000-8000-000000000000"

func newTestService(h *fakeHost, r *fakeRepo, d *fakeDeploys, a *fakeAudit) *Service {
	return NewService(r, fakeHosts{h}, d, a, fakePeople{}, nil, zap.NewNop())
}

// wait waits for the host's clean to end (its last audit entry) and
// returns it.
func wait(t *testing.T, r *fakeRepo, a *fakeAudit, n int) *Clean {
	t.Helper()
	for i := 0; i < 400; i++ {
		a.mu.Lock()
		ended := 0
		for _, act := range a.actions {
			if act == "server.disk_cleaned" {
				ended++
			}
		}
		a.mu.Unlock()
		if ended >= n {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.lastClean
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the clean didn't end")
	return nil
}

func testHost() (*fakeHost, *fakeRepo) {
	gb := int64(1_000_000_000)
	h := &fakeHost{usage: Usage{SizeBytes: 100 * gb, UsedBytes: 60 * gb, FreeBytes: 37 * gb, BuildCacheBytes: 30 * gb, Images: []Image{
		img("live", 1, "paas/"+apiID+":1"),
		img("old", 0, "paas/"+apiID+":2"),
		{ID: "twotags", Tags: []string{"postgres:15", "postgres:15.4"}, Size: 50, SharedSize: -1},
		img("busy", 0, "redis:7"),
		img("dangling", 0),
	}}, inUse: map[string]bool{"redis:7": true}}
	r := &fakeRepo{row: HostRow{ID: srvID, OrgID: "org"},
		svcs:     []HostService{{ID: apiID, Name: "api", Kept: 1}},
		deps:     []LiveDeployment{{ID: "d1", ServiceID: apiID, ImageTag: "paas/" + apiID + ":1", Rank: 1}, {ID: "d2", ServiceID: apiID, ImageTag: "paas/" + apiID + ":2", Rank: 2}},
		prefixes: map[string]bool{"aaaaaaaa": true}}
	return h, r
}

// The report's free is the host's, not size minus used: 3 GB are reserved.
func TestReportCarriesTheHostsFree(t *testing.T) {
	h, r := testHost()
	s := newTestService(h, r, &fakeDeploys{}, &fakeAudit{})
	rep, err := s.Report(context.Background(), r.row, true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.FreeBytes != 37_000_000_000 {
		t.Errorf("free = %d", rep.FreeBytes)
	}
}

func TestClean(t *testing.T) {
	h, r := testHost()
	a := &fakeAudit{}
	s := newTestService(h, r, &fakeDeploys{}, a)
	c, err := s.StartClean(context.Background(), r.row, "user-1", []Kind{Untagged, OldBuilds, UnusedPulled}, false)
	if err != nil {
		t.Fatal(err)
	}
	if c.Outcome != Running || c.StartedBy != "Nikos" {
		t.Errorf("started = %+v", c)
	}
	done := wait(t, r, a, 1)
	a.mu.Lock()
	defer a.mu.Unlock()
	if done.Outcome != Succeeded || done.Removed != 3 || done.Skipped != 1 {
		t.Errorf("done = %+v", done)
	}
	if done.FreedBytes != 60+50+60+7 { // old, twotags, dangling (unique), cache
		t.Errorf("freed = %d", done.FreedBytes)
	}
	sort.Strings(h.removed)
	want := []string{"dangling", "paas/" + apiID + ":2", "postgres:15", "postgres:15.4"}
	if !reflect.DeepEqual(h.removed, want) {
		t.Errorf("removed %v, want %v (each tag, never by ID for a tagged image)", h.removed, want)
	}
	if !reflect.DeepEqual(r.marked, []string{"d2"}) {
		t.Errorf("marked %v", r.marked)
	}
	if !reflect.DeepEqual(h.pruned, []int64{10_000_000_000}) {
		t.Errorf("pruned %v: the cache is trimmed to 10%% of the disk", h.pruned)
	}
	if !reflect.DeepEqual(a.actions, []string{"server.disk_clean_started", "server.disk_cleaned"}) || a.actors[1] != "user-1" {
		t.Errorf("audit %v by %v", a.actions, a.actors)
	}
}

func TestCleanAllCacheAndFailure(t *testing.T) {
	h, r := testHost()
	h.failNext = errors.New("connection lost")
	a := &fakeAudit{}
	s := newTestService(h, r, &fakeDeploys{}, a)
	if _, err := s.StartClean(context.Background(), r.row, "u", []Kind{Untagged}, true); err != nil {
		t.Fatal(err)
	}
	done := wait(t, r, a, 1)
	if done.Outcome != Failed || done.Error == "" || len(h.pruned) != 0 {
		t.Errorf("a failed removal stops the clean: %+v, pruned %v", done, h.pruned)
	}
	if _, err := s.StartClean(context.Background(), r.row, "u", nil, true); err != nil {
		t.Fatal(err)
	}
	if done := wait(t, r, a, 2); done.Outcome != Succeeded || !reflect.DeepEqual(h.pruned, []int64{-1}) {
		t.Errorf("clear all: %+v, pruned %v", done, h.pruned)
	}
}

func TestCleanGuards(t *testing.T) {
	h, r := testHost()
	ctx := context.Background()
	s := newTestService(h, r, &fakeDeploys{busyFrom: 1}, &fakeAudit{})
	if _, err := s.StartClean(ctx, r.row, "u", []Kind{Untagged}, false); !errors.Is(err, ErrDeployRunning) {
		t.Errorf("deploy running: %v", err)
	}
	// A deploy that began between the check and the mark is seen by the
	// second check, and the mark is undone.
	s = newTestService(h, r, &fakeDeploys{busyFrom: 2}, &fakeAudit{})
	if _, err := s.StartClean(ctx, r.row, "u", []Kind{Untagged}, false); !errors.Is(err, ErrDeployRunning) {
		t.Errorf("deploy began meanwhile: %v", err)
	}
	if busy, _ := s.Cleaning(ctx, srvID); busy {
		t.Error("the mark stayed")
	}
	s = newTestService(h, r, &fakeDeploys{}, &fakeAudit{})
	s.running[srvID] = &Clean{}
	if _, err := s.StartClean(ctx, r.row, "u", []Kind{Untagged}, false); !errors.Is(err, ErrCleanRunning) {
		t.Errorf("clean running: %v", err)
	}
	if busy, _ := s.Cleaning(ctx, srvID); !busy {
		t.Error("deploys don't see the clean")
	}
	if err := s.RemoveVolume(ctx, r.row, "u", "x"); !errors.Is(err, ErrCleanRunning) {
		t.Errorf("volume during a clean: %v", err)
	}
	for _, bad := range [][]Kind{nil, {InUse}} {
		if _, err := newTestService(h, r, &fakeDeploys{}, &fakeAudit{}).StartClean(ctx, r.row, "u", bad, false); !errors.Is(err, ErrNothingToClean) {
			t.Errorf("%v: %v", bad, err)
		}
	}
	if _, err := newTestService(h, r, &fakeDeploys{}, &fakeAudit{}).StartClean(ctx, r.row, "u", []Kind{OldPanelReleases}, false); !errors.Is(err, ErrKindNotHere) {
		t.Errorf("old panel releases on a server: %v", err)
	}
}

func TestRemoveVolume(t *testing.T) {
	h, r := testHost()
	h.usage.Volumes = []Volume{
		{Name: "paas-vol-cccccccc-data", RefCount: 0, Bytes: 5},
		{Name: "paas-vol-aaaaaaaa-data", RefCount: 0, Bytes: 5},
	}
	h.volumes = map[string]Volume{"paas-vol-cccccccc-data": {Name: "paas-vol-cccccccc-data"}, "paas-vol-aaaaaaaa-data": {Name: "paas-vol-aaaaaaaa-data"}}
	a := &fakeAudit{}
	s := newTestService(h, r, &fakeDeploys{}, a)
	ctx := context.Background()
	if err := s.RemoveVolume(ctx, r.row, "u", "paas-vol-aaaaaaaa-data"); !errors.Is(err, ErrVolumeNotRemovable) {
		t.Errorf("a live service's volume: %v", err)
	}
	// Listed when read, but a service with that prefix appeared since.
	if _, err := s.Report(ctx, r.row, true); err != nil {
		t.Fatal(err)
	}
	r.prefixes = map[string]bool{"aaaaaaaa": true, "cccccccc": true}
	if err := s.RemoveVolume(ctx, r.row, "u", "paas-vol-cccccccc-data"); !errors.Is(err, ErrVolumeNotRemovable) {
		t.Errorf("owned since the read: %v", err)
	}
	r.prefixes = map[string]bool{"aaaaaaaa": true}
	if err := s.RemoveVolume(ctx, r.row, "u", "paas-vol-cccccccc-data"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.volGone, []string{"paas-vol-cccccccc-data"}) || a.actions[len(a.actions)-1] != "server.volume_removed" {
		t.Errorf("removed %v, audit %v", h.volGone, a.actions)
	}
}

func TestReportAndPolicy(t *testing.T) {
	h, r := testHost()
	ninety := 90
	r.row.Policy = Policy{AlertPercent: &ninety}
	s := newTestService(h, r, &fakeDeploys{}, &fakeAudit{})
	ctx := context.Background()
	rep, err := s.Report(ctx, r.row, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Policy.Effective.AlertPercent != 90 || rep.BuildCache.LimitBytes != 10_000_000_000 || rep.BuildCache.OverLimitBytes != 20_000_000_000 {
		t.Errorf("report policy %+v, cache %+v", rep.Policy.Effective, rep.BuildCache)
	}
	for _, c := range rep.Classes {
		if c.Kind == Untagged && !c.Automatic || c.Kind == UnusedPulled && c.Automatic {
			t.Errorf("automatic marks: %+v", c)
		}
	}
	if _, err := s.SetPolicy(ctx, r.row, "u", Policy{AlertPercent: ptr(30)}); !errors.Is(err, ErrInvalidPolicy) {
		t.Errorf("out of range: %v", err)
	}
	v, err := s.SetPolicy(ctx, r.row, "u", Policy{AutoClean: []Kind{}})
	if err != nil || len(v.Effective.AutoClean) != 0 || r.policy == nil {
		t.Errorf("turned off: %+v %v", v, err)
	}
}

func TestAutomatic(t *testing.T) {
	h, r := testHost()
	a := &fakeAudit{}
	s := newTestService(h, r, &fakeDeploys{}, a)
	notes := &fakeAlerts{}
	s.WithAlerts(notes)
	s.automatic(context.Background(), srvID, true)
	if len(notes.details) != 1 || !strings.Contains(notes.details[0], "% used, 37 GB free of 100 GB. ") || !strings.Contains(notes.details[0], "Toft cleaned it automatically and freed ") {
		t.Errorf("alert detail = %q", notes.details)
	}
	if r.lastClean == nil || !r.lastClean.Automatic || r.lastClean.StartedBy != "" || a.actors[1] != "" {
		t.Fatalf("automatic clean: %+v, actors %v", r.lastClean, a.actors)
	}
	if !reflect.DeepEqual(r.lastClean.Kinds, []Kind{Untagged, OldBuilds}) {
		t.Errorf("kinds %v", r.lastClean.Kinds)
	}
	// The alert again within the hour does nothing.
	r.lastClean = nil
	s.automatic(context.Background(), srvID, true)
	if r.lastClean != nil {
		t.Error("cleaned twice within the hour")
	}
	// Turned off: nothing.
	r.row.Policy = Policy{AutoClean: []Kind{}}
	s.automatic(context.Background(), srvID, false)
	if r.lastClean != nil {
		t.Error("cleaned while turned off")
	}
}

type fakeAlerts struct{ details []string }

func (f *fakeAlerts) DiskCleaned(_ context.Context, _ string, d string) {
	f.details = append(f.details, d)
}

func TestAlertDetail(t *testing.T) {
	c := Clean{Outcome: Succeeded, FreedBytes: 9_400_000_000}
	c.after.size, c.after.used, c.after.free = 240e9, 206e9, 22e9
	c.after.left, c.after.leftKinds = 4_200_000_000, []Kind{UnusedPulled}
	want := "85% used, 22 GB free of 240 GB. Toft cleaned it automatically and freed 9.4 GB; 4.2 GB more is safe to delete but needs a person: pulled images no service uses."
	if got := alertDetail(c); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	c.Outcome, c.Error = Failed, "the server stopped answering"
	if got := alertDetail(c); !strings.HasSuffix(got, "Toft's automatic clean freed 9.4 GB, then stopped: the server stopped answering.") {
		t.Errorf("failed: %q", got)
	}
}

// An image with several tags is never left half untagged: Docker would
// drop all but the last without asking about containers (D-135).
func TestRemoveImageSeveralTags(t *testing.T) {
	ctx := context.Background()
	im := Image{ID: "sha256:aa", Tags: []string{"paas/x:1", "paas/x:latest"}}
	h := &fakeHost{used: map[string]bool{"sha256:aa": true}}
	if err := removeImage(ctx, h, im); !errors.Is(err, ErrInUse) || len(h.removed) != 0 {
		t.Fatalf("used: %v, removed %v", err, h.removed)
	}
	h.used = nil
	if err := removeImage(ctx, h, im); err != nil || len(h.removed) != 2 {
		t.Fatalf("unused: %v, removed %v", err, h.removed)
	}
}

func TestCleanError(t *testing.T) {
	if got := cleanError(errors.Join(ErrUnreachable, errors.New("ssh: dial 10.0.0.1:22 refused"))); got != ErrUnreachable.Error() {
		t.Errorf("unreachable: %q", got)
	}
	if got := cleanError(errors.New(strings.Repeat("x", 9000))); len(got) > 310 {
		t.Errorf("not cut: %d bytes", len(got))
	}
}

// A shared server (D-39) holds services of two workspaces; the report is the
// owning workspace's (A), and B's services show only as a total. A's clean
// still keeps B's rollback images.
func TestReportNamesOnlyTheWorkspacesServices(t *testing.T) {
	const bID = "bbbbbbbb-0000-4000-8000-000000000002"
	h := &fakeHost{usage: Usage{Images: []Image{
		img("a1", 1, "paas/"+apiID+":1"),
		img("a2", 0, "paas/"+apiID+":2"),
		img("b-live", 1, "registry.b.example/private/app:7"),
		img("b-roll", 0, "registry.b.example/private/old:1"),
		img("b-build", 0, "paas/"+bID+":1"),
		img("b-stray", 0, "registry.b.example/private/app:5"),
		img("mysql", 0, "mysql:8"),
	}}}
	r := &fakeRepo{row: HostRow{ID: srvID, OrgID: "org-a"},
		svcs: []HostService{{ID: apiID, Name: "api", OrgID: "org-a"}, {ID: bID, Name: "bsecret", OrgID: "org-b", Kept: 2}},
		deps: []LiveDeployment{
			{ID: "d1", ServiceID: apiID, ImageTag: "paas/" + apiID + ":1", Rank: 1},
			{ID: "d2", ServiceID: apiID, ImageTag: "paas/" + apiID + ":2", Rank: 2},
			{ID: "d3", ServiceID: bID, ImageTag: "registry.b.example/private/app:7", Rank: 1},
			{ID: "d4", ServiceID: bID, ImageTag: "registry.b.example/private/old:1", Rank: 2},
			{ID: "d5", ServiceID: bID, ImageTag: "paas/" + bID + ":1", Rank: 3},
		},
		prefixes: map[string]bool{"aaaaaaaa": true, "bbbbbbbb": true}}
	a := &fakeAudit{}
	s := newTestService(h, r, &fakeDeploys{}, a)
	ctx := context.Background()
	rep, err := s.Report(ctx, r.row, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"bsecret", bID, "bbbbbbbb", "registry.b.example"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("the report names %q: %s", secret, raw)
		}
	}
	if len(rep.RollbackKept) != 1 || rep.RollbackKept[0].ServiceName != "api" {
		t.Errorf("rollback_kept = %+v", rep.RollbackKept)
	}
	if want := (OtherWorkspaces{Services: 1, RollbackImages: 1, RollbackBytes: 60}); rep.OtherWorkspaces != want {
		t.Errorf("other_workspaces = %+v, want %+v", rep.OtherWorkspaces, want)
	}

	// The clean still plans with every workspace's services: B's rollback
	// images stay, as they did before.
	if _, err := s.StartClean(ctx, r.row, "u", []Kind{Untagged, OldBuilds, UnusedPulled}, false); err != nil {
		t.Fatal(err)
	}
	wait(t, r, a, 1)
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ref := range h.removed {
		if ref == "registry.b.example/private/old:1" {
			t.Errorf("the clean removed %s, a rollback image of another workspace", ref)
		}
	}
}
