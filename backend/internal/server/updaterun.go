package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/internal/hostupdate"
	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"
)

// Server updates, phase 2, actions (D-134): install the pending updates
// with Docker held back, update Docker, or reboot. The run is
// internal/hostupdate's transient unit on the host, started over SSH or by
// the server's agent (the panel's own server through its host helper, in
// package instance); the host's files say how it goes, and the panel's row
// says who started it and records its outcome once.

// UpdateRun is a host's run of updates (the API's UpdateRun).
type UpdateRun struct {
	Action     string     `json:"action"`
	Outcome    string     `json:"outcome"`
	Step       string     `json:"step"`
	Packages   int        `json:"packages"`
	StartedAt  time.Time  `json:"started_at"`
	StartedBy  string     `json:"started_by"`
	FinishedAt *time.Time `json:"finished_at"`
	Error      string     `json:"error"`
	Output     []string   `json:"output"`
}

// UpdateRunRecord is the panel's row of a run: the run, its ID on the host
// and who started it (for the audit log).
type UpdateRunRecord struct {
	UpdateRun
	RunID       string
	StartedByID string
}

// Refusals of a run.
var (
	ErrUpdateRunning     = errors.New("updates are running on this server already")
	ErrDeployRunning     = errors.New("a deploy is running here: Docker can update or the server reboot once it's done")
	ErrNothingPending    = errors.New("the last read found nothing for this to install")
	ErrDockerNotPackaged = errors.New("Docker wasn't installed from a package here, so Toft can't update it")
	ErrAgentOutdated     = errors.New("this server's agent can't run updates: upgrade the agent first")
	ErrHostUnreachable   = errors.New("the server couldn't be reached")
)

// Times that judge a run.
const (
	// updateRunStartWait: a run the host hasn't begun after this didn't start.
	updateRunStartWait = 2 * time.Minute
	// updateRebootWait: a host not back on a new boot after this didn't
	// restart.
	updateRebootWait = 10 * time.Minute
	// updateRunLost: a run the host hasn't answered about for this long is
	// given up.
	updateRunLost = 2 * time.Hour
	// updateRunCall bounds starting a run and reading its status.
	updateRunCall = 30 * time.Second
	// updateRunFresh: a status read this recent answers the next caller,
	// so each viewer's poll doesn't read the host again.
	updateRunFresh = 2 * time.Second
	// updateRunOutput is the most of a run's output kept, in bytes (the
	// table's CHECK).
	updateRunOutput = 131072
)

// CheckUpdateAction refuses an action the last read u says has nothing to
// do: an install with only Docker's updates pending (or none), a Docker
// update without a newer Docker from a package. A reboot always runs.
func CheckUpdateAction(u *HostUpdates, action string) error {
	switch action {
	case hostupdate.ActionReboot:
		return nil
	case hostupdate.ActionInstall:
		if u == nil || u.ReadAt == nil || u.Security+u.Other-u.Held <= 0 {
			return ErrNothingPending
		}
		return nil
	case hostupdate.ActionDocker:
		if u == nil || u.ReadAt == nil {
			return ErrNothingPending
		}
		if u.Docker.Package != "docker-ce" && u.Docker.Package != "docker.io" {
			return ErrDockerNotPackaged
		}
		// Held: one of Docker's packages (containerd.io) has an update
		// without the engine having one.
		if u.Docker.Candidate == "" && u.Held == 0 {
			return ErrNothingPending
		}
		return nil
	}
	return fmt.Errorf("unknown action %q", action)
}

// NewUpdateRunID is a run's ID: 32 hex characters.
func NewUpdateRunID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// StartUpdateRunOn starts the run on the host behind sc (SSH, or the
// panel's host helper): ErrUpdateRunning when one runs there already.
func StartUpdateRunOn(sc Shell, id, action string) error {
	line, err := hostupdate.StartLine(id, action)
	if err != nil {
		return err
	}
	out, err := sc.ExecInput(line, strings.NewReader(hostupdate.Script))
	if err != nil {
		return err
	}
	return startAnswer(out)
}

func startAnswer(out string) error {
	switch strings.TrimSpace(out) {
	case "started":
		return nil
	case "busy":
		return ErrUpdateRunning
	}
	return fmt.Errorf("the host answered %q", strings.TrimSpace(out))
}

// ReadUpdateRunOn reads the run's status on the host behind sc.
func ReadUpdateRunOn(sc Shell, now time.Time) (hostupdate.Status, error) {
	out, err := sc.Exec(hostupdate.StatusScript)
	if err != nil {
		return hostupdate.Status{}, err
	}
	return hostupdate.Parse(out, now)
}

// JudgeUpdateRun is rec as the host's status st says it is now, and whether
// it has ended. readErr is why st couldn't be read: during a reboot that is
// the reboot.
func JudgeUpdateRun(rec UpdateRunRecord, st hostupdate.Status, readErr error, now time.Time) (UpdateRun, bool) {
	run := rec.UpdateRun
	if run.Output == nil {
		run.Output = []string{}
	}
	end := func(outcome, msg string) (UpdateRun, bool) {
		run.Outcome, run.Error = outcome, msg
		if run.FinishedAt == nil {
			t := now.UTC()
			run.FinishedAt = &t
		}
		return run, true
	}
	since := now.Sub(rec.StartedAt)
	if readErr != nil {
		switch {
		case rec.Action == hostupdate.ActionReboot && since > updateRebootWait:
			return end(hostupdate.OutcomeFailed, fmt.Sprintf("The server isn't back %d minutes after its reboot.", int(updateRebootWait.Minutes())))
		case rec.Action == hostupdate.ActionReboot:
			run.Outcome, run.Step = hostupdate.OutcomeRebooting, hostupdate.StepReboot
		case since > updateRunLost:
			return end(hostupdate.OutcomeFailed, "Toft lost the server during the run, so how it ended isn't known.")
		}
		return run, false
	}
	if st.ID != rec.RunID {
		if since > updateRunStartWait {
			return end(hostupdate.OutcomeFailed, "The run didn't start on the server.")
		}
		run.Step = hostupdate.StepStarting
		return run, false
	}
	run.Step, run.Packages, run.Output = st.Step, st.Packages, st.Output
	switch st.Outcome {
	case hostupdate.OutcomeSucceeded, hostupdate.OutcomeFailed:
		run.FinishedAt = st.Finished
		return end(st.Outcome, st.Error)
	case hostupdate.OutcomeRebooting:
		if st.NowBoot != "" && st.NowBoot != st.Boot {
			run.Step = hostupdate.StepDone
			return end(hostupdate.OutcomeSucceeded, "")
		}
		if since > updateRebootWait {
			return end(hostupdate.OutcomeFailed, fmt.Sprintf("The server didn't restart within %d minutes.", int(updateRebootWait.Minutes())))
		}
		run.Outcome = hostupdate.OutcomeRebooting
		return run, false
	}
	if st.Active {
		return run, false
	}
	if st.NowBoot != "" && st.NowBoot != st.Boot {
		return end(hostupdate.OutcomeFailed, "The server restarted during the run, so it stopped before it finished.")
	}
	return end(hostupdate.OutcomeFailed, "The run stopped before it finished.")
}

// UpdateRunStore keeps each server's run. Satisfied by the Postgres
// repository.
type UpdateRunStore interface {
	// StartUpdateRun takes the server's run, false while one is unfinished.
	StartUpdateRun(ctx context.Context, serverID, runID, action, actorID string) (bool, error)
	// FinishUpdateRun records how it ended, true only for the caller that
	// finished it.
	FinishUpdateRun(ctx context.Context, serverID, runID string, run UpdateRun) (bool, error)
	GetUpdateRun(ctx context.Context, serverID string) (*UpdateRunRecord, error)
	ListUnfinishedUpdateRuns(ctx context.Context) ([]string, error)
	ServerUpdating(ctx context.Context, serverID string) (bool, error)
}

// DeployChecker says whether a deploy is under way to a server
// (deployment.Service).
type DeployChecker interface {
	DeployRunning(ctx context.Context, serverID string) (bool, error)
}

// WithUpdateRuns runs updates on servers: runs kept in st, deploys checked
// with deploys, outcomes written to audit.
func (s *Service) WithUpdateRuns(st UpdateRunStore, deploys DeployChecker, audit AgentAuditor) {
	s.updateRuns, s.updateDeploys, s.updateAudit = st, deploys, audit
}

// StartUpdates starts action on the server for actorID, who the handler
// has checked may (owners and operators, with the password).
func (s *Service) StartUpdates(ctx context.Context, serverID, actorID, action string) (*UpdateRun, error) {
	if s.updateRuns == nil || s.updates == nil {
		return nil, errors.New("updates runs are not configured")
	}
	srv, err := s.repo.GetByID(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("get server: %w", err)
	}
	if srv.Connection == ConnectionAgent {
		if s.agents == nil {
			return nil, ErrHostUnreachable
		}
		st, ok := s.agents.hub.State(serverID)
		if !ok || !st.Connected {
			return nil, fmt.Errorf("%w: its agent isn't connected", ErrHostUnreachable)
		}
		if !slices.Contains(st.Caps, agentproto.CapHostUpdate) {
			return nil, ErrAgentOutdated
		}
	}
	// A run in progress says so first; StartUpdateRun below is the lock.
	if rec, err := s.updateRuns.GetUpdateRun(ctx, serverID); err != nil {
		return nil, err
	} else if rec != nil && rec.FinishedAt == nil {
		return nil, ErrUpdateRunning
	}
	u, err := s.updates.GetUpdates(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if err := CheckUpdateAction(u, action); err != nil {
		return nil, err
	}
	if action != hostupdate.ActionInstall && s.updateDeploys != nil {
		busy, err := s.updateDeploys.DeployRunning(ctx, serverID)
		if err != nil {
			return nil, fmt.Errorf("check the server's deploys: %w", err)
		}
		if busy {
			return nil, ErrDeployRunning
		}
	}
	runID := NewUpdateRunID()
	took, err := s.updateRuns.StartUpdateRun(ctx, serverID, runID, action, actorID)
	if err != nil {
		return nil, err
	}
	if !took {
		return nil, ErrUpdateRunning
	}
	// A deploy that began between the check above and the row: Trigger
	// checks the other way after its own write.
	if action != hostupdate.ActionInstall && s.updateDeploys != nil {
		busy, err := s.updateDeploys.DeployRunning(ctx, serverID)
		if err != nil || busy {
			s.finishUnstarted(ctx, serverID, runID, "A deploy started first.")
			if err != nil {
				return nil, fmt.Errorf("check the server's deploys: %w", err)
			}
			return nil, ErrDeployRunning
		}
	}
	// Not cancelled with the request: the host may begin the run either way.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), updateRunCall)
	defer cancel()
	if err := s.startUpdatesOn(cctx, srv, runID, action); err != nil {
		if errors.Is(err, ErrUpdateRunning) {
			s.finishUnstarted(ctx, serverID, runID, "Another run of updates was going on on the server.")
			return nil, err
		}
		// The answer may be what was lost: ask the host whether it began.
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), updateRunCall)
		st, rerr := s.readUpdatesRunOn(rctx, srv)
		rcancel()
		if rerr != nil || st.ID != runID {
			s.finishUnstarted(ctx, serverID, runID, "It couldn't start: "+err.Error())
			return nil, fmt.Errorf("%w: %v", ErrHostUnreachable, err)
		}
	}
	s.auditUpdates(ctx, srv.OrgID, actorID, serverID, "server.updates_started", map[string]any{"action": action})
	rec, err := s.updateRuns.GetUpdateRun(ctx, serverID)
	if err != nil || rec == nil {
		return nil, err
	}
	return &rec.UpdateRun, nil
}

// finishUnstarted records a run that never began on the host. Not audited:
// nothing ran.
func (s *Service) finishUnstarted(ctx context.Context, serverID, runID, msg string) {
	now := time.Now().UTC()
	failed := UpdateRun{Outcome: hostupdate.OutcomeFailed, FinishedAt: &now, Error: msg}
	if _, err := s.updateRuns.FinishUpdateRun(context.WithoutCancel(ctx), serverID, runID, failed); err != nil {
		s.log.Warn("record an update run that didn't start", zap.Error(err), zap.String("server_id", serverID))
	}
}

func (s *Service) startUpdatesOn(ctx context.Context, srv *Server, runID, action string) error {
	if srv.Connection != ConnectionAgent {
		return StartUpdateRunOn(shellFor(ctx, s.SSH(srv)), runID, action)
	}
	req, err := json.Marshal(agentproto.HostUpdateRequest{ID: runID, Action: action})
	if err != nil {
		return err
	}
	out, err := s.SSH(srv).Subsystem(ctx, agentproto.SubsystemHostUpdate, strings.NewReader(string(req)+"\n"))
	if err != nil {
		return err
	}
	return startAnswer(out)
}

func (s *Service) readUpdatesRunOn(ctx context.Context, srv *Server) (hostupdate.Status, error) {
	if srv.Connection != ConnectionAgent {
		return ReadUpdateRunOn(shellFor(ctx, s.SSH(srv)), time.Now())
	}
	if s.agents == nil {
		return hostupdate.Status{}, ErrHostUnreachable
	}
	if st, ok := s.agents.hub.State(srv.ID); !ok || !st.Connected {
		return hostupdate.Status{}, ErrHostUnreachable
	}
	out, err := s.SSH(srv).Subsystem(ctx, agentproto.SubsystemHostUpdateStatus, nil)
	if err != nil {
		return hostupdate.Status{}, err
	}
	return hostupdate.Parse(out, time.Now())
}

// UpdateRun is the server's run, followed on the host while it lasts:
// once it has ended the outcome is recorded and audited (by whichever
// caller sees it first) and the updates are read again. nil before the
// first run.
func (s *Service) UpdateRun(ctx context.Context, serverID string) (*UpdateRun, error) {
	if s.updateRuns == nil {
		return nil, nil
	}
	rec, err := s.updateRuns.GetUpdateRun(ctx, serverID)
	if err != nil || rec == nil {
		return nil, err
	}
	if rec.FinishedAt != nil {
		return &rec.UpdateRun, nil
	}
	// One read of the host at a time per server, and a fresh one answers
	// whoever waited for it.
	v, _ := s.updateFollow.LoadOrStore(serverID, &followedRun{})
	f := v.(*followedRun)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.runID == rec.RunID && time.Since(f.at) < updateRunFresh {
		run := f.run
		return &run, nil
	}
	srv, err := s.repo.GetByID(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("get server: %w", err)
	}
	rctx, cancel := context.WithTimeout(ctx, updateRunCall)
	st, readErr := s.readUpdatesRunOn(rctx, srv)
	cancel()
	run, done := JudgeUpdateRun(*rec, st, readErr, time.Now())
	if done {
		// Recorded and audited even if the caller has gone.
		dctx := context.WithoutCancel(ctx)
		finished, err := s.updateRuns.FinishUpdateRun(dctx, serverID, rec.RunID, run)
		if err != nil {
			return nil, err
		}
		if finished {
			action := "server.updates_succeeded"
			detail := map[string]any{"action": run.Action, "packages": run.Packages, "seconds": int(run.FinishedAt.Sub(run.StartedAt).Seconds())}
			if run.Outcome != hostupdate.OutcomeSucceeded {
				action, detail["error"] = "server.updates_failed", run.Error
			}
			s.auditUpdates(dctx, srv.OrgID, rec.StartedByID, serverID, action, detail)
			// Read what the run changed; bounded by the read's own timeout.
			go func() {
				if _, err := s.ReadUpdates(dctx, serverID); err != nil {
					s.log.Warn("read updates after a run", zap.Error(err), zap.String("server_id", serverID))
				}
			}()
		}
	}
	f.runID, f.at, f.run = rec.RunID, time.Now(), run
	return &run, nil
}

// followedRun is a server's last status read of its run (Service.UpdateRun).
type followedRun struct {
	mu    sync.Mutex
	runID string
	at    time.Time
	run   UpdateRun
}

// FollowUpdateRuns follows every unfinished run once, so a run nobody
// watches is still recorded and audited.
func (s *Service) FollowUpdateRuns(ctx context.Context) {
	if s.updateRuns == nil {
		return
	}
	ids, err := s.updateRuns.ListUnfinishedUpdateRuns(ctx)
	if err != nil {
		s.log.Warn("list unfinished update runs", zap.Error(err))
		return
	}
	for _, id := range ids {
		if _, err := s.UpdateRun(ctx, id); err != nil && ctx.Err() == nil {
			s.log.Warn("follow an update run", zap.Error(err), zap.String("server_id", id))
		}
	}
}

// HostUpdating reports whether a Docker update or a reboot runs on the
// server: deploys there wait (deployment.HostUpdateGate, for servers).
func (s *Service) HostUpdating(ctx context.Context, serverID string) (bool, error) {
	if s.updateRuns == nil {
		return false, nil
	}
	updating, err := s.updateRuns.ServerUpdating(ctx, serverID)
	if err != nil || !updating {
		return false, err
	}
	// The row says unfinished: the host says whether it still is.
	run, err := s.UpdateRun(ctx, serverID)
	if err != nil {
		return false, err
	}
	return run != nil && run.FinishedAt == nil, nil
}

func (s *Service) auditUpdates(ctx context.Context, orgID, actorID, serverID, action string, detail map[string]any) {
	if s.updateAudit != nil {
		s.updateAudit.WriteAuditLogDetail(ctx, orgID, actorID, action, "server:"+serverID, detail)
	}
}

func (r *postgresRepository) StartUpdateRun(ctx context.Context, serverID, runID, action, actorID string) (bool, error) {
	by := pgtype.UUID{}
	if u, err := uuid.Parse(actorID); err == nil {
		by = pgtype.UUID{Bytes: u, Valid: true}
	}
	n, err := r.q.StartUpdateRun(ctx, sqlcdb.StartUpdateRunParams{ServerID: pgUUID(serverID), RunID: runID, Action: action, StartedBy: by})
	if err != nil {
		return false, fmt.Errorf("start update run: %w", err)
	}
	return n == 1, nil
}

func (r *postgresRepository) FinishUpdateRun(ctx context.Context, serverID, runID string, run UpdateRun) (bool, error) {
	finished := time.Now()
	if run.FinishedAt != nil {
		finished = *run.FinishedAt
	}
	n, err := r.q.FinishUpdateRun(ctx, sqlcdb.FinishUpdateRunParams{
		FinishedAt: pgtype.Timestamptz{Time: finished, Valid: true}, Outcome: run.Outcome, Packages: int32(max(run.Packages, 0)),
		Error: run.Error, Output: TailBytes(strings.Join(run.Output, "\n"), updateRunOutput), ServerID: pgUUID(serverID), RunID: runID,
	})
	if err != nil {
		return false, fmt.Errorf("finish update run: %w", err)
	}
	return n == 1, nil
}

func (r *postgresRepository) GetUpdateRun(ctx context.Context, serverID string) (*UpdateRunRecord, error) {
	row, err := r.q.GetUpdateRun(ctx, pgUUID(serverID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get update run: %w", err)
	}
	rec := &UpdateRunRecord{RunID: row.RunID, UpdateRun: UpdateRun{
		Action: row.Action, Outcome: row.Outcome, Step: hostupdate.StepStarting, Packages: int(row.Packages),
		StartedAt: row.StartedAt.Time, StartedBy: row.StartedByName, Error: row.Error, Output: []string{},
	}}
	if row.StartedBy.Valid {
		rec.StartedByID = uuid.UUID(row.StartedBy.Bytes).String()
	}
	if row.FinishedAt.Valid {
		t := row.FinishedAt.Time
		rec.FinishedAt, rec.Step = &t, hostupdate.StepDone
	}
	if row.Output != "" {
		rec.Output = strings.Split(row.Output, "\n")
	}
	return rec, nil
}

func (r *postgresRepository) ListUnfinishedUpdateRuns(ctx context.Context) ([]string, error) {
	rows, err := r.q.ListUnfinishedUpdateRuns(ctx)
	if err != nil {
		return nil, fmt.Errorf("list unfinished update runs: %w", err)
	}
	ids := make([]string, 0, len(rows))
	for _, id := range rows {
		ids = append(ids, uuid.UUID(id.Bytes).String())
	}
	return ids, nil
}

func (r *postgresRepository) ServerUpdating(ctx context.Context, serverID string) (bool, error) {
	updating, err := r.q.ServerUpdating(ctx, pgUUID(serverID))
	if err != nil {
		return false, fmt.Errorf("server updating: %w", err)
	}
	return updating, nil
}

// TailBytes is the last n bytes of s at most, from a rune's start.
func TailBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}
