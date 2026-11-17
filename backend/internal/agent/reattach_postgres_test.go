package agent_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/agent"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/pkg/actor"
	"go.uber.org/zap"
)

// The same deploy again re-attaches to its approved run only while the
// deployment that run started is pending or building (D-164): once it is
// over, or the run started none, the call is a new request a person
// approves again, never the old run's answer.
func TestRepeatedDeployReattachesOnlyWhileItsDeploymentIsInFlightInPostgres(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	repo := agent.NewPostgresRepository(db)
	svc := agent.NewService(repo, roles{}, ops{}, nopAudit{}, zap.NewNop())
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
	org := q(`INSERT INTO organizations (name) VALUES ('Velbee') RETURNING id::text`)
	exec(`INSERT INTO org_members (org_id, user_id, role) VALUES ($1, $2, 'owner'), ($1, $3, 'developer')`, org, admin, dev)
	client := q(`INSERT INTO oauth_clients (client_id, kind, name, redirect_uris) VALUES ('c', 'dcr', 'Claude Code', '{http://localhost/cb}') RETURNING id::text`)
	conn := q(`INSERT INTO agent_connections (org_id, user_id, client_id, name, all_projects, scopes, expires_at)
		VALUES ($1, $2, $3, 'sam''s laptop', true, '{read,deploy}', NOW() + INTERVAL '1 day') RETURNING id::text`, org, dev, client)
	project := q(`INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'velbee') RETURNING id::text`, admin, org)
	env := q(`INSERT INTO environments (project_id, name, protected) VALUES ($1, 'production', true) RETURNING id::text`, project)
	service := q(`INSERT INTO services (scope, environment_id, project_id, name, type, source_type, docker_image)
		VALUES ('environment', $1, $2, 'web', 'web', 'docker-image', 'web:1') RETURNING id::text`, env, project)

	actx := actor.WithIntent(actor.WithAgent(ctx, actor.Agent{ConnectionID: conn, OrgID: org, UserID: dev}),
		actor.Intent{Tool: "deploy", Reason: "ship it", Method: http.MethodPost, Path: "/api/v1/services/" + service + "/deployments"})
	deploy := func() string {
		t.Helper()
		var g *actor.GateError
		if err := svc.Check(actx, actor.Target{Action: agent.ActionDeploy, Tool: "deploy", ServiceID: service}); !errors.As(err, &g) || g.Code != "approval_pending" {
			t.Fatalf("a deploy to a protected service waits for a person: %v", err)
		}
		return g.ApprovalID
	}
	// Approved and run: the run started a deployment in status, or none.
	run := func(id, status string) string {
		t.Helper()
		if err := repo.DecideApproval(ctx, id, org, "approved", admin, ""); err != nil {
			t.Fatal(err)
		}
		if ok, err := repo.ClaimRun(ctx, id, conn); err != nil || !ok {
			t.Fatalf("claim the run: %v %v", ok, err)
		}
		d, res := "", agent.Result{Status: http.StatusConflict}
		if status != "" {
			d, res = q(`INSERT INTO deployments (service_id, status) VALUES ($1, $2) RETURNING id::text`, service, status), agent.Result{Status: http.StatusAccepted}
		}
		if err := repo.SetResult(ctx, id, res, d); err != nil {
			t.Fatal(err)
		}
		return d
	}

	first := deploy()
	if again := deploy(); again != first {
		t.Fatalf("a waiting request wasn't re-attached: %s, then %s", first, again)
	}
	d := run(first, "building")
	if again := deploy(); again != first {
		t.Fatalf("re-attached to %s while its deployment builds, want %s", again, first)
	}
	exec(`UPDATE deployments SET status = 'pending' WHERE id = $1`, d)
	if again := deploy(); again != first {
		t.Fatalf("re-attached to %s while its deployment is pending, want %s", again, first)
	}

	// The deployment finished (the Velbee report): the same call asks again.
	exec(`UPDATE deployments SET status = 'running', finished_at = NOW() WHERE id = $1`, d)
	second := deploy()
	if second == first {
		t.Fatal("a repeated deploy was answered with a finished deployment's approval")
	}
	if a, err := svc.Approval(ctx, org, second); err != nil || a.State != "waiting" {
		t.Fatalf("the new request: %+v %v", a, err)
	}

	// A run that started no deployment is over too.
	run(second, "")
	third := deploy()
	if third == second || third == first {
		t.Fatalf("re-attached to a run that started no deployment: %s", third)
	}

	// While the run is still starting the deploy, the same call waits for it.
	if err := repo.DecideApproval(ctx, third, org, "approved", admin, ""); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.ClaimRun(ctx, third, conn); err != nil || !ok {
		t.Fatalf("claim the run: %v %v", ok, err)
	}
	if again := deploy(); again != third {
		t.Fatalf("re-attached to %s while the run starts, want %s", again, third)
	}
}
