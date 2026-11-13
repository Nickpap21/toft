// Package sshclient is the SSH client the control plane uses to reach managed
// servers: host-key pinning (trust on first use), a bounded handshake,
// context-aware remote commands, PTYs and Docker socket tunnelling.
//
// Callers normally get connections from pkg/sshpool, which keeps one pooled
// connection per server; Dial is the building block underneath it.
package sshclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/lines"
	dockerclient "github.com/docker/docker/client"
	"golang.org/x/crypto/ssh"
)

const (
	// DefaultHandshakeTimeout bounds the TCP connect plus the SSH handshake
	// and authentication when Config.HandshakeTimeout is zero.
	DefaultHandshakeTimeout = 10 * time.Second

	// DockerSocketPath is the Docker daemon's socket on a managed server.
	DockerSocketPath = "/var/run/docker.sock"

	// tcpKeepAlive is the TCP keepalive period of every connection.
	tcpKeepAlive = 30 * time.Second

	// closeGrace is how long a cancelled call waits for the server to
	// acknowledge the closed session before returning anyway.
	closeGrace = time.Second

	keepaliveRequest = "keepalive@openssh.com"

	// MaxReplyBytes caps what one Exec, Subsystem or Upload call collects
	// from the remote command (stdout and stderr together). It is an initial
	// value, not a measured one: far above the largest reply the panel
	// expects (the agent's desired state, a status report), far below what
	// would endanger the panel's memory.
	MaxReplyBytes = 4 << 20
)

// ErrKeepaliveTimeout is returned by Keepalive when no reply arrives in time.
var ErrKeepaliveTimeout = errors.New("ssh: no reply to keepalive")

// ErrReplyTooLarge is returned (match with errors.Is) by a call whose remote
// command wrote more than MaxReplyBytes. The session is closed and the
// output dropped.
var ErrReplyTooLarge = errors.New("ssh: the server's reply was too large")

// Config describes one SSH connection.
type Config struct {
	Host string
	Port int
	User string
	// PrivateKeyPEM is the client key (OpenSSH or PEM format, without a
	// passphrase).
	PrivateKeyPEM string
	// PinnedHostKey is the server's host key in authorized_keys format (see
	// MarshalHostKey). With a pin, any other key fails the handshake with a
	// *HostKeyMismatchError before authentication. Empty means trust on
	// first use: the presented key is accepted and returned by
	// Client.HostKey, and the caller decides whether to pin it.
	PinnedHostKey string
	// HandshakeTimeout bounds the TCP connect plus the SSH handshake and
	// authentication. Zero means DefaultHandshakeTimeout.
	HandshakeTimeout time.Duration
	// Conn, when set, supplies the transport instead of a TCP connection to
	// Host:Port: the WebSocket a server's agent opened to the panel (D-106).
	// The handshake and the host-key pin are the same either way.
	Conn func(ctx context.Context) (net.Conn, error)
}

// Client wraps an SSH connection and provides helpers for remote execution
// and Docker socket tunnelling.
type Client struct {
	conn    *ssh.Client
	hostKey ssh.PublicKey
	done    chan struct{}
}

// SessionOpenError reports that a session could not be opened, so the remote
// command never started and retrying cannot run it twice. It wraps the cause:
// an *ssh.OpenChannelError when the server refused the session (for example
// past sshd's MaxSessions), otherwise a connection-level failure.
type SessionOpenError struct{ Err error }

func (e *SessionOpenError) Error() string { return "new SSH session: " + e.Err.Error() }
func (e *SessionOpenError) Unwrap() error { return e.Err }

// Dial connects and authenticates to cfg.Host:cfg.Port. ctx bounds the
// connect and handshake only; once Dial returns, the connection lives until
// Close.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	signer, err := parsePrivateKey(cfg.PrivateKeyPEM)
	if err != nil {
		return nil, err
	}
	timeout := cfg.HandshakeTimeout
	if timeout <= 0 {
		timeout = DefaultHandshakeTimeout
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	if cfg.Conn != nil {
		addr = "the agent on " + cfg.Host
	}

	var pinned ssh.PublicKey
	if strings.TrimSpace(cfg.PinnedHostKey) != "" {
		pinned, err = ParseHostKey(cfg.PinnedHostKey)
		if err != nil {
			return nil, fmt.Errorf("the pinned SSH host key for %s is invalid: %w", addr, err)
		}
	}
	check := &hostKeyCheck{addr: addr, pinned: pinned}
	sshCfg := &ssh.ClientConfig{
		User:              cfg.User,
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback:   check.callback,
		HostKeyAlgorithms: hostKeyAlgorithmsFor(pinned),
	}

	h, err := handshake(ctx, addr, cfg.Conn, sshCfg, timeout)
	if err != nil {
		if hkErr := check.explain(err); hkErr != nil {
			return nil, hkErr
		}
		if strings.Contains(err.Error(), "unable to authenticate") {
			return nil, fmt.Errorf(
				"SSH authentication failed for user %q on %s — make sure the public key matching this private key is in ~/.ssh/authorized_keys on the server",
				cfg.User, addr,
			)
		}
		return nil, err
	}

	c := &Client{
		conn:    ssh.NewClient(h.conn, h.chans, h.reqs),
		hostKey: check.presented,
		done:    make(chan struct{}),
	}
	go func() {
		_ = c.conn.Wait()
		close(c.done)
	}()
	return c, nil
}

// parsePrivateKey normalises and parses a pasted private key, turning the
// common mistakes into readable errors.
func parsePrivateKey(privateKeyPEM string) (ssh.Signer, error) {
	// Keys pasted from the clipboard often carry a trailing newline or CRLF endings.
	privateKeyPEM = strings.TrimSpace(privateKeyPEM)
	privateKeyPEM = strings.ReplaceAll(privateKeyPEM, "\r\n", "\n")

	// Catch the common mistake of pasting the public key instead of the private key.
	if strings.HasPrefix(privateKeyPEM, "ssh-") || strings.HasPrefix(privateKeyPEM, "ecdsa-") {
		return nil, fmt.Errorf("a public key was provided — please paste the private key (the file that does NOT end in .pub)")
	}

	signer, err := ssh.ParsePrivateKey([]byte(privateKeyPEM))
	if err != nil {
		// Passphrase-protected keys are not supported.
		if strings.Contains(err.Error(), "passphrase") || strings.Contains(err.Error(), "encrypted") {
			return nil, fmt.Errorf("the private key is passphrase-protected; please provide a key without a passphrase")
		}
		return nil, fmt.Errorf("could not parse private key (check that you copied the full key including the header/footer lines): %w", err)
	}
	return signer, nil
}

// handshaked is an SSH connection whose handshake completed.
type handshaked struct {
	conn  ssh.Conn
	chans <-chan ssh.NewChannel
	reqs  <-chan *ssh.Request
}

// handshake dials addr and runs the SSH handshake, bounded by timeout and by
// ctx. A peer that accepts TCP and then stalls fails after timeout instead
// of hanging the caller.
func handshake(ctx context.Context, addr string, dial func(context.Context) (net.Conn, error), cfg *ssh.ClientConfig, timeout time.Duration) (*handshaked, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("SSH dial %s: %w", addr, err)
	}
	var nc net.Conn
	var err error
	if dial != nil {
		dctx, cancel := context.WithTimeout(ctx, timeout)
		nc, err = dial(dctx)
		cancel()
		if err != nil {
			return nil, err
		}
	} else {
		d := net.Dialer{Timeout: timeout, KeepAlive: tcpKeepAlive}
		nc, err = d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, friendlyConnectError(ctx, addr, err)
		}
	}

	deadline := time.Now().Add(timeout)
	ctxDeadline := false
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline, ctxDeadline = dl, true
	}
	_ = nc.SetDeadline(deadline)
	// Cancelling ctx expires the deadline, which aborts the handshake.
	stop := context.AfterFunc(ctx, func() { _ = nc.SetDeadline(time.Unix(1, 0)) })

	conn, chans, reqs, err := ssh.NewClientConn(nc, addr, cfg)
	stopped := stop()
	if err != nil { // NewClientConn closed nc
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("SSH handshake with %s: %w", addr, ctxErr)
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			if ctxDeadline { // ctx's deadline, reached a moment before ctx noticed
				return nil, fmt.Errorf("SSH handshake with %s: %w", addr, context.DeadlineExceeded)
			}
			return nil, fmt.Errorf("SSH handshake with %s timed out after %s — the port accepts connections but no SSH server answered in time", addr, timeout)
		}
		return nil, fmt.Errorf("SSH dial %s: %w", addr, err)
	}
	if !stopped {
		// ctx ended as the handshake finished; the deadline may already be
		// in the past, so the connection is unusable.
		_ = conn.Close()
		return nil, fmt.Errorf("SSH handshake with %s: %w", addr, ctx.Err())
	}
	_ = nc.SetDeadline(time.Time{})
	return &handshaked{conn: conn, chans: chans, reqs: reqs}, nil
}

func friendlyConnectError(ctx context.Context, addr string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("SSH dial %s: %w", addr, ctxErr)
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"):
		return fmt.Errorf("connection refused on %s — check the IP address and that SSH is running on that port", addr)
	case strings.Contains(msg, "no route to host"), strings.Contains(msg, "timeout"), strings.Contains(msg, "timed out"):
		return fmt.Errorf("could not reach %s — check the IP address and firewall rules", addr)
	}
	return fmt.Errorf("SSH dial %s: %w", addr, err)
}

// HostKey returns the host key the server presented (a plain key, never a
// certificate). With a pin it equals the pin.
func (c *Client) HostKey() ssh.PublicKey { return c.hostKey }

// Done is closed when the connection has ended, for whatever reason.
func (c *Client) Done() <-chan struct{} { return c.done }

// Close terminates the SSH connection and every session on it.
func (c *Client) Close() error {
	return c.conn.Close()
}

// KeepaliveContext sends a keepalive@openssh.com request and waits for any
// reply (OpenSSH answers with a failure, which still proves the connection
// is alive). It returns the round-trip time, or ctx's error if no reply
// arrives before ctx ends. The caller decides whether a missing reply means
// the connection is dead.
func (c *Client) KeepaliveContext(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	errc := make(chan error, 1)
	go func() {
		_, _, err := c.conn.SendRequest(keepaliveRequest, true, nil)
		errc <- err
	}()
	select {
	case err := <-errc:
		if err != nil {
			return 0, fmt.Errorf("SSH keepalive: %w", err)
		}
		return time.Since(start), nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// Keepalive is KeepaliveContext with a timeout; a missing reply returns an
// error wrapping ErrKeepaliveTimeout.
func (c *Client) Keepalive(timeout time.Duration) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	rtt, err := c.KeepaliveContext(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		return 0, fmt.Errorf("%w within %s", ErrKeepaliveTimeout, timeout)
	}
	return rtt, err
}

// newSession opens a session channel, giving up when ctx ends. A failure
// to open is a *SessionOpenError.
func (c *Client) newSession(ctx context.Context) (*ssh.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ctx.Done() == nil {
		s, err := c.conn.NewSession()
		if err != nil {
			return nil, &SessionOpenError{Err: err}
		}
		return s, nil
	}
	type result struct {
		s   *ssh.Session
		err error
	}
	ch := make(chan result, 1)
	go func() {
		s, err := c.conn.NewSession()
		ch <- result{s, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, &SessionOpenError{Err: r.err}
		}
		return r.s, nil
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.s != nil {
				_ = r.s.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

// wait waits for a started session to finish. When ctx ends first, the
// remote process is sent SIGTERM (honoured by OpenSSH 7.9 and newer) and the
// session is closed, which also ends the process at its next write on older
// servers; wait then returns ctx's error.
func wait(ctx context.Context, sess *ssh.Session) error {
	if ctx.Done() == nil {
		return sess.Wait()
	}
	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGTERM)
		_ = sess.Close()
		select {
		case <-done:
		case <-time.After(closeGrace):
		}
		return ctx.Err()
	}
}

// lockedBuffer is a bytes.Buffer safe for concurrent stdout/stderr writes. It
// holds at most MaxReplyBytes: the write that would pass it is refused and
// calls onOver once, which run uses to close the session.
type lockedBuffer struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	over   bool
	onOver func()
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	first := !b.over && b.buf.Len()+len(p) > MaxReplyBytes
	if first {
		b.over = true
	}
	if b.over {
		b.mu.Unlock()
		if first {
			b.onOver()
		}
		return 0, ErrReplyTooLarge
	}
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) tooLarge() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.over
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// run executes cmd with stdin (nil for none) and returns the combined,
// trimmed stdout and stderr. A reply over MaxReplyBytes closes the session
// and fails with ErrReplyTooLarge.
func (c *Client) run(ctx context.Context, cmd string, stdin io.Reader) (string, error) {
	sess, err := c.newSession(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = sess.Close() }()

	out := lockedBuffer{onOver: func() { _ = sess.Close() }}
	sess.Stdout = &out
	sess.Stderr = &out
	if stdin != nil {
		sess.Stdin = stdin
	}
	if err := sess.Start(cmd); err != nil {
		return "", fmt.Errorf("start: %w", err)
	}
	err = wait(ctx, sess)
	if out.tooLarge() {
		return "", fmt.Errorf("%w: over %d bytes", ErrReplyTooLarge, MaxReplyBytes)
	}
	return strings.TrimSpace(out.String()), err
}

// ExecContext runs cmd on the remote host and returns combined stdout+stderr.
// A non-zero exit code is wrapped as an error. When ctx ends first, the
// remote process is signalled and its session closed, and ctx's error is
// returned with the output so far.
func (c *Client) ExecContext(ctx context.Context, cmd string) (string, error) {
	return c.ExecInputContext(ctx, cmd, nil)
}

// ExecInputContext is ExecContext with stdin fed to the remote command; the
// remote side sees EOF once stdin is exhausted.
func (c *Client) ExecInputContext(ctx context.Context, cmd string, stdin io.Reader) (string, error) {
	text, err := c.run(ctx, cmd, stdin)
	if err != nil {
		var openErr *SessionOpenError
		if ctx.Err() != nil || errors.As(err, &openErr) {
			return text, err
		}
		return text, fmt.Errorf("exec %q: %w\noutput: %s", cmd, err, text)
	}
	return text, nil
}

// SubsystemContext runs one of the agent's typed requests (D-106) with stdin
// and returns its output; a non-zero exit is an error carrying it. It is
// sent as an exec of the request's reserved name, which the agent serves
// itself: x/crypto's RequestSubsystem wires neither stdin nor stdout.
func (c *Client) SubsystemContext(ctx context.Context, name string, stdin io.Reader) (string, error) {
	text, err := c.run(ctx, name, stdin)
	if err != nil && ctx.Err() == nil {
		return text, fmt.Errorf("%s: %w: %s", name, err, text)
	}
	return text, err
}

// ExecStreamContext runs cmd on the remote host and calls logFn for each line
// of output (stdout and stderr merged). logFn is never called concurrently,
// and never after ExecStreamContext returns. It returns the exit error, if
// any (an *ssh.ExitError for a non-zero status). When ctx ends first, the
// remote process is signalled and its session closed, and ctx's error is
// returned.
func (c *Client) ExecStreamContext(ctx context.Context, cmd string, logFn func(string)) error {
	return c.ExecStreamInputContext(ctx, cmd, nil, logFn)
}

// ExecStreamInputContext is ExecStreamContext with stdin (nil: none). An
// agent's typed request is run this way by its exact name.
func (c *Client) ExecStreamInputContext(ctx context.Context, cmd string, stdin io.Reader, logFn func(string)) error {
	sess, err := c.newSession(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = sess.Close() }()
	sess.Stdin = stdin

	stdout, err := sess.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	if err := sess.Start(cmd); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	var mu sync.Mutex
	stopped := false
	emit := func(line string) {
		mu.Lock()
		defer mu.Unlock()
		if !stopped {
			logFn(line)
		}
	}
	var scanners sync.WaitGroup
	scanners.Add(2)
	go scanLines(stdout, emit, &scanners)
	go scanLines(stderr, emit, &scanners)

	finished := make(chan error, 1)
	go func() {
		scanners.Wait()
		finished <- sess.Wait()
	}()

	if ctx.Done() == nil {
		return <-finished
	}
	select {
	case err := <-finished:
		return err
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGTERM)
		_ = sess.Close()
		select {
		case <-finished:
		case <-time.After(closeGrace):
		}
		mu.Lock()
		stopped = true
		mu.Unlock()
		return ctx.Err()
	}
}

// scanLines delivers r line by line, until r ends.
func scanLines(r io.Reader, emit func(string), wg *sync.WaitGroup) {
	defer wg.Done()
	_ = lines.Read(r, emit)
}

// UploadContext writes content to remotePath on the remote host through the
// session's stdin (`cat > remotePath`), so the content never appears on a
// command line and has no size limit. remotePath is placed in the shell
// command verbatim: the caller quotes it. The remote file is created or
// overwritten.
func (c *Client) UploadContext(ctx context.Context, remotePath string, content []byte) error {
	out, err := c.run(ctx, "cat > "+remotePath, bytes.NewReader(content))
	if err != nil {
		var openErr *SessionOpenError
		if ctx.Err() != nil || errors.As(err, &openErr) {
			return err
		}
		if out != "" {
			return fmt.Errorf("upload to %s: %w\noutput: %s", remotePath, err, out)
		}
		return fmt.Errorf("upload to %s: %w", remotePath, err)
	}
	return nil
}

// PTYSession wraps an SSH session configured with a pseudo-terminal.
// It implements io.ReadWriter: Write sends bytes to the remote stdin, Read
// receives terminal output. Call Resize when the terminal viewport changes,
// and Close when done: it ends this session only, not the connection.
type PTYSession struct {
	sess   *ssh.Session
	stdin  io.WriteCloser
	stdout io.Reader

	mu      sync.Mutex
	closed  bool
	onClose func()
	stopCtx func() bool
}

// NewPTYContext opens a session, allocates a PTY with the given dimensions
// and starts a shell. ctx bounds the setup and the session's lifetime: when
// ctx ends the session is closed. onClose, if not nil, runs once when the
// session is closed (by Close or by ctx); it does not run when
// NewPTYContext returns an error.
func (c *Client) NewPTYContext(ctx context.Context, cols, rows int, onClose func()) (*PTYSession, error) {
	sess, err := c.newSession(ctx)
	if err != nil {
		return nil, err
	}
	p := &PTYSession{sess: sess}
	p.mu.Lock()
	p.stopCtx = context.AfterFunc(ctx, p.Close)
	p.mu.Unlock()

	fail := func(err error) (*PTYSession, error) {
		p.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := sess.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		return fail(fmt.Errorf("request PTY: %w", err))
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		return fail(fmt.Errorf("stdin pipe: %w", err))
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return fail(fmt.Errorf("stdout pipe: %w", err))
	}
	p.mu.Lock()
	p.stdin, p.stdout = stdin, stdout
	p.mu.Unlock()
	if err := sess.Shell(); err != nil {
		return fail(fmt.Errorf("start shell: %w", err))
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed { // ctx ended during the setup
		return nil, ctx.Err()
	}
	p.onClose = onClose
	return p, nil
}

// Read reads terminal output from the remote shell.
func (p *PTYSession) Read(b []byte) (int, error) { return p.stdout.Read(b) }

// Write sends bytes to the remote shell's stdin.
func (p *PTYSession) Write(b []byte) (int, error) { return p.stdin.Write(b) }

// Resize notifies the remote PTY of a terminal dimension change.
func (p *PTYSession) Resize(cols, rows int) error { return p.sess.WindowChange(rows, cols) }

// Close terminates the PTY session. It leaves the SSH connection open, and
// is safe to call more than once.
func (p *PTYSession) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	stdin, stop, onClose := p.stdin, p.stopCtx, p.onClose
	p.mu.Unlock()

	if stop != nil {
		stop()
	}
	if stdin != nil {
		_ = stdin.Close()
	}
	_ = p.sess.Close()
	if onClose != nil {
		onClose()
	}
}

// DialUnix opens a connection to the Unix socket at path on the remote host
// (a direct-streamlocal channel, which is not an sshd session). ctx bounds
// the open only.
func (c *Client) DialUnix(ctx context.Context, path string) (net.Conn, error) {
	return c.conn.DialContext(ctx, "unix", path)
}

// DockerClient returns a Docker SDK client that communicates with the remote
// Docker daemon by tunnelling each connection through the SSH link to
// /var/run/docker.sock. The caller must close the returned client when done.
func (c *Client) DockerClient() (*dockerclient.Client, error) {
	return NewDockerClient(c.DialUnix)
}

// NewDockerClient returns a Docker SDK client whose every connection is made
// by dialUnix to DockerSocketPath on the remote host.
func NewDockerClient(dialUnix func(ctx context.Context, path string) (net.Conn, error)) (*dockerclient.Client, error) {
	dialer := func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialUnix(ctx, DockerSocketPath)
	}
	dc, err := dockerclient.NewClientWithOpts(
		dockerclient.WithHost("unix://"+DockerSocketPath),
		dockerclient.WithDialContext(dialer),
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("Docker-over-SSH client: %w", err)
	}
	return dc, nil
}
