package resources

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/Nickpap21/toft/backend/internal/hoststats"
	"go.uber.org/zap"
)

// Request latency (D-156): the collector stores each routed service's
// requests per minute by duration bucket; the Service page shows their
// 95th percentile over the last 24 hours.

const (
	// minLatencyRequests is the fewest requests a p95 is given for: under
	// it the count alone shows (the founders, 2026-11-05).
	minLatencyRequests = 20
	// keepLatency is how long the minutes are kept: the 24 hours shown, and
	// as long again, as the samples are.
	keepLatency = 48 * time.Hour
	// latencyCovered: a window whose first measured minute is this close to
	// its start counts as covered, so "since" isn't shown for a minute's
	// difference.
	latencyCovered = 5 * time.Minute
)

// RecordLatency stores a routed service's minute of requests per bucket,
// or none and why it was not measured. Satisfies metrics.Usage.
func (s *Service) RecordLatency(ctx context.Context, serviceID string, at time.Time, buckets []int32, unmeasured string) error {
	if err := s.repo.InsertLatency(ctx, serviceID, at, buckets, unmeasured); err != nil {
		return fmt.Errorf("record latency: %w", err)
	}
	return nil
}

// latency24h is serviceID's latency over the 24 hours before now; nil
// when no minute was recorded, or they could not be read (logged: the
// limits and usage still answer).
func (s *Service) latency24h(ctx context.Context, serviceID string, now time.Time) *Latency {
	start := now.Add(-24 * time.Hour)
	w, err := s.repo.LatencySince(ctx, serviceID, start, hoststats.LatencyBuckets())
	if err != nil {
		if ctx.Err() == nil {
			// latency_24h null reads as no minute recorded: say it failed.
			s.log.Warn("resources: latency_24h answered null", zap.String("service_id", serviceID), zap.Error(fmt.Errorf("read latency: %w", err)))
		}
		return nil
	}
	if w.Minutes == 0 {
		return nil
	}
	out := &Latency{}
	if w.NewestUnmeasured != "" {
		why := w.NewestUnmeasured
		out.Unmeasured = &why
	}
	if w.FirstMeasured == nil {
		return out
	}
	if w.FirstMeasured.After(start.Add(latencyCovered)) {
		since := w.FirstMeasured.UTC()
		out.Since = &since
	}
	for _, n := range w.Buckets {
		out.Requests += n
	}
	if out.Requests >= minLatencyRequests {
		ms, bucket := p95(w.Buckets, hoststats.LatencyBoundsSeconds())
		ms = math.Round(ms*10) / 10 // a tenth of a millisecond is more than the buckets tell
		out.P95MS, out.P95Bucket = &ms, &bucket
	}
	return out
}

// p95 is the 95th percentile of a histogram in milliseconds, interpolated
// linearly within the bucket it falls in, and that bucket. counts are per
// bucket, one per bound (seconds) and one above them; above the top bound
// the percentile is the top bound itself, as nothing tells how far above.
func p95(counts []int64, bounds []float64) (float64, LatencyBucket) {
	var total int64
	for _, n := range counts {
		total += n
	}
	rank := 0.95 * float64(total)
	var below int64
	for i, n := range counts {
		if n == 0 || float64(below+n) < rank {
			below += n
			continue
		}
		low := 0.0
		if i > 0 {
			low = bounds[i-1]
		}
		if i >= len(bounds) {
			return low * 1000, LatencyBucket{LowMS: low * 1000}
		}
		high := bounds[i]
		highMS := high * 1000
		return (low + (high-low)*(rank-float64(below))/float64(n)) * 1000, LatencyBucket{LowMS: low * 1000, HighMS: &highMS}
	}
	return 0, LatencyBucket{} // no requests: callers don't ask
}
