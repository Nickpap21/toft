package rollout

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/netplan"
	"github.com/Nickpap21/toft/backend/internal/rollout/fakedocker"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func onceSpec(image string, env []string, network string, sec Security) Spec {
	return Spec{Slot: Slot{Image: image, Env: env, Security: sec}, Plan: netplan.Plan{Home: netplan.Network{Name: network}}}
}

func TestRunOnceGoesThroughTheDockerAPI(t *testing.T) {
	f := fakedocker.New(t)
	f.SetImageLabels("app:1", map[string]string{"paas.service_id": otherSvc, "maintainer": "x"})
	h := DockerHost{Client: f.Client()}

	var logs []string
	env := []string{"DATABASE_URL=postgres://u:hunter2@db/app", "PEM=-----BEGIN-----\nabc\n-----END-----"}
	require.NoError(t, h.RunOnce(context.Background(), onceSpec("app:1", env, "paas-env-e1", Security{}), []string{"/server", "migrate"}, false,
		func(l string) { logs = append(logs, l) }))

	var run *fakedocker.Container
	for _, c := range f.Containers {
		if c.Image == "app:1" {
			run = c
		}
	}
	require.NotNil(t, run, "the container is created through the API")
	assert.Equal(t, env, run.Env, "env, multi-line values included, travel in the request body")
	assert.Equal(t, []string{""}, run.Entrypoint, "the image's ENTRYPOINT is dropped, as --entrypoint \"\" does")
	assert.Equal(t, []string{"/server", "migrate"}, run.Cmd, "the command is the whole argv")
	assert.Equal(t, "", run.Labels["paas.service_id"], "the image's identity label is blanked")
	assert.Equal(t, "false", run.Labels["traefik.enable"], "never routed on an app network")
	assert.NotContains(t, run.Labels, "maintainer")
	assert.Equal(t, []string{"ran " + run.Name}, logs)
	assert.Equal(t, []string{"no-new-privileges:true"}, run.SecurityOpt, "the hardened runtime (D-93)")
	assert.Equal(t, []string{"CAP_NET_RAW"}, run.CapDrop, "the client sends it normalised")
	require.NotNil(t, run.PidsLimit)
	assert.Equal(t, int64(2048), *run.PidsLimit)
	assert.Equal(t, "", run.Runtime, "Standard")
	require.NoError(t, h.RunOnce(context.Background(), onceSpec("app:1", nil, "paas-network", Security{Runtime: RuntimeSandboxed}), []string{"true"}, false, func(string) {}))
	assert.Equal(t, "runsc", f.Containers[len(f.Containers)-1].Runtime, "a Sandboxed service's migrate container runs under gVisor (D-95)")

	f.SetImageLabels("evil:1", map[string]string{"traefik.enable": "true", "traefik.http.routers.x.rule": "Host(`panel.example.com`)"})
	require.NoError(t, h.RunOnce(context.Background(), onceSpec("evil:1", nil, "paas-network", Security{}), []string{"true"}, false, func(string) {}))
	last := f.Containers[len(f.Containers)-1]
	assert.Equal(t, "false", last.Labels["traefik.enable"], "never routed on the control plane's network either (legacy)")
}

func TestRunOnceReportsTheExitCode(t *testing.T) {
	f := fakedocker.New(t)
	f.ExitCode = 3
	err := DockerHost{Client: f.Client()}.RunOnce(context.Background(), onceSpec("app:1", nil, "paas-env-e1", Security{}), []string{"false"}, false, func(string) {})
	assert.ErrorContains(t, err, "exited with code 3")
}

func TestRunOnceNeedsANetwork(t *testing.T) {
	assert.Error(t, DockerHost{}.RunOnce(context.Background(), onceSpec("app:1", nil, "", Security{}), []string{"true"}, false, func(string) {}))
}

func TestHealthProbesFromTheSlotsNetwork(t *testing.T) {
	f := fakedocker.New(t)
	f.Add(slotGreen, container.StateRunning, svcLabels(slotSvcID))
	h := DockerHost{Client: f.Client()}

	require.NoError(t, h.Health(context.Background(), slotGreen, 8080, HealthCheck{Path: "/health", TimeoutSec: 5}))
	probe := f.Containers[len(f.Containers)-1]
	assert.Equal(t, HealthProbeImage, probe.Image)
	assert.Equal(t, []string{"curl"}, probe.Entrypoint)
	assert.Equal(t, []string{"-sf", "-o", "/dev/null", "--max-time", "5", "http://localhost:8080/health"}, probe.Cmd)
	assert.Equal(t, []string{"CAP_NET_RAW"}, probe.CapDrop, "the client sends it normalised")
	assert.Equal(t, "false", probe.Labels["traefik.enable"])

	f.ExitCode = 22
	assert.EqualError(t, h.Health(context.Background(), slotGreen, 8080, HealthCheck{Path: "/health", TimeoutSec: 5}), "exit status 22")
}

func TestExecReportsTheExitCode(t *testing.T) {
	f := fakedocker.New(t)
	c := f.Add(slotGreen, container.StateRunning, svcLabels(slotSvcID))
	h := DockerHost{Client: f.Client()}

	assert.EqualError(t, h.Exec(context.Background(), slotGreen, "false", func(string) {}), "exec in "+slotGreen+": exit status 1")
	c.ListensFrom = 1 // the fake's execs exit 0 from here
	require.NoError(t, h.Exec(context.Background(), slotGreen, "true", func(string) {}))
	c.State = container.StateExited
	assert.ErrorContains(t, h.Exec(context.Background(), slotGreen, "true", func(string) {}), "exec in "+slotGreen)
}

func TestRunOnceIsNamedForTheDeploymentAndAwaitedOnResume(t *testing.T) {
	f := fakedocker.New(t)
	h := DockerHost{Client: f.Client()}
	spec := onceSpec("app:1", nil, "paas-env-e1", Security{})
	spec.DeploymentID = "dep-7"

	assert.ErrorIs(t, h.RunOnce(context.Background(), spec, []string{"true"}, true, func(string) {}), ErrOnceGone,
		"resumed with no container: it may have finished, so it never runs again")
	assert.Empty(t, f.Names())

	require.NoError(t, h.RunOnce(context.Background(), spec, []string{"true"}, false, func(string) {}))
	assert.Equal(t, MigrateContainerName("dep-7"), f.Containers[len(f.Containers)-1].Name)

	// Created before the restart but never started: it starts now, once.
	f = fakedocker.New(t)
	f.Add(MigrateContainerName("dep-7"), container.StateCreated, nil)
	require.NoError(t, DockerHost{Client: f.Client()}.RunOnce(context.Background(), spec, []string{"true"}, true, func(string) {}))
	assert.Equal(t, []string{"start " + MigrateContainerName("dep-7")}, f.RecordedOps())
}

// A migrate command still running at the deploy's time limit is removed,
// not left running (D-125).
func TestRunOnceRemovesTheContainerAtTheTimeLimit(t *testing.T) {
	f := fakedocker.New(t)
	name := MigrateContainerName("dep-8")
	f.Add(name, container.StateCreated, nil).Logs = []string{} // follows until it stops
	spec := onceSpec("app:1", nil, "paas-env-e1", Security{})
	spec.DeploymentID = "dep-8"

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.Error(t, DockerHost{Client: f.Client()}.RunOnce(ctx, spec, []string{"sleep", "100000"}, true, func(string) {}))
	assert.Equal(t, []string{"start " + name, "remove " + name}, f.RecordedOps())
	assert.Empty(t, f.Names())
}

// A token stored without a username pulls with basic auth under "token",
// never as an IdentityToken (GHCR answers that "unsupported").
func TestPullAuthTokenOnly(t *testing.T) {
	enc, err := pullAuth(&RegistryAuth{Server: "ghcr.io", Password: "ghp_x"})
	require.NoError(t, err)
	raw, err := base64.URLEncoding.DecodeString(enc)
	require.NoError(t, err)
	var got registry.AuthConfig
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, registry.AuthConfig{ServerAddress: "ghcr.io", Username: "token", Password: "ghp_x"}, got)

	enc, err = pullAuth(&RegistryAuth{Username: "u"})
	require.NoError(t, err)
	require.Empty(t, enc, "no password, no auth")
}

// A line over bufio.Scanner's 64 KiB used to fail the deploy's migrate step.
func TestStreamLines_KeepsGoingPastALongLine(t *testing.T) {
	var buf bytes.Buffer
	long := strings.Repeat("x", 70_000)
	_, err := stdcopy.NewStdWriter(&buf, stdcopy.Stdout).Write([]byte(long + "\nafter\n"))
	require.NoError(t, err)

	got := make([]string, 0, 2)
	require.NoError(t, streamLines(&buf, func(l string) { got = append(got, l) }))
	assert.Equal(t, []string{long, "after"}, got)
}
