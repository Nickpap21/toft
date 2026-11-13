// Package inbox is a person's inbox (D-88): Needs you, computed when read
// from what is true now, and updates, written by a notifier that reads the
// issues', pull requests' and docs' own records.
package inbox

import (
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	ErrBadCursor = errors.New("bad cursor")
	ErrBadInput  = errors.New("keys and updates hold 200 at most")
)

// Person is who did something.
type Person struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Issue is an issue as the inbox shows it.
type Issue struct {
	Key      string `json:"key"`
	Number   int    `json:"number"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Priority string `json:"priority,omitempty"`
}

// Item is one thing that needs the person, with the key that marks it done.
type Item struct {
	Key          string    `json:"key"`
	Kind         string    `json:"kind"`
	Category     string    `json:"category"`
	Title        string    `json:"title"`
	Detail       string    `json:"detail"`
	Error        string    `json:"error,omitempty"`
	At           time.Time `json:"at"`
	ProjectID    *string   `json:"project_id"`
	Project      string    `json:"project"`
	ServiceID    string    `json:"service_id,omitempty"`
	DeploymentID string    `json:"deployment_id,omitempty"`
	ServerID     string    `json:"server_id,omitempty"`
	Issue        *Issue    `json:"issue,omitempty"`
	PageID       string    `json:"page_id,omitempty"`
	AlertKind    string    `json:"alert_kind,omitempty"`
	// A preview to approve (D-97): its pull request and the commit.
	RepositoryID string `json:"repository_id,omitempty"`
	Repository   string `json:"repository,omitempty"`
	PullNumber   int    `json:"pull_number,omitempty"`
	SHA          string `json:"sha,omitempty"`
	// An agent's request waiting for a person (D-100).
	ApprovalID string     `json:"approval_id,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	// Severity orders Needs you (D-140): problem, waiting, warning, request.
	Severity string `json:"severity"`
	// Snoozes: marking it done puts it off for snoozeFor; SnoozedUntil is
	// when it comes back, while it is put off.
	Snoozes      bool       `json:"snoozes"`
	SnoozedUntil *time.Time `json:"snoozed_until"`
}

// Marks picks Needs you's items by the person's marks; the values are the
// done query parameter's.
type Marks string

const (
	MarksOpen Marks = "false"
	MarksDone Marks = "true"
	MarksAny  Marks = "any"
)

// snoozeFor is how long marking a condition done puts it off.
const snoozeFor = 7 * 24 * time.Hour

// snoozes says a kind is a condition, true until its cause is fixed: done
// can only put it off, or an unfixed one would disappear for good. An event
// has happened, and is done once a person has dealt with it.
func snoozes(kind string) bool {
	switch kind {
	case "no_backup", "no_watchdog", "kit_out_of_date", "exposed_tool", "setup_login", "https_redeploy", "alert":
		return true
	}
	return false
}

// severities ranks Item.Severity, most severe first.
var severities = map[string]int{"problem": 0, "waiting": 1, "warning": 2, "request": 3}

// severityOf is how much an item needs someone (D-140): something broken,
// an agent waiting (its request expires), something about to break or
// left open, then work asked of the person. An alert kind it doesn't name
// is a warning.
func severityOf(it Item) string {
	switch it.Kind {
	case "deploy_failed", "upgrade_failed":
		return "problem"
	case "agent_approval":
		return "waiting"
	case "preview_approval", "assigned", "due", "stale_page":
		return "request"
	case "alert":
		switch it.AlertKind {
		case "server_unreachable", "agent_offline", "backup_failed", "service_down", "container_exited",
			"panel_container", "mirror_failing", "agent_frozen", "service_restarting":
			return "problem"
		}
	}
	return "warning"
}

// PublicService is an image service with an address and neither basic auth
// nor an IP allowlist (Repository.UnprotectedPublic).
type PublicService struct {
	ID, Name, AddonSlug, Image     string
	ProjectID                      *string
	Project, Environment, Hostname string
	Since                          time.Time
}

// UnbackedService is a service on a server nothing backs up
// (Repository.UnbackedServices); ServerID "" is the panel host.
type UnbackedService struct {
	ID, AddonSlug, Image string
	ServerID, Server     string
	Volumes              int
	SharedVolumes        []string
	// A compose stack: its inline file, or ComposeFromRepo when its file is
	// in a repository and not stored.
	Compose, ComposeFromRepo bool
	ComposeContent           string
	Since                    time.Time
}

// Update is one of the person's updates.
type Update struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	Category  string         `json:"category"`
	Actor     *Person        `json:"actor"`
	ProjectID *string        `json:"project_id"`
	Project   string         `json:"project"`
	Issue     *Issue         `json:"issue,omitempty"`
	Pull      *Pull          `json:"pull,omitempty"`
	Page      *Page          `json:"page,omitempty"`
	Detail    map[string]any `json:"detail"`
	CreatedAt time.Time      `json:"created_at"`
	Read      bool           `json:"read"`
	Done      bool           `json:"done"`
	id        int64
}

type Pull struct {
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	Title      string `json:"title"`
	State      string `json:"state"`
}

type Page struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Counts are the sidebar's.
type Counts struct {
	NeedsYou int `json:"needs_you"`
	Unread   int `json:"unread"`
	MyIssues int `json:"my_issues"`
}

// MarkInput marks Needs you items (by key) and updates (by id).
type MarkInput struct {
	Keys    []string `json:"keys"`
	Updates []string `json:"updates"`
}

// ReadInput reads updates, or every unread one.
type ReadInput struct {
	Updates []string `json:"updates"`
	All     bool     `json:"all"`
}

// UpdateQuery is a page of updates.
type UpdateQuery struct {
	UserID, OrgID string
	Kinds         []string
	Unread, Done  bool
	Before        *time.Time
	BeforeID      int64
	Limit         int
}

// Notification is an update to write.
type Notification struct {
	UserID    string         `json:"user_id"`
	OrgID     string         `json:"org_id"`
	Kind      string         `json:"kind"`
	ActorID   *string        `json:"actor_id"`
	ProjectID *string        `json:"project_id"`
	IssueID   *string        `json:"issue_id"`
	PullID    *string        `json:"pull_request_id"`
	PageID    *string        `json:"page_id"`
	Detail    map[string]any `json:"detail"`
	CreatedAt time.Time      `json:"created_at"`
}

// Row is a record of one of the notifier's sources.
type Row struct {
	RowID            string
	At               time.Time
	Kind             string // an event's kind
	ActorID          string
	Automatic        bool
	From, To         string
	IssueID, PullID  string
	PageID           string
	ProjectID, OrgID string
	Body             string // a description, a comment, a pull request's body
	Version          int
	// Agent is the agent that acted for ActorID, when one did (D-102).
	Agent string
}

// Member is a workspace's member, for mentions.
type Member struct {
	OrgID, UserID, Email string
}

var handleRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Handles are a workspace's mention handles (D-88): each member's email's
// local part, lower case, when no other member shares it.
func Handles(members []Member) map[string]string {
	seen := map[string][]string{}
	for _, m := range members {
		local, _, ok := strings.Cut(strings.ToLower(m.Email), "@")
		local = strings.TrimRight(local, ".")
		if ok && handleRe.MatchString(local) {
			seen[local] = append(seen[local], m.UserID)
		}
	}
	out := map[string]string{}
	for h, ids := range seen {
		if len(ids) == 1 {
			out[h] = ids[0]
		}
	}
	return out
}

func handleList(h map[string]string) []string {
	out := make([]string, 0, len(h))
	for k := range h {
		out = append(out, k)
	}
	// Longer first: jo.doe before jo.
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// categoryOf is the inbox tab an update belongs to.
func categoryOf(kind string, hasPull, hasPage bool) string {
	switch {
	case hasPage || kind == "page_edited":
		return "docs"
	case hasPull || strings.HasPrefix(kind, "pull_"):
		return "code"
	}
	return "issues"
}

func excerpt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 200 {
		return string(r[:200]) + "…"
	}
	return s
}

// dueDetail says when an issue is due, from today: "Due today", "Due
// tomorrow", "1 day late".
func dueDetail(due, today time.Time) string {
	switch d := int(today.Sub(due).Hours() / 24); {
	case d < 0:
		return "Due tomorrow"
	case d == 0:
		return "Due today"
	case d == 1:
		return "1 day late"
	default:
		return strconv.Itoa(d) + " days late"
	}
}
