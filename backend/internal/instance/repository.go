package instance

import (
	"context"
	"time"
)

// Repository is the data-access contract for instance-level state.
type Repository interface {
	UsersExist(ctx context.Context) (bool, error)
	IsOperator(ctx context.Context, userID string) (bool, error)
	ListOperators(ctx context.Context) ([]Operator, error)
	// FindUserIDsByEmail matches case-insensitively and may return several
	// IDs if stored emails differ only by case.
	FindUserIDsByEmail(ctx context.Context, email string) ([]string, error)
	UserExists(ctx context.Context, userID string) (bool, error)
	// GrantOperator is idempotent; it reports whether a new row was written.
	GrantOperator(ctx context.Context, userID string, grantedBy *string, source string) (bool, error)
	// RevokeOperator locks every operator row, refuses with ErrLastOperator
	// when no other active operator would remain, and returns ErrNotFound
	// when userID is not an operator. All in one transaction.
	RevokeOperator(ctx context.Context, userID string) error
	// PromoteEarliestUserIfNone makes the earliest-created active user an
	// operator (source bootstrap) when there are no operators at all, under
	// RegistrationLockKey. It returns the promoted user ID, or "" when
	// nothing changed.
	PromoteEarliestUserIfNone(ctx context.Context) (string, error)
	// GetSetting returns the raw JSON value, or ErrNotFound.
	GetSetting(ctx context.Context, key string) ([]byte, error)
	// MergeSetting writes value's top-level fields into the setting,
	// keeping its other fields; actorID is recorded as updated_by.
	MergeSetting(ctx context.Context, key string, value []byte, actorID string) error
	// DeleteSetting removes the setting; one that isn't there is no error.
	DeleteSetting(ctx context.Context, key string) error
	// CountPanelHostServices counts the services with no server of their own.
	CountPanelHostServices(ctx context.Context) (int64, error)

	ListUsers(ctx context.Context) ([]User, error)
	// SetUserActive updates users.is_active. Deactivating refuses with
	// ErrLastOperator when the user is the last active operator, and expires
	// every pending invite the user created or can read as a workspace owner
	// (reported in InvitesRevoked). An unknown user is ErrUserNotFound. All in
	// one transaction. A deactivation records actorID and the time; a
	// reactivation clears them.
	SetUserActive(ctx context.Context, actorID, userID string, active bool) (*UserUpdate, error)

	// CreatePasswordReset stores a reset link's hash for an active user and
	// deletes the user's earlier unused ones, in one transaction. An unknown
	// user is ErrUserNotFound, a deactivated one ErrUserInactive.
	CreatePasswordReset(ctx context.Context, userID, createdBy, tokenHash string, expiresAt time.Time) error
	// GetPasswordReset describes a live link, or returns ErrResetNotFound,
	// ErrResetUsed or ErrResetExpired (a deactivated account reads as expired).
	GetPasswordReset(ctx context.Context, tokenHash string) (*PasswordResetTarget, error)
	// RedeemPasswordReset marks the link used and sets the account's password
	// hash, in one transaction, and returns the account's ID and email. With
	// revokeAccess it also deletes the account's API and SSH keys and revokes
	// its agent connections. Errors as GetPasswordReset.
	RedeemPasswordReset(ctx context.Context, tokenHash, passwordHash string, revokeAccess bool) (*AuditPerson, error)

	// CountDeletedUsers counts the tombstones ListUsers leaves out.
	CountDeletedUsers(ctx context.Context) (int64, error)
	// DeleteUser turns a deactivated, non-operator account into a tombstone
	// and removes its memberships, API keys and reset links, in one
	// transaction; it returns the email the account had. ErrUserNotFound,
	// ErrUserDeleted, ErrUserActive, ErrIsOperator.
	DeleteUser(ctx context.Context, actorID, userID, passwordHash string) (string, error)
	// WriteAudit appends an instance audit row; detail is JSON or nil.
	WriteAudit(ctx context.Context, actorID, action, targetUserID string, detail []byte) error
	// ListAudit returns the newest limit rows, or the ones older than the
	// cursor (before, beforeID) when it is set, of one kind ("panel" or
	// "accounts") or of both ("").
	ListAudit(ctx context.Context, limit int32, before *time.Time, beforeID, kind string) ([]AuditEntry, error)
}
