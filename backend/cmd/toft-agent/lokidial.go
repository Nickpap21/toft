package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"time"
)

// lokiClient is the shipper's HTTP client. A Loki address that names a
// host resolving to this machine (a loopback or unspecified address) is
// refused: 127.0.0.1 is Toft's to give, where the catalog Loki of the
// service's own workspace listens (observability.LokiTarget.ShipTo), and
// on a shared server another workspace's (D-148). The panel refuses such
// an address only as written, so a name that resolves to 127.0.0.1 is
// refused here, where it is dialled. An address written as an IP is
// dialled as it is: the panel refuses a loopback one that a person sets.
// Redirects aren't followed, or a Loki elsewhere could send the push on to
// 127.0.0.1 written as an IP.
func lokiClient(lookup func(ctx context.Context, network, host string) ([]netip.Addr, error)) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if _, err := netip.ParseAddr(host); err == nil {
			return d.DialContext(ctx, network, addr)
		}
		ips, err := lookup(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("%s has no address", host)
		}
		for _, ip := range ips {
			if ip = ip.Unmap(); ip.IsLoopback() || ip.IsUnspecified() {
				return nil, fmt.Errorf("Loki's address %s is this server (%s): only the Loki Toft wires itself is reached on 127.0.0.1", host, ip)
			}
		}
		// The addresses checked are the ones dialled: no second lookup.
		var last error
		for _, ip := range ips {
			c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return c, nil
			}
			last = err
		}
		return nil, last
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: t,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
