package resources

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/Nickpap21/toft/backend/internal/composespec"
)

// fakeRepo stands in for the database: one instance, one service.
type fakeRepo struct {
	claimed    bool
	graceUntil *time.Time // the service's grace date
	claimErrs  int        // ClaimGrace fails this many times first
	panel      map[string]ComposeLimit
	usage      map[string][]DailyUsage
	since      time.Time
	added      map[string]int64
	summarised []time.Time // from, to
	deleted    time.Time
	killStored bool     // what InsertOOMKill answers
	writes     []string // InsertOOMKill and UpsertServerCPUWait calls
	// The API's reads, by service ID.
	usageBy    map[string]map[string][]DailyUsage
	panelBy    map[string]map[string]ComposeLimit
	kills      map[string]int
	killsSince time.Time
	serverWait map[string]float64 // average by "<server> <UTC day>"
	// RestartsSince: by service ID, then compose service.
	restartsBy    map[string]map[string]Restarts
	restartsSince time.Time
	restartsErr   error
	// LatencySince answers latency; DeleteLatencyBefore records its time.
	latency       LatencyWindow
	latencyErr    error
	latencySince  time.Time
	latencyBefore time.Time
}

func (f *fakeRepo) InsertLatency(_ context.Context, serviceID string, at time.Time, buckets []int32, unmeasured string) error {
	f.writes = append(f.writes, fmt.Sprintf("latency %s %s %v %s", serviceID, at.Format(time.RFC3339), buckets, unmeasured))
	return nil
}

func (f *fakeRepo) LatencySince(_ context.Context, _ string, since time.Time, _ int) (LatencyWindow, error) {
	f.latencySince = since
	return f.latency, f.latencyErr
}

func (f *fakeRepo) DeleteLatencyBefore(_ context.Context, before time.Time) error {
	f.latencyBefore = before
	return nil
}

func (f *fakeRepo) ReplaceComposeLimits(_ context.Context, serviceID, _ string, limits map[string]ComposeLimit) error {
	if f.panelBy == nil {
		f.panelBy = map[string]map[string]ComposeLimit{}
	}
	f.panelBy[serviceID] = limits
	return nil
}

func (f *fakeRepo) UsageDailyFor(_ context.Context, ids []string, since time.Time) (map[string]map[string][]DailyUsage, error) {
	f.since = since
	out := map[string]map[string][]DailyUsage{}
	for _, id := range ids {
		if u, ok := f.usageBy[id]; ok {
			out[id] = u
		}
	}
	return out, nil
}

func (f *fakeRepo) ComposeLimitsFor(_ context.Context, ids []string) (map[string]map[string]ComposeLimit, error) {
	out := map[string]map[string]ComposeLimit{}
	for _, id := range ids {
		if l, ok := f.panelBy[id]; ok {
			out[id] = l
		}
	}
	return out, nil
}

func (f *fakeRepo) OOMKillsSince(_ context.Context, _ string, since time.Time) (map[string]int, error) {
	f.killsSince = since
	return f.kills, nil
}

func (f *fakeRepo) RestartsSince(_ context.Context, serviceID string, since time.Time) (map[string]Restarts, error) {
	f.restartsSince = since
	return f.restartsBy[serviceID], f.restartsErr
}

func (f *fakeRepo) ServerCPUWait(_ context.Context, serverID string, day time.Time) (*float64, error) {
	if v, ok := f.serverWait[serverID+" "+day.Format(time.DateOnly)]; ok {
		return &v, nil
	}
	return nil, nil
}

func (f *fakeRepo) InsertOOMKill(_ context.Context, serviceID, composeService, container string, at time.Time) (bool, error) {
	f.writes = append(f.writes, fmt.Sprintf("kill %s %s %s %s", serviceID, composeService, container, at.Format(time.RFC3339)))
	return f.killStored, nil
}

func (f *fakeRepo) UpsertServerCPUWait(_ context.Context, serverID string, day time.Time, pct float64) error {
	f.writes = append(f.writes, fmt.Sprintf("cpu wait %s %s %g", serverID, day.Format(time.DateOnly), pct))
	return nil
}

func (f *fakeRepo) SummariseUsage(_ context.Context, from, to time.Time) error {
	f.summarised = []time.Time{from, to}
	return nil
}

func (f *fakeRepo) DeleteUsageBefore(_ context.Context, before time.Time) error {
	f.deleted = before
	return nil
}

func (f *fakeRepo) ComposeLimits(context.Context, string) (map[string]ComposeLimit, error) {
	return f.panel, nil
}

func (f *fakeRepo) AddDefaultComposeLimits(_ context.Context, _ string, defaults map[string]int64) error {
	f.added = defaults
	return nil
}

func (f *fakeRepo) GraceUntil(context.Context, string) (*time.Time, error) { return f.graceUntil, nil }

func (f *fakeRepo) ClaimGrace(_ context.Context, until time.Time) (bool, int64, error) {
	if f.claimErrs > 0 {
		f.claimErrs--
		return false, 0, errors.New("column memory_limit_grace_until does not exist")
	}
	if f.claimed {
		return false, 0, nil
	}
	f.claimed, f.graceUntil = true, &until
	return true, 3, nil
}

func (f *fakeRepo) UsageDaily(_ context.Context, _ string, since time.Time) (map[string][]DailyUsage, error) {
	f.since = since
	return f.usage, nil
}

func newTestService(repo *fakeRepo, now time.Time) *Service {
	s := NewService(repo, zap.NewNop())
	s.now = func() time.Time { return now }
	return s
}

// settledService is a service whose instance claim an earlier start made.
func settledService(t *testing.T, repo *fakeRepo, now time.Time) *Service {
	t.Helper()
	repo.claimed = true
	s := newTestService(repo, now)
	require.NoError(t, s.EnsureGrace(context.Background()))
	return s
}

func TestEnsureGrace_ClaimsOnce(t *testing.T) {
	repo := &fakeRepo{}
	require.NoError(t, newTestService(repo, t0).EnsureGrace(context.Background()))
	require.NotNil(t, repo.graceUntil)
	sevenDaysOn := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC) // t0 + 7 days is 19 Oct 12:00
	assert.Equal(t, sevenDaysOn, *repo.graceUntil, "the next UTC midnight: \"required from 20 Oct\" is exact")

	require.NoError(t, newTestService(repo, t0.Add(48*time.Hour)).EnsureGrace(context.Background()))
	assert.Equal(t, sevenDaysOn, *repo.graceUntil, "a restart does not extend the grace dates")

	midnight := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	repo = &fakeRepo{}
	require.NoError(t, newTestService(repo, midnight).EnsureGrace(context.Background()))
	assert.Equal(t, midnight.AddDate(0, 0, 7), *repo.graceUntil, "already a midnight: kept")
}

func TestRetryGrace_UntilTheSchemaIsApplied(t *testing.T) {
	repo := &fakeRepo{claimErrs: 2}
	newTestService(repo, t0).RetryGrace(context.Background(), time.Millisecond)
	assert.True(t, repo.claimed)

	repo = &fakeRepo{claimErrs: 1 << 30}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	newTestService(repo, t0).RetryGrace(ctx, time.Millisecond)
	assert.False(t, repo.claimed, "retries until the context ends")
}

// Until the instance claim is known to be made, a service without a grace
// date may be one the claim has yet to reach, not a new one.
func TestGraceNotSettled(t *testing.T) {
	repo := &fakeRepo{claimErrs: 1}
	s := newTestService(repo, t0)
	require.Error(t, s.EnsureGrace(context.Background()))

	added, err := s.AddComposeDefaults(context.Background(), "s1", *inline(stack))
	require.NoError(t, err)
	assert.Empty(t, added)
	assert.Nil(t, repo.added, "no defaults before the claim")

	c, err := s.MemoryCheckFor(context.Background(), ServiceRef{ID: "s1", SourceType: "docker-image", DockerImage: "nginx"})
	require.NoError(t, err)
	require.NotNil(t, c.GraceUntil)
	assert.Equal(t, time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC), *c.GraceUntil, "the date the claim will set: deploys warn meanwhile")
}

func TestGraceSettled(t *testing.T) {
	for name, claimedBefore := range map[string]bool{"claimed now": false, "claimed by an earlier start": true} {
		t.Run(name, func(t *testing.T) {
			repo := &fakeRepo{claimed: claimedBefore}
			s := newTestService(repo, t0)
			require.NoError(t, s.EnsureGrace(context.Background()))
			repo.graceUntil = nil // a service created after the claim

			c, err := s.MemoryCheckFor(context.Background(), ServiceRef{ID: "s1", SourceType: "docker-image", DockerImage: "nginx"})
			require.NoError(t, err)
			assert.Nil(t, c.GraceUntil, "a new service has no grace")
			added, err := s.AddComposeDefaults(context.Background(), "s1", *inline(stack))
			require.NoError(t, err)
			assert.NotEmpty(t, added)
		})
	}
}

func day(n, samples int, peakMB float64) DailyUsage {
	return DailyUsage{Day: t0.Truncate(24*time.Hour).AddDate(0, 0, -n), Samples: samples, MemoryPeakMB: peakMB}
}

// A row without a limit or usage offers its kind's default as one click
// (I2), but only once the rule requires a limit (offerDefault: grace over or
// none); in grace the service is measured first. A day with kills but no
// samples (I3) is not usage: no peak, no percentile; its kills still raise a
// limit.
func TestRow_DefaultWithoutDataAndKillOnlyDays(t *testing.T) {
	r := row("db", KindDatabase, 1024, nil, 0, 0, 0, 0, true, t0)
	assert.Equal(t, &Suggestion{Direction: DirectionSet, Value: "1g", Reason: "Default for database; no usage measured yet"}, r.Memory.Suggestion)
	r = row("db", KindDatabase, 1024, nil, 0, 0, 0, 0, false, t0)
	assert.Nil(t, r.Memory.Suggestion, "in grace: no default, the service is being measured")

	killOnly := []DailyUsage{{Day: t0.Truncate(24 * time.Hour), OOMKills: 1}}
	r = row("", KindApp, 512, killOnly, 1, 512, 0, 0, true, t0)
	assert.Nil(t, r.Memory.PeakMB, "nothing was measured")
	assert.Nil(t, r.CPU.P95Cores)
	require.NotNil(t, r.Memory.Suggestion)
	assert.Equal(t, DirectionUp, r.Memory.Suggestion.Direction)
	assert.Equal(t, "640m", r.Memory.Suggestion.Value, "the kill rule: 1.25 × the limit")

	r = row("", KindApp, 512, killOnly, 1, 0, 0, 0, true, t0)
	assert.Equal(t, &Suggestion{Direction: DirectionSet, Value: "512m", Reason: "Default for app; no usage measured yet"}, r.Memory.Suggestion)

	r = row("web", KindApp, 512, []DailyUsage{day(1, 1440, 100)}, 0, 0, 0, 0, false, t0)
	assert.Equal(t, "192m", r.Memory.Suggestion.Value, "with a day of usage: from the usage, in grace too")
	r = row("web", KindApp, 512, []DailyUsage{day(0, 180, 100)}, 0, 0, 0, 0, false, t0)
	assert.Nil(t, r.Memory.Suggestion, "three hours of usage: still measuring, no suggestion")
	r = row("web", KindApp, 512, nil, 0, 0, 512, 0, true, t0)
	assert.Nil(t, r.Memory.Suggestion, "the file's limit is a limit: no default offered")
}

// The card offers a single container without a limit its kind's default only
// once its grace is over; in grace it is being measured.
func TestServiceResources_DefaultOnlyAfterGrace(t *testing.T) {
	ref := ServiceRef{ID: "s1", SourceType: "docker-image", DockerImage: "nginx"}
	for name, c := range map[string]struct {
		grace time.Time
		want  *Suggestion
	}{
		"in grace":   {t0.Add(24 * time.Hour), nil},
		"grace over": {t0.Add(-time.Second), &Suggestion{Direction: DirectionSet, Value: "512m", Reason: "Default for app; no usage measured yet"}},
	} {
		t.Run(name, func(t *testing.T) {
			grace := c.grace
			s := settledService(t, &fakeRepo{graceUntil: &grace}, t0)
			res, err := s.serviceResources(context.Background(), ref)
			require.NoError(t, err)
			require.Len(t, res.Rows, 1)
			assert.Equal(t, c.want, res.Rows[0].Memory.Suggestion)
		})
	}
}

func TestMemoryCheckFor_Single(t *testing.T) {
	grace := t0.Add(24 * time.Hour)
	repo := &fakeRepo{graceUntil: &grace}
	s := newTestService(repo, t0)
	ref := ServiceRef{ID: "s1", SourceType: "docker-image", MemoryLimit: "", DockerImage: "postgres:16"}

	c, err := s.MemoryCheckFor(context.Background(), ref)
	require.NoError(t, err)
	assert.Equal(t, MemoryCheck{GraceUntil: &grace, Suggested: map[string]SuggestedLimit{"": {Value: "1g"}}}, *c, "without data: the kind's default")
	assert.Equal(t, time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC), repo.since, "the last 14 days, today included")

	repo.usage = map[string][]DailyUsage{"": {day(2, 1440, 200), day(1, 1440, 300)}}
	ref.MemoryLimit = "512m"
	c, err = s.MemoryCheckFor(context.Background(), ref)
	require.NoError(t, err)
	assert.Equal(t, "512m", c.SingleLimit)
	assert.Equal(t, map[string]SuggestedLimit{"": {Value: "448m", FromUsage: true}}, c.Suggested, "with data: 1.3 × the 300 MiB peak, rounded up to 64 MiB")
}

func inline(content string) *composespec.Source {
	return &composespec.Source{Root: "/nonexistent/toft-test", Content: content, ProjectName: "p"}
}

const stack = "services:\n  web:\n    image: nginx\n  db:\n    image: postgres:16\n    mem_limit: 256m\n  cache:\n    image: redis\n"

func TestMemoryCheckFor_Compose(t *testing.T) {
	repo := &fakeRepo{
		panel: map[string]ComposeLimit{"cache": {MemoryMB: 128}},
		usage: map[string][]DailyUsage{"web": {day(1, 1440, 100)}},
	}
	c, err := newTestService(repo, t0).MemoryCheckFor(context.Background(),
		ServiceRef{ID: "s1", SourceType: "docker-compose", Compose: inline(stack)})
	require.NoError(t, err)
	assert.True(t, c.Compose)
	assert.Equal(t, map[string]ComposeLimitSource{"cache": {PanelMB: 128}}, c.ComposeServices, "the panel's limits; the policy adds the file's")
	assert.Equal(t, map[string]SuggestedLimit{"web": {Value: "192m", FromUsage: true}, "db": {Value: "1g"}}, c.Suggested,
		"web from its usage, db (no data) its kind's default; cache has a panel limit")

	c, err = newTestService(repo, t0).MemoryCheckFor(context.Background(), ServiceRef{ID: "s1", SourceType: "docker-compose"})
	require.NoError(t, err)
	assert.Empty(t, c.Suggested, "a repository file not yet cloned: no images, no suggestions")
}

func TestAddComposeDefaults_OnlyWhereNeitherFileNorPanelHasALimit(t *testing.T) {
	repo := &fakeRepo{panel: map[string]ComposeLimit{"cache": {MemoryMB: 128}}}
	added, err := settledService(t, repo, t0).AddComposeDefaults(context.Background(), "s1", *inline(stack))
	require.NoError(t, err)
	assert.Equal(t, map[string]DefaultAdded{"web": {MemoryMB: 512, Kind: KindApp}}, added)
	assert.Equal(t, map[string]int64{"web": 512}, repo.added)
}

func TestAddComposeDefaults_NotForAStackInGrace(t *testing.T) {
	grace := t0.Add(24 * time.Hour)
	repo := &fakeRepo{graceUntil: &grace}
	added, err := settledService(t, repo, t0).AddComposeDefaults(context.Background(), "s1", *inline(stack))
	require.NoError(t, err)
	assert.Empty(t, added)
	assert.Nil(t, repo.added, "an existing stack keeps its grace: the rule warns, then refuses")
}

func TestAddComposeDefaults_SkipsNamesTheOverrideCannotCarry(t *testing.T) {
	repo := &fakeRepo{}
	added, err := settledService(t, repo, t0).AddComposeDefaults(context.Background(), "s1",
		*inline("services:\n  _web:\n    image: nginx\n"))
	require.NoError(t, err)
	assert.Empty(t, added, "a limit stored for _web would never be applied, yet satisfy the rule")
}

func TestSummariseDays_YesterdayAndTodayInUTC(t *testing.T) {
	repo := &fakeRepo{}
	now := time.Date(2026, 10, 13, 1, 30, 0, 0, time.FixedZone("CEST", 2*3600)) // 23:30 UTC on the 12th
	require.NoError(t, newTestService(repo, t0).SummariseDays(context.Background(), now))
	assert.Equal(t, []time.Time{
		time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC),
	}, repo.summarised, "from yesterday to the end of today, UTC")
}

func TestGC_KeepsThirtyDays(t *testing.T) {
	repo := &fakeRepo{}
	require.NoError(t, newTestService(repo, t0).GC(context.Background(), t0))
	assert.Equal(t, time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC), repo.deleted,
		"from midnight UTC, so a day kept keeps its kills")
	assert.Equal(t, t0.Add(-48*time.Hour), repo.latencyBefore, "latency minutes are kept 48 hours, as the samples")
}

func TestRecordOOMKill_StoresTheKillAndSaysWhetherItWasNew(t *testing.T) {
	repo := &fakeRepo{killStored: true}
	s := newTestService(repo, t0)
	stored, err := s.RecordOOMKill(context.Background(), "s1", "web", "stack-web-1", t0)
	require.NoError(t, err)
	assert.True(t, stored)

	repo.killStored = false // already stored: the event windows overlap
	stored, err = s.RecordOOMKill(context.Background(), "s1", "web", "stack-web-1", t0)
	require.NoError(t, err)
	assert.False(t, stored)
	assert.Equal(t, []string{
		"kill s1 web stack-web-1 2026-10-12T12:00:00Z",
		"kill s1 web stack-web-1 2026-10-12T12:00:00Z",
	}, repo.writes)
}

func TestRecordServerCPUWait_AddsToTheServersDay(t *testing.T) {
	repo := &fakeRepo{}
	require.NoError(t, newTestService(repo, t0).RecordServerCPUWait(context.Background(), "srv-1", t0.Truncate(24*time.Hour), 12.5))
	assert.Equal(t, []string{"cpu wait srv-1 2026-10-12 12.5"}, repo.writes)
}

// An existing service's default is its own tool's by slug, and an app's for
// a tool typed in by image (KindOf, not NewKindOf): on the card and in the
// deploy check alike.
func TestExistingServicesToolDefaults(t *testing.T) {
	grace := t0.Add(-time.Second)
	for _, c := range []struct {
		ref  ServiceRef
		want string
	}{
		{ServiceRef{ID: "s1", SourceType: "docker-image", AddonSlug: "adminer"}, "64m"}, // by its slug alone
		{ServiceRef{ID: "s2", SourceType: "docker-image", DockerImage: "dpage/pgadmin4:9"}, "512m"},
		{ServiceRef{ID: "s3", SourceType: "docker-image", DockerImage: "adminer"}, "512m"},
	} {
		s := settledService(t, &fakeRepo{graceUntil: &grace}, t0)
		res, err := s.serviceResources(context.Background(), c.ref)
		require.NoError(t, err)
		require.Len(t, res.Rows, 1)
		require.NotNil(t, res.Rows[0].Memory.Suggestion, "%+v", c.ref)
		assert.Equal(t, c.want, res.Rows[0].Memory.Suggestion.Value, "card: %+v", c.ref)

		check, err := s.MemoryCheckFor(context.Background(), c.ref)
		require.NoError(t, err)
		assert.Equal(t, c.want, check.Suggested[""].Value, "check: %+v", c.ref)
	}
}
