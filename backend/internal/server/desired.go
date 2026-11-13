package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"go.uber.org/zap"
)

// The desired state (D-111): each agent is sent what should run on its
// server, numbered by servers.spec_generation (raised by the statements
// that change a service's server, runtime status or active deployment). Its
// answer goes into observed_generation and agent_drift. Nothing acts on
// the drift yet.

// DesiredServices lists the services on a server as its agent's desired
// state (service.PostgresRepository).
type DesiredServices interface {
	DesiredOn(ctx context.Context, serverID string) ([]agentproto.DesiredService, error)
}

// desiredEvery is how often each connected agent is asked about its drift,
// and sent the desired state while it is behind.
const desiredEvery = 30 * time.Second

// WithDesired enables the desired state (after WithAgents).
func (s *Service) WithDesired(services DesiredServices) {
	s.agents.desired = services
}

// SyncDesired asks every connected agent about the desired state every
// desiredEvery until ctx ends.
func (s *Service) SyncDesired(ctx context.Context) {
	t := time.NewTicker(desiredEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		ids, err := s.agents.store.ListAgentServerIDs(ctx)
		if err != nil {
			s.log.Warn("could not list the agents' servers", zap.Error(err))
			continue
		}
		// ponytail: one server at a time, each bounded by its timeout; in
		// parallel if a tick outgrows desiredEvery.
		for _, id := range ids {
			ack, err := s.pushDesired(ctx, id, false, false)
			if err != nil {
				s.log.Warn("desired state", zap.String("server_id", id), zap.Error(err))
				continue
			}
			// Traefik still on the labels (a rollout ran when the agent
			// connected, or the switch failed): switched once it can be.
			if ack != nil && ack.Frozen == nil && ack.Routes == nil {
				if srv, err := s.repo.GetByID(ctx, id); err == nil {
					s.cutOverRoutes(ctx, srv)
				}
			}
		}
	}
}

// pushDesired sends serverID's agent the desired state when it is behind
// (or full is set: it just connected), or else asks it for the drift
// against the state it holds, and records its answer. unfreeze sends the
// state with the panel's choice to an agent frozen under another epoch
// (D-113). It returns nil, nil when the agent doesn't keep the state.
func (s *Service) pushDesired(ctx context.Context, serverID string, full, unfreeze bool) (*agentproto.DesiredAck, error) {
	if s.agents.desired == nil {
		return nil, nil
	}
	if st, ok := s.agents.hub.State(serverID); !ok || !st.Connected || !slices.Contains(st.Caps, agentproto.CapDesired) {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	epoch, err := s.agents.store.PanelEpoch(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the panel's epoch: %w", err)
	}
	spec, observed, err := s.agents.store.AgentGeneration(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("read the server's generation: %w", err)
	}
	req := agentproto.Desired{Epoch: epoch, Generation: spec, Unfreeze: unfreeze}
	if full || unfreeze || observed < spec {
		// Read after the generation: a change in between is sent again.
		if req.Services, err = s.agents.desired.DesiredOn(ctx, serverID); err != nil {
			return nil, fmt.Errorf("list the server's services: %w", err)
		}
	}
	srv, err := s.repo.GetByID(ctx, serverID)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	out, err := s.SSH(srv).Subsystem(ctx, agentproto.SubsystemDesired, bytes.NewReader(append(raw, '\n')))
	if err != nil {
		return nil, fmt.Errorf("the agent did not take the desired state: %w", err)
	}
	var ack agentproto.DesiredAck
	if err := json.Unmarshal([]byte(out), &ack); err != nil {
		return nil, fmt.Errorf("the agent's answer to the desired state can't be read: %w", err)
	}
	if ack.Frozen != nil {
		running, _ := json.Marshal(ack.Frozen)
		if err := s.agents.store.RecordAgentFrozen(ctx, serverID, running, ack.Routes); err != nil {
			return nil, fmt.Errorf("record the frozen agent's answer: %w", err)
		}
		return &ack, nil
	}
	if ack.Drift == nil {
		ack.Drift = []agentproto.Drift{}
	}
	drift, _ := json.Marshal(ack.Drift)
	if err := s.agents.store.RecordAgentObserved(ctx, serverID, ack.Generation, drift, ack.Routes); err != nil {
		return nil, fmt.Errorf("record the agent's answer to the desired state: %w", err)
	}
	return &ack, nil
}
