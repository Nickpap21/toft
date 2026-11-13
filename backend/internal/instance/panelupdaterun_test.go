package instance_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/instance"
	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// panelHost answers the host helper as the panel's server: the updates
// read, a run's start (answer) and its status (the run started, ended when
// done).
type panelHost struct {
	mu     sync.Mutex
	answer string
	err    error // the start's transport error
	began  bool  // the host began the run, whatever the start answered
	done   bool
	runID  string
	starts int
}

var startedID = regexp.MustCompile(`run\.sh" ([0-9a-f]{32}) `)

func (h *panelHost) run(argv []string, _ io.Reader, _ func(string)) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cmd := argv[2]
	switch {
	case strings.Contains(cmd, "| base64 -d | sh"):
		return "apt:1\nInst liblzma5 [1] (2 Ubuntu:24.04/noble-updates, Ubuntu:24.04/noble-security [arm64])\n" +
			"lists:1000\nuurun:2000\nuuok:2000\nuuunit:loaded active\nreboot:1500\ndocker:29.4.3\nnow:3000\n", nil
	case strings.Contains(cmd, "systemd-run"):
		h.starts++
		if m := startedID.FindStringSubmatch(cmd); m != nil && (h.began || h.answer == "started") {
			h.runID = m[1]
		}
		return h.answer, h.err
	}
	st := "active:inactive\nnowboot:b1\nnow:2000000000\n"
	if h.runID != "" {
		outcome, finished := "running", ""
		if h.done {
			outcome, finished = "succeeded", "2000000000"
		} else {
			st = "active:active\nnowboot:b1\nnow:2000000000\n"
		}
		st += "id:" + h.runID + "\naction:install\nstep:install\noutcome:" + outcome + "\nstarted:1999999990\nfinished:" + finished +
			"\nboot:b1\npackages:1\nerror:\n"
	}
	return st + "--output--\nSetting up liblzma5 ...\n", nil
}

func (h *panelHost) set(f func(h *panelHost)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f(h)
}

// A run on the panel's server (D-134): kept before the host begins it,
// failed at once when the host refused it or never began it, kept running
// when only the answer was lost, audited once when it ends, and holding off
// deploys and upgrades while it lasts.
func TestPanelUpdateRun(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo(ada, bob)
	repo.operators[ada.id] = "first_run"
	host := &panelHost{answer: "busy"}
	running(t, "v0.1.2")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"version":"v0.1.2"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "available.json"), []byte(`{"latest":"v0.1.3"}`), 0o600))
	svc := instance.NewService(repo, zap.NewNop()).WithPasswordVerifier(fakePasswords{}).
		WithPanelMonitor(&fakeProbe{host: host.run}, &fakeStore{}, t.TempDir(), t.TempDir()).WithUpgrades(dir)
	_, err := svc.CheckPanelUpdates(ctx, ada.id)
	require.NoError(t, err)

	_, err = svc.StartPanelUpdates(ctx, bob.id, "install")
	require.ErrorIs(t, err, instance.ErrNotOperator)

	_, err = svc.StartPanelUpdates(ctx, ada.id, "install")
	require.ErrorIs(t, err, server.ErrUpdateRunning, "a run the host had going already")
	run, err := svc.PanelUpdateRun(ctx, ada.id)
	require.NoError(t, err)
	require.NotNil(t, run.FinishedAt)
	assert.Equal(t, "Another run of updates was going on on the server.", run.Error)

	host.set(func(h *panelHost) { h.answer, h.err = "", errors.New("helper exited 255") })
	_, err = svc.StartPanelUpdates(ctx, ada.id, "install")
	require.ErrorIs(t, err, server.ErrHostUnreachable, "the host never began it")
	run, err = svc.PanelUpdateRun(ctx, ada.id)
	require.NoError(t, err)
	assert.Equal(t, "failed", run.Outcome)

	host.set(func(h *panelHost) { h.began = true })
	run, err = svc.StartPanelUpdates(ctx, ada.id, "install")
	require.NoError(t, err, "the host began it; only its answer was lost")
	assert.Equal(t, "running", run.Outcome)
	_, err = svc.StartPanelUpdates(ctx, ada.id, "install")
	require.ErrorIs(t, err, server.ErrUpdateRunning)
	updating, err := svc.PanelUpdating(ctx)
	require.NoError(t, err)
	assert.False(t, updating, "an install doesn't hold deploys off")
	_, err = svc.StartUpgrade(ctx, ada.id, pw, "v0.1.3")
	require.ErrorIs(t, err, server.ErrUpdateRunning, "nor does an upgrade restart the panel under it")

	host.set(func(h *panelHost) { h.done = true })
	svc.ResetPanelRunSeen()
	for range 2 {
		run, err = svc.PanelUpdateRun(ctx, ada.id)
		require.NoError(t, err)
		assert.Equal(t, "succeeded", run.Outcome)
	}
	ended := 0
	for _, a := range repo.audit {
		if a.action == "panel.updates_succeeded" {
			ended++
		}
	}
	assert.Equal(t, 1, ended, "audited once")
	assert.Equal(t, 3, host.starts)
}
