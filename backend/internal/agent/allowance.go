package agent

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Nickpap21/toft/backend/pkg/actor"
)

// What a deploy's approval also allows, and the plan's marks (D-146).

// Plan steps are cut to these, the tool input's limits.
const (
	maxPlanSteps    = 20
	maxPlanStepText = 500
)

// WithToolActions tells the service each MCP tool's policy row ("" for a
// read), from the API spec's x-toft-tools, for the plan's marks.
func (s *Service) WithToolActions(m map[string]string) { s.toolActions = m }

// planSteps keeps the steps an agent gave, within the input's limits; a
// service_id that isn't a UUID is dropped, so the step falls back to the
// call's service.
func planSteps(in []actor.PlanStep) []actor.PlanStep {
	out := make([]actor.PlanStep, 0, min(len(in), maxPlanSteps))
	for _, p := range in {
		if len(out) == maxPlanSteps {
			break
		}
		text := strings.TrimSpace(p.Text)
		if text == "" {
			continue
		}
		if utf8.RuneCountInString(text) > maxPlanStepText {
			text = string([]rune(text)[:maxPlanStepText])
		}
		step := actor.PlanStep{Text: text, Tool: clip(strings.TrimSpace(p.Tool), 64)}
		if _, err := uuid.Parse(p.ServiceID); err == nil {
			step.ServiceID = p.ServiceID
		}
		out = append(out, step)
	}
	return out
}

// explain fills what a reader of the approval sees beyond the row: the
// plan's marks (worked out now, so a policy change shows) and the rollback
// the approval allows.
func (s *Service) explain(ctx context.Context, orgID string, a *Approval) {
	a.Rollback = rollbackOf(a, time.Now())
	if a.PlanSteps == nil {
		a.PlanSteps = []PlanStep{}
	}
	m := marker{s: s, ctx: ctx, orgID: orgID, a: a, seen: map[string]string{}}
	for i, p := range a.PlanSteps {
		step := actor.PlanStep{Text: p.Text, Tool: p.Tool}
		if p.ServiceID != nil {
			step.ServiceID = *p.ServiceID
		}
		a.PlanSteps[i].Mark = nil
		if mk := m.mark(step); mk != "" {
			a.PlanSteps[i].Mark = &mk
		}
	}
}

// rollbackOf is the rollback a deploy's approval covers: nil for another
// tool, a first deploy (nothing live to go back to), a request that didn't
// run, or a run whose deploy failed or never started. While it waits it
// names what is live now.
func rollbackOf(a *Approval, now time.Time) *RollbackAllowance {
	f := a.facts
	if a.Tool != "deploy" || f.to == "" || (a.State != "waiting" && a.State != "approved") {
		return nil
	}
	if a.State == "approved" && a.Result != nil && (f.depStatus == "" || f.depStatus == "failed" || f.depStatus == "cancelled") {
		return nil
	}
	r := &RollbackAllowance{ToDeploymentID: f.to, ToCommit: f.commit, Commit: f.depCommit, UsedAt: f.usedAt}
	if a.State == "waiting" {
		return r
	}
	// It went live if it ran (running, or stopped once replaced).
	if f.depFinished != nil && (f.depStatus == "running" || f.depStatus == "stopped") {
		until := f.depFinished.Add(RollbackWindow)
		r.LiveAt, r.Until = f.depFinished, &until
	}
	r.Active = f.usedAt == nil && f.depLive && f.depStatus == "running" && r.Until != nil && now.Before(*r.Until) &&
		(f.deciderRole == "owner" || f.deciderRole == "developer")
	return r
}

// marker marks one approval's plan steps, reading each target's policy
// once.
type marker struct {
	s     *Service
	ctx   context.Context
	orgID string
	a     *Approval
	// this and covered are given once: a second deploy or rollback in the
	// plan asks again.
	this, covered bool
	seen          map[string]string
}

// mark is what a step needs by the policy now: this very request, the
// rollback this approval covers, a person again, or refused; "" for a step
// naming no tool, a read, an Allow cell, or a service outside the request's
// project (its rules aren't this reader's to see).
func (m *marker) mark(p actor.PlanStep) string {
	a := m.a
	action, ok := m.s.toolActions[p.Tool]
	if !ok || action == "" {
		return ""
	}
	own := ""
	if a.ServiceID != nil {
		own = *a.ServiceID
	}
	svc := p.ServiceID
	if svc == "" {
		svc = own
	}
	if svc == own {
		if p.Tool == a.Tool && !m.this {
			m.this = true
			return MarkThisRequest
		}
		rb := a.Rollback
		if own != "" && a.Tool == "deploy" && p.Tool == "rollback" && !m.covered &&
			rb != nil && rb.UsedAt == nil && (rb.Until == nil || rb.Active) {
			m.covered = true
			return MarkCovered
		}
	}
	key := action + "|" + svc
	if mk, ok := m.seen[key]; ok {
		return mk
	}
	mk := m.decide(action, svc)
	m.seen[key] = mk
	return mk
}

func (m *marker) decide(action, svc string) string {
	a := m.a
	if a.ProjectID == nil {
		return ""
	}
	t := actor.Target{Action: action, ServiceID: svc}
	if svc == "" {
		t = actor.Target{Action: action, ProjectID: *a.ProjectID}
	}
	info, err := m.s.target(m.ctx, t)
	if err != nil || info.ProjectID != *a.ProjectID {
		return ""
	}
	d, _, err := m.s.decide(m.ctx, m.orgID, info.ProjectID, action, info.Class)
	if err != nil {
		return ""
	}
	switch d {
	case Ask:
		return MarkAskAgain
	case Never:
		return MarkRefused
	}
	return ""
}

// ReleaseRollback gives back a covered rollback whose request failed before
// it ran (a removed image, a server away), so the agent can try again
// without a person.
func (s *Service) ReleaseRollback(ctx context.Context, approvalID string) {
	if err := s.repo.ReleaseRollback(ctx, approvalID); err != nil {
		s.log.Warn("covered rollback not given back", zap.Error(err), zap.String("approval", approvalID))
	}
}

// coveredRollback uses the rollback an approval of this agent's deploy
// covers, if one does now (D-146), and says so where the call is recorded:
// the MCP tool's audit entry, or over REST an entry of its own.
func (s *Service) coveredRollback(ctx context.Context, a actor.Agent, t actor.Target) (bool, error) {
	if t.Action != ActionDeploy || t.ServiceID == "" || t.DeploymentID == "" {
		return false, nil
	}
	id, approver, ok, err := s.repo.ClaimRollback(ctx, a.ConnectionID, t.ServiceID, t.DeploymentID)
	if err != nil || !ok {
		return false, err
	}
	if c := actor.ClaimsFrom(ctx); c != nil {
		c.Rollback = id
	}
	if i, has := actor.IntentFrom(ctx); has && i.Seen != nil {
		i.Seen.CoveredBy, i.Seen.ApprovedBy = id, approver
		return true, nil
	}
	s.audit.WriteAuditLogDetail(context.WithoutCancel(ctx), a.OrgID, a.UserID, "agent.call", "service:"+t.ServiceID,
		map[string]any{"tool": "rollback", "outcome": "allowed", "covered_by": id, "approved_by": approver,
			"args": map[string]any{"deployment_id": t.DeploymentID}})
	return true, nil
}
