package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/rollout/fakedocker"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/pkg/sshpool"
	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// call is one command a fakeHost received, with whether its ctx was already
// done when it arrived.
type call struct {
	kind     string // exec, stream, upload
	cmd      string
	ctxEnded bool
}

// fakeHost is a remoteHost that records calls. exec answers Exec (nil: ""),
// stream runs ExecStream (nil: returns nil), docker serves DockerClient
// (nil: an error).
type fakeHost struct {
	mu     sync.Mutex
	calls  []call
	inputs []string // stdin of each ExecInput, in order
	exec   func(ctx context.Context, cmd string) (string, error)
	stream func(ctx context.Context, cmd string) error
	docker *fakedocker.Docker
	// subsystem answers SubsystemStream (nil: an error).
	subsystem func(name, stdin string, lineFn func(string)) error
}

func (f *fakeHost) SubsystemStream(ctx context.Context, name string, stdin io.Reader, lineFn func(string)) error {
	f.record(ctx, "subsystem", name)
	if f.subsystem == nil {
		return errors.New("not an agent")
	}
	in, _ := io.ReadAll(stdin)
	return f.subsystem(name, string(in), lineFn)
}

func (f *fakeHost) record(ctx context.Context, kind, cmd string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{kind: kind, cmd: cmd, ctxEnded: ctx.Err() != nil})
}

func (f *fakeHost) Exec(ctx context.Context, cmd string) (string, error) {
	f.record(ctx, "exec", cmd)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if f.exec == nil {
		return "", nil
	}
	return f.exec(ctx, cmd)
}

func (f *fakeHost) ExecInput(ctx context.Context, cmd string, stdin io.Reader) (string, error) {
	in, _ := io.ReadAll(stdin)
	f.mu.Lock()
	f.inputs = append(f.inputs, string(in))
	f.mu.Unlock()
	return f.Exec(ctx, cmd)
}

func (f *fakeHost) ExecStream(ctx context.Context, cmd string, _ func(string)) error {
	f.record(ctx, "stream", cmd)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if f.stream == nil {
		return nil
	}
	return f.stream(ctx, cmd)
}

func (f *fakeHost) Upload(ctx context.Context, remotePath string, _ []byte) error {
	f.record(ctx, "upload", remotePath)
	return ctx.Err()
}

func (f *fakeHost) DockerClient() (*dockerclient.Client, error) {
	if f.docker == nil {
		return nil, errors.New("no docker in tests")
	}
	return f.docker.Client(), nil
}

func (f *fakeHost) recorded() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.calls...)
}

func (f *fakeHost) find(fragment string) (call, bool) {
	for _, c := range f.recorded() {
		if strings.Contains(c.cmd, fragment) {
			return c, true
		}
	}
	return call{}, false
}

// blockUntilDone is a stream that runs until ctx ends, like docker logs -f.
func blockUntilDone(ctx context.Context, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

func newTestRemote(h *fakeHost) *RemoteOrchestrator {
	return newRemoteOrchestrator(h, rollout.NetConfig{}, zap.NewNop())
}

// A base directory that leaves the build folder (a row saved before it was
// checked) fails every build before any command runs, on the server or here.
func TestBuildsRefuseABaseDirectoryOutsideTheBuildFolder(t *testing.T) {
	const evil = "../../var/lib/docker/volumes"
	ctx := context.Background()
	h := &fakeHost{}
	o := newTestRemote(h)
	log := func(string) {}
	nixpacks := []*deployment.NixpacksCommands{nil, {}}

	_, err := o.BuildFromGit(ctx, "https://example.com/r.git", "main", evil, "img:1", nil, log)
	assert.ErrorIs(t, err, service.ErrInvalidConfig, "BuildFromGit")
	_, err = o.BuildFromGitNixpacks(ctx, "https://example.com/r.git", "main", evil, "img:1", deployment.NixpacksCommands{}, nil, log)
	assert.ErrorIs(t, err, service.ErrInvalidConfig, "BuildFromGitNixpacks")
	for _, nix := range nixpacks {
		err = o.BuildFromArchive(ctx, strings.NewReader("tar"), evil, "img:1", nix, nil, log)
		assert.ErrorIs(t, err, service.ErrInvalidConfig, "BuildFromArchive")
	}
	assert.Empty(t, h.recorded(), "no command reached the server")

	l := &LocalOrchestrator{log: zap.NewNop()}
	_, err = l.BuildFromGit(ctx, "https://example.com/r.git", "main", evil, "img:1", nil, log)
	assert.ErrorIs(t, err, service.ErrInvalidConfig, "local BuildFromGit")
	_, err = l.BuildFromGitNixpacks(ctx, "https://example.com/r.git", "main", evil, "img:1", deployment.NixpacksCommands{}, nil, log)
	assert.ErrorIs(t, err, service.ErrInvalidConfig, "local BuildFromGitNixpacks")
	for _, nix := range nixpacks {
		err = l.BuildFromArchive(ctx, strings.NewReader("tar"), evil, "img:1", nix, nil, log)
		assert.ErrorIs(t, err, service.ErrInvalidConfig, "local BuildFromArchive")
	}
}

func TestRemoteStreamLogsReturnsWhenTheViewerLeaves(t *testing.T) {
	h := &fakeHost{stream: blockUntilDone}
	o := newTestRemote(h)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- o.StreamLogs(ctx, "svc-1", 100, "paas-svc-blue", func(string) {}) }()

	require.Eventually(t, func() bool { _, ok := h.find("docker logs --follow"); return ok }, time.Second, time.Millisecond)
	cancel()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("StreamLogs kept running after its ctx ended")
	}
}

// A line over the cap is cut with a marker and the follow goes on (logs-9).
func TestLocalStreamLogsKeepsGoingPastALongLine(t *testing.T) {
	f := fakedocker.New(t)
	f.Add("web", container.StateExited, nil)
	f.Log("web", "2026-11-09T10:00:00Z "+strings.Repeat("x", 1<<20+10), "2026-11-09T10:00:01Z after")
	o := &LocalOrchestrator{dockerClient: f.Client(), log: zap.NewNop()}

	var lines []string
	require.NoError(t, o.StreamLogs(context.Background(), "svc-1", 100, "web", func(l string) { lines = append(lines, l) }))

	require.Len(t, lines, 2)
	assert.True(t, strings.HasSuffix(lines[0], "… (line cut at 1 MiB)"), "the cut line says so")
	assert.Equal(t, "2026-11-09T10:00:01Z after", lines[1])
}

// A long line of a build's output used to fail the build (bufio.Scanner's 64 KiB).
func TestStreamBuildOutput_KeepsGoingPastALongLine(t *testing.T) {
	msg := strings.Repeat("x", 70_000)
	long, err := json.Marshal(map[string]string{"stream": msg})
	require.NoError(t, err)
	overCap := `{"stream":"` + strings.Repeat("y", 1<<20+10) + `"}`

	logged := make([]string, 0, 3)
	err = streamBuildOutput(strings.NewReader(string(long)+"\n"+overCap+"\n"+`{"stream":"next\n"}`+"\n"+`{"error":"boom"}`+"\n"+`{"stream":"after the error\n"}`+"\n"), func(l string) { logged = append(logged, l) })

	require.EqualError(t, err, "boom", "an error line still fails the build")
	require.Len(t, logged, 3)
	assert.Equal(t, msg, logged[0])
	assert.True(t, strings.HasSuffix(logged[1], "… (line cut at 1 MiB)"), "a JSON line past the cap reads as plain text")
	assert.Equal(t, "next", logged[2])
}

func TestRemoteBuildCleansUpAfterCancel(t *testing.T) {
	h := &fakeHost{
		exec: func(_ context.Context, cmd string) (string, error) {
			if strings.HasPrefix(cmd, "mktemp") {
				return "/tmp/paas-build-abc123", nil
			}
			return "", nil
		},
		stream: blockUntilDone, // the clone runs until the deploy is abandoned
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := newTestRemote(h).BuildFromGit(ctx, "https://example.com/r.git", "main", "", "paas/x:1", nil, func(string) {})
		done <- err
	}()
	require.Eventually(t, func() bool { _, ok := h.find("git clone"); return ok }, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)

	rm, ok := h.find("rm -rf /tmp/paas-build-abc123")
	require.True(t, ok, "the temp dir is removed")
	assert.False(t, rm.ctxEnded, "the cleanup runs on a live ctx after the cancel")
}

func TestRemoteRunComposeUploadsPrivatelyUnderCtx(t *testing.T) {
	const release = "/var/paas/compose/svc-1/r-abc"
	h := &fakeHost{docker: fakedocker.New(t), exec: func(_ context.Context, cmd string) (string, error) {
		if strings.Contains(cmd, "mktemp -d") {
			return release + "\n", nil
		}
		return "", nil
	}}
	err := newTestRemote(h).RunCompose(context.Background(), "dep-1", "svc-1", "web", "services: {}\n",
		map[string]string{"A": "1"}, nil, deployment.ComposeNetworks{}, nil, nil, rollout.Hooks{Log: func(string) {}})
	require.NoError(t, err)

	var seq []string
	for _, c := range h.recorded() {
		seq = append(seq, c.kind+" "+c.cmd)
	}
	joined := strings.Join(seq, "\n")
	assert.Equal(t, "exec mkdir -p '/var/paas/compose/svc-1' && mktemp -d '/var/paas/compose/svc-1'/r-XXXXXXXX", seq[0],
		"a new release directory per deploy, private (mktemp -d)")
	// uploadPrivate, line for line: create empty and private, upload through
	// stdin to the quoted path, chmod.
	assert.Contains(t, joined, "exec umask 077 && rm -f '"+release+"/docker-compose.yml' && : > '"+release+"/docker-compose.yml'\nupload '"+release+"/docker-compose.yml'\nexec chmod 600 '"+release+"/docker-compose.yml'")
	assert.Contains(t, joined, "upload '"+release+"/.env'")

	create, ok := h.find("up --no-start")
	require.True(t, ok)
	assert.Contains(t, create.cmd, "--project-name 'paas-web-svc-1' up --no-start --pull always --remove-orphans 2>&1",
		"updated in place: no --force-recreate")
	up, ok := h.find("up -d")
	require.True(t, ok)
	assert.Contains(t, up.cmd, "--project-name 'paas-web-svc-1' up -d --pull never --no-build --no-recreate --remove-orphans 2>&1",
		"what starts is what was created and checked")
	_, downCurrent := h.find("--project-name 'paas-web-svc-1' down")
	assert.False(t, downCurrent, "the running stack is not taken down first")
	downLegacy, ok := h.find("--project-name 'paas-svc-1' down")
	assert.True(t, ok, "a stack under the legacy project name is")
	assert.Equal(t, "exec", downLegacy.kind,
		"its output stays out of the deploy log: without the .env it warns of every variable")

	assert.Contains(t, joined, "exec find '/var/paas/compose/svc-1' -mindepth 1 -maxdepth 1 ! -path '"+release+"' -exec rm -rf {} +",
		"older releases are removed after a successful up")
	assert.Equal(t, "exec rm -f '"+release+"/.env'", seq[len(seq)-1], "the .env file is removed last")
	_, removed := h.find("rm -rf '" + release + "'")
	assert.False(t, removed, "the release itself stays: bind mounts point into it")
}

func TestRemoteRunComposeTakesDownALegacyStackWhoseImageRoutesItself(t *testing.T) {
	f := fakedocker.New(t)
	proj := rollout.ComposeProjectName("svc-1", "web")
	f.Add(proj+"-worker-1", container.StateRunning, map[string]string{
		rollout.LabelComposeProject: proj, "com.docker.compose.service": "worker",
		"traefik.enable": "true", "traefik.http.routers.x.rule": "Host(`panel.example.com`)",
	})
	h := &fakeHost{docker: f, exec: func(_ context.Context, cmd string) (string, error) {
		if strings.Contains(cmd, "mktemp -d") {
			return "/var/paas/compose/svc-1/r-abc\n", nil
		}
		return "", nil
	}}
	err := newTestRemote(h).RunCompose(context.Background(), "dep-1", "svc-1", "web", "services: {}\n",
		nil, nil, deployment.ComposeNetworks{Home: "paas-network"}, nil, nil, rollout.Hooks{Log: func(string) {}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), proj+"-worker-1: traefik.enable")
	_, down := h.find("--project-name '" + proj + "' down")
	assert.True(t, down, "the stack is taken down under legacy too")
}

// A routed service built during up, whose image adds routers of its own, is
// refused on its created container before anything starts; and an up that
// fails after starting such a container still has the stack checked and
// taken down.
func TestRemoteRunComposeChecksCreatedContainersAndFailedUps(t *testing.T) {
	route := &deployment.ComposeRoute{Service: "web", Port: 8080, Domains: []string{"app.example.com"}}
	evil := map[string]string{"traefik.enable": "true", "traefik.http.routers.paas-" + slotSvcID[:8] + "-x.rule": "Host(`victim.example`)"}
	run := func(failStart bool) (*fakeHost, error) {
		f := fakedocker.New(t)
		proj := rollout.ComposeProjectName(slotSvcID, "web")
		labels := map[string]string{rollout.LabelComposeProject: proj, "com.docker.compose.service": "web"}
		for k, v := range evil {
			labels[k] = v
		}
		h := &fakeHost{docker: f, exec: func(_ context.Context, cmd string) (string, error) {
			if strings.Contains(cmd, "mktemp -d") {
				return "/var/paas/compose/" + slotSvcID + "/r-abc\n", nil
			}
			return "", nil
		}, stream: func(_ context.Context, cmd string) error {
			switch {
			case strings.Contains(cmd, " up --no-start ") && !failStart:
				f.Add(proj+"-web-1", container.StateCreated, labels)
			case strings.Contains(cmd, " up -d ") && failStart:
				f.Add(proj+"-web-1", container.StateRunning, labels)
				return errors.New("dependency failed to start")
			}
			return nil
		}}
		err := newTestRemote(h).RunCompose(context.Background(), "dep-1", slotSvcID, "web", "services: {web: {build: .}}\n",
			nil, route, deployment.ComposeNetworks{Home: "paas-network"}, nil, nil, rollout.Hooks{Log: func(string) {}})
		return h, err
	}
	proj := rollout.ComposeProjectName(slotSvcID, "web")

	h, err := run(false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "traefik.http.routers.paas-"+slotSvcID[:8]+"-x.rule")
	_, started := h.find(" up -d ")
	assert.False(t, started, "nothing starts once a created container fails the check")
	_, down := h.find("--project-name '" + proj + "' down")
	assert.True(t, down)

	h, err = run(true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "traefik.http.routers.paas-"+slotSvcID[:8]+"-x.rule")
	_, down = h.find("--project-name '" + proj + "' down")
	assert.True(t, down, "a failed up's stack is checked and taken down")
}

// A create that fails (a pull or a build) has recreated nothing: the running
// stack still carries the last good deploy's routes. The error is the
// create's, and the stack is neither checked nor taken down.
func TestRemoteRunComposeFailedCreateLeavesTheRunningStackAlone(t *testing.T) {
	route := &deployment.ComposeRoute{Service: "web", Port: 8080, Domains: []string{"app.example.com"}}
	f := fakedocker.New(t)
	proj := rollout.ComposeProjectName(slotSvcID, "web")
	f.Add(proj+"-web-1", container.StateRunning, map[string]string{
		rollout.LabelComposeProject: proj, "com.docker.compose.service": "web",
		"traefik.enable": "true", "traefik.http.routers.paas-" + slotSvcID[:8] + "-old.rule": "Host(`old.example.com`)",
	})
	h := &fakeHost{docker: f, exec: func(_ context.Context, cmd string) (string, error) {
		if strings.Contains(cmd, "mktemp -d") {
			return "/var/paas/compose/" + slotSvcID + "/r-abc\n", nil
		}
		return "", nil
	}, stream: func(_ context.Context, cmd string) error {
		if strings.Contains(cmd, " up --no-start ") {
			return errors.New("pull access denied")
		}
		return nil
	}}
	err := newTestRemote(h).RunCompose(context.Background(), "dep-1", slotSvcID, "web", "services: {web: {build: .}}\n",
		nil, route, deployment.ComposeNetworks{Home: "paas-network"}, nil, nil, rollout.Hooks{Log: func(string) {}})
	require.EqualError(t, err, "pull access denied")
	_, started := h.find(" up -d ")
	assert.False(t, started)
	_, down := h.find("--project-name '" + proj + "' down")
	assert.False(t, down, "the running stack is not taken down")
}

// The routed service's image is pulled and read before up, so an image that
// routes itself is refused while the running stack keeps serving. A build
// (no image to pull) and an image without such labels go ahead.
func TestRemoteRunComposeRefusesARoutedImageThatRoutesItselfBeforeUp(t *testing.T) {
	route := &deployment.ComposeRoute{Service: "web", Port: 8080, Domains: []string{"app.example.com"}}
	run := func(image string, pullFails bool) (*fakeHost, error) {
		f := fakedocker.New(t)
		f.SetImageLabels("evil:1", map[string]string{"traefik.http.routers.x.rule": "Host(`panel.example.com`)"})
		f.SetImageLabels("clean:1", map[string]string{"maintainer": "x"})
		h := &fakeHost{docker: f, exec: func(_ context.Context, cmd string) (string, error) {
			switch {
			case strings.Contains(cmd, "mktemp -d"):
				return "/var/paas/compose/" + slotSvcID + "/r-abc\n", nil
			case strings.Contains(cmd, " config --images 'web'"):
				return image + "\n", nil
			case strings.HasPrefix(cmd, "docker pull") && pullFails:
				return "", errors.New("pull access denied")
			}
			return "", nil
		}}
		err := newTestRemote(h).RunCompose(context.Background(), "dep-1", slotSvcID, "web", "services: {web: {image: x}}\n",
			nil, route, deployment.ComposeNetworks{Home: "paas-network"}, nil, nil, rollout.Hooks{Log: func(string) {}})
		return h, err
	}

	h, err := run("evil:1", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "traefik.http.routers.x.rule")
	assert.Contains(t, err.Error(), "nothing was started")
	_, up := h.find(" up -d ")
	assert.False(t, up, "up never runs")
	_, down := h.find("--project-name '" + rollout.ComposeProjectName(slotSvcID, "web") + "' down")
	assert.False(t, down, "the running stack is not taken down")
	_, pulled := h.find("docker pull --quiet 'evil:1'")
	assert.True(t, pulled)

	for _, tc := range []struct {
		image     string
		pullFails bool
	}{{"clean:1", false}, {"paas-web-svc1-web", true}} {
		h, _ := run(tc.image, tc.pullFails)
		_, up := h.find(" up -d ")
		assert.True(t, up, "%s goes on to up", tc.image)
	}
}

func TestRemoteRunComposeKeepsOlderReleasesWhenUpFails(t *testing.T) {
	h := &fakeHost{
		exec: func(_ context.Context, cmd string) (string, error) {
			if strings.Contains(cmd, "mktemp -d") {
				return "/var/paas/compose/svc-1/r-new", nil
			}
			return "", nil
		},
		stream: func(_ context.Context, cmd string) error {
			if strings.Contains(cmd, " up -d ") {
				return errors.New("exit status 1")
			}
			return nil
		},
	}
	err := newTestRemote(h).RunCompose(context.Background(), "dep-1", "svc-1", "web", "services: {}\n",
		nil, nil, deployment.ComposeNetworks{}, nil, nil, rollout.Hooks{Log: func(string) {}})
	require.Error(t, err)
	_, pruned := h.find("find '/var/paas/compose/svc-1'")
	assert.False(t, pruned, "the running stack may still use an older release")
	_, envRemoved := h.find("rm -f '/var/paas/compose/svc-1/r-new/.env'")
	assert.True(t, envRemoved, "the .env file is removed even when up fails")
}

func TestRemoteStopComposeRemovesTheReleases(t *testing.T) {
	h := &fakeHost{exec: func(_ context.Context, cmd string) (string, error) {
		if strings.HasPrefix(cmd, "mktemp") {
			return "/tmp/paas-compose-xyz", nil
		}
		return "", nil
	}}
	_ = newTestRemote(h).StopCompose(context.Background(), "svc-1", "web", "services: {}\n")
	_, ok := h.find("rm -rf '/var/paas/compose/svc-1'")
	assert.True(t, ok)
	// Down goes by project name and never reads the stack's file, so a
	// ${TOFT_URL:?…} with no value can't make it fail.
	down, ok := h.find("down --remove-orphans")
	assert.True(t, ok)
	assert.Contains(t, down.cmd, "--project-directory '/tmp/paas-compose-xyz' --project-name")
	assert.NotContains(t, down.cmd, " -f ")
}

// providerFunc is an SSHProvider.
type providerFunc func(srv *server.Server) sshpool.RemoteHost

func (f providerFunc) SSH(srv *server.Server) sshpool.RemoteHost { return f(srv) }

// ensureOnly is a RemoteHost whose Ensure is scripted; nothing else is used.
type ensureOnly struct {
	sshpool.RemoteHost
	err      error
	deadline bool
}

func (e *ensureOnly) Ensure(ctx context.Context, opts sshpool.EnsureOpts) (sshpool.ConnInfo, error) {
	_, e.deadline = ctx.Deadline()
	if opts.Force {
		return sshpool.ConnInfo{}, errors.New("a deploy must not bypass the backoff")
	}
	return sshpool.ConnInfo{}, e.err
}

func TestFactoryForServer(t *testing.T) {
	srv := &server.Server{ID: "srv-1", Name: "web-1", IPAddress: "203.0.113.7"}

	t.Run("local without a server", func(t *testing.T) {
		f := NewFactory(nil, rollout.NetConfig{}, nil, zap.NewNop())
		orch, cleanup, err := f.ForServer(nil)
		require.NoError(t, err)
		assert.Equal(t, f.local, orch)
		cleanup()
	})

	t.Run("remote over the pooled connection", func(t *testing.T) {
		h := &ensureOnly{}
		f := NewFactory(nil, rollout.NetConfig{}, providerFunc(func(*server.Server) sshpool.RemoteHost { return h }), zap.NewNop())
		orch, cleanup, err := f.ForServer(srv)
		require.NoError(t, err)
		remote, ok := orch.(*RemoteOrchestrator)
		require.True(t, ok)
		assert.Equal(t, h, remote.host)
		assert.True(t, h.deadline, "the connect is bounded")
		cleanup() // a no-op: the pool keeps the connection
	})

	t.Run("an unreachable server fails fast with its name", func(t *testing.T) {
		h := &ensureOnly{err: errors.New("connection refused")}
		f := NewFactory(nil, rollout.NetConfig{}, providerFunc(func(*server.Server) sshpool.RemoteHost { return h }), zap.NewNop())
		_, _, err := f.ForServer(srv)
		require.Error(t, err)
		assert.Equal(t, `SSH connect to server "web-1" (203.0.113.7): connection refused`, err.Error())
	})

	t.Run("no SSH provider", func(t *testing.T) {
		_, _, err := NewFactory(nil, rollout.NetConfig{}, nil, zap.NewNop()).ForServer(srv)
		assert.Error(t, err)
	})
}

func TestRemoteRegistryLoginSendsThePasswordOnStdin(t *testing.T) {
	h := &fakeHost{}
	o := newRemoteOrchestrator(h, rollout.NetConfig{}, zap.NewNop())
	auth := &rollout.RegistryAuth{Server: "ghcr.io", Username: "bot", Password: "hunter2"}
	require.NoError(t, o.pull(context.Background(), "ghcr.io/acme/app:1", auth, func(string) {}))

	for _, c := range h.recorded() {
		assert.NotContains(t, c.cmd, "hunter2", "the password reached a command line: %s", c.cmd)
	}
	assert.Equal(t, []string{"hunter2"}, h.inputs)
	login, ok := h.find("docker login")
	require.True(t, ok)
	assert.Equal(t, "docker login 'ghcr.io' -u 'bot' --password-stdin 2>&1", login.cmd)
	_, ok = h.find("docker logout 'ghcr.io'")
	assert.True(t, ok, "logged out afterwards")
}

// User input (image, base directory, health-check path, container name)
// reaches remote shell commands quoted, so it can't end the command.
func TestRemoteCommandsQuoteUserInput(t *testing.T) {
	const evil = "x'; touch /tmp/pwned; echo '"
	h := &fakeHost{}
	o := newRemoteOrchestrator(h, rollout.NetConfig{}, zap.NewNop())
	ctx := context.Background()

	_ = o.pull(ctx, evil, nil, func(string) {})
	_ = o.ExecInContainer(ctx, "svc", evil, "true", func(string) {})
	_ = o.StreamLogs(ctx, "svc", 10, evil, func(string) {})
	_, _ = o.BuildFromGitNixpacks(ctx, "https://example.com/r.git", "main", evil, "img:1",
		deployment.NixpacksCommands{Install: evil, Build: evil}, nil, func(string) {})

	cmds := h.recorded()
	require.NotEmpty(t, cmds)
	for _, c := range cmds {
		if !strings.Contains(c.cmd, "pwned") {
			continue
		}
		// shellEscape turns each ' into '\'', so the raw "x'; touch" never
		// appears once the payload is quoted.
		assert.NotContains(t, c.cmd, "x'; touch", "unquoted payload in: %s", c.cmd)
	}
}

func TestRemoteCloneKeepsTheTokenOffTheCommandLine(t *testing.T) {
	// The second is a token saved without a username.
	for _, repo := range []string{"https://bot:ghp_secret@github.com/acme/app.git", "https://ghp_secret@github.com/acme/app.git"} {
		h := &fakeHost{exec: func(_ context.Context, cmd string) (string, error) {
			if strings.HasPrefix(cmd, "mktemp") {
				return "/tmp/paas-build-abc", nil
			}
			return "", nil
		}}
		o := newRemoteOrchestrator(h, rollout.NetConfig{}, zap.NewNop())
		_, _ = o.BuildFromGit(context.Background(), repo, "main", "", "img:1", nil, func(string) {})

		for _, c := range h.recorded() {
			assert.NotContains(t, c.cmd, "ghp_secret", "the token reached a command line: %s", c.cmd)
		}
		clone, ok := h.find("git -c credential.helper=")
		require.True(t, ok, repo)
		assert.Contains(t, clone.cmd, "'https://github.com/acme/app.git'")
		_, ok = h.find("rm -f '/tmp/paas-build-abc.git-credentials'")
		assert.True(t, ok, "the credential file is removed")
	}
}

// A remote build reads the commit its clone checked out on the server, in
// the clone's directory, and returns it also when the build then fails.
func TestRemoteBuildReturnsTheClonedCommit(t *testing.T) {
	sha := strings.Repeat("d", 40)
	for _, nixpacks := range []bool{false, true} {
		h := &fakeHost{
			exec: func(_ context.Context, cmd string) (string, error) {
				switch {
				case strings.HasPrefix(cmd, "mktemp"):
					return "/tmp/paas-build-abc", nil
				case strings.HasPrefix(cmd, "git -C "):
					return sha + "\nFix nil pointer in cart totals", nil
				}
				return "", nil
			},
			stream: func(_ context.Context, cmd string) error {
				if strings.Contains(cmd, "build") && !strings.Contains(cmd, "clone") {
					return errors.New("exit status 1")
				}
				return nil
			},
		}
		o := newTestRemote(h)
		var lines []string
		build := o.BuildFromGit
		if nixpacks {
			build = func(ctx context.Context, repo, branch, dir, tag string, args map[string]*string, logFn func(string)) (deployment.Commit, error) {
				return o.BuildFromGitNixpacks(ctx, repo, branch, dir, tag, deployment.NixpacksCommands{}, args, logFn)
			}
		}
		c, err := build(context.Background(), "https://example.com/r.git", "main", "", "img:1", nil, func(l string) { lines = append(lines, l) })

		assert.Error(t, err, "the build failed")
		assert.Equal(t, deployment.Commit{SHA: sha, Subject: "Fix nil pointer in cart totals"}, c)
		assert.Contains(t, lines, "Cloned main at ddddddd")
		head, ok := h.find("git -C ")
		require.True(t, ok)
		assert.Equal(t, "git -C '/tmp/paas-build-abc' log -1 '--format=%H%n%s'", head.cmd)

		// .git goes after the commit is read and before the build.
		var order []string
		for _, c := range h.recorded() {
			switch {
			case strings.HasPrefix(c.cmd, "git -C "):
				order = append(order, "head")
			case c.cmd == "rm -rf '/tmp/paas-build-abc/.git'":
				order = append(order, "drop .git")
			case strings.Contains(c.cmd, " build ") && !strings.Contains(c.cmd, "clone"):
				order = append(order, "build")
			}
		}
		assert.Equal(t, []string{"head", "drop .git", "build"}, order)
	}
}

// Nixpacks gets the service's install and build commands, quoted, and none
// when they are empty.
func TestRemoteNixpacksCommands(t *testing.T) {
	newHost := func() *fakeHost {
		return &fakeHost{exec: func(_ context.Context, cmd string) (string, error) {
			if strings.HasPrefix(cmd, "mktemp") {
				return "/tmp/paas-build-abc", nil
			}
			return "", nil
		}}
	}
	h := newHost()
	o := newRemoteOrchestrator(h, rollout.NetConfig{}, zap.NewNop())
	_, _ = o.BuildFromGitNixpacks(context.Background(), "https://example.com/r.git", "main", "", "img:1",
		deployment.NixpacksCommands{Install: "npm ci", Build: "npm run build"}, nil, func(string) {})
	build, ok := h.find("nixpacks build")
	require.True(t, ok)
	assert.Contains(t, build.cmd, "'--install-cmd' 'npm ci'")
	assert.Contains(t, build.cmd, "'--build-cmd' 'npm run build'")

	h = newHost()
	o = newRemoteOrchestrator(h, rollout.NetConfig{}, zap.NewNop())
	_, _ = o.BuildFromGitNixpacks(context.Background(), "https://example.com/r.git", "main", "", "img:1",
		deployment.NixpacksCommands{}, nil, func(string) {})
	build, _ = h.find("nixpacks build")
	assert.NotContains(t, build.cmd, "--install-cmd", "Nixpacks keeps what it detects")
}

func TestRemoteStatsReadsStartAndRestarts(t *testing.T) {
	h := &fakeHost{exec: func(_ context.Context, cmd string) (string, error) {
		return "0.10% 45.2MiB / 1.95GiB\n2026-10-14T16:47:34.748531544Z 2\n", nil
	}}
	o := newRemoteOrchestrator(h, rollout.NetConfig{}, zap.NewNop())
	st, err := o.Stats(context.Background(), "svc-1", "paas-production-api-svc1-green")
	require.NoError(t, err)
	assert.InDelta(t, 0.10, st.CPUPercent, 0.001)
	require.NotNil(t, st.StartedAt)
	assert.Equal(t, time.Date(2026, 10, 14, 16, 47, 34, 748531544, time.UTC), st.StartedAt.UTC())
	assert.Equal(t, 2, st.RestartCount)

	call, ok := h.find("docker inspect")
	require.True(t, ok)
	assert.Contains(t, call.cmd, "'paas-production-api-svc1-green'", "the name is quoted")
}

func TestStartedAtIsNilForAContainerThatNeverStarted(t *testing.T) {
	assert.Nil(t, startedAt("0001-01-01T00:00:00Z"))
	assert.Nil(t, startedAt(""))
	assert.NotNil(t, startedAt("2026-10-14T16:47:34Z"))
}

// The URL and the directory follow "--", so a URL that starts with "-" is a
// clone target that fails, never a git option.
func TestRemoteCloneEndsOptionsBeforeTheURL(t *testing.T) {
	h := &fakeHost{exec: func(_ context.Context, cmd string) (string, error) {
		if strings.HasPrefix(cmd, "mktemp") {
			return "/tmp/paas-build-abc", nil
		}
		return "", nil
	}}
	o := newRemoteOrchestrator(h, rollout.NetConfig{}, zap.NewNop())
	_, _ = o.BuildFromGitNixpacks(context.Background(), "--upload-pack=id", "main", "", "img:1",
		deployment.NixpacksCommands{}, nil, func(string) {})
	clone, ok := h.find("git clone")
	require.True(t, ok)
	assert.Contains(t, clone.cmd, "--branch 'main' -- '--upload-pack=id' /tmp/paas-build-abc")
}

// A build from the project's own repository (D-77) sends the tree on
// stdin to tar in a new directory and builds there: no clone, no URL, no
// credential file.
func TestRemoteBuildFromArchive(t *testing.T) {
	h := &fakeHost{exec: func(_ context.Context, cmd string) (string, error) {
		if strings.HasPrefix(cmd, "mktemp") {
			return "/tmp/paas-build-abc123\n", nil
		}
		return "", nil
	}}
	sha := "abc"
	err := newTestRemote(h).BuildFromArchive(context.Background(), strings.NewReader("TAR"), "services/api", "paas/x:1", nil,
		map[string]*string{"TOFT_GIT_SHA": &sha}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	var cmds []string
	for _, c := range h.calls {
		cmds = append(cmds, c.cmd)
	}
	all := strings.Join(cmds, "\n")
	for _, want := range []string{
		"tar -x -o -f - -C '/tmp/paas-build-abc123'",
		"docker build -t paas/x:1 --build-arg TOFT_GIT_SHA='abc' -f '/tmp/paas-build-abc123/services/api/Dockerfile' '/tmp/paas-build-abc123'",
		"rm -rf '/tmp/paas-build-abc123'",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	if strings.Contains(all, "git ") || strings.Contains(all, "credential") {
		t.Errorf("a git command or credential reached the server:\n%s", all)
	}
	if len(h.inputs) != 1 || h.inputs[0] != "TAR" {
		t.Errorf("stdin = %q", h.inputs)
	}
}

func TestRemotePullImageLogsOutAfterCancel(t *testing.T) {
	h := &fakeHost{stream: func(ctx context.Context, cmd string) error {
		if strings.HasPrefix(cmd, "docker pull") {
			return blockUntilDone(ctx, cmd)
		}
		return nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	auth := &rollout.RegistryAuth{Server: "ghcr.io", Username: "u", Password: "p"}
	go func() { done <- newTestRemote(h).pull(ctx, "ghcr.io/o/r:1", auth, func(string) {}) }()
	require.Eventually(t, func() bool { _, ok := h.find("docker pull"); return ok }, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)

	logout, ok := h.find("docker logout 'ghcr.io'")
	require.True(t, ok)
	assert.False(t, logout.ctxEnded)
}

// A pull cut off by a lost connection (an agent reconnecting, D-106) runs
// once more; a pull that fails otherwise doesn't.
func TestPullRetriesOnceWhenTheConnectionDrops(t *testing.T) {
	pulls := 0
	h := &fakeHost{stream: func(context.Context, string) error {
		pulls++
		if pulls == 1 {
			return &sshpool.ConnectionLostError{Err: errors.New("EOF")}
		}
		return nil
	}}
	if err := newTestRemote(h).pull(context.Background(), "nginx:1.27", nil, func(string) {}); err != nil || pulls != 2 {
		t.Fatalf("pull after a drop: err=%v pulls=%d", err, pulls)
	}
	pulls = 0
	h.stream = func(context.Context, string) error { pulls++; return errors.New("manifest unknown") }
	if err := newTestRemote(h).pull(context.Background(), "nginx:nope", nil, func(string) {}); err == nil || pulls != 1 {
		t.Fatalf("a failed pull must not repeat: err=%v pulls=%d", err, pulls)
	}
}

// A remote rollout pulls on the server's docker CLI (tunnelHost), so the
// login stays on stdin and a dropped connection retries the pull.
func TestRemoteRolloutPullsOnTheServer(t *testing.T) {
	h := &fakeHost{docker: fakedocker.New(t), stream: func(_ context.Context, cmd string) error {
		if strings.HasPrefix(cmd, "docker pull") {
			return errors.New("manifest unknown")
		}
		return nil
	}}
	res := newTestRemote(h).Rollout(context.Background(), rollout.Spec{Slot: rollout.Slot{ServiceID: slotSvcID, Image: "ghcr.io/o/r:1"}, Pull: true},
		deployment.Routing{}, rollout.Hooks{})
	assert.Equal(t, rollout.StepPull, res.Step)
	_, ok := h.find("docker pull 'ghcr.io/o/r:1'")
	assert.True(t, ok)
}
