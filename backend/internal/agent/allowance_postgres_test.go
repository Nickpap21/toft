package agent_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/agent"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/pkg/actor"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// The database decides when a deploy's approval covers its rollback
// (D-146): the run keeps what was live; the rollback is covered once,
// within 30 minutes of the approved deployment going live, while it is
// still live (not after a failed health check put the old one back), for
// that agent only, and while the approver is still an owner or developer.
func TestApprovalCoversRollbackInPostgres(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	repo := agent.NewPostgresRepository(db)
	svc := agent.NewService(repo, roles{}, ops{}, nopAudit{}, zap.NewNop())
	svc.WithToolActions(map[string]string{"deploy": "deploy", "rollback": "deploy"})
	q := func(query string, args ...any) string {
		t.Helper()
		var id string
		if err := db.QueryRow(ctx, query, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	admin := q(`INSERT INTO users (email, name, password_hash) VALUES ('admin@toft.test', 'Admin', 'x') RETURNING id::text`)
	dev := q(`INSERT INTO users (email, name, password_hash) VALUES ('sam@toft.test', 'sam', 'x') RETURNING id::text`)
	org := q(`INSERT INTO organizations (name) VALUES ('Founders') RETURNING id::text`)
	exec(`INSERT INTO org_members (org_id, user_id, role) VALUES ($1, $2, 'owner'), ($1, $3, 'developer')`, org, admin, dev)
	client := q(`INSERT INTO oauth_clients (client_id, kind, name, redirect_uris) VALUES ('c', 'dcr', 'Claude Code', '{http://localhost/cb}') RETURNING id::text`)
	connection := func() string {
		return q(`INSERT INTO agent_connections (org_id, user_id, client_id, name, all_projects, scopes, expires_at)
			VALUES ($1, $2, $3, 'sam''s laptop', true, '{read,deploy}', NOW() + INTERVAL '1 day') RETURNING id::text`, org, dev, client)
	}
	conn, otherConn := connection(), connection()
	project := q(`INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'storefront') RETURNING id::text`, admin, org)
	env := q(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, project)
	service := q(`INSERT INTO services (scope, environment_id, project_id, name, type, source_type, docker_image)
		VALUES ('environment', $1, $2, 'api', 'web', 'docker-image', 'api:1') RETURNING id::text`, env, project)
	deployment := func(sha, status string, finished time.Duration) string {
		return q(`INSERT INTO deployments (service_id, status, commit_sha, finished_at) VALUES ($1, $2, $3, NOW() - $4::interval) RETURNING id::text`,
			service, status, sha, finished.String())
	}
	old := deployment(strings.Repeat("9b7d3f0", 6)[:40], "running", 3*time.Hour)
	exec(`UPDATE services SET active_deployment_id = $1 WHERE id = $2`, old, service)

	id := uuid.NewString()
	if _, err := repo.InsertApproval(ctx, agent.NewApproval{ID: id, OrgID: org, ConnectionID: conn, UserID: dev, Action: "deploy", Tool: "deploy",
		ServiceID: service, EnvironmentID: env, ProjectID: project, Target: "storefront / api in production",
		Request: agent.Request{Method: "POST", Path: "/api/v1/services/" + service + "/deployments"}, RequestHash: "h", Reason: "fixes STO-118",
		PlanSteps: []actor.PlanStep{{Text: "Deploy a81c0de.", Tool: "deploy"}, {Text: "Roll back to 9b7d3f0 if errors rise.", Tool: "rollback"}},
		ExpiresAt: time.Now().Add(30 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	a, err := svc.Approval(ctx, org, id)
	if err != nil {
		t.Fatal(err)
	}
	if a.Rollback == nil || a.Rollback.ToDeploymentID != old || a.Rollback.ToCommit[:7] != "9b7d3f0" || a.Rollback.Until != nil || a.Rollback.Active ||
		a.Rollback.Commit != "" || a.Rollback.LiveAt != nil {
		t.Fatalf("while it waits, it names what is live now: %+v", a.Rollback)
	}
	if len(a.PlanSteps) != 2 || a.PlanSteps[0].Mark == nil || *a.PlanSteps[0].Mark != agent.MarkThisRequest ||
		a.PlanSteps[1].Mark == nil || *a.PlanSteps[1].Mark != agent.MarkCovered {
		t.Fatalf("the plan's marks: %+v", a.PlanSteps)
	}
	if err := repo.DecideApproval(ctx, id, org, "approved", admin, ""); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.ClaimRun(ctx, id, conn); err != nil || !ok {
		t.Fatalf("claim the run: %v %v", ok, err)
	}
	// The approved deploy builds: its commit is known, it isn't live yet.
	approved := q(`INSERT INTO deployments (service_id, status, commit_sha) VALUES ($1, 'building', $2) RETURNING id::text`,
		service, strings.Repeat("a81c0de", 6)[:40])
	if err := repo.SetResult(ctx, id, agent.Result{Status: http.StatusAccepted}, approved); err != nil {
		t.Fatal(err)
	}
	a, _ = svc.Approval(ctx, org, id)
	if a.Rollback == nil || a.Rollback.Commit[:7] != "a81c0de" || a.Rollback.LiveAt != nil || a.Rollback.Until != nil || a.Rollback.Active {
		t.Fatalf("approved and building: %+v", a.Rollback)
	}
	// It went live 5 minutes ago.
	exec(`UPDATE deployments SET status = 'running', finished_at = NOW() - interval '5 minutes' WHERE id = $1`, approved)
	exec(`UPDATE services SET active_deployment_id = $1 WHERE id = $2`, approved, service)
	a, _ = svc.Approval(ctx, org, id)
	if a.Rollback == nil || a.Rollback.ToDeploymentID != old || !a.Rollback.Active || a.Rollback.Until == nil ||
		time.Until(*a.Rollback.Until) < 24*time.Minute || time.Until(*a.Rollback.Until) > 26*time.Minute ||
		a.Rollback.Commit[:7] != "a81c0de" || a.Rollback.LiveAt == nil || a.Rollback.Until.Sub(*a.Rollback.LiveAt) != agent.RollbackWindow {
		t.Fatalf("approved and live: %+v", a.Rollback)
	}

	claim := func(connectionID, to string) bool {
		t.Helper()
		_, _, ok, err := repo.ClaimRollback(ctx, connectionID, service, to)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if claim(otherConn, old) {
		t.Fatal("another agent's rollback was covered")
	}
	if claim(conn, approved) || claim(conn, "not-a-uuid") {
		t.Fatal("a rollback to another deployment was covered")
	}

	// The approver loses their role: the allowance ends with it.
	exec(`UPDATE org_members SET role = 'viewer' WHERE user_id = $1`, admin)
	if claim(conn, old) {
		t.Fatal("covered after the approver became a viewer")
	}
	if a, _ = svc.Approval(ctx, org, id); a.Rollback.Active {
		t.Fatal("shown active after the approver became a viewer")
	}
	exec(`UPDATE org_members SET role = 'owner' WHERE user_id = $1`, admin)

	// The approved deploy failed its health check and the old one is live
	// again: nothing to roll back from, so nothing is covered.
	exec(`UPDATE deployments SET status = 'failed' WHERE id = $1`, approved)
	exec(`UPDATE services SET active_deployment_id = $1 WHERE id = $2`, old, service)
	if claim(conn, old) {
		t.Fatal("covered after the approved deploy failed and was rolled back")
	}
	if a, _ = svc.Approval(ctx, org, id); a.Rollback != nil {
		t.Fatalf("a deploy that failed still shows an allowance: %+v", a.Rollback)
	}
	exec(`UPDATE deployments SET status = 'running' WHERE id = $1`, approved)
	exec(`UPDATE services SET active_deployment_id = $1 WHERE id = $2`, approved, service)

	// Past the window.
	exec(`UPDATE deployments SET finished_at = NOW() - INTERVAL '31 minutes' WHERE id = $1`, approved)
	if claim(conn, old) {
		t.Fatal("covered 31 minutes after it went live")
	}
	exec(`UPDATE deployments SET finished_at = NOW() - INTERVAL '5 minutes' WHERE id = $1`, approved)

	// Covered once, naming who approved.
	approval, approver, ok, err := repo.ClaimRollback(ctx, conn, service, old)
	if err != nil || !ok || approval != id || approver != "Admin" {
		t.Fatalf("the covered rollback: %q %q %v %v", approval, approver, ok, err)
	}
	if claim(conn, old) {
		t.Fatal("covered twice")
	}
	if a, _ = svc.Approval(ctx, org, id); a.Rollback.UsedAt == nil || a.Rollback.Active {
		t.Fatalf("used: %+v", a.Rollback)
	}
	if a.PlanSteps[1].Mark != nil && *a.PlanSteps[1].Mark == agent.MarkCovered {
		t.Fatal("a used allowance still marks the rollback covered")
	}
	// A covered rollback that failed before it ran is given back.
	if err := repo.ReleaseRollback(ctx, id); err != nil {
		t.Fatal(err)
	}
	if !claim(conn, old) {
		t.Fatal("a released rollback wasn't covered again")
	}

	// The service stopped: nothing is live. The page still reads.
	exec(`UPDATE services SET active_deployment_id = NULL WHERE id = $1`, service)
	if a, err = svc.Approval(ctx, org, id); err != nil || a.Rollback == nil || a.Rollback.Active {
		t.Fatalf("a stopped service: %+v %v", a, err)
	}

	// A first deploy: nothing was live to go back to, nothing is covered,
	// and the page reads while its deployment builds.
	first := uuid.NewString()
	if _, err := repo.InsertApproval(ctx, agent.NewApproval{ID: first, OrgID: org, ConnectionID: conn, UserID: dev, Action: "deploy", Tool: "deploy",
		ServiceID: service, EnvironmentID: env, ProjectID: project, Target: "storefront / api in production",
		Request: agent.Request{Method: "POST", Path: "/x"}, RequestHash: "h2", Reason: "first", ExpiresAt: time.Now().Add(30 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.DecideApproval(ctx, first, org, "approved", admin, ""); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.ClaimRun(ctx, first, conn); err != nil || !ok {
		t.Fatalf("claim the run: %v %v", ok, err)
	}
	building := deployment(strings.Repeat("c58e0a1", 6)[:40], "building", 0)
	if err := repo.SetResult(ctx, first, agent.Result{Status: http.StatusAccepted}, building); err != nil {
		t.Fatal(err)
	}
	if a, err = svc.Approval(ctx, org, first); err != nil || a.Rollback != nil {
		t.Fatalf("a first deploy: %+v %v", a, err)
	}

	// A deploy whose run failed covers nothing, and says so by saying nothing.
	exec(`UPDATE services SET active_deployment_id = $1 WHERE id = $2`, approved, service)
	failedRun := uuid.NewString()
	if _, err := repo.InsertApproval(ctx, agent.NewApproval{ID: failedRun, OrgID: org, ConnectionID: conn, UserID: dev, Action: "deploy", Tool: "deploy",
		ServiceID: service, EnvironmentID: env, ProjectID: project, Target: "storefront / api in production",
		Request: agent.Request{Method: "POST", Path: "/x"}, RequestHash: "h3", Reason: "again", ExpiresAt: time.Now().Add(30 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	_ = repo.DecideApproval(ctx, failedRun, org, "approved", admin, "")
	_, _ = repo.ClaimRun(ctx, failedRun, conn)
	if err := repo.SetResult(ctx, failedRun, agent.Result{Status: http.StatusConflict}, ""); err != nil {
		t.Fatal(err)
	}
	if a, err = svc.Approval(ctx, org, failedRun); err != nil || a.Rollback != nil {
		t.Fatalf("a run that failed: %+v %v", a.Rollback, err)
	}

	// An approved rollback keeps no target: only a deploy's approval covers one.
	rb := uuid.NewString()
	if _, err := repo.InsertApproval(ctx, agent.NewApproval{ID: rb, OrgID: org, ConnectionID: conn, UserID: dev, Action: "deploy", Tool: "rollback",
		ServiceID: service, EnvironmentID: env, ProjectID: project, Target: "storefront / api in production",
		Request: agent.Request{Method: "POST", Path: "/x"}, RequestHash: "h4", Reason: "back", ExpiresAt: time.Now().Add(30 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	_ = repo.DecideApproval(ctx, rb, org, "approved", admin, "")
	_, _ = repo.ClaimRun(ctx, rb, conn)
	var target *string
	if err := db.QueryRow(ctx, `SELECT rollback_to::text FROM agent_approvals WHERE id = $1`, rb).Scan(&target); err != nil || target != nil {
		t.Fatalf("an approved rollback kept a target: %v %v", target, err)
	}
}
