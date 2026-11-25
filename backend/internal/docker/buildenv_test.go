package docker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/Nickpap21/toft/backend/internal/builder"
	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/rollout/fakedocker"
)

// dockerSettings are variables of a service that the docker CLI, buildx
// and BuildKit read as their own settings: which Docker, which builder,
// whose configuration. APP_KEY is an ordinary one beside them.
var dockerSettings = map[string]string{
	"BUILDX_BUILDER": "default",
	"DOCKER_HOST":    "tcp://service.invalid:2375",
	"BUILDX_CONFIG":  "/tmp/service-buildx",
	"DOCKER_CONFIG":  "/tmp/service-docker",
	"BUILDKIT_HOST":  "tcp://service.invalid:1234",
	"APP_KEY":        "1",
}

// noDockerSettings fails when text holds one of dockerSettings' values for
// a setting.
func noDockerSettings(t *testing.T, where, text string) {
	t.Helper()
	for _, v := range []string{"service.invalid", "/tmp/service-buildx", "/tmp/service-docker", "BUILDX_BUILDER=default", "BUILDX_BUILDER='default'", `BUILDX_BUILDER="default"`} {
		assert.NotContains(t, text, v, where)
	}
}

// On the panel's host the service's variables reach compose in a file of
// the deploy's own, and no command's environment, so nothing compose starts
// (a build) reads one as a setting. Those the docker CLI reads as settings
// are in neither: the builder is made sure of on the panel's own Docker
// with the deploy's own config, and the build is on Toft's builder.
func TestLocalComposeBuildTakesNoDockerSettingsFromTheService(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\n{ echo \"== $*\"; env; } >> " + bin + "/envs\n" +
		"p=; for a; do [ \"$p\" = --env-file ] && cat \"$a\" > " + bin + "/envfile; p=$a; done\n" + builderAnswers(bin+"/calls", stackBuilds) +
		"case \"$*\" in *' up --no-start '*) exit 1;; esac\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")

	o := &LocalOrchestrator{dataDir: t.TempDir(), dockerClient: fakedocker.New(t).Client(), log: zap.NewNop(), builder: testBuilder}
	err := o.RunCompose(context.Background(), "dep-1", testServiceID, "web", "services: {web: {build: .}}\n",
		dockerSettings, nil, deployment.ComposeNetworks{Home: "paas-network"}, nil, nil, rollout.Hooks{Log: func(string) {}})
	require.Error(t, err, "the fake's up fails")

	raw, err := os.ReadFile(filepath.Join(bin, "envs"))
	require.NoError(t, err)
	var ensured, built bool
	for _, block := range strings.Split(string(raw), "== ")[1:] {
		cmd, env, _ := strings.Cut(block, "\n")
		noDockerSettings(t, cmd, env)
		vars := map[string]string{}
		for _, line := range strings.Split(env, "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				vars[k] = v
			}
		}
		assert.NotContains(t, vars, "BUILDX_CONFIG", cmd)
		assert.NotContains(t, vars, "BUILDKIT_HOST", cmd)
		assert.Equal(t, "unix:///var/run/docker.sock", vars["DOCKER_HOST"], cmd)
		assert.NotContains(t, vars, "APP_KEY", "the service's variables are in no command's environment: "+cmd)
		_, ofEnsure := vars["TOFT_BUILDER"]
		switch {
		case ofEnsure:
			ensured = true
			assert.NotContains(t, vars, "BUILDX_BUILDER", cmd)
		case strings.Contains(cmd, " up --no-start "):
			built = true
			assert.Equal(t, "toft-build", vars["BUILDX_BUILDER"], cmd)
			assert.Equal(t, "1", vars["DOCKER_BUILDKIT"], cmd)
			assert.Equal(t, "false", vars["BUILDX_GIT_INFO"], "buildx runs no git in the repository: "+cmd)
			assert.Equal(t, "false", vars["BUILDX_GIT_LABELS"], cmd)
			assert.Contains(t, cmd, " --build")
		}
	}
	assert.True(t, ensured, "the builder was made sure of")
	assert.True(t, built, "the build ran")
	file, err := os.ReadFile(filepath.Join(bin, "envfile"))
	require.NoError(t, err, "compose is given the service's variables as a file")
	assert.Equal(t, "APP_KEY=\"1\"\n", string(file))
}

// On a server, over SSH and through its agent, the service's variables go
// in the project's .env and never in a command's environment: neither
// holds a setting of the docker CLI's.
func TestRemoteComposeBuildTakesNoDockerSettingsFromTheService(t *testing.T) {
	release := rollout.ComposeDir(slotSvcID) + "/r-abc"
	answers := func(_ context.Context, cmd string) (string, error) {
		if strings.Contains(cmd, "mktemp -d") {
			return release + "\n", nil
		}
		return "", nil
	}
	hooks := rollout.Hooks{Log: func(string) {}, HandOff: func() error { return nil }}

	h := &fakeHost{docker: fakedocker.New(t), stack: stackBuilds, exec: answers}
	require.NoError(t, newTestRemote(h).RunCompose(context.Background(), "dep-1", slotSvcID, "web", "services: {web: {build: .}}\n",
		dockerSettings, nil, deployment.ComposeNetworks{Home: "paas-network"}, nil, nil, hooks))
	var ensured, built bool
	for _, c := range h.recorded() {
		noDockerSettings(t, "over SSH", c.cmd)
		assert.NotContains(t, c.cmd, "APP_KEY", "the service's variables are in no command")
		if strings.Contains(c.cmd, "TOFT_BUILDER=") || strings.Contains(c.cmd, " up --no-start ") {
			assert.Contains(t, c.cmd, remoteCleanEnvPrefix, "in the clean environment")
			ensured = ensured || strings.Contains(c.cmd, "TOFT_BUILDER=toft-build ")
			built = built || strings.Contains(c.cmd, " BUILDX_BUILDER=toft-build DOCKER_BUILDKIT=1 [lock toft-build] docker compose ")
		}
	}
	assert.True(t, ensured, "the builder was made sure of")
	assert.True(t, built, "the build ran on the builder")
	assert.Equal(t, "APP_KEY=\"1\"\n", h.uploads[shellEscape(release+"/.env")])

	var req rollout.Request
	h = &fakeHost{docker: fakedocker.New(t), exec: answers}
	h.subsystem = func(_, stdin string, lineFn func(string)) error {
		require.NoError(t, json.Unmarshal([]byte(stdin), &req))
		lineFn(event(t, rollout.Event{Result: &rollout.Result{Changed: true}}))
		return nil
	}
	require.NoError(t, agentOrchestrator(t, h).RunCompose(t.Context(), "dep-1", slotSvcID, "web", "services: {web: {build: .}}\n",
		dockerSettings, nil, deployment.ComposeNetworks{Home: "paas-network"}, nil, nil, hooks))
	require.NotNil(t, req.Compose)
	assert.Equal(t, "APP_KEY=\"1\"\n", string(req.Compose.DotEnv), "the agent's .env")
	// The agent makes the builder the panel's own builds on the server use.
	assert.Equal(t, &builder.Spec{Name: "toft-build", Bridge: "toft-ubuild", Image: builder.Image, Cache: "10%"}, req.Compose.Builder)
	for _, c := range h.recorded() {
		noDockerSettings(t, "to the agent's server", c.cmd)
	}
}

// A build from a Dockerfile, on the panel's host and on a server, takes
// the service's variables as its arguments, never in its environment.
func TestBuildTakesNoDockerSettingsFromItsArguments(t *testing.T) {
	args := map[string]*string{}
	for k, v := range dockerSettings {
		args[k] = &v
	}

	bin := fakeDocker(t)
	t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o600))
	require.NoError(t, (&LocalOrchestrator{builder: testBuilder}).localBuild(context.Background(), dir, "Dockerfile", testImageTag, args, nil, func(string) {}))
	env, err := os.ReadFile(filepath.Join(bin, "env"))
	require.NoError(t, err)
	noDockerSettings(t, "the build's environment", string(env))
	assert.NotContains(t, string(env), "APP_KEY")
	ran, err := os.ReadFile(filepath.Join(bin, "args"))
	require.NoError(t, err)
	assert.Contains(t, string(ran), "--builder\ntoft-build\n")
	assert.Contains(t, string(ran), "--build-arg\nBUILDX_BUILDER=default\n", "an argument of the Dockerfile's, which chooses no builder")
}
