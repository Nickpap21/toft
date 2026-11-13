package service

import (
	"context"
	"errors"
	"fmt"
)

// ErrAlreadyWorkspace: the service is its workspace's own already.
var ErrAlreadyWorkspace = errors.New("the service is a workspace service already")

// ErrMoveRepositoryService: a service that builds from one of its
// project's repositories stays in the project; a workspace service has no
// project repository to build from.
var ErrMoveRepositoryService = errors.New("a service that builds from one of its project's repositories stays in the project: deploy it from a URL first")

// ErrMoveDeploysOnPush: a push to its project's repository deploys the
// service, and a workspace service belongs to no project's pushes.
var ErrMoveDeploysOnPush = errors.New("a push to its project deploys this service, which a workspace service can't keep: turn off deploy on push first")

// ErrMoveCompose: a compose stack's containers join networks by their
// compose service names too, which the move's alias check doesn't cover.
var ErrMoveCompose = errors.New("a compose stack stays in its project: create the workspace's one as a workspace service")

// ErrMoveAliasTaken: another of the workspace's services has the moved (or
// new) workspace service's network alias, and a workspace service joins
// every network of the workspace.
var ErrMoveAliasTaken = errors.New("another service of the workspace uses this service's network alias")

// MoveToWorkspace makes a project's or environment's service its
// workspace's own (D-162), keeping its id, so its volumes, containers and
// domains stay. In one transaction, it first gets its own copy of what its
// environment shares and it doesn't set. It runs on the workspace's
// network from its next deploy, which the caller starts. before is the
// service as it was, for the audit log: the move clears where it was.
func (s *ServiceManager) MoveToWorkspace(ctx context.Context, orgID, id string) (moved, before *Service, err error) {
	svc, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if svc.Scope == ScopeOrg {
		return nil, nil, ErrAlreadyWorkspace
	}
	// A preview's copy follows its pull request: moved, its preview's
	// teardown would no longer find it, and an untrusted commit would join
	// every network of the workspace.
	if preview, err := s.repo.IsPreviewCopy(ctx, id); err != nil {
		return nil, nil, err
	} else if preview {
		return nil, nil, ErrPreviewService
	}
	if svc.RepositoryID != "" {
		return nil, nil, ErrMoveRepositoryService
	}
	if svc.DeployOnPush && svc.GitRepoURL != "" {
		return nil, nil, ErrMoveDeploysOnPush
	}
	if svc.SourceType == SourceTypeDockerCompose {
		return nil, nil, ErrMoveCompose
	}
	if svc.NetworkAlias != "" {
		other, err := s.repo.AliasUserInWorkspace(ctx, orgID, id, svc.NetworkAlias)
		if err != nil {
			return nil, nil, err
		}
		if other != "" {
			return nil, nil, fmt.Errorf("%w: %s uses %q; change one of the aliases first", ErrMoveAliasTaken, other, svc.NetworkAlias)
		}
	}
	if s.tx == nil || s.variables == nil {
		return nil, nil, errors.New("move a service: no transactor or variables")
	}
	// The update first: it locks the row, so a second move at once waits and
	// then finds it moved, rather than copying the variables twice.
	if err := s.tx.InTx(ctx, func(ctx context.Context) error {
		ok, err := s.repo.MoveToWorkspace(ctx, id, orgID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrAlreadyWorkspace
		}
		if svc.EnvironmentID == "" {
			return nil
		}
		if err := s.variables.InheritEnvironment(ctx, svc.EnvironmentID, id); err != nil {
			return fmt.Errorf("copy the environment's variables: %w", err)
		}
		return nil
	}); err != nil {
		return nil, nil, err
	}
	if moved, err = s.repo.GetByID(ctx, id); err != nil {
		return nil, nil, err
	}
	return moved, svc, nil
}
