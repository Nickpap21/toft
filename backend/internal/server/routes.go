package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/internal/hoststats"
	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"go.uber.org/zap"
)

// Routes written by the agent (D-113): on a server whose agent can, Traefik
// reads one file per service the agent writes from its containers' labels,
// instead of the labels through the Docker socket. Provision sets Traefik
// up that way; an agent that gains the ability (an upgrade) has its server
// switched when it connects, recorded as a provisioning run.

// traefikFileFlag is in the arguments of a Traefik reading the files.
const traefikFileFlag = "--providers.file.directory="

// traefikRedirectPriority is in the arguments of a Traefik whose HTTP to
// HTTPS redirect gives way to a service's own HTTP router (D-150).
const traefikRedirectPriority = "--entrypoints.web.http.redirections.entrypoint.priority=1"

// traefikMetricsEntrypoint is in the arguments of a Traefik that keeps its
// services' request durations (D-156), on its container's loopback.
const traefikMetricsEntrypoint = "--entrypoints.metrics.address=127.0.0.1:" + hoststats.LatencyPort

// routesSync returns, when srv's agent writes the routes, the request that
// has it write them all (ensureTraefik), or nil.
func (s *Service) routesSync(ctx context.Context, srv *Server) func() (int, error) {
	if s.agents == nil || srv.Connection != ConnectionAgent {
		return nil
	}
	if st, ok := s.agents.hub.State(srv.ID); !ok || !st.Connected || !slices.Contains(st.Caps, agentproto.CapRoutes) {
		return nil
	}
	return func() (int, error) {
		epoch, err := s.agents.store.PanelEpoch(ctx)
		if err != nil {
			return 0, fmt.Errorf("read the panel's epoch: %w", err)
		}
		raw, _ := json.Marshal(agentproto.RoutesRequest{Epoch: epoch})
		out, err := s.SSH(srv).Subsystem(ctx, agentproto.SubsystemRoutes, bytes.NewReader(append(raw, '\n')))
		if err != nil {
			return 0, err
		}
		var ack agentproto.RoutesAck
		if err := json.Unmarshal([]byte(out), &ack); err != nil {
			return 0, fmt.Errorf("the agent's answer %q: %w", strings.TrimSpace(out), err)
		}
		return ack.Services, nil
	}
}

// KeepTraefikCurrent brings serverID's Traefik up to date where it can
// (cutOverRoutes), for the health poll once the server answered.
func (s *Service) KeepTraefikCurrent(ctx context.Context, serverID string) error {
	srv, err := s.repo.GetByID(ctx, serverID)
	if err != nil {
		return fmt.Errorf("get server: %w", err)
	}
	s.cutOverRoutes(ctx, srv)
	return nil
}

// cutOverRoutes switches srv's Traefik to the agent's files when the agent
// writes the routes and the Traefik Toft started still reads labels, and
// back to the labels, with the Loki plugin, once the server moved to SSH:
// its agent may be uninstalled then, and nothing would write the files.
// It also restarts a Traefik Toft started before 7f, whose HTTP redirect
// still outranks every router, so a temporary sslip.io address answers
// over HTTP without a Re-provision (D-150; the health poll asks every 5
// minutes), and one started before Traefik kept its services' request
// durations (D-156). Each restart applies the whole configuration, so a
// server upgraded past both restarts once. Never while a provisioning run
// is under way, nor after a failed one.
func (s *Service) cutOverRoutes(ctx context.Context, srv *Server) {
	sync := s.routesSync(ctx, srv)
	files := sync != nil
	if srv.ProvisionedAt == nil {
		return
	}
	if _, busy := s.cutovers.LoadOrStore(srv.ID, true); busy {
		return
	}
	defer s.cutovers.Delete(srv.ID)
	run, err := s.repo.GetProvisionRun(ctx, srv.ID)
	if err == nil && run.FinishedAt == nil {
		return
	}
	// After a failed run the redirect waits for a person's Re-provision:
	// retrying every 5 minutes would replace that run's log each time.
	lastFailed := err == nil && run.OK != nil && !*run.OK
	// This run replaces that one's record but sets up no firewall: what
	// that one left out still is.
	var kept []string
	if err == nil {
		kept = run.Warnings
	}
	log := s.log.With(zap.String("server_id", srv.ID))
	sc := shellFor(ctx, s.SSH(srv))
	out, err := sc.Exec(`docker container inspect --format '{{json .Args}}|{{range .Mounts}}{{.Name}},{{end}}' paas-traefik`)
	args, mounts, _ := strings.Cut(strings.TrimSpace(out), "|")
	readsFiles := strings.Contains(args, traefikFileFlag)
	// To the labels only once on SSH: an agent server whose agent can't
	// write them now keeps the files it wrote.
	switchRoutes := readsFiles != files && (files || srv.Connection == ConnectionSSH)
	redirectFirst := !strings.Contains(args, traefikRedirectPriority)
	noMetrics := !strings.Contains(args, traefikMetricsEntrypoint)
	stale := readsFiles == files && !lastFailed && (redirectFirst || noMetrics)
	if err != nil || !switchRoutes && !stale || !strings.Contains(","+mounts, ","+traefikAcmeVolume+",") {
		return
	}
	if files {
		// Only a run that changes something is recorded: a frozen agent,
		// or one whose files fail, is asked again on its next connect.
		n, err := sync()
		if err != nil {
			log.Warn("the agent could not write the routes; Traefik goes on reading labels", zap.Error(err))
			return
		}
		sync = func() (int, error) { return n, nil }
	}

	if err := s.repo.StartProvisionRun(ctx, srv.ID, ""); err != nil {
		log.Warn("record provision run", zap.Error(err))
	}
	logFn := func(line string) {
		if err := s.repo.AppendProvisionLog(ctx, srv.ID, line); err != nil {
			log.Warn("record provision log", zap.Error(err))
		}
	}
	started := time.Now()
	err = func() error {
		switch {
		case !switchRoutes && redirectFirst:
			logFn("Toft now lets a temporary sslip.io address answer over HTTP while its certificate waits (D-150): Traefik restarts to apply it, so this server's routes pause for a few seconds")
		case !switchRoutes:
			logFn("Toft now measures each service's request latency at this server's Traefik (D-156): Traefik restarts to apply it, so this server's routes pause for a few seconds")
		case files:
			logFn("This server's agent now writes Traefik's routes (D-113): Traefik restarts to read them from its files, so this server's routes pause for a few seconds")
		default:
			logFn("This server moved to SSH: Traefik restarts to read the routes from the containers' labels again, so this server's routes pause for a few seconds")
		}
		dns, err := s.repo.GetDNSProvider(ctx, srv.ID)
		if errors.Is(err, ErrNoDNSProvider) {
			dns, err = nil, nil
		}
		if err != nil {
			return fmt.Errorf("load DNS provider: %w", err)
		}
		if err := ensureTraefik(sc, s.acmeEmail, dns, sync, logFn); err != nil {
			return fmt.Errorf("Traefik setup: %w", err)
		}
		if switchRoutes && !files {
			// Containers deployed over SSH log to the plugin where their
			// service has a Loki; the agent removed it.
			if err := ensureLokiDriver(sc, false, logFn); err != nil {
				return fmt.Errorf("Loki log driver: %w", err)
			}
		}
		if s.appNetworksIsolated {
			// Recreating Traefik took it off the app networks.
			if err := ensureProxyNetworks(sc, logFn); err != nil {
				logFn("Warning: attaching Traefik to app networks failed (non-fatal): " + err.Error())
			}
		}
		logFn(fmt.Sprintf("Done in %.1fs", time.Since(started).Seconds()))
		return nil
	}()
	msg := ""
	if err != nil {
		msg = err.Error()
		log.Warn("could not switch Traefik's routes", zap.Bool("files", files), zap.Error(err))
	}
	if ferr := s.repo.FinishProvisionRun(ctx, srv.ID, err == nil, msg, kept); ferr != nil {
		log.Warn("record provision outcome", zap.Error(ferr))
	}
}
