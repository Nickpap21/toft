package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/netplan"
	serverPkg "github.com/Nickpap21/toft/backend/internal/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

func TestBuildNetworkPlanEnvironment(t *testing.T) {
	sc := NetworkScope{Scope: ScopeEnvironment, ServiceID: "s1", Name: "api", Alias: "api", EnvironmentID: "e1", ProjectID: "p1", OrgID: "o1"}
	peers := []NetworkPeer{
		{ID: "s3", Name: "grafana", Alias: "grafana", Scope: ScopeOrg, SourceType: SourceTypeDockerImage},
		{ID: "s2", Name: "db", Alias: "db", Scope: ScopeEnvironment},
		{ID: "s4", Name: "stack", Scope: ScopeProject, SourceType: SourceTypeDockerCompose},
		{ID: "s2", Name: "db", Alias: "db"}, // duplicate
		{ID: "s1", Name: "api"},             // self
	}
	p, err := BuildNetworkPlan(sc, peers, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, netplan.Env("e1", "p1", "o1"), p.Home)
	assert.Empty(t, p.Publish)
	assert.Equal(t, []netplan.Endpoint{
		{ServiceID: "s2", ServiceName: "db", Alias: "db"},
		{ServiceID: "s3", ServiceName: "grafana", Alias: "grafana"},
		{ServiceID: "s4", ServiceName: "stack", Compose: true},
	}, p.Attach)
	assert.False(t, p.Legacy)
}

func TestBuildNetworkPlanProject(t *testing.T) {
	sc := NetworkScope{Scope: ScopeProject, ServiceID: "pg", Name: "postgres", Alias: "postgres", ProjectID: "p1", OrgID: "o1"}
	targets := []NetworkTarget{{EnvironmentID: "e2", ProjectID: "p1"}, {EnvironmentID: "e1", ProjectID: "p1"}, {EnvironmentID: "x", ProjectID: "other"}}
	p, err := BuildNetworkPlan(sc, []NetworkPeer{{ID: "graf", Scope: ScopeOrg}}, targets, nil)
	require.NoError(t, err)
	assert.Equal(t, netplan.Project("p1", "o1"), p.Home)
	assert.Equal(t, []netplan.Network{netplan.Env("e1", "p1", "o1"), netplan.Env("e2", "p1", "o1")}, p.Publish)
	assert.Equal(t, "graf", p.Attach[0].ServiceID)
}

func TestBuildNetworkPlanOrg(t *testing.T) {
	sc := NetworkScope{Scope: ScopeOrg, ServiceID: "graf", OrgID: "o1"}
	targets := []NetworkTarget{{EnvironmentID: "e1", ProjectID: "p1"}, {EnvironmentID: "e3", ProjectID: "p2"}}
	p, err := BuildNetworkPlan(sc, nil, targets, []string{"p2", "p1"})
	require.NoError(t, err)
	assert.Equal(t, netplan.Org("o1"), p.Home)
	var names []string
	for _, n := range p.Publish {
		names = append(names, n.Name)
	}
	assert.Equal(t, []string{"paas-env-e1", "paas-env-e3", "paas-proj-p1", "paas-proj-p2"}, names)
	for _, n := range p.Publish {
		assert.Equal(t, "o1", n.OrgID, "consumer networks carry the org's labels")
	}
}

func TestBuildNetworkPlanWithoutScopeFails(t *testing.T) {
	for name, sc := range map[string]NetworkScope{
		"env without environment": {Scope: ScopeEnvironment, ServiceID: "s", ProjectID: "p"},
		"project without project": {Scope: ScopeProject, ServiceID: "s"},
		"org without org":         {Scope: ScopeOrg, ServiceID: "s"},
		"unknown scope":           {Scope: "galaxy", ServiceID: "s"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := BuildNetworkPlan(sc, nil, nil, nil)
			assert.ErrorIs(t, err, ErrNoNetworkScope)
		})
	}
	// A project without an org: an environment network without org labels,
	// and no org services.
	p, err := BuildNetworkPlan(NetworkScope{Scope: ScopeEnvironment, ServiceID: "s", EnvironmentID: "e", ProjectID: "p"}, nil, nil, nil)
	require.NoError(t, err)
	assert.NotContains(t, p.Home.Labels(), netplan.LabelOrgID)
}

// netRepo is a Repository fake for the network methods.
type netRepo struct {
	Repository
	scopes   map[string]*NetworkScope
	peers    map[string][]NetworkPeer
	targets  []NetworkTarget
	projects []string
	ids      []string
	err      error
	gotTgt   [][2]string

	previewOf      map[string]string
	untrusted      map[string]bool
	sourcePeers    map[string][]NetworkPeer
	previewTargets map[string][]string
}

func (r *netRepo) NetworkScope(_ context.Context, id string) (*NetworkScope, error) {
	if r.err != nil {
		return nil, r.err
	}
	sc, ok := r.scopes[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return sc, nil
}

func (r *netRepo) ListNetworkPeers(_ context.Context, sc NetworkScope) ([]NetworkPeer, error) {
	return r.peers[sc.ServiceID], nil
}

func (r *netRepo) ListNetworkTargets(_ context.Context, projectID, orgID string) ([]NetworkTarget, []string, error) {
	r.gotTgt = append(r.gotTgt, [2]string{projectID, orgID})
	return r.targets, r.projects, nil
}

// Previews (D-97): previewOf maps a preview's environment to the one it
// copies, untrusted marks it untrusted.
func (r *netRepo) PreviewOfEnvironment(_ context.Context, env string) (string, bool, bool, error) {
	src, ok := r.previewOf[env]
	return src, r.untrusted[env], ok, nil
}
func (r *netRepo) ListPreviewPeers(_ context.Context, source, _, _ string) ([]NetworkPeer, error) {
	return r.sourcePeers[source], nil
}
func (r *netRepo) ListPreviewTargets(_ context.Context, env, _ string) ([]string, error) {
	return r.previewTargets[env], nil
}
func (r *netRepo) ListUntrustedPreviewEnvironments(context.Context, string, string) ([]string, error) {
	var out []string
	for env, u := range r.untrusted {
		if u {
			out = append(out, env)
		}
	}
	return out, nil
}

func (r *netRepo) ListIDsOnServer(context.Context, string) ([]string, error) { return r.ids, nil }
func (r *netRepo) ListAllIDs(context.Context) ([]string, error)              { return r.ids, nil }

func TestNetworkPlanReadsTheRepository(t *testing.T) {
	repo := &netRepo{
		scopes: map[string]*NetworkScope{
			"pg": {Scope: ScopeProject, ServiceID: "pg", ProjectID: "p1", OrgID: "o1"},
		},
		targets: []NetworkTarget{{EnvironmentID: "e1", ProjectID: "p1"}},
	}
	m := NewServiceManager(repo, zap.NewNop())
	p, err := m.NetworkPlan(context.Background(), "pg")
	require.NoError(t, err)
	assert.Equal(t, "paas-proj-p1", p.Home.Name)
	assert.Equal(t, [][2]string{{"p1", ""}}, repo.gotTgt, "a project service asks for its project's environments only")

	repo.err = errors.New("db down")
	_, err = m.NetworkPlan(context.Background(), "pg")
	assert.ErrorContains(t, err, "db down")
}

// fakeReconciler is an orchestrator that records the plans it is given.
type fakeReconciler struct {
	ContainerOrchestrator
	plans []netplan.Plan
	ids   []string
	mode  netplan.MigrationMode
}

func (f *fakeReconciler) ReconcileNetworks(_ context.Context, plans []netplan.Plan, ids []string, mode netplan.MigrationMode, _ func(string)) (netplan.Report, error) {
	f.plans, f.mode, f.ids = plans, mode, ids
	return netplan.Report{Created: []string{"paas-env-e1"}}, nil
}

type reconcilerFactory struct{ orch ContainerOrchestrator }

func (f reconcilerFactory) ForServer(*serverPkg.Server) (ContainerOrchestrator, func(), error) {
	return f.orch, func() {}, nil
}

type fakeEnvReader map[string][]string

func (f fakeEnvReader) ListAllForDeployment(_ context.Context, id string) ([]string, error) {
	return f[id], nil
}

func TestReconcileNetworksBuildsPlansAndReportsCrossRefs(t *testing.T) {
	repo := &netRepo{
		scopes: map[string]*NetworkScope{
			"api": {Scope: ScopeEnvironment, ServiceID: "api", Name: "api", EnvironmentID: "stg", ProjectID: "p", OrgID: "o"},
			"pg":  {Scope: ScopeEnvironment, ServiceID: "pg", Name: "pg", Alias: "pg-prod", EnvironmentID: "prd", ProjectID: "p", OrgID: "o"},
			// "broken" has no scope row and is skipped.
		},
		ids: []string{"api", "broken", "pg"},
	}
	rec := &fakeReconciler{}
	m := NewServiceManager(repo, zap.NewNop())
	m.WithOrchestration(nil, reconcilerFactory{rec})
	m.WithEnvVarReader(fakeEnvReader{"api": {"DATABASE_URL=postgres://u:secret@pg-prod:5432/db"}})

	var logs []string
	report, err := m.ReconcileNetworks(context.Background(), "", netplan.MigrationReport, func(s string) { logs = append(logs, s) })
	require.NoError(t, err)
	assert.Equal(t, netplan.MigrationReport, rec.mode)
	assert.Len(t, rec.plans, 2)
	assert.Equal(t, []string{"api", "broken", "pg"}, rec.ids, "the host learns about services without a plan too")
	assert.Equal(t, []string{"paas-env-e1"}, report.Created)
	require.Len(t, report.CrossRefs, 1)
	assert.Equal(t, "DATABASE_URL", report.CrossRefs[0].Variable)
	require.Len(t, report.Errors, 1)
	assert.Contains(t, report.Errors[0], "broken")
	for _, l := range logs {
		assert.NotContains(t, l, "secret", "values are never logged")
	}
}

func TestReconcileNetworksNeedsAReconciler(t *testing.T) {
	m := NewServiceManager(&netRepo{}, zap.NewNop())
	m.WithOrchestration(nil, reconcilerFactory{nil})
	_, err := m.ReconcileNetworks(context.Background(), "", netplan.MigrationIsolate, func(string) {})
	assert.Error(t, err)
}

// exportRepo serves ListByProject for the compose export.
type exportRepo struct {
	Repository
	svcs []Service
}

func (r exportRepo) ListByProject(context.Context, string) ([]Service, error) { return r.svcs, nil }

func TestGenerateComposeExportUsesAStackLocalNetwork(t *testing.T) {
	m := NewServiceManager(exportRepo{svcs: []Service{
		{ID: "12345678-aaaa", Name: "db", DockerImage: "postgres:16", NetworkAlias: "postgres", SourceType: SourceTypeDockerImage},
	}}, zap.NewNop())
	out, err := m.GenerateComposeExport(context.Background(), "p")
	require.NoError(t, err)
	assert.NotContains(t, out, "paas-network")
	var doc struct {
		Networks map[string]map[string]any `yaml:"networks"`
		Services map[string]struct {
			Networks map[string]struct {
				Aliases []string `yaml:"aliases"`
			} `yaml:"networks"`
		} `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(out), &doc))
	require.Contains(t, doc.Networks, "app")
	assert.NotEqual(t, true, doc.Networks["app"]["external"])
	assert.Equal(t, []string{"postgres"}, doc.Services["db"].Networks["app"].Aliases)
	assert.False(t, strings.Contains(out, "external: true\n    name"), "no external network")
}

func TestNetworkPlanWithPreviews(t *testing.T) {
	repo := &netRepo{
		scopes: map[string]*NetworkScope{
			"web-pr42": {Scope: ScopeEnvironment, ServiceID: "web-pr42", EnvironmentID: "pr42", ProjectID: "p1", OrgID: "o1"},
			"web-pr45": {Scope: ScopeEnvironment, ServiceID: "web-pr45", EnvironmentID: "pr45", ProjectID: "p1", OrgID: "o1"},
			"pg":       {Scope: ScopeEnvironment, ServiceID: "pg", EnvironmentID: "staging", ProjectID: "p1", OrgID: "o1"},
			"redis":    {Scope: ScopeProject, ServiceID: "redis", ProjectID: "p1", OrgID: "o1"},
		},
		peers: map[string][]NetworkPeer{
			"web-pr42": {{ID: "api-pr42", Scope: ScopeEnvironment}, {ID: "redis", Scope: ScopeProject}},
			"web-pr45": {{ID: "api-pr45", Scope: ScopeEnvironment}, {ID: "redis", Scope: ScopeProject}, {ID: "graf", Scope: ScopeOrg}},
		},
		targets:        []NetworkTarget{{EnvironmentID: "staging", ProjectID: "p1"}, {EnvironmentID: "pr42", ProjectID: "p1"}, {EnvironmentID: "pr45", ProjectID: "p1"}},
		previewOf:      map[string]string{"pr42": "staging", "pr45": "staging"},
		untrusted:      map[string]bool{"pr45": true},
		sourcePeers:    map[string][]NetworkPeer{"staging": {{ID: "pg", Scope: ScopeEnvironment}}},
		previewTargets: map[string][]string{"staging": {"pr42"}},
	}
	m := NewServiceManager(repo, zap.NewNop())
	ctx := context.Background()

	p, err := m.NetworkPlan(ctx, "web-pr42")
	require.NoError(t, err)
	assert.False(t, p.Home.Untrusted)
	assert.ElementsMatch(t, []string{"api-pr42", "redis", "pg"}, endpointIDs(p.Attach), "a trusted preview reaches staging's services it has no copy of")

	p, err = m.NetworkPlan(ctx, "web-pr45")
	require.NoError(t, err)
	assert.True(t, p.Home.Untrusted)
	assert.Equal(t, "toft-upr45", p.Home.BridgeName())
	assert.Equal(t, []string{"api-pr45"}, endpointIDs(p.Attach), "an untrusted preview reaches its own services alone")

	p, err = m.NetworkPlan(ctx, "pg")
	require.NoError(t, err)
	assert.Equal(t, []string{"paas-env-pr42"}, p.Networks()[1:], "staging's postgres publishes itself into the trusted preview only")

	p, err = m.NetworkPlan(ctx, "redis")
	require.NoError(t, err)
	assert.NotContains(t, p.Networks(), "paas-env-pr45", "a shared service never publishes into an untrusted preview")
	assert.Contains(t, p.Networks(), "paas-env-pr42")
}

func endpointIDs(eps []netplan.Endpoint) []string {
	out := make([]string, 0, len(eps))
	for _, e := range eps {
		out = append(out, e.ServiceID)
	}
	return out
}
