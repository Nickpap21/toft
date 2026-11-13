package envvar

import "context"

// Repository stores variables and secrets. by is the user making a change
// ("" when none), recorded on what it sets.
type Repository interface {
	Upsert(ctx context.Context, serviceID, by string, input UpsertInput) (*EnvVar, error)
	// BulkUpsert makes vars the service's plain variables if version is still
	// what is stored; listversion.ErrChanged and no write otherwise.
	BulkUpsert(ctx context.Context, serviceID, by, version string, vars []UpsertInput) ([]EnvVar, error)
	// Seed sets a new service's first variables and deletes nothing.
	Seed(ctx context.Context, serviceID, by string, vars []UpsertInput) error
	ListByService(ctx context.Context, serviceID string) ([]EnvVar, error)
	// Swap sets the service's own plain variable key to value only while it
	// is still from; it never creates one.
	Swap(ctx context.Context, serviceID, key, from, value string) error
	// Unset deletes the service's own plain variable key only while it is
	// still value.
	Unset(ctx context.Context, serviceID, key, value string) error
	Delete(ctx context.Context, serviceID, key string) error
	DeleteAll(ctx context.Context, serviceID string) error

	// Secrets — separate write-once surface.
	ListSecrets(ctx context.Context, serviceID string) ([]SecretDTO, error)
	CreateSecret(ctx context.Context, serviceID, by string, input CreateSecretInput) (*SecretDTO, error)
	UpdateSecret(ctx context.Context, serviceID, by, key, plainValue string) error
	DeleteSecret(ctx context.Context, serviceID, key string) error

	// ListAllForDeployment returns all vars (env + secrets) with decrypted
	// values for Docker injection: the service's own over its environment's
	// shared ones. Only Key, Value and IsSecret are set.
	ListAllForDeployment(ctx context.Context, serviceID string) ([]EnvVar, error)
	ListPlainForDeployment(ctx context.Context, serviceID string) ([]EnvVar, error)

	// Environment (shared) variables and secrets, inherited by every service
	// in the environment.
	ListEnvironmentVars(ctx context.Context, environmentID string) ([]EnvVar, error)
	UpsertEnvironmentVar(ctx context.Context, environmentID, by string, input UpsertInput) (*EnvVar, error)
	// ReplaceEnvironmentVars is BulkUpsert for an environment.
	ReplaceEnvironmentVars(ctx context.Context, environmentID, by, version string, vars []UpsertInput) ([]EnvVar, error)
	DeleteEnvironmentVar(ctx context.Context, environmentID, key string) error
	ListEnvironmentSecrets(ctx context.Context, environmentID string) ([]SecretDTO, error)
	CreateEnvironmentSecret(ctx context.Context, environmentID, by string, input CreateSecretInput) (*SecretDTO, error)
	UpdateEnvironmentSecret(ctx context.Context, environmentID, by, key, plainValue string) error
	DeleteEnvironmentSecret(ctx context.Context, environmentID, key string) error
	// InheritEnvironment copies onto the service what its environment
	// shares and it doesn't set (D-162).
	InheritEnvironment(ctx context.Context, environmentID, serviceID string) error
}
