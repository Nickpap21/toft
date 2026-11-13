package configfile_test

import (
	"context"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/configfile"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/stretchr/testify/require"
)

// Delete removes only the service's own file and returns its path, for the
// audit log; another service's id deletes nothing and returns "".
func TestDeleteReturnsPath(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	repo := configfile.NewPostgresRepository(db)
	var svcID, otherID string
	require.NoError(t, db.QueryRow(ctx, `WITH u AS (INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id),
		p AS (INSERT INTO projects (user_id, name) SELECT id, 'Shop' FROM u RETURNING id),
		e AS (INSERT INTO environments (project_id, name) SELECT id, 'production' FROM p RETURNING id),
		s AS (INSERT INTO services (environment_id, name, type, source_type, port) SELECT id, n, 'web', 'docker-image', 80 FROM e, (VALUES ('web'), ('api')) v(n) RETURNING id, name)
		SELECT (SELECT id::text FROM s WHERE name = 'web'), (SELECT id::text FROM s WHERE name = 'api')`).Scan(&svcID, &otherID))
	f, err := repo.Upsert(ctx, svcID, configfile.UpsertInput{Path: "/etc/app/app.conf", Content: "x"})
	require.NoError(t, err)

	path, err := repo.Delete(ctx, f.ID, otherID)
	require.NoError(t, err)
	require.Empty(t, path)
	path, err = repo.Delete(ctx, f.ID, svcID)
	require.NoError(t, err)
	require.Equal(t, "/etc/app/app.conf", path)
	files, err := repo.List(ctx, svcID)
	require.NoError(t, err)
	require.Empty(t, files)
}
