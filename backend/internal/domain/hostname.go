package domain

import (
	"errors"
	"regexp"
	"strings"
)

// ErrInvalidHostname is a hostname the platform can't route: not a DNS name
// with at least two labels, or a wildcard other than one leading "*."
// followed by such a name. An IP address is not one.
var ErrInvalidHostname = errors.New(`the hostname must be a DNS name such as app.example.com, or a wildcard such as *.example.com, not an IP address`)

var label = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidHostname reports whether h is a routable hostname: lowercase labels
// of letters, digits and inner hyphens, at least two of them, at most 253
// characters, the last not all digits (not an IP address), optionally behind
// one leading "*." (a wildcard, which needs a DNS-01 certificate: IsWildcard).
func ValidHostname(h string) bool {
	name := strings.TrimPrefix(h, "*.")
	if len(name) > 253 {
		return false
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if !label.MatchString(l) {
			return false
		}
	}
	// No top-level domain is all digits: that is an IP address, which
	// would out-rank the panel's own routers on a panel without a domain.
	return strings.Trim(labels[len(labels)-1], "0123456789") != ""
}

// IsWildcard reports whether h covers every subdomain of a name ("*.x.y").
func IsWildcard(h string) bool { return strings.HasPrefix(h, "*.") }
