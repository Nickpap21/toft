package resources

import (
	"fmt"
	"math"
	"strconv"
	"time"
)

const (
	fullDaySamples = 1152 // 80% of 1,440 one-minute samples
	minMemoryMB    = 64
	minCPUCores    = 0.25 // the rounding unit; a 0-core limit is invalid
	quietBand      = 0.25
)

func roundUpMB(mb float64) int64 {
	v := int64(math.Ceil(mb/64)) * 64
	if v < minMemoryMB {
		return minMemoryMB
	}
	return v
}

func roundUpCores(c float64) float64 { return math.Max(math.Ceil(c*4)/4, minCPUCores) }

func formatCores(c float64) string { return strconv.FormatFloat(c, 'f', -1, 64) }

func within(a, b float64) bool { return b > 0 && math.Abs(a-b)/b <= quietBand }

// lastWeekStart is the first UTC day of the 7 days that count for kills,
// throttling and downgrades.
func lastWeekStart(now time.Time) time.Time {
	return now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -7)
}

// SuggestMemory applies the D-41 memory rules to the last 14 days. It returns
// nil when no change is worth showing.
func SuggestMemory(days []DailyUsage, limitMB int64, now time.Time) *Suggestion {
	if len(days) == 0 {
		return nil
	}
	weekAgo := lastWeekStart(now)
	var peak float64
	kills, fullDays, samples := 0, 0, 0
	var lastKill time.Time
	for _, d := range days {
		peak = math.Max(peak, d.MemoryPeakMB)
		samples += d.Samples
		if d.Samples >= fullDaySamples && !d.Day.Before(weekAgo) {
			fullDays++ // only the last 7 days count toward a downgrade
		}
		if d.OOMKills > 0 && !d.Day.Before(weekAgo) {
			kills += d.OOMKills
			lastKill = d.Day
		}
	}
	limit := float64(limitMB)
	switch {
	case limitMB == 0:
		// A first limit rests on at least a day of samples: an hour's peak
		// misses the nightly job or the busy hour.
		if peak <= 0 || samples < fullDaySamples {
			return nil
		}
		return &Suggestion{Direction: DirectionSet, Value: FormatMB(roundUpMB(1.3 * peak)), Days: len(days),
			Reason: fmt.Sprintf("Peak %s over %d days, with 30%% headroom", FormatMB(int64(math.Ceil(peak))), len(days))}
	case kills > 0:
		v := roundUpMB(math.Max(1.5*peak, 1.25*limit))
		return &Suggestion{Direction: DirectionUp, Value: FormatMB(v), Days: len(days),
			Reason: fmt.Sprintf("Killed for memory %d time(s), last on %s", kills, lastKill.Format("2 Jan"))}
	case peak > 0.9*limit:
		v := roundUpMB(1.3 * peak) // shown even when close: the service is near its limit
		return &Suggestion{Direction: DirectionUp, Value: FormatMB(v), Days: len(days),
			Reason: fmt.Sprintf("Peak %s, over 90%% of the limit", FormatMB(int64(math.Ceil(peak))))}
	case peak < 0.5*limit && fullDays >= 7:
		v := roundUpMB(1.3 * peak)
		if within(float64(v), limit) {
			return nil
		}
		return &Suggestion{Direction: DirectionDown, Value: FormatMB(v), Days: len(days),
			Reason: fmt.Sprintf("Peak %s of %s over %d days", FormatMB(int64(math.Ceil(peak))), FormatMB(limitMB), len(days))}
	}
	return nil
}

// SuggestCPU applies the D-41 CPU rules. It returns nil without a CPU limit or
// when no change is worth showing. Upward suggestions always show; downward
// ones inside the quiet band do not.
func SuggestCPU(days []DailyUsage, limitCores float64, now time.Time) *Suggestion {
	if limitCores <= 0 || len(days) == 0 {
		return nil
	}
	weekAgo := lastWeekStart(now)
	var throttled, p95 float64
	fullDays := 0
	for _, d := range days {
		if d.Day.Before(weekAgo) {
			continue
		}
		if d.ThrottledMax != nil {
			throttled = math.Max(throttled, *d.ThrottledMax)
		}
		p95 = math.Max(p95, d.CPUP95/100)
		if d.Samples >= fullDaySamples {
			fullDays++
		}
	}
	switch {
	case throttled > 10:
		return &Suggestion{Direction: DirectionUp, Value: formatCores(roundUpCores(1.5 * limitCores)), Days: len(days),
			Reason: fmt.Sprintf("Throttled %.0f%% of the time on its worst day this week", throttled)}
	case p95 < 0.3*limitCores && fullDays >= 7:
		v := roundUpCores(1.5 * p95)
		if within(v, limitCores) {
			return nil
		}
		return &Suggestion{Direction: DirectionDown, Value: formatCores(v), Days: len(days),
			Reason: fmt.Sprintf("95th percentile %s of %s cores over the last week", formatCores(math.Round(p95*100)/100), formatCores(limitCores))}
	}
	return nil
}
