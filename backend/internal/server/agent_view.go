package server

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Nickpap21/toft/backend/internal/agenthub"
	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"github.com/Nickpap21/toft/backend/pkg/sshclient"
	"go.uber.org/zap"
)

// Agent states the API reports (D-106).
const (
	AgentConnected    = "connected"
	AgentReconnecting = "reconnecting"
	AgentUpgrading    = "upgrading"
	AgentOffline      = "offline"
	AgentWaiting      = "waiting"
	AgentRevoked      = "revoked"
)

// AgentResponse is a server's agent as the API shows it.
type AgentResponse struct {
	Status string `json:"status"`
	// Since: connected since, away since, or revoked at.
	Since          *time.Time `json:"since"`
	LastSeenAt     *time.Time `json:"last_seen_at"`
	Version        *string    `json:"version"`
	Arch           *string    `json:"arch"`
	KeyFingerprint *string    `json:"key_fingerprint"`
	KeyPinnedAt    *time.Time `json:"key_pinned_at"`
	FromIP         *string    `json:"from_ip"`
	RTTMS          *int64     `json:"rtt_ms"`
	TokenExpiresAt *time.Time `json:"token_expires_at"`
	// PanelKeyFingerprint is the panel's own key, the only one the agent
	// accepts.
	PanelKeyFingerprint *string `json:"panel_key_fingerprint"`
	// Desired is null until the agent first answers the desired state.
	Desired *AgentDesired `json:"desired"`
	// Routes is how many services the agent's route files route (D-113),
	// null while the server's Traefik reads the containers' labels.
	Routes *int `json:"routes"`
	// Frozen is set while the agent holds another panel epoch's state.
	Frozen *AgentFrozen `json:"frozen"`
	// Rejoin: a join token for this server replaces a machine Toft trusted,
	// so issuing one asks for the password.
	Rejoin bool `json:"rejoin"`
}

// AgentDesired is the desired state the agent holds (D-111): behind while
// observed_generation < spec_generation.
type AgentDesired struct {
	SpecGeneration     int64        `json:"spec_generation"`
	ObservedGeneration int64        `json:"observed_generation"`
	Drift              []AgentDrift `json:"drift"`
	CheckedAt          time.Time    `json:"checked_at"`
}

// AgentDrift is one difference the agent sees between the desired state and
// the containers on its server.
type AgentDrift struct {
	ServiceID *string `json:"service_id"`
	Kind      string  `json:"kind"`
	Container *string `json:"container"`
}

func desiredView(rec AgentRecord) *AgentDesired {
	if rec.DriftAt == nil {
		return nil
	}
	var drift []agentproto.Drift
	_ = json.Unmarshal(rec.Drift, &drift) // the column is a JSON array (CHECK)
	d := &AgentDesired{SpecGeneration: rec.SpecGeneration, ObservedGeneration: rec.ObservedGeneration, Drift: make([]AgentDrift, 0, len(drift)), CheckedAt: *rec.DriftAt}
	for _, x := range drift {
		d.Drift = append(d.Drift, AgentDrift{ServiceID: strPtr(x.ServiceID), Kind: x.Kind, Container: strPtr(x.Container)})
	}
	return d
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// agentView combines what the database recorded with what the hub sees now.
func (s *Service) agentView(serverID string, rec AgentRecord) *AgentResponse {
	if rec.Connection != ConnectionAgent && rec.Key == "" {
		return nil // an SSH server that never had an agent
	}
	r := &AgentResponse{
		LastSeenAt: rec.LastSeenAt, Version: strPtr(rec.Version), Arch: strPtr(rec.Arch),
		KeyPinnedAt: rec.KeyPinnedAt, FromIP: strPtr(rec.FromIP), TokenExpiresAt: rec.TokenExpiresAt,
		Desired: desiredView(rec), Routes: rec.Routes, Rejoin: rejoins(&rec),
	}
	if rec.Key != "" {
		if fp, _, err := sshclient.Fingerprint(rec.Key); err == nil {
			r.KeyFingerprint = &fp
		}
	}
	if s.agents != nil {
		if fp, err := s.PanelKeyFingerprint(context.Background()); err == nil {
			r.PanelKeyFingerprint = &fp
		}
	}
	switch {
	case rec.Key == "" && rec.RevokedAt != nil && rec.TokenExpiresAt == nil:
		r.Status, r.Since = AgentRevoked, rec.RevokedAt
		return r
	case rec.Key == "":
		r.Status = AgentWaiting
		return r
	}
	if s.agents == nil {
		r.Status = AgentOffline
		return r
	}
	st, known := s.agents.hub.State(serverID)
	switch {
	case st.Upgrading:
		r.Status = AgentUpgrading
	case st.Connected:
		r.Status, r.Since = AgentConnected, &st.Since
		ms := st.RTT.Milliseconds()
		if st.RTT > 0 {
			r.RTTMS = &ms
		}
		if st.Version != "" {
			r.Version = &st.Version
		}
		return r
	case known && time.Since(st.Since) < agenthub.ReconnectGrace:
		r.Status, r.Since = AgentReconnecting, &st.Since
		return r
	case !known && time.Since(s.agents.hub.Started()) < agenthub.ReconnectGrace:
		started := s.agents.hub.Started()
		r.Status, r.Since = AgentReconnecting, &started
		return r
	default:
		r.Status = AgentOffline
	}
	if known {
		r.Since = &st.Since
	} else {
		r.Since = rec.LastSeenAt
	}
	return r
}

// WithAgentViews fills the agent of each response (one query for the
// workspace).
func (s *Service) WithAgentViews(ctx context.Context, orgID string, rs []ServerResponse) {
	if s.agents == nil {
		return
	}
	recs, err := s.agents.store.ListAgents(ctx, orgID)
	if err != nil {
		s.log.Warn("could not read the servers' agents", zap.Error(err))
		return
	}
	for i := range rs {
		if rec, ok := recs[rs[i].ID]; ok {
			rs[i].Agent = s.agentView(rs[i].ID, rec)
			if rs[i].Agent != nil {
				rs[i].Agent.Frozen = s.frozenView(ctx, rs[i].ID, rec)
			}
		}
	}
}

// WithAgentView fills one response's agent.
func (s *Service) WithAgentView(ctx context.Context, r *ServerResponse) {
	if s.agents == nil {
		return
	}
	rec, err := s.agents.store.GetAgent(ctx, r.ID)
	if err != nil {
		s.log.Warn("could not read the server's agent", zap.Error(err))
		return
	}
	r.Agent = s.agentView(r.ID, *rec)
	if r.Agent != nil {
		r.Agent.Frozen = s.frozenView(ctx, r.ID, *rec)
	}
}

// agentOfflineAfter is how long an agent is away before agent_offline fires
// (D-106). Counted in time, not samples: a sample delayed by a slow server
// probe made three samples past the grace fire at 3 min 48 s (D-107).
const agentOfflineAfter = 5 * time.Minute

// AgentsAway lists every agent server and whether its agent has been away
// for agentOfflineAfter (since it left, or since the panel started): the
// agent_offline alert's samples.
func (s *Service) AgentsAway(ctx context.Context) (map[string]bool, error) {
	if s.agents == nil {
		return map[string]bool{}, nil
	}
	ids, err := s.agents.store.ListAgentServerIDs(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		st, known := s.agents.hub.State(id)
		left := s.agents.hub.Started() // never seen since the panel started
		if known {
			left = st.Since
		}
		out[id] = !st.Connected && !st.Upgrading && time.Since(left) >= agentOfflineAfter
	}
	return out, nil
}

// AgentsFrozen lists every agent server and whether its agent holds
// another panel epoch's state (D-113): the agent_frozen alert's samples.
func (s *Service) AgentsFrozen(ctx context.Context) (map[string]bool, error) {
	if s.agents == nil {
		return map[string]bool{}, nil
	}
	return s.agents.store.AgentsFrozen(ctx)
}

// AgentsConnected lists every agent server and whether its agent is
// connected now: the services monitor leaves the others' failures to
// agent_offline (D-108).
func (s *Service) AgentsConnected(ctx context.Context) (map[string]bool, error) {
	if s.agents == nil {
		return map[string]bool{}, nil
	}
	ids, err := s.agents.store.ListAgentServerIDs(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		st, _ := s.agents.hub.State(id)
		out[id] = st.Connected
	}
	return out, nil
}
