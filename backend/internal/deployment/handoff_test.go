package deployment_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// agentOrch is a server's orchestrator whose agent runs rollouts: Rollout
// records the hand-off, and FollowRollout answers with result (unknown:
// the agent never received it).
type agentOrch struct {
	*mockOrchestrator
	result  rollout.Result
	unknown bool
	offsets []int
}

func (o *agentOrch) Rollout(_ context.Context, _ rollout.Spec, _ deployment.Routing, hooks rollout.Hooks) rollout.Result {
	if err := hooks.HandOff(); err != nil {
		return rollout.Result{Step: rollout.StepConnect, Err: err.Error()}
	}
	return o.result
}

func (o *agentOrch) FollowRollout(_ context.Context, _ string, offset int, hooks rollout.Hooks) (rollout.Result, error) {
	o.offsets = append(o.offsets, offset)
	if o.unknown {
		return rollout.Result{}, rollout.ErrUnknownRollout
	}
	hooks.Log("from the agent")
	return o.result, nil
}

// agentServers serves one server on connection, whose agent is revoked
// when revoked is set.
type agentServers struct {
	connection string
	revoked    bool
}

func (r agentServers) GetServer(_ context.Context, id string) (*server.Server, error) {
	srv := &server.Server{ID: id, Connection: r.connection}
	if r.connection == server.ConnectionAgent && !r.revoked {
		srv.AgentKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAgent"
	}
	return srv, nil
}

type onServer struct{}

func (onServer) Get(_ context.Context, id string) (*service.Service, error) {
	return &service.Service{ID: id, ServerID: "srv-1", RuntimeStatus: "running", ActiveDeploymentID: "dep-prev"}, nil
}

func agentService(repo *mockRepo, orch *agentOrch, connection string) *deployment.Service {
	return agentServiceOn(repo, orch, agentServers{connection: connection})
}

func agentServiceOn(repo *mockRepo, orch *agentOrch, servers agentServers) *deployment.Service {
	svc := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
	svc.WithServiceReader(onServer{})
	svc.WithServerReader(servers)
	return svc
}

// A deploy is recorded as handed off before its rollout goes to the agent,
// with what the panel needs to record its outcome after a restart.
func TestDeployRecordsTheHandOff(t *testing.T) {
	repo := &mockRepo{createdID: "dep-1"}
	orch := &agentOrch{mockOrchestrator: &mockOrchestrator{}}
	svc := agentService(repo, orch, server.ConnectionAgent)
	_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
		ServiceID: "svc-1", ServiceName: "web", ServerID: "srv-1", Server: &server.Server{ID: "srv-1"}, OrgID: "org-1", SourceType: "docker-image", DockerImage: "nginx",
		EnvVars: []string{"SECRET=s3"},
	})
	require.NoError(t, err)
	waitForStatus(t, repo, deployment.StatusRunning, 2*time.Second)

	repo.mu.Lock()
	raw := repo.handedOff["dep-1"]
	repo.mu.Unlock()
	require.NotEmpty(t, raw)
	var in map[string]any
	require.NoError(t, json.Unmarshal(raw, &in))
	assert.Equal(t, "org-1", in["OrgID"])
	assert.NotContains(t, string(raw), "s3", "no secret is stored")
}

// After a restart the panel follows a handed-off deploy from the lines it
// has of the agent's (its own marked lines aside) to the agent's result.
func TestFollowHandedOffRecordsTheAgentsResult(t *testing.T) {
	repo := &mockRepo{logMessages: []string{"one", rollout.PanelLinePrefix + "the panel's", "two"}}
	repo.handedOffList = []deployment.HandedOff{{
		Deployment: deployment.Deployment{ID: "dep-1", ServiceID: "svc-1", Status: deployment.StatusBuilding},
		Input:      []byte(`{"ServiceName":"web"}`),
	}}
	orch := &agentOrch{mockOrchestrator: &mockOrchestrator{}, result: rollout.Result{Changed: true, New: "green"}}
	agentService(repo, orch, server.ConnectionAgent).FollowHandedOff(context.Background())()

	assert.Equal(t, []int{2}, orch.offsets)
	assert.Equal(t, []deployment.Status{deployment.StatusRunning}, repo.statusHistory)
	assert.Contains(t, repo.logMessages, "from the agent")
}

// A deploy the server's agent runs has its service's turn from the moment
// the panel lists it, before the follow runs: a deploy asked for in between
// waits for it.
func TestFollowHandedOffHoldsItsTurnBeforeItFollows(t *testing.T) {
	repo := &mockRepo{createdID: "dep-2", handedOffList: []deployment.HandedOff{{
		Deployment: deployment.Deployment{ID: "dep-1", ServiceID: "svc-1", Status: deployment.StatusBuilding},
		Input:      []byte(`{"ServiceName":"web"}`),
	}}}
	orch := &agentOrch{mockOrchestrator: &mockOrchestrator{}, result: rollout.Result{Changed: true, New: "green"}}
	svc := agentService(repo, orch, server.ConnectionAgent)
	follow := svc.FollowHandedOff(context.Background())

	_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
		ServiceID: "svc-1", ServiceName: "web", ServerID: "srv-1", Server: &server.Server{ID: "srv-1"}, SourceType: "docker-image", DockerImage: "nginx",
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		repo.mu.Lock()
		defer repo.mu.Unlock()
		return slices.Contains(repo.logMessages, "Waiting for this service's earlier deploy to finish...")
	}, 2*time.Second, 10*time.Millisecond)
	repo.mu.Lock()
	assert.Empty(t, repo.statusHistory, "the new deploy has not started")
	repo.mu.Unlock()

	follow()
	require.Eventually(t, func() bool {
		repo.mu.Lock()
		defer repo.mu.Unlock()
		return len(repo.statusHistory) > 1 && repo.statusHistory[len(repo.statusHistory)-1] == deployment.StatusRunning
	}, 2*time.Second, 10*time.Millisecond, "the followed deploy is recorded, then the one that waited runs")
}

// A deploy aimed at a server its service has left since the caller read it
// (a move ended in between) is refused; a push is recorded as a failed
// deployment that says why. Neither keeps the service's turn, nor does a
// deploy that ends before its pipeline starts: the next one doesn't wait.
func TestTriggerRefusesAServerTheServiceLeft(t *testing.T) {
	for name, tc := range map[string]struct {
		origin           deployment.Origin
		serverID, server string // the input's ServerID, and its Server's
		createErr        error
		want             error
		recorded         []deployment.Status
	}{
		"asked for in the panel": {deployment.OriginInteractive, "srv-0", "srv-0", nil, deployment.ErrServiceMoving, nil},
		"a push":                 {deployment.OriginWebhook, "srv-0", "srv-0", nil, deployment.ErrServiceMoving, []deployment.Status{deployment.StatusFailed}},
		// A push reads the servers before it reads each service.
		"a push with the server read earlier": {deployment.OriginWebhook, "srv-1", "srv-0", nil, deployment.ErrServiceMoving, []deployment.Status{deployment.StatusFailed}},
		"already deployed":                    {deployment.OriginWebhook, "srv-1", "srv-1", deployment.ErrAlreadyDeployed, deployment.ErrAlreadyDeployed, nil},
	} {
		t.Run(name, func(t *testing.T) {
			repo := &mockRepo{createdID: "dep-1", createErr: tc.createErr}
			svc := newTestService(repo, &mockFactory{orch: &mockOrchestrator{}}, &mockStatusUpdater{})
			svc.WithServiceReader(onServer{}) // on srv-1
			in := deployment.TriggerInput{
				ServiceID: "svc-1", ServiceName: "web", ServerID: tc.serverID, Server: &server.Server{ID: tc.server},
				SourceType: "docker-image", DockerImage: "nginx", Origin: tc.origin,
			}
			_, err := svc.Trigger(context.Background(), in)
			require.ErrorIs(t, err, tc.want)
			assert.Equal(t, tc.recorded, repo.statusHistory)
			if tc.recorded != nil {
				assert.Contains(t, repo.logMessages, "✗ "+deployment.ErrServiceMoving.Error())
			}

			repo.createErr = nil
			in.ServerID, in.Server = "srv-1", &server.Server{ID: "srv-1"}
			_, err = svc.Trigger(context.Background(), in)
			require.NoError(t, err)
			waitForStatus(t, repo, deployment.StatusRunning, 2*time.Second)
			repo.mu.Lock()
			defer repo.mu.Unlock()
			assert.NotContains(t, repo.logMessages, "Waiting for this service's earlier deploy to finish...")
		})
	}
}

// A deploy the agent never received, or whose server no longer has an
// agent, is interrupted as any deploy a restart cut short.
func TestFollowHandedOffInterruptsWhatTheAgentDoesNotHave(t *testing.T) {
	for name, tc := range map[string]struct {
		unknown    bool
		connection string
		revoked    bool
	}{
		"never received": {unknown: true, connection: server.ConnectionAgent},
		"no agent":       {connection: server.ConnectionSSH},
		"agent revoked":  {connection: server.ConnectionAgent, revoked: true},
	} {
		t.Run(name, func(t *testing.T) {
			repo := &mockRepo{handedOffList: []deployment.HandedOff{{
				Deployment: deployment.Deployment{ID: "dep-1", ServiceID: "svc-1", Status: deployment.StatusBuilding},
				Input:      []byte(`{}`),
			}}}
			orch := &agentOrch{mockOrchestrator: &mockOrchestrator{}, unknown: tc.unknown}
			agentServiceOn(repo, orch, agentServers{connection: tc.connection, revoked: tc.revoked}).FollowHandedOff(context.Background())()

			assert.Equal(t, []deployment.Status{deployment.StatusFailed}, repo.statusHistory)
			assert.Equal(t, 1, repo.handOffClears)
			assert.True(t, strings.Contains(strings.Join(repo.logMessages, "\n"), "✗ Interrupted"), repo.logMessages)
		})
	}
}

func (o *agentOrch) RunCompose(_ context.Context, _, _, _, _ string, _ map[string]string, _ *deployment.ComposeRoute, _ deployment.ComposeNetworks, _ map[string]deployment.ComposeLimit, _ *deployment.RegistryAuth, hooks rollout.Hooks) error {
	return hooks.HandOff()
}

// A compose deploy is the panel's again once the agent ran its up: a
// restart from there does not run the post-deploy commands twice.
func TestComposeDeployIsTakenBackAfterUp(t *testing.T) {
	repo := &mockRepo{createdID: "dep-1"}
	orch := &agentOrch{mockOrchestrator: &mockOrchestrator{}}
	_, err := agentService(repo, orch, server.ConnectionAgent).Trigger(context.Background(), deployment.TriggerInput{
		ServiceID: "svc-1", ServiceName: "web", ServerID: "srv-1", Server: &server.Server{ID: "srv-1"}, SourceType: "docker-compose", ComposeContent: "services:\n  web:\n    image: nginx\n",
	})
	require.NoError(t, err)
	waitForStatus(t, repo, deployment.StatusRunning, 2*time.Second)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	assert.Empty(t, repo.handedOff)
	assert.Equal(t, 1, repo.handOffClears)
}

// A panel that shuts down while the agent runs the rollout keeps the hand-off,
// and marks its own line so the next start's offset into the agent's log
// skips none of the agent's lines.
func TestShutdownKeepsTheHandOffWithAPanelLine(t *testing.T) {
	repo := &mockRepo{createdID: "dep-1"}
	orch := &agentOrch{mockOrchestrator: &mockOrchestrator{}, result: rollout.Result{Step: rollout.StepConnect, Err: "the panel is shutting down", Changed: true}}
	svc := agentService(repo, orch, server.ConnectionAgent)
	svc.BeginShutdown()
	_, err := svc.Trigger(context.Background(), deployment.TriggerInput{ServiceID: "svc-1", ServiceName: "web", ServerID: "srv-1", Server: &server.Server{ID: "srv-1"}, SourceType: "docker-image", DockerImage: "nginx"})
	require.NoError(t, err)

	require.Eventually(t, func() bool { return repo.hasLog("recovered when it restarts") }, 2*time.Second, 5*time.Millisecond)
	require.Eventually(t, func() bool { return svc.InFlightCount() == 0 }, time.Second, 5*time.Millisecond)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	assert.NotEmpty(t, repo.handedOff["dep-1"], "the hand-off is kept for the next start")
	assert.Equal(t, []deployment.Status{deployment.StatusBuilding}, repo.statusHistory)
	for _, l := range repo.logMessages {
		if strings.Contains(l, "recovered when it restarts") {
			assert.True(t, strings.HasPrefix(l, rollout.PanelLinePrefix), l)
		}
	}
}
