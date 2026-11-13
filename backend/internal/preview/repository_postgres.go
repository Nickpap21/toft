package preview

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
)

// PostgresStore is Store over sqlc.
type PostgresStore struct {
	q *sqlcdb.Queries
}

// NewPostgresStore returns the previews' store.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{q: sqlcdb.New(pool)}
}

func optUUID(s string) pgtype.UUID {
	u, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: u, Valid: true}
}

func uuidStr(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return uuid.UUID(u.Bytes).String()
}

func optText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func text(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

func (r *PostgresStore) Settings(ctx context.Context, projectID string) (Settings, error) {
	row, err := r.q.GetPreviewSettings(ctx, optUUID(projectID))
	if err != nil {
		return Settings{}, fmt.Errorf("GetPreviewSettings: %w", err)
	}
	return Settings{ProjectID: uuidStr(row.ID), OrgID: uuidStr(row.OrgID), EnvironmentID: uuidStr(row.PreviewEnvironmentID),
		EnvironmentName: row.EnvironmentName, Domain: row.PreviewDomain, Max: int(row.PreviewMax)}, nil
}

func (r *PostgresStore) SetSettings(ctx context.Context, projectID, environmentID, domain string, max *int) error {
	var m *int16
	if max != nil {
		v := int16(*max)
		m = &v
	}
	if err := r.q.SetPreviewSettings(ctx, sqlcdb.SetPreviewSettingsParams{ID: optUUID(projectID),
		EnvironmentID: optUUID(environmentID), Domain: optText(domain), Max: m}); err != nil {
		return fmt.Errorf("SetPreviewSettings: %w", err)
	}
	return nil
}

func (r *PostgresStore) Pull(ctx context.Context, pullID string) (Pull, error) {
	row, err := r.q.GetPullForPreview(ctx, optUUID(pullID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Pull{}, ErrPullGone
	}
	if err != nil {
		return Pull{}, fmt.Errorf("GetPullForPreview: %w", err)
	}
	return Pull{ID: uuidStr(row.ID), RepoID: uuidStr(row.RepositoryID), RepoName: row.RepositoryName,
		ProjectID: uuidStr(row.ProjectID), ProjectName: row.ProjectName, OrgID: uuidStr(row.OrgID), Number: int(row.Number),
		Title: row.Title, State: row.State, Base: row.BaseBranch, Head: row.HeadBranch, HeadSHA: row.HeadSha, AuthorID: uuidStr(row.AuthorID),
		AuthorName: row.AuthorName, MergedBy: uuidStr(row.MergedBy), OpenedWithKey: row.OpenedWithKey, KeyPushed: row.KeyPushed,
		APIKeyName: row.ApiKeyName, AgentName: row.AgentName, FirstRepository: row.FirstRepository}, nil
}

// ErrNoPull: no pull request of that number in the repository.
var ErrNoPull = errors.New("no such pull request")

func (r *PostgresStore) PullID(ctx context.Context, projectID, repoID string, number int) (string, error) {
	id, err := r.q.GetPullIDByNumber(ctx, sqlcdb.GetPullIDByNumberParams{ProjectID: optUUID(projectID),
		RepositoryID: optUUID(repoID), Number: int32(number)})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoPull
	}
	if err != nil {
		return "", fmt.Errorf("GetPullIDByNumber: %w", err)
	}
	return uuidStr(id), nil
}

func fromRow(id, pull, project, source, env pgtype.UUID, untrusted bool, head string, approved *string, approvedBy pgtype.UUID,
	approvedAt pgtype.Timestamptz, state string, stopped *string, note string, created, updated pgtype.Timestamptz) Preview {
	return Preview{ID: uuidStr(id), PullID: uuidStr(pull), ProjectID: uuidStr(project), SourceEnvID: uuidStr(source),
		EnvID: uuidStr(env), Untrusted: untrusted, HeadSHA: head, ApprovedSHA: text(approved), ApprovedBy: uuidStr(approvedBy),
		ApprovedAt: timePtr(approvedAt), State: State(state), StoppedReason: text(stopped), Note: note,
		CreatedAt: created.Time, UpdatedAt: updated.Time}
}

func (r *PostgresStore) ByPull(ctx context.Context, pullID string) (*Preview, error) {
	row, err := r.q.GetPreviewByPull(ctx, optUUID(pullID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("GetPreviewByPull: %w", err)
	}
	p := fromRow(row.ID, row.PullRequestID, row.ProjectID, row.SourceEnvironmentID, row.EnvironmentID, row.Untrusted, row.HeadSha,
		row.ApprovedSha, row.ApprovedBy, row.ApprovedAt, row.State, row.StoppedReason, row.Note, row.CreatedAt, row.UpdatedAt)
	p.ApprovedByName, p.MigrationFiles, p.MigrationsUnchecked = row.ApprovedByName, row.MigrationFiles, row.MigrationsUnchecked
	return &p, nil
}

func (r *PostgresStore) SetMigrationFiles(ctx context.Context, id string, files []string, unchecked bool) error {
	if err := r.q.SetPreviewMigrationFiles(ctx, sqlcdb.SetPreviewMigrationFilesParams{ID: optUUID(id), Files: files, Unchecked: unchecked}); err != nil {
		return fmt.Errorf("SetPreviewMigrationFiles: %w", err)
	}
	return nil
}

func (r *PostgresStore) Insert(ctx context.Context, p *Preview) error {
	row, err := r.q.InsertPreview(ctx, sqlcdb.InsertPreviewParams{PullRequestID: optUUID(p.PullID), ProjectID: optUUID(p.ProjectID),
		SourceEnvironmentID: optUUID(p.SourceEnvID), Untrusted: p.Untrusted, HeadSha: p.HeadSHA, State: string(p.State)})
	if err != nil {
		return fmt.Errorf("InsertPreview: %w", err)
	}
	p.ID, p.CreatedAt, p.UpdatedAt = uuidStr(row.ID), row.CreatedAt.Time, row.UpdatedAt.Time
	return nil
}

func (r *PostgresStore) Update(ctx context.Context, p *Preview) error {
	var at pgtype.Timestamptz
	if p.ApprovedAt != nil {
		at = pgtype.Timestamptz{Time: *p.ApprovedAt, Valid: true}
	}
	if err := r.q.UpdatePreview(ctx, sqlcdb.UpdatePreviewParams{ID: optUUID(p.ID), EnvironmentID: optUUID(p.EnvID),
		Untrusted: p.Untrusted, HeadSha: p.HeadSHA, ApprovedSha: optText(p.ApprovedSHA), ApprovedBy: optUUID(p.ApprovedBy),
		ApprovedAt: at, State: string(p.State), StoppedReason: optText(p.StoppedReason), Note: truncate(p.Note, 1000)}); err != nil {
		return fmt.Errorf("UpdatePreview: %w", err)
	}
	return nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func (r *PostgresStore) Delete(ctx context.Context, id string) error {
	if err := r.q.DeletePreview(ctx, optUUID(id)); err != nil {
		return fmt.Errorf("DeletePreview: %w", err)
	}
	return nil
}

func (r *PostgresStore) CountHolding(ctx context.Context, projectID string) (int, error) {
	n, err := r.q.CountHoldingPreviews(ctx, optUUID(projectID))
	if err != nil {
		return 0, fmt.Errorf("CountHoldingPreviews: %w", err)
	}
	return int(n), nil
}

func (r *PostgresStore) WaitingRoom(ctx context.Context, projectID string) ([]string, error) {
	return r.PullsOf(ctx, ScopeWaitingRoom, projectID)
}

func (r *PostgresStore) PullsOf(ctx context.Context, scope Scope, id string) ([]string, error) {
	var (
		rows []pgtype.UUID
		err  error
	)
	switch scope {
	case ScopeSource:
		rows, err = r.q.ListPreviewPullsOfSource(ctx, optUUID(id))
	case ScopeProject:
		rows, err = r.q.ListPreviewPullsOfProject(ctx, optUUID(id))
	case ScopeRepository:
		rows, err = r.q.ListPreviewPullsOfRepository(ctx, optUUID(id))
	case ScopeWaitingRoom:
		rows, err = r.q.ListPreviewPullsWaitingRoom(ctx, optUUID(id))
	default:
		rows, err = r.q.ListAllPreviewPulls(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("list previews (%s): %w", scope, err)
	}
	out := make([]string, 0, len(rows))
	for _, u := range rows {
		out = append(out, uuidStr(u))
	}
	return out, nil
}

func (r *PostgresStore) UnappliedEvents(ctx context.Context, n int) ([]Event, error) {
	rows, err := r.q.ListUnappliedPreviewEvents(ctx, int32(n))
	if err != nil {
		return nil, fmt.Errorf("ListUnappliedPreviewEvents: %w", err)
	}
	out := make([]Event, 0, len(rows))
	for _, row := range rows {
		out = append(out, Event{ID: row.ID, PullID: row.PullRequestID, ActorID: uuidStr(row.ActorID)})
	}
	return out, nil
}

func (r *PostgresStore) MarkApplied(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	if err := r.q.MarkGitEventsPreviews(ctx, ids); err != nil {
		return fmt.Errorf("MarkGitEventsPreviews: %w", err)
	}
	return nil
}

func (r *PostgresStore) SourceServices(ctx context.Context, environmentID string) ([]SourceService, error) {
	rows, err := r.q.ListPreviewSourceServices(ctx, optUUID(environmentID))
	if err != nil {
		return nil, fmt.Errorf("ListPreviewSourceServices: %w", err)
	}
	out := make([]SourceService, 0, len(rows))
	for _, row := range rows {
		out = append(out, SourceService{ID: uuidStr(row.ID), Name: row.Name, SourceType: row.SourceType,
			RepositoryID: row.RepositoryID, BuildPack: row.BuildPack, ServerID: row.ServerID, Volumes: row.Volumes,
			BaseDirectory: row.BaseDirectory, ComposeFilePath: row.ComposeFilePath, MemoryLimit: row.MemoryLimit,
			Type: row.Type, SharedVolumes: row.SharedVolumes, Hostnames: row.Hostnames})
	}
	return out, nil
}

func (r *PostgresStore) Copies(ctx context.Context, environmentID string) ([]CopyService, error) {
	rows, err := r.q.ListPreviewServices(ctx, optUUID(environmentID))
	if err != nil {
		return nil, fmt.Errorf("ListPreviewServices: %w", err)
	}
	out := make([]CopyService, 0, len(rows))
	for _, row := range rows {
		out = append(out, CopyService{ID: uuidStr(row.ID), Name: row.Name, PreviewOf: row.PreviewOf, ServerID: row.ServerID,
			ServerName: row.ServerName, RuntimeStatus: row.RuntimeStatus, SourceType: row.SourceType,
			DeploymentID: row.DeploymentID, DeploymentStatus: row.DeploymentStatus, DeploymentSHA: row.DeploymentSha,
			DeploymentError: row.DeploymentError, DeploymentCreatedAt: timePtr(row.DeploymentCreatedAt),
			DeploymentFinishedAt: timePtr(row.DeploymentFinishedAt), Hostnames: row.Hostnames, Isolation: row.EffectiveIsolation})
	}
	return out, nil
}

// cursor is (created_at, id), base64.
func encodeCursor(t time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(t.UTC().Format(time.RFC3339Nano) + "|" + id))
}

// ErrBadCursor: a cursor this API didn't hand out.
var ErrBadCursor = errors.New("bad cursor")

func decodeCursor(c string) (pgtype.Timestamptz, pgtype.UUID, error) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return pgtype.Timestamptz{}, pgtype.UUID{}, ErrBadCursor
	}
	ts, id, ok := strings.Cut(string(b), "|")
	t, err := time.Parse(time.RFC3339Nano, ts)
	if !ok || err != nil || !optUUID(id).Valid {
		return pgtype.Timestamptz{}, pgtype.UUID{}, ErrBadCursor
	}
	return pgtype.Timestamptz{Time: t, Valid: true}, optUUID(id), nil
}

func (r *PostgresStore) List(ctx context.Context, projectID, cursor string, limit int) ([]Listed, string, error) {
	params := sqlcdb.ListProjectPreviewsParams{ProjectID: optUUID(projectID), N: int32(limit + 1)}
	if cursor != "" {
		t, id, err := decodeCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		params.AfterCreated, params.AfterID = t, id
	}
	rows, err := r.q.ListProjectPreviews(ctx, params)
	if err != nil {
		return nil, "", fmt.Errorf("ListProjectPreviews: %w", err)
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next = encodeCursor(last.CreatedAt.Time, uuidStr(last.ID))
	}
	out := make([]Listed, 0, len(rows))
	for _, row := range rows {
		p := fromRow(row.ID, row.PullRequestID, row.ProjectID, row.SourceEnvironmentID, row.EnvironmentID, row.Untrusted,
			row.HeadSha, row.ApprovedSha, row.ApprovedBy, row.ApprovedAt, row.State, row.StoppedReason, row.Note, row.CreatedAt, row.UpdatedAt)
		p.ApprovedByName, p.MigrationFiles, p.MigrationsUnchecked = row.ApprovedByName, row.MigrationFiles, row.MigrationsUnchecked
		out = append(out, Listed{Preview: p, Number: int(row.Number), Title: row.Title, Branch: row.HeadBranch,
			RepoID: uuidStr(row.RepositoryID), Repo: row.RepositoryName, EnvironmentName: row.EnvironmentName})
	}
	return out, next, nil
}

func (r *PostgresStore) CertificatesSince(ctx context.Context, domain string, since time.Time) (int, error) {
	n, err := r.q.CountPreviewCertificates(ctx, sqlcdb.CountPreviewCertificatesParams{Domain: domain,
		Since: pgtype.Timestamptz{Time: since, Valid: true}})
	if err != nil {
		return 0, fmt.Errorf("CountPreviewCertificates: %w", err)
	}
	return int(n), nil
}

func (r *PostgresStore) RoomCandidates(ctx context.Context, serverID, orgID string) ([]RoomCandidate, error) {
	rows, err := r.q.ListRoomCandidates(ctx, sqlcdb.ListRoomCandidatesParams{ServerID: optUUID(serverID), OrgID: optUUID(orgID)})
	if err != nil {
		return nil, fmt.Errorf("ListRoomCandidates: %w", err)
	}
	out := make([]RoomCandidate, 0)
	for _, row := range rows {
		pull := uuidStr(row.PullRequestID)
		if len(out) == 0 || out[len(out)-1].PullID != pull {
			out = append(out, RoomCandidate{PullID: pull})
		}
		out[len(out)-1].ServiceIDs = append(out[len(out)-1].ServiceIDs, uuidStr(row.ServiceID))
	}
	return out, nil
}

func (r *PostgresStore) ApprovalsNeeded(ctx context.Context, orgID string) ([]Approval, error) {
	rows, err := r.q.ListPreviewApprovalsNeeded(ctx, optUUID(orgID))
	if err != nil {
		return nil, fmt.Errorf("ListPreviewApprovalsNeeded: %w", err)
	}
	out := make([]Approval, 0, len(rows))
	for _, row := range rows {
		out = append(out, Approval{PullID: uuidStr(row.PullRequestID), HeadSHA: row.HeadSha, Number: int(row.Number),
			Title: row.Title, RepoID: uuidStr(row.RepositoryID), Repo: row.RepositoryName, ProjectID: uuidStr(row.ProjectID),
			Project: row.ProjectName, APIKeyName: row.ApiKeyName, AuthorName: row.AuthorName, AgentName: row.AgentName, UpdatedAt: row.UpdatedAt.Time})
	}
	return out, nil
}
