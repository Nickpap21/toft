package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/api"
	"github.com/Nickpap21/toft/backend/pkg/actor"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// TestToolsMatchSpec is the CI check that the spec's x-toft-tools and the
// code agree (D-18, D-100): every input resolves to a field of one of its
// tool's operations (LoadSpec), every tool has steps and every step a tool,
// acting tools are under the policy, and a tool's steps run only the
// operations the spec lists for it.
func TestToolsMatchSpec(t *testing.T) {
	spec, err := LoadSpec(api.Spec)
	if err != nil {
		t.Fatal(err)
	}
	inSpec := map[string]bool{}
	for _, tool := range spec.Tools {
		inSpec[tool.Name] = true
		if steps[tool.Name] == nil {
			t.Errorf("tool %s has no steps", tool.Name)
		}
		if !slices.Contains([]string{"read", "deploy", "code", "issues", "variables", "runbooks"}, tool.Scope) {
			t.Errorf("tool %s: scope %q", tool.Name, tool.Scope)
		}
		if (tool.Scope != "read") != (tool.Policy != "") {
			t.Errorf("tool %s: an acting tool must name its policy row, a reading one none", tool.Name)
		}
		if tool.Policy != "" {
			for _, in := range []string{"reason", "plan", "wait"} {
				if _, ok := tool.Inputs[in]; !ok && (in != "wait" || tool.Name == "deploy" || tool.Name == "rollback") {
					t.Errorf("tool %s has no %s input", tool.Name, in)
				}
			}
		}
		if tool.Description == "" || len(tool.Operations) == 0 {
			t.Errorf("tool %s: no description or operations", tool.Name)
		}
	}
	for name := range steps {
		if !inSpec[name] {
			t.Errorf("steps %s have no tool in the spec", name)
		}
	}
	for _, never := range []string{"read_secret", "change_roles", "delete_org", "remove_server"} {
		if inSpec[never] || steps[never] != nil {
			t.Errorf("%s is exposed", never)
		}
	}

	// Run every tool against a router that answers anything: a step that
	// runs an operation the spec doesn't list for its tool fails.
	answer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status, body := http.StatusOK, map[string]any{"data": []any{map[string]any{"id": "x", "status": "running", "project_id": "p"}}}
		if r.Method == http.MethodPost {
			status, body = http.StatusAccepted, map[string]any{"data": map[string]any{"id": "d", "status": "pending"}}
		} else if strings.Contains(r.URL.Path, "/deployments/") && !strings.HasSuffix(r.URL.Path, "/logs") {
			body = map[string]any{"data": map[string]any{"id": "d", "status": "running"}}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	})
	s := &Server{spec: spec, router: Router{Handler: answer}, log: zap.NewNop(), poll: time.Millisecond, progressEvery: time.Hour, maxWait: 50 * time.Millisecond}
	for i := range spec.Tools {
		tool := &spec.Tools[i]
		args := map[string]any{}
		for name := range tool.Inputs {
			args[name] = "1"
		}
		for _, only := range [][]string{nil, {"base", "head"}, {"sha"}, {"page_id"}} {
			a := map[string]any{}
			for k, v := range args {
				if slices.Contains([]string{"pull_number", "sha", "base", "head", "page_id", "deployment_id", "server_id", "query", "repository_id", "wait", "lines"}, k) &&
					(only == nil || !slices.Contains(only, k)) {
					continue
				}
				a[k] = v
			}
			if tool.Name == "search_docs" && only == nil {
				a["query"] = "x"
			}
			c := &call{ctx: context.Background(), s: s, tool: tool, token: "t", agent: middleware.AgentIdentity{AllProjects: true},
				args: a, progress: func(string) {}}
			if _, err := steps[tool.Name](c); err != nil && strings.Contains(err.Error(), "doesn't list") {
				t.Errorf("tool %s: %v", tool.Name, err)
			}
		}
	}
}

// TestToolDescriptionsMatchResults: what a tool's description promises is
// in what its steps return. Each case runs a tool against a router that
// answers every operation with an example of its response schema in the
// spec, and names, for words of the description, where the result carries
// them. A new tool needs a case; dropping a field from a step or from the
// API's schema fails.
func TestToolDescriptionsMatchResults(t *testing.T) {
	spec, err := LoadSpec(api.Spec)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(api.Spec, &doc); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		tool     string
		args     map[string]any
		promises map[string]string // a word of the description → a path in the result
		status   any               // every "status" property's value, when the enum's first won't do
	}{
		{"list_projects", nil, map[string]string{"IDs": "projects.0.id", "names": "projects.0.name", "services": "projects.0.services.0.id",
			"environment": "projects.0.services.0.environment", "status": "projects.0.services.0.status",
			"source": "projects.0.services.0.source", "server": "projects.0.services.0.server_id"}, nil},
		{"get_service", map[string]any{"service_id": "s"}, map[string]string{"source": "service.source_type", "settings": "service.port",
			"status": "service.runtime_status", "domains": "domains.0.hostname", "isolation": "service.isolation", "deployments": "deployments.0.id"}, nil},
		{"get_deploy_logs", map[string]any{"service_id": "s"}, map[string]string{"status": "deployment.status", "lines": "lines.0"}, nil},
		// getServers has no response schema in the spec: with server_id.
		{"get_capacity", map[string]any{"server_id": "sv"}, map[string]string{"has": "servers.0.effective_cpu_cores", "allocated": "servers.0.allocated_cpu_cores",
			"workspaces": "servers.0.workspaces.0.org_name", "projects": "servers.0.workspaces.0.projects.0"}, nil},
		{"search_docs", map[string]any{"query": "q"}, map[string]string{"docs pages": "pages.0.title"}, nil},
		{"search_docs", map[string]any{"project_id": "p", "page_id": "pg"}, map[string]string{"markdown": "body"}, nil},
		{"get_issue", map[string]any{"project_id": "p", "issue_number": "1"}, map[string]string{"title": "title", "description": "description",
			"status": "status", "priority": "priority", "assignee": "assignee", "cycle": "cycle", "links": "links.0"}, nil},
		{"get_diff", map[string]any{"project_id": "p", "pull_number": "1"}, map[string]string{"diff": "0.patch"}, nil},
		{"deploy", map[string]any{"service_id": "s", "wait": false}, map[string]string{"starts": "deployment.id"}, nil},
		{"deploy", map[string]any{"service_id": "s"}, map[string]string{"outcome": "deployment.status", "last log lines": "last_lines.0"}, "running"},
		{"rollback", map[string]any{"service_id": "s", "deployment_id": "d"}, map[string]string{"outcome": "deployment.status"}, "running"},
		{"create_branch", map[string]any{"project_id": "p", "name": "agent/x"}, map[string]string{"branch": "branch.branch", "password": "branch.push.password"}, nil},
		{"open_pr", map[string]any{"project_id": "p", "head": "agent/x", "title": "t"}, map[string]string{"pull request": "number",
			"head": "head_branch", "base": "base_branch"}, nil},
		{"create_issue", map[string]any{"project_id": "p", "title": "t"}, map[string]string{"issue": "number", "title": "title",
			"description": "description", "priority": "priority", "labels": "labels", "type": "type"}, nil},
		{"comment", map[string]any{"project_id": "p", "issue_number": "1", "body": "b"}, map[string]string{"comment": "body"}, nil},
		{"set_env_var", map[string]any{"service_id": "s", "key": "K", "value": "v"}, map[string]string{"variable": "variable.key", "next deploy": "next"}, nil},
		{"set_secret", map[string]any{"service_id": "s", "key": "K", "value": "v"}, map[string]string{"secret": "secret", "next deploy": "next"}, nil},
		{"run_runbook_step", map[string]any{"project_id": "p", "page_id": "pg", "step": float64(1)}, map[string]string{"last lines": "lines.0"}, nil},
		{"create_preview", map[string]any{"project_id": "p", "pull_number": "1"}, map[string]string{"preview": "preview.status"}, nil},
	}
	tested := map[string]bool{}
	for _, tc := range cases {
		tested[tc.tool] = true
		var tool *Tool
		for i := range spec.Tools {
			if spec.Tools[i].Name == tc.tool {
				tool = &spec.Tools[i]
			}
		}
		if tool == nil {
			t.Errorf("no tool %s", tc.tool)
			continue
		}
		answer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var found []*Operation
			for _, id := range tool.Operations {
				o := spec.Ops[id]
				pattern := "^" + regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(o.Path, "[^/]+") + "$"
				if o.Method == r.Method && regexp.MustCompile(pattern).MatchString(r.URL.Path) {
					found = append(found, o)
				}
			}
			if len(found) != 1 {
				t.Errorf("%s: %s %s matches %d of its operations", tc.tool, r.Method, r.URL.Path, len(found))
				w.WriteHeader(http.StatusNotFound)
				return
			}
			responses := dig(doc, "paths", found[0].Path, strings.ToLower(r.Method), "responses")
			for _, st := range []int{200, 201, 202} {
				if res := resolve(doc, responses[strconv.Itoa(st)]); res != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(st)
					_ = json.NewEncoder(w).Encode(example(doc, dig(res, "content", "application/json", "schema"), tc.status, 0))
					return
				}
			}
			t.Errorf("%s: %s answers no 200, 201 or 202", tc.tool, found[0].ID)
		})
		s := &Server{spec: spec, router: Router{Handler: answer}, log: zap.NewNop(), poll: time.Millisecond, progressEvery: time.Hour, maxWait: 50 * time.Millisecond}
		c := &call{ctx: context.Background(), s: s, tool: tool, token: "t", agent: middleware.AgentIdentity{AllProjects: true},
			args: tc.args, progress: func(string) {}}
		out, err := steps[tc.tool](c)
		if err != nil {
			t.Errorf("%s: %v", tc.tool, err)
			continue
		}
		var result any
		raw, _ := json.Marshal(out)
		_ = json.Unmarshal(raw, &result)
		for word, path := range tc.promises {
			if !strings.Contains(strings.ToLower(tool.Description), strings.ToLower(word)) {
				t.Errorf("%s's description doesn't say %q", tc.tool, word)
			}
			v := result
			for _, k := range strings.Split(path, ".") {
				if l, ok := v.([]any); ok && k == "0" && len(l) > 0 {
					v = l[0]
				} else {
					v = obj(v)[k]
				}
			}
			if v == nil {
				t.Errorf("%s promises %s, but its result has no %s", tc.tool, word, path)
			}
		}
	}
	for _, tool := range spec.Tools {
		if !tested[tool.Name] {
			t.Errorf("tool %s has no case: say where its result carries what its description promises", tool.Name)
		}
	}
}

// example is a value of an OpenAPI schema: every property of an object,
// one item of an array, an enum's first value; status, unless nil, for
// every property named status.
func example(doc map[string]any, v, status any, depth int) any {
	m := resolve(doc, v)
	if m == nil || depth > 10 {
		return nil
	}
	if e, ok := m["enum"].([]any); ok && len(e) > 0 {
		return e[0]
	}
	for _, k := range []string{"oneOf", "anyOf"} {
		if l := asList(m[k]); len(l) > 0 {
			return example(doc, l[0], status, depth+1)
		}
	}
	out := map[string]any{}
	for _, part := range asList(m["allOf"]) {
		maps.Copy(out, obj(example(doc, part, status, depth+1)))
	}
	switch m["type"] {
	case "array":
		return []any{example(doc, m["items"], status, depth+1)}
	case "string":
		return "x"
	case "integer", "number":
		return 1
	case "boolean":
		return true
	}
	props, _ := m["properties"].(map[string]any)
	for name, p := range props {
		out[name] = example(doc, p, status, depth+1)
		if name == "status" && status != nil {
			out[name] = status
		}
	}
	return out
}

// set_secret's value is never in its audit entry (D-102): inputs marked
// secret in x-toft-tools are left out; the rule that decided goes in.
func TestSecretInputsAreNeverRecorded(t *testing.T) {
	spec, err := LoadSpec(api.Spec)
	if err != nil {
		t.Fatal(err)
	}
	var tool *Tool
	for i := range spec.Tools {
		if spec.Tools[i].Name == "set_secret" {
			tool = &spec.Tools[i]
		}
	}
	if tool == nil || !tool.Secret["value"] {
		t.Fatalf("set_secret's value isn't marked secret: %+v", tool)
	}
	aud := &audit{}
	s := &Server{spec: spec, audit: aud, log: zap.NewNop()}
	c := &call{ctx: context.Background(), s: s, tool: tool, args: map[string]any{"service_id": "s1", "key": "STRIPE_KEY", "value": "sk_live_secret"},
		outcome: "asked", seen: &actor.Seen{Level: "workspace", Class: "other"}}
	s.record(context.Background(), c, nil)
	raw := fmt.Sprintf("%+v", aud.calls())
	if strings.Contains(raw, "sk_live_secret") || !strings.Contains(raw, "STRIPE_KEY") || !strings.Contains(raw, "level:workspace") {
		t.Fatalf("the audit entry: %s", raw)
	}
}

// run_runbook_step counts steps from 1, as the page and the request page do;
// the API's path counts from 0.
func TestRunbookStepCountsFromOne(t *testing.T) {
	spec, err := LoadSpec(api.Spec)
	if err != nil {
		t.Fatal(err)
	}
	var tool *Tool
	for i := range spec.Tools {
		if spec.Tools[i].Name == "run_runbook_step" {
			tool = &spec.Tools[i]
		}
	}
	var ran string
	router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{"data": map[string]any{"version": 3}}
		if r.Method == http.MethodPost {
			ran = r.URL.Path
			body = map[string]any{"data": map[string]any{"outcome": "ok", "lines": []any{"done"}}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	})
	s := &Server{spec: spec, router: Router{Handler: router}, log: zap.NewNop()}
	run := func(step float64) error {
		c := &call{ctx: context.Background(), s: s, tool: tool, token: "t",
			args: map[string]any{"project_id": "p", "page_id": "pg", "step": step}, progress: func(string) {}}
		_, err := runRunbookStep(c)
		return err
	}
	if err := run(1); err != nil || ran != "/api/v1/projects/p/docs/pg/steps/0/run" {
		t.Fatalf("step 1 ran %q: %v", ran, err)
	}
	ran = ""
	if err := run(0); err == nil || ran != "" {
		t.Fatalf("step 0 was accepted (%q)", ran)
	}
}
