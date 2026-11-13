package instance_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/instance"
	"github.com/Nickpap21/toft/backend/pkg/buildinfo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func upgradeService(t *testing.T, files map[string]string) (*instance.Service, *fakeRepo, *fakeProbe, string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	repo := newFakeRepo(ada, bob)
	repo.operators[ada.id] = "first_run"
	probe := &fakeProbe{}
	svc := instance.NewService(repo, zap.NewNop()).WithPasswordVerifier(fakePasswords{}).
		WithPanelMonitor(probe, &fakeStore{}, t.TempDir(), t.TempDir()).WithUpgrades(dir)
	return svc, repo, probe, dir
}

// The version a test binary runs: "dev" unless a release stamped it.
func running(t *testing.T, v string) {
	t.Helper()
	old := buildinfo.Version
	buildinfo.Version = v
	t.Cleanup(func() { buildinfo.Version = old })
}

func TestUpgradeStatus(t *testing.T) {
	ctx := context.Background()
	running(t, "v0.1.2")

	// deploy.sh: no state.json.
	svc, _, _, _ := upgradeService(t, nil)
	st, err := svc.UpgradeStatus(ctx, ada.id)
	require.NoError(t, err)
	assert.Equal(t, instance.InstallDeploySh, st.Install)
	assert.Nil(t, st.Run)
	_, err = svc.UpgradeStatus(ctx, bob.id)
	assert.ErrorIs(t, err, instance.ErrNotOperator)

	svc, _, _, _ = upgradeService(t, map[string]string{
		"state.json":                  `{"version":"v0.1.2","updated_at":"2026-10-17T14:02:00Z"}`,
		"available.json":              `{"current":"v0.1.2","latest":"v0.1.3","checked_at":"2026-10-21T03:00:00Z"}`,
		"run.json":                    `{"to":"v0.1.2","from":"v0.1.1","started_at":"2026-10-17T14:00:12Z","finished_at":"2026-10-17T14:02:00Z","step":"health","outcome":"succeeded","notes":{"pull":"2 images"}}`,
		"run.out":                     "Upgrading Toft v0.1.1 → v0.1.2\n  ✓ Images ready\nToft v0.1.2 is running.\n",
		"v0.1.0-20261016T100000.dump": "x",
		"v0.1.1-20261017T140012.dump": "x",
	})
	st, err = svc.UpgradeStatus(ctx, ada.id)
	require.NoError(t, err)
	assert.Equal(t, instance.InstallInstaller, st.Install)
	assert.Equal(t, "v0.1.3", st.Latest)
	assert.True(t, st.Available)
	assert.Equal(t, "https://github.com/Nickpap21/toft/releases/tag/v0.1.3", st.NotesURL)
	require.NotNil(t, st.Run)
	assert.Equal(t, "succeeded", st.Run.Outcome)
	assert.Equal(t, []string{"Upgrading Toft v0.1.1 → v0.1.2", "  ✓ Images ready", "Toft v0.1.2 is running."}, st.Run.Output)
	assert.Equal(t, map[string]string{"pull": "2 images"}, st.Run.Notes)
	assert.Equal(t, 2, st.Dumps)
	assert.NotNil(t, st.LastDumpAt)
}

func TestStartUpgrade(t *testing.T) {
	ctx := context.Background()
	running(t, "v0.1.2")
	files := map[string]string{
		"state.json":     `{"version":"v0.1.2"}`,
		"available.json": `{"latest":"v0.1.3"}`,
	}

	svc, _, probe, _ := upgradeService(t, files)
	_, err := svc.StartUpgrade(ctx, ada.id, "wrong", "v0.1.3")
	assert.ErrorIs(t, err, instance.ErrWrongPassword)
	for _, to := range []string{"v0.1.2", "v0.1.4", "v0.1.3; reboot", "latest"} {
		_, err = svc.StartUpgrade(ctx, ada.id, pw, to)
		assert.ErrorIs(t, err, instance.ErrBadRelease, to)
	}
	assert.Empty(t, probe.ran, "nothing ran on the host")

	st, err := svc.StartUpgrade(ctx, ada.id, pw, "v0.1.3")
	require.NoError(t, err)
	require.Len(t, probe.ran, 1)
	argv := probe.ran[0]
	assert.Equal(t, "systemd-run", argv[0])
	assert.Equal(t, []string{"/usr/local/bin/toft", "upgrade", "--to", "v0.1.3"}, argv[len(argv)-4:])
	require.NotNil(t, st.Run, "the page follows it at once")
	assert.Equal(t, "starting", st.Run.Step)
	assert.Equal(t, "running", st.Run.Outcome)

	_, err = svc.StartUpgrade(ctx, ada.id, pw, "v0.1.3")
	assert.ErrorIs(t, err, instance.ErrUpgradeRunning)

	// deploy.sh installs upgrade with deploy.sh.
	svc, _, _, _ = upgradeService(t, map[string]string{"available.json": `{"latest":"v0.1.3"}`})
	_, err = svc.StartUpgrade(ctx, ada.id, pw, "v0.1.3")
	assert.ErrorIs(t, err, instance.ErrUpgradeDeploySh)
}

func TestCheckRelease(t *testing.T) {
	ctx := context.Background()
	svc, _, probe, _ := upgradeService(t, map[string]string{"state.json": `{"version":"v0.1.2"}`})
	_, err := svc.CheckRelease(ctx, ada.id)
	require.NoError(t, err)
	assert.Equal(t, [][]string{{"/usr/local/bin/toft", "release-check"}}, probe.ran)
	_, err = svc.CheckRelease(ctx, bob.id)
	assert.ErrorIs(t, err, instance.ErrNotOperator)
}

// A finished upgrade is written to the audit log once, as its starter's.
func TestRecordUpgrade(t *testing.T) {
	ctx := context.Background()
	running(t, "v0.1.2")
	svc, repo, _, dir := upgradeService(t, map[string]string{
		"state.json":     `{"version":"v0.1.2"}`,
		"available.json": `{"latest":"v0.1.3"}`,
	})
	_, err := svc.StartUpgrade(ctx, ada.id, pw, "v0.1.3")
	require.NoError(t, err)
	started := time.Now().UTC().Add(time.Second).Format(time.RFC3339Nano)
	finished := time.Now().UTC().Add(110 * time.Second).Format(time.RFC3339Nano)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "run.json"),
		[]byte(`{"to":"v0.1.3","from":"v0.1.2","started_at":"`+started+`","finished_at":"`+finished+`","step":"health","outcome":"rolled_back","error":"v0.1.3 did not become healthy: rolled back"}`), 0o600))

	svc.RecordUpgrade(ctx)
	svc.RecordUpgrade(ctx)
	var rows []auditRow
	for _, a := range repo.audit {
		if strings.HasPrefix(a.action, "panel.upgrade") {
			rows = append(rows, a)
		}
	}
	require.Len(t, rows, 2, "started, then rolled back once")
	assert.Equal(t, "panel.upgrade_rolled_back", rows[1].action)
	assert.Equal(t, ada.id, rows[1].actor)
	assert.Contains(t, rows[1].detail, `"seconds":109`)
}

// A disk clean keeps the running release, the one before and, while
// toft upgrade runs, the release it pulls (D-135).
func TestPanelReleases(t *testing.T) {
	svc, _, _, _ := upgradeService(t, nil)
	assert.Nil(t, svc.PanelReleases(), "deploy.sh: unknown, so none is offered")
	svc, _, _, _ = upgradeService(t, map[string]string{"state.json": `{"version":"v0.1.2","previous":"v0.1.1"}`})
	assert.Equal(t, []string{"v0.1.2", "v0.1.1"}, svc.PanelReleases())
	svc, _, _, _ = upgradeService(t, map[string]string{
		"state.json": `{"version":"v0.1.2","previous":"v0.1.1"}`,
		"run.json":   `{"to":"v0.1.3","from":"v0.1.2","started_at":"2026-10-31T12:00:00Z","step":"pull","outcome":"running"}`,
	})
	assert.Equal(t, []string{"v0.1.2", "v0.1.1", "v0.1.3"}, svc.PanelReleases())
}
