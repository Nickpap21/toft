package sharedvolume

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/pkg/listversion"
	"go.uber.org/zap"
)

// ServiceReader is the part of service.ServiceManager this package uses.
type ServiceReader interface {
	CheckOwnership(ctx context.Context, id, orgID string) (bool, error)
	Get(ctx context.Context, id string) (*service.Service, error)
}

// ServerChecker says whether a server belongs to a workspace, and whether it
// can hold a volume yet. Satisfied by server.Service.
type ServerChecker interface {
	CheckOwnership(ctx context.Context, id, orgID string) (bool, error)
	// CheckReady refuses (server.ErrServerNotReady) a server still waiting
	// for its first agent.
	CheckReady(ctx context.Context, serverID string) error
}

// Hosts reaches the Docker volumes on a server ("" for the control-plane
// server). Satisfied by an adapter over docker.Factory in main.go.
type Hosts interface {
	// Remove deletes a Docker volume; one that does not exist is not an
	// error.
	Remove(ctx context.Context, serverID, dockerName string) error
	// Sizes returns the size in bytes of each of names that exists.
	Sizes(ctx context.Context, serverID string, names []string) (map[string]int64, error)
}

// Service manages shared volumes and services' mounts of them.
type Service struct {
	repo     Repository
	services ServiceReader
	servers  ServerChecker
	hosts    Hosts
	log      *zap.Logger
}

func NewService(repo Repository, services ServiceReader, servers ServerChecker, hosts Hosts, log *zap.Logger) *Service {
	return &Service{repo: repo, services: services, servers: servers, hosts: hosts, log: log}
}

func (s *Service) List(ctx context.Context, orgID string) ([]Volume, error) {
	return s.repo.List(ctx, orgID)
}

// Create stores a volume on a server of the workspace. Its Docker volume is
// created when a service that mounts it is first deployed.
func (s *Service) Create(ctx context.Context, orgID string, in CreateInput) (*Volume, error) {
	in.Name = strings.TrimSpace(in.Name)
	if !namePattern.MatchString(in.Name) {
		return nil, fmt.Errorf("%w: the name must be 1 to 40 lowercase letters, digits and dashes, starting with a letter or digit", ErrInvalid)
	}
	if in.SizeLimitBytes == 0 {
		in.SizeLimitBytes = DefaultSizeLimitBytes
	}
	if in.SizeLimitBytes < 0 {
		return nil, fmt.Errorf("%w: the size limit must be positive", ErrInvalid)
	}
	if in.ServerID != "" {
		ok, err := s.servers.CheckOwnership(ctx, in.ServerID, orgID)
		if err != nil || !ok {
			return nil, ErrUnknownServer
		}
		if err := s.servers.CheckReady(ctx, in.ServerID); err != nil {
			return nil, fmt.Errorf("create a volume on the server: %w", err)
		}
	}
	return s.repo.Create(ctx, orgID, in)
}

func (s *Service) UpdateLimit(ctx context.Context, orgID, id string, limit int64) (*Volume, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("%w: the size limit must be positive", ErrInvalid)
	}
	return s.repo.UpdateLimit(ctx, orgID, id, limit)
}

// Delete removes an unmounted volume and its data. The Docker volume goes
// first, so a server that cannot be reached leaves the volume listed.
func (s *Service) Delete(ctx context.Context, orgID, id string) error {
	v, err := s.repo.Get(ctx, orgID, id)
	if err != nil {
		return err
	}
	all, err := s.repo.List(ctx, orgID)
	if err != nil {
		return err
	}
	for _, other := range all {
		if other.ID == v.ID && len(other.Mounts) > 0 {
			return ErrInUse
		}
	}
	if err := s.hosts.Remove(ctx, v.ServerID, DockerName(v.ID)); err != nil {
		return fmt.Errorf("remove the volume's data from its server: %w", err)
	}
	return s.repo.Delete(ctx, orgID, id)
}

// ServiceMounts returns a service of the workspace's mounts.
func (s *Service) ServiceMounts(ctx context.Context, orgID, serviceID string) ([]Mount, error) {
	if ok, err := s.services.CheckOwnership(ctx, serviceID, orgID); err != nil || !ok {
		return nil, ErrNotFound
	}
	return s.repo.ServiceMounts(ctx, serviceID)
}

// ServiceMountsVersioned is ServiceMounts and the version a replace must send.
func (s *Service) ServiceMountsVersioned(ctx context.Context, orgID, serviceID string) ([]Mount, string, error) {
	mounts, err := s.ServiceMounts(ctx, orgID, serviceID)
	if err != nil {
		return nil, "", err
	}
	return mounts, versionOf(mounts), nil
}

// SetServiceMounts replaces a service's mounts if version is still what is
// stored (listversion.ErrRequired, listversion.ErrChanged), and returns them
// and their version. Each volume must be the workspace's and on the
// service's server; compose services cannot mount.
func (s *Service) SetServiceMounts(ctx context.Context, orgID, serviceID, version string, mounts []Mount) ([]Mount, string, error) {
	if ok, err := s.services.CheckOwnership(ctx, serviceID, orgID); err != nil || !ok {
		return nil, "", ErrNotFound
	}
	if version == "" {
		return nil, "", listversion.ErrRequired
	}
	if mounts == nil {
		return nil, "", fmt.Errorf("%w: mounts is required (an empty list clears them)", ErrInvalid)
	}
	svc, err := s.services.Get(ctx, serviceID)
	if err != nil {
		return nil, "", ErrNotFound
	}
	if len(mounts) > 0 && svc.SourceType == service.SourceTypeDockerCompose {
		return nil, "", ErrCompose
	}
	seen := map[string]bool{}
	for i := range mounts {
		m := &mounts[i]
		m.Target = strings.TrimSpace(m.Target)
		if !validTarget(m.Target) {
			return nil, "", fmt.Errorf("%w: mount %d: the container path must be absolute, without ..", ErrInvalid, i+1)
		}
		if seen[m.Target] {
			return nil, "", fmt.Errorf("%w: two mounts use %s", ErrInvalid, m.Target)
		}
		seen[m.Target] = true
		v, err := s.repo.Get(ctx, orgID, m.VolumeID)
		if err != nil {
			return nil, "", err
		}
		if v.ServerID != svc.ServerID {
			return nil, "", fmt.Errorf("%w: %s is on another server", ErrWrongServer, v.Name)
		}
	}
	if err := s.repo.ReplaceServiceMounts(ctx, serviceID, version, mounts); err != nil {
		return nil, "", err
	}
	saved, err := s.repo.ServiceMounts(ctx, serviceID)
	if err != nil {
		return nil, "", err
	}
	return saved, versionOf(saved), nil
}

// HasMounts reports whether a service mounts a shared volume. Satisfied for
// service.SharedMountChecker: a service that does cannot move server.
func (s *Service) HasMounts(ctx context.Context, serviceID string) (bool, error) {
	mounts, err := s.repo.ServiceMounts(ctx, serviceID)
	return len(mounts) > 0, err
}

// MountsForDeploy returns a service's mounts as volume mounts for its
// container, deployed to serverID ("" for the control-plane server). A
// volume on another server fails the deploy. Satisfies
// deployment.SharedMountProvider.
func (s *Service) MountsForDeploy(ctx context.Context, serviceID, serverID string) ([]service.VolumeMount, error) {
	mounts, err := s.repo.ServiceMounts(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	out := make([]service.VolumeMount, 0, len(mounts))
	for _, m := range mounts {
		if m.serverID != serverID {
			return nil, fmt.Errorf("%w: %s is on another server", ErrWrongServer, m.Name)
		}
		out = append(out, service.VolumeMount{
			Name: m.Name, Target: m.Target, ReadOnly: m.ReadOnly, Shared: DockerName(m.VolumeID),
		})
	}
	return out, nil
}

// Measure reads every volume's size from its server, records it, and
// returns every volume, measured or not. A server that cannot be reached is
// skipped; its volumes keep their last measurement.
func (s *Service) Measure(ctx context.Context) ([]Usage, error) {
	all, err := s.repo.All(ctx)
	if err != nil {
		return nil, err
	}
	byServer := map[string][]Volume{}
	for _, v := range all {
		byServer[v.ServerID] = append(byServer[v.ServerID], v)
	}
	out := make([]Usage, 0, len(all))
	for serverID, vols := range byServer {
		names := make([]string, len(vols))
		for i, v := range vols {
			names[i] = DockerName(v.ID)
		}
		sizes, err := s.hosts.Sizes(ctx, serverID, names)
		if err != nil && !errors.Is(err, context.Canceled) {
			s.log.Warn("shared volumes: cannot measure", zap.String("server_id", serverID), zap.Error(err))
		}
		for _, v := range vols {
			u := Usage{ID: v.ID, Name: v.Name, SizeLimitBytes: v.SizeLimitBytes}
			// Absent: its server was not reached, or the Docker volume is
			// created only when a service that mounts it is deployed.
			if used, ok := sizes[DockerName(v.ID)]; ok {
				u.UsedBytes, u.Measured = used, true
				if err := s.repo.RecordUsage(ctx, v.ID, used); err != nil {
					s.log.Warn("shared volumes: cannot record usage", zap.String("volume_id", v.ID), zap.Error(err))
				}
			}
			out = append(out, u)
		}
	}
	return out, nil
}
