package environment_test

import (
	"context"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/environment"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/stretchr/testify/require"
)

// An update that matches no row tells apart an environment deleted since it
// was checked (404) from a protection change that raced it (409), with or
// without the protection's compare-and-set (security close-out F8).
func TestUpdateOfAGoneEnvironment(t *testing.T) {
	db := testdb.New(t)
	repo := environment.NewPostgresRepository(db)
	ctx := context.Background()

	var userID, projectID string
	require.NoError(t, db.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`).Scan(&userID))
	require.NoError(t, db.QueryRow(ctx,
		`INSERT INTO projects (user_id, name) VALUES ($1, 'Shop') RETURNING id::text`, userID).Scan(&projectID))
	protected := true
	env, err := repo.Create(ctx, projectID, environment.CreateInput{Name: "production", Protected: &protected})
	require.NoError(t, err)

	lift, was := false, false
	_, err = repo.Update(ctx, env.ID, environment.UpdateInput{Protected: &lift, WasProtected: &was})
	require.ErrorIs(t, err, environment.ErrProtectionChanged, "it was protected, not what the caller read")

	require.NoError(t, repo.Delete(ctx, env.ID))
	was = true
	_, err = repo.Update(ctx, env.ID, environment.UpdateInput{Protected: &lift, WasProtected: &was})
	require.ErrorIs(t, err, environment.ErrNotFound, "a protection change on a deleted environment")
	name := "prod"
	_, err = repo.Update(ctx, env.ID, environment.UpdateInput{Name: &name})
	require.ErrorIs(t, err, environment.ErrNotFound, "a rename of a deleted environment")
}

// Each environment's Overview has its own recent deploys: a busy
// production never pushes staging's out (the founders' staging Overview
// said "Nothing deployed" beside services deployed an hour before).
func TestProjectOverviewRecentPerEnvironment(t *testing.T) {
	db := testdb.New(t)
	repo := environment.NewPostgresRepository(db)
	ctx := context.Background()
	q := func(sql string, args ...any) string {
		t.Helper()
		var id string
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(&id))
		return id
	}
	userID := q(`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`)
	projectID := q(`INSERT INTO projects (user_id, name) VALUES ($1, 'Shop') RETURNING id::text`, userID)
	prod := q(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, projectID)
	staging := q(`INSERT INTO environments (project_id, name) VALUES ($1, 'staging') RETURNING id::text`, projectID)
	web := q(`INSERT INTO services (environment_id, name, source_type, docker_image) VALUES ($1, 'web', 'docker-image', 'a:1') RETURNING id::text`, prod)
	api := q(`INSERT INTO services (environment_id, name, source_type, docker_image) VALUES ($1, 'api', 'docker-image', 'a:1') RETURNING id::text`, staging)
	shared := q(`INSERT INTO services (scope, project_id, name, source_type, docker_image) VALUES ('project', $1, 'redis', 'docker-image', 'redis:7') RETURNING id::text`, projectID)
	q(`INSERT INTO deployments (service_id, status, created_at) VALUES ($1, 'running', NOW() - interval '3 hours') RETURNING id::text`, api)
	q(`INSERT INTO deployments (service_id, status, created_at) VALUES ($1, 'running', NOW() - interval '4 hours') RETURNING id::text`, shared)
	for i := 0; i < 8; i++ {
		q(`INSERT INTO deployments (service_id, status, created_at) VALUES ($1, 'running', NOW() - make_interval(mins => $2)) RETURNING id::text`, web, i)
	}

	o, err := repo.ProjectOverview(ctx, projectID, 6)
	require.NoError(t, err)
	per := map[string]int{}
	for _, d := range o.RecentDeployments {
		per[d.EnvironmentName]++
	}
	require.Equal(t, map[string]int{"production": 6, "staging": 1, "": 1}, per)
	require.Equal(t, "production", o.RecentDeployments[0].EnvironmentName, "newest first")
}

// A deploy the policy refused never started: the Overview gets its
// started_at as null, as the Deploys tab does, so both read it as Refused.
func TestProjectOverviewRecentStartedAt(t *testing.T) {
	db := testdb.New(t)
	repo := environment.NewPostgresRepository(db)
	ctx := context.Background()
	q := func(sql string, args ...any) string {
		t.Helper()
		var id string
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(&id))
		return id
	}
	userID := q(`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`)
	projectID := q(`INSERT INTO projects (user_id, name) VALUES ($1, 'Shop') RETURNING id::text`, userID)
	prod := q(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, projectID)
	web := q(`INSERT INTO services (environment_id, name, source_type, docker_image) VALUES ($1, 'web', 'docker-image', 'a:1') RETURNING id::text`, prod)
	refused := q(`INSERT INTO deployments (service_id, status, finished_at) VALUES ($1, 'failed', NOW()) RETURNING id::text`, web)
	failed := q(`INSERT INTO deployments (service_id, status, started_at, finished_at) VALUES ($1, 'failed', NOW(), NOW()) RETURNING id::text`, web)

	o, err := repo.ProjectOverview(ctx, projectID, 6)
	require.NoError(t, err)
	started := map[string]bool{}
	for _, d := range o.RecentDeployments {
		started[d.ID] = d.StartedAt != nil
	}
	require.Equal(t, map[string]bool{refused: false, failed: true}, started)
}

// The Environment page counts what each service sets itself and names the
// shared keys among them, from one query: secrets by name only, masked
// variables counted as variables, another environment's services left out.
func TestOverviewOwnKeysAndOverrides(t *testing.T) {
	db := testdb.New(t)
	repo := environment.NewPostgresRepository(db)
	ctx := context.Background()
	q := func(sql string, args ...any) string {
		t.Helper()
		var id string
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(&id))
		return id
	}
	userID := q(`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`)
	projectID := q(`INSERT INTO projects (user_id, name) VALUES ($1, 'Shop') RETURNING id::text`, userID)
	prod := q(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, projectID)
	staging := q(`INSERT INTO environments (project_id, name) VALUES ($1, 'staging') RETURNING id::text`, projectID)
	web := q(`INSERT INTO services (environment_id, name, source_type, docker_image) VALUES ($1, 'web', 'docker-image', 'a:1') RETURNING id::text`, prod)
	idle := q(`INSERT INTO services (environment_id, name, source_type, docker_image) VALUES ($1, 'idle', 'docker-image', 'a:1') RETURNING id::text`, prod)
	other := q(`INSERT INTO services (environment_id, name, source_type, docker_image) VALUES ($1, 'api', 'docker-image', 'a:1') RETURNING id::text`, staging)
	for _, e := range []struct {
		env, key string
		secret   bool
	}{{prod, "LOG_LEVEL", false}, {prod, "STRIPE_KEY", true}, {staging, "LOG_LEVEL", false}} {
		q(`INSERT INTO environment_env_vars (environment_id, key, value, is_secret) VALUES ($1, $2, 'shared', $3) RETURNING id::text`, e.env, e.key, e.secret)
	}
	for _, e := range []struct {
		svc, key       string
		secret, masked bool
	}{
		{web, "PORT", false, false}, {web, "LOG_LEVEL", false, true}, {web, "STRIPE_KEY", true, false},
		{other, "LOG_LEVEL", false, false}, {other, "API_TOKEN", true, false},
	} {
		q(`INSERT INTO env_vars (service_id, key, value, is_secret, masked) VALUES ($1, $2, 'own', $3, $4) RETURNING id::text`, e.svc, e.key, e.secret, e.masked)
	}
	q(`INSERT INTO deployments (service_id, status) VALUES ($1, 'running') RETURNING id::text`, idle)

	got, err := repo.Overview(ctx, prod)
	require.NoError(t, err)
	by := map[string]environment.ServiceOverview{}
	for _, s := range got {
		by[s.ServiceID] = s
	}
	require.Len(t, by, 2, "staging's api is not in production's overview")
	require.Equal(t, 2, by[web].OwnVariables, "the masked one counts as a variable")
	require.Equal(t, 1, by[web].OwnSecrets)
	require.Equal(t, []string{"LOG_LEVEL", "STRIPE_KEY"}, by[web].Overrides, "a variable and a secret override, A to Z")
	require.Equal(t, 0, by[idle].OwnVariables)
	require.Equal(t, 0, by[idle].OwnSecrets)
	require.Equal(t, []string{}, by[idle].Overrides, "an empty list, not null")
}
