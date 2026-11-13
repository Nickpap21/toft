package preview

import (
	"context"
	"errors"
	"net"
	"testing"
)

// privateHosts is Hosts on a server with a private address.
type privateHosts struct{ *fake }

func (privateHosts) Address(context.Context, string) (string, error) { return "10.0.0.5", nil }

// unresolvedHosts: the panel's domain doesn't resolve.
type unresolvedHosts struct{ *fake }

func (unresolvedHosts) Address(context.Context, string) (string, error) {
	return "", &net.DNSError{Err: "i/o timeout", Name: "paas.local", IsTimeout: true}
}

// A new web service's name (D-150): under sslip.io, never the project's
// preview wildcard, -2 when taken, none without a public address.
func TestAddress(t *testing.T) {
	ctx := context.Background()
	none := func(string) (bool, error) { return false, nil }
	f := newFake()
	s := newSvc(f)

	h, err := s.Address(ctx, "storefront", "production", "web", "srv", none)
	if err != nil || h != "web-production-storefront.203-0-113-10.sslip.io" {
		t.Fatalf("sslip.io: %q, %v", h, err)
	}
	h, _ = s.Address(ctx, "uptime-kuma", "production", "uptime-kuma", "srv", none)
	if h != "uptime-kuma-production.203-0-113-10.sslip.io" {
		t.Errorf("the project left out when it repeats the service: %q", h)
	}
	first := "web-production-storefront.203-0-113-10.sslip.io"
	h, _ = s.Address(ctx, "storefront", "production", "web", "srv", func(n string) (bool, error) { return n == first, nil })
	if h != "web-production-storefront-2.203-0-113-10.sslip.io" {
		t.Errorf("taken: %q", h)
	}

	// The project's preview domain, even one that resolves here with
	// certificates to spare, is previews' alone.
	f.set = Settings{Domain: "preview.storefront.example"}
	s.lookup = func(context.Context, string) ([]string, error) { return []string{"203.0.113.10"}, nil }
	h, _ = s.Address(ctx, "storefront", "production", "web", "srv", none)
	if h != "web-production-storefront.203-0-113-10.sslip.io" {
		t.Errorf("under the preview domain: %q", h)
	}

	s.hosts = privateHosts{f}
	if _, err := s.Address(ctx, "storefront", "production", "web", "srv", none); !errors.Is(err, ErrNoPublicAddress) {
		t.Errorf("a private address: %v", err)
	}
	s.hosts = unresolvedHosts{f}
	if _, err := s.Address(ctx, "storefront", "production", "web", "", none); !errors.Is(err, ErrNoPublicAddress) {
		t.Errorf("the panel's domain doesn't resolve: %v", err)
	}
}
