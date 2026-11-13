package resources

import (
	"testing"
	"time"
)

func TestSuggestMemory(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	day := func(ago int, peak float64) DailyUsage {
		return DailyUsage{Day: time.Date(2026, 10, 20-ago, 0, 0, 0, 0, time.UTC), Samples: 1440, MemoryPeakMB: peak}
	}
	week := func(peak float64) []DailyUsage {
		out := []DailyUsage{}
		for ago := 7; ago >= 1; ago-- {
			out = append(out, day(ago, peak))
		}
		return out
	}
	killed := week(500)
	killed[6].OOMKills = 2

	cases := []struct {
		name  string
		days  []DailyUsage
		limit int64
		want  *Suggestion // nil = none
	}{
		{"kill: max(1.5×peak, 1.25×limit), rounded up to 64", killed, 512, &Suggestion{Direction: "up", Value: "768m"}}, // 1.5×500=750 → 768; 1.25×512=640
		{"kill with a low peak: 1.25×limit wins", func() []DailyUsage { d := week(100); d[0].OOMKills = 1; return d }(), 1024, &Suggestion{Direction: "up", Value: "1280m"}},
		{"near the limit: 1.3×peak", week(470), 512, &Suggestion{Direction: "up", Value: "640m"}},                  // 611 → 640
		{"far under for a week: down to 1.3×peak", week(100), 1024, &Suggestion{Direction: "down", Value: "192m"}}, // 130 → 192
		{"far under but 6 days: nothing", week(100)[1:], 1024, nil},
		{"far under but a sparse day: nothing", func() []DailyUsage { d := week(100); d[3].Samples = 1000; return d }(), 1024, nil},
		{"between 50% and 90%: nothing", week(300), 512, nil},
		{"down that rounds back to the limit: nothing", week(60), 128, nil},                // 60 < 64; 1.3×60=78 → 128 = the limit
		{"no limit: 1.3×peak", week(400), 0, &Suggestion{Direction: "set", Value: "576m"}}, // 520 → 576
		{"no limit, 3 days: still suggested", week(400)[4:], 0, &Suggestion{Direction: "set", Value: "576m"}},
		{"no data, no limit: nothing", nil, 0, nil},
		{"never below 64m", week(10), 1024, &Suggestion{Direction: "down", Value: "64m"}},
		{"a kill-only day (killed before it was sampled): 1.25×limit", []DailyUsage{{Day: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC), OOMKills: 1}}, 512, &Suggestion{Direction: "up", Value: "640m"}},
		{"a kill older than 7 days does not raise", func() []DailyUsage { d := append([]DailyUsage{day(9, 500)}, week(480)...); d[0].OOMKills = 1; return d }(), 768, nil}, // 500 is neither > 0.9×768 nor < 0.5×768
	}
	for _, c := range cases {
		got := SuggestMemory(c.days, c.limit, now)
		switch {
		case c.want == nil && got != nil:
			t.Errorf("%s: got %+v, want none", c.name, *got)
		case c.want != nil && got == nil:
			t.Errorf("%s: got none, want %s %s", c.name, c.want.Direction, c.want.Value)
		case c.want != nil && (got.Direction != c.want.Direction || got.Value != c.want.Value):
			t.Errorf("%s: got %s %s, want %s %s", c.name, got.Direction, got.Value, c.want.Direction, c.want.Value)
		}
	}
}

func TestSuggestCPU(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	week := func(p95 float64) []DailyUsage {
		out := []DailyUsage{}
		for ago := 7; ago >= 1; ago-- {
			out = append(out, DailyUsage{Day: time.Date(2026, 10, 20-ago, 0, 0, 0, 0, time.UTC), Samples: 1440, CPUP95: p95})
		}
		return out
	}
	throttled := week(50)
	th := 15.0
	throttled[3].ThrottledMax = &th

	cases := []struct {
		name  string
		days  []DailyUsage
		limit float64
		want  *Suggestion
	}{
		{"throttled 15% one day: 1.5×limit", throttled, 1.0, &Suggestion{Direction: "up", Value: "1.5"}},
		{"p95 0.2 cores for a week: 1.5×0.2=0.3 → 0.5", week(20), 1.0, &Suggestion{Direction: "down", Value: "0.5"}},
		{"idle: floors at 0.25 cores", week(0), 1.0, &Suggestion{Direction: "down", Value: "0.25"}},
		{"down that rounds back to the limit: nothing", week(7), 0.25, nil}, // 1.5×0.07=0.105 → 0.25 = the limit
		{"no limit: nothing", week(20), 0, nil},
		{"between 30% and throttling: nothing", week(60), 1.0, nil},
		{"6 days: nothing", week(20)[1:], 1.0, nil},
	}
	for _, c := range cases {
		got := SuggestCPU(c.days, c.limit, now)
		switch {
		case c.want == nil && got != nil:
			t.Errorf("%s: got %+v, want none", c.name, *got)
		case c.want != nil && got == nil:
			t.Errorf("%s: got none, want %s %s", c.name, c.want.Direction, c.want.Value)
		case c.want != nil && (got.Direction != c.want.Direction || got.Value != c.want.Value):
			t.Errorf("%s: got %s %s, want %s %s", c.name, got.Direction, got.Value, c.want.Direction, c.want.Value)
		}
	}
}
