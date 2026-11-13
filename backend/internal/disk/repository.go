package disk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
)

// Repository is what the advisor stores and reads. serverID "" is the
// panel's own server throughout.
type Repository interface {
	// Server is a server's workspace, name, policy and last clean;
	// ErrNotFound when there is none. For "" the workspace is empty.
	Server(ctx context.Context, serverID string) (HostRow, error)
	// Servers is every server, for the alert monitor and the daily clean.
	Servers(ctx context.Context) ([]HostRow, error)
	SetPolicy(ctx context.Context, serverID, actorID string, p Policy) error
	SetLastClean(ctx context.Context, serverID string, c Clean) error
	HostServices(ctx context.Context, serverID string) ([]HostService, error)
	ServicePrefixes(ctx context.Context) (map[string]bool, error)
	LiveDeployments(ctx context.Context, serviceIDs []string) ([]LiveDeployment, error)
	MarkImagesRemoved(ctx context.Context, deploymentIDs []string) error
}

// HostRow is a host as the database knows it.
type HostRow struct {
	ID, OrgID, Name string
	Policy          Policy
	LastClean       *Clean
	Shared          bool // the server is shared with other workspaces (D-39)
}

const (
	panelPolicyKey    = "panel_disk_policy"
	panelLastCleanKey = "panel_disk_last_clean"
)

// PostgresRepository is the Repository on Postgres.
type PostgresRepository struct {
	q *sqlcdb.Queries
}

// NewPostgresRepository returns a Repository on pool.
func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{q: sqlcdb.New(pool)}
}

func pgID(s string) (pgtype.UUID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid id %q: %w", s, err)
	}
	return pgtype.UUID{Bytes: u, Valid: true}, nil
}

func idString(p pgtype.UUID) string {
	if !p.Valid {
		return ""
	}
	return uuid.UUID(p.Bytes).String()
}

func kinds(ss []string) []Kind {
	if ss == nil {
		return nil
	}
	out := make([]Kind, len(ss))
	for i, s := range ss {
		out[i] = Kind(s)
	}
	return out
}

func strs(ks []Kind) []string {
	if ks == nil {
		return nil
	}
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = string(k)
	}
	return out
}

func policyOf(limit *int64, auto []string, alert int32) Policy {
	p := Policy{BuildCacheLimitBytes: limit, AutoClean: kinds(auto)}
	if alert > 0 {
		a := int(alert)
		p.AlertPercent = &a
	}
	return p
}

// panelPolicy is the panel's policy as instance_settings keeps it.
type panelPolicy struct {
	BuildCacheLimitBytes *int64   `json:"build_cache_limit_bytes"`
	AutoClean            []string `json:"auto_clean"`
	AlertPercent         *int     `json:"alert_percent"`
}

func (r *PostgresRepository) setting(ctx context.Context, key string, v any) (bool, error) {
	raw, err := r.q.GetInstanceSetting(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("GetInstanceSetting %s: %w", key, err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return false, fmt.Errorf("decode %s: %w", key, err)
	}
	return true, nil
}

func (r *PostgresRepository) Server(ctx context.Context, serverID string) (HostRow, error) {
	if serverID == "" {
		h := HostRow{}
		var pp panelPolicy
		if _, err := r.setting(ctx, panelPolicyKey, &pp); err != nil {
			return h, err
		}
		h.Policy = Policy{BuildCacheLimitBytes: pp.BuildCacheLimitBytes, AutoClean: kinds(pp.AutoClean), AlertPercent: pp.AlertPercent}
		var c Clean
		if ok, err := r.setting(ctx, panelLastCleanKey, &c); err != nil {
			return h, err
		} else if ok && c.ID != "" {
			h.LastClean = &c
		}
		return h, nil
	}
	id, err := pgID(serverID)
	if err != nil {
		return HostRow{}, ErrNotFound
	}
	row, err := r.q.GetServerDiskPolicy(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return HostRow{}, ErrNotFound
	}
	if err != nil {
		return HostRow{}, fmt.Errorf("GetServerDiskPolicy: %w", err)
	}
	h := HostRow{ID: serverID, OrgID: idString(row.OrgID), Name: row.Name, Policy: policyOf(row.BuildCacheLimitBytes, row.DiskAutoClean, row.DiskAlertPercent), Shared: row.Shared}
	if len(row.DiskLastClean) > 0 {
		var c Clean
		if err := json.Unmarshal(row.DiskLastClean, &c); err != nil {
			return h, fmt.Errorf("decode disk_last_clean: %w", err)
		}
		h.LastClean = &c
	}
	return h, nil
}

func (r *PostgresRepository) Servers(ctx context.Context) ([]HostRow, error) {
	rows, err := r.q.ListServerDiskPolicies(ctx)
	if err != nil {
		return nil, fmt.Errorf("ListServerDiskPolicies: %w", err)
	}
	out := make([]HostRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, HostRow{ID: idString(row.ID), OrgID: idString(row.OrgID), Name: row.Name, Policy: policyOf(row.BuildCacheLimitBytes, row.DiskAutoClean, row.DiskAlertPercent), Shared: row.Shared})
	}
	return out, nil
}

func (r *PostgresRepository) SetPolicy(ctx context.Context, serverID, actorID string, p Policy) error {
	if serverID == "" {
		raw, err := json.Marshal(panelPolicy{BuildCacheLimitBytes: p.BuildCacheLimitBytes, AutoClean: strs(p.AutoClean), AlertPercent: p.AlertPercent})
		if err != nil {
			return err
		}
		by, _ := pgID(actorID)
		if _, err := r.q.MergeInstanceSetting(ctx, sqlcdb.MergeInstanceSettingParams{Key: panelPolicyKey, Value: raw, UpdatedBy: by}); err != nil {
			return fmt.Errorf("MergeInstanceSetting %s: %w", panelPolicyKey, err)
		}
		return nil
	}
	id, err := pgID(serverID)
	if err != nil {
		return err
	}
	var alert *int16
	if p.AlertPercent != nil {
		a := int16(*p.AlertPercent)
		alert = &a
	}
	if err := r.q.SetServerDiskPolicy(ctx, sqlcdb.SetServerDiskPolicyParams{BuildCacheLimitBytes: p.BuildCacheLimitBytes, DiskAutoClean: strs(p.AutoClean), DiskAlertPercent: alert, ID: id}); err != nil {
		return fmt.Errorf("SetServerDiskPolicy: %w", err)
	}
	return nil
}

func (r *PostgresRepository) SetLastClean(ctx context.Context, serverID string, c Clean) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if serverID == "" {
		if _, err := r.q.MergeInstanceSetting(ctx, sqlcdb.MergeInstanceSettingParams{Key: panelLastCleanKey, Value: raw}); err != nil {
			return fmt.Errorf("MergeInstanceSetting %s: %w", panelLastCleanKey, err)
		}
		return nil
	}
	id, err := pgID(serverID)
	if err != nil {
		return err
	}
	if err := r.q.SetServerDiskLastClean(ctx, sqlcdb.SetServerDiskLastCleanParams{DiskLastClean: raw, ID: id}); err != nil {
		return fmt.Errorf("SetServerDiskLastClean: %w", err)
	}
	return nil
}

func (r *PostgresRepository) HostServices(ctx context.Context, serverID string) ([]HostService, error) {
	var id pgtype.UUID
	if serverID != "" {
		var err error
		if id, err = pgID(serverID); err != nil {
			return nil, err
		}
	}
	rows, err := r.q.DiskHostServices(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("DiskHostServices: %w", err)
	}
	out := make([]HostService, 0, len(rows))
	for _, row := range rows {
		out = append(out, HostService{ID: idString(row.ID), Name: row.Name, OrgID: idString(row.OrgID), Kept: int(row.RollbackImagesKept)})
	}
	return out, nil
}

func (r *PostgresRepository) ServicePrefixes(ctx context.Context) (map[string]bool, error) {
	rows, err := r.q.DiskServiceIDPrefixes(ctx)
	if err != nil {
		return nil, fmt.Errorf("DiskServiceIDPrefixes: %w", err)
	}
	out := make(map[string]bool, len(rows))
	for _, p := range rows {
		out[p] = true
	}
	return out, nil
}

func (r *PostgresRepository) LiveDeployments(ctx context.Context, serviceIDs []string) ([]LiveDeployment, error) {
	ids := make([]pgtype.UUID, 0, len(serviceIDs))
	for _, s := range serviceIDs {
		id, err := pgID(s)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	rows, err := r.q.DiskLiveDeployments(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("DiskLiveDeployments: %w", err)
	}
	out := make([]LiveDeployment, 0, len(rows))
	for _, row := range rows {
		out = append(out, LiveDeployment{ID: idString(row.ID), ServiceID: idString(row.ServiceID), ImageTag: row.ImageTag, Rank: int(row.Rank), Removed: row.ImageRemoved})
	}
	return out, nil
}

func (r *PostgresRepository) MarkImagesRemoved(ctx context.Context, deploymentIDs []string) error {
	if len(deploymentIDs) == 0 {
		return nil
	}
	ids := make([]pgtype.UUID, 0, len(deploymentIDs))
	for _, s := range deploymentIDs {
		id, err := pgID(s)
		if err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := r.q.MarkDeploymentImagesRemoved(ctx, ids); err != nil {
		return fmt.Errorf("MarkDeploymentImagesRemoved: %w", err)
	}
	return nil
}
