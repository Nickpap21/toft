package instance_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// setupRepo is an instance in setup: the first account ("op") started it.
func setupRepo() *fakeRepo {
	repo := newFakeRepo()
	repo.operators["op"] = instance.SourceFirstRun
	repo.settings = map[string][]byte{instance.SettingSetup: []byte(`{"started_at":"2026-10-17T11:00:00Z"}`)}
	return repo
}

type kitBackups struct{ b *instance.KitBackup }

func (k kitBackups) PanelKitBackup(context.Context) (*instance.KitBackup, error) { return k.b, nil }

type unreadableBackup struct{}

func (unreadableBackup) PanelKitBackup(context.Context) (*instance.KitBackup, error) {
	return nil, errors.New("backup row not decrypted")
}

// ackKit stores the kit as its page does: read it, then name it by its
// fingerprint.
func ackKit(t *testing.T, svc *instance.Service) *instance.Setup {
	t.Helper()
	ctx := context.Background()
	kit, err := svc.RecoveryKit(ctx, "op")
	require.NoError(t, err)
	st, err := svc.PatchSetup(ctx, "op", instance.SetupPatch{KitAcknowledged: true, Fingerprint: kit.Fingerprint})
	require.NoError(t, err)
	return st
}

func TestSetup_StepsAndFinish(t *testing.T) {
	ctx := context.Background()
	repo := setupRepo()
	spy := &routesSpy{}
	svc := instance.NewService(repo, zap.NewNop()).WithPanelRoutes(spy, "", false).WithRecoveryKit("enc-key-hex", "v0.1.0", kitBackups{nil})

	st, err := svc.Setup(ctx, "op")
	require.NoError(t, err)
	assert.True(t, st.InProgress)
	assert.False(t, st.DomainSet)
	assert.Equal(t, instance.RoleApps, st.Role, "Control plane and apps until a role is chosen (today's behaviour)")

	_, err = svc.Setup(ctx, "dev")
	assert.ErrorIs(t, err, instance.ErrNotOperator)

	// One change at a time.
	for _, bad := range []instance.SetupPatch{{}, {KitAcknowledged: true, Finished: true}} {
		_, err = svc.PatchSetup(ctx, "op", bad)
		assert.ErrorIs(t, err, instance.ErrInvalidPatch, "%+v", bad)
	}

	// Finishing needs the role and the kit.
	_, err = svc.PatchSetup(ctx, "op", instance.SetupPatch{Finished: true})
	assert.ErrorIs(t, err, instance.ErrSetupIncomplete)

	_, err = svc.SetServerRole(ctx, "op", instance.RoleApps)
	require.NoError(t, err)
	st = ackKit(t, svc)
	require.NotNil(t, st.KitAcknowledgedAt)

	st, err = svc.PatchSetup(ctx, "op", instance.SetupPatch{Finished: true})
	require.NoError(t, err)
	assert.False(t, st.InProgress)
	require.NotNil(t, st.FinishedAt)
	assert.Equal(t, "setup.finished", repo.audit[len(repo.audit)-1].action)
	assert.Equal(t, instance.PanelRoutes{AnyHost: true}, spy.got[len(spy.got)-1], "still on the IP: it keeps answering there")
}

func TestSetup_InstallBeforeTheWizard(t *testing.T) {
	repo := newFakeRepo()
	repo.operators["op"] = instance.SourceBootstrap
	st, err := instance.NewService(repo, zap.NewNop()).WithPanelRoutes(nil, "panel.example", false).Setup(context.Background(), "op")
	require.NoError(t, err)
	assert.False(t, st.InProgress, "no setup row: never sent through the wizard")
	assert.Equal(t, "panel.example", st.Domain)
}

func TestSetPanelDomain(t *testing.T) {
	ctx := context.Background()
	repo := setupRepo()
	spy := &routesSpy{}
	svc := instance.NewService(repo, zap.NewNop()).WithPanelRoutes(spy, "", false)

	for _, bad := range []string{"localhost", "203.0.113.20", "*.panel.example", "panel..example", "pa nel.example", "panel.example`)"} {
		_, err := svc.SetPanelDomain(ctx, "op", bad, false)
		assert.ErrorIs(t, err, instance.ErrInvalidDomain, bad)
	}
	_, err := svc.SetPanelDomain(ctx, "dev", "panel.example", false)
	assert.ErrorIs(t, err, instance.ErrNotOperator)

	st, err := svc.SetPanelDomain(ctx, "op", "  Panel.Example. ", false)
	require.NoError(t, err)
	assert.Equal(t, "panel.example", st.Domain)
	assert.True(t, st.DomainSet)
	assert.Equal(t, "panel.example", svc.PanelDomain())
	// In setup the panel keeps answering on the IP beside the domain, so
	// the browser that set it can follow the certificate.
	assert.Equal(t, instance.PanelRoutes{Domain: "panel.example", AnyHost: true}, spy.got[len(spy.got)-1])
	last := repo.audit[len(repo.audit)-1]
	assert.Equal(t, "panel.domain_changed", last.action)
	assert.JSONEq(t, `{"from":"","to":"panel.example"}`, last.detail)

	// Keep using the IP.
	st, err = svc.SetPanelDomain(ctx, "op", "", false)
	require.NoError(t, err)
	assert.Equal(t, "", st.Domain)
	assert.True(t, st.DomainSet)
}

type probeSpy struct {
	withCert    bool
	calls       int
	unreachable bool
	certPending bool
}

func (p *probeSpy) ProbeDomain(_ context.Context, domain, probeID string, withCert bool) (instance.PanelDomainCheck, error) {
	p.calls++
	p.withCert = withCert
	cert := "issued"
	if p.certPending {
		cert = "pending"
	}
	return instance.PanelDomainCheck{Domain: domain, ReachesPanel: probeID != "" && !p.unreachable, Certificate: instance.CertificateState{State: cert}}, nil
}

// readyToFinish is an instance in setup with its domain, role and kit done.
func readyToFinish(t *testing.T, domain string, prober instance.DomainProber) (*instance.Service, *fakeRepo, *routesSpy) {
	t.Helper()
	ctx := context.Background()
	repo := setupRepo()
	spy := &routesSpy{}
	svc := instance.NewService(repo, zap.NewNop()).WithPanelRoutes(spy, "", false).WithRecoveryKit("enc-key-hex", "v0.1.0", kitBackups{nil})
	if prober != nil {
		svc.WithSetup(prober, "")
	}
	_, err := svc.SetPanelDomain(ctx, "op", domain, false)
	require.NoError(t, err)
	_, err = svc.SetServerRole(ctx, "op", instance.RoleApps)
	require.NoError(t, err)
	ackKit(t, svc)
	return svc, repo, spy
}

func TestSetup_FinishChecksTheDomain(t *testing.T) {
	ctx := context.Background()
	finish := instance.SetupPatch{Finished: true}
	onDomain := instance.PanelRoutes{Domain: "panel.example"}

	t.Run("not reaching the panel is refused and changes nothing", func(t *testing.T) {
		prober := &probeSpy{unreachable: true}
		svc, repo, spy := readyToFinish(t, "panel.example", prober)
		setupBefore, routesBefore, auditBefore := string(repo.settings[instance.SettingSetup]), len(spy.got), len(repo.audit)

		_, err := svc.PatchSetup(ctx, "op", finish)
		assert.ErrorIs(t, err, instance.ErrPanelDomainUnreachable)
		assert.Equal(t, 1, prober.calls)
		assert.False(t, prober.withCert, "the certificate has no bearing on it")
		assert.Equal(t, setupBefore, string(repo.settings[instance.SettingSetup]), "nothing written")
		assert.Len(t, spy.got, routesBefore, "no route rewrite")
		assert.Len(t, repo.audit, auditBefore)

		st, err := svc.Setup(ctx, "op")
		require.NoError(t, err)
		assert.True(t, st.InProgress)
	})

	t.Run("reaching the panel finishes", func(t *testing.T) {
		prober := &probeSpy{}
		svc, _, spy := readyToFinish(t, "panel.example", prober)
		st, err := svc.PatchSetup(ctx, "op", finish)
		require.NoError(t, err)
		assert.False(t, st.InProgress)
		assert.Equal(t, 1, prober.calls)
		assert.Equal(t, onDomain, spy.got[len(spy.got)-1])
	})

	t.Run("the operator's word finishes", func(t *testing.T) {
		prober := &probeSpy{unreachable: true}
		svc, _, spy := readyToFinish(t, "panel.example", prober)
		st, err := svc.PatchSetup(ctx, "op", instance.SetupPatch{Finished: true, DomainChecked: true})
		require.NoError(t, err)
		assert.False(t, st.InProgress)
		assert.Equal(t, 0, prober.calls)
		assert.Equal(t, onDomain, spy.got[len(spy.got)-1])
	})

	t.Run("no domain isn't probed", func(t *testing.T) {
		prober := &probeSpy{unreachable: true}
		svc, _, _ := readyToFinish(t, "", prober)
		st, err := svc.PatchSetup(ctx, "op", finish)
		require.NoError(t, err)
		assert.False(t, st.InProgress)
		assert.Equal(t, 0, prober.calls)
	})

	t.Run("no prober finishes", func(t *testing.T) {
		svc, _, _ := readyToFinish(t, "panel.example", nil)
		st, err := svc.PatchSetup(ctx, "op", finish)
		require.NoError(t, err)
		assert.False(t, st.InProgress)
	})

	t.Run("domain_checked alone is not a change", func(t *testing.T) {
		svc, _, _ := readyToFinish(t, "panel.example", &probeSpy{})
		_, err := svc.PatchSetup(ctx, "op", instance.SetupPatch{DomainChecked: true})
		assert.ErrorIs(t, err, instance.ErrInvalidPatch)
	})
}

func TestSetPanelDomain_AfterSetupChecksTheDomain(t *testing.T) {
	ctx := context.Background()
	// A finished panel on its IP address.
	finished := func(t *testing.T, prober *probeSpy) (*instance.Service, *fakeRepo, *routesSpy) {
		t.Helper()
		svc, repo, spy := readyToFinish(t, "", prober)
		_, err := svc.PatchSetup(ctx, "op", instance.SetupPatch{Finished: true})
		require.NoError(t, err)
		return svc, repo, spy
	}
	onDomain := instance.PanelRoutes{Domain: "panel.example"}

	t.Run("not reaching the panel is refused and changes nothing", func(t *testing.T) {
		prober := &probeSpy{unreachable: true}
		svc, repo, spy := finished(t, prober)
		panelBefore, routesBefore, auditBefore := string(repo.settings[instance.SettingPanel]), len(spy.got), len(repo.audit)

		_, err := svc.SetPanelDomain(ctx, "op", "panel.example", false)
		assert.ErrorIs(t, err, instance.ErrPanelDomainUnreachable)
		assert.Equal(t, 1, prober.calls)
		assert.False(t, prober.withCert, "the certificate has no bearing on it")
		assert.Equal(t, panelBefore, string(repo.settings[instance.SettingPanel]), "nothing written")
		assert.Len(t, spy.got, routesBefore, "no route rewrite")
		assert.Len(t, repo.audit, auditBefore)
		assert.Equal(t, "", svc.PanelDomain())
	})

	t.Run("reaching the panel moves it", func(t *testing.T) {
		prober := &probeSpy{}
		svc, _, spy := finished(t, prober)
		st, err := svc.SetPanelDomain(ctx, "op", "panel.example", false)
		require.NoError(t, err)
		assert.Equal(t, "panel.example", st.Domain)
		assert.Equal(t, 1, prober.calls)
		assert.Equal(t, onDomain, spy.got[len(spy.got)-1])
	})

	t.Run("the operator's word moves it", func(t *testing.T) {
		prober := &probeSpy{unreachable: true}
		svc, _, spy := finished(t, prober)
		_, err := svc.SetPanelDomain(ctx, "op", "panel.example", true)
		require.NoError(t, err)
		assert.Equal(t, 0, prober.calls)
		assert.Equal(t, onDomain, spy.got[len(spy.got)-1])
	})

	t.Run("going back to the IP isn't probed", func(t *testing.T) {
		prober := &probeSpy{unreachable: true}
		svc, _, _ := finished(t, prober)
		_, err := svc.SetPanelDomain(ctx, "op", "", false)
		require.NoError(t, err)
		assert.Equal(t, 0, prober.calls)
	})

	t.Run("during setup the IP keeps answering, so it isn't probed", func(t *testing.T) {
		prober := &probeSpy{unreachable: true}
		svc, _, _ := readyToFinish(t, "", prober)
		_, err := svc.SetPanelDomain(ctx, "op", "panel.example", false)
		require.NoError(t, err)
		assert.Equal(t, 0, prober.calls)
	})
}

func TestCheckPanelDomain(t *testing.T) {
	ctx := context.Background()
	prober := &probeSpy{}
	svc := instance.NewService(setupRepo(), zap.NewNop()).WithPanelRoutes(&routesSpy{}, "", false).WithSetup(prober, "")

	_, err := svc.CheckPanelDomain(ctx, "op", "")
	assert.ErrorIs(t, err, instance.ErrInvalidDomain, "there is nothing to check on the IP")

	// Not the panel's domain yet: no certificate is looked for.
	check, err := svc.CheckPanelDomain(ctx, "op", "Panel.Example")
	require.NoError(t, err)
	assert.False(t, prober.withCert)
	assert.Equal(t, "none", check.Certificate.State)
	assert.True(t, check.ReachesPanel)
	assert.Equal(t, []string{}, check.Addresses)

	_, err = svc.SetPanelDomain(ctx, "op", "panel.example", false)
	require.NoError(t, err)
	check, err = svc.CheckPanelDomain(ctx, "op", "panel.example")
	require.NoError(t, err)
	assert.True(t, prober.withCert)
	assert.Equal(t, "issued", check.Certificate.State)
}

func TestSetServerRole(t *testing.T) {
	ctx := context.Background()
	repo := setupRepo()
	repo.panelHostServices = 2
	svc := instance.NewService(repo, zap.NewNop())

	_, err := svc.SetServerRole(ctx, "op", "worker")
	assert.ErrorIs(t, err, instance.ErrInvalidRole)

	_, err = svc.SetServerRole(ctx, "op", instance.RoleControlPlane)
	var inUse *instance.PanelHostInUseError
	require.True(t, errors.As(err, &inUse), "%v", err)
	assert.EqualValues(t, 2, inUse.Services)
	assert.Contains(t, err.Error(), "before choosing Only Toft")
	assert.True(t, svc.PanelHostRunsApps(ctx))

	repo.panelHostServices = 0
	st, err := svc.SetServerRole(ctx, "op", instance.RoleControlPlane)
	require.NoError(t, err)
	assert.Equal(t, instance.RoleControlPlane, st.Role)
	assert.True(t, st.RoleSet)
	assert.False(t, svc.PanelHostRunsApps(ctx))
	assert.JSONEq(t, `{"from":"apps","to":"control_plane"}`, repo.audit[len(repo.audit)-1].detail)
}

// A server below the floor runs the panel only (D-66, D-132): the API
// refuses what the wizard greys out. A "3 GB" VPS reports about 2.8 GiB; a
// "4 GB" one about 3.7 GiB, which passes.
func TestSetServerRole_TooSmallForApps(t *testing.T) {
	ctx := context.Background()
	small := fakePanelHost{instance.PanelHost{CPUCores: 2, MemoryBytes: 2900 << 20}}
	svc := instance.NewService(setupRepo(), zap.NewNop()).WithPanelHost(small)

	_, err := svc.SetServerRole(ctx, "op", instance.RoleApps)
	var tooSmall *instance.PanelHostTooSmallError
	require.True(t, errors.As(err, &tooSmall), "%v", err)
	assert.Contains(t, err.Error(), "Run apps here too needs at least 2 vCPU and 3.5 GiB")
	assert.Contains(t, err.Error(), "this server has 2 vCPU and 2.8 GiB")
	_, err = svc.SetServerRole(ctx, "op", instance.RoleControlPlane)
	require.NoError(t, err)

	for _, h := range []instance.PanelHost{{CPUCores: 2, MemoryBytes: 3900644 << 10}, {CPUCores: 4, MemoryBytes: 8 << 30}} {
		_, err = instance.NewService(setupRepo(), zap.NewNop()).WithPanelHost(fakePanelHost{h}).SetServerRole(ctx, "op", instance.RoleApps)
		require.NoError(t, err, "%d vCPU, %d bytes", h.CPUCores, h.MemoryBytes)
	}

	r := setupRouter(svc, "op", false, false)
	c, body := call(t, r, http.MethodPut, "/api/v1/instance/setup/role", map[string]string{"role": "apps"})
	assert.Equal(t, http.StatusConflict, c)
	assert.Equal(t, "panel_host_too_small", code(body))
}

func TestRecoveryKit(t *testing.T) {
	ctx := context.Background()
	repo := setupRepo()
	backup := &instance.KitBackup{Repository: "s3:https://s3.example/b/toft/control-plane", Password: "repo-pw", AccessKey: "AK"}
	svc := instance.NewService(repo, zap.NewNop()).WithPanelRoutes(nil, "panel.example", false).
		WithRecoveryKit("enc-key-hex", "v0.1.0", kitBackups{backup})

	_, err := svc.RecoveryKit(ctx, "dev")
	assert.ErrorIs(t, err, instance.ErrNotOperator)
	assert.Empty(t, repo.audit, "a refused read is not a read")

	kit, err := svc.RecoveryKit(ctx, "op")
	require.NoError(t, err)
	assert.Equal(t, "toft-recovery-panel.example.txt", kit.Filename)
	assert.Equal(t, "sh install.sh --restore --kit toft-recovery-panel.example.txt", kit.RestoreCommand)
	for _, want := range []string{"Encryption key:      enc-key-hex", "Backup repository:   s3:https://s3.example/b/toft/control-plane",
		"Repository password: repo-pw", "Version:             v0.1.0", kit.RestoreCommand} {
		assert.Contains(t, kit.Text, want)
	}
	assert.Equal(t, "recovery_kit.viewed", repo.audit[len(repo.audit)-1].action)

	// Without backups the kit says so, and names no repository.
	svc.WithRecoveryKit("enc-key-hex", "v0.1.0", kitBackups{nil})
	kit, err = svc.RecoveryKit(ctx, "op")
	require.NoError(t, err)
	assert.Nil(t, kit.Backup)
	assert.True(t, strings.Contains(kit.Text, "Backups are not set up"))
}

// The panel knows when the stored kit no longer restores it (D-163): the
// fingerprint follows the four lines install.sh --restore reads.
func TestKitOutOfDate(t *testing.T) {
	ctx := context.Background()
	repo := setupRepo()
	svc := instance.NewService(repo, zap.NewNop()).WithRecoveryKit("enc-key-hex", "v0.1.0", kitBackups{nil})
	outOfDate := func() bool {
		t.Helper()
		st, err := svc.Setup(ctx, "op")
		require.NoError(t, err)
		return st.KitOutOfDate
	}
	assert.False(t, outOfDate(), "no kit stored yet: nothing is out of date")

	kit, err := svc.RecoveryKit(ctx, "op")
	require.NoError(t, err)
	require.Len(t, kit.Fingerprint, 64)

	// The acknowledgement names the kit that was read.
	_, err = svc.PatchSetup(ctx, "op", instance.SetupPatch{KitAcknowledged: true})
	assert.ErrorIs(t, err, instance.ErrKitFingerprintRequired)
	_, err = svc.PatchSetup(ctx, "op", instance.SetupPatch{KitAcknowledged: true, Fingerprint: strings.Repeat("0", 64)})
	assert.ErrorIs(t, err, instance.ErrKitChanged)
	st, err := svc.Setup(ctx, "op")
	require.NoError(t, err)
	assert.Nil(t, st.KitAcknowledgedAt, "a refused acknowledgement records nothing")
	assert.Equal(t, "recovery_kit.viewed", repo.audit[len(repo.audit)-1].action, "nor in the audit log")

	// Storing it is audited as reading it is: a restore later depends on it.
	ackKit(t, svc)
	assert.False(t, outOfDate())
	assert.Equal(t, auditRow{actor: "op", action: "recovery_kit.stored"}, repo.audit[len(repo.audit)-1], "no secret and no fingerprint")

	// Backups set up after the kit was stored: that kit names no repository.
	backup := &instance.KitBackup{Repository: "s3:https://s3.example/b/toft/control-plane", Password: "repo-pw", AccessKey: "AK"}
	svc.WithRecoveryKit("enc-key-hex", "v0.1.0", kitBackups{backup})
	assert.True(t, outOfDate())
	_, err = svc.PatchSetup(ctx, "op", instance.SetupPatch{KitAcknowledged: true, Fingerprint: kit.Fingerprint})
	assert.ErrorIs(t, err, instance.ErrKitChanged, "the kit on screen was read before the change")
	assert.True(t, outOfDate())
	ackKit(t, svc)
	assert.False(t, outOfDate())

	// Each backup line the restore reads, then the key.
	for _, c := range []struct {
		name   string
		change func()
	}{
		{"repository", func() { backup.Repository += "-2" }},
		{"repository password", func() { backup.Password = "new-pw" }},
		{"access key", func() { backup.AccessKey = "AK2" }},
		{"encryption key", func() { svc.WithRecoveryKit("rotated-key-hex", "v0.1.0", kitBackups{backup}) }},
	} {
		c.change()
		assert.True(t, outOfDate(), c.name)
		ackKit(t, svc)
		assert.False(t, outOfDate(), c.name)
	}
	svc.WithRecoveryKit("rotated-key-hex", "v0.2.0", kitBackups{backup})
	assert.False(t, outOfDate(), "an upgrade changes nothing a restore reads")

	// Backups stopped: the stored kit still opens the copies in the bucket,
	// and a new one names none. A rotated key makes it out of date all the same.
	svc.WithRecoveryKit("rotated-key-hex", "v0.2.0", kitBackups{nil})
	assert.False(t, outOfDate(), "backups stopped")
	svc.WithRecoveryKit("third-key-hex", "v0.2.0", kitBackups{nil})
	assert.True(t, outOfDate(), "backups stopped, then the key rotated")
	svc.WithRecoveryKit("rotated-key-hex", "v0.2.0", kitBackups{backup})

	// Nothing stored gives a secret away.
	raw := string(repo.settings[instance.SettingSetup])
	for _, secret := range []string{"enc-key-hex", "rotated-key-hex", "third-key-hex", "repo-pw", "new-pw", "AK2"} {
		assert.NotContains(t, raw, secret)
	}
	assert.Contains(t, raw, `"kit_fingerprint"`)
}

// A backup that can't be read doesn't take setup's other answers with it:
// the kit isn't checked, and it can't be read or acknowledged either.
func TestKitOutOfDate_BackupUnreadable(t *testing.T) {
	ctx := context.Background()
	svc := instance.NewService(setupRepo(), zap.NewNop()).WithRecoveryKit("enc-key-hex", "v0.1.0", kitBackups{nil})
	kit, err := svc.RecoveryKit(ctx, "op")
	require.NoError(t, err)
	ackKit(t, svc)

	svc.WithRecoveryKit("enc-key-hex", "v0.1.0", unreadableBackup{})
	st, err := svc.Setup(ctx, "op")
	require.NoError(t, err)
	assert.False(t, st.KitOutOfDate)
	require.NotNil(t, st.KitAcknowledgedAt)
	_, err = svc.RecoveryKit(ctx, "op")
	assert.Error(t, err)
	_, err = svc.PatchSetup(ctx, "op", instance.SetupPatch{KitAcknowledged: true, Fingerprint: kit.Fingerprint})
	assert.Error(t, err)
	assert.NotErrorIs(t, err, instance.ErrKitChanged)
}

// A kit stored before fingerprints were kept: out of date once the panel
// host has backups, since nothing says that kit names them.
func TestKitOutOfDate_StoredBeforeFingerprints(t *testing.T) {
	ctx := context.Background()
	repo := setupRepo()
	repo.settings[instance.SettingSetup] = []byte(`{"started_at":"2026-10-17T11:00:00Z","kit_acknowledged_at":"2026-10-18T09:00:00Z","finished_at":"2026-10-18T09:01:00Z"}`)
	svc := instance.NewService(repo, zap.NewNop()).WithRecoveryKit("enc-key-hex", "v0.1.0", kitBackups{nil})

	st, err := svc.Setup(ctx, "op")
	require.NoError(t, err)
	assert.False(t, st.KitOutOfDate, "no backups: there is nothing that kit could miss")

	svc.WithRecoveryKit("enc-key-hex", "v0.1.0", kitBackups{&instance.KitBackup{Repository: "r", Password: "p", AccessKey: "a"}})
	st, err = svc.Setup(ctx, "op")
	require.NoError(t, err)
	assert.True(t, st.KitOutOfDate)

	st = ackKit(t, svc)
	assert.False(t, st.KitOutOfDate)
	require.NotNil(t, st.FinishedAt, "storing the kit again leaves the rest of setup's state")
}
