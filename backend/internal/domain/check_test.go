package domain

import (
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// memRepo holds one service's domains.
type memRepo struct {
	Repository
	domains []Domain
	created []string // createdBy of each Create
	mu      sync.Mutex
	certs   []string // IDs SetCertificate was given; the checker writes concurrently
}

func (r *memRepo) ListByService(context.Context, string) ([]Domain, error) { return r.domains, nil }
func (r *memRepo) SetCertificate(_ context.Context, id string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.certs = append(r.certs, id)
	return true, nil
}
func (r *memRepo) ListByOrg(context.Context, string) ([]WorkspaceDomain, error) {
	return []WorkspaceDomain{}, nil
}
func (r *memRepo) Create(_ context.Context, serviceID, createdBy string, in CreateInput) (*Domain, error) {
	r.created = append(r.created, createdBy)
	d := Domain{ID: "new", ServiceID: serviceID, Hostname: in.Hostname}
	return &d, nil
}
func (r *memRepo) Delete(_ context.Context, serviceID, id string) (string, error) {
	for _, d := range r.domains {
		if d.ID == id && d.ServiceID == serviceID {
			return d.Hostname, nil
		}
	}
	return "", ErrNotFound
}

// routing is the service: its port, server and what its container routes.
type routing struct {
	port    int
	routed  []string
	running bool
}

func (r routing) CheckOwnership(_ context.Context, id, orgID string) (bool, error) {
	return id == "s1" && orgID == "org-a", nil
}
func (r routing) DomainTarget(context.Context, string) (int, string, error) {
	return r.port, "srv", nil
}
func (r routing) RoutedHosts(context.Context, string) ([]string, bool, error) {
	return r.routed, r.running, nil
}

type servers struct{ facts ServerFacts }

func (s servers) DomainServer(context.Context, string, string) (ServerFacts, error) {
	return s.facts, nil
}

// fakeProbe answers from maps; a name missing from dns is NXDOMAIN. The
// checker probes domains concurrently, so dials is guarded.
type fakeProbe struct {
	dns   map[string][]string
	certs map[string]*x509.Certificate
	mu    sync.Mutex
	dials []string
}

func (p *fakeProbe) LookupHost(_ context.Context, host string) ([]string, error) {
	for k, v := range p.dns {
		if k == host || (strings.HasPrefix(k, "*.") && strings.HasSuffix(host, k[1:])) {
			return v, nil
		}
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func (p *fakeProbe) Certificate(_ context.Context, address, name string) (*x509.Certificate, bool, error) {
	p.mu.Lock()
	p.dials = append(p.dials, address)
	p.mu.Unlock()
	for k, c := range p.certs {
		if k == name || (strings.HasPrefix(k, "*.") && strings.HasSuffix(name, k[1:])) {
			return c, !strings.Contains(c.Subject.CommonName, "TRAEFIK"), nil
		}
	}
	return nil, false, nil
}

func cert(cn, org string, notAfter time.Time) *x509.Certificate {
	return &x509.Certificate{Subject: pkix.Name{CommonName: cn}, Issuer: pkix.Name{Organization: []string{org}}, NotAfter: notAfter}
}

func TestChecker(t *testing.T) {
	until := time.Date(2026, 12, 22, 0, 0, 0, 0, time.UTC)
	repo := &memRepo{domains: []Domain{
		{ID: "d1", ServiceID: "s1", Hostname: "api.example.com"},
		{ID: "d2", ServiceID: "s1", Hostname: "*.api.example.com"},
		{ID: "d3", ServiceID: "s1", Hostname: "api.example.dev"},
		{ID: "d4", ServiceID: "s1", Hostname: "moved.example.com"},
		{ID: "d5", ServiceID: "s1", Hostname: "shop.example.com", CertificateAt: &until},
	}}
	probe := &fakeProbe{
		dns: map[string][]string{
			"api.example.com":   {"203.0.113.10"},
			"*.api.example.com": {"203.0.113.10", "2001:db8::1"},
			"moved.example.com": {"198.51.100.7"},
			"shop.example.com":  {"203.0.113.10"},
		},
		certs: map[string]*x509.Certificate{
			"api.example.com":   cert("api.example.com", "Let's Encrypt", until),
			"*.api.example.com": cert("TRAEFIK DEFAULT CERT", "", until),
			"shop.example.com":  cert("shop.example.com", "Let's Encrypt", until),
		},
	}
	svc := routing{port: 8080, running: true, routed: []string{"api.example.com", "*.api.example.com", "old.example.com", "shop.example.com"}}
	facts := ServerFacts{Name: "fsn1", Address: "203.0.113.10", Owner: false, DNSProvider: "cloudflare"}
	c := NewChecker(repo, svc, servers{facts}, probe, zap.NewNop())

	got, err := c.Check(context.Background(), "s1", "org-a")
	require.NoError(t, err)
	byHost := map[string]DomainCheck{}
	for _, d := range got.Domains {
		byHost[d.Hostname] = d
	}

	assert.Equal(t, DNSHere, byHost["api.example.com"].DNS)
	assert.Equal(t, CertValid, byHost["api.example.com"].Certificate)
	assert.Equal(t, until, *byHost["api.example.com"].NotAfter)
	assert.Equal(t, "Let's Encrypt", byHost["api.example.com"].Issuer)
	assert.True(t, byHost["api.example.com"].Routed)
	assert.Equal(t, []string{"d1"}, repo.certs,
		"a valid certificate is recorded, once: the header and the next deploy read what the check saw (D-150)")

	assert.Equal(t, DNSHere, byHost["*.api.example.com"].DNS, "an IPv6 record is shown, not compared")
	assert.Equal(t, CertNone, byHost["*.api.example.com"].Certificate, "Traefik's default certificate is no certificate")

	assert.Equal(t, DNSNone, byHost["api.example.dev"].DNS)
	assert.False(t, byHost["api.example.dev"].Routed, "added after the live deploy")
	assert.Equal(t, CertNone, byHost["api.example.dev"].Certificate, "nothing to read before a deploy routes it")

	assert.Equal(t, DNSElsewhere, byHost["moved.example.com"].DNS)
	assert.Equal(t, []string{"198.51.100.7"}, byHost["moved.example.com"].Addresses)

	assert.Equal(t, []string{"old.example.com"}, got.RoutedRemoved, "removed, routed until the next deploy")
	assert.True(t, got.RoutingKnown)
	assert.Equal(t, "203.0.113.10", got.Server.Address, "a guest is told the DNS target")
	assert.Empty(t, got.Server.DNSProvider, "a guest isn't told the server's DNS provider")
	for _, a := range probe.dials {
		assert.Equal(t, "203.0.113.10", a, "certificates are read from the service's own server only")
	}
}

func TestCheckerStoppedService(t *testing.T) {
	repo := &memRepo{domains: []Domain{{ID: "d1", ServiceID: "s1", Hostname: "api.example.com"}}}
	probe := &fakeProbe{dns: map[string][]string{"api.example.com": {"203.0.113.10"}}}
	c := NewChecker(repo, routing{port: 80, running: false}, servers{ServerFacts{Address: "203.0.113.10", Owner: true, DNSProvider: "cloudflare"}}, probe, zap.NewNop())
	got, err := c.Check(context.Background(), "s1", "org-a")
	require.NoError(t, err)
	assert.False(t, got.RoutingKnown)
	assert.False(t, got.Domains[0].Routed)
	assert.Empty(t, probe.dials, "nothing routed: no certificate to read")
	assert.Equal(t, "cloudflare", got.Server.DNSProvider, "the owner sees the provider")
	assert.Empty(t, got.RoutedRemoved)
}

func TestCompareA(t *testing.T) {
	assert.Equal(t, DNSHere, compareA([]string{"1.2.3.4"}, "1.2.3.4"))
	assert.Equal(t, DNSElsewhere, compareA([]string{"1.2.3.4", "5.6.7.8"}, "1.2.3.4"), "round robin with another host")
	assert.Equal(t, DNSUnknown, compareA([]string{"1.2.3.4"}, ""), "the server's address is unknown")
	assert.Equal(t, DNSUnknown, compareA([]string{"2001:db8::1"}, "1.2.3.4"), "IPv6 only")
}

func TestCreateAndDeleteRules(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &memRepo{domains: []Domain{{ID: "d1", ServiceID: "s1", Hostname: "a.example.com"}, {ID: "d9", ServiceID: "s9", Hostname: "b.example.com"}}}
	router := func(port int) *gin.Engine {
		svc := routing{port: port}
		r := gin.New()
		g := r.Group("/api/v1")
		g.Use(func(c *gin.Context) {
			c.Set("orgID", "org-a")
			c.Set("userID", "u1")
			c.Set("memberRole", "developer")
			c.Next()
		})
		NewHandler(NewService(repo, svc, zap.NewNop()), nil, svc, zap.NewNop()).RegisterRoutes(g)
		return r
	}
	do := func(r http.Handler, method, path, body string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}

	assert.Equal(t, http.StatusConflict, do(router(0), http.MethodPost, "/api/v1/services/s1/domains", `{"hostname":"x.example.com"}`), "no port")
	assert.Empty(t, repo.created)
	assert.Equal(t, http.StatusCreated, do(router(8080), http.MethodPost, "/api/v1/services/s1/domains", `{"hostname":"X.example.com"}`))
	assert.Equal(t, []string{"u1"}, repo.created, "who added it is recorded")

	assert.Equal(t, http.StatusNotFound, do(router(80), http.MethodDelete, "/api/v1/services/s1/domains/d9", ""),
		"another service's domain is not deleted through this one")
	assert.Equal(t, http.StatusNoContent, do(router(80), http.MethodDelete, "/api/v1/services/s1/domains/d1", ""))
}
