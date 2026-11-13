package deploypolicy

import "context"

// Repository is the data access for exceptions and for the stored service
// configuration the policy re-checks.
type Repository interface {
	// GetException returns the service's exception, or ErrNotFound.
	GetException(ctx context.Context, serviceID string) (*Exception, error)
	// ListExceptions returns every exception, keyed by service ID.
	ListExceptions(ctx context.Context) (map[string]*Exception, error)
	// GrantException creates or replaces the service's exception and writes
	// the audit_log row (ActionExceptionGranted, metadata {allowed_rules,
	// allowed_values, reason}) in one transaction. ErrServiceNotFound when the service does
	// not exist, ErrNoWorkspace when it resolves to no workspace.
	GrantException(ctx context.Context, g Grant) (*Exception, error)
	// RevokeException deletes the exception and writes the audit_log row
	// (ActionExceptionRevoked, metadata {allowed_rules: the revoked rules,
	// reason}) in one transaction. ErrNotFound when there is none.
	RevokeException(ctx context.Context, serviceID, actorID, reason string) (*Exception, error)
	// RepinValues rewrites the exception's pinned fingerprints, each of old
	// to the one at the same position in keyed (D-126); a pin that no longer
	// holds old is left.
	RepinValues(ctx context.Context, serviceID string, old, keyed []string) error

	// GetStoredService returns one service's stored configuration and
	// owner, or ErrServiceNotFound.
	GetStoredService(ctx context.Context, serviceID string) (*StoredService, error)
	// ListStoredServices returns every service on the instance.
	ListStoredServices(ctx context.Context) ([]StoredService, error)
	// ListEnvKeys returns env var and secret keys by service ID; an empty
	// serviceID means every service.
	ListEnvKeys(ctx context.Context, serviceID string) (map[string][]string, error)
	// ListConfigFilePaths returns config file paths by service ID; an empty
	// serviceID means every service.
	ListConfigFilePaths(ctx context.Context, serviceID string) (map[string][]string, error)

	// GetLastCheck returns the last deploy-time check of a compose file
	// from a repository, or ErrNotFound; ListLastChecks every one, keyed by
	// service ID; SaveLastCheck replaces it.
	GetLastCheck(ctx context.Context, serviceID string) (*LastCheck, error)
	ListLastChecks(ctx context.Context) (map[string]*LastCheck, error)
	SaveLastCheck(ctx context.Context, serviceID, commitSHA string, values []PinnedValue) error
}
