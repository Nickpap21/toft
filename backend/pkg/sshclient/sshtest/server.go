// Package sshtest runs an in-process SSH server for tests of the SSH client,
// the connection pool and anything built on them. It needs no sshd and no
// Docker daemon.
//
// The server accepts one client key (Server.ClientKeyPEM) for any user name,
// runs exec requests through a handler, echoes PTY shells, can limit
// sessions per connection like sshd's MaxSessions, answers (or ignores)
// keepalives, records signals, serves direct-streamlocal channels through a
// handler, and counts what happened.
package sshtest

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// Session is one exec request as the handler sees it.
type Session struct {
	Command string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	// Done is closed when the client closes the session or the connection
	// drops.
	Done <-chan struct{}
	// Signals receives the names of signals the client sends ("TERM").
	Signals <-chan string
}

// ExecHandler runs a command and returns its exit status.
type ExecHandler func(s *Session) int

// StreamLocalHandler serves one direct-streamlocal channel (a Unix socket
// dial such as the Docker API tunnel). It owns ch and closes it when done.
type StreamLocalHandler func(socketPath string, ch io.ReadWriteCloser)

// Option configures a Server.
type Option func(*Server)

// WithHostKeys sets the host keys the server offers (default: one ed25519).
func WithHostKeys(signers ...ssh.Signer) Option {
	return func(s *Server) { s.hostKeys = signers }
}

// WithMaxSessions limits open sessions per connection; a session past the
// limit is rejected with ssh.Prohibited, as sshd does past MaxSessions.
// Zero means no limit.
func WithMaxSessions(n int) Option {
	return func(s *Server) { s.maxSessions.Store(int64(n)) }
}

// WithExec replaces DefaultExec.
func WithExec(h ExecHandler) Option {
	return func(s *Server) { s.exec = h }
}

// WithStreamLocal serves direct-streamlocal channels with h. Without it
// they are rejected.
func WithStreamLocal(h StreamLocalHandler) Option {
	return func(s *Server) { s.streamLocal = h }
}

// Server is an in-process SSH server listening on 127.0.0.1.
type Server struct {
	// Addr is the listening address, Host and Port its parts.
	Addr string
	Host string
	Port int
	// ClientKeyPEM is the private key (OpenSSH PEM) the server accepts.
	ClientKeyPEM string
	// ClientKey is its public half.
	ClientKey ssh.PublicKey

	ln          net.Listener
	exec        ExecHandler
	streamLocal StreamLocalHandler
	maxSessions atomic.Int64
	keepalive   atomic.Bool

	mu       sync.Mutex
	hostKeys []ssh.Signer
	conns    map[net.Conn]*connState
	signals  []string
	commands []string
	files    map[string][]byte
	closed   bool

	accepts      atomic.Int64
	authAttempts atomic.Int64
	sessions     atomic.Int64
	peakSessions atomic.Int64
	running      atomic.Int64
	peakRunning  atomic.Int64
	rejected     atomic.Int64

	wg sync.WaitGroup
}

type connState struct {
	mu       sync.Mutex
	sessions int
}

// New starts a server and stops it when the test ends.
func New(t testing.TB, opts ...Option) *Server {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("sshtest: client key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "sshtest client")
	if err != nil {
		t.Fatalf("sshtest: marshal client key: %v", err)
	}
	clientPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("sshtest: client public key: %v", err)
	}

	s := &Server{
		ClientKeyPEM: string(pem.EncodeToMemory(block)),
		ClientKey:    clientPub,
		conns:        map[net.Conn]*connState{},
		files:        map[string][]byte{},
	}
	s.keepalive.Store(true)
	s.exec = s.DefaultExec
	for _, o := range opts {
		o(s)
	}
	if len(s.hostKeys) == 0 {
		s.hostKeys = []ssh.Signer{NewEd25519Signer(t)}
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("sshtest: listen: %v", err)
	}
	s.ln = ln
	s.Addr = ln.Addr().String()
	host, port, _ := net.SplitHostPort(s.Addr)
	s.Host = host
	s.Port, _ = strconv.Atoi(port)

	s.wg.Add(1)
	go s.acceptLoop()
	t.Cleanup(s.Close)
	return s
}

// NewEd25519Signer returns a fresh ed25519 host key.
func NewEd25519Signer(t testing.TB) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("sshtest: ed25519 key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("sshtest: ed25519 signer: %v", err)
	}
	return signer
}

var (
	rsaOnce sync.Once
	rsaKey  *rsa.PrivateKey
	rsaErr  error
)

// NewRSASigner returns an RSA host key. RSA generation is slow, so every
// call in one test binary shares the same key.
func NewRSASigner(t testing.TB) ssh.Signer {
	t.Helper()
	rsaOnce.Do(func() { rsaKey, rsaErr = rsa.GenerateKey(rand.Reader, 2048) })
	if rsaErr != nil {
		t.Fatalf("sshtest: rsa key: %v", rsaErr)
	}
	signer, err := ssh.NewSignerFromKey(rsaKey)
	if err != nil {
		t.Fatalf("sshtest: rsa signer: %v", err)
	}
	return signer
}

// HostKey returns the public half of the server's first host key.
func (s *Server) HostKey() ssh.PublicKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hostKeys[0].PublicKey()
}

// SetHostKey replaces the host keys offered to new connections, as a
// rebuilt server would.
func (s *Server) SetHostKey(signers ...ssh.Signer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hostKeys = signers
}

// SetMaxSessions changes the per-connection session limit (0: none).
func (s *Server) SetMaxSessions(n int) { s.maxSessions.Store(int64(n)) }

// SetKeepaliveReplies switches keepalive replies on (the default) or off.
// Off, keepalive requests get no answer at all, like a stalled peer.
func (s *Server) SetKeepaliveReplies(on bool) { s.keepalive.Store(on) }

// DropAll severs every open connection from the server side.
func (s *Server) DropAll() {
	s.mu.Lock()
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// Close stops the listener, drops every connection and waits for the
// server's goroutines. It is safe to call more than once.
func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	_ = s.ln.Close()
	s.DropAll()
	s.wg.Wait()
}

// Accepts counts TCP connections accepted.
func (s *Server) Accepts() int { return int(s.accepts.Load()) }

// Conns counts connections open now.
func (s *Server) Conns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// AuthAttempts counts authentication attempts of any method (including
// "none"). A handshake aborted on the host key makes none.
func (s *Server) AuthAttempts() int { return int(s.authAttempts.Load()) }

// Sessions counts sessions open now, across connections.
func (s *Server) Sessions() int { return int(s.sessions.Load()) }

// PeakSessions is the highest Sessions has been. A session counts until the
// server has processed its close, which can trail the client a little; for
// the exact number of commands running at once, use PeakRunning.
func (s *Server) PeakSessions() int { return int(s.peakSessions.Load()) }

// Running counts exec commands and shells running now: from the request
// until the handler returns, before the client sees the exit status.
func (s *Server) Running() int { return int(s.running.Load()) }

// PeakRunning is the highest Running has been.
func (s *Server) PeakRunning() int { return int(s.peakRunning.Load()) }

// Rejected counts sessions refused for the session limit.
func (s *Server) Rejected() int { return int(s.rejected.Load()) }

// Signals returns the signal names received, in order.
func (s *Server) Signals() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.signals...)
}

// Commands returns the exec commands received, in order.
func (s *Server) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

// File returns what DefaultExec's `cat > path` stored at path.
func (s *Server) File(path string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.files[path]
	return b, ok
}

// DefaultExec understands a few commands:
//
//	echo TEXT     prints TEXT
//	lines N       prints "line 1" .. "line N", odd lines to stderr
//	cat > PATH    stores stdin as File(PATH) (quotes around PATH are removed)
//	exit N        exits with status N
//	block         runs until the session closes or a signal arrives (status 143)
//
// Anything else prints an error to stderr and exits 127.
func (s *Server) DefaultExec(sess *Session) int {
	cmd := strings.TrimSpace(sess.Command)
	switch {
	case cmd == "block":
		select {
		case <-sess.Done:
			return 129
		case <-sess.Signals:
			return 143
		}
	case strings.HasPrefix(cmd, "echo "):
		_, _ = fmt.Fprintln(sess.Stdout, strings.TrimPrefix(cmd, "echo "))
		return 0
	case strings.HasPrefix(cmd, "lines "):
		n, _ := strconv.Atoi(strings.TrimPrefix(cmd, "lines "))
		for i := 1; i <= n; i++ {
			w := sess.Stdout
			if i%2 == 1 {
				w = sess.Stderr
			}
			_, _ = fmt.Fprintf(w, "line %d\n", i)
		}
		return 0
	case strings.HasPrefix(cmd, "cat > "):
		path := strings.Trim(strings.TrimPrefix(cmd, "cat > "), "'")
		b, err := io.ReadAll(sess.Stdin)
		if err != nil {
			return 1
		}
		s.mu.Lock()
		s.files[path] = b
		s.mu.Unlock()
		return 0
	case strings.HasPrefix(cmd, "exit "):
		n, _ := strconv.Atoi(strings.TrimPrefix(cmd, "exit "))
		return n
	}
	_, _ = fmt.Fprintf(sess.Stderr, "sshtest: unknown command %q\n", cmd)
	return 127
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		nc, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.accepts.Add(1)
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = nc.Close()
			return
		}
		cs := &connState{}
		s.conns[nc] = cs
		hostKeys := append([]ssh.Signer(nil), s.hostKeys...)
		s.wg.Add(1)
		s.mu.Unlock()
		go s.serveConn(nc, cs, hostKeys)
	}
}

func (s *Server) serveConn(nc net.Conn, cs *connState, hostKeys []ssh.Signer) {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		delete(s.conns, nc)
		s.mu.Unlock()
		_ = nc.Close()
	}()

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(key.Marshal(), s.ClientKey.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, errors.New("sshtest: unknown client key")
		},
		AuthLogCallback: func(ssh.ConnMetadata, string, error) { s.authAttempts.Add(1) },
	}
	for _, k := range hostKeys {
		cfg.AddHostKey(k)
	}
	conn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for req := range reqs {
			if req.Type == "keepalive@openssh.com" && !s.keepalive.Load() {
				continue // a stalled peer: no answer at all
			}
			if req.WantReply {
				// OpenSSH answers unknown global requests, keepalives
				// included, with a failure.
				_ = req.Reply(false, nil)
			}
		}
	}()

	var chWG sync.WaitGroup
	for nch := range chans {
		switch nch.ChannelType() {
		case "session":
			if !s.reserveSession(cs) {
				s.rejected.Add(1)
				_ = nch.Reject(ssh.Prohibited, "open failed")
				continue
			}
			chWG.Add(1)
			go func() {
				defer chWG.Done()
				s.serveSession(cs, nch)
			}()
		case "direct-streamlocal@openssh.com":
			if s.streamLocal == nil {
				_ = nch.Reject(ssh.Prohibited, "streamlocal forwarding disabled")
				continue
			}
			var msg struct {
				SocketPath string
				Reserved0  string
				Reserved1  uint32
			}
			if err := ssh.Unmarshal(nch.ExtraData(), &msg); err != nil {
				_ = nch.Reject(ssh.ConnectionFailed, "bad request")
				continue
			}
			ch, chReqs, err := nch.Accept()
			if err != nil {
				continue
			}
			go ssh.DiscardRequests(chReqs)
			chWG.Add(1)
			go func() {
				defer chWG.Done()
				s.streamLocal(msg.SocketPath, ch)
			}()
		default:
			_ = nch.Reject(ssh.UnknownChannelType, "unsupported channel type")
		}
	}
	chWG.Wait()
}

// reserveSession takes a session slot on the connection. At the limit it
// waits briefly first: the client may have sent the close of an earlier
// session just before this open, and sshd, which handles a connection's
// messages in order, would have freed that slot already.
func (s *Server) reserveSession(cs *connState) bool {
	deadline := time.Now().Add(100 * time.Millisecond)
	for {
		cs.mu.Lock()
		limit := int(s.maxSessions.Load())
		if limit <= 0 || cs.sessions < limit {
			cs.sessions++
			cs.mu.Unlock()
			raisePeak(&s.peakSessions, s.sessions.Add(1))
			return true
		}
		cs.mu.Unlock()
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// raisePeak records n in peak if it is higher.
func raisePeak(peak *atomic.Int64, n int64) {
	for {
		p := peak.Load()
		if n <= p || peak.CompareAndSwap(p, n) {
			return
		}
	}
}

// track counts a running command or shell until the returned func is called.
func (s *Server) track() func() {
	raisePeak(&s.peakRunning, s.running.Add(1))
	return func() { s.running.Add(-1) }
}

func (s *Server) releaseSession(cs *connState) {
	cs.mu.Lock()
	cs.sessions--
	cs.mu.Unlock()
	s.sessions.Add(-1)
}

func (s *Server) serveSession(cs *connState, nch ssh.NewChannel) {
	defer s.releaseSession(cs)
	ch, reqs, err := nch.Accept()
	if err != nil {
		return
	}
	done := make(chan struct{})
	signals := make(chan string, 8)
	started := false
	var running sync.WaitGroup

	for req := range reqs {
		switch req.Type {
		case "exec":
			var msg struct{ Command string }
			if started || ssh.Unmarshal(req.Payload, &msg) != nil {
				_ = req.Reply(false, nil)
				continue
			}
			started = true
			s.mu.Lock()
			s.commands = append(s.commands, msg.Command)
			s.mu.Unlock()
			_ = req.Reply(true, nil)
			running.Add(1)
			untrack := s.track()
			go func() {
				defer running.Done()
				code := s.exec(&Session{
					Command: msg.Command,
					Stdin:   ch,
					Stdout:  ch,
					Stderr:  ch.Stderr(),
					Done:    done,
					Signals: signals,
				})
				untrack()
				finish(ch, code)
			}()
		case "shell":
			if started {
				_ = req.Reply(false, nil)
				continue
			}
			started = true
			_ = req.Reply(true, nil)
			running.Add(1)
			untrack := s.track()
			go func() {
				defer running.Done()
				// An echoing terminal: what is typed comes back.
				_, _ = io.Copy(ch, ch)
				untrack()
				finish(ch, 0)
			}()
		case "pty-req":
			_ = req.Reply(true, nil)
		case "signal":
			var msg struct{ Signal string }
			if ssh.Unmarshal(req.Payload, &msg) == nil {
				s.mu.Lock()
				s.signals = append(s.signals, msg.Signal)
				s.mu.Unlock()
				select {
				case signals <- msg.Signal:
				default:
				}
			}
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
	// The client closed the session, or the connection dropped.
	close(done)
	_ = ch.Close()
	running.Wait()
}

// finish reports the exit status and closes the channel.
func finish(ch ssh.Channel, code int) {
	status := struct{ Status uint32 }{uint32(code)}
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(&status))
	_ = ch.CloseWrite()
	_ = ch.Close()
}

// ServeHTTP returns a StreamLocalHandler that serves HTTP/1.1 requests on
// each channel with h, for faking the Docker API behind the tunnel.
func ServeHTTP(h http.Handler) StreamLocalHandler {
	return func(_ string, ch io.ReadWriteCloser) {
		defer func() { _ = ch.Close() }()
		br := bufio.NewReader(ch)
		for {
			req, err := http.ReadRequest(br)
			if err != nil {
				return
			}
			rec := newResponseRecorder()
			h.ServeHTTP(rec, req)
			_ = req.Body.Close()
			resp := &http.Response{
				StatusCode:    rec.status,
				ProtoMajor:    1,
				ProtoMinor:    1,
				Header:        rec.header,
				Body:          io.NopCloser(bytes.NewReader(rec.body.Bytes())),
				ContentLength: int64(rec.body.Len()),
				Request:       req,
			}
			if err := resp.Write(ch); err != nil {
				return
			}
		}
	}
}

type responseRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newResponseRecorder() *responseRecorder {
	return &responseRecorder{header: http.Header{}, status: http.StatusOK}
}

func (r *responseRecorder) Header() http.Header         { return r.header }
func (r *responseRecorder) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *responseRecorder) WriteHeader(status int)      { r.status = status }
