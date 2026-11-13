package resources

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

var testBounds = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// A known histogram: 1,000 requests, 900 under 50 ms and 100 between 50
// and 100 ms. The 950th is halfway into the 50–100 ms bucket: 75 ms.
func TestP95InterpolatesWithinItsBucket(t *testing.T) {
	counts := []int64{0, 0, 400, 500, 100, 0, 0, 0, 0, 0, 0, 0}
	ms, b := p95(counts, testBounds)
	assert.InDelta(t, 75, ms, 1e-9)
	require.NotNil(t, b.HighMS)
	assert.Equal(t, 50.0, b.LowMS)
	assert.Equal(t, 100.0, *b.HighMS)

	// All in the first bucket: from 0.
	ms, b = p95([]int64{20, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, testBounds)
	assert.InDelta(t, 4.75, ms, 1e-9)
	assert.Equal(t, 0.0, b.LowMS)

	// Above the top bound: the top bound, with no upper one.
	ms, b = p95([]int64{10, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 90}, testBounds)
	assert.Equal(t, 10000.0, ms)
	assert.Equal(t, LatencyBucket{LowMS: 10000}, b)

	// Exactly on a bucket's end.
	ms, _ = p95([]int64{95, 5, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, testBounds)
	assert.InDelta(t, 5, ms, 1e-9)
}

func TestLatency24h(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	repo := &fakeRepo{}
	s := newTestService(repo, now)
	ctx := context.Background()

	assert.Nil(t, s.latency24h(ctx, "svc", now), "no minute recorded: no tile")
	assert.Equal(t, now.Add(-24*time.Hour), repo.latencySince)

	first := now.Add(-3 * time.Hour)
	repo.latency = LatencyWindow{Minutes: 180, FirstMeasured: &first, Buckets: []int64{0, 0, 400, 500, 100, 0, 0, 0, 0, 0, 0, 0}}
	got := s.latency24h(ctx, "svc", now)
	require.NotNil(t, got)
	assert.Equal(t, int64(1000), got.Requests)
	require.NotNil(t, got.P95MS)
	assert.InDelta(t, 75, *got.P95MS, 1e-9)
	assert.Equal(t, &first, got.Since, "measured for 3 of the 24 hours")
	assert.Nil(t, got.Unmeasured)

	covered := now.Add(-24*time.Hour + time.Minute)
	repo.latency = LatencyWindow{Minutes: 1440, FirstMeasured: &covered, Buckets: []int64{19, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}}
	got = s.latency24h(ctx, "svc", now)
	assert.Equal(t, int64(19), got.Requests)
	assert.Nil(t, got.P95MS, "under 20 requests: the count only")
	assert.Nil(t, got.Since, "the minutes cover the 24 hours")

	repo.latency = LatencyWindow{Minutes: 60, NewestUnmeasured: "adopted"}
	got = s.latency24h(ctx, "svc", now)
	require.NotNil(t, got.Unmeasured)
	assert.Equal(t, "adopted", *got.Unmeasured)
	assert.Zero(t, got.Requests)
	assert.Nil(t, got.P95MS)
}

// A minute that can't be read answers as none recorded (the contract has no
// other null), so the failure is logged where it can be seen.
func TestLatency24hReadFailureIsLogged(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	core, logs := observer.New(zap.WarnLevel)
	s := NewService(&fakeRepo{latencyErr: errors.New("connection refused")}, zap.New(core))
	assert.Nil(t, s.latency24h(context.Background(), "svc", now))
	require.Equal(t, 1, logs.Len())
	e := logs.All()[0]
	assert.Equal(t, zap.WarnLevel, e.Level)
	assert.Equal(t, "read latency: connection refused", e.ContextMap()["error"])
}
