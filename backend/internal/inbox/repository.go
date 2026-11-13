package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/internal/catalog"
	"github.com/Nickpap21/toft/backend/internal/issue"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sources are the notifier's, in the order it reads them.
var Sources = []string{"issue_events", "issue_comments", "pull_requests", "pull_request_events", "pull_request_comments", "doc_page_versions"}

// Repository reads what needs a person and keeps their updates.
type Repository interface {
	FailedDeploys(ctx context.Context, orgID string) ([]Item, error)
	OpenAlerts(ctx context.Context, orgID string) ([]Item, error)
	// UnprotectedPublic are the workspace's image services anyone can open
	// at their address: no basic auth, no IP allowlist.
	UnprotectedPublic(ctx context.Context, orgID string) ([]PublicService, error)
	// UnbackedServices are the workspace's services on a server without a
	// backup destination (D-43), the panel host's included.
	UnbackedServices(ctx context.Context, orgID string) ([]UnbackedService, error)
	// HTTPSRedeploys are the workspace's services whose temporary name has
	// had a certificate since their live deployment, which routes it over
	// plain HTTP (behind basic auth: gave it an http:// TOFT_URL), when Toft
	// didn't redeploy them (D-159).
	HTTPSRedeploys(ctx context.Context, orgID string) ([]Item, error)
	// TemporaryLogins are the workspace's services made from one of slugs
	// with catalog.TemporaryLoginUser's basic auth still on (no Hostname).
	TemporaryLogins(ctx context.Context, orgID string, slugs []string) ([]PublicService, error)
	// PanelHostBackedUp: the panel host has a backup destination.
	PanelHostBackedUp(ctx context.Context) (bool, error)
	AssignedUrgent(ctx context.Context, orgID, userID string) ([]Item, error)
	// AssignedDue are a person's open issues due by tomorrow (D-90).
	AssignedDue(ctx context.Context, orgID, userID string) ([]Item, error)
	// DoneKeys are the keys a person marked done, with when.
	DoneKeys(ctx context.Context, userID string) (map[string]time.Time, error)
	MarkDone(ctx context.Context, userID string, keys []string, done bool) error
	CountMyIssues(ctx context.Context, orgID, userID string) (int, error)

	Updates(ctx context.Context, q UpdateQuery) ([]Update, error)
	CountUnread(ctx context.Context, userID, orgID string) (int, error)
	MarkRead(ctx context.Context, userID, orgID string, ids []int64, all bool) error
	SetDone(ctx context.Context, userID, orgID string, ids []int64, done bool) error
	Purge(ctx context.Context) error

	Members(ctx context.Context, orgIDs []string) ([]Member, error)
	// Cursor is where the notifier left a source; ok false before its first read.
	Cursor(ctx context.Context, source string) (at time.Time, id string, ok bool, err error)
	SetCursor(ctx context.Context, source string, at time.Time, id string) error
	// Rows are a source's records after a cursor, oldest first.
	Rows(ctx context.Context, source string, at time.Time, id string) ([]Row, error)
	// Participants are who issues, pull requests or pages (kind) concern, by id.
	Participants(ctx context.Context, kind string, ids []string) (map[string][]string, error)
	Insert(ctx context.Context, ns []Notification) error
}

type postgresRepository struct {
	q *sqlcdb.Queries
}

// NewPostgresRepository keeps the inbox in Postgres.
func NewPostgresRepository(pool *pgxpool.Pool) Repository {
	return &postgresRepository{q: sqlcdb.New(pool)}
}

func id(s string) pgtype.UUID {
	u, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: u, Valid: true}
}

func ids(ss []string) []pgtype.UUID {
	out := make([]pgtype.UUID, 0, len(ss))
	for _, s := range ss {
		if u := id(s); u.Valid {
			out = append(out, u)
		}
	}
	return out
}

func str(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return uuid.UUID(u.Bytes).String()
}

func strp(u pgtype.UUID) *string {
	if !u.Valid {
		return nil
	}
	s := str(u)
	return &s
}

func (r *postgresRepository) FailedDeploys(ctx context.Context, orgID string) ([]Item, error) {
	rows, err := r.q.ListDashboardFailedDeploys(ctx, id(orgID))
	if err != nil {
		return nil, fmt.Errorf("ListDashboardFailedDeploys: %w", err)
	}
	out := make([]Item, 0, len(rows))
	for _, row := range rows {
		detail := "Deploy failed"
		if row.CommitSha != "" {
			detail += " · commit " + short(row.CommitSha)
		}
		if row.ServerName != "" {
			detail += " · " + row.ServerName
		}
		title := row.ServiceName
		if row.ProjectName != "" {
			title = row.ProjectName + " / " + row.ServiceName
		}
		out = append(out, Item{Key: "deploy:" + str(row.ID), Kind: "deploy_failed", Category: "deploys", Title: title, Detail: detail,
			Error: row.ErrorSummary, At: row.FailedAt.Time, ProjectID: strp(row.ProjectID), Project: row.ProjectName,
			ServiceID: str(row.ServiceID), DeploymentID: str(row.ID)})
	}
	return out, nil
}

func (r *postgresRepository) UnprotectedPublic(ctx context.Context, orgID string) ([]PublicService, error) {
	rows, err := r.q.ListDashboardUnprotectedPublic(ctx, id(orgID))
	if err != nil {
		return nil, fmt.Errorf("ListDashboardUnprotectedPublic: %w", err)
	}
	out := make([]PublicService, 0, len(rows))
	for _, row := range rows {
		ps := PublicService{ID: str(row.ID), Name: row.Name, ProjectID: strp(row.ProjectID), Project: row.ProjectName,
			Environment: row.EnvironmentName, Hostname: row.Hostname, Since: row.CreatedAt.Time}
		if row.AddonSlug != nil {
			ps.AddonSlug = *row.AddonSlug
		}
		if row.DockerImage != nil {
			ps.Image = *row.DockerImage
		}
		out = append(out, ps)
	}
	return out, nil
}

func (r *postgresRepository) UnbackedServices(ctx context.Context, orgID string) ([]UnbackedService, error) {
	rows, err := r.q.ListDashboardUnbackedServices(ctx, id(orgID))
	if err != nil {
		return nil, fmt.Errorf("ListDashboardUnbackedServices: %w", err)
	}
	out := make([]UnbackedService, 0, len(rows))
	for _, row := range rows {
		us := UnbackedService{ID: str(row.ID), ServerID: str(row.ServerID), Server: row.ServerName, Volumes: int(row.Volumes),
			SharedVolumes: row.SharedVolumeIds, Compose: row.Compose, ComposeFromRepo: row.ComposeFromRepo, ComposeContent: row.ComposeContent,
			Since: row.CreatedAt.Time}
		if row.AddonSlug != nil {
			us.AddonSlug = *row.AddonSlug
		}
		if row.DockerImage != nil {
			us.Image = *row.DockerImage
		}
		out = append(out, us)
	}
	return out, nil
}

func (r *postgresRepository) HTTPSRedeploys(ctx context.Context, orgID string) ([]Item, error) {
	rows, err := r.q.ListDashboardHTTPSRedeploys(ctx, id(orgID))
	if err != nil {
		return nil, fmt.Errorf("ListDashboardHTTPSRedeploys: %w", err)
	}
	out := make([]Item, 0, len(rows))
	for _, row := range rows {
		at := row.CertificateAt.Time
		where := row.Hostname
		if row.EnvironmentName != "" {
			where += " · " + row.EnvironmentName
		}
		title, detail := "Redeploy "+row.Name+" to switch it to HTTPS", where+": its certificate is out, and it serves plain HTTP until its next deploy"
		if row.BehindLogin {
			// Basic auth never serves over HTTP (Orchestrator's routes).
			title = "Redeploy " + row.Name + " to give it its HTTPS address"
			detail = where + ": its certificate is out and its address redirects to HTTPS already; TOFT_URL says http:// until its next deploy"
		}
		out = append(out, Item{Key: "https_redeploy:" + str(row.ID) + ":" + strconv.FormatInt(at.Unix(), 10), Kind: "https_redeploy",
			Category: "deploys", Title: title, Detail: detail, At: at,
			ProjectID: strp(row.ProjectID), Project: row.ProjectName, ServiceID: str(row.ID)})
	}
	return out, nil
}

func (r *postgresRepository) TemporaryLogins(ctx context.Context, orgID string, slugs []string) ([]PublicService, error) {
	rows, err := r.q.ListDashboardTemporaryLogins(ctx, sqlcdb.ListDashboardTemporaryLoginsParams{OrgID: id(orgID), Slugs: slugs, LoginUser: catalog.TemporaryLoginUser})
	if err != nil {
		return nil, fmt.Errorf("ListDashboardTemporaryLogins: %w", err)
	}
	out := make([]PublicService, 0, len(rows))
	for _, row := range rows {
		ps := PublicService{ID: str(row.ID), Name: row.Name, ProjectID: strp(row.ProjectID), Project: row.ProjectName,
			Environment: row.EnvironmentName, Since: row.CreatedAt.Time}
		if row.AddonSlug != nil {
			ps.AddonSlug = *row.AddonSlug
		}
		out = append(out, ps)
	}
	return out, nil
}

func (r *postgresRepository) PanelHostBackedUp(ctx context.Context) (bool, error) {
	ok, err := r.q.DashboardPanelHostBackedUp(ctx)
	if err != nil {
		return false, fmt.Errorf("DashboardPanelHostBackedUp: %w", err)
	}
	return ok, nil
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func (r *postgresRepository) OpenAlerts(ctx context.Context, orgID string) ([]Item, error) {
	rows, err := r.q.ListDashboardOpenAlerts(ctx, id(orgID))
	if err != nil {
		return nil, fmt.Errorf("ListDashboardOpenAlerts: %w", err)
	}
	out := make([]Item, 0, len(rows))
	for _, row := range rows {
		cat := "deploys"
		switch row.Kind {
		case "server_unreachable", "disk_full", "backup_failed", "volume_full", "agent_offline", "agent_frozen",
			"security_updates", "reboot_required", "docker_advisory":
			cat = "servers"
		case "mirror_failing":
			cat = "code"
		}
		title := row.SubjectName
		if title == "" && row.Subject == "control-plane" {
			title = "The panel’s server"
		} else if title == "" {
			title = strings.ReplaceAll(row.Kind, "_", " ")
		}
		it := Item{Key: "alert:" + str(row.ID), Kind: "alert", Category: cat, Title: title, Detail: row.Detail,
			At: row.FiredAt.Time, ProjectID: strp(row.ProjectID), Project: row.ProjectName, AlertKind: row.Kind}
		if cat == "servers" && row.Subject != "control-plane" {
			it.ServerID = row.Subject
		}
		if cat == "deploys" {
			it.ServiceID = row.Subject
		}
		out = append(out, it)
	}
	return out, nil
}

func (r *postgresRepository) AssignedUrgent(ctx context.Context, orgID, userID string) ([]Item, error) {
	rows, err := r.q.InboxAssignedUrgent(ctx, sqlcdb.InboxAssignedUrgentParams{OrgID: id(orgID), UserID: id(userID)})
	if err != nil {
		return nil, fmt.Errorf("InboxAssignedUrgent: %w", err)
	}
	out := make([]Item, 0, len(rows))
	for _, row := range rows {
		key := issue.KeyOf(row.IssueKey, int(row.Number), int(row.Digits))
		out = append(out, Item{Key: "assigned:" + str(row.ID), Kind: "assigned", Category: "issues", Title: row.Title,
			Detail: "Assigned to you", At: row.UpdatedAt.Time, ProjectID: strp(row.ProjectID), Project: row.ProjectName,
			Issue: &Issue{Key: key, Number: int(row.Number), Title: row.Title, Status: row.Status, Priority: row.Priority}})
	}
	return out, nil
}

func (r *postgresRepository) AssignedDue(ctx context.Context, orgID, userID string) ([]Item, error) {
	rows, err := r.q.InboxAssignedDue(ctx, sqlcdb.InboxAssignedDueParams{OrgID: id(orgID), UserID: id(userID)})
	if err != nil {
		return nil, fmt.Errorf("InboxAssignedDue: %w", err)
	}
	out := make([]Item, 0, len(rows))
	for _, row := range rows {
		key := issue.KeyOf(row.IssueKey, int(row.Number), int(row.Digits))
		due := row.DueOn.Time
		out = append(out, Item{Key: "due:" + str(row.ID) + ":" + due.Format("2006-01-02"), Kind: "due", Category: "issues", Title: row.Title,
			Detail: dueDetail(due, row.Today.Time), At: row.UpdatedAt.Time, ProjectID: strp(row.ProjectID), Project: row.ProjectName,
			Issue: &Issue{Key: key, Number: int(row.Number), Title: row.Title, Status: row.Status, Priority: row.Priority}})
	}
	return out, nil
}

func (r *postgresRepository) DoneKeys(ctx context.Context, userID string) (map[string]time.Time, error) {
	rows, err := r.q.ListInboxDone(ctx, id(userID))
	if err != nil {
		return nil, fmt.Errorf("ListInboxDone: %w", err)
	}
	out := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		out[row.ItemKey] = row.DoneAt.Time
	}
	return out, nil
}

func (r *postgresRepository) MarkDone(ctx context.Context, userID string, keys []string, done bool) error {
	if len(keys) == 0 {
		return nil
	}
	var err error
	if done {
		err = r.q.MarkInboxDone(ctx, sqlcdb.MarkInboxDoneParams{UserID: id(userID), Keys: keys})
	} else {
		err = r.q.UnmarkInboxDone(ctx, sqlcdb.UnmarkInboxDoneParams{UserID: id(userID), Keys: keys})
	}
	if err != nil {
		return fmt.Errorf("mark done: %w", err)
	}
	return nil
}

func (r *postgresRepository) CountMyIssues(ctx context.Context, orgID, userID string) (int, error) {
	rows, err := r.q.MyIssues(ctx, sqlcdb.MyIssuesParams{OrgID: id(orgID), UserID: id(userID)})
	if err != nil {
		return 0, fmt.Errorf("MyIssues: %w", err)
	}
	return len(rows), nil
}

func (r *postgresRepository) Updates(ctx context.Context, q UpdateQuery) ([]Update, error) {
	p := sqlcdb.ListNotificationsParams{UserID: id(q.UserID), OrgID: id(q.OrgID), Done: q.Done, Unread: q.Unread,
		Kinds: q.Kinds, RowLimit: int32(q.Limit)}
	if p.Kinds == nil {
		p.Kinds = []string{}
	}
	if q.Before != nil {
		p.HasCursor, p.CursorAt, p.CursorID = true, pgtype.Timestamptz{Time: *q.Before, Valid: true}, q.BeforeID
	}
	rows, err := r.q.ListNotifications(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("ListNotifications: %w", err)
	}
	out := make([]Update, 0, len(rows))
	for _, row := range rows {
		u := Update{ID: fmt.Sprint(row.ID), id: row.ID, Kind: row.Kind, ProjectID: strp(row.ProjectID), Project: row.ProjectName,
			CreatedAt: row.CreatedAt.Time, Read: row.ReadAt.Valid, Done: row.DoneAt.Valid, Detail: map[string]any{}}
		_ = json.Unmarshal(row.Detail, &u.Detail)
		if row.ActorID.Valid {
			u.Actor = &Person{ID: str(row.ActorID), Name: row.ActorName}
		}
		if row.IssueID.Valid {
			u.Issue = &Issue{Key: issue.KeyOf(row.IssueKey, int(row.IssueNumber), int(row.Digits)), Number: int(row.IssueNumber),
				Title: row.IssueTitle, Status: row.IssueStatus}
		}
		if row.PullRequestID.Valid {
			u.Pull = &Pull{Repository: row.Repository, Number: int(row.PullNumber), Title: row.PullTitle, State: row.PullState}
		}
		if row.PageID.Valid {
			u.Page = &Page{ID: str(row.PageID), Title: row.PageTitle}
		}
		u.Category = categoryOf(u.Kind, u.Pull != nil, u.Page != nil)
		out = append(out, u)
	}
	return out, nil
}

func (r *postgresRepository) CountUnread(ctx context.Context, userID, orgID string) (int, error) {
	n, err := r.q.CountUnreadNotifications(ctx, sqlcdb.CountUnreadNotificationsParams{UserID: id(userID), OrgID: id(orgID)})
	if err != nil {
		return 0, fmt.Errorf("CountUnreadNotifications: %w", err)
	}
	return int(n), nil
}

func (r *postgresRepository) MarkRead(ctx context.Context, userID, orgID string, ns []int64, all bool) error {
	if ns == nil {
		ns = []int64{}
	}
	if err := r.q.MarkNotificationsRead(ctx, sqlcdb.MarkNotificationsReadParams{UserID: id(userID), OrgID: id(orgID), AllUnread: all, Ids: ns}); err != nil {
		return fmt.Errorf("MarkNotificationsRead: %w", err)
	}
	return nil
}

func (r *postgresRepository) SetDone(ctx context.Context, userID, orgID string, ns []int64, done bool) error {
	if len(ns) == 0 {
		return nil
	}
	if err := r.q.SetNotificationsDone(ctx, sqlcdb.SetNotificationsDoneParams{UserID: id(userID), OrgID: id(orgID), Ids: ns, Done: done}); err != nil {
		return fmt.Errorf("SetNotificationsDone: %w", err)
	}
	return nil
}

func (r *postgresRepository) Purge(ctx context.Context) error {
	if err := r.q.PurgeInbox(ctx); err != nil {
		return fmt.Errorf("PurgeInbox: %w", err)
	}
	return nil
}

func (r *postgresRepository) Members(ctx context.Context, orgIDs []string) ([]Member, error) {
	rows, err := r.q.InboxOrgMembers(ctx, ids(orgIDs))
	if err != nil {
		return nil, fmt.Errorf("InboxOrgMembers: %w", err)
	}
	out := make([]Member, 0, len(rows))
	for _, row := range rows {
		out = append(out, Member{OrgID: str(row.OrgID), UserID: str(row.UserID), Email: row.Email})
	}
	return out, nil
}

func (r *postgresRepository) Cursor(ctx context.Context, source string) (time.Time, string, bool, error) {
	row, err := r.q.GetInboxCursor(ctx, source)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, "", false, nil
	}
	if err != nil {
		return time.Time{}, "", false, fmt.Errorf("GetInboxCursor: %w", err)
	}
	return row.At.Time, row.LastID, true, nil
}

func (r *postgresRepository) SetCursor(ctx context.Context, source string, at time.Time, last string) error {
	if err := r.q.SetInboxCursor(ctx, sqlcdb.SetInboxCursorParams{Source: source, At: pgtype.Timestamptz{Time: at, Valid: true}, LastID: last}); err != nil {
		return fmt.Errorf("SetInboxCursor: %w", err)
	}
	return nil
}

func (r *postgresRepository) Rows(ctx context.Context, source string, at time.Time, after string) ([]Row, error) {
	ts := pgtype.Timestamptz{Time: at, Valid: true}
	var out []Row
	switch source {
	case "issue_events":
		rows, err := r.q.InboxIssueEvents(ctx, sqlcdb.InboxIssueEventsParams{AfterAt: ts, AfterID: after})
		if err != nil {
			return nil, fmt.Errorf("InboxIssueEvents: %w", err)
		}
		out = make([]Row, 0, len(rows))
		for _, x := range rows {
			out = append(out, Row{RowID: x.RowID, At: x.CreatedAt.Time, Kind: x.Kind, ActorID: str(x.ActorID), Automatic: x.Automatic,
				From: x.FromValue, To: x.ToValue, IssueID: str(x.IssueID), ProjectID: str(x.ProjectID), OrgID: str(x.OrgID), Body: x.Description,
				Agent: x.AgentName})
		}
	case "issue_comments":
		rows, err := r.q.InboxIssueComments(ctx, sqlcdb.InboxIssueCommentsParams{AfterAt: ts, AfterID: after})
		if err != nil {
			return nil, fmt.Errorf("InboxIssueComments: %w", err)
		}
		out = make([]Row, 0, len(rows))
		for _, x := range rows {
			out = append(out, Row{RowID: x.RowID, At: x.CreatedAt.Time, Kind: "comment", ActorID: str(x.AuthorID), Body: x.Body,
				IssueID: str(x.IssueID), ProjectID: str(x.ProjectID), OrgID: str(x.OrgID), Agent: x.AgentName})
		}
	case "pull_requests":
		rows, err := r.q.InboxPullsOpened(ctx, sqlcdb.InboxPullsOpenedParams{AfterAt: ts, AfterID: after})
		if err != nil {
			return nil, fmt.Errorf("InboxPullsOpened: %w", err)
		}
		out = make([]Row, 0, len(rows))
		for _, x := range rows {
			out = append(out, Row{RowID: x.RowID, At: x.CreatedAt.Time, Kind: "opened", ActorID: str(x.AuthorID), Body: x.Body,
				PullID: str(x.PullID), ProjectID: str(x.ProjectID), OrgID: str(x.OrgID), Agent: x.AgentName})
		}
	case "pull_request_events":
		rows, err := r.q.InboxPullEvents(ctx, sqlcdb.InboxPullEventsParams{AfterAt: ts, AfterID: after})
		if err != nil {
			return nil, fmt.Errorf("InboxPullEvents: %w", err)
		}
		out = make([]Row, 0, len(rows))
		for _, x := range rows {
			out = append(out, Row{RowID: x.RowID, At: x.CreatedAt.Time, Kind: x.Kind, ActorID: str(x.ActorID),
				PullID: str(x.PullID), ProjectID: str(x.ProjectID), OrgID: str(x.OrgID)})
		}
	case "pull_request_comments":
		rows, err := r.q.InboxPullComments(ctx, sqlcdb.InboxPullCommentsParams{AfterAt: ts, AfterID: after})
		if err != nil {
			return nil, fmt.Errorf("InboxPullComments: %w", err)
		}
		out = make([]Row, 0, len(rows))
		for _, x := range rows {
			out = append(out, Row{RowID: x.RowID, At: x.CreatedAt.Time, Kind: "comment", ActorID: str(x.AuthorID), Body: x.Body,
				PullID: str(x.PullID), ProjectID: str(x.ProjectID), OrgID: str(x.OrgID), Agent: x.AgentName})
		}
	case "doc_page_versions":
		rows, err := r.q.InboxPageVersions(ctx, sqlcdb.InboxPageVersionsParams{AfterAt: ts, AfterID: after})
		if err != nil {
			return nil, fmt.Errorf("InboxPageVersions: %w", err)
		}
		out = make([]Row, 0, len(rows))
		for _, x := range rows {
			out = append(out, Row{RowID: x.RowID, At: x.CreatedAt.Time, Kind: "edited", ActorID: str(x.AuthorID), Version: int(x.Version),
				PageID: str(x.PageID), ProjectID: str(x.ProjectID), OrgID: str(x.OrgID)})
		}
	default:
		return nil, fmt.Errorf("unknown source %q", source)
	}
	return out, nil
}

func (r *postgresRepository) Participants(ctx context.Context, kind string, of []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(of) == 0 {
		return out, nil
	}
	add := func(k, u pgtype.UUID) { out[str(k)] = append(out[str(k)], str(u)) }
	switch kind {
	case "issue":
		rows, err := r.q.InboxIssueParticipants(ctx, ids(of))
		if err != nil {
			return nil, fmt.Errorf("InboxIssueParticipants: %w", err)
		}
		for _, x := range rows {
			add(x.IssueID, x.UserID)
		}
	case "pull":
		rows, err := r.q.InboxPullParticipants(ctx, ids(of))
		if err != nil {
			return nil, fmt.Errorf("InboxPullParticipants: %w", err)
		}
		for _, x := range rows {
			add(x.PullID, x.UserID)
		}
	case "page":
		rows, err := r.q.InboxPageParticipants(ctx, ids(of))
		if err != nil {
			return nil, fmt.Errorf("InboxPageParticipants: %w", err)
		}
		for _, x := range rows {
			add(x.PageID, x.UserID)
		}
	}
	return out, nil
}

func (r *postgresRepository) Insert(ctx context.Context, ns []Notification) error {
	if len(ns) == 0 {
		return nil
	}
	b, err := json.Marshal(ns)
	if err != nil {
		return err
	}
	if err := r.q.InsertNotifications(ctx, b); err != nil {
		return fmt.Errorf("InsertNotifications: %w", err)
	}
	return nil
}
