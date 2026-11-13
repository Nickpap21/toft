package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/internal/agent"
	"github.com/Nickpap21/toft/backend/pkg/actor"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
)

// Agents is what the server needs of the agent service.
type Agents interface {
	ValidateAgentToken(ctx context.Context, raw string) (middleware.AgentIdentity, error)
	AgentApproval(ctx context.Context, connectionID, id string) (*agent.Approval, error)
	Touch(ctx context.Context, connectionID, tool string)
}

// Auditor writes the workspace's audit log. Satisfied by org.Service.
type Auditor interface {
	WriteAuditLogDetail(ctx context.Context, orgID, actorID, action, resource string, detail map[string]any)
}

// Router runs API operations in-process through the API's own handler, as
// the agent: its token, so every check the API makes applies.
type Router struct{ Handler http.Handler }

// Do runs one operation and returns its status and its JSON body.
func (r Router) Do(ctx context.Context, token, method, path string, body map[string]any) (int, map[string]any, error) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return 0, nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, path, &buf)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:0"
	rec := httptest.NewRecorder()
	r.Handler.ServeHTTP(rec, req)
	out := map[string]any{}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec.Code, out, nil
}

// Server is the MCP endpoint.
type Server struct {
	spec   *Spec
	router Router
	agents Agents
	audit  Auditor
	log    *zap.Logger
	srv    *sdk.Server
	// timing, so tests can run the waits fast.
	poll, progressEvery, maxWait time.Duration
}

func NewServer(spec *Spec, router Router, agents Agents, audit Auditor, version string, log *zap.Logger) *Server {
	s := &Server{spec: spec, router: router, agents: agents, audit: audit, log: log,
		poll: 2 * time.Second, progressEvery: 15 * time.Second, maxWait: 20 * time.Minute}
	s.srv = sdk.NewServer(&sdk.Implementation{Name: "toft", Title: "Toft", Version: version}, &sdk.ServerOptions{
		Instructions: "Toft deploys and runs this team's services. Start with list_projects. deploy and rollback follow the workspace's agent policy: some run at once, some wait for a person (give a reason and a plan), some are refused.",
	})
	for i := range spec.Tools {
		t := &spec.Tools[i]
		ann := &sdk.ToolAnnotations{ReadOnlyHint: t.Policy == "", OpenWorldHint: ptr(false)}
		if t.Policy != "" {
			ann.DestructiveHint = ptr(true)
		}
		s.srv.AddTool(&sdk.Tool{Name: t.Name, Description: t.Description, InputSchema: t.Schema, Annotations: ann}, s.handle(t))
	}
	return s
}

func ptr[T any](v T) *T { return &v }

// Handler serves /mcp: Streamable HTTP, stateless, behind the agent's
// bearer token. issuer gives the panel's origin for the 401's pointer to
// the protected resource metadata (RFC 9728).
func (s *Server) Handler(issuer func(*http.Request) string) http.Handler {
	h := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s.srv }, &sdk.StreamableHTTPOptions{Stateless: true})
	verify := func(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		if !strings.HasPrefix(token, agent.AccessTokenPrefix) {
			return nil, auth.ErrInvalidToken
		}
		id, err := s.agents.ValidateAgentToken(ctx, token)
		if err != nil {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{Scopes: id.Scopes, UserID: id.ConnectionID, Extra: map[string]any{"token": token, "agent": id}}, nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A tool call can wait for a person for 30 minutes: no write
		// deadline (as respond.Stream).
		_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
		auth.RequireBearerToken(verify, &auth.RequireBearerTokenOptions{
			ResourceMetadataURL:    issuer(r) + "/.well-known/oauth-protected-resource/mcp",
			AllowMissingExpiration: true,
		})(h).ServeHTTP(w, r)
	})
}

// toolError is a tool's failure, told to the agent as the result.
type toolError struct{ msg string }

func (e *toolError) Error() string { return e.msg }

func fail(format string, a ...any) error { return &toolError{msg: fmt.Sprintf(format, a...)} }

// call is one tool call in progress.
type call struct {
	ctx      context.Context
	s        *Server
	tool     *Tool
	token    string
	agent    middleware.AgentIdentity
	args     map[string]any
	progress func(msg string)
	// What the audit log records: outcome is allowed, asked, approved,
	// denied, expired, refused or failed; seen, how the policy decided.
	outcome, approvalID, resource, detail string
	seen                                  *actor.Seen
	// The person's note on an approval: follows whatever the tool answers.
	note string
}

// op runs one of the tool's operations: path fills its path's {params}.
// A tool may run only the operations the spec lists for it.
func (c *call) op(id string, path map[string]string, query url.Values, body map[string]any) (int, map[string]any, error) {
	if !slices.Contains(c.tool.Operations, id) {
		return 0, nil, fmt.Errorf("tool %s runs %s, which the spec doesn't list for it", c.tool.Name, id)
	}
	o := c.s.spec.Ops[id]
	p := o.Path
	for k, v := range path {
		p = strings.ReplaceAll(p, "{"+k+"}", url.PathEscape(v))
	}
	if strings.Contains(p, "{") {
		return 0, nil, fmt.Errorf("operation %s: a path parameter is missing in %s", id, p)
	}
	if len(query) > 0 {
		p += "?" + query.Encode()
	}
	ctx := c.ctx
	if c.tool.Policy != "" && o.Method != http.MethodGet {
		i, _ := actor.IntentFrom(ctx)
		i.Method, i.Path, i.Body = o.Method, p, body
		ctx = actor.WithIntent(ctx, i)
	}
	return c.s.router.Do(ctx, c.token, o.Method, p, body)
}

// data is a response's payload, its envelope's data.
func data(body map[string]any) any { return body["data"] }

// apiError turns a refused or failed operation into the tool's error, with
// the API's message and code.
func (c *call) apiError(status int, body map[string]any) error {
	msg, _ := body["error"].(string)
	code, _ := body["code"].(string)
	if msg == "" {
		msg = http.StatusText(status)
	}
	if status >= 500 {
		c.outcome = "failed"
	} else {
		c.outcome = "refused"
	}
	if code != "" {
		c.detail = code
		return fail("%s (%s)", msg, code)
	}
	return fail("%s", msg)
}

func (c *call) str(name string) string {
	switch v := c.args[name].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return fmt.Sprintf("%.0f", v)
	}
	return ""
}

func (c *call) num(name string, def int) int {
	if v, ok := c.args[name].(float64); ok && v >= 1 {
		return int(v)
	}
	return def
}

func (c *call) flag(name string, def bool) bool {
	if v, ok := c.args[name].(bool); ok {
		return v
	}
	return def
}

func (c *call) reaches(projectID string) bool {
	return c.agent.AllProjects || slices.Contains(c.agent.ProjectIDs, projectID)
}

// handle runs a tool: its steps, then one audit entry and the connection's
// last call.
func (s *Server) handle(t *Tool) sdk.ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var ti *auth.TokenInfo
		if req.Extra != nil {
			ti = req.Extra.TokenInfo
		}
		if ti == nil {
			return nil, errors.New("no token")
		}
		token, _ := ti.Extra["token"].(string)
		id, _ := ti.Extra["agent"].(middleware.AgentIdentity)
		args := map[string]any{}
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return errorResult("the arguments must be a JSON object"), nil
			}
		}
		props, _ := t.Schema["properties"].(map[string]any)
		for k := range args {
			if _, ok := props[k]; !ok {
				return errorResult(fmt.Sprintf("%s takes no argument %q", t.Name, k)), nil
			}
		}
		req0, _ := t.Schema["required"].([]string)
		for _, k := range req0 {
			if _, ok := args[k]; !ok {
				return errorResult(fmt.Sprintf("%s needs %s", t.Name, k)), nil
			}
		}
		actx := actor.WithAgent(ctx, actor.Agent{ConnectionID: id.ConnectionID, Name: id.Name, UserID: id.UserID, OrgID: id.OrgID})
		seen := &actor.Seen{}
		if t.Policy != "" {
			r, _ := args["reason"].(string)
			p, _ := args["plan"].(string)
			ctx = actor.WithIntent(ctx, actor.Intent{Tool: t.Name, Reason: r, Plan: p, PlanSteps: planStepsArg(args["plan_steps"]), Seen: seen})
		}
		var n float64
		c := &call{ctx: ctx, s: s, tool: t, token: token, agent: id, args: args, outcome: "allowed", resource: "org:" + id.OrgID, seen: seen,
			progress: func(msg string) {
				if pt := req.Params.GetProgressToken(); pt != nil && req.Session != nil {
					n++
					_ = req.Session.NotifyProgress(ctx, &sdk.ProgressNotificationParams{ProgressToken: pt, Message: msg, Progress: n})
				}
			}}
		out, err := steps[t.Name](c)
		s.record(actx, c, err)
		s.agents.Touch(context.WithoutCancel(ctx), id.ConnectionID, t.Name)
		if err != nil {
			var te *toolError
			if errors.As(err, &te) {
				return errorResult(te.msg + c.note), nil
			}
			s.log.Error("mcp tool failed", zap.String("tool", t.Name), zap.Error(err))
			return errorResult("Toft failed to run " + t.Name + "; try again" + c.note), nil
		}
		raw, _ := json.MarshalIndent(out, "", " ")
		text := string(raw)
		if len(text) > maxResult {
			text = text[:maxResult] + "\n… (cut at 80000 characters: ask for less, a commit or a narrower range)"
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: text + c.note}}}, nil
	}
}

const maxResult = 80000

// planStepsArg reads plan_steps as the tool's schema shapes it; the agent
// policy keeps it within the input's limits.
func planStepsArg(v any) []actor.PlanStep {
	list, _ := v.([]any)
	out := make([]actor.PlanStep, 0, len(list))
	for _, item := range list {
		m, _ := item.(map[string]any)
		text, _ := m["text"].(string)
		tool, _ := m["tool"].(string)
		svc, _ := m["service_id"].(string)
		out = append(out, actor.PlanStep{Text: text, Tool: tool, ServiceID: svc})
	}
	return out
}

func errorResult(msg string) *sdk.CallToolResult {
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: msg}}}
}

// record writes the call's audit entry: "<agent> acting for <user>", the
// tool, its target, its outcome, and the rule that decided (D-102).
// Arguments go in only from the tool's own inputs, never one marked secret
// (set_secret's value); reason and plan are cut short.
func (s *Server) record(ctx context.Context, c *call, err error) {
	if err != nil && c.outcome == "allowed" {
		c.outcome = "failed"
	}
	args := map[string]any{}
	for _, k := range slices.Sorted(maps.Keys(c.args)) {
		if _, ok := c.tool.Inputs[k]; !ok || c.tool.Secret[k] {
			continue
		}
		v := c.args[k]
		if str, ok := v.(string); ok && len([]rune(str)) > 200 {
			v = string([]rune(str)[:200]) + "…"
		}
		args[k] = v
	}
	d := map[string]any{"tool": c.tool.Name, "outcome": c.outcome, "args": args}
	if c.approvalID != "" {
		d["approval_id"] = c.approvalID
	}
	if c.detail != "" {
		d["detail"] = c.detail
	}
	if c.seen != nil && c.seen.Level != "" {
		d["level"], d["env_class"] = c.seen.Level, c.seen.Class
	}
	if c.seen != nil && c.seen.CoveredBy != "" {
		// A rollback an approval of this agent's deploy covered (D-146).
		d["covered_by"], d["approved_by"] = c.seen.CoveredBy, c.seen.ApprovedBy
	}
	s.audit.WriteAuditLogDetail(context.WithoutCancel(ctx), c.agent.OrgID, c.agent.UserID, "agent.call", c.resource, d)
}
