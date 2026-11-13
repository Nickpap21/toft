package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// backupServerID is serverID as the query parameter: NULL for the panel
// host ("").
func backupServerID(serverID string) (pgtype.UUID, error) {
	if serverID == "" {
		return pgtype.UUID{}, nil
	}
	id, err := uuid.Parse(serverID)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid server id %q: %w", serverID, err)
	}
	return pgtype.UUID{Bytes: id, Valid: true}, nil
}

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

// backupAD is where a backup destination's secret_key or repo_password is
// sealed for (D-126).
func backupAD(column string, id pgtype.UUID) string {
	return crypto.AD("backup_destinations."+column, uuidString(id))
}

// backupDestination decrypts a row.
func (r *postgresRepository) backupDestination(row sqlcdb.BackupDestination) (*BackupDestination, error) {
	secret, err := r.keys.Open(backupAD("secret_key", row.ID), row.SecretKey)
	if err != nil {
		return nil, fmt.Errorf("decrypt the backup secret key: %w", err)
	}
	password, err := r.keys.Open(backupAD("repo_password", row.ID), row.RepoPassword)
	if err != nil {
		return nil, fmt.Errorf("decrypt the backup repository password: %w", err)
	}
	return &BackupDestination{
		ID: uuidString(row.ID), ServerID: uuidString(row.ServerID),
		Endpoint: row.Endpoint, Bucket: row.Bucket, AccessKey: row.AccessKey, SecretKey: secret, RepoPassword: password,
		Schedule: row.Schedule, LastRunAt: timePtr(row.LastRunAt), LastOK: row.LastOk, LastOKAt: timePtr(row.LastOkAt),
		LastBytes: row.LastBytes, LastError: row.LastError, CreatedAt: row.CreatedAt.Time,
	}, nil
}

func (r *postgresRepository) GetBackupDestination(ctx context.Context, serverID string) (*BackupDestination, error) {
	sid, err := backupServerID(serverID)
	if err != nil {
		return nil, err
	}
	row, err := r.q.GetBackupDestination(ctx, sid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoBackupDestination
	}
	if err != nil {
		return nil, fmt.Errorf("GetBackupDestination: %w", err)
	}
	return r.backupDestination(row)
}

func (r *postgresRepository) ListBackupDestinations(ctx context.Context) ([]BackupDestination, error) {
	rows, err := r.q.ListBackupDestinations(ctx)
	if err != nil {
		return nil, fmt.Errorf("ListBackupDestinations: %w", err)
	}
	out := make([]BackupDestination, 0, len(rows))
	for _, row := range rows {
		d, err := r.backupDestination(row)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, nil
}

func (r *postgresRepository) CreateBackupDestination(ctx context.Context, serverID string, in BackupInput, repoPassword string) (*BackupDestination, error) {
	sid, err := backupServerID(serverID)
	if err != nil {
		return nil, err
	}
	// The ID is made here, so the secrets are sealed for it (D-126).
	id := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	secret, err := r.keys.Seal(backupAD("secret_key", id), in.SecretKey)
	if err != nil {
		return nil, fmt.Errorf("encrypt the backup secret key: %w", err)
	}
	password, err := r.keys.Seal(backupAD("repo_password", id), repoPassword)
	if err != nil {
		return nil, fmt.Errorf("encrypt the backup repository password: %w", err)
	}
	row, err := r.q.InsertBackupDestination(ctx, sqlcdb.InsertBackupDestinationParams{
		ID: id, ServerID: sid, Endpoint: in.Endpoint, Bucket: in.Bucket, AccessKey: in.AccessKey,
		SecretKey: secret, RepoPassword: password, Schedule: in.Schedule,
	})
	if err != nil {
		return nil, fmt.Errorf("InsertBackupDestination: %w", err)
	}
	return r.backupDestination(row)
}

func (r *postgresRepository) UpdateBackupDestination(ctx context.Context, id string, in BackupInput) (*BackupDestination, error) {
	pid, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid backup destination id %q: %w", id, err)
	}
	secret := ""
	if in.SecretKey != "" {
		if secret, err = r.keys.Seal(backupAD("secret_key", pgtype.UUID{Bytes: pid, Valid: true}), in.SecretKey); err != nil {
			return nil, fmt.Errorf("encrypt the backup secret key: %w", err)
		}
	}
	row, err := r.q.UpdateBackupDestination(ctx, sqlcdb.UpdateBackupDestinationParams{
		ID: pgtype.UUID{Bytes: pid, Valid: true}, Endpoint: in.Endpoint, Bucket: in.Bucket, AccessKey: in.AccessKey,
		SecretKey: secret, Schedule: in.Schedule,
	})
	if err != nil {
		return nil, fmt.Errorf("UpdateBackupDestination: %w", err)
	}
	return r.backupDestination(row)
}

func (r *postgresRepository) DeleteBackupDestination(ctx context.Context, id string) error {
	pid, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid backup destination id %q: %w", id, err)
	}
	if err := r.q.DeleteBackupDestination(ctx, pgtype.UUID{Bytes: pid, Valid: true}); err != nil {
		return fmt.Errorf("DeleteBackupDestination: %w", err)
	}
	return nil
}

func (r *postgresRepository) RecordBackupRun(ctx context.Context, id string, run BackupRun) error {
	pid, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid backup destination id %q: %w", id, err)
	}
	ok, bytes := run.OK, run.BytesTotal
	if err := r.q.RecordBackupRun(ctx, sqlcdb.RecordBackupRunParams{
		ID: pgtype.UUID{Bytes: pid, Valid: true}, RunAt: pgtype.Timestamptz{Time: run.FinishedAt, Valid: true},
		Ok: &ok, Bytes: &bytes, Error: run.Error,
	}); err != nil {
		return fmt.Errorf("RecordBackupRun: %w", err)
	}
	return nil
}
