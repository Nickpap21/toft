package code_test

import (
	"context"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/code"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/stretchr/testify/require"
)

// AgentBranch names the agent and its person, and when the branch's push
// password stops working: its own expiry, or sooner once the connection
// expires or is revoked (a pull request's About, 7g step 14).
func TestAgentBranchNamesAndEnds(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	repo := code.NewPostgresRepository(db)
	var user, org, project, repoID, client, conn string
	row := func(dst *string, sql string, args ...any) {
		t.Helper()
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(dst))
	}
	row(&user, `INSERT INTO users (email, name, password_hash) VALUES ('sam@toft.test', 'Sam', 'x') RETURNING id::text`)
	row(&org, `INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`)
	row(&project, `INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'Shop') RETURNING id::text`, user, org)
	row(&repoID, `INSERT INTO repositories (org_id, project_id, name) VALUES ($1, $2, 'web') RETURNING id::text`, org, project)
	row(&client, `INSERT INTO oauth_clients (client_id, kind, name, redirect_uris) VALUES ('c', 'dcr', 'Claude Code', '{http://127.0.0.1/cb}') RETURNING id::text`)
	row(&conn, `INSERT INTO agent_connections (org_id, user_id, client_id, name, all_projects, scopes, expires_at)
		VALUES ($1, $2, $3, 'Claude Code', TRUE, '{read,code}', NOW() + interval '30 days') RETURNING id::text`, org, user, client)
	token := time.Now().Add(23 * time.Hour).UTC().Truncate(time.Second)
	_, err := db.Exec(ctx, `INSERT INTO agent_branches (repository_id, name, connection_id, user_id, token_hash, token_expires_at)
		VALUES ($1, 'agent/fix', $2, $3, 'h', $4)`, repoID, conn, user, token)
	require.NoError(t, err)

	b, err := repo.AgentBranch(ctx, repoID, "agent/fix")
	require.NoError(t, err)
	require.Equal(t, "Claude Code", b.AgentName)
	require.Equal(t, "Sam", b.UserName)
	require.Equal(t, conn, b.ConnectionID)
	require.True(t, b.ExpiresAt.Equal(token), "the password's own expiry: %v", b.ExpiresAt)

	var revoked time.Time
	require.NoError(t, db.QueryRow(ctx, `UPDATE agent_connections SET revoked_at = NOW() WHERE id = $1 RETURNING revoked_at`, conn).Scan(&revoked))
	b, err = repo.AgentBranch(ctx, repoID, "agent/fix")
	require.NoError(t, err)
	require.True(t, b.ExpiresAt.Equal(revoked), "a revoked connection ends it: %v", b.ExpiresAt)

	_, err = repo.AgentBranch(ctx, repoID, "someone/else")
	require.ErrorIs(t, err, code.ErrNotFound)
}
