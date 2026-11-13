package apikey

import (
	"context"
	"time"
)

// Repository defines data access for the apikey domain.
type Repository interface {
	// Create stores a key; projectID "" and no scopes make it unscoped.
	Create(ctx context.Context, k NewKey) (*APIKey, error)
	ListByUser(ctx context.Context, userID string) ([]APIKey, error)
	// ListByProject returns up to limit of the keys scoped to projectID,
	// newest first and older than (beforeAt, beforeID) when beforeID is
	// set: userID's only, or every member's when userID is "".
	ListByProject(ctx context.Context, projectID, userID string, beforeAt time.Time, beforeID string, limit int) ([]ProjectKey, error)
	// GetForAuth returns who the key with this hash acts for.
	GetForAuth(ctx context.Context, keyHash string) (*AuthKey, error)
	// Delete removes the user's key and returns it (ErrNotFound).
	Delete(ctx context.Context, id, userID string) (*RevokedKey, error)
	UpdateLastUsed(ctx context.Context, id string) error
	// ListServices returns those of ids that are services, with their
	// project; IDs that aren't UUIDs are left out.
	ListServices(ctx context.Context, ids []string) ([]KeyService, error)
}

// NewKey is a key to store.
type NewKey struct {
	UserID, Name, KeyHash, Prefix, ProjectID string
	Scopes, ServiceIDs                       []string
	ExpiresAt                                *time.Time
}
