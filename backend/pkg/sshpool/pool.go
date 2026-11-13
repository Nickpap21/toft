// Package sshpool keeps one long-lived SSH connection per managed server and
// shares it between every caller: deploys, logs, stats, PTYs, the provisioner
// and the Docker API tunnel.
//
// A server's entry holds a primary connection plus up to two overflow
// connections, opened only when every session slot on the others is taken.
// Connections are probed with keepalives, redialled lazily with exponential
// backoff after failures, retired (closed once idle) when the server's
// address, credentials or pinned host key change, and reaped after long
// idleness. Every call takes a context; cancelling one signals and closes
// its remote session.
//
// Host keys: a new primary connection is dialled with the pin stored now
// (Target.LoadPin), or with Target.PinnedHostKey when there is no LoadPin.
// While nothing is pinned it connects by trust on first use, and the key it
// saw is handed to Target.OnFirstUse, which persists the pin. That callback
// is the only place pins are written, and it runs at most once at a time per
// server because dials are single-flight. A handle made before an Invalidate
// (an operator replaced the pin) whose pin no longer matches fails on its
// own, without disturbing the connections made with the current pin.
package sshpool

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/sshclient"
	"go.uber.org/zap"
)

// ErrPoolClosed is returned by every call after Pool.Close.
var ErrPoolClosed = errors.New("sshpool: pool closed")

// ErrUnreachable is matched by errors.Is for every *UnreachableError.
var ErrUnreachable = errors.New("sshpool: server unreachable")

// UnreachableError is returned without dialling while a server is in backoff
// after failed connection attempts. It wraps the last dial error, so
// errors.As still finds, say, a *sshclient.HostKeyMismatchError.
type UnreachableError struct {
	ServerID string
	Name     string
	LastErr  error
	RetryIn  time.Duration
}

func (e *UnreachableError) Error() string {
	name := e.Name
	if name == "" {
		name = "server " + e.ServerID
	}
	return fmt.Sprintf("SSH connection to %s failed recently (retrying in %s): %v",
		name, e.RetryIn.Round(100*time.Millisecond), e.LastErr)
}

func (e *UnreachableError) Unwrap() error        { return e.LastErr }
func (e *UnreachableError) Is(target error) bool { return target == ErrUnreachable }

// Target is everything needed to reach one server. Callers build it from the
// server row on every use; the pool compares it with what its connections
// were dialled with.
type Target struct {
	ServerID      string
	Name          string // for messages and logs
	Host          string
	Port          int
	User          string
	PrivateKeyPEM string
	// PinnedHostKey is the stored pin (authorized_keys format), as the
	// caller read it. Empty means none was pinned then: unless LoadPin finds
	// one now, the next new connection trusts on first use.
	PinnedHostKey string
	// LoadPin reads the pin stored now ("" when none). When set, every new
	// primary connection is dialled with the stored pin rather than
	// PinnedHostKey, which may have been read before the key was pinned or
	// replaced: so a connection trusts on first use only while nothing is
	// pinned (a different stored key fails the handshake before any
	// credential is sent), and a caller holding a replaced pin cannot make
	// the pool dial with it. When it fails, PinnedHostKey is used if set;
	// otherwise the dial fails rather than trusting on first use.
	LoadPin func(ctx context.Context) (string, error)
	// OnFirstUse persists the host key a connection made without a pin saw.
	// It returns whether it pinned the key (false when the same key had
	// already been pinned, e.g. by a concurrent path), or an error (such as
	// a *sshclient.HostKeyMismatchError when a different key won), which
	// closes the connection and fails the call. Nil means nothing is
	// persisted.
	OnFirstUse func(ctx context.Context, authorizedKey string) (pinned bool, err error)
	// Conn, when set, is the server's agent (D-106): each connection runs
	// over the WebSocket the agent opened instead of TCP to Host:Port. An
	// agent carries one connection, with room for many sessions.
	Conn func(ctx context.Context) (net.Conn, error)
}

// Agent connections: one per server (it is the agent's one WebSocket), so
// it carries more sessions than OpenSSH's MaxSessions allows; a dial waits
// up to AgentDialTimeout for an agent that is reconnecting.
const (
	AgentSessionsPerConn = 64
	AgentDialTimeout     = 30 * time.Second
)

func (t Target) addr() string {
	if t.Conn != nil {
		return "agent:" + t.Name
	}
	return net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
}

// credHash identifies the connection parameters other than the pin.
func credHash(t Target) [32]byte {
	h := sha256.New()
	for _, s := range []string{t.Host, strconv.Itoa(t.Port), t.User, t.PrivateKeyPEM, strconv.FormatBool(t.Conn != nil)} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

// normalizeKey returns the canonical form of an authorized key, or the input
// when it does not parse (so it never equals a live key and fails closed).
func normalizeKey(authorizedKey string) string {
	k, err := sshclient.ParseHostKey(authorizedKey)
	if err != nil {
		return authorizedKey
	}
	return sshclient.MarshalHostKey(k)
}

// Pool shares SSH connections between callers, keyed by server ID.
type Pool struct {
	opts   Options
	log    *zap.Logger
	ctx    context.Context // cancelled by Close; parent of dials and probes
	cancel context.CancelFunc
	closed atomic.Bool

	mu      sync.Mutex
	entries map[string]*entry
	// gens counts each server's Invalidate calls. It outlives the server's
	// entry (entries are reaped when idle), so a handle can tell whether it
	// was made before the latest Invalidate.
	gens map[string]uint64

	closeOnce sync.Once
	wg        sync.WaitGroup
}

// New returns a pool and starts its reaper. Call Close when done.
func New(opts Options) *Pool {
	opts = opts.withDefaults()
	ctx, cancel := context.WithCancel(context.Background())
	p := &Pool{
		opts:    opts,
		log:     opts.Logger,
		ctx:     ctx,
		cancel:  cancel,
		entries: map[string]*entry{},
		gens:    map[string]uint64{},
	}
	p.wg.Add(1)
	go p.reapLoop()
	return p
}

// Handle returns the RemoteHost for t. It is cheap and holds nothing: build
// one per operation from the current server row.
func (p *Pool) Handle(t Target) RemoteHost {
	p.mu.Lock()
	gen := p.gens[t.ServerID]
	p.mu.Unlock()
	return &handle{p: p, t: t, gen: gen}
}

// Invalidate closes every connection to the server now, failing the
// sessions in flight, and forgets its backoff. Use it when the server is
// deleted or its host key is replaced. Handles made before it whose pin no
// longer matches fail on their own from then on.
func (p *Pool) Invalidate(serverID string) {
	p.mu.Lock()
	p.gens[serverID]++
	gen := p.gens[serverID]
	e := p.entries[serverID]
	p.mu.Unlock()
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.gen = gen
	e.epoch++ // a dial in flight is discarded when it lands
	for _, c := range e.conns {
		c.retired = true
		_ = c.client.Close()
	}
	e.hashSet = false
	e.failures = 0
	e.backoffUntil = time.Time{}
	e.lastDialErr = nil
	e.broadcastLocked()
	p.log.Info("SSH connections invalidated", zap.String("server_id", serverID))
}

// Retry forgets the server's backoff, so the next call dials at once. The
// agent hub calls it when an agent connects, since the dials that failed
// while it was away say nothing about now.
func (p *Pool) Retry(serverID string) {
	p.mu.Lock()
	e := p.entries[serverID]
	p.mu.Unlock()
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.failures, e.backoffUntil, e.lastDialErr = 0, time.Time{}, nil
	e.broadcastLocked()
}

// Close closes every connection, stops the pool's goroutines and makes every
// later call return ErrPoolClosed. It is idempotent and always returns nil.
func (p *Pool) Close() error {
	p.closeOnce.Do(func() {
		p.closed.Store(true)
		p.cancel()
		p.mu.Lock()
		entries := make([]*entry, 0, len(p.entries))
		for _, e := range p.entries {
			entries = append(entries, e)
		}
		p.mu.Unlock()
		for _, e := range entries {
			e.mu.Lock()
			for _, c := range e.conns {
				c.retired = true
				_ = c.client.Close()
			}
			e.broadcastLocked()
			e.mu.Unlock()
		}
		p.wg.Wait()
	})
	return nil
}

// entry is the pool's state for one server.
type entry struct {
	id string

	mu      sync.Mutex
	refs    int // callers inside acquire; guarded by Pool.mu, not mu
	conns   []*pconn
	hashSet bool
	hash    [32]byte
	epoch   uint64
	gen     uint64        // Pool.gens for the server, as of the last Invalidate
	changed chan struct{} // closed and replaced on every state change

	dialing      bool
	dialGen      uint64 // dials completed
	lastDialErr  error  // error of the last completed dial, nil on success
	failures     int
	backoffUntil time.Time
}

// pconn is one pooled connection.
type pconn struct {
	client      *sshclient.Client
	primary     bool
	hostKey     string // authorized_keys form of the key it presented
	newlyPinned bool   // OnFirstUse pinned the key when it was dialled
	createdAt   time.Time
	agent       bool // over the agent's WebSocket: its one link, never reaped for idleness

	// Guarded by entry.mu.
	inUse    int // session slots taken
	tunnels  int // open Unix-socket tunnels (no slot)
	capacity int // session slots; lowered when the server refuses sessions
	retired  bool
	closed   bool
	lastUsed time.Time
}

func (c *pconn) busy() bool { return c.inUse > 0 || c.tunnels > 0 }

func (c *pconn) dead() bool {
	select {
	case <-c.client.Done():
		return true
	default:
		return false
	}
}

func (e *entry) broadcastLocked() {
	close(e.changed)
	e.changed = make(chan struct{})
}

// usableLocked reports whether c may take new work.
func usableLocked(c *pconn) bool { return !c.retired && !c.closed && !c.dead() }

// liveLocked returns the connections that may take new work, primary first.
func (e *entry) liveLocked() []*pconn {
	var live []*pconn
	for _, c := range e.conns {
		if usableLocked(c) {
			live = append(live, c)
		}
	}
	return live
}

// liveKeyLocked is the host key the live connections presented ("" if none).
func (e *entry) liveKeyLocked() string {
	if live := e.liveLocked(); len(live) > 0 {
		return live[0].hostKey
	}
	return ""
}

// retireAllLocked stops every connection from taking new work. Idle ones
// close now; busy ones close when their last session or tunnel ends, so
// in-flight work such as a build finishes on the old connection.
func (e *entry) retireAllLocked() {
	for _, c := range e.conns {
		if c.retired {
			continue
		}
		c.retired = true
		if !c.busy() {
			_ = c.client.Close()
		}
	}
}

// reconcileLocked retires the connections when t no longer describes them:
// another address, user or key, or a non-empty pin other than the key they
// presented. An empty pin (a row read before the first pin) retires nothing.
// A caller whose pin is not the key the connections present holds a
// replaced pin when its handle predates the latest Invalidate (gen), or when
// it already retired them once in this call and the new connection, dialled
// with the stored pin, presents another key again: only its call fails, so
// it cannot retire the connections made with the current pin over and over
// or put the server into backoff. pinRetired is that call's state.
func (p *Pool) reconcileLocked(e *entry, t Target, gen uint64, pinRetired *bool) error {
	h := credHash(t)
	switch {
	case !e.hashSet:
		e.hash, e.hashSet = h, true
	case e.hash != h:
		if len(e.liveLocked()) > 0 {
			p.log.Info("SSH target changed; retiring its connections",
				zap.String("server_id", e.id), zap.String("server", t.Name))
		}
		e.retireAllLocked()
		e.hash = h
		e.failures, e.backoffUntil, e.lastDialErr = 0, time.Time{}, nil
	}
	if t.PinnedHostKey == "" {
		return nil
	}
	live := e.liveKeyLocked()
	if live == "" || normalizeKey(t.PinnedHostKey) == live {
		return nil
	}
	if gen < e.gen || *pinRetired {
		return replacedPinError(t, live)
	}
	p.log.Info("pinned SSH host key changed; retiring its connections",
		zap.String("server_id", e.id), zap.String("server", t.Name))
	e.retireAllLocked()
	e.failures, e.backoffUntil, e.lastDialErr = 0, time.Time{}, nil
	*pinRetired = true
	return nil
}

// replacedPinError fails a caller holding a pin that was replaced while it
// ran: the server's connections present live, the key pinned now.
func replacedPinError(t Target, live string) error {
	mismatch := &sshclient.HostKeyMismatchError{Addr: t.addr(), Expected: "an unreadable pinned key"}
	if fp, keyType, err := sshclient.Fingerprint(t.PinnedHostKey); err == nil {
		mismatch.Expected, mismatch.ExpectedType = fp, keyType
	}
	mismatch.Presented, _, _ = sshclient.Fingerprint(live)
	name := t.Name
	if name == "" {
		name = "server " + t.ServerID
	}
	return fmt.Errorf("the SSH host key of %s was replaced while this operation ran, so it was stopped: %w", name, mismatch)
}

// acquireOpts says what a caller needs from a connection.
type acquireOpts struct {
	slot   bool // a session slot (exec, stream, upload, PTY)
	tunnel bool // a Unix-socket tunnel (no slot)
	force  bool // dial even during backoff
}

// lease is a caller's hold on a connection; release gives it back.
type lease struct {
	p    *Pool
	e    *entry
	c    *pconn
	o    acquireOpts
	once sync.Once
}

func (l *lease) release() {
	l.once.Do(func() {
		l.e.mu.Lock()
		defer l.e.mu.Unlock()
		if l.o.slot {
			l.c.inUse--
		}
		if l.o.tunnel {
			l.c.tunnels--
		}
		l.c.lastUsed = time.Now()
		if l.c.retired && !l.c.busy() {
			_ = l.c.client.Close()
		}
		l.e.broadcastLocked()
	})
}

// refused handles a session the server refused while this lease held a
// slot. If the connection carries other sessions, the server's limit is
// lower than ours: the connection's capacity drops to what it carries, the
// slot is released and refused reports true (retry). Otherwise the refusal
// is not about load and refused reports false; the lease is untouched.
func (l *lease) refused() bool {
	l.e.mu.Lock()
	others := l.c.inUse - 1
	if others <= 0 {
		l.e.mu.Unlock()
		return false
	}
	l.c.capacity = others
	l.e.mu.Unlock()
	l.p.log.Info("server refused an SSH session; lowering the connection's session limit",
		zap.String("server_id", l.e.id), zap.Int("sessions", others))
	l.release()
	return true
}

// broken closes the lease's connection, which failed at the transport
// level, and releases the lease.
func (l *lease) broken() {
	l.e.mu.Lock()
	l.c.closed = true // not picked again before the watcher removes it
	l.e.mu.Unlock()
	_ = l.c.client.Close()
	l.release()
}

// ref returns the server's entry, creating it, and pins it in the map until
// unref.
func (p *Pool) ref(serverID string) (*entry, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed.Load() {
		return nil, ErrPoolClosed
	}
	e := p.entries[serverID]
	if e == nil {
		e = &entry{id: serverID, changed: make(chan struct{}), gen: p.gens[serverID]}
		p.entries[serverID] = e
	}
	e.refs++
	return e, nil
}

func (p *Pool) unref(e *entry) {
	p.mu.Lock()
	e.refs--
	p.mu.Unlock()
}

// acquire returns a lease on a live connection to h's server, dialling one
// if needed and waiting (bounded by ctx) while every slot is taken or a dial
// is in flight.
func (p *Pool) acquire(ctx context.Context, h *handle, o acquireOpts) (*lease, error) {
	t := h.t
	if t.ServerID == "" {
		return nil, errors.New("sshpool: target has no server ID")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e, err := p.ref(t.ServerID)
	if err != nil {
		return nil, err
	}
	defer p.unref(e)

	e.mu.Lock()
	waitedOnDial, waitedGen := false, uint64(0)
	pinRetired := false
	for {
		if p.closed.Load() {
			e.mu.Unlock()
			return nil, ErrPoolClosed
		}
		if err := p.reconcileLocked(e, t, h.gen, &pinRetired); err != nil {
			e.mu.Unlock()
			return nil, err
		}

		live := e.liveLocked()
		for _, c := range live {
			if o.slot && c.inUse >= c.capacity {
				continue
			}
			if o.slot {
				c.inUse++
			}
			if o.tunnel {
				c.tunnels++
			}
			c.lastUsed = time.Now()
			e.mu.Unlock()
			return &lease{p: p, e: e, c: c, o: o}, nil
		}

		// The dial this caller waited for failed and nothing else is up.
		if waitedOnDial && e.dialGen != waitedGen && e.lastDialErr != nil && len(live) == 0 {
			err := e.lastDialErr
			e.mu.Unlock()
			return nil, err
		}

		var backoffEnds time.Time
		maxConns := p.opts.MaxConnsPerServer
		if t.Conn != nil {
			maxConns = 1
		}
		if !e.dialing && len(live) < maxConns {
			now := time.Now()
			switch {
			case now.Before(e.backoffUntil) && !o.force:
				if len(live) == 0 {
					err := &UnreachableError{ServerID: t.ServerID, Name: t.Name, LastErr: e.lastDialErr, RetryIn: e.backoffUntil.Sub(now)}
					e.mu.Unlock()
					return nil, err
				}
				// Overflow dials wait out the backoff: queue for a slot, and
				// look again when the backoff ends, since nothing else may
				// change by then.
				backoffEnds = e.backoffUntil
			default:
				p.startDialLocked(e, t, len(live) == 0)
			}
		}
		if e.dialing && !waitedOnDial {
			waitedOnDial, waitedGen = true, e.dialGen
		}

		ch := e.changed
		e.mu.Unlock()
		if err := waitChange(ctx, ch, backoffEnds); err != nil {
			return nil, err
		}
		e.mu.Lock()
	}
}

// waitChange waits until ch is closed or, when until is set, until then. It
// returns ctx's error if ctx ends first.
func waitChange(ctx context.Context, ch <-chan struct{}, until time.Time) error {
	var timeout <-chan time.Time
	if !until.IsZero() {
		t := time.NewTimer(time.Until(until))
		defer t.Stop()
		timeout = t.C
	}
	select {
	case <-ch:
	case <-timeout:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// startDialLocked starts the entry's one dial. A primary dial uses the pin
// stored now when t can read it (Target.LoadPin), else t's pin, and trusts
// on first use when that is empty; an overflow dial pins the key the live
// connections presented, so OnFirstUse never runs for it.
func (p *Pool) startDialLocked(e *entry, t Target, primary bool) {
	e.dialing = true
	pin := t.PinnedHostKey
	if pin == "" && !primary {
		pin = e.liveKeyLocked()
	}
	p.wg.Add(1)
	go p.dial(e, t, pin, primary, e.epoch)
}

// dial runs detached from the caller that started it, so a cancelled caller
// does not fail the others waiting on the same dial.
func (p *Pool) dial(e *entry, t Target, pin string, primary bool, epoch uint64) {
	defer p.wg.Done()
	log := p.log.With(zap.String("server_id", t.ServerID), zap.String("server", t.Name), zap.String("addr", t.addr()))

	var (
		client *sshclient.Client
		err    error
	)
	if primary && t.LoadPin != nil {
		pin, err = p.storedPin(t, pin, log)
	}
	if err == nil && t.Conn != nil && pin == "" {
		// An agent is trusted only by the key it joined with, never on
		// first use (D-106).
		err = fmt.Errorf("%s has no agent key pinned: join it with a token", t.Name)
	}
	if err == nil {
		timeout := p.opts.DialTimeout
		if t.Conn != nil {
			timeout = max(timeout, AgentDialTimeout)
		}
		ctx, cancel := context.WithTimeout(p.ctx, timeout)
		client, err = p.opts.Dialer(ctx, sshclient.Config{
			Host:             t.Host,
			Port:             t.Port,
			User:             t.User,
			PrivateKeyPEM:    t.PrivateKeyPEM,
			PinnedHostKey:    pin,
			HandshakeTimeout: timeout,
			Conn:             t.Conn,
		})
		cancel()
	}

	var key string
	newlyPinned := false
	if err == nil {
		key = sshclient.MarshalHostKey(client.HostKey())
		if pin == "" && t.OnFirstUse != nil {
			fctx, fcancel := context.WithTimeout(p.ctx, p.opts.FirstUseTimeout)
			newlyPinned, err = t.OnFirstUse(fctx, key)
			fcancel()
			if err != nil {
				_ = client.Close()
				if !errors.Is(err, sshclient.ErrHostKeyMismatch) {
					err = fmt.Errorf("record the SSH host key of %s: %w", t.Name, err)
				}
			}
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.dialing = false
	e.dialGen++
	defer e.broadcastLocked()

	if p.closed.Load() || e.epoch != epoch {
		// Closed or invalidated while dialling: drop the result. Waiters
		// loop and see ErrPoolClosed or start a fresh dial.
		if err == nil {
			_ = client.Close()
		}
		e.lastDialErr = nil
		return
	}
	if err != nil {
		e.lastDialErr = err
		e.failures++
		wait := p.backoff(e.failures)
		e.backoffUntil = time.Now().Add(wait)
		log.Warn("SSH connection failed", zap.Error(err), zap.Int("failures", e.failures), zap.Duration("retry_in", wait))
		return
	}

	e.lastDialErr = nil
	e.failures = 0
	e.backoffUntil = time.Time{}
	now := time.Now()
	c := &pconn{
		client:      client,
		primary:     primary,
		hostKey:     key,
		newlyPinned: newlyPinned,
		createdAt:   now,
		lastUsed:    now,
		capacity:    p.opts.MaxSessionsPerConn,
	}
	if t.Conn != nil {
		c.capacity = AgentSessionsPerConn
		c.agent = true
	}
	if primary {
		e.conns = append([]*pconn{c}, e.conns...)
	} else {
		e.conns = append(e.conns, c)
	}
	if newlyPinned {
		fp, typ, _ := sshclient.Fingerprint(key)
		log.Info("pinned SSH host key on first connection", zap.String("key_type", typ), zap.String("fingerprint", fp))
	}
	log.Debug("SSH connection opened", zap.Bool("primary", primary))

	p.wg.Add(2)
	go p.watch(e, c, log)
	go p.keepalive(c, log)
}

// storedPin returns the pin a primary dial for t uses: the one stored now
// (t.LoadPin), which is "" only when nothing is pinned. If it cannot be read,
// the caller's pin is used when it has one; otherwise the dial fails rather
// than trust on first use while a key may be pinned.
func (p *Pool) storedPin(t Target, callerPin string, log *zap.Logger) (string, error) {
	ctx, cancel := context.WithTimeout(p.ctx, p.opts.FirstUseTimeout)
	defer cancel()
	stored, err := t.LoadPin(ctx)
	if err == nil {
		return stored, nil
	}
	if callerPin != "" {
		log.Warn("could not read the pinned SSH host key; using the caller's pin", zap.Error(err))
		return callerPin, nil
	}
	return "", fmt.Errorf("read the pinned SSH host key of %s: %w", t.Name, err)
}

// backoff returns the wait after the n-th consecutive failed dial: random
// between BackoffMin and min(BackoffMax, BackoffMin·2^(n-1)).
func (p *Pool) backoff(n int) time.Duration {
	lo, hi := p.opts.BackoffMin, p.opts.BackoffMin
	for i := 1; i < n && hi < p.opts.BackoffMax; i++ {
		hi *= 2
	}
	if hi > p.opts.BackoffMax {
		hi = p.opts.BackoffMax
	}
	if hi <= lo {
		return lo
	}
	return lo + rand.N(hi-lo)
}

// watch removes c from its entry when the connection ends.
func (p *Pool) watch(e *entry, c *pconn, log *zap.Logger) {
	defer p.wg.Done()
	<-c.client.Done()
	e.mu.Lock()
	defer e.mu.Unlock()
	c.closed = true
	for i, x := range e.conns {
		if x == c {
			e.conns = append(e.conns[:i], e.conns[i+1:]...)
			break
		}
	}
	if c.primary {
		// Promote the oldest live overflow connection.
		for _, x := range e.conns {
			if usableLocked(x) {
				x.primary = true
				break
			}
		}
	}
	e.broadcastLocked()
	log.Debug("SSH connection closed", zap.Bool("primary", c.primary))
}

// keepalive probes c every KeepaliveInterval and closes it when a probe
// goes unanswered for KeepaliveTimeout.
func (p *Pool) keepalive(c *pconn, log *zap.Logger) {
	defer p.wg.Done()
	t := time.NewTicker(p.opts.KeepaliveInterval)
	defer t.Stop()
	for {
		select {
		case <-c.client.Done():
			return
		case <-p.ctx.Done():
			return
		case <-t.C:
		}
		ctx, cancel := context.WithTimeout(p.ctx, p.opts.KeepaliveTimeout)
		_, err := c.client.KeepaliveContext(ctx)
		cancel()
		if err != nil {
			if p.ctx.Err() != nil {
				return
			}
			log.Warn("SSH keepalive failed; closing the connection", zap.Error(err))
			_ = c.client.Close()
			return
		}
	}
}

func (p *Pool) reapLoop() {
	defer p.wg.Done()
	t := time.NewTicker(p.opts.ReapInterval)
	defer t.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case now := <-t.C:
			p.reap(now)
		}
	}
}

// reap closes connections idle past their limit and forgets entries that
// hold nothing (no connections, no dial, no backoff, no caller).
func (p *Pool) reap(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, e := range p.entries {
		e.mu.Lock()
		for _, c := range e.conns {
			if c.busy() || c.closed || (c.agent && !c.retired) {
				continue
			}
			limit := p.opts.OverflowIdle
			if c.primary {
				limit = p.opts.PrimaryIdle
			}
			if c.retired || now.Sub(c.lastUsed) >= limit {
				c.retired = true
				_ = c.client.Close()
			}
		}
		forget := e.refs == 0 && len(e.conns) == 0 && !e.dialing && !now.Before(e.backoffUntil)
		e.mu.Unlock()
		if forget {
			delete(p.entries, id)
		}
	}
}

// connStats is a snapshot of one connection, for tests.
type connStats struct {
	Primary  bool
	InUse    int
	Tunnels  int
	Capacity int
	Retired  bool
}

// stats returns a snapshot of a server's connections, primary first.
func (p *Pool) stats(serverID string) []connStats {
	p.mu.Lock()
	e := p.entries[serverID]
	p.mu.Unlock()
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]connStats, 0, len(e.conns))
	for _, c := range e.conns {
		out = append(out, connStats{Primary: c.primary, InUse: c.inUse, Tunnels: c.tunnels, Capacity: c.capacity, Retired: c.retired})
	}
	return out
}
