package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/Nickpap21/toft/backend/internal/resources"
)

// PreviewCopy is a preview's copy of one of the services of the environment
// it copies (D-97): in its environment, building the pull request's
// branch. ExtraMemoryMB is added to its memory limit (the original's, else
// its kind's default): gVisor's own memory for an untrusted one.
type PreviewCopy struct {
	OriginalID, EnvironmentID, Branch string
	ExtraMemoryMB                     int
	Untrusted                         bool
	Hostnames                         []string
	// Isolation is an untrusted copy's, which CopyForPreview decides.
	Isolation string
}

// ErrPreviewService: a preview's services follow their originals and the
// pull request's commits, never edits.
var ErrPreviewService = errors.New("a preview's services follow the environment they copy and the pull request's commits: change the original, or push")

// ErrPreviewEnvironment: services aren't added to a preview's environment.
var ErrPreviewEnvironment = errors.New("a preview's environment holds the pull request's copies only")

// CopyForPreview writes a preview's copy of a service and returns its ID.
func (s *ServiceManager) CopyForPreview(ctx context.Context, in PreviewCopy) (string, error) {
	orig, err := s.repo.GetByID(ctx, in.OriginalID)
	if err != nil {
		return "", fmt.Errorf("get service: %w", err)
	}
	memory := orig.MemoryLimit
	if orig.SourceType != SourceTypeDockerCompose {
		mb, ok := resources.ParseMB(memory)
		if !ok || mb <= 0 {
			mb = resources.DefaultMemoryMB(resources.KindOf(orig.AddonSlug, orig.DockerImage), orig.AddonSlug, orig.DockerImage)
		}
		memory = resources.FormatMB(mb + int64(in.ExtraMemoryMB))
	}
	if in.Untrusted {
		if in.Isolation, err = s.untrustedIsolation(ctx, orig); err != nil {
			return "", err
		}
	}
	return s.repo.CopyForPreview(ctx, in, memory)
}

// AddPreviewAddress gives a preview's copy an address it didn't start
// with: its original got a domain after the preview started.
func (s *ServiceManager) AddPreviewAddress(ctx context.Context, id, hostname string) error {
	return s.repo.AddPreviewAddress(ctx, id, hostname)
}

// untrustedIsolation is an untrusted copy's isolation (D-115): Sandboxed
// with a strict network where its server runs it or the original already
// does, else Sandboxed.
func (s *ServiceManager) untrustedIsolation(ctx context.Context, orig *Service) (string, error) {
	own, def, err := s.repo.Isolation(ctx, orig.ID)
	if err != nil {
		return "", err
	}
	if own == IsolationSandboxedStrict || own == "" && def == IsolationSandboxedStrict {
		return IsolationSandboxedStrict, nil
	}
	if s.sandboxes != nil {
		level, _, err := s.sandboxes.SandboxedStrict(ctx, orig.ServerID)
		if err != nil {
			return "", fmt.Errorf("check the server's strict network: %w", err)
		}
		if level.State == "available" {
			return IsolationSandboxedStrict, nil
		}
	}
	return IsolationSandboxed, nil
}

// IsPreviewCopy reports whether a service is one of a preview's.
func (s *ServiceManager) IsPreviewCopy(ctx context.Context, id string) (bool, error) {
	return s.repo.IsPreviewCopy(ctx, id)
}

// IsPreviewEnvironment reports whether an environment is a preview's.
func (s *ServiceManager) IsPreviewEnvironment(ctx context.Context, environmentID string) (bool, error) {
	return s.repo.IsPreviewEnvironment(ctx, environmentID)
}
