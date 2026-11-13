package apikey

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type postgresRepository struct {
	q *sqlcdb.Queries
}

func NewPostgresRepository(pool *pgxpool.Pool) Repository {
	return &postgresRepository{q: sqlcdb.New(pool)}
}

func (r *postgresRepository) Create(ctx context.Context, k NewKey) (*APIKey, error) {
	uid, err := uuid.Parse(k.UserID)
	if err != nil {
		return nil, fmt.Errorf("invalid user id: %w", err)
	}
	var project pgtype.UUID
	if k.ProjectID != "" {
		pid, err := uuid.Parse(k.ProjectID)
		if err != nil {
			return nil, fmt.Errorf("invalid project id: %w", err)
		}
		project = pgtype.UUID{Bytes: pid, Valid: true}
	}
	scopes := k.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	services, err := toUUIDs(k.ServiceIDs)
	if err != nil {
		return nil, err
	}
	var expires pgtype.Timestamptz
	if k.ExpiresAt != nil {
		expires = pgtype.Timestamptz{Time: *k.ExpiresAt, Valid: true}
	}
	row, err := r.q.CreateAPIKey(ctx, sqlcdb.CreateAPIKeyParams{
		UserID:     pgtype.UUID{Bytes: uid, Valid: true},
		Name:       k.Name,
		KeyHash:    k.KeyHash,
		Prefix:     k.Prefix,
		ProjectID:  project,
		Scopes:     scopes,
		ExpiresAt:  expires,
		ServiceIds: services,
	})
	if err != nil {
		return nil, fmt.Errorf("CreateAPIKey: %w", err)
	}
	key := toAPIKey(sqlcdb.APIKey{
		ID: row.ID, UserID: row.UserID, Name: row.Name, Prefix: row.Prefix, ProjectID: row.ProjectID,
		Scopes: row.Scopes, LastUsedAt: row.LastUsedAt, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt, ServiceIds: row.ServiceIds,
	})
	key.ProjectName = row.ProjectName
	return key, nil
}

func (r *postgresRepository) ListByUser(ctx context.Context, userID string) ([]APIKey, error) {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return nil, fmt.Errorf("invalid user id: %w", err)
	}
	rows, err := r.q.ListAPIKeysByUser(ctx, pgtype.UUID{Bytes: uid, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("ListAPIKeysByUser: %w", err)
	}
	keys := make([]APIKey, len(rows))
	var ids []string
	for i, row := range rows {
		keys[i] = *toAPIKey(sqlcdb.APIKey{
			ID: row.ID, UserID: row.UserID, Name: row.Name, Prefix: row.Prefix, ProjectID: row.ProjectID,
			Scopes: row.Scopes, LastUsedAt: row.LastUsedAt, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt, ServiceIds: row.ServiceIds,
		})
		keys[i].ProjectName, keys[i].OrgName = row.ProjectName, row.OrgName
		ids = append(ids, keys[i].ServiceIDs...)
	}
	if len(ids) == 0 {
		return keys, nil
	}
	// Every key's services in one query.
	services, err := r.ListServices(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]KeyService, len(services))
	for _, svc := range services {
		byID[svc.ID] = svc
	}
	for i := range keys {
		for _, id := range keys[i].ServiceIDs {
			if svc, ok := byID[id]; ok {
				keys[i].Services = append(keys[i].Services, svc)
			}
		}
	}
	return keys, nil
}

func (r *postgresRepository) ListByProject(ctx context.Context, projectID, userID string, beforeAt time.Time, beforeID string, limit int) ([]ProjectKey, error) {
	pid, err := uuid.Parse(projectID)
	if err != nil {
		return nil, fmt.Errorf("invalid project id: %w", err)
	}
	arg := sqlcdb.ListProjectAPIKeysParams{ProjectID: pgtype.UUID{Bytes: pid, Valid: true}, Lim: int32(limit)}
	if userID != "" {
		uid, err := uuid.Parse(userID)
		if err != nil {
			return nil, fmt.Errorf("invalid user id: %w", err)
		}
		arg.UserID = pgtype.UUID{Bytes: uid, Valid: true}
	}
	if beforeID != "" {
		bid, err := uuid.Parse(beforeID)
		if err != nil {
			return nil, fmt.Errorf("invalid cursor id: %w", err)
		}
		arg.BeforeAt = pgtype.Timestamptz{Time: beforeAt, Valid: true}
		arg.BeforeID = pgtype.UUID{Bytes: bid, Valid: true}
	}
	rows, err := r.q.ListProjectAPIKeys(ctx, arg)
	if err != nil {
		return nil, fmt.Errorf("ListProjectAPIKeys: %w", err)
	}
	keys := make([]ProjectKey, 0, len(rows))
	for _, row := range rows {
		scopes := row.Scopes
		if scopes == nil {
			scopes = []string{}
		}
		keys = append(keys, ProjectKey{ID: uuidStr(row.ID), Name: row.Name, Prefix: row.Prefix, Scopes: scopes,
			OwnerID: uuidStr(row.UserID), OwnerName: row.OwnerName, LastUsedAt: timePtr(row.LastUsedAt),
			CreatedAt: row.CreatedAt.Time, ExpiresAt: timePtr(row.ExpiresAt)})
	}
	return keys, nil
}

func (r *postgresRepository) ListServices(ctx context.Context, ids []string) ([]KeyService, error) {
	var parsed []pgtype.UUID
	for _, id := range ids {
		if u, err := uuid.Parse(id); err == nil {
			parsed = append(parsed, pgtype.UUID{Bytes: u, Valid: true})
		}
	}
	if len(parsed) == 0 {
		return []KeyService{}, nil
	}
	rows, err := r.q.ListAPIKeyServices(ctx, parsed)
	if err != nil {
		return nil, fmt.Errorf("ListAPIKeyServices: %w", err)
	}
	out := make([]KeyService, len(rows))
	for i, row := range rows {
		out[i] = KeyService{ID: uuidStr(row.ID), Name: row.Name, EnvironmentName: row.EnvironmentName, ProjectID: uuidStr(row.ProjectID)}
	}
	return out, nil
}

func (r *postgresRepository) GetForAuth(ctx context.Context, keyHash string) (*AuthKey, error) {
	row, err := r.q.GetAPIKeyForAuth(ctx, keyHash)
	if err != nil {
		return nil, fmt.Errorf("GetAPIKeyForAuth: %w", err)
	}
	return &AuthKey{
		ID:           uuidStr(row.ID),
		UserID:       uuidStr(row.UserID),
		ProjectID:    uuidStr(row.ProjectID),
		ProjectOrgID: uuidStr(row.ProjectOrgID),
		Scopes:       row.Scopes,
		ServiceIDs:   uuidStrs(row.ServiceIds),
		ExpiresAt:    timePtr(row.ExpiresAt),
	}, nil
}

func (r *postgresRepository) Delete(ctx context.Context, id, userID string) (*RevokedKey, error) {
	kid, err := uuid.Parse(id)
	if err != nil {
		return nil, ErrNotFound
	}
	uid, err := uuid.Parse(userID)
	if err != nil {
		return nil, fmt.Errorf("invalid user id: %w", err)
	}
	row, err := r.q.DeleteAPIKey(ctx, sqlcdb.DeleteAPIKeyParams{
		ID:     pgtype.UUID{Bytes: kid, Valid: true},
		UserID: pgtype.UUID{Bytes: uid, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("DeleteAPIKey: %w", err)
	}
	k := toAPIKey(sqlcdb.APIKey{
		ID: row.ID, UserID: row.UserID, Name: row.Name, Prefix: row.Prefix, ProjectID: row.ProjectID,
		Scopes: row.Scopes, LastUsedAt: row.LastUsedAt, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt, ServiceIds: row.ServiceIds,
	})
	k.ProjectName = row.ProjectName
	return &RevokedKey{APIKey: *k, ProjectOrgID: uuidStr(row.ProjectOrgID)}, nil
}

func (r *postgresRepository) UpdateLastUsed(ctx context.Context, id string) error {
	kid, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid key id: %w", err)
	}
	return r.q.UpdateAPIKeyLastUsed(ctx, pgtype.UUID{Bytes: kid, Valid: true})
}

// ─── Mapper ───────────────────────────────────────────────────────────────────

func toAPIKey(row sqlcdb.APIKey) *APIKey {
	k := &APIKey{
		ID:         uuidStr(row.ID),
		UserID:     uuidStr(row.UserID),
		Name:       row.Name,
		Prefix:     row.Prefix,
		ProjectID:  uuidStr(row.ProjectID),
		Scopes:     row.Scopes,
		CreatedAt:  row.CreatedAt.Time,
		LastUsedAt: timePtr(row.LastUsedAt),
		ExpiresAt:  timePtr(row.ExpiresAt),
		ServiceIDs: uuidStrs(row.ServiceIds),
		Services:   []KeyService{},
	}
	if k.Scopes == nil {
		k.Scopes = []string{}
	}
	return k
}

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

func uuidStrs(ids []pgtype.UUID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, uuidStr(id))
	}
	return out
}

func toUUIDs(ids []string) ([]pgtype.UUID, error) {
	out := make([]pgtype.UUID, 0, len(ids))
	for _, id := range ids {
		u, err := uuid.Parse(id)
		if err != nil {
			return nil, fmt.Errorf("invalid service id: %w", err)
		}
		out = append(out, pgtype.UUID{Bytes: u, Valid: true})
	}
	return out, nil
}

func uuidStr(p pgtype.UUID) string {
	val, _ := p.Value()
	if val == nil {
		return ""
	}
	return val.(string)
}
