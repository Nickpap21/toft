package configfile

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct {
	db *pgxpool.Pool
}

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{db: pool}
}

func (r *PostgresRepository) List(ctx context.Context, serviceID string) ([]ConfigFile, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, service_id, path, content, created_at, updated_at
		 FROM service_config_files
		 WHERE service_id = $1
		 ORDER BY path`,
		serviceID,
	)
	if err != nil {
		return nil, fmt.Errorf("list config files: %w", err)
	}
	defer rows.Close()

	result := make([]ConfigFile, 0)
	for rows.Next() {
		var cf ConfigFile
		var id, svcID uuid.UUID
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&id, &svcID, &cf.Path, &cf.Content, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan config file: %w", err)
		}
		cf.ID = id.String()
		cf.ServiceID = svcID.String()
		cf.CreatedAt = createdAt
		cf.UpdatedAt = updatedAt
		result = append(result, cf)
	}
	return result, rows.Err()
}

func (r *PostgresRepository) Upsert(ctx context.Context, serviceID string, in UpsertInput) (ConfigFile, error) {
	var cf ConfigFile
	var id, svcID uuid.UUID
	var createdAt, updatedAt time.Time

	err := r.db.QueryRow(ctx,
		`INSERT INTO service_config_files (service_id, path, content)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (service_id, path) DO UPDATE
		 SET content = EXCLUDED.content, updated_at = NOW()
		 RETURNING id, service_id, path, content, created_at, updated_at`,
		serviceID, in.Path, in.Content,
	).Scan(&id, &svcID, &cf.Path, &cf.Content, &createdAt, &updatedAt)
	if err != nil {
		return ConfigFile{}, fmt.Errorf("upsert config file: %w", err)
	}

	cf.ID = id.String()
	cf.ServiceID = svcID.String()
	cf.CreatedAt = createdAt
	cf.UpdatedAt = updatedAt
	return cf, nil
}

// Delete removes the service's file id and returns its path; "" when the
// service has no file id.
func (r *PostgresRepository) Delete(ctx context.Context, id, serviceID string) (string, error) {
	fileID, err := uuid.Parse(id)
	if err != nil {
		return "", fmt.Errorf("invalid config file id %q: %w", id, err)
	}
	svcID, err := uuid.Parse(serviceID)
	if err != nil {
		return "", fmt.Errorf("invalid service id %q: %w", serviceID, err)
	}
	path, err := sqlcdb.New(r.db).DeleteConfigFile(ctx, sqlcdb.DeleteConfigFileParams{
		ID:        pgtype.UUID{Bytes: fileID, Valid: true},
		ServiceID: pgtype.UUID{Bytes: svcID, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("delete config file: %w", err)
	}
	return path, nil
}
