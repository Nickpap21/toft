package domain

import "context"

type Repository interface {
	// Create adds a domain; createdBy is the user adding it ("" none).
	Create(ctx context.Context, serviceID, createdBy string, input CreateInput) (*Domain, error)
	ListByService(ctx context.Context, serviceID string) ([]Domain, error)
	// ListByOrg lists every domain of the workspace's services.
	ListByOrg(ctx context.Context, orgID string) ([]WorkspaceDomain, error)
	// Delete removes the service's domain id and returns its hostname
	// (ErrNotFound when it isn't one).
	Delete(ctx context.Context, serviceID, id string) (string, error)
	UpdateStatus(ctx context.Context, id string, status Status) error
	ActivateForService(ctx context.Context, serviceID string) error
	// AwaitingCertificate lists the routed names no valid certificate has
	// answered for yet; SetCertificate records that one did (D-150),
	// reporting true only the first time.
	AwaitingCertificate(ctx context.Context) ([]Domain, error)
	SetCertificate(ctx context.Context, id string) (bool, error)
	// Taken reports whether any service has hostname.
	Taken(ctx context.Context, hostname string) (bool, error)
}
