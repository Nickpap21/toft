package agent_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/agent"
	"github.com/Nickpap21/toft/backend/internal/agent/agenttest"
	"github.com/Nickpap21/toft/backend/pkg/actor"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"go.uber.org/zap"
)

type roles map[string]string

func (r roles) RoleIn(_ context.Context, userID, orgID string) (string, error) {
	if orgID != "org1" || r[userID] == "" {
		return "", errors.New("not a member")
	}
	return r[userID], nil
}

type ops struct{}

func (ops) IsOperator(_ context.Context, id string) (bool, error)      { return id == "op", nil }
func (ops) WriteAudit(context.Context, string, string, map[string]any) {}

type nopAudit struct{}

func (nopAudit) WriteAuditLogDetail(context.Context, string, string, string, string, map[string]any) {
}

const issuer = "https://panel.example"

func setup(t *testing.T) (*agent.Service, *agenttest.Repo, string) {
	t.Helper()
	repo := agenttest.New()
	repo.OrgProjects["org1"] = []string{"p1"}
	svc := agent.NewService(repo, roles{"owner": "owner", "dev": "developer", "viewer": "viewer"}, ops{}, nopAudit{}, zap.NewNop())
	reg, err := svc.Register(context.Background(), agent.Registration{ClientName: "Claude Code", RedirectURIs: []string{"http://localhost/callback"}})
	if err != nil {
		t.Fatal(err)
	}
	return svc, repo, reg["client_id"].(string)
}

func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func authorize(t *testing.T, svc *agent.Service, clientID, user, verifier string, scopes []string) (string, error) {
	t.Helper()
	to, err := svc.Authorize(context.Background(), issuer, user, agent.Consent{
		AuthRequest: agent.AuthRequest{ResponseType: "code", ClientID: clientID, RedirectURI: "http://localhost:6123/callback",
			CodeChallenge: challenge(verifier), CodeChallengeMethod: "S256", State: "s1", Resource: issuer + "/mcp"},
		Allow: true, OrgID: "org1", AllProjects: true, Scopes: scopes})
	if err != nil {
		return "", err
	}
	u, _ := url.Parse(to)
	if u.Query().Get("state") != "s1" || u.Query().Get("iss") != issuer || u.Port() != "6123" {
		t.Fatalf("redirect %s", to)
	}
	return u.Query().Get("code"), nil
}

func exchange(svc *agent.Service, clientID, code, verifier string) (map[string]any, error) {
	return svc.Token(context.Background(), issuer, agent.TokenRequest{GrantType: "authorization_code", Code: code, ClientID: clientID,
		RedirectURI: "http://localhost:6123/callback", CodeVerifier: verifier})
}

func oauthCode(err error) string {
	var oe *agent.OAuthError
	if errors.As(err, &oe) {
		return oe.Code
	}
	return ""
}

func TestOAuthCodeAndRefresh(t *testing.T) {
	ctx := context.Background()
	svc, _, client := setup(t)

	code, err := authorize(t, svc, client, "owner", "v1", []string{"read", "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exchange(svc, client, code, "wrong"); oauthCode(err) != "invalid_grant" {
		t.Fatalf("a wrong verifier: %v", err)
	}
	// The failed exchange used the code: it works once.
	code, _ = authorize(t, svc, client, "owner", "v1", []string{"read", "deploy"})
	tok, err := exchange(svc, client, code, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if tok["scope"] != "read deploy" || tok["expires_in"] != 3600 {
		t.Fatalf("token %v", tok)
	}
	if _, err := exchange(svc, client, code, "v1"); oauthCode(err) != "invalid_grant" {
		t.Fatalf("a code used twice: %v", err)
	}
	id, err := svc.ValidateAgentToken(ctx, tok["access_token"].(string))
	if err != nil || id.UserID != "owner" || id.OrgID != "org1" || !id.AllProjects {
		t.Fatalf("access token: %+v %v", id, err)
	}

	// Refresh rotates; the old refresh token used again revokes everything.
	old := tok["refresh_token"].(string)
	next, err := svc.Token(ctx, issuer, agent.TokenRequest{GrantType: "refresh_token", RefreshToken: old, ClientID: client})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Token(ctx, issuer, agent.TokenRequest{GrantType: "refresh_token", RefreshToken: old, ClientID: client}); oauthCode(err) != "invalid_grant" {
		t.Fatalf("a refresh token used twice: %v", err)
	}
	if _, err := svc.ValidateAgentToken(ctx, next["access_token"].(string)); err == nil {
		t.Fatal("the connection outlived a reused refresh token")
	}
}

func TestAuthorizationRequestChecks(t *testing.T) {
	ctx := context.Background()
	svc, _, client := setup(t)
	base := agent.AuthRequest{ResponseType: "code", ClientID: client, RedirectURI: "http://localhost:1/callback", CodeChallenge: "c", CodeChallengeMethod: "S256"}
	if _, _, err := svc.CheckRequest(ctx, issuer, base); err != nil {
		t.Fatalf("a loopback redirect on another port: %v", err)
	}
	for name, mutate := range map[string]func(*agent.AuthRequest){
		"unknown client":     func(r *agent.AuthRequest) { r.ClientID = "nope" },
		"redirect elsewhere": func(r *agent.AuthRequest) { r.RedirectURI = "https://evil.example/callback" },
		"other path":         func(r *agent.AuthRequest) { r.RedirectURI = "http://localhost:1/other" },
		"no PKCE":            func(r *agent.AuthRequest) { r.CodeChallenge = "" },
		"plain PKCE":         func(r *agent.AuthRequest) { r.CodeChallengeMethod = "plain" },
		"implicit":           func(r *agent.AuthRequest) { r.ResponseType = "token" },
		"another resource":   func(r *agent.AuthRequest) { r.Resource = "https://other.example/mcp" },
	} {
		r := base
		mutate(&r)
		if _, _, err := svc.CheckRequest(ctx, issuer, r); oauthCode(err) == "" {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
	if _, err := svc.Register(ctx, agent.Registration{RedirectURIs: []string{"http://evil.example/cb"}}); oauthCode(err) != "invalid_redirect_uri" {
		t.Errorf("an http redirect off this computer registered: %v", err)
	}
	// Registration needs no account, so what it stores is bounded.
	if _, err := svc.Register(ctx, agent.Registration{RedirectURIs: []string{"https://x.example/" + strings.Repeat("a", 2048)}}); oauthCode(err) != "invalid_redirect_uri" {
		t.Errorf("a 2 KiB redirect URI registered: %v", err)
	}
	if _, err := svc.Register(ctx, agent.Registration{RedirectURIs: []string{"https://x.example/cb"}, TokenEndpointAuthMethod: "client_secret_basic"}); oauthCode(err) == "" {
		t.Errorf("a confidential client registered: %v", err)
	}
	// A viewer lets an agent read only; a non-member nothing.
	if _, err := authorize(t, svc, client, "viewer", "v", []string{"read", "deploy"}); err == nil {
		t.Error("a viewer granted deploy")
	}
	if _, err := authorize(t, svc, client, "viewer", "v", []string{"read"}); err != nil {
		t.Errorf("a viewer's read-only consent: %v", err)
	}
	if _, err := authorize(t, svc, client, "stranger", "v", []string{"read"}); !errors.Is(err, agent.ErrForbidden) {
		t.Errorf("a non-member consented: %v", err)
	}
}

func TestPolicyGate(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := setup(t)
	repo.Services["s-prev"] = &agent.ServiceInfo{ID: "s-prev", Class: agent.ClassPreview, Label: "web in pr-1"}
	repo.Services["s-prod"] = &agent.ServiceInfo{ID: "s-prod", Class: agent.ClassProtected, Label: "web in production"}

	if err := svc.Check(ctx, dep("s-prod")); err != nil {
		t.Fatalf("a person's deploy met the agent policy: %v", err)
	}
	actx := actor.WithAgent(ctx, actor.Agent{ConnectionID: "c1", OrgID: "org1", UserID: "owner"})
	if err := svc.Check(actx, dep("s-prev")); err != nil {
		t.Fatalf("default Allow on a preview: %v", err)
	}
	var ge *actor.GateError
	if err := svc.Check(actx, dep("s-prod")); !errors.As(err, &ge) || ge.Code != "approval_required" {
		t.Fatalf("default Ask over REST: %v", err)
	}
	// Owners only; tightening needs no password, loosening does.
	cell := []agent.PolicyCell{{Action: "deploy", Class: "preview", Decision: "never"}}
	if _, err := svc.SetPolicy(ctx, "org1", "dev", "developer", cell, true); !errors.Is(err, agent.ErrForbidden) {
		t.Fatalf("a developer changed the policy: %v", err)
	}
	if _, err := svc.SetPolicy(ctx, "org1", "owner", "owner", cell, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.Check(actx, dep("s-prev")); !errors.As(err, &ge) || ge.Code != "agent_policy_never" {
		t.Fatalf("Never: %v", err)
	}
	cell[0].Decision = "allow"
	if _, err := svc.SetPolicy(ctx, "org1", "owner", "owner", cell, false); !errors.Is(err, agent.ErrReauthRequired) {
		t.Fatalf("loosened without the password: %v", err)
	}
	if _, err := svc.SetPolicy(ctx, "org1", "owner", "owner", []agent.PolicyCell{{Action: "read", Class: "preview", Decision: "never"}}, true); err == nil {
		t.Fatal("reads became a policy cell")
	}
}

func TestApprovalRunsOnceAndOnlyForAPerson(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := setup(t)
	repo.Services["s-prod"] = &agent.ServiceInfo{ID: "s-prod", Class: agent.ClassProtected, Label: "web in production"}
	conn, _ := repo.CreateConnection(ctx, agent.NewConnection{Code: agent.Code{OrgID: "org1", UserID: "dev", AllProjects: true, Scopes: []string{"read", "deploy"}}, Name: "Claude Code"})
	actx := actor.WithIntent(actor.WithAgent(ctx, actor.Agent{ConnectionID: conn, OrgID: "org1", UserID: "dev"}),
		actor.Intent{Tool: "deploy", Reason: "fixes it", Method: "POST", Path: "/api/v1/services/s-prod/deployments"})
	var ge *actor.GateError
	if err := svc.Check(actx, dep("s-prod")); !errors.As(err, &ge) || ge.Code != "approval_pending" {
		t.Fatalf("Ask: %v", err)
	}
	id := ge.ApprovalID
	// The same call again attaches to the same request.
	if err := svc.Check(actx, dep("s-prod")); !errors.As(err, &ge) || ge.ApprovalID != id {
		t.Fatalf("a second request: %v", err)
	}
	if _, err := svc.Decide(ctx, "org1", "viewer", "viewer", id, true, "", ""); !errors.Is(err, agent.ErrForbidden) {
		t.Fatalf("a viewer decided: %v", err)
	}
	// The acting person may approve their agent's request, signed in
	// (D-100); without a dispatcher the run records a failure.
	a, err := svc.Decide(ctx, "org1", "dev", "developer", id, true, "ok", "")
	if err != nil || a.State != "approved" {
		t.Fatalf("approve: %+v %v", a, err)
	}
	if _, err := svc.Decide(ctx, "org1", "owner", "owner", id, false, "", ""); !errors.Is(err, agent.ErrConflict) {
		t.Fatalf("decided twice: %v", err)
	}
	grant := actor.WithGrant(actx, id)
	// Never, set after the approval, still wins.
	never := []agent.PolicyCell{{Action: "deploy", Class: "protected", Decision: "never"}}
	if _, err := svc.SetPolicy(ctx, "org1", "owner", "owner", never, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.Check(grant, dep("s-prod")); !errors.As(err, &ge) || ge.Code != "agent_policy_never" {
		t.Fatalf("an approval ran under Never: %v", err)
	}
	never[0].Decision = "ask"
	if _, err := svc.SetPolicy(ctx, "org1", "owner", "owner", never, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.Check(grant, dep("s-prod")); err != nil {
		t.Fatalf("the approved run: %v", err)
	}
	if err := svc.Check(grant, dep("s-prod")); !errors.As(err, &ge) || ge.Code != "approval_used" {
		t.Fatalf("an approval used twice: %v", err)
	}
}

func TestPauseTurnsDownWaitingRequests(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := setup(t)
	repo.Services["s-prod"] = &agent.ServiceInfo{ID: "s-prod", Class: agent.ClassProtected}
	conn, _ := repo.CreateConnection(ctx, agent.NewConnection{Code: agent.Code{OrgID: "org1", UserID: "dev", AllProjects: true, Scopes: []string{"read"}}, Name: "x",
		AccessHash: "h"})
	actx := actor.WithIntent(actor.WithAgent(ctx, actor.Agent{ConnectionID: conn, OrgID: "org1"}), actor.Intent{Tool: "deploy", Reason: "r"})
	var ge *actor.GateError
	_ = svc.Check(actx, dep("s-prod"))
	if _, err := svc.PauseWorkspace(ctx, "org1", "dev", "dev", "developer", true); !errors.Is(err, agent.ErrForbidden) {
		t.Fatalf("a developer paused: %v", err)
	}
	if _, err := svc.PauseWorkspace(ctx, "org1", "owner", "admin", "owner", true); err != nil {
		t.Fatal(err)
	}
	if w, _ := svc.Waiting(ctx, "org1"); len(w) != 0 {
		t.Fatalf("still waiting after a pause: %+v", w)
	}
	if _, err := svc.PauseInstance(ctx, "org1", "owner", "admin", true); !errors.Is(err, agent.ErrForbidden) {
		t.Fatalf("a non-operator paused the panel: %v", err)
	}
	p, err := svc.PauseInstance(ctx, "org1", "op", "operator", true)
	if err != nil || p.Instance == nil || p.Workspace == nil {
		t.Fatalf("pause state: %+v %v", p, err)
	}
	_ = ge
}

func dep(serviceID string) actor.Target {
	return actor.Target{Action: "deploy", Tool: "deploy", ServiceID: serviceID}
}

// A project's cell beats the workspace's; a cell it leaves alone follows the
// workspace; loosening what applies to the project, dropping a rule
// included, asks for the password (D-101).
func TestProjectRules(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := setup(t)
	repo.Projects["p1"] = agenttest.Project{Name: "billing-api", Org: "org1"}
	repo.Projects["p2"] = agenttest.Project{Name: "storefront", Org: "org1"}
	repo.Services["b-stg"] = &agent.ServiceInfo{ID: "b-stg", ProjectID: "p1", Class: agent.ClassOther, Label: "billing-api / worker in staging"}
	repo.Services["s-stg"] = &agent.ServiceInfo{ID: "s-stg", ProjectID: "p2", Class: agent.ClassOther, Label: "storefront / api in staging"}
	actx := actor.WithAgent(ctx, actor.Agent{ConnectionID: "c1", OrgID: "org1", UserID: "owner"})
	seen := &actor.Seen{}
	actx = actor.WithIntent(actx, actor.Intent{Tool: "deploy", Seen: seen})

	never := []agent.PolicyCell{{Action: "deploy", Class: "other", Decision: "never"}}
	if _, err := svc.SetProjectPolicy(ctx, "org1", "dev", "developer", "p1", never, true); !errors.Is(err, agent.ErrForbidden) {
		t.Fatalf("a developer set a project's rule: %v", err)
	}
	if _, err := svc.SetProjectPolicy(ctx, "org2", "owner", "owner", "p1", never, true); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("another workspace's project: %v", err)
	}
	p, err := svc.SetProjectPolicy(ctx, "org1", "owner", "owner", "p1", never, false)
	if err != nil || len(p.Projects) != 1 || p.Projects[0].Name != "billing-api" {
		t.Fatalf("tightening needs no password: %+v %v", p, err)
	}
	var ge *actor.GateError
	if err := svc.Check(actx, dep("b-stg")); !errors.As(err, &ge) || ge.Code != "agent_policy_never" || seen.Level != agent.LevelProject {
		t.Fatalf("the project's Never: %v %+v", err, seen)
	}
	if err := svc.Check(actx, dep("s-stg")); err != nil || seen.Level != agent.LevelDefault {
		t.Fatalf("another project follows the workspace: %v %+v", err, seen)
	}
	// The workspace says Ask; the project's own Allow loosens what applies.
	if _, err := svc.SetPolicy(ctx, "org1", "owner", "owner", []agent.PolicyCell{{Action: "deploy", Class: "other", Decision: "ask"}}, false); err != nil {
		t.Fatal(err)
	}
	allow := []agent.PolicyCell{{Action: "deploy", Class: "other", Decision: "allow"}}
	if _, err := svc.SetProjectPolicy(ctx, "org1", "owner", "owner", "p1", allow, false); !errors.Is(err, agent.ErrReauthRequired) {
		t.Fatalf("loosened a project's cell without the password: %v", err)
	}
	if _, err := svc.SetProjectPolicy(ctx, "org1", "owner", "owner", "p1", allow, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.Check(actx, dep("b-stg")); err != nil || seen.Level != agent.LevelProject {
		t.Fatalf("the project's Allow beats the workspace's Ask: %v %+v", err, seen)
	}
	if err := svc.Check(actx, dep("s-stg")); !errors.As(err, &ge) || seen.Level != agent.LevelWorkspace {
		t.Fatalf("the workspace's Ask for the other project: %v %+v", err, seen)
	}
	// Dropping the project's Never would loosen it to the workspace's Ask.
	if _, err := svc.SetProjectPolicy(ctx, "org1", "owner", "owner", "p1", never, false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResetProjectPolicy(ctx, "org1", "owner", "owner", "p1", false); !errors.Is(err, agent.ErrReauthRequired) {
		t.Fatalf("dropped a tighter rule without the password: %v", err)
	}
	if p, err := svc.ResetProjectPolicy(ctx, "org1", "owner", "owner", "p1", true); err != nil || len(p.Projects) != 0 {
		t.Fatalf("reset: %+v %v", p, err)
	}
	if _, err := svc.SetProjectPolicy(ctx, "org1", "owner", "owner", "p1", []agent.PolicyCell{{Action: "code", Class: "other", Decision: "never"}}, true); err == nil {
		t.Fatal("code got an environment's cell: it has one, any")
	}
}

// A database or cache counts as protected outside previews, for every
// service action (D-101, D-102).
func TestDataServicesAreProtected(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := setup(t)
	repo.Services["db-stg"] = &agent.ServiceInfo{ID: "db-stg", Class: agent.ClassOther, Data: true, Label: "storefront / postgres in staging"}
	repo.Services["db-pr"] = &agent.ServiceInfo{ID: "db-pr", Class: agent.ClassPreview, Data: true, Label: "storefront / postgres in pr-4"}
	actx := actor.WithAgent(ctx, actor.Agent{ConnectionID: "c1", OrgID: "org1", UserID: "owner"})
	var ge *actor.GateError
	if err := svc.Check(actx, dep("db-stg")); !errors.As(err, &ge) || ge.Code != "approval_required" {
		t.Fatalf("a staging database's deploy didn't ask: %v", err)
	}
	if err := svc.Check(actx, actor.Target{Action: "variables", Tool: "set_env_var", ServiceID: "db-stg"}); !errors.As(err, &ge) || ge.Code != "approval_required" {
		t.Fatalf("a staging database's variable didn't ask: %v", err)
	}
	if err := svc.Check(actx, dep("db-pr")); err != nil {
		t.Fatalf("a preview's copy of a database is a preview: %v", err)
	}
}

// Each act tool has its own hourly limit per connection (D-102).
func TestToolLimits(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := setup(t)
	repo.Projects["p1"] = agenttest.Project{Name: "storefront", Org: "org1"}
	actx := actor.WithAgent(ctx, actor.Agent{ConnectionID: "c1", OrgID: "org1", UserID: "owner"})
	issue := actor.Target{Action: "issues", Tool: "create_issue", ProjectID: "p1"}
	for i := range 20 {
		if err := svc.Check(actx, issue); err != nil {
			t.Fatalf("issue %d: %v", i+1, err)
		}
	}
	var ge *actor.GateError
	if err := svc.Check(actx, issue); !errors.As(err, &ge) || ge.Code != "rate_limited" {
		t.Fatalf("a 21st issue in an hour: %v", err)
	}
	if err := svc.Check(actx, actor.Target{Action: "issues", Tool: "comment", ProjectID: "p1"}); err != nil {
		t.Fatalf("comments count apart: %v", err)
	}
	other := actor.WithAgent(ctx, actor.Agent{ConnectionID: "c2", OrgID: "org1", UserID: "owner"})
	if err := svc.Check(other, issue); err != nil {
		t.Fatalf("another connection counts apart: %v", err)
	}
}

type capture struct {
	body map[string]any
	path string
}

func (c *capture) Do(_ context.Context, _, _, path string, body map[string]any) (int, map[string]any, error) {
	c.path, c.body = path, body
	return 201, map[string]any{"data": map[string]any{"key": "STRIPE_KEY"}}, nil
}

// set_secret under Ask (D-102): the value is sealed apart, never in the
// request, the approval or anything the API returns; the run gets it back
// and the request keeps nothing after.
func TestSecretWaitsSealed(t *testing.T) {
	ctx := context.Background()
	const value = "sk_live_51NyEXAMPLEvalue" // gitleaks:allow
	for _, approve := range []bool{true, false} {
		svc, repo, _ := setup(t)
		svc.WithKeys(crypto.NewKeys("an-encryption-key-of-32-characters!", ""), nil)
		run := &capture{}
		svc.WithDispatcher(run)
		repo.Services["api-stg"] = &agent.ServiceInfo{ID: "api-stg", Class: agent.ClassOther, Label: "billing-api / api in staging"}
		conn, _ := repo.CreateConnection(ctx, agent.NewConnection{Code: agent.Code{OrgID: "org1", UserID: "dev", AllProjects: true, Scopes: []string{"read", "variables"}}, Name: "Claude Code"})
		actx := actor.WithIntent(actor.WithAgent(ctx, actor.Agent{ConnectionID: conn, OrgID: "org1", UserID: "dev"}),
			actor.Intent{Tool: "set_secret", Reason: "Stripe rotated it", Method: "POST", Path: "/api/v1/services/api-stg/secrets",
				Body: map[string]any{"key": "STRIPE_KEY", "value": value}})
		var ge *actor.GateError
		if err := svc.Check(actx, actor.Target{Action: "secrets", Tool: "set_secret", ServiceID: "api-stg"}); !errors.As(err, &ge) || ge.Code != "approval_pending" {
			t.Fatalf("set_secret didn't ask: %v", err)
		}
		sealed := repo.SealedOf(ge.ApprovalID)
		if sealed == "" || strings.Contains(sealed, value) {
			t.Fatalf("the value isn't sealed: %q", sealed)
		}
		a, err := svc.Approval(ctx, "org1", ge.ApprovalID)
		if err != nil {
			t.Fatal(err)
		}
		if raw, _ := json.Marshal(a); strings.Contains(string(raw), value) || a.Request.Body["key"] != "STRIPE_KEY" {
			t.Fatalf("the approval shows the value: %s", raw)
		}
		if a.EnvClass != agent.ClassOther {
			t.Fatalf("the approval's class: %q", a.EnvClass)
		}
		if _, err := svc.Decide(ctx, "org1", "owner", "owner", ge.ApprovalID, approve, "", ""); err != nil {
			t.Fatal(err)
		}
		svc.WaitRuns()
		if approve && (run.body["value"] != value || run.path != "/api/v1/services/api-stg/secrets") {
			t.Fatalf("the approved run didn't get the value: %+v", run)
		}
		if !approve && run.body != nil {
			t.Fatal("a denied request ran")
		}
		if left := repo.SealedOf(ge.ApprovalID); left != "" {
			t.Fatalf("the value outlived its request (approve %v)", approve)
		}
		a, _ = svc.Approval(ctx, "org1", ge.ApprovalID)
		if raw, _ := json.Marshal(a); strings.Contains(string(raw), value) {
			t.Fatalf("the decided approval shows the value: %s", raw)
		}
	}
}

// A deploy that names no commit runs the one the person was shown (the
// branch can move while it waits); the stored request stays what the agent
// asked. A request that named a commit keeps it, and Deny runs nothing.
func TestApprovalRunsTheCommitShown(t *testing.T) {
	ctx := context.Background()
	shown := strings.Repeat("a", 40)
	named := strings.Repeat("b", 40)
	for _, tc := range []struct {
		name    string
		tool    string
		body    map[string]any
		approve bool
		commit  string
		want    any
		bad     bool
	}{
		{name: "names none, nil body", tool: "deploy", approve: true, commit: shown, want: shown},
		{name: "names a branch", tool: "deploy", body: map[string]any{"git_ref": "main"}, approve: true, commit: shown, want: shown},
		{name: "names an empty commit", tool: "deploy", body: map[string]any{"commit_sha": ""}, approve: true, commit: shown, want: shown},
		{name: "names its own", tool: "deploy", body: map[string]any{"commit_sha": named}, approve: true, commit: shown, want: named},
		{name: "no commit sent", tool: "deploy", body: map[string]any{"git_ref": "main"}, approve: true},
		{name: "another tool", tool: "rollback", body: map[string]any{}, approve: true, commit: shown},
		{name: "not a sha", tool: "deploy", approve: true, commit: "main", bad: true},
		{name: "deny", tool: "deploy", commit: shown},
		{name: "deny with a bad commit", tool: "deploy", commit: "main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo, _ := setup(t)
			run := &capture{}
			svc.WithDispatcher(run)
			repo.Services["s-prod"] = &agent.ServiceInfo{ID: "s-prod", Class: agent.ClassProtected, Label: "web in production"}
			conn, _ := repo.CreateConnection(ctx, agent.NewConnection{Code: agent.Code{OrgID: "org1", UserID: "dev", AllProjects: true, Scopes: []string{"read", "deploy"}}, Name: "Claude Code"})
			actx := actor.WithIntent(actor.WithAgent(ctx, actor.Agent{ConnectionID: conn, OrgID: "org1", UserID: "dev"}),
				actor.Intent{Tool: tc.tool, Reason: "fixes it", Method: "POST", Path: "/api/v1/services/s-prod/deployments", Body: tc.body})
			var ge *actor.GateError
			if err := svc.Check(actx, actor.Target{Action: "deploy", Tool: tc.tool, ServiceID: "s-prod"}); !errors.As(err, &ge) || ge.Code != "approval_pending" {
				t.Fatalf("Ask: %v", err)
			}
			_, err := svc.Decide(ctx, "org1", "owner", "owner", ge.ApprovalID, tc.approve, "", tc.commit)
			svc.WaitRuns()
			a, _ := svc.Approval(ctx, "org1", ge.ApprovalID)
			if tc.bad {
				var inv *agent.ErrInvalid
				if !errors.As(err, &inv) || a.State != "waiting" || run.path != "" {
					t.Fatalf("a bad commit: err %v, state %s, ran %q", err, a.State, run.path)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !tc.approve {
				if run.path != "" {
					t.Fatal("a denied request ran")
				}
				return
			}
			if run.path == "" || run.body["commit_sha"] != tc.want {
				t.Fatalf("the run's body: %+v, want commit %v", run.body, tc.want)
			}
			if a.Request.Body["commit_sha"] != tc.body["commit_sha"] {
				t.Fatalf("the stored request changed: %+v", a.Request.Body)
			}
		})
	}
}
