package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/pkg/reservation"
	"github.com/Nickpap21/toft/backend/pkg/sshpool"
)

// A deploy's orchestrator builds on the builder its deploy names, on the
// panel's host and on a server: the untrusted one for an untrusted
// preview. Only the panel's own host ever builds without asking its
// firewall (the development setting); a server always asks. The
// orchestrator of everything else is left as it was.
func TestForDeployNamesTheDeploysBuilder(t *testing.T) {
	ctx := context.Background()
	srv := &server.Server{ID: "srv-1", Name: "web-1", IPAddress: "203.0.113.7"}
	local := &LocalOrchestrator{log: zap.NewNop(), dataDir: "/data", helperOwner: "h", latencyURL: "http://traefik:9100/metrics", builder: builder.Trusted}
	f := NewFactory(local, rollout.NetConfig{}, providerFunc(func(*server.Server) sshpool.RemoteHost { return &ensureOnly{} }), zap.NewNop())

	for name, tc := range map[string]struct {
		builds deployment.Builds
		want   builder.Spec
	}{
		"a deploy":               {deployment.Builds{}, builder.Spec{Name: "toft-build", Bridge: "toft-ubuild", Firewall: true}},
		"an untrusted preview":   {deployment.Builds{Untrusted: true}, builder.Spec{Name: "toft-build-untrusted", Bridge: "toft-upbuild", Firewall: true}},
		"development":            {deployment.Builds{Unchecked: true}, builder.Spec{Name: "toft-build", Bridge: "toft-ubuild", Firewall: true}},
		"development, untrusted": {deployment.Builds{Untrusted: true, Unchecked: true}, builder.Spec{Name: "toft-build-untrusted", Bridge: "toft-upbuild", Firewall: true}},
	} {
		orch, cleanup, err := f.ForDeploy(ctx, nil, tc.builds)
		require.NoError(t, err, name)
		cleanup()
		got := orch.(*LocalOrchestrator)
		onPanel := tc.want
		onPanel.Unchecked = tc.builds.Unchecked
		assert.Equal(t, onPanel, got.builder, "the panel's host: "+name)
		assert.NotSame(t, local, got, name)
		assert.Equal(t, "/data", got.dataDir)
		assert.Equal(t, "h", got.helperOwner, "its helpers are this process's")
		assert.Empty(t, got.latencyURL, "the collector reads the latency, on the host's own orchestrator")

		orch, cleanup, err = f.ForDeploy(ctx, srv, tc.builds)
		require.NoError(t, err, name)
		cleanup()
		assert.Equal(t, tc.want, orch.(*RemoteOrchestrator).builder, "a server: "+name)
	}
	assert.Equal(t, builder.Trusted, local.builder, "the host's own orchestrator is no deploy's")

	_, _, err := NewFactory(local, rollout.NetConfig{}, nil, zap.NewNop()).ForDeploy(ctx, srv, deployment.Builds{})
	assert.Error(t, err, "no SSH provider")
}

// On the panel's server the builder is in the apps' slice with half of the
// apps' share of memory. Where that half is under 1 GiB it has 1 GiB in the
// builders' slice, the untrusted builder beside it: that slice's limit is
// theirs together (toft upgrade writes it). Off the panel's server (no slice: a development
// stack) it has neither, and Docker isn't asked.
func TestThePanelsBuilderIsInTheAppsSlice(t *testing.T) {
	ctx := context.Background()
	spec := builder.Spec{Name: "toft-build", Bridge: "toft-ubuild", Firewall: true}
	onHost := func(cores int, mem int64) builder.Spec {
		d := fakedocker.New(t)
		d.NCPU, d.MemTotal = cores, mem
		o, err := (&LocalOrchestrator{dockerClient: d.Client(), log: zap.NewNop(), net: rollout.NetConfig{AppsSlice: "toft-apps.slice"}}).forDeploy(ctx, spec)
		require.NoError(t, err)
		return o.builder
	}

	_, apps := reservation.Apps(8, 16<<30)
	require.Greater(t, apps/2, int64(1<<30))
	big := spec
	big.Memory, big.Cgroup = apps/2, "toft-apps.slice"
	assert.Equal(t, big, onHost(8, 16<<30))

	_, apps = reservation.Apps(2, 4<<30)
	require.Less(t, apps/2, int64(1<<30), "a 4 GB panel host")
	small := spec
	small.Memory, small.Cgroup = 1<<30, "toft-builds.slice"
	assert.Equal(t, small, onHost(2, 4<<30), "1 GiB outside the apps' slice")
	spec.Name, spec.Bridge = builder.Untrusted.Name, builder.Untrusted.Bridge
	small.Name, small.Bridge = spec.Name, spec.Bridge
	assert.Equal(t, small, onHost(2, 4<<30), "the untrusted builder is in the same slice")
	spec.Name, spec.Bridge = builder.Trusted.Name, builder.Trusted.Bridge

	o, err := (&LocalOrchestrator{log: zap.NewNop()}).forDeploy(ctx, spec)
	require.NoError(t, err)
	assert.Equal(t, spec, o.builder, "no slice: ensure.sh takes half of the host's memory")

	down := fakedocker.New(t)
	down.Srv.Close()
	_, err = (&LocalOrchestrator{dockerClient: down.Client(), log: zap.NewNop(), net: rollout.NetConfig{AppsSlice: "toft-apps.slice"}}).forDeploy(ctx, spec)
	assert.ErrorContains(t, err, "size the builder")
}

// Every build asks its host's firewall first, and where the chain is
// missing nothing is built: on the panel's host, which is asked through the
// host helper (the backend's container isn't in the host's namespaces), and
// on a server over SSH. A host that can't be asked builds nothing either,
// never as if it had answered. The orchestrator of anything but a deploy
// (ForServer's) builds nothing, unasked. A stack that builds nothing still
// goes up.
func TestTheFirewallIsAskedBeforeEveryBuild(t *testing.T) {
	ctx := context.Background()
	const check = builder.FirewallChain + " >/dev/null 2>&1 && echo loaded || echo missing"
	deploys := builder.Spec{Name: "toft-build", Bridge: "toft-ubuild", Memory: 1 << 30, Firewall: true}
	dockerfile := func() io.Reader { return sourceTar(t, map[string]string{"Dockerfile": "FROM scratch\n"}, nil) }
	build := func(o *LocalOrchestrator) error {
		return o.BuildFromArchive(ctx, dockerfile(), "", testImageTag, nil, nil, nil, func(string) {})
	}

	// The panel's host.
	dir := fakeDocker(t)
	var asked [][]string
	answer := func(out string, err error) *LocalOrchestrator {
		return &LocalOrchestrator{builder: deploys, onHost: func(_ context.Context, argv []string) (string, error) {
			asked = append(asked, argv)
			return out, err
		}}
	}
	require.ErrorIs(t, build(answer("missing\n", nil)), builder.ErrNoFirewall, "the chain is missing")
	require.ErrorIs(t, build(answer("", errors.New("exit status 127"))), builder.ErrFirewallUnread)
	assert.Equal(t, [][]string{{"sh", "-c", check}, {"sh", "-c", check}}, asked, "asked on the host itself")
	// The helper as it is: a Docker that doesn't answer, and a backend that
	// isn't in a container, reach no host.
	down := fakedocker.New(t)
	down.Srv.Close()
	require.ErrorIs(t, build(&LocalOrchestrator{builder: deploys, dockerClient: down.Client(), log: zap.NewNop()}), builder.ErrFirewallUnread, "the helper is unreachable")
	err := build(&LocalOrchestrator{builder: deploys, dockerClient: fakedocker.New(t).Client(), log: zap.NewNop()})
	require.ErrorIs(t, err, builder.ErrFirewallUnread)
	assert.ErrorContains(t, err, "not running in a container")
	require.ErrorIs(t, build(&LocalOrchestrator{builder: builder.Trusted}), builder.ErrNoFirewall, "no deploy's orchestrator")
	assert.NoFileExists(t, filepath.Join(dir, "calls"), "docker didn't run on the panel's host")
	asked = nil
	require.NoError(t, build(answer("loaded\n", nil)))
	assert.Len(t, asked, 1)
	assert.FileExists(t, filepath.Join(dir, "calls"))

	// A server.
	release := rollout.ComposeDir(slotSvcID) + "/r-abc"
	answers := func(_ context.Context, cmd string) (string, error) {
		if strings.Contains(cmd, "mktemp -d") {
			return release + "\n", nil
		}
		return "", nil
	}
	hooks := rollout.Hooks{Log: func(string) {}, HandOff: func() error { return nil }}
	up := func(o *RemoteOrchestrator) error {
		return o.RunCompose(ctx, "dep-1", slotSvcID, "web", "services: {web: {build: .}}\n",
			nil, nil, deployment.ComposeNetworks{Home: "paas-network"}, nil, nil, hooks)
	}
	for name, tc := range map[string]struct {
		firewall func() (string, error)
		deploys  bool
		want     error
	}{
		"the chain is missing":  {func() (string, error) { return "missing\n", nil }, true, builder.ErrNoFirewall},
		"the server can't say":  {func() (string, error) { return "", errors.New("exit status 255") }, true, builder.ErrFirewallUnread},
		"no deploy's (unasked)": {func() (string, error) { t.Error("asked"); return "", nil }, false, builder.ErrNoFirewall},
	} {
		h := &fakeHost{docker: fakedocker.New(t), stack: stackBuilds, exec: answers, firewall: tc.firewall}
		o := newRemoteOrchestrator(h, rollout.NetConfig{}, zap.NewNop())
		o.builder.Firewall = tc.deploys
		require.ErrorIs(t, o.dockerBuild(ctx, testImageTag, nil, nil, "", "/tmp/paas-build-abc", func(string) {}), tc.want, name)
		require.ErrorIs(t, up(o), tc.want, name)
		for _, c := range h.recorded() {
			assert.NotContains(t, c.cmd, "TOFT_BUILDER=", "%s: no builder is made", name)
			assert.False(t, strings.Contains(c.cmd, " build ") || strings.Contains(c.cmd, " up "), "%s: ran without the firewall: %s", name, c.cmd)
		}
	}

	// A connection lost while it asks is one the step is run again for.
	lost := 1
	h := &fakeHost{docker: fakedocker.New(t), exec: answers, firewall: func() (string, error) {
		if lost--; lost >= 0 {
			return "", sshpool.ErrConnectionLost
		}
		return "loaded\n", nil
	}}
	require.NoError(t, newTestRemote(h).dockerBuild(ctx, testImageTag, nil, nil, "", "/tmp/paas-build-abc", func(string) {}))

	h = &fakeHost{docker: fakedocker.New(t), stack: stackPulls, exec: answers, firewall: func() (string, error) { return "missing\n", nil }}
	require.NoError(t, up(newTestRemote(h)), "a stack that builds nothing")
}

// The agent gets the deploy's builder as the panel has it: the untrusted
// one for an untrusted preview. The agent asks its server's firewall.
func TestTheAgentGetsTheDeploysBuilder(t *testing.T) {
	release := rollout.ComposeDir(slotSvcID) + "/r-abc"
	for name, spec := range map[string]builder.Spec{
		"untrusted": {Name: "toft-build-untrusted", Bridge: "toft-upbuild", Firewall: true},
		"trusted":   {Name: "toft-build", Bridge: "toft-ubuild", Firewall: true},
	} {
		var req rollout.Request
		h := &fakeHost{docker: fakedocker.New(t), exec: func(_ context.Context, cmd string) (string, error) {
			if strings.Contains(cmd, "mktemp -d") {
				return release + "\n", nil
			}
			return "", nil
		}}
		h.subsystem = func(_, stdin string, lineFn func(string)) error {
			require.NoError(t, json.Unmarshal([]byte(stdin), &req))
			lineFn(event(t, rollout.Event{Result: &rollout.Result{Changed: true}}))
			return nil
		}
		o := agentOrchestrator(t, h)
		o.builder = spec
		require.NoError(t, o.RunCompose(t.Context(), "dep-1", slotSvcID, "web", "services: {web: {build: .}}\n",
			nil, nil, deployment.ComposeNetworks{Home: "paas-network"}, nil, nil, rollout.Hooks{Log: func(string) {}, HandOff: func() error { return nil }}))
		require.NotNil(t, req.Compose, name)
		want := spec.Full()
		assert.Equal(t, &want, req.Compose.Builder, name)
	}
}
