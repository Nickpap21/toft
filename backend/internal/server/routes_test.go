package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/agenthub"
	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"github.com/Nickpap21/toft/backend/pkg/sshpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// routesHost is a server whose agent writes the routes, its Traefik
// started by Toft with args.
type routesHost struct {
	sshpool.RemoteHost
	sh     *fakeShell
	synced int  // len(sh.cmds) when the agent was asked to write the routes
	refuse bool // the agent is frozen
}

func newRoutesHost(args string) *routesHost {
	return &routesHost{synced: -1, sh: &fakeShell{answer: func(cmd string) string {
		switch {
		case cmd == "uname -m":
			return "aarch64"
		case strings.Contains(cmd, "{{json .Args}}"):
			return args + "|" + traefikAcmeVolume + ","
		case strings.HasPrefix(cmd, "docker ps --filter name=paas-traefik"):
			return "Up 3 days"
		case strings.HasPrefix(cmd, "docker container inspect"):
			_, hash, _ := traefikConfig("", nil, strings.Contains(args, traefikFileFlag))
			return hash + "|" + traefikAcmeVolume + ","
		}
		return ""
	}}}
}

func (h *routesHost) Exec(_ context.Context, cmd string) (string, error) { return h.sh.Exec(cmd) }
func (h *routesHost) ExecInput(_ context.Context, cmd string, in io.Reader) (string, error) {
	return h.sh.ExecInput(cmd, in)
}
func (h *routesHost) ExecStream(_ context.Context, cmd string, logFn func(string)) error {
	return h.sh.ExecStream(cmd, logFn)
}
func (h *routesHost) Upload(context.Context, string, []byte) error { return nil }
func (h *routesHost) Subsystem(_ context.Context, name string, _ io.Reader) (string, error) {
	if name != agentproto.SubsystemRoutes {
		return "", io.EOF
	}
	h.synced = len(h.sh.cmds)
	if h.refuse {
		return "", errors.New(agentproto.ErrEpochFrozen + ": the panel was restored from a backup")
	}
	return `{"services":4}` + "\n", nil
}

type routesConn struct{ h *routesHost }

func (c routesConn) Handle(sshpool.Target) sshpool.RemoteHost { return c.h }
func (routesConn) Invalidate(string)                          {}

// An agent that writes the routes has its server's Traefik switched to its
// files when it connects, once, recorded as a provisioning run by no one;
// one that can't is left alone.
func TestAgentThatWritesRoutesHasTraefikSwitchedToThem(t *testing.T) {
	svc, _, hub := agentService(t)
	repo := svc.repo.(*fakeRepo)
	now := time.Now()
	repo.servers["s1"].Connection, repo.servers["s1"].ProvisionedAt = ConnectionAgent, &now
	ctx := context.Background()
	srv, _ := repo.GetByID(ctx, "s1")

	host := newRoutesHost(`["--providers.docker=true","` + traefikRedirectPriority + `","` + traefikMetricsEntrypoint + `"]`)
	svc.WithSSH(routesConn{host}, nil)
	hub.known, hub.state = true, agenthub.State{Connected: true, Caps: []string{agentproto.CapRollout, agentproto.CapDesired}}
	svc.cutOverRoutes(ctx, srv)
	assert.Equal(t, -1, indexOf(host.sh.cmds, "docker rm -f paas-traefik"), "an agent that doesn't write the routes")
	assert.Nil(t, repo.runs)

	// The last Provision left the firewall out; the switch doesn't set it up.
	ok := true
	repo.runs = map[string]*ProvisionRun{"s1": {FinishedAt: &now, OK: &ok, Warnings: []string{warningFirewall}}}
	hub.state.Caps = append(hub.state.Caps, agentproto.CapRoutes)
	svc.cutOverRoutes(ctx, srv)
	run := repo.runs["s1"]
	require.NotNil(t, run)
	require.NotNil(t, run.OK)
	assert.True(t, *run.OK, run.Log+run.Error)
	assert.Equal(t, "", run.StartedBy)
	assert.Equal(t, []string{warningFirewall}, run.Warnings, "what the last run left out is still missing")
	assert.Contains(t, run.Log, "The agent wrote the routes of 4 services")
	rm := indexOf(host.sh.cmds, "docker rm -f paas-traefik")
	assert.True(t, host.synced >= 0 && host.synced < rm, "the agent writes the routes before Traefik restarts")
	assert.Contains(t, host.sh.cmds[indexOf(host.sh.cmds, "docker run -d")], traefikFileFlag)

	// Switched already: nothing runs, and the last run stays.
	repo.runs = nil
	host = newRoutesHost(`["` + traefikFileFlag + `/etc/traefik/dynamic","` + traefikRedirectPriority + `","` + traefikMetricsEntrypoint + `"]`)
	svc.WithSSH(routesConn{host}, nil)
	svc.cutOverRoutes(ctx, srv)
	assert.Nil(t, repo.runs)
	assert.Len(t, host.sh.cmds, 1)
}

// A server moved from SSH to the agent has its Traefik switched to the
// agent's files once the move is done: the agent connected while the
// server was still reached over SSH.
func TestMovingToTheAgentSwitchesTraefik(t *testing.T) {
	svc, store, hub := agentService(t)
	repo := svc.repo.(*fakeRepo)
	store.moved = func(id, c string) { repo.servers[id].Connection = c }
	now := time.Now()
	repo.servers["s1"].Connection, repo.servers["s1"].ProvisionedAt = ConnectionSSH, &now
	require.NoError(t, os.WriteFile(filepath.Join(svc.agents.binDir, "toft-agent-linux-arm64"), []byte("bin"), 0o600))
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKey(priv, "")
	require.NoError(t, err)
	svc.agents.keyPEM = string(pem.EncodeToMemory(block))
	host := newRoutesHost(`["--providers.docker=true"]`)
	svc.WithSSH(routesConn{host}, nil)
	hub.known, hub.state = true, agenthub.State{Connected: true, Caps: []string{agentproto.CapRollout, agentproto.CapRoutes}}

	require.NoError(t, svc.InstallAgent(context.Background(), "u1", "s1", AgentOrigin{URL: "https://panel.example"}, true, func(string) {}))
	run := repo.runs["s1"]
	require.NotNil(t, run, "the switch is recorded as a provisioning run")
	assert.Contains(t, run.Log, "The agent wrote the routes of 4 services")
	assert.Contains(t, host.sh.cmds[indexOf(host.sh.cmds, "docker run -d")], traefikFileFlag)
}

// A frozen agent's refusal records no run: nothing changed, and the agent
// is asked again on its next connect. A server moved to SSH has its
// Traefik back on the labels, with the Loki plugin for its deploys.
func TestTraefikFollowsTheServersConnection(t *testing.T) {
	svc, _, hub := agentService(t)
	repo := svc.repo.(*fakeRepo)
	now := time.Now()
	repo.servers["s1"].Connection, repo.servers["s1"].ProvisionedAt = ConnectionAgent, &now
	ctx := context.Background()
	srv, _ := repo.GetByID(ctx, "s1")
	hub.known, hub.state = true, agenthub.State{Connected: true, Caps: []string{agentproto.CapRoutes}}

	host := newRoutesHost(`["--providers.docker=true"]`)
	host.refuse = true
	svc.WithSSH(routesConn{host}, nil)
	svc.cutOverRoutes(ctx, srv)
	assert.Nil(t, repo.runs["s1"])
	assert.Equal(t, -1, indexOf(host.sh.cmds, "docker rm -f paas-traefik"))

	srv.Connection = ConnectionSSH
	host = newRoutesHost(`["` + traefikFileFlag + `/etc/traefik/dynamic"]`)
	svc.WithSSH(routesConn{host}, nil)
	svc.cutOverRoutes(ctx, srv)
	run := repo.runs["s1"]
	require.NotNil(t, run)
	require.NotNil(t, run.OK)
	assert.True(t, *run.OK, run.Log+run.Error)
	assert.Contains(t, run.Log, "This server moved to SSH")
	assert.Equal(t, -1, host.synced, "the agent isn't asked")
	assert.NotContains(t, host.sh.cmds[indexOf(host.sh.cmds, "docker run -d")], traefikFileFlag)
	assert.GreaterOrEqual(t, indexOf(host.sh.cmds, "amd64) docker plugin install grafana/loki-docker-driver:3.7.9-amd64@sha256:"), 0)
	assert.GreaterOrEqual(t, indexOf(host.sh.cmds, "arm64) docker plugin install grafana/loki-docker-driver:3.7.9-arm64@sha256:"), 0)
}

// A Traefik Toft started before 7f, whose redirect outranks every router,
// is restarted with the new settings on the health poll, once, recorded as
// a provisioning run, in the mode it is in (D-150). Not during a
// provisioning run, and never one Toft didn't start.
func TestTraefikBefore7fRestartsSoTemporaryNamesAnswerOverHTTP(t *testing.T) {
	svc, _, _ := agentService(t)
	repo := svc.repo.(*fakeRepo)
	now := time.Now()
	repo.servers["s1"].Connection, repo.servers["s1"].ProvisionedAt = ConnectionSSH, &now
	ctx := context.Background()

	old := func(mounts string) *routesHost {
		h := newRoutesHost(`["--providers.docker=true"]`)
		answer := h.sh.answer
		h.sh.answer = func(cmd string) string {
			switch {
			case strings.Contains(cmd, "{{json .Args}}"):
				return `["--providers.docker=true"]|` + mounts
			case strings.HasPrefix(cmd, "docker container inspect"):
				return "a-hash-from-before-7f|" + mounts
			}
			return answer(cmd)
		}
		return h
	}

	repo.runs = map[string]*ProvisionRun{"s1": {StartedAt: now}} // a provisioning run under way
	host := old(traefikAcmeVolume + ",")
	svc.WithSSH(routesConn{host}, nil)
	require.NoError(t, svc.KeepTraefikCurrent(ctx, "s1"))
	assert.Empty(t, host.sh.cmds, "never during a provisioning run")

	repo.runs = nil
	host = old("some-other-volume,")
	svc.WithSSH(routesConn{host}, nil)
	require.NoError(t, svc.KeepTraefikCurrent(ctx, "s1"))
	assert.Nil(t, repo.runs, "a Traefik Toft didn't start is left as it is")

	host = old(traefikAcmeVolume + ",")
	svc.WithSSH(routesConn{host}, nil)
	require.NoError(t, svc.KeepTraefikCurrent(ctx, "s1"))
	run := repo.runs["s1"]
	require.NotNil(t, run)
	require.NotNil(t, run.OK)
	assert.True(t, *run.OK, run.Log+run.Error)
	assert.Equal(t, "", run.StartedBy)
	assert.Contains(t, run.Log, "temporary sslip.io address answer over HTTP")
	assert.Less(t, indexOf(host.sh.cmds, "docker pull -q "+traefikImage), indexOf(host.sh.cmds, "docker rm -f paas-traefik"), "pulled before the old one goes")
	start := host.sh.cmds[indexOf(host.sh.cmds, "docker run -d")]
	assert.Contains(t, start, traefikRedirectPriority)
	assert.NotContains(t, start, traefikFileFlag, "it stays on the labels")
	assert.Equal(t, -1, indexOf(host.sh.cmds, "docker plugin install"), "nothing else changes")

	// A pull that fails leaves the running Traefik, and the failed run
	// waits for a person's Re-provision: the next poll doesn't retry.
	repo.runs = nil
	host = old(traefikAcmeVolume + ",")
	host.sh.fails = func(cmd string) bool { return strings.HasPrefix(cmd, "docker pull") }
	svc.WithSSH(routesConn{host}, nil)
	require.NoError(t, svc.KeepTraefikCurrent(ctx, "s1"))
	require.NotNil(t, repo.runs["s1"].OK)
	assert.False(t, *repo.runs["s1"].OK)
	assert.Equal(t, -1, indexOf(host.sh.cmds, "docker rm -f paas-traefik"))
	host = old(traefikAcmeVolume + ",")
	svc.WithSSH(routesConn{host}, nil)
	require.NoError(t, svc.KeepTraefikCurrent(ctx, "s1"))
	assert.Equal(t, -1, indexOf(host.sh.cmds, "docker rm -f paas-traefik"), "not after a failed run")

	// Current already: nothing restarts.
	repo.runs = nil
	host = newRoutesHost(`["--providers.docker=true","` + traefikRedirectPriority + `","` + traefikMetricsEntrypoint + `"]`)
	svc.WithSSH(routesConn{host}, nil)
	require.NoError(t, svc.KeepTraefikCurrent(ctx, "s1"))
	assert.Nil(t, repo.runs)
}

// A Traefik Toft started before latency was measured (D-156) restarts once
// with the metrics entrypoint on the health poll, as one before 7f does,
// in the mode it is in; then it is current. One Toft didn't start is
// never touched, and the failed run rule holds as for D-150.
func TestTraefikWithoutMetricsRestartsOnceToMeasureLatency(t *testing.T) {
	svc, _, hub := agentService(t)
	repo := svc.repo.(*fakeRepo)
	now := time.Now()
	ctx := context.Background()
	for _, files := range []bool{false, true} {
		repo.runs = nil
		args := `["--providers.docker=true","` + traefikRedirectPriority + `"]`
		repo.servers["s1"].Connection, repo.servers["s1"].ProvisionedAt = ConnectionSSH, &now
		if files {
			args = `["` + traefikFileFlag + `/etc/traefik/dynamic","` + traefikRedirectPriority + `"]`
			repo.servers["s1"].Connection = ConnectionAgent
			hub.known, hub.state = true, agenthub.State{Connected: true, Caps: []string{agentproto.CapRoutes}}
		}
		host := newRoutesHost(args)
		answer := host.sh.answer
		host.sh.answer = func(cmd string) string {
			if strings.HasPrefix(cmd, "docker container inspect") && !strings.Contains(cmd, "{{json .Args}}") {
				return "a-hash-from-v0.36|" + traefikAcmeVolume + ","
			}
			return answer(cmd)
		}
		svc.WithSSH(routesConn{host}, nil)
		require.NoError(t, svc.KeepTraefikCurrent(ctx, "s1"))
		run := repo.runs["s1"]
		require.NotNil(t, run, "files=%v", files)
		require.NotNil(t, run.OK)
		assert.True(t, *run.OK, run.Log+run.Error)
		assert.Contains(t, run.Log, "measures each service's request latency")
		start := host.sh.cmds[indexOf(host.sh.cmds, "docker run -d")]
		assert.Contains(t, start, traefikMetricsEntrypoint)
		assert.Equal(t, files, strings.Contains(start, traefikFileFlag), "it stays in its mode")
		assert.Equal(t, 1, countOf(host.sh.cmds, "docker rm -f paas-traefik"), "restarted once")

		// Restarted: current.
		repo.runs = nil
		host = newRoutesHost(strings.TrimSuffix(args, "]") + `,"` + traefikMetricsEntrypoint + `"]`)
		svc.WithSSH(routesConn{host}, nil)
		require.NoError(t, svc.KeepTraefikCurrent(ctx, "s1"))
		assert.Nil(t, repo.runs, "files=%v", files)
	}

	// An adopted Traefik stays as it is.
	repo.runs = nil
	repo.servers["s1"].Connection = ConnectionSSH
	host := newRoutesHost(`["--providers.docker=true"]`)
	answer := host.sh.answer
	host.sh.answer = func(cmd string) string {
		if strings.Contains(cmd, "{{json .Args}}") {
			return `["--providers.docker=true"]|some-other-volume,`
		}
		return answer(cmd)
	}
	svc.WithSSH(routesConn{host}, nil)
	require.NoError(t, svc.KeepTraefikCurrent(ctx, "s1"))
	assert.Nil(t, repo.runs)
}

func countOf(cmds []string, prefix string) int {
	n := 0
	for _, c := range cmds {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}
