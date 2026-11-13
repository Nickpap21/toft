package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/docker/docker/api/types/container"
	dockerevents "github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

// Traefik's routes (D-113): on a server whose agent writes them, Traefik
// reads one file per service from routesDir instead of the containers'
// labels, and no longer mounts the Docker socket. The agent writes each
// service's file from its serving container's labels (rollout.RoutesFor):
// when containers start or stop, every resyncRoutes, and, for a rollout,
// at its switch (route), where it waits for Traefik to serve the new slot
// before the previous one is retired.
//
// A file Traefik can't parse stops it from loading any change to the
// directory (and leaves it with no file routes when it starts), so every
// file is rendered from typed data and parsed back before it is renamed
// into place, and a file that doesn't parse is set aside.

// routesDir is the directory Traefik reads, bind-mounted into it.
var routesDir = "/opt/paas-traefik/dynamic"

const (
	// traefikContainer is the server's Traefik (server.provision).
	traefikContainer = "paas-traefik"
	// traefikFileFlag marks a Traefik that reads routesDir.
	traefikFileFlag = "--providers.file.directory="
	// traefikAPIPort is the API's entry point in the container, published
	// on the server's loopback only, behind agentAPIUser's password.
	traefikAPIPort  = "8082/tcp"
	agentRoutesFile = "toft-agent.yml"
	agentAPIUser    = "toft-agent"
	// resyncRoutes is how often the routes are compared with the
	// containers besides Docker's events.
	resyncRoutes = 30 * time.Second
	// routeWait bounds how long a rollout's switch waits for Traefik to
	// serve the new slot (it reloads files within a few seconds).
	routeWait = 20 * time.Second
)

func apiSecretFile() string { return filepath.Join(stateDir, "traefik-api.secret") }

// routes writes the routes. Its methods are safe for concurrent use.
type routes struct {
	dc *client.Client
	// busy are the services with a rollout running or queued: their
	// rollout routes them.
	busy func() map[string]bool
	// active is the service's active deployment in the desired state the
	// agent holds, to tell which of two running slots serves.
	active func() map[string]string
	http   *http.Client

	mu   sync.Mutex // one writer at a time
	kick chan struct{}
}

func newRoutes(dc *client.Client, busy func() map[string]bool, active func() map[string]string) *routes {
	return &routes{dc: dc, busy: busy, active: active, http: &http.Client{Timeout: 5 * time.Second}, kick: make(chan struct{}, 1)}
}

// soon asks for a resync.
func (r *routes) soon() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// watch keeps the routes in step with the containers until ctx ends:
// after a container starts, stops or goes, and every resyncRoutes. It does
// nothing until the routes directory exists (the panel's cutover makes it).
func (r *routes) watch(ctx context.Context) {
	go func() {
		for ctx.Err() == nil {
			msgs, errs := r.dc.Events(ctx, dockerevents.ListOptions{Filters: filters.NewArgs(
				filters.Arg("type", string(dockerevents.ContainerEventType)),
				filters.Arg("event", "start"), filters.Arg("event", "die"), filters.Arg("event", "destroy"),
				filters.Arg("event", "rename"))})
		read:
			for {
				select {
				case <-ctx.Done():
					return
				case <-msgs:
					r.soon()
				case <-errs:
					break read
				}
			}
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second): // Docker restarting
			}
		}
	}()
	t := time.NewTicker(resyncRoutes)
	defer t.Stop()
	r.soon()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.kick:
			// A burst of events (a stack's containers) is one resync.
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
		if _, err := os.Stat(routesDir); err != nil {
			continue
		}
		if _, err := r.sync(ctx); err != nil {
			slog.Warn("could not bring Traefik's routes in step with the containers", "error", err)
		}
	}
}

// candidate is a running container that routes a service.
type candidate struct {
	name, deployment string
	labels           map[string]string
	created          int64
}

// sync writes every routed service's file from its running containers and
// removes the files of services with none, skipping services in a rollout.
// It answers how many services it routes.
func (r *routes) sync(ctx context.Context) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := os.MkdirAll(routesDir, 0o755); err != nil {
		return 0, err
	}
	if err := r.ensureAPIRoute(); err != nil {
		return 0, err
	}
	list, err := r.dc.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return 0, fmt.Errorf("list the containers: %w", err)
	}
	busy8 := map[string]bool{}
	for id := range r.busy() {
		busy8[short(id)] = true
	}
	active := r.active()
	slots := map[string][]candidate{}  // by service ID
	stacks := map[string][]candidate{} // by service ID's first 8
	for _, c := range list {
		if c.State != container.StateRunning || !strings.EqualFold(c.Labels["traefik.enable"], "true") {
			continue
		}
		name := rollout.SummaryName(c)
		cand := candidate{name: name, deployment: c.Labels[rollout.LabelDeploymentID], labels: c.Labels, created: c.Created}
		if rollout.IsPlatformSlot(c.Labels, name) {
			id := c.Labels[rollout.LabelServiceID]
			slots[id] = append(slots[id], cand)
		} else if m := composeProjectRe.FindStringSubmatch(c.Labels[rollout.LabelComposeProject]); m != nil &&
			c.Labels["traefik.http.services.paas-svc-"+m[1]+".loadbalancer.server.port"] != "" {
			// The stack's routed service, every replica of it.
			stacks[m[1]] = append(stacks[m[1]], cand)
		}
	}
	want := map[string]bool{}
	var errs []error
	write := func(id8 string, cands []candidate) {
		want[id8] = true
		if busy8[id8] {
			return
		}
		sort.Slice(cands, func(i, j int) bool { return cands[i].name < cands[j].name })
		hosts := make([]string, 0, len(cands))
		for _, c := range cands {
			hosts = append(hosts, c.name)
		}
		f, err := rollout.RoutesFor(id8, cands[0].labels, hosts)
		if err == nil && f == nil {
			delete(want, id8)
			return
		}
		if err == nil {
			_, err = r.write(id8, f)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id8, err))
		}
	}
	for id, cands := range slots {
		// Two slots run outside a rollout when a switch couldn't retire the
		// previous one: the desired state's active deployment serves.
		if dep := active[id]; dep != "" && len(cands) > 1 {
			if i := slices.IndexFunc(cands, func(c candidate) bool { return c.deployment == dep }); i >= 0 {
				cands = cands[i : i+1]
			}
		}
		write(short(id), cands)
	}
	for id8, cands := range stacks {
		write(id8, cands)
	}
	entries, err := os.ReadDir(routesDir)
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !(strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")) {
			continue
		}
		path := filepath.Join(routesDir, name)
		id8, ours := strings.CutSuffix(strings.TrimPrefix(name, "paas-"), ".yml")
		ours = ours && strings.HasPrefix(name, "paas-")
		if ours && !want[id8] && !busy8[id8] {
			if err := os.Remove(path); err != nil {
				errs = append(errs, err)
			} else {
				slog.Info("removed the routes of a service with no running container", "file", name)
			}
			continue
		}
		if raw, err := os.ReadFile(path); err == nil && !parses(raw, ours) {
			slog.Error("set aside a routes file Traefik can't parse: it would keep every route here from changing", "file", name)
			if err := os.Rename(path, path+".broken"); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return len(want), errors.Join(errs...)
}

// count is how many services have a route file.
func (r *routes) count() int {
	entries, _ := os.ReadDir(routesDir)
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "paas-") && strings.HasSuffix(e.Name(), ".yml") {
			n++
		}
	}
	return n
}

func parses(raw []byte, ours bool) bool {
	if ours {
		_, err := rollout.ParseRouteFile(raw)
		return err == nil
	}
	var v any
	return yaml.Unmarshal(raw, &v) == nil
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// write puts f in place as id8's file, unless it already says that, and
// returns what the file held before (nil: none).
func (r *routes) write(id8 string, f *rollout.RouteFile) (prev []byte, err error) {
	raw, err := f.Marshal()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(routesDir, rollout.RouteFileName(id8))
	prev, _ = os.ReadFile(path)
	if bytes.Equal(prev, raw) {
		return prev, nil
	}
	if err := writeFileAtomic(path, raw, 0o600); err != nil {
		return prev, fmt.Errorf("write the routes: %w", err)
	}
	return prev, nil
}

// route is a rollout's switch (rollout.Hooks.Route): it writes the
// service's file for newSlot alone and waits for Traefik to serve it there.
// When Traefik doesn't, the previous file is put back.
func (r *routes) route(ctx context.Context, api, serviceID, newSlot string, logFn func(string)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	info, err := r.dc.ContainerInspect(ctx, newSlot)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", newSlot, err)
	}
	id8 := short(serviceID)
	path := filepath.Join(routesDir, rollout.RouteFileName(id8))
	f, err := rollout.RoutesFor(id8, info.Config.Labels, []string{newSlot})
	if err != nil {
		return err
	}
	if f == nil { // no domains: nothing routes it
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	prev, err := r.write(id8, f)
	if err != nil {
		return err
	}
	logFn("→ Traefik routes to " + newSlot + " now; waiting for it to serve there before the previous container stops")
	started := time.Now()
	if err := r.served(ctx, api, f); err != nil {
		if prev != nil {
			_ = writeFileAtomic(path, prev, 0o600)
		} else {
			_ = os.Remove(path)
		}
		return err
	}
	logFn(fmt.Sprintf("✓ Traefik serves %s (%.1fs)", newSlot, time.Since(started).Seconds()))
	return nil
}

// apiState is what Traefik's API says of a router or service.
type apiState struct {
	Status       string   `json:"status"`
	Error        []string `json:"error"`
	LoadBalancer struct {
		Servers []struct {
			URL string `json:"url"`
		} `json:"servers"`
	} `json:"loadBalancer"`
}

// served waits until Traefik serves f: its service's servers are f's and
// every router is enabled. A router Traefik disabled fails at once.
func (r *routes) served(ctx context.Context, api string, f *rollout.RouteFile) error {
	secret, err := os.ReadFile(apiSecretFile())
	if err != nil {
		return fmt.Errorf("Traefik's API password: %w", err)
	}
	get := func(path string, v *apiState) (int, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+path, nil)
		if err != nil {
			return 0, err
		}
		req.SetBasicAuth(agentAPIUser, strings.TrimSpace(string(secret)))
		res, err := r.http.Do(req)
		if err != nil {
			return 0, err
		}
		defer res.Body.Close()
		if res.StatusCode == http.StatusOK {
			return res.StatusCode, json.NewDecoder(res.Body).Decode(v)
		}
		return res.StatusCode, nil
	}
	deadline := time.Now().Add(routeWait)
	var last error
	for time.Now().Before(deadline) {
		last = func() error {
			for name, svc := range f.HTTP.Services {
				var st apiState
				code, err := get("/api/http/services/"+name+"@file", &st)
				if err != nil {
					return err
				}
				if code != http.StatusOK {
					return fmt.Errorf("Traefik has no service %s yet (%d)", name, code)
				}
				got := make([]string, 0, len(st.LoadBalancer.Servers))
				for _, s := range st.LoadBalancer.Servers {
					got = append(got, s.URL)
				}
				wantURLs := make([]string, 0, len(svc.LoadBalancer.Servers))
				for _, s := range svc.LoadBalancer.Servers {
					wantURLs = append(wantURLs, s.URL)
				}
				sort.Strings(got)
				sort.Strings(wantURLs)
				if !slices.Equal(got, wantURLs) {
					return fmt.Errorf("Traefik still sends %s to %s", name, strings.Join(got, ", "))
				}
			}
			for name := range f.HTTP.Routers {
				var st apiState
				code, err := get("/api/http/routers/"+name+"@file", &st)
				if err != nil {
					return err
				}
				if code != http.StatusOK {
					return fmt.Errorf("Traefik has no router %s yet (%d)", name, code)
				}
				if st.Status != "enabled" {
					return &routerError{name: name, errs: st.Error}
				}
			}
			return nil
		}()
		var re *routerError
		if last == nil || errors.As(last, &re) {
			return last
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("Traefik didn't serve the new routes within %s: %w", routeWait, last)
}

type routerError struct {
	name string
	errs []string
}

func (e *routerError) Error() string {
	return fmt.Sprintf("Traefik disabled router %s: %s", e.name, strings.Join(e.errs, "; "))
}

// liveAPI returns Traefik's API address when the server's Traefik reads
// the routes directory, or "" while it routes from labels.
func (r *routes) liveAPI(ctx context.Context) string {
	info, err := r.dc.ContainerInspect(ctx, traefikContainer)
	if err != nil || info.State == nil || !info.State.Running || info.NetworkSettings == nil {
		return ""
	}
	if !slices.ContainsFunc(info.Args, func(a string) bool { return strings.HasPrefix(a, traefikFileFlag) }) {
		return ""
	}
	// The port Docker bound: Toft asks for any (-p 127.0.0.1::8082), so
	// HostConfig's binding has none.
	for _, b := range info.NetworkSettings.Ports[traefikAPIPort] {
		if b.HostIP == "127.0.0.1" && b.HostPort != "" {
			return "http://127.0.0.1:" + b.HostPort
		}
	}
	return ""
}

// ensureAPIRoute writes toft-agent.yml, the router to Traefik's API behind
// a password only the agent holds: the entry point is published on the
// server's loopback, but app containers on Traefik's networks reach it too.
func (r *routes) ensureAPIRoute() error {
	secret, err := os.ReadFile(apiSecretFile())
	if err != nil {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		secret = []byte(hex.EncodeToString(b))
		if err := writeFileAtomic(apiSecretFile(), secret, 0o600); err != nil {
			return fmt.Errorf("keep Traefik's API password: %w", err)
		}
	}
	secret = bytes.TrimSpace(secret)
	path := filepath.Join(routesDir, agentRoutesFile)
	if raw, err := os.ReadFile(path); err == nil {
		var cur agentRoutes
		if yaml.Unmarshal(raw, &cur) == nil {
			if users := cur.HTTP.Middlewares["toft-agent-api-auth"].BasicAuth.Users; len(users) == 1 {
				if _, hash, ok := strings.Cut(users[0], ":"); ok && bcrypt.CompareHashAndPassword([]byte(hash), secret) == nil {
					return nil
				}
			}
		}
	}
	var f agentRoutes
	f.HTTP.Routers = map[string]agentRouter{"toft-agent-api": {
		Rule: "PathPrefix(`/api`)", EntryPoints: []string{"toft-api"}, Service: "api@internal", Middlewares: []string{"toft-agent-api-auth"},
	}}
	f.HTTP.Middlewares = map[string]agentMiddleware{"toft-agent-api-auth": {}}
	m := f.HTTP.Middlewares["toft-agent-api-auth"]
	m.BasicAuth.Users = []string{rollout.HtpasswdEntry(agentAPIUser, string(secret))}
	f.HTTP.Middlewares["toft-agent-api-auth"] = m
	raw, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, raw, 0o600)
}

type agentRoutes struct {
	HTTP struct {
		Routers     map[string]agentRouter     `yaml:"routers"`
		Middlewares map[string]agentMiddleware `yaml:"middlewares"`
	} `yaml:"http"`
}

type agentRouter struct {
	Rule        string   `yaml:"rule"`
	EntryPoints []string `yaml:"entryPoints"`
	Service     string   `yaml:"service"`
	Middlewares []string `yaml:"middlewares"`
}

type agentMiddleware struct {
	BasicAuth struct {
		Users []string `yaml:"users"`
	} `yaml:"basicAuth"`
}
