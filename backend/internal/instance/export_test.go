package instance

import (
	"context"
	"time"
)

// MonitorTick runs everything RunPanelMonitor does on a tick, at now.
func (s *Service) MonitorTick(ctx context.Context, now time.Time) { s.monitorTick(ctx, now) }

// PanelTick runs one of RunPanelMonitor's readings at now.
func (s *Service) PanelTick(ctx context.Context, now time.Time) { s.panelTick(ctx, now) }

// PanelContainersTick runs one of RunPanelMonitor's container readings at now.
func (s *Service) PanelContainersTick(ctx context.Context, now time.Time) {
	s.panelContainersTick(ctx, now)
}

// PanelSystemTick runs one of RunPanelMonitor's host readings at now.
func (s *Service) PanelSystemTick(ctx context.Context, now time.Time) { s.panelSystemTick(ctx, now) }

// ParsePanelSystem is parsePanelSystem, for its cases.
var ParsePanelSystem = parsePanelSystem

// SweepPanelHelpers runs one of RunPanelMonitor's sweeps.
func (s *Service) SweepPanelHelpers(ctx context.Context) { s.sweepPanelHelpers(ctx) }

// RecordUpgrade runs one of RunPanelMonitor's upgrade checks.
func (s *Service) RecordUpgrade(ctx context.Context) { s.recordUpgrade(ctx) }

// WatchdogTick runs one of RunPanelMonitor's watchdog checks at now.
func (s *Service) WatchdogTick(ctx context.Context, now time.Time) { s.watchdogTick(ctx, now) }

// CertificateTick runs one of RunPanelMonitor's looks at the panel's domain at now.
func (s *Service) CertificateTick(ctx context.Context, now time.Time) {
	s.certificateTick(ctx, now)
}

// AcmeFailure is acmeFailure, for Traefik's lines.
var AcmeFailure = acmeFailure

// CertProblem is certProblem, for its cases.
var CertProblem = certProblem

// AgePanelCertificate moves the cached read of the panel's certificate back by d.
func (s *Service) AgePanelCertificate(d time.Duration) {
	s.panel.mu.Lock()
	defer s.panel.mu.Unlock()
	s.panel.certAt = s.panel.certAt.Add(-d)
}

// ResetPanelRunSeen forgets the last read of the panel's server's run, so
// the next follow reads the host.
func (s *Service) ResetPanelRunSeen() {
	s.panelRunMu.Lock()
	defer s.panelRunMu.Unlock()
	s.panelRunAt = time.Time{}
}
