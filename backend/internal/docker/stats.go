package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/hoststats"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

// Both orchestrators sample every service on their host in one call for the
// metrics collector.
var (
	_ service.ServiceStatsCollector = (*LocalOrchestrator)(nil)
	_ service.ServiceStatsCollector = (*RemoteOrchestrator)(nil)
	_ service.AgentStatsReader      = (*RemoteOrchestrator)(nil)
)

// localStatsConcurrency bounds the one-shot stats requests in flight to the
// local Docker daemon; each takes about a second (the daemon samples CPU
// twice).
const localStatsConcurrency = 8

// hostContainer is a container and the labels that say which service, and
// which of its compose services, it belongs to.
type hostContainer struct {
	ID             string // full; "" when not listed
	Name           string
	ServiceID      string // paas.service_id
	ComposeProject string // com.docker.compose.project
	ComposeService string // com.docker.compose.service, as set: attribution checks it
	Image          string // as the local listing names it; "" elsewhere
}

// hostStatRow is a running container with its CPU and memory use and, on a
// remote server, its cgroup counters.
type hostStatRow struct {
	hostContainer
	Stats service.ContainerStats
}

// hostKill is a container killed for memory.
type hostKill struct {
	hostContainer
	At time.Time
}

// hostSample is a server's sample before attribution.
type hostSample struct {
	rows       []hostStatRow
	cpuWaitPct *float64 // the host's; nil when not read
	kills      []hostKill
	until      int64 // the kill window's end; 0 when the kills were not read
	latency    *hoststats.Latency
}

// CollectServiceStats samples the targets' containers on the local Docker
// daemon: one listing, then one-shot stats for the containers that belong to
// a target, at most localStatsConcurrency at a time. The control-plane
// server's cgroup counters and memory kills are not read (the backend runs
// in a container and does not see the host's cgroups), so since is unused.
func (o *LocalOrchestrator) CollectServiceStats(ctx context.Context, targets []service.MetricsTarget, _ int64) (service.HostSample, error) {
	s, err := collectLocalStats(ctx, o.dockerClient, targets)
	if err != nil || o.latencyURL == "" {
		return s, err
	}
	l, cause := readLocalLatency(ctx, o.latencyURL)
	if last := o.latencyCause.Swap(cause); cause != "" && last != cause && ctx.Err() == nil {
		o.log.Warn("metrics: the panel host's Traefik's latency could not be read", zap.String("url", o.latencyURL), zap.String("cause", cause))
	}
	s.Latency = attributeLatency(l, targets)
	return s, nil
}

// CollectServiceStats samples the targets' containers on the remote server,
// and reads their memory kills since since, with a single command over the
// server's pooled SSH connection; ctx ending stops it.
func (o *RemoteOrchestrator) CollectServiceStats(ctx context.Context, targets []service.MetricsTarget, since int64) (service.HostSample, error) {
	out, err := o.host.Exec(ctx, hoststats.Cmd(since))
	if err != nil {
		return service.HostSample{}, fmt.Errorf("remote stats: %w", err)
	}
	s, err := parseHostStats(out)
	if err != nil {
		return service.HostSample{}, err
	}
	return attributeSample(s, targets), nil
}

// KeepsStats reports whether the server's agent samples it itself and keeps
// the samples while the panel is away (D-113).
func (o *RemoteOrchestrator) KeepsStats() bool { return o.metrics }

// AgentServiceStats asks the server's agent for the samples it took after
// after (unix seconds on its clock), oldest first, and reads each as
// CollectServiceStats reads its own, at the time it was taken. The agent
// read each sample's kills from where its last one ended.
func (o *RemoteOrchestrator) AgentServiceStats(ctx context.Context, targets []service.MetricsTarget, after int64) ([]service.HostSample, error) {
	req, err := json.Marshal(agentproto.MetricsRequest{Since: after})
	if err != nil {
		return nil, fmt.Errorf("encode the metrics request: %w", err)
	}
	out := make([]service.HostSample, 0)
	refused := ""
	err = o.host.SubsystemStream(ctx, agentproto.SubsystemMetrics, bytes.NewReader(append(req, '\n')), func(line string) {
		if msg, ok := strings.CutPrefix(line, "error: "); ok {
			refused = msg
			return
		}
		var m agentproto.MetricsSample
		if json.Unmarshal([]byte(line), &m) != nil {
			return
		}
		s, err := parseHostStats(m.Out)
		if err != nil {
			return // no listing: not a sample the script printed
		}
		h := attributeSample(s, targets)
		h.At = time.Unix(m.At, 0).UTC()
		out = append(out, h)
	})
	if refused != "" {
		return nil, fmt.Errorf("the server's agent refused the metrics request: %s", refused)
	}
	if err != nil {
		return nil, fmt.Errorf("agent metrics: %w", err)
	}
	return out, nil
}

func collectLocalStats(ctx context.Context, dc *client.Client, targets []service.MetricsTarget) (service.HostSample, error) {
	list, err := dc.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return service.HostSample{}, fmt.Errorf("ContainerList: %w", err)
	}
	a := newAttributor(targets)

	var (
		mu       sync.Mutex
		rows     []hostStatRow
		sampled  int
		firstErr error
		g        errgroup.Group
	)
	g.SetLimit(localStatsConcurrency)
	for _, c := range list {
		hc := hostContainer{
			ID:             c.ID,
			Name:           rollout.SummaryName(c),
			ServiceID:      c.Labels[rollout.LabelServiceID],
			ComposeProject: c.Labels[rollout.LabelComposeProject],
			ComposeService: c.Labels[rollout.LabelComposeService],
			Image:          c.Image,
		}
		if a.owner(hc) == "" {
			continue // not a target's: never sampled
		}
		sampled++
		id := c.ID
		g.Go(func() error {
			st, err := containerStats(ctx, dc, id)
			var info container.InspectResponse
			var inspectErr error
			if err == nil {
				info, inspectErr = dc.ContainerInspect(ctx, id)
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				// The container may have stopped since the listing.
				if firstErr == nil {
					firstErr = err
				}
				return nil
			}
			row := service.ContainerStats{CPUPercent: st.CPUPercent, MemoryMB: st.MemoryMB}
			if inspectErr == nil { // else it stopped since: its restarts stay unread
				row.RestartCount, row.RestartsRead = info.RestartCount, true
			}
			rows = append(rows, hostStatRow{hostContainer: hc, Stats: row})
			return nil
		})
	}
	_ = g.Wait()
	if err := ctx.Err(); err != nil {
		return service.HostSample{}, err
	}
	if sampled > 0 && len(rows) == 0 {
		return service.HostSample{}, firstErr // every sample failed: the daemon, not a container
	}
	return attributeSample(hostSample{rows: rows}, targets), nil
}

// startedAt parses Docker's State.StartedAt; nil when the container never
// started (Docker writes the zero time) or the value isn't a time.
func startedAt(v string) *time.Time {
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil || t.IsZero() || t.Year() < 2000 {
		return nil
	}
	return &t
}

// containerStats returns a one-shot CPU and memory snapshot of a container.
func containerStats(ctx context.Context, dc *client.Client, nameOrID string) (*service.ContainerStats, error) {
	resp, err := dc.ContainerStats(ctx, nameOrID, false)
	if err != nil {
		return nil, fmt.Errorf("ContainerStats: %w", err)
	}
	defer resp.Body.Close()

	var raw struct {
		CPUStats struct {
			CPUUsage struct {
				TotalUsage uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
			SystemCPUUsage uint64 `json:"system_cpu_usage"`
			OnlineCPUs     int    `json:"online_cpus"`
		} `json:"cpu_stats"`
		PreCPUStats struct {
			CPUUsage struct {
				TotalUsage uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
			SystemCPUUsage uint64 `json:"system_cpu_usage"`
		} `json:"precpu_stats"`
		MemoryStats struct {
			Usage uint64 `json:"usage"`
			Limit uint64 `json:"limit"`
		} `json:"memory_stats"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode stats: %w", err)
	}

	cpuDelta := float64(raw.CPUStats.CPUUsage.TotalUsage - raw.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(raw.CPUStats.SystemCPUUsage - raw.PreCPUStats.SystemCPUUsage)
	numCPUs := raw.CPUStats.OnlineCPUs
	if numCPUs == 0 {
		numCPUs = 1
	}

	var cpuPercent float64
	if systemDelta > 0 && cpuDelta > 0 {
		cpuPercent = (cpuDelta / systemDelta) * float64(numCPUs) * 100.0
	}

	const mb = 1024 * 1024
	return &service.ContainerStats{
		CPUPercent:    round2(cpuPercent),
		MemoryMB:      round2(float64(raw.MemoryStats.Usage) / mb),
		MemoryLimitMB: round2(float64(raw.MemoryStats.Limit) / mb),
	}, nil
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// parseHostStats parses hoststats.Cmd's output. A section an older command
// did not print (counters, kills, restarts) reads as empty; only the listing and
// docker stats are required. Lines that do not parse, such as a CLI warning
// on stderr, are skipped: one bad line never fails the sample.
func parseHostStats(out string) (hostSample, error) {
	sections := [][]string{nil}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimRight(l, "\r")
		if strings.TrimSpace(l) == hoststats.Separator {
			sections = append(sections, nil)
			continue
		}
		sections[len(sections)-1] = append(sections[len(sections)-1], l)
	}
	if len(sections) < 2 {
		return hostSample{}, errors.New("unexpected docker stats output: no container listing")
	}
	latency := hoststats.Latency{Unmeasured: hoststats.UnmeasuredAgent} // an older agent's script
	if len(sections) >= 6 {
		latency = hoststats.ParseLatency(sections[5])
	}
	sections = append(sections, nil, nil, nil) // the sections older output lacks

	s := hostSample{rows: parseStatRows(sections[0], sections[1]), latency: &latency}
	s.cpuWaitPct = readCounters(s.rows, sections[2])
	s.kills, s.until = parseKills(sections[3])
	readRestarts(s.rows, sections[4])
	return s, nil
}

// readLocalLatency reads the panel host's Traefik's metrics page (url, on
// the control plane's network; its router lets only the control plane
// in). One that does not answer is not measured, and why is the cause ("" when
// it answered): it never fails the sample.
func readLocalLatency(ctx context.Context, url string) (*hoststats.Latency, string) {
	unmeasured := &hoststats.Latency{Unmeasured: hoststats.UnmeasuredTraefik}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return unmeasured, err.Error()
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return unmeasured, err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return unmeasured, fmt.Sprintf("status %d", resp.StatusCode)
	}
	// The page is tens of KB; 8 MiB bounds a misbehaving one.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return unmeasured, "read the page: " + err.Error()
	}
	l := hoststats.ParseLatency(strings.Split(string(body), "\n"))
	return &l, ""
}

// readRestarts puts each restart count line ("<id> <count>") on the row of
// the same container ID, as readCounters does. A row without one (an older
// command, or a container that stopped first) keeps it unread.
func readRestarts(rows []hostStatRow, lines []string) {
	byID := make(map[string]*service.ContainerStats, len(rows))
	for i := range rows {
		if rows[i].ID != "" {
			byID[rows[i].ID] = &rows[i].Stats
		}
	}
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) != 2 {
			continue
		}
		st, ok := byID[f[0]]
		n, err := strconv.Atoi(f[1])
		if !ok || err != nil || n < 0 {
			continue
		}
		st.RestartCount, st.RestartsRead = n, true
	}
}

// parseStatRows returns the running containers that have both a listing
// line and a sample (a container that started or stopped between the two
// commands is skipped, as is one sampled while stopping: "--").
func parseStatRows(listing, stats []string) []hostStatRow {
	var listed []hostContainer
	for _, l := range listing {
		if c, ok := parseListingLine(l); ok {
			listed = append(listed, c)
		}
	}

	type sample struct{ cpu, mem float64 }
	samples := make(map[string]sample)
	for _, l := range stats {
		fields := strings.Split(l, "\t")
		if len(fields) < 3 {
			continue
		}
		cpu, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(fields[1]), "%"), 64)
		if err != nil {
			continue
		}
		used, _, _ := strings.Cut(fields[2], "/")
		mem, err := parseDockerSize(strings.TrimSpace(used))
		if err != nil {
			continue
		}
		samples[strings.TrimSpace(fields[0])] = sample{cpu: cpu, mem: mem}
	}

	rows := make([]hostStatRow, 0, len(listed))
	for _, c := range listed {
		if s, ok := samples[c.Name]; ok {
			rows = append(rows, hostStatRow{hostContainer: c, Stats: service.ContainerStats{CPUPercent: s.cpu, MemoryMB: s.mem}})
		}
	}
	return rows
}

// readCounters puts each counter line on the row of the same container ID
// and returns the host's CPU pressure. Only IDs the listing gave (64 hex
// digits) match. A line for a container that is not a row (started after
// the listing) is ignored, a row without one (stopped since) keeps zero
// counters, and a value that is not a number ("-": the kernel has no such
// file) is left unset. Counters are kept per container, as read: never
// added up, so a restart shows as a counter that went down.
func readCounters(rows []hostStatRow, lines []string) (hostWait *float64) {
	byID := make(map[string]*service.ContainerStats, len(rows))
	for i := range rows {
		if rows[i].ID != "" {
			byID[rows[i].ID] = &rows[i].Stats
		}
	}
	for _, l := range lines {
		f := strings.Split(l, "\t")
		if len(f) == 2 && f[0] == "host" {
			hostWait = parsePressure(f[1])
			continue
		}
		st, ok := byID[f[0]]
		if len(f) != 4 || !ok {
			continue
		}
		st.NrPeriods, _ = strconv.ParseUint(f[1], 10, 64)
		st.NrThrottled, _ = strconv.ParseUint(f[2], 10, 64)
		st.CPUWaitPct = parsePressure(f[3])
	}
	return hostWait
}

// parsePressure parses a PSI avg60, a percentage; nil when it is not one.
func parsePressure(s string) *float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || !(v >= 0 && v <= 100) { // NaN fails both
		return nil
	}
	return &v
}

// parseKills parses the kill section: a listing line and the time per kill,
// then the window's end.
func parseKills(lines []string) (kills []hostKill, until int64) {
	for _, l := range lines {
		f := strings.Split(l, "\t")
		if len(f) == 2 && f[0] == "until" {
			if v, err := strconv.ParseInt(f[1], 10, 64); err == nil && v > 0 {
				until = v
			}
			continue
		}
		if len(f) != 6 {
			continue
		}
		c, ok := parseListingLine(strings.Join(f[:5], "\t"))
		at, err := strconv.ParseInt(f[5], 10, 64)
		if !ok || err != nil {
			continue
		}
		kills = append(kills, hostKill{hostContainer: c, At: time.Unix(at, 0).UTC()})
	}
	return kills, until
}

// parseListingLine parses a listing line: the names and two labels
// (hostStatesCmd), then the compose service label and the full ID
// (hoststats.Cmd), each a JSON string, separated by tabs. A label keeps its
// value only when it has the shape the platform gives it (a service ID is a
// UUID, a compose project a lowercase name), and the ID only when it is 64
// hex digits; any other value came from an image and could not belong to a
// target anyway. The compose service is kept as set: attribution checks it.
func parseListingLine(line string) (hostContainer, bool) {
	fields := strings.Split(line, "\t")
	if len(fields) != 3 && len(fields) != 5 {
		return hostContainer{}, false
	}
	values := make([]string, len(fields))
	for i, f := range fields {
		if err := json.Unmarshal([]byte(f), &values[i]); err != nil {
			return hostContainer{}, false
		}
	}
	name := firstContainerName(values[0])
	if name == "" {
		return hostContainer{}, false
	}
	c := hostContainer{Name: name}
	if serviceIDPattern.MatchString(values[1]) {
		c.ServiceID = values[1]
	}
	if composeProjectPattern.MatchString(values[2]) {
		c.ComposeProject = values[2]
	}
	if len(values) == 5 {
		c.ComposeService = values[3]
		if containerIDPattern.MatchString(values[4]) {
			c.ID = values[4]
		}
	}
	return c, true
}

var (
	serviceIDPattern      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	composeProjectPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	containerIDPattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// A build step's image, as the listing names it: an ID, short or full.
	imageIDPattern = regexp.MustCompile(`^(sha256:)?([0-9a-f]{12}|[0-9a-f]{64})$`)
)

// firstContainerName returns a container's own name from a docker ps Names
// column, which also lists legacy link aliases (other/alias), comma
// separated.
func firstContainerName(names string) string {
	for _, n := range strings.Split(strings.TrimSpace(names), ",") {
		if n = strings.TrimSpace(n); n != "" && !strings.Contains(n, "/") {
			return n
		}
	}
	return ""
}

// attributor decides which target service a running container belongs to.
type attributor struct {
	targets  map[string]bool   // target service IDs
	projects map[string]string // compose project name → target service ID, "" when two targets derive it
}

func newAttributor(targets []service.MetricsTarget) attributor {
	a := attributor{targets: make(map[string]bool), projects: make(map[string]string)}
	for _, t := range targets {
		if t.ID == "" {
			continue
		}
		a.targets[t.ID] = true
		for _, p := range rollout.ComposeProjectNames(t.ID, t.Name) {
			if owner, seen := a.projects[p]; seen && owner != t.ID {
				a.projects[p] = "" // ambiguous: it counts for neither
				continue
			}
			a.projects[p] = t.ID
		}
	}
	return a
}

// owner returns the ID of the target service c belongs to, or "". No label
// is taken at its word, because a container inherits every LABEL of its
// image and tenants choose their images:
//  1. paas.service_id counts only on a container the platform created and
//     labelled itself: a blue/green slot, recognised by its reserved name
//     (rollout.IsSlotName). Such a container is never counted through its compose
//     label, which its image may have set.
//  2. Otherwise a compose project counts only when it is a name the
//     platform derives from a target (rollout.ComposeProjectNames), which is the
//     project the platform runs that target's stack under; a project name
//     that two targets derive is ambiguous and counts for neither.
//
// So a compose service cannot claim another service's slot, nor have its
// own containers counted for another service by labelling them
// paas.service_id, and a slot's image cannot move it into a stack.
//
// The platform's other containers from a tenant's image (slots, one-offs)
// have their image's identity labels blanked (rollout.WithImageLabelsBlanked).
// A build step of the control plane's builder (Docker's classic one) keeps
// its base image's, so rule 2 skips a container created from a bare image
// ID, as every such step is and as an image cannot change; compose names its
// images. Remote servers build with BuildKit, whose steps are not
// containers. (BuildKit on the control plane would share RUN --mount caches
// across workspaces.)
func (a attributor) owner(c hostContainer) string {
	id, _ := a.attribute(c)
	return id
}

// attribute returns the target c belongs to (owner) and the compose service
// it counts under there: its com.docker.compose.service label, only when it
// belongs to the target through the platform-derived compose project and
// the label is a compose service name (ValidServiceName). Compose sets that
// label on the containers it creates, and the deploy policy refuses
// com.docker.compose.* labels in compose files. Otherwise "", as for a
// blue/green slot, whose image may set any label.
func (a attributor) attribute(c hostContainer) (serviceID, composeService string) {
	if c.ServiceID != "" && rollout.IsSlotName(c.Name, c.ServiceID) {
		if a.targets[c.ServiceID] {
			return c.ServiceID, ""
		}
		return "", ""
	}
	if c.ComposeProject == "" || imageIDPattern.MatchString(c.Image) {
		return "", ""
	}
	id := a.projects[c.ComposeProject]
	if id == "" || !composespec.ValidServiceName(c.ComposeService) {
		return id, ""
	}
	return id, c.ComposeService
}

// attributeSample keeps the rows and kills of target containers, each under
// its service and compose service (attribute). Containers of no target,
// such as Traefik, are ignored, and a target with no running container is
// left out.
func attributeSample(s hostSample, targets []service.MetricsTarget) service.HostSample {
	a := newAttributor(targets)
	out := service.HostSample{
		Containers: make(map[string]map[string][]service.ContainerStats),
		CPUWaitPct: s.cpuWaitPct,
		Until:      s.until,
	}
	for _, r := range s.rows {
		id, compose := a.attribute(r.hostContainer)
		if id == "" {
			continue
		}
		st := r.Stats
		st.ContainerID, st.ComposeService = r.ID, compose
		if out.Containers[id] == nil {
			out.Containers[id] = make(map[string][]service.ContainerStats)
		}
		out.Containers[id][compose] = append(out.Containers[id][compose], st)
	}
	for _, k := range s.kills {
		if id, compose := a.attribute(k.hostContainer); id != "" {
			out.OOMKills = append(out.OOMKills, service.OOMEvent{ServiceID: id, ComposeService: compose, Container: k.Name, At: k.At})
		}
	}
	out.Latency = attributeLatency(s.latency, targets)
	return out
}

// attributeLatency names the Traefik service each target routes to: the
// platform names it paas-svc-<the target's first 8 ID characters>
// (traefikServiceName). A name two targets share counts for neither; nil
// stays nil (not read). Every series stays in Counts, a target's or not, so
// the collector can count a service back in the targets from its last
// minute.
func attributeLatency(l *hoststats.Latency, targets []service.MetricsTarget) *service.LatencyReading {
	if l == nil {
		return nil
	}
	out := &service.LatencyReading{Unmeasured: l.Unmeasured, Start: l.Start}
	if l.Unmeasured != "" {
		return out
	}
	owners := make(map[string]string, len(targets))
	for _, t := range targets {
		if len(t.ID) < 8 {
			continue
		}
		name := traefikServiceName(t.ID)
		if owner, seen := owners[name]; seen && owner != t.ID {
			owners[name] = "" // ambiguous
			continue
		}
		owners[name] = t.ID
	}
	out.Counts, out.Names = l.Services, make(map[string]string, len(owners))
	for name, id := range owners {
		if id != "" {
			out.Names[id] = name
		}
	}
	return out
}
