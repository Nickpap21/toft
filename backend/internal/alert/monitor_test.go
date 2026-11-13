package alert

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/code"
	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// fakeProbe: services and their containers per server, which servers fail,
// and disk usage per server ("" = the control plane).
type fakeProbe struct {
	mu      sync.Mutex
	targets []service.MetricsTarget
	states  map[string][]service.ContainerState
	down    map[string]bool
	disk    map[string]int
}

func (p *fakeProbe) ListMetricsTargets(context.Context) ([]service.MetricsTarget, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]service.MetricsTarget(nil), p.targets...), nil
}
func (p *fakeProbe) ProbeServerStates(_ context.Context, serverID string, targets []service.MetricsTarget) (map[string][]service.ContainerState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.down[serverID] {
		return nil, errors.New("ssh: connection refused")
	}
	out := map[string][]service.ContainerState{}
	for _, t := range targets {
		if s, ok := p.states[t.ID]; ok {
			out[t.ID] = s
		}
	}
	return out, nil
}
func (p *fakeProbe) ProbeServerDisk(_ context.Context, serverID string) ([]service.DiskUsage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pct, ok := p.disk[serverID]
	if !ok {
		return nil, errors.New("unreachable")
	}
	return []service.DiskUsage{{Mount: "/", UsedPercent: pct}}, nil
}

type fakeServers []string

func (f fakeServers) ListServerIDs(context.Context) ([]string, error) { return f, nil }

// memStore is the open-alert table in memory.
type memStore struct {
	open    map[key]bool
	gone    []key
	details map[key]string
}

func (m *memStore) ListOpen(context.Context) ([]key, error) {
	var ks []key
	for k := range m.open {
		ks = append(ks, k)
	}
	return ks, nil
}
func (m *memStore) Open(_ context.Context, k key, _ string) (bool, error) {
	if m.open[k] {
		return false, nil
	}
	m.open[k] = true
	return true, nil
}
func (m *memStore) Resolve(_ context.Context, k key) (bool, error) {
	if !m.open[k] {
		return false, nil
	}
	delete(m.open, k)
	return true, nil
}
func (m *memStore) SetDetail(_ context.Context, k key, d string) error {
	if m.open[k] {
		if m.details == nil {
			m.details = map[key]string{}
		}
		m.details[k] = d
	}
	return nil
}
func (m *memStore) ListGoneServiceAlerts(context.Context) ([]key, error) {
	var ks []key
	for _, k := range m.gone {
		if m.open[k] {
			ks = append(ks, k)
		}
	}
	return ks, nil
}
func (m *memStore) DeleteResolvedBefore(context.Context, time.Time) error { return nil }

type events []Event

func (e *events) Notify(ev Event) { *e = append(*e, ev) }

// summary lists events as "kind subject firing|resolved".
func (e events) summary() []string {
	var out []string
	for _, ev := range e {
		status := "firing"
		if ev.Resolved {
			status = "resolved"
		}
		out = append(out, string(ev.Kind)+" "+ev.Subject+" "+status)
	}
	return out
}

func newTestMonitor(p *fakeProbe, st *memStore, ev *events) *Monitor {
	return NewMonitor(p, fakeServers{"srv-1"}, st, ev, zap.NewNop())
}

func running(name string) []service.ContainerState {
	return []service.ContainerState{{Name: name, State: "running"}}
}

func TestMonitorFiresAndResolvesAServiceAlert(t *testing.T) {
	p := &fakeProbe{
		targets: []service.MetricsTarget{{ID: "svc-1", Name: "api", ServerID: "srv-1"}},
		states:  map[string][]service.ContainerState{"svc-1": {{Name: "api-blue", State: "restarting"}}},
		disk:    map[string]int{"": 40, "srv-1": 40},
	}
	st, ev := &memStore{open: map[key]bool{}}, &events{}
	m := newTestMonitor(p, st, ev)
	ctx := context.Background()

	m.tick(ctx)
	assert.Empty(t, *ev, "one bad sample is not enough")
	m.tick(ctx)
	assert.Equal(t, []string{"service_restarting svc-1 firing"}, ev.summary())

	p.states["svc-1"] = running("api-blue")
	m.tick(ctx)
	m.tick(ctx)
	m.tick(ctx)
	assert.Equal(t, []string{"service_restarting svc-1 firing", "service_restarting svc-1 resolved"}, ev.summary())
}

// fakeAgents: srv-1 is an agent server; up says whether its agent is
// connected.
type fakeAgents struct{ up, frozen bool }

func (fakeAgents) AgentsAway(context.Context) (map[string]bool, error) {
	return map[string]bool{"srv-1": false}, nil
}

func (a fakeAgents) AgentsConnected(context.Context) (map[string]bool, error) {
	return map[string]bool{"srv-1": a.up}, nil
}

func (a fakeAgents) AgentsFrozen(context.Context) (map[string]bool, error) {
	return map[string]bool{"srv-1": a.frozen}, nil
}

// A server whose agent isn't connected is agent_offline's alone: in
// production's checks the probe's deadline ran out while the agent
// reconnected, and server_unreachable fired at 1 min 40 s (D-108). A
// connected agent whose server fails still fires it.
func TestMonitorAgentAwayIsNotUnreachable(t *testing.T) {
	p := &fakeProbe{
		targets: []service.MetricsTarget{{ID: "svc-1", Name: "api", ServerID: "srv-1"}},
		states:  map[string][]service.ContainerState{"svc-1": running("api-blue")},
		down:    map[string]bool{"srv-1": true},
		disk:    map[string]int{"": 40},
	}
	st, ev := &memStore{open: map[key]bool{}}, &events{}
	m := newTestMonitor(p, st, ev).WithAgents(fakeAgents{up: false})
	for range 4 {
		m.tick(context.Background())
	}
	assert.Empty(t, ev.summary())

	m.WithAgents(fakeAgents{up: true})
	m.tick(context.Background())
	m.tick(context.Background())
	assert.Equal(t, []string{"server_unreachable srv-1 firing"}, ev.summary())
}

// A frozen agent (D-113) fires agent_frozen at once, and its choice
// resolves it.
func TestMonitorAgentFrozenUntilAStateIsChosen(t *testing.T) {
	p := &fakeProbe{disk: map[string]int{"": 40}}
	st, ev := &memStore{open: map[key]bool{}}, &events{}
	m := newTestMonitor(p, st, ev).WithAgents(fakeAgents{up: true, frozen: true})
	m.tick(context.Background())
	assert.Equal(t, []string{"agent_frozen srv-1 firing"}, ev.summary())

	m.WithAgents(fakeAgents{up: true})
	m.tick(context.Background())
	assert.Equal(t, []string{"agent_frozen srv-1 firing", "agent_frozen srv-1 resolved"}, ev.summary())
}

func TestMonitorUnreachableServerSuspendsItsServices(t *testing.T) {
	p := &fakeProbe{
		targets: []service.MetricsTarget{{ID: "svc-1", Name: "api", ServerID: "srv-1"}},
		states:  map[string][]service.ContainerState{"svc-1": running("api-blue")},
		down:    map[string]bool{"srv-1": true},
		disk:    map[string]int{"": 40},
	}
	st, ev := &memStore{open: map[key]bool{}}, &events{}
	m := newTestMonitor(p, st, ev)
	m.tick(context.Background())
	m.tick(context.Background())
	assert.Equal(t, []string{"server_unreachable srv-1 firing"}, ev.summary(), "no service_down for a server that can't be read")

	p.down["srv-1"] = false
	m.tick(context.Background())
	assert.Equal(t, []string{"server_unreachable srv-1 firing", "server_unreachable srv-1 resolved"}, ev.summary())
}

func TestMonitorDiskHysteresis(t *testing.T) {
	p := &fakeProbe{disk: map[string]int{"": 91, "srv-1": 50}}
	st, ev := &memStore{open: map[key]bool{}}, &events{}
	m := newTestMonitor(p, st, ev)
	ctx := context.Background()
	diskCheck := func(pct int) {
		p.disk[""] = pct
		for i := 0; i < diskEvery; i++ {
			m.tick(ctx)
		}
	}
	diskCheck(91)
	assert.Equal(t, []string{"disk_full control-plane firing"}, ev.summary(), "fires on the first full sample")
	diskCheck(82)
	assert.Len(t, *ev, 1, "82% is between the thresholds: still open, nothing sent")
	diskCheck(70)
	assert.Equal(t, []string{"disk_full control-plane firing", "disk_full control-plane resolved"}, ev.summary())
}

func TestMonitorRestartDoesNotResendOpenAlerts(t *testing.T) {
	k := key{KindServiceDown, "svc-1"}
	p := &fakeProbe{
		targets: []service.MetricsTarget{{ID: "svc-1", Name: "api", ServerID: "srv-1"}},
		states:  map[string][]service.ContainerState{},
		disk:    map[string]int{"": 40, "srv-1": 40},
	}
	st, ev := &memStore{open: map[key]bool{k: true}}, &events{}
	m := newTestMonitor(p, st, ev)
	for i := 0; i < 4; i++ {
		m.tick(context.Background())
	}
	assert.Empty(t, *ev, "the alert was already open before the restart")

	p.states["svc-1"] = running("api-blue")
	for i := 0; i < 3; i++ {
		m.tick(context.Background())
	}
	assert.Equal(t, []string{"service_down svc-1 resolved"}, ev.summary(), "but it still resolves")
}

func TestMonitorResolvesAlertsOfStoppedServices(t *testing.T) {
	k := key{KindServiceDown, "svc-gone"}
	p := &fakeProbe{disk: map[string]int{"": 40, "srv-1": 40}}
	st, ev := &memStore{open: map[key]bool{k: true}, gone: []key{k}}, &events{}
	newTestMonitor(p, st, ev).tick(context.Background())
	assert.Equal(t, []string{"service_down svc-gone resolved"}, ev.summary())
	assert.Equal(t, "the service was stopped or deleted", (*ev)[0].Detail)
}

// fakeVolumes returns the usage it holds.
type fakeVolumes struct{ usage []VolumeUsage }

func (f *fakeVolumes) Measure(context.Context) ([]VolumeUsage, error) { return f.usage, nil }

// A shared volume fires at 80% of its limit, resolves under 75%, keeps its
// alert while it cannot be measured, and resolves when it is deleted.
func TestMonitorSharedVolumeHysteresis(t *testing.T) {
	p := &fakeProbe{disk: map[string]int{"": 40, "srv-1": 40}}
	st, ev := &memStore{open: map[key]bool{}}, &events{}
	vols := &fakeVolumes{}
	m := newTestMonitor(p, st, ev).WithVolumes(vols)
	ctx := context.Background()
	const gib = int64(1) << 30
	check := func(used int64, measured bool) {
		vols.usage = []VolumeUsage{{ID: "vol-1", Name: "northwind-images", SizeLimitBytes: 20 * gib, UsedBytes: used, Measured: measured}}
		for i := 0; i < volumeEvery; i++ {
			m.tick(ctx)
		}
	}

	check(15*gib, true) // 75%
	assert.Empty(t, *ev)
	check(17*gib, true) // 85%
	assert.Equal(t, []string{"volume_full vol-1 firing"}, ev.summary())
	check(0, false)
	assert.Len(t, *ev, 1, "an unmeasured volume keeps its alert")
	check(15*gib+gib/2, true) // 77%: between the thresholds
	assert.Len(t, *ev, 1)
	check(10*gib, true) // 50%
	assert.Equal(t, []string{"volume_full vol-1 firing", "volume_full vol-1 resolved"}, ev.summary())

	check(25*gib, true) // over the limit
	vols.usage = nil
	for i := 0; i < volumeEvery; i++ {
		m.tick(ctx)
	}
	assert.Equal(t, []string{"volume_full vol-1 firing", "volume_full vol-1 resolved", "volume_full vol-1 firing", "volume_full vol-1 resolved"}, ev.summary(),
		"a deleted volume's alert resolves")
}

type fakeBackups struct{ health []server.BackupHealth }

func (f *fakeBackups) CheckBackups(context.Context) ([]server.BackupHealth, error) {
	return f.health, nil
}

func TestMonitorBackupFailed(t *testing.T) {
	p := &fakeProbe{disk: map[string]int{"": 40, "srv-1": 40}}
	st, ev := &memStore{open: map[key]bool{}}, &events{}
	b := &fakeBackups{}
	m := newTestMonitor(p, st, ev).WithBackups(b)
	ctx := context.Background()
	check := func(h ...server.BackupHealth) {
		b.health = h
		for i := 0; i < diskEvery; i++ {
			m.tick(ctx)
		}
	}

	check(server.BackupHealth{ServerID: "srv-1"}, server.BackupHealth{ServerID: ""})
	assert.Empty(t, *ev)
	check(server.BackupHealth{ServerID: "srv-1", Failing: true, Detail: "the last backup failed: x"}, server.BackupHealth{ServerID: ""})
	assert.Equal(t, []string{"backup_failed srv-1 firing"}, ev.summary(), "fires on the first failing check")
	check(server.BackupHealth{ServerID: "srv-1", Failing: true}, server.BackupHealth{ServerID: "", Failing: true})
	assert.Equal(t, []string{"backup_failed srv-1 firing", "backup_failed control-plane firing"}, ev.summary())
	check(server.BackupHealth{ServerID: "srv-1"})
	assert.Equal(t, []string{"backup_failed srv-1 firing", "backup_failed control-plane firing",
		"backup_failed srv-1 resolved", "backup_failed control-plane resolved"}, ev.summary(),
		"a success resolves it, and so does removing the destination")
}

type fakeMirrors struct{ health []code.MirrorHealth }

func (f *fakeMirrors) FailingMirrors(context.Context) ([]code.MirrorHealth, error) {
	return f.health, nil
}

// A mirror whose syncs keep failing (D-79): checked every minute, resolved
// by a success or by removing the mirror.
func TestMonitorMirrorFailing(t *testing.T) {
	p, st, ev := &fakeProbe{}, &memStore{open: map[key]bool{}}, &events{}
	f := &fakeMirrors{}
	m := newTestMonitor(p, st, ev).WithMirrors(f)
	ctx := context.Background()
	check := func(h ...code.MirrorHealth) {
		f.health = h
		m.tick(ctx)
	}
	check(code.MirrorHealth{MirrorID: "m-1"})
	assert.Empty(t, *ev)
	check(code.MirrorHealth{MirrorID: "m-1", Failing: true, Detail: "github.com refused the credentials (401)"}, code.MirrorHealth{MirrorID: "m-2"})
	assert.Equal(t, []string{"mirror_failing m-1 firing"}, ev.summary())
	check(code.MirrorHealth{MirrorID: "m-1"}, code.MirrorHealth{MirrorID: "m-2", Failing: true})
	check()
	assert.Equal(t, []string{"mirror_failing m-1 firing", "mirror_failing m-1 resolved", "mirror_failing m-2 firing", "mirror_failing m-2 resolved"}, ev.summary(),
		"a success resolves it, and so does removing the mirror")
}

func TestHumanBytes(t *testing.T) {
	assert.Equal(t, "512 B", humanBytes(512))
	assert.Equal(t, "3.1 GiB", humanBytes(3*(1<<30)+(1<<27)))
	assert.Equal(t, "20.0 GiB", humanBytes(20<<30))
}

type fakePanel struct {
	containers, certificate string
	certUnread              bool
}

func (f *fakePanel) PanelProblems(_ context.Context, cert bool) (string, string, bool, error) {
	return f.containers, f.certificate, cert && !f.certUnread, nil
}

// The panel's own alerts (D-68): its containers every minute, its
// certificate every five.
func TestMonitorPanel(t *testing.T) {
	p, st, ev := &fakeProbe{}, &memStore{open: map[key]bool{}}, &events{}
	panel := &fakePanel{}
	m := newTestMonitor(p, st, ev).WithPanel(panel)
	ctx := context.Background()

	m.tick(ctx)
	panel.containers = "paas-frontend keeps restarting (3 restarts)"
	m.tick(ctx)
	assert.Empty(t, *ev, "one bad minute doesn't fire")
	m.tick(ctx)
	assert.Equal(t, []string{"panel_container control-plane firing"}, ev.summary())
	panel.containers = ""
	for i := 0; i < 3; i++ {
		m.tick(ctx)
	}
	assert.Equal(t, []string{"panel_container control-plane firing", "panel_container control-plane resolved"}, ev.summary())

	*ev = nil
	panel.certificate = "panel.example's certificate expires in 9 days"
	for i := 0; i < 3*diskEvery; i++ {
		m.tick(ctx)
	}
	assert.Equal(t, []string{"panel_certificate control-plane firing"}, ev.summary(), "three checks, five minutes apart")

	// A certificate that couldn't be read is not a renewed one (D-164).
	*ev = nil
	panel.certificate, panel.certUnread = "", true
	for i := 0; i < 3*diskEvery; i++ {
		m.tick(ctx)
	}
	assert.Empty(t, *ev, "the alert stays as it is")
	panel.certUnread = false
	for i := 0; i < 3*diskEvery; i++ {
		m.tick(ctx)
	}
	assert.Equal(t, []string{"panel_certificate control-plane resolved"}, ev.summary())
}

func TestPanelUpgradeFailedIsAOneOff(t *testing.T) {
	assert.True(t, oneOff(KindPanelUpgrade))
	assert.Equal(t, "Panel upgrade failed", titles[KindPanelUpgrade])
}

// fakeUpdates is one checker's hosts; err fails it.
type fakeUpdates struct {
	hosts []server.UpdateHealth
	err   error
}

func (f *fakeUpdates) UpdateProblems(context.Context) ([]server.UpdateHealth, error) {
	return f.hosts, f.err
}

// A host's updates read (D-133) fires its three alerts at once and resolves
// each when it's fine; a host whose read never worked keeps its alerts; a
// removed server's resolve, but not while a checker fails.
func TestMonitorUpdates(t *testing.T) {
	p := &fakeProbe{disk: map[string]int{"": 40}}
	st, ev := &memStore{open: map[key]bool{}}, &events{}
	servers := &fakeUpdates{hosts: []server.UpdateHealth{{ServerID: "srv-1", Known: true, Security: "2 security updates waiting", Reboot: "a reboot is waiting"}}}
	panel := &fakeUpdates{hosts: []server.UpdateHealth{{Known: true, Docker: "Docker 29.4.3 is affected"}}}
	m := newTestMonitor(p, st, ev).WithUpdates(servers, panel)
	ctx := context.Background()
	m.tick(ctx)
	assert.Equal(t, []string{"security_updates srv-1 firing", "reboot_required srv-1 firing", "docker_advisory control-plane firing"}, ev.summary())

	*ev = nil
	servers.hosts[0].Reboot = ""
	panel.hosts[0] = server.UpdateHealth{} // the read failed since: unknown
	m.checkUpdates(ctx)
	assert.Equal(t, []string{"reboot_required srv-1 resolved"}, ev.summary())

	*ev = nil
	servers.hosts, servers.err = nil, errors.New("database down")
	m.checkUpdates(ctx)
	assert.Empty(t, ev.summary(), "nothing resolves while a checker fails")

	servers.err = nil
	m.checkUpdates(ctx)
	assert.Equal(t, []string{"security_updates srv-1 resolved"}, ev.summary())
}
