package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"github.com/Nickpap21/toft/backend/pkg/buildinfo"
	"github.com/Nickpap21/toft/backend/pkg/wsconn"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

const (
	backoffMin = time.Second
	backoffMax = time.Minute
	// stableAfter: a connection that lasted this long resets the backoff.
	stableAfter = time.Minute
	// readIdle: the panel pings every 20 s; nothing for this long means the
	// link is dead even if TCP hasn't noticed.
	readIdle = 60 * time.Second
	// trialWindow: a new version must complete a connection within this, or
	// the agent goes back to the previous one (D-106).
	trialWindow = 120 * time.Second
	// refusedFor: refused this long without a break, the agent stops. Not at
	// once: a panel being restored answers from an empty database for a while.
	refusedFor = time.Hour
)

// errRevoked: the panel refused this agent's key for good.
var errRevoked = errors.New("the panel revoked this agent")

// errRefused: the panel doesn't know this agent, and it has no join token
// to change that: its server was deleted, or no longer uses an agent (or
// the panel is being restored: see refusedFor).
var errRefused = errors.New("the panel doesn't know this agent")

// run is the service: connect, serve, reconnect with backoff, forever.
func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	signer, err := loadOrCreateKey()
	if err != nil {
		return fmt.Errorf("agent key: %w", err)
	}
	pinReleaseKey()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	// An upgrade installed while rollouts run ends the loop once they end.
	ctx, restartNow := context.WithCancel(ctx)
	defer restartNow()
	ro, err := newRollouts()
	if err != nil {
		slog.Warn("rollouts stay with the panel", "error", err)
		ro = nil
	} else {
		go ro.resume(ctx)
		go ro.routes.watch(ctx)
		go ro.metrics.run(ctx)
		go newShipper(ro.dc).watch(ctx) // logs to Loki while the panel is away too (D-113)
		if err := startDNSRelay(ctx, ro.dc); err != nil {
			slog.Warn("strict-network containers get no names here", "error", err)
		} else {
			ro.dns = true
		}
	}

	connected := make(chan struct{}, 1)
	go trialBoot(ctx, connected)

	slog.Info("toft-agent starting", "version", buildinfo.Version, "panel", cfg.Panel, "server", cfg.Server,
		"key", ssh.FingerprintSHA256(signer.PublicKey()))
	failures := 0
	var refusedSince time.Time
	for ctx.Err() == nil {
		start := time.Now()
		err := connectOnce(ctx, &cfg, &signer, connected, ro, restartNow)
		if errors.Is(err, errRevoked) {
			slog.Error("the panel refused this agent's key: it was revoked or replaced. Join the server again to use it", "error", err)
			os.Exit(agentproto.ExitRevoked)
		}
		if refusedTooLong(&refusedSince, err, time.Now()) {
			slog.Error("the panel has refused this agent for an hour: its server was deleted from the panel, or no longer uses an agent. Join the server again to use it", "error", err)
			os.Exit(agentproto.ExitRevoked)
		}
		if errors.Is(err, errRestart) {
			slog.Info("restarting into the new version")
			return nil // systemd starts the current version
		}
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(start) > stableAfter {
			failures = 0
		}
		failures++
		wait := backoff(failures)
		s := readState()
		s.LastError = errString(err)
		writeState(s)
		slog.Warn("disconnected from the panel", "error", err, "retry_in", wait.Round(time.Millisecond))
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
	return nil
}

// refusedTooLong reports whether the panel has refused this agent
// (errRefused) for refusedFor without a break; any other outcome resets
// since.
func refusedTooLong(since *time.Time, err error, now time.Time) bool {
	if !errors.Is(err, errRefused) {
		*since = time.Time{}
		return false
	}
	if since.IsZero() {
		*since = now
	}
	return now.Sub(*since) >= refusedFor
}

// backoff: full jitter between backoffMin and min(backoffMax, min·2^(n-1)).
func backoff(n int) time.Duration {
	ceil := backoffMin << min(n-1, 10)
	if ceil > backoffMax || ceil <= 0 {
		ceil = backoffMax
	}
	return backoffMin + time.Duration(rand.Int64N(int64(ceil-backoffMin)+1))
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// dialPanel opens the WebSocket. With a join token it also sends the
// agent's public key; the panel pins it before the SSH handshake proves it.
func dialPanel(ctx context.Context, cfg config, signer ssh.Signer, caps string) (*websocket.Conn, *http.Response, error) {
	u, err := url.Parse(cfg.Panel)
	if err != nil {
		return nil, nil, fmt.Errorf("panel address %q: %w", cfg.Panel, err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/") + agentproto.ConnectPath
	d := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: 20 * time.Second,
		// The SSH layer authenticates both ends with pinned keys; TLS
		// verification is skipped only for a panel that has no domain.
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.InsecureTLS}, //nolint:gosec // D-106
		ReadBufferSize:  32 << 10,
		WriteBufferSize: 32 << 10,
	}
	// Until the join, nothing vouches for the panel but its certificate:
	// the token goes only to the one the join command named, refused in
	// the handshake, before any header is sent. Once joined, the pinned
	// SSH keys carry the trust, and Traefik's certificate may change.
	if cfg.Token != "" && (cfg.InsecureTLS || cfg.PanelCert != "") {
		d.TLSClientConfig.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) > 0 {
				sum := sha256.Sum256(cs.PeerCertificates[0].RawSubjectPublicKeyInfo)
				if cfg.PanelCert != "" && base64.StdEncoding.EncodeToString(sum[:]) == cfg.PanelCert {
					return nil
				}
			}
			return errors.New("the panel's certificate isn't the one the join command named (the panel restarted since, or something is in between): make a new join command in the panel")
		}
	}
	h := http.Header{}
	h.Set(agentproto.HeaderServer, cfg.Server)
	h.Set(agentproto.HeaderVersion, buildinfo.Version)
	h.Set(agentproto.HeaderArch, runtime.GOARCH)
	h.Set(agentproto.HeaderBoot, bootID())
	if caps != "" {
		h.Set(agentproto.HeaderCaps, caps)
	}
	// The key it will present: the panel refuses a revoked or replaced one
	// before the WebSocket opens, and the SSH handshake proves this one.
	h.Set(agentproto.HeaderKey, authorizedKey(signer.PublicKey()))
	if cfg.Token != "" {
		h.Set("Authorization", "Bearer "+cfg.Token)
	}
	return d.DialContext(ctx, u.String(), h)
}

func bootID() string {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// connectOnce runs one connection to its end.
func connectOnce(ctx context.Context, cfg *config, signer *ssh.Signer, connected chan<- struct{}, ro *rollouts, restartNow func()) error {
	// Host updates need no Docker (D-134).
	ws, resp, err := dialPanel(ctx, *cfg, *signer, strings.TrimSpace(ro.caps()+" "+agentproto.CapHostUpdate))
	if err != nil {
		if resp != nil {
			msg := resp.Header.Get("X-Toft-Error")
			if resp.StatusCode == http.StatusForbidden && msg == "agent_revoked" {
				return errRevoked
			}
			// With a token, the panel may yet accept it; an outage answers 503.
			if resp.StatusCode == http.StatusUnauthorized && msg == "agent_refused" && cfg.Token == "" {
				return errRefused
			}
			return fmt.Errorf("the panel answered %s %s", resp.Status, msg)
		}
		return err
	}
	conn := wsconn.New(ws)
	defer conn.Close()
	// The panel admitted this agent (its token, or its pinned key): the
	// join is done, and a version on trial reached its panel.
	s := readState()
	if cfg.Token != "" {
		s.JoinedWith = tokenSum(cfg.Token)
		cfg.Token, cfg.PanelCert = "", ""
		if err := cfg.save(); err != nil {
			slog.Warn("could not drop the used join token from "+envFile, "error", err)
		}
	}
	s.ConnectedAt = time.Now().UTC().Format(time.RFC3339)
	s.LastError = ""
	writeState(s)
	select {
	case connected <- struct{}{}:
	default:
	}
	_ = ws.SetReadDeadline(time.Now().Add(readIdle))
	ws.SetPingHandler(func(data string) error {
		_ = ws.SetReadDeadline(time.Now().Add(readIdle))
		return ws.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(5*time.Second))
	})

	srv := &agentServer{cfg: cfg, signer: signer, rollouts: ro, restartNow: restartNow}
	return srv.serve(ctx, conn, func() { slog.Info("the panel signed in") })
}

// trialBoot: right after an upgrade, the new version has trialWindow to
// connect; otherwise it switches back to the previous one and exits, and
// systemd starts that.
func trialBoot(ctx context.Context, connected <-chan struct{}) {
	s := readState()
	if s.Trial == "" || s.Trial != buildinfo.Version {
		return
	}
	select {
	case <-ctx.Done():
	case <-connected:
		s := readState()
		s.Trial, s.Previous = "", ""
		writeState(s)
		slog.Info("the new version connected; keeping it", "version", buildinfo.Version)
	case <-time.After(trialWindow):
		slog.Error("the new version did not connect in time; going back", "version", buildinfo.Version, "to", s.Previous)
		if err := switchCurrent(s.Previous); err != nil {
			slog.Error("could not go back", "error", err)
			return
		}
		s.Trial = ""
		writeState(s)
		os.Exit(1)
	}
}
