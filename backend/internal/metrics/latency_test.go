package metrics

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// cum is a cumulative histogram over the 12 buckets: n requests in the
// first, m more in the fourth (25–50 ms), nothing above.
func cum(n, m uint64) []uint64 {
	out := make([]uint64, 12)
	for i := range out {
		out[i] = n
		if i >= 3 {
			out[i] += m
		}
	}
	return out
}

// minute is the non-cumulative row of cum(n, m)'s difference.
func minute(n, m int32) []int32 {
	out := make([]int32, 12)
	out[0], out[3] = n, m
	return out
}

// withLatency is a sample of a's container and the server's Traefik, whose
// series are named as the services whose counts they are.
func withLatency(start float64, counts map[string][]uint64) service.HostSample {
	s := perService(map[string]service.ContainerStats{"a": {CPUPercent: 1}})
	s.Latency = &service.LatencyReading{Start: start, Counts: counts, Names: make(map[string]string)}
	for id := range counts {
		s.Latency.Names[id] = id
	}
	return s
}

// withLatencyFor is withLatency as attribution gives it for targets: every
// series, and the names of the targets' only.
func withLatencyFor(start float64, counts map[string][]uint64, targets []service.MetricsTarget) service.HostSample {
	s := withLatency(start, counts)
	s.Latency.Names = make(map[string]string)
	for _, t := range targets {
		s.Latency.Names[t.ID] = t.ID
	}
	return s
}

func routed(id, serverID string) service.MetricsTarget {
	t := target(id, serverID)
	t.Routed = true
	return t
}

// latencyCycles runs one cycle per sample on c and returns the rows
// stored, "<service> <buckets> <unmeasured>".
func latencyCycles(c *collector, src *fakeSource, samples ...service.HostSample) []string {
	for _, s := range samples {
		src.sample = func(context.Context, string, []service.MetricsTarget) (service.HostSample, error) { return s, nil }
		c.collect(context.Background())
	}
	return src.latencyRows()
}

// The first reading is only where the counts start: a row per routed
// service per minute after it, its requests since the reading before. A
// routed service Traefik has answered nothing for gets a row of zeros; one
// without a domain gets none.
func TestLatencyIsTheDifferenceBetweenTwoReadings(t *testing.T) {
	src := newFakeSource(routed("a", "srv-a"), routed("quiet", "srv-a"), target("worker", "srv-a"))
	c := testCollector(src, zap.NewNop())
	rows := latencyCycles(c, src,
		withLatency(1, map[string][]uint64{"a": cum(10, 2), "worker": cum(1, 1)}),
		withLatency(1, map[string][]uint64{"a": cum(15, 3), "worker": cum(2, 2)}),
	)
	assert.Equal(t, []string{
		fmt.Sprintf("a %v ", minute(5, 1)),
		fmt.Sprintf("quiet %v ", minute(0, 0)),
	}, rows)
}

// A counter that went down, or another Traefik start, means the counts
// started again: the minute's requests are the counts themselves. A series
// new under the same start is only where its counts start: nothing tells
// how many of them came in the last minute.
func TestLatencyAfterATraefikRestart(t *testing.T) {
	src := newFakeSource(routed("a", "srv-a"), routed("b", "srv-a"))
	c := testCollector(src, zap.NewNop())
	rows := latencyCycles(c, src,
		withLatency(1, map[string][]uint64{"a": cum(100, 0)}),
		withLatency(1, map[string][]uint64{"a": cum(3, 1), "b": cum(2, 0)}),   // a went down; b is new
		withLatency(2, map[string][]uint64{"a": cum(400, 0), "b": cum(9, 0)}), // restarted, a higher than before
	)
	assert.Equal(t, []string{
		fmt.Sprintf("a %v ", minute(3, 1)),
		fmt.Sprintf("a %v ", minute(400, 0)),
		fmt.Sprintf("b %v ", minute(9, 0)),
	}, rows, "never a negative count, never a minute counted twice")
}

// A partial read drops series: the next minute counts a dropped one from
// its last counts, never from zero.
func TestLatencyAfterAPartialRead(t *testing.T) {
	src := newFakeSource(routed("a", "srv-a"), routed("b", "srv-a"))
	rows := latencyCycles(testCollector(src, zap.NewNop()), src,
		withLatency(1, map[string][]uint64{"a": cum(10, 0), "b": cum(100, 0)}),
		withLatency(1, map[string][]uint64{"a": cum(20, 0)}), // cut off before b
		withLatency(1, map[string][]uint64{"a": cum(30, 0), "b": cum(130, 0)}),
	)
	assert.Equal(t, []string{
		fmt.Sprintf("a %v ", minute(10, 0)),
		fmt.Sprintf("b %v ", minute(0, 0)),
		fmt.Sprintf("a %v ", minute(10, 0)),
		fmt.Sprintf("b %v ", minute(30, 0)),
	}, rows)
}

// A service that left the targets (its build failed, its old container
// still serving) and came back is counted from its counts of the minute
// before, which Traefik kept reporting meanwhile.
func TestLatencyOfAServiceBackInTheTargets(t *testing.T) {
	a, b := routed("a", "srv-a"), routed("b", "srv-a")
	src := newFakeSource(a, b)
	c := testCollector(src, zap.NewNop())
	for _, step := range []struct {
		targets []service.MetricsTarget
		a, b    uint64
	}{{[]service.MetricsTarget{a, b}, 10, 100}, {[]service.MetricsTarget{a}, 20, 5000}, {[]service.MetricsTarget{a, b}, 30, 5010}} {
		src.targets = step.targets
		src.sample = func(_ context.Context, _ string, ts []service.MetricsTarget) (service.HostSample, error) {
			return withLatencyFor(1, map[string][]uint64{"a": cum(step.a, 0), "b": cum(step.b, 0)}, ts), nil
		}
		c.collect(context.Background())
	}
	assert.Equal(t, []string{
		fmt.Sprintf("a %v ", minute(10, 0)),
		fmt.Sprintf("a %v ", minute(10, 0)),
		fmt.Sprintf("b %v ", minute(10, 0)),
	}, src.latencyRows())
}

// An agent's kept samples are compared in order, each with the one before.
func TestLatencyFromAnAgentsKeptSamples(t *testing.T) {
	src := newFakeSource(routed("a", "srv-a"))
	t0 := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	kept := func(at time.Time, n uint64) service.HostSample {
		s := withLatency(1, map[string][]uint64{"a": cum(n, 0)})
		s.At = at
		return s
	}
	src.agent = func(_ string, after time.Time) ([]service.HostSample, bool, error) {
		if after.IsZero() {
			return []service.HostSample{kept(t0, 10), kept(t0.Add(time.Minute), 30), kept(t0.Add(2*time.Minute), 60)}, true, nil
		}
		return []service.HostSample{}, true, nil
	}
	testCollector(src, zap.NewNop()).collect(context.Background())
	assert.Equal(t, []string{
		fmt.Sprintf("a 09:01 %v ", minute(20, 0)),
		fmt.Sprintf("a 09:02 %v ", minute(30, 0)),
	}, src.latencyRowsAt())
}

// A gap of more than 24 hours between two readings (the agent kept samples
// longer than the Service page's window) would put requests older than the
// window into one minute: the reading after it is only where the counts start.
func TestLatencyAfterALongGap(t *testing.T) {
	t0 := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		gap  time.Duration
		rows []string
	}{
		{25 * time.Hour, []string{}},
		{23 * time.Hour, []string{fmt.Sprintf("a 08:00 %v ", minute(20, 0))}},
	} {
		t.Run(tc.gap.String(), func(t *testing.T) {
			src := newFakeSource(routed("a", "srv-a"))
			kept := func(at time.Time, n uint64) service.HostSample {
				s := withLatency(1, map[string][]uint64{"a": cum(n, 0)})
				s.At = at
				return s
			}
			src.agent = func(_ string, after time.Time) ([]service.HostSample, bool, error) {
				if after.IsZero() {
					return []service.HostSample{kept(t0, 10), kept(t0.Add(tc.gap), 30)}, true, nil
				}
				return []service.HostSample{}, true, nil
			}
			testCollector(src, zap.NewNop()).collect(context.Background())
			assert.Equal(t, tc.rows, src.latencyRowsAt())
		})
	}
}

// A restarted panel has no reading to compare with: its first is a new start.
func TestLatencyAfterAPanelRestart(t *testing.T) {
	src := newFakeSource(routed("a", "srv-a"))
	latencyCycles(testCollector(src, zap.NewNop()), src, withLatency(1, map[string][]uint64{"a": cum(10, 0)}))
	rows := latencyCycles(testCollector(src, zap.NewNop()), src, withLatency(1, map[string][]uint64{"a": cum(50, 0)}))
	assert.Empty(t, rows, "the restarted panel's first reading writes nothing")
}

// A server whose Traefik can't be read has a row per routed service saying
// why; once it can, the first reading starts the counts again.
func TestLatencyNotMeasured(t *testing.T) {
	src := newFakeSource(routed("a", "srv-a"), target("worker", "srv-a"))
	unmeasured := perService(map[string]service.ContainerStats{"a": {CPUPercent: 1}})
	unmeasured.Latency = &service.LatencyReading{Unmeasured: "adopted"}
	rows := latencyCycles(testCollector(src, zap.NewNop()), src,
		withLatency(1, map[string][]uint64{"a": cum(10, 0)}),
		unmeasured,
		withLatency(1, map[string][]uint64{"a": cum(20, 0)}),
		service.HostSample{}, // not read: a source without latency
	)
	assert.Equal(t, []string{"a [] adopted"}, rows)
}

func (f *fakeSource) RecordLatency(_ context.Context, serviceID string, at time.Time, buckets []int32, unmeasured string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.latency = append(f.latency, latencyRow{serviceID, at, buckets, unmeasured})
	return nil
}

type latencyRow struct {
	id         string
	at         time.Time
	buckets    []int32
	unmeasured string
}

func (f *fakeSource) latencyRows() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.latency))
	for _, r := range f.latency {
		out = append(out, fmt.Sprintf("%s %v %s", r.id, r.buckets, r.unmeasured))
	}
	return out
}

func (f *fakeSource) latencyRowsAt() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.latency))
	for _, r := range f.latency {
		out = append(out, fmt.Sprintf("%s %s %v %s", r.id, r.at.Format("15:04"), r.buckets, r.unmeasured))
	}
	return out
}
