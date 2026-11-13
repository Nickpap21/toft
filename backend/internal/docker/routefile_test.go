package docker

import (
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/service"
)

// Every route the panel labels a container with turns into the same routes
// in the agent's file (D-113): no label is one the file can't say.
func TestEveryLabelledRouteHasAFile(t *testing.T) {
	const id = "1234abcd-0000-0000-0000-000000000001"
	cases := map[string]map[string]string{
		"plain":    buildTraefikLabels(id, []string{"app.example.com", "www.example.com"}, 8080, service.AccessControl{}),
		"access":   buildTraefikLabels(id, []string{"app.example.com"}, 80, service.AccessControl{BasicAuthUser: "u", BasicAuthPassword: "p", IPAllowlist: []string{"10.0.0.0/8", "1.2.3.4"}}),
		"wildcard": buildTraefikLabels(id, []string{"*.apps.example.com"}, 80, service.AccessControl{}),
		"preview":  buildTraefikLabels(id, []string{"a.pr-1.example.com", "b.pr-1.example.com"}, 80, service.AccessControl{TLSDomains: []string{"*.pr-1.example.com", "pr-1.example.com"}, TLSResolver: "letsencrypt-dns"}),
		"compose":  composeRouteLabels(id, "paas-env-net", &deployment.ComposeRoute{Service: "web", Domains: []string{"app.example.com"}, Port: 3000}),
	}
	for name, labels := range cases {
		f, err := rollout.RoutesFor(id[:8], labels, []string{"paas-web-blue"})
		if err != nil || f == nil {
			t.Fatalf("%s: %v (%v)", name, err, f)
		}
		raw, err := f.Marshal()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(string(raw), "url: http://paas-web-blue:") {
			t.Fatalf("%s: no server in\n%s", name, raw)
		}
	}
	f, _ := rollout.RoutesFor(id[:8], cases["access"], []string{"c"})
	r := f.HTTP.Routers["paas-1234abcd-0"]
	if r.Service != "paas-svc-1234abcd" || r.TLS.CertResolver != "letsencrypt" || len(r.Middlewares) != 3 || r.EntryPoints[0] != "websecure" {
		t.Fatalf("router %+v", r)
	}
	if m := f.HTTP.Middlewares["paas-1234abcd-ipallow"]; m == nil || len(m.IPAllowList.SourceRange) != 2 {
		t.Fatalf("ipallow %+v", m)
	}
	w, _ := rollout.RoutesFor(id[:8], cases["wildcard"], []string{"c"})
	if r := w.HTTP.Routers["paas-1234abcd-0"]; r.Priority != 1 || r.TLS.Domains[0].Main != "*.apps.example.com" || r.TLS.CertResolver != dnsResolver {
		t.Fatalf("wildcard %+v", r.TLS)
	}
	if none, err := rollout.RoutesFor(id[:8], buildTraefikLabels(id, nil, 0, service.AccessControl{}), []string{"c"}); none != nil || err != nil {
		t.Fatalf("an unrouted service: %v %v", none, err)
	}
}
