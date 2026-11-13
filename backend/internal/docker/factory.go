package docker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/netplan"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/pkg/sshpool"
	"go.uber.org/zap"
)

// connectTimeout bounds how long ForServer waits for a server's SSH
// connection, so an unreachable server fails fast.
const connectTimeout = 15 * time.Second

// SSHProvider hands out the pooled SSH connection to a server, with its
// pinned host key. Satisfied by *server.Service.
type SSHProvider interface {
	SSH(srv *server.Server) sshpool.RemoteHost
}

// RolloutAgents tells which servers' agents run the rollouts the panel
// hands them (D-111). Satisfied by *server.Service.
type RolloutAgents interface {
	AgentRunsRollouts(serverID string) bool
	// AgentKeepsDeadlines: the agent ends a rollout at the deploy's time
	// limit (D-125).
	AgentKeepsDeadlines(serverID string) bool
	// AgentAppliesContainerChecks: the agent creates a slot with the
	// service's own container check (rollout.Slot.Check).
	AgentAppliesContainerChecks(serverID string) bool
	// PanelEpoch is sent with each rollout: an agent holding another
	// epoch's state refuses it (D-113).
	PanelEpoch(ctx context.Context) (string, error)
}

// MetricsAgents tells which servers' agents keep their own samples
// (D-113). Satisfied by *server.Service.
type MetricsAgents interface {
	AgentKeepsMetrics(serverID string) bool
}

// Factory implements deployment.OrchestratorFactory.
// ForServer(nil) returns the local Docker orchestrator.
// ForServer(srv) returns a RemoteOrchestrator over the server's pooled SSH connection.
type Factory struct {
	local *LocalOrchestrator
	net   rollout.NetConfig
	ssh   SSHProvider
	log   *zap.Logger
}

// NewFactory creates an OrchestratorFactory backed by a local orchestrator for
// the nil-server (dev) case and SSH-based remote orchestrators otherwise.
// net names the proxy and the control plane's network; each remote server
// gets its own HostKey.
func NewFactory(local *LocalOrchestrator, net rollout.NetConfig, ssh SSHProvider, log *zap.Logger) *Factory {
	return &Factory{local: local, net: net, ssh: ssh, log: log}
}

// ForServer returns an Orchestrator appropriate for srv.
// - srv == nil or srv.ID == "" → LocalOrchestrator (local Docker socket)
// - otherwise → RemoteOrchestrator (the server's pooled SSH connection)
//
// For a remote server it makes sure the connection is up (bounded by
// connectTimeout) so an unreachable server or a changed host key fails here.
// The second return value is a cleanup function; always call it when done.
// It is a no-op: the pool owns the connections and keeps them for the next
// operation.
func (f *Factory) ForServer(srv *server.Server) (deployment.Orchestrator, func(), error) {
	if srv == nil || srv.ID == "" {
		return f.local, func() {}, nil
	}
	if f.ssh == nil {
		return nil, nil, errors.New("remote servers are not configured: no SSH provider")
	}

	h := f.ssh.SSH(srv)
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	if _, err := h.Ensure(ctx, sshpool.EnsureOpts{}); err != nil {
		return nil, nil, fmt.Errorf("SSH connect to server %q (%s): %w", srv.Name, srv.IPAddress, err)
	}

	// Remote servers run the provisioner's names, whatever the control
	// plane's own TRAEFIK_CONTAINER and TRAEFIK_NETWORK are. On an agent
	// server containers log locally and the agent ships them (D-113).
	net := rollout.NetConfig{ProxyContainer: "paas-traefik", LegacyNetwork: netplan.LegacyNetwork, HostKey: "server:" + srv.ID,
		LocalLogs: srv.Connection == server.ConnectionAgent}
	remote := newRemoteOrchestrator(h, net, f.log)
	if ra, ok := f.ssh.(RolloutAgents); ok && srv.Connection == server.ConnectionAgent {
		if remote.agent = ra.AgentRunsRollouts(srv.ID); remote.agent {
			remote.deadlines = ra.AgentKeepsDeadlines(srv.ID)
			remote.checks = ra.AgentAppliesContainerChecks(srv.ID)
			epoch, err := ra.PanelEpoch(ctx)
			if err != nil {
				return nil, nil, fmt.Errorf("read the panel's epoch: %w", err)
			}
			remote.epoch = epoch
		}
	}
	if ma, ok := f.ssh.(MetricsAgents); ok && srv.Connection == server.ConnectionAgent {
		remote.metrics = ma.AgentKeepsMetrics(srv.ID)
	}
	return remote, func() {}, nil
}
