package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"go.uber.org/zap"
)

// The restore freeze (D-113): an agent that holds another panel epoch's
// state (the panel was restored from an older backup) takes no order from
// this one, and answers with what runs. Deploys, moves and removals there
// are refused (ErrServerFrozen) until someone with the right chooses:
// adopt what runs, or use the panel's state. Either way nothing on the
// server is stopped or re-routed.

// ErrNotFrozen: the server's agent isn't frozen, so there is nothing to
// choose.
var ErrNotFrozen = errors.New("this server's agent follows this panel: there is no state to choose")

// DeploymentAdopter makes a deployment a frozen server kept running its
// service's active one, recording it when the panel has no record of it
// (deployment.Service).
type DeploymentAdopter interface {
	AdoptDeployment(ctx context.Context, actorID, serverID, serviceID, deploymentID, imageTag string, startedAt time.Time) error
}

// WithAdopter enables adopting a frozen server's state (after WithAgents).
func (s *Service) WithAdopter(a DeploymentAdopter) { s.agents.adopter = a }

// Frozen change kinds: a service runs a deployment other than the panel's
// active one (adopting makes it the active one), runs the panel's, runs for
// a service the panel doesn't have here, or doesn't run although the panel
// says it should.
const (
	FrozenNewer   = "newer"
	FrozenSame    = "same"
	FrozenUnknown = "unknown"
	FrozenMissing = "missing"
)

// AgentFrozen is a frozen agent as the API shows it: since when, and how
// what runs compares with what the panel says.
type AgentFrozen struct {
	Since   time.Time      `json:"since"`
	Changes []FrozenChange `json:"changes"`
}

// FrozenChange is one service or container of a frozen server. ServiceID is
// nil for a container of a service the panel doesn't have (Container names
// it); Deployment, Image and StartedAt describe what runs.
type FrozenChange struct {
	ServiceID    *string    `json:"service_id"`
	Container    *string    `json:"container"`
	Kind         string     `json:"kind"`
	DeploymentID *string    `json:"deployment_id"`
	Image        *string    `json:"image"`
	StartedAt    *time.Time `json:"started_at"`
}

// frozenChanges compares what runs on a frozen server with the services
// the panel has there: one change per service, and one per container of a
// service it doesn't have.
func frozenChanges(running []agentproto.Running, services []agentproto.DesiredService) []FrozenChange {
	byID := make(map[string]agentproto.DesiredService, len(services))
	compose8 := map[string]string{} // a compose service's ID's first 8 characters → its ID
	for _, svc := range services {
		byID[svc.ID] = svc
		if svc.Compose && len(svc.ID) >= 8 {
			compose8[svc.ID[:8]] = svc.ID
		}
	}
	// The newest running container of each service.
	runs := map[string]agentproto.Running{}
	out := make([]FrozenChange, 0, len(services))
	for _, r := range running {
		if r.State != "running" {
			continue
		}
		id := r.Service
		if r.Compose {
			id = compose8[r.Service]
		} else if svc, ok := byID[id]; ok && svc.Compose {
			id = ""
		}
		if _, ok := byID[id]; !ok {
			out = append(out, FrozenChange{Container: strPtr(r.Container), Kind: FrozenUnknown, DeploymentID: strPtr(r.Deployment), Image: strPtr(r.Image), StartedAt: unixPtr(r.Created)})
			continue
		}
		if prev, ok := runs[id]; !ok || r.Created > prev.Created {
			runs[id] = r
		}
	}
	for _, svc := range services {
		r, ok := runs[svc.ID]
		c := FrozenChange{ServiceID: strPtr(svc.ID)}
		switch {
		case !ok && svc.Running && svc.Deployment != "":
			c.Kind = FrozenMissing
		case !ok:
			continue
		case svc.Compose || svc.Running && r.Deployment == svc.Deployment:
			c.Kind = FrozenSame
		default:
			c.Kind = FrozenNewer
		}
		if ok {
			c.Container, c.DeploymentID, c.Image, c.StartedAt = strPtr(r.Container), strPtr(r.Deployment), strPtr(r.Image), unixPtr(r.Created)
		}
		out = append(out, c)
	}
	order := map[string]int{FrozenNewer: 0, FrozenMissing: 1, FrozenUnknown: 2, FrozenSame: 3}
	sort.SliceStable(out, func(i, j int) bool {
		if order[out[i].Kind] != order[out[j].Kind] {
			return order[out[i].Kind] < order[out[j].Kind]
		}
		return deref(out[i].ServiceID)+deref(out[i].Container) < deref(out[j].ServiceID)+deref(out[j].Container)
	})
	return out
}

func unixPtr(sec int64) *time.Time {
	if sec <= 0 {
		return nil
	}
	t := time.Unix(sec, 0).UTC()
	return &t
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// frozenView is rec's freeze for the API, nil when it isn't frozen.
func (s *Service) frozenView(ctx context.Context, serverID string, rec AgentRecord) *AgentFrozen {
	if rec.FrozenAt == nil {
		return nil
	}
	f := &AgentFrozen{Since: *rec.FrozenAt, Changes: []FrozenChange{}}
	changes, err := s.frozenState(ctx, serverID, rec)
	if err != nil {
		s.log.Warn("could not compare the frozen server's state", zap.Error(err), zap.String("server_id", serverID))
		return f
	}
	f.Changes = changes
	return f
}

func (s *Service) frozenState(ctx context.Context, serverID string, rec AgentRecord) ([]FrozenChange, error) {
	var running []agentproto.Running
	if err := json.Unmarshal(rec.Frozen, &running); err != nil {
		return nil, fmt.Errorf("read what runs: %w", err)
	}
	if s.agents.desired == nil {
		return nil, errors.New("the desired state is not configured")
	}
	services, err := s.agents.desired.DesiredOn(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("list the server's services: %w", err)
	}
	return frozenChanges(running, services), nil
}

// ChooseAgentState ends a server's freeze (D-113): adopt makes what runs
// there the panel's state (each service's newer deployment becomes its
// active one), and otherwise the agent drops the state it held and follows
// the panel. Nothing on the server is stopped or re-routed. Owners and
// instance operators only (the handler's adminGate); audited.
func (s *Service) ChooseAgentState(ctx context.Context, actorID, serverID string, adopt bool) error {
	if s.agents == nil {
		return ErrAgentsNotConfigured
	}
	srv, err := s.repo.GetByID(ctx, serverID)
	if err != nil {
		return err
	}
	rec, err := s.agents.store.GetAgent(ctx, serverID)
	if err != nil {
		return err
	}
	if rec.FrozenAt == nil {
		return ErrNotFrozen
	}
	if st, ok := s.agents.hub.State(serverID); !ok || !st.Connected || !slices.Contains(st.Caps, agentproto.CapDesired) {
		return ErrAgentOffline
	}
	adopted := []string{}
	// What was adopted is audited even when a later step fails.
	failed := func(err error) error {
		if len(adopted) > 0 {
			s.auditAgent(ctx, srv.OrgID, actorID, "server.agent_state_adopted", serverID,
				map[string]any{"name": srv.Name, "services": adopted, "failed": err.Error()})
		}
		return err
	}
	if adopt {
		if s.agents.adopter == nil {
			return errors.New("adopting a server's state is not configured")
		}
		changes, err := s.frozenState(ctx, serverID, *rec)
		if err != nil {
			return err
		}
		for _, c := range changes {
			if c.Kind != FrozenNewer || c.DeploymentID == nil || c.StartedAt == nil {
				continue
			}
			if err := s.agents.adopter.AdoptDeployment(ctx, actorID, serverID, *c.ServiceID, *c.DeploymentID, deref(c.Image), *c.StartedAt); err != nil {
				return failed(fmt.Errorf("adopt %s's deployment: %w", *c.ServiceID, err))
			}
			adopted = append(adopted, *c.ServiceID)
		}
	}
	ack, err := s.pushDesired(ctx, serverID, true, true)
	if err != nil {
		return failed(err)
	}
	if ack == nil {
		return failed(ErrAgentOffline)
	}
	if ack.Frozen != nil {
		return failed(errors.New("the agent kept its state: try again"))
	}
	action, detail := "server.agent_state_discarded", map[string]any{"name": srv.Name}
	if adopt {
		action, detail["services"] = "server.agent_state_adopted", adopted
	}
	s.auditAgent(ctx, srv.OrgID, actorID, action, serverID, detail)
	// The switch to the agent's routes waited for the choice.
	go s.cutOverRoutes(context.WithoutCancel(ctx), srv)
	return nil
}
