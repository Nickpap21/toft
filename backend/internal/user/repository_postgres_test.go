package user_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/internal/user"
	"github.com/stretchr/testify/require"
)

// What the auth middleware reads per request: a signed-out session token is
// refused and the account's others are not; "sign out other browsers"
// moves the moment before which tokens are refused; and a workspace the
// account isn't a member of gives no role.
func TestSessionRevocation(t *testing.T) {
	db := testdb.New(t)
	repo := user.NewPostgresRepository(db)
	ctx := context.Background()

	var userID, orgID, otherOrg string
	require.NoError(t, db.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`,
	).Scan(&userID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`).Scan(&orgID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('Other') RETURNING id::text`).Scan(&otherOrg))
	_, err := db.Exec(ctx, `INSERT INTO org_members (org_id, user_id, role) VALUES ($1, $2, 'developer')`, orgID, userID)
	require.NoError(t, err)
	_, err = db.Exec(ctx, `UPDATE users SET active_org_id = $1 WHERE id = $2`, orgID, userID)
	require.NoError(t, err)

	// Session tokens' sha256, as the middleware computes them.
	signedOut, other, stale := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	ident, err := repo.RequestIdentity(ctx, userID, "", signedOut)
	require.NoError(t, err)
	require.True(t, ident.Active)
	require.False(t, ident.SessionRevoked)
	require.Equal(t, orgID, ident.OrgID)
	require.Equal(t, "developer", ident.Role)

	// A revoked token past its expiry goes with the next sign-out.
	_, err = db.Exec(ctx, `INSERT INTO revoked_sessions (token_hash, expires_at) VALUES ($1, NOW() - interval '1 minute')`, stale)
	require.NoError(t, err)
	require.NoError(t, repo.RevokeSession(ctx, signedOut, time.Now().Add(time.Hour)))
	require.NoError(t, repo.RevokeSession(ctx, signedOut, time.Now().Add(time.Hour)), "signing out twice is fine")

	ident, err = repo.RequestIdentity(ctx, userID, "", signedOut)
	require.NoError(t, err)
	require.True(t, ident.SessionRevoked)
	ident, err = repo.RequestIdentity(ctx, userID, "", other)
	require.NoError(t, err)
	require.False(t, ident.SessionRevoked, "the account's other sessions stay")
	ident, err = repo.RequestIdentity(ctx, userID, "", "")
	require.NoError(t, err)
	require.False(t, ident.SessionRevoked, "an API key carries no session")
	var staleLeft bool
	require.NoError(t, db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM revoked_sessions WHERE token_hash = $1)`, stale).Scan(&staleLeft))
	require.False(t, staleLeft)

	before := ident.TokensValidFrom
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, repo.RevokeSessions(ctx, userID))
	ident, err = repo.RequestIdentity(ctx, userID, "", other)
	require.NoError(t, err)
	require.True(t, ident.TokensValidFrom.After(before), "every token issued before now is refused")

	ident, err = repo.RequestIdentity(ctx, userID, otherOrg, other)
	require.NoError(t, err)
	require.Empty(t, ident.Role, "not a member of the workspace it names")
	require.Empty(t, ident.OrgID)
}
