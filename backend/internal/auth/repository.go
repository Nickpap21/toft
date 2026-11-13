package auth

import (
	"context"
	"errors"
)

// ErrNotFirstUser is returned by CreateFirstUser when, under the lock, the
// instance turns out to have users already (someone else registered first).
var ErrNotFirstUser = errors.New("instance already has users")

// NewUser is an account to create. PasswordHash is already bcrypt-hashed.
type NewUser struct {
	Email        string
	Name         string
	PasswordHash string
}

// Repository runs registration as single transactions spanning users,
// instance_operators, organizations, org_members and org_invites. Any failure
// rolls back everything: no user, no workspace, no consumed invite.
type Repository interface {
	UsersExist(ctx context.Context) (bool, error)
	// CreateFirstUser takes instance.RegistrationLockKey, re-checks that no
	// user exists (else ErrNotFirstUser), then creates the user, makes them an
	// instance operator (source first_run), creates a workspace named orgName
	// with them as owner, and makes it their active workspace.
	CreateFirstUser(ctx context.Context, u NewUser, orgName string) (userID string, err error)
	// CreateUserWithInvite claims the invite atomically (org.ErrInviteNotFound,
	// ErrInviteExpired or ErrInviteUsed when it cannot), creates the user
	// (ErrUserExists on a duplicate email) and adds them to the invite's
	// workspace with the invite's role, as their active workspace.
	CreateUserWithInvite(ctx context.Context, u NewUser, inviteToken string) (userID string, err error)
}
