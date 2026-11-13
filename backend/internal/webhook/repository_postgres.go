package webhook

import (
	"context"
	"errors"
	"fmt"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresRepository is the Postgres implementation of Repository.
type PostgresRepository struct {
	queries *sqlcdb.Queries
}

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{queries: sqlcdb.New(pool)}
}

// projectUUID parses a project ID; a malformed one is ErrUnknownWebhook, so
// the public route answers it like any unknown project.
func projectUUID(id string) (pgtype.UUID, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return pgtype.UUID{}, ErrUnknownWebhook
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}, nil
}

func (r *PostgresRepository) Secret(ctx context.Context, projectID string) (string, error) {
	id, err := projectUUID(projectID)
	if err != nil {
		return "", err
	}
	secret, err := r.queries.GetProjectWebhookSecret(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrUnknownWebhook
	}
	if err != nil {
		return "", fmt.Errorf("GetProjectWebhookSecret: %w", err)
	}
	return secret, nil
}

func (r *PostgresRepository) Ensure(ctx context.Context, projectID, encrypted string) (string, error) {
	id, err := projectUUID(projectID)
	if err != nil {
		return "", err
	}
	secret, err := r.queries.EnsureProjectWebhookSecret(ctx, sqlcdb.EnsureProjectWebhookSecretParams{ProjectID: id, Secret: encrypted})
	if err != nil {
		return "", fmt.Errorf("EnsureProjectWebhookSecret: %w", err)
	}
	return secret, nil
}

func (r *PostgresRepository) LastPush(ctx context.Context, projectID string) (LastPush, error) {
	id, err := projectUUID(projectID)
	if err != nil {
		return LastPush{}, err
	}
	row, err := r.queries.GetProjectWebhookLastPush(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return LastPush{}, nil
	}
	if err != nil {
		return LastPush{}, fmt.Errorf("GetProjectWebhookLastPush: %w", err)
	}
	lp := LastPush{Repo: row.LastPushRepo}
	if row.LastPushAt.Valid {
		lp.At = &row.LastPushAt.Time
	}
	return lp, nil
}

func (r *PostgresRepository) MarkPushed(ctx context.Context, projectID, repo string) error {
	id, err := projectUUID(projectID)
	if err != nil {
		return err
	}
	var name *string
	if repo != "" {
		name = &repo
	}
	if err := r.queries.MarkProjectWebhookPushed(ctx, sqlcdb.MarkProjectWebhookPushedParams{ProjectID: id, Repo: name}); err != nil {
		return fmt.Errorf("MarkProjectWebhookPushed: %w", err)
	}
	return nil
}

func (r *PostgresRepository) Replace(ctx context.Context, projectID, encrypted string) error {
	id, err := projectUUID(projectID)
	if err != nil {
		return err
	}
	if err := r.queries.ReplaceProjectWebhookSecret(ctx, sqlcdb.ReplaceProjectWebhookSecretParams{ProjectID: id, Secret: encrypted}); err != nil {
		return fmt.Errorf("ReplaceProjectWebhookSecret: %w", err)
	}
	return nil
}
