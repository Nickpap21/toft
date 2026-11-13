package resources

import (
	"context"
	"time"
)

// Repository is the data access for memory limits, grace dates and usage.
type Repository interface {
	// ComposeLimits are the panel's limits per compose service.
	ComposeLimits(ctx context.Context, serviceID string) (map[string]ComposeLimit, error)
	// ComposeLimitsFor is ComposeLimits of several services in one query,
	// by service ID.
	ComposeLimitsFor(ctx context.Context, serviceIDs []string) (map[string]map[string]ComposeLimit, error)
	// ReplaceComposeLimits makes limits the panel's limits of a compose
	// stack, in one transaction: a compose service absent from it has none.
	ReplaceComposeLimits(ctx context.Context, serviceID, version string, limits map[string]ComposeLimit) error
	// AddDefaultComposeLimits stores memory limits (MiB) for compose
	// services that have no panel limit; existing ones are kept.
	AddDefaultComposeLimits(ctx context.Context, serviceID string, defaults map[string]int64) error
	// GraceUntil is the service's grace date; nil when it has none.
	GraceUntil(ctx context.Context, serviceID string) (*time.Time, error)
	// ClaimGrace records, once per instance, that the grace dates were set
	// and, only when this call recorded it, gives until to every
	// single-container service without a memory limit and every compose
	// stack, in one transaction. services is how many got it.
	ClaimGrace(ctx context.Context, until time.Time) (claimed bool, services int64, err error)
	// UsageDaily are the service's daily rows from the UTC day since on, by
	// compose service ("" for a single-container service), oldest first.
	UsageDaily(ctx context.Context, serviceID string, since time.Time) (map[string][]DailyUsage, error)
	// UsageDailyFor is UsageDaily of several services in one query, by
	// service ID.
	UsageDailyFor(ctx context.Context, serviceIDs []string, since time.Time) (map[string]map[string][]DailyUsage, error)
	// OOMKillsSince counts a service's memory kills from since on, by
	// compose service.
	OOMKillsSince(ctx context.Context, serviceID string, since time.Time) (map[string]int, error)
	// RestartsSince reads a service's restarts from since on, by compose
	// service, in one query; a compose service without a counted sample is
	// absent.
	RestartsSince(ctx context.Context, serviceID string, since time.Time) (map[string]Restarts, error)
	// ServerCPUWait is a server's average CPU waiting (percent) on the UTC
	// day day; nil when that day has no row.
	ServerCPUWait(ctx context.Context, serverID string, day time.Time) (*float64, error)
	// SummariseUsage recomputes every service's daily rows of the UTC days
	// from from (inclusive) to to (exclusive) from its samples and kills,
	// overwriting them.
	SummariseUsage(ctx context.Context, from, to time.Time) error
	// DeleteUsageBefore deletes the daily rows of services and servers of
	// the days before before's, and the kills before before.
	DeleteUsageBefore(ctx context.Context, before time.Time) error
	// InsertOOMKill stores a memory kill; false when the same kill (service,
	// container, time) was already stored.
	InsertOOMKill(ctx context.Context, serviceID, composeService, container string, at time.Time) (bool, error)
	// UpsertServerCPUWait adds a sample of a server's CPU waiting to the
	// day's running sum and maximum.
	UpsertServerCPUWait(ctx context.Context, serverID string, day time.Time, pct float64) error
	// InsertLatency stores a service's minute of requests per bucket, or
	// none and why (D-156); a minute already stored stays.
	InsertLatency(ctx context.Context, serviceID string, at time.Time, buckets []int32, unmeasured string) error
	// LatencySince reads a service's minutes after since; only those with
	// bucketCount buckets are summed.
	LatencySince(ctx context.Context, serviceID string, since time.Time, bucketCount int) (LatencyWindow, error)
	// DeleteLatencyBefore deletes every service's minutes before before.
	DeleteLatencyBefore(ctx context.Context, before time.Time) error
}

// ComposeLimit is the panel's limit for one compose service.
type ComposeLimit struct {
	MemoryMB int64    `json:"memory_mb"`
	CPUCores *float64 `json:"cpu_cores"`
}
