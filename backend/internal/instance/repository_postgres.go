package instance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

func (r *PostgresRepository) IsOperator(ctx context.Context, userID string) (bool, error) {
	id, ok := toPgUUID(userID)
	if !ok {
		return false, nil
	}
	isOp, err := r.queries.IsInstanceOperator(ctx, id)
	if err != nil {
		return false, fmt.Errorf("IsInstanceOperator: %w", err)
	}
	return isOp, nil
}

func (r *PostgresRepository) ListOperators(ctx context.Context) ([]Operator, error) {
	rows, err := r.queries.ListInstanceOperators(ctx)
	if err != nil {
		return nil, fmt.Errorf("ListInstanceOperators: %w", err)
	}
	ops := make([]Operator, 0, len(rows))
	for _, row := range rows {
		op := Operator{
			UserID:    fromPgUUID(row.UserID),
			Email:     row.Email,
			Name:      row.Name,
			IsActive:  row.IsActive,
			Source:    row.Source,
			GrantedAt: row.GrantedAt.Time,
		}
		if row.GrantedBy.Valid {
			g := fromPgUUID(row.GrantedBy)
			op.GrantedBy = &g
		}
		ops = append(ops, op)
	}
	return ops, nil
}

func (r *PostgresRepository) FindUserIDsByEmail(ctx context.Context, email string) ([]string, error) {
	rows, err := r.queries.FindUserIDsByEmailCI(ctx, email)
	if err != nil {
		return nil, fmt.Errorf("FindUserIDsByEmailCI: %w", err)
	}
	ids := make([]string, 0, len(rows))
	for _, id := range rows {
		ids = append(ids, fromPgUUID(id))
	}
	return ids, nil
}

func (r *PostgresRepository) UserExists(ctx context.Context, userID string) (bool, error) {
	id, ok := toPgUUID(userID)
	if !ok {
		return false, nil
	}
	exists, err := r.queries.UserIDExists(ctx, id)
	if err != nil {
		return false, fmt.Errorf("UserIDExists: %w", err)
	}
	return exists, nil
}

func (r *PostgresRepository) GrantOperator(ctx context.Context, userID string, grantedBy *string, source string) (bool, error) {
	id, ok := toPgUUID(userID)
	if !ok {
		return false, ErrUserNotFound
	}
	params := sqlcdb.InsertInstanceOperatorParams{UserID: id, Source: source}
	if grantedBy != nil {
		if g, ok := toPgUUID(*grantedBy); ok {
			params.GrantedBy = g
		}
	}
	n, err := r.queries.InsertInstanceOperator(ctx, params)
	if err != nil {
		return false, fmt.Errorf("InsertInstanceOperator: %w", err)
	}
	return n > 0, nil
}

func (r *PostgresRepository) RevokeOperator(ctx context.Context, userID string) error {
	id, ok := toPgUUID(userID)
	if !ok {
		return ErrNotFound
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := r.queries.WithTx(tx)

	// Lock every operator row first so two concurrent revokes (or a revoke
	// and a deactivation) serialise and cannot both pass the check below.
	locked, err := q.LockInstanceOperators(ctx)
	if err != nil {
		return fmt.Errorf("LockInstanceOperators: %w", err)
	}
	if !containsUUID(locked, id) {
		return ErrNotFound
	}

	others, err := q.CountOtherActiveOperators(ctx, id)
	if err != nil {
		return fmt.Errorf("CountOtherActiveOperators: %w", err)
	}
	if others == 0 {
		return ErrLastOperator
	}

	n, err := q.DeleteInstanceOperator(ctx, id)
	if err != nil {
		return fmt.Errorf("DeleteInstanceOperator: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

func (r *PostgresRepository) PromoteEarliestUserIfNone(ctx context.Context) (string, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := r.queries.WithTx(tx)

	// Same lock as first-run registration, so a bootstrap promotion and a
	// first registration cannot interleave.
	if err := q.AcquireInstanceLock(ctx, RegistrationLockKey); err != nil {
		return "", fmt.Errorf("AcquireInstanceLock: %w", err)
	}

	promoted, err := q.PromoteEarliestUserToOperator(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		// Operators already exist, or there are no active users.
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("PromoteEarliestUserToOperator: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit transaction: %w", err)
	}
	return fromPgUUID(promoted), nil
}

func (r *PostgresRepository) GetSetting(ctx context.Context, key string) ([]byte, error) {
	value, err := r.queries.GetInstanceSetting(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("GetInstanceSetting: %w", err)
	}
	return value, nil
}

func (r *PostgresRepository) MergeSetting(ctx context.Context, key string, value []byte, actorID string) error {
	by, _ := toPgUUID(actorID)
	if _, err := r.queries.MergeInstanceSetting(ctx, sqlcdb.MergeInstanceSettingParams{Key: key, Value: value, UpdatedBy: by}); err != nil {
		return fmt.Errorf("MergeInstanceSetting: %w", err)
	}
	return nil
}

func (r *PostgresRepository) DeleteSetting(ctx context.Context, key string) error {
	if err := r.queries.DeleteInstanceSetting(ctx, key); err != nil {
		return fmt.Errorf("DeleteInstanceSetting: %w", err)
	}
	return nil
}

func (r *PostgresRepository) CountPanelHostServices(ctx context.Context) (int64, error) {
	n, err := r.queries.CountPanelHostServices(ctx)
	if err != nil {
		return 0, fmt.Errorf("CountPanelHostServices: %w", err)
	}
	return n, nil
}

func (r *PostgresRepository) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := r.queries.ListInstanceUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("ListInstanceUsers: %w", err)
	}
	users := make([]User, 0, len(rows))
	for _, row := range rows {
		users = append(users, User{
			ID:             fromPgUUID(row.ID),
			Email:          row.Email,
			Name:           row.Name,
			IsActive:       row.IsActive,
			IsOperator:     row.IsOperator,
			CreatedAt:      row.CreatedAt.Time,
			DeactivatedAt:  timePtr(row.DeactivatedAt),
			DeactivatedBy:  row.DeactivatedByName,
			Workspaces:     names(row.Workspaces),
			LastLoginAt:    timePtr(row.LastLoginAt),
			ResetExpiresAt: timePtr(row.ResetExpiresAt),
		})
	}
	return users, nil
}

func (r *PostgresRepository) CountDeletedUsers(ctx context.Context) (int64, error) {
	n, err := r.queries.CountDeletedUsers(ctx)
	if err != nil {
		return 0, fmt.Errorf("CountDeletedUsers: %w", err)
	}
	return n, nil
}

func (r *PostgresRepository) DeleteUser(ctx context.Context, actorID, userID, passwordHash string) (string, error) {
	id, ok := toPgUUID(userID)
	if !ok {
		return "", ErrUserNotFound
	}
	actor, _ := toPgUUID(actorID)

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := r.queries.WithTx(tx)

	u, err := q.GetUserForDelete(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrUserNotFound
	}
	if err != nil {
		return "", fmt.Errorf("GetUserForDelete: %w", err)
	}
	switch {
	case u.Deleted:
		return "", ErrUserDeleted
	case u.IsActive:
		return "", ErrUserActive
	}
	isOp, err := q.IsInstanceOperator(ctx, id)
	if err != nil {
		return "", fmt.Errorf("IsInstanceOperator: %w", err)
	}
	if isOp {
		return "", ErrIsOperator
	}
	if err := q.DeleteUserMemberships(ctx, id); err != nil {
		return "", fmt.Errorf("DeleteUserMemberships: %w", err)
	}
	if err := q.DeleteUserAPIKeys(ctx, id); err != nil {
		return "", fmt.Errorf("DeleteUserAPIKeys: %w", err)
	}
	if err := q.DeleteUnusedPasswordResets(ctx, id); err != nil {
		return "", fmt.Errorf("DeleteUnusedPasswordResets: %w", err)
	}
	if err := q.TombstoneUser(ctx, sqlcdb.TombstoneUserParams{ID: id, ActorID: actor, PasswordHash: passwordHash}); err != nil {
		return "", fmt.Errorf("TombstoneUser: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit transaction: %w", err)
	}
	return u.Email, nil
}

func (r *PostgresRepository) WriteAudit(ctx context.Context, actorID, action, targetUserID string, detail []byte) error {
	actor, _ := toPgUUID(actorID)
	target, _ := toPgUUID(targetUserID)
	if err := r.queries.WriteInstanceAudit(ctx, sqlcdb.WriteInstanceAuditParams{ActorID: actor, Action: action, TargetUserID: target, Detail: detail}); err != nil {
		return fmt.Errorf("WriteInstanceAudit: %w", err)
	}
	return nil
}

func (r *PostgresRepository) ListAudit(ctx context.Context, limit int32, before *time.Time, beforeID, kind string) ([]AuditEntry, error) {
	params := sqlcdb.ListInstanceAuditParams{PageSize: limit}
	if kind != "" {
		params.Kind = &kind
	}
	if before != nil {
		params.BeforeAt = pgtype.Timestamptz{Time: *before, Valid: true}
		params.BeforeID, _ = toPgUUID(beforeID)
	}
	rows, err := r.queries.ListInstanceAudit(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("ListInstanceAudit: %w", err)
	}
	out := make([]AuditEntry, 0, len(rows))
	for _, row := range rows {
		e := AuditEntry{ID: fromPgUUID(row.ID), Action: row.Action, CreatedAt: row.CreatedAt.Time, Detail: map[string]any{}}
		if len(row.Detail) > 0 {
			_ = json.Unmarshal(row.Detail, &e.Detail)
		}
		if row.ActorID.Valid {
			e.Actor = &AuditPerson{ID: fromPgUUID(row.ActorID), Name: row.ActorName, Email: row.ActorEmail}
		}
		if row.TargetUserID.Valid {
			e.Target = &AuditPerson{ID: fromPgUUID(row.TargetUserID), Name: row.TargetName, Email: row.TargetEmail}
		}
		out = append(out, e)
	}
	return out, nil
}

func (r *PostgresRepository) CreatePasswordReset(ctx context.Context, userID, createdBy, tokenHash string, expiresAt time.Time) error {
	id, ok := toPgUUID(userID)
	if !ok {
		return ErrUserNotFound
	}
	actor, _ := toPgUUID(createdBy)

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := r.queries.WithTx(tx)

	active, err := q.GetUserActiveForUpdate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUserNotFound
	}
	if err != nil {
		return fmt.Errorf("GetUserActiveForUpdate: %w", err)
	}
	if !active {
		return ErrUserInactive
	}
	if err := q.DeleteUnusedPasswordResets(ctx, id); err != nil {
		return fmt.Errorf("DeleteUnusedPasswordResets: %w", err)
	}
	if err := q.CreatePasswordReset(ctx, sqlcdb.CreatePasswordResetParams{
		UserID: id, CreatedBy: actor, TokenHash: tokenHash,
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
	}); err != nil {
		return fmt.Errorf("CreatePasswordReset: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

func (r *PostgresRepository) GetPasswordReset(ctx context.Context, tokenHash string) (*PasswordResetTarget, error) {
	return resetTarget(r.queries.GetPasswordResetByHash(ctx, tokenHash))
}

// resetTarget classifies a GetPasswordResetByHash row. A deactivated
// account's link reads as expired: a new one needs the account reactivated
// first, and the answer does not reveal the account's state.
func resetTarget(row sqlcdb.GetPasswordResetByHashRow, err error) (*PasswordResetTarget, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrResetNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("GetPasswordResetByHash: %w", err)
	}
	if row.UsedAt.Valid {
		return nil, ErrResetUsed
	}
	if !row.IsActive || !time.Now().Before(row.ExpiresAt.Time) {
		return nil, ErrResetExpired
	}
	return &PasswordResetTarget{Email: row.Email, Name: row.Name, ExpiresAt: row.ExpiresAt.Time}, nil
}

func (r *PostgresRepository) RedeemPasswordReset(ctx context.Context, tokenHash, passwordHash string, revokeAccess bool) (*AuditPerson, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := r.queries.WithTx(tx)

	// Claim first: only one concurrent redemption gets a row back. The
	// database clock decides expiry, as for invites.
	row, err := q.ClaimPasswordReset(ctx, tokenHash)
	if errors.Is(err, pgx.ErrNoRows) {
		_, why := resetTarget(q.GetPasswordResetByHash(ctx, tokenHash))
		if why == nil {
			why = ErrResetExpired
		}
		return nil, why
	}
	if err != nil {
		return nil, fmt.Errorf("ClaimPasswordReset: %w", err)
	}
	if err := q.UpdateUserPassword(ctx, sqlcdb.UpdateUserPasswordParams{ID: row.UserID, PasswordHash: passwordHash}); err != nil {
		return nil, fmt.Errorf("UpdateUserPassword: %w", err)
	}
	if revokeAccess {
		if err := q.DeleteUserAPIKeys(ctx, row.UserID); err != nil {
			return nil, fmt.Errorf("DeleteUserAPIKeys: %w", err)
		}
		if err := q.DeleteUserSSHKeys(ctx, row.UserID); err != nil {
			return nil, fmt.Errorf("DeleteUserSSHKeys: %w", err)
		}
		if err := q.RevokeUserAgentConnections(ctx, row.UserID); err != nil {
			return nil, fmt.Errorf("RevokeUserAgentConnections: %w", err)
		}
		if err := q.DeleteUserOAuthCodes(ctx, row.UserID); err != nil {
			return nil, fmt.Errorf("DeleteUserOAuthCodes: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}
	return &AuditPerson{ID: fromPgUUID(row.UserID), Email: row.Email}, nil
}

func (r *PostgresRepository) SetUserActive(ctx context.Context, actorID, userID string, active bool) (*UserUpdate, error) {
	id, ok := toPgUUID(userID)
	if !ok {
		return nil, ErrUserNotFound
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := r.queries.WithTx(tx)

	if !active {
		// Serialise with RevokeOperator and other deactivations; see there.
		if _, err := q.LockInstanceOperators(ctx); err != nil {
			return nil, fmt.Errorf("LockInstanceOperators: %w", err)
		}
	}

	current, err := q.GetUserActiveForUpdate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("GetUserActiveForUpdate: %w", err)
	}

	if !active && current {
		isOp, err := q.IsInstanceOperator(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("IsInstanceOperator: %w", err)
		}
		if isOp {
			others, err := q.CountOtherActiveOperators(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("CountOtherActiveOperators: %w", err)
			}
			if others == 0 {
				return nil, ErrLastOperator
			}
		}
	}

	actor, _ := toPgUUID(actorID)
	row, err := q.SetUserActive(ctx, sqlcdb.SetUserActiveParams{ID: id, IsActive: active, ActorID: actor})
	if err != nil {
		return nil, fmt.Errorf("SetUserActive: %w", err)
	}

	// A deactivated user must not get back in through an invite link they
	// hold: one they created, or any pending one in a workspace they own.
	// Run even when the user was already inactive, so a repeat call cleans up.
	var revoked int64
	if !active {
		revoked, err = q.ExpireInvitesExposedToUser(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("ExpireInvitesExposedToUser: %w", err)
		}
		// Nor through a reset link they hold (D-57).
		if err := q.DeleteUnusedPasswordResets(ctx, id); err != nil {
			return nil, fmt.Errorf("DeleteUnusedPasswordResets: %w", err)
		}
	}

	isOp, err := q.IsInstanceOperator(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("IsInstanceOperator: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}
	return &UserUpdate{
		User: User{
			ID:             fromPgUUID(row.ID),
			Email:          row.Email,
			Name:           row.Name,
			IsActive:       row.IsActive,
			IsOperator:     isOp,
			CreatedAt:      row.CreatedAt.Time,
			DeactivatedAt:  timePtr(row.DeactivatedAt),
			DeactivatedBy:  row.DeactivatedByName,
			Workspaces:     names(row.Workspaces),
			LastLoginAt:    timePtr(row.LastLoginAt),
			ResetExpiresAt: timePtr(row.ResetExpiresAt),
		},
		InvitesRevoked: revoked,
	}, nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// toPgUUID parses s; ok is false for anything that is not a UUID, which
// callers treat as "no such row" rather than a server error.
func toPgUUID(s string) (pgtype.UUID, bool) {
	parsed, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}, false
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}, true
}

func fromPgUUID(p pgtype.UUID) string {
	if !p.Valid {
		return ""
	}
	return uuid.UUID(p.Bytes).String()
}

// names is the list as JSON wants it: [] when there are none, never null.
func names(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

func containsUUID(ids []pgtype.UUID, id pgtype.UUID) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// InsertPanelSample stores a minute's sample of the panel's server (D-68).
func (r *PostgresRepository) InsertPanelSample(ctx context.Context, s PanelSample) error {
	if err := r.queries.InsertPanelSample(ctx, sqlcdb.InsertPanelSampleParams{
		TakenAt:         pgtype.Timestamptz{Time: s.At, Valid: true},
		CpuPercent:      float32(s.CPUPercent),
		MemoryUsedBytes: s.MemoryUsedBytes,
		DiskUsedBytes:   s.DiskUsedBytes,
	}); err != nil {
		return fmt.Errorf("InsertPanelSample: %w", err)
	}
	return nil
}

// ListPanelSamples averages the samples since since into buckets.
func (r *PostgresRepository) ListPanelSamples(ctx context.Context, since time.Time, bucket time.Duration) ([]PanelSample, error) {
	rows, err := r.queries.ListPanelSamples(ctx, sqlcdb.ListPanelSamplesParams{
		Bucket: pgtype.Interval{Microseconds: bucket.Microseconds(), Valid: true},
		Since:  pgtype.Timestamptz{Time: since, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("ListPanelSamples: %w", err)
	}
	out := make([]PanelSample, 0, len(rows))
	for _, row := range rows {
		out = append(out, PanelSample{At: row.BucketAt.Time, CPUPercent: row.CpuPercent, CPUPeak: row.CpuPeak, MemoryUsedBytes: row.MemoryUsedBytes, DiskUsedBytes: row.DiskUsedBytes})
	}
	return out, nil
}

// DeletePanelSamplesBefore deletes the samples older than before.
func (r *PostgresRepository) DeletePanelSamplesBefore(ctx context.Context, before time.Time) (int64, error) {
	n, err := r.queries.DeletePanelSamplesBefore(ctx, pgtype.Timestamptz{Time: before, Valid: true})
	if err != nil {
		return 0, fmt.Errorf("DeletePanelSamplesBefore: %w", err)
	}
	return n, nil
}

// PanelDatabase reads the panel's own database's size, connections and
// version.
func (r *PostgresRepository) PanelDatabase(ctx context.Context) (PanelDatabase, error) {
	row, err := r.queries.PanelDatabaseStats(ctx)
	if err != nil {
		return PanelDatabase{}, fmt.Errorf("PanelDatabaseStats: %w", err)
	}
	return PanelDatabase{Version: row.Version, SizeBytes: row.SizeBytes, Connections: int(row.Connections), MaxConnections: int(row.MaxConnections)}, nil
}
