package preview

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// EnvironmentName is a preview's environment: pr-42, or <repository>-pr-42
// for a pull request of a project's other repositories (pull request
// numbers are per repository).
func EnvironmentName(number int, repository string, first bool) string {
	if first {
		return fmt.Sprintf("pr-%d", number)
	}
	return label(fmt.Sprintf("%s-pr-%d", repository, number))
}

// Hostname is a preview service's address under the project's preview
// domain: pr-42-web.preview.example.com.
func Hostname(domain, service string, number int, repository string, first bool) string {
	l := fmt.Sprintf("pr-%d-%s", number, service)
	if !first {
		l = fmt.Sprintf("%s-pr-%d-%s", repository, number, service)
	}
	return label(l) + "." + domain
}

// SslipHostname is a preview service's address without a preview domain:
// web-pr42-storefront.203-0-113-10.sslip.io, which sslip.io answers with
// the server's address.
func SslipHostname(ip, service, project string, number int, repository string, first bool) string {
	l := fmt.Sprintf("%s-pr%d-%s", service, number, project)
	if !first {
		l = fmt.Sprintf("%s-%s-pr%d-%s", service, repository, number, project)
	}
	return label(l) + "." + strings.ReplaceAll(ip, ".", "-") + ".sslip.io"
}

// label makes s one DNS label: lowercase letters, digits and inner hyphens,
// at most 63 characters; a longer one keeps its start and ends with a hash
// of the whole, so two long names don't collide.
func label(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) <= 63 {
		return out
	}
	sum := sha256.Sum256([]byte(out))
	return strings.TrimRight(out[:54], "-") + "-" + hex.EncodeToString(sum[:])[:8]
}

// RegisteredDomain is the domain Let's Encrypt counts a name's
// certificates under (the Public Suffix List's eTLD+1): example.com for
// pr-42-web.preview.example.com.
func RegisteredDomain(host string) string {
	d, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return host
	}
	return d
}
