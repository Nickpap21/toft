package catalog_test

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/catalog"
	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/deploypolicy"
)

var composeVar = regexp.MustCompile(`\$\{([A-Z0-9_]+)`)

// Every compose template passes the deploy policy as it is shipped, under
// isolated networking: no refusal, a memory limit on every service, the
// routed service in the file, and a spec for every variable it reads (an
// upstream bump that breaks one fails here, not on a user's server).
func TestComposeTemplatesPassThePolicy(t *testing.T) {
	ctx := context.Background()
	n := 0
	for _, tmpl := range catalog.All() {
		if tmpl.Kind != catalog.KindCompose {
			continue
		}
		n++
		t.Run(tmpl.Slug, func(t *testing.T) {
			if tmpl.DockerImage != "" || tmpl.NetworkAlias != "" || tmpl.RouteService == "" || tmpl.DefaultPort == 0 {
				t.Fatalf("a compose template names no image or alias, and routes a service's port: %+v", tmpl)
			}
			env := map[string]string{"TOFT_URL": "https://app.example.com"}
			for _, s := range tmpl.EnvVarSpecs {
				env[s.Key] = "Secret23abc"
			}
			src := deploypolicy.InlineComposeSource("0b1e7c3a-5a4e-4c38-9d43-5f6f2c1d9a10", tmpl.Slug, tmpl.ComposeContent, env)
			src.AppNetworks = composespec.AppNetworkPolicy{Isolated: true, Legacy: []string{"paas-network"}}
			vs, services, fileMB, _ := composespec.InspectLimits(ctx, src)
			if len(vs) > 0 {
				t.Fatalf("refused: %+v", vs)
			}
			if len(services) != tmpl.ComposeServices || !slices.Contains(services, tmpl.RouteService) {
				t.Fatalf("services %v: want %d with %s", services, tmpl.ComposeServices, tmpl.RouteService)
			}
			for _, s := range services {
				if fileMB[s] <= 0 {
					t.Errorf("%s has no memory limit", s)
				}
			}
			// Pinned by digest, so a temporary address's certificate can
			// redeploy the stack without its up pulling anything new (D-150).
			p, _, err := composespec.Load(ctx, src)
			if err != nil || p == nil {
				t.Fatalf("load: %v", err)
			}
			if unpinned := composespec.NotPinned(p); len(unpinned) > 0 {
				t.Errorf("images not pinned by digest: %v", unpinned)
			}
			read := map[string]bool{}
			for _, m := range composeVar.FindAllStringSubmatch(tmpl.ComposeContent, -1) {
				read[m[1]] = true
			}
			for _, s := range tmpl.EnvVarSpecs {
				if !read[s.Key] {
					t.Errorf("%s has a spec the file never reads", s.Key)
				}
				delete(read, s.Key)
			}
			delete(read, "TOFT_URL")
			if len(read) > 0 {
				t.Errorf("the file reads variables with no spec: %v", read)
			}
			// With no domain, a template that needs its address says so.
			if strings.Contains(tmpl.ComposeContent, "TOFT_URL:?") {
				delete(env, "TOFT_URL")
				vs, _ := composespec.Inspect(ctx, deploypolicy.InlineComposeSource("0b1e7c3a-5a4e-4c38-9d43-5f6f2c1d9a10", tmpl.Slug, tmpl.ComposeContent, env))
				if len(vs) != 1 || vs[0].Rule != composespec.RuleComposeInvalid || !strings.Contains(vs[0].Message, "add a domain") {
					t.Errorf("with no domain: %+v", vs)
				}
			}
		})
	}
	if n != 5 {
		t.Fatalf("%d compose templates, want 5", n)
	}
}

// A template that tells its image which port to listen on says the
// service's port (pgAdmin's 80 needs a capability no-new-privileges strips,
// D-93; Grafana's own default is 3000).
func TestListenPortVariablesMatchThePort(t *testing.T) {
	for slug, key := range map[string]string{"pgadmin": "PGADMIN_LISTEN_PORT", "grafana": "GF_SERVER_HTTP_PORT"} {
		tmpl, ok := catalog.Get(slug)
		if !ok {
			t.Fatalf("no %s template", slug)
		}
		i := slices.IndexFunc(tmpl.EnvVarSpecs, func(s catalog.EnvVarSpec) bool { return s.Key == key })
		if i < 0 || tmpl.EnvVarSpecs[i].Default != strconv.Itoa(tmpl.DefaultPort) {
			t.Errorf("%s: %s should default to its port %d", slug, key, tmpl.DefaultPort)
		}
	}
}
