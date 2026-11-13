package server

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/agenthub"
	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"github.com/Nickpap21/toft/backend/pkg/sshpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// desiredHost is an agent that holds generation held and answers drift.
type desiredHost struct {
	sshpool.RemoteHost
	held int64
	sent []agentproto.Desired
}

func (h *desiredHost) Subsystem(_ context.Context, name string, stdin io.Reader) (string, error) {
	if name != agentproto.SubsystemDesired {
		return "", io.EOF
	}
	var req agentproto.Desired
	if err := json.NewDecoder(stdin).Decode(&req); err != nil {
		return "", err
	}
	h.sent = append(h.sent, req)
	if req.Services != nil {
		h.held = req.Generation
	}
	raw, _ := json.Marshal(agentproto.DesiredAck{Generation: h.held, Drift: []agentproto.Drift{{ServiceID: "svc-1", Kind: agentproto.DriftMissing}}})
	return string(raw), nil
}

type desiredConn struct{ h *desiredHost }

func (c desiredConn) Handle(sshpool.Target) sshpool.RemoteHost { return c.h }
func (desiredConn) Invalidate(string)                          {}

type oneService struct{}

func (oneService) DesiredOn(context.Context, string) ([]agentproto.DesiredService, error) {
	return []agentproto.DesiredService{{ID: "svc-1", Running: true, Deployment: "dep-1"}}, nil
}

// An agent behind the panel's generation is sent the services; one up to
// date is only asked for its drift. Its answer is recorded either way, and
// an agent that can't keep the desired state is left alone.
func TestDesiredStateGoesToAgentsBehindIt(t *testing.T) {
	svc, store, hub := agentService(t)
	host := &desiredHost{}
	svc.WithSSH(desiredConn{host}, nil)
	svc.WithDesired(oneService{})
	hub.known, hub.state = true, agenthub.State{Connected: true, Caps: []string{agentproto.CapRollout, agentproto.CapDesired}}
	store.spec = 3
	ctx := context.Background()

	svc.pushDesired(ctx, "s1", false, false)
	require.Len(t, host.sent, 1)
	assert.Equal(t, agentproto.Desired{Epoch: "epoch-1", Generation: 3, Services: []agentproto.DesiredService{{ID: "svc-1", Running: true, Deployment: "dep-1"}}}, host.sent[0])
	assert.Equal(t, int64(3), store.observed)
	assert.JSONEq(t, `[{"serviceId":"svc-1","kind":"missing"}]`, string(store.drift))

	svc.pushDesired(ctx, "s1", false, false)
	require.Len(t, host.sent, 2)
	assert.Nil(t, host.sent[1].Services, "an agent up to date is only asked for its drift")

	svc.pushDesired(ctx, "s1", true, false)
	assert.NotNil(t, host.sent[2].Services, "one that just connected gets it all")

	hub.state.Caps = []string{agentproto.CapRollout}
	svc.pushDesired(ctx, "s1", true, false)
	assert.Len(t, host.sent, 3, "an agent without the capability is not asked")
}
