package deployment

import "time"

type Status string

const (
	StatusPending  Status = "pending"
	StatusBuilding Status = "building"
	StatusRunning  Status = "running"
	StatusFailed   Status = "failed"
	StatusStopped  Status = "stopped"
)

// Deployment represents a single run/build attempt for a service.
type Deployment struct {
	ID        string `json:"id"`
	ServiceID string `json:"service_id"`
	Status    Status `json:"status"`
	ImageTag  string `json:"image_tag,omitempty"`
	CommitSHA string `json:"commit_sha,omitempty"`
	// CommitMessage is a push's head commit subject; TriggeredBy is how and
	// by whom ("Manual · Alex", "Push · alex"). Empty before they were recorded.
	CommitMessage string `json:"commit_message,omitempty"`
	TriggeredBy   string `json:"triggered_by,omitempty"`
	// ComposeCommitSHA is the commit a compose service's repository was
	// checked out at; a rollback checks it out again.
	ComposeCommitSHA string `json:"compose_commit_sha,omitempty"`
	// PullRequest is the pull request whose merge started it (D-77).
	PullRequest *PullRef   `json:"pull_request,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	ErrorMsg    string     `json:"error_msg,omitempty"`
	// ImageRemoved: a disk clean removed its image (D-135), so it can't be
	// rolled back to.
	ImageRemoved bool      `json:"image_removed"`
	CreatedAt    time.Time `json:"created_at"`
	// SettingsFingerprint is what it shipped of its service's settings
	// (SettingsFingerprint, D-159); "" when unknown.
	SettingsFingerprint string `json:"-"`
}

// PullRef is a pull request of one of the project's repositories.
type PullRef struct {
	Number       int    `json:"number"`
	RepositoryID string `json:"repository_id"`
	// Issues are those linked to it, which the deploy ships once live
	// (D-81).
	Issues []IssueRef `json:"issues"`
}

// IssueRef is an issue a deploy ships.
type IssueRef struct {
	Key    string `json:"key"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// Log is a single line emitted during a deployment build.
type Log struct {
	ID           int64     `json:"id"`
	DeploymentID string    `json:"deployment_id"`
	Level        string    `json:"level"` // info | error | debug
	Message      string    `json:"message"`
	CreatedAt    time.Time `json:"created_at"`
}
