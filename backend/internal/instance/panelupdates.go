package instance

import (
	"context"
	"errors"
	"time"

	"github.com/Nickpap21/toft/backend/internal/advisory"
	"github.com/Nickpap21/toft/backend/internal/server"
	"go.uber.org/zap"
)

// The panel's own server's updates (D-133): a server's read
// (server.ReadUpdatesOn) through the host helper, kept in instance_settings
// as the probe's is.

// SettingPanelUpdates is the panel's server's last updates read.
const SettingPanelUpdates = "panel_updates"

// panelUpdatesEvery: the read runs at start, then once it is this old.
const panelUpdatesEvery = 6 * time.Hour

// WithAdvisories judges the panel's Docker against Docker's advisories.
func (s *Service) WithAdvisories(b *advisory.Book) { s.advisories = b }

// storedPanelUpdates is the last read, with the advisories that affect its
// Docker now; nil before the first.
func (s *Service) storedPanelUpdates(ctx context.Context) (*server.HostUpdates, error) {
	var u server.HostUpdates
	ok, err := s.readJSON(ctx, SettingPanelUpdates, &u)
	if err != nil || !ok {
		return nil, err
	}
	u.Complete()
	u.SetAdvisories(s.advisories)
	return &u, nil
}

// PanelUpdates is the panel's server's last updates read. Operators only.
func (s *Service) PanelUpdates(ctx context.Context, actorID string) (*server.HostUpdates, error) {
	if err := s.requireOperator(ctx, actorID); err != nil {
		return nil, err
	}
	return s.storedPanelUpdates(ctx)
}

// CheckPanelUpdates reads the panel's server's updates now. Operators only.
func (s *Service) CheckPanelUpdates(ctx context.Context, actorID string) (*server.HostUpdates, error) {
	if err := s.requireOperator(ctx, actorID); err != nil {
		return nil, err
	}
	return s.readPanelUpdates(ctx)
}

// readPanelUpdates reads and keeps what it read; a read that fails keeps
// what the last one read, with its error (the setting's other fields stay).
func (s *Service) readPanelUpdates(ctx context.Context) (*server.HostUpdates, error) {
	if s.panel == nil {
		return nil, errors.New("the panel's monitor is not configured")
	}
	rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	now := time.Now().UTC()
	u, err := server.ReadUpdatesOn(helperShell{ctx: rctx, probe: s.panel.probe}, now)
	var write any = u
	if err != nil {
		write = map[string]any{"error": server.UpdatesError(err), "checked_at": now}
	} else {
		u.Docker.Advisories = nil
	}
	if err := s.writeJSON(ctx, "", SettingPanelUpdates, write); err != nil {
		return nil, err
	}
	return s.storedPanelUpdates(ctx)
}

// RunPanelUpdateReads reads the panel's server's updates shortly after
// start, then every 6 hours, and follows a run of updates nobody watches
// (D-134; after a reboot, at the panel's start), until ctx ends.
func (s *Service) RunPanelUpdateReads(ctx context.Context) {
	if s.panel == nil {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(panelProbeDelay):
	}
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		if _, err := s.followPanelUpdateRun(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("following the panel's server's run of updates failed", zap.Error(err))
		}
		u, err := s.storedPanelUpdates(ctx)
		if err == nil && (u == nil || time.Since(u.CheckedAt) >= panelUpdatesEvery) {
			if _, err := s.readPanelUpdates(ctx); err != nil && ctx.Err() == nil {
				s.log.Warn("reading the panel's server's updates failed", zap.Error(err))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// UpdateProblems is the panel's server's UpdateHealth, for the alerts.
func (s *Service) UpdateProblems(ctx context.Context) ([]server.UpdateHealth, error) {
	u, err := s.storedPanelUpdates(ctx)
	if err != nil || u == nil {
		return nil, err
	}
	return []server.UpdateHealth{u.Health(time.Now())}, nil
}
