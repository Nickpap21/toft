package reveal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/internal/dbtx"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// column is the sealed column as crypto.AD and reseal name it.
const column = "secret_reveals.sealed"

type postgresRepository struct {
	db   dbtx.DB
	keys *crypto.Keys
}

// NewPostgresRepository stores copies sealed with keys. Keep joins the
// transaction its ctx carries (dbtx), as a service's create does.
func NewPostgresRepository(pool *pgxpool.Pool, keys *crypto.Keys) Repository {
	return &postgresRepository{db: pool, keys: keys}
}

func pgUUID(s string) (pgtype.UUID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return pgtype.UUID{Bytes: u, Valid: true}, nil
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func (r *postgresRepository) q(ctx context.Context) *sqlcdb.Queries {
	return sqlcdb.New(dbtx.On(ctx, r.db))
}

func (r *postgresRepository) Keep(ctx context.Context, serviceID, userID, key, value, reason string) error {
	svc, err := pgUUID(serviceID)
	if err != nil {
		return fmt.Errorf("service id: %w", err)
	}
	user, err := pgUUID(userID)
	if err != nil {
		return fmt.Errorf("user id: %w", err)
	}
	id := uuid.New()
	sealed, err := r.keys.Seal(crypto.AD(column, id.String()), value)
	if err != nil {
		return fmt.Errorf("seal: %w", err)
	}
	if err := r.q(ctx).KeepSecretReveal(ctx, sqlcdb.KeepSecretRevealParams{
		ID: pgtype.UUID{Bytes: id, Valid: true}, ServiceID: svc, UserID: user, SecretKey: key, Sealed: sealed, Reason: reason,
	}); err != nil {
		return fmt.Errorf("KeepSecretReveal: %w", err)
	}
	return nil
}

func (r *postgresRepository) Get(ctx context.Context, serviceID, userID string, since time.Time) (*Reveal, error) {
	svc, err := pgUUID(serviceID)
	if err != nil {
		return nil, nil // not a service: no copy
	}
	user, err := pgUUID(userID)
	if err != nil {
		return nil, fmt.Errorf("user id: %w", err)
	}
	row, err := r.q(ctx).GetSecretReveal(ctx, sqlcdb.GetSecretRevealParams{ServiceID: svc, UserID: user, Since: ts(since)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("GetSecretReveal: %w", err)
	}
	plain, err := r.keys.Open(crypto.AD(column, uuid.UUID(row.ID.Bytes).String()), row.Sealed)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	return &Reveal{Key: row.SecretKey, Reason: row.Reason, CreatedAt: row.CreatedAt.Time, Password: plain}, nil
}

func (r *postgresRepository) Delete(ctx context.Context, serviceID, userID string) (bool, error) {
	svc, err := pgUUID(serviceID)
	if err != nil {
		return false, nil
	}
	user, err := pgUUID(userID)
	if err != nil {
		return false, fmt.Errorf("user id: %w", err)
	}
	n, err := r.q(ctx).DeleteSecretReveal(ctx, sqlcdb.DeleteSecretRevealParams{ServiceID: svc, UserID: user})
	if err != nil {
		return false, fmt.Errorf("DeleteSecretReveal: %w", err)
	}
	return n > 0, nil
}

func (r *postgresRepository) DeleteBefore(ctx context.Context, before time.Time) (int64, error) {
	n, err := r.q(ctx).DeleteOldSecretReveals(ctx, ts(before))
	if err != nil {
		return 0, fmt.Errorf("DeleteOldSecretReveals: %w", err)
	}
	return n, nil
}

func (r *postgresRepository) Forget(ctx context.Context, serviceID, key string) error {
	svc, err := pgUUID(serviceID)
	if err != nil {
		return nil // not a service: no copy
	}
	if err := r.q(ctx).ForgetSecretReveals(ctx, sqlcdb.ForgetSecretRevealsParams{ServiceID: svc, SecretKey: key}); err != nil {
		return fmt.Errorf("ForgetSecretReveals: %w", err)
	}
	return nil
}
