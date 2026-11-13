package environment

import "time"

// Environment groups services within a project (e.g. "production", "staging").
type Environment struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	// Color is its chip's colour, one of Colors; Protected makes changes
	// to it (deploy, stop, restart, delete, replace a secret) confirm
	// first (D-55).
	Color     string    `json:"color"`
	Protected bool      `json:"protected"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Preview is set on a pull request's preview environment (D-97): it
	// changes with its pull request, never by hand.
	Preview *PreviewOf `json:"preview,omitempty"`
}

// PreviewOf is the pull request a preview environment is of.
type PreviewOf struct {
	PullNumber   int    `json:"pull_number"`
	RepositoryID string `json:"repository_id"`
}

// Colors an environment's chip can take. Red left them (D-142): it means
// failing. The schema's CHECK still allows it until contract.sql tightens
// it in the next release, since the previous release's code writes red.
const Colors = "amber violet blue teal gray"

// CreateInput is the request body for creating a new environment. Color
// and Protected left out are set from the name (defaultsFor).
type CreateInput struct {
	Name      string  `json:"name" binding:"required,max=100"`
	Color     *string `json:"color" binding:"omitempty,oneof=amber violet blue teal gray"`
	Protected *bool   `json:"protected"`
}

// UpdateInput is the PATCH body: a field left out keeps its value.
type UpdateInput struct {
	Name      *string `json:"name" binding:"omitempty,min=1,max=100"`
	Color     *string `json:"color" binding:"omitempty,oneof=amber violet blue teal gray"`
	Protected *bool   `json:"protected"`
	// WasProtected, when set, applies the update only while the
	// environment's protection is still this (ErrProtectionChanged).
	WasProtected *bool `json:"-"`
}

// Overview is what the Environment page shows beside the environment
// itself: its app network and, per service, what it overrides, its domains
// and its latest deployment.
type Overview struct {
	// Network is the environment's own network, under isolated app
	// networks; "" under legacy, where apps share the control plane's.
	Network  string            `json:"network"`
	Services []ServiceOverview `json:"services"`
}

// ServiceOverview is one service of the environment.
type ServiceOverview struct {
	ServiceID string `json:"service_id"`
	// OwnVariables and OwnSecrets count what the service sets itself,
	// overrides included.
	OwnVariables int `json:"own_variables"`
	OwnSecrets   int `json:"own_secrets"`
	// Overrides are the shared keys it sets itself, so its value wins.
	Overrides      []string        `json:"overrides"`
	Domains        []string        `json:"domains"`
	LastDeployment *LastDeployment `json:"last_deployment"`
}

// LastDeployment is a service's latest deployment.
type LastDeployment struct {
	ID         string     `json:"id"`
	Status     string     `json:"status"`
	CommitSHA  string     `json:"commit_sha"`
	ImageTag   string     `json:"image_tag"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

// ProjectOverview is what a project's Overview shows beside its services:
// its environments with their shared keys counted, each service's domains
// and latest deployment, and the project's latest deployments.
type ProjectOverview struct {
	Environments      []EnvironmentCounts      `json:"environments"`
	Services          []ProjectServiceOverview `json:"services"`
	RecentDeployments []RecentDeployment       `json:"recent_deployments"`
}

// EnvironmentCounts is an environment with how many shared variables and
// secrets it has.
type EnvironmentCounts struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	Protected bool   `json:"protected"`
	Variables int64  `json:"variables"`
	Secrets   int64  `json:"secrets"`
}

// ProjectServiceOverview is one service of a project, from any of its
// environments or shared by the project.
type ProjectServiceOverview struct {
	ServiceID      string          `json:"service_id"`
	Domains        []string        `json:"domains"`
	LastDeployment *LastDeployment `json:"last_deployment"`
}

// RecentDeployment is one of a project's latest deployments.
type RecentDeployment struct {
	ID              string     `json:"id"`
	ServiceID       string     `json:"service_id"`
	ServiceName     string     `json:"service_name"`
	EnvironmentName string     `json:"environment_name"` // "" for a project service
	Status          string     `json:"status"`
	CommitSHA       string     `json:"commit_sha"`
	CommitMessage   string     `json:"commit_message"`
	TriggeredBy     string     `json:"triggered_by"`
	ImageTag        string     `json:"image_tag"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at"` // nil: never started (refused)
	FinishedAt      *time.Time `json:"finished_at"`
}

// WorkspaceEnvironment is an environment as the Projects table and the
// workspace services' reach list it.
type WorkspaceEnvironment struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	Protected bool   `json:"protected"`
	// Preview: a pull request's preview environment (D-97).
	Preview bool `json:"preview"`
}
