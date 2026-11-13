package netplan

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNames(t *testing.T) {
	assert.Equal(t, "paas-env-e1", EnvNetwork("e1"))
	assert.Equal(t, "paas-proj-p1", ProjectNetwork("p1"))
	assert.Equal(t, "paas-org-o1", OrgNetwork("o1"))
	assert.True(t, IsManagedName("paas-env-0f0e0d0c-0000-0000-0000-000000000001"))
	assert.True(t, IsManagedName(OrgNetwork("0f0e0d0c-0000-0000-0000-00000000000a")))
	assert.False(t, IsManagedName("paas-env-e1"))
	assert.False(t, IsManagedName("paas-env-x-1234abcd_default"), "a stack called env-x")
	assert.False(t, IsManagedName("paas-network"))
}

func TestLabelsOmitEmptyIDs(t *testing.T) {
	assert.Equal(t, map[string]string{
		LabelManaged: "true", LabelKind: "env", LabelEnvironmentID: "e", LabelProjectID: "p",
	}, Env("e", "p", "").Labels())
	assert.Equal(t, map[string]string{LabelManaged: "true", LabelKind: "org", LabelOrgID: "o"}, Org("o").Labels())
}

func TestMatches(t *testing.T) {
	n := Env("e", "p", "o")
	assert.True(t, n.Matches(n.Labels()))

	cases := map[string]map[string]string{
		"not managed":       {LabelKind: "env", LabelEnvironmentID: "e", LabelProjectID: "p", LabelOrgID: "o"},
		"other kind":        {LabelManaged: "true", LabelKind: "project", LabelEnvironmentID: "e", LabelProjectID: "p", LabelOrgID: "o"},
		"other env":         {LabelManaged: "true", LabelKind: "env", LabelEnvironmentID: "x", LabelProjectID: "p", LabelOrgID: "o"},
		"no labels":         nil,
		"managed=false":     {LabelManaged: "false", LabelKind: "env", LabelEnvironmentID: "e", LabelProjectID: "p", LabelOrgID: "o"},
		"other org network": {LabelManaged: "true", LabelKind: "org", LabelOrgID: "x"},
	}
	for name, labels := range cases {
		t.Run(name, func(t *testing.T) {
			target := n
			if name == "other org network" {
				target = Org("o")
			}
			assert.False(t, target.Matches(labels))
		})
	}
}

func TestMatchesIgnoresAProjectMovingOrgs(t *testing.T) {
	n := Env("e", "p", "o")
	moved := Env("e", "p", "").Labels()
	assert.True(t, n.Matches(moved), "the org was cleared or backfilled after the network was created")
	assert.False(t, Env("", "p", "o").Matches(n.Labels()), "no own ID matches nothing")
}

func TestIsReservedName(t *testing.T) {
	for _, n := range []string{"paas-network", "paas-env-x", "PAAS-postgres", "toft-x", "cp-net"} {
		assert.True(t, IsReservedName(n, "cp-net"), n)
	}
	for _, n := range []string{"web", "backend", "my-paas-net"} {
		assert.False(t, IsReservedName(n, "cp-net"), n)
	}
}

func TestParseMigrationMode(t *testing.T) {
	for in, want := range map[string]MigrationMode{
		"": MigrationIsolate, "isolate": MigrationIsolate, " Attach ": MigrationAttach,
		"report": MigrationReport, "off": MigrationOff,
	} {
		got, err := ParseMigrationMode(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, in := range []string{"legacy", "yes", "isolated"} {
		_, err := ParseMigrationMode(in)
		assert.Error(t, err, in)
	}
	assert.True(t, MigrationIsolate.Detaches())
	assert.False(t, MigrationAttach.Detaches())
	assert.True(t, MigrationAttach.Changes())
	assert.False(t, MigrationReport.Changes())
	assert.False(t, MigrationOff.Changes())
}

func TestPlanValid(t *testing.T) {
	assert.Error(t, Plan{}.Valid())
	assert.Error(t, Plan{ServiceID: "s"}.Valid(), "no home")
	assert.NoError(t, LegacyPlan("s", "web", "web", "").Valid())
	assert.Equal(t, LegacyNetwork, LegacyPlan("s", "", "", "").Home.Name)
	assert.NoError(t, Plan{ServiceID: "s", Home: Env("e", "p", "o")}.Valid())
	assert.Error(t, Plan{ServiceID: "s", Home: Network{Name: "paas-network", Kind: KindEnv, EnvironmentID: "e"}}.Valid(),
		"an isolated plan never uses the control plane's network")
	assert.Error(t, Plan{ServiceID: "s", Home: Network{Name: "paas-env-e", Kind: KindEnv}}.Valid(), "no environment id")
	assert.Error(t, Plan{ServiceID: "s", Home: Project("p", "o"), Publish: []Network{{Name: "x"}}}.Valid())
}

func TestParseAddressPool(t *testing.T) {
	got, err := ParseAddressPool("")
	require.NoError(t, err)
	assert.Equal(t, DefaultAddressPool, got)
	got, err = ParseAddressPool("10.203.5.0/16")
	require.NoError(t, err)
	assert.Equal(t, "10.203.0.0/16", got)
	for _, bad := range []string{"10.0.0.0/25", "fd00::/48", "nope", "10.0.0.1"} {
		_, err := ParseAddressPool(bad)
		assert.Error(t, err, bad)
	}
}

func TestFindCrossRefs(t *testing.T) {
	staging := Env("stg", "p1", "o1")
	prod := Env("prd", "p1", "o1")
	other := Env("oth", "p2", "o1")
	plans := []Plan{
		{ServiceID: "api-stg", ServiceName: "api", Alias: "api-staging", Home: staging},
		{ServiceID: "db-stg", ServiceName: "db", Alias: "db", Home: staging},
		{ServiceID: "db-prd", ServiceName: "postgres", Alias: "postgres-production", Home: prod},
		{ServiceID: "web-oth", ServiceName: "web", Alias: "web", Home: other},
		// A project-shared cache that both environments of p1 reach.
		{ServiceID: "cache", ServiceName: "cache", Alias: "redis", Home: Project("p1", "o1"), Publish: []Network{staging, prod}},
		// An org-shared Grafana, attached into staging.
		{ServiceID: "grafana", ServiceName: "grafana", Alias: "grafana", Home: Org("o1")},
	}
	plans[0].Attach = []Endpoint{{ServiceID: "grafana"}}
	env := map[string][]string{
		"api-stg": {
			"DATABASE_URL=postgres://u:p@postgres-production:5432/db", // other environment
			"OWN_DB=postgres://u:p@db:5432/app",                       // same environment
			"CACHE=redis://redis:6379",                                // project shared
			"GRAFANA=http://grafana:3000",                             // attached org service
			"DOMAIN=mydb.example.com",                                 // not a host token for "db"
			"OTHER=http://web/x",                                      // another project
			"NOTHING",
		},
	}
	got := FindCrossRefs(plans, env)
	require.Len(t, got, 2)
	assert.Equal(t, CrossRef{From: "api-stg", FromName: "api", To: "db-prd", ToName: "postgres", Alias: "postgres-production", Variable: "DATABASE_URL"}, got[0])
	assert.Equal(t, "web-oth", got[1].To)
	assert.Equal(t, "OTHER", got[1].Variable)
}

func TestFindCrossRefsSkipsAliasAnsweredLocally(t *testing.T) {
	// Two environments each with their own "postgres": staging's reference
	// resolves to its own, so it is not reported.
	plans := []Plan{
		{ServiceID: "api", Home: Env("a", "p", "o")},
		{ServiceID: "pg-a", Alias: "postgres", Home: Env("a", "p", "o")},
		{ServiceID: "pg-b", Alias: "postgres", Home: Env("b", "p", "o")},
	}
	assert.Empty(t, FindCrossRefs(plans, map[string][]string{"api": {"DB=postgres:5432"}}))
}

func TestHostTokenPattern(t *testing.T) {
	re := hostTokenPattern("db")
	for _, s := range []string{"db", "db:5432", "postgres://u:p@db/x", "a,db,b", "host=db port=1"} {
		assert.True(t, re.MatchString(s), s)
	}
	for _, s := range []string{"mydb", "db.example.com", "dbx:1", "x-db"} {
		assert.False(t, re.MatchString(s), s)
	}
}
