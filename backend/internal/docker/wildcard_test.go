package docker

import (
	"regexp"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWildcardDomainLabels(t *testing.T) {
	const id = "0a1b2c3d-1111-2222-3333-444455556666"
	l := buildTraefikLabels(id, []string{"app.example.org", "*.studio.example.org"}, 8080, service.AccessControl{})

	// An exact host: as before.
	assert.Equal(t, "Host(`app.example.org`)", l["traefik.http.routers.paas-0a1b2c3d-0.rule"])
	assert.Equal(t, "letsencrypt", l["traefik.http.routers.paas-0a1b2c3d-0.tls.certresolver"])
	assert.NotContains(t, l, "traefik.http.routers.paas-0a1b2c3d-0.priority")

	// The wildcard: a regexp at the lowest priority, the DNS-01 resolver and
	// the certificate name given.
	rule := l["traefik.http.routers.paas-0a1b2c3d-1.rule"]
	assert.Equal(t, "HostRegexp(`^[^.]+\\.studio\\.example\\.org$`)", rule)
	assert.Equal(t, "1", l["traefik.http.routers.paas-0a1b2c3d-1.priority"])
	assert.Equal(t, "1", l["traefik.http.routers.paas-0a1b2c3d-1-http.priority"])
	assert.Equal(t, rule, l["traefik.http.routers.paas-0a1b2c3d-1-http.rule"])
	assert.Equal(t, dnsResolver, l["traefik.http.routers.paas-0a1b2c3d-1.tls.certresolver"])
	assert.Equal(t, "*.studio.example.org", l["traefik.http.routers.paas-0a1b2c3d-1.tls.domains[0].main"])

	// The regexp matches one label in front, nothing else.
	re := regexp.MustCompile(strings.TrimSuffix(strings.TrimPrefix(rule, "HostRegexp(`"), "`)"))
	for host, want := range map[string]bool{
		"acme.studio.example.org": true, "studio.example.org": false, "a.b.studio.example.org": false,
		"acme.studioxexample.org": false, "acme.studio.example.org.evil.com": false,
	} {
		assert.Equal(t, want, re.MatchString(host), host)
	}
}

// Compose interpolates $, so the override doubles it; the stack's container
// gets the rule with a single $.
func TestWildcardRuleSurvivesComposeEscaping(t *testing.T) {
	out, err := composeRouteOverride("0a1b2c3d-1111-2222-3333-444455556666", "", "paas-network", &deploymentRouteForTest)
	require.NoError(t, err)
	assert.Contains(t, string(out), "example\\.org$$`)")
}

var deploymentRouteForTest = deployment.ComposeRoute{Service: "web", Port: 8080, Domains: []string{"*.example.org"}}

// A preview's routers share one certificate for all its addresses, or its
// wildcard from the DNS-01 resolver (D-97).
func TestPreviewCertificateLabels(t *testing.T) {
	id := "11111111-2222-3333-4444-555555555555"
	l := buildTraefikLabels(id, []string{"pr-42-web.preview.example.com"}, 3000, service.AccessControl{
		TLSDomains: []string{"pr-42-web.preview.example.com", "pr-42-api.preview.example.com"}})
	if l["traefik.http.routers.paas-11111111-0.tls.domains[0].main"] != "pr-42-web.preview.example.com" ||
		l["traefik.http.routers.paas-11111111-0.tls.domains[0].sans"] != "pr-42-api.preview.example.com" ||
		l["traefik.http.routers.paas-11111111-0.tls.certresolver"] != "letsencrypt" {
		t.Fatalf("labels = %v", l)
	}
	l = buildTraefikLabels(id, []string{"pr-42-web.preview.example.com"}, 3000, service.AccessControl{
		TLSDomains: []string{"*.preview.example.com"}, TLSResolver: "letsencrypt-dns"})
	if l["traefik.http.routers.paas-11111111-0.tls.domains[0].main"] != "*.preview.example.com" ||
		l["traefik.http.routers.paas-11111111-0.tls.certresolver"] != "letsencrypt-dns" ||
		l["traefik.http.routers.paas-11111111-0.rule"] != "Host(`pr-42-web.preview.example.com`)" {
		t.Fatalf("wildcard labels = %v", l)
	}
}
