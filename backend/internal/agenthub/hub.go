// Package agenthub accepts the WebSockets servers' agents open to the panel
// (D-106) and hands each one to the SSH pool as the server's connection. It
// is transport only: who may connect is the server service's decision
// (Admitter), and inside the WebSocket the pool runs SSH as it does to any
// server.
package agenthub

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"github.com/Nickpap21/toft/backend/pkg/wsconn"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

// Timings (D-106).
const (
	// ReconnectGrace: an agent away for less than this is Reconnecting, and
	// a dial waits for it; after it, the agent is Offline and dials fail at
	// once. Also how long after the panel starts every agent is given to
	// come back.
	ReconnectGrace = 2 * time.Minute
	pingEvery      = 20 * time.Second
	seenEvery      = time.Minute
)

// Hello is what an agent says about itself when it connects.
type Hello struct {
	ServerID string
	Version  string
	Arch     string
	BootID   string
	FromIP   string
	// Caps are what the agent can do beyond the tunnel
	// (agentproto.HeaderCaps): agentproto.CapRollout runs rollouts (D-111).
	Caps []string
	// Token and Key are set on a join only.
	Token string
	Key   string
}

// Admission errors the Admitter returns; each maps to an answer the agent
// understands.
var (
	ErrUnknown = errors.New("unknown server or token")
	ErrRevoked = errors.New("agent revoked")
)

// Admitter is the server service's part: whether an agent may connect, and
// what happens when it does.
type Admitter interface {
	// AdmitAgent checks a connecting agent before its WebSocket opens. With
	// a join token it consumes the token, pins the agent's key and returns
	// the server's ID; without one the server must have a pinned key (the
	// SSH handshake then proves the agent holds it).
	AdmitAgent(ctx context.Context, h Hello) (serverID string, err error)
	// AgentConnected runs once the WebSocket is open (in its own goroutine).
	AgentConnected(serverID string, h Hello)
	// AgentSeen runs every minute while connected and once when it ends.
	AgentSeen(serverID string)
}

// ErrOffline is returned by Conn while the server's agent is away for longer
// than ReconnectGrace.
var ErrOffline = errors.New("the server's agent is offline")

// State is an agent's connection as the panel sees it now.
type State struct {
	Connected bool
	Since     time.Time // connected since, or away since
	RTT       time.Duration
	Version   string
	Upgrading bool
	// Caps are the connected agent's (Hello.Caps).
	Caps []string
}

type agent struct {
	conn  *tracked
	hello Hello
	since time.Time
	taken bool
	rtt   time.Duration
	sent  atomic.Int64  // the last ping, in Unix nanoseconds
	pong  chan struct{} // signalled on every pong
}

// Hub holds one agent connection per server.
type Hub struct {
	adm     Admitter
	log     *zap.Logger
	up      websocket.Upgrader
	started time.Time

	mu        sync.Mutex
	agents    map[string]*agent
	away      map[string]time.Time // server → when its agent left
	upgrading map[string]time.Time
	changed   chan struct{} // closed and replaced on every change
}

func New(adm Admitter, log *zap.Logger) *Hub {
	return &Hub{
		adm: adm, log: log, started: time.Now(),
		up: websocket.Upgrader{
			ReadBufferSize: 32 << 10, WriteBufferSize: 32 << 10,
			// Agents aren't browsers: they send no Origin.
			CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" },
		},
		agents:    map[string]*agent{},
		away:      map[string]time.Time{},
		upgrading: map[string]time.Time{},
		changed:   make(chan struct{}),
	}
}

func (h *Hub) broadcastLocked() {
	close(h.changed)
	h.changed = make(chan struct{})
}

// Connect is GET /api/v1/agent/connect. fromIP is the agent's address as
// the router sees it (behind a trusted proxy only, the proxy's header).
func (h *Hub) Connect(w http.ResponseWriter, r *http.Request, fromIP string) {
	hello := Hello{
		ServerID: r.Header.Get(agentproto.HeaderServer),
		Version:  r.Header.Get(agentproto.HeaderVersion),
		Arch:     r.Header.Get(agentproto.HeaderArch),
		BootID:   r.Header.Get(agentproto.HeaderBoot),
		FromIP:   fromIP,
		Key:      r.Header.Get(agentproto.HeaderKey),
		Caps:     strings.Fields(strings.ReplaceAll(r.Header.Get(agentproto.HeaderCaps), ",", " ")),
	}
	if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		hello.Token = strings.TrimSpace(tok)
	}
	serverID, err := h.adm.AdmitAgent(r.Context(), hello)
	if err != nil {
		code, status := "agent_refused", http.StatusUnauthorized
		switch {
		case errors.Is(err, ErrRevoked):
			code, status = "agent_revoked", http.StatusForbidden
		case errors.Is(err, ErrUnknown):
		default:
			code, status = "agent_unavailable", http.StatusServiceUnavailable
			h.log.Warn("could not admit an agent", zap.Error(err), zap.String("server_id", hello.ServerID))
		}
		w.Header().Set("X-Toft-Error", code)
		http.Error(w, code, status)
		return
	}
	hello.ServerID = serverID
	// The live connection stays while it answers: a second one is an agent
	// that gave up on a link the panel still hears, or someone with only
	// the public key, who proves nothing until the SSH handshake.
	if h.answers(serverID) {
		w.Header().Set("X-Toft-Error", "agent_connected")
		http.Error(w, "agent_connected", http.StatusConflict)
		return
	}
	hdr := http.Header{}
	hdr.Set(agentproto.HeaderServer, serverID)
	ws, err := h.up.Upgrade(w, r, hdr)
	if err != nil {
		return // Upgrade answered
	}
	h.add(serverID, hello, ws)
}

// add registers the agent's connection, replacing an older one (a stale
// TCP connection the agent gave up on).
func (h *Hub) add(serverID string, hello Hello, ws *websocket.Conn) {
	a := &agent{hello: hello, since: time.Now(), pong: make(chan struct{}, 1)}
	a.conn = &tracked{Conn: wsconn.New(ws), gone: func() { h.gone(serverID, a) }}
	// Set before anyone reads the connection: the handler runs on its reader.
	ws.SetPongHandler(func(string) error {
		h.mu.Lock()
		a.rtt = time.Since(time.Unix(0, a.sent.Load()))
		h.mu.Unlock()
		select {
		case a.pong <- struct{}{}:
		default:
		}
		return nil
	})
	h.mu.Lock()
	old := h.agents[serverID]
	h.agents[serverID] = a
	delete(h.away, serverID)
	delete(h.upgrading, serverID) // back, on the new version or the old
	h.broadcastLocked()
	h.mu.Unlock()
	if old != nil {
		go old.conn.Close() // its close handshake may wait on a dead link
	}
	h.log.Info("agent connected", zap.String("server_id", serverID), zap.String("version", hello.Version), zap.String("from", hello.FromIP))
	go h.adm.AgentConnected(serverID, hello)
	go h.keepUp(serverID, a)
}

// answerWithin is how long a live connection has to answer a ping before
// a new one for the same server replaces it.
const answerWithin = 5 * time.Second

// answers reports whether the server's current connection answers a ping.
func (h *Hub) answers(serverID string) bool {
	h.mu.Lock()
	a := h.agents[serverID]
	h.mu.Unlock()
	if a == nil {
		return false
	}
	select {
	case <-a.pong:
	default:
	}
	if err := a.conn.Conn.Ping(answerWithin); err != nil {
		return false
	}
	t := time.NewTimer(answerWithin)
	defer t.Stop()
	select {
	case <-a.pong:
		return true
	case <-a.conn.done():
	case <-t.C:
	}
	return false
}

// keepUp pings the agent (it treats a minute of silence as a dead link),
// measures the round trip, and reports it seen every minute.
func (h *Hub) keepUp(serverID string, a *agent) {
	ping := time.NewTicker(pingEvery)
	seen := time.NewTicker(seenEvery)
	defer ping.Stop()
	defer seen.Stop()
	for {
		select {
		case <-a.conn.done():
			h.adm.AgentSeen(serverID)
			return
		case <-ping.C:
			a.sent.Store(time.Now().UnixNano())
			if err := a.conn.Conn.Ping(10 * time.Second); err != nil {
				_ = a.conn.Close()
			}
		case <-seen.C:
			h.adm.AgentSeen(serverID)
		}
	}
}

func (h *Hub) gone(serverID string, a *agent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.agents[serverID] == a {
		delete(h.agents, serverID)
		h.away[serverID] = time.Now()
		h.broadcastLocked()
		h.log.Info("agent disconnected", zap.String("server_id", serverID))
	}
}

// Conn hands the server's agent connection to the SSH pool, once per
// connection. While the agent is reconnecting it waits (until ctx ends);
// when it has been away past ReconnectGrace it fails at once.
func (h *Hub) Conn(ctx context.Context, serverID string) (net.Conn, error) {
	for {
		h.mu.Lock()
		a := h.agents[serverID]
		if a != nil && !a.taken {
			a.taken = true
			h.mu.Unlock()
			return a.conn, nil
		}
		if a != nil && a.taken {
			// The pool dials again: the SSH connection on this WebSocket
			// ended, so the WebSocket is spent. Drop it; the agent
			// reconnects.
			delete(h.agents, serverID)
			h.away[serverID] = time.Now()
			go func() { _ = a.conn.Close() }()
		}
		if !h.waitableLocked(serverID) {
			h.mu.Unlock()
			return nil, ErrOffline
		}
		ch := h.changed
		h.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ErrOffline
		case <-ch:
		}
	}
}

// waitableLocked: the agent left recently, or the panel started recently
// and it hasn't come back yet.
func (h *Hub) waitableLocked(serverID string) bool {
	if t, ok := h.away[serverID]; ok {
		return time.Since(t) < ReconnectGrace
	}
	return time.Since(h.started) < ReconnectGrace
}

// State reports the server's agent connection. ok is false when the panel
// hasn't heard from it since it started.
func (h *Hub) State(serverID string) (State, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	up := false
	if t, ok := h.upgrading[serverID]; ok && time.Since(t) < 3*time.Minute {
		up = true
	}
	if a := h.agents[serverID]; a != nil {
		return State{Connected: true, Since: a.since, RTT: a.rtt, Version: a.hello.Version, Upgrading: up, Caps: a.hello.Caps}, true
	}
	if t, ok := h.away[serverID]; ok {
		return State{Since: t, Upgrading: up}, true
	}
	return State{Upgrading: up}, false
}

// Started is when the hub (the backend) started.
func (h *Hub) Started() time.Time { return h.started }

// MarkUpgrading notes that the server's agent is restarting into a new
// version, until it reconnects (or 3 minutes pass).
func (h *Hub) MarkUpgrading(serverID string) {
	h.mu.Lock()
	h.upgrading[serverID] = time.Now()
	h.mu.Unlock()
}

// Drop closes the server's agent connection (revoked, or moved).
func (h *Hub) Drop(serverID string) {
	h.mu.Lock()
	a := h.agents[serverID]
	h.mu.Unlock()
	if a != nil {
		_ = a.conn.Close()
	}
}

// tracked tells the hub when its connection ends: on the first read or
// write error, or Close.
type tracked struct {
	*wsconn.Conn
	gone func()
	once sync.Once
	end  chan struct{}
	mu   sync.Mutex
}

func (t *tracked) done() <-chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.end == nil {
		t.end = make(chan struct{})
	}
	return t.end
}

func (t *tracked) finish() {
	t.once.Do(func() {
		t.mu.Lock()
		if t.end == nil {
			t.end = make(chan struct{})
		}
		close(t.end)
		t.mu.Unlock()
		t.gone()
	})
}

func (t *tracked) Read(p []byte) (int, error) {
	n, err := t.Conn.Read(p)
	if err != nil {
		t.finish()
	}
	return n, err
}

func (t *tracked) Write(p []byte) (int, error) {
	n, err := t.Conn.Write(p)
	if err != nil {
		t.finish()
	}
	return n, err
}

func (t *tracked) Close() error {
	err := t.Conn.Close()
	t.finish()
	return err
}
