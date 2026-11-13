package instance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/buildinfo"
	"github.com/Nickpap21/toft/backend/pkg/hostproc"
	"go.uber.org/zap"
)

// The panel looks after itself (D-68): its own containers, its certificate
// and its server's usage, for Instance → Overview and Server. Operators only.

// panelContainers are the panel's own containers, as the compose file names
// them, and what each one is.
type panelContainer struct{ name, role string }

var panelContainers = []panelContainer{
	{"paas-backend", "api"},
	{"paas-frontend", "web"},
	{"paas-postgres", "database"},
	{"paas-traefik", "proxy"},
}

// gitContainer is git hosting's (D-71), on installs that have it.
var gitContainer = panelContainer{"paas-git", "git"}

// containers are this install's panel containers.
func (s *Service) containers() []panelContainer {
	if s.gitOn {
		return append(slices.Clone(panelContainers), gitContainer)
	}
	return panelContainers
}

func (s *Service) panelContainerNames() []string {
	cs := s.containers()
	names := make([]string, len(cs))
	for i, c := range cs {
		names[i] = c.name
	}
	return names
}

// WithGit says the install hosts git, so its container is the panel's too.
func (s *Service) WithGit(on bool) *Service {
	s.gitOn = on
	return s
}

// PanelContainer is one of the panel's own containers.
type PanelContainer struct {
	Name  string `json:"name"`
	Role  string `json:"role"`
	Image string `json:"image"`
	// State is Docker's (running, restarting, exited, …), or missing.
	State string `json:"state"`
	// Health is healthy, unhealthy or starting; "" without a health check.
	Health    string     `json:"health"`
	StartedAt *time.Time `json:"started_at"`
	Restarts  int        `json:"restarts"`
	// Live, while it runs; null otherwise.
	CPUPercent       *float64 `json:"cpu_percent"`
	MemoryBytes      *int64   `json:"memory_bytes"`
	MemoryLimitBytes *int64   `json:"memory_limit_bytes"`
}

// PanelCertificate is the certificate the panel's address serves.
type PanelCertificate struct {
	Domain   string    `json:"domain"`
	Issuer   string    `json:"issuer"`
	NotAfter time.Time `json:"not_after"`
	// Ask, while Let's Encrypt hasn't issued one: why, and when Toft has
	// Traefik ask again (certificateTick).
	Ask *PanelCertificateAsk `json:"ask"`
}

// PanelStatus is Instance → Overview's: is the panel well.
type PanelStatus struct {
	Version string `json:"version"`
	// Healthy: every container runs, and none is unhealthy or starting.
	Healthy    bool             `json:"healthy"`
	Containers []PanelContainer `json:"containers"`
	// Null while the panel has no domain, or its certificate can't be read.
	Certificate *PanelCertificate `json:"certificate"`
	// The host's; 0 until the panel has read it.
	UptimeSeconds int64 `json:"uptime_seconds"`
	// Shell: the host shell is on (TOFT_PANEL_HOST_SHELL isn't off).
	Shell bool `json:"shell"`
}

// PanelUsage is the panel server's usage now: the last 5-second reading.
type PanelUsage struct {
	CPUPercent       float64 `json:"cpu_percent"`
	Load1            float64 `json:"load_1"`
	Load5            float64 `json:"load_5"`
	Load15           float64 `json:"load_15"`
	MemoryUsedBytes  int64   `json:"memory_used_bytes"`
	MemoryCacheBytes int64   `json:"memory_cache_bytes"`
	SwapTotalBytes   int64   `json:"swap_total_bytes"`
	SwapUsedBytes    int64   `json:"swap_used_bytes"`
	DiskUsedBytes    int64   `json:"disk_used_bytes"`
	DiskFreeBytes    int64   `json:"disk_free_bytes"`
}

// PanelSample is one point of the panel server's history. In a day's or a
// week's range it is a bucket's average, with CPUPeak its busiest minute.
type PanelSample struct {
	At              time.Time `json:"at"`
	CPUPercent      float64   `json:"cpu_percent"`
	CPUPeak         float64   `json:"cpu_peak"`
	MemoryUsedBytes int64     `json:"memory_used_bytes"`
	DiskUsedBytes   int64     `json:"disk_used_bytes"`
}

// PanelDisk is what Docker keeps on the panel server's disk.
type PanelDisk struct {
	Images          int   `json:"images"`
	ImagesUnused    int   `json:"images_unused"`
	ImagesBytes     int64 `json:"images_bytes"`
	VolumesBytes    int64 `json:"volumes_bytes"`
	BuildCacheBytes int64 `json:"build_cache_bytes"`
	ContainersBytes int64 `json:"containers_bytes"`
}

// PanelDatabase is the panel's own Postgres.
type PanelDatabase struct {
	Version        string `json:"version"`
	SizeBytes      int64  `json:"size_bytes"`
	Connections    int    `json:"connections"`
	MaxConnections int    `json:"max_connections"`
}

// PanelFacts is what Docker says about the panel's host.
type PanelFacts struct {
	CPUs          int
	MemoryBytes   int64
	OS, Kernel    string
	DockerVersion string
}

// PanelServer is Instance → Server's: the panel server's usage and history.
type PanelServer struct {
	CPUs             int    `json:"cpus"`
	MemoryTotalBytes int64  `json:"memory_total_bytes"`
	DiskTotalBytes   int64  `json:"disk_total_bytes"`
	UptimeSeconds    int64  `json:"uptime_seconds"`
	OS               string `json:"os"`
	Kernel           string `json:"kernel"`
	DockerVersion    string `json:"docker_version"`
	// Null until the panel has read the host twice (5 seconds after start).
	Now     *PanelUsage   `json:"now"`
	Samples []PanelSample `json:"samples"`
	// Null when Docker couldn't say.
	Disk     *PanelDisk     `json:"disk"`
	Database *PanelDatabase `json:"database"`
	// Null until the host has been read, every 10 minutes.
	System *PanelSystem `json:"system"`
}

// PanelSystem is what the host says of itself, read every 10 minutes: its
// firewall (toft-firewall, D-37) and its security updates
// (unattended-upgrades) as systemd's ActiveState (active, inactive, failed,
// …) or missing when the unit isn't installed, and the filesystem holding /.
type PanelSystem struct {
	Firewall        string `json:"firewall"`
	SecurityUpdates string `json:"security_updates"`
	// Filesystem is its device and type: "/dev/sda1 ext4".
	Filesystem string `json:"filesystem"`
}

// panelSystemCmd asks systemd about both units, then findmnt about /: three
// blocks, a blank line apart.
var panelSystemCmd = []string{"sh", "-c", "systemctl show -p LoadState,ActiveState toft-firewall.service unattended-upgrades.service; echo; findmnt -no SOURCE,FSTYPE /"}

// parsePanelSystem reads panelSystemCmd's answer.
func parsePanelSystem(out string) PanelSystem {
	blocks := strings.Split(strings.TrimSpace(out), "\n\n")
	state := func(i int) string {
		if i >= len(blocks) {
			return ""
		}
		load, active := "", ""
		for _, line := range strings.Split(blocks[i], "\n") {
			k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
			switch k {
			case "LoadState":
				load = v
			case "ActiveState":
				active = v
			}
		}
		if load == "not-found" {
			return "missing"
		}
		return active
	}
	sys := PanelSystem{Firewall: state(0), SecurityUpdates: state(1)}
	if len(blocks) > 2 {
		sys.Filesystem = strings.Join(strings.Fields(blocks[2]), " ")
	}
	return sys
}

// PanelProbe reads the panel's host; cmd/server adapts the local Docker
// orchestrator to it.
type PanelProbe interface {
	// Containers inspects the named containers, in order; with stats, the
	// running ones' CPU and memory too (about a second).
	Containers(ctx context.Context, names []string, stats bool) ([]PanelContainer, error)
	Facts(ctx context.Context) (PanelFacts, error)
	Disk(ctx context.Context) (PanelDisk, error)
	// Certificate is what domain's TLS handshake serves.
	Certificate(ctx context.Context, domain string) (*PanelCertificate, error)
	// Logs follows a container's log from tail lines back until ctx ends.
	Logs(ctx context.Context, name string, tail int, line func(string)) error
	// LogsSince reads a container's log from since to now, each line led by
	// Docker's RFC 3339 timestamp and a space.
	LogsSince(ctx context.Context, name string, since time.Time, line func(string)) error
	// Shell opens a root shell on the host, cols by rows, for actorID.
	Shell(ctx context.Context, cols, rows int, actorID string) (PanelShell, error)
	// RunOnHost runs argv on the host and returns what it printed.
	RunOnHost(ctx context.Context, argv []string) (string, error)
	// RunOnHostStream is RunOnHost with stdin, unless nil, and each line
	// printed handed to logFn, unless nil (the gVisor install, D-97).
	RunOnHostStream(ctx context.Context, argv []string, stdin io.Reader, logFn func(string)) (string, error)
	// SweepHelpers removes the idle shells and finished helpers an earlier
	// backend process left on the host, and returns the shells it removed.
	SweepHelpers(ctx context.Context) ([]SweptShell, error)
}

// SweptShell is a shell an earlier backend left open, now removed: who
// opened it ("" when its helper predates the label) and when.
type SweptShell struct {
	ActorID string
	Opened  time.Time
}

// PanelShell is a root shell on the panel's host.
type PanelShell interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Resize(cols, rows int) error
	// Close ends the shell; Detach lets go of it and leaves it running.
	Close() error
	Detach() error
}

// PanelStore keeps the panel server's samples; PostgresRepository
// satisfies it.
type PanelStore interface {
	InsertPanelSample(ctx context.Context, s PanelSample) error
	ListPanelSamples(ctx context.Context, since time.Time, bucket time.Duration) ([]PanelSample, error)
	DeletePanelSamplesBefore(ctx context.Context, before time.Time) (int64, error)
	PanelDatabase(ctx context.Context) (PanelDatabase, error)
}

// Errors of the panel's reads.
var (
	ErrBadRange         = errors.New("range must be live, 24h or 7d")
	ErrUnknownContainer = errors.New("not one of the panel's containers")
	ErrPanelShellOff    = errors.New("the host shell on the panel's server is turned off (TOFT_PANEL_HOST_SHELL=off)")
)

const (
	panelTick      = 5 * time.Second
	panelLive      = 60 // readings: 5 minutes
	panelKeep      = 7 * 24 * time.Hour
	panelDiskTTL   = time.Minute
	panelCertTTL   = 10 * time.Minute
	panelSystemTTL = 10 * time.Minute
	panelSampleAt  = time.Minute
	// A new domain's certificate arrives within a minute or so; a bad read
	// is read again soon, so the alert judges a fresh one.
	panelCertRetryTTL = time.Minute
	// The monitor's containers, older than this, are read again on request.
	panelContainersTTL = 3 * panelTick
)

// panelMonitor reads the host every panelTick for the live view and stores
// a sample a minute. Its caches keep Docker's slow answers (system df, a
// TLS handshake) off every page refresh.
type panelMonitor struct {
	probe    PanelProbe
	store    PanelStore
	procRoot string // /proc
	diskPath string // a path on the host's Docker filesystem: /

	mu          sync.Mutex
	prev        hostproc.Reading
	havePrev    bool
	now         *PanelUsage
	uptime      time.Duration
	diskTotal   int64
	live        []PanelSample
	stored      hostproc.Reading
	storedAt    time.Time
	prunedAt    time.Time
	lastErr     string
	disk        *PanelDisk
	diskAt      time.Time
	diskReading bool
	cert        *PanelCertificate
	certDomain  string
	certAt      time.Time
	// ask is certificateTick's latest for askDomain.
	ask       *PanelCertificateAsk
	askDomain string
	// The containers with their stats, from the last tick.
	containers   []PanelContainer
	containersAt time.Time
	system       *PanelSystem
	systemAt     time.Time
}

// WithPanelShell turns the host shell on the panel's server on or off
// (TOFT_PANEL_HOST_SHELL, D-68). It is set in the panel's settings file and
// not in the UI, so a stolen session can't turn it back on.
func (s *Service) WithPanelShell(on bool) *Service {
	s.panelShellOn = on
	return s
}

// WithPanelMonitor sets how the panel reads its own host (D-68): Docker
// through probe, samples in store, the host's /proc at procRoot and its
// disk at diskPath. Without it the panel's reads fail.
func (s *Service) WithPanelMonitor(probe PanelProbe, store PanelStore, procRoot, diskPath string) *Service {
	s.panel = &panelMonitor{probe: probe, store: store, procRoot: procRoot, diskPath: diskPath}
	return s
}

// RunPanelMonitor reads the host until ctx ends. One goroutine: a CPU
// percentage needs two readings, so they are taken on a clock rather than
// on request.
func (s *Service) RunPanelMonitor(ctx context.Context) {
	if s.panel == nil {
		return
	}
	t := time.NewTicker(panelTick)
	defer t.Stop()
	s.panelTick(ctx, time.Now())
	s.panelContainersTick(ctx, time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.monitorTick(ctx, now)
		}
	}
}

// monitorTick is everything the monitor does on a tick.
func (s *Service) monitorTick(ctx context.Context, now time.Time) {
	s.panelTick(ctx, now)
	s.panelContainersTick(ctx, now)
	s.sweepPanelHelpers(ctx)
	s.panelSystemTick(ctx, now)
	s.recordUpgrade(ctx)
	s.watchdogTick(ctx, now)
	s.certificateTick(ctx, now)
}

func (s *Service) panelTick(ctx context.Context, now time.Time) {
	m := s.panel
	r, err := hostproc.Read(m.procRoot)
	var total, used, free int64
	if err == nil {
		total, used, free, err = hostproc.Disk(m.diskPath)
	}
	if err != nil {
		m.mu.Lock()
		repeat := m.lastErr == err.Error()
		m.lastErr = err.Error()
		m.mu.Unlock()
		if !repeat {
			s.log.Warn("reading the panel's host failed", zap.Error(err))
		}
		return
	}

	m.mu.Lock()
	m.lastErr = ""
	m.uptime, m.diskTotal = r.Uptime, total
	if m.havePrev {
		u := &PanelUsage{
			CPUPercent: hostproc.CPUPercent(m.prev, r),
			Load1:      r.Load1, Load5: r.Load5, Load15: r.Load15,
			MemoryUsedBytes: r.MemoryUsed, MemoryCacheBytes: r.MemoryCache,
			SwapTotalBytes: r.SwapTotal, SwapUsedBytes: r.SwapUsed,
			DiskUsedBytes: used, DiskFreeBytes: free,
		}
		m.now = u
		m.live = append(m.live, PanelSample{At: now.UTC(), CPUPercent: u.CPUPercent, CPUPeak: u.CPUPercent, MemoryUsedBytes: r.MemoryUsed, DiskUsedBytes: used})
		if len(m.live) > panelLive {
			m.live = slices.Delete(m.live, 0, len(m.live)-panelLive)
		}
	}
	m.prev, m.havePrev = r, true
	var sample *PanelSample
	switch {
	case m.storedAt.IsZero():
		m.stored, m.storedAt = r, now
	case now.Sub(m.storedAt) >= panelSampleAt:
		// The minute's average, from the counters at its start.
		sample = &PanelSample{At: now.UTC().Truncate(time.Second), CPUPercent: hostproc.CPUPercent(m.stored, r), MemoryUsedBytes: r.MemoryUsed, DiskUsedBytes: used}
		m.stored, m.storedAt = r, now
	}
	prune := now.Sub(m.prunedAt) >= time.Hour
	if prune {
		m.prunedAt = now
	}
	m.mu.Unlock()

	if sample != nil {
		if err := m.store.InsertPanelSample(ctx, *sample); err != nil && ctx.Err() == nil {
			s.log.Warn("storing the panel's sample failed", zap.Error(err))
		}
	}
	if prune {
		if _, err := m.store.DeletePanelSamplesBefore(ctx, now.Add(-panelKeep)); err != nil && ctx.Err() == nil {
			s.log.Warn("pruning the panel's samples failed", zap.Error(err))
		}
	}
}

// panelContainersTick takes the containers with their stats, which wait
// about two seconds for a second CPU reading, so that PanelStatus, polled
// every 5 seconds, answers without that wait. A failed read leaves the last
// one to go stale; PanelStatus then reads them itself and says why.
func (s *Service) panelContainersTick(ctx context.Context, now time.Time) {
	cs, err := s.panel.probe.Containers(ctx, s.panelContainerNames(), true)
	if err != nil {
		return
	}
	s.panel.mu.Lock()
	s.panel.containers, s.panel.containersAt = cs, now
	s.panel.mu.Unlock()
}

// panelSystemTick reads the host every panelSystemTTL, through a helper
// container, so that a page doesn't start one.
func (s *Service) panelSystemTick(ctx context.Context, now time.Time) {
	m := s.panel
	m.mu.Lock()
	due := now.Sub(m.systemAt) >= panelSystemTTL
	m.mu.Unlock()
	if !due {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := m.probe.RunOnHost(rctx, panelSystemCmd)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.systemAt = now // a failure waits its turn too
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("reading the panel's host failed", zap.Error(err))
		}
		return
	}
	sys := parsePanelSystem(out)
	m.system = &sys
}

// sweepPanelHelpers removes what an earlier backend left on the host; a
// shell only once what was started from it has ended. Each shell's audit
// row closes then: the process that would have written it is gone.
func (s *Service) sweepPanelHelpers(ctx context.Context) {
	shells, err := s.panel.probe.SweepHelpers(ctx)
	if err != nil && ctx.Err() == nil {
		s.log.Debug("sweeping the panel's helpers failed", zap.Error(err))
	}
	for _, sh := range shells {
		s.log.Info("removed a shell an earlier backend left open on the panel's server", zap.String("actor", sh.ActorID), zap.Time("opened", sh.Opened))
		if sh.ActorID != "" {
			s.audit(ctx, sh.ActorID, "panel.shell_closed", "", map[string]any{"seconds": int(time.Since(sh.Opened).Seconds()), "restart": true})
		}
	}
}

// PanelStatus says whether the panel is well: its containers and its
// certificate. Operators only.
func (s *Service) PanelStatus(ctx context.Context, actorID string) (*PanelStatus, error) {
	if err := s.requireOperator(ctx, actorID); err != nil {
		return nil, err
	}
	if s.panel == nil {
		return nil, errors.New("the panel's monitor is not configured")
	}
	s.panel.mu.Lock()
	cs := slices.Clone(s.panel.containers)
	fresh := time.Since(s.panel.containersAt) < panelContainersTTL
	s.panel.mu.Unlock()
	if !fresh {
		var err error
		cs, err = s.panel.probe.Containers(ctx, s.panelContainerNames(), true)
		if err != nil {
			return nil, fmt.Errorf("the panel's containers: %w", err)
		}
	}
	want := s.containers()
	st := &PanelStatus{Version: buildinfo.Version, Healthy: len(cs) == len(want), Containers: cs, Shell: s.panelShellOn}
	for i := range st.Containers {
		c := &st.Containers[i]
		c.Role = want[i].role
		if c.State != "running" || c.Health == "unhealthy" || c.Health == "starting" {
			st.Healthy = false
		}
	}
	st.Certificate = s.panelCertificate(ctx)
	s.panel.mu.Lock()
	st.UptimeSeconds = int64(s.panel.uptime.Seconds())
	s.panel.mu.Unlock()
	return st, nil
}

// panelCertificate is the panel domain's certificate, read at most every
// panelCertTTL (panelCertRetryTTL when it is not a healthy certificate),
// with certificateTick's ask; nil
// without a domain or when the handshake fails.
func (s *Service) panelCertificate(ctx context.Context) *PanelCertificate {
	m := s.panel
	domain := s.PanelDomain()
	if domain == "" {
		return nil
	}
	m.mu.Lock()
	ttl := panelCertTTL
	if m.cert == nil || certProblem(m.cert, time.Now()) != "" {
		ttl = panelCertRetryTTL
	}
	if m.certDomain == domain && time.Since(m.certAt) < ttl {
		c := m.withAsk(m.cert)
		m.mu.Unlock()
		return c
	}
	m.mu.Unlock()
	cert, err := m.probe.Certificate(ctx, domain)
	if err != nil {
		s.log.Debug("reading the panel's certificate failed", zap.Error(err))
		cert = nil
	}
	m.mu.Lock()
	m.cert, m.certDomain, m.certAt = cert, domain, time.Now()
	cert = m.withAsk(cert)
	m.mu.Unlock()
	return cert
}

// withAsk is c with certificateTick's latest for its domain; m.mu held.
func (m *panelMonitor) withAsk(c *PanelCertificate) *PanelCertificate {
	if c == nil || m.ask == nil || m.askDomain != c.Domain {
		return c
	}
	cp := *c
	cp.Ask = m.ask
	return &cp
}

// PanelServer is the panel server's usage now and over rng: live (the last
// 5 minutes, every 5 seconds), 24h (5-minute averages) or 7d (30-minute
// averages). Operators only.
func (s *Service) PanelServer(ctx context.Context, actorID, rng string) (*PanelServer, error) {
	var since time.Duration
	var bucket time.Duration
	switch rng {
	case "live":
	case "24h":
		since, bucket = 24*time.Hour, 5*time.Minute
	case "7d":
		since, bucket = panelKeep, 30*time.Minute
	default:
		return nil, ErrBadRange
	}
	if err := s.requireOperator(ctx, actorID); err != nil {
		return nil, err
	}
	m := s.panel
	if m == nil {
		return nil, errors.New("the panel's monitor is not configured")
	}
	facts, err := m.probe.Facts(ctx)
	if err != nil {
		return nil, fmt.Errorf("the panel's host: %w", err)
	}
	out := &PanelServer{CPUs: facts.CPUs, MemoryTotalBytes: facts.MemoryBytes, OS: facts.OS, Kernel: facts.Kernel, DockerVersion: facts.DockerVersion}

	m.mu.Lock()
	out.DiskTotalBytes, out.UptimeSeconds = m.diskTotal, int64(m.uptime.Seconds())
	if m.now != nil {
		now := *m.now
		out.Now = &now
	}
	live := slices.Clone(m.live)
	if m.system != nil {
		sys := *m.system
		out.System = &sys
	}
	m.mu.Unlock()

	if rng == "live" {
		out.Samples = live
	} else if out.Samples, err = m.store.ListPanelSamples(ctx, time.Now().Add(-since), bucket); err != nil {
		return nil, fmt.Errorf("the panel's samples: %w", err)
	}
	if out.Samples == nil {
		out.Samples = []PanelSample{}
	}
	out.Disk = s.panelDisk(ctx)
	if db, err := m.store.PanelDatabase(ctx); err == nil {
		out.Database = &db
	} else {
		s.log.Warn("reading the panel's database stats failed", zap.Error(err))
	}
	return out, nil
}

// panelDisk is the last docker system df, nil before the first. A stale one
// starts a read in the background rather than holding the request: df
// sizes every volume, which takes seconds on a busy host. One read at a
// time, each bounded by a minute.
func (s *Service) panelDisk(ctx context.Context) *PanelDisk {
	m := s.panel
	m.mu.Lock()
	defer m.mu.Unlock()
	if (m.disk == nil || time.Since(m.diskAt) >= panelDiskTTL) && !m.diskReading {
		m.diskReading = true
		go func() {
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			d, err := m.probe.Disk(rctx)
			m.mu.Lock()
			defer m.mu.Unlock()
			m.diskReading = false
			if err != nil {
				s.log.Warn("reading Docker's disk use failed", zap.Error(err))
				return
			}
			m.disk, m.diskAt = &d, time.Now()
		}()
	}
	if m.disk == nil {
		return nil
	}
	d := *m.disk
	return &d
}

// PanelLogs follows one of the panel's containers' logs, tail lines back,
// until ctx ends. Operators only.
func (s *Service) PanelLogs(ctx context.Context, actorID, name string, tail int, line func(string)) error {
	if err := s.CheckPanelContainer(ctx, actorID, name); err != nil {
		return err
	}
	return s.panel.probe.Logs(ctx, name, tail, line)
}

// CheckPanelContainer refuses anyone but an operator, and any name but the
// panel's containers', before a log stream starts.
func (s *Service) CheckPanelContainer(ctx context.Context, actorID, name string) error {
	if err := s.requireOperator(ctx, actorID); err != nil {
		return err
	}
	if s.panel == nil {
		return errors.New("the panel's monitor is not configured")
	}
	if !slices.ContainsFunc(s.containers(), func(c panelContainer) bool { return c.name == name }) {
		return ErrUnknownContainer
	}
	return nil
}

// CheckPanelShell refuses anyone but an operator, and everyone while the
// shell is turned off, before the WebSocket opens.
func (s *Service) CheckPanelShell(ctx context.Context, actorID string) error {
	if err := s.requireOperator(ctx, actorID); err != nil {
		return err
	}
	if s.panel == nil || !s.panelShellOn {
		return ErrPanelShellOff
	}
	return nil
}

// OpenPanelShell opens a root shell on the panel's server for an operator
// whose password was just confirmed (the handler checks it), and writes it
// to the instance audit log with where it was opened from (D-53). The
// caller closes it and calls ClosedPanelShell.
func (s *Service) OpenPanelShell(ctx context.Context, actorID, from string, cols, rows int) (PanelShell, error) {
	if err := s.CheckPanelShell(ctx, actorID); err != nil {
		return nil, err
	}
	sh, err := s.panel.probe.Shell(ctx, cols, rows, actorID)
	if err != nil {
		return nil, err
	}
	s.audit(ctx, actorID, "panel.shell_opened", "", map[string]any{"from": from})
	return sh, nil
}

// ClosedPanelShell records how long a shell was open.
func (s *Service) ClosedPanelShell(ctx context.Context, actorID string, opened time.Time) {
	s.audit(ctx, actorID, "panel.shell_closed", "", map[string]any{"seconds": int(time.Since(opened).Seconds())})
}
