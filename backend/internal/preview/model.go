// Package preview runs a pull request's preview environment (D-07, D-97):
// an environment copying the project's preview environment, whose services
// that build from the pull request's repository build from its head, moved
// by the code module's events outbox.
package preview

import (
	"errors"
	"time"
)

// SandboxMemoryMB is added to an untrusted copy's memory limit: gVisor's own
// memory sits inside the container's limit (D-96 measured about 30 MiB).
const SandboxMemoryMB = 64

// State is what the platform decided about a preview. Whether a started one
// is starting, running or failed is its deployments'.
type State string

const (
	StateWaitingApproval State = "waiting_approval"
	StateWaitingRoom     State = "waiting_room"
	StateActive          State = "active"
	StateStopped         State = "stopped"
	StateRemoving        State = "removing"
)

// Stopped reasons.
const (
	StoppedForRoom = "room"
	StoppedByHand  = "manual"
)

// Preview is a previews row.
type Preview struct {
	ID, PullID, ProjectID, SourceEnvID, EnvID string
	Untrusted                                 bool
	HeadSHA, ApprovedSHA, ApprovedBy          string
	ApprovedByName                            string
	ApprovedAt                                *time.Time
	State                                     State
	StoppedReason, Note                       string
	MigrationFiles                            []string // the migrations its diff touches, which it skips (D-144)
	MigrationsUnchecked                       bool     // its diff was too large to check, so MigrationFiles may miss some (D-147)
	CreatedAt, UpdatedAt                      time.Time
}

// Settings are a project's previews: the environment they copy ("" is off)
// and their domain ("" is sslip.io names).
type Settings struct {
	ProjectID, OrgID string
	EnvironmentID    string
	EnvironmentName  string
	Domain           string
	// Max is how many previews it runs at once (D-93, 1 to 20; 5 unless an
	// owner changed it): started ones, running, failed or stopped. One
	// waiting for approval doesn't count; one more waits for room.
	Max int
}

// Pull is a pull request as its preview needs it.
type Pull struct {
	ID, RepoID, RepoName, ProjectID, ProjectName, OrgID string
	Number                                              int
	Title, State, Base, Head, HeadSHA                   string
	AuthorID, AuthorName, MergedBy                      string
	OpenedWithKey, KeyPushed                            bool
	APIKeyName                                          string
	// AgentName is the agent that opened it, when one did (D-102).
	AgentName string
	// FirstRepository: the pull request is of the project's first
	// repository, whose previews are named pr-<n> alone.
	FirstRepository bool
}

// Untrusted reports whether p's preview waits for approval: opened, or
// pushed to, with an API key (or by someone unknown), its head branch's
// pushes before it was opened and an agent's branch included.
func (p Pull) Untrusted() bool { return p.OpenedWithKey || p.KeyPushed }

// SourceService is a service of the environment previews copy.
type SourceService struct {
	ID, Name, SourceType, RepositoryID, BuildPack, ServerID string
	Volumes, BaseDirectory, ComposeFilePath, MemoryLimit    string
	Type                                                    string
	SharedVolumes                                           bool
	Hostnames                                               []string
}

// CopyService is one of a preview's services with its newest deployment.
type CopyService struct {
	ID, Name, PreviewOf, ServerID, ServerName, RuntimeStatus, SourceType string
	DeploymentID, DeploymentStatus, DeploymentSHA, DeploymentError       string
	DeploymentCreatedAt, DeploymentFinishedAt                            *time.Time
	Hostnames                                                            []string
	// Isolation is the copy's own level or its workspace's default.
	Isolation string
}

// Actor is who caused what a preview does: a person, or "" for the
// platform itself.
type Actor struct {
	UserID, Name string
}

// Errors.
var (
	ErrNotFound       = errors.New("no preview for this pull request")
	ErrOff            = errors.New("previews are off in this project: an owner turns them on in Settings")
	ErrPullNotOpen    = errors.New("the pull request isn't open")
	ErrNotUntrusted   = errors.New("this preview doesn't wait for approval")
	ErrStaleSHA       = errors.New("that commit isn't the pull request's head any more")
	ErrNotStopped     = errors.New("the preview isn't stopped")
	ErrNotStarted     = errors.New("the preview hasn't started")
	ErrBadEnvironment = errors.New("previews copy one of the project's own environments, never a preview")
	ErrBadDomain      = errors.New("the preview domain must be a hostname of two labels or more, like preview.example.com")
	ErrBadMax         = errors.New("a project runs 1 to 20 previews at once")
	ErrLegacyNetworks = errors.New("previews need isolated app networks (TOFT_APP_NETWORKS=isolated)")
)

// View is a preview as the API shows it.
type View struct {
	PullNumber   int            `json:"pull_number"`
	RepositoryID string         `json:"repository_id"`
	Repository   string         `json:"repository"`
	Title        string         `json:"title"`
	Branch       string         `json:"branch"`
	Environment  *ViewEnv       `json:"environment"`
	Copies       string         `json:"copies"`
	Status       string         `json:"status"`
	Note         string         `json:"note,omitempty"`
	Untrusted    bool           `json:"untrusted"`
	HeadSHA      string         `json:"head_sha"`
	ApprovedSHA  string         `json:"approved_sha,omitempty"`
	ApprovedBy   string         `json:"approved_by,omitempty"`
	ApprovedAt   *time.Time     `json:"approved_at,omitempty"`
	NeedsApprove bool           `json:"needs_approval"`
	CanStart     string         `json:"blocked,omitempty"`
	Services     []ServiceView  `json:"services"`
	Reached      []string       `json:"reached"`
	Migrations   []string       `json:"migration_files"`      // the migrations the diff touches, which it skips (D-144)
	Unchecked    bool           `json:"migrations_unchecked"` // the diff was too large to check for migrations (D-147)
	Stopped      string         `json:"stopped_reason,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	Opened       *OpenedWithKey `json:"opened_with_key,omitempty"`
}

// ViewEnv is a started preview's environment.
type ViewEnv struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// OpenedWithKey names the API key a pull request was opened with.
type OpenedWithKey struct {
	Key   string `json:"key"`
	Owner string `json:"owner"`
	// Agent is the agent that opened it for Owner, when an agent did: then
	// no key did (D-102).
	Agent string `json:"agent,omitempty"`
}

// ServiceView is one of a preview's services.
type ServiceView struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Server       string     `json:"server"`
	Status       string     `json:"status"`
	SHA          string     `json:"sha,omitempty"`
	Error        string     `json:"error,omitempty"`
	DeploymentID string     `json:"deployment_id,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	Addresses    []string   `json:"addresses"`
	// Isolation is the level the copy runs at: an untrusted preview's
	// copies run strict where their server offers it (D-115).
	Isolation string `json:"isolation"`
}
