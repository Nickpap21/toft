package rollout

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/Nickpap21/toft/backend/pkg/lines"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	dockerimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// DockerHost runs rollouts on the Docker Client reaches: the panel's own,
// a server's through the SSH tunnel, or the agent's on its server.
type DockerHost struct {
	Client *client.Client
	Net    NetConfig
	// Warn receives what goes wrong without failing the rollout (a probe
	// tool that could not be mounted, a network a slot could not join).
	Warn func(string)
}

var _ Host = DockerHost{}

// pullAuth encodes auth for ImagePull, "" when it has no password. A token
// stored without a username goes as basic auth under "token", as docker
// login does on the SSH and compose paths: sent as an IdentityToken, the
// daemon tries an OAuth refresh-token exchange, which GHCR answers
// "unsupported".
func pullAuth(auth *RegistryAuth) (string, error) {
	if auth == nil || auth.Password == "" {
		return "", nil
	}
	username := auth.Username
	if username == "" {
		username = "token"
	}
	authJSON, err := json.Marshal(registry.AuthConfig{
		ServerAddress: auth.Server,
		Username:      username,
		Password:      auth.Password,
	})
	if err != nil {
		return "", fmt.Errorf("marshal auth config: %w", err)
	}
	return base64.URLEncoding.EncodeToString(authJSON), nil
}

// HasImage reports whether the host has image.
func (h DockerHost) HasImage(ctx context.Context, image string) bool {
	_, err := h.Client.ImageInspect(ctx, image)
	return err == nil
}

// Pull pulls image with auth, when it has a password, streaming the pull's
// progress to logFn.
func (h DockerHost) Pull(ctx context.Context, image string, auth *RegistryAuth, logFn func(string)) error {
	encodedAuth, err := pullAuth(auth)
	if err != nil {
		return err
	}

	reader, err := h.Client.ImagePull(ctx, image, dockerimage.PullOptions{RegistryAuth: encodedAuth})
	if err != nil {
		return fmt.Errorf("ImagePull: %w", err)
	}
	defer reader.Close()

	sc := bufio.NewScanner(reader)
	for sc.Scan() {
		var line struct {
			Status   string `json:"status"`
			Progress string `json:"progress"`
			Error    string `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &line) == nil {
			if line.Error != "" {
				return errors.New(line.Error)
			}
			if line.Status != "" {
				msg := line.Status
				if line.Progress != "" {
					msg += " " + line.Progress
				}
				logFn(msg)
			}
		}
	}
	return sc.Err()
}

// RunOnce runs argv in a throwaway container from spec's image on its home
// network (runEphemeral), named for the deployment (MigrateContainerName).
// Resumed, it only waits for the one an earlier attempt started
// (awaitEphemeral).
func (h DockerHost) RunOnce(ctx context.Context, spec Spec, argv []string, resume bool, logFn func(string)) error {
	name := ""
	if spec.DeploymentID != "" {
		name = MigrateContainerName(spec.DeploymentID)
	}
	if resume {
		return awaitEphemeral(ctx, h.Client, name, logFn)
	}
	if spec.Plan.Home.Name == "" {
		return errors.New("ephemeral run: no network")
	}
	return runEphemeral(ctx, h.Client, name, spec.Slot.Image, spec.Slot.Env, argv, spec.Plan.Home.Name, spec.Slot.Security, logFn)
}

// MigrateContainerName is the migrate container of a deployment: a resumed
// rollout finds it by this name.
func MigrateContainerName(deploymentID string) string { return "toft-migrate-" + deploymentID }

// ErrOnceGone: a resumed rollout found no migrate container. It may have
// finished while the host restarted, so its outcome is unknown and it is not
// run again.
var ErrOnceGone = errors.New("the migrate command was running when the agent restarted and its container is gone, so its outcome is unknown; " +
	"it never runs twice: check the database, then deploy again")

// Start starts the new slot (StartSlot).
func (h DockerHost) Start(ctx context.Context, spec Spec, sw *SlotSwitch) error {
	return StartSlot(ctx, h.Client, h.Net, spec.Plan, spec.Slot, sw, h.Warn)
}

// WaitReady waits until the new slot accepts connections (WaitReady).
func (h DockerHost) WaitReady(ctx context.Context, name string, port int, logFn func(string)) error {
	return WaitReady(ctx, h.Client, name, port, logFn)
}

// Health asks the health check's path from inside name's network namespace
// (healthProbe), so the app's image needs no curl.
func (h DockerHost) Health(ctx context.Context, name string, port int, hc HealthCheck) error {
	code, err := healthProbe(ctx, h.Client, name, port, hc)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("exit status %d", code)
	}
	return nil
}

// Slots are the names of the service's slot containers, running or not.
func (h DockerHost) Slots(ctx context.Context, serviceID string) ([]string, error) {
	list, err := ServiceContainers(ctx, h.Client, serviceID)
	if err != nil {
		return nil, err
	}
	slots := SlotsOf(list, serviceID)
	names := make([]string, 0, len(slots))
	for _, c := range slots {
		names = append(names, SummaryName(c))
	}
	return names, nil
}

// Exec runs command with sh -c in the running container name, streaming
// its output to logFn, and fails when it exits non-zero.
func (h DockerHost) Exec(ctx context.Context, name, command string, logFn func(string)) error {
	ex, err := h.Client.ContainerExecCreate(ctx, name, container.ExecOptions{
		Cmd:          []string{"sh", "-c", command},
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("exec in %s: %w", name, err)
	}
	resp, err := h.Client.ContainerExecAttach(ctx, ex.ID, container.ExecAttachOptions{})
	if err != nil {
		return fmt.Errorf("exec in %s: %w", name, err)
	}
	err = streamLines(resp.Reader, logFn)
	resp.Close()
	if err != nil {
		return fmt.Errorf("exec in %s: %w", name, err)
	}
	in, err := h.Client.ContainerExecInspect(ctx, ex.ID)
	if err != nil {
		return fmt.Errorf("exec in %s: %w", name, err)
	}
	if in.ExitCode != 0 {
		return fmt.Errorf("exec in %s: exit status %d", name, in.ExitCode)
	}
	return nil
}

// FinishSwitch retires the service's containers other than keep (FinishSwitch).
func (h DockerHost) FinishSwitch(ctx context.Context, serviceID, keep string, drain bool) error {
	return FinishSwitch(ctx, h.Client, serviceID, keep, drain)
}

// AwaitHealthy waits for Docker to report the new slot healthy (AwaitHealthy).
func (h DockerHost) AwaitHealthy(ctx context.Context, name string, settle bool) error {
	return AwaitHealthy(ctx, h.Client, name, settle)
}

// AbortSwitch removes the new slot of a failed rollout (AbortSwitch).
func (h DockerHost) AbortSwitch(ctx context.Context, serviceID, name string) error {
	return AbortSwitch(ctx, h.Client, serviceID, name)
}

// RestorePrevious starts the slots a stop-then-start stopped (RestorePrevious).
func (h DockerHost) RestorePrevious(ctx context.Context, serviceID string, names []string) error {
	return RestorePrevious(ctx, h.Client, serviceID, names)
}

// streamLines writes each line of a multiplexed stdout/stderr stream to
// logFn.
func streamLines(r io.Reader, logFn func(string)) error {
	pr, pw := io.Pipe()
	go func() {
		_, err := stdcopy.StdCopy(pw, pw, r)
		pw.CloseWithError(err)
	}()
	err := lines.Read(pr, logFn)
	_ = pr.CloseWithError(io.EOF) // ends StdCopy if the read stopped early
	return err
}

// ProbeTarget is where a probe container asks name's port from: name's
// network namespace and localhost, except under gVisor's own network stack
// (RuntimeStrict), which keeps the namespace to itself, so there the probe
// joins one of name's networks and asks its address on it (D-115).
func ProbeTarget(ctx context.Context, dc *client.Client, name string) (networkMode, host string, err error) {
	ci, err := dc.ContainerInspect(ctx, name)
	if err != nil {
		return "", "", err
	}
	if ci.HostConfig == nil || ci.HostConfig.Runtime != RuntimeStrict {
		return "container:" + name, "localhost", nil
	}
	if ci.NetworkSettings != nil {
		for net, ep := range ci.NetworkSettings.Networks {
			if ep != nil && ep.IPAddress != "" {
				return net, ep.IPAddress, nil
			}
		}
	}
	return "", "", fmt.Errorf("%s has no address to probe", name)
}

// healthProbe runs one health check request: a throwaway probe container
// asks port/path from where ProbeTarget says, so distroless and slim images
// without curl pass too. It returns curl's exit code: non-zero
// on no answer, a timeout, or an HTTP status of 400 or more (curl -f).
func healthProbe(ctx context.Context, dc *client.Client, name string, port int, hc HealthCheck) (int64, error) {
	if err := ensureImage(ctx, dc, HealthProbeImage); err != nil {
		return 0, fmt.Errorf("the probe image: %w", err)
	}
	networkMode, host, err := ProbeTarget(ctx, dc, name)
	if err != nil {
		return 0, fmt.Errorf("the probe's target: %w", err)
	}
	pids := int64(64)
	hostCfg := &container.HostConfig{
		AutoRemove:  true,
		NetworkMode: container.NetworkMode(networkMode),
		SecurityOpt: []string{"no-new-privileges:true"},
		CapDrop:     []string{"NET_RAW"},
		Resources:   container.Resources{Memory: 32 << 20, PidsLimit: &pids},
	}
	cfg := &container.Config{
		Image:      HealthProbeImage,
		Entrypoint: []string{"curl"},
		Cmd:        []string{"-sf", "-o", "/dev/null", "--max-time", strconv.Itoa(hc.TimeoutSec), fmt.Sprintf("http://%s:%d%s", host, port, hc.Path)},
		Labels:     map[string]string{"traefik.enable": "false"},
	}
	resp, err := dc.ContainerCreate(ctx, cfg, hostCfg, nil, nil, "")
	if err != nil {
		return 0, fmt.Errorf("ContainerCreate (probe): %w", err)
	}
	// Registered before start, as runEphemeral's.
	waitCtx, cancelWait := context.WithCancel(ctx)
	defer cancelWait()
	statusCh, errCh := dc.ContainerWait(waitCtx, resp.ID, container.WaitConditionRemoved)
	if err := dc.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		_ = dc.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return 0, fmt.Errorf("ContainerStart (probe): %w", err)
	}
	select {
	case st := <-statusCh:
		return st.StatusCode, nil
	case err := <-errCh:
		return 0, fmt.Errorf("ContainerWait (probe): %w", err)
	}
}

// ensureImage pulls image when the host lacks it.
func ensureImage(ctx context.Context, dc *client.Client, image string) error {
	if _, err := dc.ImageInspect(ctx, image); err == nil {
		return nil
	} else if !client.IsErrNotFound(err) {
		return err
	}
	rc, err := dc.ImagePull(ctx, image, dockerimage.PullOptions{})
	if err != nil {
		return err
	}
	_, err = io.Copy(io.Discard, rc)
	rc.Close()
	return err
}

// HealthProbeImage makes health check requests. Pinned by digest (a
// multi-arch index): it runs on every server.
const HealthProbeImage = "curlimages/curl@sha256:58adaa4e8dca9c988bae2aba4ab3434a0bb2da16bbe3f92dec39ec7785166777"

// runEphemeral creates an auto-removed container, named name ("": Docker
// picks one), from image on networkName through dc, streams its output to
// logFn and returns an error if it exits non-zero. It never inherits the image's identity labels, and is never
// routed: Traefik joins its network in both app network modes. Env vars
// travel in the API request body, never on a command line.
func runEphemeral(ctx context.Context, dc *client.Client, name, image string, envVars, command []string, networkName string, sec Security, logFn func(string)) error {
	labels, err := WithImageLabelsBlanked(ctx, dc, image, map[string]string{"traefik.enable": "false"}, false)
	if err != nil {
		return err
	}

	pids := sec.Pids()
	hostCfg := &container.HostConfig{AutoRemove: true, NetworkMode: container.NetworkMode(networkName),
		SecurityOpt: []string{"no-new-privileges:true"}, CapDrop: sec.CapDrop(), Runtime: sec.Runtime, Mounts: sec.Mounts()}
	hostCfg.PidsLimit = &pids
	cfg := &container.Config{
		Image: image,
		// [""] is what `docker run --entrypoint ""` sends: the image's
		// ENTRYPOINT is dropped and command is the whole argv, so an image
		// whose entrypoint is its server binary (or has no shell) can still
		// run its migrate command.
		Entrypoint: []string{""},
		Cmd:        command,
		Env:        envVars,
		Labels:     labels,
	}
	netCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			networkName: {},
		},
	}

	resp, err := dc.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, name)
	if err != nil {
		return fmt.Errorf("ContainerCreate (ephemeral): %w", err)
	}
	return startEphemeral(ctx, dc, resp.ID, logFn)
}

// awaitEphemeral waits for the container name that runEphemeral created
// before a restart: it starts one that was created but never started,
// streams its output (again, from its start) and reports its exit code. ErrOnceGone
// when there is none.
func awaitEphemeral(ctx context.Context, dc *client.Client, name string, logFn func(string)) error {
	if name == "" {
		return ErrOnceGone
	}
	in, err := dc.ContainerInspect(ctx, name)
	if cerrdefs.IsNotFound(err) {
		return ErrOnceGone
	}
	if err != nil {
		return fmt.Errorf("ContainerInspect (ephemeral): %w", err)
	}
	if in.State != nil && in.State.Status != container.StateCreated {
		logFn("→ waiting for the migrate container started before the agent restarted")
	}
	return startEphemeral(ctx, dc, in.ID, logFn)
}

// startEphemeral starts container id (a no-op if it runs), streams its logs
// to logFn and waits for it to exit and be removed.
func startEphemeral(ctx context.Context, dc *client.Client, id string, logFn func(string)) error {
	// Wait for removal, registered before start as `docker run --rm` does:
	// with AutoRemove a wait sent after the container exits can find it
	// already gone and lose the exit code. Cancelling waitCtx on return ends
	// the wait if the container never runs.
	waitCtx, cancelWait := context.WithCancel(ctx)
	defer cancelWait()
	statusCh, errCh := dc.ContainerWait(waitCtx, id, container.WaitConditionRemoved)
	// Only the deploy's time limit cancels a rollout (D-125): stopping the
	// panel or the agent leaves ctx live, so the agent can wait for this
	// container again. Past the limit it is removed, not left running.
	defer func() {
		if ctx.Err() == nil {
			return
		}
		rctx, cancel := settle(ctx)
		defer cancel()
		if err := dc.ContainerRemove(rctx, id, container.RemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			logFn("⚠ Could not remove the migrate container: " + err.Error())
		}
	}()

	if err := dc.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		// AutoRemove only applies once a container has run.
		_ = dc.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
		return fmt.Errorf("ContainerStart (ephemeral): %w", err)
	}

	// Stream logs while the container runs.
	logReader, err := dc.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
	})
	if err != nil {
		return fmt.Errorf("ContainerLogs (ephemeral): %w", err)
	}
	defer logReader.Close()
	if err := streamLines(logReader, logFn); err != nil {
		return fmt.Errorf("read ephemeral logs: %w", err)
	}

	select {
	case waitResp := <-statusCh:
		if waitResp.StatusCode != 0 {
			return fmt.Errorf("ephemeral container exited with code %d", waitResp.StatusCode)
		}
	case err := <-errCh:
		if cerrdefs.IsNotFound(err) {
			return ErrOnceGone // removed before the wait: only an awaited one
		}
		if err != nil {
			return fmt.Errorf("ContainerWait (ephemeral): %w", err)
		}
	}
	return nil
}
