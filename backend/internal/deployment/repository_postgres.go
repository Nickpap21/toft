package deployment

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresRepository is the concrete Postgres implementation of deployment.Repository.
type PostgresRepository struct {
	db      *pgxpool.Pool
	queries *sqlcdb.Queries
}

// NewPostgresRepository creates a new Postgres-backed deployment repository.
func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{
		db:      pool,
		queries: sqlcdb.New(pool),
	}
}

func (r *PostgresRepository) Create(ctx context.Context, serviceID, triggeredBy, commitMessage string, gitEventID int64) (*Deployment, error) {
	parsedServiceID, err := uuid.Parse(serviceID)
	if err != nil {
		return nil, fmt.Errorf("invalid service id %q: %w", serviceID, err)
	}

	var event *int64
	if gitEventID > 0 {
		event = &gitEventID
	}
	row, err := r.queries.CreateDeployment(ctx, sqlcdb.CreateDeploymentParams{
		ServiceID:     pgtype.UUID{Bytes: parsedServiceID, Valid: true},
		CommitSha:     nil,
		CommitMessage: optString(commitMessage),
		TriggeredBy:   optString(triggeredBy),
		GitEventID:    event,
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "uq_deployments_git_event" {
		return nil, ErrAlreadyDeployed
	}
	if err != nil {
		return nil, fmt.Errorf("CreateDeployment: %w", err)
	}

	d := mapDBDeploymentToDeployment(row)
	return &d, nil
}

func (r *PostgresRepository) SetSettingsFingerprint(ctx context.Context, id, fingerprint string) error {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid deployment id %q: %w", id, err)
	}
	if err := r.queries.SetDeploymentSettingsFingerprint(ctx, sqlcdb.SetDeploymentSettingsFingerprintParams{
		ID: pgtype.UUID{Bytes: parsed, Valid: true}, SettingsFingerprint: &fingerprint}); err != nil {
		return fmt.Errorf("SetDeploymentSettingsFingerprint: %w", err)
	}
	return nil
}

func (r *PostgresRepository) SetReservedMemory(ctx context.Context, id string, memoryMB float64) error {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid deployment id %q: %w", id, err)
	}
	if err := r.queries.SetDeploymentReservedMemory(ctx, sqlcdb.SetDeploymentReservedMemoryParams{
		ID: pgtype.UUID{Bytes: parsed, Valid: true}, ReservedMemoryMb: memoryMB}); err != nil {
		return fmt.Errorf("SetDeploymentReservedMemory: %w", err)
	}
	return nil
}

func (r *PostgresRepository) ListPulls(ctx context.Context, ids []string) (map[string]PullRef, error) {
	out := map[string]PullRef{}
	if len(ids) == 0 {
		return out, nil
	}
	uuids := make([]pgtype.UUID, 0, len(ids))
	for _, id := range ids {
		if u, err := uuid.Parse(id); err == nil {
			uuids = append(uuids, pgtype.UUID{Bytes: u, Valid: true})
		}
	}
	rows, err := r.queries.ListDeploymentPulls(ctx, uuids)
	if err != nil {
		return nil, fmt.Errorf("ListDeploymentPulls: %w", err)
	}
	for _, row := range rows {
		out[uuid.UUID(row.DeploymentID.Bytes).String()] = PullRef{Number: int(row.Number), RepositoryID: uuid.UUID(row.RepositoryID.Bytes).String(), Issues: []IssueRef{}}
	}
	issues, err := r.queries.ListDeploymentIssues(ctx, uuids)
	if err != nil {
		return nil, fmt.Errorf("ListDeploymentIssues: %w", err)
	}
	for _, row := range issues {
		id := uuid.UUID(row.DeploymentID.Bytes).String()
		if p, ok := out[id]; ok {
			p.Issues = append(p.Issues, IssueRef{Key: fmt.Sprintf("%s-%0*d", row.IssueKey, int(row.Digits), row.Number), Number: int(row.Number), Title: row.Title, Status: row.Status})
			out[id] = p
		}
	}
	return out, nil
}

func (r *PostgresRepository) UpdateStatus(ctx context.Context, id string, status Status, imageTag, commitSHA, errMsg string) (*Deployment, error) {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid deployment id %q: %w", id, err)
	}

	now := time.Now()
	var startedAt, finishedAt pgtype.Timestamptz

	switch status {
	case StatusBuilding:
		startedAt = pgtype.Timestamptz{Time: now, Valid: true}
	case StatusRunning, StatusFailed, StatusStopped:
		finishedAt = pgtype.Timestamptz{Time: now, Valid: true}
	}

	row, err := r.queries.UpdateDeploymentStatus(ctx, sqlcdb.UpdateDeploymentStatusParams{
		ID:           pgtype.UUID{Bytes: parsedID, Valid: true},
		Status:       string(status),
		ImageTag:     optString(imageTag),
		CommitSha:    optString(commitSHA),
		StartedAt:    startedAt,
		FinishedAt:   finishedAt,
		ErrorSummary: optString(errMsg),
	})
	if err != nil {
		return nil, fmt.Errorf("UpdateDeploymentStatus: %w", err)
	}

	d := mapDBDeploymentToDeployment(row)
	return &d, nil
}

func (r *PostgresRepository) SetComposeCommit(ctx context.Context, id, sha string) error {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid deployment id %q: %w", id, err)
	}
	if err := r.queries.SetDeploymentComposeCommit(ctx, sqlcdb.SetDeploymentComposeCommitParams{
		ID:               pgtype.UUID{Bytes: parsedID, Valid: true},
		ComposeCommitSha: sha,
	}); err != nil {
		return fmt.Errorf("SetDeploymentComposeCommit: %w", err)
	}
	return nil
}

func (r *PostgresRepository) SetCommit(ctx context.Context, id, sha, subject string) error {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid deployment id %q: %w", id, err)
	}
	if err := r.queries.SetDeploymentCommit(ctx, sqlcdb.SetDeploymentCommitParams{
		ID:            pgtype.UUID{Bytes: parsedID, Valid: true},
		CommitSha:     sha,
		CommitMessage: subject,
	}); err != nil {
		return fmt.Errorf("SetDeploymentCommit: %w", err)
	}
	return nil
}

func (r *PostgresRepository) ListByServicePaginated(ctx context.Context, serviceID string, limit int32, before *time.Time, beforeID string) ([]Deployment, error) {
	parsedServiceID, err := uuid.Parse(serviceID)
	if err != nil {
		return nil, fmt.Errorf("invalid service id %q: %w", serviceID, err)
	}

	const q = `SELECT id, service_id, status, image_tag, commit_sha, commit_message, triggered_by, queued_at, started_at, finished_at, error_summary, created_at, compose_commit_sha
		FROM deployments WHERE service_id = $1
		  AND ($2::timestamptz IS NULL OR (created_at, id) < ($2::timestamptz, $3::uuid))
		ORDER BY created_at DESC, id DESC LIMIT $4`

	var beforeAt pgtype.Timestamptz
	var beforeUUID pgtype.UUID
	if before != nil {
		beforeAt = pgtype.Timestamptz{Time: *before, Valid: true}
		if id, err := uuid.Parse(beforeID); err == nil {
			beforeUUID = pgtype.UUID{Bytes: id, Valid: true}
		}
	}
	rows, err := r.db.Query(ctx, q, pgtype.UUID{Bytes: parsedServiceID, Valid: true}, beforeAt, beforeUUID, limit)
	if err != nil {
		return nil, fmt.Errorf("ListByServicePaginated: %w", err)
	}
	defer rows.Close()

	result := make([]Deployment, 0)
	for rows.Next() {
		var d sqlcdb.Deployment
		if err := rows.Scan(
			&d.ID, &d.ServiceID, &d.Status, &d.ImageTag, &d.CommitSha, &d.CommitMessage,
			&d.TriggeredBy, &d.QueuedAt, &d.StartedAt, &d.FinishedAt, &d.ErrorSummary, &d.CreatedAt, &d.ComposeCommitSha,
		); err != nil {
			return nil, fmt.Errorf("scan deployment: %w", err)
		}
		result = append(result, mapDBDeploymentToDeployment(d))
	}
	return result, rows.Err()
}

func (r *PostgresRepository) ListByService(ctx context.Context, serviceID string) ([]Deployment, error) {
	parsedServiceID, err := uuid.Parse(serviceID)
	if err != nil {
		return nil, fmt.Errorf("invalid service id %q: %w", serviceID, err)
	}

	rows, err := r.queries.ListRecentDeploymentsForService(ctx, pgtype.UUID{Bytes: parsedServiceID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("ListRecentDeploymentsForService: %w", err)
	}

	return mapDBDeploymentsToDeployments(rows), nil
}

func (r *PostgresRepository) GetByID(ctx context.Context, id string) (*Deployment, error) {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid deployment id %q: %w", id, err)
	}

	row, err := r.queries.FetchDeploymentByID(ctx, pgtype.UUID{Bytes: parsedID, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDeploymentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("FetchDeploymentByID: %w", err)
	}

	d := mapDBDeploymentToDeployment(row)
	return &d, nil
}

func (r *PostgresRepository) InsertLog(ctx context.Context, deploymentID, message string) (*Log, error) {
	parsedID, err := uuid.Parse(deploymentID)
	if err != nil {
		return nil, fmt.Errorf("invalid deployment id %q: %w", deploymentID, err)
	}

	row, err := r.queries.InsertDeploymentLog(ctx, sqlcdb.InsertDeploymentLogParams{
		DeploymentID: pgtype.UUID{Bytes: parsedID, Valid: true},
		Level:        "info",
		Message:      message,
	})
	if err != nil {
		return nil, fmt.Errorf("InsertDeploymentLog: %w", err)
	}

	return &Log{
		ID:           row.ID,
		DeploymentID: uuidString(row.DeploymentID),
		Level:        row.Level,
		Message:      row.Message,
		CreatedAt:    row.CreatedAt.Time,
	}, nil
}

func (r *PostgresRepository) ListLogs(ctx context.Context, deploymentID string) ([]Log, error) {
	parsedID, err := uuid.Parse(deploymentID)
	if err != nil {
		return nil, fmt.Errorf("invalid deployment id %q: %w", deploymentID, err)
	}

	rows, err := r.queries.FetchAllLogsForDeployment(ctx, pgtype.UUID{Bytes: parsedID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("FetchAllLogsForDeployment: %w", err)
	}

	logs := make([]Log, len(rows))
	for i, row := range rows {
		logs[i] = Log{
			ID:           row.ID,
			DeploymentID: uuidString(row.DeploymentID),
			Level:        row.Level,
			Message:      row.Message,
			CreatedAt:    row.CreatedAt.Time,
		}
	}
	return logs, nil
}

func (r *PostgresRepository) ListLogsAfter(ctx context.Context, deploymentID string, afterID int64) ([]Log, error) {
	parsedID, err := uuid.Parse(deploymentID)
	if err != nil {
		return nil, fmt.Errorf("invalid deployment id %q: %w", deploymentID, err)
	}

	rows, err := r.queries.FetchLogsAfterID(ctx, sqlcdb.FetchLogsAfterIDParams{
		DeploymentID: pgtype.UUID{Bytes: parsedID, Valid: true},
		ID:           afterID,
	})
	if err != nil {
		return nil, fmt.Errorf("FetchLogsAfterID: %w", err)
	}

	logs := make([]Log, len(rows))
	for i, row := range rows {
		logs[i] = Log{
			ID:           row.ID,
			DeploymentID: uuidString(row.DeploymentID),
			Level:        row.Level,
			Message:      row.Message,
			CreatedAt:    row.CreatedAt.Time,
		}
	}
	return logs, nil
}

func (r *PostgresRepository) ListLogsFiltered(ctx context.Context, deploymentID, search string) ([]Log, error) {
	parsedID, err := uuid.Parse(deploymentID)
	if err != nil {
		return nil, fmt.Errorf("invalid deployment id %q: %w", deploymentID, err)
	}

	query := `SELECT id, deployment_id, level, message, created_at
	          FROM deployment_logs WHERE deployment_id = $1`
	args := []any{pgtype.UUID{Bytes: parsedID, Valid: true}}
	if search != "" {
		query += ` AND message ILIKE $2`
		args = append(args, "%"+search+"%")
	}
	query += ` ORDER BY id ASC`

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("ListLogsFiltered: %w", err)
	}
	defer rows.Close()

	result := make([]Log, 0)
	for rows.Next() {
		var (
			id             int64
			depID          pgtype.UUID
			level, message string
			createdAt      pgtype.Timestamptz
		)
		if err := rows.Scan(&id, &depID, &level, &message, &createdAt); err != nil {
			return nil, fmt.Errorf("scan log: %w", err)
		}
		result = append(result, Log{
			ID:           id,
			DeploymentID: uuidString(depID),
			Level:        level,
			Message:      message,
			CreatedAt:    createdAt.Time,
		})
	}
	return result, rows.Err()
}

func (r *PostgresRepository) ListServiceIDs(ctx context.Context) ([]string, error) {
	rows, err := r.db.Query(ctx, `SELECT DISTINCT service_id::text FROM deployments`)
	if err != nil {
		return nil, fmt.Errorf("ListServiceIDs: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *PostgresRepository) DeleteOldDeploymentsForService(ctx context.Context, serviceID string, keepLast int) error {
	parsedID, err := uuid.Parse(serviceID)
	if err != nil {
		return fmt.Errorf("invalid service id %q: %w", serviceID, err)
	}

	_, err = r.db.Exec(ctx, `
		DELETE FROM deployments
		WHERE service_id = $1
		  AND id NOT IN (
			SELECT id FROM deployments
			WHERE service_id = $1
			ORDER BY created_at DESC
			LIMIT $2
		  )`,
		pgtype.UUID{Bytes: parsedID, Valid: true},
		keepLast,
	)
	if err != nil {
		return fmt.Errorf("DeleteOldDeploymentsForService: %w", err)
	}
	return nil
}

func (r *PostgresRepository) MarkInterrupted(ctx context.Context, summary string, excludeIDs []string) ([]Deployment, error) {
	exclude := make([]pgtype.UUID, 0, len(excludeIDs))
	for _, id := range excludeIDs {
		parsed, err := uuid.Parse(id)
		if err != nil {
			return nil, fmt.Errorf("invalid deployment id %q: %w", id, err)
		}
		exclude = append(exclude, pgtype.UUID{Bytes: parsed, Valid: true})
	}
	rows, err := r.queries.MarkInterruptedDeployments(ctx, sqlcdb.MarkInterruptedDeploymentsParams{
		ErrorSummary: summary,
		ExcludeIds:   exclude,
	})
	if err != nil {
		return nil, fmt.Errorf("MarkInterruptedDeployments: %w", err)
	}
	return mapDBDeploymentsToDeployments(rows), nil
}

func (r *PostgresRepository) ListUnreconciledInterrupted(ctx context.Context, summary string) ([]Deployment, error) {
	rows, err := r.queries.ListUnreconciledInterruptedDeployments(ctx, summary)
	if err != nil {
		return nil, fmt.Errorf("ListUnreconciledInterruptedDeployments: %w", err)
	}
	return mapDBDeploymentsToDeployments(rows), nil
}

func (r *PostgresRepository) RecordRecovery(ctx context.Context, id, imageTag, summary string) error {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid deployment id %q: %w", id, err)
	}
	if err := r.queries.RecordDeploymentRecovery(ctx, sqlcdb.RecordDeploymentRecoveryParams{
		ImageTag:     optString(imageTag),
		ErrorSummary: summary,
		ID:           pgtype.UUID{Bytes: parsedID, Valid: true},
	}); err != nil {
		return fmt.Errorf("RecordDeploymentRecovery: %w", err)
	}
	return nil
}

func (r *PostgresRepository) Adopt(ctx context.Context, serverID, serviceID, deploymentID, imageTag, triggeredBy string, startedAt time.Time) (bool, error) {
	ids := make([]pgtype.UUID, 3)
	for i, id := range []string{serverID, serviceID, deploymentID} {
		u, err := uuid.Parse(id)
		if err != nil {
			return false, fmt.Errorf("invalid id %q: %w", id, err)
		}
		ids[i] = pgtype.UUID{Bytes: u, Valid: true}
	}
	ok, err := r.queries.AdoptDeployment(ctx, sqlcdb.AdoptDeploymentParams{
		ServerID: ids[0], ServiceID: ids[1], DeploymentID: ids[2],
		ImageTag: optString(imageTag), TriggeredBy: optString(triggeredBy),
		StartedAt: pgtype.Timestamptz{Time: startedAt, Valid: true},
	})
	if err != nil {
		return false, fmt.Errorf("AdoptDeployment: %w", err)
	}
	return ok, nil
}

func (r *PostgresRepository) MarkHandedOff(ctx context.Context, id, imageTag string, input []byte) error {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid deployment id %q: %w", id, err)
	}
	if err := r.queries.MarkDeploymentHandedOff(ctx, sqlcdb.MarkDeploymentHandedOffParams{
		HandedOffInput: input,
		ImageTag:       imageTag,
		ID:             pgtype.UUID{Bytes: parsedID, Valid: true},
	}); err != nil {
		return fmt.Errorf("MarkDeploymentHandedOff: %w", err)
	}
	return nil
}

func (r *PostgresRepository) ClearHandOff(ctx context.Context, id string) error {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid deployment id %q: %w", id, err)
	}
	if err := r.queries.ClearDeploymentHandOff(ctx, pgtype.UUID{Bytes: parsedID, Valid: true}); err != nil {
		return fmt.Errorf("ClearDeploymentHandOff: %w", err)
	}
	return nil
}

func (r *PostgresRepository) IsHandedOff(ctx context.Context, id string) (bool, error) {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return false, fmt.Errorf("invalid deployment id %q: %w", id, err)
	}
	ok, err := r.queries.IsDeploymentHandedOff(ctx, pgtype.UUID{Bytes: parsedID, Valid: true})
	if err != nil {
		return false, fmt.Errorf("IsDeploymentHandedOff: %w", err)
	}
	return ok, nil
}

func (r *PostgresRepository) DeployRunningOnServer(ctx context.Context, serverID string) (bool, error) {
	var id pgtype.UUID
	if serverID != "" {
		parsed, err := uuid.Parse(serverID)
		if err != nil {
			return false, fmt.Errorf("invalid server id %q: %w", serverID, err)
		}
		id = pgtype.UUID{Bytes: parsed, Valid: true}
	}
	running, err := r.queries.DeployRunningOnServer(ctx, id)
	if err != nil {
		return false, fmt.Errorf("DeployRunningOnServer: %w", err)
	}
	return running, nil
}

func (r *PostgresRepository) ListHandedOff(ctx context.Context) ([]HandedOff, error) {
	rows, err := r.queries.ListHandedOffDeployments(ctx)
	if err != nil {
		return nil, fmt.Errorf("ListHandedOffDeployments: %w", err)
	}
	deps := mapDBDeploymentsToDeployments(rows)
	out := make([]HandedOff, 0, len(rows))
	for i, row := range rows {
		out = append(out, HandedOff{Deployment: deps[i], Input: row.HandedOffInput})
	}
	return out, nil
}

func (r *PostgresRepository) CountAgentLogLines(ctx context.Context, id, panelPrefix string) (int, error) {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return 0, fmt.Errorf("invalid deployment id %q: %w", id, err)
	}
	n, err := r.queries.CountAgentLogLines(ctx, sqlcdb.CountAgentLogLinesParams{
		ID:          pgtype.UUID{Bytes: parsedID, Valid: true},
		PanelPrefix: panelPrefix,
	})
	if err != nil {
		return 0, fmt.Errorf("CountAgentLogLines: %w", err)
	}
	return int(n), nil
}

func (r *PostgresRepository) LatestIDForService(ctx context.Context, serviceID string) (string, error) {
	parsedID, err := uuid.Parse(serviceID)
	if err != nil {
		return "", fmt.Errorf("invalid service id %q: %w", serviceID, err)
	}
	id, err := r.queries.LatestDeploymentIDForService(ctx, pgtype.UUID{Bytes: parsedID, Valid: true})
	if err != nil {
		return "", fmt.Errorf("LatestDeploymentIDForService: %w", err)
	}
	return uuidString(id), nil
}

// ─── Mappers ──────────────────────────────────────────────────────────────────

func mapDBDeploymentToDeployment(row sqlcdb.Deployment) Deployment {
	d := Deployment{
		ID:                  uuidString(row.ID),
		ServiceID:           uuidString(row.ServiceID),
		Status:              Status(row.Status),
		ImageTag:            derefString(row.ImageTag),
		CommitSHA:           derefString(row.CommitSha),
		CommitMessage:       derefString(row.CommitMessage),
		TriggeredBy:         derefString(row.TriggeredBy),
		ComposeCommitSHA:    derefString(row.ComposeCommitSha),
		CreatedAt:           row.CreatedAt.Time,
		ImageRemoved:        row.ImageRemovedAt.Valid,
		SettingsFingerprint: derefString(row.SettingsFingerprint),
		ReservedMemoryMB:    row.ReservedMemoryMb,
	}
	if row.StartedAt.Valid {
		t := row.StartedAt.Time
		d.StartedAt = &t
	}
	if row.FinishedAt.Valid {
		t := row.FinishedAt.Time
		d.FinishedAt = &t
	}
	if row.ErrorSummary != nil {
		d.ErrorMsg = *row.ErrorSummary
	}
	return d
}

func mapDBDeploymentsToDeployments(rows []sqlcdb.Deployment) []Deployment {
	deployments := make([]Deployment, len(rows))
	for i, row := range rows {
		deployments[i] = mapDBDeploymentToDeployment(row)
	}
	return deployments
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func uuidString(p pgtype.UUID) string {
	val, _ := p.Value()
	if val == nil {
		return ""
	}
	return val.(string)
}
