package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/api"
	"github.com/Nickpap21/toft/backend/internal/agent"
	"github.com/Nickpap21/toft/backend/internal/agent/agenttest"
	"github.com/Nickpap21/toft/backend/internal/apikey"
	"github.com/Nickpap21/toft/backend/pkg/actor"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"go.uber.org/zap"
)

type orgs struct{}

func (orgs) GetForUser(context.Context, string) (string, string, error) { return "org1", "owner", nil }
func (orgs) RoleIn(_ context.Context, userID, orgID string) (string, error) {
	if orgID != "org1" {
		return "", errors.New("not a member")
	}
	return map[string]string{"u1": "owner", "u2": "viewer"}[userID], nil
}

type operators struct{}

func (operators) IsOperator(context.Context, string) (bool, error)           { return true, nil }
func (operators) WriteAudit(context.Context, string, string, map[string]any) {}

type entry struct {
	action, actor, agent string
	detail               map[string]any
}

type audit struct {
	mu      sync.Mutex
	entries []entry
}

func (a *audit) WriteAuditLogDetail(ctx context.Context, _, actorID, action, _ string, d map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ag, _ := actor.AgentFrom(ctx)
	a.entries = append(a.entries, entry{action, actorID, ag.ConnectionID, d})
}

func (a *audit) calls() []entry {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []entry{}
	for _, e := range a.entries {
		if e.action == "agent.call" {
			out = append(out, e)
		}
	}
	return out
}

type projects struct{}

func (projects) ServiceProject(context.Context, string) (string, error)     { return "p1", nil }
func (projects) EnvironmentProject(context.Context, string) (string, error) { return "p1", nil }

type creds struct {
	middleware.APIKeyValidator
	middleware.AgentTokenValidator
}

// TestAgentEndToEnd connects an MCP client the way Claude Code does
// (dynamic registration, PKCE, consent, token), then deploys under Allow,
// waits under Ask until a person approves, is refused under Never and while
// paused, and loses access when revoked. Every call is audited as the agent
// acting for its person (D-100).
func TestAgentEndToEnd(t *testing.T) {
	gin.SetMode(gin.TestMode)
	log := zap.NewNop()
	repo := agenttest.New()
	repo.OrgProjects["org1"] = []string{"p1", "p2"}
	repo.Services["staging-web"] = &agent.ServiceInfo{ID: "staging-web", EnvironmentID: "e1", Class: agent.ClassOther, Label: "storefront / web in staging"}
	repo.Services["prod-web"] = &agent.ServiceInfo{ID: "prod-web", EnvironmentID: "e2", Class: agent.ClassProtected, Label: "storefront / web in production"}
	aud := &audit{}
	svc := agent.NewService(repo, orgs{}, operators{}, aud, log)

	var deploys atomic.Int32
	engine := gin.New()
	ah := agent.NewHandler(svc, func() string { return "" }, nil, log)
	v1 := engine.Group("/api/v1")
	ah.RegisterPublicRoutes(engine, v1, func(c *gin.Context) { c.Next() })
	protected := v1.Group("")
	protected.Use(middleware.AuthWithOrg("secret", creds{nil, svc}, orgs{}, nil), apikey.EnforceScopes(projects{}))
	protected.GET("/projects", func(c *gin.Context) {
		respond.JSON(c, 200, []map[string]any{{"id": "p1", "name": "storefront"}})
	})
	protected.GET("/workspace/services", func(c *gin.Context) {
		respond.JSON(c, 200, []map[string]any{{"id": "staging-web", "name": "web", "project_id": "p1", "environment_name": "staging"}})
	})
	protected.POST("/services/:serviceID/deployments", func(c *gin.Context) {
		if err := svc.Check(c.Request.Context(), actor.Target{Action: "deploy", Tool: "deploy", ServiceID: c.Param("serviceID")}); err != nil {
			var ge *actor.GateError
			if !errors.As(err, &ge) {
				respond.Error(c, 500, err.Error())
				return
			}
			if ge.Status < 400 {
				respond.JSON(c, ge.Status, gin.H{"code": ge.Code, "approval_id": ge.ApprovalID})
				return
			}
			respond.ErrorData(c, ge.Status, ge.Message, gin.H{"code": ge.Code})
			return
		}
		deploys.Add(1)
		respond.JSON(c, 202, gin.H{"id": "dep-" + c.Param("serviceID"), "status": "pending"})
	})
	protected.GET("/services/:serviceID/deployments/:deploymentID", func(c *gin.Context) {
		respond.JSON(c, 200, gin.H{"id": c.Param("deploymentID"), "status": "running"})
	})
	protected.GET("/services/:serviceID/deployments/:deploymentID/logs", func(c *gin.Context) {
		respond.JSON(c, 200, []map[string]any{{"created_at": "t", "level": "info", "message": "healthy"}})
	})
	spec, err := LoadSpec(api.Spec)
	if err != nil {
		t.Fatal(err)
	}
	router := Router{Handler: engine}
	svc.WithDispatcher(router)
	srv := NewServer(spec, router, svc, aud, "test", log)
	srv.poll, srv.progressEvery = 20*time.Millisecond, 20*time.Millisecond
	engine.POST("/mcp", gin.WrapH(srv.Handler(ah.IssuerOf)))
	ts := httptest.NewServer(engine)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The person's consent, as the browser would send it; after the revoke
	// they don't sign it in again.
	var consents atomic.Int32
	fetch := func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
		if consents.Add(1) > 1 {
			return nil, errors.New("the person didn't sign in again")
		}
		u, err := url.Parse(args.URL)
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(args.URL, ts.URL+"/oauth/authorize?") {
			t.Errorf("authorization endpoint %s", args.URL)
		}
		q := u.Query()
		in := agent.Consent{AuthRequest: agent.AuthRequest{ResponseType: q.Get("response_type"), ClientID: q.Get("client_id"),
			RedirectURI: q.Get("redirect_uri"), CodeChallenge: q.Get("code_challenge"), CodeChallengeMethod: q.Get("code_challenge_method"),
			State: q.Get("state"), Scope: q.Get("scope"), Resource: q.Get("resource")},
			Allow: true, OrgID: "org1", AllProjects: true, Scopes: []string{"read", "deploy"}}
		to, err := svc.Authorize(ctx, ts.URL, "u1", in)
		if err != nil {
			return nil, err
		}
		back, _ := url.Parse(to)
		return &auth.AuthorizationResult{Code: back.Query().Get("code"), State: back.Query().Get("state"), Iss: back.Query().Get("iss")}, nil
	}
	oh, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
			ClientName: "Claude Code", RedirectURIs: []string{"http://localhost:54321/callback"}}},
		RedirectURL:              "http://localhost:54321/callback",
		AuthorizationCodeFetcher: fetch,
	})
	if err != nil {
		t.Fatal(err)
	}
	var progress atomic.Int32
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, &sdk.ClientOptions{
		ProgressNotificationHandler: func(context.Context, *sdk.ProgressNotificationClientRequest) { progress.Add(1) },
	})
	session, err := client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: ts.URL + "/mcp", OAuthHandler: oh}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tl := range tools.Tools {
		names[tl.Name] = true
	}
	for _, want := range []string{"list_projects", "get_service", "get_deploy_logs", "get_capacity", "search_docs", "get_issue", "get_diff", "deploy", "rollback"} {
		if !names[want] {
			t.Errorf("tool %s missing", want)
		}
	}
	for _, never := range []string{"read_secret", "change_roles", "delete_org", "remove_server"} {
		if names[never] {
			t.Errorf("%s is exposed", never)
		}
	}

	call := func(name string, args map[string]any) *sdk.CallToolResult {
		t.Helper()
		res, err := session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args, Meta: sdk.Meta{"progressToken": name}})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return res
	}
	text := func(r *sdk.CallToolResult) string { return r.Content[0].(*sdk.TextContent).Text }

	// A read: allowed.
	if r := call("list_projects", nil); r.IsError || !strings.Contains(text(r), "storefront") {
		t.Fatalf("list_projects: %s", text(r))
	}

	// Allow: staging deploys at once and the call waits for the outcome.
	if r := call("deploy", map[string]any{"service_id": "staging-web"}); r.IsError || !strings.Contains(text(r), "healthy") {
		t.Fatalf("deploy under Allow: %s", text(r))
	}
	if deploys.Load() != 1 {
		t.Fatalf("deploys = %d, want 1", deploys.Load())
	}

	// Ask: production waits for a person, who approves it; it runs once.
	if r := call("deploy", map[string]any{"service_id": "prod-web"}); !r.IsError || !strings.Contains(text(r), "reason_required") {
		t.Fatalf("Ask without a reason: %s", text(r))
	}
	done := make(chan *sdk.CallToolResult, 1)
	go func() {
		res, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "deploy", Meta: sdk.Meta{"progressToken": "ask"},
			Arguments: map[string]any{"service_id": "prod-web", "reason": "Fixes STO-118", "plan": "Watch the logs"}})
		if err != nil {
			t.Errorf("deploy under Ask: %v", err)
		}
		done <- res
	}()
	var waiting []agent.Waiting
	for i := 0; i < 200 && len(waiting) == 0; i++ {
		time.Sleep(10 * time.Millisecond)
		waiting, _ = svc.Waiting(ctx, "org1")
	}
	if len(waiting) != 1 || waiting[0].Reason != "Fixes STO-118" {
		t.Fatalf("waiting = %+v", waiting)
	}
	if deploys.Load() != 1 {
		t.Fatal("Ask deployed before a person approved")
	}
	if _, err := svc.Decide(ctx, "org1", "u2", "viewer", waiting[0].ID, true, "", ""); !errors.Is(err, agent.ErrForbidden) {
		t.Fatalf("a viewer approved: %v", err)
	}
	if _, err := svc.Decide(ctx, "org1", "u1", "owner", waiting[0].ID, true, "go", ""); err != nil {
		t.Fatal(err)
	}
	res := <-done
	if res == nil || res.IsError || !strings.Contains(text(res), "healthy") {
		t.Fatalf("deploy after approval: %+v", res)
	}
	if deploys.Load() != 2 {
		t.Fatalf("deploys = %d, want 2 (the approval runs once)", deploys.Load())
	}
	if !strings.Contains(text(res), "u1 approved this at ") || !strings.Contains(text(res), ": “go”") {
		t.Fatalf("the person's note didn't reach the agent: %s", text(res))
	}
	if progress.Load() == 0 {
		t.Error("no progress while waiting")
	}

	// Never.
	if _, err := svc.SetPolicy(ctx, "org1", "u1", "owner", []agent.PolicyCell{{Action: "deploy", Class: "other", Decision: "never"}}, false); err != nil {
		t.Fatal(err)
	}
	if r := call("deploy", map[string]any{"service_id": "staging-web"}); !r.IsError || !strings.Contains(text(r), "agent_policy_never") {
		t.Fatalf("deploy under Never: %s", text(r))
	}
	if _, err := svc.SetPolicy(ctx, "org1", "u1", "owner", []agent.PolicyCell{{Action: "deploy", Class: "other", Decision: "allow"}}, false); !errors.Is(err, agent.ErrReauthRequired) {
		t.Fatalf("loosening without the password: %v", err)
	}

	// Pause-all: every call refused, even reads.
	if _, err := svc.PauseWorkspace(ctx, "org1", "u1", "admin", "owner", true); err != nil {
		t.Fatal(err)
	}
	if r := call("list_projects", nil); !r.IsError || !strings.Contains(text(r), "agents_paused") {
		t.Fatalf("read while paused: %s", text(r))
	}
	if _, err := svc.PauseWorkspace(ctx, "org1", "u1", "admin", "owner", false); err != nil {
		t.Fatal(err)
	}

	// Audit: every call as the agent acting for u1, refused ones included.
	calls := aud.calls()
	outcomes := map[string]int{}
	for _, e := range calls {
		if e.actor != "u1" || e.agent == "" {
			t.Errorf("audit entry not as the agent acting for u1: %+v", e)
		}
		outcomes[e.detail["outcome"].(string)]++
	}
	if outcomes["allowed"] < 2 || outcomes["approved"] != 1 || outcomes["refused"] < 3 {
		t.Errorf("outcomes = %v", outcomes)
	}

	// Revoke: the token stops working.
	conns, _ := svc.ListConnections(ctx, "org1", "u1", "owner")
	if len(conns) != 1 || conns[0].Name != "Claude Code" {
		t.Fatalf("connections = %+v", conns)
	}
	if err := svc.Revoke(ctx, "org1", "u1", "owner", conns[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "list_projects"}); err == nil {
		t.Fatal("a revoked agent's call went through")
	}
}

// TestNoRouteServesAnAgentWithoutPolicy: an agent's token reaches no write
// route but deploy and rollback, whose use cases run the policy.
func TestNoRouteServesAnAgentWithoutPolicy(t *testing.T) {
	spec, err := LoadSpec(api.Spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range spec.Ops {
		if o.Method == http.MethodGet {
			continue
		}
		gin := strings.NewReplacer("{", ":", "}", "").Replace(o.Path)
		if _, ok := apikey.AgentScopeFor(o.Method, gin); ok {
			switch o.ID {
			// Each of these runs the agent policy in its use case (D-100, D-102).
			case "postServicesServiceidDeployments", "postServicesServiceidDeploymentsDeploymentidRollback",
				"startPullRequestPreview", "createBranch", "openPullRequest", "createIssue", "addIssueComment",
				"addPullRequestComment", "postServicesServiceidEnvVars", "postEnvironmentsEnvironmentidEnvVars",
				"postServicesServiceidSecrets", "putServicesServiceidSecretsKey", "postEnvironmentsEnvironmentidSecrets",
				"putEnvironmentsEnvironmentidSecretsKey", "runDocStep":
			default:
				t.Errorf("%s %s reaches agents without a policy gate", o.Method, o.Path)
			}
		}
	}
}
