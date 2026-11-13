package instance_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// panelProbeOut is the capability probe's output on a KVM guest with gVisor
// registered as Toft registers it.
func panelProbeOut(kernel string) string {
	return strings.Join([]string{
		"kernel:" + kernel, "arch:x86_64", "cgroup:2", "virt:kvm", "kvm:0", "docker:29.4.3",
		`security:["name=apparmor","name=seccomp,profile=builtin","name=cgroupns"]`,
		`runtimeargs:{"runc":{"path":"runc"},"runsc":{"path":"/usr/bin/runsc","runtimeArgs":["--network=host","--oci-seccomp"]}}`,
		"runsc:runsc version release-20261012.0", "runtime:runc:ok", "runtime:runsc:ok",
	}, "\n")
}

func sandboxedState(t *testing.T, svc *instance.Service) string {
	t.Helper()
	c, err := svc.StoredPanelCapabilities(context.Background())
	require.NoError(t, err)
	require.NotNil(t, c)
	for _, l := range c.Levels {
		if l.ID == "sandboxed" {
			return l.State
		}
	}
	return ""
}

// The panel's own server is probed through the host helper with a server's
// script (D-97); a failed probe keeps what the last one read, with why.
func TestPanelProbe(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo(ada, bob)
	repo.operators[ada.id] = "first_run"
	out, fail := panelProbeOut("6.8.0-146-generic"), error(nil)
	probe := &fakeProbe{host: func(argv []string, _ io.Reader, _ func(string)) (string, error) {
		require.Equal(t, "sh", argv[0])
		require.Equal(t, "-c", argv[1])
		require.Contains(t, argv[2], "| base64 -d | sh")
		return out, fail
	}}
	svc := instance.NewService(repo, zap.NewNop()).WithPanelMonitor(probe, &fakeStore{}, t.TempDir(), t.TempDir())

	_, err := svc.ProbePanel(ctx, bob.id)
	require.ErrorIs(t, err, instance.ErrNotOperator)
	c, err := svc.PanelCapabilities(ctx, ada.id)
	require.NoError(t, err)
	assert.Nil(t, c, "never probed")

	c, err = svc.ProbePanel(ctx, ada.id)
	require.NoError(t, err)
	assert.Equal(t, "6.8.0-146-generic", c.Kernel)
	assert.Equal(t, "available", sandboxedState(t, svc))

	fail = errors.New("sh exited 1: docker: not found")
	c, err = svc.ProbePanel(ctx, ada.id)
	require.NoError(t, err)
	assert.Equal(t, "6.8.0-146-generic", c.Kernel, "the last read is kept")
	assert.Contains(t, c.Error, "docker: not found")
	assert.Equal(t, "available", sandboxedState(t, svc))
}

// gVisor isn't offered where nothing would use it, nor where the probe says
// it can't run.
func TestCheckPanelGvisor(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo(ada)
	repo.operators[ada.id] = "first_run"
	kernel := "4.19.0"
	probe := &fakeProbe{host: func([]string, io.Reader, func(string)) (string, error) { return panelProbeOut(kernel), nil }}
	svc := instance.NewService(repo, zap.NewNop()).WithPanelMonitor(probe, &fakeStore{}, t.TempDir(), t.TempDir())

	require.NoError(t, svc.CheckPanelGvisor(ctx), "never probed: the install's own check decides")
	_, err := svc.ProbePanel(ctx, ada.id)
	require.NoError(t, err)
	assert.ErrorIs(t, svc.CheckPanelGvisor(ctx), instance.ErrPanelGvisorUnsupported)

	kernel = "6.8.0-146-generic"
	_, err = svc.ProbePanel(ctx, ada.id)
	require.NoError(t, err)
	require.NoError(t, svc.CheckPanelGvisor(ctx))

	require.NoError(t, repo.MergeSetting(ctx, instance.SettingServerRole, []byte(`{"role":"control_plane"}`), ada.id))
	assert.ErrorIs(t, svc.CheckPanelGvisor(ctx), instance.ErrPanelNoApps)
}

// The install runs a server's steps through the host helper: its commands
// on the host's shell, a file written through stdin, apt's output streamed.
// A failure is recorded with its log, audited with its step, and the server
// is probed again.
func TestInstallPanelGvisorThroughTheHelper(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo(ada)
	repo.operators[ada.id] = "first_run"
	wrote := map[string]string{}
	probe := &fakeProbe{host: func(argv []string, stdin io.Reader, logFn func(string)) (string, error) {
		require.Equal(t, []string{"sh", "-c"}, argv[:2])
		cmd := argv[2]
		switch {
		case strings.Contains(cmd, "| base64 -d | sh"):
			return panelProbeOut("6.8.0-146-generic"), nil
		case strings.Contains(cmd, "os-release"):
			return "6.8.0-146-generic\namd64\n/usr/bin/apt-get\nkvm\n\nUbuntu 24.04.1 LTS", nil
		case strings.HasPrefix(cmd, "docker version"):
			return "29.4.3", nil
		case strings.HasPrefix(cmd, "sha256sum"):
			return "", nil
		case strings.Contains(cmd, "cat > "):
			require.NotNil(t, stdin)
			b, err := io.ReadAll(stdin)
			require.NoError(t, err)
			wrote[cmd[strings.Index(cmd, "cat > ")+len("cat > "):]] = string(b)
			return "", nil
		case strings.HasPrefix(cmd, "dpkg-query"):
			return "", errors.New("dpkg-query exited 1")
		case strings.Contains(cmd, "apt-get"):
			require.NotNil(t, logFn, "apt's output is streamed")
			logFn("Get:1 https://storage.googleapis.com/gvisor/releases 20261012/main amd64 runsc")
			logFn("E: Could not connect to storage.googleapis.com:443")
			return "", errors.New("sh exited 100: E: Could not connect to storage.googleapis.com:443")
		}
		t.Errorf("unexpected command %q", cmd)
		return "", errors.New("unexpected")
	}}
	svc := instance.NewService(repo, zap.NewNop()).WithPanelMonitor(probe, &fakeStore{}, t.TempDir(), t.TempDir())

	var lines []string
	err := svc.InstallPanelGvisor(ctx, ada.id, func(l string) { lines = append(lines, l) })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "apt-get install runsc=20261012.0")
	assert.Contains(t, wrote["/etc/apt/sources.list.d/gvisor.list"], "https://storage.googleapis.com/gvisor/releases 20261012 main")
	assert.Contains(t, wrote["/usr/share/keyrings/gvisor-archive-keyring.asc"], "BEGIN PGP PUBLIC KEY BLOCK")
	assert.Contains(t, lines, "[3/6] Installing runsc")
	assert.Contains(t, lines, "Get:1 https://storage.googleapis.com/gvisor/releases 20261012/main amd64 runsc")
	assert.False(t, svc.PanelGvisorInstalling())

	run, err := svc.PanelGvisorRun(ctx, ada.id)
	require.NoError(t, err)
	require.NotNil(t, run)
	require.NotNil(t, run.FinishedAt)
	require.NotNil(t, run.OK)
	assert.False(t, *run.OK)
	assert.Equal(t, "Ada", run.StartedBy)
	assert.Equal(t, "20261012", run.Release)
	assert.Contains(t, run.Error, "Could not connect")
	assert.Contains(t, run.Log, "Get:1 https://storage.googleapis.com")
	assert.Contains(t, run.Log, "  ✗ apt-get install runsc=20261012.0")

	last := repo.audit[len(repo.audit)-1]
	assert.Equal(t, "panel.gvisor_install_failed", last.action)
	assert.Contains(t, last.detail, `"step":"[3/6] Installing runsc"`)
	assert.Equal(t, "available", sandboxedState(t, svc), "probed again afterwards")
}

// A restart in the middle of an install closes its run as failed.
func TestCloseUnfinishedPanelGvisorRun(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo(ada)
	repo.operators[ada.id] = "first_run"
	svc := instance.NewService(repo, zap.NewNop())
	require.NoError(t, svc.CloseUnfinishedPanelGvisorRun(ctx), "no run yet")
	require.NoError(t, repo.MergeSetting(ctx, instance.SettingPanelGvisorRun,
		[]byte(`{"release":"20261012","started_at":"2026-10-21T12:00:00Z","started_by":"Ada","finished_at":null,"ok":null,"error":"","log":"[1/5] Checking the server\n"}`), ada.id))
	require.NoError(t, svc.CloseUnfinishedPanelGvisorRun(ctx))
	run, err := svc.PanelGvisorRun(ctx, ada.id)
	require.NoError(t, err)
	require.NotNil(t, run.OK)
	assert.False(t, *run.OK)
	assert.Contains(t, run.Error, "restarted during the install")
	assert.Equal(t, "[1/5] Checking the server\n", run.Log)
}

// The panel's isolation endpoints (D-97): operators only, the install with
// a session and the password, every answer as the spec says.
func TestPanelGvisorRoutes(t *testing.T) {
	repo := newFakeRepo(ada, bob)
	repo.operators[ada.id] = instance.SourceFirstRun
	probe := &fakeProbe{host: func([]string, io.Reader, func(string)) (string, error) {
		return panelProbeOut("6.8.0-146-generic"), nil
	}}
	svc := instance.NewService(repo, zap.NewNop()).WithPanelMonitor(probe, &fakeStore{}, t.TempDir(), t.TempDir())

	status, body := call(t, setupRouter(svc, ada.id, false, false), http.MethodGet, "/api/v1/instance/panel/capabilities", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Nil(t, body["data"], "never probed")
	status, body = call(t, setupRouter(svc, ada.id, false, false), http.MethodPost, "/api/v1/instance/panel/capabilities", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "6.8.0-146-generic", body["data"].(map[string]any)["kernel"])
	status, body = call(t, setupRouter(svc, ada.id, false, false), http.MethodGet, "/api/v1/instance/panel/gvisor", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Nil(t, body["data"])

	status, body = call(t, setupRouter(svc, ada.id, false, false), http.MethodPost, "/api/v1/instance/panel/gvisor", nil)
	assert.Equal(t, http.StatusForbidden, status)
	assert.Equal(t, "reauth_required", code(body))
	status, body = call(t, setupRouter(svc, ada.id, true, true), http.MethodPost, "/api/v1/instance/panel/gvisor", nil)
	assert.Equal(t, http.StatusForbidden, status)
	assert.Equal(t, "session_required", code(body))

	require.NoError(t, repo.MergeSetting(context.Background(), instance.SettingServerRole, []byte(`{"role":"control_plane"}`), ada.id))
	status, body = call(t, setupRouter(svc, ada.id, true, false), http.MethodPost, "/api/v1/instance/panel/gvisor", nil)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "panel_control_plane_only", code(body))

	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/instance/panel/capabilities"}, {http.MethodPost, "/api/v1/instance/panel/capabilities"},
		{http.MethodGet, "/api/v1/instance/panel/gvisor"}, {http.MethodPost, "/api/v1/instance/panel/gvisor"},
	} {
		status, _ = call(t, setupRouter(svc, bob.id, true, false), r.method, r.path, nil)
		assert.Equal(t, http.StatusForbidden, status, r.path)
	}
}
