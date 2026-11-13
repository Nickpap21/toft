package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func pgUUIDs(serverID, orgID string) (pgtype.UUID, pgtype.UUID, error) {
	sid, err := uuid.Parse(serverID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, fmt.Errorf("invalid server id format: %w", err)
	}
	oid, err := uuid.Parse(orgID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, fmt.Errorf("invalid org id format: %w", err)
	}
	return pgtype.UUID{Bytes: sid, Valid: true}, pgtype.UUID{Bytes: oid, Valid: true}, nil
}

func (r *postgresRepository) ListShares(ctx context.Context, serverID string) ([]Share, error) {
	sid, err := uuid.Parse(serverID)
	if err != nil {
		return nil, fmt.Errorf("invalid server id format: %w", err)
	}
	rows, err := r.q.ListServerShares(ctx, pgtype.UUID{Bytes: sid, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("ListServerShares: %w", err)
	}
	out := make([]Share, 0, len(rows))
	for _, row := range rows {
		out = append(out, Share{
			OrgID: uuid.UUID(row.OrgID.Bytes).String(), OrgName: row.OrgName,
			CPUCores: row.CpuCores, MemoryMB: row.MemoryMb, CreatedAt: row.CreatedAt.Time,
		})
	}
	return out, nil
}

func (r *postgresRepository) GetShare(ctx context.Context, serverID, orgID string) (*Share, error) {
	sid, oid, err := pgUUIDs(serverID, orgID)
	if err != nil {
		return nil, ErrNoShare
	}
	row, err := r.q.GetServerShare(ctx, sqlcdb.GetServerShareParams{ServerID: sid, OrgID: oid})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoShare
	}
	if err != nil {
		return nil, fmt.Errorf("GetServerShare: %w", err)
	}
	return &Share{OrgID: orgID, CPUCores: row.CpuCores, MemoryMB: row.MemoryMb}, nil
}

func (r *postgresRepository) UpsertShare(ctx context.Context, serverID, orgID string, cpuCores float64, memoryMB int64) error {
	sid, oid, err := pgUUIDs(serverID, orgID)
	if err != nil {
		return err
	}
	if err := r.q.UpsertServerShare(ctx, sqlcdb.UpsertServerShareParams{ServerID: sid, OrgID: oid, CpuCores: cpuCores, MemoryMb: memoryMB}); err != nil {
		return fmt.Errorf("UpsertServerShare: %w", err)
	}
	return nil
}

func (r *postgresRepository) DeleteShare(ctx context.Context, serverID, orgID string) (bool, error) {
	sid, oid, err := pgUUIDs(serverID, orgID)
	if err != nil {
		return false, err
	}
	n, err := r.q.DeleteServerShare(ctx, sqlcdb.DeleteServerShareParams{ServerID: sid, OrgID: oid})
	if err != nil {
		return false, fmt.Errorf("DeleteServerShare: %w", err)
	}
	return n > 0, nil
}

func (r *postgresRepository) CountOrgServicesOnServer(ctx context.Context, serverID, orgID string) (int64, error) {
	sid, oid, err := pgUUIDs(serverID, orgID)
	if err != nil {
		return 0, err
	}
	n, err := r.q.CountOrgServicesOnServer(ctx, sqlcdb.CountOrgServicesOnServerParams{ServerID: sid, OrgID: oid})
	if err != nil {
		return 0, fmt.Errorf("CountOrgServicesOnServer: %w", err)
	}
	return n, nil
}

func (r *postgresRepository) CountServicesOnServer(ctx context.Context, serverID string) (int64, error) {
	sid, err := uuid.Parse(serverID)
	if err != nil {
		return 0, fmt.Errorf("invalid server id format: %w", err)
	}
	n, err := r.q.CountServicesOnServer(ctx, pgtype.UUID{Bytes: sid, Valid: true})
	if err != nil {
		return 0, fmt.Errorf("CountServicesOnServer: %w", err)
	}
	return n, nil
}

func (r *postgresRepository) CountSharedServicesOnServer(ctx context.Context, serverID string) (int64, error) {
	sid, err := uuid.Parse(serverID)
	if err != nil {
		return 0, fmt.Errorf("invalid server id format: %w", err)
	}
	n, err := r.q.CountSharedServicesOnServer(ctx, pgtype.UUID{Bytes: sid, Valid: true})
	if err != nil {
		return 0, fmt.Errorf("CountSharedServicesOnServer: %w", err)
	}
	return n, nil
}

func (r *postgresRepository) ListSharedWithOrg(ctx context.Context, orgID string) ([]SharedServer, error) {
	oid, err := uuid.Parse(orgID)
	if err != nil {
		return nil, fmt.Errorf("invalid org id format: %w", err)
	}
	rows, err := r.db.Query(ctx,
		`SELECT s.id, o.name, sh.cpu_cores::float8, sh.memory_mb, sh.created_at
		 FROM server_shares sh
		 JOIN servers s ON s.id = sh.server_id
		 JOIN organizations o ON o.id = s.org_id
		 WHERE sh.org_id = $1
		 ORDER BY s.name`,
		pgtype.UUID{Bytes: oid, Valid: true},
	)
	if err != nil {
		return nil, fmt.Errorf("list servers shared with org: %w", err)
	}
	type shared struct {
		id    pgtype.UUID
		owner string
		share Share
	}
	var found []shared
	for rows.Next() {
		var x shared
		var created pgtype.Timestamptz
		if err := rows.Scan(&x.id, &x.owner, &x.share.CPUCores, &x.share.MemoryMB, &created); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan shared server: %w", err)
		}
		x.share.OrgID, x.share.CreatedAt = orgID, created.Time
		found = append(found, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]SharedServer, 0, len(found))
	if len(found) == 0 {
		return out, nil
	}
	ids := make([]pgtype.UUID, len(found))
	for i, x := range found {
		ids[i] = x.id
	}
	srvRows, err := r.db.Query(ctx, `SELECT `+serverColumns+` FROM servers WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("load shared servers: %w", err)
	}
	servers, err := r.collectServers(srvRows)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]Server, len(servers))
	for _, srv := range servers {
		byID[srv.ID] = srv
	}
	for _, x := range found {
		if srv, ok := byID[uuid.UUID(x.id.Bytes).String()]; ok {
			out = append(out, SharedServer{Server: srv, OwnerOrgName: x.owner, Share: x.share})
		}
	}
	return out, nil
}
