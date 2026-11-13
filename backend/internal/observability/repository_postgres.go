package observability

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/google/uuid"
)

// PostgresRepository implements Repository backed by PostgreSQL.
type PostgresRepository struct {
	db *pgxpool.Pool
	q  *sqlcdb.Queries
}

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{db: pool, q: sqlcdb.New(pool)}
}

func (r *PostgresRepository) Upsert(ctx context.Context, cfg Config) (*Config, error) {
	parsedOrgID, err := uuid.Parse(cfg.OrgID)
	if err != nil {
		return nil, fmt.Errorf("invalid org_id: %w", err)
	}

	var projectID pgtype.UUID
	if cfg.ProjectID != "" {
		pid, parseErr := uuid.Parse(cfg.ProjectID)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid project_id: %w", parseErr)
		}
		projectID = pgtype.UUID{Bytes: pid, Valid: true}
	}

	toUUID := func(s string) pgtype.UUID {
		if s == "" {
			return pgtype.UUID{}
		}
		id, parseErr := uuid.Parse(s)
		if parseErr != nil {
			return pgtype.UUID{}
		}
		return pgtype.UUID{Bytes: id, Valid: true}
	}

	const q = `
		INSERT INTO observability_configs (
			org_id, project_id,
			loki_service_id, grafana_service_id, prometheus_service_id,
			loki_url, grafana_url, prometheus_url
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (org_id, project_id) DO UPDATE SET
			loki_service_id       = COALESCE(EXCLUDED.loki_service_id,       observability_configs.loki_service_id),
			grafana_service_id    = COALESCE(EXCLUDED.grafana_service_id,    observability_configs.grafana_service_id),
			prometheus_service_id = COALESCE(EXCLUDED.prometheus_service_id, observability_configs.prometheus_service_id),
			loki_url              = COALESCE(EXCLUDED.loki_url,              observability_configs.loki_url),
			grafana_url           = COALESCE(EXCLUDED.grafana_url,           observability_configs.grafana_url),
			prometheus_url        = COALESCE(EXCLUDED.prometheus_url,        observability_configs.prometheus_url),
			updated_at            = NOW()
		RETURNING id, org_id, project_id,
			loki_service_id, grafana_service_id, prometheus_service_id,
			loki_url, grafana_url, prometheus_url,
			created_at, updated_at`

	row := r.db.QueryRow(ctx, q,
		pgtype.UUID{Bytes: parsedOrgID, Valid: true},
		projectID,
		toUUID(cfg.LokiServiceID),
		toUUID(cfg.GrafanaServiceID),
		toUUID(cfg.PrometheusServiceID),
		nullableText(cfg.LokiURL),
		nullableText(cfg.GrafanaURL),
		nullableText(cfg.PrometheusURL),
	)
	return scanConfig(row.Scan)
}

func (r *PostgresRepository) GetByScope(ctx context.Context, orgID, projectID string) (*Config, error) {
	parsedOrgID, err := uuid.Parse(orgID)
	if err != nil {
		return nil, fmt.Errorf("invalid org_id: %w", err)
	}

	var q string
	var args []any
	if projectID == "" {
		q = `SELECT id, org_id, project_id,
			loki_service_id, grafana_service_id, prometheus_service_id,
			loki_url, grafana_url, prometheus_url,
			created_at, updated_at
		FROM observability_configs
		WHERE org_id = $1 AND project_id IS NULL LIMIT 1`
		args = []any{pgtype.UUID{Bytes: parsedOrgID, Valid: true}}
	} else {
		parsedProjectID, parseErr := uuid.Parse(projectID)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid project_id: %w", parseErr)
		}
		q = `SELECT id, org_id, project_id,
			loki_service_id, grafana_service_id, prometheus_service_id,
			loki_url, grafana_url, prometheus_url,
			created_at, updated_at
		FROM observability_configs
		WHERE org_id = $1 AND project_id = $2 LIMIT 1`
		args = []any{
			pgtype.UUID{Bytes: parsedOrgID, Valid: true},
			pgtype.UUID{Bytes: parsedProjectID, Valid: true},
		}
	}

	cfg, err := scanConfig(r.db.QueryRow(ctx, q, args...).Scan)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("GetByScope: %w", err)
	}
	return cfg, nil
}

func (r *PostgresRepository) LokiFor(ctx context.Context, orgID, projectID string) (LokiTarget, error) {
	oid, err := uuid.Parse(orgID)
	if err != nil {
		return LokiTarget{}, fmt.Errorf("invalid org_id: %w", err)
	}
	pid := pgtype.UUID{}
	if projectID != "" {
		p, err := uuid.Parse(projectID)
		if err != nil {
			return LokiTarget{}, fmt.Errorf("invalid project_id: %w", err)
		}
		pid = pgtype.UUID{Bytes: p, Valid: true}
	}
	row, err := r.q.GetLokiFor(ctx, sqlcdb.GetLokiForParams{OrgID: pgtype.UUID{Bytes: oid, Valid: true}, ProjectID: pid})
	if errors.Is(err, pgx.ErrNoRows) {
		return LokiTarget{}, nil
	}
	if err != nil {
		return LokiTarget{}, fmt.Errorf("GetLokiFor: %w", err)
	}
	t := LokiTarget{URL: row.LokiUrl, ServiceID: uuidStr(row.LokiServiceID), ServerID: uuidStr(row.LokiServerID), Server: row.LokiServerName}
	if row.LoopbackPort != nil {
		t.Port = int(*row.LoopbackPort)
	}
	return t, nil
}

func (r *PostgresRepository) ListProjects(ctx context.Context, orgID string) ([]ProjectSetting, error) {
	oid, err := uuid.Parse(orgID)
	if err != nil {
		return nil, fmt.Errorf("invalid org_id: %w", err)
	}
	rows, err := r.q.ListProjectObservability(ctx, pgtype.UUID{Bytes: oid, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("ListProjectObservability: %w", err)
	}
	out := make([]ProjectSetting, 0, len(rows))
	for _, row := range rows {
		id, _ := uuid.FromBytes(row.ID.Bytes[:])
		out = append(out, ProjectSetting{ProjectID: id.String(), Name: row.Name, LokiURL: row.LokiUrl, GrafanaURL: row.GrafanaUrl})
	}
	return out, nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func scanConfig(scanFn func(dest ...any) error) (*Config, error) {
	var (
		id, orgID                          pgtype.UUID
		projectID                          pgtype.UUID
		lokiSvcID, grafanaSvcID, promSvcID pgtype.UUID
		lokiURL, grafanaURL, prometheusURL *string
		createdAt, updatedAt               pgtype.Timestamptz
	)
	if err := scanFn(
		&id, &orgID, &projectID,
		&lokiSvcID, &grafanaSvcID, &promSvcID,
		&lokiURL, &grafanaURL, &prometheusURL,
		&createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}

	cfg := &Config{
		ID:                  uuidStr(id),
		OrgID:               uuidStr(orgID),
		ProjectID:           uuidStr(projectID),
		LokiServiceID:       uuidStr(lokiSvcID),
		GrafanaServiceID:    uuidStr(grafanaSvcID),
		PrometheusServiceID: uuidStr(promSvcID),
		LokiURL:             derefStr(lokiURL),
		GrafanaURL:          derefStr(grafanaURL),
		PrometheusURL:       derefStr(prometheusURL),
		CreatedAt:           createdAt.Time,
		UpdatedAt:           updatedAt.Time,
	}
	return cfg, nil
}

func uuidStr(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", u.Bytes[0:4], u.Bytes[4:6], u.Bytes[6:8], u.Bytes[8:10], u.Bytes[10:16])
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nullableText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (r *PostgresRepository) Unregister(ctx context.Context, orgID, kind string) error {
	oid, err := uuid.Parse(orgID)
	if err != nil {
		return fmt.Errorf("invalid org_id: %w", err)
	}
	if err := r.q.UnregisterWorkspaceObservability(ctx, sqlcdb.UnregisterWorkspaceObservabilityParams{
		OrgID: pgtype.UUID{Bytes: oid, Valid: true}, Kind: kind,
	}); err != nil {
		return fmt.Errorf("UnregisterWorkspaceObservability: %w", err)
	}
	return nil
}
