package metrics

import (
	"context"
	"fmt"
	"maps"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/Nickpap21/toft/backend/internal/hoststats"
	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

// Collection tuning. Code defaults, not settings.
const (
	collectInterval = 60 * time.Second
	gcInterval      = time.Hour
	retention       = 48 * time.Hour
	// latencyWindow is what the Service page's p95 covers; a longer gap
	// between two Traefik readings is not counted into one minute.
	latencyWindow = 24 * time.Hour

	// serverConcurrency is how many servers are sampled at once.
	serverConcurrency = 8
	// serverTimeout bounds one server's sample (connect, list, stats), so a
	// dead or slow server cannot hold up the others or the next cycle.
	serverTimeout = 20 * time.Second
)

// Sample is one stored row: a service's compose service in one cycle.
type Sample = service.MetricSample

// Source is what the collector needs from the services: the running ones,
// one stats call per server, and the samples table. Satisfied by
// *service.ServiceManager.
type Source interface {
	ListMetricsTargets(ctx context.Context) ([]service.MetricsTarget, error)
	CollectServerStats(ctx context.Context, serverID string, targets []service.MetricsTarget, since int64) (service.HostSample, error)
	// CollectAgentStats returns the samples an agent server's agent kept
	// after after (zero: after the newest stored), oldest first, each at its
	// own time; kept is false when the server's agent keeps none and the
	// collector samples the server itself (D-113).
	CollectAgentStats(ctx context.Context, serverID string, targets []service.MetricsTarget, after time.Time) (samples []service.HostSample, kept bool, err error)
	// InsertSample stores a row collected at the cycle's time at.
	InsertSample(ctx context.Context, serviceID, composeService string, at time.Time, s Sample) error
	DeleteOldMetrics(ctx context.Context, before time.Time) error
}

// Alerter is told of each memory kill. Satisfied by *alert.Service.
type Alerter interface {
	OOMKilled(serviceID, detail string)
}

// PanelRoom is the panel's own server's ledger: the memory it leaves its
// apps together (D-62) and what their limits add up to. Satisfied by
// cmd/server's panelLedger.
type PanelRoom interface {
	AppsMemoryMB(ctx context.Context) (float64, error)
	AllocatedMemoryMB(ctx context.Context) (float64, error)
}

// Usage is where memory kills and servers' CPU waiting are stored, and
// what folds the samples into daily rows and deletes old ones. Satisfied by
// *resources.Service, which owns those tables.
type Usage interface {
	// RecordOOMKill stores a kill; false when it was already stored.
	RecordOOMKill(ctx context.Context, serviceID, composeService, container string, at time.Time) (bool, error)
	// RecordServerCPUWait adds a sample of a server's CPU waiting to its
	// UTC day.
	RecordServerCPUWait(ctx context.Context, serverID string, day time.Time, pct float64) error
	// RecordLatency stores a routed service's minute of requests, per
	// bucket (D-156); none with why it was not measured.
	RecordLatency(ctx context.Context, serviceID string, at time.Time, buckets []int32, unmeasured string) error
	SummariseDays(ctx context.Context, now time.Time) error
	GC(ctx context.Context, now time.Time) error
}

// RunCollector runs a background loop that:
//   - every 60s samples every running service, one stats call per server,
//     up to 8 servers in parallel, and stores a row per service and compose
//     service, each memory kill (and alerts on it), and each remote server's
//     CPU waiting;
//   - every hour recomputes yesterday's and today's daily rows, deletes
//     samples older than 48h, and daily rows and kills older than 30 days.
//
// The daily rows are also recomputed once at start. A cycle runs inline, so
// it never overlaps the next one; the per-server timeout bounds it. It
// stops when ctx is cancelled.
func RunCollector(ctx context.Context, src Source, alerts Alerter, usage Usage, panel PanelRoom, log *zap.Logger) {
	c := newCollector(src, alerts, usage, log)
	c.panel = panel
	collectTicker := time.NewTicker(collectInterval)
	gcTicker := time.NewTicker(gcInterval)
	defer collectTicker.Stop()
	defer gcTicker.Stop()

	// Run one collection immediately on start so the UI has data without waiting 60s.
	c.collect(ctx)
	c.summarise(ctx, c.now())

	for {
		select {
		case <-ctx.Done():
			return
		case <-collectTicker.C:
			c.collect(ctx)
		case <-gcTicker.C:
			c.hourly(ctx, c.now())
		}
	}
}

// hourly summarises, then deletes what is past its retention; the samples
// are kept long enough for yesterday to be summarised first.
func (c *collector) hourly(ctx context.Context, now time.Time) {
	c.summarise(ctx, now)
	if err := c.src.DeleteOldMetrics(ctx, now.Add(-retention)); err != nil && ctx.Err() == nil {
		c.log.Warn("metrics gc failed", zap.Error(err))
	}
	if err := c.usage.GC(ctx, now); err != nil && ctx.Err() == nil {
		c.log.Warn("usage gc failed", zap.Error(err))
	}
}

func (c *collector) summarise(ctx context.Context, now time.Time) {
	if err := c.usage.SummariseDays(ctx, now); err != nil && ctx.Err() == nil {
		c.log.Warn("daily usage summary failed", zap.Error(err))
	}
}

// collector samples services per server and remembers which servers failed
// last time, so a server that stays down is reported once, not every cycle.
type collector struct {
	src         Source
	alerts      Alerter
	usage       Usage
	panel       PanelRoom // nil: no ledger for the panel's server
	log         *zap.Logger
	now         func() time.Time
	concurrency int
	timeout     time.Duration

	mu      sync.Mutex
	failing map[string]bool         // server ID ("" = local) → its last sample failed
	servers map[string]*serverState // server ID → what its next sample compares with
}

// serverState is what a server's next sample needs from its last one. Only
// that server's collectServer touches it, and cycles do not overlap, so it
// needs no lock of its own; the map holding it does.
type serverState struct {
	// since is the start of the next kill window (HostSample.Until).
	since int64
	// containers are the last counters of each container seen in the last
	// successful sample, by container ID.
	containers map[string]reading
	// restarts are the last restart count read of each container, by
	// container ID. Unlike containers, a container missing from a sample
	// (docker stats printed "--", it started between the listing and the
	// stats, a crash loop) keeps its count, so that on its return only the
	// restarts since then are counted, not its whole count.
	restarts map[string]restartReading
	// after is the time of the newest sample stored from the server's
	// agent (D-113); zero until one is, and the source then reads it from
	// the samples table.
	after time.Time
	// latency is the server's Traefik's last reading (D-156); nil until
	// one was read, and after one that could not be.
	latency *service.LatencyReading
	// latencyAt is the sample time of latency, not the time it was handled.
	latencyAt time.Time
}

// reading is a container's cumulative cgroup counters at one sample.
type reading struct {
	periods, throttled uint64
}

// restartReading is a container's restart count and the sample it was read at.
type restartReading struct {
	count int
	at    time.Time
}

// restartMemory is how long a container's restart count is kept after it
// was last read. ponytail: a container gone longer than this and back
// counts from its return; redeploys replace containers well before.
const restartMemory = time.Hour

func newCollector(src Source, alerts Alerter, usage Usage, log *zap.Logger) *collector {
	return &collector{
		src:         src,
		alerts:      alerts,
		usage:       usage,
		log:         log,
		now:         time.Now,
		concurrency: serverConcurrency,
		timeout:     serverTimeout,
		failing:     make(map[string]bool),
		servers:     make(map[string]*serverState),
	}
}

// collect runs one cycle: list the running services, group them by server
// and sample each server once, in parallel.
func (c *collector) collect(ctx context.Context) {
	targets, err := c.src.ListMetricsTargets(ctx)
	if err != nil {
		if ctx.Err() == nil {
			c.log.Warn("metrics: failed to list running services", zap.Error(err))
		}
		return
	}
	groups := groupByServer(targets)
	at := c.now() // every row of the cycle carries it: the chart sums them by it

	var g errgroup.Group
	g.SetLimit(c.concurrency)
	for _, serverID := range sortedKeys(groups) {
		group := groups[serverID]
		g.Go(func() error {
			c.collectServer(ctx, serverID, group, at)
			return nil
		})
	}
	_ = g.Wait()
	c.forgetServersWithout(groups)
}

// serverState returns the server's state, created on its first sample.
func (c *collector) serverState(serverID string) *serverState {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.servers[serverID]
	if st == nil {
		st = &serverState{}
		c.servers[serverID] = st
	}
	return st
}

// collectServer samples one server's services, under the per-server
// timeout: it takes the samples the server's agent kept since the last one
// stored (D-113), or else samples the server now, at the cycle's time at.
// Each sample is stored at its own time (store).
func (c *collector) collectServer(ctx context.Context, serverID string, targets []service.MetricsTarget, at time.Time) {
	if ctx.Err() != nil {
		return
	}
	st := c.serverState(serverID)
	sctx, cancel := context.WithTimeout(ctx, c.timeout)
	samples, kept, err := c.src.CollectAgentStats(sctx, serverID, targets, st.after)
	if err == nil && !kept {
		var sample service.HostSample
		sample, err = c.src.CollectServerStats(sctx, serverID, targets, st.since)
		sample.At = at
		samples = []service.HostSample{sample}
	}
	cancel()
	if ctx.Err() != nil {
		return // shutting down: not the server's fault
	}
	c.recordState(serverID, len(targets), err)
	if err != nil {
		return // the containers' last readings stay: the next sample compares with them
	}
	for _, sample := range samples {
		c.store(ctx, serverID, st, targets, sample)
		if kept {
			// ponytail: an agent's sample is not asked for again, so a row
			// or kill that failed to store stays lost, as over SSH; asking
			// again would need the rows to be unique per sample.
			st.after = sample.At
		}
	}
}

// store stores a row for each compose service of the sample that has a
// running container, collected at the sample's time, the server's memory
// kills and its CPU waiting.
func (c *collector) store(ctx context.Context, serverID string, st *serverState, targets []service.MetricsTarget, sample service.HostSample) {
	at := sample.At
	seen := make(map[string]reading)
	if st.restarts == nil {
		st.restarts = make(map[string]restartReading)
	}
	for id, r := range st.restarts {
		if r.at.Before(at.Add(-restartMemory)) {
			delete(st.restarts, id)
		}
	}
	for _, t := range targets {
		byCompose, ok := sample.Containers[t.ID]
		if !ok {
			continue // no running container right now (starting, or just stopped)
		}
		for compose, containers := range byCompose {
			row := st.row(containers, seen, at)
			if err := c.src.InsertSample(ctx, t.ID, compose, at, row); err != nil && ctx.Err() == nil {
				c.log.Warn("metrics: failed to insert", zap.Error(err), zap.String("service_id", t.ID))
			}
		}
	}
	st.containers = seen // containers not seen this cycle are forgotten
	c.storeLatency(ctx, st, targets, sample)

	if c.storeKills(ctx, serverID, sample.OOMKills) && sample.Until != 0 {
		st.since = sample.Until
	}
	// The control-plane server's pressure is not read in v1.
	if sample.CPUWaitPct != nil && serverID != "" {
		day := at.UTC().Truncate(24 * time.Hour)
		if err := c.usage.RecordServerCPUWait(ctx, serverID, day, *sample.CPUWaitPct); err != nil && ctx.Err() == nil {
			c.log.Warn("metrics: failed to record server CPU waiting", zap.Error(err), serverField(serverID))
		}
	}
}

// row is one compose service's row from its containers this cycle (several
// for blue/green slots and replicas): CPU and memory summed (the chart), the
// largest single container's memory as the peak (the limit is per
// container, so a blue/green overlap must not double it), and the highest
// throttled share and waiting. Its restarts are each container's since its
// last count (st.restarts), summed: a container seen for the first time
// adds none, so a redeploy's new containers and a slot swap count nothing,
// and the row has none (nil) when one container's count was not read. It
// records each container's counters in seen and its count, read at at, in
// st.restarts.
func (st *serverState) row(containers []service.ContainerStats, seen map[string]reading, at time.Time) Sample {
	var s Sample
	restarts, restartsRead := 0, len(containers) > 0
	for _, ct := range containers {
		switch {
		case !ct.RestartsRead:
			restartsRead = false
		case ct.ContainerID != "": // without an ID there is nothing to compare with: none
			if last, ok := st.restarts[ct.ContainerID]; ok {
				restarts += max(ct.RestartCount-last.count, 0)
			}
			st.restarts[ct.ContainerID] = restartReading{count: ct.RestartCount, at: at}
		}
		s.CPUPercent += ct.CPUPercent
		s.MemoryMB += ct.MemoryMB
		mem := ct.MemoryMB
		s.MemoryPeakMB = higher(s.MemoryPeakMB, &mem)
		s.CPUWaitPct = higher(s.CPUWaitPct, ct.CPUWaitPct)
		if ct.ContainerID == "" {
			continue // no ID to compare its counters by
		}
		last, ok := st.containers[ct.ContainerID]
		seen[ct.ContainerID] = reading{periods: ct.NrPeriods, throttled: ct.NrThrottled}
		s.ThrottledPct = higher(s.ThrottledPct, throttledShare(last, ok, ct))
	}
	if restartsRead {
		s.Restarts = &restarts
	}
	return s
}

// throttledShare is the share of CPU periods a container's limit throttled
// since its last reading, 0 when it held nothing back. It is nil, not
// measured, when no period passed (without a CPU limit nr_periods stays 0),
// on a first reading, and after a restart: a counter that went down means
// the container restarted and its counters started again, so the reading
// counts as a first one (Review Focus 2).
func throttledShare(last reading, ok bool, cur service.ContainerStats) *float64 {
	if !ok || cur.NrPeriods < last.periods || cur.NrThrottled < last.throttled {
		return nil
	}
	periods, held := cur.NrPeriods-last.periods, cur.NrThrottled-last.throttled // neither went down
	if periods == 0 {
		return nil
	}
	v := min(100, float64(held)/float64(periods)*100) // the column's CHECK
	return &v
}

// storeLatency stores each routed target's requests since the server's
// last Traefik reading, a row per bucket (D-156), or why the server's
// Traefik was not read. The first reading after a start (the panel's, or
// one after a reading that failed, or one more than 24 hours after the last
// by sample time) only sets where the counts start, and so
// does a series first seen under the same Traefik start. A series missing
// from a reading (cut off, or Traefik dropped it) keeps its last counts
// until that Traefik restarts, so it is never counted again from zero. A
// sample that did not read Traefik at all (nil) changes nothing.
func (c *collector) storeLatency(ctx context.Context, st *serverState, targets []service.MetricsTarget, sample service.HostSample) {
	cur := sample.Latency
	if cur == nil {
		return
	}
	prev := st.latency
	if prev != nil && sample.At.Sub(st.latencyAt) > latencyWindow {
		prev = nil // a gap past the page's window: only a new baseline
	}
	st.latency = nil
	if cur.Unmeasured == "" {
		st.latencyAt = sample.At
		kept := &service.LatencyReading{Start: cur.Start, Counts: make(map[string][]uint64, len(cur.Counts))}
		if prev != nil && prev.Start == cur.Start {
			maps.Copy(kept.Counts, prev.Counts)
		}
		maps.Copy(kept.Counts, cur.Counts)
		st.latency = kept
		if prev == nil {
			return
		}
	}
	for _, t := range targets {
		if !t.Routed {
			continue
		}
		var buckets []int32
		if cur.Unmeasured == "" {
			name, restarted := cur.Names[t.ID], cur.Start != prev.Start
			before, had := prev.Counts[name]
			now, has := cur.Counts[name]
			if has && !had && !restarted {
				continue // a baseline: how many of its counts are this minute's is unknown
			}
			buckets = latencyMinute(before, now, restarted)
		}
		if err := c.usage.RecordLatency(ctx, t.ID, sample.At, buckets, cur.Unmeasured); err != nil && ctx.Err() == nil {
			c.log.Warn("metrics: failed to store latency", zap.Error(err), zap.String("service_id", t.ID))
		}
	}
}

// latencyMinute is the requests per bucket between two cumulative readings
// of a service's histogram. Traefik started again (restarted), or any
// count lower than before (the service left its configuration and came
// back), means the counts started from zero. ponytail: a service that left and came back with more
// requests than before is counted from its last reading, an undercount of
// its first minute back.
func latencyMinute(prev, cur []uint64, restarted bool) []int32 {
	n := hoststats.LatencyBuckets()
	if len(cur) != n {
		cur = make([]uint64, n) // no request since Traefik started
	}
	if restarted || len(prev) != n {
		prev = nil
	}
	for i := range prev {
		if cur[i] < prev[i] {
			prev = nil
			break
		}
	}
	out := make([]int32, n)
	var below uint64 // the cumulative difference of the bucket before
	for i := range cur {
		d := cur[i]
		if prev != nil {
			d -= prev[i]
		}
		out[i] = int32(min(d-min(d, below), math.MaxInt32))
		below = max(below, d)
	}
	return out
}

// higher is the higher of two measurements; nil when neither was measured.
func higher(a, b *float64) *float64 {
	if a == nil || (b != nil && *b > *a) {
		return b
	}
	return a
}

// storeKills stores each kill with a time and alerts on the ones not stored
// before: the windows of two samples overlap at their edge and after a
// backend restart. It reports whether every kill was stored; if not, the
// caller keeps the window's start so the next sample reads them again.
func (c *collector) storeKills(ctx context.Context, serverID string, kills []service.OOMEvent) bool {
	stored := true
	why := ""
	if len(kills) > 0 {
		why = c.overCommitted(ctx, serverID)
	}
	for _, k := range kills {
		if k.At.Unix() <= 0 {
			continue // no time: cannot be told from another kill
		}
		inserted, err := c.usage.RecordOOMKill(ctx, k.ServiceID, k.ComposeService, k.Container, k.At)
		if err != nil {
			stored = false
			if ctx.Err() == nil {
				c.log.Warn("metrics: failed to store a memory kill", zap.Error(err), zap.String("service_id", k.ServiceID))
			}
			continue
		}
		if inserted {
			detail := k.Container + " was killed for memory"
			if k.ComposeService != "" {
				detail += " (compose service " + k.ComposeService + ")"
			}
			c.alerts.OOMKilled(k.ServiceID, detail+why)
		}
	}
	return stored
}

// overCommitted is what a kill on the panel's server adds to its alert when
// the services there reserve more than the apps' shared cap: the kernel
// then kills inside the cap, so the service may be far from its own limit.
// "" on another server, within the cap, or when the ledger can't say.
func (c *collector) overCommitted(ctx context.Context, serverID string) string {
	if serverID != "" || c.panel == nil {
		return ""
	}
	apps, err := c.panel.AppsMemoryMB(ctx)
	if err != nil || apps <= 0 {
		return ""
	}
	allocated, err := c.panel.AllocatedMemoryMB(ctx)
	if err != nil || allocated <= apps {
		return ""
	}
	return fmt.Sprintf(". The panel's server leaves its apps %s between them and their limits add up to %s, so a service can be killed "+
		"before it reaches its own limit: lower the limits, move a service to another server or give this one more memory",
		server.Memory(apps), server.Memory(allocated))
}

// recordState logs a server's sample outcome when it differs from the last
// one: a warning when it starts failing, an info line when it recovers.
func (c *collector) recordState(serverID string, services int, err error) {
	c.mu.Lock()
	wasFailing := c.failing[serverID]
	if err != nil {
		c.failing[serverID] = true
	} else {
		delete(c.failing, serverID)
	}
	c.mu.Unlock()

	switch {
	case err != nil && !wasFailing:
		c.log.Warn("metrics: cannot sample server; its services get no metrics until it recovers",
			serverField(serverID), zap.Int("services", services), zap.Error(err))
	case err == nil && wasFailing:
		c.log.Info("metrics: sampling server again", serverField(serverID))
	}
}

// forgetServersWithout drops the state of servers that have no running
// service any more, so the maps do not grow with deleted servers.
func (c *collector) forgetServersWithout(groups map[string][]service.MetricsTarget) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id := range c.failing {
		if _, ok := groups[id]; !ok {
			delete(c.failing, id)
		}
	}
	for id := range c.servers {
		if _, ok := groups[id]; !ok {
			delete(c.servers, id)
		}
	}
}

func serverField(serverID string) zap.Field {
	if serverID == "" {
		return zap.String("server", "local")
	}
	return zap.String("server_id", serverID)
}

// groupByServer groups targets by ServerID ("" = the local Docker host).
func groupByServer(targets []service.MetricsTarget) map[string][]service.MetricsTarget {
	groups := make(map[string][]service.MetricsTarget)
	for _, t := range targets {
		groups[t.ServerID] = append(groups[t.ServerID], t)
	}
	return groups
}

func sortedKeys(m map[string][]service.MetricsTarget) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
