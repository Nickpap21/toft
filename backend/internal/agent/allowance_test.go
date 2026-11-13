package agent_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/agent"
	"github.com/Nickpap21/toft/backend/internal/agent/agenttest"
	"github.com/Nickpap21/toft/backend/pkg/actor"
	"go.uber.org/zap"
)

type recAudit struct {
	mu   sync.Mutex
	rows []map[string]any
}

func (r *recAudit) WriteAuditLogDetail(_ context.Context, _, _, action, _ string, d map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, map[string]any{"action": action, "detail": d})
}

const (
	prodAPI  = "11111111-1111-4111-8111-111111111111"
	otherSvc = "22222222-2222-4222-8222-222222222222"
)

func allowanceSetup(t *testing.T) (*agent.Service, *agenttest.Repo, *recAudit, string) {
	t.Helper()
	repo := agenttest.New()
	repo.OrgProjects["org1"] = []string{"p1", "p2"}
	repo.Projects["p1"] = agenttest.Project{Name: "storefront", Org: "org1"}
	repo.Projects["p2"] = agenttest.Project{Name: "billing-api", Org: "org1"}
	repo.Services[prodAPI] = &agent.ServiceInfo{ID: prodAPI, ProjectID: "p1", Class: agent.ClassProtected, Label: "storefront / api in production"}
	repo.Services[otherSvc] = &agent.ServiceInfo{ID: otherSvc, ProjectID: "p2", Class: agent.ClassProtected, Label: "billing-api / api in production"}
	audit := &recAudit{}
	svc := agent.NewService(repo, roles{"owner": "owner", "dev": "developer", "viewer": "viewer"}, ops{}, audit, zap.NewNop())
	svc.WithToolActions(map[string]string{"deploy": "deploy", "rollback": "deploy", "set_env_var": "variables", "comment": "issues", "get_deploy_logs": ""})
	conn, err := repo.CreateConnection(context.Background(), agent.NewConnection{Code: agent.Code{OrgID: "org1", UserID: "dev", AllProjects: true,
		Scopes: []string{"read", "deploy"}}, Name: "Claude Code"})
	if err != nil {
		t.Fatal(err)
	}
	return svc, repo, audit, conn
}

func rollbackTo(deploymentID string) actor.Target {
	return actor.Target{Action: "deploy", Tool: "rollback", ServiceID: prodAPI, DeploymentID: deploymentID}
}

// An approval of the agent's deploy covers its rollback to what was live,
// once, and says who approved it; Never still wins; another agent, or
// another target, asks a person (D-146).
func TestApprovedDeployCoversItsRollback(t *testing.T) {
	ctx := context.Background()
	svc, repo, audit, conn := allowanceSetup(t)
	repo.Rollbacks[conn+"|"+prodAPI+"|d-old"] = [2]string{"ap1", "admin"}
	seen := &actor.Seen{}
	actx := actor.WithIntent(actor.WithAgent(ctx, actor.Agent{ConnectionID: conn, OrgID: "org1", UserID: "dev"}),
		actor.Intent{Tool: "rollback", Reason: "errors rose", Seen: seen})
	var ge *actor.GateError

	never := []agent.PolicyCell{{Action: "deploy", Class: "protected", Decision: "never"}}
	if _, err := svc.SetPolicy(ctx, "org1", "owner", "owner", never, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.Check(actx, rollbackTo("d-old")); !errors.As(err, &ge) || ge.Code != "agent_policy_never" {
		t.Fatalf("a covered rollback under Never: %v", err)
	}
	if _, ok := repo.Rollbacks[conn+"|"+prodAPI+"|d-old"]; !ok {
		t.Fatal("Never used up the allowance")
	}
	never[0].Decision = "ask"
	if _, err := svc.SetPolicy(ctx, "org1", "owner", "owner", never, true); err != nil {
		t.Fatal(err)
	}

	if err := svc.Check(actx, rollbackTo("d-other")); !errors.As(err, &ge) || ge.Code != "approval_pending" {
		t.Fatalf("a rollback elsewhere asks: %v", err)
	}
	cctx, claims := actor.WithClaims(actx)
	if err := svc.Check(cctx, rollbackTo("d-old")); err != nil {
		t.Fatalf("the covered rollback: %v", err)
	}
	if claims.Rollback != "ap1" {
		t.Fatalf("the request doesn't know what it used: %+v", claims)
	}
	if seen.CoveredBy != "ap1" || seen.ApprovedBy != "admin" {
		t.Fatalf("the audit entry doesn't name the approval and approver: %+v", seen)
	}
	if err := svc.Check(actx, rollbackTo("d-old")); !errors.As(err, &ge) || ge.Code != "approval_pending" {
		t.Fatalf("a second rollback asks a person: %v", err)
	}

	// Over REST (no MCP intent) the gate records it itself.
	repo.Rollbacks[conn+"|"+prodAPI+"|d-old"] = [2]string{"ap2", "sam"}
	rest := actor.WithAgent(ctx, actor.Agent{ConnectionID: conn, OrgID: "org1", UserID: "dev"})
	if err := svc.Check(rest, rollbackTo("d-old")); err != nil {
		t.Fatalf("the covered rollback over REST: %v", err)
	}
	last := audit.rows[len(audit.rows)-1]
	d, _ := last["detail"].(map[string]any)
	if last["action"] != "agent.call" || d["covered_by"] != "ap2" || d["approved_by"] != "sam" {
		t.Fatalf("REST audit entry: %+v", last)
	}
	// A deploy is never a covered rollback.
	if err := svc.Check(rest, dep(prodAPI)); !errors.As(err, &ge) || ge.Code != "approval_required" {
		t.Fatalf("a deploy over REST: %v", err)
	}
}

// Each step that names a tool shows what it needs, by the policy now;
// a step on another project's service shows nothing (D-146).
func TestPlanStepMarks(t *testing.T) {
	ctx := context.Background()
	svc, _, _, conn := allowanceSetup(t)
	steps := []actor.PlanStep{
		{Text: "Deploy a81c0de to production.", Tool: "deploy"},
		{Text: "Watch the error rate.", Tool: "get_deploy_logs"},
		{Text: "Roll back to 9b7d3f0 if errors rise.", Tool: "rollback", ServiceID: prodAPI},
		{Text: "Raise DATABASE_POOL_MAX to 30.", Tool: "set_env_var"},
		{Text: "Say so on STO-118.", Tool: "comment"},
		{Text: "Deploy billing-api too.", Tool: "deploy", ServiceID: otherSvc},
		{Text: "Tell sam."},
		{Text: "   "},
		{Text: "Deploy again once it's quiet.", Tool: "deploy"},
	}
	actx := actor.WithIntent(actor.WithAgent(ctx, actor.Agent{ConnectionID: conn, OrgID: "org1", UserID: "dev"}),
		actor.Intent{Tool: "deploy", Reason: "fixes STO-118", PlanSteps: steps})
	var ge *actor.GateError
	if err := svc.Check(actx, dep(prodAPI)); !errors.As(err, &ge) || ge.Code != "approval_pending" {
		t.Fatalf("Ask: %v", err)
	}
	a, err := svc.Approval(ctx, "org1", ge.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	// Nothing is live in the fake, so the rollback is nobody's to cover: it asks.
	want := []string{agent.MarkThisRequest, "", agent.MarkAskAgain, agent.MarkAskAgain, "", "", "", agent.MarkAskAgain}
	if len(a.PlanSteps) != len(want) {
		t.Fatalf("steps kept: %+v", a.PlanSteps)
	}
	for i, w := range want {
		got := ""
		if a.PlanSteps[i].Mark != nil {
			got = *a.PlanSteps[i].Mark
		}
		if got != w {
			t.Errorf("step %d (%s): mark %q, want %q", i, a.PlanSteps[i].Text, got, w)
		}
	}
	// The marks follow the policy now: variables in production allowed.
	if _, err := svc.SetPolicy(ctx, "org1", "owner", "owner", []agent.PolicyCell{{Action: "variables", Class: "protected", Decision: "allow"}}, true); err != nil {
		t.Fatal(err)
	}
	if a, _ = svc.Approval(ctx, "org1", ge.ApprovalID); a.PlanSteps[3].Mark != nil {
		t.Fatalf("an Allow cell still marked: %v", *a.PlanSteps[3].Mark)
	}
}

type deployDispatcher struct{}

func (deployDispatcher) Do(context.Context, string, string, string, map[string]any) (int, map[string]any, error) {
	return http.StatusAccepted, map[string]any{"data": map[string]any{"id": "dep-new", "status": "pending"}}, nil
}

// An approved deploy's run keeps the deployment it started: its going
// live opens the rollback the approval covers.
func TestApprovedDeployKeepsItsDeployment(t *testing.T) {
	ctx := context.Background()
	svc, repo, _, conn := allowanceSetup(t)
	svc.WithDispatcher(deployDispatcher{})
	actx := actor.WithIntent(actor.WithAgent(ctx, actor.Agent{ConnectionID: conn, OrgID: "org1", UserID: "dev"}),
		actor.Intent{Tool: "deploy", Reason: "fixes it", Method: "POST", Path: "/api/v1/services/" + prodAPI + "/deployments"})
	var ge *actor.GateError
	if err := svc.Check(actx, dep(prodAPI)); !errors.As(err, &ge) {
		t.Fatalf("Ask: %v", err)
	}
	if _, err := svc.Decide(ctx, "org1", "owner", "owner", ge.ApprovalID, true, "", ""); err != nil {
		t.Fatal(err)
	}
	svc.WaitRuns()
	if repo.Deployments[ge.ApprovalID] != "dep-new" {
		t.Fatalf("deployment kept: %q", repo.Deployments[ge.ApprovalID])
	}
}
