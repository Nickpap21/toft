package service

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The New service form names the alias a service will get before it exists
// (frontend/src/lib/network-alias.ts); both read these vectors, so the two
// rules fail together if they drift.
func TestDefaultNetworkAliasVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/network_alias.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct{ Name, Environment, Alias string }
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		if got := defaultNetworkAlias(v.Name, v.Environment); got != v.Alias {
			t.Errorf("defaultNetworkAlias(%q, %q) = %q, want %q", v.Name, v.Environment, got, v.Alias)
		}
	}
}

func TestConnectTo(t *testing.T) {
	cases := []struct {
		name string
		svc  Service
		want *ConnectAddress
	}{
		{"an image answers to its alias, on its port",
			Service{SourceType: SourceTypeDockerImage, NetworkAlias: "whoami-qa", Port: 80},
			&ConnectAddress{Host: "whoami-qa", Port: 80}},
		{"a worker answers to its alias, on no port",
			Service{SourceType: SourceTypeGit, NetworkAlias: "jobs-production"},
			&ConnectAddress{Host: "jobs-production"}},
		{"a catalog image listens on its image's port",
			Service{SourceType: SourceTypeDockerImage, AddonSlug: "postgres", NetworkAlias: "postgres", Port: 5433},
			&ConnectAddress{Host: "postgres", Port: 5432}},
		{"a Mailpit listens on its web port",
			Service{SourceType: SourceTypeDockerImage, AddonSlug: "mailpit", NetworkAlias: "mail-qa", Port: 8026},
			&ConnectAddress{Host: "mail-qa", Port: 8025}},
		// MailHog left the catalog for Mailpit; services made from it still run.
		{"a MailHog made before Mailpit still listens on its web port",
			Service{SourceType: SourceTypeDockerImage, AddonSlug: "mailhog", NetworkAlias: "mail-qa", Port: 8026},
			&ConnectAddress{Host: "mail-qa", Port: 8025}},
		// QA #9: from web, umami-production:3000 is a bad address, umami:3000 answers.
		{"a compose stack answers to its routed compose service, not its alias",
			Service{SourceType: SourceTypeDockerCompose, AddonSlug: "umami", ComposeRouteService: "umami", ComposeNetworkService: "umami", NetworkAlias: "umami-production", Port: 3000},
			&ConnectAddress{Host: "umami", Port: 3000}},
		{"a compose stack routed automatically answers to the service its deploy routed",
			Service{SourceType: SourceTypeDockerCompose, ComposeNetworkService: "app", NetworkAlias: "stack-production", Port: 8080},
			&ConnectAddress{Host: "app", Port: 8080}},
		{"a compose stack its deploy routed none (no domain) is on no network",
			Service{SourceType: SourceTypeDockerCompose, ComposeRouteService: "umami", NetworkAlias: "umami-production", Port: 3000},
			nil},
		{"no alias, no address",
			Service{SourceType: SourceTypeDockerImage, Port: 80},
			nil},
	}
	for _, c := range cases {
		got := connectTo(c.svc)
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("%s: connectTo = %+v, want %+v", c.name, got, c.want)
		}
	}

	raw, err := json.Marshal(Service{SourceType: SourceTypeDockerCompose, ComposeRouteService: "umami", ComposeNetworkService: "umami", NetworkAlias: "umami-production", Port: 3000})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"connect_to":{"host":"umami","port":3000}`) {
		t.Errorf("the API does not show connect_to: %s", raw)
	}
}
