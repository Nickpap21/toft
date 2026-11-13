package code_test

import (
	"context"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/code"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/stretchr/testify/require"
)

// Protection is stored by the branch's name (L2, 0080). A repository
// protected before 0080 has none until a push stores HEAD's, and only while
// protection is still on and no name is stored: an owner's change wins.
func TestProtectedBranchByName(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	repo := code.NewPostgresRepository(db)
	var user, org, project, repoID string
	row := func(dst *string, sql string, args ...any) {
		t.Helper()
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(dst))
	}
	row(&user, `INSERT INTO users (email, name, password_hash) VALUES ('sam@toft.test', 'Sam', 'x') RETURNING id::text`)
	row(&org, `INSERT INTO organizations (name, slug) VALUES ('Acme', 'acme') RETURNING id::text`)
	row(&project, `INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'Shop') RETURNING id::text`, user, org)
	row(&repoID, `INSERT INTO repositories (org_id, project_id, name, protect_default_branch) VALUES ($1, $2, 'web', TRUE) RETURNING id::text`, org, project)
	protected := func() (bool, string) {
		t.Helper()
		tg, err := repo.RepoByPath(ctx, "acme", "web")
		require.NoError(t, err)
		return tg.ProtectDefault, tg.ProtectedBranch
	}

	on, name := protected()
	require.True(t, on)
	require.Empty(t, name, "protected before 0080: no name yet")
	require.NoError(t, repo.FillProtectedBranch(ctx, repoID, "main"))
	require.NoError(t, repo.FillProtectedBranch(ctx, repoID, "other"))
	_, name = protected()
	require.Equal(t, "main", name, "the backfill stores the first name, once")

	require.NoError(t, repo.SetProtection(ctx, repoID, false, ""))
	require.NoError(t, repo.FillProtectedBranch(ctx, repoID, "main"))
	on, name = protected()
	require.False(t, on)
	require.Empty(t, name, "the backfill never names a branch while protection is off")

	require.NoError(t, repo.SetProtection(ctx, repoID, true, "release"))
	on, name = protected()
	require.True(t, on)
	require.Equal(t, "release", name)

	_, err := db.Exec(ctx, `UPDATE repositories SET protected_branch = '' WHERE id = $1`, repoID)
	require.Error(t, err, "an empty name")
}
