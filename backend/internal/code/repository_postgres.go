package code

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

type postgresRepository struct {
	pool *pgxpool.Pool
	q    *sqlcdb.Queries
}

func NewPostgresRepository(pool *pgxpool.Pool) Repository {
	return &postgresRepository{pool: pool, q: sqlcdb.New(pool)}
}

func pgUUID(s string) (pgtype.UUID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return pgtype.UUID{Bytes: u, Valid: true}, nil
}

func uuidStr(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return uuid.UUID(u.Bytes).String()
}

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

func (r *postgresRepository) Unnamed(ctx context.Context) ([]Unnamed, error) {
	rows, err := r.q.ListProjectsWithoutRepository(ctx)
	if err != nil {
		return nil, fmt.Errorf("ListProjectsWithoutRepository: %w", err)
	}
	out := make([]Unnamed, 0, len(rows))
	for _, row := range rows {
		u := Unnamed{ProjectID: uuidStr(row.ProjectID), OrgID: uuidStr(row.OrgID), ProjectName: row.ProjectName, OrgName: row.OrgName}
		if row.OrgSlug != nil {
			u.OrgSlug = *row.OrgSlug
		}
		out = append(out, u)
	}
	return out, nil
}

func (r *postgresRepository) TakenSlugs(ctx context.Context) ([]string, error) {
	slugs, err := r.q.ListOrgSlugs(ctx)
	if err != nil {
		return nil, fmt.Errorf("ListOrgSlugs: %w", err)
	}
	return slugs, nil
}

func (r *postgresRepository) TakenNames(ctx context.Context, orgIDs []string) (map[string][]string, error) {
	ids := make([]pgtype.UUID, 0, len(orgIDs))
	for _, id := range orgIDs {
		u, err := pgUUID(id)
		if err != nil {
			return nil, fmt.Errorf("invalid workspace id: %w", err)
		}
		ids = append(ids, u)
	}
	rows, err := r.q.ListRepositoryNamesIn(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("ListRepositoryNamesIn: %w", err)
	}
	out := map[string][]string{}
	for _, row := range rows {
		out[uuidStr(row.OrgID)] = append(out[uuidStr(row.OrgID)], row.Name)
	}
	return out, nil
}

func (r *postgresRepository) Name(ctx context.Context, slugs map[string]string, repos []NewRepo) error {
	slugIDs := make([]pgtype.UUID, 0, len(slugs))
	slugValues := make([]string, 0, len(slugs))
	for id, slug := range slugs {
		u, err := pgUUID(id)
		if err != nil {
			return fmt.Errorf("invalid workspace id: %w", err)
		}
		slugIDs, slugValues = append(slugIDs, u), append(slugValues, slug)
	}
	orgIDs := make([]pgtype.UUID, 0, len(repos))
	projectIDs := make([]pgtype.UUID, 0, len(repos))
	names := make([]string, 0, len(repos))
	for _, n := range repos {
		o, err := pgUUID(n.OrgID)
		if err != nil {
			return fmt.Errorf("invalid workspace id: %w", err)
		}
		p, err := pgUUID(n.ProjectID)
		if err != nil {
			return fmt.Errorf("invalid project id: %w", err)
		}
		orgIDs, projectIDs, names = append(orgIDs, o), append(projectIDs, p), append(names, n.Name)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := r.q.WithTx(tx)
	if len(slugIDs) > 0 {
		if err := q.SetOrgSlugs(ctx, sqlcdb.SetOrgSlugsParams{Ids: slugIDs, Slugs: slugValues}); err != nil {
			return fmt.Errorf("SetOrgSlugs: %w", err)
		}
	}
	if len(names) > 0 {
		if err := q.InsertRepositories(ctx, sqlcdb.InsertRepositoriesParams{OrgIds: orgIDs, ProjectIds: projectIDs, Names: names}); err != nil {
			return fmt.Errorf("InsertRepositories: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (r *postgresRepository) ListProjectRepos(ctx context.Context, projectID string) ([]StoredRepo, error) {
	pid, err := pgUUID(projectID)
	if err != nil {
		return nil, ErrNotFound
	}
	rows, err := r.q.ListProjectRepositories(ctx, pid)
	if err != nil {
		return nil, fmt.Errorf("ListProjectRepositories: %w", err)
	}
	out := make([]StoredRepo, 0, len(rows))
	for _, row := range rows {
		out = append(out, StoredRepo{ID: uuidStr(row.ID), Name: row.Name, OrgSlug: row.OrgSlug, OrgID: uuidStr(row.OrgID), CreatedAt: row.CreatedAt.Time,
			Protect: row.ProtectDefaultBranch, ProtectedBranch: str(row.ProtectedBranch), OpenPulls: int(row.OpenPulls), Mirrored: row.Mirrored})
	}
	return out, nil
}

func (r *postgresRepository) RepoByPath(ctx context.Context, orgSlug, name string) (*Target, error) {
	row, err := r.q.GetRepositoryByPath(ctx, sqlcdb.GetRepositoryByPathParams{OrgSlug: &orgSlug, Name: name})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("GetRepositoryByPath: %w", err)
	}
	return &Target{ID: uuidStr(row.ID), OrgID: uuidStr(row.OrgID), ProjectID: uuidStr(row.ProjectID), ProtectDefault: row.ProtectDefaultBranch,
		ProtectedBranch: str(row.ProtectedBranch), MirrorURL: row.MirrorUrl}, nil
}

func toKey(id, title, fingerprint, keyType string, lastUsed, created pgtype.Timestamptz) *SSHKey {
	return &SSHKey{ID: id, Title: title, Fingerprint: fingerprint, KeyType: keyType, LastUsedAt: timePtr(lastUsed), CreatedAt: created.Time}
}

func (r *postgresRepository) ListKeys(ctx context.Context, userID string) ([]SSHKey, error) {
	uid, err := pgUUID(userID)
	if err != nil {
		return nil, fmt.Errorf("invalid user id: %w", err)
	}
	rows, err := r.q.ListSSHKeys(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("ListSSHKeys: %w", err)
	}
	out := make([]SSHKey, 0, len(rows))
	for _, row := range rows {
		out = append(out, *toKey(uuidStr(row.ID), row.Title, row.Fingerprint, row.KeyType, row.LastUsedAt, row.CreatedAt))
	}
	return out, nil
}

func (r *postgresRepository) CountKeys(ctx context.Context, userID string) (int64, error) {
	uid, err := pgUUID(userID)
	if err != nil {
		return 0, fmt.Errorf("invalid user id: %w", err)
	}
	n, err := r.q.CountSSHKeys(ctx, uid)
	if err != nil {
		return 0, fmt.Errorf("CountSSHKeys: %w", err)
	}
	return n, nil
}

func (r *postgresRepository) AddKey(ctx context.Context, userID, title, publicKey, fingerprint, keyType string) (*SSHKey, error) {
	uid, err := pgUUID(userID)
	if err != nil {
		return nil, fmt.Errorf("invalid user id: %w", err)
	}
	row, err := r.q.CreateSSHKey(ctx, sqlcdb.CreateSSHKeyParams{UserID: uid, Title: title, PublicKey: publicKey, Fingerprint: fingerprint, KeyType: keyType})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return nil, ErrKeyInUse
	}
	if err != nil {
		return nil, fmt.Errorf("CreateSSHKey: %w", err)
	}
	return toKey(uuidStr(row.ID), row.Title, row.Fingerprint, row.KeyType, row.LastUsedAt, row.CreatedAt), nil
}

func (r *postgresRepository) DeleteKey(ctx context.Context, userID, id string) (*SSHKey, error) {
	uid, err := pgUUID(userID)
	if err != nil {
		return nil, fmt.Errorf("invalid user id: %w", err)
	}
	kid, err := pgUUID(id)
	if err != nil {
		return nil, ErrNotFound
	}
	row, err := r.q.DeleteSSHKey(ctx, sqlcdb.DeleteSSHKeyParams{ID: kid, UserID: uid})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("DeleteSSHKey: %w", err)
	}
	return toKey(uuidStr(row.ID), row.Title, row.Fingerprint, row.KeyType, row.LastUsedAt, row.CreatedAt), nil
}

func (r *postgresRepository) KeyOwner(ctx context.Context, fingerprint string) (string, string, error) {
	row, err := r.q.GetSSHKeyByFingerprint(ctx, fingerprint)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("GetSSHKeyByFingerprint: %w", err)
	}
	return uuidStr(row.ID), uuidStr(row.UserID), nil
}

func (r *postgresRepository) TouchKey(ctx context.Context, keyID string) error {
	kid, err := pgUUID(keyID)
	if err != nil {
		return err
	}
	if err := r.q.TouchSSHKey(ctx, kid); err != nil {
		return fmt.Errorf("TouchSSHKey: %w", err)
	}
	return nil
}
