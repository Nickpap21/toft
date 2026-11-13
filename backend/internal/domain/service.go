package domain

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// ServiceRouting is what the domains of a service need of the service.
// Satisfied by *service.ServiceManager, defined here to avoid a circular
// import.
type ServiceRouting interface {
	CheckOwnership(ctx context.Context, id, orgID string) (bool, error)
	// DomainTarget is where the service's domains go: the port Traefik
	// routes them to (0: none) and its server ("" the panel host).
	DomainTarget(ctx context.Context, serviceID string) (port int, serverID string, err error)
	// RoutedHosts are the hostnames the service's running containers are
	// routed for now, as their labels say; running is false when none runs.
	RoutedHosts(ctx context.Context, serviceID string) (hosts []string, running bool, err error)
}

type Service struct {
	repo     Repository
	services ServiceRouting
	log      *zap.Logger
	// certificateErr is CertificateError; tests replace it.
	certificateErr func(ctx context.Context, hostname string) error
	redeployer     Redeployer // nil: a certificate redeploys nothing
	// failing is what the certificate poll last logged of each name still
	// waiting, and of its own list ("" key), so each failure is logged when
	// it changes, not every round. The poll's goroutine alone touches it.
	failing map[string]string
}

func NewService(repo Repository, services ServiceRouting, log *zap.Logger) *Service {
	return &Service{repo: repo, services: services, log: log, certificateErr: CertificateError}
}

// WithRedeployer redeploys a service when a temporary name of its first
// has a certificate (D-150).
func (s *Service) WithRedeployer(r Redeployer) { s.redeployer = r }

// Create adds a hostname to a service (ErrInvalidHostname). A wildcard
// ("*.example.com") routes every subdomain without its own route, and needs
// its server to have a DNS provider (server.DNSProvider). A service with no
// port can't take one (ErrNoPort): Traefik would have nowhere to send it.
func (s *Service) Create(ctx context.Context, serviceID, createdBy string, input CreateInput) (*Domain, error) {
	input.Hostname = strings.ToLower(strings.TrimSpace(input.Hostname))
	if !ValidHostname(input.Hostname) {
		return nil, ErrInvalidHostname
	}
	port, _, err := s.services.DomainTarget(ctx, serviceID)
	if err != nil {
		return nil, fmt.Errorf("get the service's port: %w", err)
	}
	if port == 0 {
		return nil, ErrNoPort
	}
	return s.repo.Create(ctx, serviceID, createdBy, input)
}

func (s *Service) List(ctx context.Context, serviceID string) ([]Domain, error) {
	return s.repo.ListByService(ctx, serviceID)
}

// ListForOrg lists every domain of the workspace's services.
func (s *Service) ListForOrg(ctx context.Context, orgID string) ([]WorkspaceDomain, error) {
	return s.repo.ListByOrg(ctx, orgID)
}

// Delete removes one of the service's domains (ErrNotFound for another's)
// and returns its hostname, for the audit log.
func (s *Service) Delete(ctx context.Context, serviceID, id string) (string, error) {
	return s.repo.Delete(ctx, serviceID, id)
}

// ActivateForService marks all domains for a service as verified after a successful deployment.
func (s *Service) ActivateForService(ctx context.Context, serviceID string) error {
	return s.repo.ActivateForService(ctx, serviceID)
}

// Taken reports whether any service has hostname (a new service's address
// takes the next free name, D-150).
func (s *Service) Taken(ctx context.Context, hostname string) (bool, error) {
	return s.repo.Taken(ctx, hostname)
}
