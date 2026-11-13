// Package actor carries who acts on a request beyond its user: the agent
// connection acting for them, or the API key they used (D-100). The auth
// middleware puts it in the request's context.Context, so use cases and the
// audit log read it without knowing about HTTP.
package actor

import (
	"context"
	"fmt"
)

// Agent is an agent connection acting for UserID.
type Agent struct {
	ConnectionID string
	Name         string
	UserID       string
	OrgID        string
}

// Intent is what an MCP tool call says it is for: the tool, and the reason
// and plan a person reads when the policy asks them. Only the MCP server
// sets it, in-process; no header carries it.
type Intent struct {
	Tool   string
	Reason string
	Plan   string
	// PlanSteps is the plan as steps, when the agent gave it so (D-146).
	PlanSteps []PlanStep
	// The operation the call runs, which Approve runs again for it.
	Method string
	Path   string
	Body   map[string]any
	// Seen is filled by the agent policy with what decided the call, for
	// the MCP server's audit entry (D-102).
	Seen *Seen
}

// Seen is how the agent policy decided a call: the environment class of
// its target and the level whose rule applied (project, workspace or
// default).
type Seen struct {
	Class string
	Level string
	// CoveredBy is the approval whose deploy covered this rollback, and
	// ApprovedBy the person who approved it (D-146).
	CoveredBy  string
	ApprovedBy string
}

// PlanStep is one step of an agent's plan: what it says, and the tool and
// service it would act on, for the person to see what it needs (D-146).
type PlanStep struct {
	Text      string `json:"text"`
	Tool      string `json:"tool,omitempty"`
	ServiceID string `json:"service_id,omitempty"`
}

// Target is what an agent's call acts on, for the agent policy (D-102):
// a service, an environment, or a project (code and issues). Tool names the
// call for its rate limit; the MCP tool's name, or the one a use case
// stands for over REST.
type Target struct {
	Action        string
	Tool          string
	ServiceID     string
	EnvironmentID string
	ProjectID     string
	// DeploymentID is a rollback's target: the deployment it goes back to.
	DeploymentID string
	// Class is set when the call's class is known without a service or an
	// environment: a preview not started yet.
	Class string
}

type ctxKey int

const (
	agentKey ctxKey = iota
	apiKeyKey
	intentKey
	grantKey
	claimsKey
)

func WithAgent(ctx context.Context, a Agent) context.Context {
	return context.WithValue(ctx, agentKey, a)
}

// AgentFrom returns the agent acting on this request, if one is.
func AgentFrom(ctx context.Context) (Agent, bool) {
	a, ok := ctx.Value(agentKey).(Agent)
	return a, ok
}

func WithAPIKey(ctx context.Context, keyID string) context.Context {
	return context.WithValue(ctx, apiKeyKey, keyID)
}

// APIKeyFrom returns the ID of the API key the request used, "" for none.
func APIKeyFrom(ctx context.Context) string {
	s, _ := ctx.Value(apiKeyKey).(string)
	return s
}

func WithIntent(ctx context.Context, i Intent) context.Context {
	return context.WithValue(ctx, intentKey, i)
}

func IntentFrom(ctx context.Context) (Intent, bool) {
	i, ok := ctx.Value(intentKey).(Intent)
	return i, ok
}

// WithGrant marks a request as the run of an approved request: the policy
// lets it through once, for what was approved. Only Approve sets it.
func WithGrant(ctx context.Context, approvalID string) context.Context {
	return context.WithValue(ctx, grantKey, approvalID)
}

func GrantFrom(ctx context.Context) string {
	s, _ := ctx.Value(grantKey).(string)
	return s
}

// Claims is what the agent policy used up for one request: the approval
// whose deploy covered this rollback (D-146), so the request gives it back
// when it fails before anything runs.
type Claims struct{ Rollback string }

// WithClaims gives a request a place for what the policy uses up.
func WithClaims(ctx context.Context) (context.Context, *Claims) {
	c := &Claims{}
	return context.WithValue(ctx, claimsKey, c), c
}

// ClaimsFrom is the request's Claims, nil when it has none.
func ClaimsFrom(ctx context.Context) *Claims {
	c, _ := ctx.Value(claimsKey).(*Claims)
	return c
}

// GateError is the agent policy refusing a call, or holding it for a person:
// the status and code the API answers with, and the approval it waits on.
type GateError struct {
	Status     int
	Code       string
	Message    string
	ApprovalID string
}

func (e *GateError) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }
