package deployment_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/netplan"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const netSvcID = "5e4f0000-0000-0000-0000-000000000001"

type stubPlanner struct {
	plan netplan.Plan
	err  error
}

func (p stubPlanner) NetworkPlan(context.Context, string) (netplan.Plan, error) { return p.plan, p.err }

func isolatedPlan() netplan.Plan {
	return netplan.Plan{ServiceID: netSvcID, ServiceName: "api", Alias: "api", Home: netplan.Env("0e000000-0000-0000-0000-0000000000e1", "0a000000-0000-0000-0000-0000000000a1", "0b000000-0000-0000-0000-0000000000b1")}
}

func imageInput() deployment.TriggerInput {
	return deployment.TriggerInput{
		ServiceID:      netSvcID,
		ServiceName:    "api",
		SourceType:     "docker-image",
		DockerImage:    "nginx:1",
		MigrateCommand: "migrate up",
		NetworkAlias:   "api-alias",
	}
}

func runDeploy(t *testing.T, svc *deployment.Service, repo *mockRepo, in deployment.TriggerInput, want deployment.Status) {
	t.Helper()
	_, err := svc.Trigger(context.Background(), in)
	require.NoError(t, err)
	waitForStatus(t, repo, want, 5*time.Second)
}

func TestPipeline_LegacyAppNetworksDeployAsBefore(t *testing.T) {
	repo := &mockRepo{createdID: "dep-legacy"}
	orch := &mockOrchestrator{}
	svc := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
	svc.WithAppNetworks(false, "cp-net", stubPlanner{err: errors.New("never asked")})

	runDeploy(t, svc, repo, imageInput(), deployment.StatusRunning)
	assert.Equal(t, []string{"ephemeral cp-net", "run cp-net"}, orch.recorded(), "no PrepareNetworks under legacy")
	assert.True(t, orch.runPlan.Legacy)
	assert.Equal(t, "api-alias", orch.runPlan.Alias, "the alias comes from the trigger, as before")
}

func TestPipeline_DefaultIsLegacyOnPaasNetwork(t *testing.T) {
	repo := &mockRepo{createdID: "dep-default"}
	orch := &mockOrchestrator{}
	svc := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
	runDeploy(t, svc, repo, imageInput(), deployment.StatusRunning)
	assert.Equal(t, []string{"ephemeral paas-network", "run paas-network"}, orch.recorded())
}

func TestPipeline_IsolatedFailsClosedWithoutAPlan(t *testing.T) {
	for name, planner := range map[string]deployment.NetworkPlanner{
		"no planner":    nil,
		"planner error": stubPlanner{err: errors.New("environment_id is NULL")},
		"legacy plan":   stubPlanner{plan: netplan.LegacyPlan(netSvcID, "api", "", "")},
		"invalid plan":  stubPlanner{plan: netplan.Plan{ServiceID: netSvcID, Home: netplan.Network{Name: "paas-network"}}},
	} {
		t.Run(name, func(t *testing.T) {
			repo := &mockRepo{createdID: "dep-noplan"}
			orch := &mockOrchestrator{}
			svc := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
			svc.WithAppNetworks(true, "paas-network", planner)
			runDeploy(t, svc, repo, imageInput(), deployment.StatusFailed)
			assert.Empty(t, orch.recorded(), "nothing runs, and nothing falls back to the control plane's network")
			repo.mu.Lock()
			defer repo.mu.Unlock()
			assert.Contains(t, strings.Join(repo.logMessages, "\n"), "network plan")
		})
	}
}

func TestPipeline_IsolatedPreparesNetworksFirst(t *testing.T) {
	repo := &mockRepo{createdID: "dep-iso"}
	orch := &mockOrchestrator{}
	svc := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
	svc.WithAppNetworks(true, "paas-network", stubPlanner{plan: isolatedPlan()})

	runDeploy(t, svc, repo, imageInput(), deployment.StatusRunning)
	home := isolatedPlan().Home.Name
	assert.Equal(t, []string{"prepare " + home, "ephemeral " + home, "run " + home}, orch.recorded())
	assert.Equal(t, "api", orch.runPlan.Alias, "the alias comes from the plan")
	repo.mu.Lock()
	defer repo.mu.Unlock()
	assert.Contains(t, repo.logMessages, "→ network: "+home)
}

func TestPipeline_IsolatedPrepareFailureFailsTheDeploy(t *testing.T) {
	repo := &mockRepo{createdID: "dep-prep"}
	orch := &mockOrchestrator{prepareErr: errors.New("network paas-env-0e000000-0000-0000-0000-0000000000e1 exists but was not created by the panel")}
	svc := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
	svc.WithAppNetworks(true, "paas-network", stubPlanner{plan: isolatedPlan()})
	runDeploy(t, svc, repo, imageInput(), deployment.StatusFailed)
	assert.Equal(t, []string{"prepare " + isolatedPlan().Home.Name}, orch.recorded())
}

func composeInput(content string) deployment.TriggerInput {
	return deployment.TriggerInput{ServiceID: netSvcID, ServiceName: "api", SourceType: "docker-compose", ComposeContent: content}
}

func TestPipeline_IsolatedComposePointsTheControlPlaneNetworkAtHome(t *testing.T) {
	repo := &mockRepo{createdID: "dep-compose"}
	orch := &mockOrchestrator{}
	svc := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
	plan := isolatedPlan()
	plan.Compose = true
	svc.WithAppNetworks(true, "paas-network", stubPlanner{plan: plan})

	content := "services:\n  web:\n    image: nginx\n    networks: [front]\nnetworks:\n  front:\n    external: true\n    name: paas-network\n"
	runDeploy(t, svc, repo, composeInput(content), deployment.StatusRunning)
	home := plan.Home.Name
	assert.Equal(t, []string{"prepare " + home, "compose", "prepare " + home}, orch.recorded(),
		"networks are prepared before up, and again after so the stack is published")
	orch.mu.Lock()
	defer orch.mu.Unlock()
	assert.True(t, orch.nets.Isolated)
	assert.Equal(t, home, orch.nets.Home)
	assert.Equal(t, "front", orch.nets.RouteKey)
	assert.Contains(t, string(orch.nets.Override), "name: "+home)
	assert.Equal(t, content, orch.composeIn, "the user's file is passed unchanged; the override does the rewrite")
}

func TestPipeline_IsolatedComposeRefusesOtherNetworksEvenWithoutAPolicy(t *testing.T) {
	repo := &mockRepo{createdID: "dep-compose-bad"}
	orch := &mockOrchestrator{}
	svc := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
	svc.WithAppNetworks(true, "paas-network", stubPlanner{plan: isolatedPlan()})

	content := "services:\n  web:\n    image: nginx\n    networks: [x]\nnetworks:\n  x:\n    external: true\n    name: paas-env-${OTHER}\n"
	in := composeInput(content)
	in.EnvVars = []string{"OTHER=s3cret-env-id"}
	runDeploy(t, svc, repo, in, deployment.StatusFailed)
	assert.NotContains(t, orch.recorded(), "compose")
	repo.mu.Lock()
	defer repo.mu.Unlock()
	logs := strings.Join(repo.logMessages, "\n")
	assert.Contains(t, logs, "compose rejected: networks.x")
	assert.NotContains(t, logs, "s3cret", "interpolated values are not logged")
}

func TestPipeline_LegacyComposeListsItsServicesForTheGuard(t *testing.T) {
	repo := &mockRepo{createdID: "dep-compose-legacy"}
	orch := &mockOrchestrator{}
	svc := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
	content := "services:\n  web:\n    image: nginx\n    networks: [x]\nnetworks:\n  x:\n    external: true\n    name: monitoring\n"
	runDeploy(t, svc, repo, composeInput(content), deployment.StatusRunning)
	assert.Equal(t, []string{"compose"}, orch.recorded())
	orch.mu.Lock()
	defer orch.mu.Unlock()
	assert.Equal(t, deployment.ComposeNetworks{Home: "paas-network", Services: []string{"web"}}, orch.nets,
		"networks unchanged; services listed so unrouted ones get traefik.enable=false")
}
