// Package issue is a project's issues (D-06, D-81): opened and moved by
// people, and moved forward by real events, a pushed branch, a pull request
// and its merge going live, which reach it through the code module's
// events outbox and the deploys.
package issue

import (
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/internal/code"
)

// Statuses, in the order events move an issue.
const (
	StatusBacklog    = "backlog"
	StatusTodo       = "todo"
	StatusInProgress = "in_progress"
	StatusInReview   = "in_review"
	StatusShipped    = "shipped"
	StatusCancelled  = "cancelled"
)

var (
	statuses   = []string{StatusBacklog, StatusTodo, StatusInProgress, StatusInReview, StatusShipped, StatusCancelled}
	priorities = []string{"none", "low", "medium", "high", "urgent"}

	// What each event moves an issue forward from (D-81): a pushed branch,
	// a pull request opened or pushed to, one closed unmerged (back from
	// review), a merge live everywhere it deploys.
	fromForBranch   = []string{StatusBacklog, StatusTodo}
	fromForPull     = []string{StatusBacklog, StatusTodo, StatusInProgress}
	fromForReopened = []string{StatusInReview}
	fromForShipped  = []string{StatusBacklog, StatusTodo, StatusInProgress, StatusInReview}
)

// Errors a client can act on (handler.go maps them).
var (
	ErrNotFound       = errors.New("not found")
	ErrBadTitle       = errors.New("a title has 1 to 256 characters and no control characters")
	ErrBadDescription = errors.New("a description has 64 KiB at most")
	ErrBadLabel       = errors.New("a label has 1 to 40 characters, no comma, and an issue 10 labels at most")
	ErrBadStatus      = errors.New("status is backlog, todo, in_progress, in_review, shipped or cancelled")
	ErrBadPriority    = errors.New("priority is none, low, medium, high or urgent")
	ErrNotAssignable  = errors.New("issues are assigned to owners and developers of the workspace")
	ErrAgentField     = errors.New("agents don't assign people or put issues in cycles: a person does")
	ErrBadCursor      = errors.New("cursor is not one this API issued")
	ErrEmptyComment   = errors.New("a comment has 1 character to 64 KiB")
	ErrNotYours       = errors.New("only its author can change it")
	ErrBadName        = errors.New("a view's name has 1 to 60 characters")
	ErrNameTaken      = errors.New("a view with this name exists in this project")
	ErrBadFilters     = errors.New("filters hold status, priority, assignee, label, q, order and group, with their values")
	ErrBadQuery       = errors.New("a filter's value is not one of its own")
	ErrBadKey         = errors.New("a key has 2 to 10 capital letters or digits, starting with a letter")
	ErrKeyTaken       = errors.New("the workspace has this key, as a project's or a former one")
	ErrBadDigits      = errors.New("digits is 0 to 6")
	ErrBadType        = errors.New("not one of the project's issue types")
	ErrBadTypeInput   = errors.New("a type has a name of 1 to 40 characters, a kind, an icon and a colour from their lists")
	ErrTypeTaken      = errors.New("the workspace has a type with this name")
	ErrLastType       = errors.New("the workspace keeps one standard type at least")
	ErrBadParent      = errors.New("a parent is another issue of the workspace, not a sub-task, and not one of its own children")
	ErrNeedsParent    = errors.New("a sub-task has a parent")
	ErrIssueNotFound  = errors.New("no issue in the workspace has this key")
	ErrRelated        = errors.New("the issues are related so already")
	ErrBadRelation    = errors.New("kind is blocks, blocked_by, relates, duplicates or duplicated_by")
)

// TypeRef is an issue's type as issues show it (D-83).
type TypeRef struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Kind  string `json:"kind"` // standard, container, subtask
	Icon  string `json:"icon"`
	Color string `json:"color"`
}

// Type is one of the workspace's issue types.
type Type struct {
	TypeRef
	Position int  `json:"position"`
	Archived bool `json:"archived"`
}

var (
	typeKinds  = []string{"standard", "container", "subtask"}
	typeIcons  = []string{"task", "bug", "feature", "epic", "release", "subtask", "story", "chore", "incident", "spike"}
	typeColors = []string{"gray", "blue", "red", "green", "violet", "amber", "teal"}
	// defaultTypes are a workspace's until its owners change them (D-83).
	defaultTypes = []TypeRef{
		{Name: "Task", Kind: "standard", Icon: "task", Color: "blue"},
		{Name: "Bug", Kind: "standard", Icon: "bug", Color: "red"},
		{Name: "Feature", Kind: "standard", Icon: "feature", Color: "green"},
		{Name: "Epic", Kind: "container", Icon: "epic", Color: "violet"},
		{Name: "Release", Kind: "container", Icon: "release", Color: "teal"},
		{Name: "Sub-task", Kind: "subtask", Icon: "subtask", Color: "gray"},
	}
)

// ParentRef names an issue's parent.
type ParentRef struct {
	Key   string `json:"key"`
	Title string `json:"title"`
}

// Progress is a parent's children: how many, shipped, and done (shipped or
// cancelled).
type Progress struct {
	Total   int `json:"total"`
	Shipped int `json:"shipped"`
	Done    int `json:"done"`
}

// Related is another issue as a relation, a child or a parent shows it.
type Related struct {
	Key       string  `json:"key"`
	Number    int     `json:"number"`
	Title     string  `json:"title"`
	Status    string  `json:"status"`
	Type      TypeRef `json:"type"`
	ProjectID string  `json:"project_id"`
	Assignee  *Person `json:"assignee,omitempty"`
}

// Relation is how an issue relates to another, from its own side.
type Relation struct {
	ID    string  `json:"id"`
	Kind  string  `json:"kind"`
	Issue Related `json:"issue"`
}

// WorkspaceSettings are the workspace's defaults for its issues: the
// digits keys show, its cycles and its estimate scale (D-90).
type WorkspaceSettings struct {
	Digits    int           `json:"digits"`
	Cycles    CycleSchedule `json:"cycles"`
	Estimates string        `json:"estimates"`
}

// WorkspaceSettingsInput changes what it sends.
type WorkspaceSettingsInput struct {
	Digits    *int           `json:"digits"`
	Cycles    *CycleSchedule `json:"cycles"`
	Estimates *string        `json:"estimates"`
}

// ProjectSettings are a project's key, digits and types.
type ProjectSettings struct {
	// version is its hidden types': what a change of them must send back.
	version         string
	Key             string   `json:"key"`
	Aliases         []string `json:"aliases"`
	Digits          int      `json:"digits"`
	WorkspaceDigits int      `json:"workspace_digits"`
	InheritDigits   bool     `json:"inherit_digits"`
	NextKey         string   `json:"next_key"`
	DefaultTypeID   *string  `json:"default_type_id"`
	HiddenTypeIDs   []string `json:"hidden_type_ids"`
	// FailedDeployIssues opens an issue for a service's run of failed
	// deploys (D-88).
	FailedDeployIssues bool `json:"failed_deploy_issues"`
}

// MyIssue is an open issue assigned to the caller, across the workspace.
type MyIssue struct {
	Key       string    `json:"key"`
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	Priority  string    `json:"priority"`
	ProjectID string    `json:"project_id"`
	Project   string    `json:"project"`
	Type      *TypeRef  `json:"type,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	DueOn     *string   `json:"due_on"`
	Cycle     *int      `json:"cycle"`
	typeID    string
}

// ProjectSettingsInput changes what it sends; Digits null inherits.
type ProjectSettingsInput struct {
	// Version is the hidden types' from the read this came from: required
	// when HiddenTypeIDs is set, which replaces the whole list.
	Version       string    `json:"version"`
	Key           *string   `json:"key"`
	Digits        *int      `json:"digits"`
	InheritDigits bool      `json:"-"`
	DefaultTypeID *string   `json:"default_type_id"`
	HiddenTypeIDs *[]string `json:"hidden_type_ids"`
	// FailedDeployIssues turns issues for failed deploys on or off (D-88).
	FailedDeployIssues *bool `json:"failed_deploy_issues"`
}

// TypeInput adds a type; TypeUpdate changes one.
type TypeInput struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Icon  string `json:"icon"`
	Color string `json:"color"`
}

type TypeUpdate struct {
	Name     *string `json:"name"`
	Icon     *string `json:"icon"`
	Color    *string `json:"color"`
	Position *int    `json:"position"`
	Archived *bool   `json:"archived"`
}

// RelationInput relates an issue to another, by its key.
type RelationInput struct {
	Kind  string `json:"kind"`
	Issue string `json:"issue"`
	// KeyProject is a project-scoped API key's project (the handler sets
	// it): the other issue must be in it.
	KeyProject string `json:"-"`
}

// CloneInput clones an issue.
type CloneInput struct {
	Title     string   `json:"title"`
	ProjectID string   `json:"project_id"`
	Keep      []string `json:"keep"`
}

// Person is an account, as the API shows it.
type Person struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Agent is the agent that acted for this person, when one did (D-102).
	Agent string `json:"agent,omitempty"`
}

func withAgent(p *Person, agent string) *Person {
	if p != nil {
		p.Agent = agent
	}
	return p
}

func person(id, name string) *Person {
	if id == "" {
		return nil
	}
	if name == "" {
		name = "a deleted account"
	}
	return &Person{ID: id, Name: name}
}

// Issue is a row of the list.
type Issue struct {
	Key       string     `json:"key"`
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	Status    string     `json:"status"`
	Priority  string     `json:"priority"`
	Assignee  *Person    `json:"assignee"`
	Labels    []string   `json:"labels"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	Work      *Work      `json:"work"`
	Type      TypeRef    `json:"type"`
	Parent    *ParentRef `json:"parent"`
	BlockedBy []string   `json:"blocked_by"`
	Progress  *Progress  `json:"progress"`
	// Its cycle, start and due dates (YYYY-MM-DD) and estimate in points,
	// a container's being its children's (D-90).
	Cycle    *CycleRef `json:"cycle"`
	StartOn  *string   `json:"start_on"`
	DueOn    *string   `json:"due_on"`
	Estimate *int      `json:"estimate"`
}

// Work is what a row shows of the work linked to it: the open pull request,
// else the merged one, else the branch pushed last.
type Work struct {
	Kind         string             `json:"kind"`
	Branch       string             `json:"branch,omitempty"`
	Number       int                `json:"number,omitempty"`
	State        string             `json:"state,omitempty"`
	RepositoryID string             `json:"repository_id,omitempty"`
	SHA          string             `json:"sha,omitempty"`
	Checks       *code.CheckSummary `json:"checks,omitempty"`
	Deploy       string             `json:"deploy,omitempty"`
}

// Detail is one issue.
type Detail struct {
	// version is its labels': what a change of them must send back.
	version string
	Issue
	Description     string     `json:"description"`
	HTML            string     `json:"html"`
	CreatedBy       *Person    `json:"created_by"`
	StatusChangedAt time.Time  `json:"status_changed_at"`
	Comments        int        `json:"comments"`
	Links           []Link     `json:"links"`
	Relations       []Relation `json:"relations"`
	Children        []Related  `json:"children"`
	// Docs are the pages that name it (Referenced in docs, D-86).
	Docs []code.DocRef `json:"docs"`
	// CarriedFrom is the cycle that carried it into its own; Cycles, a
	// container's, are its children's (D-90).
	CarriedFrom *int  `json:"carried_from"`
	Cycles      []int `json:"cycles"`
}

// Link is a branch or a pull request that names the issue's key.
type Link struct {
	Kind         string             `json:"kind"`
	Repository   string             `json:"repository"`
	RepositoryID string             `json:"repository_id"`
	Branch       string             `json:"branch,omitempty"`
	Exists       *bool              `json:"exists,omitempty"`
	Number       int                `json:"number,omitempty"`
	Title        string             `json:"title,omitempty"`
	State        string             `json:"state,omitempty"`
	HeadBranch   string             `json:"head_branch,omitempty"`
	BaseBranch   string             `json:"base_branch,omitempty"`
	MergeSHA     string             `json:"merge_sha,omitempty"`
	Checks       *code.CheckSummary `json:"checks,omitempty"`
	Deploys      []Deploy           `json:"deploys,omitempty"`
}

// Deploy is a service a merge deploys to, and whether it runs the merge.
type Deploy struct {
	ServiceID    string `json:"service_id"`
	Service      string `json:"service"`
	Environment  string `json:"environment"`
	State        string `json:"state"` // live, building, failed, waiting
	SHA          string `json:"sha,omitempty"`
	DeploymentID string `json:"deployment_id,omitempty"`
}

// TimelineItem is a comment or an event of an issue's history.
type TimelineItem struct {
	Type        string     `json:"type"` // comment, event
	ID          string     `json:"id"`
	Actor       *Person    `json:"actor"`
	CreatedAt   time.Time  `json:"created_at"`
	EditedAt    *time.Time `json:"edited_at"`
	Body        string     `json:"body,omitempty"`
	HTML        string     `json:"html,omitempty"`
	Kind        string     `json:"kind,omitempty"`
	Automatic   bool       `json:"automatic,omitempty"`
	From        string     `json:"from,omitempty"`
	To          string     `json:"to,omitempty"`
	Assignee    *Person    `json:"assignee,omitempty"`
	Branch      string     `json:"branch,omitempty"`
	PullRequest *PullRef   `json:"pull_request,omitempty"`
	Deploy      *DeployRef `json:"deploy,omitempty"`
}

// PullRef is a pull request an event names.
type PullRef struct {
	Number     int    `json:"number"`
	Title      string `json:"title"`
	Repository string `json:"repository"`
}

// DeployRef is the deploy that went live and shipped an issue.
type DeployRef struct {
	Service     string `json:"service"`
	Environment string `json:"environment"`
	SHA         string `json:"sha"`
}

// Label is a label the project's issues carry, or a service's name.
type Label struct {
	Name    string `json:"name"`
	Open    int    `json:"open"`
	Total   int    `json:"total"`
	Service bool   `json:"service"`
}

// Filters is the list's state, as its URL and a saved view hold it.
type Filters struct {
	Status   []string `json:"status,omitempty"`
	Priority []string `json:"priority,omitempty"`
	Assignee string   `json:"assignee,omitempty"`
	Label    []string `json:"label,omitempty"`
	Q        string   `json:"q,omitempty"`
	Type     []string `json:"type,omitempty"`
	Parent   string   `json:"parent,omitempty"`
	Cycle    string   `json:"cycle,omitempty"`
	Order    string   `json:"order,omitempty"`
	Group    string   `json:"group,omitempty"`
}

// View is a saved view, shared with the project.
type View struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Filters   Filters   `json:"filters"`
	Owner     *Person   `json:"owner"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Search is ⌘K's answer.
type Search struct {
	Issues []SearchIssue `json:"issues"`
	Pulls  []SearchPull  `json:"pulls"`
	Docs   []DocHit      `json:"docs"`
}

// DocHit is a page ⌘K found (D-86).
type DocHit struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Project   string `json:"project"`
	Title     string `json:"title"`
	Snippet   string `json:"snippet"`
}

type SearchIssue struct {
	ProjectID string  `json:"project_id"`
	Project   string  `json:"project"`
	Key       string  `json:"key"`
	Number    int     `json:"number"`
	Title     string  `json:"title"`
	Status    string  `json:"status"`
	Priority  string  `json:"priority"`
	Type      TypeRef `json:"type"`
	typeID    string
}

type SearchPull struct {
	ProjectID  string `json:"project_id"`
	Project    string `json:"project"`
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	Title      string `json:"title"`
	State      string `json:"state"`
}

// CreateInput opens an issue.
type CreateInput struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
	Priority    string   `json:"priority"`
	AssigneeID  string   `json:"assignee_id"`
	Labels      []string `json:"labels"`
	TypeID      string   `json:"type_id"`
	Parent      string   `json:"parent"`
	// Cycle is a cycle's number (absent: a sub-issue's parent's);
	// Estimate is in points (D-90).
	Cycle    *int   `json:"cycle"`
	StartOn  string `json:"start_on"`
	DueOn    string `json:"due_on"`
	Estimate int    `json:"estimate"`
	// KeyProject is a project-scoped API key's project (the handler sets
	// it): the parent must be in it.
	KeyProject string `json:"-"`
}

// UpdateInput changes the fields it sends.
type UpdateInput struct {
	// Version is the labels' from the read this came from: required when
	// Labels is set, which replaces the whole list.
	Version     string    `json:"version"`
	Title       *string   `json:"title"`
	Description *string   `json:"description"`
	Status      *string   `json:"status"`
	Priority    *string   `json:"priority"`
	AssigneeID  *string   `json:"assignee_id"`
	Labels      *[]string `json:"labels"`
	TypeID      *string   `json:"type_id"`
	Parent      *string   `json:"parent"`
	// 0 or "" takes the issue out of its cycle, clears a date or the
	// estimate (D-90).
	Cycle    *int    `json:"cycle"`
	StartOn  *string `json:"start_on"`
	DueOn    *string `json:"due_on"`
	Estimate *int    `json:"estimate"`
	// KeyProject is a project-scoped API key's project (the handler sets
	// it): the parent must be in it.
	KeyProject string `json:"-"`
}

// ViewInput saves a view.
type ViewInput struct {
	Name    string  `json:"name"`
	Filters Filters `json:"filters"`
}

// KeyOf is an issue's key, as STO-12, its number padded to digits
// (VLB-001 with 3; never cut).
func KeyOf(prefix string, n, digits int) string {
	s := strconv.Itoa(n)
	for len(s) < digits {
		s = "0" + s
	}
	return prefix + "-" + s
}

var validKey = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}$`)

// keyCandidates are the keys a project named name tries, in order (D-81):
// its first word's first three letters (STO for storefront), the words'
// initials, four and five letters, then the three with 2 to 9 after them.
func keyCandidates(name string) []string {
	words := strings.FieldsFunc(strings.ToUpper(name), func(r rune) bool { return r < 'A' || r > 'Z' })
	if len(words) == 0 {
		words = []string{"PRJ"}
	}
	first := words[0]
	if len(first) < 2 {
		first += "X"
	}
	var out []string
	add := func(k string) {
		if validKey.MatchString(k) && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	add(first[:min(3, len(first))])
	if len(words) > 1 {
		var b strings.Builder
		for _, w := range words[:min(5, len(words))] {
			b.WriteByte(w[0])
		}
		add(b.String())
	}
	add(first[:min(4, len(first))])
	add(first[:min(5, len(first))])
	base := first[:min(3, len(first))]
	for d := '2'; d <= '9'; d++ {
		add(base + string(d))
	}
	return out
}

// numbersIn are the issue numbers texts name with any of keys, the
// project's and its former ones (any case, word-bounded, with or without
// leading zeros: fix/sto-118-idem, "Closes VLB-004"), each once.
func numbersIn(keys []string, texts ...string) []int {
	quoted := make([]string, 0, len(keys))
	for _, k := range keys {
		if k != "" {
			quoted = append(quoted, regexp.QuoteMeta(k))
		}
	}
	if len(quoted) == 0 {
		return nil
	}
	re := regexp.MustCompile(`(?i)\b(?:` + strings.Join(quoted, "|") + `)-0*(\d{1,9})\b`)
	var out []int
	for _, t := range texts {
		for _, m := range re.FindAllStringSubmatch(t, -1) {
			n, _ := strconv.Atoi(m[1])
			if n > 0 && !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
	}
	return out
}
