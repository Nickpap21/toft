// Package instance holds instance-level state that sits above workspaces:
// who the instance operators are (D-14, D-21) and instance settings such as
// the registration mode. It imports none of auth, org or user; those packages
// declare the small interfaces they need and *Service satisfies them.
package instance

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Nickpap21/toft/backend/internal/server"
)

// RegistrationLockKey is the pg_advisory_xact_lock key that serialises
// first-run registration (auth) and operator bootstrap (this package), so the
// two can never both decide they are creating the first operator. The value
// is "toft_reg" in ASCII; keep it unique among advisory lock keys.
const RegistrationLockKey int64 = 0x746f66745f726567

// SettingRegistration is the instance_settings key holding RegistrationSettings.
const SettingRegistration = "registration"

// Registration modes. Only ModeInviteOnly is accepted today; the others are
// reserved by D-21 and documented here so the JSON shape is settled.
const (
	ModeInviteOnly = "invite_only"
	// ModeEmailDomain (not accepted yet) will let addresses in listed domains
	// self-register into one workspace. Needs email verification first.
	ModeEmailDomain = "email_domain"
	// ModeApproval (not accepted yet) will queue sign-ups as inactive users
	// for an operator to approve.
	ModeApproval = "approval"
)

// Operator grant sources, recorded in instance_operators.source. Together
// with granted_by they say why someone is an operator; audit_log cannot hold
// instance-level events because its org_id is NOT NULL.
const (
	SourceFirstRun  = "first_run" // the first account created on an empty instance
	SourceBootstrap = "bootstrap" // earliest-created user promoted at startup
	SourceEnv       = "env"       // listed in TOFT_OPERATOR_EMAILS
	SourceAPI       = "api"       // granted by another operator
)

var (
	ErrNotOperator          = errors.New("only instance operators can do this")
	ErrLastOperator         = errors.New("cannot remove the last active instance operator")
	ErrUserNotFound         = errors.New("user not found")
	ErrAmbiguousEmail       = errors.New("more than one user matches this email")
	ErrNotFound             = errors.New("not found")
	ErrCannotDeactivateSelf = errors.New("you cannot deactivate your own account")
	// ErrWrongPassword: the operator's own password, asked again for every
	// change to accounts and operators (D-23, D-50), did not match.
	ErrWrongPassword = errors.New("your password is incorrect")
	// ErrUserInactive: a password reset for a deactivated account (D-56);
	// reactivate it first.
	ErrUserInactive = errors.New("the account is deactivated")
	// Password reset link errors (D-56), reported to whoever holds the link.
	ErrResetNotFound = errors.New("reset link not found")
	ErrResetUsed     = errors.New("reset link has already been used")
	ErrResetExpired  = errors.New("reset link has expired")
	// Account deletion (D-57): deactivate first, revoke operator first, and
	// never yourself; a deleted account is gone for every later change.
	ErrUserActive       = errors.New("the account is still active — deactivate it first")
	ErrIsOperator       = errors.New("the account is an instance operator — revoke that first")
	ErrUserDeleted      = errors.New("this account was deleted")
	ErrCannotDeleteSelf = errors.New("you cannot delete your own account")
	// ErrBadCursor: a ?cursor= this API did not issue (D-59).
	ErrBadCursor = errors.New("bad cursor")
	// ErrBadKind: a ?kind= the instance audit log doesn't have.
	ErrBadKind = errors.New("kind must be panel or accounts")
)

// ResetErrorCode is the stable code sent next to a reset link error:
// reset_not_found, reset_used or reset_expired; "" for any other error.
func ResetErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrResetNotFound):
		return "reset_not_found"
	case errors.Is(err, ErrResetUsed):
		return "reset_used"
	case errors.Is(err, ErrResetExpired):
		return "reset_expired"
	default:
		return ""
	}
}

// RegistrationSettings is the JSON stored at instance_settings['registration'].
//
// Today: {"mode":"invite_only"}. Shapes reserved for later modes, which need
// no migration because the value is JSONB and modes are validated in Go:
//
//	{"mode":"email_domain","email_domains":["example.com"],"org_id":"<uuid>","role":"developer"}
//	{"mode":"approval"}
type RegistrationSettings struct {
	Mode string `json:"mode"`
}

// ParseRegistrationSettings decodes and validates the stored registration
// setting. An unknown mode is an error so callers can fail closed.
func ParseRegistrationSettings(raw []byte) (RegistrationSettings, error) {
	var s RegistrationSettings
	if err := json.Unmarshal(raw, &s); err != nil {
		return RegistrationSettings{}, fmt.Errorf("decode registration settings: %w", err)
	}
	switch s.Mode {
	case ModeInviteOnly:
		return s, nil
	default:
		return RegistrationSettings{}, fmt.Errorf("unsupported registration mode %q", s.Mode)
	}
}

// RegistrationStatus is the public, unauthenticated view of registration.
type RegistrationStatus struct {
	Mode string `json:"mode"`
	// FirstRun is true while the instance has no accounts at all: the next
	// registration creates the instance operator.
	FirstRun bool `json:"first_run"`
	// SetupTokenRequired is true when FirstRun is true and TOFT_SETUP_TOKEN
	// is set, so the first registration must present it.
	SetupTokenRequired bool `json:"setup_token_required"`
}

// PanelHost is what any member sees of the server the panel runs on, which
// runs a workspace's services until it adds a server of its own. It
// reserves nothing for the panel itself (D-26 is not built), so there is no
// "free for apps" here.
type PanelHost struct {
	// Address is what the panel's domain resolves to; "" when it doesn't.
	Address     string `json:"address"`
	CPUCores    int    `json:"cpu_cores"`
	MemoryBytes int64  `json:"memory_bytes"`
	// DiskBytes is the size of the filesystem under Docker's data root.
	DiskBytes uint64 `json:"disk_bytes"`
	// AppNetworks is "isolated" or "legacy" (TOFT_APP_NETWORKS). Under
	// legacy only operators deploy to the panel host, and pushes don't.
	AppNetworks string `json:"app_networks"`
	// OS is the host's operating system, as Docker reports it.
	OS string `json:"os"`
	// Role is the server role: RoleApps or RoleControlPlane (D-62).
	Role string `json:"role"`
	// ControlPlaneSubnet is the control plane's network (paas-network);
	// AppAddressPools are the local Docker's default-address-pools, which
	// app networks take their subnets from under isolated networks: empty
	// when it has none, nil (null) when Docker didn't say.
	ControlPlaneSubnet string               `json:"control_plane_subnet"`
	AppAddressPools    []server.AddressPool `json:"app_address_pools"`
	// AppAddressPool is deprecated (v0.36.0, goes in a later release): the
	// first of AppAddressPools' bases, "" when none.
	AppAddressPool string      `json:"app_address_pool"`
	Reservation    Reservation `json:"reservation"`
	// Version is the release the panel runs (buildinfo).
	Version string `json:"version"`
}

// Reservation is D-62's fixed share on a server that runs the panel and
// apps (D-26), what it leaves the apps, and the least a server needs for
// it (D-132). Enforced when the apps run under toft-apps.slice
// (TOFT_APPS_SLICE).
type Reservation struct {
	MinCPU            int     `json:"min_cpu"`
	MinMemoryBytes    int64   `json:"min_memory_bytes"`
	SystemCPU         float64 `json:"system_cpu"`
	SystemMemoryBytes int64   `json:"system_memory_bytes"`
	PanelCPU          float64 `json:"panel_cpu"`
	PanelMemoryBytes  int64   `json:"panel_memory_bytes"`
	AppsCPU           float64 `json:"apps_cpu"`
	AppsMemoryBytes   int64   `json:"apps_memory_bytes"`
	Enforced          bool    `json:"enforced"`
}

// Operator is one instance operator, as returned by the API.
type Operator struct {
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	IsActive  bool      `json:"is_active"`
	Source    string    `json:"source"`
	GrantedBy *string   `json:"granted_by"`
	GrantedAt time.Time `json:"granted_at"`
}

// User is one account as seen by an instance operator.
type User struct {
	ID         string    `json:"id"`
	Email      string    `json:"email"`
	Name       string    `json:"name"`
	IsActive   bool      `json:"is_active"`
	IsOperator bool      `json:"is_operator"`
	CreatedAt  time.Time `json:"created_at"`
	// DeactivatedAt and DeactivatedBy (the operator's name) are set while the
	// account is deactivated, unless that happened before 0030.
	DeactivatedAt *time.Time `json:"deactivated_at"`
	DeactivatedBy string     `json:"deactivated_by"`
	// Workspaces are the names of the workspaces the account is a member of,
	// sorted; LastLoginAt is nil for an account that never signed in (D-56).
	Workspaces  []string   `json:"workspaces"`
	LastLoginAt *time.Time `json:"last_login_at"`
	// ResetExpiresAt is set while an unused reset link is live (D-57).
	ResetExpiresAt *time.Time `json:"reset_expires_at"`
}

// AuditPerson names who did or received an instance change; a deleted
// account reads as its tombstone ("Deleted account").
type AuditPerson struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// AuditEntry is one row of the instance audit log (D-57): operator.granted,
// operator.revoked, user.deactivated, user.reactivated, user.deleted,
// password_reset.issued, password_reset.used.
type AuditEntry struct {
	ID        string         `json:"id"`
	Action    string         `json:"action"`
	Actor     *AuditPerson   `json:"actor"`
	Target    *AuditPerson   `json:"target"`
	Detail    map[string]any `json:"detail"`
	CreatedAt time.Time      `json:"created_at"`
}

// PasswordReset is a freshly issued reset link (D-56): the raw token goes to
// the operator once and is never stored.
type PasswordReset struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// PasswordResetTarget is what the reset page shows the holder of a link.
type PasswordResetTarget struct {
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	ExpiresAt time.Time `json:"expires_at"`
}

// UserUpdate is the result of SetUserActive: the account as it now stands.
type UserUpdate struct {
	User
	// InvitesRevoked counts the pending invites a deactivation expired: every
	// unused invite the user created, and every unused invite in a workspace
	// they own (owners can read those tokens). Their owners must send new
	// links. Always 0 on reactivation, which restores none of them.
	InvitesRevoked int64 `json:"invites_revoked"`
}
