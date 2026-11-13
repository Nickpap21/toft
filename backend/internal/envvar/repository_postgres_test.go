package envvar_test

import (
	"context"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/envvar"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/pkg/actor"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/stretchr/testify/require"
)

// Secrets sealed in v1 (D-126) open where they were written: a service's and
// its environment's, created and updated. One copied to another service
// doesn't open.
func TestSecretsRoundTripInV1(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	repo := envvar.NewPostgresRepository(db, crypto.NewKeys("test-encryption-key", ""))
	q := func(sql string, dst *string, args ...any) {
		t.Helper()
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(dst))
	}
	var userID, projectID, envID, svcA, svcB string
	q(`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`, &userID)
	q(`INSERT INTO projects (user_id, name) VALUES ($1, 'Shop') RETURNING id::text`, &projectID, userID)
	q(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, &envID, projectID)
	q(`INSERT INTO services (environment_id, name, source_type, docker_image) VALUES ($1, 'a', 'docker-image', 'a:1') RETURNING id::text`, &svcA, envID)
	q(`INSERT INTO services (environment_id, name, source_type, docker_image) VALUES ($1, 'b', 'docker-image', 'b:1') RETURNING id::text`, &svcB, envID)

	_, err := repo.CreateSecret(ctx, svcA, userID, envvar.CreateSecretInput{Key: "DB_URL", Value: "postgres://a"})
	require.NoError(t, err)
	_, err = repo.CreateSecret(ctx, svcA, userID, envvar.CreateSecretInput{Key: "TOKEN", Value: "old"})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateSecret(ctx, svcA, userID, "TOKEN", "new"))
	_, err = repo.CreateEnvironmentSecret(ctx, envID, userID, envvar.CreateSecretInput{Key: "SHARED", Value: "s1"})
	require.NoError(t, err)
	_, err = repo.CreateEnvironmentSecret(ctx, envID, userID, envvar.CreateSecretInput{Key: "ROTATED", Value: "r1"})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateEnvironmentSecret(ctx, envID, userID, "ROTATED", "r2"))

	vars, err := repo.ListAllForDeployment(ctx, svcA)
	require.NoError(t, err)
	got := map[string]string{}
	for _, v := range vars {
		got[v.Key] = v.Value
	}
	require.Equal(t, map[string]string{"DB_URL": "postgres://a", "TOKEN": "new", "SHARED": "s1", "ROTATED": "r2"}, got)

	_, err = db.Exec(ctx, `INSERT INTO env_vars (service_id, key, value, is_secret)
		SELECT $2, key, value, TRUE FROM env_vars WHERE service_id = $1 AND key = 'DB_URL'`, svcA, svcB)
	require.NoError(t, err)
	_, err = repo.ListAllForDeployment(ctx, svcB)
	require.Error(t, err, "a secret copied to another service opens")
}

// A variable or secret an agent set, a service's or its environment's, is
// shown set by the agent acting for its person (D-100); the person setting
// it again is shown as the person.
func TestAgentSetVariablesNameTheAgent(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	repo := envvar.NewPostgresRepository(db, crypto.NewKeys("test-encryption-key", ""))
	q := func(sql string, dst *string, args ...any) {
		t.Helper()
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(dst))
	}
	var userID, orgID, clientID, connID, projectID, envID, svc string
	q(`INSERT INTO users (email, name, password_hash) VALUES ('ada@toft.test', 'Ada', 'x') RETURNING id::text`, &userID)
	q(`INSERT INTO organizations (name) VALUES ('Founders') RETURNING id::text`, &orgID)
	q(`INSERT INTO oauth_clients (client_id, kind, name, redirect_uris) VALUES ('c', 'dcr', 'Claude Code', '{http://localhost/cb}') RETURNING id::text`, &clientID)
	q(`INSERT INTO agent_connections (org_id, user_id, client_id, name, all_projects, scopes, expires_at)
		VALUES ($1, $2, $3, 'Claude Code', true, '{read}', now() + interval '1 day') RETURNING id::text`, &connID, orgID, userID, clientID)
	q(`INSERT INTO projects (user_id, name) VALUES ($1, 'Shop') RETURNING id::text`, &projectID, userID)
	q(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, &envID, projectID)
	q(`INSERT INTO services (environment_id, name, source_type, docker_image) VALUES ($1, 'a', 'docker-image', 'a:1') RETURNING id::text`, &svc, envID)
	agent := actor.WithAgent(ctx, actor.Agent{ConnectionID: connID, Name: "Claude Code", UserID: userID, OrgID: orgID})

	_, err := repo.Upsert(agent, svc, userID, envvar.UpsertInput{Key: "LOG_LEVEL", Value: "debug"})
	require.NoError(t, err)
	_, err = repo.Upsert(agent, svc, userID, envvar.UpsertInput{Key: "MODE", Value: "a"})
	require.NoError(t, err)
	_, err = repo.Upsert(ctx, svc, userID, envvar.UpsertInput{Key: "MODE", Value: "b"})
	require.NoError(t, err)
	_, err = repo.CreateSecret(agent, svc, userID, envvar.CreateSecretInput{Key: "TOKEN", Value: "t"})
	require.NoError(t, err)
	_, err = repo.UpsertEnvironmentVar(agent, envID, userID, envvar.UpsertInput{Key: "REGION", Value: "eu"})
	require.NoError(t, err)
	_, err = repo.CreateEnvironmentSecret(agent, envID, userID, envvar.CreateSecretInput{Key: "SHARED", Value: "s"})
	require.NoError(t, err)

	// A write with no person, as the IMAGE_TAG an agent's deploy saves,
	// names no agent either.
	_, err = repo.Upsert(agent, svc, "", envvar.UpsertInput{Key: "IMAGE_TAG", Value: "1.2"})
	require.NoError(t, err)

	setBy := func(svc, env string) map[string]string {
		t.Helper()
		by := map[string]string{}
		own, err := repo.ListByService(ctx, svc)
		require.NoError(t, err)
		for _, v := range own {
			by[v.Key] = v.UpdatedBy
		}
		secrets, err := repo.ListSecrets(ctx, svc)
		require.NoError(t, err)
		for _, v := range secrets {
			by[v.Key] = v.UpdatedBy
		}
		if env == "" {
			return by
		}
		envVars, err := repo.ListEnvironmentVars(ctx, env)
		require.NoError(t, err)
		for _, v := range envVars {
			by["env:"+v.Key] = v.UpdatedBy
		}
		envSecrets, err := repo.ListEnvironmentSecrets(ctx, env)
		require.NoError(t, err)
		for _, v := range envSecrets {
			by["env:"+v.Key] = v.UpdatedBy
		}
		return by
	}
	require.Equal(t, map[string]string{
		"LOG_LEVEL": "Claude Code for Ada", "MODE": "Ada", "TOKEN": "Claude Code for Ada", "IMAGE_TAG": "",
		"env:REGION": "Claude Code for Ada", "env:SHARED": "Claude Code for Ada",
	}, setBy(svc, envID))

	// A person replacing the agent's secret is shown as the person.
	require.NoError(t, repo.UpdateSecret(ctx, svc, userID, "TOKEN", "t2"))
	require.Equal(t, "Ada", setBy(svc, "")["TOKEN"])

	// A service given its environment's copies keeps who set each.
	var moved string
	q(`INSERT INTO services (environment_id, name, source_type, docker_image) VALUES ($1, 'b', 'docker-image', 'b:1') RETURNING id::text`, &moved, envID)
	require.NoError(t, repo.InheritEnvironment(ctx, envID, moved))
	require.Equal(t, map[string]string{"REGION": "Claude Code for Ada", "SHARED": "Claude Code for Ada"}, setBy(moved, ""))
}
