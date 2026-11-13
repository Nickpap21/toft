package domain

import (
	"context"
	"errors"
	"slices"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// certRepo answers AwaitingCertificate with the names not yet set, and
// records SetCertificate as the query does: true the first time only.
type certRepo struct {
	Repository
	pending []Domain
	set     []string
}

func (r *certRepo) AwaitingCertificate(context.Context) ([]Domain, error) {
	var out []Domain
	for _, d := range r.pending {
		if !slices.Contains(r.set, d.ID) {
			out = append(out, d)
		}
	}
	return out, nil
}
func (r *certRepo) SetCertificate(_ context.Context, id string) (bool, error) {
	if slices.Contains(r.set, id) {
		return false, nil
	}
	r.set = append(r.set, id)
	return true, nil
}

// redeploys records the services RedeployForCertificate was asked for.
type redeploys []string

func (r *redeploys) RedeployForCertificate(_ context.Context, serviceID string) {
	*r = append(*r, serviceID)
}

// A name is marked once a valid certificate answers for it, and only then
// (D-150).
func TestCheckCertificates(t *testing.T) {
	repo := &certRepo{pending: []Domain{{ID: "a", Hostname: "web.203-0-113-10.sslip.io"}, {ID: "b", Hostname: "shop.example.com"}}}
	s := &Service{repo: repo, log: zap.NewNop(), certificateErr: func(_ context.Context, h string) error {
		if h == "shop.example.com" {
			return nil
		}
		return errors.New("x509: certificate is valid for TRAEFIK DEFAULT CERT")
	}}
	s.CheckCertificates(context.Background())
	if len(repo.set) != 1 || repo.set[0] != "b" {
		t.Fatalf("marked %v, want [b]", repo.set)
	}
}

// Why a name has no certificate is logged when it changes, not every round,
// and the certificate once it is out.
func TestCheckCertificatesLogsChanges(t *testing.T) {
	repo := &certRepo{pending: []Domain{{ID: "a", Hostname: "web.203-0-113-10.sslip.io"}}}
	core, logs := observer.New(zap.InfoLevel)
	why := errors.New("dial tcp 203.0.113.10:443: connect: connection refused")
	s := &Service{repo: repo, log: zap.New(core), certificateErr: func(context.Context, string) error { return why }}
	s.CheckCertificates(context.Background())
	s.CheckCertificates(context.Background())
	why = errors.New("tls: failed to verify certificate: x509: certificate is valid for TRAEFIK DEFAULT CERT")
	s.CheckCertificates(context.Background())
	s.CheckCertificates(context.Background())
	why = nil
	s.CheckCertificates(context.Background())
	s.CheckCertificates(context.Background())
	var got []string
	for _, e := range logs.All() {
		line := e.Message
		if err, ok := e.ContextMap()["error"]; ok {
			line += ": " + err.(string)
		}
		got = append(got, line)
	}
	want := []string{
		"no valid certificate answers for a domain yet: dial tcp 203.0.113.10:443: connect: connection refused",
		"no valid certificate answers for a domain yet: tls: failed to verify certificate: x509: certificate is valid for TRAEFIK DEFAULT CERT",
		"a domain's certificate is out",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("logged\n%q\nwant\n%q", got, want)
	}
}

// A temporary name's first certificate redeploys its service, once: a
// later poll doesn't, and a person's own domain never does (D-150).
func TestCheckCertificatesRedeploysOnce(t *testing.T) {
	repo := &certRepo{pending: []Domain{
		{ID: "a", ServiceID: "s1", Hostname: "web.203-0-113-10.sslip.io"},
		{ID: "b", ServiceID: "s2", Hostname: "shop.example.com"},
	}}
	var got redeploys
	s := &Service{repo: repo, log: zap.NewNop(), certificateErr: func(context.Context, string) error { return nil }}
	s.WithRedeployer(&got)
	s.CheckCertificates(context.Background())
	s.CheckCertificates(context.Background())
	if !slices.Equal(got, redeploys{"s1"}) {
		t.Fatalf("redeployed %v, want [s1]", got)
	}
}

func TestIsTemporary(t *testing.T) {
	for h, want := range map[string]bool{"web-production-shop.203-0-113-10.sslip.io": true, "shop.example.com": false, "sslip.io.example.com": false} {
		if IsTemporary(h) != want {
			t.Errorf("IsTemporary(%q) = %v", h, !want)
		}
	}
}

// D-150: a move rewrites a temporary name under the old server's address,
// and nothing else.
func TestMoveTemporary(t *testing.T) {
	for _, c := range []struct{ in, from, to, want string }{
		{"web-production-shop.203-0-113-10.sslip.io", "203.0.113.10", "198.51.100.7", "web-production-shop.198-51-100-7.sslip.io"},
		{"mine.192-0-2-1.sslip.io", "203.0.113.10", "198.51.100.7", "mine.192-0-2-1.sslip.io"}, // another address: not the move's
		{"shop.example.com", "203.0.113.10", "198.51.100.7", "shop.example.com"},
		{"web.203-0-113-10.sslip.io", "203.0.113.10", "", "web.203-0-113-10.sslip.io"}, // the new server has no address
	} {
		if got := MoveTemporary(c.in, c.from, c.to); got != c.want {
			t.Errorf("MoveTemporary(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
