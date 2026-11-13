package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/internal/instance"
	"github.com/Nickpap21/toft/backend/internal/org"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresRepository is the sqlc-backed Repository.
type PostgresRepository struct {
	db      *pgxpool.Pool
	queries *sqlcdb.Queries
}

// NewPostgresRepository returns a Repository backed by PostgreSQL.
func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{db: pool, queries: sqlcdb.New(pool)}
}

func (r *PostgresRepository) UsersExist(ctx context.Context) (bool, error) {
	exists, err := r.queries.UsersExist(ctx)
	if err != nil {
		return false, fmt.Errorf("UsersExist: %w", err)
	}
	return exists, nil
}

func (r *PostgresRepository) CreateFirstUser(ctx context.Context, nu NewUser, orgName string) (string, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := r.queries.WithTx(tx)

	// Serialise concurrent first-run registrations (and the operator
	// bootstrap): the loser waits here, then sees a user and stops.
	if err := q.AcquireInstanceLock(ctx, instance.RegistrationLockKey); err != nil {
		return "", fmt.Errorf("AcquireInstanceLock: %w", err)
	}
	exists, err := q.UsersExist(ctx)
	if err != nil {
		return "", fmt.Errorf("UsersExist: %w", err)
	}
	if exists {
		return "", ErrNotFirstUser
	}

	u, err := q.CreateUser(ctx, sqlcdb.CreateUserParams{
		Email:        nu.Email,
		Name:         nu.Name,
		PasswordHash: nu.PasswordHash,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return "", ErrUserExists
		}
		return "", fmt.Errorf("CreateUser: %w", err)
	}

	if _, err := q.InsertInstanceOperator(ctx, sqlcdb.InsertInstanceOperatorParams{
		UserID: u.ID,
		Source: instance.SourceFirstRun,
	}); err != nil {
		return "", fmt.Errorf("InsertInstanceOperator: %w", err)
	}

	orgID, err := q.CreateOrganizationReturningID(ctx, orgName)
	if err != nil {
		return "", fmt.Errorf("CreateOrganizationReturningID: %w", err)
	}
	if err := q.InsertOrgMember(ctx, sqlcdb.InsertOrgMemberParams{
		OrgID:  orgID,
		UserID: u.ID,
		Role:   org.RoleOwner,
	}); err != nil {
		return "", fmt.Errorf("InsertOrgMember: %w", err)
	}
	if err := q.SetUserActiveOrg(ctx, sqlcdb.SetUserActiveOrgParams{ID: u.ID, ActiveOrgID: orgID}); err != nil {
		return "", fmt.Errorf("SetUserActiveOrg: %w", err)
	}
	// The setup wizard starts with the first account (D-62); an install set
	// up before the wizard has no such row and is never sent through it.
	started, err := json.Marshal(map[string]time.Time{"started_at": time.Now().UTC()})
	if err != nil {
		return "", fmt.Errorf("encode setup: %w", err)
	}
	if _, err := q.MergeInstanceSetting(ctx, sqlcdb.MergeInstanceSettingParams{
		Key: instance.SettingSetup, Value: started, UpdatedBy: u.ID,
	}); err != nil {
		return "", fmt.Errorf("MergeInstanceSetting: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit transaction: %w", err)
	}
	return uuidString(u.ID), nil
}

func (r *PostgresRepository) CreateUserWithInvite(ctx context.Context, nu NewUser, inviteToken string) (string, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := r.queries.WithTx(tx)

	// Claim first: only one concurrent redemption gets a row back. If a
	// later step fails, the rollback un-claims it.
	tokenHash := org.HashInviteToken(inviteToken)
	inv, err := q.ClaimInviteByToken(ctx, tokenHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", classifyUnclaimableInvite(ctx, q, tokenHash)
	}
	if err != nil {
		return "", fmt.Errorf("ClaimInviteByToken: %w", err)
	}

	u, err := q.CreateUser(ctx, sqlcdb.CreateUserParams{
		Email:        nu.Email,
		Name:         nu.Name,
		PasswordHash: nu.PasswordHash,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return "", ErrUserExists
		}
		return "", fmt.Errorf("CreateUser: %w", err)
	}

	if err := q.InsertOrgMember(ctx, sqlcdb.InsertOrgMemberParams{
		OrgID:  inv.OrgID,
		UserID: u.ID,
		Role:   inv.Role,
	}); err != nil {
		return "", fmt.Errorf("InsertOrgMember: %w", err)
	}
	if err := q.SetUserActiveOrg(ctx, sqlcdb.SetUserActiveOrgParams{ID: u.ID, ActiveOrgID: inv.OrgID}); err != nil {
		return "", fmt.Errorf("SetUserActiveOrg: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit transaction: %w", err)
	}
	return uuidString(u.ID), nil
}

// classifyUnclaimableInvite explains why ClaimInviteByToken matched nothing.
func classifyUnclaimableInvite(ctx context.Context, q *sqlcdb.Queries, tokenHash string) error {
	st, err := q.GetInviteStateByToken(ctx, tokenHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return org.ErrInviteNotFound
	}
	if err != nil {
		return fmt.Errorf("GetInviteStateByToken: %w", err)
	}
	if st.UsedAt.Valid {
		return org.ErrInviteUsed
	}
	// Unused, so the claim failed on expires_at > NOW() (the database clock
	// decides expiry, not this host's) or because the invite's creator has
	// been deactivated. Both mean a dead link that needs a new one, and
	// "expired" does not reveal the creator's account state.
	return org.ErrInviteExpired
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func uuidString(p pgtype.UUID) string {
	if !p.Valid {
		return ""
	}
	return uuid.UUID(p.Bytes).String()
}
