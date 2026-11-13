package instance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/internal/server"
	"go.uber.org/zap"
)

// Sandboxed on the panel's own server (D-97). The capability probe and the
// gVisor install are a server's (D-93, D-95), the same script and steps, run
// through the host helper upgrades use instead of SSH. What they read and
// did is kept in instance_settings, since the panel's server is no row of
// servers.

const (
	SettingPanelCapabilities = "panel_capabilities"
	SettingPanelGvisorRun    = "panel_gvisor_run"

	// panelProbeEvery: the probe runs at start, then once it is this old.
	panelProbeEvery = 24 * time.Hour
	// panelGvisorLogKeep is how much of an install's log is kept, as for a
	// server's (server_gvisor_runs).
	panelGvisorLogKeep = 256 << 10
)

var (
	// ErrPanelNoApps refuses gVisor on a panel's server that runs the
	// control plane only: no app there would use it.
	ErrPanelNoApps = errors.New("the panel's server is Only Toft, so no app here would run Sandboxed; change its role to Run apps here too first")
	// ErrPanelGvisorRunning is a second install while one runs.
	ErrPanelGvisorRunning = errors.New("gVisor is being installed on the panel's server already")
	// ErrPanelGvisorUnsupported: the last probe says gVisor can't run here.
	ErrPanelGvisorUnsupported = errors.New("gVisor can't run on the panel's server")
)

// helperShell runs a server's install and probe commands on the panel's
// host through the host helper.
type helperShell struct {
	ctx   context.Context
	probe PanelProbe
}

func (h helperShell) Exec(cmd string) (string, error) {
	return h.probe.RunOnHostStream(h.ctx, []string{"sh", "-c", cmd}, nil, nil)
}

func (h helperShell) ExecInput(cmd string, stdin io.Reader) (string, error) {
	return h.probe.RunOnHostStream(h.ctx, []string{"sh", "-c", cmd}, stdin, nil)
}

func (h helperShell) ExecStream(cmd string, logFn func(string)) error {
	_, err := h.probe.RunOnHostStream(h.ctx, []string{"sh", "-c", cmd}, nil, logFn)
	return err
}

// StoredPanelCapabilities is what the panel's server's probe last read,
// with the levels it can run; nil before the first probe. No operator check:
// deploys and the workspace's isolation read it (server.PanelCapabilities).
func (s *Service) StoredPanelCapabilities(ctx context.Context) (*server.Capabilities, error) {
	var c server.Capabilities
	ok, err := s.readJSON(ctx, SettingPanelCapabilities, &c)
	if err != nil || !ok {
		return nil, err
	}
	if c.Runtimes == nil {
		c.Runtimes = []server.Runtime{}
	}
	c.SetLevels()
	return &c, nil
}

// PanelCapabilities is StoredPanelCapabilities for an operator.
func (s *Service) PanelCapabilities(ctx context.Context, actorID string) (*server.Capabilities, error) {
	if err := s.requireOperator(ctx, actorID); err != nil {
		return nil, err
	}
	return s.StoredPanelCapabilities(ctx)
}

// ProbePanel reads what the panel's server can run now (Check again).
func (s *Service) ProbePanel(ctx context.Context, actorID string) (*server.Capabilities, error) {
	if err := s.requireOperator(ctx, actorID); err != nil {
		return nil, err
	}
	return s.probePanel(ctx)
}

// probePanel probes the panel's server and keeps what it read; a probe
// that fails keeps what the last one read, with its error.
func (s *Service) probePanel(ctx context.Context) (*server.Capabilities, error) {
	if s.panel == nil {
		return nil, errors.New("the panel's monitor is not configured")
	}
	pctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	c, err := server.ProbeOn(helperShell{ctx: pctx, probe: s.panel.probe})
	var write any
	if err != nil {
		msg := "The probe couldn't run: " + err.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		write = map[string]any{"error": msg, "probed_at": time.Now().UTC()}
	} else {
		c.ProbedAt, c.Levels = time.Now().UTC(), nil
		write = c
	}
	if err := s.writeJSON(ctx, "", SettingPanelCapabilities, write); err != nil {
		return nil, err
	}
	return s.StoredPanelCapabilities(ctx)
}

// panelProbeDelay is how long after a start the panel's server is probed.
var panelProbeDelay = 2 * time.Minute

// RunPanelProbes probes the panel's server shortly after start, then daily,
// while apps run there, until ctx ends.
func (s *Service) RunPanelProbes(ctx context.Context) {
	if s.panel == nil {
		return
	}
	// The start's probe waits a little: toft upgrade loads the host's units
	// (the firewall among them) once the new release is healthy, after the
	// backend started.
	select {
	case <-ctx.Done():
		return
	case <-time.After(panelProbeDelay):
	}
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	first := true
	for {
		if s.PanelHostRunsApps(ctx) {
			c, err := s.StoredPanelCapabilities(ctx)
			if err == nil && (first || c == nil || time.Since(c.ProbedAt) >= panelProbeEvery) {
				if _, err := s.probePanel(ctx); err != nil && ctx.Err() == nil {
					s.log.Warn("probing the panel's server failed", zap.Error(err))
				}
			}
			first = false
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// PanelGvisorRun is the last gVisor install on the panel's server, nil
// before the first. Operators only.
func (s *Service) PanelGvisorRun(ctx context.Context, actorID string) (*server.GvisorRun, error) {
	if err := s.requireOperator(ctx, actorID); err != nil {
		return nil, err
	}
	var run server.GvisorRun
	ok, err := s.readJSON(ctx, SettingPanelGvisorRun, &run)
	if err != nil || !ok {
		return nil, err
	}
	return &run, nil
}

// CloseUnfinishedPanelGvisorRun closes, at startup, an install a restart
// cut short.
func (s *Service) CloseUnfinishedPanelGvisorRun(ctx context.Context) error {
	var run server.GvisorRun
	ok, err := s.readJSON(ctx, SettingPanelGvisorRun, &run)
	if err != nil || !ok || run.FinishedAt != nil {
		return err
	}
	return s.writeJSON(ctx, "", SettingPanelGvisorRun, map[string]any{"finished_at": time.Now().UTC(), "ok": false,
		"error": "The panel restarted during the install, so it stopped without finishing."})
}

// AuthorizePanelGvisor returns ErrNotOperator unless actorID may install
// gVisor on the panel's server: the install is root on it. It is allowed
// with the host shell off (TOFT_PANEL_HOST_SHELL): the install is a fixed
// operation, as an upgrade is.
func (s *Service) AuthorizePanelGvisor(ctx context.Context, actorID string) error {
	return s.requireOperator(ctx, actorID)
}

// CheckPanelGvisor says whether gVisor can be installed on the panel's
// server now: one that runs apps (ErrPanelNoApps), whose last probe doesn't
// rule gVisor out (ErrPanelGvisorUnsupported, with why), with no install
// running (ErrPanelGvisorRunning).
func (s *Service) CheckPanelGvisor(ctx context.Context) error {
	if !s.PanelHostRunsApps(ctx) {
		return ErrPanelNoApps
	}
	c, err := s.StoredPanelCapabilities(ctx)
	if err != nil {
		return err
	}
	if c != nil {
		for _, l := range c.Levels {
			if l.ID == "sandboxed" && l.State == "unavailable" {
				return fmt.Errorf("%w: %s", ErrPanelGvisorUnsupported, l.Reason)
			}
		}
	}
	if s.gvisorBusy.Load() {
		return ErrPanelGvisorRunning
	}
	return nil
}

// InstallPanelGvisor installs gVisor on the panel's server through the host
// helper, records the run as it goes, writes the instance audit log and
// probes the server again. Like a server's, it carries on when the caller
// stops following it. The caller has checked AuthorizePanelGvisor and
// CheckPanelGvisor.
func (s *Service) InstallPanelGvisor(ctx context.Context, actorID string, logFn func(string)) error {
	if s.panel == nil {
		return errors.New("the panel's monitor is not configured")
	}
	if !s.gvisorBusy.CompareAndSwap(false, true) {
		return ErrPanelGvisorRunning
	}
	defer s.gvisorBusy.Store(false)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), server.GvisorTimeout)
	defer cancel()

	by := ""
	if ops, err := s.repo.ListOperators(ctx); err == nil {
		for _, o := range ops {
			if o.UserID == actorID {
				by = o.Name
			}
		}
	}
	run := server.GvisorRun{Release: server.GvisorRelease, StartedAt: time.Now().UTC(), StartedBy: by}
	if err := s.writeJSON(ctx, actorID, SettingPanelGvisorRun, map[string]any{"release": run.Release, "started_at": run.StartedAt,
		"started_by": by, "finished_at": nil, "ok": nil, "error": "", "log": ""}); err != nil {
		s.log.Warn("record the panel's gvisor run", zap.Error(err))
	}
	var logged strings.Builder
	step := ""
	record := func(line string) {
		if strings.HasPrefix(line, "[") {
			step = line
		}
		logged.WriteString(line + "\n")
		text := logged.String()
		if len(text) > panelGvisorLogKeep {
			text = text[len(text)-panelGvisorLogKeep:]
		}
		if err := s.writeJSON(ctx, actorID, SettingPanelGvisorRun, map[string]any{"log": text}); err != nil {
			s.log.Warn("record the panel's gvisor log", zap.Error(err))
		}
		logFn(line)
	}
	logFn("Installing gVisor " + server.GvisorRelease + " on the panel's server")
	platform, err := server.InstallGvisorOn(helperShell{ctx: ctx, probe: s.panel.probe}, record)
	msg := ""
	if err != nil {
		msg = err.Error()
		record("  ✗ " + msg)
	}
	if werr := s.writeJSON(ctx, actorID, SettingPanelGvisorRun, map[string]any{"finished_at": time.Now().UTC(), "ok": err == nil,
		"error": cut(msg, 2000)}); werr != nil {
		s.log.Warn("record the panel's gvisor outcome", zap.Error(werr))
	}
	if err != nil {
		s.audit(ctx, actorID, "panel.gvisor_install_failed", "", map[string]any{"release": server.GvisorRelease, "step": step, "error": msg})
	} else {
		s.audit(ctx, actorID, "panel.gvisor_installed", "", map[string]any{"release": server.GvisorRelease, "platform": platform})
	}
	if _, perr := s.probePanel(ctx); perr != nil {
		s.log.Warn("probe the panel's server after installing gVisor", zap.Error(perr))
	}
	return err
}

// PanelGvisorInstalling reports whether an install runs on the panel's
// server.
func (s *Service) PanelGvisorInstalling() bool { return s.gvisorBusy.Load() }

func cut(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
