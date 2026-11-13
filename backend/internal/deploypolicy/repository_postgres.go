package deploypolicy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/pkg/listversion"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type postgresRepository struct {
	db *pgxpool.Pool
}

// NewPostgresRepository returns a Repository backed by PostgreSQL (raw pgx,
// like internal/org; no sqlc queries).
func NewPostgresRepository(db *pgxpool.Pool) Repository {
	return &postgresRepository{db: db}
}

// serviceOrgSQL resolves a service's workspace the same way
// service.CheckOwnership does: through its environment's project, its own
// project, or scoped_org_id for org-scoped services.
const serviceOrgSQL = `
SELECT (CASE WHEN s.scope = 'org' THEN s.scoped_org_id ELSE p.org_id END)::text
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
WHERE s.id = $1`

// exceptionRow is either sqlc row of an exception (same columns).
type exceptionRow = sqlcdb.GetPolicyExceptionRow

func toException(r exceptionRow) (*Exception, error) {
	values, err := decodeValues(r.AllowedValues)
	if err != nil {
		return nil, fmt.Errorf("read the exception's values: %w", err)
	}
	return &Exception{
		ServiceID: r.ServiceID, AllowedRules: toRules(r.AllowedRules), Reason: r.Reason,
		GrantedBy: r.GrantedBy, GrantedByName: r.GrantedByName, GrantedByEmail: r.GrantedByEmail,
		CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
		AllowedValues: values, AnyValue: r.AllowedValues == nil,
	}, nil
}

// storedValue is a PinnedValue as the database keeps it, with its
// fingerprint (the API never shows one).
type storedValue struct {
	PinnedValue
	Fingerprint string `json:"fingerprint"`
}

// encodeValues is nil (SQL NULL) for nil: no values, any value allowed.
func encodeValues(vs []PinnedValue) ([]byte, error) {
	if vs == nil {
		return nil, nil
	}
	out := make([]storedValue, len(vs))
	for i, v := range vs {
		out[i] = storedValue{PinnedValue: v, Fingerprint: v.Fingerprint}
	}
	return json.Marshal(out)
}

func decodeValues(raw []byte) ([]PinnedValue, error) {
	if raw == nil {
		return nil, nil
	}
	var stored []storedValue
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, err
	}
	out := make([]PinnedValue, len(stored))
	for i, v := range stored {
		out[i] = v.PinnedValue
		out[i].Fingerprint = v.Fingerprint
	}
	return out, nil
}

func pgUUID(s string) pgtype.UUID {
	id, err := uuid.Parse(s)
	return pgtype.UUID{Bytes: id, Valid: err == nil}
}

func toRules(ss []string) []Rule {
	out := make([]Rule, len(ss))
	for i, s := range ss {
		out[i] = Rule(s)
	}
	return out
}

func fromRules(rs []Rule) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = string(r)
	}
	return out
}

func (r *postgresRepository) GetException(ctx context.Context, serviceID string) (*Exception, error) {
	if _, err := uuid.Parse(serviceID); err != nil {
		return nil, ErrNotFound
	}
	row, err := sqlcdb.New(r.db).GetPolicyException(ctx, pgUUID(serviceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get deploy policy exception: %w", err)
	}
	return toException(row)
}

func (r *postgresRepository) ListExceptions(ctx context.Context) (map[string]*Exception, error) {
	rows, err := sqlcdb.New(r.db).ListPolicyExceptions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list deploy policy exceptions: %w", err)
	}
	out := make(map[string]*Exception, len(rows))
	for _, row := range rows {
		e, err := toException(exceptionRow(row))
		if err != nil {
			return nil, err
		}
		out[e.ServiceID] = e
	}
	return out, nil
}

// auditMetadata is audit_log.metadata for exception changes.
type auditMetadata struct {
	AllowedRules  []Rule        `json:"allowed_rules"`
	AllowedValues []PinnedValue `json:"allowed_values,omitempty"`
	Reason        string        `json:"reason"`
}

func (r *postgresRepository) GrantException(ctx context.Context, g Grant) (*Exception, error) {
	if _, err := uuid.Parse(g.ServiceID); err != nil {
		return nil, ErrServiceNotFound
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var orgID *string
	err = tx.QueryRow(ctx, serviceOrgSQL, g.ServiceID).Scan(&orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrServiceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolve service workspace: %w", err)
	}
	if orgID == nil || *orgID == "" {
		return nil, ErrNoWorkspace
	}

	// As envvar's BulkUpsert: the service's lock conflicts with the FOR KEY
	// SHARE of a first grant's foreign key, and the row's lock holds the one
	// the check saw.
	q := sqlcdb.New(tx)
	if _, err := q.LockServiceForReplace(ctx, pgUUID(g.ServiceID)); err != nil {
		return nil, fmt.Errorf("LockServiceForReplace: %w", err)
	}
	var current *Exception
	row, err := q.GetPolicyExceptionForReplace(ctx, pgUUID(g.ServiceID))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return nil, fmt.Errorf("read the exception to replace: %w", err)
	default:
		current = &Exception{AllowedRules: toRules(row.AllowedRules), UpdatedAt: row.UpdatedAt.Time}
	}
	if versionOf(current) != g.Version {
		return nil, listversion.ErrChanged
	}

	values, err := encodeValues(g.AllowedValues)
	if err != nil {
		return nil, fmt.Errorf("encode the exception's values: %w", err)
	}
	if err := q.UpsertPolicyException(ctx, sqlcdb.UpsertPolicyExceptionParams{
		ServiceID: pgUUID(g.ServiceID), AllowedRules: fromRules(g.AllowedRules), Reason: g.Reason,
		GrantedBy: pgUUID(g.ActorID), AllowedValues: values,
	}); err != nil {
		return nil, fmt.Errorf("upsert deploy policy exception: %w", err)
	}

	meta, err := json.Marshal(auditMetadata{AllowedRules: g.AllowedRules, AllowedValues: g.AllowedValues, Reason: g.Reason})
	if err != nil {
		return nil, fmt.Errorf("encode audit metadata: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO audit_log (org_id, actor_id, action, resource, metadata) VALUES ($1, $2, $3, $4, $5)`,
		*orgID, g.ActorID, ActionExceptionGranted, "service:"+g.ServiceID, meta,
	); err != nil {
		return nil, fmt.Errorf("write audit log: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}
	return r.GetException(ctx, g.ServiceID)
}

// RevokeException never fails for lack of a workspace: removing an
// exception only takes power away. When the service resolves to no
// workspace the audit row is skipped (audit_log.org_id is NOT NULL).
func (r *postgresRepository) RevokeException(ctx context.Context, serviceID, actorID, reason string) (*Exception, error) {
	if _, err := uuid.Parse(serviceID); err != nil {
		return nil, ErrNotFound
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var (
		e     Exception
		rules []string
	)
	err = tx.QueryRow(ctx, `
		DELETE FROM service_deploy_policy_exceptions WHERE service_id = $1
		RETURNING service_id::text, allowed_rules, reason, COALESCE(granted_by::text, ''), created_at, updated_at`,
		serviceID,
	).Scan(&e.ServiceID, &rules, &e.Reason, &e.GrantedBy, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("delete deploy policy exception: %w", err)
	}
	e.AllowedRules = toRules(rules)

	var orgID *string
	if err := tx.QueryRow(ctx, serviceOrgSQL, serviceID).Scan(&orgID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("resolve service workspace: %w", err)
	}
	if orgID != nil && *orgID != "" {
		meta, err := json.Marshal(auditMetadata{AllowedRules: e.AllowedRules, Reason: reason})
		if err != nil {
			return nil, fmt.Errorf("encode audit metadata: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO audit_log (org_id, actor_id, action, resource, metadata) VALUES ($1, $2, $3, $4, $5)`,
			*orgID, actorID, ActionExceptionRevoked, "service:"+serviceID, meta,
		); err != nil {
			return nil, fmt.Errorf("write audit log: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}
	return &e, nil
}

// storedService builds a StoredService from either sqlc row (they have the
// same columns).
func storedService(r sqlcdb.GetPolicyServiceRow) StoredService {
	return StoredService{
		ID: r.ID, Name: r.Name, SourceType: r.SourceType, ComposeContent: r.ComposeContent,
		ComposeRepoURL: r.ComposeRepoUrl, Volumes: parseVolumes(r.Volumes), PreDeployCommands: r.PreDeployCommands,
		ServerID: r.ServerID, ServerName: r.ServerName, OrgID: r.OrgID, OrgName: r.OrgName,
		ProjectID: r.ProjectID, ProjectName: r.ProjectName, EnvironmentName: r.EnvironmentName,
		DockerImage: r.DockerImage, GitRepoURL: r.GitRepoUrl, RepositoryID: r.RepositoryID,
		Hostnames: r.Hostnames,
	}
}

func toLastCheck(commitSHA string, checkedAt pgtype.Timestamptz, raw []byte) (*LastCheck, error) {
	values, err := decodeValues(raw)
	if err != nil {
		return nil, fmt.Errorf("read the deploy check: %w", err)
	}
	if values == nil {
		values = []PinnedValue{}
	}
	return &LastCheck{CommitSHA: commitSHA, CheckedAt: checkedAt.Time, Values: values}, nil
}

func (r *postgresRepository) GetLastCheck(ctx context.Context, serviceID string) (*LastCheck, error) {
	if _, err := uuid.Parse(serviceID); err != nil {
		return nil, ErrNotFound
	}
	row, err := sqlcdb.New(r.db).GetPolicyCheck(ctx, pgUUID(serviceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get deploy check: %w", err)
	}
	return toLastCheck(row.CommitSha, row.CheckedAt, row.Violations)
}

func (r *postgresRepository) ListLastChecks(ctx context.Context) (map[string]*LastCheck, error) {
	rows, err := sqlcdb.New(r.db).ListPolicyChecks(ctx)
	if err != nil {
		return nil, fmt.Errorf("list deploy checks: %w", err)
	}
	out := make(map[string]*LastCheck, len(rows))
	for _, row := range rows {
		c, err := toLastCheck(row.CommitSha, row.CheckedAt, row.Violations)
		if err != nil {
			return nil, err
		}
		out[row.ServiceID] = c
	}
	return out, nil
}

func (r *postgresRepository) RepinValues(ctx context.Context, serviceID string, old, keyed []string) error {
	if _, err := sqlcdb.New(r.db).RepinPolicyValues(ctx, sqlcdb.RepinPolicyValuesParams{ServiceID: pgUUID(serviceID), OldFps: old, NewFps: keyed}); err != nil {
		return fmt.Errorf("repin deploy policy values: %w", err)
	}
	return nil
}

func (r *postgresRepository) SaveLastCheck(ctx context.Context, serviceID, commitSHA string, values []PinnedValue) error {
	if values == nil {
		values = []PinnedValue{}
	}
	raw, err := encodeValues(values)
	if err != nil {
		return fmt.Errorf("encode the deploy check: %w", err)
	}
	if err := sqlcdb.New(r.db).UpsertPolicyCheck(ctx, sqlcdb.UpsertPolicyCheckParams{
		ServiceID: pgUUID(serviceID), CommitSha: commitSHA, Violations: raw,
	}); err != nil {
		return fmt.Errorf("save deploy check: %w", err)
	}
	return nil
}

// parseVolumes reads services.volumes (a JSON array of service.VolumeMount).
// Unparseable JSON is reported as one volume with an unknown host path, so a
// corrupt row cannot hide a bind mount.
func parseVolumes(raw string) []VolumeSpec {
	var vols []struct {
		Name     string `json:"name"`
		Target   string `json:"target"`
		HostPath string `json:"host_path"`
	}
	if raw == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(raw), &vols); err != nil {
		return []VolumeSpec{{Name: "unparseable", HostPath: "(unreadable volumes column)"}}
	}
	out := make([]VolumeSpec, len(vols))
	for i, v := range vols {
		out[i] = VolumeSpec{Name: v.Name, Target: v.Target, HostPath: v.HostPath}
	}
	return out
}

func (r *postgresRepository) GetStoredService(ctx context.Context, serviceID string) (*StoredService, error) {
	if _, err := uuid.Parse(serviceID); err != nil {
		return nil, ErrServiceNotFound
	}
	id, err := uuid.Parse(serviceID)
	if err != nil {
		return nil, ErrServiceNotFound
	}
	row, err := sqlcdb.New(r.db).GetPolicyService(ctx, pgtype.UUID{Bytes: id, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrServiceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get service: %w", err)
	}
	s := storedService(row)
	return &s, nil
}

func (r *postgresRepository) ListStoredServices(ctx context.Context) ([]StoredService, error) {
	rows, err := sqlcdb.New(r.db).ListPolicyServices(ctx)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	out := make([]StoredService, 0, len(rows))
	for _, row := range rows {
		out = append(out, storedService(sqlcdb.GetPolicyServiceRow(row)))
	}
	return out, nil
}

func (r *postgresRepository) ListEnvKeys(ctx context.Context, serviceID string) (map[string][]string, error) {
	return r.pairs(ctx, `SELECT service_id::text, key FROM env_vars`, serviceID, "key")
}

func (r *postgresRepository) ListConfigFilePaths(ctx context.Context, serviceID string) (map[string][]string, error) {
	return r.pairs(ctx, `SELECT service_id::text, path FROM service_config_files`, serviceID, "path")
}

// pairs runs a (service_id, value) query, optionally for one service.
func (r *postgresRepository) pairs(ctx context.Context, q, serviceID, orderBy string) (map[string][]string, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if serviceID != "" {
		if _, perr := uuid.Parse(serviceID); perr != nil {
			return map[string][]string{}, nil
		}
		rows, err = r.db.Query(ctx, q+` WHERE service_id = $1 ORDER BY `+orderBy, serviceID)
	} else {
		rows, err = r.db.Query(ctx, q+` ORDER BY service_id, `+orderBy)
	}
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var id, v string
		if err := rows.Scan(&id, &v); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out[id] = append(out[id], v)
	}
	return out, rows.Err()
}
