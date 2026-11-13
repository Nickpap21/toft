// Package resources is D-41: memory limits on every deploy, their defaults
// by kind of service, and the suggestions the panel makes from measured
// usage (private/specs/2026-10-12-resource-limits-design.md).
package resources

import (
	"fmt"
	"strconv"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/listversion"
)

// Kind is what a service is, for its default memory limit.
type Kind string

const (
	KindDatabase Kind = "database"
	KindCache    Kind = "cache"
	KindTool     Kind = "tool"
	KindApp      Kind = "app"
)

// DailyUsage is one UTC day of measured usage for a service (or one compose
// service of it), as the suggestion rules read it.
type DailyUsage struct {
	Day          time.Time // UTC midnight
	Samples      int
	MemoryPeakMB float64
	CPUP95       float64  // percent of one core × cores, as docker stats reports (100 = one core)
	ThrottledMax *float64 // percent; nil without a CPU limit
	CPUWaitAvg   *float64 // percent; nil when not read
	OOMKills     int
}

// Direction is which way a suggestion moves a limit.
type Direction string

const (
	DirectionUp   Direction = "up"
	DirectionDown Direction = "down"
	DirectionSet  Direction = "set" // no limit yet
)

// Suggestion is a limit the panel proposes, with the reason shown as is.
type Suggestion struct {
	Direction Direction `json:"direction"`
	Value     string    `json:"value"`  // "768m", or cores "1.5"
	Reason    string    `json:"reason"` // one sentence
	Days      int       `json:"days"`   // days of data it rests on
}

// LimitSource is where a memory limit comes from.
type LimitSource string

const (
	SourcePanel       LimitSource = "panel"        // the service's settings, or the panel's limit for a compose service
	SourceComposeFile LimitSource = "compose_file" // the compose file's; the panel has none
	SourceNone        LimitSource = "none"
)

// ServiceResources is GET /services/:serviceID/resources: the memory rule's
// grace date and, per compose service ("" for a single container), its
// limits, the last 14 days' usage and the suggestions.
type ServiceResources struct {
	// version is that of its panel limits: what a replace must send back.
	version    string
	GraceUntil *time.Time    `json:"grace_until"`
	Rows       []ResourceRow `json:"rows"`
	// Latency24h is the service's request latency over the last 24 hours,
	// from its server's Traefik (D-156); nil when no minute of it was
	// recorded (no domain, or just started).
	Latency24h *Latency `json:"latency_24h"`
}

// Latency is a routed service's HTTP requests over a window, as its
// server's Traefik measured them.
type Latency struct {
	// Requests is how many the service answered in the measured minutes.
	Requests int64 `json:"requests"`
	// P95MS is their 95th percentile, interpolated within its bucket; nil
	// under minLatencyRequests.
	P95MS *float64 `json:"p95_ms"`
	// P95Bucket is the bucket the 95th percentile falls in.
	P95Bucket *LatencyBucket `json:"p95_bucket"`
	// Since is the first measured minute while they cover less than the
	// window; nil once they cover it, or none was measured.
	Since *time.Time `json:"since"`
	// Unmeasured says why the newest minute was not measured
	// (hoststats.Unmeasured*); nil when it was.
	Unmeasured *string `json:"unmeasured"`
}

// LatencyBucket is a histogram bucket's bounds in milliseconds; HighMS is
// nil for the one above the top bound.
type LatencyBucket struct {
	LowMS  float64  `json:"low_ms"`
	HighMS *float64 `json:"high_ms"`
}

// LatencyWindow is how a service's minutes in a window were read.
type LatencyWindow struct {
	Minutes          int        // rows in the window
	NewestUnmeasured string     // the newest row's reason; "" when it was measured
	FirstMeasured    *time.Time // nil when none was
	// Buckets are the measured minutes' requests summed per bucket.
	Buckets []int64
}

type ResourceRow struct {
	ComposeService string      `json:"compose_service"`
	Kind           Kind        `json:"kind"`
	Memory         MemoryUsage `json:"memory"`
	CPU            CPUUsage    `json:"cpu"`
	// Restarts24h is how many times Docker restarted its containers in the
	// last 24 hours; nil when no sample of them carries a count.
	// RestartsSince is when the counted samples start while they cover less
	// than the 24 hours.
	Restarts24h   *int       `json:"restarts_24h"`
	RestartsSince *time.Time `json:"restarts_since"`
}

// Restarts are a compose service's counted samples from a time on: how many
// carry a count, the restarts they add up to, when the first was collected,
// and whether a sample before the time carries one.
type Restarts struct {
	Samples  int
	Restarts int
	FirstAt  time.Time
	Earlier  bool
}

type MemoryUsage struct {
	Limit      string      `json:"limit"` // the one in force, "" for none
	Source     LimitSource `json:"source"`
	FileLimit  string      `json:"file_limit"`  // the compose file's, also when the panel's overrides it
	PeakMB     *float64    `json:"peak_14d_mb"` // nil without data
	Days       int         `json:"days"`        // days measured of the last 14 (UTC)
	OOMKills   int         `json:"oom_kills_7d"`
	Suggestion *Suggestion `json:"suggestion"`
}

type CPUUsage struct {
	Limit        string      `json:"limit"`             // cores, "" for none
	P95Cores     *float64    `json:"p95_cores"`         // the highest daily 95th percentile
	ThrottledMax *float64    `json:"throttled_max_pct"` // nil without a CPU limit or data
	WaitAvg      *float64    `json:"wait_avg_pct"`      // yesterday's (UTC) average
	Suggestion   *Suggestion `json:"suggestion"`
}

// ServerResources is GET /servers/:serverID/resources: the memory
// suggestions of the server's services, the memory following the downward
// ones would free, and yesterday's (UTC) CPU waiting.
type ServerResources struct {
	Services []ServerSuggestion `json:"services"`
	FreesMB  int64              `json:"frees_mb"`
	CPUWait  *CPUWaitDay        `json:"cpu_wait"`
}

type ServerSuggestion struct {
	ServiceID      string     `json:"service_id"`
	ServiceName    string     `json:"service_name"`
	ComposeService string     `json:"compose_service"`
	Suggestion     Suggestion `json:"suggestion"`
}

type CPUWaitDay struct {
	Day    string  `json:"day"` // 2006-01-02
	AvgPct float64 `json:"avg_pct"`
}

// DefaultLimit is the memory limit a new single-container service of a kind
// gets when none is set (D-41).
type DefaultLimit struct {
	Kind     Kind  `json:"kind"`
	MemoryMB int64 `json:"memory_mb"`
}

// versionOf says which state of a stack's panel limits a client read, and a
// replace must send it back: a digest of what the replace can delete.
func versionOf(limits map[string]ComposeLimit) string {
	lines := make([]string, 0, len(limits))
	for name, l := range limits {
		cpu := "none"
		if l.CPUCores != nil {
			cpu = strconv.FormatFloat(*l.CPUCores, 'f', -1, 64)
		}
		lines = append(lines, fmt.Sprintf("%q %d %s", name, l.MemoryMB, cpu))
	}
	return listversion.Of(lines)
}
