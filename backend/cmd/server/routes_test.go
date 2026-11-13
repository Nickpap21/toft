package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/api"
	"github.com/Nickpap21/toft/backend/internal/agent"
	"github.com/Nickpap21/toft/backend/internal/alert"
	"github.com/Nickpap21/toft/backend/internal/apikey"
	"github.com/Nickpap21/toft/backend/internal/auth"
	"github.com/Nickpap21/toft/backend/internal/catalog"
	"github.com/Nickpap21/toft/backend/internal/code"
	"github.com/Nickpap21/toft/backend/internal/configfile"
	"github.com/Nickpap21/toft/backend/internal/dashboard"
	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/deploypolicy"
	"github.com/Nickpap21/toft/backend/internal/disk"
	"github.com/Nickpap21/toft/backend/internal/doc"
	"github.com/Nickpap21/toft/backend/internal/domain"
	"github.com/Nickpap21/toft/backend/internal/environment"
	"github.com/Nickpap21/toft/backend/internal/envvar"
	"github.com/Nickpap21/toft/backend/internal/events"
	"github.com/Nickpap21/toft/backend/internal/inbox"
	"github.com/Nickpap21/toft/backend/internal/instance"
	"github.com/Nickpap21/toft/backend/internal/issue"
	"github.com/Nickpap21/toft/backend/internal/mcp"
	"github.com/Nickpap21/toft/backend/internal/observability"
	"github.com/Nickpap21/toft/backend/internal/org"
	"github.com/Nickpap21/toft/backend/internal/project"
	"github.com/Nickpap21/toft/backend/internal/resources"
	"github.com/Nickpap21/toft/backend/internal/reveal"
	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/internal/sharedvolume"
	"github.com/Nickpap21/toft/backend/internal/user"
	"github.com/Nickpap21/toft/backend/internal/version"
	"github.com/Nickpap21/toft/backend/internal/webhook"
	"github.com/Nickpap21/toft/backend/pkg/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// specPath is api/openapi.yaml, the API's source of truth (D-18, D-59).
const specPath = "../../api/openapi.yaml"

// testRouter mounts every handler with no service behind it: registration
// never calls one, and the route table is all this test reads.
func testRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	return newRouter(&config.Config{}, zap.NewNop(), testHandlers(), routerDeps{})
}

// testHandlers is every handler, with no service behind it.
func testHandlers() handlers {
	log := zap.NewNop()
	return handlers{
		auth:          auth.NewHandler(nil, log),
		user:          user.NewHandler(nil, log),
		instance:      instance.NewHandler(nil, log),
		org:           org.NewHandler(nil, log),
		server:        server.NewHandler(nil, nil, log),
		project:       project.NewHandler(nil, nil, log),
		environment:   environment.NewHandler(nil, nil, log),
		service:       service.NewHandler(nil, nil, nil, nil, nil, log),
		domain:        domain.NewHandler(nil, nil, nil, log),
		envvar:        envvar.NewHandler(envvar.NewService(nil, log), nil, nil, nil, log),
		reveal:        reveal.NewHandler(nil, log),
		observability: observability.NewHandler(nil, nil, log),
		sharedVolume:  sharedvolume.NewHandler(nil, log),
		resources:     resources.NewHandler(nil, nil, log),
		version:       version.NewHandler(nil, log),
		events:        events.NewHandler(nil, log),
		deployment:    deployment.NewHandler(nil, nil, log),
		webhook:       webhook.NewHandler(nil, nil, nil, nil, nil, nil, nil, nil, log),
		apiKey:        apikey.NewHandler(nil, nil, log),
		dashboard:     dashboard.NewHandler(nil, log),
		catalog:       catalog.NewHandler(),
		configFile:    configfile.NewHandler(nil, nil, log),
		deployPolicy:  deploypolicy.NewHandler(nil, log),
		alert:         alert.NewHandler(nil, nil, log),
		code:          code.NewHandler(nil, nil, nil, log),
		gitHTTP:       code.NewGitHTTP(nil, nil, log),
		issue:         issue.NewHandler(nil, nil, log),
		doc:           doc.NewHandler(nil, nil, nil, log),
		inbox:         inbox.NewHandler(nil, log),
		agent:         agent.NewHandler(nil, func() string { return "" }, nil, log),
		disk:          disk.NewHandler(nil, log),
		mcp:           mcp.NewServer(mustSpec(), mcp.Router{}, nil, nil, "test", log),
	}
}

func mustSpec() *mcp.Spec {
	s, err := mcp.LoadSpec(api.Spec)
	if err != nil {
		panic(err)
	}
	return s
}

var ginParam = regexp.MustCompile(`:([A-Za-z0-9_]+)`)

// routedOperations returns "METHOD /path" for every route, with Gin's :param
// written as OpenAPI's {param}.
func routedOperations(r *gin.Engine) map[string]bool {
	out := map[string]bool{}
	for _, ri := range r.Routes() {
		out[ri.Method+" "+ginParam.ReplaceAllString(ri.Path, "{$1}")] = true
	}
	return out
}

// specOperations returns "METHOD /path" for every operation in the spec.
func specOperations(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(specPath)
	require.NoError(t, err)
	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	out := map[string]bool{}
	for path, item := range doc.Paths {
		for method := range item {
			switch method {
			case "get", "post", "put", "patch", "delete", "head", "options":
				out[strings.ToUpper(method)+" "+path] = true
			}
		}
	}
	return out
}

func sortedDiff(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Every route the router mounts is in api/openapi.yaml, and every operation
// in the spec is routed (D-18). A new endpoint is written into the spec
// first; a removed one is taken out of it.
func TestRoutesMatchOpenAPISpec(t *testing.T) {
	routed := routedOperations(testRouter())
	spec := specOperations(t)
	if missing := sortedDiff(routed, spec); len(missing) > 0 {
		t.Errorf("%d routed operations are missing from %s:\n  %s", len(missing), specPath, strings.Join(missing, "\n  "))
	}
	if extra := sortedDiff(spec, routed); len(extra) > 0 {
		t.Errorf("%d operations in %s are not routed:\n  %s", len(extra), specPath, strings.Join(extra, "\n  "))
	}
}
