package project

import (
	"errors"
	"time"
)

// ErrDuplicateName is returned when a project name already exists within the same org.
var ErrDuplicateName = errors.New("a project with this name already exists in this workspace")

// Project is the top-level logical group for services (e.g. "my-startup").
type Project struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"org_id"`
	UserID      string    `json:"user_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Icon        string    `json:"icon"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	// IssueKey is its issues' key ("" before the first); OpenIssues and
	// OpenPulls count what isn't shipped, cancelled, merged or closed (D-81);
	// DocPages its pages, not deleted ones (D-86).
	IssueKey   string `json:"issue_key"`
	OpenIssues int    `json:"open_issues"`
	OpenPulls  int    `json:"open_pulls"`
	DocPages   int    `json:"doc_pages"`
}

type CreateProjectInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
}

type UpdateProjectInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
}
