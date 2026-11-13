package preview

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	pactor "github.com/Nickpap21/toft/backend/pkg/actor"
	"go.uber.org/zap"
)

// Store is the previews' rows and what they read of the rest (sqlc).
type Store interface {
	Settings(ctx context.Context, projectID string) (Settings, error)
	SetSettings(ctx context.Context, projectID, environmentID, domain string, max *int) error
	// Pull returns ErrPullGone when the pull request no longer exists.
	Pull(ctx context.Context, pullID string) (Pull, error)
	PullID(ctx context.Context, projectID, repoID string, number int) (string, error)
	// ByPull returns nil, nil when the pull request has no preview.
	ByPull(ctx context.Context, pullID string) (*Preview, error)
	Insert(ctx context.Context, p *Preview) error
	Update(ctx context.Context, p *Preview) error
	SetMigrationFiles(ctx context.Context, id string, files []string, unchecked bool) error
	Delete(ctx context.Context, id string) error
	CountHolding(ctx context.Context, projectID string) (int, error)
	WaitingRoom(ctx context.Context, projectID string) ([]string, error)
	// PullsOf lists the pull requests with a preview: of a source
	// environment, a project, a repository, or every one (scope "").
	PullsOf(ctx context.Context, scope Scope, id string) ([]string, error)
	UnappliedEvents(ctx context.Context, n int) ([]Event, error)
	MarkApplied(ctx context.Context, ids []int64) error
	SourceServices(ctx context.Context, environmentID string) ([]SourceService, error)
	Copies(ctx context.Context, environmentID string) ([]CopyService, error)
	List(ctx context.Context, projectID, cursor string, limit int) ([]Listed, string, error)
	CertificatesSince(ctx context.Context, domain string, since time.Time) (int, error)
	RoomCandidates(ctx context.Context, serverID, orgID string) ([]RoomCandidate, error)
	ApprovalsNeeded(ctx context.Context, orgID string) ([]Approval, error)
}

// Scope is what PullsOf lists previews of.
type Scope string

const (
	ScopeAll         Scope = ""
	ScopeSource      Scope = "source"
	ScopeProject     Scope = "project"
	ScopeRepository  Scope = "repository"
	ScopeWaitingRoom Scope = "waiting_room"
)

// ErrPullGone: the pull request was deleted.
var ErrPullGone = errors.New("the pull request no longer exists")

// Event is an outbox row the previews module hasn't applied.
type Event struct {
	ID              int64
	PullID, ActorID string
}

// Listed is a preview with its pull request, as a project lists them.
type Listed struct {
	Preview
	Number                      int
	Title, Branch, RepoID, Repo string
	EnvironmentName             string
}

// RoomCandidate is one running preview's services on a server.
type RoomCandidate struct {
	PullID     string
	ServiceIDs []string
}

// Approval is a preview waiting for an owner or developer to approve its
// head, for Needs you.
type Approval struct {
	PullID, HeadSHA        string
	Number                 int
	Title, RepoID, Repo    string
	ProjectID, Project     string
	APIKeyName, AuthorName string
	AgentName              string
	UpdatedAt              time.Time
}

// Environments creates and deletes previews' environments (environment
// module).
type Environments interface {
	CreatePreview(ctx context.Context, projectID, name string) (string, error)
	// Delete removes the environment's services from their servers first.
	Delete(ctx context.Context, id string) error
}

// CopyInput is a preview's copy of one service: ExtraMemoryMB is added to
// its memory limit (the original's, else its kind's default).
type CopyInput struct {
	OriginalID, EnvironmentID, Branch string
	ExtraMemoryMB                     int
	Untrusted                         bool
	Hostnames                         []string
}

// Services makes, stops and removes a preview's services (service module).
type Services interface {
	CopyForPreview(ctx context.Context, in CopyInput) (string, error)
	Delete(ctx context.Context, id string) error
	Stop(ctx context.Context, id string) error
}

// DeployInput is one preview service's deploy.
type DeployInput struct {
	ServiceID, SHA, TriggeredBy, Message, ActorID string
	EventID                                       int64
	EnvVars                                       []string
	// CertDomains are the names of the preview's one certificate (the first
	// is its main name); CertResolver is "letsencrypt-dns" for a wildcard.
	CertDomains  []string
	CertResolver string
}

// Deployer deploys a preview's service (deployment module).
type Deployer interface {
	DeployPreview(ctx context.Context, in DeployInput) error
}

// Code is what previews need of the code module.
type Code interface {
	AddEvent(ctx context.Context, pullID, actorID, kind string, data map[string]any)
	ReportPreviewCheck(ctx context.Context, repoID, sha, service, state, description string)
	ReadFile(ctx context.Context, projectID, repoID, sha, path string) ([]byte, error)
	// ChangedFiles are the files head changes since it left base;
	// truncated when there were too many to list them all.
	ChangedFiles(ctx context.Context, projectID, repoID, base, head string) (files []ChangedFile, truncated bool, err error)
}

// Vars reads a service's variables for a deploy (envvar module): all of
// them, or the plain ones (neither secret nor masked).
type Vars interface {
	All(ctx context.Context, serviceID string) ([]string, error)
	Plain(ctx context.Context, serviceID string) ([]string, error)
}

// Hosts is what previews need of servers ("" is the panel's own).
type Hosts interface {
	Address(ctx context.Context, serverID string) (string, error)
	HasDNSProvider(ctx context.Context, serverID string) (bool, error)
	// UntrustedReady is "" when the server runs untrusted previews:
	// Sandboxed and the previews' firewall chain; else why not.
	UntrustedReady(ctx context.Context, serverID string) string
}

// Auditor writes the workspace's audit log.
type Auditor interface {
	WriteAuditLogDetail(ctx context.Context, orgID, actorID, action, resource string, metadata map[string]any)
}

// AgentGate is the workspace's agent policy (D-100, D-102). Satisfied by
// *agent.Service.
type AgentGate interface {
	Check(ctx context.Context, t pactor.Target) error
}

// WithAgentGate puts agents' create_preview under the policy's Previews
// cell (D-102).
func (s *Service) WithAgentGate(g AgentGate) { s.agents = g }

// Service runs previews.
type Service struct {
	agents   AgentGate
	store    Store
	envs     Environments
	services Services
	deployer Deployer
	code     Code
	vars     Vars
	hosts    Hosts
	audit    Auditor
	lookup   func(ctx context.Context, host string) ([]string, error)
	isolated bool
	log      *zap.Logger
	wake     chan struct{}
	now      func() time.Time
	// ponytail: one lock for every preview, so two reconciles of the same
	// pull request never interleave; per-pull locks if previews get busy.
	mu sync.Mutex
}

// NewService returns the previews service. isolated says app networks are
// isolated (TOFT_APP_NETWORKS); previews need them.
func NewService(store Store, envs Environments, services Services, deployer Deployer, code Code, vars Vars, hosts Hosts,
	audit Auditor, lookup func(ctx context.Context, host string) ([]string, error), isolated bool, log *zap.Logger) *Service {
	return &Service{store: store, envs: envs, services: services, deployer: deployer, code: code, vars: vars, hosts: hosts,
		audit: audit, lookup: lookup, isolated: isolated, log: log, wake: make(chan struct{}, 1), now: time.Now}
}

// Nudge wakes Run: the code module wrote an event.
func (s *Service) Nudge() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run applies the outbox's events to previews until ctx ends: at once when
// one is written, and every 30 s. It first reconciles every preview, so a
// restart finishes what it interrupted and removes the previews of pull
// requests closed meanwhile.
func (s *Service) Run(ctx context.Context) {
	s.reconcileAll(ctx)
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		// A full batch means more are waiting (an upgrade's backlog).
		for s.ApplyEvents(ctx) == eventBatch && ctx.Err() == nil {
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-tick.C:
		}
	}
}

func (s *Service) reconcileAll(ctx context.Context) {
	pulls, err := s.store.PullsOf(ctx, ScopeAll, "")
	if err != nil {
		s.log.Error("previews: list at start", zap.Error(err))
		return
	}
	for _, id := range pulls {
		if err := s.reconcile(ctx, id, Actor{}, cause{kind: causeRestart}); err != nil {
			s.log.Warn("previews: reconcile at start", zap.Error(err), zap.String("pull_request_id", id))
		}
	}
}

// ApplyEvents reconciles the previews of the pull requests the unapplied
// events moved, newest event per pull request, and marks them applied. A
// pull request whose reconcile fails keeps its events for the next run. It
// returns how many it marked.
func (s *Service) ApplyEvents(ctx context.Context) int {
	events, err := s.store.UnappliedEvents(ctx, eventBatch)
	if err != nil {
		s.log.Error("previews: read events", zap.Error(err))
		return 0
	}
	if len(events) == 0 {
		return 0
	}
	newest := map[string]Event{}
	order := []string{}
	for _, e := range events {
		if e.PullID == "" {
			continue
		}
		if _, ok := newest[e.PullID]; !ok {
			order = append(order, e.PullID)
		}
		newest[e.PullID] = e
	}
	failed := map[string]bool{}
	for _, pull := range order {
		e := newest[pull]
		if err := s.reconcile(ctx, pull, Actor{UserID: e.ActorID}, cause{kind: causeEvent, eventID: e.ID}); err != nil {
			s.log.Warn("previews: apply an event", zap.Error(err), zap.String("pull_request_id", pull), zap.Int64("event_id", e.ID))
			failed[pull] = true
		}
	}
	ids := make([]int64, 0, len(events))
	for _, e := range events {
		if !failed[e.PullID] {
			ids = append(ids, e.ID)
		}
	}
	if err := s.store.MarkApplied(ctx, ids); err != nil {
		s.log.Error("previews: mark events applied", zap.Error(err))
		return 0
	}
	return len(ids)
}

// eventBatch is how many events ApplyEvents reads at once.
const eventBatch = 200

type causeKind int

const (
	causeEvent causeKind = iota
	causeRestart
	causeStart
	causeApprove
	causeRedeploy
	causeSettings
	causeRemoved // the source environment, the repository or the project goes
)

type cause struct {
	kind    causeKind
	eventID int64
	why     string
}

// reconcile brings a pull request's preview to what its pull request and
// project say now. It is safe to run again: nothing is redone at the
// commit a preview runs.
func (s *Service) reconcile(ctx context.Context, pullID string, by Actor, c cause) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reconcileLocked(ctx, pullID, by, c)
}

func (s *Service) reconcileLocked(ctx context.Context, pullID string, by Actor, c cause) error {
	pr, err := s.store.Pull(ctx, pullID)
	if errors.Is(err, ErrPullGone) {
		return nil
	}
	if err != nil {
		return err
	}
	pv, err := s.store.ByPull(ctx, pullID)
	if err != nil {
		return err
	}
	set, err := s.store.Settings(ctx, pr.ProjectID)
	if err != nil {
		return err
	}
	switch {
	case c.kind == causeRemoved:
		return s.removeIf(ctx, pv, pr, c.why, by)
	case pr.State == "merged":
		return s.removeIf(ctx, pv, pr, "merged", Actor{UserID: pr.MergedBy})
	case pr.State != "open":
		return s.removeIf(ctx, pv, pr, "closed", by)
	case set.EnvironmentID == "":
		return s.removeIf(ctx, pv, pr, "previews off", by)
	case !s.isolated:
		return s.removeIf(ctx, pv, pr, "app networks", by)
	}
	if pv != nil && pv.SourceEnvID != set.EnvironmentID {
		if err := s.remove(ctx, pv, pr, "copies another environment", by); err != nil {
			return err
		}
		pv = nil
	}
	// A pull request that has no preview gets one from an event, a start
	// or a settings change; an approval or a redeploy needs one.
	fresh := pv == nil
	if pv == nil {
		if c.kind != causeEvent && c.kind != causeStart && c.kind != causeSettings {
			return nil
		}
		pv = &Preview{PullID: pr.ID, ProjectID: pr.ProjectID, SourceEnvID: set.EnvironmentID, Untrusted: pr.Untrusted(),
			HeadSHA: pr.HeadSHA, State: StateWaitingApproval}
		if !pv.Untrusted {
			pv.State = StateWaitingRoom
		}
		if err := s.store.Insert(ctx, pv); err != nil {
			return err
		}
	}

	headMoved := pv.HeadSHA != pr.HeadSHA || fresh
	pv.HeadSHA = pr.HeadSHA
	if pr.Untrusted() && !pv.Untrusted {
		// A key pushed to a writer's preview: it starts again, alone and
		// Sandboxed, once an owner or developer approves.
		old := pv.EnvID
		pv.EnvID, pv.Untrusted, pv.ApprovedSHA, pv.ApprovedBy, pv.ApprovedAt = "", true, "", "", nil
		pv.State, pv.StoppedReason = StateWaitingApproval, ""
		// Detached first: deleting its environment would delete the row.
		if err := s.store.Update(ctx, pv); err != nil {
			return err
		}
		if old != "" {
			if err := s.envs.Delete(context.WithoutCancel(ctx), old); err != nil {
				// Attached again, so the next try finds what to remove.
				pv.EnvID = old
				_ = s.store.Update(ctx, pv)
				return fmt.Errorf("remove the preview to sandbox it: %w", err)
			}
		}
		headMoved = true
	}

	target := pv.HeadSHA
	if pv.Untrusted {
		if pv.ApprovedSHA != pv.HeadSHA && headMoved {
			s.waitingChecks(ctx, pr, set, pv)
			s.code.AddEvent(ctx, pr.ID, "", "preview_waiting", map[string]any{"sha": pv.HeadSHA})
		}
		if pv.ApprovedSHA == "" {
			pv.State, pv.Note = StateWaitingApproval, s.untrustedBlock(ctx, set)
			return s.store.Update(ctx, pv)
		}
		target = pv.ApprovedSHA
	}

	// A stopped preview starts again from Start; one stopped for room also
	// from an approval or a push (it fails its check if it still doesn't
	// fit).
	if pv.State == StateStopped && c.kind != causeStart &&
		(pv.StoppedReason == StoppedByHand || c.kind != causeApprove && !(c.kind == causeEvent && headMoved)) {
		return s.store.Update(ctx, pv)
	}

	started := false
	if pv.EnvID == "" {
		n, err := s.store.CountHolding(ctx, pr.ProjectID)
		if err != nil {
			return err
		}
		if n >= set.Max {
			pv.State, pv.Note = StateWaitingRoom, ""
			return s.store.Update(ctx, pv)
		}
		if pv.Untrusted {
			if why := s.untrustedBlock(ctx, set); why != "" {
				pv.Note = why
				return s.store.Update(ctx, pv)
			}
		}
		env, certDomain, blocked, err := s.start(ctx, pr, set, pv)
		if err != nil {
			return err
		}
		if blocked != "" {
			// Not retried: the next push, approval or Start tries again.
			pv.Note = blocked
			return s.store.Update(ctx, pv)
		}
		pv.EnvID, started = env, true
		s.code.AddEvent(ctx, pr.ID, by.UserID, "preview_started", map[string]any{"sha": target, "certificate_domain": certDomain})
		s.auditf(ctx, set.OrgID, by, "preview.started", pr, map[string]any{"sha": target, "untrusted": pv.Untrusted})
		s.noteMigrations(ctx, pr, pv, target)
	}
	pv.State, pv.StoppedReason, pv.Note = StateActive, "", ""
	if err := s.store.Update(ctx, pv); err != nil {
		return err
	}
	deployed, err := s.deployAll(ctx, pr, set, pv, target, by, c)
	if err != nil {
		return err
	}
	if pv.Note != "" {
		// A refused commit has no deployment to say so; view reads the note.
		if err := s.store.Update(ctx, pv); err != nil {
			return fmt.Errorf("keep the preview's refusal: %w", err)
		}
	}
	if deployed && !started {
		s.code.AddEvent(ctx, pr.ID, by.UserID, "preview_updated", map[string]any{"sha": target})
		s.auditf(ctx, set.OrgID, by, "preview.updated", pr, map[string]any{"sha": target})
		s.noteMigrations(ctx, pr, pv, target)
	}
	return nil
}

// noteMigrations keeps the migrations the pull request's diff touches at
// sha, which its preview skips, and names them on its conversation when
// they changed (D-144), and whether the diff was too large to check them
// all (D-147). A diff it can't read leaves what it kept.
func (s *Service) noteMigrations(ctx context.Context, pr Pull, pv *Preview, sha string) {
	changed, truncated, err := s.code.ChangedFiles(ctx, pr.ProjectID, pr.RepoID, pr.Base, sha)
	if err != nil {
		s.log.Warn("previews: read the diff for migrations", zap.Error(err), zap.String("pull_request_id", pr.ID))
		return
	}
	files := MigrationFiles(changed)
	if slices.Equal(files, pv.MigrationFiles) && truncated == pv.MigrationsUnchecked {
		return
	}
	if err := s.store.SetMigrationFiles(ctx, pv.ID, files, truncated); err != nil {
		s.log.Warn("previews: keep the migrations", zap.Error(err), zap.String("pull_request_id", pr.ID))
		return
	}
	filesChanged := !slices.Equal(files, pv.MigrationFiles)
	pv.MigrationFiles, pv.MigrationsUnchecked = files, truncated
	if filesChanged && len(files) > 0 {
		s.code.AddEvent(ctx, pr.ID, "", "preview_migrations_skipped", map[string]any{"sha": sha, "files": files})
	}
}

// removeIf removes pv when there is one.
func (s *Service) removeIf(ctx context.Context, pv *Preview, pr Pull, why string, by Actor) error {
	if pv == nil {
		return nil
	}
	return s.remove(ctx, pv, pr, why, by)
}

// remove takes a preview down: its environment with its services,
// addresses and network (the row goes with the environment), then the
// project's oldest preview waiting for room starts.
func (s *Service) remove(ctx context.Context, pv *Preview, pr Pull, why string, by Actor) error {
	ctx = context.WithoutCancel(ctx)
	copies := []string{}
	if pv.EnvID != "" {
		cs, _ := s.store.Copies(ctx, pv.EnvID)
		for _, c := range cs {
			copies = append(copies, c.Name)
		}
	}
	pv.State = StateRemoving
	if err := s.store.Update(ctx, pv); err != nil {
		return err
	}
	if pv.EnvID != "" {
		if err := s.envs.Delete(ctx, pv.EnvID); err != nil {
			return fmt.Errorf("remove the preview's environment: %w", err)
		}
	} else if err := s.store.Delete(ctx, pv.ID); err != nil {
		return err
	}
	s.code.AddEvent(ctx, pr.ID, by.UserID, "preview_removed", map[string]any{"reason": why, "services": copies})
	set, _ := s.store.Settings(ctx, pr.ProjectID)
	s.auditf(ctx, set.OrgID, by, "preview.removed", pr, map[string]any{"reason": why})
	if waiting, err := s.store.PullsOf(ctx, ScopeWaitingRoom, pr.ProjectID); err == nil && len(waiting) > 0 && why != "previews off" {
		next := waiting[0]
		go func() {
			if err := s.reconcile(context.Background(), next, Actor{}, cause{kind: causeStart}); err != nil {
				s.log.Warn("previews: start one waiting for room", zap.Error(err), zap.String("pull_request_id", next))
			}
		}()
	}
	return nil
}

// untrustedBlock is why an untrusted preview can't start on the servers of
// the environment it copies, or "".
func (s *Service) untrustedBlock(ctx context.Context, set Settings) string {
	sources, err := s.store.SourceServices(ctx, set.EnvironmentID)
	if err != nil {
		return ""
	}
	seen := map[string]bool{}
	for _, src := range sources {
		if seen[src.ServerID] {
			continue
		}
		seen[src.ServerID] = true
		if why := s.hosts.UntrustedReady(ctx, src.ServerID); why != "" {
			return why
		}
	}
	return ""
}

// rebuilt are the services of the copied environment that build from the
// pull request's repository.
func rebuilt(sources []SourceService, repoID string) []SourceService {
	out := make([]SourceService, 0, len(sources))
	for _, src := range sources {
		if src.RepositoryID == repoID {
			out = append(out, src)
		}
	}
	return out
}

// start creates a preview's environment and its copies with their
// addresses. certDomain is the registered domain a certificate was ordered
// under ("" for sslip.io or a wildcard).
//
// blocked is why it can't start, which waits for the next push, approval
// or Start rather than being retried.
func (s *Service) start(ctx context.Context, pr Pull, set Settings, pv *Preview) (envID, certDomain, blocked string, err error) {
	sources, err := s.store.SourceServices(ctx, set.EnvironmentID)
	if err != nil {
		return "", "", "", err
	}
	build := rebuilt(sources, pr.RepoID)
	if len(build) == 0 {
		return "", "", fmt.Sprintf("No service of %s builds from %s.", set.EnvironmentName, pr.RepoName), nil
	}
	if pv.Untrusted {
		for _, src := range build {
			if why := untrustedRefusal(src); why != "" {
				return "", "", why, nil
			}
		}
	}
	names, certDomain := s.addresses(ctx, pr, set, build)
	envID, err = s.envs.CreatePreview(ctx, pr.ProjectID, EnvironmentName(pr.Number, pr.RepoName, pr.FirstRepository))
	if err != nil {
		return "", "", "", err
	}
	for _, src := range build {
		extra := 0
		if pv.Untrusted {
			extra = SandboxMemoryMB
		}
		if _, err := s.services.CopyForPreview(ctx, CopyInput{OriginalID: src.ID, EnvironmentID: envID, Branch: pr.Head,
			ExtraMemoryMB: extra, Untrusted: pv.Untrusted, Hostnames: names[src.ID]}); err != nil {
			_ = s.envs.Delete(context.WithoutCancel(ctx), envID)
			return "", "", "", fmt.Errorf("copy %s: %w", src.Name, err)
		}
	}
	return envID, certDomain, "", nil
}

// untrustedRefusal is why an untrusted preview can't copy src, or "".
func untrustedRefusal(src SourceService) string {
	switch {
	case src.SharedVolumes:
		return src.Name + " mounts a shared volume, which a preview opened with an API key can't reach"
	case strings.Contains(src.Volumes, `"host_path":"/`):
		return src.Name + " mounts a path on its server, which a preview opened with an API key can't reach"
	case src.BuildPack == "nixpacks":
		return src.Name + " builds with Nixpacks, whose cache is shared with every build on the server: a preview opened with an API key needs a Dockerfile"
	}
	return ""
}

// addresses names each rebuilt service that has a domain in the copied
// environment: under the project's preview domain where its wildcard
// record resolves to the service's server, else under sslip.io; past 40
// certificates in 7 days for the domain, under sslip.io too (D-97).
func (s *Service) addresses(ctx context.Context, pr Pull, set Settings, build []SourceService) (map[string][]string, string) {
	out := map[string][]string{}
	certDomain := ""
	useDomain := set.Domain != ""
	if useDomain {
		reg := RegisteredDomain(set.Domain)
		if n, err := s.store.CertificatesSince(ctx, reg, s.now().Add(-7*24*time.Hour)); err == nil && n >= 40 {
			useDomain = false
		} else {
			certDomain = reg
		}
	}
	resolves := map[string]bool{}
	for _, src := range build {
		if len(src.Hostnames) == 0 {
			continue
		}
		ip, err := s.hosts.Address(ctx, src.ServerID)
		if err != nil || ip == "" {
			continue
		}
		if useDomain {
			ok, seen := resolves[src.ServerID]
			if !seen {
				ok = s.resolvesTo(ctx, "toft-check-"+strconv.FormatInt(s.now().UnixNano(), 36)+"."+set.Domain, ip)
				resolves[src.ServerID] = ok
			}
			if ok {
				out[src.ID] = []string{Hostname(set.Domain, src.Name, pr.Number, pr.RepoName, pr.FirstRepository)}
				continue
			}
		}
		out[src.ID] = []string{SslipHostname(ip, src.Name, pr.ProjectName, pr.Number, pr.RepoName, pr.FirstRepository)}
	}
	if !useDomain {
		certDomain = ""
	}
	return out, certDomain
}

// resolvesTo reports whether host resolves to ip.
func (s *Service) resolvesTo(ctx context.Context, host, ip string) bool {
	if s.lookup == nil {
		return false
	}
	lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addrs, err := s.lookup(lctx, host)
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if a == ip {
			return true
		}
	}
	return false
}

// deployAll deploys each of the preview's services that doesn't run target
// yet, or every one for a redeploy. It reports whether it deployed any.
func (s *Service) deployAll(ctx context.Context, pr Pull, set Settings, pv *Preview, target string, by Actor, c cause) (bool, error) {
	copies, err := s.store.Copies(ctx, pv.EnvID)
	if err != nil {
		return false, err
	}
	sources, err := s.store.SourceServices(ctx, set.EnvironmentID)
	if err != nil {
		return false, err
	}
	// Each original's addresses become its copy's in the variables.
	addresses := map[string]string{}
	bySource := map[string]SourceService{}
	for _, src := range sources {
		bySource[src.ID] = src
	}
	names := []string{}
	for _, cp := range copies {
		names = append(names, cp.Hostnames...)
		if src, ok := bySource[cp.PreviewOf]; ok && len(cp.Hostnames) > 0 {
			for _, h := range src.Hostnames {
				addresses[h] = cp.Hostnames[0]
			}
		}
	}
	certNames, resolver := names, ""
	if set.Domain != "" && len(copies) > 0 && len(names) > 0 && strings.HasSuffix(names[0], "."+set.Domain) {
		if ok, err := s.hosts.HasDNSProvider(ctx, copies[0].ServerID); err == nil && ok {
			certNames, resolver = []string{"*." + set.Domain}, "letsencrypt-dns"
		}
	}
	deployed := false
	for _, cp := range copies {
		if c.kind != causeRedeploy && c.kind != causeStart && cp.DeploymentSHA == target && cp.RuntimeStatus != "stopped" &&
			(cp.DeploymentStatus == "running" || cp.DeploymentStatus == "building" || cp.DeploymentStatus == "pending") {
			continue
		}
		// At a start, only what a restart cut is retried; a head that moved
		// while the backend was down (or an older release ran) deploys.
		if c.kind == causeRestart && cp.DeploymentSHA == target && !interrupted(cp) {
			continue
		}
		if pv.Untrusted {
			if why := s.dockerfileRefusal(ctx, pr, bySource[cp.PreviewOf], target); why != "" {
				s.code.ReportPreviewCheck(ctx, pr.RepoID, target, cp.Name, "failure", why)
				pv.Note = why
				continue
			}
		}
		var base []string
		if pv.Untrusted {
			base, err = s.vars.Plain(ctx, cp.PreviewOf)
		} else {
			base, err = s.vars.All(ctx, cp.PreviewOf)
		}
		if err != nil {
			return deployed, fmt.Errorf("read %s's variables: %w", cp.Name, err)
		}
		extra := map[string]string{"TOFT_PULL_REQUEST": strconv.Itoa(pr.Number)}
		if len(cp.Hostnames) > 0 {
			extra["TOFT_PREVIEW_URL"] = "https://" + cp.Hostnames[0]
		}
		who := "Preview · !" + strconv.Itoa(pr.Number)
		if err := s.deployer.DeployPreview(ctx, DeployInput{ServiceID: cp.ID, SHA: target, TriggeredBy: who, Message: pr.Title,
			ActorID: by.UserID, EventID: c.eventID, EnvVars: Variables(base, addresses, extra),
			CertDomains: certNames, CertResolver: resolver}); err != nil {
			s.log.Warn("previews: deploy", zap.Error(err), zap.String("service_id", cp.ID))
			continue
		}
		deployed = true
	}
	return deployed, nil
}

// interrupted reports whether a copy's newest deploy was cut by a restart:
// each start tries it once more.
func interrupted(cp CopyService) bool {
	return cp.DeploymentStatus == "failed" && strings.HasPrefix(cp.DeploymentError, "interrupted")
}

// dockerfileRefusal is why an untrusted preview can't build src at sha:
// its Dockerfile gives RUN steps flags (D-97). A Dockerfile that can't be
// read is the build's to report.
func (s *Service) dockerfileRefusal(ctx context.Context, pr Pull, src SourceService, sha string) string {
	if src.SourceType != "git" {
		return ""
	}
	p := path.Join(strings.TrimPrefix(src.BaseDirectory, "/"), "Dockerfile")
	b, err := s.code.ReadFile(ctx, pr.ProjectID, pr.RepoID, sha, p)
	if err != nil {
		return ""
	}
	if flags := RunFlags(b); len(flags) > 0 {
		return fmt.Sprintf("%s gives RUN steps %s, which a preview opened with an API key can't use: they reach past the build", p, strings.Join(uniq(flags), ", "))
	}
	return ""
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// waitingChecks marks each service a preview would build as waiting for
// approval on the pull request's head.
func (s *Service) waitingChecks(ctx context.Context, pr Pull, set Settings, pv *Preview) {
	sources, err := s.store.SourceServices(ctx, set.EnvironmentID)
	if err != nil {
		return
	}
	for _, src := range rebuilt(sources, pr.RepoID) {
		s.code.ReportPreviewCheck(ctx, pr.RepoID, pv.HeadSHA, src.Name, "pending", "Waiting for an owner or developer to approve this commit")
	}
}

func (s *Service) auditf(ctx context.Context, orgID string, by Actor, action string, pr Pull, meta map[string]any) {
	if s.audit == nil || by.UserID == "" || orgID == "" {
		return
	}
	meta["pull_request"] = pr.Number
	meta["repository"] = pr.RepoName
	s.audit.WriteAuditLogDetail(ctx, orgID, by.UserID, action, "pull_request:"+pr.ID, meta)
}
