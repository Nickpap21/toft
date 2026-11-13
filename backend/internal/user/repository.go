package user

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned when no user matches.
var ErrNotFound = errors.New("user not found")

// Repository handles data access for User models.
type Repository interface {
	Create(ctx context.Context, email, name, passwordHash string) (*User, error)
	GetByEmail(ctx context.Context, email string) (*User, error)
	GetByID(ctx context.Context, id string) (*User, error)
	// IsActive returns users.is_active, or ErrNotFound.
	IsActive(ctx context.Context, id string) (bool, error)
	// RequestIdentity is what the auth middleware needs per request, in one
	// query, for the workspace orgID ("" for the one the account last
	// switched to), and whether the session token with the sha256
	// sessionHash was signed out ("" for none). ErrNotFound for an unknown
	// user.
	RequestIdentity(ctx context.Context, id, orgID, sessionHash string) (Identity, error)
	UpdateLastLogin(ctx context.Context, id string) error
	UpdateProfile(ctx context.Context, id, name string) (*User, error)
	UpdatePassword(ctx context.Context, id, passwordHash string) error
	UpdateTheme(ctx context.Context, id, theme string) error
	// RevokeSessions refuses every session token issued before now.
	RevokeSessions(ctx context.Context, id string) error
	// RevokeSession refuses the session token with the sha256 tokenHash,
	// until it expires.
	RevokeSession(ctx context.Context, tokenHash string, expires time.Time) error
}
