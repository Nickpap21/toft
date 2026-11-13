package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"go.uber.org/zap"
)

// Both orchestrators report container states and disk usage for alerts.
var (
	_ service.ServiceStateProbe = (*LocalOrchestrator)(nil)
	_ service.ServiceStateProbe = (*RemoteOrchestrator)(nil)
)

// hostStatesCmd lists every container, stopped ones too, with the labels
// attribution reads (as hoststats.Cmd) plus its state and status line. Each
// field is a JSON string, for the reason given at hoststats.Cmd.
const hostStatesCmd = `docker ps -a --format '{{json .Names}}\t{{json (.Label "` + rollout.LabelServiceID + `")}}\t{{json (.Label "` + rollout.LabelComposeProject + `")}}\t{{json .State}}\t{{json .Status}}'`

// hostLogDriversCmd reads, for every container, the fields attribution
// reads (as hostStatesCmd) and where its output goes: the log driver and,
// for Loki's, the address it pushes to. Each field is a JSON string; the
// tabs are template strings, since docker inspect, unlike docker ps, leaves
// a "\t" in --format as it is.
const hostLogDriversCmd = `ids=$(docker ps -aq); [ -z "$ids" ] || docker inspect --format '{{json .Name}}{{"\t"}}{{json (index .Config.Labels "` + rollout.LabelServiceID + `")}}{{"\t"}}{{json (index .Config.Labels "` + rollout.LabelComposeProject + `")}}{{"\t"}}{{json .HostConfig.LogConfig.Type}}{{"\t"}}{{json (index .HostConfig.LogConfig.Config "loki-url")}}' $ids`

// hostDiskCmd reports the root filesystem and Docker's data root. A missing
// /var/lib/docker (rootless Docker) only drops that line.
const hostDiskCmd = `df -P / /var/lib/docker 2>/dev/null; true`

// ServiceStates returns every container (running or not) of each target on
// the remote server, from one command over its pooled SSH connection.
func (o *RemoteOrchestrator) ServiceStates(ctx context.Context, targets []service.MetricsTarget) (map[string][]service.ContainerState, error) {
	out, err := o.host.Exec(ctx, hostStatesCmd)
	if err != nil {
		return nil, fmt.Errorf("remote container states: %w", err)
	}
	a := newAttributor(targets)
	states := make(map[string][]service.ContainerState)
	for _, line := range strings.Split(out, "\n") {
		hc, state, status, ok := parseStateLine(line)
		if !ok {
			continue
		}
		if owner := a.owner(hc); owner != "" {
			states[owner] = append(states[owner], containerState(hc.Name, state, status))
		}
	}
	if dc, err := o.host.DockerClient(); err != nil {
		o.log.Warn("health check probes skipped: no Docker client", zap.Error(err))
	} else {
		probeTargets(ctx, dc, targets, states)
	}
	return states, nil
}

// ServiceStates returns every container (running or not) of each target on
// the local Docker daemon.
func (o *LocalOrchestrator) ServiceStates(ctx context.Context, targets []service.MetricsTarget) (map[string][]service.ContainerState, error) {
	list, err := o.dockerClient.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("ContainerList: %w", err)
	}
	a := newAttributor(targets)
	states := make(map[string][]service.ContainerState)
	for _, c := range list {
		hc := hostContainer{Name: rollout.SummaryName(c), ServiceID: c.Labels[rollout.LabelServiceID], ComposeProject: c.Labels[rollout.LabelComposeProject]}
		if owner := a.owner(hc); owner != "" {
			states[owner] = append(states[owner], containerState(hc.Name, string(c.State), c.Status))
		}
	}
	probeTargets(ctx, o.dockerClient, targets, states)
	return states, nil
}

// LogDrivers returns where each target's containers send their output (the
// Observability page, D-48), from one command over the pooled SSH
// connection.
func (o *RemoteOrchestrator) LogDrivers(ctx context.Context, targets []service.MetricsTarget) (map[string][]service.ContainerLog, error) {
	out, err := o.host.Exec(ctx, hostLogDriversCmd)
	if err != nil {
		return nil, fmt.Errorf("remote log drivers: %w", err)
	}
	a := newAttributor(targets)
	logs := make(map[string][]service.ContainerLog)
	for _, line := range strings.Split(out, "\n") {
		hc, cl, ok := parseLogDriverLine(line)
		if !ok {
			continue
		}
		if owner := a.owner(hc); owner != "" {
			logs[owner] = append(logs[owner], cl)
		}
	}
	return logs, nil
}

// LogDrivers returns where each target's containers on the local Docker
// daemon send their output.
func (o *LocalOrchestrator) LogDrivers(ctx context.Context, targets []service.MetricsTarget) (map[string][]service.ContainerLog, error) {
	list, err := o.dockerClient.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("ContainerList: %w", err)
	}
	a := newAttributor(targets)
	logs := make(map[string][]service.ContainerLog)
	for _, c := range list {
		hc := hostContainer{Name: rollout.SummaryName(c), ServiceID: c.Labels[rollout.LabelServiceID], ComposeProject: c.Labels[rollout.LabelComposeProject]}
		owner := a.owner(hc)
		if owner == "" {
			continue
		}
		info, err := o.dockerClient.ContainerInspect(ctx, c.ID)
		if err != nil {
			return nil, fmt.Errorf("ContainerInspect %s: %w", hc.Name, err)
		}
		cl := service.ContainerLog{Name: hc.Name}
		if info.HostConfig != nil {
			cl.Driver = info.HostConfig.LogConfig.Type
			cl.LokiURL = info.HostConfig.LogConfig.Config["loki-url"]
		}
		logs[owner] = append(logs[owner], cl)
	}
	return logs, nil
}

// Both orchestrators say whether the Loki log driver is installed, so a
// deploy never starts a container with a driver Docker lacks (D-48).
var (
	_ deployment.LokiDriverChecker = (*LocalOrchestrator)(nil)
	_ deployment.LokiDriverChecker = (*RemoteOrchestrator)(nil)
)

// HasLokiDriver reports whether the server's Docker has an enabled Loki
// log driver plugin (provisioning installs it with the alias "loki"). An
// agent server needs none: its agent ships the logs (D-113).
func (o *RemoteOrchestrator) HasLokiDriver(ctx context.Context) (bool, error) {
	if o.net.LocalLogs {
		return true, nil
	}
	out, err := o.host.Exec(ctx, `docker plugin ls --format '{{.Name}} {{.Enabled}}' 2>/dev/null; true`)
	if err != nil {
		return false, fmt.Errorf("list plugins: %w", err)
	}
	return hasLokiPlugin(strings.Split(out, "\n")), nil
}

// HasLokiDriver reports whether the local Docker has an enabled Loki log
// driver plugin.
func (o *LocalOrchestrator) HasLokiDriver(ctx context.Context) (bool, error) {
	plugins, err := o.dockerClient.PluginList(ctx, filters.Args{})
	if err != nil {
		return false, fmt.Errorf("PluginList: %w", err)
	}
	lines := make([]string, 0, len(plugins))
	for _, p := range plugins {
		lines = append(lines, fmt.Sprintf("%s %t", p.Name, p.Enabled))
	}
	return hasLokiPlugin(lines), nil
}

// hasLokiPlugin reads "name enabled" lines: "loki:latest true".
func hasLokiPlugin(lines []string) bool {
	for _, l := range lines {
		name, enabled, _ := strings.Cut(strings.TrimSpace(l), " ")
		if enabled == "true" && (strings.HasPrefix(name, "loki:") || name == "loki" || strings.Contains(name, "loki-docker-driver")) {
			return true
		}
	}
	return false
}

// parseLogDriverLine reads one line of hostLogDriversCmd.
func parseLogDriverLine(line string) (hostContainer, service.ContainerLog, bool) {
	fields := strings.Split(line, "\t")
	if len(fields) != 5 {
		return hostContainer{}, service.ContainerLog{}, false
	}
	vals := make([]string, len(fields))
	for i, f := range fields {
		if json.Unmarshal([]byte(f), &vals[i]) != nil {
			return hostContainer{}, service.ContainerLog{}, false
		}
	}
	// docker inspect names a container "/web-blue"; the listing says "web-blue".
	head := make([]string, 3)
	for i, v := range []string{strings.TrimPrefix(vals[0], "/"), vals[1], vals[2]} {
		b, _ := json.Marshal(v)
		head[i] = string(b)
	}
	hc, ok := parseListingLine(strings.Join(head, "\t"))
	if !ok {
		return hostContainer{}, service.ContainerLog{}, false
	}
	return hc, service.ContainerLog{Name: hc.Name, Driver: vals[3], LokiURL: vals[4]}, true
}

// DiskUsage reports how full the remote server's root filesystem and
// Docker data root are.
func (o *RemoteOrchestrator) DiskUsage(ctx context.Context) ([]service.DiskUsage, error) {
	out, err := o.host.Exec(ctx, hostDiskCmd)
	if err != nil {
		return nil, fmt.Errorf("remote df: %w", err)
	}
	usage := parseDF(out)
	if len(usage) == 0 {
		return nil, fmt.Errorf("remote df: no filesystem reported")
	}
	return usage, nil
}

// DiskUsage reports how full the filesystems under / (inside the backend
// container: Docker's data root, which backs its overlay) and the data
// directory (a host directory) are.
func (o *LocalOrchestrator) DiskUsage(context.Context) ([]service.DiskUsage, error) {
	paths := []string{"/"}
	if o.dataDir != "" {
		paths = append(paths, o.dataDir)
	}
	var usage []service.DiskUsage
	for _, p := range paths {
		var st syscall.Statfs_t
		if err := syscall.Statfs(p, &st); err != nil {
			continue
		}
		if pct, ok := usedPercent(st.Blocks-st.Bfree, uint64(st.Bavail)); ok {
			usage = append(usage, service.DiskUsage{Mount: p, UsedPercent: pct})
		}
	}
	if len(usage) == 0 {
		return nil, fmt.Errorf("statfs: no filesystem readable")
	}
	return usage, nil
}

// HostSize is the local Docker host's CPU cores, memory and operating
// system, from its daemon, and the size of the filesystem under / inside
// the backend container (Docker's data root, as DiskUsage reads); 0 when
// unreadable.
func (o *LocalOrchestrator) HostSize(ctx context.Context) (cores int, memBytes int64, diskBytes uint64, os string, err error) {
	info, err := o.dockerClient.Info(ctx)
	if err != nil {
		return 0, 0, 0, "", fmt.Errorf("docker info: %w", err)
	}
	var st syscall.Statfs_t
	if syscall.Statfs("/", &st) == nil {
		diskBytes = st.Blocks * uint64(st.Bsize)
	}
	return info.NCPU, info.MemTotal, diskBytes, info.OperatingSystem, nil
}

// AddressPools are the local Docker's default-address-pools, none when it
// has none (app networks then take its default ranges).
func (o *LocalOrchestrator) AddressPools(ctx context.Context) ([]server.AddressPool, error) {
	info, err := o.dockerClient.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("docker info: %w", err)
	}
	out := make([]server.AddressPool, 0, len(info.DefaultAddressPools))
	for _, p := range info.DefaultAddressPools {
		out = append(out, server.AddressPool{Base: p.Base, Size: p.Size})
	}
	return out, nil
}

// NetworkSubnet is a network's first IPv4 subnet on the local host, "" when
// it has none.
func (o *LocalOrchestrator) NetworkSubnet(ctx context.Context, name string) (string, error) {
	ins, err := o.dockerClient.NetworkInspect(ctx, name, network.InspectOptions{})
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", name, err)
	}
	for _, c := range ins.IPAM.Config {
		if !strings.Contains(c.Subnet, ":") {
			return c.Subnet, nil
		}
	}
	return "", nil
}

// usedPercent is df's Use%: used blocks over the blocks a user can use
// (used + available; blocks reserved for root count as neither), rounded up.
func usedPercent(used, avail uint64) (int, bool) {
	total := used + avail
	if total == 0 {
		return 0, false
	}
	return int((used*100 + total - 1) / total), true
}

// parseStateLine parses one line of hostStatesCmd: the three listing fields
// parseListingLine reads, then the state and the status line.
func parseStateLine(line string) (hc hostContainer, state, status string, ok bool) {
	fields := strings.Split(line, "\t")
	if len(fields) != 5 {
		return hostContainer{}, "", "", false
	}
	hc, ok = parseListingLine(strings.Join(fields[:3], "\t"))
	if !ok {
		return hostContainer{}, "", "", false
	}
	if json.Unmarshal([]byte(fields[3]), &state) != nil || json.Unmarshal([]byte(fields[4]), &status) != nil {
		return hostContainer{}, "", "", false
	}
	return hc, state, status, true
}

var (
	exitCodePattern = regexp.MustCompile(`^Exited \((-?\d+)\)`)
	healthPattern   = regexp.MustCompile(`\((healthy|unhealthy|health: starting)\)`)
)

// containerState builds a ContainerState from Docker's state ("running",
// "restarting", "exited", ...) and status line ("Up 3 minutes (unhealthy)",
// "Exited (1) 2 minutes ago"), which carries the health and the exit code.
func containerState(name, state, status string) service.ContainerState {
	cs := service.ContainerState{Name: name, State: state}
	if m := healthPattern.FindStringSubmatch(status); m != nil {
		cs.Health = strings.TrimPrefix(m[1], "health: ")
	}
	if m := exitCodePattern.FindStringSubmatch(status); m != nil {
		cs.ExitCode, _ = strconv.Atoi(m[1])
	}
	return cs
}

// parseDF reads `df -P` output: a header, then one line per filesystem whose
// fifth column is the capacity ("85%") and sixth the mount point. A
// filesystem listed twice (both paths on /) is reported once.
func parseDF(out string) []service.DiskUsage {
	var usage []service.DiskUsage
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 6 || !strings.HasSuffix(f[4], "%") {
			continue
		}
		pct, err := strconv.Atoi(strings.TrimSuffix(f[4], "%"))
		if err != nil {
			continue // the header ("Capacity")
		}
		mount := strings.Join(f[5:], " ")
		if seen[f[0]+" "+mount] {
			continue
		}
		seen[f[0]+" "+mount] = true
		usage = append(usage, service.DiskUsage{Mount: mount, UsedPercent: pct})
	}
	return usage
}
