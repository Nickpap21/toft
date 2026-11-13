package apikey

import (
	"context"
	"net/http"
	"slices"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
)

// routeScopes lists every route a project-scoped key may call, by method and
// route pattern, with the scope it needs. Deny by default: a route that is
// not here is refused, so a new route stays closed to scoped keys until it is
// added. Every route here names a :serviceID, an :environmentID or a
// :projectID, which EnforceScopes checks is in the key's project; the
// handlers then check that a :deploymentID belongs to that service and a
// :repositoryID to that project.
var routeScopes = map[string]string{
	"GET /api/v1/services/:serviceID":                                       ScopeDeploy,
	"GET /api/v1/services/:serviceID/domains":                               ScopeDeploy,
	"POST /api/v1/services/:serviceID/deployments":                          ScopeDeploy,
	"GET /api/v1/services/:serviceID/deployments":                           ScopeDeploy,
	"GET /api/v1/services/:serviceID/deployments/:deploymentID":             ScopeDeploy,
	"GET /api/v1/services/:serviceID/deployments/:deploymentID/logs":        ScopeDeploy,
	"GET /api/v1/services/:serviceID/deployments/:deploymentID/logs/stream": ScopeDeploy,
	"POST /api/v1/services/:serviceID/deployments/:deploymentID/rollback":   ScopeDeploy,
	"GET /api/v1/services/:serviceID/env-vars":                              ScopeVariables,
	"POST /api/v1/services/:serviceID/env-vars":                             ScopeVariables,
	"PUT /api/v1/services/:serviceID/env-vars":                              ScopeVariables,
	"DELETE /api/v1/services/:serviceID/env-vars/:key":                      ScopeVariables,
	"GET /api/v1/services/:serviceID/secrets":                               ScopeVariables,
	"POST /api/v1/services/:serviceID/secrets":                              ScopeVariables,
	"PUT /api/v1/services/:serviceID/secrets/:key":                          ScopeVariables,
	"DELETE /api/v1/services/:serviceID/secrets/:key":                       ScopeVariables,
	"GET /api/v1/environments/:environmentID/env-vars":                      ScopeVariables,
	"POST /api/v1/environments/:environmentID/env-vars":                     ScopeVariables,
	"PUT /api/v1/environments/:environmentID/env-vars":                      ScopeVariables,
	"DELETE /api/v1/environments/:environmentID/env-vars/:key":              ScopeVariables,
	"GET /api/v1/environments/:environmentID/secrets":                       ScopeVariables,
	"POST /api/v1/environments/:environmentID/secrets":                      ScopeVariables,
	"PUT /api/v1/environments/:environmentID/secrets/:key":                  ScopeVariables,
	"DELETE /api/v1/environments/:environmentID/secrets/:key":               ScopeVariables,
	// The repository browser (D-73), read-only.
	"GET /api/v1/projects/:projectID/repositories":                                 ScopeCodeRead,
	"GET /api/v1/projects/:projectID/repositories/:repositoryID":                   ScopeCodeRead,
	"GET /api/v1/projects/:projectID/repositories/:repositoryID/branches":          ScopeCodeRead,
	"GET /api/v1/projects/:projectID/repositories/:repositoryID/tags":              ScopeCodeRead,
	"GET /api/v1/projects/:projectID/repositories/:repositoryID/tree":              ScopeCodeRead,
	"GET /api/v1/projects/:projectID/repositories/:repositoryID/blob":              ScopeCodeRead,
	"GET /api/v1/projects/:projectID/repositories/:repositoryID/raw":               ScopeCodeRead,
	"GET /api/v1/projects/:projectID/repositories/:repositoryID/commits":           ScopeCodeRead,
	"GET /api/v1/projects/:projectID/repositories/:repositoryID/commits/:sha":      ScopeCodeRead,
	"GET /api/v1/projects/:projectID/repositories/:repositoryID/commits/:sha/diff": ScopeCodeRead,
	// Pull requests (D-75): code_write keys open them and comment; merging,
	// closing and repository settings need a person.
	"GET /api/v1/projects/:projectID/repositories/:repositoryID/compare":                                 ScopeCodeRead,
	"GET /api/v1/projects/:projectID/repositories/:repositoryID/compare/diff":                            ScopeCodeRead,
	"GET /api/v1/projects/:projectID/repositories/:repositoryID/pulls":                                   ScopeCodeRead,
	"GET /api/v1/projects/:projectID/repositories/:repositoryID/pulls/:pullNumber":                       ScopeCodeRead,
	"GET /api/v1/projects/:projectID/repositories/:repositoryID/pulls/:pullNumber/diff":                  ScopeCodeRead,
	"GET /api/v1/projects/:projectID/repositories/:repositoryID/pulls/:pullNumber/timeline":              ScopeCodeRead,
	"POST /api/v1/projects/:projectID/repositories/:repositoryID/pulls":                                  ScopeCodeWrite,
	"POST /api/v1/projects/:projectID/repositories/:repositoryID/pulls/:pullNumber/comments":             ScopeCodeWrite,
	"PATCH /api/v1/projects/:projectID/repositories/:repositoryID/pulls/:pullNumber/comments/:commentID": ScopeCodeWrite,
	// Checks and pipelines (D-77): a checks key reads and reports a commit's
	// checks; code keys read them too.
	"GET /api/v1/projects/:projectID/repositories/:repositoryID/commits/:sha/checks":  ScopeChecks,
	"POST /api/v1/projects/:projectID/repositories/:repositoryID/commits/:sha/checks": ScopeChecks,
	"GET /api/v1/projects/:projectID/repositories/:repositoryID/pipelines":            ScopeCodeRead,
	// Issues (D-81): issues_write opens, changes and comments; saved views
	// and their changes need a person.
	"GET /api/v1/projects/:projectID/issues":                                     ScopeIssuesRead,
	"GET /api/v1/projects/:projectID/issue-counts":                               ScopeIssuesRead,
	"GET /api/v1/projects/:projectID/issue-labels":                               ScopeIssuesRead,
	"GET /api/v1/projects/:projectID/issue-views":                                ScopeIssuesRead,
	"GET /api/v1/projects/:projectID/issues/:issueNumber":                        ScopeIssuesRead,
	"GET /api/v1/projects/:projectID/issues/:issueNumber/timeline":               ScopeIssuesRead,
	"POST /api/v1/projects/:projectID/issues":                                    ScopeIssuesWrite,
	"PATCH /api/v1/projects/:projectID/issues/:issueNumber":                      ScopeIssuesWrite,
	"POST /api/v1/projects/:projectID/issues/:issueNumber/comments":              ScopeIssuesWrite,
	"PATCH /api/v1/projects/:projectID/issues/:issueNumber/comments/:commentID":  ScopeIssuesWrite,
	"DELETE /api/v1/projects/:projectID/issues/:issueNumber/comments/:commentID": ScopeIssuesWrite,
	// D-83: relations and clones; a scoped key clones within its project.
	"GET /api/v1/projects/:projectID/issue-settings":                               ScopeIssuesRead,
	"POST /api/v1/projects/:projectID/issues/:issueNumber/relations":               ScopeIssuesWrite,
	"DELETE /api/v1/projects/:projectID/issues/:issueNumber/relations/:relationID": ScopeIssuesWrite,
	"POST /api/v1/projects/:projectID/issues/:issueNumber/clone":                   ScopeIssuesWrite,
	// D-90: the workspace's cycles as the project sees them, and its Timeline.
	"GET /api/v1/projects/:projectID/cycles":         ScopeIssuesRead,
	"GET /api/v1/projects/:projectID/issue-timeline": ScopeIssuesRead,
	// D-88: an issue from a failed deploy of the key's project.
	"POST /api/v1/services/:serviceID/deployments/:deploymentID/issue": ScopeIssuesWrite,
	// Docs (D-86): docs_write writes; running a page's commands needs a
	// person until stage 6.
	"GET /api/v1/projects/:projectID/docs":                           ScopeDocsRead,
	"GET /api/v1/projects/:projectID/docs/:pageID":                   ScopeDocsRead,
	"GET /api/v1/projects/:projectID/docs/:pageID/versions":          ScopeDocsRead,
	"GET /api/v1/projects/:projectID/docs/:pageID/versions/:version": ScopeDocsRead,
	"POST /api/v1/projects/:projectID/docs":                          ScopeDocsWrite,
	"PATCH /api/v1/projects/:projectID/docs/:pageID":                 ScopeDocsWrite,
	"DELETE /api/v1/projects/:projectID/docs/:pageID":                ScopeDocsWrite,
	"POST /api/v1/projects/:projectID/docs/:pageID/restore":          ScopeDocsWrite,
	"POST /api/v1/projects/:projectID/docs/:pageID/verify":           ScopeDocsWrite,
	// Previews (D-97): code keys read them; a deploy key starts, stops and
	// redeploys them. Approving needs a person, and so do the settings.
	"GET /api/v1/projects/:projectID/previews":                                                       ScopeCodeRead,
	"GET /api/v1/projects/:projectID/repositories/:repositoryID/pulls/:pullNumber/preview":           ScopeCodeRead,
	"POST /api/v1/projects/:projectID/repositories/:repositoryID/pulls/:pullNumber/preview":          ScopeDeploy,
	"POST /api/v1/projects/:projectID/repositories/:repositoryID/pulls/:pullNumber/preview/stop":     ScopeDeploy,
	"POST /api/v1/projects/:projectID/repositories/:repositoryID/pulls/:pullNumber/preview/redeploy": ScopeDeploy,
}

// ProjectResolver says which project a service or an environment belongs to
// ("" for a workspace-shared service, which no scoped key reaches).
type ProjectResolver interface {
	ServiceProject(ctx context.Context, serviceID string) (string, error)
	EnvironmentProject(ctx context.Context, environmentID string) (string, error)
}

const scopeRefused = "this API key is limited to its scopes in one project"

const servicesRefused = "this API key is limited to some services of its project"

// EnforceScopes refuses what a project-scoped API key may not do: any route
// not in routeScopes, a route whose scope the key lacks, and a service or
// environment outside the key's project. JWTs and full-access keys pass.
// It runs after middleware.AuthWithOrg, which marks scoped keys.
func EnforceScopes(projects ProjectResolver) gin.HandlerFunc {
	return func(c *gin.Context) {
		if a, ok := middleware.AgentFrom(c); ok {
			enforceAgent(c, projects, a)
			return
		}
		projectID, scopes, scoped := middleware.APIKeyScopeFrom(c)
		if !scoped {
			c.Next()
			return
		}
		need, listed := routeScopes[c.Request.Method+" "+c.FullPath()]
		// code_write is code_read and push; code keys read checks too.
		code := slices.Contains(scopes, ScopeCodeRead) || slices.Contains(scopes, ScopeCodeWrite)
		has := slices.Contains(scopes, need) || need == ScopeCodeRead && slices.Contains(scopes, ScopeCodeWrite) ||
			need == ScopeIssuesRead && slices.Contains(scopes, ScopeIssuesWrite) ||
			need == ScopeDocsRead && slices.Contains(scopes, ScopeDocsWrite) ||
			need == ScopeChecks && c.Request.Method == http.MethodGet && code
		if !listed || !has {
			respond.ErrorData(c, http.StatusForbidden, scopeRefused, gin.H{"code": "insufficient_scope"})
			c.Abort()
			return
		}
		// A key limited to services (K5) reaches only routes that name one
		// of them: never an environment's variables (shared with other
		// services), a preview or a project's routes.
		if services := middleware.APIKeyServicesFrom(c); len(services) > 0 && !slices.Contains(services, c.Param("serviceID")) {
			respond.ErrorData(c, http.StatusForbidden, servicesRefused, gin.H{"code": "insufficient_scope"})
			c.Abort()
			return
		}

		var owner string
		var err error
		if id := c.Param("serviceID"); id != "" {
			owner, err = projects.ServiceProject(c.Request.Context(), id)
		} else if id := c.Param("projectID"); id != "" {
			owner = id
		} else {
			owner, err = projects.EnvironmentProject(c.Request.Context(), c.Param("environmentID"))
		}
		if err != nil || owner == "" || owner != projectID {
			respond.ErrorData(c, http.StatusForbidden, scopeRefused, gin.H{"code": "insufficient_scope"})
			c.Abort()
			return
		}
		c.Next()
	}
}

// Agent connections' scopes (D-100, D-102). An agent's token reaches only
// these:
//   - read: every GET a code_read, issues_read, docs_read or deploy key may
//     call, and agentWorkspaceReads;
//   - deploy: deploying and rolling back a service, and starting a pull
//     request's preview;
//   - code: making a branch and opening a pull request (pushes go through
//     git with the branch's own credential);
//   - issues: opening issues and commenting on issues and pull requests;
//   - variables: setting one variable and setting a secret, of a service or
//     an environment (never reading a secret, replacing them all or
//     deleting one), and listing secrets' names;
//   - runbooks: running a page's step (in a container only; the use case
//     refuses a host step).
//
// Deny by default, as for keys. Never exposed (read_secret, change_roles,
// delete_org, remove_server) are outside every scope.
const (
	AgentScopeRead      = "read"
	AgentScopeDeploy    = "deploy"
	AgentScopeCode      = "code"
	AgentScopeIssues    = "issues"
	AgentScopeVariables = "variables"
	AgentScopeRunbooks  = "runbooks"
)

// agentWorkspaceReads have no project in their path: the workspace's
// projects, services and servers, server capacity, and search. The MCP tools narrow
// what they return to the connection's projects; search must name one of
// them (project_id) unless the connection reaches every project.
var agentWorkspaceReads = map[string]bool{
	"GET /api/v1/workspace/services":                 true,
	"GET /api/v1/projects":                           true,
	"GET /api/v1/servers":                            true,
	"GET /api/v1/servers/:serverID/capacity-summary": true,
	"GET /api/v1/workspace/search":                   true,
}

// agentActRoutes are the writes each act scope reaches; every one runs the
// agent policy in its use case (D-102).
var agentActRoutes = map[string]string{
	"POST /api/v1/services/:serviceID/deployments":                                           AgentScopeDeploy,
	"POST /api/v1/services/:serviceID/deployments/:deploymentID/rollback":                    AgentScopeDeploy,
	"POST /api/v1/projects/:projectID/repositories/:repositoryID/pulls/:pullNumber/preview":  AgentScopeDeploy,
	"POST /api/v1/projects/:projectID/repositories/:repositoryID/branches":                   AgentScopeCode,
	"POST /api/v1/projects/:projectID/repositories/:repositoryID/pulls":                      AgentScopeCode,
	"POST /api/v1/projects/:projectID/issues":                                                AgentScopeIssues,
	"POST /api/v1/projects/:projectID/issues/:issueNumber/comments":                          AgentScopeIssues,
	"POST /api/v1/projects/:projectID/repositories/:repositoryID/pulls/:pullNumber/comments": AgentScopeIssues,
	"POST /api/v1/services/:serviceID/env-vars":                                              AgentScopeVariables,
	"GET /api/v1/services/:serviceID/secrets":                                                AgentScopeVariables,
	"POST /api/v1/services/:serviceID/secrets":                                               AgentScopeVariables,
	"PUT /api/v1/services/:serviceID/secrets/:key":                                           AgentScopeVariables,
	"POST /api/v1/environments/:environmentID/env-vars":                                      AgentScopeVariables,
	"GET /api/v1/environments/:environmentID/secrets":                                        AgentScopeVariables,
	"POST /api/v1/environments/:environmentID/secrets":                                       AgentScopeVariables,
	"PUT /api/v1/environments/:environmentID/secrets/:key":                                   AgentScopeVariables,
	"POST /api/v1/projects/:projectID/docs/:pageID/steps/:step/run":                          AgentScopeRunbooks,
}

// AgentScopeFor returns the agent scope a route needs, ok false when no
// agent may call it.
func AgentScopeFor(method, path string) (scope string, ok bool) {
	route := method + " " + path
	if scope, ok := agentActRoutes[route]; ok {
		return scope, true
	}
	if agentWorkspaceReads[route] {
		return AgentScopeRead, true
	}
	if method != http.MethodGet {
		return "", false
	}
	switch routeScopes[route] {
	case ScopeCodeRead, ScopeIssuesRead, ScopeDocsRead, ScopeDeploy, ScopeChecks:
		return AgentScopeRead, true
	}
	return "", false
}

const agentRefused = "agents can't do this with this connection"

func enforceAgent(c *gin.Context, projects ProjectResolver, a middleware.AgentIdentity) {
	need, ok := AgentScopeFor(c.Request.Method, c.FullPath())
	if !ok || !slices.Contains(a.Scopes, need) {
		respond.ErrorData(c, http.StatusForbidden, agentRefused, gin.H{"code": "insufficient_scope"})
		c.Abort()
		return
	}
	var owner string
	var err error
	switch {
	case c.Param("serviceID") != "":
		owner, err = projects.ServiceProject(c.Request.Context(), c.Param("serviceID"))
	case c.Param("projectID") != "":
		owner = c.Param("projectID")
	case c.Param("environmentID") != "":
		owner, err = projects.EnvironmentProject(c.Request.Context(), c.Param("environmentID"))
	case c.FullPath() == "/api/v1/workspace/search" && !a.AllProjects:
		owner = c.Query("project_id")
	default:
		// A workspace read; the handlers keep it in the workspace.
		c.Next()
		return
	}
	// A workspace service ("" project) is reached only by a connection to
	// every project.
	if err != nil || (owner == "" && !a.AllProjects) || (owner != "" && !a.AllProjects && !slices.Contains(a.ProjectIDs, owner)) {
		respond.ErrorData(c, http.StatusForbidden, "this agent's connection doesn't reach that project", gin.H{"code": "insufficient_scope"})
		c.Abort()
		return
	}
	c.Next()
}
