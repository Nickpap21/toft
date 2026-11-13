package sshpool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/sshclient"
	dockerclient "github.com/docker/docker/client"
	"golang.org/x/crypto/ssh"
)

// RemoteHost runs work on one server over the pool's shared connection.
// Every call honours ctx: when it ends, a remote command is sent SIGTERM and
// its session closed. Errors from a connection that cannot be made carry the
// cause (errors.As finds a *sshclient.HostKeyMismatchError, and errors.Is
// matches ErrUnreachable during backoff).
type RemoteHost interface {
	// Exec runs cmd and returns its combined, trimmed stdout and stderr;
	// a non-zero exit status is an error.
	Exec(ctx context.Context, cmd string) (string, error)
	// ExecInput is Exec with stdin fed to the command (EOF after it).
	ExecInput(ctx context.Context, cmd string, stdin io.Reader) (string, error)
	// ExecStream runs cmd and calls logFn for each output line (stdout and
	// stderr merged); logFn is never called concurrently or after return.
	ExecStream(ctx context.Context, cmd string, logFn func(string)) error
	// Upload writes content to remotePath through stdin (`cat > remotePath`).
	// remotePath goes into the shell command verbatim: callers quote it.
	Upload(ctx context.Context, remotePath string, content []byte) error
	// NewPTY starts an interactive shell. It holds a session slot until
	// PTYSession.Close (or until ctx ends), which leaves the connection open.
	NewPTY(ctx context.Context, cols, rows int) (*sshclient.PTYSession, error)
	// DialUnix opens a Unix socket on the server. It rides the primary
	// connection without taking a session slot.
	DialUnix(ctx context.Context, path string) (net.Conn, error)
	// DockerClient returns a Docker SDK client tunnelled to the server's
	// Docker socket through DialUnix. Close it when done.
	DockerClient() (*dockerclient.Client, error)
	// Ensure makes sure a connection is up (dialling if needed) and
	// describes it. With Force it dials even during backoff.
	Ensure(ctx context.Context, opts EnsureOpts) (ConnInfo, error)
	// Ping measures the round trip of a keepalive on the connection.
	Ping(ctx context.Context) (time.Duration, error)
	// Subsystem runs one of the agent's typed requests with stdin and
	// returns its output (D-106). An SSH server refuses it.
	Subsystem(ctx context.Context, name string, stdin io.Reader) (string, error)
	// SubsystemStream is Subsystem with its output delivered line by line
	// (a rollout's events, D-111).
	SubsystemStream(ctx context.Context, name string, stdin io.Reader, lineFn func(string)) error
}

// EnsureOpts tunes Ensure.
type EnsureOpts struct {
	// Force dials even while the server is in backoff after failures. For
	// operator-initiated checks: test connection, provision, host-key scan
	// and reset.
	Force bool
}

// ConnInfo describes the connection Ensure found or made.
type ConnInfo struct {
	// HostKey is the key the server presented, in authorized_keys format.
	HostKey string
	// NewlyPinned is true when this call's dial pinned the key (trust on
	// first use).
	NewlyPinned bool
	// Fresh is true when the connection was dialled during this call.
	Fresh bool
}

// maxRefusalRetries bounds the retries after the server refuses sessions.
const maxRefusalRetries = 8

type handle struct {
	p   *Pool
	t   Target
	gen uint64 // Pool.gens for t's server when the handle was made
}

var _ RemoteHost = (*handle)(nil)

// withSession runs op on a session slot. A session the server refused for
// load is retried on another slot; a session that could not be opened
// because the connection broke is retried once on a new connection. Both
// are safe: the remote command never started. op returns handedOff when it
// passed the release func on (a PTY releases on Close).
func (h *handle) withSession(ctx context.Context, op func(c *sshclient.Client, release func()) (handedOff bool, err error)) error {
	refusals, redialled := 0, false
	for {
		l, err := h.p.acquire(ctx, h, acquireOpts{slot: true})
		if err != nil {
			return err
		}
		handedOff, err := op(l.c.client, l.release)
		if handedOff {
			return err
		}
		var openErr *sshclient.SessionOpenError
		if err != nil && ctx.Err() == nil && errors.As(err, &openErr) {
			var refused *ssh.OpenChannelError
			if errors.As(openErr, &refused) {
				if (refused.Reason == ssh.Prohibited || refused.Reason == ssh.ResourceShortage) &&
					refusals < maxRefusalRetries && l.refused() {
					refusals++
					continue
				}
			} else if !redialled {
				redialled = true
				l.broken()
				continue
			}
		}
		var exited *ssh.ExitError
		if err != nil && ctx.Err() == nil && !errors.As(err, &exited) && connectionEnded(l.c.client) {
			err = &ConnectionLostError{Err: err}
		}
		l.release()
		return err
	}
}

// ErrConnectionLost is matched (errors.Is) by the error of a command whose
// connection ended under it: the server or its agent went away mid-command.
// The command may or may not have finished; only callers whose work is safe
// to repeat retry it (a build, a pull).
var ErrConnectionLost = errors.New("sshpool: the connection was lost during the command")

// ConnectionLostError wraps the command's own error.
type ConnectionLostError struct{ Err error }

func (e *ConnectionLostError) Error() string {
	return "the connection to the server was lost: " + e.Err.Error()
}
func (e *ConnectionLostError) Unwrap() error        { return e.Err }
func (e *ConnectionLostError) Is(target error) bool { return target == ErrConnectionLost }

// connectionEnded reports whether c's connection is closed, allowing a
// moment for the close to land after the session's own error.
func connectionEnded(c *sshclient.Client) bool {
	select {
	case <-c.Done():
		return true
	case <-time.After(200 * time.Millisecond):
		return false
	}
}

func (h *handle) Subsystem(ctx context.Context, name string, stdin io.Reader) (string, error) {
	var out string
	err := h.withSession(ctx, func(c *sshclient.Client, _ func()) (bool, error) {
		var err error
		out, err = c.SubsystemContext(ctx, name, stdin)
		return false, err
	})
	return out, err
}

func (h *handle) SubsystemStream(ctx context.Context, name string, stdin io.Reader, lineFn func(string)) error {
	return h.withSession(ctx, func(c *sshclient.Client, _ func()) (bool, error) {
		return false, c.ExecStreamInputContext(ctx, name, stdin, lineFn)
	})
}

func (h *handle) Exec(ctx context.Context, cmd string) (string, error) {
	return h.ExecInput(ctx, cmd, nil)
}

func (h *handle) ExecInput(ctx context.Context, cmd string, stdin io.Reader) (string, error) {
	var out string
	err := h.withSession(ctx, func(c *sshclient.Client, _ func()) (bool, error) {
		var err error
		out, err = c.ExecInputContext(ctx, cmd, stdin)
		return false, err
	})
	return out, err
}

func (h *handle) ExecStream(ctx context.Context, cmd string, logFn func(string)) error {
	return h.withSession(ctx, func(c *sshclient.Client, _ func()) (bool, error) {
		return false, c.ExecStreamContext(ctx, cmd, logFn)
	})
}

func (h *handle) Upload(ctx context.Context, remotePath string, content []byte) error {
	return h.withSession(ctx, func(c *sshclient.Client, _ func()) (bool, error) {
		return false, c.UploadContext(ctx, remotePath, content)
	})
}

func (h *handle) NewPTY(ctx context.Context, cols, rows int) (*sshclient.PTYSession, error) {
	var pty *sshclient.PTYSession
	err := h.withSession(ctx, func(c *sshclient.Client, release func()) (bool, error) {
		p, err := c.NewPTYContext(ctx, cols, rows, release)
		if err != nil {
			return false, err
		}
		pty = p
		return true, nil
	})
	return pty, err
}

func (h *handle) DialUnix(ctx context.Context, path string) (net.Conn, error) {
	redialled := false
	for {
		l, err := h.p.acquire(ctx, h, acquireOpts{tunnel: true})
		if err != nil {
			return nil, err
		}
		nc, err := l.c.client.DialUnix(ctx, path)
		if err == nil {
			return &tunnelConn{Conn: nc, release: l.release}, nil
		}
		var refused *ssh.OpenChannelError
		if ctx.Err() == nil && !errors.As(err, &refused) && !redialled {
			// Not an answer from the server: the connection is broken.
			redialled = true
			l.broken()
			continue
		}
		l.release()
		return nil, fmt.Errorf("open %s on %s: %w", path, h.t.Name, err)
	}
}

// tunnelConn releases its pool lease when closed.
type tunnelConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *tunnelConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

// CloseWrite ends what is sent while the answer still comes back (a
// command's stdin, docker exec): the SSH channel's, which embedding
// net.Conn would hide.
func (c *tunnelConn) CloseWrite() error {
	cw, ok := c.Conn.(interface{ CloseWrite() error })
	if !ok {
		return errors.New("this connection can't close its write side")
	}
	return cw.CloseWrite()
}

func (h *handle) DockerClient() (*dockerclient.Client, error) {
	return sshclient.NewDockerClient(h.DialUnix)
}

func (h *handle) Ensure(ctx context.Context, opts EnsureOpts) (ConnInfo, error) {
	start := time.Now()
	l, err := h.p.acquire(ctx, h, acquireOpts{force: opts.Force})
	if err != nil {
		return ConnInfo{}, err
	}
	defer l.release()
	fresh := !l.c.createdAt.Before(start)
	return ConnInfo{
		HostKey:     l.c.hostKey,
		NewlyPinned: fresh && l.c.newlyPinned,
		Fresh:       fresh,
	}, nil
}

// Ping sends a keepalive and returns its round trip. A connection that does
// not answer within KeepaliveTimeout is closed; when it had been reused (it
// may have died silently since), Ping retries once on a new connection.
func (h *handle) Ping(ctx context.Context) (time.Duration, error) {
	for attempt := 0; ; attempt++ {
		start := time.Now()
		l, err := h.p.acquire(ctx, h, acquireOpts{})
		if err != nil {
			return 0, err
		}
		kctx, cancel := context.WithTimeout(ctx, h.p.opts.KeepaliveTimeout)
		rtt, err := l.c.client.KeepaliveContext(kctx)
		cancel()
		if err == nil {
			l.release()
			return rtt, nil
		}
		if ctx.Err() != nil {
			l.release()
			return 0, ctx.Err()
		}
		reused := l.c.createdAt.Before(start)
		l.broken()
		if reused && attempt == 0 {
			continue
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return 0, fmt.Errorf("SSH keepalive to %s: no reply within %s", h.t.Name, h.p.opts.KeepaliveTimeout)
		}
		return 0, fmt.Errorf("SSH keepalive to %s: %w", h.t.Name, err)
	}
}
