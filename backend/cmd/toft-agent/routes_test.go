package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/rollout/fakedocker"
	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"github.com/docker/docker/api/types/container"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
)

// routedLabels are a routed slot's labels as the panel writes them.
func routedLabels(svc, dep string) map[string]string {
	id8 := svc[:8]
	return map[string]string{
		rollout.LabelServiceID: svc, rollout.LabelDeploymentID: dep,
		"traefik.enable": "true",
		"traefik.http.routers.paas-" + id8 + "-0.rule":                        "Host(`" + id8 + ".test`)",
		"traefik.http.routers.paas-" + id8 + "-0.entrypoints":                 "websecure",
		"traefik.http.routers.paas-" + id8 + "-0.tls.certresolver":            "letsencrypt",
		"traefik.http.services.paas-svc-" + id8 + ".loadbalancer.server.port": "8080",
	}
}

func readRoutes(t *testing.T, id8 string) *rollout.RouteFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(routesDir, rollout.RouteFileName(id8)))
	if err != nil {
		return nil
	}
	f, err := rollout.ParseRouteFile(raw)
	if err != nil {
		t.Fatalf("%s: %v", id8, err)
	}
	return f
}

func serverURLs(f *rollout.RouteFile, id8 string) []string {
	var out []string
	for _, s := range f.HTTP.Services["paas-svc-"+id8].LoadBalancer.Servers {
		out = append(out, s.URL)
	}
	return out
}

// The agent writes each routed service's file from its serving container
// (D-113): the desired state's deployment where two slots run, nothing for a
// service in a rollout, nothing from a container that only claims to be a
// service's, and it removes the file of a service that no longer runs and sets
// aside a file Traefik couldn't parse.
func TestAgentWritesEachServicesRoutes(t *testing.T) {
	stateDir, routesDir = t.TempDir(), t.TempDir()
	const (
		plain   = "aaaa1111-0000-0000-0000-000000000001"
		twoRun  = "bbbb2222-0000-0000-0000-000000000002"
		rolling = "cccc3333-0000-0000-0000-000000000003"
		gone    = "dddd4444-0000-0000-0000-000000000004"
		stack   = "eeee5555-0000-0000-0000-000000000005"
	)
	f := fakedocker.New(t)
	f.Add(rollout.BlueContainerName(plain, "web", ""), container.StateRunning, routedLabels(plain, "dep-a"))
	f.Add(rollout.BlueContainerName(twoRun, "api", ""), container.StateRunning, routedLabels(twoRun, "dep-b1"))
	f.Add(rollout.GreenContainerName(twoRun, "api", ""), container.StateRunning, routedLabels(twoRun, "dep-b2"))
	f.Add(rollout.BlueContainerName(rolling, "job", ""), container.StateRunning, routedLabels(rolling, "dep-c"))
	stackLabels := routedLabels(stack, "")
	delete(stackLabels, rollout.LabelServiceID)
	stackLabels[rollout.LabelComposeProject] = "paas-shop-" + stack[:8]
	f.Add("paas-shop-"+stack[:8]+"-web-1", container.StateRunning, stackLabels)
	// An app's container carrying another service's labels from its image.
	f.Add("evil", container.StateRunning, routedLabels(gone, "dep-x"))

	desired, _ := json.Marshal(agentproto.Desired{Epoch: "e", Generation: 1, Services: []agentproto.DesiredService{{ID: twoRun, Running: true, Deployment: "dep-b2"}}})
	if err := os.WriteFile(desiredFile(), desired, 0o600); err != nil {
		t.Fatal(err)
	}
	kept := []byte("http: {}\n")
	for name, body := range map[string][]byte{
		rollout.RouteFileName(rolling[:8]): kept,
		rollout.RouteFileName(gone[:8]):    kept,
		"someone-elses.yml":                []byte("http:\n  routers: [oops\n"),
	} {
		if err := os.WriteFile(filepath.Join(routesDir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	m := testRollouts(f)
	m.queues[rolling] = []func(){func() {}}
	n, err := m.routes.sync(context.Background())
	if err != nil || n != 4 {
		t.Fatalf("sync: %d %v", n, err)
	}
	if got := serverURLs(readRoutes(t, plain[:8]), plain[:8]); len(got) != 1 || got[0] != "http://"+rollout.BlueContainerName(plain, "web", "")+":8080" {
		t.Fatalf("plain: %v", got)
	}
	if got := serverURLs(readRoutes(t, twoRun[:8]), twoRun[:8]); len(got) != 1 || got[0] != "http://"+rollout.GreenContainerName(twoRun, "api", "")+":8080" {
		t.Fatalf("two slots: %v", got)
	}
	if got := serverURLs(readRoutes(t, stack[:8]), stack[:8]); len(got) != 1 || !strings.Contains(got[0], "-web-1:8080") {
		t.Fatalf("stack: %v", got)
	}
	if raw, _ := os.ReadFile(filepath.Join(routesDir, rollout.RouteFileName(rolling[:8]))); string(raw) != string(kept) {
		t.Fatalf("a service in a rollout had its file changed: %q", raw)
	}
	if _, err := os.Stat(filepath.Join(routesDir, rollout.RouteFileName(gone[:8]))); !os.IsNotExist(err) {
		t.Fatalf("the file of a service with no slot stayed (an impostor kept it?): %v", err)
	}
	if _, err := os.Stat(filepath.Join(routesDir, "someone-elses.yml.broken")); err != nil {
		t.Fatalf("the broken file wasn't set aside: %v", err)
	}

	// Traefik's API route: the agent's password, and only on its entry point.
	var api agentRoutes
	raw, _ := os.ReadFile(filepath.Join(routesDir, agentRoutesFile))
	if err := yaml.Unmarshal(raw, &api); err != nil {
		t.Fatal(err)
	}
	secret, _ := os.ReadFile(apiSecretFile())
	user, hash, _ := strings.Cut(api.HTTP.Middlewares["toft-agent-api-auth"].BasicAuth.Users[0], ":")
	if r := api.HTTP.Routers["toft-agent-api"]; user != agentAPIUser || bcrypt.CompareHashAndPassword([]byte(hash), secret) != nil ||
		r.Service != "api@internal" || len(r.EntryPoints) != 1 || r.EntryPoints[0] != "toft-api" {
		t.Fatalf("the API's route: %s", raw)
	}
	if _, err := m.routes.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(filepath.Join(routesDir, agentRoutesFile)); string(again) != string(raw) {
		t.Fatal("a resync replaced the API's route (and Traefik's password hash) for nothing")
	}
}

// fakeTraefik answers Traefik's API for the services and routers it holds,
// behind the agent's password.
type fakeTraefik struct {
	mu       sync.Mutex
	services map[string][]string // name → server URLs
	routers  map[string]string   // name → status
}

func (ft *fakeTraefik) start(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secret, _ := os.ReadFile(apiSecretFile())
		if u, p, ok := r.BasicAuth(); !ok || u != agentAPIUser || p != strings.TrimSpace(string(secret)) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		ft.mu.Lock()
		defer ft.mu.Unlock()
		if name, ok := strings.CutPrefix(r.URL.Path, "/api/http/services/"); ok {
			urls, ok := ft.services[strings.TrimSuffix(name, "@file")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			var st apiState
			for _, u := range urls {
				st.LoadBalancer.Servers = append(st.LoadBalancer.Servers, struct {
					URL string `json:"url"`
				}{u})
			}
			_ = json.NewEncoder(w).Encode(st)
			return
		}
		name, _ := strings.CutPrefix(r.URL.Path, "/api/http/routers/")
		status, ok := ft.routers[strings.TrimSuffix(name, "@file")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(apiState{Status: status, Error: []string{"bad rule"}})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// A rollout's switch writes the new slot's file and returns once Traefik
// serves it there; a router Traefik disables fails the switch at once and
// puts the previous file back.
func TestRouteWaitsForTraefikToServeTheNewSlot(t *testing.T) {
	stateDir, routesDir = t.TempDir(), t.TempDir()
	id8 := testSvc[:8]
	blue, green := rollout.BlueContainerName(testSvc, "web", ""), rollout.GreenContainerName(testSvc, "web", "")
	f := fakedocker.New(t)
	f.Add(blue, container.StateRunning, routedLabels(testSvc, "dep-1"))
	f.Add(green, container.StateRunning, routedLabels(testSvc, "dep-2"))
	m := testRollouts(f)
	if err := m.routes.ensureAPIRoute(); err != nil {
		t.Fatal(err)
	}
	ft := &fakeTraefik{services: map[string][]string{"paas-svc-" + id8: {"http://" + blue + ":8080"}}, routers: map[string]string{"paas-" + id8 + "-0": "enabled"}}
	api := ft.start(t)
	prev := []byte("http: {}\n")
	path := filepath.Join(routesDir, rollout.RouteFileName(id8))
	if err := os.WriteFile(path, prev, 0o600); err != nil {
		t.Fatal(err)
	}

	// Traefik picks the file up a little later.
	go func() {
		for {
			if raw, _ := os.ReadFile(path); strings.Contains(string(raw), green) {
				ft.mu.Lock()
				ft.services["paas-svc-"+id8] = []string{"http://" + green + ":8080"}
				ft.mu.Unlock()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	var lines []string
	if err := m.routes.route(context.Background(), api, testSvc, green, func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatalf("route: %v %v", err, lines)
	}
	if got := serverURLs(readRoutes(t, id8), id8); len(got) != 1 || got[0] != "http://"+green+":8080" {
		t.Fatalf("file: %v", got)
	}

	ft.mu.Lock()
	ft.routers["paas-"+id8+"-0"] = "disabled"
	ft.services["paas-svc-"+id8] = []string{"http://" + blue + ":8080"}
	ft.mu.Unlock()
	before, _ := os.ReadFile(path)
	err := m.routes.route(context.Background(), api, testSvc, blue, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "bad rule") {
		t.Fatalf("a disabled router: %v", err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatalf("the previous file wasn't put back:\n%s", after)
	}
}

// The agent switches routes itself only where Traefik reads its files, and
// reaches Traefik's API on the loopback port it is published on.
func TestRoutesFollowTraefiksProvider(t *testing.T) {
	f := fakedocker.New(t)
	tr := f.Add(traefikContainer, container.StateRunning, nil)
	m := testRollouts(f)
	tr.Args = []string{"--providers.docker=true"}
	tr.HostPorts = map[string]string{traefikAPIPort: "127.0.0.1:41234"}
	if api := m.routes.liveAPI(context.Background()); api != "" {
		t.Fatalf("labels: %q", api)
	}
	tr.Args = []string{traefikFileFlag + "/etc/traefik/dynamic"}
	if api := m.routes.liveAPI(context.Background()); api != "http://127.0.0.1:41234" {
		t.Fatalf("files: %q", api)
	}
	tr.HostPorts = map[string]string{traefikAPIPort: "0.0.0.0:41234"}
	if api := m.routes.liveAPI(context.Background()); api != "" {
		t.Fatalf("an API published beyond the loopback was used: %q", api)
	}
}

// The agent writes no routes while a rollout runs: that rollout's switch
// was chosen at its start, so the panel asks again once it ends.
func TestRoutesWaitForTheRunningRollouts(t *testing.T) {
	stateDir, routesDir = t.TempDir(), t.TempDir()
	m := testRollouts(fakedocker.New(t))
	m.mu.Lock()
	m.queues[testSvc] = nil
	m.mu.Unlock()
	pem, pub := panelKey(t)
	c, err := connectWith(t, pem, ssh.FingerprintSHA256(pub), m)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ask := func() (string, error) {
		return c.SubsystemContext(context.Background(), agentproto.SubsystemRoutes, strings.NewReader("{}\n"))
	}
	if out, err := ask(); err == nil || !strings.Contains(out, "a rollout is running") {
		t.Fatalf("routes written during a rollout: %q %v", out, err)
	}
	m.mu.Lock()
	delete(m.queues, testSvc)
	m.mu.Unlock()
	if out, err := ask(); err != nil {
		t.Fatalf("after the rollout: %q %v", out, err)
	}
}
