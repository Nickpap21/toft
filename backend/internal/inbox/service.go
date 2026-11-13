package inbox

import (
	"context"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/internal/agent"
	"github.com/Nickpap21/toft/backend/internal/catalog"
	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"github.com/Nickpap21/toft/backend/internal/instance"
	"github.com/Nickpap21/toft/backend/internal/resources"
	"github.com/Nickpap21/toft/backend/pkg/broker"
	"github.com/compose-spec/compose-go/v2/types"
	"go.uber.org/zap"
)

// StalePage is a page out of date that a person wrote or edited.
type StalePage struct {
	ID, ProjectID, Project, Title string
	Since                         time.Time // its last verification, else its creation
	Change                        string
}

// Pages finds the out-of-date pages a person wrote or edited. Satisfied by
// *doc.Service.
type Pages interface {
	StalePagesFor(ctx context.Context, orgID, userID string) ([]StalePage, error)
}

// Instance says who runs the instance, how its last upgrade went and
// whether its stored recovery kit still restores it.
// Satisfied by *instance.Service.
type Instance interface {
	IsOperator(ctx context.Context, userID string) (bool, error)
	UpgradeStatus(ctx context.Context, actorID string) (*instance.UpgradeStatus, error)
	WatchdogStatus(ctx context.Context, actorID string) (*instance.Watchdog, error)
	Setup(ctx context.Context, actorID string) (*instance.Setup, error)
}

// Publisher tells a workspace's open tabs; Satisfied by *broker.Broker.
type Publisher interface {
	Publish(orgID string, e broker.Event)
}

// Service is the inbox (D-88).
type Service struct {
	repo     Repository
	pages    Pages
	instance Instance
	previews Previews
	agents   AgentApprovals
	pub      Publisher
	log      *zap.Logger
	every    time.Duration
}

// PreviewApproval is a preview waiting for an owner or developer to approve
// its pull request's head (D-97).
type PreviewApproval struct {
	PullID, SHA, RepositoryID, Repository string
	Number                                int
	Title, ProjectID, Project             string
	APIKeyName, AuthorName                string
	AgentName                             string
	At                                    time.Time
}

// Previews lists a workspace's previews waiting for approval. Satisfied via
// main.go by *preview.Service.
type Previews interface {
	ApprovalsNeeded(ctx context.Context, orgID string) ([]PreviewApproval, error)
}

// AgentApproval is an agent's request waiting for a person (D-100).
// Satisfied, as a list, by *agent.Service.
type AgentApprovals interface {
	Waiting(ctx context.Context, orgID string) ([]agent.Waiting, error)
}

// WithAgentApprovals puts agents' requests waiting for a person in Needs you.
func (s *Service) WithAgentApprovals(a AgentApprovals) { s.agents = a }

// WithPreviews puts previews waiting for approval in Needs you.
func (s *Service) WithPreviews(p Previews) { s.previews = p }

func NewService(repo Repository, pages Pages, inst Instance, pub Publisher, log *zap.Logger) *Service {
	return &Service{repo: repo, pages: pages, instance: inst, pub: pub, log: log, every: 5 * time.Second}
}

// ─── Needs you ───────────────────────────────────────────────────────────────

// NeedsYou is what needs the person now; MarksDone lists what they marked
// done that still holds, MarksAny all of it, marked or not, for a screen
// that shows state.
func (s *Service) NeedsYou(ctx context.Context, orgID, userID, role string, which Marks) ([]Item, error) {
	items := []Item{}
	var unbacked []UnbackedService
	if role == "owner" || role == "developer" {
		var err error
		if unbacked, err = s.repo.UnbackedServices(ctx, orgID); err != nil {
			return nil, err
		}
		failed, err := s.repo.FailedDeploys(ctx, orgID)
		if err != nil {
			return nil, err
		}
		alerts, err := s.repo.OpenAlerts(ctx, orgID)
		if err != nil {
			return nil, err
		}
		items = append(append(items, failed...), alerts...)
		public, err := s.repo.UnprotectedPublic(ctx, orgID)
		if err != nil {
			return nil, err
		}
		items = append(items, exposedTools(public)...)
		logins, err := s.repo.TemporaryLogins(ctx, orgID, setupLoginSlugs())
		if err != nil {
			return nil, err
		}
		items = append(items, temporaryLogins(logins)...)
		https, err := s.repo.HTTPSRedeploys(ctx, orgID)
		if err != nil {
			return nil, err
		}
		items = append(items, https...)
		if s.previews != nil {
			approvals, err := s.previews.ApprovalsNeeded(ctx, orgID)
			if err != nil {
				return nil, err
			}
			for _, a := range approvals {
				pid := a.ProjectID
				who := a.AuthorName
				if a.AgentName != "" {
					who = a.AgentName + " for " + a.AuthorName
				} else if a.APIKeyName != "" {
					who = a.APIKeyName + " (" + a.AuthorName + "’s API key)"
				}
				items = append(items, Item{Key: "preview:" + a.PullID + ":" + a.SHA, Kind: "preview_approval", Category: "code",
					Title: a.Title, Detail: a.Repository + " !" + strconv.Itoa(a.Number) + " · " + who + " · " + shortSHA(a.SHA) +
						" · runs Sandboxed, no secrets", At: a.At, ProjectID: &pid, Project: a.Project,
					RepositoryID: a.RepositoryID, Repository: a.Repository, PullNumber: a.Number, SHA: a.SHA})
			}
		}
	}
	if (role == "owner" || role == "developer") && s.agents != nil {
		waiting, err := s.agents.Waiting(ctx, orgID)
		if err != nil {
			return nil, err
		}
		for _, w := range waiting {
			exp := w.ExpiresAt
			sid := ""
			if w.ServiceID != nil {
				sid = *w.ServiceID
			}
			items = append(items, Item{Key: "agent:" + w.ID, Kind: "agent_approval", Category: "deploys",
				Title: agentAsks[w.Tool] + w.Target, Detail: w.AgentName + " for " + w.UserName + " · “" + w.Reason + "”", At: w.CreatedAt,
				ServiceID: sid, ApprovalID: w.ID, ExpiresAt: &exp})
		}
	}
	assigned, err := s.repo.AssignedUrgent(ctx, orgID, userID)
	if err != nil {
		return nil, err
	}
	items = append(items, assigned...)
	due, err := s.repo.AssignedDue(ctx, orgID, userID)
	if err != nil {
		return nil, err
	}
	items = append(items, due...)
	if s.pages != nil {
		stale, err := s.pages.StalePagesFor(ctx, orgID, userID)
		if err != nil {
			return nil, err
		}
		for _, p := range stale {
			pid := p.ProjectID
			items = append(items, Item{Key: "stale:" + p.ID + ":" + strconv.FormatInt(p.Since.Unix(), 10), Kind: "stale_page", Category: "docs",
				Title: p.Title, Detail: p.Change, At: p.Since, ProjectID: &pid, Project: p.Project, PageID: p.ID})
		}
	}
	toftDB := false
	if s.instance != nil {
		if op, err := s.instance.IsOperator(ctx, userID); err == nil && op {
			backed, err := s.repo.PanelHostBackedUp(ctx)
			if err != nil {
				return nil, err
			}
			toftDB = !backed
			if st, err := s.instance.UpgradeStatus(ctx, userID); err == nil {
				items = append(items, upgradeFailed(st)...)
			}
			if w, err := s.instance.WatchdogStatus(ctx, userID); err == nil && w.URL == "" {
				items = append(items, Item{Key: "no_watchdog", Kind: "no_watchdog", Category: "servers",
					Title: "The panel host", Detail: "Nobody hears if this server goes down", At: time.Now()})
			}
			// Keyed by the kit it is about: putting it off doesn't cover the
			// next kit that goes out of date (D-163).
			if st, err := s.instance.Setup(ctx, userID); err == nil && st.KitOutOfDate && st.KitAcknowledgedAt != nil {
				items = append(items, Item{Key: "kit_out_of_date:" + strconv.FormatInt(st.KitAcknowledgedAt.Unix(), 10), Kind: "kit_out_of_date", Category: "servers",
					Title: "This panel", Detail: "The encryption key or the panel host’s backup settings changed after the recovery kit was stored. A restore with that kit fails.", At: time.Now()})
			}
		}
	}
	items = append(items, noBackups(ctx, unbacked, toftDB)...)
	marked, err := s.repo.DoneKeys(ctx, userID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]Item, 0, len(items))
	for _, it := range items {
		at, isMarked := marked[it.Key]
		it.Snoozes = snoozes(it.Kind)
		if isMarked && it.Snoozes {
			if until := at.Add(snoozeFor); now.Before(until) {
				it.SnoozedUntil = &until
			} else {
				isMarked = false
			}
		}
		if which == MarksAny || isMarked == (which == MarksDone) {
			it.Severity = severityOf(it)
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := severities[out[i].Severity], severities[out[j].Severity]; a != b {
			return a < b
		}
		return out[i].At.After(out[j].At)
	})
	return out, nil
}

// upgradeFailed is the panel's last upgrade when it rolled back or stopped:
// it stays until a later upgrade's run replaces it, or it's marked done.
func upgradeFailed(st *instance.UpgradeStatus) []Item {
	r := st.Run
	if r == nil || r.FinishedAt == nil || (r.Outcome != "rolled_back" && r.Outcome != "failed") {
		return nil
	}
	title, detail := "Upgrade to "+r.To+" failed and rolled back", r.From+" is running again"
	if r.Outcome == "failed" {
		title, detail = "Upgrade to "+r.To+" failed", r.From+" kept running"
	}
	return []Item{{Key: "upgrade_failed:" + r.To + ":" + strconv.FormatInt(r.StartedAt.Unix(), 10), Kind: "upgrade_failed", Category: "servers",
		Title: title, Detail: detail, Error: r.Error, At: *r.FinishedAt}}
}

// agentAsks starts a waiting request's title with what it asks for, in the
// words of its page. Home splits this off the title (home.ts): change both.
var agentAsks = map[string]string{
	"deploy": "Deploy ", "rollback": "Roll back ", "set_secret": "Set a secret on ", "set_env_var": "Set a variable on ",
	"run_runbook_step": "Run a runbook step in ", "create_branch": "Make a branch in ", "open_pr": "Open a pull request in ",
	"create_issue": "Open an issue in ", "comment": "Comment in ", "create_preview": "Start a preview in ",
}

// exposedTools are the admin tools among public services (phpMyAdmin,
// Adminer, pgAdmin, MailHog, RedisInsight: resources.NewKindOf, as the
// service's kind): anyone who finds the address reaches a tool that holds
// databases' passwords or mail.
func exposedTools(public []PublicService) []Item {
	out := []Item{}
	for _, p := range public {
		if resources.NewKindOf(p.AddonSlug, p.Image) != resources.KindTool {
			continue
		}
		title := p.Name
		if p.Project != "" {
			title = p.Project + " / " + p.Name
		}
		where := p.Hostname
		if p.Environment != "" {
			where += " · " + p.Environment
		}
		out = append(out, Item{Key: "exposed_tool:" + p.ID, Kind: "exposed_tool", Category: "deploys", Title: title,
			Detail: "Admin tool open to anyone at " + where + ": no basic auth or IP allowlist", At: p.Since,
			ProjectID: p.ProjectID, Project: p.Project, ServiceID: p.ID})
	}
	return out
}

// setupLoginSlugs are the catalog's apps whoever opens first becomes the
// admin of (catalog.Template.SetupLogin).
func setupLoginSlugs() []string {
	var out []string
	for _, t := range catalog.All() {
		if t.SetupLogin {
			out = append(out, t.Slug)
		}
	}
	return out
}

// temporaryLogins are those apps with the basic auth still on that Toft put
// in front of them at creation: once the person has set up the app's admin
// account, they turn it off (marked done, it comes back in a week: snoozeFor).
func temporaryLogins(svcs []PublicService) []Item {
	out := []Item{}
	for _, p := range svcs {
		app := p.AddonSlug
		if t, ok := catalog.Get(p.AddonSlug); ok {
			app = t.Name
		}
		where := app
		if p.Project != "" {
			where = p.Project + " · " + app
		}
		if p.Environment != "" {
			where += " · " + p.Environment
		}
		out = append(out, Item{Key: "setup_login:" + p.ID, Kind: "setup_login", Category: "deploys",
			Title:  "Set up " + p.Name + "’s admin account, then turn off its temporary login",
			Detail: where + ": whoever opens it first becomes its admin; the temporary login keeps others out until then",
			At:     p.Since, ProjectID: p.ProjectID, Project: p.Project, ServiceID: p.ID})
	}
	return out
}

// noBackups are the servers nothing backs up that hold data (D-43): the
// databases among the workspace's services there (resources.KindOf:
// Postgres, MySQL, MariaDB, MongoDB), and the other services' volumes and
// the shared volumes they mount, so a cache counts by its volumes; an
// inline compose stack counts the same way inside its file (composeData),
// and one whose file is in a repository counts as a stack. toftDB adds the
// panel host, for an operator, while Toft's own database has no backup.
// The key holds what is at risk: marked done, the row comes back when that
// changes, and after a week anyway (snoozeFor).
func noBackups(ctx context.Context, svcs []UnbackedService, toftDB bool) []Item {
	type risk struct {
		item              Item
		dbs, vols, stacks int
		shared            map[string]bool
	}
	var order []*risk
	byServer := map[string]*risk{}
	at := func(serverID, name string) *risk {
		if r := byServer[serverID]; r != nil {
			return r
		}
		if serverID == "" {
			name = "The panel host"
		}
		r := &risk{item: Item{Kind: "no_backup", Category: "servers", Title: name, ServerID: serverID}, shared: map[string]bool{}}
		byServer[serverID] = r
		order = append(order, r)
		return r
	}
	if toftDB {
		at("", "")
	}
	for _, s := range svcs {
		r := at(s.ServerID, s.Server)
		if s.Compose {
			if dbs, vols, ok := composeData(ctx, s); ok {
				r.dbs += dbs
				r.vols += vols
			} else {
				r.stacks++
			}
		} else if resources.KindOf(s.AddonSlug, s.Image) == resources.KindDatabase {
			r.dbs++
		} else {
			r.vols += s.Volumes
		}
		for _, v := range s.SharedVolumes {
			r.shared[v] = true
		}
		// Its age is how long it has been at risk: the oldest service's, so
		// adding another doesn't restart the clock.
		if !s.Since.IsZero() && (r.item.At.IsZero() || s.Since.Before(r.item.At)) {
			r.item.At = s.Since
		}
	}
	count := func(n int, one string) string {
		if n == 1 {
			return "1 " + one
		}
		return strconv.Itoa(n) + " " + one + "s"
	}
	out := []Item{}
	for _, r := range order {
		toft := toftDB && r.item.ServerID == ""
		vols := r.vols + len(r.shared)
		var parts []string
		if toft {
			parts = append(parts, "Toft’s database")
		}
		if r.dbs > 0 {
			parts = append(parts, count(r.dbs, "database"))
		}
		if vols > 0 {
			parts = append(parts, count(vols, "volume"))
		}
		if r.stacks > 0 {
			parts = append(parts, count(r.stacks, "compose stack"))
		}
		if len(parts) == 0 {
			continue
		}
		where := r.item.ServerID
		if where == "" {
			where = "panel"
		}
		r.item.Key = "no_backup:" + where + ":" + strconv.FormatBool(toft) + ":" + strconv.Itoa(r.dbs) + ":" + strconv.Itoa(vols) + ":" + strconv.Itoa(r.stacks)
		r.item.Detail = strings.Join(parts, ", ") + ", no backups"
		if r.item.At.IsZero() {
			r.item.At = time.Now()
		}
		out = append(out, r.item)
	}
	return out
}

// composeData counts an inline compose file's databases (by image, as
// resources.NewKindOf) and the named volumes and bind mounts its other
// services use, each once. ok is false for a file in a repository, which
// isn't stored, or one that doesn't load: the stack may hold data.
// ponytail: parses each unbacked stack on every Needs you read; cache by
// content if that shows up in profiles.
func composeData(ctx context.Context, s UnbackedService) (dbs, vols int, ok bool) {
	if s.ComposeFromRepo || strings.TrimSpace(s.ComposeContent) == "" {
		return 0, 0, false
	}
	p, vs, err := composespec.Load(ctx, composespec.Source{Root: "/nonexistent/toft-inbox", Content: s.ComposeContent, ProjectName: "p"})
	if err != nil || len(vs) > 0 || p == nil {
		return 0, 0, false
	}
	seen := map[string]bool{}
	for _, svc := range p.Services {
		if resources.NewKindOf("", svc.Image) == resources.KindDatabase {
			dbs++
			continue
		}
		for _, v := range svc.Volumes {
			if (v.Type == types.VolumeTypeVolume || v.Type == types.VolumeTypeBind) && v.Source != "" {
				seen[v.Type+":"+v.Source] = true
			}
		}
	}
	return dbs, len(seen), true
}

// Mark marks Needs you items and updates done, or brings them back.
func (s *Service) Mark(ctx context.Context, orgID, userID string, in MarkInput, done bool) error {
	if len(in.Keys) > 200 || len(in.Updates) > 200 {
		return ErrBadInput
	}
	if err := s.repo.MarkDone(ctx, userID, in.Keys, done); err != nil {
		return err
	}
	return s.repo.SetDone(ctx, userID, orgID, parseIDs(in.Updates), done)
}

func parseIDs(ss []string) []int64 {
	out := make([]int64, 0, len(ss))
	for _, s := range ss {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// Read marks updates read.
func (s *Service) Read(ctx context.Context, orgID, userID string, in ReadInput) error {
	if len(in.Updates) > 200 {
		return ErrBadInput
	}
	return s.repo.MarkRead(ctx, userID, orgID, parseIDs(in.Updates), in.All)
}

// Counts are the sidebar's: Needs you, unread updates, my issues.
func (s *Service) Counts(ctx context.Context, orgID, userID, role string) (*Counts, error) {
	items, err := s.NeedsYou(ctx, orgID, userID, role, MarksOpen)
	if err != nil {
		return nil, err
	}
	unread, err := s.repo.CountUnread(ctx, userID, orgID)
	if err != nil {
		return nil, err
	}
	mine, err := s.repo.CountMyIssues(ctx, orgID, userID)
	if err != nil {
		return nil, err
	}
	return &Counts{NeedsYou: len(items), Unread: unread, MyIssues: mine}, nil
}

// Updates pages the person's updates, newest first; the cursor is the last
// one's time and id.
func (s *Service) Updates(ctx context.Context, q UpdateQuery, cursor string) ([]Update, string, error) {
	q.Limit = min(max(q.Limit, 1), 100)
	if cursor != "" {
		at, idStr, err := parseCursor(cursor)
		if err != nil {
			return nil, "", ErrBadCursor
		}
		q.Before, q.BeforeID = &at, idStr
	}
	limit := q.Limit
	q.Limit++
	us, err := s.repo.Updates(ctx, q)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(us) > limit {
		us = us[:limit]
		last := us[limit-1]
		next = last.CreatedAt.UTC().Format(time.RFC3339Nano) + "_" + strconv.FormatInt(last.id, 10)
	}
	return us, next, nil
}

var cursorRe = regexp.MustCompile(`^(.+)_(\d+)$`)

func parseCursor(c string) (time.Time, int64, error) {
	m := cursorRe.FindStringSubmatch(c)
	if m == nil {
		return time.Time{}, 0, ErrBadCursor
	}
	at, err := time.Parse(time.RFC3339Nano, m[1])
	if err != nil {
		return time.Time{}, 0, ErrBadCursor
	}
	n, err := strconv.ParseInt(m[2], 10, 64)
	return at, n, err
}

// ─── mentions ────────────────────────────────────────────────────────────────

// Handles are the workspace's mention handles; the issues and code modules
// render @handle with them.
func (s *Service) Handles(ctx context.Context, orgID string) []string {
	ms, err := s.repo.Members(ctx, []string{orgID})
	if err != nil {
		s.log.Warn("mention handles", zap.Error(err))
		return nil
	}
	return handleList(Handles(ms))
}

var mentioned = regexp.MustCompile(`data-toft-user="([^"]+)"`)

// mentionsIn are the people body mentions, rendered as the panel does
// (so code spans and links don't count).
func mentionsIn(body string, handles map[string]string) []string {
	if body == "" || len(handles) == 0 {
		return nil
	}
	html := gitstore.MarkdownRefs([]byte(body), gitstore.Refs{Mentions: handleList(handles)})
	var out []string
	for _, m := range mentioned.FindAllStringSubmatch(html, -1) {
		if u := handles[m[1]]; u != "" && !slices.Contains(out, u) {
			out = append(out, u)
		}
	}
	return out
}

// ─── the notifier ────────────────────────────────────────────────────────────

// RunNotifier writes updates from its sources until ctx ends, and purges
// old ones hourly.
func (s *Service) RunNotifier(ctx context.Context) {
	tick := time.NewTicker(s.every)
	defer tick.Stop()
	lastPurge := time.Time{}
	for {
		for _, src := range Sources {
			if err := s.Notify(ctx, src); err != nil && ctx.Err() == nil {
				s.log.Warn("inbox notifier", zap.Error(err), zap.String("source", src))
			}
		}
		if time.Since(lastPurge) > time.Hour {
			if err := s.repo.Purge(ctx); err != nil && ctx.Err() == nil {
				s.log.Warn("purge the inbox", zap.Error(err))
			}
			lastPurge = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Notify writes the updates of a source's records since its cursor. Its
// first read starts now: history isn't sent.
func (s *Service) Notify(ctx context.Context, source string) error {
	at, last, ok, err := s.repo.Cursor(ctx, source)
	if err != nil {
		return err
	}
	if !ok {
		return s.repo.SetCursor(ctx, source, time.Now(), "")
	}
	rows, err := s.repo.Rows(ctx, source, at, last)
	if err != nil || len(rows) == 0 {
		return err
	}
	ns, err := s.fanOut(ctx, source, rows)
	if err != nil {
		return err
	}
	if err := s.repo.Insert(ctx, ns); err != nil {
		return err
	}
	end := rows[len(rows)-1]
	if err := s.repo.SetCursor(ctx, source, end.At, end.RowID); err != nil {
		return err
	}
	s.tell(ns)
	return nil
}

// tell says to each workspace's open tabs whose inbox changed.
func (s *Service) tell(ns []Notification) {
	if s.pub == nil {
		return
	}
	byOrg := map[string][]string{}
	for _, n := range ns {
		if !slices.Contains(byOrg[n.OrgID], n.UserID) {
			byOrg[n.OrgID] = append(byOrg[n.OrgID], n.UserID)
		}
	}
	for org, users := range byOrg {
		s.pub.Publish(org, broker.Event{Type: "inbox", Payload: map[string]any{"users": users}})
	}
}

// fanOut is who hears of each record, never its actor: the people an issue,
// a pull request or a page concerns, and whoever its text mentions.
func (s *Service) fanOut(ctx context.Context, source string, rows []Row) ([]Notification, error) {
	var issueIDs, pullIDs, pageIDs, orgIDs []string
	for _, r := range rows {
		for _, v := range []struct {
			id  string
			dst *[]string
		}{{r.IssueID, &issueIDs}, {r.PullID, &pullIDs}, {r.PageID, &pageIDs}, {r.OrgID, &orgIDs}} {
			if v.id != "" && !slices.Contains(*v.dst, v.id) {
				*v.dst = append(*v.dst, v.id)
			}
		}
	}
	var who map[string][]string
	var err error
	switch {
	case len(issueIDs) > 0:
		who, err = s.repo.Participants(ctx, "issue", issueIDs)
	case len(pullIDs) > 0:
		who, err = s.repo.Participants(ctx, "pull", pullIDs)
	case len(pageIDs) > 0:
		who, err = s.repo.Participants(ctx, "page", pageIDs)
	}
	if err != nil {
		return nil, err
	}
	handles := map[string]map[string]string{}
	if source == "issue_events" || source == "issue_comments" || source == "pull_requests" || source == "pull_request_comments" {
		ms, err := s.repo.Members(ctx, orgIDs)
		if err != nil {
			return nil, err
		}
		byOrg := map[string][]Member{}
		for _, m := range ms {
			byOrg[m.OrgID] = append(byOrg[m.OrgID], m)
		}
		for org, list := range byOrg {
			handles[org] = Handles(list)
		}
	}

	var out []Notification
	for _, r := range rows {
		base := Notification{OrgID: r.OrgID, ProjectID: opt(r.ProjectID), IssueID: opt(r.IssueID), PullID: opt(r.PullID),
			PageID: opt(r.PageID), ActorID: opt(r.ActorID), CreatedAt: r.At}
		sent := map[string]bool{r.ActorID: true}
		send := func(users []string, kind string, detail map[string]any) {
			for _, u := range users {
				if u == "" || sent[u] {
					continue
				}
				sent[u] = true
				n := base
				if r.Agent != "" {
					// An agent's work names it (D-102): "Claude Code for admin".
					detail = maps.Clone(detail)
					detail["agent"] = r.Agent
				}
				n.UserID, n.Kind, n.Detail = u, kind, detail
				out = append(out, n)
			}
		}
		subject := r.IssueID + r.PullID + r.PageID
		mentions := mentionsIn(r.Body, handles[r.OrgID])
		switch {
		case source == "issue_events" && r.Kind == "opened":
			send(mentions, "mention", map[string]any{"excerpt": excerpt(r.Body)})
		case source == "issue_events" && r.Kind == "status":
			send(who[subject], "issue_status", map[string]any{"from": r.From, "to": r.To, "automatic": r.Automatic})
		case source == "issue_events" && r.Kind == "assigned":
			send([]string{r.To}, "issue_assigned", map[string]any{})
		case source == "issue_comments":
			send(mentions, "mention", map[string]any{"excerpt": excerpt(r.Body)})
			send(who[subject], "issue_comment", map[string]any{"excerpt": excerpt(r.Body)})
		case source == "pull_requests":
			send(mentions, "mention", map[string]any{"excerpt": excerpt(r.Body)})
		case source == "pull_request_events":
			send(who[subject], "pull_"+r.Kind, map[string]any{})
		case source == "pull_request_comments":
			send(mentions, "mention", map[string]any{"excerpt": excerpt(r.Body)})
			send(who[subject], "pull_comment", map[string]any{"excerpt": excerpt(r.Body)})
		case source == "doc_page_versions":
			send(who[subject], "page_edited", map[string]any{"version": r.Version})
		}
	}
	return out, nil
}

func opt(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func shortSHA(sha string) string { return sha[:min(7, len(sha))] }
