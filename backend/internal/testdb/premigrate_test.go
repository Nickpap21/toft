package testdb_test

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/testdb"
)

// premigrate.sql runs before every schema apply: on a fresh install's empty
// database, and on one with the schema, twice.
func TestPremigrateRunsOnAnEmptyAndAFullDatabase(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	sql, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "db", "migrations", "premigrate.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db := testdb.New(t)
	ctx := context.Background()
	for i := range 2 {
		if _, err := db.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("on the schema, run %d: %v", i+1, err)
		}
	}
	if _, err := db.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("on an empty database: %v", err)
	}
}

// Red left the environment colours (D-142): premigrate turns a red one gray
// and keeps its protection; other colours stay.
func TestPremigrateTurnsRedEnvironmentsGray(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	sql, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "db", "migrations", "premigrate.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db := testdb.New(t)
	ctx := context.Background()
	var userID, projectID string
	if err := db.QueryRow(ctx, `INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO projects (user_id, name) VALUES ($1, 'Shop') RETURNING id::text`, userID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	// Written by v0.33.x before Atlas tightens the CHECK (0071): premigrate
	// runs on the schema the previous release ran on.
	if _, err := db.Exec(ctx, `ALTER TABLE environments DROP CONSTRAINT environments_color_check`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO environments (project_id, name, color, protected) VALUES ($1, 'production', 'red', true), ($1, 'staging', 'amber', false)`, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]struct {
		color     string
		protected bool
	}{"production": {"gray", true}, "staging": {"amber", false}} {
		var color string
		var protected bool
		if err := db.QueryRow(ctx, `SELECT color, protected FROM environments WHERE name = $1`, name).Scan(&color, &protected); err != nil {
			t.Fatal(err)
		}
		if color != want.color || protected != want.protected {
			t.Errorf("%s: got %s, protected %v; want %s, protected %v", name, color, protected, want.color, want.protected)
		}
	}
}

// A preview never migrates (D-144): premigrate clears the migrate and
// pre-deploy commands of the copies of previews made before; other
// services keep theirs.
func TestPremigrateClearsPreviewMigrations(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	sql, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "db", "migrations", "premigrate.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db := testdb.New(t)
	ctx := context.Background()
	q := func(query string, args ...any) string {
		t.Helper()
		var id string
		if err := db.QueryRow(ctx, query, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	userID := q(`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`)
	orgID := q(`INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`)
	projectID := q(`INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'Shop') RETURNING id::text`, userID, orgID)
	repoID := q(`INSERT INTO repositories (org_id, project_id, name) VALUES ($1, $2, 'shop') RETURNING id::text`, orgID, projectID)
	pullID := q(`INSERT INTO pull_requests (repository_id, number, title, base_branch, head_branch, head_sha)
		VALUES ($1, 1, 't', 'main', 'feat', $2) RETURNING id::text`, repoID, strings.Repeat("a", 40))
	stagingID := q(`INSERT INTO environments (project_id, name) VALUES ($1, 'staging') RETURNING id::text`, projectID)
	previewID := q(`INSERT INTO environments (project_id, name) VALUES ($1, 'pr-1') RETURNING id::text`, projectID)
	q(`INSERT INTO previews (pull_request_id, project_id, source_environment_id, environment_id, head_sha, state)
		VALUES ($1, $2, $3, $4, $5, 'active') RETURNING id::text`, pullID, projectID, stagingID, previewID, strings.Repeat("a", 40))
	service := func(envID string) string {
		return q(`INSERT INTO services (scope, environment_id, project_id, name, type, source_type, docker_image, migrate_command, pre_deploy_commands)
			VALUES ('environment', $1, $2, 'web', 'web', 'docker-image', 'web:1', 'rails db:migrate', 'echo hi') RETURNING id::text`, envID, projectID)
	}
	original, cp := service(stagingID), service(previewID)
	// A copy whose preview is gone, made before services_preview_no_migrate
	// (0071): premigrate runs before Atlas adds it.
	if _, err := db.Exec(ctx, `ALTER TABLE services DROP CONSTRAINT services_preview_no_migrate`); err != nil {
		t.Fatal(err)
	}
	orphan := service(q(`INSERT INTO environments (project_id, name) VALUES ($1, 'pr-0') RETURNING id::text`, projectID))
	if _, err := db.Exec(ctx, `UPDATE services SET preview_of = $1 WHERE id = $2`, original, orphan); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if _, err := db.Exec(ctx, string(sql)); err != nil {
			t.Fatal(err)
		}
	}
	for id, want := range map[string][2]string{original: {"rails db:migrate", "echo hi"}, cp: {"", ""}, orphan: {"", ""}} {
		var got [2]string
		if err := db.QueryRow(ctx, `SELECT migrate_command, pre_deploy_commands FROM services WHERE id = $1`, id).Scan(&got[0], &got[1]); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("service %s: got %q, want %q", id, got, want)
		}
	}
}

// 0066 (D-151): on the upgrade that adds setup_hidden_at, a workspace with a
// live service (in a project, or a workspace service) gets its checklist
// hidden; one without stays. Once: a workspace that goes live after the
// column exists is never hidden by a later run.
func TestPremigrateHidesSetupForLiveWorkspacesOnce(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	sql, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "db", "migrations", "premigrate.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db := testdb.New(t)
	ctx := context.Background()
	q := func(query string, args ...any) string {
		t.Helper()
		var out string
		if err := db.QueryRow(ctx, query, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return out
	}
	// The database as the release before 0066 left it.
	if _, err := db.Exec(ctx, `ALTER TABLE organizations DROP COLUMN setup_hidden_at`); err != nil {
		t.Fatal(err)
	}
	user := q(`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`)
	org := func(name string) string {
		return q(`INSERT INTO organizations (name) VALUES ($1) RETURNING id::text`, name)
	}
	live, failed, shared, gced := org("live"), org("failed"), org("shared"), org("gced")
	org("idle")
	inProject := func(orgID, status string) string {
		env := q(`WITH p AS (INSERT INTO projects (org_id, user_id, name) VALUES ($1, $2, 'shop') RETURNING id)
			INSERT INTO environments (project_id, name) SELECT id, 'production' FROM p RETURNING id::text`, orgID, user)
		svc := q(`INSERT INTO services (environment_id, name, type, source_type, port) VALUES ($1, 'web', 'web', 'docker-image', 80) RETURNING id::text`, env)
		q(`INSERT INTO deployments (service_id, status) VALUES ($1, $2) RETURNING id::text`, svc, status)
		return svc
	}
	inProject(live, "running")
	idleSvc := inProject(failed, "failed")
	grafana := q(`INSERT INTO services (scope, scoped_org_id, name, type, source_type, port) VALUES ('org', $1, 'grafana', 'web', 'docker-image', 3000) RETURNING id::text`, shared)
	q(`INSERT INTO deployments (service_id, status) VALUES ($1, 'running') RETURNING id::text`, grafana)
	// Running, its running deployment gone to log GC; a project-scoped service.
	q(`WITH p AS (INSERT INTO projects (org_id, user_id, name) VALUES ($1, $2, 'ops') RETURNING id)
		INSERT INTO services (scope, project_id, name, type, source_type, port, runtime_status) SELECT 'project', id, 'db', 'web', 'docker-image', 5432, 'running' FROM p RETURNING id::text`, gced, user)

	hidden := func() map[string]bool {
		rows, err := db.Query(ctx, `SELECT name, setup_hidden_at IS NOT NULL FROM organizations`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]bool{}
		for rows.Next() {
			var name string
			var h bool
			if err := rows.Scan(&name, &h); err != nil {
				t.Fatal(err)
			}
			out[name] = h
		}
		return out
	}
	if _, err := db.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"live": true, "failed": false, "shared": true, "gced": true, "idle": false}
	if got := hidden(); !maps.Equal(got, want) {
		t.Fatalf("after the upgrade: got %v, want %v", got, want)
	}

	q(`INSERT INTO deployments (service_id, status) VALUES ($1, 'running') RETURNING id::text`, idleSvc)
	if _, err := db.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
	if got := hidden(); !maps.Equal(got, want) {
		t.Fatalf("a later run hid a workspace that went live since: got %v, want %v", got, want)
	}
}

// Add a server's loop (0073): a burst of server.agent_token_created rows
// keeps its first; separate visits, another action, rejoin commands (and a
// command just after one) and, after the step ran once, a later burst stay.
func TestPremigrateCollapsesJoinTokenAuditBursts(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	sql, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "db", "migrations", "premigrate.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db := testdb.New(t)
	ctx := context.Background()
	q := func(query string, args ...any) string {
		t.Helper()
		var out string
		if err := db.QueryRow(ctx, query, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return out
	}
	// The database as v0.35 left it: the step runs only before 0075's column.
	if _, err := db.Exec(ctx, `ALTER TABLE services DROP COLUMN compose_network_service`); err != nil {
		t.Fatal(err)
	}
	org := q(`INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`)
	user := q(`INSERT INTO users (email, name, password_hash) VALUES ('ada@toft.test', 'Ada', 'x') RETURNING id::text`)
	// at is seconds before now.
	auditRejoin := func(action string, at int, rejoin bool) {
		t.Helper()
		if _, err := db.Exec(ctx, `INSERT INTO audit_log (org_id, actor_id, action, resource, metadata, created_at)
			VALUES ($1, $2, $3, 'server:s-1', jsonb_build_object('name', 's-1', 'rejoin', $5::boolean), NOW() - make_interval(secs => $4))`, org, user, action, at, rejoin); err != nil {
			t.Fatal(err)
		}
	}
	audit := func(action string, at int) {
		t.Helper()
		auditRejoin(action, at, false)
	}
	// One looped visit three hours ago: 50 commands, a few a second, and a
	// rejoin command in the middle of it.
	for i := range 50 {
		audit("server.agent_token_created", 3*3600-i/4)
		audit("server.updated", 3*3600-i/4)
	}
	auditRejoin("server.agent_token_created", 3*3600-5, true)
	// Two visits an hour apart.
	audit("server.agent_token_created", 2*3600)
	audit("server.agent_token_created", 3600)
	// A rejoin command, and a plain one ten seconds after it.
	auditRejoin("server.agent_token_created", 1800, true)
	audit("server.agent_token_created", 1790)
	count := func(action string) string {
		return q(`SELECT count(*)::text FROM audit_log WHERE action = $1`, action)
	}
	// A joined server the loop gave 30 commands, then one more it joined
	// with, and a newer unused one; another server with one of each.
	server := func(name string) string {
		return q(`INSERT INTO servers (org_id, user_id, name, ip_address, connection, ssh_key, agent_key) VALUES ($1, $2, $3, '', 'agent', '', 'ssh-ed25519 AAAA') RETURNING id::text`, org, user, name)
	}
	token := func(srv string, ago int, used bool) string {
		return q(`INSERT INTO server_join_tokens (server_id, token_hash, created_at, expires_at, used_at)
			VALUES ($1, decode(md5(random()::text), 'hex'), NOW() - make_interval(secs => $2), NOW() - make_interval(secs => $2 - 1), CASE WHEN $3 THEN NOW() - make_interval(secs => $2) END)
			RETURNING id::text`, srv, ago, used)
	}
	joined, other := server("web-1"), server("web-2")
	for i := range 30 {
		token(joined, 7200-i, false)
	}
	usedID, newestID := token(joined, 3600, true), token(joined, 60, false)
	otherUsed, otherUnused := token(other, 3600, true), token(other, 60, false)
	tokens := func(srv string) string {
		return q(`SELECT string_agg(id::text, ',' ORDER BY id) FROM server_join_tokens WHERE server_id = $1`, srv)
	}
	sorted := func(ids ...string) string {
		slices.Sort(ids)
		return strings.Join(ids, ",")
	}

	for range 2 {
		if _, err := db.Exec(ctx, string(sql)); err != nil {
			t.Fatal(err)
		}
		if got := count("server.agent_token_created"); got != "6" {
			t.Fatalf("token rows: got %s, want 6", got)
		}
		if got := q(`SELECT count(*)::text FROM audit_log WHERE metadata->>'rejoin' = 'true'`); got != "2" {
			t.Fatalf("rejoin rows: got %s, want 2", got)
		}
		if got := count("server.updated"); got != "50" {
			t.Fatalf("another action: got %s, want 50", got)
		}
		if got, want := tokens(joined), sorted(usedID, newestID); got != want {
			t.Fatalf("the joined server's tokens: got %s, want %s", got, want)
		}
		if got, want := tokens(other), sorted(otherUsed, otherUnused); got != want {
			t.Fatalf("another server's tokens: got %s, want %s", got, want)
		}
	}

	// It ran once: a person's quick reloads since stay.
	audit("server.agent_token_created", 10)
	audit("server.agent_token_created", 5)
	if _, err := db.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
	if got := count("server.agent_token_created"); got != "8" {
		t.Fatalf("after a later run: got %s, want 8", got)
	}
}

// A fresh install (premigrate on an empty database, then schema.sql) never
// runs 0073's or 0075's step, on that or any later upgrade: a person's quick
// repeats stay, and a running stack's NULL stays for its next deploy.
func TestPremigrateSkipsOneOffStepsAfterAFreshInstall(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(file), "..", "..", "db", "migrations")
	sql, err := os.ReadFile(filepath.Join(dir, "premigrate.sql"))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile(filepath.Join(dir, "schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db := testdb.New(t)
	ctx := context.Background()
	for _, stmt := range []string{`DROP SCHEMA public CASCADE; CREATE SCHEMA public`, string(sql), string(schema)} {
		if _, err := db.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	q := func(query string, args ...any) string {
		t.Helper()
		var out string
		if err := db.QueryRow(ctx, query, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return out
	}
	org := q(`INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`)
	user := q(`INSERT INTO users (email, name, password_hash) VALUES ('ada@toft.test', 'Ada', 'x') RETURNING id::text`)
	for i := range 5 {
		q(`INSERT INTO audit_log (org_id, actor_id, action, resource, created_at)
			VALUES ($1, $2, 'server.agent_token_created', 'server:s-1', NOW() - make_interval(secs => $3)) RETURNING id::text`, org, user, 60-i)
	}
	env := q(`WITH p AS (INSERT INTO projects (user_id, name) VALUES ($1, 'shop') RETURNING id)
		INSERT INTO environments (project_id, name) SELECT id, 'production' FROM p RETURNING id::text`, user)
	stack := q(`INSERT INTO services (environment_id, name, type, source_type, port, compose_route_service, runtime_status)
		VALUES ($1, 'umami', 'web', 'docker-compose', 3000, 'umami', 'running') RETURNING id::text`, env)
	q(`INSERT INTO domains (service_id, hostname) VALUES ($1, 'umami.example.com') RETURNING id::text`, stack)

	if _, err := db.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
	if got := q(`SELECT count(*)::text FROM audit_log`); got != "5" {
		t.Errorf("audit rows after the next upgrade: got %s, want 5", got)
	}
	if got := q(`SELECT COALESCE(compose_network_service, '<null>') FROM services WHERE id = $1`, stack); got != "<null>" {
		t.Errorf("the stack's network service: got %s, want NULL", got)
	}
}

// 0075: on the upgrade that adds services.compose_network_service, a running
// compose stack with an explicit route and a domain gets its route; every
// other service stays NULL until its next deploy. Once: a NULL since stays.
func TestPremigrateRecordsRunningStacksNetworkServiceOnce(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	sql, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "db", "migrations", "premigrate.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db := testdb.New(t)
	ctx := context.Background()
	q := func(query string, args ...any) string {
		t.Helper()
		var out string
		if err := db.QueryRow(ctx, query, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return out
	}
	// The database as the release before 0075 left it.
	if _, err := db.Exec(ctx, `ALTER TABLE services DROP COLUMN compose_network_service`); err != nil {
		t.Fatal(err)
	}
	user := q(`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`)
	env := q(`WITH p AS (INSERT INTO projects (user_id, name) VALUES ($1, 'shop') RETURNING id)
		INSERT INTO environments (project_id, name) SELECT id, 'production' FROM p RETURNING id::text`, user)
	service := func(name, source, route, status string, domain bool) string {
		id := q(`INSERT INTO services (environment_id, name, type, source_type, port, compose_route_service, runtime_status)
			VALUES ($1, $2, 'web', $3, 3000, $4, $5) RETURNING id::text`, env, name, source, route, status)
		if domain {
			q(`INSERT INTO domains (service_id, hostname) VALUES ($1, $2) RETURNING id::text`, id, name+".example.com")
		}
		return id
	}
	umami := service("umami", "docker-compose", "umami", "running", true)
	service("nodomain", "docker-compose", "umami", "running", false)
	service("auto", "docker-compose", "", "running", true)
	service("stopped", "docker-compose", "web", "stopped", true)
	service("image", "docker-image", "", "running", true)

	recorded := func() map[string]string {
		rows, err := db.Query(ctx, `SELECT name, COALESCE(compose_network_service, '<null>') FROM services`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var name, v string
			if err := rows.Scan(&name, &v); err != nil {
				t.Fatal(err)
			}
			out[name] = v
		}
		return out
	}
	if _, err := db.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"umami": "umami", "nodomain": "<null>", "auto": "<null>", "stopped": "<null>", "image": "<null>"}
	if got := recorded(); !maps.Equal(got, want) {
		t.Fatalf("after the upgrade: got %v, want %v", got, want)
	}

	if _, err := db.Exec(ctx, `UPDATE services SET compose_network_service = NULL WHERE id = $1`, umami); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
	if got := recorded()["umami"]; got != "<null>" {
		t.Fatalf("a later run filled a NULL: got %s", got)
	}
}

// contract.sql (D-63) runs after Atlas on every upgrade: twice changes
// nothing (also while it holds no statement, as in v0.36.0), and the
// database then refuses red and a preview copy with a migrate or pre-deploy
// command (0071, kept by schema.sql).
func TestContractRunsTwiceAndRefuses(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	sql, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "db", "migrations", "contract.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db := testdb.New(t)
	ctx := context.Background()
	for i := range 2 {
		if _, err := db.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	var projectID, envID, originalID string
	if err := db.QueryRow(ctx, `WITH u AS (INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id)
		INSERT INTO projects (user_id, name) SELECT id, 'Shop' FROM u RETURNING id::text`).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, projectID).Scan(&envID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO services (environment_id, name, source_type, docker_image) VALUES ($1, 'web', 'docker-image', 'web:1') RETURNING id::text`, envID).Scan(&originalID); err != nil {
		t.Fatal(err)
	}
	for name, stmt := range map[string]string{
		"red":              `INSERT INTO environments (project_id, name, color) VALUES ('` + projectID + `', 'ops', 'red')`,
		"copy migrates":    `INSERT INTO services (environment_id, name, source_type, docker_image, preview_of, migrate_command) VALUES ('` + envID + `', 'a', 'docker-image', 'web:1', '` + originalID + `', 'migrate')`,
		"copy pre-deploys": `INSERT INTO services (environment_id, name, source_type, docker_image, preview_of, pre_deploy_commands) VALUES ('` + envID + `', 'b', 'docker-image', 'web:1', '` + originalID + `', 'echo hi')`,
	} {
		if _, err := db.Exec(ctx, stmt); err == nil || !strings.Contains(err.Error(), "check constraint") {
			t.Errorf("%s: got %v, want a check constraint violation", name, err)
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO services (environment_id, name, source_type, docker_image, preview_of) VALUES ($1, 'c', 'docker-image', 'web:1', $2)`, envID, originalID); err != nil {
		t.Errorf("a copy without commands: %v", err)
	}
}
