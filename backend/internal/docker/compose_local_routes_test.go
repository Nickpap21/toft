package docker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/rollout/fakedocker"
	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// On the panel host, a routed service whose image adds routers of its own
// is refused on its created container before anything starts; and an up
// that fails after starting such a container still has the stack checked
// and taken down.
func TestLocalComposeChecksCreatedContainersAndFailedUps(t *testing.T) {
	route := &deployment.ComposeRoute{Service: "web", Port: 8080, Domains: []string{"app.example.com"}}
	proj := rollout.ComposeProjectName(testServiceID, "web")
	labels := map[string]string{
		rollout.LabelComposeProject: proj, "com.docker.compose.service": "web",
		"traefik.enable": "true", "traefik.http.routers.paas-" + testServiceID[:8] + "-x.rule": "Host(`victim.example`)",
	}
	run := func(t *testing.T, failStart bool) (string, error) {
		bin := t.TempDir()
		args := filepath.Join(bin, "args")
		script := "#!/bin/sh\necho \"$@\" >> " + args + "\n"
		if failStart {
			script += "case \"$*\" in *' up -d '*) sleep 0.5; exit 1;; esac\n"
		}
		require.NoError(t, os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o700))
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

		f := fakedocker.New(t)
		if failStart {
			go func() {
				for {
					raw, _ := os.ReadFile(args)
					if strings.Contains(string(raw), " up -d ") {
						break
					}
					time.Sleep(time.Millisecond)
				}
				f.Add(proj+"-web-1", container.StateRunning, labels)
			}()
		} else {
			f.Add(proj+"-web-1", container.StateCreated, labels)
		}
		o := &LocalOrchestrator{dataDir: t.TempDir(), dockerClient: f.Client(), log: zap.NewNop()}
		err := o.RunCompose(context.Background(), "dep-1", testServiceID, "web", "services: {web: {build: .}}\n",
			nil, route, deployment.ComposeNetworks{Home: "paas-network"}, nil, nil, rollout.Hooks{Log: func(string) {}})
		raw, _ := os.ReadFile(args)
		return string(raw), err
	}

	t.Run("created container", func(t *testing.T) {
		args, err := run(t, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "traefik.http.routers.paas-"+testServiceID[:8]+"-x.rule")
		assert.Contains(t, args, " up --no-start --pull always --build --remove-orphans\n")
		assert.NotContains(t, args, " up -d ", "nothing starts once a created container fails the check")
		assert.Contains(t, args, "--project-name "+proj+" down --remove-orphans")
	})
	t.Run("failed up", func(t *testing.T) {
		args, err := run(t, true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "traefik.http.routers.paas-"+testServiceID[:8]+"-x.rule")
		assert.Contains(t, args, " up -d --pull never --no-build --no-recreate --remove-orphans\n")
		assert.Contains(t, args, "--project-name "+proj+" down --remove-orphans", "a failed up's stack is checked and taken down")
	})
}

// A create that fails (a pull or a build) has recreated nothing: the running
// stack's containers still carry the routes of the last good deploy, which
// may differ from the new ones. The error is the create's, and the stack is
// neither checked nor taken down.
func TestLocalComposeFailedCreateLeavesTheRunningStackAlone(t *testing.T) {
	route := &deployment.ComposeRoute{Service: "web", Port: 8080, Domains: []string{"app.example.com"}}
	proj := rollout.ComposeProjectName(testServiceID, "web")
	bin := t.TempDir()
	args := filepath.Join(bin, "args")
	script := "#!/bin/sh\necho \"$@\" >> " + args + "\ncase \"$*\" in *' up --no-start '*) echo pull access denied; exit 1;; esac\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	f := fakedocker.New(t)
	f.Add(proj+"-web-1", container.StateRunning, map[string]string{
		rollout.LabelComposeProject: proj, "com.docker.compose.service": "web",
		"traefik.enable": "true", "traefik.http.routers.paas-" + testServiceID[:8] + "-old.rule": "Host(`old.example.com`)",
	})
	o := &LocalOrchestrator{dataDir: t.TempDir(), dockerClient: f.Client(), log: zap.NewNop()}
	err := o.RunCompose(context.Background(), "dep-1", testServiceID, "web", "services: {web: {build: .}}\n",
		nil, route, deployment.ComposeNetworks{Home: "paas-network"}, nil, nil, rollout.Hooks{Log: func(string) {}})
	raw, _ := os.ReadFile(args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "compose up")
	assert.NotContains(t, err.Error(), "-old.rule")
	assert.NotContains(t, string(raw), " up -d ")
	assert.NotContains(t, string(raw), "--project-name "+proj+" down", "the running stack is not taken down")
}
