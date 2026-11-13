package sharedvolume

import (
	"context"
	"errors"
	"fmt"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/pkg/listversion"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresRepository is the Postgres implementation of Repository.
type PostgresRepository struct {
	pool    *pgxpool.Pool
	queries *sqlcdb.Queries
}

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool, queries: sqlcdb.New(pool)}
}

// pgID parses an ID; a malformed one is ErrNotFound, as an unknown one is.
func pgID(id string) (pgtype.UUID, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return pgtype.UUID{}, ErrNotFound
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}, nil
}

// pgServerID is a server ID, or NULL for the control-plane server ("").
func pgServerID(id string) (pgtype.UUID, error) {
	if id == "" {
		return pgtype.UUID{}, nil
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return pgtype.UUID{}, ErrUnknownServer
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}, nil
}

func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return uuid.UUID(u.Bytes).String()
}

func pgErrCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func fromRow(r sqlcdb.SharedVolume) Volume {
	v := Volume{
		ID: uuidString(r.ID), orgID: uuidString(r.OrgID), ServerID: uuidString(r.ServerID),
		Name: r.Name, SizeLimitBytes: r.SizeLimitBytes, UsedBytes: r.UsedBytes,
		CreatedAt: r.CreatedAt.Time, Mounts: []MountedBy{},
	}
	if r.UsedAt.Valid {
		t := r.UsedAt.Time
		v.UsedAt = &t
	}
	return v
}

func (r *PostgresRepository) List(ctx context.Context, orgID string) ([]Volume, error) {
	org, err := pgID(orgID)
	if err != nil {
		return nil, err
	}
	rows, err := r.queries.ListSharedVolumes(ctx, org)
	if err != nil {
		return nil, fmt.Errorf("ListSharedVolumes: %w", err)
	}
	mounts, err := r.queries.ListSharedVolumeMounts(ctx, org)
	if err != nil {
		return nil, fmt.Errorf("ListSharedVolumeMounts: %w", err)
	}
	byVolume := make(map[string][]MountedBy, len(rows))
	for _, m := range mounts {
		id := uuidString(m.SharedVolumeID)
		byVolume[id] = append(byVolume[id], MountedBy{
			ServiceID: uuidString(m.ServiceID), ServiceName: m.ServiceName, Target: m.Target, ReadOnly: m.ReadOnly,
		})
	}
	out := make([]Volume, 0, len(rows))
	for _, row := range rows {
		v := fromRow(sqlcdb.SharedVolume{
			ID: row.ID, OrgID: row.OrgID, ServerID: row.ServerID, Name: row.Name, SizeLimitBytes: row.SizeLimitBytes,
			UsedBytes: row.UsedBytes, UsedAt: row.UsedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		})
		v.ServerName = row.ServerName
		if m := byVolume[v.ID]; m != nil {
			v.Mounts = m
		}
		out = append(out, v)
	}
	return out, nil
}

func (r *PostgresRepository) Get(ctx context.Context, orgID, id string) (*Volume, error) {
	org, err := pgID(orgID)
	if err != nil {
		return nil, err
	}
	vid, err := pgID(id)
	if err != nil {
		return nil, err
	}
	row, err := r.queries.GetSharedVolume(ctx, sqlcdb.GetSharedVolumeParams{ID: vid, OrgID: org})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("GetSharedVolume: %w", err)
	}
	v := fromRow(row)
	return &v, nil
}

func (r *PostgresRepository) Create(ctx context.Context, orgID string, in CreateInput) (*Volume, error) {
	org, err := pgID(orgID)
	if err != nil {
		return nil, err
	}
	srv, err := pgServerID(in.ServerID)
	if err != nil {
		return nil, err
	}
	row, err := r.queries.CreateSharedVolume(ctx, sqlcdb.CreateSharedVolumeParams{
		OrgID: org, ServerID: srv, Name: in.Name, SizeLimitBytes: in.SizeLimitBytes,
	})
	if pgErrCode(err) == "23505" {
		return nil, ErrNameTaken
	}
	if err != nil {
		return nil, fmt.Errorf("CreateSharedVolume: %w", err)
	}
	v := fromRow(row)
	return &v, nil
}

func (r *PostgresRepository) UpdateLimit(ctx context.Context, orgID, id string, limit int64) (*Volume, error) {
	org, err := pgID(orgID)
	if err != nil {
		return nil, err
	}
	vid, err := pgID(id)
	if err != nil {
		return nil, err
	}
	row, err := r.queries.UpdateSharedVolumeLimit(ctx, sqlcdb.UpdateSharedVolumeLimitParams{ID: vid, OrgID: org, SizeLimitBytes: limit})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("UpdateSharedVolumeLimit: %w", err)
	}
	v := fromRow(row)
	return &v, nil
}

func (r *PostgresRepository) Delete(ctx context.Context, orgID, id string) error {
	org, err := pgID(orgID)
	if err != nil {
		return err
	}
	vid, err := pgID(id)
	if err != nil {
		return err
	}
	n, err := r.queries.DeleteSharedVolume(ctx, sqlcdb.DeleteSharedVolumeParams{ID: vid, OrgID: org})
	if pgErrCode(err) == "23503" {
		return ErrInUse
	}
	if err != nil {
		return fmt.Errorf("DeleteSharedVolume: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PostgresRepository) ServiceMounts(ctx context.Context, serviceID string) ([]Mount, error) {
	sid, err := pgID(serviceID)
	if err != nil {
		return nil, err
	}
	rows, err := r.queries.ListServiceSharedMounts(ctx, sid)
	if err != nil {
		return nil, fmt.Errorf("ListServiceSharedMounts: %w", err)
	}
	out := make([]Mount, 0, len(rows))
	for _, row := range rows {
		out = append(out, Mount{
			VolumeID: uuidString(row.SharedVolumeID), Name: row.Name, Target: row.Target, ReadOnly: row.ReadOnly,
			serverID: uuidString(row.ServerID), orgID: uuidString(row.OrgID),
		})
	}
	return out, nil
}

// ReplaceServiceMounts holds to version the way envvar's BulkUpsert does: the
// service's lock conflicts with the FOR KEY SHARE that adding a mount takes
// through its foreign key, and the rows' locks hold the ones the check saw.
func (r *PostgresRepository) ReplaceServiceMounts(ctx context.Context, serviceID, version string, mounts []Mount) error {
	sid, err := pgID(serviceID)
	if err != nil {
		return err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // a no-op after Commit
	q := r.queries.WithTx(tx)
	if _, err := q.LockServiceForReplace(ctx, sid); err != nil {
		return fmt.Errorf("LockServiceForReplace: %w", err)
	}
	stored, err := q.ListServiceMountsForReplace(ctx, sid)
	if err != nil {
		return fmt.Errorf("ListServiceMountsForReplace: %w", err)
	}
	current := make([]Mount, 0, len(stored))
	for _, row := range stored {
		current = append(current, Mount{VolumeID: uuidString(row.SharedVolumeID), Target: row.Target, ReadOnly: row.ReadOnly})
	}
	if versionOf(current) != version {
		return listversion.ErrChanged
	}
	if err := q.DeleteServiceSharedMounts(ctx, sid); err != nil {
		return fmt.Errorf("DeleteServiceSharedMounts: %w", err)
	}
	for _, m := range mounts {
		vid, err := pgID(m.VolumeID)
		if err != nil {
			return err
		}
		if err := q.InsertServiceSharedMount(ctx, sqlcdb.InsertServiceSharedMountParams{
			ServiceID: sid, SharedVolumeID: vid, Target: m.Target, ReadOnly: m.ReadOnly,
		}); err != nil {
			if pgErrCode(err) == "23503" {
				return ErrNotFound // the volume was deleted meanwhile
			}
			return fmt.Errorf("InsertServiceSharedMount: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (r *PostgresRepository) All(ctx context.Context) ([]Volume, error) {
	rows, err := r.queries.ListAllSharedVolumes(ctx)
	if err != nil {
		return nil, fmt.Errorf("ListAllSharedVolumes: %w", err)
	}
	out := make([]Volume, 0, len(rows))
	for _, row := range rows {
		out = append(out, Volume{
			ID: uuidString(row.ID), orgID: uuidString(row.OrgID), ServerID: uuidString(row.ServerID),
			Name: row.Name, SizeLimitBytes: row.SizeLimitBytes, Mounts: []MountedBy{},
		})
	}
	return out, nil
}

func (r *PostgresRepository) RecordUsage(ctx context.Context, id string, bytes int64) error {
	vid, err := pgID(id)
	if err != nil {
		return err
	}
	if err := r.queries.RecordSharedVolumeUsage(ctx, sqlcdb.RecordSharedVolumeUsageParams{ID: vid, UsedBytes: &bytes}); err != nil {
		return fmt.Errorf("RecordSharedVolumeUsage: %w", err)
	}
	return nil
}
