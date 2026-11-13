package environment

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Nickpap21/toft/backend/internal/netplan"
)

// Service contains the business logic for the environment domain.
type Service struct {
	repo     Repository
	services ServiceRemover
	previews PreviewRemover
	// isolated: every environment has its own app network (D-31).
	isolated bool
}

// PreviewRemover removes the previews copying an environment before it is
// deleted (D-97). Satisfied by *preview.Service via main.go.
type PreviewRemover interface {
	RemoveForEnvironment(ctx context.Context, environmentID string) error
}

// WithPreviews makes Delete remove the previews copying the environment
// first. Called from main.go.
func (s *Service) WithPreviews(p PreviewRemover) { s.previews = p }

// ErrPreview: a preview's environment changes with its pull request, never
// by hand.
var ErrPreview = errors.New("a preview's environment changes with its pull request: its preview is removed when the pull request merges or closes")

// CreatePreview creates a pull request's preview environment: blue, never
// protected (D-55, D-97).
func (s *Service) CreatePreview(ctx context.Context, projectID, name string) (string, error) {
	color, protected := "blue", false
	env, err := s.repo.Create(ctx, projectID, CreateInput{Name: name, Color: &color, Protected: &protected})
	if err != nil {
		return "", err
	}
	return env.ID, nil
}

// OwnEnvironment reports whether id is one of the project's own
// environments, not a preview's.
func (s *Service) OwnEnvironment(ctx context.Context, projectID, id string) (bool, error) {
	env, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return false, nil
	}
	return env.ProjectID == projectID && env.Preview == nil, nil
}

// WithIsolatedNetworks says apps run on their environment's own network
// (TOFT_APP_NETWORKS=isolated). Called from main.go.
func (s *Service) WithIsolatedNetworks(isolated bool) {
	s.isolated = isolated
}

// Overview is the environment's app network and, per service, what it
// overrides, its domains and its latest deployment.
func (s *Service) Overview(ctx context.Context, id string) (*Overview, error) {
	services, err := s.repo.Overview(ctx, id)
	if err != nil {
		return nil, err
	}
	o := &Overview{Services: services}
	if s.isolated {
		o.Network = netplan.EnvNetwork(id)
	}
	return o, nil
}

// recentDeployments is how many deployments a project's Overview lists.
const recentDeployments = 6

// ProjectOverview is what a project's Overview shows beside its services.
func (s *Service) ProjectOverview(ctx context.Context, projectID string) (*ProjectOverview, error) {
	return s.repo.ProjectOverview(ctx, projectID, recentDeployments)
}

// ListByWorkspace returns every environment of a workspace.
func (s *Service) ListByWorkspace(ctx context.Context, orgID string) ([]WorkspaceEnvironment, error) {
	return s.repo.ListByWorkspace(ctx, orgID)
}

// ServiceRemover deletes every service of an environment, with what each
// has on its server. Satisfied by *service.ServiceManager via main.go.
type ServiceRemover interface {
	DeleteEnvironmentServices(ctx context.Context, environmentID string) error
}

// ErrServicesNotRemoved: a service of the environment could not be removed
// from its server, so the environment was not deleted.
// ErrProtectionChanged: the environment's protection changed between the
// check and the update.
var ErrProtectionChanged = errors.New("the environment's protection just changed: reload and try again")

// ErrNotFound: the environment is gone (deleted since it was checked).
var ErrNotFound = errors.New("environment not found")

var ErrServicesNotRemoved = errors.New("the environment's services could not all be removed")

// WithServiceRemover makes Delete remove the environment's services from
// their servers first. Called from main.go.
func (s *Service) WithServiceRemover(r ServiceRemover) {
	s.services = r
}

// NewService creates a new environment Service.
func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// Create creates a new environment under the given project. A colour or
// protection left out comes from the name.
func (s *Service) Create(ctx context.Context, projectID string, input CreateInput) (*Environment, error) {
	color, protected := defaultsFor(input.Name)
	if input.Color == nil {
		input.Color = &color
	}
	if input.Protected == nil {
		input.Protected = &protected
	}
	return s.repo.Create(ctx, projectID, input)
}

// defaultsFor is the colour and protection a new environment gets from its
// name: production is gray and protected, its chip drawn neutral with the
// lock (D-142); the other well-known names get a colour; anything else is
// gray and unprotected.
func defaultsFor(name string) (color string, protected bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "production", "prod":
		return "gray", true
	case "staging", "preprod":
		return "amber", false
	case "test", "qa":
		return "violet", false
	case "dev", "development":
		return "teal", false
	case "preview":
		return "blue", false
	}
	return "gray", false
}

// Get fetches an environment by ID.
func (s *Service) Get(ctx context.Context, id string) (*Environment, error) {
	return s.repo.GetByID(ctx, id)
}

// GetName returns the name of an environment by ID.
func (s *Service) GetName(ctx context.Context, id string) (string, error) {
	env, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return "", err
	}
	return env.Name, nil
}

// ListByProject returns all environments for a project.
func (s *Service) ListByProject(ctx context.Context, projectID string) ([]Environment, error) {
	return s.repo.ListByProject(ctx, projectID)
}

// Update changes an environment's name, colour or protection; fields left
// nil keep their value.
func (s *Service) Update(ctx context.Context, id string, input UpdateInput) (*Environment, error) {
	return s.repo.Update(ctx, id, input)
}

// Delete removes the environment's services from their servers (data
// included), then the environment, its shared variables and secrets.
func (s *Service) Delete(ctx context.Context, id string) error {
	if s.previews != nil {
		if err := s.previews.RemoveForEnvironment(ctx, id); err != nil {
			return fmt.Errorf("remove the previews copying it: %w", err)
		}
	}
	if s.services != nil {
		if err := s.services.DeleteEnvironmentServices(ctx, id); err != nil {
			return err
		}
	}
	return s.repo.Delete(ctx, id)
}

// CheckOwnership returns true when the environment belongs to the given org.
func (s *Service) CheckOwnership(ctx context.Context, id, orgID string) (bool, error) {
	return s.repo.CheckOwnership(ctx, id, orgID)
}

// EnsureDefault creates a "production" environment for the project if none exist yet.
// It is idempotent — safe to call multiple times.
func (s *Service) EnsureDefault(ctx context.Context, projectID string) (*Environment, error) {
	envs, err := s.repo.ListByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if len(envs) > 0 {
		return &envs[0], nil
	}
	return s.Create(ctx, projectID, CreateInput{Name: "production"})
}
