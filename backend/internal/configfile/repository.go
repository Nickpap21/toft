package configfile

import "context"

// Repository defines the data-access contract for config files.
type Repository interface {
	List(ctx context.Context, serviceID string) ([]ConfigFile, error)
	Upsert(ctx context.Context, serviceID string, in UpsertInput) (ConfigFile, error)
	// Delete removes the service's file id and returns its path ("" when
	// the service has no file id).
	Delete(ctx context.Context, id, serviceID string) (string, error)
}
