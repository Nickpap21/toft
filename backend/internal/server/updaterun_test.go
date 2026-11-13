package server

import (
	"context"
	"errors"
	"io"
	"regexp"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/hostupdate"
	"github.com/Nickpap21/toft/backend/pkg/sshpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJudgeUpdateRun(t *testing.T) {
	start := time.Date(2026, 10, 30, 18, 0, 0, 0, time.UTC)
	rec := func(action string) UpdateRunRecord {
		return UpdateRunRecord{RunID: "r1", UpdateRun: UpdateRun{Action: action, Outcome: hostupdate.OutcomeRunning, Step: hostupdate.StepStarting, StartedAt: start}}
	}
	host := func(outcome string, active bool, boot, nowBoot string) hostupdate.Status {
		return hostupdate.Status{ID: "r1", Step: hostupdate.StepInstall, Outcome: outcome, Active: active, Boot: boot, NowBoot: nowBoot, Packages: 3, Output: []string{"x"}}
	}
	finished := start.Add(2 * time.Minute)
	down := errors.New("dial tcp: connection refused")

	cases := []struct {
		name    string
		rec     UpdateRunRecord
		st      hostupdate.Status
		readErr error
		after   time.Duration
		outcome string
		done    bool
		err     string
	}{
		{"not begun yet", rec("install"), hostupdate.Status{ID: "old"}, nil, time.Minute, hostupdate.OutcomeRunning, false, ""},
		{"never began", rec("install"), hostupdate.Status{ID: "old"}, nil, 3 * time.Minute, hostupdate.OutcomeFailed, true, "The run didn't start on the server."},
		{"installing", rec("install"), host(hostupdate.OutcomeRunning, true, "b1", "b1"), nil, time.Minute, hostupdate.OutcomeRunning, false, ""},
		{"installed", rec("install"), func() hostupdate.Status {
			st := host(hostupdate.OutcomeSucceeded, false, "b1", "b1")
			st.Finished = &finished
			return st
		}(), nil, 3 * time.Minute, hostupdate.OutcomeSucceeded, true, ""},
		{"apt failed", rec("install"), func() hostupdate.Status {
			st := host(hostupdate.OutcomeFailed, false, "b1", "b1")
			st.Finished, st.Error = &finished, "apt couldn't install the updates"
			return st
		}(), nil, 3 * time.Minute, hostupdate.OutcomeFailed, true, "apt couldn't install the updates"},
		{"killed", rec("install"), host(hostupdate.OutcomeRunning, false, "b1", "b1"), nil, 3 * time.Minute, hostupdate.OutcomeFailed, true, "The run stopped before it finished."},
		{"restarted under it", rec("docker"), host(hostupdate.OutcomeRunning, false, "b1", "b2"), nil, 3 * time.Minute, hostupdate.OutcomeFailed, true,
			"The server restarted during the run, so it stopped before it finished."},
		{"unreachable mid-install", rec("install"), hostupdate.Status{}, down, time.Hour, hostupdate.OutcomeRunning, false, ""},
		{"lost", rec("install"), hostupdate.Status{}, down, 3 * time.Hour, hostupdate.OutcomeFailed, true, "Toft lost the server during the run, so how it ended isn't known."},
		{"rebooting, down", rec("reboot"), hostupdate.Status{}, down, time.Minute, hostupdate.OutcomeRebooting, false, ""},
		{"rebooting, not yet", rec("reboot"), host(hostupdate.OutcomeRebooting, false, "b1", "b1"), nil, time.Minute, hostupdate.OutcomeRebooting, false, ""},
		{"back on a new boot", rec("reboot"), host(hostupdate.OutcomeRebooting, false, "b1", "b2"), nil, 2 * time.Minute, hostupdate.OutcomeSucceeded, true, ""},
		{"never restarted", rec("reboot"), host(hostupdate.OutcomeRebooting, false, "b1", "b1"), nil, 11 * time.Minute, hostupdate.OutcomeFailed, true,
			"The server didn't restart within 10 minutes."},
		{"never back", rec("reboot"), hostupdate.Status{}, down, 11 * time.Minute, hostupdate.OutcomeFailed, true, "The server isn't back 10 minutes after its reboot."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run, done := JudgeUpdateRun(c.rec, c.st, c.readErr, start.Add(c.after))
			assert.Equal(t, c.outcome, run.Outcome)
			assert.Equal(t, c.done, done)
			assert.Equal(t, c.err, run.Error)
			assert.Equal(t, done, run.FinishedAt != nil)
			assert.NotNil(t, run.Output)
		})
	}
	run, _ := JudgeUpdateRun(rec("install"), func() hostupdate.Status {
		st := host(hostupdate.OutcomeSucceeded, false, "b1", "b1")
		st.Finished = &finished
		return st
	}(), nil, start.Add(time.Hour))
	assert.Equal(t, finished, *run.FinishedAt, "the host's end, not when the panel saw it")
	assert.Equal(t, 3, run.Packages)
}

func TestCheckUpdateAction(t *testing.T) {
	read := time.Now()
	only := &HostUpdates{ReadAt: &read, Other: 1, Held: 1, Docker: DockerEngine{Package: "docker-ce", Candidate: "29.8.2"}}
	assert.ErrorIs(t, CheckUpdateAction(only, hostupdate.ActionInstall), ErrNothingPending, "only Docker's update waits: the install holds it")
	assert.NoError(t, CheckUpdateAction(only, hostupdate.ActionDocker))
	containerd := &HostUpdates{ReadAt: &read, Other: 1, Held: 1, Docker: DockerEngine{Package: "docker-ce"}}
	assert.NoError(t, CheckUpdateAction(containerd, hostupdate.ActionDocker), "containerd.io's update alone")
	assert.NoError(t, CheckUpdateAction(nil, hostupdate.ActionReboot))
	assert.ErrorIs(t, CheckUpdateAction(nil, hostupdate.ActionInstall), ErrNothingPending)
	snap := &HostUpdates{ReadAt: &read, Security: 2, Docker: DockerEngine{Package: ""}}
	assert.NoError(t, CheckUpdateAction(snap, hostupdate.ActionInstall))
	assert.ErrorIs(t, CheckUpdateAction(snap, hostupdate.ActionDocker), ErrDockerNotPackaged)
	current := &HostUpdates{ReadAt: &read, Docker: DockerEngine{Package: "docker.io"}}
	assert.ErrorIs(t, CheckUpdateAction(current, hostupdate.ActionDocker), ErrNothingPending)
	assert.Error(t, CheckUpdateAction(current, "upgrade"))
}

// runHost is a server's SSH for its runs of updates: the start answers
// answer (or err), and the status shows the run once the host began it.
type runHost struct {
	sshpool.RemoteHost
	answer string
	err    error
	began  bool
	runID  string
}

func (h *runHost) ExecInput(_ context.Context, cmd string, _ io.Reader) (string, error) {
	if m := regexp.MustCompile(`run\.sh" ([0-9a-f]{32}) `).FindStringSubmatch(cmd); m != nil && (h.began || h.answer == "started") {
		h.runID = m[1]
	}
	return h.answer, h.err
}

func (h *runHost) Exec(context.Context, string) (string, error) {
	st := "active:inactive\nnowboot:b1\nnow:2000000000\n"
	if h.runID != "" {
		st = "active:active\nnowboot:b1\nnow:2000000000\nid:" + h.runID + "\naction:install\nstep:install\noutcome:running\nstarted:1999999990\nfinished:\nboot:b1\npackages:1\nerror:\n"
	}
	return st + "--output--\n", nil
}

type runConn struct{ h *runHost }

func (c runConn) Handle(sshpool.Target) sshpool.RemoteHost { return c.h }
func (runConn) Invalidate(string)                          {}

// runStore keeps one server's run as the table does: StartUpdateRun takes
// it only when the last has finished, FinishUpdateRun finishes it once.
type runStore struct {
	UpdateStore
	u   *HostUpdates
	rec *UpdateRunRecord
}

func (s *runStore) GetUpdates(context.Context, string) (*HostUpdates, error) { return s.u, nil }
func (s *runStore) StartUpdateRun(_ context.Context, _, runID, action, actorID string) (bool, error) {
	if s.rec != nil && s.rec.FinishedAt == nil {
		return false, nil
	}
	s.rec = &UpdateRunRecord{RunID: runID, StartedByID: actorID, UpdateRun: UpdateRun{Action: action, Outcome: hostupdate.OutcomeRunning,
		Step: hostupdate.StepStarting, StartedAt: time.Now(), Output: []string{}}}
	return true, nil
}
func (s *runStore) FinishUpdateRun(_ context.Context, _, runID string, run UpdateRun) (bool, error) {
	if s.rec == nil || s.rec.RunID != runID || s.rec.FinishedAt != nil {
		return false, nil
	}
	s.rec.UpdateRun = run
	return true, nil
}
func (s *runStore) GetUpdateRun(context.Context, string) (*UpdateRunRecord, error) {
	if s.rec == nil {
		return nil, nil
	}
	rec := *s.rec
	return &rec, nil
}
func (s *runStore) ListUnfinishedUpdateRuns(context.Context) ([]string, error) { return nil, nil }
func (s *runStore) ServerUpdating(context.Context, string) (bool, error) {
	return s.rec != nil && s.rec.FinishedAt == nil && s.rec.Action != hostupdate.ActionInstall, nil
}

// deploys answers DeployRunning from its list, one answer a call.
type deploys []bool

func (d *deploys) DeployRunning(context.Context, string) (bool, error) {
	busy := (*d)[0]
	if len(*d) > 1 {
		*d = (*d)[1:]
	}
	return busy, nil
}

// Starting a run (D-134): a deploy that began before or while the row was
// written refuses a Docker update; a lost answer is asked about, and the
// run is failed at once only when the host never began it.
func TestStartUpdates(t *testing.T) {
	ctx := context.Background()
	read := time.Now()
	store := &runStore{u: &HostUpdates{ReadAt: &read, Security: 1, Docker: DockerEngine{Package: "docker-ce", Candidate: "29.9.2"}}}
	host := &runHost{}
	busy := deploys{true}
	var log audits
	svc := NewService(newFakeRepo(&Server{ID: "s1", Name: "fsn1", OrgID: "o1", IPAddress: "192.0.2.1"}), "")
	svc.WithSSH(runConn{host}, nil)
	svc.WithUpdates(store, nil)
	svc.WithUpdateRuns(store, &busy, &log)

	_, err := svc.StartUpdates(ctx, "s1", "u1", hostupdate.ActionDocker)
	require.ErrorIs(t, err, ErrDeployRunning)
	assert.Nil(t, store.rec, "refused before the row")

	busy = deploys{false, true}
	_, err = svc.StartUpdates(ctx, "s1", "u1", hostupdate.ActionDocker)
	require.ErrorIs(t, err, ErrDeployRunning, "the deploy began while the row was written")
	require.NotNil(t, store.rec.FinishedAt)
	assert.Equal(t, "A deploy started first.", store.rec.Error)

	host.err = errors.New("ssh: connection reset")
	_, err = svc.StartUpdates(ctx, "s1", "u1", hostupdate.ActionInstall)
	require.ErrorIs(t, err, ErrHostUnreachable)
	assert.Equal(t, hostupdate.OutcomeFailed, store.rec.Outcome, "the host never began it")

	host.began = true
	run, err := svc.StartUpdates(ctx, "s1", "u1", hostupdate.ActionInstall)
	require.NoError(t, err, "the host began it; only its answer was lost")
	assert.Equal(t, hostupdate.OutcomeRunning, run.Outcome)
	assert.Nil(t, store.rec.FinishedAt)
	_, err = svc.StartUpdates(ctx, "s1", "u1", hostupdate.ActionReboot)
	require.ErrorIs(t, err, ErrUpdateRunning)
	assert.Equal(t, []string{"server.updates_started"}, log.actions)
}
