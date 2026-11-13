package resources

import (
	"fmt"
	"sort"
	"time"

	"github.com/Nickpap21/toft/backend/internal/composespec"
)

// MemoryCheck is what the memory rule looks at for one deploy or save.
type MemoryCheck struct {
	SingleLimit string // single-container services: the stored memory_limit
	Compose     bool
	// ComposeServices are the stack's services when known (nil otherwise),
	// with the file's limit in MiB (0 = none) and the panel's (0 = none).
	ComposeServices map[string]ComposeLimitSource
	GraceUntil      *time.Time
	Suggested       map[string]SuggestedLimit // "" key for single-container; compose service names otherwise
}

// SuggestedLimit is the limit a refusal offers: from the service's usage,
// or its kind's default without data.
type SuggestedLimit struct {
	Value     string
	FromUsage bool
}

// ComposeLimitSource is where a compose service's limit comes from.
type ComposeLimitSource struct{ FileMB, PanelMB int64 }

// CheckMemory returns the violations (after grace) or warnings (in grace).
// A grace that ends exactly at now has ended: the date is the first day the
// limit is required.
func CheckMemory(c MemoryCheck, now time.Time) ([]composespec.Violation, []string) {
	var missing []string // "" = the service itself
	if c.Compose {
		for name, l := range c.ComposeServices {
			if l.FileMB == 0 && l.PanelMB == 0 {
				missing = append(missing, name)
			}
		}
		sort.Strings(missing)
	} else if _, ok := ParseMB(c.SingleLimit); !ok {
		missing = []string{""}
	}
	var vs []composespec.Violation
	var warnings []string
	for _, name := range missing {
		what, field := "this service has no memory limit", "memory_limit"
		if name != "" {
			what, field = fmt.Sprintf("compose service %s has no memory limit", name), "services."+name+".mem_limit"
		}
		inGrace := c.GraceUntil != nil && now.Before(*c.GraceUntil)
		hint := ""
		// In grace the service is still being measured: only a value from
		// its usage is offered, never the kind's default.
		if s := c.Suggested[name]; s.Value != "" && (s.FromUsage || !inGrace) {
			from := "the default for its kind"
			if s.FromUsage {
				from = "the suggestion from its usage"
			}
			hint = "; set it to " + s.Value + ", " + from
		}
		if inGrace {
			warnings = append(warnings, fmt.Sprintf("%s: a memory limit is required from %s%s", what, c.GraceUntil.UTC().Format("2 Jan 2006"), hint))
			continue
		}
		vs = append(vs, composespec.Violation{Rule: composespec.RuleMemoryLimit, Service: name, Field: field, Message: what + hint, Value: "no limit"})
	}
	return vs, warnings
}
