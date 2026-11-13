package project

import "context"

// Repository defines the data access contract for the project domain.
// Any concrete implementation (postgres, in-memory) must satisfy this interface.
type Repository interface {
	List(ctx context.Context, orgID string) ([]Project, error)
	GetByID(ctx context.Context, id string) (*Project, error)
	Create(ctx context.Context, orgID, userID string, input CreateProjectInput) (*Project, error)
	Update(ctx context.Context, id string, input UpdateProjectInput) (*Project, error)
	Delete(ctx context.Context, id string) error
	CheckOwnership(ctx context.Context, id, orgID string) (bool, error)
}
