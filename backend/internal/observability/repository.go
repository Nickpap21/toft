package observability

import "context"

// Repository defines data access for observability configs.
type Repository interface {
	// Upsert creates or updates the observability config for (orgID, projectID).
	// Pass empty projectID for an org-scoped config.
	Upsert(ctx context.Context, cfg Config) (*Config, error)
	// GetByScope returns the observability config for the given (orgID, projectID).
	// Returns nil (no error) when no config exists yet.
	GetByScope(ctx context.Context, orgID, projectID string) (*Config, error)
	// LokiFor returns the project's Loki, else the workspace's (a zero
	// LokiTarget for none). projectID "" asks for the workspace's.
	LokiFor(ctx context.Context, orgID, projectID string) (LokiTarget, error)
	// Unregister clears the workspace's kind (loki, grafana, prometheus):
	// its service and address.
	Unregister(ctx context.Context, orgID, kind string) error
	// ListProjects lists the workspace's projects with their own settings.
	ListProjects(ctx context.Context, orgID string) ([]ProjectSetting, error)
}
