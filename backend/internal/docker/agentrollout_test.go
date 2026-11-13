package docker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/rollout/fakedocker"
	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func event(t *testing.T, ev rollout.Event) string {
	raw, err := json.Marshal(ev)
	require.NoError(t, err)
	return string(raw)
}

func agentOrchestrator(t *testing.T, h *fakeHost) *RemoteOrchestrator {
	saved := followRetry
	followRetry = time.Millisecond
	t.Cleanup(func() { followRetry = saved })
	o := newRemoteOrchestrator(h, rollout.NetConfig{}, zap.NewNop())
	o.agent = true
	return o
}

// A rollout goes to the server's agent once the hand-off is recorded; a
// connection lost on the way is picked up again from the lines the panel
// has, through to the agent's result.
func TestRolloutIsHandedToTheAgent(t *testing.T) {
	var order []string
	attaches := 0
	h := &fakeHost{}
	h.subsystem = func(name, stdin string, lineFn func(string)) error {
		order = append(order, name)
		switch name {
		case agentproto.SubsystemRollout:
			var req rollout.Request
			require.NoError(t, json.Unmarshal([]byte(stdin), &req))
			assert.Equal(t, "dep-1", req.Spec.Slot.Labels[rollout.LabelDeploymentID], "the labels the panel computes travel with it")
			lineFn(event(t, rollout.Event{Log: "one"}))
			lineFn(event(t, rollout.Event{Log: "two"}))
			return errors.New("connection lost")
		default:
			if attaches++; attaches == 1 {
				return errors.New("the agent is away")
			}
			var at rollout.Attach
			require.NoError(t, json.Unmarshal([]byte(stdin), &at))
			assert.Equal(t, rollout.Attach{ID: "dep-1", Offset: 2}, at)
			lineFn(event(t, rollout.Event{Log: "three"}))
			lineFn(event(t, rollout.Event{Result: &rollout.Result{New: "green", Changed: true}}))
			return nil
		}
	}
	var logs []string
	res := agentOrchestrator(t, h).Rollout(t.Context(), rollout.Spec{DeploymentID: "dep-1", Slot: rollout.Slot{ServiceID: "svc"}}, deployment.Routing{},
		rollout.Hooks{Log: func(l string) { logs = append(logs, l) }, HandOff: func() error { order = append(order, "handoff"); return nil }})

	assert.Equal(t, rollout.Result{New: "green", Changed: true}, res)
	assert.Equal(t, []string{"handoff", agentproto.SubsystemRollout, agentproto.SubsystemRolloutAttach, agentproto.SubsystemRolloutAttach}, order)
	assert.Equal(t, []string{"one", "two"}, logs[:2])
	assert.True(t, strings.HasPrefix(logs[2], rollout.PanelLinePrefix), "the panel's own line is marked: %q", logs[2])
	assert.Equal(t, "three", logs[3])
}

// A request the agent never got fails the deploy before it changed
// anything; so does one the agent refused; and an unrecorded hand-off is
// never sent.
func TestRolloutTheAgentNeverRan(t *testing.T) {
	spec := rollout.Spec{DeploymentID: "dep-1", Slot: rollout.Slot{ServiceID: "svc"}}
	handOff := func() error { return nil }

	h := &fakeHost{subsystem: func(name, _ string, lineFn func(string)) error {
		if name == agentproto.SubsystemRolloutAttach {
			lineFn("error: " + rollout.UnknownRolloutPrefix + "dep-1")
		}
		return errors.New("exit status 1")
	}}
	res := agentOrchestrator(t, h).Rollout(t.Context(), spec, deployment.Routing{}, rollout.Hooks{HandOff: handOff})
	assert.Equal(t, rollout.StepConnect, res.Step)
	assert.False(t, res.Changed)
	assert.Contains(t, res.Err, "never received")

	h = &fakeHost{subsystem: func(_, _ string, lineFn func(string)) error {
		lineFn("error: the rollout names no service")
		return errors.New("exit status 1")
	}}
	res = agentOrchestrator(t, h).Rollout(t.Context(), spec, deployment.Routing{}, rollout.Hooks{HandOff: handOff})
	assert.False(t, res.Changed)
	assert.Contains(t, res.Err, "names no service")
	assert.Len(t, h.recorded(), 1, "a refusal is not followed")

	h = &fakeHost{subsystem: func(string, string, func(string)) error { t.Fatal("sent"); return nil }}
	res = agentOrchestrator(t, h).Rollout(t.Context(), spec, deployment.Routing{}, rollout.Hooks{HandOff: func() error { return errors.New("db down") }})
	assert.False(t, res.Changed)
	assert.Contains(t, res.Err, "db down")
}

// A panel shutting down stops following; the deploy stays the agent's.
func TestFollowStopsWhenThePanelShutsDown(t *testing.T) {
	h := &fakeHost{subsystem: func(string, string, func(string)) error { return errors.New("offline") }}
	_, err := agentOrchestrator(t, h).FollowRollout(t.Context(), "dep-1", 0, rollout.Hooks{Abandon: func() bool { return true }})
	require.Error(t, err)
	assert.NotErrorIs(t, err, rollout.ErrUnknownRollout)
}

// A server moved to SSH, or whose agent was revoked, mid-rollout stops the
// follow: nobody can say how the rollout ended, and the deploy fails as one
// that changed the server.
func TestFollowStopsWhenTheAgentIsGone(t *testing.T) {
	h := &fakeHost{subsystem: func(string, string, func(string)) error { return errors.New("offline") }}
	gone := func() bool { return true }
	_, err := agentOrchestrator(t, h).FollowRollout(t.Context(), "dep-1", 0, rollout.Hooks{Gone: gone})
	require.ErrorIs(t, err, rollout.ErrAgentGone)

	spec := rollout.Spec{DeploymentID: "dep-1", Slot: rollout.Slot{ServiceID: slotSvcID, ServiceName: "web", Image: "app:1"}}
	res := agentOrchestrator(t, h).Rollout(t.Context(), spec, deployment.Routing{}, rollout.Hooks{HandOff: func() error { return nil }, Gone: gone})
	assert.True(t, res.Changed)
	assert.Contains(t, res.Err, "no longer connects through its agent")
}

// A compose deploy's up goes to the server's agent with the .env file, the
// override files and the route check in the request: none of them is
// uploaded, and the panel does not run up or log in itself.
func TestComposeUpIsHandedToTheAgent(t *testing.T) {
	release := rollout.ComposeDir(slotSvcID) + "/r-abc"
	var req rollout.Request
	h := &fakeHost{docker: fakedocker.New(t), exec: func(_ context.Context, cmd string) (string, error) {
		if strings.Contains(cmd, "mktemp -d") {
			return release + "\n", nil
		}
		return "", nil
	}}
	h.subsystem = func(name, stdin string, lineFn func(string)) error {
		require.Equal(t, agentproto.SubsystemRollout, name)
		require.NoError(t, json.Unmarshal([]byte(stdin), &req))
		lineFn(event(t, rollout.Event{Result: &rollout.Result{Changed: true}}))
		return nil
	}
	route := &deployment.ComposeRoute{Service: "web", Port: 8080, Domains: []string{"app.example.com"}}
	err := agentOrchestrator(t, h).RunCompose(t.Context(), "dep-1", slotSvcID, "web", "services: {}\n",
		map[string]string{"A": "1"}, route, deployment.ComposeNetworks{Home: "paas-network"}, nil,
		&deployment.RegistryAuth{Server: "ghcr.io", Password: "pw"},
		rollout.Hooks{Log: func(string) {}, HandOff: func() error { return nil }})
	require.NoError(t, err)

	c := req.Compose
	require.NotNil(t, c)
	assert.Equal(t, "dep-1", c.DeploymentID)
	assert.Equal(t, release, c.ProjectDir)
	assert.Equal(t, release+"/docker-compose.yml", c.ComposeFile)
	assert.Contains(t, string(c.DotEnv), `A="1"`)
	assert.Equal(t, "pw", c.Auth.Password)
	assert.Equal(t, "web", c.RoutedService)
	assert.NotEmpty(t, c.AllowedLabels)
	assert.NotEmpty(t, c.Overrides)
	_, up := h.find("up -d")
	assert.False(t, up, "the panel runs no up of its own")
	_, login := h.find("docker login")
	assert.False(t, login, "nor a registry login")
	for _, r := range h.recorded() {
		assert.NotEqual(t, "upload '"+release+"/.env'", r.kind+" "+r.cmd, "the .env file is not uploaded")
	}
	_, pruned := h.find("exec rm -rf {} +")
	assert.True(t, pruned, "older releases are removed after a successful up")
}

// A service's own container check goes to an agent that applies it; one
// that would drop it, and keep the image's, is handed nothing: the deploy
// fails and says why.
func TestHandOffContainerCheck(t *testing.T) {
	spec := rollout.Spec{DeploymentID: "dep-1", Slot: rollout.Slot{ServiceID: "svc", Check: rollout.ContainerCheck{Off: true}}}
	for _, applies := range []bool{true, false} {
		var req rollout.Request
		h := &fakeHost{subsystem: func(_, stdin string, lineFn func(string)) error {
			require.NoError(t, json.Unmarshal([]byte(stdin), &req))
			lineFn(event(t, rollout.Event{Result: &rollout.Result{New: "green"}}))
			return nil
		}}
		o := agentOrchestrator(t, h)
		o.checks = applies
		handedOff := false
		res := o.Rollout(t.Context(), spec, deployment.Routing{}, rollout.Hooks{Log: func(string) {}, HandOff: func() error { handedOff = true; return nil }})
		if applies {
			assert.Equal(t, "green", res.New)
			assert.True(t, req.Spec.Slot.Check.Off)
			continue
		}
		assert.False(t, handedOff)
		assert.Empty(t, h.recorded(), "nothing was sent")
		assert.Contains(t, res.Err, "can't apply the service's container check until it upgrades")
	}

	// The image's check needs nothing of the agent.
	h := &fakeHost{subsystem: func(_, _ string, lineFn func(string)) error {
		lineFn(event(t, rollout.Event{Result: &rollout.Result{New: "green"}}))
		return nil
	}}
	res := agentOrchestrator(t, h).Rollout(t.Context(), rollout.Spec{DeploymentID: "dep-1", Slot: rollout.Slot{ServiceID: "svc"}},
		deployment.Routing{}, rollout.Hooks{Log: func(string) {}, HandOff: func() error { return nil }})
	assert.Equal(t, "green", res.New)
}

// An agent that keeps the deploy time limit is handed the time left, and the
// panel follows it to its end past the panel's own deadline; for one that
// can't, the panel's limit ends at the hand-off (D-125).
func TestHandOffCarriesTheDeployTimeLimit(t *testing.T) {
	spec := rollout.Spec{DeploymentID: "dep-1", Slot: rollout.Slot{ServiceID: "svc"}}
	for _, keeps := range []bool{true, false} {
		var req rollout.Request
		h := &fakeHost{subsystem: func(_, stdin string, lineFn func(string)) error {
			require.NoError(t, json.Unmarshal([]byte(stdin), &req))
			lineFn(event(t, rollout.Event{Result: &rollout.Result{New: "green"}}))
			return nil
		}}
		o := agentOrchestrator(t, h)
		o.deadlines = keeps
		ctx, cancel := context.WithCancel(t.Context())
		lifted := false
		var logs []string
		res := o.Rollout(ctx, spec, deployment.Routing{}, rollout.Hooks{
			Log:      func(l string) { logs = append(logs, l) },
			HandOff:  func() error { cancel(); return nil }, // the limit runs out as the agent takes over
			TimeLeft: func() time.Duration { return 3 * time.Minute },
			NoLimit:  func() { lifted = true },
		})

		assert.Equal(t, "green", res.New, "keeps=%v", keeps)
		if keeps {
			assert.Equal(t, 3*time.Minute, req.TimeLeft)
			assert.False(t, lifted)
			assert.False(t, h.recorded()[0].ctxEnded, "the panel follows past its own deadline")
		} else {
			assert.Zero(t, req.TimeLeft)
			assert.True(t, lifted, "the panel's limit ends at the hand-off")
			assert.Contains(t, strings.Join(logs, "\n"), "can't keep the deploy time limit")
		}
	}
}
