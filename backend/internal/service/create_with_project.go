package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/Nickpap21/toft/backend/internal/envvar"
	"github.com/Nickpap21/toft/backend/internal/project"
)

// firstEnvironment is the environment a new project starts with, where its
// first service goes.
const firstEnvironment = "production"

// ProjectCreator creates a project with what add puts in it, in one
// transaction (7g). Satisfied by *project.Service.
type ProjectCreator interface {
	CreateProjectWith(ctx context.Context, orgID, userID string, input project.CreateProjectInput,
		add func(ctx context.Context, projectID, environmentID string) error) (*project.Project, error)
}

// VariableWriter writes a new service's variables and secrets, and a
// reset's admin password; List reads a service's plain variables. Satisfied
// by *envvar.Service.
type VariableWriter interface {
	Seed(ctx context.Context, serviceID, by string, vars []envvar.UpsertInput) error
	CreateSecret(ctx context.Context, serviceID, by string, input envvar.CreateSecretInput) (*envvar.SecretDTO, error)
	ReplaceSecret(ctx context.Context, serviceID, by, key, plainValue string) error
	List(ctx context.Context, serviceID string) ([]envvar.EnvVar, error)
	InheritEnvironment(ctx context.Context, environmentID, serviceID string) error
}

// WithProjects and WithVariables let CreateWithProject make a project with
// its first service. Called from main.go.
func (s *ServiceManager) WithProjects(p ProjectCreator)  { s.projects = p }
func (s *ServiceManager) WithVariables(v VariableWriter) { s.variables = v }

// KeyValue is a secret given with a new service, or generated for it.
type KeyValue struct {
	Key   string `json:"key" binding:"required,max=255"`
	Value string `json:"value" binding:"required,max=32768"`
}

// Variable is a plain variable given with a new service; its value may be
// empty.
type Variable struct {
	Key   string `json:"key" binding:"required,max=255"`
	Value string `json:"value" binding:"max=32768"`
}

// AddressForNewProject is the address q would get as the first service of a
// new project named project (D-150, 7g).
func (s *ServiceManager) AddressForNewProject(ctx context.Context, project string, q AddressQuery) (Address, error) {
	return s.address(q, func() (string, error) {
		return s.addresser.AddressIn(ctx, project, firstEnvironment, q.ServerID, q.Name)
	})
}

// CreateWithProject creates a project, its production environment and its
// first service with the service's address, variables and secrets in one
// transaction (7g): none of them is stored unless all are. The service is
// checked and its address named before anything is written. A name taken
// is project.ErrDuplicateName.
func (s *ServiceManager) CreateWithProject(ctx context.Context, orgID, userID string, p project.CreateProjectInput, input CreateInput) (*project.Project, *SavedService, error) {
	if s.projects == nil || s.variables == nil {
		return nil, nil, errors.New("CreateWithProject: no projects or variables")
	}
	if input.RepositoryID != "" {
		return nil, nil, fmt.Errorf("%w: a new project has no repositories yet", ErrInvalidConfig)
	}
	if err := validateCreateInput(input); err != nil {
		return nil, nil, err
	}
	if err := s.checkPanelHost(ctx, input.ServerID); err != nil {
		return nil, nil, err
	}
	if err := checkKeysOnce(input); err != nil {
		return nil, nil, err
	}
	input, generated, login := withGenerated(withDefaultMemory(input))
	nameAddress := func(ctx context.Context) error {
		a, err := s.AddressForNewProject(ctx, p.Name, AddressQuery{Name: input.Name, Type: string(input.Type), ServerID: input.ServerID,
			AddonSlug: input.AddonSlug, DockerImage: input.DockerImage, Port: input.Port})
		if err != nil {
			return fmt.Errorf("name its address: %w", err)
		}
		input.AddressHostname = a.Hostname
		return nil
	}
	if input.Address {
		if err := nameAddress(ctx); err != nil {
			return nil, nil, err
		}
	}
	warnings, err := s.checkPolicy(ctx, saveCheckFromCreate(input))
	if err != nil {
		return nil, nil, err
	}
	// As the create in an environment names it: with the environment's name.
	if input.NetworkAlias == "" {
		input.NetworkAlias = defaultNetworkAlias(input.Name, firstEnvironment)
	}

	var svc *Service
	created, err := s.projects.CreateProjectWith(ctx, orgID, userID, p, func(ctx context.Context, _, environmentID string) error {
		var err error
		svc, err = s.repo.Create(ctx, environmentID, input)
		if errors.Is(err, ErrAddressTaken) {
			// Another service took the name since it was named: name the next.
			if err := nameAddress(ctx); err != nil {
				return err
			}
			svc, err = s.repo.Create(ctx, environmentID, input)
		}
		if err != nil {
			return err
		}
		return s.writeGiven(ctx, svc.ID, userID, input)
	})
	if err != nil {
		return nil, nil, err
	}
	s.addComposeDefaults(ctx, svc, input)
	s.seedConfigFiles(ctx, svc.ID, input.AddonSlug)
	s.startSandboxed(ctx, svc, input)
	return created, &SavedService{Service: svc, PolicyViolations: warnings, GeneratedSecrets: generated, TemporaryLogin: login}, nil
}
