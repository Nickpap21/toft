package environment

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/internal/dbtx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresRepository implements Repository using a pgxpool connection pool.
type PostgresRepository struct {
	db      *pgxpool.Pool
	queries *sqlcdb.Queries
}

// NewPostgresRepository creates a new PostgresRepository.
func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{db: pool, queries: sqlcdb.New(pool)}
}

// envUUIDStr converts a pgtype.UUID to its string representation.
// Returns an empty string when the UUID is null/invalid.
func envUUIDStr(p pgtype.UUID) string {
	val, _ := p.Value()
	if val == nil {
		return ""
	}
	return val.(string)
}

// scanRow scans a single pgx row into an Environment.
func fromRow(e sqlcdb.Environment) Environment {
	return Environment{
		ID:        envUUIDStr(e.ID),
		ProjectID: envUUIDStr(e.ProjectID),
		Name:      e.Name,
		Color:     e.Color,
		Protected: e.Protected,
		CreatedAt: e.CreatedAt.Time,
		UpdatedAt: e.UpdatedAt.Time,
	}
}

// Create inserts an environment. The service fills Color and Protected.
func (r *PostgresRepository) Create(ctx context.Context, projectID string, input CreateInput) (*Environment, error) {
	parsed, err := uuid.Parse(projectID)
	if err != nil {
		return nil, fmt.Errorf("Create environment: invalid project_id %q: %w", projectID, err)
	}
	color, protected := "gray", false
	if input.Color != nil {
		color = *input.Color
	}
	if input.Protected != nil {
		protected = *input.Protected
	}
	row, err := sqlcdb.New(dbtx.On(ctx, r.db)).CreateEnvironment(ctx, sqlcdb.CreateEnvironmentParams{
		ProjectID: pgtype.UUID{Bytes: parsed, Valid: true},
		Name:      input.Name,
		Color:     color,
		Protected: protected,
	})
	if err != nil {
		return nil, fmt.Errorf("Create environment: %w", err)
	}
	env := fromRow(row)
	return &env, nil
}

// GetByID fetches an environment by its primary key.
func (r *PostgresRepository) GetByID(ctx context.Context, id string) (*Environment, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("GetByID environment: invalid id %q: %w", id, err)
	}
	row, err := r.queries.GetEnvironment(ctx, pgtype.UUID{Bytes: parsed, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("GetByID environment: %w", err)
	}
	env := fromRow(row)
	if pv, err := r.queries.GetEnvironmentPreview(ctx, row.ID); err == nil {
		env.Preview = &PreviewOf{PullNumber: int(pv.Number), RepositoryID: envUUIDStr(pv.RepositoryID)}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("GetByID environment's preview: %w", err)
	}
	return &env, nil
}

// ListByProject returns a project's environments, oldest first.
func (r *PostgresRepository) ListByProject(ctx context.Context, projectID string) ([]Environment, error) {
	parsed, err := uuid.Parse(projectID)
	if err != nil {
		return nil, fmt.Errorf("ListByProject environment: invalid project_id %q: %w", projectID, err)
	}
	// On the transaction inside a project's create (7g), which made the project.
	q := sqlcdb.New(dbtx.On(ctx, r.db))
	rows, err := q.ListProjectEnvironments(ctx, pgtype.UUID{Bytes: parsed, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("ListByProject environment: %w", err)
	}
	previews, err := q.ListProjectPreviewEnvironments(ctx, pgtype.UUID{Bytes: parsed, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("ListByProject environment previews: %w", err)
	}
	of := make(map[string]*PreviewOf, len(previews))
	for _, pv := range previews {
		of[envUUIDStr(pv.EnvironmentID)] = &PreviewOf{PullNumber: int(pv.Number), RepositoryID: envUUIDStr(pv.RepositoryID)}
	}
	result := make([]Environment, 0, len(rows))
	for _, row := range rows {
		env := fromRow(row)
		env.Preview = of[env.ID]
		result = append(result, env)
	}
	return result, nil
}

// Update changes the fields set in input and returns the environment.
func (r *PostgresRepository) Update(ctx context.Context, id string, input UpdateInput) (*Environment, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("Update environment: invalid id %q: %w", id, err)
	}
	row, err := r.queries.UpdateEnvironment(ctx, sqlcdb.UpdateEnvironmentParams{
		ID:           pgtype.UUID{Bytes: parsed, Valid: true},
		Name:         input.Name,
		Color:        input.Color,
		Protected:    input.Protected,
		WasProtected: input.WasProtected,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// No row: the environment is gone, or its protection changed under
		// the compare-and-set.
		if input.WasProtected != nil {
			_, err := r.queries.GetEnvironment(ctx, pgtype.UUID{Bytes: parsed, Valid: true})
			if err == nil {
				return nil, ErrProtectionChanged
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return nil, fmt.Errorf("Update environment: %w", err)
			}
		}
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("Update environment: %w", err)
	}
	env := fromRow(row)
	return &env, nil
}

// Delete removes an environment by ID. Cascades to services via FK.
func (r *PostgresRepository) Delete(ctx context.Context, id string) error {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("Delete environment: invalid id %q: %w", id, err)
	}
	if _, err := r.db.Exec(ctx,
		// Its services go by cascade: their servers' spec_generation
		// rises (D-111).
		`WITH bumped AS (
			UPDATE servers SET spec_generation = spec_generation + 1
			WHERE id IN (SELECT server_id FROM services WHERE environment_id = $1)
		)
		DELETE FROM environments WHERE id = $1`,
		pgtype.UUID{Bytes: parsed, Valid: true},
	); err != nil {
		return fmt.Errorf("Delete environment: %w", err)
	}
	return nil
}

// CheckOwnership returns true when the environment belongs to the given org
// (by joining through the projects table).
func (r *PostgresRepository) CheckOwnership(ctx context.Context, id, orgID string) (bool, error) {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return false, fmt.Errorf("CheckOwnership environment: invalid id %q: %w", id, err)
	}
	parsedOrgID, err := uuid.Parse(orgID)
	if err != nil {
		return false, fmt.Errorf("CheckOwnership environment: invalid org_id %q: %w", orgID, err)
	}

	var exists bool
	err = r.db.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM environments e
			JOIN projects p ON e.project_id = p.id
			WHERE e.id = $1 AND p.org_id = $2
		)`,
		pgtype.UUID{Bytes: parsedID, Valid: true},
		pgtype.UUID{Bytes: parsedOrgID, Valid: true},
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("CheckOwnership environment: %w", err)
	}
	return exists, nil
}

// Overview reads, for each service of the environment, how many variables
// and secrets it sets itself and the shared keys among them, its domains and
// its latest deployment: three queries, joined here by service.
func (r *PostgresRepository) Overview(ctx context.Context, id string) ([]ServiceOverview, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid environment id %q: %w", id, err)
	}
	envID := pgtype.UUID{Bytes: parsed, Valid: true}
	byService := map[string]*ServiceOverview{}
	order := make([]string, 0)
	get := func(sid pgtype.UUID) *ServiceOverview {
		k := envUUIDStr(sid)
		if o, ok := byService[k]; ok {
			return o
		}
		o := &ServiceOverview{ServiceID: k, Overrides: make([]string, 0), Domains: make([]string, 0)}
		byService[k] = o
		order = append(order, k)
		return o
	}

	keys, err := r.queries.ListEnvironmentServiceKeys(ctx, envID)
	if err != nil {
		return nil, fmt.Errorf("ListEnvironmentServiceKeys: %w", err)
	}
	for _, row := range keys {
		o := get(row.ServiceID)
		o.OwnVariables, o.OwnSecrets = int(row.Variables), int(row.Secrets)
		o.Overrides = append(o.Overrides, row.Overrides...)
	}
	domains, err := r.queries.ListEnvironmentDomains(ctx, envID)
	if err != nil {
		return nil, fmt.Errorf("ListEnvironmentDomains: %w", err)
	}
	for _, row := range domains {
		o := get(row.ServiceID)
		o.Domains = append(o.Domains, row.Hostname)
	}
	deps, err := r.queries.ListLatestDeploymentsForEnvironment(ctx, envID)
	if err != nil {
		return nil, fmt.Errorf("ListLatestDeploymentsForEnvironment: %w", err)
	}
	for _, row := range deps {
		d := &LastDeployment{
			ID: envUUIDStr(row.ID), Status: row.Status, CommitSHA: row.CommitSha, ImageTag: row.ImageTag,
			CreatedAt: row.CreatedAt.Time,
		}
		if row.FinishedAt.Valid {
			t := row.FinishedAt.Time
			d.FinishedAt = &t
		}
		get(row.ServiceID).LastDeployment = d
	}

	out := make([]ServiceOverview, 0, len(order))
	for _, k := range order {
		out = append(out, *byService[k])
	}
	return out, nil
}

// ProjectOverview reads a project's environments with their counts, each
// service's domains and latest deployment, and the latest deployments:
// four queries, joined here by service.
func (r *PostgresRepository) ProjectOverview(ctx context.Context, projectID string, recent int32) (*ProjectOverview, error) {
	parsed, err := uuid.Parse(projectID)
	if err != nil {
		return nil, fmt.Errorf("invalid project id %q: %w", projectID, err)
	}
	pid := pgtype.UUID{Bytes: parsed, Valid: true}

	envs, err := r.queries.ListProjectEnvironmentCounts(ctx, pid)
	if err != nil {
		return nil, fmt.Errorf("ListProjectEnvironmentCounts: %w", err)
	}
	o := &ProjectOverview{
		Environments:      make([]EnvironmentCounts, 0, len(envs)),
		Services:          make([]ProjectServiceOverview, 0),
		RecentDeployments: make([]RecentDeployment, 0, recent),
	}
	for _, e := range envs {
		o.Environments = append(o.Environments, EnvironmentCounts{ID: envUUIDStr(e.ID), Name: e.Name, Color: e.Color, Protected: e.Protected, Variables: e.Variables, Secrets: e.Secrets})
	}

	byService := map[string]int{}
	get := func(sid pgtype.UUID) *ProjectServiceOverview {
		k := envUUIDStr(sid)
		if i, ok := byService[k]; ok {
			return &o.Services[i]
		}
		byService[k] = len(o.Services)
		o.Services = append(o.Services, ProjectServiceOverview{ServiceID: k, Domains: make([]string, 0)})
		return &o.Services[len(o.Services)-1]
	}
	domains, err := r.queries.ListProjectDomains(ctx, pid)
	if err != nil {
		return nil, fmt.Errorf("ListProjectDomains: %w", err)
	}
	for _, row := range domains {
		s := get(row.ServiceID)
		s.Domains = append(s.Domains, row.Hostname)
	}
	latest, err := r.queries.ListLatestDeploymentsForProject(ctx, pid)
	if err != nil {
		return nil, fmt.Errorf("ListLatestDeploymentsForProject: %w", err)
	}
	for _, row := range latest {
		get(row.ServiceID).LastDeployment = &LastDeployment{
			ID: envUUIDStr(row.ID), Status: row.Status, CommitSHA: row.CommitSha, ImageTag: row.ImageTag,
			CreatedAt: row.CreatedAt.Time, FinishedAt: timePtr(row.FinishedAt),
		}
	}

	rows, err := r.queries.ListRecentProjectDeployments(ctx, sqlcdb.ListRecentProjectDeploymentsParams{ProjectID: pid, RowLimit: recent})
	if err != nil {
		return nil, fmt.Errorf("ListRecentProjectDeployments: %w", err)
	}
	for _, row := range rows {
		o.RecentDeployments = append(o.RecentDeployments, RecentDeployment{
			ID: envUUIDStr(row.ID), ServiceID: envUUIDStr(row.ServiceID), ServiceName: row.ServiceName,
			EnvironmentName: row.EnvironmentName, Status: row.Status, CommitSHA: row.CommitSha,
			CommitMessage: row.CommitMessage, TriggeredBy: row.TriggeredBy, ImageTag: row.ImageTag,
			CreatedAt: row.CreatedAt.Time, StartedAt: timePtr(row.StartedAt), FinishedAt: timePtr(row.FinishedAt),
		})
	}
	return o, nil
}

// ListByWorkspace returns every environment of a workspace, oldest first.
func (r *PostgresRepository) ListByWorkspace(ctx context.Context, orgID string) ([]WorkspaceEnvironment, error) {
	parsed, err := uuid.Parse(orgID)
	if err != nil {
		return nil, fmt.Errorf("invalid workspace id %q: %w", orgID, err)
	}
	rows, err := r.queries.ListWorkspaceEnvironments(ctx, pgtype.UUID{Bytes: parsed, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("ListWorkspaceEnvironments: %w", err)
	}
	out := make([]WorkspaceEnvironment, 0, len(rows))
	for _, row := range rows {
		out = append(out, WorkspaceEnvironment{ID: envUUIDStr(row.ID), ProjectID: envUUIDStr(row.ProjectID), Name: row.Name, Color: row.Color, Protected: row.Protected, Preview: row.Preview})
	}
	return out, nil
}

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}
