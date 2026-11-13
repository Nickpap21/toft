package agent_test

import (
	"context"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/agent"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/stretchr/testify/require"
)

// Registration needs no account, so unused registrations are capped; one
// with a code (or a connection) no longer counts (security close-out F6).
func TestDCRRegistrationsAreCapped(t *testing.T) {
	db := testdb.New(t)
	repo := agent.NewPostgresRepository(db)
	ctx := context.Background()
	uris := []string{"http://localhost/cb"}

	first, err := repo.InsertDCRClient(ctx, "toft_client_1", "One", uris, 2)
	require.NoError(t, err)
	_, err = repo.InsertDCRClient(ctx, "toft_client_2", "Two", uris, 2)
	require.NoError(t, err)
	_, err = repo.InsertDCRClient(ctx, "toft_client_3", "Three", uris, 2)
	require.ErrorIs(t, err, agent.ErrFull)

	var userID, orgID string
	require.NoError(t, db.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`).Scan(&userID))
	require.NoError(t, db.QueryRow(ctx,
		`INSERT INTO organizations (name) VALUES ('Shop') RETURNING id::text`).Scan(&orgID))
	require.NoError(t, repo.InsertCode(ctx, agent.Code{Hash: "h", ClientID: first.ID, UserID: userID, OrgID: orgID,
		AllProjects: true, Scopes: []string{"read"}, RedirectURI: uris[0], CodeChallenge: "c", ExpiresAt: time.Now().Add(time.Minute)}))
	_, err = repo.InsertDCRClient(ctx, "toft_client_3", "Three", uris, 2)
	require.NoError(t, err, "a registration with a code is in use")
}
