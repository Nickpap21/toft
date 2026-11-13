package org

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	RoleOwner     = "owner"
	RoleDeveloper = "developer"
	RoleViewer    = "viewer"
)

// Organization is the top-level multi-tenant workspace.
type Organization struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Member represents a user's membership in an org with their resolved profile fields.
type Member struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	UserID    string    `json:"user_id"`
	UserEmail string    `json:"user_email"`
	UserName  string    `json:"user_name"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

// AddableAccount is a suggestion for "Has an account here" (D-58): a former
// member of the workspace, or (for an operator) any account on the instance.
type AddableAccount struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	// Former is true for someone who was a member; FormerRole and LeftAt say
	// what they were and when they went. Both empty otherwise.
	Former     bool       `json:"former"`
	FormerRole string     `json:"former_role"`
	LeftAt     *time.Time `json:"left_at"`
}

// Invite is a token-based link that grants membership upon redemption.
type Invite struct {
	ID        string     `json:"id"`
	OrgID     string     `json:"org_id"`
	CreatedBy string     `json:"created_by"`
	Token     string     `json:"token"`
	Role      string     `json:"role"`
	ExpiresAt time.Time  `json:"expires_at"`
	UsedAt    *time.Time `json:"used_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	// CreatorActive reports whether CreatedBy is still an active account. An
	// invite from a deactivated user cannot be redeemed. Only GetInviteByToken
	// fills it in; never serialised.
	CreatorActive bool `json:"-"`
	// CreatorName is CreatedBy's name, for the invite page. Only
	// GetInviteByToken fills it in; never serialised.
	CreatorName string `json:"-"`
}

// AuditEntry is a single line in the org audit log.
type AuditEntry struct {
	ID         string `json:"id"`
	OrgID      string `json:"org_id"`
	ActorID    string `json:"actor_id"`
	ActorName  string `json:"actor"`
	ActorEmail string `json:"actor_email"`
	Action     string `json:"action"`
	// Resource names what the entry is about ("storefront / web"), as
	// Target.Name does.
	Resource  string      `json:"resource"`
	CreatedAt string      `json:"created_at"`
	Target    AuditTarget `json:"target"`
	// Detail is what the action recorded with it (a role, rules, a key's
	// scope); {} for most actions and for older entries.
	Detail json.RawMessage `json:"detail"`
	// The agent connection that acted for the actor, or the API key the
	// actor used (D-100); nil for a session and for older entries. A
	// revoked key's name is gone, its ID stays.
	AgentID    *string `json:"agent_id"`
	AgentName  *string `json:"agent_name"`
	APIKeyID   *string `json:"api_key_id"`
	APIKeyName *string `json:"api_key_name"`
}

// AuditTarget is what an entry is about, resolved to names: a service (or
// a deployment's), a server, a project, an environment, a member, an API
// key, an invite, the workspace, or the panel host.
type AuditTarget struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
	// Deleted: the object is gone; Name is what the entry recorded, or its ID.
	Deleted     bool   `json:"deleted"`
	ProjectID   string `json:"project_id,omitempty"`
	ServiceID   string `json:"service_id,omitempty"`
	Environment string `json:"environment,omitempty"`
	// OtherWorkspace is a server share's other workspace.
	OtherWorkspace string `json:"other_workspace,omitempty"`
}

// AuditRow is an audit_log row with the names its target resolves to;
// nil where the object is gone or the kind doesn't apply.
type AuditRow struct {
	Kind, Target                         string
	Detail                               map[string]any
	ServiceID, ServiceName               *string
	ServiceProjectID, ServiceProjectName *string
	ServiceEnvironment                   *string
	ServerName, ProjectName              *string
	EnvironmentName                      *string
	EnvironmentProjectID                 *string
	EnvironmentProjectName               *string
	UserName, KeyName, OtherOrgName      *string
	// A repository's or a docs page's, with its project (D-104).
	RepositoryName, RepositoryProjectID *string
	RepositoryProjectName               *string
	DocPageTitle, DocPageProjectID      *string
	DocPageProjectName                  *string
}

// TargetOf names what an audit entry is about. action tells the
// workspace's alert webhook apart from the workspace itself.
func TargetOf(action string, r AuditRow) AuditTarget {
	t := AuditTarget{Kind: r.Kind, ID: r.Target}
	recorded, _ := r.Detail["name"].(string)
	gone := func(alive *string) {
		if alive != nil {
			t.Name = *alive
			return
		}
		t.Deleted = true
		t.Name = recorded
		if t.Name == "" {
			t.Name = r.Target
		}
	}
	switch r.Kind {
	case "service", "deployment":
		if r.ServiceName == nil {
			gone(nil)
			return t
		}
		t.Name = *r.ServiceName
		if r.ServiceProjectName != nil {
			t.Name = *r.ServiceProjectName + " / " + t.Name
		}
		t.ServiceID = deref(r.ServiceID)
		t.ProjectID = deref(r.ServiceProjectID)
		t.Environment = deref(r.ServiceEnvironment)
	case "server":
		gone(r.ServerName)
		t.OtherWorkspace = deref(r.OtherOrgName)
	case "project":
		gone(r.ProjectName)
		if !t.Deleted {
			t.ProjectID = r.Target
		}
	case "environment":
		if r.EnvironmentName != nil && r.EnvironmentProjectName != nil {
			t.Name = *r.EnvironmentProjectName + " / " + *r.EnvironmentName
			t.ProjectID = deref(r.EnvironmentProjectID)
		} else {
			gone(nil)
		}
	case "repository":
		if r.RepositoryName != nil && r.RepositoryProjectName != nil {
			t.Name = *r.RepositoryProjectName + " / " + *r.RepositoryName
			t.ProjectID = deref(r.RepositoryProjectID)
		} else {
			gone(nil)
		}
	case "doc_page":
		if r.DocPageTitle != nil && r.DocPageProjectName != nil {
			t.Name = *r.DocPageProjectName + " / " + *r.DocPageTitle
			t.ProjectID = deref(r.DocPageProjectID)
		} else {
			gone(nil)
		}
	case "user":
		gone(r.UserName)
	case "apikey":
		gone(r.KeyName)
	case "invite":
		t.Name = "Invite link"
	case "org":
		if strings.HasPrefix(action, "alerts.") {
			t.Name = "Alert webhook"
		} else {
			t.Name = "the workspace"
		}
	case "instance":
		t.Name = "Panel host"
	case "agent":
		// An agent connection (D-100): the name it had when allowed.
		t.Name = recorded
		if t.Name == "" {
			t.Name = "an agent"
		}
	case "agent_approval":
		t.Name, _ = r.Detail["target"].(string)
		if t.Name == "" {
			t.Name = "an agent's request"
		}
	case "observability":
		// The workspace's Loki, Grafana or Prometheus address.
		t.Name = map[string]string{"loki": "Loki", "grafana": "Grafana", "prometheus": "Prometheus"}[r.Target]
	case "ssh_key":
		// The title it had when the entry was written.
		t.Name, _ = r.Detail["title"].(string)
		if t.Name == "" {
			t.Name = "an SSH key"
		}
	default:
		t.Name = r.Target
	}
	return t
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// AuditLogFilter narrows ListAuditLog. Empty fields don't filter.
type AuditLogFilter struct {
	// Action is one action exactly; Actions any of several.
	Action  string
	Actions []string
	ActorID string
	Search  string
	// Resource is one resource exactly, as written ("service:<id>").
	Resource string
	// Agent: "" everyone, "any" agents only, "none" people only, or one
	// agent connection's ID (D-100).
	Agent string
	// APIKey: "" every entry, "any" made with a key, "none" made without.
	APIKey string
	Since  *time.Time
	// Before and BeforeID are the cursor: entries older than that one.
	Before   *time.Time
	BeforeID string
	Limit    int
}

// OrgSettings holds org-level configuration that owners can tune.
type OrgSettings struct {
	ResourceBypassMinRole string `json:"resource_bypass_min_role"`
	// DefaultIsolation is the level the workspace's services run at unless
	// one chooses its own (D-95): standard, sandboxed or sandboxed_strict
	// (D-115). "" on input keeps it.
	DefaultIsolation string `json:"default_isolation"`
	// SetupHiddenAt is when an owner hid Home's Finish setting up checklist
	// (7g); nil while it shows. Read only: PUT ignores it.
	SetupHiddenAt *time.Time `json:"setup_hidden_at"`
}

// Isolation levels (D-95, D-115).
const (
	IsolationStandard        = "standard"
	IsolationSandboxed       = "sandboxed"
	IsolationSandboxedStrict = "sandboxed_strict"
)

// OrgIsolation is where a workspace's services can run Sandboxed (D-95).
type OrgIsolation struct {
	DefaultIsolation string            `json:"default_isolation"`
	Servers          []IsolationServer `json:"servers"`
}

// IsolationServer is one of the workspace's servers, or the panel's own
// (ID ""), with its Sandboxed levels and the services there that follow the
// default.
type IsolationServer struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Sandboxed       string `json:"sandboxed"`
	Reason          string `json:"reason"`
	SandboxedStrict string `json:"sandboxed_strict"`
	StrictReason    string `json:"strict_reason"`
	GvisorVersion   string `json:"gvisor_version,omitempty"`
	Following       int    `json:"following"`
}

// ValidateBypassRole returns an error if the given role is not a valid bypass role.
// Viewers can never bypass the capacity gate.
func ValidateBypassRole(role string) error {
	switch role {
	case RoleOwner, RoleDeveloper:
		return nil
	default:
		return fmt.Errorf("resource_bypass_min_role must be %q or %q", RoleOwner, RoleDeveloper)
	}
}

// OrgMembership is a summary of one org a user belongs to, used for the
// workspace switcher — one row per org the user can switch to.
type OrgMembership struct {
	OrgID string `json:"org_id"`
	Name  string `json:"name"`
	Role  string `json:"role"`
}
