// Package deploypolicy is the deploy policy (D-19): what a deployment may do
// to the host it runs on, checked when a service's configuration is saved and
// again, authoritatively, on every deploy. Compose files are checked by
// internal/composespec; this package checks single-container services and
// pre-deploy host commands, applies per-service exceptions that only instance
// operators can grant, and the enforcement mode (TOFT_DEPLOY_POLICY).
package deploypolicy

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/domain"
	"github.com/Nickpap21/toft/backend/internal/resources"
	"github.com/Nickpap21/toft/backend/pkg/listversion"
)

// Rule and Violation are composespec's; this package adds rules for things a
// compose file cannot express (host_exec for pre-deploy commands,
// config_path).
type (
	Rule      = composespec.Rule
	Violation = composespec.Violation
)

// Mode is the enforcement mode, from TOFT_DEPLOY_POLICY.
type Mode string

const (
	// ModeEnforce refuses saves and deploys that violate the policy.
	ModeEnforce Mode = "enforce"
	// ModeWarn records violations (deploy log, save responses) and lets the
	// save or deploy go ahead. For a short migration window only.
	ModeWarn Mode = "warn"
)

// ParseMode reads TOFT_DEPLOY_POLICY. Empty means enforce. ok is false for an
// unknown value, which also gives enforce (fail closed).
func ParseMode(s string) (mode Mode, ok bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(ModeEnforce):
		return ModeEnforce, true
	case string(ModeWarn):
		return ModeWarn, true
	default:
		return ModeEnforce, false
	}
}

// Audit actions written to audit_log for exception changes.
const (
	ActionExceptionGranted = "deploy_policy.exception.granted"
	ActionExceptionRevoked = "deploy_policy.exception.revoked"
)

// MaxReasonLength bounds an exception's reason.
const MaxReasonLength = 1000

var (
	ErrNotOperator     = errors.New("only instance operators can manage deploy policy exceptions")
	ErrServiceNotFound = errors.New("service not found")
	ErrNotFound        = errors.New("this service has no deploy policy exception")
	ErrNoWorkspace     = errors.New("the service belongs to no workspace, so the exception cannot be audited")
	ErrInvalidRules    = errors.New("invalid rules")
	ErrReasonRequired  = errors.New("a reason is required")
	// ErrValuesChanged: the values a grant would approve aren't the ones
	// the operator was shown (values_digest).
	ErrValuesChanged = errors.New("the service's values changed since you opened the exception: review them and approve again")
)

// Exception is a stored per-service exception.
type Exception struct {
	ServiceID      string
	AllowedRules   []Rule
	Reason         string
	GrantedBy      string
	GrantedByName  string
	GrantedByEmail string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	// AllowedValues are the violations the grant approved (K3): a granted
	// rule's violation is allowed at a deploy only when it is one of them.
	// AnyValue is an exception granted before v0.25.0, which covers its
	// rules at any value until granted again.
	AllowedValues []PinnedValue
	AnyValue      bool
}

// PinnedValue is a violation an exception approved, as it was when granted.
// Fingerprint identifies it (Violation.Fingerprint); the rest is what the
// screens show, with secrets redacted.
type PinnedValue struct {
	Fingerprint string `json:"-"`
	Rule        Rule   `json:"rule"`
	Service     string `json:"service,omitempty"`
	Field       string `json:"field"`
	Value       string `json:"value"`
	Image       string `json:"image,omitempty"`
	Message     string `json:"message"`
}

func pinned(v Violation) PinnedValue {
	return PinnedValue{Fingerprint: v.Fingerprint, Rule: v.Rule, Service: v.Service, Field: v.Field, Value: v.Value, Image: v.Image, Message: v.Message}
}

func (p PinnedValue) violation() Violation {
	return Violation{Rule: p.Rule, Service: p.Service, Field: p.Field, Value: p.Value, Image: p.Image, Message: p.Message, Fingerprint: p.Fingerprint}
}

// A violation's State: what the service's exception makes of it.
const (
	// StatePinned is a granted rule at a value the exception approved.
	StatePinned = "pinned"
	// StateGranted is a granted rule the exception covers at any value: an
	// exception from before v0.25.0, or control_plane and net_raw, which
	// aren't configuration.
	StateGranted = "granted"
	// StatePending is a granted rule at a value the exception didn't
	// approve: a deploy is refused until an operator approves it.
	StatePending = "pending"
	// StateNotGranted is a rule the exception doesn't grant, or none.
	StateNotGranted = "not_granted"
)

// byRuleOnly are the grantable rules with no value to approve, so an
// exception grants them by rule: control_plane and net_raw aren't
// configuration, and a missing memory limit is always "no limit".
var byRuleOnly = map[Rule]bool{composespec.RuleControlPlane: true, composespec.RuleNetRaw: true, composespec.RuleMemoryLimit: true}

// state is what e makes of v.
func (e *Exception) state(v Violation) string {
	switch {
	case !e.allowed()[v.Rule]:
		return StateNotGranted
	case e.AnyValue || byRuleOnly[v.Rule]:
		return StateGranted
	}
	for _, p := range e.AllowedValues {
		if p.Fingerprint != "" && (p.Fingerprint == v.Fingerprint || p.Fingerprint == v.AltFingerprint) {
			return StatePinned
		}
	}
	return StatePending
}

// LastCheck is the last deploy-time check of a compose file from a
// repository: its violations of grantable rules, for an operator to approve.
type LastCheck struct {
	CommitSHA string
	CheckedAt time.Time
	Values    []PinnedValue
}

// DeployCheckView is a LastCheck as the API shows it, each violation with
// its state under the service's exception.
type DeployCheckView struct {
	CommitSHA  string      `json:"commit_sha"`
	CheckedAt  time.Time   `json:"checked_at"`
	Violations []Violation `json:"violations"`
}

func (c *LastCheck) view(exc *Exception) *DeployCheckView {
	if c == nil {
		return nil
	}
	vs := make([]Violation, 0, len(c.Values))
	for _, p := range c.Values {
		v := p.violation()
		v.State = exc.state(v)
		vs = append(vs, v)
	}
	return &DeployCheckView{CommitSHA: c.CommitSHA, CheckedAt: c.CheckedAt, Violations: vs}
}

// allowed returns the grantable rules of e as a set. Non-grantable rules are
// never in it, even if a row somehow holds one.
func (e *Exception) allowed() map[Rule]bool {
	set := map[Rule]bool{}
	if e == nil {
		return set
	}
	for _, r := range e.AllowedRules {
		if composespec.Grantable(r) {
			set[r] = true
		}
	}
	return set
}

// Grant is the input to Repository.GrantException.
type Grant struct {
	ServiceID     string
	AllowedRules  []Rule
	AllowedValues []PinnedValue
	Reason        string
	ActorID       string
	// Version is the exception's the grant was read from; a grant that
	// doesn't match what is stored writes nothing (listversion.ErrChanged).
	Version string
}

// ExceptionResponse is the API view of an exception.
type ExceptionResponse struct {
	// version is what a new grant must send back.
	version        string
	ServiceID      string    `json:"service_id"`
	AllowedRules   []Rule    `json:"allowed_rules"`
	Reason         string    `json:"reason"`
	GrantedBy      string    `json:"granted_by"`
	GrantedByName  string    `json:"granted_by_name"`
	GrantedByEmail string    `json:"granted_by_email"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	// AllowedValues are the values the grant approved; null for an
	// exception from before v0.25.0, which covers any value.
	AllowedValues []PinnedValue `json:"allowed_values"`
}

// versionOf says which state of a service's exception a client read, and a
// new grant must send it back: its rules and when it was last granted, to the
// microsecond as stored. Not the pinned values: a deploy rewrites those
// without anyone granting anything (D-126). No exception has its own version.
func versionOf(e *Exception) string {
	if e == nil {
		return listversion.Of(nil)
	}
	lines := make([]string, 0, len(e.AllowedRules))
	for _, r := range e.AllowedRules {
		lines = append(lines, fmt.Sprintf("%s %d", r, e.UpdatedAt.UnixMicro()))
	}
	return listversion.Of(lines)
}

func toExceptionResponse(e *Exception) *ExceptionResponse {
	if e == nil {
		return nil
	}
	rules := e.AllowedRules
	if rules == nil {
		rules = []Rule{}
	}
	return &ExceptionResponse{
		version:        versionOf(e),
		ServiceID:      e.ServiceID,
		AllowedRules:   rules,
		Reason:         e.Reason,
		GrantedBy:      e.GrantedBy,
		GrantedByName:  e.GrantedByName,
		GrantedByEmail: e.GrantedByEmail,
		CreatedAt:      e.CreatedAt,
		UpdatedAt:      e.UpdatedAt,
		AllowedValues:  e.values(),
	}
}

// values is AllowedValues for the API: null for AnyValue, never otherwise.
func (e *Exception) values() []PinnedValue {
	if e == nil || e.AnyValue {
		return nil
	}
	if e.AllowedValues == nil {
		return []PinnedValue{}
	}
	return e.AllowedValues
}

// Decision is the outcome of a policy check.
type Decision struct {
	Mode Mode
	// Violations are not covered by the service's exception.
	Violations []Violation
	// Allowed are violations the service's exception covers.
	Allowed []Violation
	// Warnings never block (invalid env var names on non-compose services).
	Warnings []string
	// Blocked is true in enforce mode when there are violations.
	Blocked bool
	// ComposeServices are the names of the compose file's services, when it
	// was loaded (an inline file in CheckDeploy, a repository file in
	// CheckComposeRepo); nil otherwise.
	ComposeServices []string
	// ComposeMemoryMB is each of those services' memory limit in the file,
	// in MiB (0 = none); nil when ComposeServices is.
	ComposeMemoryMB map[string]int64
	// ComposeDeployLimits are those services that declare
	// deploy.resources.limits in the file; the limits override mirrors it.
	ComposeDeployLimits map[string]bool
}

// Err returns a *composespec.ViolationsError when the decision blocks.
func (d Decision) Err() error {
	if !d.Blocked {
		return nil
	}
	var refused []Violation
	for _, v := range d.Violations {
		if d.refuses(v) {
			refused = append(refused, v)
		}
	}
	return &composespec.ViolationsError{Violations: refused}
}

// SelfRouting reports whether v is a traefik.* label on a compose service.
// Warn mode refuses these at deploy too: the deploy would go ahead, then the
// check after `up` would take the stack down (or the guard would switch the
// service's routing off), so refusing first leaves the running stack as it
// is.
func SelfRouting(v Violation) bool {
	return v.Rule == composespec.RuleReservedLabel && v.Service != "" &&
		strings.Contains(strings.ToLower(v.Field), ".labels.traefik.")
}

// refuses reports whether v is a reason the deploy is refused.
func (d Decision) refuses(v Violation) bool {
	return d.Blocked && (d.Mode != ModeWarn || SelfRouting(v))
}

// LogLines renders the decision for a deployment log.
func (d Decision) LogLines() []string {
	var lines []string
	for _, v := range d.Allowed {
		lines = append(lines, "⚠ allowed by deploy policy exception: "+v.String())
	}
	for _, w := range d.Warnings {
		lines = append(lines, "⚠ "+w)
	}
	for _, v := range d.Violations {
		switch {
		case !d.refuses(v):
			lines = append(lines, "⚠ deploy policy (warn mode, not enforced): "+v.String())
		case d.Mode == ModeWarn:
			lines = append(lines, "✗ deploy policy (refused in warn mode too, or the stack would be taken down after it starts): "+v.String())
		default:
			lines = append(lines, "✗ deploy policy: "+v.String())
		}
	}
	return lines
}

// SourceType values, as in service.SourceType.
const (
	SourceGit     = "git"
	SourceImage   = "docker-image"
	SourceCompose = "docker-compose"
)

// DeployCheck is what CheckDeploy inspects before a deployment is created.
type DeployCheck struct {
	ServiceID   string
	ServiceName string
	SourceType  string
	// Image is what a single-container service runs (ServiceImage).
	Image string
	// ComposeContent is the inline compose file; empty when ComposeFromRepo.
	ComposeContent string
	// ComposeFromRepo means the compose file comes from a repository and is
	// checked by CheckComposeRepo after the clone.
	ComposeFromRepo bool
	// EnvVars are the service's KEY=VALUE pairs, as the deploy injects them.
	EnvVars []string
	// Volumes are the service's own volumes (single-container services).
	Volumes []VolumeSpec
	// PreDeployCommands run on the host before the pipeline.
	PreDeployCommands []string
	// Memory is the memory_limit input (D-41); nil = not checked.
	Memory *resources.MemoryCheck
}

// RepoCheck is what CheckComposeRepo inspects after an infra repo is cloned.
type RepoCheck struct {
	ServiceID   string
	ServiceName string
	// RepoDir is the clone; ComposeFilePath is relative to it.
	RepoDir         string
	ComposeFilePath string
	// CommitSHA is the commit the clone is at, recorded with the check.
	CommitSHA string
	EnvVars   []string
	// Local is true when the deploy runs on the control-plane server. Docker
	// compose then uses the repository root as the project directory and
	// reads the repository's .env. Remote deploys use the compose file's
	// directory and replace its .env with the service's env vars; the caller
	// writes that file into RepoDir before the check
	// (composespec.StageDotEnv), because compose also reads it for files
	// included from that directory.
	Local bool
	// Memory is the memory_limit input (D-41); nil = not checked.
	Memory *resources.MemoryCheck
}

// VolumeSpec is a single-container volume.
type VolumeSpec struct {
	Name     string
	Target   string
	HostPath string
}

// ServicePolicy is the GET /services/:serviceID/deploy-policy response.
type ServicePolicy struct {
	// version is the exception's: what a new grant must send back.
	version    string
	ServiceID  string             `json:"service_id"`
	Mode       Mode               `json:"mode"`
	Violations []Violation        `json:"violations"`
	Allowed    []Violation        `json:"allowed"`
	Exception  *ExceptionResponse `json:"exception"`
	// ComposeCheckedAtDeploy is true for compose files from a repository:
	// their content is only known, and checked, when they are deployed.
	ComposeCheckedAtDeploy bool                   `json:"compose_checked_at_deploy"`
	InvalidEnvKeys         []string               `json:"invalid_env_keys"`
	Rules                  []composespec.RuleInfo `json:"rules"`
	CanManageExceptions    bool                   `json:"can_manage_exceptions"`
	// ControlPlaneRestricted is true when the service has no server (it
	// deploys to the control-plane server), the D-19 stopgap is on and no
	// control_plane exception lifts it: only operators can deploy it, and
	// git-push deploys are refused.
	ControlPlaneRestricted bool `json:"control_plane_restricted"`
	// ComposeServices are the service names of an inline compose file,
	// sorted, as loaded with the service's stored env vars: the choices for
	// Settings → Domain Routing. null for a compose file from a repository
	// (known only at deploy time), a file that cannot be loaded, and
	// services that are not compose services.
	ComposeServices []string `json:"compose_services"`
	// LastDeployCheck is what the last deploy found in a compose file from
	// a repository, which only a deploy reads; null otherwise, and before
	// its first deploy.
	LastDeployCheck *DeployCheckView `json:"last_deploy_check"`
	// ValuesDigest names the values a grant would approve now; a grant
	// sends it back, so it approves what the operator was shown.
	ValuesDigest string `json:"values_digest"`
}

// ReportEntry is one service in the instance-wide violations report.
type ReportEntry struct {
	ServiceID       string `json:"service_id"`
	ServiceName     string `json:"service_name"`
	OrgID           string `json:"org_id"`
	OrgName         string `json:"org_name"`
	ProjectID       string `json:"project_id"`
	ProjectName     string `json:"project_name"`
	EnvironmentName string `json:"environment_name"`
	ServerID        string `json:"server_id"`
	// ServerName is "" for the control-plane server.
	ServerName             string      `json:"server_name"`
	SourceType             string      `json:"source_type"`
	Violations             []Violation `json:"violations"`
	Allowed                []Violation `json:"allowed"`
	AllowedRules           []Rule      `json:"allowed_rules"`
	ComposeCheckedAtDeploy bool        `json:"compose_checked_at_deploy"`
	InvalidEnvKeys         []string    `json:"invalid_env_keys"`
	// ExceptionReason is the reason the exception was granted with; empty
	// when the service has none. The operators reading the report may not
	// be members of the service's workspace, so the report carries it.
	ExceptionReason string `json:"exception_reason"`
	// ExceptionGrantedBy and ExceptionGrantedAt: who granted the exception
	// and when (its last grant replaces the earlier); empty without one.
	ExceptionGrantedBy string     `json:"exception_granted_by"`
	ExceptionGrantedAt *time.Time `json:"exception_granted_at"`
	// AllowedValues are the values the exception approved; null without
	// one, or for one from before v0.25.0, which covers any value.
	AllowedValues []PinnedValue `json:"allowed_values"`
	// LastDeployCheck is as in ServicePolicy.
	LastDeployCheck *DeployCheckView `json:"last_deploy_check"`
}

// Report is the GET /deploy-policy/violations response.
type Report struct {
	Mode        Mode          `json:"mode"`
	GeneratedAt time.Time     `json:"generated_at"`
	Services    []ReportEntry `json:"services"`
	// ServicesChecked and ServersChecked count everything the report read,
	// not only the services it lists (the control-plane server counts).
	ServicesChecked int `json:"services_checked"`
	ServersChecked  int `json:"servers_checked"`
	// Rules is every rule a deploy is checked against.
	Rules []composespec.RuleInfo `json:"rules"`
}

// StoredService is the part of a stored service the report checks, with
// where it belongs.
type StoredService struct {
	ID                string
	Name              string
	SourceType        string
	ComposeContent    string
	ComposeRepoURL    string
	DockerImage       string
	GitRepoURL        string
	RepositoryID      string
	Volumes           []VolumeSpec
	PreDeployCommands string
	ServerID          string
	ServerName        string
	OrgID             string
	OrgName           string
	ProjectID         string
	ProjectName       string
	EnvironmentName   string
	Hostnames         []string // its domains, oldest first, as a deploy gets them
}

// ServiceURL is a compose stack's TOFT_URL, so a catalog template can tell
// its app where it lives (D-147): https and the service's first domain that
// isn't a wildcard, a domain the person added before its temporary sslip.io
// address (D-150). A temporary one is http until it is among certified,
// the names a certificate has answered for: its certificate can wait for
// hours, and it answers over HTTP meanwhile. "" with no such domain.
func ServiceURL(hostnames, certified []string) string {
	temporary := ""
	for _, h := range hostnames {
		switch {
		case strings.HasPrefix(h, "*."):
		case domain.IsTemporary(h):
			if temporary == "" {
				temporary = "http://" + h
				if slices.Contains(certified, h) {
					temporary = "https://" + h
				}
			}
		default:
			return "https://" + h
		}
	}
	return temporary
}

// ServiceImage is what a single-container service runs, which an
// exception's values are tied to (K3): its image reference, or its
// repository. "" for a compose service, whose services carry their own.
func ServiceImage(sourceType, dockerImage, gitRepoURL, repositoryID string) string {
	switch sourceType {
	case SourceImage:
		return dockerImage
	case SourceGit:
		if gitRepoURL != "" {
			// A token in the URL is neither shown nor pinned: rotating it
			// keeps the repository approved.
			if u, err := url.Parse(gitRepoURL); err == nil && u.User != nil {
				u.User = nil
				return u.String()
			}
			return gitRepoURL
		}
		if repositoryID != "" {
			return "repository " + repositoryID
		}
	}
	return ""
}
