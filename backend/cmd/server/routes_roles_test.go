package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/api"
	"github.com/Nickpap21/toft/backend/internal/agent"
	"github.com/Nickpap21/toft/backend/internal/apikey"
	"github.com/Nickpap21/toft/backend/internal/code"
	"github.com/Nickpap21/toft/backend/internal/configfile"
	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/deploypolicy"
	"github.com/Nickpap21/toft/backend/internal/disk"
	"github.com/Nickpap21/toft/backend/internal/doc"
	"github.com/Nickpap21/toft/backend/internal/domain"
	"github.com/Nickpap21/toft/backend/internal/environment"
	"github.com/Nickpap21/toft/backend/internal/envvar"
	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"github.com/Nickpap21/toft/backend/internal/instance"
	"github.com/Nickpap21/toft/backend/internal/issue"
	"github.com/Nickpap21/toft/backend/internal/observability"
	"github.com/Nickpap21/toft/backend/internal/org"
	"github.com/Nickpap21/toft/backend/internal/preview"
	"github.com/Nickpap21/toft/backend/internal/project"
	"github.com/Nickpap21/toft/backend/internal/reveal"
	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/pkg/config"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/reauth"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// access is what a route asks of its caller today: the least role that gets
// past the router and the handler's checks, whether an API key (or an agent)
// is refused, and whether a session must confirm its password first.
type access struct {
	// role: public, viewer, developer, owner, operator (an instance
	// operator), or self (git over HTTP and MCP check their own
	// credentials, outside the protected group).
	role     string
	session  bool // a key is refused (session_required)
	password bool // a session without a recent proof is refused (reauth_required)
	// serviceKey: a project key limited to the route's service, with the
	// deploy and variables scopes, gets through (K5).
	serviceKey bool
}

func (a access) String() string {
	s := fmt.Sprintf("{role: %q", a.role)
	if a.session {
		s += ", session: true"
	}
	if a.password {
		s += ", password: true"
	}
	if a.serviceKey {
		s += ", serviceKey: true"
	}
	return s + "}"
}

// selfAuthenticated are the routes outside the protected group that check
// their own credentials: git's smart HTTP an API key in Basic auth, MCP an
// agent's OAuth token, the agent's download a join token. The test only checks that they refuse a request
// without one.
var selfAuthenticated = map[string]bool{
	"GET /{workspace}/{repo}/info/refs":         true,
	"POST /{workspace}/{repo}/git-upload-pack":  true,
	"POST /{workspace}/{repo}/git-receive-pack": true,
	"GET /api/v1/agent/binary":                  true, // a join token
	"GET /mcp":                                  true,
	"POST /mcp":                                 true,
	"DELETE /mcp":                               true,
}

// Every route's access, observed: each route is called through the real
// router and AuthWithOrg as nobody, a viewer, a developer, an owner and an
// instance operator, with and without a password proof, and with an API key.
// A 401 or 403 is a refusal; a success, or a 500 from the nil repository
// behind the service, got past the checks. Role checks in the services
// count too: they run over a directory that answers who is an operator and
// what role each user has, and over fakes that give a check that depends on
// data the data that makes it strictest (a service on the control-plane
// server, an agent key pinned, the host as an exec's container).
//
// routeAccess records today's answers (security close-out plan, I2). A change
// to who may call a route fails here until the table is edited, on purpose,
// in the same change. Checks the fakes don't reach are their packages'
// tests: a runbook's host step (internal/doc), and a rollback's
// control-plane check, past the domain service (internal/deployment).
func TestRouteAccess(t *testing.T) {
	r, dir := accessRouter(t)
	var spec map[string]any
	require.NoError(t, yaml.Unmarshal(api.Spec, &spec))
	observed := map[string]access{}
	for _, ri := range r.Routes() {
		op := ri.Method + " " + ginParam.ReplaceAllString(ri.Path, "{$1}")
		observed[op] = observeAccess(t, r, dir, spec, ri.Method, ri.Path, op, selfAuthenticated[op])
	}
	// A join token for a server whose agent key isn't pinned is a first
	// join, not a rejoin.
	join := "POST /api/v1/servers/{serverID}/agent/join-token"
	observed[join+" (first join)"] = observeAccess(t, r, dir, spec, http.MethodPost,
		"/api/v1/servers/"+unpinnedServer+"/agent/join-token", join, false)

	var wrong []string
	for op, got := range observed {
		if want, ok := routeAccess[op]; !ok || want != got {
			wrong = append(wrong, fmt.Sprintf("\t%q: %s,", op, got))
		}
	}
	for op := range routeAccess {
		if _, ok := observed[op]; !ok {
			wrong = append(wrong, fmt.Sprintf("\t%q: (no longer routed: remove it)", op))
		}
	}
	if len(wrong) > 0 {
		sort.Strings(wrong)
		t.Errorf("%d routes' access differs from routeAccess. If the change is meant, edit the table "+
			"in the same change; these are the observed entries:\n%s", len(wrong), strings.Join(wrong, "\n"))
	}
}

// foreign makes the ownership fakes answer that nothing is the caller's.
var foreign bool

// Another workspace's resource answers as an unknown one: 404, never a 403
// that says it exists (security close-out I9). The caller is an operator
// who owns the workspace, with the password, so no role check refuses
// first. Routes whose packages keep their own fakes (git, issues, docs,
// agents) are checked in those packages.
func TestForeignResourcesAreNotFound(t *testing.T) {
	r, dir := accessRouter(t)
	var spec map[string]any
	require.NoError(t, yaml.Unmarshal(api.Spec, &spec))
	foreign = true
	t.Cleanup(func() { foreign = false })
	for _, ri := range r.Routes() {
		op := ri.Method + " " + ginParam.ReplaceAllString(ri.Path, "{$1}")
		if !strings.Contains(ri.Path, ":") || selfAuthenticated[op] {
			continue
		}
		input, ok := probeInputs[op]
		if !ok {
			input = specInput(spec, op)
		}
		if status, code := probe(t, r, dir, ri.Method, ri.Path, input, caller{role: "owner", operator: true, password: true}); status == http.StatusForbidden {
			t.Errorf("%s answers another workspace's resource with 403 %s", op, code)
		}
	}
}

// caller is who a probe acts as.
type caller struct {
	anonymous  bool
	key        bool
	serviceKey bool
	role       string
	operator   bool
	password   bool
}

var roleOrder = []string{"viewer", "developer", "owner", "operator"}

func observeAccess(t *testing.T, r *gin.Engine, dir *directory, spec map[string]any, method, path, op string, self bool) access {
	t.Helper()
	input, ok := probeInputs[op]
	if !ok {
		input = specInput(spec, op)
	}
	call := func(c caller) (bool, int, string) {
		status, code := probe(t, r, dir, method, path, input, c)
		return status != http.StatusUnauthorized && status != http.StatusForbidden, status, code
	}
	if passed, _, _ := call(caller{anonymous: true}); passed {
		if self {
			t.Errorf("%s answers a request without credentials", op)
		}
		return access{role: "public"}
	}
	if self {
		return access{role: "self"}
	}

	least := ""
	for _, role := range roleOrder {
		c := caller{role: role, password: true}
		if role == "operator" {
			c = caller{role: "owner", operator: true, password: true}
		}
		passed, status, _ := call(c)
		switch {
		case passed && least == "":
			least = role
			// Anything but a success, the panic of a service the test
			// left out, or the 502 of a server it has no SSH to (a 400,
			// a 404, a 409, a 503) may come before a check that would
			// refuse: the probe must get past it.
			if (status < 200 || status > 299) && status != http.StatusInternalServerError &&
				status != http.StatusBadGateway && !handshakes[op] {
				t.Errorf("%s first lets a %s through with a %d: give it the input or the fakes that get past it", op, role, status)
			}
		case !passed && least != "":
			t.Errorf("%s lets a %s through but refuses a %s", op, least, role)
		}
	}
	if least == "" {
		return access{role: "nobody"}
	}

	a := access{role: least}
	c := caller{role: least}
	if least == "operator" {
		c = caller{role: "owner", operator: true}
	}
	if passed, _, code := call(c); !passed {
		if code != "reauth_required" {
			t.Errorf("%s refuses a %s without a password proof as %q, not reauth_required", op, least, code)
		}
		a.password = true
	}
	// The key acts for an operator who owns the workspace: a refusal is
	// for being a key, and says so.
	if passed, _, code := call(caller{key: true}); !passed {
		if code != "session_required" {
			t.Errorf("%s refuses an operator's key as %q, not session_required", op, code)
		}
		a.session = true
	}
	if passed, _, _ := call(caller{serviceKey: true}); passed {
		a.serviceKey = true
	}
	return a
}

const accessSecret = "route-access-test-secret-route-access-test"

// testOrg is the workspace every probe works in.
var testOrg = uuid.NewString()

// paramValues are path parameters that parse; any other is a UUID.
var paramValues = map[string]string{
	"sha":         strings.Repeat("a", 40),
	"pullNumber":  "1",
	"issueNumber": "1",
	"cycleNumber": "1",
	"step":        "1",
	"version":     "1",
	"key":         "FOO",
	"mount":       "data",
	"slug":        "postgres",
	"container":   "paas-backend",
	"workspace":   "acme",
	"repo":        "app.git",
}

// probeInputs are the bodies (and queries) of routes whose input the spec's
// required fields don't make valid; every other route gets specInput's.
var probeInputs = map[string]string{
	"GET /api/v1/oauth/authorize/request":                                                    "?client_id=probe&redirect_uri=https://example.com/callback",
	"PATCH /api/v1/instance/setup":                                                           `{"finished": true}`,
	"PATCH /api/v1/shared-volumes/{volumeID}":                                                `{"size_limit_bytes": 1073741824}`,
	"PATCH /api/v1/projects/{projectID}/repositories/{repositoryID}":                         `{"default_branch": "main"}`, // moving it moves protection (L2)
	"POST /api/v1/instance/operators":                                                        `{"email": "probe@example.com", "password": "probe-password"}`,
	"POST /api/v1/org/members":                                                               `{"email": "probe@example.com", "role": "viewer"}`,
	"POST /api/v1/projects/{projectID}/repositories/{repositoryID}/pulls/{pullNumber}/merge": `{"method": "merge", "expected_head_sha": "` + strings.Repeat("a", 40) + `"}`,
	"POST /api/v1/services/{serviceID}/deploy-policy/exception":                              `{"version": "v", "allowed_rules": ["privileged"], "reason": "probe", "values_digest": "probe"}`,
	"POST /api/v1/services/{serviceID}/domains":                                              `{"hostname": "app.example.com"}`,
	"POST /api/v1/services/{serviceID}/health-check/test":                                    `{"path": "/health", "port": 8080}`,
	"POST /api/v1/shared-volumes":                                                            `{"name": "data", "size_limit_bytes": 1073741824}`,
	"PUT /api/v1/instance/backup":                                                            backupInput,
	"PUT /api/v1/me/password":                                                                `{"current_password": "probe-password", "new_password": "probe-password-2"}`,
	"PUT /api/v1/servers/{serverID}/backup":                                                  backupInput,
	"PUT /api/v1/servers/{serverID}/dns-provider":                                            `{"provider": "cloudflare", "credentials": {"CF_DNS_API_TOKEN": "probe"}}`,
	"PUT /api/v1/servers/{serverID}/host-key":                                                `{"fingerprint": "SHA256:` + strings.Repeat("a", 43) + `"}`,
	"PUT /api/v1/services/{serviceID}/config-files":                                          `{"path": "/etc/app/config.yml", "content": "probe"}`,
	"PUT /api/v1/services/{serviceID}/config":                                                `{"isolation": "standard"}`,                 // below the default: owners'
	"POST /api/v1/services/{serviceID}/exec":                                                 `{"command": "ls", "container": "@server"}`, // the host: operators'
	"PUT /api/v1/servers/{serverID}/shares/{orgID}":                                          `{"cpu_cores": 1, "memory_mb": 512}`,
}

const backupInput = `{"endpoint": "https://s3.example.com", "bucket": "backups", "access_key": "probe", "secret_key": "probe", "schedule": "03:00"}`

// handshakes answer 400 after every check: the probe isn't a WebSocket.
var handshakes = map[string]bool{
	"GET /api/v1/instance/panel/pty":     true,
	"GET /api/v1/servers/{serverID}/pty": true,
}

// specInput builds, from api/openapi.yaml, a body with every required
// field and a query with every required parameter, so a route that
// validates its input before its checks still reaches them.
func specInput(spec map[string]any, op string) string {
	method, path, _ := strings.Cut(op, " ")
	o, _ := dig(spec, "paths", path, strings.ToLower(method)).(map[string]any)
	if o == nil {
		return ""
	}
	var query []string
	for _, p := range asSlice(o["parameters"]) {
		p := resolve(spec, p)
		if p["in"] == "query" && p["required"] == true {
			v, _ := json.Marshal(example(spec, resolve(spec, p["schema"]), fmt.Sprint(p["name"])))
			query = append(query, fmt.Sprintf("%s=%s", p["name"], strings.Trim(string(v), `"`)))
		}
	}
	if len(query) > 0 {
		return "?" + strings.Join(query, "&")
	}
	schema := resolve(spec, dig(resolve(spec, o["requestBody"]), "content", "application/json", "schema"))
	if schema == nil {
		return ""
	}
	b, _ := json.Marshal(example(spec, schema, ""))
	return string(b)
}

// example is a value schema accepts, as far as the spec says: its first
// enum value, a date for a pattern (they are all dates), one item of a
// list, and every required property of an object. name guesses the rest.
func example(spec map[string]any, s map[string]any, name string) any {
	for _, k := range []string{"allOf", "oneOf", "anyOf"} {
		if list := asSlice(s[k]); len(list) > 0 {
			merged := map[string]any{}
			for _, sub := range list {
				for k, v := range resolve(spec, sub) {
					merged[k] = v
				}
				if k != "allOf" {
					break
				}
			}
			return example(spec, merged, name)
		}
	}
	if e := asSlice(s["enum"]); len(e) > 0 {
		return e[0]
	}
	switch s["type"] {
	case "object", nil:
		out := map[string]any{}
		props, _ := s["properties"].(map[string]any)
		for _, r := range asSlice(s["required"]) {
			k := fmt.Sprint(r)
			out[k] = example(spec, resolve(spec, props[k]), k)
		}
		return out
	case "array":
		return []any{example(spec, resolve(spec, s["items"]), name)}
	case "integer", "number":
		if m, ok := s["minimum"]; ok {
			return m
		}
		return 1
	case "boolean":
		return true
	}
	switch {
	case s["pattern"] != nil:
		return "2026-01-01"
	case s["format"] == "date-time":
		return "2026-01-01T00:00:00Z"
	case name == "id" || strings.HasSuffix(name, "_id"):
		return "00000000-0000-4000-8000-000000000001"
	case strings.Contains(name, "email"):
		return "probe@example.com"
	case strings.Contains(name, "url") || s["format"] == "uri":
		return "https://example.com"
	case strings.Contains(name, "domain"):
		return "example.com"
	}
	return "probe"
}

func resolve(spec map[string]any, v any) map[string]any {
	m, _ := v.(map[string]any)
	if ref, ok := m["$ref"].(string); ok {
		var parts []string
		for _, p := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			parts = append(parts, p)
		}
		return resolve(spec, dig(spec, parts...))
	}
	return m
}

func dig(v any, keys ...string) any {
	for _, k := range keys {
		m, _ := v.(map[string]any)
		v = m[k]
	}
	return v
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// probe calls the route once as c, as a new user from a new address, so no
// rate limiter carries over between probes, and returns the status and the
// refusal's code. input is the body; one starting with ? is the query.
func probe(t *testing.T, r *gin.Engine, dir *directory, method, path, input string, c caller) (int, string) {
	t.Helper()
	url := ginParam.ReplaceAllStringFunc(path, func(p string) string {
		name := p[1:]
		if name == "token" {
			return randomHex(16) // the reset link's limiter keys on it
		}
		if v, ok := paramValues[name]; ok {
			return v
		}
		return "00000000-0000-4000-8000-000000000001"
	})
	body := ""
	switch {
	case strings.HasPrefix(input, "?"):
		url += input
	case input != "":
		body = input
	case method != http.MethodGet:
		body = "{}"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req := httptest.NewRequest(method, url, strings.NewReader(body)).WithContext(ctx)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if service, ok := strings.CutPrefix(path[strings.LastIndex(path, "/")+1:], "git-"); ok {
		req.Header.Set("Content-Type", "application/x-git-"+service+"-request")
	}
	req.RemoteAddr = dir.nextAddr()

	switch {
	case c.anonymous:
	case c.key, c.serviceKey:
		raw := "paas_" + randomHex(16)
		dir.addKey(raw, dir.add("owner", true))
		if c.serviceKey {
			dir.limitKey(raw)
		}
		req.Header.Set("Authorization", "Bearer "+raw)
	default:
		userID := dir.add(c.role, c.operator)
		now := time.Now()
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
			"sub": userID, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
		}).SignedString([]byte(accessSecret))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if c.password {
			proof, _, err := reauth.Issue([]byte(accessSecret), userID, token, now, reauth.TTL)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set(reauth.Header, proof)
		}
	}

	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.ServeHTTP(w, req)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s %s did not answer", method, url)
	}
	if w.Code == http.StatusTooManyRequests {
		t.Fatalf("%s %s was rate limited: the probe would read the limiter, not the route", method, url)
	}
	var e struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &e)
	return w.Code, e.Code
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// accessRouter is testRouter with the auth middleware's lookups, and the
// services that check roles themselves, answered by one directory.
func accessRouter(t *testing.T) (*gin.Engine, *directory) {
	gin.SetMode(gin.TestMode)
	log := zap.NewNop()
	dir := &directory{users: map[string]member{}, keys: map[string]string{}}
	// An installer's panel, so the upgrade routes go past "not installed
	// by the installer".
	upgrades := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(upgrades, "state.json"), []byte(`{"version": "1.0.0"}`), 0o600))
	servers := server.NewService(ownedServers{}, "")
	// Agent keys pinned (a join token is a rejoin), sharing turned on, and
	// the caller holding the same role in the workspace shared with.
	servers.WithAgents(pinnedAgents{}, nil, nil, "")
	servers.WithAppNetworks(true, "")
	servers.WithOrgRoles(sameRoleEverywhere{dir})
	envs := environment.NewService(ownedEnvironments{})
	projects := project.NewService(ownedProjects{})
	services := service.NewServiceManager(ownedServices{}, log)
	servers.WithOperatorChecker(dir)
	services.WithOperatorChecker(dir)

	h := testHandlers()
	h.server = server.NewHandler(servers, nil, log)
	h.project = project.NewHandler(projects, nil, log)
	h.environment = environment.NewHandler(envs, dir, log)
	h.service = service.NewHandler(services, projects, dir, nil, nil, log)
	h.domain = domain.NewHandler(nil, nil, dir, log)
	h.envvar = envvar.NewHandler(envvar.NewService(nil, log), dir, dir, nil, log)
	reveals := reveal.NewService(keptReveal{}, dir, noAudit{}, log)
	reveals.WithLogins(keptReveal{})
	h.reveal = reveal.NewHandler(reveals, log)
	h.observability = observability.NewHandler(nil, dir, log)
	// The service runs on the control-plane server under the stopgap
	// (D-19): deploys are operators'.
	deployments := deployment.NewService(nil, nil, nil, log)
	deployments.WithControlPlaneStopgap(true, dir)
	deployments.WithDeployUseCase(ownedServiceOps{}, servers, nil, nil, nil, envs, nil)
	h.deployment = deployment.NewHandler(deployments, ownedServiceOps{}, log)
	h.org = org.NewHandler(org.NewService(nil, log).WithOperatorChecker(dir), log)
	h.apiKey = apikey.NewHandler(nil, dir, log)
	h.configFile = configfile.NewHandler(nil, dir, log)
	h.code = code.NewHandler(nil, dir, nil, log)
	h.issue = issue.NewHandler(nil, dir, log)
	h.doc = doc.NewHandler(nil, dir, nil, log)
	h.preview = preview.NewHandler(nil, dir, dir, log)
	// git over HTTP turned on, so its own sign-in answers.
	h.gitHTTP = code.NewGitHTTP(code.NewService(nil, &gitstore.Client{}, nil, func() string { return "" }, 0, log), dir, log)
	h.instance = instance.NewHandler(instance.NewService(instanceOperators{d: dir}, log).WithPanelMonitor(nil, nil, "", "").
		WithPanelShell(true).WithUpgrades(upgrades), log)
	h.deployPolicy = deploypolicy.NewHandler(deploypolicy.NewService(nil, deploypolicy.ModeEnforce, log).
		WithOperatorChecker(dir).WithOwnershipChecker(dir), log)
	h.agent = agent.NewHandler(agent.NewService(agentRows{}, dir, dir, nil, log), func() string { return "" }, nil, log)
	h.disk = disk.NewHandler(disk.NewService(diskRows{}, diskNoHosts{}, nil, diskNoAudit{}, diskProbePeople{dir}, nil, log), log)
	r := newRouter(&config.Config{JWTSecret: accessSecret}, log, h, routerDeps{apiKeys: dir, orgs: dir, users: dir,
		services: services, environments: envs})
	return r, dir
}

type member struct {
	role     string
	operator bool
}

// directory is every user, key and operator the probes make, all members of
// testOrg, owning everything in it.
type directory struct {
	mu    sync.Mutex
	users map[string]member
	keys  map[string]string // raw key → user
	// limited are keys limited to probeService, in probeProject (K5).
	limited map[string]bool
	addrs   int
}

func (d *directory) add(role string, operator bool) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	id := uuid.NewString()
	d.users[id] = member{role: role, operator: operator}
	return id
}

func (d *directory) addKey(raw, userID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.keys[raw] = userID
}

func (d *directory) limitKey(raw string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.limited == nil {
		d.limited = map[string]bool{}
	}
	d.limited[raw] = true
}

func (d *directory) nextAddr() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.addrs++
	return fmt.Sprintf("10.%d.%d.%d:40000", d.addrs>>16&255, d.addrs>>8&255, d.addrs&255)
}

func (d *directory) member(userID string) (member, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	m, ok := d.users[userID]
	return m, ok
}

func (d *directory) ValidateAPIKey(_ context.Context, raw string) (middleware.APIKeyIdentity, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	userID, ok := d.keys[raw]
	if !ok {
		return middleware.APIKeyIdentity{}, fmt.Errorf("unknown key")
	}
	if d.limited[raw] {
		return middleware.APIKeyIdentity{ID: uuid.NewString(), UserID: userID, ProjectID: probeProject, OrgID: testOrg,
			Scopes: []string{"deploy", "variables"}, ServiceIDs: []string{probeService}}, nil
	}
	return middleware.APIKeyIdentity{ID: uuid.NewString(), UserID: userID}, nil
}

// probeService is the ID every probe's path names; probeProject is its
// project.
const (
	probeService = "00000000-0000-4000-8000-000000000001"
	probeProject = "00000000-0000-4000-8000-0000000000aa"
)

func (d *directory) GetForUser(_ context.Context, userID string) (string, string, error) {
	m, ok := d.member(userID)
	if !ok {
		return "", "", fmt.Errorf("no workspace")
	}
	return testOrg, m.role, nil
}

func (d *directory) RoleIn(_ context.Context, userID, orgID string) (string, error) {
	m, ok := d.member(userID)
	if !ok || orgID != testOrg {
		return "", fmt.Errorf("not a member")
	}
	return m.role, nil
}

func (d *directory) IsActive(_ context.Context, userID string) (bool, error) {
	_, ok := d.member(userID)
	return ok, nil
}

func (d *directory) RequestIdentity(_ context.Context, userID, orgID, _ string) (middleware.Identity, error) {
	m, ok := d.member(userID)
	if !ok {
		return middleware.Identity{}, nil
	}
	if orgID != "" && orgID != testOrg {
		return middleware.Identity{Active: true}, nil
	}
	return middleware.Identity{Active: true, OrgID: testOrg, Role: m.role}, nil
}

func (d *directory) IsOperator(_ context.Context, userID string) (bool, error) {
	m, _ := d.member(userID)
	return m.operator, nil
}

func (d *directory) WriteAudit(context.Context, string, string, map[string]any) {}

// keptReveal holds an admin password for every caller and service (D-160).
type keptReveal struct{}

func (keptReveal) Keep(context.Context, string, string, string, string, string) error { return nil }
func (keptReveal) Get(context.Context, string, string, time.Time) (*reveal.Reveal, error) {
	return &reveal.Reveal{Key: "GF_SECURITY_ADMIN_PASSWORD", Reason: "created", CreatedAt: time.Now(), Password: "pw"}, nil
}
func (keptReveal) Delete(context.Context, string, string) (bool, error)   { return true, nil }
func (keptReveal) DeleteBefore(context.Context, time.Time) (int64, error) { return 0, nil }
func (keptReveal) Forget(context.Context, string, string) error           { return nil }
func (keptReveal) AdminLogin(context.Context, string) (service.AdminLogin, error) {
	return service.AdminLogin{User: "admin", Reset: "app"}, nil
}
func (keptReveal) ResetAdminPassword(context.Context, string, string) (*service.AdminPasswordReset, error) {
	return &service.AdminPasswordReset{Key: "GF_SECURITY_ADMIN_PASSWORD", User: "admin", Password: "pw"}, nil
}

type noAudit struct{}

func (noAudit) RecordAuditLog(context.Context, string, string, string, string) error { return nil }

func (d *directory) CheckOwnership(context.Context, string, string) (bool, error) {
	return !foreign, nil
}

func (d *directory) OwnEnvironment(context.Context, string, string) (bool, error) {
	return !foreign, nil
}

func (d *directory) GetName(context.Context, string) (string, error) { return "production", nil }

// owned* are repositories (or a service's narrower view of one) that say
// everything is in the caller's workspace; anything past that check panics
// (the embedded nil interface), which the probe reads as getting through.
// With foreign set they, and the directory, say nothing is, and no server
// is shared (TestForeignResourcesAreNotFound).
type ownedServers struct{ server.Repository }
type ownedEnvironments struct{ environment.Repository }
type ownedProjects struct{ project.Repository }
type ownedServices struct{ service.Repository }
type ownedServiceOps struct{ deployment.ServiceManagerOps }

func (ownedServers) CheckOwnership(context.Context, string, string) (bool, error) {
	return !foreign, nil
}
func (ownedServers) CanUse(context.Context, string, string) (bool, error) { return false, nil }
func (ownedServers) GetByID(_ context.Context, id string) (*server.Server, error) {
	// On the agent, with its key pinned; port -1 so a host key scan fails
	// before it dials anything.
	return &server.Server{ID: id, OrgID: testOrg, Connection: server.ConnectionAgent, AgentKey: "pinned", Port: -1}, nil
}
func (ownedEnvironments) CheckOwnership(context.Context, string, string) (bool, error) {
	return !foreign, nil
}
func (ownedProjects) CheckOwnership(context.Context, string, string) (bool, error) {
	return !foreign, nil
}
func (ownedServices) CheckOwnership(context.Context, string, string) (bool, error) {
	return !foreign, nil
}

// A service on the control-plane server, in a workspace whose default
// isolation is the strongest, so any other level is the owners'.
func (ownedServices) GetByID(_ context.Context, id string) (*service.Service, error) {
	return &service.Service{ID: id, ProjectID: probeProject}, nil
}
func (ownedServices) PidsLimit(context.Context, string) (int, error)          { return 0, nil }
func (ownedServices) DeployTimeout(context.Context, string) (int, error)      { return 0, nil }
func (ownedServices) RollbackImagesKept(context.Context, string) (int, error) { return 0, nil }
func (ownedServices) ContainerHealth(context.Context, string) (string, string, error) {
	return service.ContainerHealthImage, "", nil
}
func (ownedServices) IsPreviewCopy(context.Context, string) (bool, error) { return false, nil }
func (ownedServices) Isolation(context.Context, string) (string, string, error) {
	return "", service.IsolationSandboxedStrict, nil
}
func (ownedServiceOps) Get(_ context.Context, id string) (*service.Service, error) {
	return &service.Service{ID: id}, nil
}
func (ownedServiceOps) CheckDependencies(context.Context, *service.Service) error { return nil }
func (ownedServiceOps) CheckOwnership(context.Context, string, string) (bool, error) {
	return !foreign, nil
}

// pinnedAgents says every server but unpinnedServer has its agent key
// pinned and is frozen (D-113), and knows no join token.
type pinnedAgents struct{ server.AgentStore }

func (pinnedAgents) GetAgent(_ context.Context, serverID string) (*server.AgentRecord, error) {
	if serverID == unpinnedServer {
		return &server.AgentRecord{Connection: server.ConnectionAgent}, nil
	}
	frozen := time.Now()
	return &server.AgentRecord{Connection: server.ConnectionAgent, Key: "pinned", FrozenAt: &frozen}, nil
}

// unpinnedServer is the one server whose agent hasn't joined yet.
const unpinnedServer = "00000000-0000-4000-8000-000000000002"

func (pinnedAgents) PeekJoinToken(context.Context, []byte) (string, error) {
	return "", server.ErrUnknownToken
}

// sameRoleEverywhere gives each user their testOrg role in every workspace.
type sameRoleEverywhere struct{ d *directory }

func (r sameRoleEverywhere) RoleIn(ctx context.Context, userID, _ string) (string, error) {
	return r.d.RoleIn(ctx, userID, testOrg)
}

// instanceOperators answers the instance service's operator check; anything
// past it panics (nil Repository), which the probe reads as getting through.
type instanceOperators struct {
	instance.Repository
	d *directory
}

func (r instanceOperators) IsOperator(ctx context.Context, userID string) (bool, error) {
	return r.d.IsOperator(ctx, userID)
}

// routeAccess: see TestRouteAccess.
var routeAccess = map[string]access{
	"DELETE /api/v1/agents/connections/{connectionID}":                                                        {role: "owner", session: true},
	"DELETE /api/v1/agents/pause":                                                                             {role: "owner", session: true, password: true},
	"DELETE /api/v1/agents/policy/projects/{projectID}":                                                       {role: "owner", session: true},
	"DELETE /api/v1/alerts/channel":                                                                           {role: "owner"},
	"DELETE /api/v1/environments/{environmentID}":                                                             {role: "owner", session: true, password: true},
	"DELETE /api/v1/environments/{environmentID}/env-vars/{key}":                                              {role: "developer"},
	"DELETE /api/v1/environments/{environmentID}/secrets/{key}":                                               {role: "developer"},
	"DELETE /api/v1/instance/agents/pause":                                                                    {role: "operator", session: true, password: true},
	"DELETE /api/v1/instance/backup":                                                                          {role: "operator"},
	"DELETE /api/v1/instance/operators/{userID}":                                                              {role: "operator", session: true},
	"DELETE /api/v1/instance/users/{userID}":                                                                  {role: "operator", session: true},
	"DELETE /api/v1/org":                                                                                      {role: "owner"},
	"DELETE /api/v1/org/invites/{inviteID}":                                                                   {role: "owner"},
	"DELETE /api/v1/org/members/{userID}":                                                                     {role: "owner"},
	"DELETE /api/v1/projects/{projectID}":                                                                     {role: "owner", session: true, password: true},
	"DELETE /api/v1/projects/{projectID}/docs/{pageID}":                                                       {role: "developer"},
	"DELETE /api/v1/projects/{projectID}/issue-views/{viewID}":                                                {role: "developer"},
	"DELETE /api/v1/projects/{projectID}/issues/{issueNumber}/comments/{commentID}":                           {role: "developer"},
	"DELETE /api/v1/projects/{projectID}/issues/{issueNumber}/relations/{relationID}":                         {role: "developer"},
	"DELETE /api/v1/projects/{projectID}/repositories/{repositoryID}":                                         {role: "owner", session: true, password: true},
	"DELETE /api/v1/projects/{projectID}/repositories/{repositoryID}/mirrors/{mirrorID}":                      {role: "developer"},
	"DELETE /api/v1/projects/{projectID}/repositories/{repositoryID}/pulls/{pullNumber}/comments/{commentID}": {role: "developer"},
	"DELETE /api/v1/projects/{projectID}/repositories/{repositoryID}/webhooks/{webhookID}":                    {role: "owner"},
	"DELETE /api/v1/servers/{serverID}":                                                                       {role: "developer"},
	"PATCH /api/v1/servers/{serverID}":                                                                        {role: "owner"},
	"POST /api/v1/servers/{serverID}/ssh-key":                                                                 {role: "owner"},
	"PUT /api/v1/servers/{serverID}/ssh":                                                                      {role: "owner"},
	"DELETE /api/v1/servers/{serverID}/agent":                                                                 {role: "owner", session: true},
	"DELETE /api/v1/servers/{serverID}/backup":                                                                {role: "owner"},
	"DELETE /api/v1/servers/{serverID}/dns-provider":                                                          {role: "owner"},
	"DELETE /api/v1/servers/{serverID}/shares/{orgID}":                                                        {role: "owner"},
	"DELETE /api/v1/services/{serviceID}":                                                                     {role: "developer"},
	"DELETE /api/v1/services/{serviceID}/config-files/{fileID}":                                               {role: "developer"},
	"DELETE /api/v1/services/{serviceID}/deploy-policy/exception":                                             {role: "operator", session: true, password: true},
	"DELETE /api/v1/services/{serviceID}/domains/{id}":                                                        {role: "developer"},
	"DELETE /api/v1/services/{serviceID}/env-vars/{key}":                                                      {role: "developer", serviceKey: true},
	"DELETE /api/v1/services/{serviceID}/admin-password":                                                      {role: "viewer", session: true},
	"DELETE /api/v1/services/{serviceID}/secrets/{key}":                                                       {role: "developer", serviceKey: true},
	"DELETE /api/v1/services/{serviceID}/volumes/{mount}":                                                     {role: "developer"},
	"DELETE /api/v1/shared-volumes/{volumeID}":                                                                {role: "owner"},
	"DELETE /api/v1/user/api-keys/{keyID}":                                                                    {role: "viewer"},
	"DELETE /api/v1/user/ssh-keys/{keyID}":                                                                    {role: "viewer"},
	"DELETE /api/v1/workspace/issue-types/{typeID}":                                                           {role: "owner"},
	"DELETE /mcp": {role: "self"},
	"GET /.well-known/oauth-authorization-server":                                                  {role: "public"},
	"GET /.well-known/oauth-protected-resource":                                                    {role: "public"},
	"GET /.well-known/oauth-protected-resource/mcp":                                                {role: "public"},
	"GET /api/v1/agent/binary":                                                                     {role: "self"},
	"GET /api/v1/agent/connect":                                                                    {role: "public"},
	"GET /api/v1/agent/install.sh":                                                                 {role: "public"},
	"GET /api/v1/agents/approvals":                                                                 {role: "viewer"},
	"GET /api/v1/agents/approvals/{approvalID}":                                                    {role: "viewer"},
	"GET /api/v1/agents/connections":                                                               {role: "viewer"},
	"GET /api/v1/agents/pause":                                                                     {role: "viewer"},
	"GET /api/v1/agents/policy":                                                                    {role: "viewer"},
	"GET /api/v1/alerts":                                                                           {role: "viewer"},
	"GET /api/v1/alerts/channel":                                                                   {role: "owner"},
	"GET /api/v1/catalog":                                                                          {role: "viewer"},
	"GET /api/v1/catalog/{slug}":                                                                   {role: "viewer"},
	"GET /api/v1/dashboard":                                                                        {role: "viewer"},
	"GET /api/v1/deploy-policy/violations":                                                         {role: "operator"},
	"GET /api/v1/docs":                                                                             {role: "public"},
	"GET /api/v1/domains":                                                                          {role: "viewer"},
	"GET /api/v1/environments/{environmentID}":                                                     {role: "viewer"},
	"GET /api/v1/environments/{environmentID}/data":                                                {role: "developer"},
	"GET /api/v1/environments/{environmentID}/address":                                             {role: "developer"},
	"GET /api/v1/services/address":                                                                 {role: "developer"},
	"GET /api/v1/environments/{environmentID}/env-vars":                                            {role: "viewer"},
	"GET /api/v1/environments/{environmentID}/overview":                                            {role: "viewer"},
	"GET /api/v1/environments/{environmentID}/secrets":                                             {role: "viewer"},
	"GET /api/v1/environments/{environmentID}/services":                                            {role: "viewer"},
	"GET /api/v1/events":                                                                           {role: "viewer"},
	"GET /api/v1/git":                                                                              {role: "viewer"},
	"GET /api/v1/health":                                                                           {role: "public"},
	"GET /api/v1/inbox/counts":                                                                     {role: "viewer"},
	"GET /api/v1/inbox/needs-you":                                                                  {role: "viewer"},
	"GET /api/v1/inbox/updates":                                                                    {role: "viewer"},
	"GET /api/v1/instance/audit-log":                                                               {role: "operator"},
	"GET /api/v1/instance/backup":                                                                  {role: "operator"},
	"GET /api/v1/instance/operators":                                                               {role: "operator"},
	"GET /api/v1/instance/panel":                                                                   {role: "operator"},
	"GET /api/v1/instance/panel-host":                                                              {role: "viewer"},
	"GET /api/v1/instance/panel/capabilities":                                                      {role: "operator"},
	"GET /api/v1/instance/panel/updates":                                                           {role: "operator"},
	"GET /api/v1/instance/panel/updates/run":                                                       {role: "operator"},
	"GET /api/v1/instance/panel/disk":                                                              {role: "operator"},
	"GET /api/v1/instance/panel/disk/clean":                                                        {role: "operator"},
	"GET /api/v1/instance/panel/disk/policy":                                                       {role: "operator"},
	"GET /api/v1/instance/panel/containers/{container}/logs":                                       {role: "operator"},
	"GET /api/v1/instance/panel/gvisor":                                                            {role: "operator"},
	"GET /api/v1/instance/panel/pty":                                                               {role: "operator", session: true, password: true},
	"GET /api/v1/instance/panel/server":                                                            {role: "operator"},
	"GET /api/v1/instance/probe":                                                                   {role: "public"},
	"GET /api/v1/instance/recovery-kit":                                                            {role: "operator", session: true, password: true},
	"GET /api/v1/instance/registration":                                                            {role: "public"},
	"GET /api/v1/instance/setup":                                                                   {role: "operator"},
	"GET /api/v1/instance/upgrade":                                                                 {role: "operator"},
	"GET /api/v1/instance/users":                                                                   {role: "operator"},
	"GET /api/v1/instance/watchdog":                                                                {role: "operator"},
	"GET /api/v1/me":                                                                               {role: "viewer"},
	"GET /api/v1/oauth/authorize/request":                                                          {role: "viewer", session: true},
	"GET /api/v1/observability":                                                                    {role: "viewer"},
	"GET /api/v1/observability/logs":                                                               {role: "viewer"},
	"GET /api/v1/observability/projects":                                                           {role: "viewer"},
	"GET /api/v1/openapi.yaml":                                                                     {role: "public"},
	"GET /api/v1/org":                                                                              {role: "viewer"},
	"GET /api/v1/org/audit-log":                                                                    {role: "viewer"},
	"GET /api/v1/org/audit-log/count":                                                              {role: "viewer"},
	"GET /api/v1/org/invites":                                                                      {role: "owner"},
	"GET /api/v1/org/invites/validate/{token}":                                                     {role: "public"},
	"GET /api/v1/org/isolation":                                                                    {role: "viewer"},
	"GET /api/v1/org/list":                                                                         {role: "viewer"},
	"GET /api/v1/org/members":                                                                      {role: "viewer"},
	"GET /api/v1/org/members/addable":                                                              {role: "owner"},
	"GET /api/v1/org/settings":                                                                     {role: "viewer"},
	"GET /api/v1/password-resets/{token}":                                                          {role: "public"},
	"GET /api/v1/projects":                                                                         {role: "viewer"},
	"GET /api/v1/projects/{projectID}":                                                             {role: "viewer"},
	"GET /api/v1/projects/{projectID}/api-keys":                                                    {role: "viewer"},
	"GET /api/v1/projects/{projectID}/data":                                                        {role: "developer"},
	"GET /api/v1/projects/{projectID}/compose-export":                                              {role: "viewer"},
	"GET /api/v1/projects/{projectID}/cycles":                                                      {role: "viewer"},
	"GET /api/v1/projects/{projectID}/docs":                                                        {role: "viewer"},
	"GET /api/v1/projects/{projectID}/docs/{pageID}":                                               {role: "viewer"},
	"GET /api/v1/projects/{projectID}/docs/{pageID}/versions":                                      {role: "viewer"},
	"GET /api/v1/projects/{projectID}/docs/{pageID}/versions/{version}":                            {role: "viewer"},
	"GET /api/v1/projects/{projectID}/environments":                                                {role: "viewer"},
	"GET /api/v1/projects/{projectID}/issue-counts":                                                {role: "viewer"},
	"GET /api/v1/projects/{projectID}/issue-labels":                                                {role: "viewer"},
	"GET /api/v1/projects/{projectID}/issue-settings":                                              {role: "viewer"},
	"GET /api/v1/projects/{projectID}/issue-timeline":                                              {role: "viewer"},
	"GET /api/v1/projects/{projectID}/issue-views":                                                 {role: "viewer"},
	"GET /api/v1/projects/{projectID}/issues":                                                      {role: "viewer"},
	"GET /api/v1/projects/{projectID}/issues/{issueNumber}":                                        {role: "viewer"},
	"GET /api/v1/projects/{projectID}/issues/{issueNumber}/timeline":                               {role: "viewer"},
	"GET /api/v1/projects/{projectID}/observability":                                               {role: "viewer"},
	"GET /api/v1/projects/{projectID}/overview":                                                    {role: "viewer"},
	"GET /api/v1/projects/{projectID}/previews":                                                    {role: "viewer"},
	"GET /api/v1/projects/{projectID}/previews/domain-check":                                       {role: "viewer"},
	"GET /api/v1/projects/{projectID}/previews/settings":                                           {role: "viewer"},
	"GET /api/v1/projects/{projectID}/repositories":                                                {role: "viewer"},
	"GET /api/v1/projects/{projectID}/repositories/{repositoryID}":                                 {role: "viewer"},
	"GET /api/v1/projects/{projectID}/repositories/{repositoryID}/blob":                            {role: "viewer"},
	"GET /api/v1/projects/{projectID}/repositories/{repositoryID}/branches":                        {role: "viewer"},
	"GET /api/v1/projects/{projectID}/repositories/{repositoryID}/commits":                         {role: "viewer"},
	"GET /api/v1/projects/{projectID}/repositories/{repositoryID}/commits/{sha}":                   {role: "viewer"},
	"GET /api/v1/projects/{projectID}/repositories/{repositoryID}/commits/{sha}/checks":            {role: "viewer"},
	"GET /api/v1/projects/{projectID}/repositories/{repositoryID}/commits/{sha}/diff":              {role: "viewer"},
	"GET /api/v1/projects/{projectID}/repositories/{repositoryID}/compare":                         {role: "viewer"},
	"GET /api/v1/projects/{projectID}/repositories/{repositoryID}/compare/diff":                    {role: "viewer"},
	"GET /api/v1/projects/{projectID}/repositories/{repositoryID}/mirrors":                         {role: "viewer"},
	"GET /api/v1/projects/{projectID}/repositories/{repositoryID}/pipelines":                       {role: "viewer"},
	"GET /api/v1/projects/{projectID}/repositories/{repositoryID}/pulls":                           {role: "viewer"},
	"GET /api/v1/projects/{projectID}/repositories/{repositoryID}/pulls/{pullNumber}":              {role: "viewer"},
	"GET /api/v1/projects/{projectID}/repositories/{repositoryID}/pulls/{pullNumber}/diff":         {role: "viewer"},
	"GET /api/v1/projects/{projectID}/repositories/{repositoryID}/pulls/{pullNumber}/preview":      {role: "viewer"},
	"GET /api/v1/projects/{projectID}/repositories/{repositoryID}/pulls/{pullNumber}/timeline":     {role: "viewer"},
	"GET /api/v1/projects/{projectID}/repositories/{repositoryID}/raw":                             {role: "viewer"},
	"GET /api/v1/projects/{projectID}/repositories/{repositoryID}/tags":                            {role: "viewer"},
	"GET /api/v1/projects/{projectID}/repositories/{repositoryID}/tree":                            {role: "viewer"},
	"GET /api/v1/projects/{projectID}/repositories/{repositoryID}/webhooks":                        {role: "owner"},
	"GET /api/v1/projects/{projectID}/repositories/{repositoryID}/webhooks/{webhookID}":            {role: "owner"},
	"GET /api/v1/projects/{projectID}/repositories/{repositoryID}/webhooks/{webhookID}/deliveries": {role: "owner"},
	"GET /api/v1/projects/{projectID}/repositories/{repositoryID}/webhooks/{webhookID}/deliveries/{deliveryID}": {role: "owner"},
	"GET /api/v1/projects/{projectID}/services":                                                                 {role: "viewer"},
	"GET /api/v1/projects/{projectID}/shared-services":                                                          {role: "viewer"},
	"GET /api/v1/projects/{projectID}/webhook":                                                                  {role: "developer"},
	"GET /api/v1/resources/default-limit":                                                                       {role: "viewer"},
	"GET /api/v1/servers":                                                                                       {role: "viewer"},
	"GET /api/v1/servers/{serverID}":                                                                            {role: "viewer"},
	"GET /api/v1/servers/{serverID}/backup":                                                                     {role: "viewer"},
	"GET /api/v1/servers/{serverID}/capabilities":                                                               {role: "viewer"},
	"GET /api/v1/servers/{serverID}/updates":                                                                    {role: "viewer"},
	"GET /api/v1/servers/{serverID}/updates/run":                                                                {role: "viewer"},
	"GET /api/v1/servers/{serverID}/disk":                                                                       {role: "viewer"},
	"GET /api/v1/servers/{serverID}/disk/clean":                                                                 {role: "viewer"},
	"GET /api/v1/servers/{serverID}/disk/policy":                                                                {role: "viewer"},
	"GET /api/v1/servers/{serverID}/capacity-summary":                                                           {role: "viewer"},
	"GET /api/v1/servers/{serverID}/dns-provider":                                                               {role: "viewer"},
	"GET /api/v1/servers/{serverID}/fit":                                                                        {role: "viewer"},
	"GET /api/v1/servers/{serverID}/gvisor":                                                                     {role: "viewer"},
	"GET /api/v1/servers/{serverID}/provisioning":                                                               {role: "viewer"},
	"GET /api/v1/servers/{serverID}/pty":                                                                        {role: "operator", session: true, password: true},
	"GET /api/v1/servers/{serverID}/requirements":                                                               {role: "viewer"},
	"GET /api/v1/servers/{serverID}/resources":                                                                  {role: "viewer"},
	"GET /api/v1/servers/{serverID}/services":                                                                   {role: "viewer"},
	"GET /api/v1/servers/{serverID}/shares":                                                                     {role: "viewer"},
	"GET /api/v1/servers/{serverID}/system-stats":                                                               {role: "viewer"},
	"GET /api/v1/servers/{serverID}/test-connection":                                                            {role: "viewer"},
	"GET /api/v1/services/{serviceID}":                                                                          {role: "viewer", serviceKey: true},
	"GET /api/v1/services/{serviceID}/config-files":                                                             {role: "viewer"},
	"GET /api/v1/services/{serviceID}/containers":                                                               {role: "viewer"},
	"GET /api/v1/services/{serviceID}/deploy-policy":                                                            {role: "viewer"},
	"GET /api/v1/services/{serviceID}/deployments":                                                              {role: "viewer", serviceKey: true},
	"GET /api/v1/services/{serviceID}/deployments/{deploymentID}":                                               {role: "viewer", serviceKey: true},
	"GET /api/v1/services/{serviceID}/deployments/{deploymentID}/logs":                                          {role: "viewer", serviceKey: true},
	"GET /api/v1/services/{serviceID}/deployments/{deploymentID}/logs/stream":                                   {role: "viewer", serviceKey: true},
	"GET /api/v1/services/{serviceID}/domains":                                                                  {role: "viewer", serviceKey: true},
	"GET /api/v1/services/{serviceID}/domains/check":                                                            {role: "viewer"},
	"GET /api/v1/services/{serviceID}/env-vars":                                                                 {role: "viewer", serviceKey: true},
	"GET /api/v1/services/{serviceID}/logs/live":                                                                {role: "viewer"},
	"GET /api/v1/services/{serviceID}/metrics":                                                                  {role: "viewer"},
	"GET /api/v1/services/{serviceID}/resources":                                                                {role: "viewer"},
	"GET /api/v1/services/{serviceID}/admin-password":                                                           {role: "developer", session: true},
	"GET /api/v1/services/{serviceID}/admin-password/value":                                                     {role: "developer", session: true, password: true},
	"GET /api/v1/services/{serviceID}/secrets":                                                                  {role: "viewer", serviceKey: true},
	"GET /api/v1/services/{serviceID}/shared-volumes":                                                           {role: "viewer"},
	"GET /api/v1/services/{serviceID}/stats":                                                                    {role: "viewer"},
	"GET /api/v1/services/{serviceID}/versions":                                                                 {role: "viewer"},
	"GET /api/v1/services/{serviceID}/data":                                                                     {role: "developer"},
	"GET /api/v1/services/{serviceID}/volumes":                                                                  {role: "viewer"},
	"GET /api/v1/services/{serviceID}/work":                                                                     {role: "viewer"},
	"GET /api/v1/shared-services":                                                                               {role: "viewer"},
	"GET /api/v1/shared-volumes":                                                                                {role: "viewer"},
	"GET /api/v1/user/api-keys":                                                                                 {role: "viewer"},
	"GET /api/v1/user/ssh-keys":                                                                                 {role: "viewer"},
	"GET /api/v1/workspace/cycles":                                                                              {role: "viewer"},
	"GET /api/v1/workspace/cycles/{cycleNumber}":                                                                {role: "viewer"},
	"GET /api/v1/workspace/cycles/{cycleNumber}/issues":                                                         {role: "viewer"},
	"GET /api/v1/workspace/environments":                                                                        {role: "viewer"},
	"GET /api/v1/workspace/issue-settings":                                                                      {role: "viewer"},
	"GET /api/v1/workspace/issue-types":                                                                         {role: "viewer"},
	"GET /api/v1/workspace/my-issues":                                                                           {role: "viewer"},
	"GET /api/v1/workspace/search":                                                                              {role: "viewer"},
	"GET /api/v1/workspace/services":                                                                            {role: "viewer"},
	"GET /health":                                                                                               {role: "public"},
	"GET /mcp":                                                                                                  {role: "self"},
	"GET /{workspace}/{repo}/info/refs":                                                                         {role: "self"},
	"PATCH /api/v1/environments/{environmentID}":                                                                {role: "developer"},
	"PATCH /api/v1/instance/setup":                                                                              {role: "operator", session: true},
	"PATCH /api/v1/instance/users/{userID}":                                                                     {role: "operator", session: true},
	"PATCH /api/v1/projects/{projectID}/docs/{pageID}":                                                          {role: "developer"},
	"PATCH /api/v1/projects/{projectID}/issue-settings":                                                         {role: "owner"},
	"PATCH /api/v1/projects/{projectID}/issue-views/{viewID}":                                                   {role: "developer"},
	"PATCH /api/v1/projects/{projectID}/issues/{issueNumber}":                                                   {role: "developer"},
	"PATCH /api/v1/projects/{projectID}/issues/{issueNumber}/comments/{commentID}":                              {role: "developer"},
	"PATCH /api/v1/projects/{projectID}/repositories/{repositoryID}":                                            {role: "owner", session: true, password: true},
	"PATCH /api/v1/projects/{projectID}/repositories/{repositoryID}/pulls/{pullNumber}":                         {role: "developer"},
	"PATCH /api/v1/projects/{projectID}/repositories/{repositoryID}/pulls/{pullNumber}/comments/{commentID}": {role: "developer"},
	"PATCH /api/v1/projects/{projectID}/repositories/{repositoryID}/webhooks/{webhookID}":                    {role: "owner"},
	"PATCH /api/v1/shared-volumes/{volumeID}":                                                                {role: "owner"},
	"PATCH /api/v1/workspace/cycles/{cycleNumber}":                                                           {role: "developer"},
	"PATCH /api/v1/workspace/issue-settings":                                                                 {role: "owner"},
	"PATCH /api/v1/workspace/issue-types/{typeID}":                                                           {role: "owner"},
	"POST /api/v1/agents/approvals/{approvalID}/approve":                                                     {role: "developer", session: true},
	"POST /api/v1/agents/approvals/{approvalID}/deny":                                                        {role: "developer", session: true},
	"POST /api/v1/agents/pause":                                                                              {role: "owner", session: true},
	"POST /api/v1/alerts/channel/rotate-secret":                                                              {role: "owner"},
	"POST /api/v1/alerts/channel/test":                                                                       {role: "owner"},
	"POST /api/v1/auth/login":                                                                                {role: "public"},
	"POST /api/v1/auth/reauth":                                                                               {role: "viewer", session: true},
	"POST /api/v1/auth/register":                                                                             {role: "public"},
	"POST /api/v1/environments/{environmentID}/env-vars":                                                     {role: "developer"},
	"POST /api/v1/environments/{environmentID}/secrets":                                                      {role: "developer"},
	"POST /api/v1/environments/{environmentID}/services":                                                     {role: "developer"},
	"POST /api/v1/inbox/done":                                                                                {role: "viewer"},
	"POST /api/v1/inbox/read":                                                                                {role: "viewer"},
	"POST /api/v1/inbox/undone":                                                                              {role: "viewer"},
	"POST /api/v1/instance/agents/pause":                                                                     {role: "operator", session: true},
	"POST /api/v1/instance/operators":                                                                        {role: "operator", session: true},
	"POST /api/v1/instance/panel/capabilities":                                                               {role: "operator"},
	"POST /api/v1/instance/panel/updates":                                                                    {role: "operator"},
	"POST /api/v1/instance/panel/updates/run":                                                                {role: "operator", session: true, password: true},
	"POST /api/v1/instance/panel/disk/clean":                                                                 {role: "operator", session: true, password: true},
	"POST /api/v1/instance/panel/disk/volumes/remove":                                                        {role: "operator", session: true, password: true},
	"PUT /api/v1/instance/panel/disk/policy":                                                                 {role: "operator", session: true},
	"POST /api/v1/instance/panel/gvisor":                                                                     {role: "operator", session: true, password: true},
	"POST /api/v1/instance/setup/domain-check":                                                               {role: "operator"},
	"POST /api/v1/instance/upgrade":                                                                          {role: "operator", session: true},
	"POST /api/v1/instance/upgrade/check":                                                                    {role: "operator"},
	"POST /api/v1/instance/users/{userID}/password-resets":                                                   {role: "operator", session: true},
	"POST /api/v1/instance/watchdog/test":                                                                    {role: "operator"},
	"POST /api/v1/markdown":                                                                                  {role: "viewer"},
	"POST /api/v1/me/sessions/sign-out":                                                                      {role: "viewer", session: true},
	"POST /api/v1/me/sessions/sign-out-others":                                                               {role: "viewer", session: true},
	"POST /api/v1/oauth/authorize":                                                                           {role: "viewer", session: true},
	"POST /api/v1/oauth/register":                                                                            {role: "public"},
	"POST /api/v1/oauth/revoke":                                                                              {role: "public"},
	"POST /api/v1/oauth/token":                                                                               {role: "public"},
	"POST /api/v1/org":                                                                                       {role: "operator"},
	"POST /api/v1/org/invites":                                                                               {role: "owner"},
	"POST /api/v1/org/invites/accept":                                                                        {role: "viewer"},
	"POST /api/v1/org/leave":                                                                                 {role: "viewer"},
	"POST /api/v1/org/members":                                                                               {role: "owner"},
	"POST /api/v1/org/switch":                                                                                {role: "viewer"},
	"POST /api/v1/password-resets/{token}":                                                                   {role: "public"},
	"POST /api/v1/projects":                                                                                  {role: "developer"},
	"POST /api/v1/projects/{projectID}/docs":                                                                 {role: "developer"},
	"POST /api/v1/projects/{projectID}/docs/{pageID}/restore":                                                {role: "developer"},
	"POST /api/v1/projects/{projectID}/docs/{pageID}/steps/{step}/run":                                       {role: "developer", session: true},
	"POST /api/v1/projects/{projectID}/docs/{pageID}/verify":                                                 {role: "developer"},
	"POST /api/v1/projects/{projectID}/environments":                                                         {role: "developer"},
	"POST /api/v1/projects/{projectID}/issue-views":                                                          {role: "developer"},
	"POST /api/v1/projects/{projectID}/issues":                                                               {role: "developer"},
	"POST /api/v1/projects/{projectID}/issues/{issueNumber}/clone":                                           {role: "developer"},
	"POST /api/v1/projects/{projectID}/issues/{issueNumber}/comments":                                        {role: "developer"},
	"POST /api/v1/projects/{projectID}/issues/{issueNumber}/relations":                                       {role: "developer"},
	"POST /api/v1/projects/{projectID}/repositories":                                                         {role: "developer"},
	"POST /api/v1/projects/{projectID}/repositories/{repositoryID}/branches":                                 {role: "developer"},
	"POST /api/v1/projects/{projectID}/repositories/{repositoryID}/commits/{sha}/checks":                     {role: "developer"},
	"POST /api/v1/projects/{projectID}/repositories/{repositoryID}/import":                                   {role: "developer"},
	"POST /api/v1/projects/{projectID}/repositories/{repositoryID}/mirrors":                                  {role: "owner", session: true, password: true},
	"POST /api/v1/projects/{projectID}/repositories/{repositoryID}/mirrors/{mirrorID}/sync":                  {role: "developer"},
	"POST /api/v1/projects/{projectID}/repositories/{repositoryID}/pulls":                                    {role: "developer"},
	"POST /api/v1/projects/{projectID}/repositories/{repositoryID}/pulls/{pullNumber}/comments":              {role: "developer"},
	"POST /api/v1/projects/{projectID}/repositories/{repositoryID}/pulls/{pullNumber}/merge":                 {role: "developer", session: true},
	"POST /api/v1/projects/{projectID}/repositories/{repositoryID}/pulls/{pullNumber}/preview":               {role: "developer"},
	"POST /api/v1/projects/{projectID}/repositories/{repositoryID}/pulls/{pullNumber}/preview/approve":       {role: "developer", session: true},
	"POST /api/v1/projects/{projectID}/repositories/{repositoryID}/pulls/{pullNumber}/preview/redeploy":      {role: "developer"},
	"POST /api/v1/projects/{projectID}/repositories/{repositoryID}/pulls/{pullNumber}/preview/stop":          {role: "developer"},
	"POST /api/v1/projects/{projectID}/repositories/{repositoryID}/webhooks":                                 {role: "owner"},
	"POST /api/v1/projects/{projectID}/repositories/{repositoryID}/webhooks/{webhookID}/deliveries/{deliveryID}/redeliver": {role: "owner"},
	"POST /api/v1/projects/{projectID}/repositories/{repositoryID}/webhooks/{webhookID}/secret":                            {role: "owner"},
	"POST /api/v1/projects/{projectID}/repositories/{repositoryID}/webhooks/{webhookID}/test":                              {role: "owner"},
	"POST /api/v1/projects/{projectID}/shared-services":                                                                    {role: "developer"},
	"POST /api/v1/projects/{projectID}/webhook/rotate":                                                                     {role: "developer"},
	"POST /api/v1/servers":                             {role: "owner"},
	"POST /api/v1/servers/{serverID}/agent/adopt":      {role: "owner", session: true, password: true},
	"POST /api/v1/servers/{serverID}/agent/install":    {role: "owner", session: true, password: true},
	"POST /api/v1/servers/{serverID}/agent/join-token": {role: "owner", session: true, password: true},
	// Owners and API keys, as creating an agent server, which answers with
	// a join token too; single use, an hour, and only for a server whose
	// agent nobody has pinned.
	"POST /api/v1/servers/{serverID}/agent/join-token (first join)":         {role: "owner"},
	"POST /api/v1/servers/{serverID}/agent/rotate":                          {role: "owner", session: true},
	"POST /api/v1/servers/{serverID}/agent/use-panel-state":                 {role: "owner", session: true, password: true},
	"POST /api/v1/servers/{serverID}/capabilities":                          {role: "developer"},
	"POST /api/v1/servers/{serverID}/updates":                               {role: "developer"},
	"POST /api/v1/servers/{serverID}/updates/run":                           {role: "owner", session: true, password: true},
	"POST /api/v1/servers/{serverID}/disk/clean":                            {role: "owner", session: true, password: true},
	"POST /api/v1/servers/{serverID}/disk/volumes/remove":                   {role: "owner", session: true, password: true},
	"PUT /api/v1/servers/{serverID}/disk/policy":                            {role: "owner", session: true},
	"POST /api/v1/servers/{serverID}/connection":                            {role: "owner", session: true, password: true},
	"POST /api/v1/servers/{serverID}/discover-capacity":                     {role: "developer"},
	"POST /api/v1/servers/{serverID}/exec":                                  {role: "operator", session: true, password: true},
	"POST /api/v1/servers/{serverID}/gvisor":                                {role: "operator", session: true, password: true},
	"POST /api/v1/servers/{serverID}/host-key/scan":                         {role: "developer"},
	"POST /api/v1/servers/{serverID}/provision":                             {role: "developer"},
	"POST /api/v1/services/{serviceID}/admin-password":                      {role: "developer", session: true, password: true},
	"POST /api/v1/services/{serviceID}/deploy-policy/exception":             {role: "operator", session: true, password: true},
	"POST /api/v1/services/{serviceID}/deployments":                         {role: "operator", serviceKey: true},
	"POST /api/v1/services/{serviceID}/deployments/{deploymentID}/issue":    {role: "developer"},
	"POST /api/v1/services/{serviceID}/deployments/{deploymentID}/rollback": {role: "developer", serviceKey: true},
	"POST /api/v1/services/{serviceID}/domains":                             {role: "developer"},
	"POST /api/v1/services/{serviceID}/env-vars":                            {role: "developer", serviceKey: true},
	"POST /api/v1/services/{serviceID}/exec":                                {role: "operator", session: true, password: true},
	"POST /api/v1/services/{serviceID}/health-check/test":                   {role: "developer"},
	"POST /api/v1/services/{serviceID}/restart":                             {role: "developer"},
	"POST /api/v1/services/{serviceID}/secrets":                             {role: "developer", serviceKey: true},
	"POST /api/v1/services/{serviceID}/move-to-workspace":                   {role: "developer"},
	"POST /api/v1/services/{serviceID}/stop":                                {role: "developer"},
	"POST /api/v1/shared-services":                                          {role: "developer"},
	"POST /api/v1/shared-volumes":                                           {role: "owner"},
	"POST /api/v1/user/api-keys":                                            {role: "viewer", session: true},
	"POST /api/v1/user/ssh-keys":                                            {role: "viewer"},
	"POST /api/v1/webhooks/github":                                          {role: "public"},
	"POST /api/v1/webhooks/github/{projectID}":                              {role: "public"},
	"POST /api/v1/workspace/issue-types":                                    {role: "owner"},
	"POST /mcp":                                                             {role: "self"},
	"POST /{workspace}/{repo}/git-receive-pack":                             {role: "self"},
	"POST /{workspace}/{repo}/git-upload-pack":                              {role: "self"},
	"PUT /api/v1/agents/policy":                                             {role: "owner", session: true},
	"PUT /api/v1/agents/policy/projects/{projectID}":                        {role: "owner", session: true},
	"PUT /api/v1/alerts/channel":                                            {role: "owner"},
	"PUT /api/v1/environments/{environmentID}/env-vars":                     {role: "developer"},
	"PUT /api/v1/environments/{environmentID}/secrets/{key}":                {role: "developer"},
	"PUT /api/v1/instance/backup":                                           {role: "operator"},
	"PUT /api/v1/instance/setup/domain":                                     {role: "operator", session: true, password: true},
	"PUT /api/v1/instance/setup/role":                                       {role: "operator", session: true},
	"PUT /api/v1/instance/watchdog":                                         {role: "operator"},
	"PUT /api/v1/me":                                                        {role: "viewer"},
	"PUT /api/v1/me/password":                                               {role: "viewer", session: true},
	"PUT /api/v1/me/theme":                                                  {role: "viewer"},
	"DELETE /api/v1/observability/grafana":                                  {role: "owner"},
	"DELETE /api/v1/observability/loki":                                     {role: "owner"},
	"DELETE /api/v1/observability/prometheus":                               {role: "owner"},
	"PUT /api/v1/observability/grafana":                                     {role: "owner"},
	"PUT /api/v1/observability/loki":                                        {role: "owner"},
	"PUT /api/v1/observability/prometheus":                                  {role: "owner"},
	"PUT /api/v1/org":                                                       {role: "owner"},
	"PUT /api/v1/org/members/{userID}/role":                                 {role: "owner"},
	"PUT /api/v1/org/settings":                                              {role: "owner"},
	"PATCH /api/v1/org/settings":                                            {role: "owner"},
	"PUT /api/v1/projects/{projectID}":                                      {role: "developer"},
	"PUT /api/v1/projects/{projectID}/observability/grafana":                {role: "developer"},
	"PUT /api/v1/projects/{projectID}/observability/loki":                   {role: "developer"},
	"PUT /api/v1/projects/{projectID}/previews/settings":                    {role: "owner", session: true},
	"PUT /api/v1/servers/{serverID}/backup":                                 {role: "owner"},
	"PUT /api/v1/servers/{serverID}/capacity":                               {role: "owner"},
	"PUT /api/v1/servers/{serverID}/dns-provider":                           {role: "owner"},
	"PUT /api/v1/servers/{serverID}/host-key":                               {role: "operator", session: true, password: true},
	"PUT /api/v1/servers/{serverID}/shares/{orgID}":                         {role: "owner"},
	"PUT /api/v1/services/{serviceID}/compose-limits":                       {role: "developer"},
	"PUT /api/v1/services/{serviceID}/config":                               {role: "owner", session: true},
	"PUT /api/v1/services/{serviceID}/config-files":                         {role: "developer"},
	"PUT /api/v1/services/{serviceID}/env-vars":                             {role: "developer", serviceKey: true},
	"PUT /api/v1/services/{serviceID}/secrets/{key}":                        {role: "developer", serviceKey: true},
	"PUT /api/v1/services/{serviceID}/server":                               {role: "developer"},
	"PUT /api/v1/services/{serviceID}/shared-volumes":                       {role: "developer"},
}

// agentRows answers the reads the agent service makes before its checks: a
// connection in testOrg that belongs to someone else, and a project with no
// policy of its own.
type agentRows struct{ agent.Repository }

func (agentRows) GetConnection(_ context.Context, id string) (*agent.ConnectionRow, error) {
	return &agent.ConnectionRow{ID: id, OrgID: testOrg, UserID: uuid.NewString()}, nil
}

func (agentRows) ListProjectPolicy(context.Context, string) (map[string]map[string]string, error) {
	return map[string]map[string]string{}, nil
}

// diskRows owns every server for testOrg, with the default policy.
type diskRows struct{}

func (diskRows) Server(_ context.Context, id string) (disk.HostRow, error) {
	if id == "" {
		return disk.HostRow{}, nil
	}
	return disk.HostRow{ID: id, OrgID: testOrg}, nil
}
func (diskRows) Servers(context.Context) ([]disk.HostRow, error)                  { return nil, nil }
func (diskRows) SetPolicy(context.Context, string, string, disk.Policy) error     { return nil }
func (diskRows) SetLastClean(context.Context, string, disk.Clean) error           { return nil }
func (diskRows) HostServices(context.Context, string) ([]disk.HostService, error) { return nil, nil }
func (diskRows) ServicePrefixes(context.Context) (map[string]bool, error)         { return nil, nil }
func (diskRows) LiveDeployments(context.Context, []string) ([]disk.LiveDeployment, error) {
	return nil, nil
}
func (diskRows) MarkImagesRemoved(context.Context, []string) error { return nil }

// diskNoHosts reaches no Docker.
type diskNoHosts struct{}

func (diskNoHosts) Host(context.Context, string) (disk.Host, error) {
	return nil, errors.New("no Docker in the access probes")
}

type diskNoAudit struct{}

func (diskNoAudit) Audit(context.Context, string, string, string, string, map[string]any) {}

type diskProbePeople struct{ d *directory }

func (p diskProbePeople) IsOperator(ctx context.Context, id string) (bool, error) {
	return p.d.IsOperator(ctx, id)
}
func (diskProbePeople) Name(context.Context, string) string { return "" }
