package main

import (
	"context"
	"time"

	"github.com/Nickpap21/toft/backend/internal/netplan"
	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
	"go.uber.org/zap"
)

// networkReconcileTimeout bounds the startup network reconcile of one host.
const networkReconcileTimeout = 2 * time.Minute

// legacyNetworkNames are the control plane's network names: paas-network
// and TRAEFIK_NETWORK when it differs.
func legacyNetworkNames(traefikNetwork string) []string {
	names := []string{netplan.LegacyNetwork}
	if traefikNetwork != "" && traefikNetwork != netplan.LegacyNetwork {
		names = append(names, traefikNetwork)
	}
	return names
}

// reconcileAppNetworks converges the control-plane server, then each remote
// server in turn, on its services' network plans (APP_NETWORK_MIGRATION),
// each under networkReconcileTimeout, and logs every report. A server that
// fails keeps what it has; its next deploy uses its environment network.
func reconcileAppNetworks(ctx context.Context, svc *service.ServiceManager, servers *server.Service, mode netplan.MigrationMode, log *zap.Logger) {
	run := func(serverID, name string) {
		hctx, cancel := context.WithTimeout(ctx, networkReconcileTimeout)
		defer cancel()
		hlog := log.With(zap.String("server", name), zap.String("server_id", serverID), zap.String("mode", string(mode)))
		report, err := svc.ReconcileNetworks(hctx, serverID, mode, func(line string) { hlog.Info(line) })
		if err != nil {
			hlog.Warn("app network reconcile failed", zap.Error(err))
		}
		logNetworkReport(hlog, mode, report)
	}
	run("", "control plane")
	list, err := servers.ListAllServers(ctx)
	if err != nil {
		log.Warn("app network reconcile: could not list servers", zap.Error(err))
		return
	}
	for _, srv := range list {
		if ctx.Err() != nil {
			return
		}
		run(srv.ID, srv.Name)
	}
}

// logNetworkReport logs a reconcile report: what changed (or, in report
// mode, would change) at Info, and what needs a person at Warn.
func logNetworkReport(log *zap.Logger, mode netplan.MigrationMode, r netplan.Report) {
	verb := "app network reconcile"
	if mode == netplan.MigrationReport {
		verb = "app network report (dry run, nothing changed)"
	}
	log.Info(verb,
		zap.Strings("created", r.Created),
		zap.Strings("attached", r.Attached),
		zap.Strings("detached", r.Detached),
		zap.Strings("removed", r.Removed))
	for _, c := range r.CrossRefs {
		log.Warn("preflight: an env var names an alias that does not resolve under isolation",
			zap.String("service", c.FromName), zap.String("service_id", c.From),
			zap.String("variable", c.Variable), zap.String("alias", c.Alias),
			zap.String("target", c.ToName), zap.String("target_id", c.To))
	}
	if len(r.Orphans) > 0 {
		log.Warn("orphan containers (slots of services that no longer exist); left running and on their networks: check, then remove them with docker rm -f",
			zap.Strings("containers", r.Orphans))
	}
	if len(r.Unknown) > 0 {
		log.Warn("other containers on the control plane's network were left alone", zap.Strings("containers", r.Unknown))
	}
	if len(r.Errors) > 0 {
		log.Warn("app network reconcile problems", zap.Strings("errors", r.Errors))
	}
}
