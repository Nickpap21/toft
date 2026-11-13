package docker

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

// Both orchestrators let startup recovery check a service's containers.
var (
	_ deployment.ServiceContainerInspector = (*LocalOrchestrator)(nil)
	_ deployment.ServiceContainerInspector = (*RemoteOrchestrator)(nil)
)

// InspectServiceContainers lists the service's containers on the local
// Docker daemon, running or not.
func (o *LocalOrchestrator) InspectServiceContainers(ctx context.Context, serviceID, serviceName string) ([]deployment.ContainerState, error) {
	return inspectServiceContainers(ctx, o.dockerClient, serviceID, serviceName)
}

// RemoveContainer stops and removes a container on the local Docker daemon.
func (o *LocalOrchestrator) RemoveContainer(ctx context.Context, id string) error {
	return rollout.RemoveContainer(ctx, o.dockerClient, id)
}

// StartContainer starts a stopped container on the local Docker daemon.
func (o *LocalOrchestrator) StartContainer(ctx context.Context, id string) error {
	return startContainer(ctx, o.dockerClient, id)
}

// InspectServiceContainers lists the service's containers on the remote
// server through the SSH Docker tunnel, running or not.
func (o *RemoteOrchestrator) InspectServiceContainers(ctx context.Context, serviceID, serviceName string) ([]deployment.ContainerState, error) {
	dc, err := o.host.DockerClient()
	if err != nil {
		return nil, fmt.Errorf("SSH Docker client: %w", err)
	}
	defer dc.Close()
	return inspectServiceContainers(ctx, dc, serviceID, serviceName)
}

// RemoveContainer stops and removes a container on the remote server
// through the SSH Docker tunnel.
func (o *RemoteOrchestrator) RemoveContainer(ctx context.Context, id string) error {
	dc, err := o.host.DockerClient()
	if err != nil {
		return fmt.Errorf("SSH Docker client: %w", err)
	}
	defer dc.Close()
	return rollout.RemoveContainer(ctx, dc, id)
}

// StartContainer starts a stopped container on the remote server through
// the SSH Docker tunnel.
func (o *RemoteOrchestrator) StartContainer(ctx context.Context, id string) error {
	dc, err := o.host.DockerClient()
	if err != nil {
		return fmt.Errorf("SSH Docker client: %w", err)
	}
	defer dc.Close()
	return startContainer(ctx, dc, id)
}

// RestartService restarts the service's running containers on the local
// Docker daemon (restartRunning).
func (o *LocalOrchestrator) RestartService(ctx context.Context, serviceID, serviceName string) (int, error) {
	return restartRunning(ctx, o.dockerClient, serviceID, serviceName)
}

// RestartService restarts the service's running containers on the remote
// server through the SSH Docker tunnel (restartRunning).
func (o *RemoteOrchestrator) RestartService(ctx context.Context, serviceID, serviceName string) (int, error) {
	dc, err := o.host.DockerClient()
	if err != nil {
		return 0, fmt.Errorf("SSH Docker client: %w", err)
	}
	defer dc.Close()
	return restartRunning(ctx, dc, serviceID, serviceName)
}

// restartRunning restarts, in place, each running container of the service
// as inspectServiceContainers finds them: its live slot, or every container
// of its compose project. The image, settings and data stay as they are; a
// stopped container (a previous slot) is left alone. It returns how many
// were restarted.
func restartRunning(ctx context.Context, dc *client.Client, serviceID, serviceName string) (int, error) {
	states, err := inspectServiceContainers(ctx, dc, serviceID, serviceName)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, s := range states {
		if s.State != "running" {
			continue
		}
		timeout := 10
		if err := dc.ContainerRestart(ctx, s.ID, container.StopOptions{Timeout: &timeout}); err != nil {
			return n, fmt.Errorf("ContainerRestart %s: %w", s.Name, err)
		}
		n++
	}
	return n, nil
}

func startContainer(ctx context.Context, dc *client.Client, id string) error {
	if err := dc.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		return fmt.Errorf("ContainerStart: %w", err)
	}
	return nil
}

// inspectServiceContainers lists a service's containers, running or not:
// its blue/green slots and the containers of its compose project, under its
// current and legacy names. A container that carries the service's
// paas.service_id label without a slot's reserved name is left out: the
// label came from its image (tenants choose their images), not from the
// platform, unless the container is also in the service's compose project.
// Any listing that fails fails the whole call, so recovery never acts on a
// partial picture.
func inspectServiceContainers(ctx context.Context, dc *client.Client, serviceID, serviceName string) ([]deployment.ContainerState, error) {
	all, err := serviceContainerList(ctx, dc, serviceID, serviceName)
	if err != nil {
		return nil, err
	}
	return containerStatesFrom(all, serviceID), nil
}

// serviceContainerList lists the containers inspectServiceContainers
// describes, as Docker summarises them (labels included).
func serviceContainerList(ctx context.Context, dc *client.Client, serviceID, serviceName string) ([]container.Summary, error) {
	type query struct {
		filter    filters.Args
		slotsOnly bool
	}
	queries := []query{{filters.NewArgs(filters.Arg("label", rollout.LabelServiceID+"="+serviceID)), true}}
	for _, p := range rollout.ComposeProjectNames(serviceID, serviceName) {
		queries = append(queries, query{filters.NewArgs(filters.Arg("label", rollout.LabelComposeProject+"="+p)), false})
	}
	var all []container.Summary
	seen := make(map[string]bool)
	for _, q := range queries {
		list, err := dc.ContainerList(ctx, container.ListOptions{All: true, Filters: q.filter})
		if err != nil {
			return nil, fmt.Errorf("ContainerList: %w", err)
		}
		for _, c := range list {
			if q.slotsOnly && !rollout.IsSlotName(rollout.SummaryName(c), serviceID) {
				continue
			}
			if !q.slotsOnly && rollout.IsPlatformSlot(c.Labels, rollout.SummaryName(c)) {
				continue // a slot whose image carries the project label
			}
			if !seen[c.ID] {
				seen[c.ID] = true
				all = append(all, c)
			}
		}
	}
	return all, nil
}

// containerStatesFrom maps listed containers to what recovery needs. Slot
// marks serviceID's own blue/green slots (rollout.IsSlotName).
func containerStatesFrom(list []container.Summary, serviceID string) []deployment.ContainerState {
	out := make([]deployment.ContainerState, 0, len(list))
	for _, c := range list {
		var created time.Time
		if c.Created > 0 {
			created = time.Unix(c.Created, 0)
		}
		name := rollout.SummaryName(c)
		out = append(out, deployment.ContainerState{
			ID:             c.ID,
			Name:           name,
			Image:          c.Image,
			State:          string(c.State),
			Created:        created,
			DeploymentID:   c.Labels[rollout.LabelDeploymentID],
			ServiceID:      c.Labels[rollout.LabelServiceID],
			Slot:           c.Labels[rollout.LabelServiceID] == serviceID && rollout.IsSlotName(name, serviceID),
			ComposeProject: c.Labels[rollout.LabelComposeProject],
		})
	}
	return out
}

// listServiceContainerNames returns the names of the service's containers,
// running or not, as inspectServiceContainers finds them: never a container
// that only carries the service's paas.service_id label because its image
// does. Exec, logs and post-deploy commands check names against it.
func listServiceContainerNames(ctx context.Context, dc *client.Client, serviceID, serviceName string) ([]string, error) {
	states, err := inspectServiceContainers(ctx, dc, serviceID, serviceName)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(states))
	for _, s := range states {
		if s.Name != "" {
			names = append(names, s.Name)
		}
	}
	return names, nil
}

// Both orchestrators restart a service's containers (service.Restart).
var (
	_ interface {
		RestartService(context.Context, string, string) (int, error)
	} = (*LocalOrchestrator)(nil)
	_ interface {
		RestartService(context.Context, string, string) (int, error)
	} = (*RemoteOrchestrator)(nil)
)

// ActiveContainer is the container a log stream with no name follows: the
// service's running slot, else its compose project's first running
// container (running true). With none running, the newest of its
// containers, whose last lines say why it stopped (running false); "" when
// it has none.
func (o *LocalOrchestrator) ActiveContainer(ctx context.Context, serviceID, serviceName string) (string, bool) {
	if name := resolveContainer(ctx, o.dockerClient, serviceID, serviceName); name != "" {
		return name, true
	}
	return newestContainer(ctx, o.dockerClient, serviceID, serviceName), false
}

// ActiveContainer is the container a log stream with no name follows (see
// LocalOrchestrator.ActiveContainer).
func (o *RemoteOrchestrator) ActiveContainer(ctx context.Context, serviceID, serviceName string) (string, bool) {
	if name := remoteActiveContainerName(ctx, o.host, serviceID, serviceName); name != "" {
		return name, true
	}
	dc, err := o.host.DockerClient()
	if err != nil {
		return "", false
	}
	defer dc.Close()
	return newestContainer(ctx, dc, serviceID, serviceName), false
}

// ContainerRunning says whether the service's container name runs: false
// for one that exited (a one-shot job). A container it can't find or read
// counts as running, so the page doesn't call a live one finished.
func (o *LocalOrchestrator) ContainerRunning(ctx context.Context, serviceID, serviceName, name string) bool {
	return containerRuns(ctx, o.dockerClient, serviceID, serviceName, name)
}

// ContainerRunning says whether the service's container name runs (see
// LocalOrchestrator.ContainerRunning).
func (o *RemoteOrchestrator) ContainerRunning(ctx context.Context, serviceID, serviceName, name string) bool {
	dc, err := o.host.DockerClient()
	if err != nil {
		return true
	}
	defer dc.Close()
	return containerRuns(ctx, dc, serviceID, serviceName, name)
}

func containerRuns(ctx context.Context, dc *client.Client, serviceID, serviceName, name string) bool {
	states, err := inspectServiceContainers(ctx, dc, serviceID, serviceName)
	if err != nil {
		return true
	}
	for _, s := range states {
		if s.Name == name {
			return s.State != "exited" && s.State != "dead"
		}
	}
	return true
}

// newestContainer is the most recently created of the service's
// containers, running or not; "" when it has none.
func newestContainer(ctx context.Context, dc *client.Client, serviceID, serviceName string) string {
	states, err := inspectServiceContainers(ctx, dc, serviceID, serviceName)
	if err != nil {
		return ""
	}
	name, created := "", time.Time{}
	for _, s := range states {
		if s.Name != "" && (name == "" || s.Created.After(created)) {
			name, created = s.Name, s.Created
		}
	}
	return name
}

// RoutedHosts are the hostnames the service's running containers on the
// local Docker daemon are routed for (routedHostsOf).
func (o *LocalOrchestrator) RoutedHosts(ctx context.Context, serviceID, serviceName string) ([]string, bool, error) {
	list, err := serviceContainerList(ctx, o.dockerClient, serviceID, serviceName)
	if err != nil {
		return nil, false, err
	}
	hosts, running := routedHostsOf(list, serviceID)
	return hosts, running, nil
}

// RoutedHosts are the hostnames the service's running containers on the
// remote server are routed for (routedHostsOf).
func (o *RemoteOrchestrator) RoutedHosts(ctx context.Context, serviceID, serviceName string) ([]string, bool, error) {
	dc, err := o.host.DockerClient()
	if err != nil {
		return nil, false, fmt.Errorf("SSH Docker client: %w", err)
	}
	defer dc.Close()
	list, err := serviceContainerList(ctx, dc, serviceID, serviceName)
	if err != nil {
		return nil, false, err
	}
	hosts, running := routedHostsOf(list, serviceID)
	return hosts, running, nil
}

// routedHostsOf reads, from the running containers among list, the
// hostnames the platform's routers send to them (buildTraefikLabels): a
// Host rule's name, and a wildcard router's certificate name ("*.base").
// running is false when none of them runs. Only the service's own routers
// are read (paas-<id8>-<n>); images can't add routers of their own.
func routedHostsOf(list []container.Summary, serviceID string) ([]string, bool) {
	prefix := fmt.Sprintf("traefik.http.routers.paas-%s-", serviceID[:8])
	hosts := make([]string, 0)
	running := false
	for _, c := range list {
		if c.State != "running" {
			continue
		}
		running = true
		for k, v := range c.Labels {
			router, ok := strings.CutSuffix(strings.TrimPrefix(k, prefix), ".rule")
			if !strings.HasPrefix(k, prefix) || !ok || strings.HasSuffix(router, "-http") {
				continue
			}
			host := ""
			if h, ok := strings.CutPrefix(v, "Host(`"); ok {
				host, _, _ = strings.Cut(h, "`")
			} else if strings.HasPrefix(v, "HostRegexp(") {
				host = c.Labels[prefix+router+".tls.domains[0].main"]
			}
			if host != "" && !slices.Contains(hosts, host) {
				hosts = append(hosts, host)
			}
		}
	}
	slices.Sort(hosts)
	return hosts, running
}
