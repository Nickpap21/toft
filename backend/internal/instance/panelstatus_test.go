package instance_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type fakeProbe struct {
	ran            [][]string
	runErr         error
	runOut         string
	containers     []instance.PanelContainer
	containerCalls int
	cert           *instance.PanelCertificate
	certErr        error
	certCalls      int
	swept          []instance.SweptShell
	// logs is what LogsSince answers, whatever the time.
	logs      []string
	logsSince []time.Time
	logsErr   error // after the lines, as a read cut short
	// host, when set, answers RunOnHostStream (the panel's gVisor, D-97).
	host func(argv []string, stdin io.Reader, logFn func(string)) (string, error)
}

func (f *fakeProbe) Containers(_ context.Context, names []string, _ bool) ([]instance.PanelContainer, error) {
	f.containerCalls++
	out := make([]instance.PanelContainer, len(names))
	for i, n := range names {
		out[i] = instance.PanelContainer{Name: n, State: "running", Health: "healthy"}
		for _, c := range f.containers {
			if c.Name == n {
				out[i] = c
			}
		}
	}
	return out, nil
}
func (f *fakeProbe) Facts(context.Context) (instance.PanelFacts, error) {
	return instance.PanelFacts{CPUs: 4, MemoryBytes: 8 << 30, OS: "Ubuntu 24.04.1 LTS"}, nil
}
func (f *fakeProbe) Disk(context.Context) (instance.PanelDisk, error) {
	return instance.PanelDisk{Images: 3}, nil
}
func (f *fakeProbe) Certificate(_ context.Context, domain string) (*instance.PanelCertificate, error) {
	f.certCalls++
	return f.cert, f.certErr
}
func (f *fakeProbe) Logs(context.Context, string, int, func(string)) error { return nil }
func (f *fakeProbe) LogsSince(_ context.Context, _ string, since time.Time, line func(string)) error {
	f.logsSince = append(f.logsSince, since)
	for _, l := range f.logs {
		line(l)
	}
	return f.logsErr
}
func (f *fakeProbe) RunOnHost(_ context.Context, argv []string) (string, error) {
	f.ran = append(f.ran, argv)
	return f.runOut, f.runErr
}
func (f *fakeProbe) RunOnHostStream(ctx context.Context, argv []string, stdin io.Reader, logFn func(string)) (string, error) {
	if f.host != nil {
		return f.host(argv, stdin, logFn)
	}
	return f.RunOnHost(ctx, argv)
}
func (f *fakeProbe) SweepHelpers(context.Context) ([]instance.SweptShell, error) {
	swept := f.swept
	f.swept = nil
	return swept, nil
}
func (f *fakeProbe) Shell(context.Context, int, int, string) (instance.PanelShell, error) {
	return fakeShell{}, nil
}

type fakeShell struct{}

func (fakeShell) Read([]byte) (int, error)    { return 0, nil }
func (fakeShell) Write(p []byte) (int, error) { return len(p), nil }
func (fakeShell) Resize(int, int) error       { return nil }
func (fakeShell) Close() error                { return nil }
func (fakeShell) Detach() error               { return nil }

type fakeStore struct{ samples []instance.PanelSample }

func (f *fakeStore) InsertPanelSample(_ context.Context, s instance.PanelSample) error {
	f.samples = append(f.samples, s)
	return nil
}
func (f *fakeStore) ListPanelSamples(context.Context, time.Time, time.Duration) ([]instance.PanelSample, error) {
	return f.samples, nil
}
func (f *fakeStore) DeletePanelSamplesBefore(context.Context, time.Time) (int64, error) {
	return 0, nil
}
func (f *fakeStore) PanelDatabase(context.Context) (instance.PanelDatabase, error) {
	return instance.PanelDatabase{Version: "16.4", MaxConnections: 100}, nil
}

// procAt writes a /proc with the given CPU counters.
func procAt(t *testing.T, dir string, busy, idle int) {
	t.Helper()
	files := map[string]string{
		"stat":    "cpu  " + itoa(busy) + " 0 0 " + itoa(idle) + " 0 0 0 0 0 0\n",
		"meminfo": "MemTotal: 8000000 kB\nMemAvailable: 5000000 kB\nCached: 1000000 kB\n",
		"loadavg": "0.5 0.4 0.3 1/1 1\n",
		"uptime":  "100.0 50.0\n",
	}
	for n, b := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, n), []byte(b), 0o644))
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func panelService(t *testing.T, probe *fakeProbe, store *fakeStore, proc string) (*instance.Service, *fakeRepo) {
	t.Helper()
	repo := newFakeRepo(ada, bob)
	repo.operators[ada.id] = "first_run"
	svc := instance.NewService(repo, zap.NewNop()).WithPanelMonitor(probe, store, proc, t.TempDir())
	return svc, repo
}

func TestPanelStatus(t *testing.T) {
	ctx := context.Background()
	probe := &fakeProbe{}
	svc, _ := panelService(t, probe, &fakeStore{}, t.TempDir())

	_, err := svc.PanelStatus(ctx, bob.id)
	assert.ErrorIs(t, err, instance.ErrNotOperator)

	st, err := svc.PanelStatus(ctx, ada.id)
	require.NoError(t, err)
	assert.True(t, st.Healthy)
	require.Len(t, st.Containers, 4)
	assert.Equal(t, "paas-backend", st.Containers[0].Name)
	assert.Equal(t, "api", st.Containers[0].Role)
	assert.Equal(t, "proxy", st.Containers[3].Role)
	assert.Nil(t, st.Certificate, "no domain, no certificate")
	assert.Zero(t, probe.certCalls)

	for _, c := range []instance.PanelContainer{
		{Name: "paas-frontend", State: "restarting"},
		{Name: "paas-frontend", State: "running", Health: "unhealthy"},
		{Name: "paas-frontend", State: "running", Health: "starting"},
		{Name: "paas-frontend", State: "missing"},
	} {
		probe.containers = []instance.PanelContainer{c}
		st, err := svc.PanelStatus(ctx, ada.id)
		require.NoError(t, err)
		assert.False(t, st.Healthy, "%s %s", c.State, c.Health)
	}
	// No health check at all is fine.
	probe.containers = []instance.PanelContainer{{Name: "paas-frontend", State: "running"}}
	st, err = svc.PanelStatus(ctx, ada.id)
	require.NoError(t, err)
	assert.True(t, st.Healthy)
}

func TestPanelStatusFromTheMonitor(t *testing.T) {
	ctx := context.Background()
	probe := &fakeProbe{}
	svc, _ := panelService(t, probe, &fakeStore{}, t.TempDir())

	svc.PanelContainersTick(ctx, time.Now())
	probe.containers = []instance.PanelContainer{{Name: "paas-frontend", State: "restarting"}}
	calls := probe.containerCalls
	st, err := svc.PanelStatus(ctx, ada.id)
	require.NoError(t, err)
	assert.Equal(t, calls, probe.containerCalls, "a fresh tick answers without asking Docker")
	assert.True(t, st.Healthy, "the tick's reading")
	assert.Equal(t, "web", st.Containers[1].Role)

	svc.PanelContainersTick(ctx, time.Now().Add(-time.Minute))
	calls = probe.containerCalls
	st, err = svc.PanelStatus(ctx, ada.id)
	require.NoError(t, err)
	assert.Equal(t, calls+1, probe.containerCalls, "a stale tick is read again")
	assert.False(t, st.Healthy)
}

func TestPanelServer(t *testing.T) {
	ctx := context.Background()
	proc := t.TempDir()
	store := &fakeStore{}
	svc, _ := panelService(t, &fakeProbe{}, store, proc)

	_, err := svc.PanelServer(ctx, ada.id, "1y")
	assert.ErrorIs(t, err, instance.ErrBadRange)
	_, err = svc.PanelServer(ctx, bob.id, "live")
	assert.ErrorIs(t, err, instance.ErrNotOperator)

	srv, err := svc.PanelServer(ctx, ada.id, "live")
	require.NoError(t, err)
	assert.Nil(t, srv.Now, "nothing read yet")
	assert.NotNil(t, srv.Samples, "an empty list, not null")

	// Two readings make a CPU percentage: 100 busy of 400 jiffies.
	t0 := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	procAt(t, proc, 100, 300)
	svc.PanelTick(ctx, t0)
	procAt(t, proc, 200, 600)
	svc.PanelTick(ctx, t0.Add(5*time.Second))
	srv, err = svc.PanelServer(ctx, ada.id, "live")
	require.NoError(t, err)
	require.NotNil(t, srv.Now)
	assert.InDelta(t, 25, srv.Now.CPUPercent, 0.01)
	assert.Equal(t, int64(3000000*1024), srv.Now.MemoryUsedBytes)
	assert.Positive(t, srv.Now.DiskFreeBytes)
	assert.Len(t, srv.Samples, 1)
	assert.Equal(t, 4, srv.CPUs)
	assert.Equal(t, "16.4", srv.Database.Version)
	assert.Empty(t, store.samples, "nothing stored before a minute has passed")

	// A minute after the first reading, the minute's average is stored.
	procAt(t, proc, 700, 700)
	svc.PanelTick(ctx, t0.Add(time.Minute))
	require.Len(t, store.samples, 1)
	assert.InDelta(t, 60, store.samples[0].CPUPercent, 0.01, "600 busy of 1000 since the first reading")

	srv, err = svc.PanelServer(ctx, ada.id, "7d")
	require.NoError(t, err)
	assert.Len(t, srv.Samples, 1)
}

func TestCheckPanelContainer(t *testing.T) {
	ctx := context.Background()
	svc, _ := panelService(t, &fakeProbe{}, &fakeStore{}, t.TempDir())
	assert.NoError(t, svc.CheckPanelContainer(ctx, ada.id, "paas-postgres"))
	assert.ErrorIs(t, svc.CheckPanelContainer(ctx, ada.id, "storefront-web"), instance.ErrUnknownContainer)
	assert.ErrorIs(t, svc.CheckPanelContainer(ctx, bob.id, "paas-postgres"), instance.ErrNotOperator)
}

// The panel's host shell (D-68): operators only, off with
// TOFT_PANEL_HOST_SHELL=off, and in the audit log.
func TestPanelShell(t *testing.T) {
	ctx := context.Background()
	svc, repo := panelService(t, &fakeProbe{}, &fakeStore{}, t.TempDir())

	assert.ErrorIs(t, svc.CheckPanelShell(ctx, ada.id), instance.ErrPanelShellOff, "off until it is turned on")
	svc.WithPanelShell(true)
	assert.ErrorIs(t, svc.CheckPanelShell(ctx, bob.id), instance.ErrNotOperator)
	require.NoError(t, svc.CheckPanelShell(ctx, ada.id))

	sh, err := svc.OpenPanelShell(ctx, ada.id, "198.51.100.24", 120, 30)
	require.NoError(t, err)
	require.NoError(t, sh.Close())
	svc.ClosedPanelShell(ctx, ada.id, time.Now().Add(-12*time.Minute))
	require.Len(t, repo.audit, 2)
	assert.Equal(t, "panel.shell_opened", repo.audit[0].action)
	assert.Contains(t, repo.audit[0].detail, "198.51.100.24")
	assert.Equal(t, "panel.shell_closed", repo.audit[1].action)
	assert.Contains(t, repo.audit[1].detail, `"seconds":720`)

	_, err = svc.OpenPanelShell(ctx, bob.id, "198.51.100.24", 120, 30)
	assert.ErrorIs(t, err, instance.ErrNotOperator)
	assert.Len(t, repo.audit, 2, "a refused shell writes nothing")
}

func TestSweptShellsClose(t *testing.T) {
	ctx := context.Background()
	probe := &fakeProbe{swept: []instance.SweptShell{
		{ActorID: ada.id, Opened: time.Now().Add(-5 * time.Minute)},
		{Opened: time.Now()}, // from before the label: nobody to write it for
	}}
	svc, repo := panelService(t, probe, &fakeStore{}, t.TempDir())

	svc.SweepPanelHelpers(ctx)
	require.Len(t, repo.audit, 1)
	assert.Equal(t, "panel.shell_closed", repo.audit[0].action)
	assert.Equal(t, ada.id, repo.audit[0].actor)
	assert.Contains(t, repo.audit[0].detail, `"restart":true`)
	assert.Contains(t, repo.audit[0].detail, `"seconds":300`)
}

func TestPanelSystem(t *testing.T) {
	all := "LoadState=loaded\nActiveState=active\n\nLoadState=loaded\nActiveState=active\n\n/dev/sda1 ext4\n"
	assert.Equal(t, instance.PanelSystem{Firewall: "active", SecurityUpdates: "active", Filesystem: "/dev/sda1 ext4"}, instance.ParsePanelSystem(all))
	assert.Equal(t, instance.PanelSystem{Firewall: "failed", SecurityUpdates: "missing"},
		instance.ParsePanelSystem("LoadState=loaded\nActiveState=failed\n\nLoadState=not-found\nActiveState=inactive\n\n"))
	assert.Equal(t, instance.PanelSystem{}, instance.ParsePanelSystem(""))

	ctx := context.Background()
	probe := &fakeProbe{runOut: all}
	svc, _ := panelService(t, probe, &fakeStore{}, t.TempDir())
	srv, err := svc.PanelServer(ctx, ada.id, "live")
	require.NoError(t, err)
	assert.Nil(t, srv.System, "not read yet")

	t0 := time.Now()
	svc.PanelSystemTick(ctx, t0)
	svc.PanelSystemTick(ctx, t0.Add(time.Minute))
	require.Len(t, probe.ran, 1, "once every 10 minutes")
	assert.Equal(t, "sh", probe.ran[0][0])
	srv, err = svc.PanelServer(ctx, ada.id, "live")
	require.NoError(t, err)
	require.NotNil(t, srv.System)
	assert.Equal(t, "active", srv.System.Firewall)
	assert.Equal(t, "/dev/sda1 ext4", srv.System.Filesystem)

	svc.PanelSystemTick(ctx, t0.Add(11*time.Minute))
	assert.Len(t, probe.ran, 2)
}
