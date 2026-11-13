package agent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/resources"
	"github.com/Nickpap21/toft/backend/pkg/actor"
	"github.com/google/uuid"
	"golang.org/x/time/rate"
)

// Check is the agent policy, for the use cases that act (D-100, D-102):
// nil for a request no agent makes, or one the policy allows; otherwise a
// *actor.GateError saying it is refused or waits for a person. An approved
// request's run (actor.GrantFrom) passes once. The use case calls it after
// its workspace and role checks and before it acts, so the REST API with an
// agent's token meets the policy the MCP tools do.
func (s *Service) Check(ctx context.Context, t actor.Target) error {
	a, ok := actor.AgentFrom(ctx)
	if !ok {
		return nil
	}
	info, err := s.target(ctx, t)
	if err != nil {
		return err
	}
	d, level, err := s.decide(ctx, a.OrgID, info.ProjectID, t.Action, info.Class)
	if err != nil {
		return err
	}
	if i, ok := actor.IntentFrom(ctx); ok && i.Seen != nil {
		i.Seen.Class, i.Seen.Level = info.Class, level
	}
	// Never wins, even over an approval given before the policy changed.
	if d == Never {
		return &actor.GateError{Status: http.StatusForbidden, Code: "agent_policy_never",
			Message: fmt.Sprintf("%s never lets agents %s %s", ruleOf(level), verb(t.Action), info.Label)}
	}
	if grant := actor.GrantFrom(ctx); grant != "" {
		claimed, err := s.repo.ClaimRun(ctx, grant, a.ConnectionID)
		if err != nil {
			return err
		}
		if claimed {
			return nil
		}
		return &actor.GateError{Status: http.StatusConflict, Code: "approval_used", Message: "this approval was already used"}
	}
	if !s.allowTool(a.ConnectionID, t.Tool) {
		return &actor.GateError{Status: http.StatusTooManyRequests, Code: "rate_limited",
			Message: fmt.Sprintf("this agent called %s %d times in the last hour; wait before calling it again", t.Tool, ToolLimits[t.Tool])}
	}
	if d == Allow {
		return nil
	}
	if covered, err := s.coveredRollback(ctx, a, t); err != nil || covered {
		return err
	}
	return s.ask(ctx, a, t, info)
}

func ruleOf(level string) string {
	switch level {
	case LevelProject:
		return "the project's agent rule"
	case LevelWorkspace:
		return "the workspace's agent policy"
	}
	return "the agent policy"
}

func verb(action string) string {
	switch action {
	case ActionDeploy:
		return "deploy or roll back"
	case ActionCode:
		return "push or open pull requests in"
	case ActionIssues:
		return "open issues or comment in"
	case ActionVariables:
		return "change variables of"
	case ActionSecrets:
		return "set secrets of"
	case ActionRunbooks:
		return "run runbook steps in"
	}
	return action
}

// target reads what the call acts on, with its class: a preview's, a
// protected environment's or another's; a database or cache counts as
// protected outside previews (D-101); code and issues touch no environment.
func (s *Service) target(ctx context.Context, t actor.Target) (*ServiceInfo, error) {
	var info *ServiceInfo
	var err error
	switch {
	case t.ServiceID != "":
		info, err = s.repo.ServiceInfo(ctx, t.ServiceID)
	case t.EnvironmentID != "":
		info, err = s.repo.EnvironmentInfo(ctx, t.EnvironmentID)
	case t.ProjectID != "":
		var name string
		name, _, err = s.repo.ProjectInfo(ctx, t.ProjectID)
		info = &ServiceInfo{ProjectID: t.ProjectID, Class: ClassProtected, Label: name}
	default:
		return nil, errors.New("agent policy: a target names a service, an environment or a project")
	}
	if err != nil {
		return nil, err
	}
	if t.Class != "" {
		info.Class = t.Class
	}
	if info.Data && info.Class != ClassPreview {
		info.Class = ClassProtected
	}
	if len(ActionClasses[t.Action]) == 1 {
		info.Class = ActionClasses[t.Action][0]
	}
	return info, nil
}

// dataService says whether a service is a database or cache (D-101,
// D-102): by its kind, or for a compose stack, any service of its inline
// file, or a file in a repository, which can't be read here.
func dataService(ctx context.Context, addonSlug, image, sourceType, composeRepo, compose string) bool {
	data := func(k resources.Kind) bool { return k == resources.KindDatabase || k == resources.KindCache }
	if sourceType != "docker-compose" {
		return data(resources.KindOf(addonSlug, image))
	}
	if composeRepo != "" || strings.TrimSpace(compose) == "" {
		return true
	}
	p, vs, err := composespec.Load(ctx, composespec.Source{Root: "/nonexistent/toft-agent", Content: compose, ProjectName: "p"})
	if err != nil || len(vs) > 0 || p == nil {
		return true
	}
	for _, svc := range p.Services {
		if data(resources.KindOf("", svc.Image)) {
			return true
		}
	}
	return false
}

// decide reads a cell: the project's own, else the workspace's, else the
// default (D-101).
func (s *Service) decide(ctx context.Context, orgID, projectID, action, class string) (string, string, error) {
	if projectID != "" {
		own, err := s.repo.ListProjectPolicy(ctx, projectID)
		if err != nil {
			return "", "", err
		}
		if d, ok := own[action][class]; ok {
			return d, LevelProject, nil
		}
	}
	set, err := s.repo.ListPolicy(ctx, orgID)
	if err != nil {
		return "", "", err
	}
	if d, ok := set[action][class]; ok {
		return d, LevelWorkspace, nil
	}
	if d, ok := DefaultPolicy[action][class]; ok {
		return d, LevelDefault, nil
	}
	return Never, LevelDefault, nil
}

func (s *Service) ask(ctx context.Context, a actor.Agent, t actor.Target, info *ServiceInfo) error {
	intent, ok := actor.IntentFrom(ctx)
	if !ok {
		return &actor.GateError{Status: http.StatusForbidden, Code: "approval_required",
			Message: fmt.Sprintf("agents ask a person before they %s %s: call Toft's MCP tool, with a reason", verb(t.Action), info.Label)}
	}
	if strings.TrimSpace(intent.Reason) == "" {
		return &actor.GateError{Status: http.StatusUnprocessableEntity, Code: "reason_required",
			Message: fmt.Sprintf("a person approves this first (%s): give a reason, and a plan, for them to read", info.Label)}
	}
	req := Request{Method: intent.Method, Path: intent.Path, Body: intent.Body}
	raw, _ := json.Marshal(struct {
		T string
		R Request
	}{intent.Tool, req})
	hash := s.requestHash(raw)
	// set_secret's value never rests in the request (D-102): sealed apart,
	// encrypted with the panel's key, until the request is over.
	// The ID is made here, so the value is sealed for it (D-126).
	approvalID := uuid.NewString()
	sealed := ""
	if t.Action == ActionSecrets {
		value, _ := req.Body["value"].(string)
		if s.keys == nil {
			return errors.New("agent policy: no encryption key to keep a secret's value with its request")
		}
		enc, err := s.keys.Seal(sealedAD(approvalID), value)
		if err != nil {
			return fmt.Errorf("seal the value: %w", err)
		}
		sealed = enc
		req.Body = maps.Clone(req.Body)
		delete(req.Body, "value")
	}
	if id, err := s.repo.FindOpenApproval(ctx, a.ConnectionID, hash); err == nil {
		return pending(id)
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	if !s.allowAsk(a.ConnectionID) {
		return &actor.GateError{Status: http.StatusTooManyRequests, Code: "too_many_approvals",
			Message: "this agent asked for 10 approvals in the last hour; wait before asking again"}
	}
	id, err := s.repo.InsertApproval(ctx, NewApproval{ID: approvalID, OrgID: a.OrgID, ConnectionID: a.ConnectionID, UserID: a.UserID,
		Action: t.Action, Tool: intent.Tool, ServiceID: info.ID, EnvironmentID: info.EnvironmentID, ProjectID: info.ProjectID,
		Target: info.Label, Request: req, Sealed: sealed, RequestHash: hash, Reason: clip(intent.Reason, 2000),
		Plan: clip(intent.Plan, 4000), PlanSteps: planSteps(intent.PlanSteps), ExpiresAt: time.Now().Add(ApprovalTTL)})
	if err != nil {
		return err
	}
	return pending(id)
}

// requestHash finds the same call again. Keyed with the instance's HMAC
// key, so a secret's value in it can't be guessed from the hash (D-102,
// D-126).
func (s *Service) requestHash(raw []byte) string {
	m := hmac.New(sha256.New, s.hashKey)
	m.Write(raw)
	return hex.EncodeToString(m.Sum(nil))
}

// allowTool counts a connection's calls of an act tool (ToolLimits).
func (s *Service) allowTool(connectionID, tool string) bool {
	n, ok := ToolLimits[tool]
	if !ok {
		return true
	}
	s.askMu.Lock()
	defer s.askMu.Unlock()
	key := connectionID + "\x00" + tool
	l, ok := s.tools[key]
	if !ok {
		// ponytail: per connection and tool, never evicted, as allowAsk.
		l = rate.NewLimiter(rate.Every(time.Hour/time.Duration(n)), n)
		s.tools[key] = l
	}
	return l.Allow()
}
