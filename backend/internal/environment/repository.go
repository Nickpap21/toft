package environment

import "context"

// Repository defines data access for Environments.
type Repository interface {
	Create(ctx context.Context, projectID string, input CreateInput) (*Environment, error)
	GetByID(ctx context.Context, id string) (*Environment, error)
	ListByProject(ctx context.Context, projectID string) ([]Environment, error)
	Update(ctx context.Context, id string, input UpdateInput) (*Environment, error)
	Delete(ctx context.Context, id string) error
	CheckOwnership(ctx context.Context, id, orgID string) (bool, error)
	// Overview fills Overview.Services (one per service with anything to
	// say; the page lists the services themselves).
	Overview(ctx context.Context, id string) ([]ServiceOverview, error)
	// ProjectOverview reads a project's Overview; recent caps its latest
	// deployments.
	ProjectOverview(ctx context.Context, projectID string, recent int32) (*ProjectOverview, error)
	ListByWorkspace(ctx context.Context, orgID string) ([]WorkspaceEnvironment, error)
}
