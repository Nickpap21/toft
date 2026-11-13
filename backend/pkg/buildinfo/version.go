package buildinfo

import (
	"strconv"
	"strings"
)

// Parse reads vMAJOR.MINOR.PATCH with an optional -pre-release.
func Parse(v string) (nums [3]int, ok bool) {
	core, _, _ := strings.Cut(strings.TrimPrefix(v, "v"), "-")
	parts := strings.Split(core, ".")
	if !strings.HasPrefix(v, "v") || len(parts) != 3 {
		return nums, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nums, false
		}
		nums[i] = n
	}
	return nums, true
}

// Newer reports whether a is a later release than b; a pre-release comes
// before its release.
func Newer(a, b string) bool {
	na, _ := Parse(a)
	nb, _ := Parse(b)
	for i := range na {
		if na[i] != nb[i] {
			return na[i] > nb[i]
		}
	}
	_, preA, _ := strings.Cut(a, "-")
	_, preB, _ := strings.Cut(b, "-")
	switch {
	case preA == "" || preB == "":
		return preA == "" && preB != ""
	default:
		return preA > preB
	}
}
