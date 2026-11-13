package sharedvolume

import "context"

// Repository stores shared volumes and services' mounts of them.
type Repository interface {
	// List returns a workspace's volumes with the services that mount them.
	List(ctx context.Context, orgID string) ([]Volume, error)
	// Get returns one of a workspace's volumes, or ErrNotFound.
	Get(ctx context.Context, orgID, id string) (*Volume, error)
	// Create stores a volume, or returns ErrNameTaken.
	Create(ctx context.Context, orgID string, in CreateInput) (*Volume, error)
	UpdateLimit(ctx context.Context, orgID, id string, limit int64) (*Volume, error)
	// Delete removes a volume's row: ErrNotFound, or ErrInUse while mounted.
	Delete(ctx context.Context, orgID, id string) error
	// ServiceMounts returns a service's mounts with each volume's workspace
	// and server.
	ServiceMounts(ctx context.Context, serviceID string) ([]Mount, error)
	// ReplaceServiceMounts makes mounts the service's, in one transaction, if
	// version is still what is stored; listversion.ErrChanged and no write
	// otherwise.
	ReplaceServiceMounts(ctx context.Context, serviceID, version string, mounts []Mount) error
	// All returns every volume, for the usage check.
	All(ctx context.Context) ([]Volume, error)
	RecordUsage(ctx context.Context, id string, bytes int64) error
}
