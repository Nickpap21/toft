package alert

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/Nickpap21/toft/backend/internal/code"
	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

// Monitor tuning. Code defaults, not settings.
const (
	sampleInterval    = 60 * time.Second
	diskEvery         = 5  // samples: disks are checked every 5 minutes
	volumeEvery       = 15 // samples: shared volumes are measured every 15 minutes
	cleanupEvery      = 60 // samples: resolved alerts are pruned hourly
	resolvedRetention = 30 * 24 * time.Hour
	serverConcurrency = 8
	serverTimeout     = 20 * time.Second
)

// Probe reads the running services and, per server ("" = the control-plane
// server), their containers and the disks. Satisfied by
// *service.ServiceManager.
type Probe interface {
	ListMetricsTargets(ctx context.Context) ([]service.MetricsTarget, error)
	ProbeServerStates(ctx context.Context, serverID string, targets []service.MetricsTarget) (map[string][]service.ContainerState, error)
	ProbeServerDisk(ctx context.Context, serverID string) ([]service.DiskUsage, error)
}

// ServerLister lists every remote server's ID, for the disk check.
type ServerLister interface {
	ListServerIDs(ctx context.Context) ([]string, error)
}

// store keeps the open alerts. Satisfied by *PostgresRepository.
type store interface {
	ListOpen(ctx context.Context) ([]key, error)
	// Open stores an open alert; false when one was already open.
	Open(ctx context.Context, k key, detail string) (bool, error)
	// Resolve closes the open alert; false when none was open.
	Resolve(ctx context.Context, k key) (bool, error)
	// SetDetail rewrites the open alert's detail; nothing when none is open.
	SetDetail(ctx context.Context, k key, detail string) error
	ListGoneServiceAlerts(ctx context.Context) ([]key, error)
	DeleteResolvedBefore(ctx context.Context, before time.Time) error
}

type notifier interface{ Notify(Event) }

// Announcer tells the workspaces whose alerts opened or resolved in a pass.
// Satisfied by *Service.
type Announcer interface {
	Announce(ctx context.Context, changed []Event)
}

// WithAnnouncer has each pass that opened or resolved an alert tell the
// workspaces once, not once per alert.
func (m *Monitor) WithAnnouncer(a Announcer) *Monitor { m.announcer = a; return m }

// Monitor samples services, servers and disks, and fires and resolves
// alerts. Run is its only goroutine; the tracker is used from it alone.
type Monitor struct {
	probe   Probe
	servers ServerLister
	store   store
	notify  notifier
	log     *zap.Logger
	tracker *tracker
	loaded  bool
	sample  int
	volumes VolumeMeter
	backups BackupChecker
	// agentsAway: see WithAgents.
	agentsAway AgentChecker
	panel      PanelChecker
	mirrors    MirrorChecker
	updates    []UpdatesChecker
	disk       DiskAdvisor
	announcer  Announcer
	// changed is what the pass in progress opened or resolved; tick
	// announces and clears it.
	changed []Event

	// health is each service's last health-check answer, with the
	// container it came from; read by the API (LastHealthCheck), so behind
	// a lock. Kept in memory: it starts empty after a restart and fills at
	// the next sample.
	healthMu sync.RWMutex
	health   map[string]healthEntry
}

type healthEntry struct {
	answer    service.HealthAnswer
	container string
}

// LastHealthCheck is the service's last health-check answer, from the
// minute probe; false when none was taken (no check, not running, or the
// panel restarted since).
func (m *Monitor) LastHealthCheck(serviceID string) (service.HealthAnswer, bool) {
	m.healthMu.RLock()
	defer m.healthMu.RUnlock()
	e, ok := m.health[serviceID]
	return e.answer, ok
}

// recordHealth keeps each probed service's last answer. Since carries over
// while the same container answers.
func (m *Monitor) recordHealth(serviceID string, states []service.ContainerState, at time.Time) {
	for _, cs := range states {
		if cs.HealthAnswer == nil {
			continue
		}
		m.healthMu.Lock()
		if m.health == nil {
			m.health = map[string]healthEntry{}
		}
		a := *cs.HealthAnswer
		a.At, a.Since = at, at
		if prev, ok := m.health[serviceID]; ok && prev.container == cs.Name {
			a.Since = prev.answer.Since
		}
		m.health[serviceID] = healthEntry{answer: a, container: cs.Name}
		m.healthMu.Unlock()
		return
	}
}

// VolumeUsage is a shared volume's measured size (D-38). Measured is false
// when its server could not be reached or its Docker volume does not exist
// yet; the volume is still listed, so an alert about it is kept.
type VolumeUsage struct {
	ID, Name       string
	SizeLimitBytes int64
	UsedBytes      int64
	Measured       bool
}

// VolumeMeter measures every shared volume. Satisfied by an adapter over
// sharedvolume.Service in main.go.
type VolumeMeter interface {
	Measure(ctx context.Context) ([]VolumeUsage, error)
}

// WithVolumes checks shared volumes against their size limits.
func (m *Monitor) WithVolumes(v VolumeMeter) *Monitor { m.volumes = v; return m }

// BackupChecker reads every server's last backup and judges it (D-43).
// Satisfied by *server.Service.
type BackupChecker interface {
	CheckBackups(ctx context.Context) ([]server.BackupHealth, error)
}

// WithBackups fires backup_failed for a server whose backups fail.
func (m *Monitor) WithBackups(b BackupChecker) *Monitor { m.backups = b; return m }

// MirrorChecker is every repository mirror, failing after 3 failed syncs
// in a row (D-79). Satisfied by *code.Service.
type MirrorChecker interface {
	FailingMirrors(ctx context.Context) ([]code.MirrorHealth, error)
}

// AgentChecker is every agent server, and whether its agent is away now
// (D-106) or connected now. Satisfied by *server.Service.
type AgentChecker interface {
	AgentsAway(ctx context.Context) (map[string]bool, error)
	AgentsConnected(ctx context.Context) (map[string]bool, error)
	// AgentsFrozen: whether each agent holds another panel epoch's state
	// (D-113).
	AgentsFrozen(ctx context.Context) (map[string]bool, error)
}

// WithAgents fires agent_offline for a server whose agent is away.
func (m *Monitor) WithAgents(a AgentChecker) *Monitor { m.agentsAway = a; return m }

// checkAgents fires agent_offline for each agent server whose agent stays
// away (rules: 5 minutes), and resolves it when it is back or the server
// no longer uses an agent.
func (m *Monitor) checkAgents(ctx context.Context) {
	away, err := m.agentsAway.AgentsAway(ctx)
	if err != nil {
		if ctx.Err() == nil {
			m.log.Warn("alerts: cannot check agents", zap.Error(err))
		}
		return
	}
	for serverID, gone := range away {
		m.apply(ctx, key{KindAgentOffline, serverSubject(serverID)}, gone, "the agent has not connected for 5 minutes; its apps keep serving", "connected again")
	}
	for k := range m.tracker.open {
		if _, listed := away[serverIDOf(k.subject)]; k.kind == KindAgentOffline && !listed {
			m.resolve(ctx, k, "the server no longer uses an agent")
		}
	}

	frozen, err := m.agentsAway.AgentsFrozen(ctx)
	if err != nil {
		if ctx.Err() == nil {
			m.log.Warn("alerts: cannot check agents", zap.Error(err))
		}
		return
	}
	for serverID, f := range frozen {
		m.apply(ctx, key{KindAgentFrozen, serverSubject(serverID)}, f,
			"the panel was restored from a backup and the server kept its own state; choose which to keep on the server's page", "a state was chosen")
	}
	for k := range m.tracker.open {
		if _, listed := frozen[serverIDOf(k.subject)]; k.kind == KindAgentFrozen && !listed {
			m.resolve(ctx, k, "the server no longer uses an agent")
		}
	}
}

// UpdatesChecker judges hosts' last updates reads (D-133): server.Service
// for the servers, instance.Service for the panel's own.
type UpdatesChecker interface {
	UpdateProblems(ctx context.Context) ([]server.UpdateHealth, error)
}

// WithUpdates fires security_updates, reboot_required and docker_advisory.
func (m *Monitor) WithUpdates(c ...UpdatesChecker) *Monitor { m.updates = c; return m }

// DiskAdvisor is the disk advisor (D-135): each host's own disk alert
// threshold, and the automatic clean that runs when a host's alert fires.
type DiskAdvisor interface {
	// AlertPercents are the thresholds that aren't the default, by server
	// ID, "" the panel's own.
	AlertPercents(ctx context.Context) (map[string]int, error)
	DiskAlertFired(serverID string)
}

// WithDisk takes each host's disk alert threshold from the advisor and
// tells it when a host's alert fires.
func (m *Monitor) WithDisk(d DiskAdvisor) *Monitor { m.disk = d; return m }

// DiskCleaned rewrites the host's open disk alert, if one is, with how the
// automatic clean went (D-135); it sends nothing. Safe from any goroutine.
func (m *Monitor) DiskCleaned(ctx context.Context, serverID, detail string) {
	if err := m.store.SetDetail(ctx, key{KindDiskFull, serverSubject(serverID)}, detail); err != nil {
		m.log.Warn("alerts: cannot update the disk alert", zap.String("server_id", serverID), zap.Error(err))
	}
}

// updateKinds are judged from a host's last updates read.
var updateKinds = []Kind{KindSecurityUpdates, KindRebootRequired, KindDockerAdvisory}

// checkUpdates applies each host's judgement. A host without a read that
// worked keeps its alerts; one no checker lists any more (a server
// removed) has them resolved, unless a checker failed.
func (m *Monitor) checkUpdates(ctx context.Context) {
	listed := map[string]bool{}
	complete := true
	for _, c := range m.updates {
		hosts, err := c.UpdateProblems(ctx)
		if err != nil {
			if ctx.Err() == nil {
				m.log.Warn("alerts: cannot check updates", zap.Error(err))
			}
			complete = false
			continue
		}
		for _, h := range hosts {
			subject := serverSubject(h.ServerID)
			listed[subject] = true
			if !h.Known {
				continue
			}
			m.apply(ctx, key{KindSecurityUpdates, subject}, h.Security != "", h.Security, "no security updates wait now")
			m.apply(ctx, key{KindRebootRequired, subject}, h.Reboot != "", h.Reboot, "no reboot waits now")
			m.apply(ctx, key{KindDockerAdvisory, subject}, h.Docker != "", h.Docker, "no published advisory affects its Docker now")
		}
	}
	if !complete {
		return
	}
	for k := range m.tracker.open {
		if slices.Contains(updateKinds, k.kind) && !listed[k.subject] {
			m.resolve(ctx, k, "the server was removed")
		}
	}
}

// WithMirrors fires mirror_failing for a mirror whose syncs keep failing.
func (m *Monitor) WithMirrors(c MirrorChecker) *Monitor { m.mirrors = c; return m }

// PanelChecker says what is wrong with the panel itself (D-68), a sentence
// each, "" when nothing is; certRead is false when the certificate wasn't
// read. instance.Service satisfies it.
type PanelChecker interface {
	PanelProblems(ctx context.Context, cert bool) (containers, certificate string, certRead bool, err error)
}

// WithPanel fires panel_container and panel_certificate for the panel's own
// containers and certificate.
func (m *Monitor) WithPanel(p PanelChecker) *Monitor { m.panel = p; return m }

// checkPanel judges the panel's containers every minute and, with cert,
// its certificate.
func (m *Monitor) checkPanel(ctx context.Context, cert bool) {
	containers, certificate, certRead, err := m.panel.PanelProblems(ctx, cert)
	if err != nil {
		if ctx.Err() == nil {
			m.log.Warn("alerts: cannot check the panel", zap.Error(err))
		}
		return
	}
	m.apply(ctx, key{KindPanelContainer, ControlPlane}, containers != "", containers, "every panel container is healthy again")
	// Not read is not renewed: the alert stays as it is.
	if certRead {
		m.apply(ctx, key{KindPanelCertificate, ControlPlane}, certificate != "", certificate, "the certificate was renewed")
	}
}

func NewMonitor(probe Probe, servers ServerLister, st store, notify notifier, log *zap.Logger) *Monitor {
	return &Monitor{probe: probe, servers: servers, store: st, notify: notify, log: log, tracker: newTracker()}
}

// Run samples every minute until ctx ends.
func (m *Monitor) Run(ctx context.Context) {
	t := time.NewTicker(sampleInterval)
	defer t.Stop()
	for {
		m.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (m *Monitor) tick(ctx context.Context) {
	if !m.loaded {
		// The open alerts, so a restart neither re-sends them nor loses
		// their resolution. Until they are loaded nothing is judged.
		open, err := m.store.ListOpen(ctx)
		if err != nil {
			m.log.Warn("alerts: cannot load open alerts; retrying next minute", zap.Error(err))
			return
		}
		for _, k := range open {
			m.tracker.setOpen(k, true)
		}
		m.loaded = true
	}
	m.checkServices(ctx)
	if m.panel != nil {
		m.checkPanel(ctx, m.sample%diskEvery == 0)
	}
	if m.sample%diskEvery == 0 {
		m.checkDisks(ctx)
		if m.backups != nil {
			m.checkBackups(ctx)
		}
		if len(m.updates) > 0 {
			m.checkUpdates(ctx)
		}
	}
	if m.volumes != nil && m.sample%volumeEvery == 0 {
		m.checkVolumes(ctx)
	}
	if m.mirrors != nil {
		m.checkMirrors(ctx)
	}
	if m.agentsAway != nil {
		m.checkAgents(ctx)
	}
	if m.announcer != nil && len(m.changed) > 0 {
		m.announcer.Announce(ctx, m.changed)
	}
	m.changed = nil
	if m.sample%cleanupEvery == 0 {
		if err := m.store.DeleteResolvedBefore(ctx, time.Now().Add(-resolvedRetention)); err != nil && ctx.Err() == nil {
			m.log.Warn("alerts: cleanup failed", zap.Error(err))
		}
	}
	m.sample++
}

func serverSubject(serverID string) string {
	if serverID == "" {
		return ControlPlane
	}
	return serverID
}

// checkServices samples every server that runs services, judges each
// running service from its containers, and resolves the alerts of services
// that were stopped or deleted.
func (m *Monitor) checkServices(ctx context.Context) {
	targets, err := m.probe.ListMetricsTargets(ctx)
	if err != nil {
		if ctx.Err() == nil {
			m.log.Warn("alerts: cannot list running services", zap.Error(err))
		}
		return
	}
	groups := map[string][]service.MetricsTarget{}
	for _, t := range targets {
		if t.Preview {
			continue
		}
		groups[t.ServerID] = append(groups[t.ServerID], t)
	}
	type result struct {
		states map[string][]service.ContainerState
		err    error
	}
	results := map[string]result{}
	var mu sync.Mutex
	var g errgroup.Group
	g.SetLimit(serverConcurrency)
	for serverID, group := range groups {
		g.Go(func() error {
			sctx, cancel := context.WithTimeout(ctx, serverTimeout)
			defer cancel()
			states, err := m.probe.ProbeServerStates(sctx, serverID, group)
			mu.Lock()
			results[serverID] = result{states, err}
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	if ctx.Err() != nil {
		return // shutting down: not the servers' fault
	}
	// A server whose agent isn't connected is agent_offline's to tell,
	// after its grace (D-108): its probe fails however the dial ends
	// (offline, or the deadline while it reconnects).
	connected := map[string]bool{}
	if m.agentsAway != nil {
		if c, err := m.agentsAway.AgentsConnected(ctx); err == nil {
			connected = c
		}
	}

	for _, serverID := range sortedKeys(groups) {
		r := results[serverID]
		if up, agent := connected[serverID]; r.err != nil && agent && !up {
			continue
		}
		detail := ""
		if r.err != nil {
			detail = r.err.Error()
		}
		m.apply(ctx, key{KindServerUnreachable, serverSubject(serverID)}, r.err != nil, detail, "reachable again")
		if r.err != nil {
			continue // its services can't be judged; their counts wait
		}
		for _, t := range groups[serverID] {
			m.recordHealth(t.ID, r.states[t.ID], time.Now().UTC())
			conds := conditions(r.states[t.ID], t.ContainerHealth)
			for _, kind := range serviceKinds {
				detail, bad := conds[kind]
				m.apply(ctx, key{kind, t.ID}, bad, detail, "fixed")
			}
		}
	}

	// A server with no running service left is not sampled any more.
	for k := range m.tracker.open {
		if _, sampled := groups[serverIDOf(k.subject)]; k.kind == KindServerUnreachable && !sampled {
			m.resolve(ctx, k, "no running service left to check")
		}
	}
	gone, err := m.store.ListGoneServiceAlerts(ctx)
	if err != nil {
		m.log.Warn("alerts: cannot list alerts of stopped services", zap.Error(err))
		return
	}
	for _, k := range gone {
		m.resolve(ctx, k, "the service was stopped or deleted")
	}
}

func serverIDOf(subject string) string {
	if subject == ControlPlane {
		return ""
	}
	return subject
}

// checkDisks samples the control-plane server's and every remote server's
// disks. A server that can't be reached is skipped: its services' alerts
// cover it.
func (m *Monitor) checkDisks(ctx context.Context) {
	ids, err := m.servers.ListServerIDs(ctx)
	if err != nil {
		if ctx.Err() == nil {
			m.log.Warn("alerts: cannot list servers", zap.Error(err))
		}
		return
	}
	ids = append([]string{""}, ids...)
	usage := map[string][]service.DiskUsage{}
	var mu sync.Mutex
	var g errgroup.Group
	g.SetLimit(serverConcurrency)
	for _, id := range ids {
		g.Go(func() error {
			sctx, cancel := context.WithTimeout(ctx, serverTimeout)
			defer cancel()
			u, err := m.probe.ProbeServerDisk(sctx, id)
			if err == nil {
				mu.Lock()
				usage[id] = u
				mu.Unlock()
			}
			return nil
		})
	}
	_ = g.Wait()
	if ctx.Err() != nil {
		return
	}

	var thresholds map[string]int
	if m.disk != nil {
		var err error
		if thresholds, err = m.disk.AlertPercents(ctx); err != nil {
			m.log.Warn("alerts: cannot read the disk alert thresholds", zap.Error(err))
		}
	}
	listed := map[string]bool{}
	for _, id := range ids {
		listed[serverSubject(id)] = true
		d, ok := fullestDisk(usage[id])
		if !ok {
			continue
		}
		fire, resolve := diskFire, diskResolve
		if p, ok := thresholds[id]; ok {
			fire, resolve = p, p-(diskFire-diskResolve)
		}
		k := key{KindDiskFull, serverSubject(id)}
		detail := fmt.Sprintf("%s is %d%% full", d.Mount, d.UsedPercent)
		switch {
		case d.UsedPercent >= fire:
			wasOpen := m.tracker.open[k]
			m.apply(ctx, k, true, detail, "")
			if !wasOpen && m.tracker.open[k] && m.disk != nil {
				m.disk.DiskAlertFired(id)
			}
		case d.UsedPercent < resolve:
			m.apply(ctx, k, false, "", detail)
		}
	}
	for k := range m.tracker.open {
		if k.kind == KindDiskFull && !listed[k.subject] {
			m.resolve(ctx, k, "the server was removed")
		}
	}
}

// checkVolumes fires volume_full for a shared volume at volumeFire percent
// of its size limit or more, and resolves it under volumeResolve. A volume
// that no longer exists resolves; one that could not be measured keeps its
// alert as it is.
func (m *Monitor) checkVolumes(ctx context.Context) {
	usage, err := m.volumes.Measure(ctx)
	if err != nil {
		if ctx.Err() == nil {
			m.log.Warn("alerts: cannot measure shared volumes", zap.Error(err))
		}
		return
	}
	listed := map[string]bool{}
	for _, u := range usage {
		listed[u.ID] = true
		if !u.Measured || u.SizeLimitBytes <= 0 {
			continue
		}
		pct := u.UsedBytes * 100 / u.SizeLimitBytes
		k := key{KindVolumeFull, u.ID}
		detail := fmt.Sprintf("%s is %d%% full: %s of %s", u.Name, pct, humanBytes(u.UsedBytes), humanBytes(u.SizeLimitBytes))
		if pct >= 100 {
			detail += "; it is over its limit, which nothing enforces: free space or raise the limit"
		}
		switch {
		case pct >= volumeFire:
			m.apply(ctx, k, true, detail, "")
		case pct < volumeResolve:
			m.apply(ctx, k, false, "", detail)
		}
	}
	for k := range m.tracker.open {
		if k.kind == KindVolumeFull && !listed[k.subject] {
			m.resolve(ctx, k, "the shared volume was deleted")
		}
	}
}

// checkBackups fires backup_failed for each destination whose backups fail
// (server.BackupHealth), and resolves it when they succeed again or the
// destination is removed.
func (m *Monitor) checkBackups(ctx context.Context) {
	health, err := m.backups.CheckBackups(ctx)
	if err != nil {
		if ctx.Err() == nil {
			m.log.Warn("alerts: cannot check backups", zap.Error(err))
		}
		return
	}
	listed := map[string]bool{}
	for _, h := range health {
		k := key{KindBackupFailed, serverSubject(h.ServerID)}
		listed[k.subject] = true
		m.apply(ctx, k, h.Failing, h.Detail, "backed up")
	}
	for k := range m.tracker.open {
		if k.kind == KindBackupFailed && !listed[k.subject] {
			m.resolve(ctx, k, "the backup destination was removed")
		}
	}
}

// checkMirrors fires mirror_failing for each mirror whose last syncs
// failed, and resolves it when one succeeds or the mirror is removed.
func (m *Monitor) checkMirrors(ctx context.Context) {
	health, err := m.mirrors.FailingMirrors(ctx)
	if err != nil {
		if ctx.Err() == nil {
			m.log.Warn("alerts: cannot check mirrors", zap.Error(err))
		}
		return
	}
	listed := map[string]bool{}
	for _, h := range health {
		k := key{KindMirrorFailing, h.MirrorID}
		listed[k.subject] = true
		m.apply(ctx, k, h.Failing, h.Detail, "syncing again")
	}
	for k := range m.tracker.open {
		if k.kind == KindMirrorFailing && !listed[k.subject] {
			m.resolve(ctx, k, "the mirror was removed")
		}
	}
}

// humanBytes renders a size in binary units: "3.1 GiB".
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// apply records one sample of k and fires or resolves it.
func (m *Monitor) apply(ctx context.Context, k key, bad bool, detail, okDetail string) {
	fire, resolve := m.tracker.observe(k, bad)
	switch {
	case fire:
		m.fire(ctx, k, detail)
	case resolve:
		m.resolve(ctx, k, okDetail)
	}
}

// fire stores the alert and sends it. If it can't be stored, the tracker
// leaves it closed and the next bad sample tries again.
func (m *Monitor) fire(ctx context.Context, k key, detail string) {
	inserted, err := m.store.Open(ctx, k, detail)
	if err != nil {
		m.log.Warn("alerts: cannot store alert", zap.String("kind", string(k.kind)), zap.String("subject", k.subject), zap.Error(err))
		return
	}
	m.tracker.setOpen(k, true)
	if inserted {
		m.log.Warn("alert firing", zap.String("kind", string(k.kind)), zap.String("subject", k.subject), zap.String("detail", detail))
		e := Event{Kind: k.kind, Subject: k.subject, Detail: detail, At: time.Now()}
		m.notify.Notify(e)
		m.changed = append(m.changed, e)
	}
}

func (m *Monitor) resolve(ctx context.Context, k key, detail string) {
	updated, err := m.store.Resolve(ctx, k)
	if err != nil {
		m.log.Warn("alerts: cannot resolve alert", zap.String("kind", string(k.kind)), zap.String("subject", k.subject), zap.Error(err))
		return
	}
	m.tracker.setOpen(k, false)
	if updated {
		m.log.Info("alert resolved", zap.String("kind", string(k.kind)), zap.String("subject", k.subject), zap.String("detail", detail))
		e := Event{Kind: k.kind, Subject: k.subject, Resolved: true, Detail: detail, At: time.Now()}
		m.notify.Notify(e)
		m.changed = append(m.changed, e)
	}
}

func sortedKeys(m map[string][]service.MetricsTarget) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
