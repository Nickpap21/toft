package server

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/agenthub"
	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"github.com/Nickpap21/toft/backend/pkg/sshpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	apiID    = "aaaaaaaa-0000-0000-0000-000000000001"
	webID    = "bbbbbbbb-0000-0000-0000-000000000002"
	cronID   = "cccccccc-0000-0000-0000-000000000003"
	stackID  = "dddddddd-0000-0000-0000-000000000004"
	strayID  = "eeeeeeee-0000-0000-0000-000000000005"
	newDepID = "11111111-0000-0000-0000-000000000001"
)

func panelServices() []agentproto.DesiredService {
	return []agentproto.DesiredService{
		{ID: apiID, Running: true, Deployment: "dep-old"},
		{ID: webID, Running: true, Deployment: "dep-web"},
		{ID: cronID, Running: true, Deployment: "dep-cron"},
		{ID: stackID, Compose: true, Running: true, Deployment: "dep-stack"},
	}
}

func serverRuns() []agentproto.Running {
	return []agentproto.Running{
		{Service: apiID, Deployment: "dep-old", Container: "api-blue", Image: "api:old", State: "exited", Created: 100},
		{Service: apiID, Deployment: newDepID, Container: "api-green", Image: "nginx:1.27.3", State: "running", Created: 200},
		{Service: webID, Deployment: "dep-web", Container: "web-blue", Image: "web:1", State: "running", Created: 50},
		{Service: stackID[:8], Compose: true, Container: "paas-stack-dddddddd-db-1", Image: "postgres:16", State: "running", Created: 60},
		{Service: strayID, Deployment: "dep-x", Container: "paas-eeeeeeee-blue", Image: "x:1", State: "running", Created: 70},
	}
}

// What a frozen server runs, against the restored panel: api runs a newer
// deploy, web and the compose stack match, cron runs nothing, and one
// container is of a service the panel doesn't have.
func TestFrozenChangesCompareWhatRunsWithThePanel(t *testing.T) {
	got := frozenChanges(serverRuns(), panelServices())
	kinds := make([]string, 0, len(got))
	for _, c := range got {
		kinds = append(kinds, c.Kind+" "+deref(c.ServiceID)+deref(c.Container))
	}
	assert.Equal(t, []string{
		"newer " + apiID + "api-green",
		"missing " + cronID,
		"unknown paas-eeeeeeee-blue",
		"same " + webID + "web-blue",
		"same " + stackID + "paas-stack-dddddddd-db-1",
	}, kinds)
	assert.Equal(t, newDepID, *got[0].DeploymentID)
	assert.Equal(t, "nginx:1.27.3", *got[0].Image)
	assert.Equal(t, time.Unix(200, 0).UTC(), *got[0].StartedAt)
}

// frozenHost is an agent frozen under another epoch until the panel sends
// its choice.
type frozenHost struct {
	sshpool.RemoteHost
	frozen bool
	sent   []agentproto.Desired
	drops  bool // the connection drops on the panel's choice
}

func (h *frozenHost) Subsystem(_ context.Context, name string, stdin io.Reader) (string, error) {
	if name != agentproto.SubsystemDesired {
		return "", io.EOF
	}
	var req agentproto.Desired
	if err := json.NewDecoder(stdin).Decode(&req); err != nil {
		return "", err
	}
	h.sent = append(h.sent, req)
	if req.Unfreeze && h.drops {
		return "", io.ErrUnexpectedEOF
	}
	if req.Unfreeze {
		h.frozen = false
	}
	ack := agentproto.DesiredAck{Generation: req.Generation, Drift: []agentproto.Drift{}}
	if h.frozen {
		ack = agentproto.DesiredAck{Frozen: serverRuns()}
	}
	raw, _ := json.Marshal(ack)
	return string(raw), nil
}

type frozenConn struct{ h *frozenHost }

func (c frozenConn) Handle(sshpool.Target) sshpool.RemoteHost { return c.h }
func (frozenConn) Invalidate(string)                          {}

type panelState struct{}

func (panelState) DesiredOn(context.Context, string) ([]agentproto.DesiredService, error) {
	return panelServices(), nil
}

type adoptions struct{ got []string }

func (a *adoptions) AdoptDeployment(_ context.Context, actorID, serverID, serviceID, deploymentID, image string, _ time.Time) error {
	a.got = append(a.got, actorID+" "+serverID+" "+serviceID+" "+deploymentID+" "+image)
	return nil
}

type audits struct{ actions []string }

func (a *audits) WriteAuditLogDetail(_ context.Context, _, _, action, _ string, _ map[string]any) {
	a.actions = append(a.actions, action)
}

// A frozen agent's answer is recorded, with what runs; adopting makes the
// newer deploy the active one and unfreezes the agent, and a server that
// isn't frozen has nothing to choose.
func TestAdoptingAFrozenServersState(t *testing.T) {
	svc, store, hub := agentService(t)
	audit := &audits{}
	svc.agents.audit = audit
	store.rec("s1").Key = "k"
	host := &frozenHost{frozen: true}
	svc.WithSSH(frozenConn{host}, nil)
	svc.WithDesired(panelState{})
	adopted := &adoptions{}
	svc.WithAdopter(adopted)
	hub.known, hub.state = true, agenthub.State{Connected: true, Caps: []string{agentproto.CapDesired}}
	ctx := context.Background()

	assert.ErrorIs(t, svc.ChooseAgentState(ctx, "u1", "s1", true), ErrNotFrozen)

	ack, err := svc.pushDesired(ctx, "s1", true, false)
	require.NoError(t, err)
	require.NotNil(t, ack.Frozen)
	rec, _ := store.GetAgent(ctx, "s1")
	require.NotNil(t, rec.FrozenAt)
	frozen, _ := store.AgentsFrozen(ctx)
	assert.Equal(t, map[string]bool{"s1": true}, frozen)
	view := svc.frozenView(ctx, "s1", *rec)
	require.NotNil(t, view)
	assert.Len(t, view.Changes, 5)

	require.NoError(t, svc.ChooseAgentState(ctx, "u1", "s1", true))
	assert.Equal(t, []string{"u1 s1 " + apiID + " " + newDepID + " nginx:1.27.3"}, adopted.got)
	last := host.sent[len(host.sent)-1]
	assert.True(t, last.Unfreeze)
	assert.NotNil(t, last.Services)
	rec, _ = store.GetAgent(ctx, "s1")
	assert.Nil(t, rec.FrozenAt, "the agent's answer under this epoch ends the freeze")
	assert.Equal(t, []string{"server.agent_state_adopted"}, audit.actions)
}

// Using the panel's state adopts nothing.
func TestUsingThePanelsStateAdoptsNothing(t *testing.T) {
	svc, store, hub := agentService(t)
	store.rec("s1").Key = "k"
	host := &frozenHost{frozen: true}
	svc.WithSSH(frozenConn{host}, nil)
	svc.WithDesired(panelState{})
	adopted := &adoptions{}
	svc.WithAdopter(adopted)
	hub.known, hub.state = true, agenthub.State{Connected: true, Caps: []string{agentproto.CapDesired}}
	ctx := context.Background()
	_, err := svc.pushDesired(ctx, "s1", true, false)
	require.NoError(t, err)

	require.NoError(t, svc.ChooseAgentState(ctx, "u1", "s1", false))
	assert.Empty(t, adopted.got)
	assert.True(t, host.sent[len(host.sent)-1].Unfreeze)
}

// An adoption cut short (the agent drops before it takes the choice) is
// still audited with what it adopted.
func TestAnAdoptionCutShortIsAudited(t *testing.T) {
	svc, store, hub := agentService(t)
	audit := &audits{}
	svc.agents.audit = audit
	store.rec("s1").Key = "k"
	host := &frozenHost{frozen: true, drops: true}
	svc.WithSSH(frozenConn{host}, nil)
	svc.WithDesired(panelState{})
	adopted := &adoptions{}
	svc.WithAdopter(adopted)
	hub.known, hub.state = true, agenthub.State{Connected: true, Caps: []string{agentproto.CapDesired}}
	ctx := context.Background()
	_, err := svc.pushDesired(ctx, "s1", true, false)
	require.NoError(t, err)

	require.Error(t, svc.ChooseAgentState(ctx, "u1", "s1", true))
	assert.Len(t, adopted.got, 1)
	assert.Equal(t, []string{"server.agent_state_adopted"}, audit.actions)
}
