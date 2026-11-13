package netcheck

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestIsPublic(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8": true, "2606:4700::1111": true,
		"127.0.0.1": false, "10.1.2.3": false, "172.16.0.9": false, "192.168.1.1": false,
		"169.254.169.254": false, "100.64.0.1": false, "0.0.0.0": false, "::1": false,
		"::ffff:10.0.0.1": false, "64:ff9b::a00:1": false, "224.0.0.1": false,
	} {
		if got := IsPublic(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s: public=%v, want %v", addr, got, want)
		}
	}
}

func TestCheckHost(t *testing.T) {
	if err := CheckHost("registry.example.com"); err != nil {
		t.Errorf("hostname: %v", err)
	}
	if err := CheckHost("1.1.1.1"); err != nil {
		t.Errorf("public address: %v", err)
	}
	if err := CheckHost("10.0.0.5"); !errors.Is(err, ErrPrivateAddress) {
		t.Errorf("private address accepted: %v", err)
	}
}

// The client refuses the connection itself, so a hostname that resolves to a
// private address (here: the loopback a test server listens on) is refused
// after DNS, not only when the URL spells the address out.
func TestPublicOnlyClientRefusesLoopbackAtDial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	_, err := NewPublicOnlyClient(2*time.Second, false).Get(srv.URL)
	if !errors.Is(err, ErrPrivateAddress) {
		t.Fatalf("got %v, want ErrPrivateAddress", err)
	}
}
