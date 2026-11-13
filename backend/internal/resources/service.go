package resources

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/pkg/listversion"
)

const (
	// gracePeriod is how long existing services may deploy without a memory
	// limit after the upgrade.
	gracePeriod = 7 * 24 * time.Hour
	// suggestDays is how many daily rows a suggestion rests on.
	suggestDays = 14
	// keepDays is how long daily rows and memory kills are kept.
	keepDays = 30
	// sourceCompose is service.SourceTypeDockerCompose.
	sourceCompose = "docker-compose"
)

// Service is the memory rule's data (D-41): grace dates, the panel's compose
// limits, the defaults new compose services get, and suggestions.
type Service struct {
	repo     Repository
	services Services
	servers  ServerChecker
	log      *zap.Logger
	now      func() time.Time
	// settled is set once EnsureGrace succeeded: the instance claim is made,
	// so a service without a grace date is one created since, not one the
	// claim has yet to reach.
	settled atomic.Bool
}

func NewService(repo Repository, log *zap.Logger) *Service {
	if log == nil {
		log = zap.NewNop()
	}
	return &Service{repo: repo, log: log, now: time.Now}
}

// Services is what the API reads of the service module. Satisfied by
// *service.ServiceManager.
type Services interface {
	// CheckOwnership says whether a service is in a workspace.
	CheckOwnership(ctx context.Context, id, orgID string) (bool, error)
	ResourceRef(ctx context.Context, id string) (*ServiceRef, error)
	// ResourceRefsOnServer are the workspace's services on a server, as
	// GET /servers/:serverID/services lists them, in one query.
	ResourceRefsOnServer(ctx context.Context, orgID, serverID string) ([]ServiceRef, error)
}

// ServerChecker says whether a server belongs to a workspace. Satisfied by
// *server.Service.
type ServerChecker interface {
	CheckOwnership(ctx context.Context, id, orgID string) (bool, error)
}

// WithLookups gives the API its services and servers.
func (s *Service) WithLookups(services Services, servers ServerChecker) {
	s.services, s.servers = services, servers
}

var (
	// ErrNotFound: the service is not in the caller's workspace (or does not
	// exist; the two are not told apart).
	ErrNotFound = errors.New("service not found")
	// ErrServerNotFound: the server is not the caller's workspace's.
	ErrServerNotFound = errors.New("server not found")
	ErrInvalid        = errors.New("invalid limits")
)

// ServiceRef is what the memory rule reads of a service.
type ServiceRef struct {
	ID, Name, SourceType, MemoryLimit, CPULimit, AddonSlug, DockerImage string
	// Compose is the compose file when it is known (inline content, or a
	// cloned repository's file): its images give each compose service's
	// kind, for the suggestion. nil otherwise.
	Compose *composespec.Source
}

// DefaultAdded is the default memory limit a compose service was given.
type DefaultAdded struct {
	MemoryMB int64
	Kind     Kind
}

// EnsureGrace gives existing services 7 days to get a memory limit: once
// per instance, on the first start with this release. A restart does not
// extend the dates.
func (s *Service) EnsureGrace(ctx context.Context) error {
	until := graceEnd(s.now())
	claimed, n, err := s.repo.ClaimGrace(ctx, until)
	if err != nil {
		return fmt.Errorf("set the memory limit grace dates: %w", err)
	}
	s.settled.Store(true)
	if claimed {
		s.log.Info("memory limits are required on deploys from the grace date on",
			zap.Time("grace_until", until), zap.Int64("services", n))
	}
	return nil
}

// RetryGrace retries a failed EnsureGrace every interval until it succeeds
// or ctx ends. A fresh local stack starts the backend before `make migrate`,
// so the attempts at start can fail. The caller logs the first failure at
// Error; retries log at Warn. Until one succeeds, MemoryCheckFor gives every
// service without a grace date the provisional one.
func (s *Service) RetryGrace(ctx context.Context, every time.Duration) {
	for attempt := 2; ; attempt++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
		err := s.EnsureGrace(ctx)
		if err == nil || ctx.Err() != nil {
			return
		}
		s.log.Warn("memory limit grace dates still not set; retrying", zap.Int("attempt", attempt), zap.Error(err))
	}
}

// RecordOOMKill stores a container of a service killed for memory; false
// when that kill (service, container, time) was already stored, as the
// collector's event windows overlap. Satisfies metrics.Usage.
func (s *Service) RecordOOMKill(ctx context.Context, serviceID, composeService, container string, at time.Time) (bool, error) {
	stored, err := s.repo.InsertOOMKill(ctx, serviceID, composeService, container, at)
	if err != nil {
		return false, fmt.Errorf("record memory kill: %w", err)
	}
	return stored, nil
}

// RecordServerCPUWait adds a sample of a server's CPU waiting to its UTC
// day. Satisfies metrics.Usage.
func (s *Service) RecordServerCPUWait(ctx context.Context, serverID string, day time.Time, pct float64) error {
	if err := s.repo.UpsertServerCPUWait(ctx, serverID, day, pct); err != nil {
		return fmt.Errorf("record server CPU waiting: %w", err)
	}
	return nil
}

// SummariseDays recomputes yesterday's and today's daily rows (UTC days)
// from the samples and kills. Reruns overwrite, so a restart or a missed
// hour corrects itself on the next run. Satisfies metrics.Usage.
func (s *Service) SummariseDays(ctx context.Context, now time.Time) error {
	today := now.UTC().Truncate(24 * time.Hour)
	if err := s.repo.SummariseUsage(ctx, today.AddDate(0, 0, -1), today.AddDate(0, 0, 1)); err != nil {
		return fmt.Errorf("summarise usage: %w", err)
	}
	return nil
}

// GC deletes daily rows and memory kills older than 30 days, counted from
// midnight UTC so that a day kept keeps its kills, and latency minutes
// older than 48 hours. Satisfies metrics.Usage.
func (s *Service) GC(ctx context.Context, now time.Time) error {
	before := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -keepDays)
	if err := s.repo.DeleteUsageBefore(ctx, before); err != nil {
		return fmt.Errorf("delete old usage: %w", err)
	}
	if err := s.repo.DeleteLatencyBefore(ctx, now.Add(-keepLatency)); err != nil {
		return fmt.Errorf("delete old latency: %w", err)
	}
	return nil
}

// ComposeLimits are the panel's limits per compose service.
func (s *Service) ComposeLimits(ctx context.Context, serviceID string) (map[string]ComposeLimit, error) {
	limits, err := s.repo.ComposeLimits(ctx, serviceID)
	if err != nil {
		return nil, fmt.Errorf("compose limits: %w", err)
	}
	return limits, nil
}

// MemoryCheckFor is the memory rule's input for a save or deploy of ref:
// its grace date, its limit (a compose stack's: the panel's, which the
// deploy policy merges with the file's), and the limit to offer where one
// may be missing. Until EnsureGrace succeeded, a service without a grace
// date gets the one the claim will set (provisional), so a save or deploy in
// the upgrade window warns instead of refusing.
func (s *Service) MemoryCheckFor(ctx context.Context, ref ServiceRef) (*MemoryCheck, error) {
	grace, err := s.grace(ctx, ref.ID)
	if err != nil {
		return nil, err
	}
	usage, err := s.repo.UsageDaily(ctx, ref.ID, usageSince(s.now()))
	if err != nil {
		return nil, fmt.Errorf("usage: %w", err)
	}
	c := &MemoryCheck{GraceUntil: grace, Suggested: map[string]SuggestedLimit{}}
	if ref.SourceType != sourceCompose {
		c.SingleLimit = ref.MemoryLimit
		c.Suggested[""] = s.suggestion(usage[""], DefaultMemoryMB(KindOf(ref.AddonSlug, ref.DockerImage), ref.AddonSlug, ref.DockerImage))
		return c, nil
	}
	panel, err := s.ComposeLimits(ctx, ref.ID)
	if err != nil {
		return nil, err
	}
	c.Compose = true
	c.ComposeServices = make(map[string]ComposeLimitSource, len(panel))
	for name, l := range panel {
		c.ComposeServices[name] = ComposeLimitSource{PanelMB: l.MemoryMB}
	}
	if ref.Compose != nil {
		for name, image := range composeImages(ctx, *ref.Compose) {
			if _, set := panel[name]; !set {
				c.Suggested[name] = s.suggestion(usage[name], DefaultMemoryMB(KindOf("", image), "", image))
			}
		}
	}
	return c, nil
}

// grace is a service's grace date or, until EnsureGrace succeeded, the
// provisional one the claim will set when it has none.
func (s *Service) grace(ctx context.Context, serviceID string) (*time.Time, error) {
	grace, err := s.repo.GraceUntil(ctx, serviceID)
	if err != nil {
		return nil, fmt.Errorf("grace date: %w", err)
	}
	if grace == nil && !s.settled.Load() {
		provisional := graceEnd(s.now())
		grace = &provisional
	}
	return grace, nil
}

// graceEnd is the next UTC midnight on or after now + gracePeriod, so the
// date a warning gives ("required from 20 Oct") is the moment it applies.
func graceEnd(now time.Time) time.Time {
	end := now.UTC().Add(gracePeriod)
	if day := end.Truncate(24 * time.Hour); !day.Equal(end) {
		return day.Add(24 * time.Hour)
	}
	return end
}

// usageSince is the first UTC day of the 14 a suggestion rests on, today
// included.
func usageSince(now time.Time) time.Time {
	return now.UTC().Truncate(24*time.Hour).AddDate(0, 0, 1-suggestDays)
}

// suggestion is the limit offered to a service without one: from its usage,
// or its default (defaultMB) without data.
func (s *Service) suggestion(days []DailyUsage, defaultMB int64) SuggestedLimit {
	if sg := SuggestMemory(days, 0, s.now()); sg != nil {
		return SuggestedLimit{Value: sg.Value, FromUsage: true}
	}
	return SuggestedLimit{Value: FormatMB(defaultMB)}
}

// AddComposeDefaults gives each compose service in src that has no memory
// limit, in the file or the panel, its kind's default as a panel limit, and
// returns what it added. A stack with a grace date (it existed before the
// upgrade) gets none: the rule warns, then refuses, as for other services.
// Neither does any stack before EnsureGrace succeeded: without the claim, no
// grace date does not mean a new stack.
// Names the compose override cannot carry (composespec.ValidServiceName) get
// none either: the limit would never be applied, yet satisfy the rule.
func (s *Service) AddComposeDefaults(ctx context.Context, serviceID string, src composespec.Source) (map[string]DefaultAdded, error) {
	if !s.settled.Load() {
		return nil, nil
	}
	grace, err := s.repo.GraceUntil(ctx, serviceID)
	if err != nil {
		return nil, fmt.Errorf("grace date: %w", err)
	}
	if grace != nil {
		return nil, nil
	}
	added := composeDefaults(ctx, src)
	if len(added) == 0 {
		return nil, nil
	}
	panel, err := s.ComposeLimits(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	defaults := map[string]int64{}
	for name, d := range added {
		if _, set := panel[name]; set {
			delete(added, name)
			continue
		}
		defaults[name] = d.MemoryMB
	}
	if len(defaults) == 0 {
		return nil, nil
	}
	if err := s.repo.AddDefaultComposeLimits(ctx, serviceID, defaults); err != nil {
		return nil, fmt.Errorf("add default compose limits: %w", err)
	}
	return added, nil
}

// NewComposeDefaults are the limits AddComposeDefaults will give src as a
// new stack's, before the stack is created: a new stack has no grace date
// and no panel limits yet.
func (s *Service) NewComposeDefaults(ctx context.Context, src composespec.Source) map[string]DefaultAdded {
	if !s.settled.Load() {
		return nil
	}
	return composeDefaults(ctx, src)
}

// composeDefaults are the defaults of src's compose services without a
// memory limit in the file; nil when the file cannot be loaded (the check
// reports it).
func composeDefaults(ctx context.Context, src composespec.Source) map[string]DefaultAdded {
	_, _, fileMB, _ := composespec.InspectLimits(ctx, src)
	if len(fileMB) == 0 {
		return nil
	}
	images := composeImages(ctx, src)
	added := map[string]DefaultAdded{}
	for name, mb := range fileMB {
		if mb > 0 || !composespec.ValidServiceName(name) {
			continue
		}
		k := KindOf("", images[name])
		added[name] = DefaultAdded{MemoryMB: DefaultMemoryMB(k, "", images[name]), Kind: k}
	}
	return added
}

// composeImages are the images of the compose file's services, profiles
// included; nil when the file cannot be loaded.
func composeImages(ctx context.Context, src composespec.Source) map[string]string {
	p, _, err := composespec.Load(ctx, src)
	if err != nil || p == nil {
		return nil
	}
	out := make(map[string]string, len(p.Services)+len(p.DisabledServices))
	for name, svc := range p.Services {
		out[name] = svc.Image
	}
	for name, svc := range p.DisabledServices {
		out[name] = svc.Image
	}
	return out
}

// owned is a service of the workspace, or ErrNotFound.
func (s *Service) owned(ctx context.Context, orgID, serviceID string) (*ServiceRef, error) {
	if ok, err := s.services.CheckOwnership(ctx, serviceID, orgID); err != nil || !ok {
		return nil, ErrNotFound
	}
	ref, err := s.services.ResourceRef(ctx, serviceID)
	if err != nil {
		return nil, ErrNotFound
	}
	return ref, nil
}

// ServiceResources are a service of the workspace's limits, the last 14
// days' usage and the suggestions, per compose service.
func (s *Service) ServiceResources(ctx context.Context, orgID, serviceID string) (*ServiceResources, error) {
	ref, err := s.owned(ctx, orgID, serviceID)
	if err != nil {
		return nil, err
	}
	return s.serviceResources(ctx, *ref)
}

func (s *Service) serviceResources(ctx context.Context, ref ServiceRef) (*ServiceResources, error) {
	now := s.now().UTC()
	grace, err := s.grace(ctx, ref.ID)
	if err != nil {
		return nil, err
	}
	if grace != nil {
		utc := grace.UTC()
		grace = &utc
	}
	usage, err := s.repo.UsageDailyFor(ctx, []string{ref.ID}, usageSince(now))
	if err != nil {
		return nil, fmt.Errorf("usage: %w", err)
	}
	panel, err := s.repo.ComposeLimitsFor(ctx, []string{ref.ID})
	if err != nil {
		return nil, fmt.Errorf("compose limits: %w", err)
	}
	kills, err := s.repo.OOMKillsSince(ctx, ref.ID, lastWeekStart(now))
	if err != nil {
		return nil, fmt.Errorf("memory kills: %w", err)
	}
	in := rowInputs{usage: usage[ref.ID], panel: panel[ref.ID], kills: kills,
		offerDefault: grace == nil || !now.Before(*grace)}
	if ref.SourceType == sourceCompose && ref.Compose != nil {
		_, _, in.fileMB, _ = composespec.InspectLimits(ctx, *ref.Compose)
		in.images = composeImages(ctx, *ref.Compose)
	}
	rows := rowsFor(ref, in, now)
	s.addRestarts(ctx, ref.ID, rows, now)
	return &ServiceResources{version: versionOf(panel[ref.ID]), GraceUntil: grace, Rows: rows, Latency24h: s.latency24h(ctx, ref.ID, now)}, nil
}

// addRestarts puts each row's restarts over the 24 hours before now on it.
// A failure to read them leaves them unset, as not measured: the limits
// and usage still answer.
func (s *Service) addRestarts(ctx context.Context, serviceID string, rows []ResourceRow, now time.Time) {
	byCompose, err := s.repo.RestartsSince(ctx, serviceID, now.Add(-24*time.Hour))
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("count a service's restarts", zap.String("service_id", serviceID), zap.Error(err))
		}
		return
	}
	for i := range rows {
		r, ok := byCompose[rows[i].ComposeService]
		if !ok || r.Samples == 0 {
			continue // no sample carries a count: not measured
		}
		n := r.Restarts
		rows[i].Restarts24h = &n
		if !r.Earlier { // the counted samples start inside the 24 hours
			since := r.FirstAt.UTC()
			rows[i].RestartsSince = &since
		}
	}
}

const (
	// maxCPUCores is the highest CPU limit the panel takes for a compose service.
	maxCPUCores = 64
	// maxComposeLimits is the most compose services one PUT may set.
	maxComposeLimits = 100
)

// SetComposeLimits makes limits a compose stack of the workspace's panel
// limits, replacing the set if version is still what is stored
// (listversion.ErrRequired, listversion.ErrChanged): a compose service left
// out goes back to its file's limit. It returns the stack's resources as
// ServiceResources does.
func (s *Service) SetComposeLimits(ctx context.Context, orgID, serviceID, version string, limits map[string]ComposeLimit) (*ServiceResources, error) {
	ref, err := s.owned(ctx, orgID, serviceID)
	if err != nil {
		return nil, err
	}
	if version == "" {
		return nil, listversion.ErrRequired
	}
	if ref.SourceType != sourceCompose {
		return nil, fmt.Errorf("%w: limits per compose service are for compose stacks; set this service's memory limit in its settings", ErrInvalid)
	}
	if limits == nil {
		return nil, fmt.Errorf("%w: limits is required (an empty object clears them)", ErrInvalid)
	}
	if len(limits) > maxComposeLimits {
		return nil, fmt.Errorf("%w: at most %d compose services", ErrInvalid, maxComposeLimits)
	}
	for _, name := range slices.Sorted(maps.Keys(limits)) {
		l := limits[name]
		switch {
		case !composespec.ValidServiceName(name):
			return nil, fmt.Errorf("%w: %q is not a valid compose service name", ErrInvalid, name)
		case l.MemoryMB < minMemoryMB || l.MemoryMB > maxMB:
			return nil, fmt.Errorf("%w: %s: memory_mb must be from %d to %d", ErrInvalid, name, minMemoryMB, maxMB)
		case l.CPUCores != nil && (*l.CPUCores <= 0 || *l.CPUCores > maxCPUCores):
			return nil, fmt.Errorf("%w: %s: cpu_cores must be above 0 and at most %d, or null", ErrInvalid, name, maxCPUCores)
		}
	}
	if err := s.repo.ReplaceComposeLimits(ctx, ref.ID, version, limits); err != nil {
		return nil, fmt.Errorf("replace compose limits: %w", err)
	}
	return s.serviceResources(ctx, *ref)
}

// ServerResources are the memory suggestions of the services on a server of
// the workspace, the memory following the downward ones would free, and
// yesterday's (UTC) CPU waiting. Usage and panel limits are read for all of
// the services in one query each.
func (s *Service) ServerResources(ctx context.Context, orgID, serverID string) (*ServerResources, error) {
	ok, err := s.servers.CheckOwnership(ctx, serverID, orgID)
	if err != nil {
		return nil, fmt.Errorf("server ownership: %w", err)
	}
	if !ok {
		return nil, ErrServerNotFound
	}
	refs, err := s.services.ResourceRefsOnServer(ctx, orgID, serverID)
	if err != nil {
		return nil, fmt.Errorf("services on the server: %w", err)
	}
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.ID)
	}
	now := s.now().UTC()
	usage, err := s.repo.UsageDailyFor(ctx, ids, usageSince(now))
	if err != nil {
		return nil, fmt.Errorf("usage: %w", err)
	}
	panel, err := s.repo.ComposeLimitsFor(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("compose limits: %w", err)
	}
	out := &ServerResources{Services: make([]ServerSuggestion, 0, len(refs))}
	for _, ref := range refs {
		in := rowInputs{usage: usage[ref.ID], panel: panel[ref.ID]}
		if ref.SourceType == sourceCompose && ref.Compose != nil {
			// ponytail: parses every inline stack's file twice per request; cache by content if a server carries many.
			_, _, in.fileMB, _ = composespec.InspectLimits(ctx, *ref.Compose)
			in.images = composeImages(ctx, *ref.Compose) // the kind
		}
		for _, row := range rowsFor(ref, in, now) {
			sg := row.Memory.Suggestion
			if sg == nil {
				continue
			}
			out.Services = append(out.Services, ServerSuggestion{
				ServiceID: ref.ID, ServiceName: ref.Name, ComposeService: row.ComposeService, Suggestion: *sg,
			})
			if sg.Direction == DirectionDown {
				limit, _ := ParseMB(row.Memory.Limit)
				value, _ := ParseMB(sg.Value)
				out.FreesMB += limit - value
			}
		}
	}
	yesterday := now.Truncate(24*time.Hour).AddDate(0, 0, -1)
	wait, err := s.repo.ServerCPUWait(ctx, serverID, yesterday)
	if err != nil {
		return nil, fmt.Errorf("server CPU waiting: %w", err)
	}
	if wait != nil {
		out.CPUWait = &CPUWaitDay{Day: yesterday.Format(time.DateOnly), AvgPct: round2(*wait)}
	}
	return out, nil
}

// rowInputs is what a service's rows are built from, by compose service
// ("" for a single container).
type rowInputs struct {
	usage  map[string][]DailyUsage
	panel  map[string]ComposeLimit
	kills  map[string]int    // memory kills in the suggestion's week
	fileMB map[string]int64  // the inline compose file's limits; nil when not loaded
	images map[string]string // its images, for the kind
	// offerDefault: a row without a limit or a suggestion from usage offers
	// its kind's default. Only once the rule requires a limit (no grace
	// date, or it has passed); in grace the service is being measured, and
	// the server page never offers it.
	offerDefault bool
}

// rowsFor are a service's rows: one for a single container; for a compose
// stack one per compose service in its file, in the panel's limits or in its
// usage, sorted. The limit a suggestion moves is the one in force: the
// panel's, else the file's.
func rowsFor(ref ServiceRef, in rowInputs, now time.Time) []ResourceRow {
	if ref.SourceType != sourceCompose {
		mb, _ := ParseMB(ref.MemoryLimit)
		cores, err := strconv.ParseFloat(strings.TrimSpace(ref.CPULimit), 64)
		if err != nil || !(cores > 0) || math.IsInf(cores, 1) {
			cores = 0
		}
		k := KindOf(ref.AddonSlug, ref.DockerImage)
		return []ResourceRow{row("", k, DefaultMemoryMB(k, ref.AddonSlug, ref.DockerImage), in.usage[""], in.kills[""], mb, 0, cores, in.offerDefault, now)}
	}
	names := map[string]struct{}{}
	for name := range in.fileMB {
		names[name] = struct{}{}
	}
	for name := range in.panel {
		names[name] = struct{}{}
	}
	for name := range in.usage {
		if name != "" { // a stack's samples from before they were kept per compose service
			names[name] = struct{}{}
		}
	}
	rows := make([]ResourceRow, 0, len(names))
	for _, name := range slices.Sorted(maps.Keys(names)) {
		p := in.panel[name]
		cores := 0.0
		if p.CPUCores != nil {
			cores = *p.CPUCores
		}
		k := KindOf("", in.images[name])
		rows = append(rows, row(name, k, DefaultMemoryMB(k, "", in.images[name]), in.usage[name], in.kills[name], p.MemoryMB, in.fileMB[name], cores, in.offerDefault, now))
	}
	return rows
}

// row is one compose service's, or a single container's, row. panelMB is the
// panel's memory limit (a single container's setting), fileMB the compose
// file's, cores the CPU limit; 0 is none. defaultMB is the default a limit
// without usage data is offered.
func row(name string, kind Kind, defaultMB int64, days []DailyUsage, kills int, panelMB, fileMB int64, cores float64, offerDefault bool, now time.Time) ResourceRow {
	limitMB, source := int64(0), SourceNone
	switch {
	case panelMB > 0:
		limitMB, source = panelMB, SourcePanel
	case fileMB > 0:
		limitMB, source = fileMB, SourceComposeFile
	}
	r := ResourceRow{
		ComposeService: name,
		Kind:           kind,
		Memory: MemoryUsage{
			Limit: mbString(limitMB), Source: source, FileLimit: mbString(fileMB), OOMKills: kills,
			Suggestion: SuggestMemory(days, limitMB, now),
		},
		CPU: CPUUsage{Suggestion: SuggestCPU(days, cores, now)},
	}
	if limitMB == 0 && r.Memory.Suggestion == nil && offerDefault {
		// The refusal's one click: the kind's default until usage is measured.
		r.Memory.Suggestion = &Suggestion{Direction: DirectionSet, Value: FormatMB(defaultMB),
			Reason: fmt.Sprintf("Default for %s; no usage measured yet", kind)}
	}
	if cores > 0 {
		r.CPU.Limit = formatCores(cores)
	}
	yesterday := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	for _, d := range days {
		if d.Samples == 0 {
			continue // a day with kills but no samples: its kills count above, it has no usage
		}
		r.Memory.PeakMB = maxOf(r.Memory.PeakMB, d.MemoryPeakMB)
		r.Memory.Days++
		r.CPU.P95Cores = maxOf(r.CPU.P95Cores, round2(d.CPUP95/100))
		if d.ThrottledMax != nil {
			r.CPU.ThrottledMax = maxOf(r.CPU.ThrottledMax, *d.ThrottledMax)
		}
		if d.Day.Equal(yesterday) {
			r.CPU.WaitAvg = d.CPUWaitAvg
		}
	}
	return r
}

func mbString(mb int64) string {
	if mb <= 0 {
		return ""
	}
	return FormatMB(mb)
}

func maxOf(cur *float64, v float64) *float64 {
	if cur == nil || v > *cur {
		return &v
	}
	return cur
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// DefaultLimit is the memory limit a new service running addonSlug or image
// gets when none is set: what service.withDefaultMemory applies on create.
func (s *Service) DefaultLimit(addonSlug, image string) DefaultLimit {
	k := NewKindOf(addonSlug, image)
	return DefaultLimit{Kind: k, MemoryMB: DefaultMemoryMB(k, addonSlug, image)}
}
