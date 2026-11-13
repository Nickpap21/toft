package project

import (
	"context"
	"errors"
	"fmt"
)

// DefaultEnvEnsurer creates a default environment for a newly created project.
// Satisfied by *environment.Service via a main.go adapter — defined here to avoid a circular import.
type DefaultEnvEnsurer interface {
	// EnsureDefault returns the environment's ID.
	EnsureDefault(ctx context.Context, projectID string) (string, error)
}

// Transactor runs fn in one transaction, which the repositories fn calls
// take part in through its context (dbtx.Runner).
type Transactor interface {
	InTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// ServiceRemover deletes every service of a project, with what each has on
// its server. Satisfied by *service.ServiceManager via a main.go adapter.
type ServiceRemover interface {
	DeleteProjectServices(ctx context.Context, projectID string) error
}

// Repositories makes and removes projects' git repositories (D-71).
// Satisfied by *code.Service.
type Repositories interface {
	// EnsureRepositories gives every project without one its repository.
	EnsureRepositories(ctx context.Context) error
	// DeleteProjectRepositories removes the project's repositories from
	// disk before the project goes.
	DeleteProjectRepositories(ctx context.Context, projectID string) error
}

// ErrServicesNotRemoved: a service of the project could not be removed from
// its server, so the project was not deleted. Its message says why.
var ErrServicesNotRemoved = errors.New("the project's services could not all be removed")

// Service contains the business logic for the project domain.
type Service struct {
	repo       Repository
	envEnsurer DefaultEnvEnsurer
	services   ServiceRemover
	repos      Repositories
	tx         Transactor
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// WithServiceRemover makes DeleteProject remove the project's services from
// their servers first. Called from main.go.
func (s *Service) WithServiceRemover(r ServiceRemover) {
	s.services = r
}

// WithRepositories makes CreateProject give the project its repository and
// DeleteProject remove it. Called from main.go.
func (s *Service) WithRepositories(r Repositories) {
	s.repos = r
}

// WithTransactor lets CreateProjectWith write a project with what is added
// to it in one transaction. Called from main.go.
func (s *Service) WithTransactor(tx Transactor) {
	s.tx = tx
}

// WithEnvCreator injects the environment creator dependency.
// Call this after NewService and before handling requests.
func (s *Service) WithEnvCreator(e DefaultEnvEnsurer) {
	s.envEnsurer = e
}

func (s *Service) ListProjects(ctx context.Context, orgID string) ([]Project, error) {
	projects, err := s.repo.List(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("project.Service.ListProjects: %w", err)
	}
	return projects, nil
}

func (s *Service) GetProject(ctx context.Context, id string) (*Project, error) {
	p, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("project.Service.GetProject: %w", err)
	}
	return p, nil
}

func (s *Service) CreateProject(ctx context.Context, orgID, userID string, input CreateProjectInput) (*Project, error) {
	if input.Name == "" {
		return nil, fmt.Errorf("project name is required")
	}
	p, err := s.repo.Create(ctx, orgID, userID, input)
	if err != nil {
		return nil, fmt.Errorf("project.Service.CreateProject: %w", err)
	}
	// Auto-create the default "production" environment so the project is immediately usable.
	if s.envEnsurer != nil {
		if _, err := s.envEnsurer.EnsureDefault(ctx, p.ID); err != nil {
			// Non-fatal: the project was created successfully; the user can create environments manually.
			_ = err
		}
	}
	// Non-fatal too: the backend's next start, or the first read of the
	// project's repositories, makes a missing one.
	if s.repos != nil {
		_ = s.repos.EnsureRepositories(ctx)
	}
	return p, nil
}

// CreateProjectWith creates a project, its production environment and what
// add puts in it (its first service, 7g) in one transaction: an error from
// any of them leaves none. add gets the transaction's context. The project's
// repository is made after the commit, as CreateProject makes it.
func (s *Service) CreateProjectWith(ctx context.Context, orgID, userID string, input CreateProjectInput,
	add func(ctx context.Context, projectID, environmentID string) error) (*Project, error) {
	if input.Name == "" {
		return nil, fmt.Errorf("project name is required")
	}
	if s.tx == nil || s.envEnsurer == nil {
		return nil, errors.New("project.Service.CreateProjectWith: no transactor or environments")
	}
	var p *Project
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		if p, err = s.repo.Create(ctx, orgID, userID, input); err != nil {
			return err
		}
		envID, err := s.envEnsurer.EnsureDefault(ctx, p.ID)
		if err != nil {
			return fmt.Errorf("its production environment: %w", err)
		}
		return add(ctx, p.ID, envID)
	})
	if err != nil {
		return nil, err
	}
	if s.repos != nil {
		_ = s.repos.EnsureRepositories(ctx)
	}
	return p, nil
}

func (s *Service) UpdateProject(ctx context.Context, id string, input UpdateProjectInput) (*Project, error) {
	p, err := s.repo.Update(ctx, id, input)
	if err != nil {
		return nil, fmt.Errorf("project.Service.UpdateProject: %w", err)
	}
	return p, nil
}

// DeleteProject removes the project's services from their servers (every
// environment's and its shared ones, data included), its repositories from
// disk, then the project.
func (s *Service) DeleteProject(ctx context.Context, id string) error {
	if s.services != nil {
		if err := s.services.DeleteProjectServices(ctx, id); err != nil {
			return err
		}
	}
	if s.repos != nil {
		if err := s.repos.DeleteProjectRepositories(ctx, id); err != nil {
			return fmt.Errorf("project.Service.DeleteProject: %w", err)
		}
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return fmt.Errorf("project.Service.DeleteProject: %w", err)
	}
	return nil
}

func (s *Service) CheckOwnership(ctx context.Context, id, orgID string) (bool, error) {
	return s.repo.CheckOwnership(ctx, id, orgID)
}
