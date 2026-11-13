package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/actor"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

// Roles says what a person is in a workspace ("" when not a member).
// Satisfied by org.Service.
type Roles interface {
	RoleIn(ctx context.Context, userID, orgID string) (string, error)
}

// Operators says who runs the instance. Satisfied by instance.Service.
type Operators interface {
	IsOperator(ctx context.Context, userID string) (bool, error)
	WriteAudit(ctx context.Context, actorID, action string, detail map[string]any)
}

// Auditor writes the workspace's audit log. Satisfied by org.Service; the
// agent or key acting comes from the context (D-100).
type Auditor interface {
	WriteAuditLogDetail(ctx context.Context, orgID, actorID, action, resource string, detail map[string]any)
}

// Dispatcher runs an API operation in-process with a token, as the MCP
// tools do. Approve runs an approved request through it.
type Dispatcher interface {
	Do(ctx context.Context, token, method, path string, body map[string]any) (int, map[string]any, error)
}

// Service is agents' use cases.
type Service struct {
	repo      Repository
	roles     Roles
	operators Operators
	audit     Auditor
	dispatch  Dispatcher
	fetch     metadataFetcher
	log       *zap.Logger

	// keys seal set_secret's value while it waits; hashKey keys the hash
	// that finds the same request again (D-102, D-126).
	keys    *crypto.Keys
	hashKey []byte

	// Approvals per connection: 10 an hour, so Needs you can't be flooded;
	// tools: each act tool's calls an hour per connection (ToolLimits).
	askMu  sync.Mutex
	askers map[string]*rate.Limiter
	tools  map[string]*rate.Limiter

	// runs are approved requests running after Approve answered.
	runs sync.WaitGroup

	// toolActions is each MCP tool's policy row, for the plan's marks.
	toolActions map[string]string
}

func NewService(repo Repository, roles Roles, operators Operators, audit Auditor, log *zap.Logger) *Service {
	return &Service{repo: repo, roles: roles, operators: operators, audit: audit, fetch: fetchClientMetadata,
		log: log, askers: map[string]*rate.Limiter{}, tools: map[string]*rate.Limiter{}}
}

// WithDispatcher lets Approve run what it approved.
func (s *Service) WithDispatcher(d Dispatcher) { s.dispatch = d }

// WithKeys lets set_secret wait for a person: its value is kept sealed
// until the request is over (D-102). hashKey is the instance's HMAC key.
func (s *Service) WithKeys(keys *crypto.Keys, hashKey []byte) { s.keys, s.hashKey = keys, hashKey }

// sealedAD is where an approval's set_secret value is sealed for (D-126).
func sealedAD(approvalID string) string {
	if id, err := uuid.Parse(approvalID); err == nil {
		approvalID = id.String()
	}
	return crypto.AD("agent_approvals.sealed", approvalID)
}

// WaitRuns waits for approved requests still running, for tests and
// shutdown.
func (s *Service) WaitRuns() { s.runs.Wait() }

// ─── Tokens ──────────────────────────────────────────────────────────────────

func newSecret(prefix string) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// ValidateAgentToken checks an access token for the auth middleware.
func (s *Service) ValidateAgentToken(ctx context.Context, raw string) (middleware.AgentIdentity, error) {
	live, err := s.repo.ValidateAccess(ctx, hashToken(raw))
	if err != nil {
		return middleware.AgentIdentity{}, err
	}
	id := middleware.AgentIdentity{ConnectionID: live.ConnectionID, Name: live.Name, UserID: live.UserID, OrgID: live.OrgID,
		AllProjects: live.AllProjects, ProjectIDs: live.ProjectIDs, Scopes: live.Scopes}
	if live.OrgPaused {
		id.Paused = "workspace"
	} else if p, err := s.repo.InstancePaused(ctx); err != nil {
		return middleware.AgentIdentity{}, err
	} else if p != nil {
		id.Paused = "instance"
	}
	return id, nil
}

// Touch records a connection's last call.
func (s *Service) Touch(ctx context.Context, connectionID, tool string) {
	if err := s.repo.Touch(ctx, connectionID, tool); err != nil {
		s.log.Warn("agent connection not touched", zap.Error(err))
	}
}

// RunCleaner removes expired tokens, old codes, unused registrations and
// the sealed values of requests that are over, every 5 minutes until ctx
// ends (D-102).
func (s *Service) RunCleaner(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		if err := s.repo.Clean(ctx, time.Now()); err != nil && ctx.Err() == nil {
			s.log.Warn("agent clean-up failed", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ─── Connections ─────────────────────────────────────────────────────────────

// ListConnections lists the workspace's live connections: every one for an
// owner, the caller's own for anyone else.
func (s *Service) ListConnections(ctx context.Context, orgID, userID, role string) ([]Connection, error) {
	only := userID
	if role == "owner" {
		only = ""
	}
	return s.repo.ListConnections(ctx, orgID, only)
}

// Revoke ends a connection: its owner's or, for an owner, anyone's in the
// workspace. Its tokens stop at once and its waiting approvals are turned
// down.
func (s *Service) Revoke(ctx context.Context, orgID, userID, role, connectionID string) error {
	c, err := s.repo.GetConnection(ctx, connectionID)
	if err != nil {
		return err
	}
	if c.OrgID != orgID || c.RevokedAt != nil {
		return ErrNotFound
	}
	if c.UserID != userID && role != "owner" {
		return ErrForbidden
	}
	if _, err := s.repo.RevokeConnection(ctx, connectionID, userID, "The agent's connection was revoked, so nothing was done."); err != nil {
		return err
	}
	s.audit.WriteAuditLogDetail(ctx, orgID, userID, "agent.revoked", "agent:"+connectionID, map[string]any{"name": c.Name})
	return nil
}

// ─── Policy ──────────────────────────────────────────────────────────────────

func (s *Service) decisions(ctx context.Context, orgID string) (map[string]map[string]string, error) {
	set, err := s.repo.ListPolicy(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]string{}
	for _, a := range PolicyActions {
		out[a] = map[string]string{}
		for _, c := range ActionClasses[a] {
			out[a][c] = DefaultPolicy[a][c]
			if d, ok := set[a][c]; ok {
				out[a][c] = d
			}
		}
	}
	return out, nil
}

// Policy returns the workspace's policy, every cell filled, and the
// projects with cells of their own.
func (s *Service) Policy(ctx context.Context, orgID string) (*Policy, error) {
	d, err := s.decisions(ctx, orgID)
	if err != nil {
		return nil, err
	}
	p := &Policy{Cells: make([]PolicyCell, 0, len(PolicyActions)*len(Classes))}
	for _, a := range PolicyActions {
		for _, c := range ActionClasses[a] {
			p.Cells = append(p.Cells, PolicyCell{Action: a, Class: c, Decision: d[a][c], Default: DefaultPolicy[a][c]})
		}
	}
	if p.Projects, err = s.repo.ListOrgProjectPolicies(ctx, orgID); err != nil {
		return nil, err
	}
	return p, nil
}

var looseness = map[string]int{Never: 0, Ask: 1, Allow: 2}

// ErrReauthRequired: loosening the policy asks for the password.
var ErrReauthRequired = errors.New("reauth required")

// ErrInvalid is input the API refuses with 400; its message says why.
type ErrInvalid struct{ Msg string }

func (e *ErrInvalid) Error() string { return e.Msg }

// SetPolicy changes cells of the policy, for an owner. A cell that
// loosens (Never → Ask → Allow) needs reauthed.
func (s *Service) SetPolicy(ctx context.Context, orgID, userID, role string, cells []PolicyCell, reauthed bool) (*Policy, error) {
	if role != "owner" {
		return nil, ErrForbidden
	}
	cur, err := s.decisions(ctx, orgID)
	if err != nil {
		return nil, err
	}
	changed := make([]PolicyCell, 0, len(cells))
	detail := make([]string, 0, len(cells))
	for _, c := range cells {
		if _, ok := cur[c.Action]; !ok || !slices.Contains(ActionClasses[c.Action], c.Class) {
			return nil, &ErrInvalid{Msg: fmt.Sprintf("no policy cell %s / %s", c.Action, c.Class)}
		}
		if _, ok := looseness[c.Decision]; !ok {
			return nil, &ErrInvalid{Msg: "decision must be allow, ask or never"}
		}
		was := cur[c.Action][c.Class]
		if was == c.Decision {
			continue
		}
		if looseness[c.Decision] > looseness[was] && !reauthed {
			return nil, ErrReauthRequired
		}
		changed = append(changed, c)
		detail = append(detail, fmt.Sprintf("%s, %s: %s → %s", c.Action, c.Class, was, c.Decision))
	}
	if len(changed) > 0 {
		if err := s.repo.SetPolicy(ctx, orgID, userID, changed); err != nil {
			return nil, err
		}
		s.audit.WriteAuditLogDetail(ctx, orgID, userID, "agent.policy_changed", "org:"+orgID, map[string]any{"changes": detail})
	}
	return s.Policy(ctx, orgID)
}

// SetProjectPolicy changes a project's own cells (D-101), for an owner. A
// cell's Decision "" drops the project's rule, so the workspace's applies.
// A change that loosens what applies to the project, a dropped rule
// included, needs reauthed.
func (s *Service) SetProjectPolicy(ctx context.Context, orgID, userID, role, projectID string, cells []PolicyCell, reauthed bool) (*Policy, error) {
	if role != "owner" {
		return nil, ErrForbidden
	}
	name, org, err := s.repo.ProjectInfo(ctx, projectID)
	if errors.Is(err, ErrNotFound) || err == nil && org != orgID {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	ws, err := s.decisions(ctx, orgID)
	if err != nil {
		return nil, err
	}
	own, err := s.repo.ListProjectPolicy(ctx, projectID)
	if err != nil {
		return nil, err
	}
	label := func(d string) string {
		if d == "" {
			return "as the workspace"
		}
		return d
	}
	changed := make([]PolicyCell, 0, len(cells))
	detail := make([]string, 0, len(cells))
	for _, c := range cells {
		if _, ok := ws[c.Action]; !ok || !slices.Contains(ActionClasses[c.Action], c.Class) {
			return nil, &ErrInvalid{Msg: fmt.Sprintf("no policy cell %s / %s", c.Action, c.Class)}
		}
		if _, ok := looseness[c.Decision]; !ok && c.Decision != "" {
			return nil, &ErrInvalid{Msg: "decision must be allow, ask, never, or empty to follow the workspace"}
		}
		was := own[c.Action][c.Class]
		if was == c.Decision {
			continue
		}
		before, after := ws[c.Action][c.Class], ws[c.Action][c.Class]
		if was != "" {
			before = was
		}
		if c.Decision != "" {
			after = c.Decision
		}
		if looseness[after] > looseness[before] && !reauthed {
			return nil, ErrReauthRequired
		}
		changed = append(changed, c)
		detail = append(detail, fmt.Sprintf("%s, %s: %s → %s", c.Action, c.Class, label(was), label(c.Decision)))
	}
	if len(changed) > 0 {
		if err := s.repo.SetProjectPolicy(ctx, projectID, userID, changed); err != nil {
			return nil, err
		}
		s.audit.WriteAuditLogDetail(ctx, orgID, userID, "agent.project_policy_changed", "project:"+projectID,
			map[string]any{"project": name, "changes": detail})
	}
	return s.Policy(ctx, orgID)
}

// ResetProjectPolicy drops every cell of a project's own: it follows the
// workspace again.
func (s *Service) ResetProjectPolicy(ctx context.Context, orgID, userID, role, projectID string, reauthed bool) (*Policy, error) {
	own, err := s.repo.ListProjectPolicy(ctx, projectID)
	if err != nil {
		return nil, err
	}
	cells := make([]PolicyCell, 0)
	for a, m := range own {
		for c := range m {
			cells = append(cells, PolicyCell{Action: a, Class: c})
		}
	}
	return s.SetProjectPolicy(ctx, orgID, userID, role, projectID, cells, reauthed)
}

func pending(id string) error {
	return &actor.GateError{Status: http.StatusAccepted, Code: "approval_pending",
		Message: "a person approves this first; it waits in Needs you", ApprovalID: id}
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func (s *Service) allowAsk(connectionID string) bool {
	s.askMu.Lock()
	defer s.askMu.Unlock()
	l, ok := s.askers[connectionID]
	if !ok {
		// ponytail: one limiter per connection, never evicted; connections
		// are few and live 90 days.
		l = rate.NewLimiter(rate.Every(6*time.Minute), 10)
		s.askers[connectionID] = l
	}
	return l.Allow()
}

// ─── Approvals ───────────────────────────────────────────────────────────────

// state reads a waiting approval past its time as expired.
func state(a *Approval) {
	if a.State == "waiting" && time.Now().After(a.ExpiresAt) {
		a.State = "expired"
	}
}

// Approval returns one of the workspace's approvals.
func (s *Service) Approval(ctx context.Context, orgID, id string) (*Approval, error) {
	a, org, err := s.repo.GetApproval(ctx, id)
	if err != nil {
		return nil, err
	}
	if org != orgID {
		return nil, ErrNotFound
	}
	state(a)
	s.classOf(ctx, a)
	s.explain(ctx, orgID, a)
	return a, nil
}

// classOf fills the class the policy reads for the approval's target, for
// the request page (D-104); "" when the target is gone.
func (s *Service) classOf(ctx context.Context, a *Approval) {
	t := actor.Target{Action: a.Action}
	switch {
	case a.ServiceID != nil && *a.ServiceID != "":
		t.ServiceID = *a.ServiceID
	case a.EnvironmentID != nil && *a.EnvironmentID != "":
		t.EnvironmentID = *a.EnvironmentID
	case a.ProjectID != nil && *a.ProjectID != "":
		t.ProjectID = *a.ProjectID
	default:
		return
	}
	if a.Tool == "create_preview" {
		t.Class = ClassPreview
	}
	if info, err := s.target(ctx, t); err == nil {
		a.EnvClass = info.Class
	}
}

// AgentApproval returns an approval to the connection that asked for it,
// for its waiting tool call.
func (s *Service) AgentApproval(ctx context.Context, connectionID, id string) (*Approval, error) {
	a, orgID, err := s.repo.GetApproval(ctx, id)
	if err != nil {
		return nil, err
	}
	if a.ConnectionID != connectionID {
		return nil, ErrNotFound
	}
	state(a)
	s.explain(ctx, orgID, a)
	return a, nil
}

// Waiting lists what waits for a person in the workspace.
func (s *Service) Waiting(ctx context.Context, orgID string) ([]Waiting, error) {
	return s.repo.ListWaiting(ctx, orgID)
}

// Decide approves or denies a waiting request, for an owner or developer
// with a session (the handler refuses agents and keys). Approve starts the
// request, as the agent, and answers without waiting for it (D-102: a
// runbook step can take minutes); the run records what it answered.
func (s *Service) Decide(ctx context.Context, orgID, userID, role, id string, approve bool, note, commit string) (*Approval, error) {
	if role != "owner" && role != "developer" {
		return nil, ErrForbidden
	}
	a, err := s.Approval(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if a.State != "waiting" {
		return nil, ErrConflict
	}
	next := "denied"
	if approve {
		next = "approved"
	}
	// A deploy that names no commit builds its branch's head when it runs:
	// pin the one the person was shown, or a push during the wait ships unseen.
	named, _ := a.Request.Body["commit_sha"].(string)
	pin := approve && a.Tool == "deploy" && commit != "" && named == ""
	if pin {
		if _, err := hex.DecodeString(commit); err != nil || len(commit) != 40 {
			return nil, &ErrInvalid{Msg: "commit must be a full 40-character commit SHA"}
		}
	}
	if err := s.repo.DecideApproval(ctx, id, orgID, next, userID, clip(note, 2000)); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrConflict
		}
		return nil, err
	}
	detail := map[string]any{"agent": a.AgentName, "for": a.UserName, "tool": a.Tool, "target": a.Target, "note": clip(note, 200)}
	if approve && a.Rollback != nil {
		// What approving also allows (D-146), for the audit log to say.
		detail["also_allows"] = map[string]any{"rollback_to": a.Rollback.ToDeploymentID, "rollback_commit": a.Rollback.ToCommit,
			"window": "30 minutes after the deploy goes live, once"}
	}
	if pin {
		detail["commit"] = commit
	}
	s.audit.WriteAuditLogDetail(ctx, orgID, userID, "agent."+next, "agent_approval:"+id, detail)
	if approve {
		body := a.Request.Body
		if pin {
			// A copy: the stored request stays what the agent asked for.
			body = make(map[string]any, len(a.Request.Body)+1)
			maps.Copy(body, a.Request.Body)
			body["commit_sha"] = commit
		}
		if a.Action == ActionSecrets {
			// The value waits sealed (D-102); the run puts it back, and
			// nothing else ever sees it.
			sealed, err := s.repo.Sealed(ctx, id)
			if err == nil {
				var value string
				if value, err = s.keys.Open(sealedAD(id), sealed); err == nil {
					body = maps.Clone(body)
					body["value"] = value
				}
			}
			if err != nil {
				s.log.Warn("approved secret's value not read", zap.Error(err), zap.String("approval", id))
				body = nil
			}
		}
		s.runs.Add(1)
		go func() {
			defer s.runs.Done()
			s.run(context.WithoutCancel(ctx), a, body)
		}()
	}
	return s.Approval(ctx, orgID, id)
}

// run runs an approved request as its agent: a token for two minutes, the
// approval as the grant the policy lets through once.
func (s *Service) run(ctx context.Context, a *Approval, body map[string]any) {
	res := Result{Status: http.StatusServiceUnavailable, Body: map[string]any{"error": "the panel couldn't run it"}}
	deploymentID := ""
	defer func() {
		if a.Action == ActionSecrets {
			if err := s.repo.ClearSealed(ctx, a.ID); err != nil {
				s.log.Warn("approved secret's value not deleted; the cleaner deletes it", zap.Error(err), zap.String("approval", a.ID))
			}
		}
		if err := s.repo.SetResult(ctx, a.ID, res, deploymentID); err != nil {
			s.log.Warn("approval result not saved", zap.Error(err), zap.String("approval", a.ID))
		}
	}()
	if s.dispatch == nil || a.Action == ActionSecrets && body == nil {
		return
	}
	raw := newSecret(AccessTokenPrefix)
	if err := s.repo.InsertToken(ctx, hashToken(raw), a.ConnectionID, "access", time.Now().Add(internalTTL)); err != nil {
		s.log.Warn("approval token not issued", zap.Error(err))
		return
	}
	defer func() { _ = s.repo.DeleteToken(context.WithoutCancel(ctx), hashToken(raw)) }()
	runCtx := actor.WithGrant(ctx, a.ID)
	status, out, err := s.dispatch.Do(runCtx, raw, a.Request.Method, a.Request.Path, body)
	if err != nil {
		s.log.Warn("approved request failed", zap.Error(err), zap.String("approval", a.ID))
		return
	}
	res = Result{Status: status, Body: withoutPassword(out)}
	if a.Tool == "deploy" && status == http.StatusAccepted {
		// The deployment it started, whose going live opens the rollback
		// the approval covers (D-146).
		d, _ := out["data"].(map[string]any)
		deploymentID, _ = d["id"].(string)
	}
}

// withoutPassword keeps create_branch's push password out of a stored
// result (D-102): the agent asks for a new one on its branch, without a
// person.
func withoutPassword(body map[string]any) map[string]any {
	d, _ := body["data"].(map[string]any)
	if p, ok := d["push"].(map[string]any); ok {
		p["password"], p["command"] = "", ""
	}
	return body
}

// ─── Pause ───────────────────────────────────────────────────────────────────

// PauseState says where agents are paused.
func (s *Service) PauseState(ctx context.Context, orgID string) (*Pause, error) {
	w, err := s.repo.OrgPaused(ctx, orgID)
	if err != nil {
		return nil, err
	}
	i, err := s.repo.InstancePaused(ctx)
	if err != nil {
		return nil, err
	}
	return &Pause{Workspace: w, Instance: i}, nil
}

// PauseWorkspace pauses or resumes the workspace's agents, for an owner.
// Pausing turns down waiting approvals; running deploys finish.
func (s *Service) PauseWorkspace(ctx context.Context, orgID, userID, userName, role string, paused bool) (*Pause, error) {
	if role != "owner" {
		return nil, ErrForbidden
	}
	note := "Agents were paused in the workspace by " + userName + ", so nothing was done."
	if err := s.repo.SetOrgPaused(ctx, orgID, userID, paused, note); err != nil {
		return nil, err
	}
	action := "agent.resumed"
	if paused {
		action = "agent.paused"
	}
	s.audit.WriteAuditLogDetail(ctx, orgID, userID, action, "org:"+orgID, nil)
	return s.PauseState(ctx, orgID)
}

// PauseInstance pauses or resumes every workspace's agents, for an
// instance operator.
func (s *Service) PauseInstance(ctx context.Context, orgID, userID, userName string, paused bool) (*Pause, error) {
	op, err := s.operators.IsOperator(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !op {
		return nil, ErrForbidden
	}
	note := "Agents were paused on this panel by " + userName + ", so nothing was done."
	if err := s.repo.SetInstancePaused(ctx, userID, userName, paused, note); err != nil {
		return nil, err
	}
	action := "agents.resumed"
	if paused {
		action = "agents.paused"
	}
	s.operators.WriteAudit(ctx, userID, action, nil)
	return s.PauseState(ctx, orgID)
}
