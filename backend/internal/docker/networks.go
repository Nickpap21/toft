package docker

import (
	"context"
	"fmt"
	"time"

	"github.com/Nickpap21/toft/backend/internal/netplan"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"go.uber.org/zap"
)

// ─── Orchestrator methods ────────────────────────────────────────────────────

// PrepareNetworks makes the local host ready for plan (rollout.PrepareNetworks). A
// no-op under legacy.
func (o *LocalOrchestrator) PrepareNetworks(ctx context.Context, plan netplan.Plan, logFn func(string)) error {
	return rollout.PrepareNetworks(ctx, o.dockerClient, o.net, plan, logFn)
}

// ReconcileNetworks converges the local host on plans (rollout.ReconcileHost).
func (o *LocalOrchestrator) ReconcileNetworks(ctx context.Context, plans []netplan.Plan, serviceIDs []string, mode netplan.MigrationMode, logFn func(string)) (netplan.Report, error) {
	return rollout.ReconcileHost(ctx, o.dockerClient, o.net, plans, serviceIDs, mode, logFn)
}

// PrepareNetworks makes the remote host ready for plan through the SSH
// Docker tunnel. A no-op under legacy.
func (o *RemoteOrchestrator) PrepareNetworks(ctx context.Context, plan netplan.Plan, logFn func(string)) error {
	if plan.Legacy {
		return plan.Valid()
	}
	dc, err := o.host.DockerClient()
	if err != nil {
		return fmt.Errorf("SSH Docker client: %w", err)
	}
	defer dc.Close()
	return rollout.PrepareNetworks(ctx, dc, o.net, plan, logFn)
}

// ReconcileNetworks converges the remote host on plans through the SSH
// Docker tunnel.
func (o *RemoteOrchestrator) ReconcileNetworks(ctx context.Context, plans []netplan.Plan, serviceIDs []string, mode netplan.MigrationMode, logFn func(string)) (netplan.Report, error) {
	dc, err := o.host.DockerClient()
	if err != nil {
		return netplan.Report{}, fmt.Errorf("SSH Docker client: %w", err)
	}
	defer dc.Close()
	return rollout.ReconcileHost(ctx, dc, o.net, plans, serviceIDs, mode, logFn)
}

// proxyWatchInterval is the safety tick of WatchProxy.
var proxyWatchInterval = 60 * time.Second

// WatchProxy keeps the local proxy on every app network: `docker compose up`
// can recreate paas-traefik, which drops the networks it was connected to
// at runtime. It re-attaches on every start event of the proxy container
// and on a 60-second tick, until ctx ends. Run it only under isolated app
// networks.
func (o *LocalOrchestrator) WatchProxy(ctx context.Context) {
	cfg := o.net.WithDefaults()
	attach := func() {
		if n, err := rollout.AttachProxy(ctx, o.dockerClient, cfg); err != nil {
			o.log.Warn("could not attach the proxy to app networks", zap.Error(err))
		} else if n > 0 {
			o.log.Info("attached the proxy to app networks", zap.Int("networks", n))
		}
	}
	attach()
	tick := time.NewTicker(proxyWatchInterval)
	defer tick.Stop()
	backoff := time.Second
	for {
		msgs, errc := o.dockerClient.Events(ctx, events.ListOptions{Filters: filters.NewArgs(
			filters.Arg("type", string(events.ContainerEventType)),
			filters.Arg("event", string(events.ActionStart)),
			filters.Arg("container", cfg.ProxyContainer),
		)})
	loop:
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				attach()
			case <-msgs:
				backoff = time.Second
				attach()
			case err := <-errc:
				if ctx.Err() != nil {
					return
				}
				o.log.Warn("proxy watch: Docker events stream ended; resubscribing", zap.Error(err))
				break loop
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}
