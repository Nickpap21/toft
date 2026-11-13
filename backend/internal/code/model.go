// Package code is git hosting (D-20, D-71): projects' repositories, the SSH
// keys of accounts, who may read and push, and git's two front doors (SSH on
// port 2222 and smart HTTP). git itself runs only in internal/gitstore's
// executor.
package code

import (
	"errors"
	"time"

	"github.com/Nickpap21/toft/backend/internal/gitstore"
)

// Repo is a project's git repository as the API shows it.
type Repo struct {
	ID string `json:"id"`
	// Name is the repository's part of its URLs; FullName adds the
	// workspace's ("founders/storefront").
	Name      string    `json:"name"`
	FullName  string    `json:"full_name"`
	SSHURL    string    `json:"ssh_url"`
	HTTPSURL  string    `json:"https_url"`
	CreatedAt time.Time `json:"created_at"`
	// ProtectDefaultBranch refuses pushes to the default branch (D-75).
	ProtectDefaultBranch bool `json:"protect_default_branch"`
	OpenPulls            int  `json:"open_pulls"`
	// PullMirror is set while the repository is imported or pulls from
	// another host (D-79); it is read-only then.
	PullMirror *Mirror `json:"pull_mirror"`
}

// RepositoryDetail is a repository with what the browser opens on (D-73).
type RepositoryDetail struct {
	Repo
	gitstore.Summary
	// ProtectedBranch is the branch the push hook protects: null while
	// protection is off or no name is stored yet (the hook then protects
	// the default branch).
	ProtectedBranch *string    `json:"protected_branch"`
	Pulls           PullCounts `json:"pulls"`
	// Deploys is what builds from the repository, by branch (D-77).
	Deploys []DeployTarget `json:"deploys"`
	// AgentBranches are the branches agents made, each with "<agent> for
	// <person>"; only that agent pushes to one (D-102).
	AgentBranches map[string]string `json:"agent_branches"`
}

// Target is a repository a git client asked for.
type Target struct {
	ID        string
	OrgID     string
	ProjectID string
	// ProtectDefault: pushes to ProtectedBranch are refused; "" while on is
	// a name not stored yet (Service.Serve reads HEAD's then, 0080).
	ProtectDefault  bool
	ProtectedBranch string
	// MirrorURL is what the repository pulls from while it mirrors one:
	// every push is refused then (D-79).
	MirrorURL string
}

// SSHKey is one of an account's public keys.
type SSHKey struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Fingerprint string     `json:"fingerprint"`
	KeyType     string     `json:"key_type"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

// AddSSHKeyInput is POST /user/ssh-keys's body.
type AddSSHKeyInput struct {
	Title     string `json:"title" binding:"required,min=1,max=100"`
	PublicKey string `json:"public_key" binding:"required,max=16384"`
}

// GitInfo is GET /git: whether this instance hosts git, and what a client
// needs to reach it.
type GitInfo struct {
	Enabled bool   `json:"enabled"`
	SSHHost string `json:"ssh_host,omitempty"`
	SSHPort int    `json:"ssh_port,omitempty"`
	// HostKeyFingerprint is the SSH host key's SHA256 fingerprint, to check
	// against what ssh shows on the first connection.
	HostKeyFingerprint string `json:"host_key_fingerprint,omitempty"`
}

// Actor is who a git request acts for: an account, through an SSH key, an
// API key, or the panel's session.
type Actor struct {
	UserID string
	// ProjectID and Scopes are set for a project-scoped API key.
	ProjectID string
	Scopes    []string
	// ViaKey: the client signed in with an API key (git over HTTPS always
	// does), not a person's SSH key (D-97).
	ViaKey bool
	// An agent's push credential (D-102): the connection acting, and the
	// branch it alone may push to; Paused, its workspace's agents are.
	AgentConnectionID, AgentName, PushOnly string
	Paused                                 bool
}

// Scopes a project-scoped API key may have for code (internal/apikey).
const (
	ScopeCodeRead  = "code_read"
	ScopeCodeWrite = "code_write"
)

// CreateRepositoryInput is POST /projects/{projectID}/repositories's body.
type CreateRepositoryInput struct {
	Name string `json:"name" binding:"required,max=100"`
}

// UpdateRepositoryInput is PATCH …/repositories/{repositoryID}'s body: the
// fields to change.
type UpdateRepositoryInput struct {
	DefaultBranch        *string `json:"default_branch"`
	ProtectDefaultBranch *bool   `json:"protect_default_branch"`
}

// Person is who opened, merged or commented: an account, by name.
type Person struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Agent is the agent that acted for this person, when one did (D-102).
	Agent string `json:"agent,omitempty"`
}

// PullRequest is a pull request as the API shows it (D-75).
type PullRequest struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	// HTML is Body rendered and sanitised (gitstore.Markdown).
	HTML        string     `json:"html"`
	State       string     `json:"state"`
	Author      *Person    `json:"author"`
	BaseBranch  string     `json:"base_branch"`
	HeadBranch  string     `json:"head_branch"`
	HeadSHA     string     `json:"head_sha"`
	MergeMethod string     `json:"merge_method,omitempty"`
	MergeSHA    string     `json:"merge_sha,omitempty"`
	MergedBy    *Person    `json:"merged_by"`
	MergedAt    *time.Time `json:"merged_at"`
	ClosedAt    *time.Time `json:"closed_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	Comments    int        `json:"comments"`
	// Commits, Ahead/Behind and Conflicts are the list's view of an open
	// one's branches: null when a branch is gone or there was no time.
	Ahead     *int  `json:"ahead"`
	Behind    *int  `json:"behind"`
	Conflicts *bool `json:"conflicts"`
	// Checks is the head commit's checks together (D-77).
	Checks *CheckSummary `json:"checks"`
}

// PullRequestDetail is one pull request with what its page needs: its
// comparison (commits, files, mergeability) and whether its head branch
// still exists.
type PullRequestDetail struct {
	PullRequest
	HeadExists bool `json:"head_exists"`
	// HeadKept: its head is the repository's default branch or a service
	// deploys from it, so a merge can't delete it.
	HeadKept bool              `json:"head_kept"`
	Compare  *gitstore.Compare `json:"compare"`
	// HeadChecks are the head commit's checks; Deploys what builds from the
	// base branch, with how many of the files each covers (D-77).
	HeadChecks *CommitChecks  `json:"head_checks"`
	Deploys    []DeployTarget `json:"deploys"`
	// Issues are those whose key its head branch, title or description
	// names (D-81).
	Issues []IssueRef `json:"issues"`
	// Docs are the pages that name it (Referenced in docs, D-86).
	Docs []DocRef `json:"docs"`
	// AgentBranch is set when its head branch is an agent's (D-102).
	AgentBranch *PullAgentBranch `json:"agent_branch,omitempty"`
}

// PullAgentBranch is the agent whose branch a pull request merges, for its
// person, and when the branch's push password expires.
type PullAgentBranch struct {
	Agent             string    `json:"agent"`
	Person            string    `json:"person"`
	PasswordExpiresAt time.Time `json:"password_expires_at"`
}

// DocRef is a page, where something is named in the docs (D-86).
type DocRef struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	ProjectID string `json:"project_id"`
}

// IssueRef is an issue a pull request or a branch names (D-81).
type IssueRef struct {
	Key    string `json:"key"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// PullCounts is how many pull requests a repository has by state.
type PullCounts struct {
	Open   int `json:"open"`
	Merged int `json:"merged"`
	Closed int `json:"closed"`
}

// OpenPullInput is POST …/pulls's body.
type OpenPullInput struct {
	Title string `json:"title" binding:"required,max=256"`
	Body  string `json:"body" binding:"max=65536"`
	Base  string `json:"base" binding:"required,max=255"`
	Head  string `json:"head" binding:"required,max=255"`
	// ViaKey and APIKeyID: opened with that API key (the handler sets them;
	// D-97).
	ViaKey   bool   `json:"-"`
	APIKeyID string `json:"-"`
}

// UpdatePullInput is PATCH …/pulls/{pullNumber}'s body.
type UpdatePullInput struct {
	Title *string `json:"title" binding:"omitempty,max=256"`
	Body  *string `json:"body" binding:"omitempty,max=65536"`
	State *string `json:"state" binding:"omitempty,oneof=open closed"`
}

// MergeInput is POST …/pulls/{pullNumber}/merge's body.
type MergeInput struct {
	Method string `json:"method" binding:"required,oneof=merge squash fast_forward"`
	// ExpectedHeadSHA is the head the person merging saw.
	ExpectedHeadSHA string `json:"expected_head_sha" binding:"required,len=40|len=64,hexadecimal"`
	DeleteBranch    bool   `json:"delete_branch"`
}

// CommentInput is a comment's body.
type CommentInput struct {
	Body string `json:"body" binding:"required,max=65536"`
}

// TimelineItem is a comment or an event of a pull request's conversation.
type TimelineItem struct {
	Type      string     `json:"type"` // comment, event
	ID        string     `json:"id"`
	Actor     *Person    `json:"actor"`
	CreatedAt time.Time  `json:"created_at"`
	EditedAt  *time.Time `json:"edited_at"`
	// A comment's text and its rendering.
	Body string `json:"body,omitempty"`
	HTML string `json:"html,omitempty"`
	// An event's kind (pushed, closed, reopened, merged, head_deleted,
	// title_changed) and details.
	Kind string         `json:"kind,omitempty"`
	Data map[string]any `json:"data,omitempty"`
}

var (
	ErrNotFound     = errors.New("not found")
	ErrNameTaken    = errors.New("this workspace already has a repository by that name")
	ErrBadName      = errors.New("a repository name is lowercase letters, digits, dots, dashes and underscores, starting and ending with a letter or digit, and not ending in .git")
	ErrLastRepo     = errors.New("a project keeps at least one repository")
	ErrPullExists   = errors.New("an open pull request already merges this branch into that one")
	ErrPullClosed   = errors.New("this pull request is merged or closed")
	ErrSameBranch   = errors.New("pick two different branches")
	ErrNoChanges    = errors.New("the base branch already has every commit of this branch")
	ErrBadTitle     = errors.New("a title has 1 to 256 characters")
	ErrNotYours     = errors.New("only its author can edit a comment")
	ErrHeadGone     = errors.New("the pull request's branch was deleted: push it again to reopen it")
	ErrConfirmName  = errors.New("type the repository's name to confirm")
	ErrBadCursor    = errors.New("cursor is not one this API issued")
	ErrBadState     = errors.New("state is open, merged or closed")
	ErrNotBranches  = errors.New("a pull request goes from one branch into another: pick branches, not tags or commits")
	ErrEmptyComment = errors.New("a comment can't be empty")
	ErrHeadKept     = errors.New("this branch is the repository's default branch or a service deploys from it: merge without deleting it")
	ErrHeadMoved    = errors.New("the branch got new commits since you looked: check them, then merge again")
	ErrForbidden    = errors.New("you may read this repository but not push to it")
	ErrGitOff       = errors.New("git hosting is turned off on this instance")
	ErrInvalidKey   = errors.New("that is not an SSH public key: paste one line from a .pub file, such as ~/.ssh/id_ed25519.pub")
	ErrWeakKey      = errors.New("that key is too weak: use ed25519, ECDSA or RSA of 2048 bits or more")
	ErrKeyInUse     = errors.New("that key is already added to an account on this instance")
	ErrTooManyKeys  = errors.New("an account can have 50 SSH keys: remove one first")
)

// PullExistsError is ErrPullExists with the open pull request's number.
type PullExistsError struct{ Number int }

func (e *PullExistsError) Error() string { return ErrPullExists.Error() }
func (e *PullExistsError) Unwrap() error { return ErrPullExists }
