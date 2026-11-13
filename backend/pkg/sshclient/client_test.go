package sshclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/sshclient/sshtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func testConfig(srv *sshtest.Server) Config {
	return Config{Host: srv.Host, Port: srv.Port, User: "deploy", PrivateKeyPEM: srv.ClientKeyPEM}
}

func dialT(t *testing.T, cfg Config) *Client {
	t.Helper()
	c, err := Dial(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func otherKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	return sshtest.NewEd25519Signer(t).PublicKey()
}

func TestDialTOFUCapturesPresentedKey(t *testing.T) {
	srv := sshtest.New(t)
	c := dialT(t, testConfig(srv))

	require.NotNil(t, c.HostKey())
	assert.Equal(t, MarshalHostKey(srv.HostKey()), MarshalHostKey(c.HostKey()))

	out, err := c.ExecContext(context.Background(), "echo hello")
	require.NoError(t, err)
	assert.Equal(t, "hello", out)
}

func TestDialPinnedKeyMatches(t *testing.T) {
	srv := sshtest.New(t)
	cfg := testConfig(srv)
	cfg.PinnedHostKey = MarshalHostKey(srv.HostKey()) + " root@server\n" // a comment and newline are tolerated
	c := dialT(t, cfg)
	assert.True(t, sameKey(srv.HostKey(), c.HostKey()))
}

func TestDialPinnedMismatchFailsBeforeAuth(t *testing.T) {
	srv := sshtest.New(t)
	pinned := otherKey(t)
	cfg := testConfig(srv)
	cfg.PinnedHostKey = MarshalHostKey(pinned)

	_, err := Dial(context.Background(), cfg)
	require.Error(t, err)
	var mm *HostKeyMismatchError
	require.True(t, errors.As(err, &mm), "got %v", err)
	assert.True(t, errors.Is(err, ErrHostKeyMismatch))
	assert.Equal(t, ssh.FingerprintSHA256(pinned), mm.Expected)
	assert.Equal(t, ssh.FingerprintSHA256(srv.HostKey()), mm.Presented)
	assert.Equal(t, srv.Addr, mm.Addr)
	assert.Contains(t, err.Error(), mm.Expected)
	assert.Contains(t, err.Error(), mm.Presented)
	assert.Equal(t, 0, srv.AuthAttempts(), "credentials must never be offered to a server with the wrong key")
}

func TestPinnedKeyTypeRestrictsNegotiation(t *testing.T) {
	rsaKey := sshtest.NewRSASigner(t)
	edKey := sshtest.NewEd25519Signer(t)
	srv := sshtest.New(t, sshtest.WithHostKeys(rsaKey, edKey))

	// Without a pin, ed25519 is preferred.
	tofu := dialT(t, testConfig(srv))
	assert.Equal(t, ssh.KeyAlgoED25519, tofu.HostKey().Type())

	// Pinning either key works: negotiation is restricted to its type.
	for _, key := range []ssh.Signer{rsaKey, edKey} {
		cfg := testConfig(srv)
		cfg.PinnedHostKey = MarshalHostKey(key.PublicKey())
		c := dialT(t, cfg)
		assert.True(t, sameKey(key.PublicKey(), c.HostKey()), "pinned %s", key.PublicKey().Type())
	}

	// A server that no longer offers the pinned key's type is a mismatch.
	edOnly := sshtest.New(t)
	cfg := testConfig(edOnly)
	cfg.PinnedHostKey = MarshalHostKey(rsaKey.PublicKey())
	_, err := Dial(context.Background(), cfg)
	var mm *HostKeyMismatchError
	require.True(t, errors.As(err, &mm), "got %v", err)
	assert.Empty(t, mm.Presented)
	assert.Equal(t, ssh.KeyAlgoRSA, mm.ExpectedType)
	assert.Contains(t, err.Error(), "no longer offers")
	assert.Equal(t, 0, edOnly.AuthAttempts())
}

func TestHostKeyAlgorithms(t *testing.T) {
	assert.Equal(t, tofuHostKeyAlgorithms, hostKeyAlgorithmsFor(nil))
	for _, a := range hostKeyAlgorithmsFor(nil) {
		assert.NotContains(t, a, "cert", "trust on first use must not accept certificates")
	}
	assert.Equal(t, []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA},
		hostKeyAlgorithmsFor(sshtest.NewRSASigner(t).PublicKey()))
	assert.Equal(t, []string{ssh.KeyAlgoED25519}, hostKeyAlgorithmsFor(otherKey(t)))
}

func TestCertificateHostKeyIsUnwrapped(t *testing.T) {
	ca := sshtest.NewEd25519Signer(t)
	host := sshtest.NewEd25519Signer(t)
	cert := &ssh.Certificate{
		Key:             host.PublicKey(),
		CertType:        ssh.HostCert,
		ValidPrincipals: []string{"server"},
		ValidBefore:     ssh.CertTimeInfinity,
	}
	require.NoError(t, cert.SignCert(rand.Reader, ca))

	// Trust on first use records the certified key, not the certificate.
	tofu := &hostKeyCheck{addr: "server:22"}
	require.NoError(t, tofu.callback("server:22", nil, cert))
	assert.True(t, sameKey(host.PublicKey(), tofu.presented))
	assert.Equal(t, MarshalHostKey(host.PublicKey()), MarshalHostKey(cert))

	// A re-key of a trust-on-first-use connection must present the same key.
	assert.NoError(t, tofu.callback("server:22", nil, host.PublicKey()))
	assert.ErrorIs(t, tofu.callback("server:22", nil, ca.PublicKey()), ErrHostKeyMismatch)

	// A pin is compared with the certified key.
	pinned := &hostKeyCheck{addr: "server:22", pinned: host.PublicKey()}
	assert.NoError(t, pinned.callback("server:22", nil, cert))
	wrong := &hostKeyCheck{addr: "server:22", pinned: ca.PublicKey()}
	assert.ErrorIs(t, wrong.callback("server:22", nil, cert), ErrHostKeyMismatch)

	// A stored certificate line parses to its key.
	parsed, err := ParseHostKey(string(ssh.MarshalAuthorizedKey(cert)))
	require.NoError(t, err)
	assert.True(t, sameKey(host.PublicKey(), parsed))
}

// stallingListener accepts TCP connections and never says anything.
func stallingListener(t *testing.T) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	addr := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port
}

func TestHandshakeTimeout(t *testing.T) {
	host, port := stallingListener(t)
	key := sshtest.New(t).ClientKeyPEM

	start := time.Now()
	_, err := Dial(context.Background(), Config{Host: host, Port: port, User: "u", PrivateKeyPEM: key, HandshakeTimeout: 200 * time.Millisecond})
	require.Error(t, err)
	assert.Less(t, time.Since(start), 3*time.Second)
	assert.Contains(t, err.Error(), "timed out")

	// ctx ends the handshake too.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start = time.Now()
	_, err = Dial(ctx, Config{Host: host, Port: port, User: "u", PrivateKeyPEM: key})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 3*time.Second)

	// A scan is bounded the same way.
	_, err = ScanHostKey(context.Background(), ScanConfig{Host: host, Port: port, Timeout: 200 * time.Millisecond})
	require.Error(t, err)
}

func TestDialFriendlyErrors(t *testing.T) {
	srv := sshtest.New(t)

	// A key the server does not know.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKey(priv, "")
	require.NoError(t, err)
	cfg := testConfig(srv)
	cfg.PrivateKeyPEM = string(pem.EncodeToMemory(block))
	_, err = Dial(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SSH authentication failed")

	// A public key pasted instead of the private one.
	cfg.PrivateKeyPEM = MarshalHostKey(srv.ClientKey)
	_, err = Dial(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a public key was provided")

	// A closed port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	_, err = Dial(context.Background(), Config{Host: "127.0.0.1", Port: port, User: "u", PrivateKeyPEM: srv.ClientKeyPEM})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection refused")

	// An unparseable pin fails closed.
	cfg = testConfig(srv)
	cfg.PinnedHostKey = "ssh-ed25519 not-base64"
	_, err = Dial(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pinned SSH host key")
}

func TestScanHostKeyNeedsNoCredentials(t *testing.T) {
	rsaKey := sshtest.NewRSASigner(t)
	edKey := sshtest.NewEd25519Signer(t)
	srv := sshtest.New(t, sshtest.WithHostKeys(rsaKey, edKey))

	key, err := ScanHostKey(context.Background(), ScanConfig{Host: srv.Host, Port: srv.Port})
	require.NoError(t, err)
	assert.True(t, sameKey(edKey.PublicKey(), key))
	assert.Equal(t, 0, srv.AuthAttempts())

	// Prefer negotiates the pinned key's type first.
	key, err = ScanHostKey(context.Background(), ScanConfig{Host: srv.Host, Port: srv.Port, Prefer: MarshalHostKey(rsaKey.PublicKey())})
	require.NoError(t, err)
	assert.True(t, sameKey(rsaKey.PublicKey(), key))

	// ...but a server without that type still shows its key.
	edOnly := sshtest.New(t)
	key, err = ScanHostKey(context.Background(), ScanConfig{Host: edOnly.Host, Port: edOnly.Port, Prefer: MarshalHostKey(rsaKey.PublicKey())})
	require.NoError(t, err)
	assert.True(t, sameKey(edOnly.HostKey(), key))
	assert.Equal(t, 0, srv.AuthAttempts()+edOnly.AuthAttempts())
}

func TestExecContextCancelSignalsAndCloses(t *testing.T) {
	srv := sshtest.New(t)
	c := dialT(t, testConfig(srv))

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := c.ExecContext(ctx, "block")
		errc <- err
	}()
	require.Eventually(t, func() bool { return srv.Running() == 1 }, 2*time.Second, 5*time.Millisecond)

	start := time.Now()
	cancel()
	select {
	case err := <-errc:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("ExecContext did not return after cancel")
	}
	assert.Less(t, time.Since(start), 500*time.Millisecond)
	require.Eventually(t, func() bool { return len(srv.Signals()) == 1 }, 2*time.Second, 5*time.Millisecond, "the remote process is sent SIGTERM")
	assert.Equal(t, []string{"TERM"}, srv.Signals())
	require.Eventually(t, func() bool { return srv.Running() == 0 }, 2*time.Second, 5*time.Millisecond)

	// The connection is still usable.
	out, err := c.ExecContext(context.Background(), "echo still here")
	require.NoError(t, err)
	assert.Equal(t, "still here", out)

	// An already-cancelled ctx opens nothing.
	_, err = c.ExecContext(ctx, "echo never")
	assert.ErrorIs(t, err, context.Canceled)
	assert.NotContains(t, srv.Commands(), "echo never")
}

func TestExecContextErrors(t *testing.T) {
	srv := sshtest.New(t)
	c := dialT(t, testConfig(srv))

	out, err := c.ExecContext(context.Background(), "nope")
	require.Error(t, err)
	assert.Contains(t, out, "unknown command")
	assert.Contains(t, err.Error(), `exec "nope"`)
	var exit *ssh.ExitError
	require.True(t, errors.As(err, &exit))
	assert.Equal(t, 127, exit.ExitStatus())

	// Stdout and stderr are combined.
	out, err = c.ExecContext(context.Background(), "lines 2")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"line 1", "line 2"}, strings.Split(out, "\n"))
}

// A reply past MaxReplyBytes fails the call with ErrReplyTooLarge and ends
// the session, instead of being buffered whole; a reply under it is unchanged.
func TestExecContextCapsTheReply(t *testing.T) {
	var sent atomic.Int64
	srv := sshtest.New(t, sshtest.WithExec(func(s *sshtest.Session) int {
		switch s.Command {
		case "under":
			_, _ = io.WriteString(s.Stdout, strings.Repeat("x", MaxReplyBytes-10))
			return 0
		case "small":
			_, _ = io.WriteString(s.Stdout, "still here")
			return 0
		}
		chunk := []byte(strings.Repeat("x", 32<<10))
		for {
			select {
			case <-s.Done:
				return 129
			default:
			}
			n, err := s.Stdout.Write(chunk)
			if sent.Add(int64(n)) > 512<<20 || err != nil {
				return 141
			}
		}
	}))
	c := dialT(t, testConfig(srv))

	out, err := c.ExecContext(context.Background(), "under")
	require.NoError(t, err)
	assert.Len(t, out, MaxReplyBytes-10)

	start := time.Now()
	out, err = c.ExecContext(context.Background(), "flood")
	assert.ErrorIs(t, err, ErrReplyTooLarge)
	assert.Empty(t, out, "the partial reply is not returned")
	assert.Less(t, time.Since(start), 5*time.Second)
	assert.Less(t, sent.Load(), int64(64<<20), "the server was cut off, not drained")
	require.Eventually(t, func() bool { return srv.Running() == 0 }, 2*time.Second, 5*time.Millisecond)

	// The connection is still usable.
	out, err = c.ExecContext(context.Background(), "small")
	require.NoError(t, err)
	assert.Equal(t, "still here", out)
}

func TestSessionOpenErrorWhenRefused(t *testing.T) {
	srv := sshtest.New(t, sshtest.WithMaxSessions(1))
	c := dialT(t, testConfig(srv))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _, _ = c.ExecContext(ctx, "block") }()
	require.Eventually(t, func() bool { return srv.Running() == 1 }, 2*time.Second, 5*time.Millisecond)

	_, err := c.ExecContext(context.Background(), "echo hi")
	var openErr *SessionOpenError
	require.True(t, errors.As(err, &openErr), "got %v", err)
	var oce *ssh.OpenChannelError
	require.True(t, errors.As(err, &oce))
	assert.Equal(t, ssh.Prohibited, oce.Reason)
	assert.NotContains(t, srv.Commands(), "echo hi")
}

func TestExecStreamContextLinesAndExitStatus(t *testing.T) {
	srv := sshtest.New(t)
	c := dialT(t, testConfig(srv))

	var mu sync.Mutex
	var lines []string
	var inFlight, overlap atomic.Int32
	logFn := func(l string) {
		if inFlight.Add(1) > 1 {
			overlap.Add(1)
		}
		defer inFlight.Add(-1)
		mu.Lock()
		lines = append(lines, l)
		mu.Unlock()
	}
	require.NoError(t, c.ExecStreamContext(context.Background(), "lines 6", logFn))
	sort.Strings(lines)
	assert.Equal(t, []string{"line 1", "line 2", "line 3", "line 4", "line 5", "line 6"}, lines)
	assert.Zero(t, overlap.Load(), "logFn must not be called concurrently")

	err := c.ExecStreamContext(context.Background(), "exit 3", func(string) {})
	var exit *ssh.ExitError
	require.True(t, errors.As(err, &exit), "got %v", err)
	assert.Equal(t, 3, exit.ExitStatus())
}

// A line over the cap is cut with a marker and the output goes on (logs-9).
func TestExecStreamContextKeepsGoingPastALongLine(t *testing.T) {
	srv := sshtest.New(t, sshtest.WithExec(func(s *sshtest.Session) int {
		_, _ = io.WriteString(s.Stdout, strings.Repeat("x", 1<<20+10)+"\nafter\n")
		return 0
	}))
	c := dialT(t, testConfig(srv))

	var lines []string
	require.NoError(t, c.ExecStreamContext(context.Background(), "long", func(l string) { lines = append(lines, l) }))

	require.Len(t, lines, 2)
	assert.True(t, strings.HasSuffix(lines[0], "… (line cut at 1 MiB)"), "the cut line says so")
	assert.Equal(t, "after", lines[1])
}

func TestExecStreamContextCancel(t *testing.T) {
	ready := make(chan struct{})
	srv := sshtest.New(t, sshtest.WithExec(func(s *sshtest.Session) int {
		_, _ = io.WriteString(s.Stdout, "first\n")
		close(ready)
		// Keep writing until the session ends, like `docker logs --follow`.
		for {
			select {
			case <-s.Done:
				return 129
			case <-s.Signals:
				return 143
			case <-time.After(time.Millisecond):
				if _, err := io.WriteString(s.Stdout, "more\n"); err != nil {
					return 141
				}
			}
		}
	}))
	c := dialT(t, testConfig(srv))

	ctx, cancel := context.WithCancel(context.Background())
	var returned atomic.Bool
	var lateCalls atomic.Int32
	errc := make(chan error, 1)
	go func() {
		errc <- c.ExecStreamContext(ctx, "docker logs --follow app", func(string) {
			if returned.Load() {
				lateCalls.Add(1)
			}
		})
	}()
	<-ready
	cancel()
	select {
	case err := <-errc:
		returned.Store(true)
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("ExecStreamContext did not return after cancel")
	}
	require.Eventually(t, func() bool { return len(srv.Signals()) == 1 }, 2*time.Second, 5*time.Millisecond, "the remote process is sent SIGTERM")
	assert.Equal(t, []string{"TERM"}, srv.Signals())
	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, lateCalls.Load(), "logFn was called after ExecStreamContext returned")
}

func TestUploadContextUsesStdin(t *testing.T) {
	srv := sshtest.New(t)
	c := dialT(t, testConfig(srv))

	content := []byte("SECRET=1\nOTHER='x y'\n")
	require.NoError(t, c.UploadContext(context.Background(), "'/tmp/paas-x/.env'", content))
	got, ok := srv.File("/tmp/paas-x/.env")
	require.True(t, ok)
	assert.Equal(t, content, got)
	assert.Equal(t, []string{"cat > '/tmp/paas-x/.env'"}, srv.Commands(), "content must not be on the command line")

	out, err := c.ExecInputContext(context.Background(), "cat > '/tmp/y'", strings.NewReader("abc"))
	require.NoError(t, err)
	assert.Empty(t, out)
	got, _ = srv.File("/tmp/y")
	assert.Equal(t, "abc", string(got))
}

func TestKeepalive(t *testing.T) {
	srv := sshtest.New(t)
	c := dialT(t, testConfig(srv))

	rtt, err := c.Keepalive(time.Second)
	require.NoError(t, err)
	assert.Greater(t, rtt, time.Duration(0))

	srv.SetKeepaliveReplies(false)
	_, err = c.Keepalive(100 * time.Millisecond)
	assert.ErrorIs(t, err, ErrKeepaliveTimeout)
}

func TestDoneClosesWhenConnectionEnds(t *testing.T) {
	srv := sshtest.New(t)
	c := dialT(t, testConfig(srv))
	select {
	case <-c.Done():
		t.Fatal("Done closed early")
	default:
	}
	srv.DropAll()
	select {
	case <-c.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done did not close after the server dropped the connection")
	}
}

func TestPTYCloseLeavesConnectionUsable(t *testing.T) {
	srv := sshtest.New(t)
	c := dialT(t, testConfig(srv))

	var closed atomic.Int32
	pty, err := c.NewPTYContext(context.Background(), 80, 24, func() { closed.Add(1) })
	require.NoError(t, err)
	_, err = pty.Write([]byte("hi"))
	require.NoError(t, err)
	buf := make([]byte, 2)
	_, err = io.ReadFull(pty, buf)
	require.NoError(t, err)
	assert.Equal(t, "hi", string(buf))
	require.NoError(t, pty.Resize(120, 40))

	pty.Close()
	pty.Close()
	assert.Equal(t, int32(1), closed.Load(), "onClose runs exactly once")

	out, err := c.ExecContext(context.Background(), "echo after pty")
	require.NoError(t, err)
	assert.Equal(t, "after pty", out)
	assert.Equal(t, 1, srv.Accepts())
}

func TestPTYClosedWhenContextEnds(t *testing.T) {
	srv := sshtest.New(t)
	c := dialT(t, testConfig(srv))

	ctx, cancel := context.WithCancel(context.Background())
	closed := make(chan struct{})
	pty, err := c.NewPTYContext(ctx, 80, 24, func() { close(closed) })
	require.NoError(t, err)
	cancel()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("the PTY was not closed when its ctx ended")
	}
	_, err = io.ReadAll(pty)
	assert.NoError(t, err) // EOF once closed
	require.Eventually(t, func() bool { return srv.Running() == 0 }, 2*time.Second, 5*time.Millisecond)
}

func TestDialUnixAndDockerClient(t *testing.T) {
	var paths []string
	var mu sync.Mutex
	api := sshtest.ServeHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Api-Version", "1.47")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "OK")
	}))
	srv := sshtest.New(t, sshtest.WithStreamLocal(func(path string, ch io.ReadWriteCloser) {
		mu.Lock()
		paths = append(paths, path)
		mu.Unlock()
		api(path, ch)
	}))
	c := dialT(t, testConfig(srv))

	dc, err := c.DockerClient()
	require.NoError(t, err)
	defer func() { _ = dc.Close() }()
	ping, err := dc.Ping(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "1.47", ping.APIVersion)

	mu.Lock()
	assert.Contains(t, paths, DockerSocketPath)
	mu.Unlock()
	assert.Zero(t, srv.PeakSessions(), "the Docker tunnel is not an sshd session")
}

func TestHostKeyMarshalParseFingerprint(t *testing.T) {
	for _, signer := range []ssh.Signer{sshtest.NewEd25519Signer(t), sshtest.NewRSASigner(t)} {
		key := signer.PublicKey()
		s := MarshalHostKey(key)
		assert.True(t, strings.HasPrefix(s, key.Type()+" "))
		assert.NotContains(t, s, "\n")

		parsed, err := ParseHostKey(s + " comment@host\n")
		require.NoError(t, err)
		assert.True(t, sameKey(key, parsed))
		assert.Equal(t, s, MarshalHostKey(parsed))

		fp, typ, err := Fingerprint(s)
		require.NoError(t, err)
		assert.Equal(t, ssh.FingerprintSHA256(key), fp)
		assert.True(t, strings.HasPrefix(fp, "SHA256:"))
		assert.Equal(t, key.Type(), typ)
	}

	_, err := ParseHostKey("")
	assert.Error(t, err)
	_, _, err = Fingerprint("garbage")
	assert.Error(t, err)
	assert.Equal(t, "", MarshalHostKey(nil))
}
