package instance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Nickpap21/toft/backend/internal/hostupdate"
	"github.com/Nickpap21/toft/backend/internal/server"
	"go.uber.org/zap"
)

// The panel's own server's runs of updates (D-134): a server's run
// (internal/hostupdate) started and followed through the host helper, so it
// runs outside the containers a Docker update or a reboot restarts, as toft
// upgrade does. The run is kept in instance_settings, as the read is.

// SettingPanelUpdateRun is the panel's server's run in progress, or its last.
const SettingPanelUpdateRun = "panel_update_run"

// panelUpdateRun is the setting: the run, its ID on the host, and who
// started it.
type panelUpdateRun struct {
	server.UpdateRun
	RunID       string `json:"run_id"`
	StartedByID string `json:"started_by_id"`
}

// DeployChecker says whether a deploy is under way to the panel's server
// (deployment.Service, with serverID "").
type DeployChecker interface {
	DeployRunning(ctx context.Context, serverID string) (bool, error)
}

// WithDeploys has a Docker update or a reboot of the panel's server wait
// for its deploys.
func (s *Service) WithDeploys(d DeployChecker) { s.deploys = d }

func (s *Service) storedPanelUpdateRun(ctx context.Context) (*panelUpdateRun, error) {
	var r panelUpdateRun
	ok, err := s.readJSON(ctx, SettingPanelUpdateRun, &r)
	if err != nil || !ok || r.RunID == "" {
		return nil, err
	}
	if r.Output == nil {
		r.Output = []string{}
	}
	return &r, nil
}

// AuthorizePanelUpdates returns ErrNotOperator unless actorID is an
// instance operator: the run is root on the panel's server.
func (s *Service) AuthorizePanelUpdates(ctx context.Context, actorID string) error {
	return s.requireOperator(ctx, actorID)
}

// StartPanelUpdates starts action on the panel's server. Operators only;
// the handler asks for the password.
func (s *Service) StartPanelUpdates(ctx context.Context, actorID, action string) (*server.UpdateRun, error) {
	if err := s.requireOperator(ctx, actorID); err != nil {
		return nil, err
	}
	if s.panel == nil {
		return nil, errors.New("the panel's monitor is not configured")
	}
	if st := s.upgradeStatus(); st.Run != nil && st.Run.Outcome == "running" {
		return nil, ErrUpgradeRunning
	}
	// ponytail: one panel starts and follows its own server's runs; the
	// lock doesn't reach a second replica.
	s.panelRunMu.Lock()
	defer s.panelRunMu.Unlock()
	if r, err := s.storedPanelUpdateRun(ctx); err != nil {
		return nil, err
	} else if r != nil && r.FinishedAt == nil {
		return nil, server.ErrUpdateRunning
	}
	u, err := s.storedPanelUpdates(ctx)
	if err != nil {
		return nil, err
	}
	if err := server.CheckUpdateAction(u, action); err != nil {
		return nil, err
	}
	if action != hostupdate.ActionInstall && s.deploys != nil {
		busy, err := s.deploys.DeployRunning(ctx, "")
		if err != nil {
			return nil, fmt.Errorf("check the panel's server's deploys: %w", err)
		}
		if busy {
			return nil, server.ErrDeployRunning
		}
	}
	by := ""
	if ops, err := s.repo.ListOperators(ctx); err == nil {
		for _, o := range ops {
			if o.UserID == actorID {
				by = o.Name
			}
		}
	}
	// Written before the host begins, so a deploy that checks from now on
	// waits; one that began before shows up in the check after.
	r := panelUpdateRun{RunID: server.NewUpdateRunID(), StartedByID: actorID, UpdateRun: server.UpdateRun{
		Action: action, Outcome: hostupdate.OutcomeRunning, Step: hostupdate.StepStarting,
		StartedAt: time.Now().UTC(), StartedBy: by, Output: []string{},
	}}
	if err := s.writeJSON(ctx, actorID, SettingPanelUpdateRun, r); err != nil {
		return nil, err
	}
	unstarted := func(msg string) {
		now := time.Now().UTC()
		r.Outcome, r.FinishedAt, r.Error = hostupdate.OutcomeFailed, &now, msg
		if err := s.writeJSON(context.WithoutCancel(ctx), actorID, SettingPanelUpdateRun, r); err != nil {
			s.log.Warn("recording a run of updates that didn't start failed", zap.Error(err))
		}
	}
	if action != hostupdate.ActionInstall && s.deploys != nil {
		if busy, err := s.deploys.DeployRunning(ctx, ""); err != nil || busy {
			unstarted("A deploy started first.")
			if err != nil {
				return nil, fmt.Errorf("check the panel's server's deploys: %w", err)
			}
			return nil, server.ErrDeployRunning
		}
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	err = server.StartUpdateRunOn(helperShell{ctx: cctx, probe: s.panel.probe}, r.RunID, action)
	cancel()
	if errors.Is(err, server.ErrUpdateRunning) {
		unstarted("Another run of updates was going on on the server.")
		return nil, err
	}
	if err != nil {
		// The answer may be what was lost: ask the host whether it began.
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		st, rerr := server.ReadUpdateRunOn(helperShell{ctx: rctx, probe: s.panel.probe}, time.Now())
		rcancel()
		if rerr != nil || st.ID != r.RunID {
			unstarted("It couldn't start: " + err.Error())
			return nil, fmt.Errorf("%w: %v", server.ErrHostUnreachable, err)
		}
	}
	s.audit(ctx, actorID, "panel.updates_started", "", map[string]any{"action": action})
	return &r.UpdateRun, nil
}

// PanelUpdateRun is the panel's server's run, followed on the host while it
// lasts. Operators only.
func (s *Service) PanelUpdateRun(ctx context.Context, actorID string) (*server.UpdateRun, error) {
	if err := s.requireOperator(ctx, actorID); err != nil {
		return nil, err
	}
	return s.followPanelUpdateRun(ctx)
}

// followPanelUpdateRun reads the run on the host while it lasts, a read at
// a time, a fresh one answering whoever waited; once it has ended the
// outcome is kept and audited as the operator who started it, and the
// updates are read again.
func (s *Service) followPanelUpdateRun(ctx context.Context) (*server.UpdateRun, error) {
	s.panelRunMu.Lock()
	defer s.panelRunMu.Unlock()
	r, err := s.storedPanelUpdateRun(ctx)
	if err != nil || r == nil {
		return nil, err
	}
	if r.FinishedAt != nil || s.panel == nil {
		return &r.UpdateRun, nil
	}
	if s.panelRunSeen.RunID == r.RunID && time.Since(s.panelRunAt) < 2*time.Second {
		run := s.panelRunSeen.UpdateRun
		return &run, nil
	}
	rctx, cancel := context.WithTimeout(ctx, time.Minute)
	st, readErr := server.ReadUpdateRunOn(helperShell{ctx: rctx, probe: s.panel.probe}, time.Now())
	cancel()
	rec := server.UpdateRunRecord{UpdateRun: r.UpdateRun, RunID: r.RunID, StartedByID: r.StartedByID}
	run, done := server.JudgeUpdateRun(rec, st, readErr, time.Now())
	if !done {
		s.panelRunSeen, s.panelRunAt = panelUpdateRun{UpdateRun: run, RunID: r.RunID}, time.Now()
		return &run, nil
	}
	// Recorded and audited even if the caller has gone.
	dctx := context.WithoutCancel(ctx)
	r.UpdateRun = run
	if err := s.writeJSON(dctx, r.StartedByID, SettingPanelUpdateRun, r); err != nil {
		return nil, err
	}
	action := "panel.updates_succeeded"
	detail := map[string]any{"action": run.Action, "packages": run.Packages, "seconds": int(run.FinishedAt.Sub(run.StartedAt).Seconds())}
	if run.Outcome != hostupdate.OutcomeSucceeded {
		action, detail["error"] = "panel.updates_failed", run.Error
	}
	s.audit(dctx, r.StartedByID, action, "", detail)
	// Read what the run changed; bounded by the read's own timeout.
	go func() {
		if _, err := s.readPanelUpdates(dctx); err != nil {
			s.log.Warn("reading the panel's server's updates after a run failed", zap.Error(err))
		}
	}()
	return &run, nil
}

// panelUpdateRunActive reports whether a run of updates is unfinished on
// the panel's server: an upgrade waits for it.
func (s *Service) panelUpdateRunActive(ctx context.Context) (bool, error) {
	run, err := s.followPanelUpdateRun(ctx)
	return run != nil && run.FinishedAt == nil, err
}

// PanelUpdating reports whether a Docker update or a reboot runs on the
// panel's server: deploys there wait (deployment.HostUpdateGate).
func (s *Service) PanelUpdating(ctx context.Context) (bool, error) {
	r, err := s.storedPanelUpdateRun(ctx)
	if err != nil || r == nil || r.FinishedAt != nil || r.Action == hostupdate.ActionInstall {
		return false, err
	}
	// The setting says unfinished: the host says whether it still is.
	return s.panelUpdateRunActive(ctx)
}
