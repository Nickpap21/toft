package apikey

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/envvar"
)

// The four actions agents never get (D-100), whatever their scopes: reading
// a secret, changing a role, deleting the workspace, removing a server. With
// session 2's scopes (D-102), an agent sets a secret and lists secrets'
// names; no route answers with a secret's value, and the listing carries
// none.
func TestAgentsNeverReachTheUnexposedActions(t *testing.T) {
	for _, r := range []struct{ name, method, path string }{
		{"read_secret", http.MethodGet, "/api/v1/services/:serviceID/secrets/:key"},
		{"read_secret", http.MethodGet, "/api/v1/environments/:environmentID/secrets/:key"},
		{"change_roles", http.MethodPut, "/api/v1/org/members/:userID/role"},
		{"change_roles", http.MethodDelete, "/api/v1/org/members/:userID"},
		{"delete_org", http.MethodDelete, "/api/v1/org"},
		{"remove_server", http.MethodDelete, "/api/v1/servers/:serverID"},
	} {
		if scope, ok := AgentScopeFor(r.method, r.path); ok {
			t.Errorf("%s: %s %s reaches agents with scope %s", r.name, r.method, r.path, scope)
		}
	}
	for i := range reflect.TypeFor[envvar.SecretDTO]().NumField() {
		f := reflect.TypeFor[envvar.SecretDTO]().Field(i)
		if tag := strings.ToLower(f.Tag.Get("json")); strings.Contains(tag, "value") || f.Name == "Value" {
			t.Errorf("a secret's listing carries its value (%s)", f.Name)
		}
	}
}

// A catalog app's admin password kept for its creator (D-160) never
// reaches an agent or a project key, whatever its scopes: neither is
// listed, so both are refused before the handler, which also refuses any
// key (session_required; cmd/server's route table records it).
func TestAdminPasswordsReachNoAgentNorKey(t *testing.T) {
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/services/:serviceID/admin-password"},
		{http.MethodGet, "/api/v1/services/:serviceID/admin-password/value"},
		{http.MethodDelete, "/api/v1/services/:serviceID/admin-password"},
		{http.MethodPost, "/api/v1/services/:serviceID/admin-password"},
	} {
		if scope, ok := AgentScopeFor(r.method, r.path); ok {
			t.Errorf("%s %s reaches agents with scope %s", r.method, r.path, scope)
		}
		if scope, listed := routeScopes[r.method+" "+r.path]; listed {
			t.Errorf("%s %s reaches project keys with scope %s", r.method, r.path, scope)
		}
	}
}

// What D-102 keeps from agents whatever their scopes: deleting or replacing
// all variables, merging, approving a preview, changing a repository or its
// protection, deleting a branch or a comment, deploy-policy exceptions, the
// agent policy itself.
func TestAgentsNeverReachPeoplesWrites(t *testing.T) {
	for _, r := range []struct{ method, path string }{
		{http.MethodPut, "/api/v1/services/:serviceID/env-vars"},
		{http.MethodDelete, "/api/v1/services/:serviceID/env-vars/:key"},
		{http.MethodDelete, "/api/v1/services/:serviceID/secrets/:key"},
		{http.MethodPut, "/api/v1/environments/:environmentID/env-vars"},
		{http.MethodDelete, "/api/v1/environments/:environmentID/secrets/:key"},
		{http.MethodGet, "/api/v1/services/:serviceID/env-vars"},
		{http.MethodPost, "/api/v1/projects/:projectID/repositories/:repositoryID/pulls/:pullNumber/merge"},
		{http.MethodPost, "/api/v1/projects/:projectID/repositories/:repositoryID/pulls/:pullNumber/preview/approve"},
		{http.MethodPatch, "/api/v1/projects/:projectID/repositories/:repositoryID"},
		{http.MethodPatch, "/api/v1/projects/:projectID/repositories/:repositoryID/pulls/:pullNumber"},
		{http.MethodDelete, "/api/v1/projects/:projectID/repositories/:repositoryID/pulls/:pullNumber/comments/:commentID"},
		{http.MethodPatch, "/api/v1/projects/:projectID/issues/:issueNumber"},
		{http.MethodDelete, "/api/v1/projects/:projectID/issues/:issueNumber/comments/:commentID"},
		{http.MethodPost, "/api/v1/services/:serviceID/deploy-policy/exceptions"},
		{http.MethodPut, "/api/v1/agents/policy"},
		{http.MethodPut, "/api/v1/agents/policy/projects/:projectID"},
		{http.MethodPost, "/api/v1/agents/approvals/:approvalID/approve"},
	} {
		if scope, ok := AgentScopeFor(r.method, r.path); ok {
			t.Errorf("%s %s reaches agents with scope %s", r.method, r.path, scope)
		}
	}
}

func TestAgentScopes(t *testing.T) {
	for _, r := range []struct {
		method, path, want string
		ok                 bool
	}{
		{http.MethodGet, "/api/v1/services/:serviceID", AgentScopeRead, true},
		{http.MethodGet, "/api/v1/services/:serviceID/domains", AgentScopeRead, true},
		{http.MethodPost, "/api/v1/services/:serviceID/domains", "", false},
		{http.MethodGet, "/api/v1/projects/:projectID/issues/:issueNumber", AgentScopeRead, true},
		{http.MethodGet, "/api/v1/projects/:projectID/docs/:pageID", AgentScopeRead, true},
		{http.MethodGet, "/api/v1/workspace/search", AgentScopeRead, true},
		{http.MethodPost, "/api/v1/services/:serviceID/deployments", AgentScopeDeploy, true},
		{http.MethodPost, "/api/v1/services/:serviceID/deployments/:deploymentID/rollback", AgentScopeDeploy, true},
		{http.MethodPost, "/api/v1/projects/:projectID/repositories/:repositoryID/pulls/:pullNumber/preview", AgentScopeDeploy, true},
		{http.MethodPost, "/api/v1/projects/:projectID/repositories/:repositoryID/branches", AgentScopeCode, true},
		{http.MethodPost, "/api/v1/projects/:projectID/repositories/:repositoryID/pulls", AgentScopeCode, true},
		{http.MethodPost, "/api/v1/projects/:projectID/issues", AgentScopeIssues, true},
		{http.MethodPost, "/api/v1/projects/:projectID/repositories/:repositoryID/pulls/:pullNumber/comments", AgentScopeIssues, true},
		{http.MethodPost, "/api/v1/services/:serviceID/env-vars", AgentScopeVariables, true},
		{http.MethodPut, "/api/v1/environments/:environmentID/secrets/:key", AgentScopeVariables, true},
		{http.MethodPost, "/api/v1/projects/:projectID/docs/:pageID/steps/:step/run", AgentScopeRunbooks, true},
		{http.MethodGet, "/api/v1/agents/connections", "", false},
	} {
		got, ok := AgentScopeFor(r.method, r.path)
		if ok != r.ok || got != r.want {
			t.Errorf("%s %s = %q %v, want %q %v", r.method, r.path, got, ok, r.want, r.ok)
		}
	}
}
