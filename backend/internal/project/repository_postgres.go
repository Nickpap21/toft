package project

import (
	"context"
	"errors"
	"fmt"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/internal/dbtx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresRepository is the concrete Postgres implementation of project.Repository.
type PostgresRepository struct {
	db      *pgxpool.Pool
	queries *sqlcdb.Queries
}

// NewPostgresRepository creates a new Postgres-backed project repository.
func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{
		db:      pool,
		queries: sqlcdb.New(pool),
	}
}

func (r *PostgresRepository) List(ctx context.Context, orgID string) ([]Project, error) {
	oid, err := uuid.Parse(orgID)
	if err != nil {
		return nil, fmt.Errorf("invalid org id %q: %w", orgID, err)
	}
	rows, err := r.queries.ListOrgProjects(ctx, pgtype.UUID{Bytes: oid, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list projects by org: %w", err)
	}
	projects := make([]Project, 0, len(rows))
	for _, row := range rows {
		p := mapDBProjectToProject(sqlcdb.Project{ID: row.ID, OrgID: row.OrgID, UserID: row.UserID, Name: row.Name,
			Description: row.Description, Icon: row.Icon, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt})
		p.IssueKey, p.OpenIssues, p.OpenPulls, p.DocPages = row.IssueKey, int(row.OpenIssues), int(row.OpenPulls), int(row.DocPages)
		projects = append(projects, p)
	}
	return projects, nil
}

func (r *PostgresRepository) GetByID(ctx context.Context, id string) (*Project, error) {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid project id %q: %w", id, err)
	}
	row, err := r.queries.FetchProjectByID(ctx, pgtype.UUID{Bytes: parsedID, Valid: true})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("project not found")
		}
		return nil, fmt.Errorf("FetchProjectByID: %w", err)
	}
	p := mapDBProjectToProject(row)
	counts, err := r.queries.GetProjectWorkCounts(ctx, row.ID)
	if err != nil {
		return nil, fmt.Errorf("GetProjectWorkCounts: %w", err)
	}
	p.IssueKey, p.OpenIssues, p.OpenPulls, p.DocPages = counts.IssueKey, int(counts.OpenIssues), int(counts.OpenPulls), int(counts.DocPages)
	return &p, nil
}

func (r *PostgresRepository) Create(ctx context.Context, orgID, userID string, input CreateProjectInput) (*Project, error) {
	orgUUID, err := uuid.Parse(orgID)
	if err != nil {
		return nil, fmt.Errorf("invalid org id %q: %w", orgID, err)
	}
	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, fmt.Errorf("invalid user id %q: %w", userID, err)
	}
	// On the caller's transaction when there is one (CreateProjectWith).
	row, err := sqlcdb.New(dbtx.On(ctx, r.db)).CreateProject(ctx, sqlcdb.CreateProjectParams{
		OrgID:       pgtype.UUID{Bytes: orgUUID, Valid: true},
		UserID:      pgtype.UUID{Bytes: userUUID, Valid: true},
		Name:        input.Name,
		Description: orEmpty(input.Description),
		Icon:        orDefault(input.Icon, "▦"),
	})
	if err != nil {
		if isDuplicateNameError(err) {
			return nil, ErrDuplicateName
		}
		return nil, fmt.Errorf("create project: %w", err)
	}
	p := mapDBProjectToProject(row)
	return &p, nil
}

func isDuplicateNameError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (r *PostgresRepository) Update(ctx context.Context, id string, input UpdateProjectInput) (*Project, error) {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid project id %q: %w", id, err)
	}
	row, err := r.queries.UpdateProject(ctx, sqlcdb.UpdateProjectParams{
		ID:          pgtype.UUID{Bytes: parsedID, Valid: true},
		Name:        input.Name,
		Description: orEmpty(input.Description),
		Icon:        orDefault(input.Icon, "▦"),
	})
	if err != nil {
		return nil, fmt.Errorf("UpdateProject: %w", err)
	}
	p := mapDBProjectToProject(row)
	return &p, nil
}

func (r *PostgresRepository) Delete(ctx context.Context, id string) error {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid project id %q: %w", id, err)
	}
	if err := r.queries.DeleteProject(ctx, pgtype.UUID{Bytes: parsedID, Valid: true}); err != nil {
		return fmt.Errorf("DeleteProject: %w", err)
	}
	return nil
}

func (r *PostgresRepository) CheckOwnership(ctx context.Context, id, orgID string) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1 AND org_id = $2)`,
		id, orgID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("CheckOwnership: %w", err)
	}
	return exists, nil
}

// ─── Mapper ───────────────────────────────────────────────────────────────────

func mapDBProjectToProject(row sqlcdb.Project) Project {
	id, _ := row.ID.Value()
	uid, _ := row.UserID.Value()
	oid, _ := row.OrgID.Value()

	idStr := ""
	if id != nil {
		idStr = id.(string)
	}
	uidStr := ""
	if uid != nil {
		uidStr = uid.(string)
	}
	oidStr := ""
	if oid != nil {
		oidStr = oid.(string)
	}

	return Project{
		ID:          idStr,
		OrgID:       oidStr,
		UserID:      uidStr,
		Name:        row.Name,
		Description: row.Description,
		Icon:        row.Icon,
		CreatedAt:   row.CreatedAt.Time,
		UpdatedAt:   row.UpdatedAt.Time,
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func orEmpty(s string) string { return s }

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
