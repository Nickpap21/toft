// Package agenttest is an in-memory agent.Repository for tests, with the
// database's rules where the service relies on them (codes once, rotation,
// one decision per approval, a run claimed once).
package agenttest

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/Nickpap21/toft/backend/internal/agent"
	"github.com/Nickpap21/toft/backend/pkg/actor"
	"github.com/google/uuid"
)

type conn struct {
	agent.ConnectionRow
	ClientID    string
	AllProjects bool
	ProjectIDs  []string
	LastTool    string
}

type token struct {
	conn, kind string
	exp        time.Time
	used       bool
}

type approval struct {
	agent.Approval
	org, hash string
	reqHash   string
	Sealed    string
}

// Project is a seeded project: its name and workspace.
type Project struct{ Name, Org string }

// Repo is the fake. Services seeds ServiceInfo; OrgProjects which projects
// belong to which workspace.
type Repo struct {
	mu          sync.Mutex
	Clients     map[string]*agent.Client // by client_id
	codes       map[string]*agent.Code
	conns       map[string]*conn
	tokens      map[string]*token
	policy      map[string]map[string]map[string]string
	approvals   map[string]*approval
	orgPaused   map[string]*agent.PauseState
	instPaused  *agent.PauseState
	Services    map[string]*agent.ServiceInfo
	Envs        map[string]*agent.ServiceInfo
	Projects    map[string]Project
	projPolicy  map[string]map[string]map[string]string
	OrgProjects map[string][]string
	// Rollbacks are the rollbacks an approval covers, by
	// connection|service|deployment: the approval and its approver; a
	// claim uses one up (D-146). The database decides them for real.
	Rollbacks map[string][2]string
	// Deployments are the deployments approved runs started, by approval.
	Deployments map[string]string
	Released    []string
}

func New() *Repo {
	return &Repo{Clients: map[string]*agent.Client{}, codes: map[string]*agent.Code{}, conns: map[string]*conn{},
		tokens: map[string]*token{}, policy: map[string]map[string]map[string]string{}, approvals: map[string]*approval{},
		orgPaused: map[string]*agent.PauseState{}, Services: map[string]*agent.ServiceInfo{}, OrgProjects: map[string][]string{},
		Envs: map[string]*agent.ServiceInfo{}, Projects: map[string]Project{}, projPolicy: map[string]map[string]map[string]string{},
		Rollbacks: map[string][2]string{}, Deployments: map[string]string{}}
}

func (r *Repo) GetClient(_ context.Context, clientID string) (*agent.Client, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.Clients[clientID]; ok {
		cp := *c
		return &cp, nil
	}
	return nil, agent.ErrNotFound
}

func (r *Repo) GetClientByID(_ context.Context, id string) (*agent.Client, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.Clients {
		if c.ID == id {
			cp := *c
			return &cp, nil
		}
	}
	return nil, agent.ErrNotFound
}

func (r *Repo) UpsertCIMDClient(_ context.Context, clientID, name string, uris []string) (*agent.Client, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.Clients[clientID]
	if ok && c.Kind != "cimd" {
		return nil, agent.ErrConflict
	}
	if !ok {
		c = &agent.Client{ID: uuid.NewString(), ClientID: clientID, Kind: "cimd"}
		r.Clients[clientID] = c
	}
	c.Name, c.RedirectURIs, c.FetchedAt = name, uris, time.Now()
	cp := *c
	return &cp, nil
}

func (r *Repo) InsertDCRClient(_ context.Context, clientID, name string, uris []string, maxUnused int) (*agent.Client, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	unused := 0
	for _, c := range r.Clients {
		if c.Kind == "dcr" && !r.used(c.ID) {
			unused++
		}
	}
	if unused >= maxUnused {
		return nil, agent.ErrFull
	}
	c := &agent.Client{ID: uuid.NewString(), ClientID: clientID, Kind: "dcr", Name: name, RedirectURIs: uris, FetchedAt: time.Now()}
	r.Clients[clientID] = c
	cp := *c
	return &cp, nil
}

// used is whether a client has a connection or a code: the database's
// unused registration, inverted. The caller holds mu.
func (r *Repo) used(clientID string) bool {
	for _, c := range r.conns {
		if c.ClientID == clientID {
			return true
		}
	}
	for _, c := range r.codes {
		if c.ClientID == clientID {
			return true
		}
	}
	return false
}

func (r *Repo) InsertCode(_ context.Context, c agent.Code) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.codes[c.Hash] = &c
	return nil
}

func (r *Repo) UseCode(_ context.Context, hash string) (*agent.Code, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.codes[hash]
	if !ok || time.Now().After(c.ExpiresAt) {
		return nil, agent.ErrNotFound
	}
	delete(r.codes, hash)
	return c, nil
}

func (r *Repo) CreateConnection(_ context.Context, n agent.NewConnection) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range n.Code.ProjectIDs {
		if !slices.Contains(r.OrgProjects[n.Code.OrgID], p) {
			return "", agent.ErrForbidden
		}
	}
	id := uuid.NewString()
	r.conns[id] = &conn{ConnectionRow: agent.ConnectionRow{ID: id, OrgID: n.Code.OrgID, UserID: n.Code.UserID, Name: n.Name,
		Scopes: n.Code.Scopes, ExpiresAt: n.ExpiresAt}, ClientID: n.Code.ClientID, AllProjects: n.Code.AllProjects, ProjectIDs: n.Code.ProjectIDs}
	r.tokens[n.AccessHash] = &token{conn: id, kind: "access", exp: n.AccessExp}
	r.tokens[n.RefreshHash] = &token{conn: id, kind: "refresh", exp: n.ExpiresAt}
	return id, nil
}

func (r *Repo) live(id string) (*conn, bool) {
	c, ok := r.conns[id]
	return c, ok && c.RevokedAt == nil && time.Now().Before(c.ExpiresAt)
}

func (r *Repo) RotateRefresh(ctx context.Context, hash, accessHash string, accessExp time.Time, refreshHash string) (string, []string, error) {
	r.mu.Lock()
	t, ok := r.tokens[hash]
	if !ok || t.kind != "refresh" || time.Now().After(t.exp) {
		r.mu.Unlock()
		return "", nil, agent.ErrNotFound
	}
	if t.used {
		r.mu.Unlock()
		_, _ = r.RevokeConnection(ctx, t.conn, "", "reused")
		return "", nil, agent.ErrConflict
	}
	c, live := r.live(t.conn)
	if !live {
		r.mu.Unlock()
		return "", nil, agent.ErrNotFound
	}
	t.used = true
	r.tokens[accessHash] = &token{conn: t.conn, kind: "access", exp: accessExp}
	r.tokens[refreshHash] = &token{conn: t.conn, kind: "refresh", exp: c.ExpiresAt}
	r.mu.Unlock()
	return t.conn, c.Scopes, nil
}

func (r *Repo) ValidateAccess(_ context.Context, hash string) (*agent.Live, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tokens[hash]
	if !ok || t.kind != "access" || time.Now().After(t.exp) {
		return nil, agent.ErrNotFound
	}
	c, live := r.live(t.conn)
	if !live {
		return nil, agent.ErrNotFound
	}
	return &agent.Live{ConnectionID: c.ID, OrgID: c.OrgID, UserID: c.UserID, Name: c.Name, AllProjects: c.AllProjects,
		ProjectIDs: slices.Clone(c.ProjectIDs), Scopes: slices.Clone(c.Scopes), OrgPaused: r.orgPaused[c.OrgID] != nil}, nil
}

func (r *Repo) InsertToken(_ context.Context, hash, connectionID, kind string, exp time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens[hash] = &token{conn: connectionID, kind: kind, exp: exp}
	return nil
}

func (r *Repo) DeleteToken(_ context.Context, hash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tokens, hash)
	return nil
}

func (r *Repo) TokenConnection(_ context.Context, hash string) (string, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tokens[hash]
	if !ok {
		return "", "", agent.ErrNotFound
	}
	return t.conn, t.kind, nil
}

func (r *Repo) GetConnection(_ context.Context, id string) (*agent.ConnectionRow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.conns[id]
	if !ok {
		return nil, agent.ErrNotFound
	}
	cp := c.ConnectionRow
	return &cp, nil
}

func (r *Repo) ListConnections(_ context.Context, orgID, userID string) ([]agent.Connection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []agent.Connection{}
	for _, c := range r.conns {
		if _, live := r.live(c.ID); !live || c.OrgID != orgID || (userID != "" && c.UserID != userID) {
			continue
		}
		out = append(out, agent.Connection{ID: c.ID, Name: c.Name, UserID: c.UserID, AllProjects: c.AllProjects,
			Projects: []string{}, Scopes: c.Scopes, ExpiresAt: c.ExpiresAt})
	}
	return out, nil
}

func (r *Repo) RevokeConnection(_ context.Context, id, by, note string) (*agent.ConnectionRow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.conns[id]
	if !ok || c.RevokedAt != nil {
		return nil, agent.ErrNotFound
	}
	now := time.Now()
	c.RevokedAt = &now
	r.cancel(func(a *approval) bool { return a.ConnectionID == id }, note)
	cp := c.ConnectionRow
	return &cp, nil
}

func (r *Repo) cancel(match func(*approval) bool, note string) {
	now := time.Now()
	for _, a := range r.approvals {
		if a.State == "waiting" && match(a) {
			a.State, a.DecidedAt, a.Note, a.Sealed = "cancelled", &now, note, ""
		}
	}
}

func (r *Repo) Touch(_ context.Context, id, tool string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.conns[id]; ok {
		c.LastTool = tool
	}
	return nil
}

func (r *Repo) ListPolicy(_ context.Context, orgID string) (map[string]map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]map[string]string{}
	for a, m := range r.policy[orgID] {
		out[a] = map[string]string{}
		for k, v := range m {
			out[a][k] = v
		}
	}
	return out, nil
}

func (r *Repo) SetPolicy(_ context.Context, orgID, _ string, cells []agent.PolicyCell) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.policy[orgID] == nil {
		r.policy[orgID] = map[string]map[string]string{}
	}
	for _, c := range cells {
		if r.policy[orgID][c.Action] == nil {
			r.policy[orgID][c.Action] = map[string]string{}
		}
		r.policy[orgID][c.Action][c.Class] = c.Decision
	}
	return nil
}

func (r *Repo) ServiceInfo(_ context.Context, id string) (*agent.ServiceInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.Services[id]; ok {
		cp := *s
		return &cp, nil
	}
	return nil, agent.ErrNotFound
}

func (r *Repo) InsertApproval(_ context.Context, n agent.NewApproval) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := n.ID
	if id == "" {
		id = uuid.NewString()
	}
	sid, pid := n.ServiceID, n.ProjectID
	r.approvals[id] = &approval{Approval: agent.Approval{ID: id, ConnectionID: n.ConnectionID, AgentName: r.conns[n.ConnectionID].Name,
		UserID: n.UserID, Action: n.Action, Tool: n.Tool, ServiceID: &sid, ProjectID: &pid, Target: n.Target, Request: n.Request,
		Reason: n.Reason, Plan: n.Plan, PlanSteps: fakeSteps(n.PlanSteps), State: "waiting", ExpiresAt: n.ExpiresAt, CreatedAt: time.Now()},
		org: n.OrgID, reqHash: n.RequestHash, Sealed: n.Sealed}
	return id, nil
}

func fakeSteps(in []actor.PlanStep) []agent.PlanStep {
	out := make([]agent.PlanStep, 0, len(in))
	for _, p := range in {
		s := agent.PlanStep{Text: p.Text, Tool: p.Tool}
		if p.ServiceID != "" {
			id := p.ServiceID
			s.ServiceID = &id
		}
		out = append(out, s)
	}
	return out
}

func (r *Repo) FindOpenApproval(_ context.Context, connectionID, hash string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.approvals {
		if a.ConnectionID == connectionID && a.reqHash == hash && time.Now().Before(a.ExpiresAt) &&
			(a.State == "waiting" || (a.State == "approved" && a.ExecutedAt != nil)) {
			return a.ID, nil
		}
	}
	return "", agent.ErrNotFound
}

func (r *Repo) GetApproval(_ context.Context, id string) (*agent.Approval, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.approvals[id]
	if !ok {
		return nil, "", agent.ErrNotFound
	}
	cp := a.Approval
	return &cp, a.org, nil
}

func (r *Repo) ListWaiting(_ context.Context, orgID string) ([]agent.Waiting, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []agent.Waiting{}
	for _, a := range r.approvals {
		if a.org == orgID && a.State == "waiting" && time.Now().Before(a.ExpiresAt) {
			out = append(out, agent.Waiting{ID: a.ID, AgentName: a.AgentName, Tool: a.Tool, Target: a.Target, Reason: a.Reason, ExpiresAt: a.ExpiresAt})
		}
	}
	return out, nil
}

func (r *Repo) DecideApproval(_ context.Context, id, orgID, state, by, note string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.approvals[id]
	if !ok || a.org != orgID || a.State != "waiting" || time.Now().After(a.ExpiresAt) {
		return agent.ErrNotFound
	}
	now := time.Now()
	a.State, a.DecidedBy, a.DecidedAt, a.Note = state, by, &now, note
	if state != "approved" {
		a.Sealed = ""
	}
	return nil
}

func (r *Repo) Sealed(_ context.Context, id string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a, ok := r.approvals[id]; ok && a.State == "approved" && a.Sealed != "" {
		return a.Sealed, nil
	}
	return "", agent.ErrNotFound
}

func (r *Repo) ClearSealed(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a, ok := r.approvals[id]; ok {
		a.Sealed = ""
	}
	return nil
}

// SealedOf returns what the request keeps of a secret's value, for tests.
func (r *Repo) SealedOf(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a, ok := r.approvals[id]; ok {
		return a.Sealed
	}
	return ""
}

func (r *Repo) EnvironmentInfo(_ context.Context, id string) (*agent.ServiceInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.Envs[id]; ok {
		cp := *s
		return &cp, nil
	}
	return nil, agent.ErrNotFound
}

func (r *Repo) ProjectInfo(_ context.Context, id string) (string, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.Projects[id]; ok {
		return p.Name, p.Org, nil
	}
	return "", "", agent.ErrNotFound
}

func (r *Repo) ListProjectPolicy(_ context.Context, id string) (map[string]map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]map[string]string{}
	for a, m := range r.projPolicy[id] {
		out[a] = map[string]string{}
		for k, v := range m {
			out[a][k] = v
		}
	}
	return out, nil
}

func (r *Repo) ListOrgProjectPolicies(_ context.Context, orgID string) ([]agent.ProjectPolicy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []agent.ProjectPolicy{}
	for id, m := range r.projPolicy {
		if r.Projects[id].Org != orgID || len(m) == 0 {
			continue
		}
		p := agent.ProjectPolicy{ProjectID: id, Name: r.Projects[id].Name, Cells: []agent.PolicyCell{}}
		for a, cells := range m {
			for c, d := range cells {
				p.Cells = append(p.Cells, agent.PolicyCell{Action: a, Class: c, Decision: d})
			}
		}
		out = append(out, p)
	}
	return out, nil
}

func (r *Repo) SetProjectPolicy(_ context.Context, id, _ string, cells []agent.PolicyCell) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.projPolicy[id] == nil {
		r.projPolicy[id] = map[string]map[string]string{}
	}
	for _, c := range cells {
		if r.projPolicy[id][c.Action] == nil {
			r.projPolicy[id][c.Action] = map[string]string{}
		}
		if c.Decision == "" {
			delete(r.projPolicy[id][c.Action], c.Class)
			if len(r.projPolicy[id][c.Action]) == 0 {
				delete(r.projPolicy[id], c.Action)
			}
			continue
		}
		r.projPolicy[id][c.Action][c.Class] = c.Decision
	}
	return nil
}

func (r *Repo) ClaimRun(_ context.Context, id, connectionID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.approvals[id]
	if !ok || a.ConnectionID != connectionID || a.State != "approved" || a.ExecutedAt != nil {
		return false, nil
	}
	now := time.Now()
	a.ExecutedAt = &now
	return true, nil
}

func (r *Repo) SetResult(_ context.Context, id string, res agent.Result, deploymentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a, ok := r.approvals[id]; ok {
		a.Result = &res
	}
	if deploymentID != "" {
		r.Deployments[id] = deploymentID
	}
	return nil
}

// Released are the approvals whose rollback was given back.
func (r *Repo) ReleaseRollback(_ context.Context, approvalID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Released = append(r.Released, approvalID)
	return nil
}

func (r *Repo) ClaimRollback(_ context.Context, connectionID, serviceID, rollbackTo string) (string, string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := connectionID + "|" + serviceID + "|" + rollbackTo
	v, ok := r.Rollbacks[k]
	if !ok {
		return "", "", false, nil
	}
	delete(r.Rollbacks, k)
	return v[0], v[1], true, nil
}

func (r *Repo) SetOrgPaused(_ context.Context, orgID, by string, paused bool, note string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !paused {
		delete(r.orgPaused, orgID)
		return nil
	}
	r.orgPaused[orgID] = &agent.PauseState{At: time.Now(), By: by}
	r.cancel(func(a *approval) bool { return a.org == orgID }, note)
	return nil
}

func (r *Repo) OrgPaused(_ context.Context, orgID string) (*agent.PauseState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.orgPaused[orgID], nil
}

func (r *Repo) SetInstancePaused(_ context.Context, _, byName string, paused bool, note string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !paused {
		r.instPaused = nil
		return nil
	}
	r.instPaused = &agent.PauseState{At: time.Now(), By: byName}
	r.cancel(func(*approval) bool { return true }, note)
	return nil
}

func (r *Repo) InstancePaused(context.Context) (*agent.PauseState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.instPaused, nil
}

func (r *Repo) Clean(context.Context, time.Time) error { return nil }
