package deployment_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/netplan"
	"github.com/Nickpap21/toft/backend/internal/server"
)

// A deploy's pipeline asks for an orchestrator whose builds run on the
// untrusted builder for a preview on an untrusted network. What a probe
// once read of the server's firewall decides nothing: every build asks its
// host. The development setting takes the question away on the panel's own
// host alone, and the build's log says so.
func TestPipeline_AsksForTheDeploysBuilds(t *testing.T) {
	const warning = "TOFT_DEV_BUILDS_WITHOUT_FIREWALL is set"
	srv := &server.Server{ID: "srv-1", Name: "fsn1"}
	git := deployment.TriggerInput{ServiceID: netSvcID, ServiceName: "api", SourceType: "git", GitRepoURL: "https://github.com/example/repo", GitBranch: "main"}
	onServer := git
	onServer.Server, onServer.ServerID = srv, srv.ID
	// A server without its ID beside it is still that server.
	serverOnly := git
	serverOnly.Server = srv
	untrusted := isolatedPlan()
	untrusted.Home.Untrusted = true

	for name, tc := range map[string]struct {
		dev    bool
		plan   netplan.Plan
		in     deployment.TriggerInput
		want   deployment.Builds
		warned bool
	}{
		"the panel's host":                   {false, isolatedPlan(), git, deployment.Builds{}, false},
		"a server":                           {false, isolatedPlan(), onServer, deployment.Builds{}, false},
		"an untrusted preview":               {false, untrusted, git, deployment.Builds{Untrusted: true}, false},
		"development, the panel's host":      {true, isolatedPlan(), git, deployment.Builds{Unchecked: true}, true},
		"development, an image builds none":  {true, isolatedPlan(), imageInput(), deployment.Builds{Unchecked: true}, false},
		"development is never a server's":    {true, isolatedPlan(), onServer, deployment.Builds{}, false},
		"a server is never the panel's host": {true, isolatedPlan(), serverOnly, deployment.Builds{}, false},
		"development, an untrusted preview":  {true, untrusted, git, deployment.Builds{Untrusted: true, Unchecked: true}, true},
	} {
		repo := &mockRepo{createdID: "dep-b"}
		factory := &mockFactory{orch: &mockOrchestrator{}}
		svc := newTestService(repo, factory, &mockStatusUpdater{})
		svc.WithAppNetworks(true, "paas-network", stubPlanner{plan: tc.plan})
		svc.WithDevBuilds(tc.dev)
		runDeploy(t, svc, repo, tc.in, deployment.StatusRunning)
		assert.Equal(t, []deployment.Builds{tc.want}, factory.asked(), name)
		repo.mu.Lock()
		assert.Equal(t, tc.warned, strings.Contains(strings.Join(repo.logMessages, "\n"), warning), name)
		repo.mu.Unlock()
	}
}
