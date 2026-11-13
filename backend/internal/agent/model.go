// Package agent is agents' access to Toft (D-100): Toft as the OAuth
// authorization server agents connect through, the connections and tokens
// that come of a person's consent, the workspace's agent policy, the
// approvals Ask creates, and pause-all.
package agent

import (
	"errors"
	"time"
)

// Scopes a connection may hold (D-100, D-102), the abilities on the
// consent screen.
const (
	ScopeRead      = "read"
	ScopeDeploy    = "deploy"
	ScopeCode      = "code"
	ScopeIssues    = "issues"
	ScopeVariables = "variables"
	ScopeRunbooks  = "runbooks"
)

// ConsentScopes are what the consent screen offers, in its order.
var ConsentScopes = []string{ScopeRead, ScopeDeploy, ScopeCode, ScopeIssues, ScopeVariables, ScopeRunbooks}

// Policy actions, environment classes and decisions.
const (
	ActionDeploy    = "deploy"
	ActionCode      = "code"
	ActionIssues    = "issues"
	ActionVariables = "variables"
	ActionSecrets   = "secrets"
	ActionRunbooks  = "runbooks"

	ClassPreview   = "preview"
	ClassOther     = "other"
	ClassProtected = "protected"
	// ClassAny is the one cell of an action that touches no environment:
	// a branch, a pull request, an issue (D-102).
	ClassAny = "any"

	Allow = "allow"
	Ask   = "ask"
	Never = "never"
)

// Classes in the order the policy shows them.
var Classes = []string{ClassPreview, ClassOther, ClassProtected}

// PolicyActions are the editable rows, in order. Reads are always Allow and
// not a row (D-100).
var PolicyActions = []string{ActionDeploy, ActionCode, ActionIssues, ActionVariables, ActionSecrets, ActionRunbooks}

// ActionClasses are each row's cells.
var ActionClasses = map[string][]string{
	ActionDeploy: Classes, ActionCode: {ClassAny}, ActionIssues: {ClassAny},
	ActionVariables: Classes, ActionSecrets: Classes, ActionRunbooks: Classes,
}

// DefaultPolicy is what a workspace that changed nothing gets.
var DefaultPolicy = map[string]map[string]string{
	ActionDeploy:    {ClassPreview: Allow, ClassOther: Allow, ClassProtected: Ask},
	ActionCode:      {ClassAny: Allow},
	ActionIssues:    {ClassAny: Allow},
	ActionVariables: {ClassPreview: Allow, ClassOther: Ask, ClassProtected: Ask},
	ActionSecrets:   {ClassPreview: Ask, ClassOther: Ask, ClassProtected: Ask},
	ActionRunbooks:  {ClassPreview: Ask, ClassOther: Ask, ClassProtected: Ask},
}

// ToolLimits are each act tool's calls an hour per connection (D-102),
// counted by the policy whether the call runs or asks.
var ToolLimits = map[string]int{
	"create_branch": 20, "open_pr": 10, "create_issue": 20, "comment": 60,
	"set_env_var": 60, "set_secret": 20, "run_runbook_step": 30, "create_preview": 20,
}

// Levels whose rule decides a call.
const (
	LevelProject   = "project"
	LevelWorkspace = "workspace"
	LevelDefault   = "default"
)

const (
	AccessTokenPrefix  = "toft_oat_"
	RefreshTokenPrefix = "toft_ort_"

	AccessTTL     = time.Hour
	ConnectionTTL = 90 * 24 * time.Hour
	CodeTTL       = 10 * time.Minute
	// MaxUnusedDCR is how many dynamic registrations nobody has connected
	// with the instance holds (registration needs no account); Clean frees
	// them a day old.
	MaxUnusedDCR = 1000
	ApprovalTTL  = 30 * time.Minute
	// internalTTL is the life of the token Approve runs a request with.
	internalTTL = 2 * time.Minute
)

var (
	ErrNotFound  = errors.New("not found")
	ErrForbidden = errors.New("forbidden")
	ErrConflict  = errors.New("conflict")
	// ErrFull is MaxUnusedDCR reached.
	ErrFull = errors.New("full")
)

// OAuthError is an OAuth error response (RFC 6749 §5.2): code is the
// standard's error, desc a sentence for a person.
type OAuthError struct {
	Status int
	Code   string
	Desc   string
	// RetryAfter, in seconds, is sent as Retry-After when set.
	RetryAfter int
}

func (e *OAuthError) Error() string { return e.Code + ": " + e.Desc }

func oauthErr(status int, code, desc string) *OAuthError {
	return &OAuthError{Status: status, Code: code, Desc: desc}
}

// Client is an OAuth client: a Client ID Metadata Document's URL ("cimd")
// or a dynamically registered one ("dcr").
type Client struct {
	ID           string
	ClientID     string
	Kind         string
	Name         string
	RedirectURIs []string
	FetchedAt    time.Time
}

// Connection is one consent, as the Agents page lists it.
type Connection struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	ClientKind  string     `json:"client_kind"`
	ClientRef   string     `json:"client"`
	UserID      string     `json:"user_id"`
	UserName    string     `json:"user_name"`
	UserEmail   string     `json:"user_email"`
	UserRole    string     `json:"user_role"`
	AllProjects bool       `json:"all_projects"`
	Projects    []string   `json:"projects"`
	Scopes      []string   `json:"scopes"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	LastTool    *string    `json:"last_tool"`
}

// PolicyCell is one decision of the policy.
type PolicyCell struct {
	Action   string `json:"action"`
	Class    string `json:"env_class"`
	Decision string `json:"decision"`
	Default  string `json:"default"`
}

// Policy is the workspace's policy, every cell filled, and the projects
// with rules of their own.
type Policy struct {
	Cells    []PolicyCell    `json:"cells"`
	Projects []ProjectPolicy `json:"projects"`
}

// ProjectPolicy is a project's own cells (D-101); the rest follow the
// workspace.
type ProjectPolicy struct {
	ProjectID string       `json:"project_id"`
	Name      string       `json:"name"`
	Cells     []PolicyCell `json:"cells"`
}

// Pause says whether agents are paused, where, since when and by whom.
type Pause struct {
	Workspace *PauseState `json:"workspace"`
	Instance  *PauseState `json:"instance"`
}

type PauseState struct {
	At time.Time `json:"at"`
	By string    `json:"by"`
}

// Request is the operation an approval runs: what the agent's call was.
type Request struct {
	Method string         `json:"method"`
	Path   string         `json:"path"`
	Body   map[string]any `json:"body,omitempty"`
}

// Approval is an agent's call waiting for a person, or decided.
type Approval struct {
	ID            string  `json:"id"`
	ConnectionID  string  `json:"connection_id"`
	AgentName     string  `json:"agent"`
	UserID        string  `json:"user_id"`
	UserName      string  `json:"user_name"`
	UserRole      string  `json:"user_role"`
	Action        string  `json:"action"`
	Tool          string  `json:"tool"`
	ServiceID     *string `json:"service_id"`
	EnvironmentID *string `json:"environment_id"`
	ProjectID     *string `json:"project_id"`
	Target        string  `json:"target"`
	// EnvClass is the class the policy reads for its target now: preview,
	// other, protected (a database or cache counts as protected), or any.
	EnvClass string  `json:"env_class"`
	Request  Request `json:"request"`
	Reason   string  `json:"reason"`
	Plan     string  `json:"plan"`
	// PlanSteps is the plan in steps, each with Toft's mark (D-146).
	PlanSteps []PlanStep `json:"plan_steps"`
	// Rollback is what a deploy's approval also allows (D-146).
	Rollback *RollbackAllowance `json:"rollback"`
	// State: waiting, approved, denied, cancelled, or expired (waiting
	// past ExpiresAt).
	State      string     `json:"state"`
	ExpiresAt  time.Time  `json:"expires_at"`
	DecidedBy  string     `json:"decided_by"`
	DecidedAt  *time.Time `json:"decided_at"`
	Note       string     `json:"note"`
	ExecutedAt *time.Time `json:"executed_at"`
	Result     *Result    `json:"result"`
	CreatedAt  time.Time  `json:"created_at"`

	facts rollbackFacts
}

// PlanStep is a step of the agent's plan with Toft's mark: this_request,
// allowed_by_this_approval, will_ask_again, refused, or nil (D-146).
type PlanStep struct {
	Text      string  `json:"text"`
	Tool      string  `json:"tool"`
	ServiceID *string `json:"service_id"`
	Mark      *string `json:"mark"`
}

// Plan step marks.
const (
	MarkThisRequest = "this_request"
	MarkCovered     = "allowed_by_this_approval"
	MarkAskAgain    = "will_ask_again"
	MarkRefused     = "refused"
)

// RollbackWindow is how long after an approved deploy goes live its
// approval also covers rolling back to what was live before (D-146).
const RollbackWindow = 30 * time.Minute

// RollbackAllowance is the rollback a deploy's approval covers: back to
// ToDeploymentID, once, until Until (nil until the deploy goes live).
type RollbackAllowance struct {
	ToDeploymentID string     `json:"to_deployment_id"`
	ToCommit       string     `json:"to_commit"`
	Commit         string     `json:"commit"`
	LiveAt         *time.Time `json:"live_at"`
	Until          *time.Time `json:"until"`
	UsedAt         *time.Time `json:"used_at"`
	Active         bool       `json:"active"`
}

// rollbackFacts is what the repository reads for an approval's allowance;
// the service decides from it (rollbackOf).
type rollbackFacts struct {
	to, commit  string
	usedAt      *time.Time
	depStatus   string
	depCommit   string
	depFinished *time.Time
	depLive     bool
	deciderRole string
}

// Result is what running an approved request answered.
type Result struct {
	Status int            `json:"status"`
	Body   map[string]any `json:"body,omitempty"`
}

// Waiting is an approval in a list (Needs you, the Agents page).
type Waiting struct {
	ID        string    `json:"id"`
	AgentName string    `json:"agent"`
	UserID    string    `json:"user_id"`
	UserName  string    `json:"user_name"`
	Action    string    `json:"action"`
	Tool      string    `json:"tool"`
	ServiceID *string   `json:"service_id"`
	Target    string    `json:"target"`
	Reason    string    `json:"reason"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}
