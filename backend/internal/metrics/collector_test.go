package metrics

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// fakeSource is a Source whose servers answer through sample, and the
// collector's Alerter and Usage. Kills are unique per container and time,
// as in the database.
type fakeSource struct {
	targets []service.MetricsTarget
	listErr error
	// sample answers CollectServerStats; nil answers 1% CPU and 10 MB for
	// every target.
	sample  func(ctx context.Context, serverID string, targets []service.MetricsTarget) (service.HostSample, error)
	killErr error
	// agent answers CollectAgentStats; nil: no server's agent keeps
	// samples.
	agent func(serverID string, after time.Time) ([]service.HostSample, bool, error)

	mu       sync.Mutex
	afters   []time.Time                  // the after of each CollectAgentStats call
	calls    map[string][][]string        // server → the target IDs of each call
	sinces   map[string][]int64           // server → the since of each call
	inserted map[string]map[string]Sample // service → compose service → its last row
	stamps   map[time.Time]int            // collected_at → rows stored with it
	kills    map[string]bool              // stored kills, by container and time
	alerts   []string                     // service ID: detail
	waits    []string                     // server ID day pct
	hourly   []string                     // Summariser and GC calls, in order
	latency  []latencyRow                 // RecordLatency calls, in order
	inFlight int
	maxIn    int
}

func newFakeSource(targets ...service.MetricsTarget) *fakeSource {
	return &fakeSource{targets: targets, calls: map[string][][]string{}, sinces: map[string][]int64{},
		inserted: map[string]map[string]Sample{}, stamps: map[time.Time]int{}, kills: map[string]bool{}}
}

func (f *fakeSource) ListMetricsTargets(context.Context) ([]service.MetricsTarget, error) {
	return f.targets, f.listErr
}

func (f *fakeSource) CollectServerStats(ctx context.Context, serverID string, targets []service.MetricsTarget, since int64) (service.HostSample, error) {
	ids := make([]string, 0, len(targets))
	for _, t := range targets {
		ids = append(ids, t.ID)
	}
	f.mu.Lock()
	f.calls[serverID] = append(f.calls[serverID], ids)
	f.sinces[serverID] = append(f.sinces[serverID], since)
	f.inFlight++
	if f.inFlight > f.maxIn {
		f.maxIn = f.inFlight
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()

	if f.sample != nil {
		return f.sample(ctx, serverID, targets)
	}
	out := map[string]service.ContainerStats{}
	for _, t := range targets {
		out[t.ID] = service.ContainerStats{CPUPercent: 1, MemoryMB: 10}
	}
	return perService(out), nil
}

// CollectAgentStats answers through agent; nil: no server's agent keeps
// samples.
func (f *fakeSource) CollectAgentStats(_ context.Context, serverID string, _ []service.MetricsTarget, after time.Time) ([]service.HostSample, bool, error) {
	if f.agent == nil {
		return nil, false, nil
	}
	f.mu.Lock()
	f.afters = append(f.afters, after)
	f.mu.Unlock()
	return f.agent(serverID, after)
}

// perService is a sample with one container per service.
func perService(stats map[string]service.ContainerStats) service.HostSample {
	h := service.HostSample{Containers: map[string]map[string][]service.ContainerStats{}}
	for id, st := range stats {
		h.Containers[id] = map[string][]service.ContainerStats{"": {st}}
	}
	return h
}

func (f *fakeSource) InsertSample(_ context.Context, id, compose string, at time.Time, s Sample) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stamps[at]++
	if f.inserted[id] == nil {
		f.inserted[id] = map[string]Sample{}
	}
	f.inserted[id][compose] = s
	return nil
}

func (f *fakeSource) RecordOOMKill(_ context.Context, serviceID, _, container string, at time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.killErr != nil {
		return false, f.killErr
	}
	k := fmt.Sprintf("%s %s %d", serviceID, container, at.Unix())
	if f.kills[k] {
		return false, nil
	}
	f.kills[k] = true
	return true, nil
}

func (f *fakeSource) RecordServerCPUWait(_ context.Context, serverID string, day time.Time, pct float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waits = append(f.waits, fmt.Sprintf("%s %s %g", serverID, day.Format(time.RFC3339), pct))
	return nil
}

func (f *fakeSource) DeleteOldMetrics(_ context.Context, before time.Time) error {
	f.hourly = append(f.hourly, "delete metrics before "+before.Format(time.RFC3339))
	return nil
}

func (f *fakeSource) OOMKilled(serviceID, detail string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alerts = append(f.alerts, serviceID+": "+detail)
}

func (f *fakeSource) SummariseDays(_ context.Context, now time.Time) error {
	f.hourly = append(f.hourly, "summarise "+now.Format(time.RFC3339))
	return nil
}

func (f *fakeSource) GC(_ context.Context, now time.Time) error {
	f.hourly = append(f.hourly, "gc "+now.Format(time.RFC3339))
	return nil
}

func (f *fakeSource) insertedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]string, 0, len(f.inserted))
	for id := range f.inserted {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// row is the service's last row for a single container.
func (f *fakeSource) row(id string) Sample {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inserted[id][""]
}

// testCollector is a collector on src, which also takes its alerts and
// usage.
func testCollector(src *fakeSource, log *zap.Logger) *collector {
	return newCollector(src, src, src, log)
}

func target(id, serverID string) service.MetricsTarget {
	return service.MetricsTarget{ID: id, Name: "svc-" + id, ServerID: serverID}
}

func observed() (*zap.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(zapcore.InfoLevel)
	return zap.New(core), logs
}

func TestCollectSamplesEachServerOnce(t *testing.T) {
	src := newFakeSource(
		target("local-1", ""), target("local-2", ""),
		target("a-1", "srv-a"), target("a-2", "srv-a"), target("a-3", "srv-a"),
		target("b-1", "srv-b"),
	)
	testCollector(src, zap.NewNop()).collect(context.Background())

	assert.Equal(t, map[string][][]string{
		"":      {{"local-1", "local-2"}},
		"srv-a": {{"a-1", "a-2", "a-3"}},
		"srv-b": {{"b-1"}},
	}, src.calls, "one call per server, the local host included, with that server's services")
	assert.Equal(t, []string{"a-1", "a-2", "a-3", "b-1", "local-1", "local-2"}, src.insertedIDs())
	assert.Equal(t, Sample{CPUPercent: 1, MemoryMB: 10, MemoryPeakMB: f64(10)}, src.row("a-2"))
}

func TestCollectSkipsServicesWithoutAContainer(t *testing.T) {
	src := newFakeSource(target("up", "srv-a"), target("starting", "srv-a"))
	src.sample = func(context.Context, string, []service.MetricsTarget) (service.HostSample, error) {
		return perService(map[string]service.ContainerStats{"up": {CPUPercent: 3, MemoryMB: 30}}), nil
	}
	testCollector(src, zap.NewNop()).collect(context.Background())
	assert.Equal(t, []string{"up"}, src.insertedIDs())
}

func TestCollectStoresARowPerComposeService(t *testing.T) {
	src := newFakeSource(target("a", "srv-a"))
	src.sample = func(context.Context, string, []service.MetricsTarget) (service.HostSample, error) {
		return service.HostSample{Containers: map[string]map[string][]service.ContainerStats{"a": {
			"":    {{CPUPercent: 1, MemoryMB: 10}},
			"web": {{CPUPercent: 2, MemoryMB: 20}, {CPUPercent: 3, MemoryMB: 30}},
			"db":  {{CPUPercent: 4, MemoryMB: 40}},
		}}}, nil
	}
	testCollector(src, zap.NewNop()).collect(context.Background())
	assert.Equal(t, map[string]map[string]Sample{"a": {
		"":    {CPUPercent: 1, MemoryMB: 10, MemoryPeakMB: f64(10)},
		"web": {CPUPercent: 5, MemoryMB: 50, MemoryPeakMB: f64(30)},
		"db":  {CPUPercent: 4, MemoryMB: 40, MemoryPeakMB: f64(40)},
	}}, src.inserted, "one row per compose service, its containers summed; the peak is its largest container, as the limit is per container")
}

func TestCollectStampsEveryRowOfACycleWithOneTime(t *testing.T) {
	src := newFakeSource(target("a", "srv-a"), target("b", "srv-b"), target("l", ""))
	src.sample = func(_ context.Context, _ string, ts []service.MetricsTarget) (service.HostSample, error) {
		return service.HostSample{Containers: map[string]map[string][]service.ContainerStats{ts[0].ID: {
			"web": {{CPUPercent: 1}}, "db": {{CPUPercent: 2}},
		}}}, nil
	}
	c := testCollector(src, zap.NewNop())
	var mu sync.Mutex
	clock := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	c.now = func() time.Time { // moves on at every reading
		mu.Lock()
		defer mu.Unlock()
		clock = clock.Add(time.Second)
		return clock
	}
	c.collect(context.Background())
	c.collect(context.Background())
	assert.Len(t, src.stamps, 2, "one time per cycle")
	for at, rows := range src.stamps {
		assert.Equal(t, 6, rows, "the chart sums a cycle's rows by their shared time %s", at)
	}
}

// An agent server's samples kept while the panel was away fill the gap:
// each is stored at its own time, its throttling compared with the sample
// before it and its kills stored, and the next cycle asks only for what
// came after the last one stored. Its server is not sampled over SSH.
func TestCollectFillsAGapFromTheAgentsSamples(t *testing.T) {
	src := newFakeSource(target("a", "srv-a"))
	t0 := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	kept := func(at time.Time, periods, throttled uint64, kills ...service.OOMEvent) service.HostSample {
		s := perService(map[string]service.ContainerStats{"a": counters("c1", periods, throttled)})
		s.At, s.OOMKills = at, kills
		return s
	}
	kill := service.OOMEvent{ServiceID: "a", Container: "paas-a-blue", At: t0.Add(90 * time.Second)}
	src.agent = func(_ string, after time.Time) ([]service.HostSample, bool, error) {
		if after.IsZero() {
			return []service.HostSample{
				kept(t0, 100, 0),
				kept(t0.Add(time.Minute), 200, 50),
				kept(t0.Add(2*time.Minute), 300, 50, kill),
			}, true, nil
		}
		return []service.HostSample{}, true, nil
	}
	var rows []string
	c := testCollector(src, zap.NewNop())
	c.collect(context.Background())
	c.collect(context.Background())

	for at, n := range src.stamps {
		rows = append(rows, fmt.Sprintf("%s %d", at.Format("15:04"), n))
	}
	sort.Strings(rows)
	assert.Equal(t, []string{"09:00 1", "09:01 1", "09:02 1"}, rows, "a row at each sample's own time")
	assert.Equal(t, []time.Time{{}, t0.Add(2 * time.Minute)}, src.afters, "then only what came after the last one stored")
	assert.Empty(t, src.calls, "not sampled over SSH")
	assert.Equal(t, f64(0), src.row("a").ThrottledPct, "the last sample is compared with the one before it")
	assert.Equal(t, []string{"a: paas-a-blue was killed for memory"}, src.alerts)
}

func f64(v float64) *float64 { return &v }

// counters is a container with cgroup counters.
func counters(id string, periods, throttled uint64) service.ContainerStats {
	return service.ContainerStats{ContainerID: id, NrPeriods: periods, NrThrottled: throttled}
}

// cycles samples one service on srv-a, one cycle per reading, and returns
// its row of each cycle.
func cycles(t *testing.T, readings ...service.ContainerStats) []Sample {
	t.Helper()
	src := newFakeSource(target("a", "srv-a"))
	c := testCollector(src, zap.NewNop())
	rows := make([]Sample, 0, len(readings))
	for _, r := range readings {
		src.sample = func(context.Context, string, []service.MetricsTarget) (service.HostSample, error) {
			return perService(map[string]service.ContainerStats{"a": r}), nil
		}
		c.collect(context.Background())
		rows = append(rows, src.row("a"))
	}
	return rows
}

// The peak is each sample's own (docker stats' memory, page cache left
// out), never a cumulative counter: the same use stores the same peak.
func TestCollectStoresEachSamplesPeak(t *testing.T) {
	mem := func(mb float64) service.ContainerStats {
		return service.ContainerStats{ContainerID: "c1", MemoryMB: mb}
	}
	rows := cycles(t, mem(100), mem(100), mem(150), mem(80), service.ContainerStats{MemoryMB: 70})
	assert.Equal(t, f64(100), rows[0].MemoryPeakMB)
	assert.Equal(t, f64(100), rows[1].MemoryPeakMB, "a flat use is stored again")
	assert.Equal(t, f64(150), rows[2].MemoryPeakMB)
	assert.Equal(t, f64(80), rows[3].MemoryPeakMB, "lower than before: no ratchet")
	assert.Equal(t, f64(70), rows[4].MemoryPeakMB, "a container without an ID has a peak too")
}

func TestCollectComputesTheThrottledShareOfTheMinute(t *testing.T) {
	rows := cycles(t,
		counters("c1", 1000, 100),
		counters("c1", 1600, 250), // 150 of 600 periods
		counters("c1", 2200, 250), // periods advanced, not throttled
		counters("c1", 2200, 250), // no periods: idle
		counters("c1", 2300, 400), // more throttled than periods: cannot happen, yet the column's CHECK must hold
	)
	assert.Nil(t, rows[0].ThrottledPct, "no earlier reading to compare with")
	assert.Equal(t, f64(25), rows[1].ThrottledPct)
	assert.Equal(t, f64(0), rows[2].ThrottledPct, "a CPU limit that held nothing back: 0%, not unmeasured")
	assert.Nil(t, rows[3].ThrottledPct)
	assert.Equal(t, f64(100), rows[4].ThrottledPct, "never above 100")

	noLimit := cycles(t, counters("c1", 0, 0), counters("c1", 0, 0))
	assert.Nil(t, noLimit[1].ThrottledPct, "without a CPU limit nr_periods stays 0: not measured")
}

// Review Focus 2: a container restarted between two samples starts its
// counters again. No negative share.
func TestCollectTreatsCountersThatWentDownAsARestart(t *testing.T) {
	rows := cycles(t,
		counters("c1", 5000, 1000),
		counters("c1", 600, 30), // restarted
		counters("c1", 1200, 90),
	)
	assert.Nil(t, rows[1].ThrottledPct, "skipped, never negative")
	assert.Equal(t, f64(10), rows[2].ThrottledPct, "measured from the restart's reading: 60 of 600")
}

func TestCollectSumsSlotsAndTakesTheirHighestPeakThrottleAndWait(t *testing.T) {
	src := newFakeSource(target("a", "srv-a"))
	blue := counters("blue", 1000, 0)
	green := counters("green", 1000, 0)
	blue.CPUPercent, blue.MemoryMB, blue.CPUWaitPct = 10, 300, f64(2)
	green.CPUPercent, green.MemoryMB, green.CPUWaitPct = 30, 200, f64(7)
	src.sample = func(context.Context, string, []service.MetricsTarget) (service.HostSample, error) {
		return service.HostSample{Containers: map[string]map[string][]service.ContainerStats{"a": {"": {blue, green}}}}, nil
	}
	c := testCollector(src, zap.NewNop())
	c.collect(context.Background())
	assert.Equal(t, Sample{CPUPercent: 40, MemoryMB: 500, MemoryPeakMB: f64(300), CPUWaitPct: f64(7)}, src.row("a"),
		"a blue/green overlap sums the chart's memory, not the peak: the limit is per container")

	blue.NrPeriods, blue.NrThrottled = 2000, 100   // 10%
	green.NrPeriods, green.NrThrottled = 2000, 500 // 50%
	green.MemoryMB = 360
	c.collect(context.Background())
	got := src.row("a")
	assert.Equal(t, f64(50), got.ThrottledPct)
	assert.Equal(t, f64(360), got.MemoryPeakMB)
}

// ct is a container whose restart count was read.
func ct(id string, restarts int) service.ContainerStats {
	return service.ContainerStats{ContainerID: id, RestartCount: restarts, RestartsRead: true}
}

// restartRows samples service a's containers on srv-a, one cycle a minute
// (gap: that many minutes instead), and returns each cycle's restarts, -1
// for none (not read).
func restartRows(t *testing.T, gap time.Duration, cycles ...[]service.ContainerStats) []int {
	t.Helper()
	src := newFakeSource(target("a", "srv-a"))
	c := testCollector(src, zap.NewNop())
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	out := make([]int, 0, len(cycles))
	for i, containers := range cycles {
		src.sample = func(context.Context, string, []service.MetricsTarget) (service.HostSample, error) {
			return service.HostSample{Containers: map[string]map[string][]service.ContainerStats{"a": {"": containers}}}, nil
		}
		c.collect(context.Background())
		if r := src.row("a").Restarts; r != nil {
			out = append(out, *r)
		} else {
			out = append(out, -1)
		}
		now = now.Add(time.Minute)
		if i == 0 && gap > 0 {
			now = now.Add(gap)
		}
	}
	return out
}

// Each row stores its containers' restarts since their last count, summed
// (blue/green slots, replicas); a container seen for the first time adds
// none, and the row has none when one container's count was not read.
func TestCollectCountsRestartsPerContainer(t *testing.T) {
	assert.Equal(t, []int{0, 0, 1, 2}, restartRows(t, 0,
		[]service.ContainerStats{ct("a", 0), ct("b", 5)}, // first seen: none, whatever the count
		[]service.ContainerStats{ct("a", 0), ct("b", 5)},
		[]service.ContainerStats{ct("a", 0), ct("b", 6)},
		[]service.ContainerStats{ct("a", 1), ct("b", 7)},
	), "replicas' steps summed")

	assert.Equal(t, []int{0, 0, 1}, restartRows(t, 0,
		[]service.ContainerStats{ct("a", 0), ct("b", 5)},
		[]service.ContainerStats{ct("a", 0)}, // b dropped out of the sample (stats "--", a crash loop)
		[]service.ContainerStats{ct("a", 0), ct("b", 6)},
	), "b's return counts its one restart, not its whole 6")

	assert.Equal(t, []int{0, 0, 0}, restartRows(t, 0,
		[]service.ContainerStats{ct("blue", 3)},
		[]service.ContainerStats{ct("blue", 3), ct("green", 0)},
		[]service.ContainerStats{ct("green", 0)},
	), "a blue/green swap: the new slot starts at 0 and the old one goes; no restart")

	unread := service.ContainerStats{ContainerID: "a"}
	assert.Equal(t, []int{-1, -1, 0, 1}, restartRows(t, 0,
		[]service.ContainerStats{unread},
		[]service.ContainerStats{unread},
		[]service.ContainerStats{ct("a", 4)}, // the agent was upgraded: a first count
		[]service.ContainerStats{ct("a", 5)},
	), "an old agent's samples carry none; the new agent's first count adds none")

	assert.Equal(t, []int{0, -1}, restartRows(t, 0,
		[]service.ContainerStats{ct("a", 0), ct("b", 0)},
		[]service.ContainerStats{ct("a", 1), {ContainerID: "b"}},
	), "one container not read: the row has none")

	assert.Equal(t, []int{0, 0}, restartRows(t, restartMemory,
		[]service.ContainerStats{ct("a", 0)},
		[]service.ContainerStats{ct("a", 2)},
	), "a container gone longer than restartMemory counts from its return")
}

func TestCollectForgetsContainersNoLongerRunning(t *testing.T) {
	src := newFakeSource(target("a", "srv-a"), target("b", "srv-a"))
	running := map[string]service.ContainerStats{"a": counters("ca", 1000, 0), "b": counters("cb", 1000, 0)}
	src.sample = func(context.Context, string, []service.MetricsTarget) (service.HostSample, error) {
		return perService(running), nil
	}
	c := testCollector(src, zap.NewNop())
	c.collect(context.Background())

	delete(running, "a")
	c.collect(context.Background())
	assert.NotContains(t, c.servers["srv-a"].containers, "ca", "ca was not seen this cycle")
	assert.Contains(t, c.servers["srv-a"].containers, "cb")

	running["a"] = counters("ca", 2000, 100)
	running["b"] = counters("cb", 2000, 100)
	c.collect(context.Background())
	assert.Nil(t, src.row("a").ThrottledPct, "a first reading again")
	assert.Equal(t, f64(10), src.row("b").ThrottledPct)
}

func TestCollectKeepsEachServersKillWindow(t *testing.T) {
	src := newFakeSource(target("a", "srv-a"), target("l", ""))
	until := map[string]int64{"srv-a": 1000}
	src.sample = func(_ context.Context, serverID string, ts []service.MetricsTarget) (service.HostSample, error) {
		return service.HostSample{Containers: map[string]map[string][]service.ContainerStats{}, Until: until[serverID]}, nil
	}
	c := testCollector(src, zap.NewNop())
	c.collect(context.Background())
	until["srv-a"] = 0 // the kills were not read
	c.collect(context.Background())
	until["srv-a"] = 1060
	c.collect(context.Background())
	c.collect(context.Background())

	assert.Equal(t, []int64{0, 1000, 1000, 1060}, src.sinces["srv-a"], "each sample reads from the previous one's end")
	assert.Equal(t, []int64{0, 0, 0, 0}, src.sinces[""])

	src.targets = nil
	c.collect(context.Background())
	assert.Empty(t, c.servers, "a server without services is forgotten")
}

func TestCollectStoresEachKillOnceAndAlertsOnce(t *testing.T) {
	src := newFakeSource(target("a", "srv-a"))
	at := time.Unix(1_800_000_000, 0).UTC()
	src.sample = func(context.Context, string, []service.MetricsTarget) (service.HostSample, error) {
		return service.HostSample{
			OOMKills: []service.OOMEvent{
				{ServiceID: "a", ComposeService: "web", Container: "stack-web-1", At: at},
				{ServiceID: "a", Container: "a-blue", At: at.Add(time.Second)},
				{ServiceID: "a", Container: "a-green", At: time.Unix(0, 0)}, // no time: dropped
				{ServiceID: "a", Container: "a-green"},
			},
			Until: 1_800_000_060,
		}, nil
	}
	c := testCollector(src, zap.NewNop())
	c.collect(context.Background())
	c.collect(context.Background()) // the windows overlapped: the same kills again

	assert.Len(t, src.kills, 2)
	assert.Equal(t, []string{
		"a: stack-web-1 was killed for memory (compose service web)",
		"a: a-blue was killed for memory",
	}, src.alerts, "one alert per kill stored")
}

type panelRoom struct{ apps, alloc float64 }

func (p panelRoom) AppsMemoryMB(context.Context) (float64, error)      { return p.apps, nil }
func (p panelRoom) AllocatedMemoryMB(context.Context) (float64, error) { return p.alloc, nil }

// On the panel's server the apps share one memory cap: a kill there, with
// more reserved than the cap, says so, since the service may be far from
// its own limit.
func TestCollectSaysWhenThePanelsServerIsOverCommitted(t *testing.T) {
	kill := func(room PanelRoom, serverID string) string {
		t.Helper()
		src := newFakeSource(target("a", serverID))
		src.sample = func(context.Context, string, []service.MetricsTarget) (service.HostSample, error) {
			return service.HostSample{OOMKills: []service.OOMEvent{{ServiceID: "a", Container: "a-blue", At: time.Unix(1_800_000_000, 0)}}, Until: 1_800_000_060}, nil
		}
		c := testCollector(src, zap.NewNop())
		c.panel = room
		c.collect(context.Background())
		require.Len(t, src.alerts, 1)
		return src.alerts[0]
	}
	assert.Equal(t, "a: a-blue was killed for memory. The panel's server leaves its apps 747 MiB between them and their limits add up to 1.9 GiB, "+
		"so a service can be killed before it reaches its own limit: lower the limits, move a service to another server or give this one more memory",
		kill(panelRoom{apps: 747, alloc: 1920}, ""))
	assert.Equal(t, "a: a-blue was killed for memory", kill(panelRoom{apps: 747, alloc: 512}, ""), "within the cap: it reached its own limit")
	assert.Equal(t, "a: a-blue was killed for memory", kill(panelRoom{apps: 747, alloc: 1920}, "srv-a"), "another server has no shared cap")
	assert.Equal(t, "a: a-blue was killed for memory", kill(nil, ""))
}

func TestCollectReadsTheKillWindowAgainWhenAKillWasNotStored(t *testing.T) {
	src := newFakeSource(target("a", "srv-a"))
	until := int64(1000)
	src.sample = func(context.Context, string, []service.MetricsTarget) (service.HostSample, error) {
		until += 60
		return service.HostSample{
			OOMKills: []service.OOMEvent{{ServiceID: "a", Container: "a-blue", At: time.Unix(990, 0)}},
			Until:    until,
		}, nil
	}
	c := testCollector(src, zap.NewNop())
	src.killErr = errors.New("db down")
	c.collect(context.Background())
	c.collect(context.Background())
	src.killErr = nil
	c.collect(context.Background())
	c.collect(context.Background())

	assert.Equal(t, []int64{0, 0, 0, 1180}, src.sinces["srv-a"])
	assert.Equal(t, []string{"a: a-blue was killed for memory"}, src.alerts)
}

func TestCollectRecordsServerCPUWaitForRemoteServers(t *testing.T) {
	src := newFakeSource(target("a", "srv-a"), target("l", ""))
	src.sample = func(context.Context, string, []service.MetricsTarget) (service.HostSample, error) {
		return service.HostSample{CPUWaitPct: f64(12.5)}, nil
	}
	c := testCollector(src, zap.NewNop())
	c.now = func() time.Time { return time.Date(2026, 10, 13, 1, 30, 0, 0, time.FixedZone("CEST", 2*3600)) }
	c.collect(context.Background())
	assert.Equal(t, []string{"srv-a 2026-10-12T00:00:00Z 12.5"}, src.waits,
		"the server's UTC day; not the control-plane server")
}

func TestRunCollectorSummarisesAtStart(t *testing.T) {
	src := newFakeSource()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	RunCollector(ctx, src, src, src, nil, zap.NewNop())
	require.Len(t, src.hourly, 1)
	assert.Contains(t, src.hourly[0], "summarise ")
}

func TestHourlySummarisesThenDeletes(t *testing.T) {
	src := newFakeSource()
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	testCollector(src, zap.NewNop()).hourly(context.Background(), now)
	assert.Equal(t, []string{
		"summarise 2026-10-12T10:00:00Z",
		"delete metrics before 2026-10-10T10:00:00Z", // 48 h
		"gc 2026-10-12T10:00:00Z",
	}, src.hourly)
}

func TestCollectNeverExceedsTheServerLimit(t *testing.T) {
	var targets []service.MetricsTarget
	for i := range 3 * serverConcurrency {
		targets = append(targets, target(fmt.Sprintf("svc-%02d", i), fmt.Sprintf("srv-%02d", i)))
	}
	src := newFakeSource(targets...)
	src.sample = func(_ context.Context, _ string, ts []service.MetricsTarget) (service.HostSample, error) {
		time.Sleep(20 * time.Millisecond)
		return perService(map[string]service.ContainerStats{ts[0].ID: {CPUPercent: 1}}), nil
	}
	testCollector(src, zap.NewNop()).collect(context.Background())

	assert.Len(t, src.insertedIDs(), len(targets))
	assert.LessOrEqual(t, src.maxIn, serverConcurrency)
	assert.Greater(t, src.maxIn, 1, "servers are sampled in parallel")
}

func TestCollectGivesUpOnASlowServer(t *testing.T) {
	src := newFakeSource(target("dead-1", "srv-dead"), target("ok-1", "srv-ok"), target("local-1", ""))
	var deadErr error
	src.sample = func(ctx context.Context, serverID string, ts []service.MetricsTarget) (service.HostSample, error) {
		if serverID == "srv-dead" {
			<-ctx.Done() // a server that never answers
			deadErr = ctx.Err()
			return service.HostSample{}, ctx.Err()
		}
		return perService(map[string]service.ContainerStats{ts[0].ID: {CPUPercent: 2}}), nil
	}
	log, logs := observed()
	c := testCollector(src, log)
	c.timeout = 50 * time.Millisecond

	start := time.Now()
	c.collect(context.Background())

	assert.Less(t, time.Since(start), 2*time.Second, "the cycle ends after the per-server timeout")
	assert.ErrorIs(t, deadErr, context.DeadlineExceeded)
	assert.Equal(t, []string{"local-1", "ok-1"}, src.insertedIDs(), "the other servers' metrics are recorded")
	require.Equal(t, 1, logs.FilterLevelExact(zapcore.WarnLevel).Len())
	assert.Equal(t, "srv-dead", logs.FilterLevelExact(zapcore.WarnLevel).All()[0].ContextMap()["server_id"])
}

func TestCollectWarnsOnlyWhenAServerChangesState(t *testing.T) {
	src := newFakeSource(target("a-1", "srv-a"), target("local-1", ""))
	failing := true
	src.sample = func(_ context.Context, serverID string, ts []service.MetricsTarget) (service.HostSample, error) {
		if serverID == "srv-a" && failing {
			return service.HostSample{}, errors.New("SSH connect to server: connection refused")
		}
		return perService(map[string]service.ContainerStats{ts[0].ID: {CPUPercent: 1}}), nil
	}
	log, logs := observed()
	c := testCollector(src, log)

	c.collect(context.Background()) // ok → failing: warn
	c.collect(context.Background()) // still failing: quiet
	c.collect(context.Background())
	require.Equal(t, 1, logs.FilterLevelExact(zapcore.WarnLevel).Len())

	failing = false
	c.collect(context.Background()) // failing → ok: info
	c.collect(context.Background())
	assert.Equal(t, 1, logs.FilterLevelExact(zapcore.WarnLevel).Len())
	require.Equal(t, 1, logs.FilterLevelExact(zapcore.InfoLevel).Len())
	assert.Equal(t, "srv-a", logs.FilterLevelExact(zapcore.InfoLevel).All()[0].ContextMap()["server_id"])

	failing = true
	c.collect(context.Background()) // ok → failing again: warn again
	assert.Equal(t, 2, logs.FilterLevelExact(zapcore.WarnLevel).Len())
}

func TestCollectForgetsServersWithoutServices(t *testing.T) {
	src := newFakeSource(target("a-1", "srv-a"))
	src.sample = func(context.Context, string, []service.MetricsTarget) (service.HostSample, error) {
		return service.HostSample{}, errors.New("unreachable")
	}
	c := testCollector(src, zap.NewNop())
	c.collect(context.Background())
	assert.True(t, c.failing["srv-a"])

	src.targets = nil // the service was stopped or the server deleted
	c.collect(context.Background())
	assert.Empty(t, c.failing)
}

func TestCollectIsQuietWhenShuttingDown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	src := newFakeSource(target("a-1", "srv-a"), target("b-1", "srv-b"))
	src.sample = func(sctx context.Context, _ string, _ []service.MetricsTarget) (service.HostSample, error) {
		cancel()
		<-sctx.Done()
		return service.HostSample{}, sctx.Err()
	}
	log, logs := observed()
	c := testCollector(src, log)
	c.collect(ctx)

	assert.Zero(t, logs.Len(), "a cancelled cycle is not a server failure")
	assert.Empty(t, c.failing)
	assert.Empty(t, src.insertedIDs())
}

func TestCollectWarnsWhenServicesCannotBeListed(t *testing.T) {
	src := newFakeSource()
	src.listErr = errors.New("db down")
	log, logs := observed()
	testCollector(src, log).collect(context.Background())

	assert.Empty(t, src.calls)
	assert.Equal(t, 1, logs.FilterLevelExact(zapcore.WarnLevel).Len())
}
