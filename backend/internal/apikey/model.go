package apikey

import (
	"encoding/json"
	"errors"
	"time"
)

// Scopes a project-scoped key can have (internal/apikey/scope.go lists what
// each allows).
const (
	ScopeDeploy    = "deploy"
	ScopeVariables = "variables"
	// Clone and fetch the project's repositories; push too (D-71). Read by
	// internal/code for git's own front doors; the browser's routes are in
	// scope.go, where code_write includes code_read (D-73).
	ScopeCodeRead  = "code_read"
	ScopeCodeWrite = "code_write"
	// Read and report a commit's checks, and nothing else: what an
	// external CI holds (D-77).
	ScopeChecks = "checks"
	// Read a project's issues; issues_write opens, changes and comments on
	// them too (D-81).
	ScopeIssuesRead  = "issues_read"
	ScopeIssuesWrite = "issues_write"
	// Read a project's docs; docs_write writes, moves, deletes and verifies
	// pages too. Neither runs a page's commands (D-86).
	ScopeDocsRead  = "docs_read"
	ScopeDocsWrite = "docs_write"
)

var knownScopes = map[string]bool{ScopeDeploy: true, ScopeVariables: true, ScopeCodeRead: true, ScopeCodeWrite: true, ScopeChecks: true,
	ScopeIssuesRead: true, ScopeIssuesWrite: true, ScopeDocsRead: true, ScopeDocsWrite: true}

// ErrInvalidScope is a project without scopes, scopes without a project, or
// an unknown scope.
// ErrNotFound is a key that isn't the user's, or doesn't exist.
var ErrNotFound = errors.New("api key not found")

var ErrInvalidScope = errors.New(`a project-scoped key needs project_id and one or more scopes ("deploy", "variables", "code_read", "code_write", "checks", "issues_read", "issues_write", "docs_read", "docs_write"); a full-access key has neither`)

// ErrInvalidServices is service_ids that aren't each once a service of the
// key's project, or a key limited to services with a scope other than
// deploy and variables (code, checks, issues and docs are project-wide).
var ErrInvalidServices = errors.New(`service_ids must name services of the key's project, each once, and a key limited to services may only have the "deploy" and "variables" scopes`)

// ErrInvalidExpiry is an expires_at that isn't a date-time in the future.
var ErrInvalidExpiry = errors.New("expires_at must be a date-time in the future, or null for a key that never expires")

// DefaultExpiry is how long a key lasts when expires_at is left out.
const DefaultExpiry = 90 * 24 * time.Hour

// ProjectKey is a key scoped to a project, as the project's Settings lists
// it: never the key itself.
type ProjectKey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	OwnerID    string     `json:"owner_id"`
	OwnerName  string     `json:"owner_name"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
}

// APIKey represents a long-lived personal access token for CI/CD use.
type APIKey struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	Name      string `json:"name"`
	Prefix    string `json:"prefix"`               // "paas_XXXXXXXX" — safe to display
	ProjectID string `json:"project_id,omitempty"` // set for a project-scoped key
	// ProjectName and OrgName name a project key's project and its
	// workspace, which may not be the one open in the panel. List only.
	ProjectName string     `json:"project_name,omitempty"`
	OrgName     string     `json:"org_name,omitempty"`
	Scopes      []string   `json:"scopes"`       // empty for a full-access key
	LastUsedAt  *time.Time `json:"last_used_at"` // nil if never used
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at"` // nil: never expires
	// ServiceIDs limit a project key to those services; empty is the whole
	// project. Services are the ones that still exist, named.
	ServiceIDs []string     `json:"service_ids"`
	Services   []KeyService `json:"services"`
}

// KeyService is a service a key is limited to.
type KeyService struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	EnvironmentName string `json:"environment_name"` // "" for a service shared by the project
	ProjectID       string `json:"-"`
}

// CreateInput is the request body for creating an API key.
type CreateInput struct {
	Name string `json:"name" binding:"required,min=1,max=64"`
	// ProjectID and Scopes make a project-scoped key; both or neither.
	ProjectID string   `json:"project_id"`
	Scopes    []string `json:"scopes"`
	// ServiceIDs limit a project key to some of its services.
	ServiceIDs []string `json:"service_ids"`
	ExpiresAt  Expiry   `json:"expires_at"`
}

// Expiry is expires_at as the client sent it: left out (Set false) is the
// default, null (Time nil) is never. They differ on purpose, so a client
// that predates the field gets the default.
type Expiry struct {
	Set  bool
	Time *time.Time
}

// UnmarshalJSON is called for null too (Expiry is not a pointer).
func (e *Expiry) UnmarshalJSON(b []byte) error {
	e.Set = true
	if string(b) == "null" {
		return nil
	}
	var t time.Time
	if err := json.Unmarshal(b, &t); err != nil {
		return ErrInvalidExpiry
	}
	e.Time = &t
	return nil
}

// AuthKey is who a key acts for, as the auth middleware needs it.
type AuthKey struct {
	ID           string
	UserID       string
	ProjectID    string // "" for a full-access key
	ProjectOrgID string // the project's workspace
	Scopes       []string
	ServiceIDs   []string
	ExpiresAt    *time.Time
}

// CreatedResponse is returned once on key creation. The full Key is never returned again.
type CreatedResponse struct {
	APIKey
	Key string `json:"key"` // full raw key — shown only once
}

// RevokedKey is a deleted key; ProjectOrgID is a project key's workspace,
// whose audit log records the revocation.
type RevokedKey struct {
	APIKey
	ProjectOrgID string
}
