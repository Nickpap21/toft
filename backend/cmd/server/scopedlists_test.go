package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/apikey"
	"github.com/Nickpap21/toft/backend/internal/project"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/pkg/config"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Projects A and B of testOrg, a service in A, two in B and one shared by
// the workspace.
const (
	listProjectA  = "00000000-0000-4000-8000-0000000000a1"
	listProjectB  = "00000000-0000-4000-8000-0000000000b1"
	listServiceA  = "00000000-0000-4000-8000-00000000a001"
	listServiceB1 = "00000000-0000-4000-8000-00000000b001"
	listServiceB2 = "00000000-0000-4000-8000-00000000b002"
	listServiceW  = "00000000-0000-4000-8000-00000000c001"
)

type twoProjects struct{ project.Repository }

func (twoProjects) List(context.Context, string) ([]project.Project, error) {
	return []project.Project{{ID: listProjectA, OrgID: testOrg}, {ID: listProjectB, OrgID: testOrg}}, nil
}

type twoProjectsServices struct{ service.Repository }

func (twoProjectsServices) ListWorkspaceServices(context.Context, string) ([]service.WorkspaceService, error) {
	return []service.WorkspaceService{
		{ID: listServiceA, ProjectID: listProjectA},
		{ID: listServiceB1, ProjectID: listProjectB},
		{ID: listServiceB2, ProjectID: listProjectB},
		{ID: listServiceW},
	}, nil
}

// listCallers answers the four callers' tokens; the directory, their
// owner's membership.
type listCallers struct {
	*directory
	keys   map[string]middleware.APIKeyIdentity
	agents map[string]middleware.AgentIdentity
}

func (l *listCallers) ValidateAPIKey(_ context.Context, raw string) (middleware.APIKeyIdentity, error) {
	if k, ok := l.keys[raw]; ok {
		return k, nil
	}
	return middleware.APIKeyIdentity{}, errors.New("unknown key")
}

func (l *listCallers) ValidateAgentToken(_ context.Context, raw string) (middleware.AgentIdentity, error) {
	if a, ok := l.agents[raw]; ok {
		return a, nil
	}
	return middleware.AgentIdentity{}, errors.New("unknown token")
}

// workspaceLists are what each caller sees of every list a scoped key or an
// agent connected to some projects reaches that names no project or
// service in its path: a key scoped to B, the same key limited to B1 (K5),
// an agent connected to B (D-100) and an agent connected to every project.
var workspaceLists = map[string]map[string][]string{
	"GET /api/v1/projects": {
		"key": {listProjectB}, "service key": {listProjectB},
		"agent": {listProjectB}, "every-project agent": {listProjectA, listProjectB},
	},
	"GET /api/v1/workspace/services": {
		"key": {listServiceB1, listServiceB2}, "service key": {listServiceB1},
		"agent": {listServiceB1, listServiceB2}, "every-project agent": {listServiceA, listServiceB1, listServiceB2, listServiceW},
	},
}

// notProjectLists are the other routes such callers reach with no project
// or service in their path, and why none needs narrowing to the caller's
// projects.
var notProjectLists = map[string]string{
	"GET /api/v1/servers":                            "the workspace's servers belong to no project",
	"GET /api/v1/servers/:serverID/capacity-summary": "one server's figures, not a list",
	"GET /api/v1/workspace/search":                   "an agent connected to some projects must name one of them (project_id)",
}

// Every workspace list a project key, a key limited to some services or an
// agent connected to some projects reaches keeps to their projects and
// services, and an agent connected to every project sees all of it. A new
// such route (keyLists, agentWorkspaceReads) fails here until it has a case.
func TestScopedCallersWorkspaceListsStayInTheirProjects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	log := zap.NewNop()
	dir := &directory{users: map[string]member{}, keys: map[string]string{}}
	owner := dir.add("owner", false)
	allScopes := []string{apikey.ScopeDeploy, apikey.ScopeVariables, apikey.ScopeCodeRead, apikey.ScopeCodeWrite, apikey.ScopeChecks,
		apikey.ScopeIssuesRead, apikey.ScopeIssuesWrite, apikey.ScopeDocsRead, apikey.ScopeDocsWrite, apikey.ScopeLogs}
	callers := &listCallers{directory: dir,
		keys: map[string]middleware.APIKeyIdentity{
			"paas_key":         {ID: "k1", UserID: owner, ProjectID: listProjectB, OrgID: testOrg, Scopes: allScopes},
			"paas_service_key": {ID: "k2", UserID: owner, ProjectID: listProjectB, OrgID: testOrg, Scopes: allScopes, ServiceIDs: []string{listServiceB1}},
		},
		agents: map[string]middleware.AgentIdentity{
			"toft_oat_agent":     {ConnectionID: "c1", UserID: owner, OrgID: testOrg, ProjectIDs: []string{listProjectB}, Scopes: []string{apikey.AgentScopeRead}},
			"toft_oat_all_agent": {ConnectionID: "c2", UserID: owner, OrgID: testOrg, AllProjects: true, Scopes: []string{apikey.AgentScopeRead}},
		},
	}
	tokens := map[string]string{"key": "paas_key", "service key": "paas_service_key", "agent": "toft_oat_agent", "every-project agent": "toft_oat_all_agent"}

	projects := project.NewService(twoProjects{})
	services := service.NewServiceManager(twoProjectsServices{}, log)
	h := testHandlers()
	h.project = project.NewHandler(projects, nil, log)
	h.service = service.NewHandler(services, projects, dir, nil, nil, log)
	r := newRouter(&config.Config{JWTSecret: accessSecret}, log, h, routerDeps{apiKeys: callers, orgs: callers, users: callers, services: services})

	get := func(path, token string) (int, []byte) {
		req := httptest.NewRequest(http.MethodGet, ginParam.ReplaceAllString(path, probeService), nil)
		req.RemoteAddr = dir.nextAddr()
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code, w.Body.Bytes()
	}

	for _, ri := range r.Routes() {
		if ri.Method != http.MethodGet || strings.Contains(ri.Path, ":serviceID") || strings.Contains(ri.Path, ":projectID") ||
			strings.Contains(ri.Path, ":environmentID") || selfAuthenticated["GET "+ginParam.ReplaceAllString(ri.Path, "{$1}")] {
			continue
		}
		route := ri.Method + " " + ri.Path
		anonymous, _ := get(ri.Path, "")
		key, _ := get(ri.Path, tokens["key"])
		_, agent := apikey.AgentScopeFor(ri.Method, ri.Path)
		keyReaches := anonymous == http.StatusUnauthorized && key != http.StatusUnauthorized && key != http.StatusForbidden
		if (keyReaches || agent) && workspaceLists[route] == nil && notProjectLists[route] == "" {
			t.Errorf("%s reaches a scoped key or an agent and names no project: give it a case in workspaceLists, or say in notProjectLists why it needs none", route)
		}
	}

	for route, want := range workspaceLists {
		for who, ids := range want {
			status, body := get(strings.TrimPrefix(route, "GET "), tokens[who])
			var out struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if status != http.StatusOK || json.Unmarshal(body, &out) != nil {
				t.Errorf("%s as the %s: %d %s", route, who, status, body)
				continue
			}
			got := make([]string, 0, len(out.Data))
			for _, row := range out.Data {
				got = append(got, row.ID)
			}
			slices.Sort(got)
			if !slices.Equal(got, ids) {
				t.Errorf("%s as the %s returns %v, want %v", route, who, got, ids)
			}
		}
	}
}
