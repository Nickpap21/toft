package webhook_test

import (
	"context"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/internal/webhook"
	"github.com/stretchr/testify/require"
)

// The last signed push (0077): none until one, then when and from where.
func TestLastPushRoundTrip(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	repo := webhook.NewPostgresRepository(db)

	var user, org, project string
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO users (email, name, password_hash) VALUES ('a@toft.test', 'a', 'x') RETURNING id::text`).Scan(&user))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`).Scan(&org))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'storefront') RETURNING id::text`, user, org).Scan(&project))

	last, err := repo.LastPush(ctx, project)
	require.NoError(t, err)
	require.Nil(t, last.At, "no webhook yet")
	_, err = repo.Ensure(ctx, project, "sealed")
	require.NoError(t, err)
	last, err = repo.LastPush(ctx, project)
	require.NoError(t, err)
	require.Nil(t, last.At, "no push yet")

	require.NoError(t, repo.MarkPushed(ctx, project, "founders/storefront"))
	last, err = repo.LastPush(ctx, project)
	require.NoError(t, err)
	require.NotNil(t, last.At)
	require.Equal(t, "founders/storefront", last.Repo)
}
