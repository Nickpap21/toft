package observability

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"

	"github.com/Nickpap21/toft/backend/internal/catalog"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ServiceChecker verifies that a service belongs to an org, and whether it
// is one of the workspace's own services (scope org). Satisfied by
// service.Manager.
type ServiceChecker interface {
	CheckOwnership(ctx context.Context, id, orgID string) (bool, error)
	IsWorkspaceService(ctx context.Context, id string) (bool, error)
}

// Service contains the business logic for observability configuration.
type Service struct {
	repo     Repository
	services ServiceChecker
	log      *zap.Logger
}

func NewService(repo Repository, services ServiceChecker, log *zap.Logger) *Service {
	return &Service{repo: repo, services: services, log: log}
}

// ErrServiceNotFound: the service_id is not a service of the workspace (or
// does not exist; the two are not told apart).
var ErrServiceNotFound = errors.New("service not found")

// ErrNotWorkspaceService: the workspace's own Loki, Grafana or Prometheus
// is one of its workspace services, never a project's (D-162).
var ErrNotWorkspaceService = errors.New("the workspace's Loki, Grafana and Prometheus are workspace services: move this one to the workspace first")

// ownService checks the service is the workspace's and, for the
// workspace's own settings (projectID ""), one of its workspace services.
func (s *Service) ownService(ctx context.Context, orgID, projectID, serviceID string) error {
	if uuid.Validate(serviceID) != nil {
		return ErrServiceNotFound
	}
	owned, err := s.services.CheckOwnership(ctx, serviceID, orgID)
	if err != nil {
		return fmt.Errorf("checking the service: %w", err)
	}
	if !owned {
		return ErrServiceNotFound
	}
	if projectID != "" {
		return nil
	}
	workspace, err := s.services.IsWorkspaceService(ctx, serviceID)
	if err != nil {
		return fmt.Errorf("checking the service's scope: %w", err)
	}
	if !workspace {
		return ErrNotWorkspaceService
	}
	return nil
}

// GetForOrg returns the org-scoped observability config. Returns nil when not configured.
func (s *Service) GetForOrg(ctx context.Context, orgID string) (*Config, error) {
	return s.repo.GetByScope(ctx, orgID, "")
}

// GetForProject returns the project-scoped observability config, falling back to the org config.
// Returns nil when neither is configured.
func (s *Service) GetForProject(ctx context.Context, orgID, projectID string) (*Config, error) {
	cfg, err := s.repo.GetByScope(ctx, orgID, projectID)
	if err != nil {
		return nil, err
	}
	if cfg != nil {
		return cfg, nil
	}
	return s.repo.GetByScope(ctx, orgID, "")
}

// ErrInvalidURL: a Loki or Grafana URL that is not http(s) with a host. The
// frontend renders these as links (a javascript: URL would run in every
// member's session) and Docker gets the Loki one as a log sink.
var ErrInvalidURL = errors.New("the URL must start with http:// or https:// and name a host")

// validateURL accepts an absolute http(s) URL with a host; "" passes (the
// caller has a default or no link). Errors wrap ErrInvalidURL.
func validateURL(field, raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || len(raw) > 2048 {
		return fmt.Errorf("%s: %w", field, ErrInvalidURL)
	}
	return nil
}

// isLoopback reports whether a URL's host is this machine: localhost or a
// loopback or unspecified address.
func isLoopback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && (ip.IsLoopback() || ip.IsUnspecified())
}

// ErrLokiAlias: Loki's address is a container alias such as
// http://loki:3100. Every server's Docker log driver pushes to it from the
// host, where such a name never resolves (D-48). It is an ErrInvalidURL.
var ErrLokiAlias error = lokiAliasError{}

type lokiAliasError struct{}

func (lokiAliasError) Error() string {
	return "loki_url: a container alias such as http://loki:3100 resolves only inside containers, and Docker's log driver pushes from the host: " +
		"give Loki an address every server reaches, such as a domain of the Loki service limited by its IP allowlist"
}
func (lokiAliasError) Unwrap() error { return ErrInvalidURL }

// UpsertLoki registers a Loki service for the given scope.
// orgID + empty projectID = org scope; orgID + projectID = project scope.
// An empty LokiURL keeps the one set before: deploying Loki from the
// catalog registers it with no address, since none Toft could pick reaches
// every server (D-48).
func (s *Service) UpsertLoki(ctx context.Context, orgID, projectID string, input UpsertLokiInput) (*Config, error) {
	if err := validateURL("loki_url", input.LokiURL); err != nil {
		return nil, err
	}
	if IsContainerAlias(input.LokiURL) {
		return nil, ErrLokiAlias
	}
	if isLoopback(input.LokiURL) {
		// 127.0.0.1 is Toft's to give: on a shared server it is another
		// workspace's Loki (D-148).
		return nil, fmt.Errorf("loki_url: a 127.0.0.1 address is the Loki on that server, which Toft wires itself: %w", ErrInvalidURL)
	}
	if err := s.ownService(ctx, orgID, projectID, input.ServiceID); err != nil {
		return nil, err
	}
	cfg := Config{
		OrgID:         orgID,
		ProjectID:     projectID,
		LokiServiceID: input.ServiceID,
		LokiURL:       input.LokiURL,
	}
	return s.repo.Upsert(ctx, cfg)
}

// UpsertGrafana registers a Grafana service for the given scope.
func (s *Service) UpsertGrafana(ctx context.Context, orgID, projectID string, input UpsertGrafanaInput) (*Config, error) {
	if err := validateURL("grafana_url", input.GrafanaURL); err != nil {
		return nil, err
	}
	if err := s.ownService(ctx, orgID, projectID, input.ServiceID); err != nil {
		return nil, err
	}
	cfg := Config{
		OrgID:            orgID,
		ProjectID:        projectID,
		GrafanaServiceID: input.ServiceID,
		GrafanaURL:       input.GrafanaURL,
	}
	return s.repo.Upsert(ctx, cfg)
}

// UpsertPrometheus registers a Prometheus service for the given scope.
func (s *Service) UpsertPrometheus(ctx context.Context, orgID, projectID string, input UpsertPrometheusInput) (*Config, error) {
	if err := s.ownService(ctx, orgID, projectID, input.ServiceID); err != nil {
		return nil, err
	}
	cfg := Config{
		OrgID:               orgID,
		ProjectID:           projectID,
		PrometheusServiceID: input.ServiceID,
	}
	return s.repo.Upsert(ctx, cfg)
}

// Unregister forgets the workspace's Loki, Grafana or Prometheus (kind):
// its service and address. The service itself is untouched (D-162).
func (s *Service) Unregister(ctx context.Context, orgID, kind string) error {
	return s.repo.Unregister(ctx, orgID, kind)
}

// LokiFor is the Loki a service's logs go to: its project's, else its
// workspace's (D-148). Satisfies deployment.ObservabilityQuerier.
func (s *Service) LokiFor(ctx context.Context, orgID, projectID string) (LokiTarget, error) {
	return s.repo.LokiFor(ctx, orgID, projectID)
}

// ProjectSettings lists each of the workspace's projects with its own Loki
// and Grafana, for the Observability page.
func (s *Service) ProjectSettings(ctx context.Context, orgID string) ([]ProjectSetting, error) {
	return s.repo.ListProjects(ctx, orgID)
}

// IsContainerAlias reports whether a URL's host is a bare name such as
// "loki", which only Docker's embedded DNS resolves, inside containers.
func IsContainerAlias(raw string) bool {
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host != "" && !strings.Contains(host, ".") && !strings.Contains(host, ":") && host != "localhost"
}

// RegisterIfObservability auto-registers a service in the observability config
// when it is a PLG catalog addon (loki|grafana|prometheus) and a workspace
// service: one in a project's environment never becomes the workspace's
// (D-162). Satisfies deployment.ObservabilityRegistrar.
func (s *Service) RegisterIfObservability(ctx context.Context, orgID, serviceID, addonSlug string) error {
	tmpl, ok := catalog.Get(addonSlug)
	if !ok || !tmpl.IsObservability {
		return nil
	}
	workspace, err := s.services.IsWorkspaceService(ctx, serviceID)
	if err != nil {
		return fmt.Errorf("checking the service's scope: %w", err)
	}
	if !workspace {
		return nil
	}

	s.log.Info("auto-registering observability service",
		zap.String("addon_slug", addonSlug),
		zap.String("service_id", serviceID),
		zap.String("org_id", orgID),
	)

	switch addonSlug {
	case "loki":
		// No address: the page asks for one every server reaches (D-48).
		_, err := s.UpsertLoki(ctx, orgID, "", UpsertLokiInput{ServiceID: serviceID})
		return err
	case "grafana":
		_, err := s.UpsertGrafana(ctx, orgID, "", UpsertGrafanaInput{
			ServiceID: serviceID,
		})
		return err
	case "prometheus":
		_, err := s.UpsertPrometheus(ctx, orgID, "", UpsertPrometheusInput{
			ServiceID: serviceID,
		})
		return err
	}
	return nil
}
