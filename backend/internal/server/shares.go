package server

import (
	"context"
	"errors"
	"fmt"
)

// Shared servers (private/specs/2026-10-12-shared-servers-design.md):
// a server's owner workspace lets another workspace deploy to it, within a
// CPU and memory share, so one machine is one server entry.

var (
	ErrNoShare = errors.New("the server is not shared with this workspace")
	// ErrSharingNeedsIsolation: under legacy app networks every app on a
	// machine shares one network, so another workspace's apps could reach
	// this one's (founders, 2026-10-12).
	ErrSharingNeedsIsolation = errors.New("sharing a server needs isolated app networks (TOFT_APP_NETWORKS=isolated), so one workspace's apps cannot reach another's")
	ErrInvalidShare          = errors.New("invalid share")
	// ErrNotTargetOwner: only someone who owns both workspaces may share a
	// server between them.
	ErrNotTargetOwner = errors.New("you can share a server only with a workspace you own")
	ErrShareTooLarge  = errors.New("the shares are larger than the server")
	ErrShareInUse     = errors.New("the workspace still has services on this server: move them first")
	// ErrServerShared: deleting the server would move the services of the
	// workspaces it is shared with to the control-plane server.
	ErrServerShared = errors.New("workspaces this server is shared with still have services on it: move them first, or remove the shares")
)

// OrgRoleReader reads a user's role in a workspace. Satisfied by org.Service.
type OrgRoleReader interface {
	RoleIn(ctx context.Context, userID, orgID string) (string, error)
}

// WithOrgRoles enables sharing: it checks that whoever shares a server owns
// the workspace it is shared with. Without it, sharing is refused.
func (s *Service) WithOrgRoles(r OrgRoleReader) { s.orgRoles = r }

// CanUse reports whether orgID may deploy to serverID: it owns the server or
// the server is shared with it.
func (s *Service) CanUse(ctx context.Context, serverID, orgID string) (bool, error) {
	if serverID == "" || orgID == "" {
		return false, nil
	}
	owned, err := s.repo.CheckOwnership(ctx, serverID, orgID)
	if err != nil || owned {
		return owned, err
	}
	if _, err := s.repo.GetShare(ctx, serverID, orgID); errors.Is(err, ErrNoShare) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}

// ListShares returns the shares of a server ownerOrgID owns.
func (s *Service) ListShares(ctx context.Context, ownerOrgID, serverID string) ([]Share, error) {
	if ok, err := s.repo.CheckOwnership(ctx, serverID, ownerOrgID); err != nil || !ok {
		return nil, ErrServerNotFound
	}
	return s.repo.ListShares(ctx, serverID)
}

// ShareWith shares a server ownerOrgID owns with targetOrgID, or changes the
// share. actorID must own targetOrgID too, the app networks must be
// isolated, and the shares together must fit the server.
func (s *Service) ShareWith(ctx context.Context, actorID, ownerOrgID, serverID, targetOrgID string, cpuCores float64, memoryMB int64) error {
	if !s.appNetworksIsolated {
		return ErrSharingNeedsIsolation
	}
	srv, err := s.repo.GetByID(ctx, serverID)
	if err != nil || srv.OrgID != ownerOrgID {
		return ErrServerNotFound
	}
	if srv.WaitingForAgent() {
		return ErrServerNotReady
	}
	if targetOrgID == "" || targetOrgID == ownerOrgID {
		return fmt.Errorf("%w: choose another workspace", ErrInvalidShare)
	}
	if cpuCores <= 0 || memoryMB <= 0 {
		return fmt.Errorf("%w: the CPU and memory shares must be positive", ErrInvalidShare)
	}
	if s.orgRoles == nil {
		return ErrNotTargetOwner
	}
	if role, err := s.orgRoles.RoleIn(ctx, actorID, targetOrgID); err != nil || role != "owner" {
		return ErrNotTargetOwner
	}
	shares, err := s.repo.ListShares(ctx, serverID)
	if err != nil {
		return err
	}
	cpu, mem := cpuCores, memoryMB
	for _, sh := range shares {
		if sh.OrgID != targetOrgID {
			cpu += sh.CPUCores
			mem += sh.MemoryMB
		}
	}
	if total := srv.EffectiveCPUCores(); total > 0 && cpu >= total {
		return fmt.Errorf("%w: %.2f of %.2f cores would be shared, leaving the owner none", ErrShareTooLarge, cpu, total)
	}
	if total := srv.EffectiveMemoryMB(); total > 0 && mem >= total {
		return fmt.Errorf("%w: %d of %d MB would be shared, leaving the owner none", ErrShareTooLarge, mem, total)
	}
	return s.repo.UpsertShare(ctx, serverID, targetOrgID, cpuCores, memoryMB)
}

// Unshare removes a share; refused while that workspace has services on the
// server.
func (s *Service) Unshare(ctx context.Context, ownerOrgID, serverID, targetOrgID string) error {
	if ok, err := s.repo.CheckOwnership(ctx, serverID, ownerOrgID); err != nil || !ok {
		return ErrServerNotFound
	}
	n, err := s.repo.CountOrgServicesOnServer(ctx, serverID, targetOrgID)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrShareInUse
	}
	deleted, err := s.repo.DeleteShare(ctx, serverID, targetOrgID)
	if err != nil {
		return err
	}
	if !deleted {
		return ErrNoShare
	}
	return nil
}

// ListSharedWithOrg returns the servers other workspaces share with orgID.
func (s *Service) ListSharedWithOrg(ctx context.Context, orgID string) ([]SharedServer, error) {
	return s.repo.ListSharedWithOrg(ctx, orgID)
}

// GetCapacitySummaryFor is the capacity orgID has on a server: a sharing
// workspace's share, or for the owner the server less every share, with
// only that workspace's services allocated. The deploy capacity gate uses it.
func (s *Service) GetCapacitySummaryFor(ctx context.Context, serverID, orgID string) (*CapacitySummary, error) {
	srv, err := s.repo.GetByID(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("get server: %w", err)
	}
	summary := &CapacitySummary{ServerID: serverID, ServerName: srv.Name, OwnerOrgID: srv.OrgID, CapacityDiscovered: srv.TotalCPUCores != nil && *srv.TotalCPUCores > 0}
	if srv.OrgID == orgID {
		shares, err := s.repo.ListShares(ctx, serverID)
		if err != nil {
			return nil, err
		}
		summary.EffectiveCPUCores, summary.EffectiveMemoryMB = srv.EffectiveCPUCores(), srv.EffectiveMemoryMB()
		for _, sh := range shares {
			summary.EffectiveCPUCores -= sh.CPUCores
			summary.EffectiveMemoryMB -= sh.MemoryMB
		}
	} else {
		sh, err := s.repo.GetShare(ctx, serverID, orgID)
		if err != nil {
			return nil, err
		}
		summary.EffectiveCPUCores, summary.EffectiveMemoryMB = sh.CPUCores, sh.MemoryMB
		summary.CapacityDiscovered = true // the share is set by hand
		summary.Share = true
	}
	if r, ok := s.allocReader.(orgAllocReader); ok {
		cpu, mem, err := r.SumAllocatedForServerOrg(ctx, serverID, orgID)
		if err != nil {
			return nil, err
		}
		summary.AllocatedCPUCores, summary.AllocatedMemoryMB = cpu, mem
	}
	return summary, nil
}

// orgAllocReader is the part of the allocation reader that sums one
// workspace's services on a server. Satisfied by service.PostgresRepository.
type orgAllocReader interface {
	SumAllocatedForServerOrg(ctx context.Context, serverID, orgID string) (cpuCores float64, memoryMB float64, err error)
}
