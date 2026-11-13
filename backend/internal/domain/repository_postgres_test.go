package domain_test

import (
	"context"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/domain"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/stretchr/testify/require"
)

// Only routed names without a certificate are checked, wildcards never;
// recording one takes it off the list (D-150).
func TestAwaitingCertificate(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	repo := domain.NewPostgresRepository(db)
	var userID, projectID, envID, svcID string
	for _, s := range []struct {
		sql  string
		dst  *string
		args []any
	}{
		{`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`, &userID, nil},
		{`INSERT INTO projects (user_id, name) VALUES ($1, 'Shop') RETURNING id::text`, &projectID, []any{&userID}},
		{`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, &envID, []any{&projectID}},
		{`INSERT INTO services (environment_id, name, type, source_type, port) VALUES ($1, 'web', 'web', 'docker-image', 80) RETURNING id::text`, &svcID, []any{&envID}},
	} {
		args := make([]any, len(s.args))
		for i, a := range s.args {
			args[i] = *a.(*string)
		}
		require.NoError(t, db.QueryRow(ctx, s.sql, args...).Scan(s.dst))
	}
	_, err := db.Exec(ctx, `INSERT INTO domains (service_id, hostname, routed_at) VALUES
		($1, 'routed.example.com', NOW()), ($1, '*.wild.example.com', NOW()), ($1, 'pending.example.com', NULL)`, svcID)
	require.NoError(t, err)

	got, err := repo.AwaitingCertificate(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "routed.example.com", got[0].Hostname)
	require.Equal(t, svcID, got[0].ServiceID, "the service a temporary name's certificate redeploys")

	recorded, err := repo.SetCertificate(ctx, got[0].ID)
	require.NoError(t, err)
	require.True(t, recorded)
	recorded, err = repo.SetCertificate(ctx, got[0].ID)
	require.NoError(t, err)
	require.False(t, recorded, "recorded once: only the first redeploys (D-150)")
	got, err = repo.AwaitingCertificate(ctx)
	require.NoError(t, err)
	require.Empty(t, got)

	taken, err := repo.Taken(ctx, "routed.example.com")
	require.NoError(t, err)
	require.True(t, taken)
}

// Delete removes only the service's own domain and returns its hostname,
// for the audit log; another service's id is not found and stays.
func TestDeleteReturnsHostname(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	repo := domain.NewPostgresRepository(db)
	var svcID, otherID, domainID string
	require.NoError(t, db.QueryRow(ctx, `WITH u AS (INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id),
		p AS (INSERT INTO projects (user_id, name) SELECT id, 'Shop' FROM u RETURNING id),
		e AS (INSERT INTO environments (project_id, name) SELECT id, 'production' FROM p RETURNING id),
		s AS (INSERT INTO services (environment_id, name, type, source_type, port) SELECT id, n, 'web', 'docker-image', 80 FROM e, (VALUES ('web'), ('api')) v(n) RETURNING id, name)
		SELECT (SELECT id::text FROM s WHERE name = 'web'), (SELECT id::text FROM s WHERE name = 'api')`).Scan(&svcID, &otherID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO domains (service_id, hostname) VALUES ($1, 'shop.example.com') RETURNING id::text`, svcID).Scan(&domainID))

	_, err := repo.Delete(ctx, otherID, domainID)
	require.ErrorIs(t, err, domain.ErrNotFound)
	hostname, err := repo.Delete(ctx, svcID, domainID)
	require.NoError(t, err)
	require.Equal(t, "shop.example.com", hostname)
	_, err = repo.Delete(ctx, svcID, domainID)
	require.ErrorIs(t, err, domain.ErrNotFound)
}
