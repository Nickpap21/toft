package user

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

type PostgresRepository struct {
	db      *pgxpool.Pool
	queries *sqlcdb.Queries
}

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{
		db:      pool,
		queries: sqlcdb.New(pool),
	}
}

func (r *PostgresRepository) Create(ctx context.Context, email, name, passwordHash string) (*User, error) {
	row, err := r.queries.CreateUser(ctx, sqlcdb.CreateUserParams{
		Email:        email,
		Name:         name,
		PasswordHash: passwordHash,
	})
	if err != nil {
		return nil, fmt.Errorf("CreateUser: %w", err)
	}

	u := mapDBUserToUser(row)
	return &u, nil
}

func (r *PostgresRepository) GetByEmail(ctx context.Context, email string) (*User, error) {
	row, err := r.queries.GetUserByEmail(ctx, email)
	if err != nil {
		return nil, fmt.Errorf("GetUserByEmail: %w", err)
	}

	u := mapDBUserToUser(row)
	return &u, nil
}

func (r *PostgresRepository) GetByID(ctx context.Context, id string) (*User, error) {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid user id %q: %w", id, err)
	}

	row, err := r.queries.GetUserByID(ctx, pgtype.UUID{Bytes: parsedID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("GetUserByID: %w", err)
	}

	u := mapDBUserToUser(row)
	return &u, nil
}

func (r *PostgresRepository) RequestIdentity(ctx context.Context, id, orgID, sessionHash string) (Identity, error) {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return Identity{}, ErrNotFound
	}
	var org pgtype.UUID
	if orgID != "" {
		parsedOrg, err := uuid.Parse(orgID)
		if err != nil {
			return Identity{}, fmt.Errorf("invalid org id %q: %w", orgID, err)
		}
		org = pgtype.UUID{Bytes: parsedOrg, Valid: true}
	}
	var session *string
	if sessionHash != "" {
		session = &sessionHash
	}
	row, err := r.queries.GetRequestIdentity(ctx, sqlcdb.GetRequestIdentityParams{OrgID: org, SessionHash: session, ID: pgtype.UUID{Bytes: parsedID, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		return Identity{}, ErrNotFound
	}
	if err != nil {
		return Identity{}, fmt.Errorf("GetRequestIdentity: %w", err)
	}
	ident := Identity{Active: row.IsActive, TokensValidFrom: row.TokensValidFrom.Time, SessionRevoked: row.SessionRevoked}
	if row.Role != nil && row.OrgID.Valid {
		ident.OrgID = uuid.UUID(row.OrgID.Bytes).String()
		ident.Role = *row.Role
	}
	return ident, nil
}

func (r *PostgresRepository) IsActive(ctx context.Context, id string) (bool, error) {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return false, ErrNotFound
	}

	active, err := r.queries.GetUserIsActive(ctx, pgtype.UUID{Bytes: parsedID, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("GetUserIsActive: %w", err)
	}
	return active, nil
}

func (r *PostgresRepository) UpdateProfile(ctx context.Context, id, name string) (*User, error) {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid user id %q: %w", id, err)
	}

	row, err := r.queries.UpdateUserProfile(ctx, sqlcdb.UpdateUserProfileParams{
		ID:   pgtype.UUID{Bytes: parsedID, Valid: true},
		Name: name,
	})
	if err != nil {
		return nil, fmt.Errorf("UpdateUserProfile: %w", err)
	}

	u := mapDBUserToUser(row)
	return &u, nil
}

func (r *PostgresRepository) UpdatePassword(ctx context.Context, id, passwordHash string) error {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid user id %q: %w", id, err)
	}

	if err := r.queries.UpdateUserPassword(ctx, sqlcdb.UpdateUserPasswordParams{
		ID:           pgtype.UUID{Bytes: parsedID, Valid: true},
		PasswordHash: passwordHash,
	}); err != nil {
		return fmt.Errorf("UpdateUserPassword: %w", err)
	}
	return nil
}

func (r *PostgresRepository) UpdateLastLogin(ctx context.Context, id string) error {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid user id %q: %w", id, err)
	}

	if err := r.queries.UpdateLastLogin(ctx, pgtype.UUID{Bytes: parsedID, Valid: true}); err != nil {
		return fmt.Errorf("UpdateLastLogin: %w", err)
	}
	return nil
}

// ─── Mappers ──────────────────────────────────────────────────────────────────

func mapDBUserToUser(row sqlcdb.User) User {
	u := User{
		ID:           uuidString(row.ID),
		Email:        row.Email,
		Name:         row.Name,
		PasswordHash: row.PasswordHash,
		Role:         row.Role,
		IsActive:     row.IsActive,
		Theme:        row.Theme,
		CreatedAt:    row.CreatedAt.Time,
		UpdatedAt:    row.UpdatedAt.Time,
	}

	if row.LastLoginAt.Valid {
		t := row.LastLoginAt.Time
		u.LastLoginAt = &t
	}

	return u
}

func uuidString(p pgtype.UUID) string {
	val, _ := p.Value()
	if val == nil {
		return ""
	}
	return val.(string)
}

func (r *PostgresRepository) RevokeSessions(ctx context.Context, id string) error {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid user id %q: %w", id, err)
	}
	if err := r.queries.RevokeUserSessions(ctx, pgtype.UUID{Bytes: parsedID, Valid: true}); err != nil {
		return fmt.Errorf("RevokeUserSessions: %w", err)
	}
	return nil
}

func (r *PostgresRepository) RevokeSession(ctx context.Context, tokenHash string, expires time.Time) error {
	if err := r.queries.RevokeSession(ctx, sqlcdb.RevokeSessionParams{TokenHash: tokenHash, ExpiresAt: pgtype.Timestamptz{Time: expires, Valid: true}}); err != nil {
		return fmt.Errorf("RevokeSession: %w", err)
	}
	return nil
}

func (r *PostgresRepository) UpdateTheme(ctx context.Context, id, theme string) error {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid user id %q: %w", id, err)
	}
	if err := r.queries.UpdateUserTheme(ctx, sqlcdb.UpdateUserThemeParams{
		ID:    pgtype.UUID{Bytes: parsedID, Valid: true},
		Theme: &theme,
	}); err != nil {
		return fmt.Errorf("UpdateUserTheme: %w", err)
	}
	return nil
}
