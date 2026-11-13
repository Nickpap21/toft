// Package fakedocker is the slice of the Docker Engine API the panel and the
// agent use, in memory, for tests.
package fakedocker

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/stretchr/testify/require"
)

// APIVersion is the API version the test clients speak, so they
// never negotiate.
const APIVersion = "1.47"

var fakeAPIPrefix = regexp.MustCompile(`^/v1\.[0-9]+`)

// Container is a container in a Docker.
type Container struct {
	ID     string
	Name   string
	Image  string
	Labels map[string]string
	Env    []string
	// Entrypoint and Cmd are what create was sent, nil when unset.
	Entrypoint []string
	Cmd        []string
	State      container.ContainerState
	Created    int64
	// Networks maps the networks the container is on to its aliases there.
	Networks map[string][]string
	// Mounts are the mounts it was created with.
	Mounts []mount.Mount
	// SecurityOpt, CapDrop and PidsLimit are the hardened runtime's (D-93).
	SecurityOpt []string
	CapDrop     []string
	PidsLimit   *int64
	// Runtime is a Sandboxed service's (D-95); "" is Docker's default.
	Runtime string
	// LogConfig is the log driver it was created with, as inspect reports.
	LogConfig container.LogConfig
	// Logs, when set, are its log lines, each "<RFC 3339 timestamp> <text>":
	// /logs then serves them with since, timestamps and follow (until the
	// container stops or goes) as Docker does. Without them /logs answers
	// one "ran <name>" line. Change them under Mu, or with Log.
	Logs []string
	// ExitCode and RestartCount are what inspect reports.
	ExitCode     int
	RestartCount int
	// Args and HostPorts (a port such as "8082/tcp" to the host's
	// "ip:port" it is published on) are what inspect reports too: the
	// port as bound under NetworkSettings, and under HostConfig as asked
	// for, with Docker to pick the host's port (Toft's Traefik API).
	Args      []string
	HostPorts map[string]string
	// PortBindings is what create asked to publish (the catalog Loki's
	// 127.0.0.1 port, D-148).
	PortBindings nat.PortMap
	// ListensFrom is the exec of the probe's nc, counted from 1, from
	// which something accepts connections on the container's port; 0:
	// never. Probes counts those execs. ExitsAfter is the exec after
	// which it exits with code 1; 0: never.
	ListensFrom int
	ExitsAfter  int
	Probes      int
	// Healthcheck and Tmpfs are what create was sent. Health is what
	// inspect reports while it runs: a container with a health check
	// starts healthy, an exec that touches the drain flag
	// (/.toft-drain/now) makes it unhealthy, as Docker's next check would,
	// and one that removes it healthy again.
	Healthcheck *container.HealthConfig
	Tmpfs       map[string]string
	Health      container.HealthStatus
}

// Network is a network in a Docker.
type Network struct {
	ID      string
	Name    string
	Driver  string
	Labels  map[string]string
	Members map[string]bool // container IDs
	Created time.Time
}

// Docker is the slice of the Docker Engine API the slot, inspection,
// stats and ephemeral-run helpers use (list, create, start, stop, remove,
// wait for and read the logs of containers; one-shot container stats; create
// volumes), in memory, so they can be tested without a daemon.
type Docker struct {
	t   *testing.T
	Srv *httptest.Server

	Mu         sync.Mutex
	Containers []*Container
	Ops        []string // "create <name>", "start <name>", "stop <name>", "remove <name>"
	created    map[string]map[string]string
	nextID     int
	clock      int64
	StartErr   bool // fail every ContainerStart
	ExitCode   int  // what ContainerWait reports

	// imageLabels are the labels of images by reference; an image not in
	// it has none (every image exists).
	imageLabels map[string]map[string]string
	// imageHealth are images' own HEALTHCHECKs by reference.
	imageHealth map[string]*container.HealthConfig

	networks     map[string]*Network
	NetCreateErr string // fail every NetworkCreate with this message
	// netCreateRace makes NetworkCreate fail after creating the network,
	// as when another creator wins.
	NetCreateRace bool
	ConnectErr    bool // NetworkConnect connects, then reports an error

	// One-shot stats (GET /containers/{id}/stats), served without holding
	// mu so concurrent requests overlap: samples by container name,
	// statsDelay per request, the names asked for, and the most requests
	// seen in flight at once.
	samples       map[string]Sample
	StatsErr      bool // fail every stats request
	PingDown      bool // Docker not answering yet: /_ping fails
	StatsDelay    time.Duration
	statsCalls    []string
	statsInFlight int
	statsMax      int

	// Execs (POST /containers/{id}/exec, then an attached /exec/{id}/start
	// and /exec/{id}/json), by ID: their exit codes. noProbe makes every
	// exec exit 126, as in a container without the probe mount.
	execs   map[string]int
	NoProbe bool
}

// Sample is what the stats endpoint reports for a container.
type Sample struct {
	CPUPercent float64 // of one CPU
	MemoryMB   float64
}

func New(t *testing.T) *Docker {
	t.Helper()
	f := &Docker{t: t, created: map[string]map[string]string{}, clock: 1_700_000_000}
	f.Srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Srv.Close)
	return f
}

// client returns a new Docker client for f. The caller may Close it.
func (f *Docker) Client() *client.Client {
	f.t.Helper()
	dc, err := client.NewClientWithOpts(
		client.WithHost("tcp://"+f.Srv.Listener.Addr().String()),
		client.WithVersion(APIVersion),
	)
	require.NoError(f.t, err)
	return dc
}

// add puts a container in f and returns it.
func (f *Docker) Add(name string, state container.ContainerState, labels map[string]string) *Container {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	return f.addLocked(name, "img", state, labels)
}

func (f *Docker) addLocked(name, image string, state container.ContainerState, labels map[string]string) *Container {
	f.nextID++
	f.clock += 10
	c := &Container{
		ID:      fmt.Sprintf("%064x", f.nextID),
		Name:    name,
		Image:   image,
		Labels:  labels,
		State:   state,
		Created: f.clock,
	}
	f.Containers = append(f.Containers, c)
	return c
}

// byName returns the container named name, or nil.
func (f *Docker) ByName(name string) *Container {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	for _, c := range f.Containers {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// addNetwork puts a network in f.
func (f *Docker) AddNetwork(name string, labels map[string]string) *Network {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	return f.addNetworkLocked(name, "bridge", labels)
}

func (f *Docker) addNetworkLocked(name, driver string, labels map[string]string) *Network {
	if f.networks == nil {
		f.networks = map[string]*Network{}
	}
	f.nextID++
	// Networks added by tests are old; NetworkCreate makes new ones.
	n := &Network{ID: fmt.Sprintf("net%061x", f.nextID), Name: name, Driver: driver, Labels: labels, Members: map[string]bool{}, Created: time.Now().Add(-time.Hour)}
	f.networks[name] = n
	return n
}

// attach puts container c on network name (which must exist) with aliases.
func (f *Docker) Attach(c *Container, name string, aliases ...string) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	f.attachLocked(c, name, aliases)
}

func (f *Docker) attachLocked(c *Container, name string, aliases []string) {
	if c.Networks == nil {
		c.Networks = map[string][]string{}
	}
	c.Networks[name] = aliases
	if n := f.networks[name]; n != nil {
		n.Members[c.ID] = true
	}
}

// networksOf returns the networks the container named name is on, sorted.
func (f *Docker) NetworksOf(name string) []string {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	for _, c := range f.Containers {
		if c.Name == name {
			out := make([]string, 0, len(c.Networks))
			for n := range c.Networks {
				out = append(out, n)
			}
			sort.Strings(out)
			return out
		}
	}
	return nil
}

// aliasesOn returns the aliases of the container named name on network.
func (f *Docker) AliasesOn(name, network string) []string {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	for _, c := range f.Containers {
		if c.Name == name {
			return c.Networks[network]
		}
	}
	return nil
}

func (f *Docker) Network(name string) *Network {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	return f.networks[name]
}

var (
	fakeNetworkPath = regexp.MustCompile(`^/networks/([^/]+)(/connect|/disconnect)?$`)
	fakeInspectPath = regexp.MustCompile(`^/containers/([^/]+)/json$`)
)

func (f *Docker) netByIDOrName(idOrName string) *Network {
	for _, n := range f.networks {
		if n.ID == idOrName || n.Name == idOrName {
			return n
		}
	}
	return nil
}

func (f *Docker) inspectNetwork(n *Network) map[string]any {
	// As Docker: only running containers have endpoints listed.
	members := map[string]any{}
	for id := range n.Members {
		if c := f.findLocked(id); c != nil && c.State == container.StateRunning {
			members[id] = map[string]string{"Name": c.Name}
		}
	}
	return map[string]any{"Name": n.Name, "Id": n.ID, "Driver": n.Driver, "Labels": n.Labels, "Containers": members, "Created": n.Created}
}

// serveNetworks answers the network endpoints; it reports whether it did.
func (f *Docker) serveNetworks(w http.ResponseWriter, r *http.Request, path string) bool {
	switch {
	case r.Method == http.MethodGet && path == "/networks":
		args, err := filters.FromJSON(r.URL.Query().Get("filters"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
			return true
		}
		out := []map[string]any{}
		for _, n := range f.networks {
			ok := true
			for _, l := range args.Get("label") {
				k, v, hasValue := strings.Cut(l, "=")
				got, present := n.Labels[k]
				if !present || (hasValue && got != v) {
					ok = false
				}
			}
			if ok {
				out = append(out, f.inspectNetwork(n))
			}
		}
		writeJSON(w, http.StatusOK, out)
		return true
	case r.Method == http.MethodPost && path == "/networks/create":
		var body struct {
			Name   string
			Driver string
			Labels map[string]string
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
			return true
		}
		if f.NetCreateErr != "" {
			writeJSON(w, http.StatusForbidden, map[string]string{"message": f.NetCreateErr})
			return true
		}
		if f.networks[body.Name] != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"message": "network with name " + body.Name + " already exists"})
			return true
		}
		n := f.addNetworkLocked(body.Name, body.Driver, body.Labels)
		n.Created = time.Now()
		f.Ops = append(f.Ops, "netcreate "+body.Name)
		if f.NetCreateRace {
			writeJSON(w, http.StatusConflict, map[string]string{"message": "network with name " + body.Name + " already exists"})
			return true
		}
		writeJSON(w, http.StatusCreated, map[string]string{"Id": n.ID})
		return true
	}
	m := fakeNetworkPath.FindStringSubmatch(path)
	if m == nil {
		return false
	}
	n := f.netByIDOrName(m[1])
	if n == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "network " + m[1] + " not found"})
		return true
	}
	switch {
	case r.Method == http.MethodGet && m[2] == "":
		writeJSON(w, http.StatusOK, f.inspectNetwork(n))
	case r.Method == http.MethodDelete && m[2] == "":
		f.Ops = append(f.Ops, "netremove "+n.Name)
		delete(f.networks, n.Name)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && (m[2] == "/connect" || m[2] == "/disconnect"):
		var body struct {
			Container      string
			EndpointConfig struct{ Aliases []string }
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
			return true
		}
		c := f.findLocked(body.Container)
		if c == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such container: " + body.Container})
			return true
		}
		if m[2] == "/connect" {
			if _, on := c.Networks[n.Name]; on {
				writeJSON(w, http.StatusForbidden, map[string]string{"message": "endpoint with name " + c.Name + " already exists in network " + n.Name})
				return true
			}
			f.Ops = append(f.Ops, "connect "+n.Name+" "+c.Name)
			f.attachLocked(c, n.Name, body.EndpointConfig.Aliases)
			if f.ConnectErr {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "timeout"})
				return true
			}
		} else {
			if _, on := c.Networks[n.Name]; !on {
				writeJSON(w, http.StatusForbidden, map[string]string{"message": "container " + c.ID + " is not connected to network " + n.Name})
				return true
			}
			f.Ops = append(f.Ops, "disconnect "+n.Name+" "+c.Name)
			delete(c.Networks, n.Name)
			delete(n.Members, c.ID)
		}
		w.WriteHeader(http.StatusOK)
	default:
		f.t.Errorf("fake docker: unexpected %s %s", r.Method, r.URL.Path)
		writeJSON(w, http.StatusNotImplemented, map[string]string{"message": "not implemented"})
	}
	return true
}

// inspectContainer answers GET /containers/{id}/json.
func (f *Docker) inspectContainer(w http.ResponseWriter, idOrName string) {
	c := f.findLocked(idOrName)
	if c == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such container: " + idOrName})
		return
	}
	nets := map[string]any{}
	for n, aliases := range c.Networks {
		nets[n] = map[string]any{"Aliases": aliases}
	}
	ports, asked := map[string]any{}, map[string]any{}
	for p, hp := range c.HostPorts {
		ip, port, _ := strings.Cut(hp, ":")
		ports[p] = []map[string]string{{"HostIp": ip, "HostPort": port}}
		asked[p] = []map[string]string{{"HostIp": ip, "HostPort": ""}}
	}
	state := map[string]any{"Status": string(c.State), "Running": c.State == container.StateRunning, "ExitCode": c.ExitCode}
	if c.Healthcheck != nil && c.State == container.StateRunning {
		state["Health"] = map[string]any{"Status": c.Health}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"Id":              c.ID,
		"Name":            "/" + c.Name,
		"Args":            c.Args,
		"HostConfig":      map[string]any{"PortBindings": asked, "LogConfig": c.LogConfig, "Tmpfs": c.Tmpfs},
		"State":           state,
		"RestartCount":    c.RestartCount,
		"Config":          map[string]any{"Labels": c.Labels, "Image": c.Image, "Healthcheck": c.Healthcheck},
		"NetworkSettings": map[string]any{"Networks": nets, "Ports": ports},
	})
}

// names returns the names of f's containers in creation order.
func (f *Docker) Names() []string {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	out := make([]string, 0, len(f.Containers))
	for _, c := range f.Containers {
		out = append(out, c.Name)
	}
	return out
}

// recordedOps returns the mutating calls f received, in order.
func (f *Docker) RecordedOps() []string {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	return append([]string(nil), f.Ops...)
}

// labelsOf returns the labels the container named name was created with.
func (f *Docker) LabelsOf(name string) map[string]string {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	return f.created[name]
}

var fakeImagePath = regexp.MustCompile(`^/images/(.+)/json$`)

// setImageLabels gives the image ref the labels a container created from
// it inherits.
func (f *Docker) SetImageLabels(ref string, labels map[string]string) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	if f.imageLabels == nil {
		f.imageLabels = map[string]map[string]string{}
	}
	f.imageLabels[ref] = labels
}

// SetImageHealthcheck gives the image ref a HEALTHCHECK of its own.
func (f *Docker) SetImageHealthcheck(ref string, hc *container.HealthConfig) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	if f.imageHealth == nil {
		f.imageHealth = map[string]*container.HealthConfig{}
	}
	f.imageHealth[ref] = hc
}

var (
	fakeContainerPath = regexp.MustCompile(`^/containers/([^/]+)(/start|/stop|/logs|/wait|/exec)?$`)
	fakeExecPath      = regexp.MustCompile(`^/exec/([^/]+)/(start|json)$`)
)

// sample sets what the stats endpoint reports for the container named name.
func (f *Docker) Sample(name string, cpuPercent, memoryMB float64) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	if f.samples == nil {
		f.samples = map[string]Sample{}
	}
	f.samples[name] = Sample{CPUPercent: cpuPercent, MemoryMB: memoryMB}
}

// sampledNames returns the containers whose stats were asked for, sorted.
func (f *Docker) SampledNames() []string {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	out := append([]string(nil), f.statsCalls...)
	sort.Strings(out)
	return out
}

func (f *Docker) MaxStatsInFlight() int {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	return f.statsMax
}

var fakeStatsPath = regexp.MustCompile(`^/containers/([^/]+)/stats$`)

// serveStats answers a one-shot stats request the way Docker does: a
// current sample with the previous one in precpu_stats, so the CPU share
// is the delta between them.
func (f *Docker) serveStats(w http.ResponseWriter, idOrName string) {
	f.Mu.Lock()
	c := f.findLocked(idOrName)
	if c == nil || c.State != container.StateRunning {
		f.Mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such container: " + idOrName})
		return
	}
	if f.StatsErr {
		f.Mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "cgroup read failed"})
		return
	}
	s := f.samples[c.Name]
	f.statsCalls = append(f.statsCalls, c.Name)
	f.statsInFlight++
	if f.statsInFlight > f.statsMax {
		f.statsMax = f.statsInFlight
	}
	delay := f.StatsDelay
	f.Mu.Unlock()

	time.Sleep(delay)

	f.Mu.Lock()
	f.statsInFlight--
	f.Mu.Unlock()

	const system = 1_000_000_000
	writeJSON(w, http.StatusOK, map[string]any{
		"cpu_stats": map[string]any{
			"cpu_usage":        map[string]any{"total_usage": uint64(s.CPUPercent / 100 * system)},
			"system_cpu_usage": uint64(2 * system),
			"online_cpus":      1,
		},
		"precpu_stats": map[string]any{
			"cpu_usage":        map[string]any{"total_usage": 0},
			"system_cpu_usage": uint64(system),
		},
		"memory_stats": map[string]any{"usage": uint64(s.MemoryMB * 1024 * 1024), "limit": uint64(2048 * 1024 * 1024)},
	})
}

func (f *Docker) serve(w http.ResponseWriter, r *http.Request) {
	path := fakeAPIPrefix.ReplaceAllString(r.URL.Path, "") // any version: probe_test speaks 1.48
	if m := fakeStatsPath.FindStringSubmatch(path); m != nil && r.Method == http.MethodGet {
		f.serveStats(w, m[1])
		return
	}
	if m := fakeContainerPath.FindStringSubmatch(path); m != nil && m[2] == "/logs" && r.Method == http.MethodGet {
		f.serveLogs(w, r, m[1])
		return
	}
	f.Mu.Lock()
	defer f.Mu.Unlock()

	if f.serveNetworks(w, r, path) {
		return
	}
	if m := fakeExecPath.FindStringSubmatch(path); m != nil {
		f.serveExec(w, r, m[1], m[2])
		return
	}
	if m := fakeInspectPath.FindStringSubmatch(path); m != nil && r.Method == http.MethodGet && m[1] != "json" {
		f.inspectContainer(w, m[1])
		return
	}
	switch {
	case path == "/_ping" && f.PingDown:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"message": "not started"})
	case path == "/_ping":
		_, _ = io.WriteString(w, "OK")
	case r.Method == http.MethodGet && path == "/containers/json":
		f.list(w, r)
	case r.Method == http.MethodPost && path == "/containers/create":
		f.create(w, r)
	case r.Method == http.MethodPost && path == "/volumes/create":
		var body struct{ Name string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.Ops = append(f.Ops, "volume create "+body.Name) // serve holds f.Mu
		writeJSON(w, http.StatusCreated, map[string]string{"Name": body.Name})
	case r.Method == http.MethodGet && fakeImagePath.MatchString(path):
		ref := fakeImagePath.FindStringSubmatch(path)[1]
		writeJSON(w, http.StatusOK, map[string]any{"Id": "sha256:" + ref, "Config": map[string]any{"Labels": f.imageLabels[ref], "Healthcheck": f.imageHealth[ref]}})
	default:
		m := fakeContainerPath.FindStringSubmatch(path)
		if m == nil {
			f.t.Errorf("fake docker: unexpected %s %s", r.Method, r.URL.Path)
			writeJSON(w, http.StatusNotImplemented, map[string]string{"message": "not implemented"})
			return
		}
		c := f.findLocked(m[1])
		if c == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such container: " + m[1]})
			return
		}
		switch {
		case r.Method == http.MethodPost && m[2] == "/start":
			if f.StartErr {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "cannot start"})
				return
			}
			f.Ops = append(f.Ops, "start "+c.Name)
			c.State = container.StateRunning
			if c.Healthcheck != nil && c.Health == "" {
				c.Health = container.Healthy
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && m[2] == "/exec":
			if c.State != container.StateRunning {
				writeJSON(w, http.StatusConflict, map[string]string{"message": "container " + c.ID + " is not running"})
				return
			}
			var body struct{ Cmd []string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			code := 1
			if slices.Contains(body.Cmd, "/.toft-drain/now") {
				if slices.Contains(body.Cmd, "touch") {
					f.Ops = append(f.Ops, "drain "+c.Name)
					c.Health = container.Unhealthy
				} else {
					f.Ops = append(f.Ops, "undrain "+c.Name)
					c.Health = container.Healthy
				}
				code = 0
				if f.execs == nil {
					f.execs = map[string]int{}
				}
				id := fmt.Sprintf("exec%d", len(f.execs)+1)
				f.execs[id] = code
				writeJSON(w, http.StatusCreated, map[string]string{"Id": id})
				return
			}
			c.Probes++
			switch {
			case f.NoProbe:
				code = 126
			case c.ListensFrom > 0 && c.Probes >= c.ListensFrom:
				code = 0
			}
			if f.execs == nil {
				f.execs = map[string]int{}
			}
			id := fmt.Sprintf("exec%d", len(f.execs)+1)
			f.execs[id] = code
			if c.ExitsAfter > 0 && c.Probes >= c.ExitsAfter {
				c.State, c.ExitCode = container.StateExited, 1
			}
			writeJSON(w, http.StatusCreated, map[string]string{"Id": id})
		case r.Method == http.MethodPost && m[2] == "/wait":
			writeJSON(w, http.StatusOK, map[string]any{"StatusCode": f.ExitCode})
		case r.Method == http.MethodPost && m[2] == "/stop":
			f.Ops = append(f.Ops, "stop "+c.Name)
			c.State = container.StateExited
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && m[2] == "":
			f.Ops = append(f.Ops, "remove "+c.Name)
			for i, x := range f.Containers {
				if x == c {
					f.Containers = append(f.Containers[:i], f.Containers[i+1:]...)
					break
				}
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			f.t.Errorf("fake docker: unexpected %s %s", r.Method, r.URL.Path)
			writeJSON(w, http.StatusNotImplemented, map[string]string{"message": "not implemented"})
		}
	}
}

// Log appends lines ("<RFC 3339 timestamp> <text>") to the logs of the
// container named name.
func (f *Docker) Log(name string, lines ...string) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	c := f.findLocked(name)
	c.Logs = append(c.Logs, lines...)
}

// serveLogs answers GET /containers/{id}/logs in the multiplexed stream
// format (stdcopy), one stdout frame per line, without holding Mu while it
// follows.
func (f *Docker) serveLogs(w http.ResponseWriter, r *http.Request, idOrName string) {
	frame := func(line string) {
		header := make([]byte, 8, 8+len(line))
		header[0] = 1
		binary.BigEndian.PutUint32(header[4:], uint32(len(line)))
		_, _ = w.Write(append(header, line...))
	}
	f.Mu.Lock()
	c := f.findLocked(idOrName)
	if c == nil {
		f.Mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such container: " + idOrName})
		return
	}
	if c.Logs == nil {
		f.Mu.Unlock()
		w.WriteHeader(http.StatusOK)
		frame("ran " + c.Name + "\n")
		return
	}
	f.Mu.Unlock()
	q := r.URL.Query()
	var since time.Time
	if v := q.Get("since"); v != "" {
		sec, nsec, _ := strings.Cut(v, ".")
		s, _ := strconv.ParseInt(sec, 10, 64)
		ns, _ := strconv.ParseInt(nsec, 10, 64)
		since = time.Unix(s, ns)
	}
	timestamps := q.Get("timestamps") == "1" || q.Get("timestamps") == "true"
	follow := q.Get("follow") == "1" || q.Get("follow") == "true"
	w.WriteHeader(http.StatusOK)
	for next := 0; ; {
		f.Mu.Lock()
		logs, running, gone := append([]string(nil), c.Logs...), c.State == container.StateRunning, f.findLocked(c.ID) == nil
		f.Mu.Unlock()
		for _, l := range logs[min(next, len(logs)):] {
			ts, text, _ := strings.Cut(l, " ")
			if t, err := time.Parse(time.RFC3339Nano, ts); err == nil && t.Before(since) {
				continue // Docker's since keeps lines at that very time
			}
			if !timestamps {
				l = text
			}
			frame(l + "\n")
		}
		next = len(logs)
		w.(http.Flusher).Flush()
		if gone || !running || !follow {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// serveExec answers an exec's attached start, which upgrades the connection
// and closes it as the exec ends (no output), and its inspect.
func (f *Docker) serveExec(w http.ResponseWriter, r *http.Request, id, op string) {
	code, ok := f.execs[id]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such exec instance: " + id})
		return
	}
	if op == "json" {
		writeJSON(w, http.StatusOK, map[string]any{"ID": id, "Running": false, "ExitCode": code})
		return
	}
	// Read the start's body first: Linux resets a connection closed with
	// unread bytes, and the client would see that instead of the exec's end.
	_, _ = io.Copy(io.Discard, r.Body)
	conn, buf, err := w.(http.Hijacker).Hijack()
	if err != nil {
		f.t.Errorf("fake docker: hijack: %v", err)
		return
	}
	_, _ = buf.WriteString("HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
	_ = buf.Flush()
	_ = conn.Close()
}

func (f *Docker) findLocked(idOrName string) *Container {
	for _, c := range f.Containers {
		if c.ID == idOrName || c.Name == idOrName {
			return c
		}
	}
	return nil
}

func (f *Docker) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	all := q.Get("all") == "1" || q.Get("all") == "true"
	args, err := filters.FromJSON(q.Get("filters"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
		return
	}
	out := []container.Summary{}
	for _, c := range f.Containers {
		if !all && c.State != container.StateRunning {
			continue
		}
		if !matchesFilters(c, args) {
			continue
		}
		nets := map[string]*network.EndpointSettings{}
		for n, aliases := range c.Networks {
			nets[n] = &network.EndpointSettings{Aliases: aliases}
		}
		out = append(out, container.Summary{
			ID:              c.ID,
			Names:           []string{"/" + c.Name},
			Image:           c.Image,
			Labels:          c.Labels,
			State:           c.State,
			Created:         c.Created,
			NetworkSettings: &container.NetworkSettingsSummary{Networks: nets},
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// matchesFilters applies the name (a regular expression, as Docker does)
// and label filters: values of one filter are ORed, filters are ANDed.
func matchesFilters(c *Container, args filters.Args) bool {
	if names := args.Get("name"); len(names) > 0 {
		ok := false
		for _, n := range names {
			if re, err := regexp.Compile(n); err == nil && re.MatchString("/"+c.Name) {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	if nets := args.Get("network"); len(nets) > 0 {
		ok := false
		for _, n := range nets {
			if _, on := c.Networks[n]; on {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	if labels := args.Get("label"); len(labels) > 0 {
		ok := false
		for _, l := range labels {
			k, v, hasValue := strings.Cut(l, "=")
			got, present := c.Labels[k]
			if present && (!hasValue || got == v) {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func (f *Docker) create(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	var body struct {
		Image       string
		Labels      map[string]string
		Env         []string
		Entrypoint  []string
		Cmd         []string
		Healthcheck *container.HealthConfig
		HostConfig  struct {
			NetworkMode  string
			Tmpfs        map[string]string
			Mounts       []mount.Mount
			SecurityOpt  []string
			CapDrop      []string
			PidsLimit    *int64
			Runtime      string
			LogConfig    container.LogConfig
			PortBindings nat.PortMap
		}
		NetworkingConfig struct {
			EndpointsConfig map[string]struct{ Aliases []string }
		}
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
		return
	}
	if name == "" {
		name = fmt.Sprintf("anon-%d", f.nextID+1) // Docker would generate one
	}
	if f.findLocked(name) != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"message": "Conflict. The container name " + name + " is already in use"})
		return
	}
	c := f.addLocked(name, body.Image, container.StateCreated, body.Labels)
	c.Env = body.Env
	c.Entrypoint, c.Cmd = body.Entrypoint, body.Cmd
	c.Mounts = body.HostConfig.Mounts
	c.SecurityOpt, c.CapDrop, c.PidsLimit = body.HostConfig.SecurityOpt, body.HostConfig.CapDrop, body.HostConfig.PidsLimit
	c.Runtime = body.HostConfig.Runtime
	c.LogConfig = body.HostConfig.LogConfig
	c.PortBindings = body.HostConfig.PortBindings
	c.Healthcheck, c.Tmpfs = body.Healthcheck, body.HostConfig.Tmpfs
	if mode := body.HostConfig.NetworkMode; mode != "" {
		f.attachLocked(c, mode, body.NetworkingConfig.EndpointsConfig[mode].Aliases)
	}
	f.created[name] = body.Labels
	f.Ops = append(f.Ops, "create "+name)
	writeJSON(w, http.StatusCreated, map[string]any{"Id": c.ID, "Warnings": []string{}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
