package reveal_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/reveal"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type owners struct{ ok bool }

func (o owners) CheckOwnership(context.Context, string, string) (bool, error) { return o.ok, nil }

type audit struct {
	actions []string
	fail    bool
}

func (a *audit) RecordAuditLog(_ context.Context, _, _, action, resource string) error {
	if a.fail {
		return errors.New("audit log down")
	}
	a.actions = append(a.actions, action+" "+resource)
	return nil
}

// D-160: a copy is its keeper's alone, for owners and developers; sealed
// at rest; opened and dismissed with an audit entry that never holds the
// value; a dismissal and the 7-day clean-up delete the row; a reset
// replaces it; deleting the service deletes it.
func TestReveals(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	row := func(sql string, args ...any) string {
		t.Helper()
		var id string
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(&id), sql)
		return id
	}
	count := func() int {
		t.Helper()
		var n int
		require.NoError(t, db.QueryRow(ctx, `SELECT count(*) FROM secret_reveals`).Scan(&n))
		return n
	}
	alex := row(`INSERT INTO users (email, name, password_hash) VALUES ('alex@toft.test', 'Alex', 'x') RETURNING id::text`)
	sam := row(`INSERT INTO users (email, name, password_hash) VALUES ('sam@toft.test', 'Sam', 'x') RETURNING id::text`)
	org := row(`INSERT INTO organizations (name) VALUES ('Founders') RETURNING id::text`)
	project := row(`INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'shop') RETURNING id::text`, alex, org)
	env := row(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, project)
	svc := row(`INSERT INTO services (environment_id, name) VALUES ($1, 'grafana') RETURNING id::text`, env)

	repo := reveal.NewPostgresRepository(db, crypto.NewKeys("test-encryption-key", ""))
	log := &audit{}
	s := reveal.NewService(repo, owners{ok: true}, log, zap.NewNop())
	require.NoError(t, repo.Keep(ctx, svc, alex, "GF_SECURITY_ADMIN_PASSWORD", "first-password", "created"))

	var sealed string
	require.NoError(t, db.QueryRow(ctx, `SELECT sealed FROM secret_reveals`).Scan(&sealed))
	require.NotContains(t, sealed, "first-password", "sealed at rest")

	// Only its keeper, and only an owner or developer.
	p, err := s.Pending(ctx, org, alex, "developer", svc)
	require.NoError(t, err)
	require.Equal(t, "GF_SECURITY_ADMIN_PASSWORD", p.Key)
	require.Empty(t, p.Password, "Pending carries no value")
	_, err = s.Open(ctx, org, sam, "owner", svc)
	require.ErrorIs(t, err, reveal.ErrNone, "another member, even an owner, has none")
	_, err = s.Open(ctx, org, alex, "viewer", svc)
	require.ErrorIs(t, err, reveal.ErrForbidden)
	_, err = reveal.NewService(repo, owners{ok: false}, log, zap.NewNop()).Open(ctx, org, alex, "owner", svc)
	require.ErrorIs(t, err, reveal.ErrNotFound, "another workspace's service")
	require.Empty(t, log.actions, "nothing was revealed")

	// A reveal its audit entry can't record is refused.
	log.fail = true
	_, err = s.Open(ctx, org, alex, "owner", svc)
	require.Error(t, err)
	log.fail = false

	o, err := s.Open(ctx, org, alex, "owner", svc)
	require.NoError(t, err)
	require.Equal(t, "first-password", o.Password)

	// A reset replaces it.
	require.NoError(t, repo.Keep(ctx, svc, alex, "GF_SECURITY_ADMIN_PASSWORD", "second-password", "reset"))
	require.Equal(t, 1, count())
	o, err = s.Open(ctx, org, alex, "owner", svc)
	require.NoError(t, err)
	require.Equal(t, []string{"second-password", "reset"}, []string{o.Password, o.Reason})

	// Dismissed, here by its keeper demoted to viewer: deleted, not hidden.
	require.NoError(t, s.Dismiss(ctx, org, alex, svc))
	require.Zero(t, count())
	require.ErrorIs(t, s.Dismiss(ctx, org, alex, svc), reveal.ErrNone)
	require.Equal(t, []string{
		"service.admin_password_revealed service:" + svc,
		"service.admin_password_revealed service:" + svc,
		"service.admin_password_dismissed service:" + svc,
	}, log.actions, "audited, never with the value")

	// Past 7 days: never served, and the clean-up deletes it.
	require.NoError(t, repo.Keep(ctx, svc, alex, "K", "old", "created"))
	_, err = db.Exec(ctx, `UPDATE secret_reveals SET created_at = NOW() - INTERVAL '7 days 1 minute'`)
	require.NoError(t, err)
	_, err = s.Pending(ctx, org, alex, "owner", svc)
	require.ErrorIs(t, err, reveal.ErrNone)
	n, err := repo.DeleteBefore(ctx, time.Now().Add(-reveal.Keep))
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.Zero(t, count())

	// The service's deletion takes its copies with it.
	require.NoError(t, repo.Keep(ctx, svc, sam, "K", "v", "created"))
	_, err = db.Exec(ctx, `DELETE FROM services WHERE id = $1`, svc)
	require.NoError(t, err)
	require.Zero(t, count())
}
