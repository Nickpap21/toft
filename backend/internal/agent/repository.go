package agent

import (
	"context"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/actor"
)

// Code is an authorization code's grant: the consent's choices.
type Code struct {
	Hash          string
	ClientID      string // oauth_clients.id
	UserID        string
	OrgID         string
	AllProjects   bool
	ProjectIDs    []string
	Scopes        []string
	RedirectURI   string
	CodeChallenge string
	ExpiresAt     time.Time
}

// NewConnection is what a code's exchange creates, with its first tokens.
type NewConnection struct {
	Code        Code
	Name        string
	ExpiresAt   time.Time
	AccessHash  string
	AccessExp   time.Time
	RefreshHash string
}

// Live is a live connection, as a token's validation reads it.
type Live struct {
	ConnectionID string
	OrgID        string
	UserID       string
	Name         string
	AllProjects  bool
	ProjectIDs   []string
	Scopes       []string
	OrgPaused    bool
}

// ConnectionRow is one connection, live or not.
type ConnectionRow struct {
	ID        string
	OrgID     string
	UserID    string
	Name      string
	Scopes    []string
	ExpiresAt time.Time
	RevokedAt *time.Time
}

// ServiceInfo is what the policy needs to know about a service, an
// environment or a project: the target of a call.
type ServiceInfo struct {
	ID            string
	EnvironmentID string
	ProjectID     string
	Class         string
	// Label names it for a person: "storefront / api in production".
	Label string
	// Data: a database or cache (D-101), counted as protected.
	Data bool
}

// NewApproval is an approval to create.
type NewApproval struct {
	ID, OrgID, ConnectionID  string
	UserID                   string
	Action, Tool             string
	ServiceID, EnvironmentID string
	ProjectID                string
	Target                   string
	Request                  Request
	// Sealed is set_secret's value, encrypted (D-102).
	Sealed       string
	RequestHash  string
	Reason, Plan string
	PlanSteps    []actor.PlanStep
	ExpiresAt    time.Time
}

// Repository is agents' persistence.
type Repository interface {
	GetClient(ctx context.Context, clientID string) (*Client, error)
	GetClientByID(ctx context.Context, id string) (*Client, error)
	UpsertCIMDClient(ctx context.Context, clientID, name string, redirectURIs []string) (*Client, error)
	// InsertDCRClient is ErrFull with maxUnused unused registrations held.
	InsertDCRClient(ctx context.Context, clientID, name string, redirectURIs []string, maxUnused int) (*Client, error)

	InsertCode(ctx context.Context, c Code) error
	UseCode(ctx context.Context, hash string) (*Code, error)
	// CreateConnection makes the connection, its projects and its first
	// tokens in one transaction. ErrForbidden: a project isn't the
	// workspace's.
	CreateConnection(ctx context.Context, n NewConnection) (connectionID string, err error)
	// RotateRefresh uses a refresh token once and issues the next pair. A
	// second use of one revokes its connection (ErrConflict).
	RotateRefresh(ctx context.Context, hash, accessHash string, accessExp time.Time, refreshHash string) (connectionID string, scopes []string, err error)
	ValidateAccess(ctx context.Context, hash string) (*Live, error)
	InsertToken(ctx context.Context, hash, connectionID, kind string, expires time.Time) error
	DeleteToken(ctx context.Context, hash string) error
	TokenConnection(ctx context.Context, hash string) (connectionID, kind string, err error)

	GetConnection(ctx context.Context, id string) (*ConnectionRow, error)
	ListConnections(ctx context.Context, orgID, userID string) ([]Connection, error)
	// RevokeConnection ends a connection and turns down its waiting
	// approvals. ErrNotFound when it was already revoked.
	RevokeConnection(ctx context.Context, id, by, note string) (*ConnectionRow, error)
	Touch(ctx context.Context, connectionID, tool string) error

	ListPolicy(ctx context.Context, orgID string) (map[string]map[string]string, error)
	SetPolicy(ctx context.Context, orgID, by string, cells []PolicyCell) error
	ServiceInfo(ctx context.Context, serviceID string) (*ServiceInfo, error)
	EnvironmentInfo(ctx context.Context, environmentID string) (*ServiceInfo, error)
	// ProjectInfo returns a project's name and workspace.
	ProjectInfo(ctx context.Context, projectID string) (name, orgID string, err error)
	ListProjectPolicy(ctx context.Context, projectID string) (map[string]map[string]string, error)
	ListOrgProjectPolicies(ctx context.Context, orgID string) ([]ProjectPolicy, error)
	// SetProjectPolicy writes a project's cells; Decision "" removes one.
	SetProjectPolicy(ctx context.Context, projectID, by string, cells []PolicyCell) error

	InsertApproval(ctx context.Context, a NewApproval) (string, error)
	FindOpenApproval(ctx context.Context, connectionID, requestHash string) (string, error)
	GetApproval(ctx context.Context, id string) (*Approval, string, error)
	ListWaiting(ctx context.Context, orgID string) ([]Waiting, error)
	DecideApproval(ctx context.Context, id, orgID, state, by, note string) error
	ClaimRun(ctx context.Context, id, connectionID string) (bool, error)
	// SetResult keeps what a run answered, and the deployment an approved
	// deploy started ("" for none).
	SetResult(ctx context.Context, id string, r Result, deploymentID string) error
	// ClaimRollback uses the rollback a deploy's approval covers (D-146):
	// ok with that approval and its approver's name, or false when none
	// covers it now.
	ClaimRollback(ctx context.Context, connectionID, serviceID, rollbackTo string) (approvalID, approver string, ok bool, err error)
	// ReleaseRollback makes a used rollback unused again.
	ReleaseRollback(ctx context.Context, approvalID string) error
	// Sealed returns an approved request's encrypted value; ClearSealed
	// deletes it.
	Sealed(ctx context.Context, id string) (string, error)
	ClearSealed(ctx context.Context, id string) error

	SetOrgPaused(ctx context.Context, orgID, by string, paused bool, note string) error
	OrgPaused(ctx context.Context, orgID string) (*PauseState, error)
	SetInstancePaused(ctx context.Context, byID, byName string, paused bool, note string) error
	InstancePaused(ctx context.Context) (*PauseState, error)

	// Clean removes expired tokens, old codes and unused registrations.
	Clean(ctx context.Context, now time.Time) error
}
