// Package netcheck keeps the panel's outgoing HTTP off private networks. A
// member can name a host in a webhook URL, a registry (the first component
// of an image name) or a repository URL; the panel must not become their
// way onto its own host, the control-plane network or cloud metadata.
package netcheck

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// ErrPrivateAddress: the destination is loopback, private, link-local,
// multicast, unspecified or in another range that is not public.
var ErrPrivateAddress = errors.New("the address is not public")

// nonPublic are address ranges netip's predicates don't cover that must not
// be reached either.
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT, Tailscale
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), // NAT64: could map to a private IPv4
	netip.MustParsePrefix("64:ff9b:1::/48"),
}

// IsPublic reports whether ip is a public unicast address.
func IsPublic(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, p := range nonPublic {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// CheckHost refuses host when it is a literal address that is not public.
// A hostname passes here and is checked when the client connects.
func CheckHost(host string) error {
	if ip, err := netip.ParseAddr(host); err == nil && !IsPublic(ip) {
		return fmt.Errorf("%w (%s)", ErrPrivateAddress, host)
	}
	return nil
}

// PublicOnly is a net.Dialer Control that refuses any address that is not
// public, checked on the address actually dialled (after DNS).
func PublicOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !IsPublic(ip) {
		return fmt.Errorf("%w (%s)", ErrPrivateAddress, host)
	}
	return nil
}

// NewPublicOnlyClient returns an HTTP client that refuses, at connect time,
// any address that is not public: checking the address actually dialled
// (after DNS) means a hostname that resolves, or is later re-pointed, to a
// private address is refused too. No proxy (it would hide the destination).
// With followRedirects false every redirect is returned to the caller
// instead; with it true, each redirect's connection is checked like the
// first.
func NewPublicOnlyClient(timeout time.Duration, followRedirects bool) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, Control: PublicOnly}
	c := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: 5 * time.Second,
			MaxIdleConns:        10,
			IdleConnTimeout:     90 * time.Second,
		},
	}
	if !followRedirects {
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return c
}
