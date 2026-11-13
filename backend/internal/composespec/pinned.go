package composespec

import (
	"regexp"
	"sort"

	"github.com/compose-spec/compose-go/v2/types"
)

// digestPinned is an image reference that ends in a digest.
var digestPinned = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)

// NotPinned lists, sorted, the services of p whose image isn't pinned by
// digest (…@sha256:<64 hex>), or that have none or build one: pulling the
// stack again could change what they run. Empty when the pull can't.
func NotPinned(p *types.Project) []string {
	out := make([]string, 0)
	for _, services := range []types.Services{p.Services, p.DisabledServices} {
		for name, s := range services {
			if s.Build != nil || !digestPinned.MatchString(s.Image) {
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
}
