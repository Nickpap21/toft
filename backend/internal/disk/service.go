package disk

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Host is one host's Docker for as long as a read or a clean lasts.
// Removals are never forced: Docker refuses what a container uses.
type Host interface {
	Usage(ctx context.Context) (Usage, error)
	// Filesystem is the size and use of the filesystem holding Docker's data.
	Filesystem(ctx context.Context) (size, used, free int64, err error)
	// RemoveImage removes a tag, or by ID an untagged image, with its
	// untagged parents; ErrInUse when Docker refuses; nil when it's gone.
	RemoveImage(ctx context.Context, ref string) error
	// ImageUsed reports whether any container, running or not, uses the image.
	ImageUsed(ctx context.Context, id string) (bool, error)
	// PruneBuildCache trims the build cache to maxUsed bytes, or with all
	// removes every unused record; it returns what it freed.
	PruneBuildCache(ctx context.Context, maxUsed int64, all bool) (int64, error)
	InspectVolume(ctx context.Context, name string) (Volume, error)
	// RemoveVolume deletes a volume; ErrVolumeInUse when a container mounts it.
	RemoveVolume(ctx context.Context, name string) error
	Close()
}

// AlertNotes takes how an automatic clean went, for the host's open disk
// alert (alert.Monitor).
type AlertNotes interface {
	DiskCleaned(ctx context.Context, serverID, detail string)
}

// WithAlerts has each automatic clean rewrite the host's open disk alert.
func (s *Service) WithAlerts(a AlertNotes) { s.alerts = a }

// Hosts opens a host's Docker; serverID "" is the panel's own.
type Hosts interface {
	Host(ctx context.Context, serverID string) (Host, error)
}

// DeployGate says whether a deploy runs on a host.
type DeployGate interface {
	DeployRunning(ctx context.Context, serverID string) (bool, error)
}

// Auditor writes the audit log: a server's to its workspace's log, the
// panel's ("" serverID) to the instance's. actorID "" is Toft itself.
type Auditor interface {
	Audit(ctx context.Context, serverID, orgID, actorID, action string, detail map[string]any)
}

// People answers who someone is.
type People interface {
	IsOperator(ctx context.Context, userID string) (bool, error)
	Name(ctx context.Context, userID string) string
}

// Service is the disk advisor.
type Service struct {
	repo     Repository
	hosts    Hosts
	deploys  DeployGate
	audit    Auditor
	people   People
	releases func() []string
	alerts   AlertNotes
	log      *zap.Logger
	now      func() time.Time

	mu       sync.Mutex
	cache    map[string]cached
	running  map[string]*Clean
	lastAuto map[string]time.Time
}

type cached struct {
	report Report
	at     time.Time
}

// NewService returns the advisor. releases are the panel releases toft
// upgrade keeps (nil when it wasn't installed with the installer).
func NewService(repo Repository, hosts Hosts, deploys DeployGate, audit Auditor, people People, releases func() []string, log *zap.Logger) *Service {
	return &Service{repo: repo, hosts: hosts, deploys: deploys, audit: audit, people: people, releases: releases, log: log, now: time.Now,
		cache: map[string]cached{}, running: map[string]*Clean{}, lastAuto: map[string]time.Time{}}
}

// Authorize finds the host for orgID's caller. Reading takes any member of
// the server's workspace; writing takes an owner (D-128) or an instance
// operator. The panel's own server ("") takes an operator either way.
func (s *Service) Authorize(ctx context.Context, serverID, orgID, actorID, role string, write bool) (HostRow, error) {
	if serverID == "" {
		ok, err := s.people.IsOperator(ctx, actorID)
		if err != nil {
			return HostRow{}, err
		}
		if !ok {
			return HostRow{}, ErrForbidden
		}
		return s.repo.Server(ctx, "")
	}
	h, err := s.repo.Server(ctx, serverID)
	if err != nil {
		return HostRow{}, err
	}
	if h.OrgID != orgID {
		return HostRow{}, ErrNotFound
	}
	if write && role != "owner" {
		ok, err := s.people.IsOperator(ctx, actorID)
		if err != nil {
			return HostRow{}, err
		}
		if !ok {
			return HostRow{}, ErrForbidden
		}
	}
	return h, nil
}

func (s *Service) open(ctx context.Context, serverID string) (Host, error) {
	h, err := s.hosts.Host(ctx, serverID)
	if err != nil {
		return nil, errors.Join(ErrUnreachable, err)
	}
	return h, nil
}

// Report reads what fills the host's disk, from the last 5 minutes unless
// fresh.
func (s *Service) Report(ctx context.Context, h HostRow, fresh bool) (*Report, error) {
	s.mu.Lock()
	c, ok := s.cache[h.ID]
	s.mu.Unlock()
	if !ok || fresh || s.now().Sub(c.at) > readTTL {
		r, _, err := s.read(ctx, h)
		if err != nil {
			return nil, err
		}
		c = cached{report: *r, at: s.now()}
		s.mu.Lock()
		s.cache[h.ID] = c
		s.mu.Unlock()
	}
	r := c.report
	r.Classes = slices.Clone(c.report.Classes) // the cached report is shared
	r.Policy = h.Policy.view(h.ID == "", r.SizeBytes)
	for i := range r.Classes {
		r.Classes[i].Automatic = contains(r.Policy.Effective.AutoClean, r.Classes[i].Kind)
	}
	r.BuildCache.LimitBytes = r.Policy.Effective.BuildCacheLimitBytes
	r.BuildCache.OverLimitBytes = max(r.BuildCache.Bytes-r.BuildCache.LimitBytes, 0)
	r.Clean, r.LastClean = s.cleans(h)
	return &r, nil
}

func contains(ks []Kind, k Kind) bool {
	for _, x := range ks {
		if x == k {
			return true
		}
	}
	return false
}

// read reads the host's Docker and classes everything on it.
func (s *Service) read(ctx context.Context, h HostRow) (*Report, plan, error) {
	host, err := s.open(ctx, h.ID)
	if err != nil {
		return nil, plan{}, err
	}
	defer host.Close()
	u, err := host.Usage(ctx)
	if err != nil {
		return nil, plan{}, errors.Join(ErrUnreachable, err)
	}
	f, err := s.facts(ctx, h.ID, h.OrgID)
	if err != nil {
		return nil, plan{}, err
	}
	f.Shared = h.Shared
	p := classify(u, f)
	pol := h.Policy.view(h.ID == "", u.SizeBytes)
	var volBytes int64
	for _, v := range u.Volumes {
		volBytes += max(v.Bytes, 0)
	}
	r := &Report{
		ReadAt: s.now().UTC(), SizeBytes: u.SizeBytes, UsedBytes: u.UsedBytes, FreeBytes: u.FreeBytes,
		Classes: p.classes(pol.Effective.AutoClean),
		BuildCache: BuildCache{Bytes: u.BuildCacheBytes, ReclaimableBytes: u.BuildCacheReclaimable,
			LimitBytes: pol.Effective.BuildCacheLimitBytes, OverLimitBytes: max(u.BuildCacheBytes-pol.Effective.BuildCacheLimitBytes, 0)},
		Volumes: p.volumes, VolumesInUse: p.inUse, ContainersBytes: u.ContainersBytes,
		OtherBytes:   max(u.UsedBytes-u.ImagesBytes-u.BuildCacheBytes-volBytes-u.ContainersBytes, 0),
		RollbackKept: p.rollback, OtherWorkspaces: p.others, Policy: pol,
	}
	if r.Volumes == nil {
		r.Volumes = []OrphanVolume{}
	}
	if r.RollbackKept == nil {
		r.RollbackKept = []RollbackKept{}
	}
	return r, p, nil
}

// facts reads what the database knows of the host, for orgID's report (the
// host's workspace: Authorize lets no other read it; "" the panel's).
func (s *Service) facts(ctx context.Context, serverID, orgID string) (Facts, error) {
	svcs, err := s.repo.HostServices(ctx, serverID)
	if err != nil {
		return Facts{}, err
	}
	ids := make([]string, 0, len(svcs))
	for _, sv := range svcs {
		ids = append(ids, sv.ID)
	}
	deps, err := s.repo.LiveDeployments(ctx, ids)
	if err != nil {
		return Facts{}, err
	}
	prefixes, err := s.repo.ServicePrefixes(ctx)
	if err != nil {
		return Facts{}, err
	}
	f := Facts{Panel: serverID == "", OrgID: orgID, Services: svcs, Deployments: deps, Prefixes: prefixes}
	if f.Panel && s.releases != nil {
		f.PanelReleases = s.releases()
	}
	return f, nil
}

// cleans are the clean running on the host and the last that ended.
func (s *Service) cleans(h HostRow) (*Clean, *Clean) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var cur *Clean
	if c := s.running[h.ID]; c != nil {
		cp := *c
		cur = &cp
	}
	return cur, h.LastClean
}

// CleanState is the clean running on the host, else the last that ended;
// nil before the first.
func (s *Service) CleanState(h HostRow) *Clean {
	cur, last := s.cleans(h)
	if cur != nil {
		return cur
	}
	return last
}

// Cleaning reports whether a clean runs on the host: deploys there wait.
func (s *Service) Cleaning(_ context.Context, serverID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running[serverID] != nil, nil
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// StartClean starts removing the kinds asked for, and with all the whole
// build cache; it answers at once.
func (s *Service) StartClean(ctx context.Context, h HostRow, actorID string, ks []Kind, all bool) (*Clean, error) {
	for _, k := range ks {
		if !k.Removable() {
			return nil, fmt.Errorf("%w: %s isn't removable", ErrNothingToClean, k)
		}
		if k == OldPanelReleases && h.ID != "" {
			return nil, ErrKindNotHere
		}
	}
	if len(ks) == 0 && !all {
		return nil, ErrNothingToClean
	}
	c, err := s.begin(ctx, h, actorID, ks, all, false)
	if err != nil {
		return nil, err
	}
	cp := *c // before the clean starts changing it
	go s.run(h, c)
	return &cp, nil
}

// begin marks a clean as running on the host, unless one runs or a deploy
// does. The deploy is checked again after the mark: a deploy that began in
// between sees the clean (deployment.Service checks after its own record).
func (s *Service) begin(ctx context.Context, h HostRow, actorID string, ks []Kind, all, automatic bool) (*Clean, error) {
	busy := func() error {
		if s.deploys == nil {
			return nil
		}
		running, err := s.deploys.DeployRunning(ctx, h.ID)
		if err != nil {
			return fmt.Errorf("check the server's deploys: %w", err)
		}
		if running {
			return ErrDeployRunning
		}
		return nil
	}
	if err := busy(); err != nil {
		return nil, err
	}
	c := &Clean{ID: newID(), Kinds: ks, BuildCacheAll: all, Automatic: automatic, Outcome: Running, Step: "reading", StartedAt: s.now().UTC()}
	if c.Kinds == nil {
		c.Kinds = []Kind{}
	}
	if actorID != "" {
		c.StartedBy, c.actorID = s.people.Name(ctx, actorID), actorID
	}
	s.mu.Lock()
	if s.running[h.ID] != nil {
		s.mu.Unlock()
		return nil, ErrCleanRunning
	}
	s.running[h.ID] = c
	s.mu.Unlock()
	if err := busy(); err != nil {
		s.mu.Lock()
		delete(s.running, h.ID)
		s.mu.Unlock()
		return nil, err
	}
	s.audit.Audit(ctx, h.ID, h.OrgID, actorID, auditAction(h.ID, "disk_clean_started"), map[string]any{"clean": c.ID, "kinds": c.Kinds, "build_cache_all": all, "automatic": automatic})
	return c, nil
}

func auditAction(serverID, what string) string {
	if serverID == "" {
		return "panel." + what
	}
	return "server." + what
}

// cleanTimeout bounds one clean.
const cleanTimeout = 30 * time.Minute

// run carries out a clean begun by begin, and records how it went.
func (s *Service) run(h HostRow, c *Clean) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanTimeout)
	defer cancel()
	err := s.clean(ctx, h, c)
	s.mu.Lock()
	now := s.now().UTC()
	c.FinishedAt = &now
	c.Step = "done"
	c.Outcome = Succeeded
	if err != nil {
		c.Outcome, c.Error = Failed, cleanError(err)
	}
	done := *c
	delete(s.running, h.ID)
	delete(s.cache, h.ID)
	s.mu.Unlock()
	bg := context.WithoutCancel(ctx)
	if err := s.repo.SetLastClean(bg, h.ID, done); err != nil {
		s.log.Error("disk: store the last clean", zap.Error(err), zap.String("server_id", h.ID))
	}
	s.audit.Audit(bg, h.ID, h.OrgID, done.actorID, auditAction(h.ID, "disk_cleaned"), map[string]any{
		"clean": done.ID, "kinds": done.Kinds, "build_cache_all": done.BuildCacheAll, "automatic": done.Automatic, "outcome": done.Outcome,
		"freed_bytes": done.FreedBytes, "removed": done.Removed, "skipped": done.Skipped, "error": done.Error,
		"seconds": int(now.Sub(done.StartedAt).Seconds())})
	if done.Automatic && s.alerts != nil {
		s.alerts.DiskCleaned(bg, h.ID, alertDetail(done))
	}
}

// cleanError is what a failed clean says, to everyone who reads the
// server: never the transport's own message (as handler.fail), and short
// enough for disk_last_clean's 8 KB.
func cleanError(err error) string {
	if errors.Is(err, ErrUnreachable) {
		return ErrUnreachable.Error()
	}
	msg := err.Error()
	if len(msg) > 300 {
		msg = strings.ToValidUTF8(msg[:300], "") + "…"
	}
	return msg
}

// alertDetail is the disk alert's detail once the automatic clean ended:
// "86% used, 22 GB free of 240 GB. Toft cleaned it automatically and freed
// 9.4 GB; 4.2 GB more is safe to delete but needs a person: pulled images
// no service uses."
func alertDetail(c Clean) string {
	a := c.after
	head := ""
	if a.size > 0 {
		head = fmt.Sprintf("%d%% used, %s free of %s. ", a.used*100/a.size, gbText(a.free), gbText(a.size))
	}
	if c.Outcome == Failed {
		return head + fmt.Sprintf("Toft's automatic clean freed %s, then stopped: %s.", gbText(c.FreedBytes), c.Error)
	}
	out := head + "Toft cleaned it automatically and freed " + gbText(c.FreedBytes)
	if a.left <= 0 {
		return out + "."
	}
	names := make([]string, 0, len(a.leftKinds))
	for _, k := range a.leftKinds {
		names = append(names, kindText[k])
	}
	return out + fmt.Sprintf("; %s more is safe to delete but needs a person: %s.", gbText(a.left), strings.Join(names, ", "))
}

var kindText = map[Kind]string{
	Untagged: "untagged images", OldBuilds: "old builds", OldPanelReleases: "old panel releases", UnusedPulled: "pulled images no service uses",
}

// gbText writes bytes as the disk card does: "9.4 GB", "205 MB".
func gbText(n int64) string {
	if n >= 1e9 {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1e9), ".0") + " GB"
	}
	return fmt.Sprintf("%d MB", (n+5e5)/1e6)
}

func (s *Service) progress(c *Clean, fn func()) {
	s.mu.Lock()
	fn()
	s.mu.Unlock()
}

func (s *Service) clean(ctx context.Context, h HostRow, c *Clean) error {
	_, p, err := s.read(ctx, h)
	if err != nil {
		return err
	}
	host, err := s.open(ctx, h.ID)
	if err != nil {
		return err
	}
	defer host.Close()
	for _, k := range cleanOrder {
		if !contains(c.Kinds, k) {
			var left int64
			for _, im := range p.byKind[k] {
				left += im.unique()
			}
			if left > 0 {
				s.progress(c, func() { c.after.left += left; c.after.leftKinds = append(c.after.leftKinds, k) })
			}
			continue
		}
		s.progress(c, func() { c.Step = string(k) })
		for _, im := range p.byKind[k] {
			err := removeImage(ctx, host, im)
			switch {
			case errors.Is(err, ErrInUse):
				s.progress(c, func() { c.Skipped++ })
			case err != nil:
				return fmt.Errorf("remove %s: %w", firstRef(im), err)
			default:
				s.progress(c, func() { c.Removed++; c.FreedBytes += im.unique() })
				// At once, so a clean cut short still marks what it removed.
				if ids := p.marks[im.ID]; len(ids) > 0 {
					if err := s.repo.MarkImagesRemoved(context.WithoutCancel(ctx), ids); err != nil {
						s.log.Error("disk: mark removed images", zap.Error(err), zap.String("server_id", h.ID))
					}
				}
			}
		}
	}
	s.progress(c, func() { c.Step = "build_cache" })
	limit := h.Policy.view(h.ID == "", 0).Effective.BuildCacheLimitBytes
	if size, _, _, err := host.Filesystem(ctx); err == nil {
		limit = h.Policy.view(h.ID == "", size).Effective.BuildCacheLimitBytes
	}
	freed, err := host.PruneBuildCache(ctx, limit, c.BuildCacheAll)
	if err != nil {
		return fmt.Errorf("prune the build cache: %w", err)
	}
	s.progress(c, func() { c.FreedBytes += freed })
	if size, used, free, err := host.Filesystem(ctx); err == nil {
		s.progress(c, func() { c.after.size, c.after.used, c.after.free = size, used, free })
	}
	return nil
}

// removeImage removes each of an image's tags (the last takes the image),
// or an untagged image by ID. Removing a tagged image by ID would need
// force once it has two tags, and force is never used.
func removeImage(ctx context.Context, host Host, im Image) error {
	if !tagged(im) {
		return host.RemoveImage(ctx, im.ID)
	}
	// Docker untags a tag that isn't an image's last without asking whether
	// a container uses the image: with several tags, ask first, or a
	// container started since the read would leave it half untagged.
	if len(refsOf(Image{Tags: im.Tags})) > 1 {
		used, err := host.ImageUsed(ctx, im.ID)
		if err != nil {
			return err
		}
		if used {
			return ErrInUse
		}
	}
	for _, t := range im.Tags {
		if t == "" || t == "<none>:<none>" {
			continue
		}
		if err := host.RemoveImage(ctx, t); err != nil {
			return err
		}
	}
	return nil
}

func firstRef(im Image) string {
	if refs := refsOf(im); len(refs) > 0 {
		return refs[0]
	}
	return im.ID
}

// RemoveVolume deletes one volume the last read offered, after checking
// again that no service owns it.
func (s *Service) RemoveVolume(ctx context.Context, h HostRow, actorID, name string) error {
	s.mu.Lock()
	busy := s.running[h.ID] != nil
	s.mu.Unlock()
	if busy {
		return ErrCleanRunning
	}
	r, err := s.Report(ctx, h, false)
	if err != nil {
		return err
	}
	var listed *OrphanVolume
	for i := range r.Volumes {
		if r.Volumes[i].Name == name {
			listed = &r.Volumes[i]
		}
	}
	if listed == nil {
		return ErrVolumeNotRemovable
	}
	host, err := s.open(ctx, h.ID)
	if err != nil {
		return err
	}
	defer host.Close()
	v, err := host.InspectVolume(ctx, name)
	if err != nil {
		return errors.Join(ErrUnreachable, err)
	}
	prefixes, err := s.repo.ServicePrefixes(ctx)
	if err != nil {
		return err
	}
	if _, _, ok := orphan(v, prefixes); !ok {
		return ErrVolumeNotRemovable
	}
	if err := host.RemoveVolume(ctx, name); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.cache, h.ID)
	s.mu.Unlock()
	s.audit.Audit(ctx, h.ID, h.OrgID, actorID, auditAction(h.ID, "volume_removed"), map[string]any{"name": name, "bytes": listed.Bytes, "kind": listed.Kind})
	return nil
}

// Policy is the host's policy with its defaults.
func (s *Service) Policy(ctx context.Context, h HostRow) PolicyView {
	return h.Policy.view(h.ID == "", s.size(ctx, h.ID))
}

// size is the host's disk size from the last read, else read now; 0 when
// it can't be read.
func (s *Service) size(ctx context.Context, serverID string) int64 {
	s.mu.Lock()
	c, ok := s.cache[serverID]
	s.mu.Unlock()
	if ok {
		return c.report.SizeBytes
	}
	host, err := s.open(ctx, serverID)
	if err != nil {
		return 0
	}
	defer host.Close()
	size, _, _, err := host.Filesystem(ctx)
	if err != nil {
		return 0
	}
	return size
}

// SetPolicy replaces the host's policy.
func (s *Service) SetPolicy(ctx context.Context, h HostRow, actorID string, p Policy) (PolicyView, error) {
	size := s.size(ctx, h.ID)
	if err := p.Validate(h.ID == "", size); err != nil {
		return PolicyView{}, err
	}
	if err := s.repo.SetPolicy(ctx, h.ID, actorID, p); err != nil {
		return PolicyView{}, err
	}
	old := h.Policy.view(h.ID == "", size)
	h.Policy = p
	v := p.view(h.ID == "", size)
	s.audit.Audit(ctx, h.ID, h.OrgID, actorID, auditAction(h.ID, "disk_policy_changed"), map[string]any{
		"old": map[string]any{"build_cache_limit_bytes": old.BuildCacheLimitBytes, "auto_clean": old.AutoClean, "alert_percent": old.AlertPercent},
		"new": map[string]any{"build_cache_limit_bytes": v.BuildCacheLimitBytes, "auto_clean": v.AutoClean, "alert_percent": v.AlertPercent}})
	return v, nil
}

// AfterBuild trims the host's build cache to its limit once a deploy
// there has built; it runs in the background and only logs failures.
func (s *Service) AfterBuild(serverID string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if busy, _ := s.Cleaning(ctx, serverID); busy {
			return // the clean trims it
		}
		h, err := s.repo.Server(ctx, serverID)
		if err != nil {
			s.log.Warn("disk: trim the build cache: read the policy", zap.Error(err), zap.String("server_id", serverID))
			return
		}
		host, err := s.open(ctx, serverID)
		if err != nil {
			s.log.Warn("disk: trim the build cache", zap.Error(err), zap.String("server_id", serverID))
			return
		}
		defer host.Close()
		size, _, _, _ := host.Filesystem(ctx)
		limit := h.Policy.view(serverID == "", size).Effective.BuildCacheLimitBytes
		if _, err := host.PruneBuildCache(ctx, limit, false); err != nil {
			s.log.Warn("disk: trim the build cache", zap.Error(err), zap.String("server_id", serverID))
			return
		}
		s.mu.Lock()
		delete(s.cache, serverID)
		s.mu.Unlock()
	}()
}

// AlertPercents are each host's disk alert threshold that isn't the
// default, by server ID ("" the panel's own).
func (s *Service) AlertPercents(ctx context.Context) (map[string]int, error) {
	out := map[string]int{}
	panel, err := s.repo.Server(ctx, "")
	if err != nil {
		return nil, err
	}
	if p := panel.Policy.AlertPercent; p != nil {
		out[""] = *p
	}
	servers, err := s.repo.Servers(ctx)
	if err != nil {
		return nil, err
	}
	for _, h := range servers {
		if p := h.Policy.AlertPercent; p != nil {
			out[h.ID] = *p
		}
	}
	return out, nil
}

// automaticGap is the least time between two automatic cleans of a host
// started by its disk alert, so a flapping alert doesn't loop.
const automaticGap = time.Hour

// DiskAlertFired cleans the host automatically, in the background, when
// its disk alert fires.
func (s *Service) DiskAlertFired(serverID string) {
	go s.automatic(context.Background(), serverID, true)
}

// automatic runs the host's automatic clean and waits for it; it does
// nothing when the policy turns it off, a clean or a deploy runs, or (for
// the alert) one ran within the hour.
func (s *Service) automatic(ctx context.Context, serverID string, alert bool) {
	ctx, cancel := context.WithTimeout(ctx, cleanTimeout)
	defer cancel()
	h, err := s.repo.Server(ctx, serverID)
	if err != nil {
		s.log.Warn("disk: automatic clean: read the policy", zap.Error(err), zap.String("server_id", serverID))
		return
	}
	auto := h.Policy.view(serverID == "", 0).Effective.AutoClean
	if len(auto) == 0 {
		return
	}
	s.mu.Lock()
	if alert && s.now().Sub(s.lastAuto[serverID]) < automaticGap {
		s.mu.Unlock()
		return
	}
	s.lastAuto[serverID] = s.now()
	s.mu.Unlock()
	c, err := s.begin(ctx, h, "", auto, false, true)
	if err != nil {
		if !errors.Is(err, ErrCleanRunning) && !errors.Is(err, ErrDeployRunning) {
			s.log.Warn("disk: automatic clean", zap.Error(err), zap.String("server_id", serverID))
		}
		return
	}
	s.run(h, c)
}

// RunAutomatic cleans every host automatically once a day, one host at a
// time, until ctx ends.
func (s *Service) RunAutomatic(ctx context.Context) {
	t := time.NewTimer(automaticCleanStartIn)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		servers, err := s.repo.Servers(ctx)
		if err != nil {
			s.log.Warn("disk: automatic clean: list servers", zap.Error(err))
		}
		ids := []string{""}
		for _, h := range servers {
			ids = append(ids, h.ID)
		}
		for _, id := range ids {
			if ctx.Err() != nil {
				return
			}
			s.automatic(ctx, id, false)
		}
		t.Reset(automaticCleanEvery)
	}
}
