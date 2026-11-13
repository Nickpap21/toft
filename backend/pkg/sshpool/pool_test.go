package sshpool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/sshclient"
	"github.com/Nickpap21/toft/backend/pkg/sshclient/sshtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

const serverID = "srv-1"

func newPool(t *testing.T, o Options) *Pool {
	t.Helper()
	p := New(o)
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func targetFor(srv *sshtest.Server) Target {
	return Target{ServerID: serverID, Name: "web-1", Host: srv.Host, Port: srv.Port, User: "deploy", PrivateKeyPEM: srv.ClientKeyPEM}
}

// holder is an exec handler where `hold` blocks until release() and then
// succeeds; anything else goes to the server's DefaultExec.
type holder struct {
	srv  *sshtest.Server
	gate chan struct{}
	once sync.Once
}

func newHoldServer(t *testing.T, opts ...sshtest.Option) (*sshtest.Server, *holder) {
	h := &holder{gate: make(chan struct{})}
	opts = append(opts, sshtest.WithExec(func(s *sshtest.Session) int {
		if s.Command == "hold" {
			select {
			case <-h.gate:
				_, _ = io.WriteString(s.Stdout, "held\n")
				return 0
			case <-s.Done:
				return 129
			case <-s.Signals:
				return 143
			}
		}
		return h.srv.DefaultExec(s)
	}))
	h.srv = sshtest.New(t, opts...)
	t.Cleanup(h.release)
	return h.srv, h
}

func (h *holder) release() { h.once.Do(func() { close(h.gate) }) }

// background runs fn in a goroutine and returns a channel with its error.
func background(fn func() error) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- fn() }()
	return ch
}

func waitErr(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("call did not return")
		return nil
	}
}

func eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	require.Eventually(t, cond, 5*time.Second, 5*time.Millisecond, msg)
}

func TestConcurrentExecsShareOneConnection(t *testing.T) {
	srv := sshtest.New(t)
	p := newPool(t, Options{MaxConnsPerServer: 1})
	h := p.Handle(targetFor(srv))

	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := h.Exec(context.Background(), fmt.Sprintf("echo %d", i))
			if err == nil && out != fmt.Sprint(i) {
				err = fmt.Errorf("exec %d returned %q", i, out)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, 1, srv.Accepts(), "every call shares one TCP connection")
	assert.LessOrEqual(t, srv.PeakRunning(), DefaultMaxSessionsPerConn)
}

func TestConcurrentExecsStayWithinConnectionCap(t *testing.T) {
	srv := sshtest.New(t)
	p := newPool(t, Options{})
	h := p.Handle(targetFor(srv))

	var wg sync.WaitGroup
	var failed atomic.Int32
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := h.Exec(context.Background(), fmt.Sprintf("echo %d", i)); err != nil {
				failed.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.Zero(t, failed.Load())
	assert.LessOrEqual(t, srv.Accepts(), DefaultMaxConnsPerServer)
}

func TestSessionSlotsCapConnectionsAndCallersWait(t *testing.T) {
	srv, hold := newHoldServer(t)
	p := newPool(t, Options{MaxSessionsPerConn: 2, MaxConnsPerServer: 3})
	h := p.Handle(targetFor(srv))

	var holders []<-chan error
	for range 6 {
		holders = append(holders, background(func() error { _, err := h.Exec(context.Background(), "hold"); return err }))
	}
	eventually(t, func() bool { return srv.Running() == 6 }, "six sessions run")
	assert.Equal(t, 3, srv.Accepts(), "at most MaxConnsPerServer connections")

	// Every slot is taken: the next caller waits until its deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := h.Exec(ctx, "echo late")
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, 3, srv.Accepts())

	// A waiting caller proceeds once a slot frees.
	waiter := background(func() error {
		out, err := h.Exec(context.Background(), "echo waited")
		if err == nil && out != "waited" {
			err = fmt.Errorf("got %q", out)
		}
		return err
	})
	time.Sleep(50 * time.Millisecond)
	hold.release()
	require.NoError(t, waitErr(t, waiter))
	for _, ch := range holders {
		require.NoError(t, waitErr(t, ch))
	}
	assert.Equal(t, 3, srv.Accepts())
}

func TestAdaptsToServerMaxSessions(t *testing.T) {
	srv, hold := newHoldServer(t, sshtest.WithMaxSessions(2))
	p := newPool(t, Options{}) // 8 sessions per connection by default
	h := p.Handle(targetFor(srv))

	// Seven callers: six fit on three connections of two sessions each, the
	// seventh is refused on the last one and then waits.
	var holders []<-chan error
	for range 7 {
		holders = append(holders, background(func() error { _, err := h.Exec(context.Background(), "hold"); return err }))
	}
	eventually(t, func() bool { return srv.Running() == 6 }, "six sessions run across three connections")
	eventually(t, func() bool {
		stats := p.stats(serverID)
		if len(stats) != 3 {
			return false
		}
		for _, c := range stats {
			if c.Capacity != 2 || c.InUse != 2 {
				return false
			}
		}
		return true
	}, "every connection's capacity is lowered to the server's limit")
	assert.Equal(t, 3, srv.Accepts())
	assert.Positive(t, srv.Rejected())

	hold.release()
	for _, ch := range holders {
		require.NoError(t, waitErr(t, ch))
	}
}

func TestReconnectsAfterDropAll(t *testing.T) {
	srv := sshtest.New(t)
	p := newPool(t, Options{})
	h := p.Handle(targetFor(srv))

	_, err := h.Exec(context.Background(), "echo one")
	require.NoError(t, err)
	srv.DropAll()

	out, err := h.Exec(context.Background(), "echo two")
	require.NoError(t, err)
	assert.Equal(t, "two", out)
	assert.Equal(t, 2, srv.Accepts())
}

func TestKeepaliveClosesSilentConnection(t *testing.T) {
	srv := sshtest.New(t)
	p := newPool(t, Options{KeepaliveInterval: 20 * time.Millisecond, KeepaliveTimeout: 50 * time.Millisecond})
	h := p.Handle(targetFor(srv))

	_, err := h.Exec(context.Background(), "echo up")
	require.NoError(t, err)
	srv.SetKeepaliveReplies(false)
	eventually(t, func() bool { return len(p.stats(serverID)) == 0 }, "silent connection removed")
	eventually(t, func() bool { return srv.Conns() == 0 }, "silent connection closed")

	srv.SetKeepaliveReplies(true)
	_, err = h.Exec(context.Background(), "echo again")
	require.NoError(t, err)
	assert.Equal(t, 2, srv.Accepts())
}

func TestBackoffFailsFastAndForceBypassesIt(t *testing.T) {
	srv := sshtest.New(t)
	var dials atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	dialErr := errors.New("connection refused on web-1")
	p := newPool(t, Options{
		BackoffMin: time.Minute,
		Dialer: func(ctx context.Context, cfg sshclient.Config) (*sshclient.Client, error) {
			dials.Add(1)
			if fail.Load() {
				return nil, dialErr
			}
			return sshclient.Dial(ctx, cfg)
		},
	})
	h := p.Handle(targetFor(srv))

	_, err := h.Exec(context.Background(), "echo x")
	require.ErrorIs(t, err, dialErr)
	assert.Equal(t, int32(1), dials.Load())

	// During backoff: fail fast, no dial.
	_, err = h.Exec(context.Background(), "echo x")
	require.ErrorIs(t, err, ErrUnreachable)
	require.ErrorIs(t, err, dialErr, "the last dial error is wrapped")
	var unreachable *UnreachableError
	require.True(t, errors.As(err, &unreachable))
	assert.Greater(t, unreachable.RetryIn, 50*time.Second)
	_, err = h.Ping(context.Background())
	require.ErrorIs(t, err, ErrUnreachable)
	assert.Equal(t, int32(1), dials.Load())

	// Force dials anyway.
	fail.Store(false)
	info, err := h.Ensure(context.Background(), EnsureOpts{Force: true})
	require.NoError(t, err)
	assert.True(t, info.Fresh)
	assert.Equal(t, int32(2), dials.Load())
	_, err = h.Exec(context.Background(), "echo x")
	require.NoError(t, err)
	assert.Equal(t, int32(2), dials.Load())
}

func TestBackoffRange(t *testing.T) {
	p := newPool(t, Options{BackoffMin: time.Second, BackoffMax: time.Minute})
	assert.Equal(t, time.Second, p.backoff(1))
	for n := 2; n <= 12; n++ {
		hi := time.Second << (n - 1)
		if hi > time.Minute {
			hi = time.Minute
		}
		for range 20 {
			d := p.backoff(n)
			assert.GreaterOrEqual(t, d, time.Second)
			assert.LessOrEqual(t, d, hi)
		}
	}
}

func TestOnFirstUseRunsOnceUnderConcurrentFirstUse(t *testing.T) {
	srv := sshtest.New(t)
	p := newPool(t, Options{MaxSessionsPerConn: 2}) // force overflow dials too
	var calls atomic.Int32
	var pinned atomic.Value
	tgt := targetFor(srv)
	tgt.OnFirstUse = func(_ context.Context, key string) (bool, error) {
		calls.Add(1)
		pinned.Store(key)
		time.Sleep(20 * time.Millisecond) // widen the window for a second dial
		return true, nil
	}

	var wg sync.WaitGroup
	var failed atomic.Int32
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.Handle(tgt).Exec(context.Background(), "echo hi"); err != nil {
				failed.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.Zero(t, failed.Load())
	assert.Equal(t, int32(1), calls.Load(), "the pin is written exactly once")
	assert.Equal(t, sshclient.MarshalHostKey(srv.HostKey()), pinned.Load())
	assert.Greater(t, srv.Accepts(), 1, "overflow connections were dialled with the live key as their pin")
}

func TestEnsureReportsNewlyPinnedAndFresh(t *testing.T) {
	srv := sshtest.New(t)
	p := newPool(t, Options{})
	tgt := targetFor(srv)
	tgt.OnFirstUse = func(context.Context, string) (bool, error) { return true, nil }
	h := p.Handle(tgt)

	info, err := h.Ensure(context.Background(), EnsureOpts{Force: true})
	require.NoError(t, err)
	assert.True(t, info.Fresh)
	assert.True(t, info.NewlyPinned)
	assert.Equal(t, sshclient.MarshalHostKey(srv.HostKey()), info.HostKey)

	info, err = h.Ensure(context.Background(), EnsureOpts{})
	require.NoError(t, err)
	assert.False(t, info.Fresh)
	assert.False(t, info.NewlyPinned)

	rtt, err := h.Ping(context.Background())
	require.NoError(t, err)
	assert.Positive(t, rtt)
	assert.Equal(t, 1, srv.Accepts())
}

func TestOnFirstUseErrorClosesConnection(t *testing.T) {
	srv := sshtest.New(t)
	p := newPool(t, Options{})
	dbDown := errors.New("db down")
	tgt := targetFor(srv)
	tgt.OnFirstUse = func(context.Context, string) (bool, error) { return false, dbDown }

	_, err := p.Handle(tgt).Exec(context.Background(), "echo hi")
	require.ErrorIs(t, err, dbDown)
	assert.Contains(t, err.Error(), "record the SSH host key")
	assert.Empty(t, p.stats(serverID))
	eventually(t, func() bool { return srv.Conns() == 0 }, "the connection is closed")
	assert.NotContains(t, srv.Commands(), "echo hi")

	// A concurrent winner with another key surfaces as the mismatch itself.
	p2 := newPool(t, Options{})
	mismatch := &sshclient.HostKeyMismatchError{Addr: srv.Addr, Expected: "SHA256:a", Presented: "SHA256:b"}
	tgt.OnFirstUse = func(context.Context, string) (bool, error) { return false, mismatch }
	_, err = p2.Handle(tgt).Exec(context.Background(), "echo hi")
	var mm *sshclient.HostKeyMismatchError
	require.True(t, errors.As(err, &mm))
	assert.Equal(t, mismatch.Error(), err.Error())
}

func TestCredentialChangeRetiresConnection(t *testing.T) {
	srv, hold := newHoldServer(t)
	p := newPool(t, Options{})
	old := targetFor(srv)

	inFlight := background(func() error {
		out, err := p.Handle(old).Exec(context.Background(), "hold")
		if err == nil && out != "held" {
			err = fmt.Errorf("got %q", out)
		}
		return err
	})
	eventually(t, func() bool { return srv.Running() == 1 }, "in-flight session runs")

	changed := old
	changed.User = "root"
	out, err := p.Handle(changed).Exec(context.Background(), "echo new")
	require.NoError(t, err)
	assert.Equal(t, "new", out)
	assert.Equal(t, 2, srv.Accepts(), "new sessions use a new connection")

	stats := p.stats(serverID)
	require.Len(t, stats, 2)
	retired := 0
	for _, c := range stats {
		if c.Retired {
			retired++
			assert.Equal(t, 1, c.InUse)
		}
	}
	assert.Equal(t, 1, retired)

	// The in-flight session finishes on the old connection, which then closes.
	hold.release()
	require.NoError(t, waitErr(t, inFlight))
	eventually(t, func() bool { return srv.Conns() == 1 && len(p.stats(serverID)) == 1 }, "the retired connection closes when idle")
}

func TestPinnedKeyChangeRetiresConnection(t *testing.T) {
	srv, hold := newHoldServer(t)
	p := newPool(t, Options{})
	tofu := targetFor(srv)
	tofu.OnFirstUse = func(context.Context, string) (bool, error) { return true, nil }
	_, err := p.Handle(tofu).Exec(context.Background(), "echo pinned")
	require.NoError(t, err)

	oldKey := sshclient.MarshalHostKey(srv.HostKey())
	pinned := targetFor(srv)
	pinned.PinnedHostKey = oldKey
	_, err = p.Handle(pinned).Exec(context.Background(), "echo same pin")
	require.NoError(t, err)
	_, err = p.Handle(targetFor(srv)).Exec(context.Background(), "echo stale empty pin")
	require.NoError(t, err)
	assert.Equal(t, 1, srv.Accepts(), "a matching or empty pin keeps the connection")

	inFlight := background(func() error { _, err := p.Handle(pinned).Exec(context.Background(), "hold"); return err })
	eventually(t, func() bool { return srv.Running() == 1 }, "in-flight session runs")

	// The server is rebuilt and an operator accepts its new key.
	newKey := sshtest.NewEd25519Signer(t)
	srv.SetHostKey(newKey)
	repinned := targetFor(srv)
	repinned.PinnedHostKey = sshclient.MarshalHostKey(newKey.PublicKey())
	_, err = p.Handle(repinned).Exec(context.Background(), "echo new key")
	require.NoError(t, err)
	assert.Equal(t, 2, srv.Accepts())

	hold.release()
	require.NoError(t, waitErr(t, inFlight))
	eventually(t, func() bool { return len(p.stats(serverID)) == 1 }, "the old connection closes when idle")

	// A pin the server does not present fails with the mismatch, then fails
	// fast during backoff while still naming it.
	wrong := targetFor(srv)
	wrong.PinnedHostKey = oldKey
	_, err = p.Handle(wrong).Exec(context.Background(), "echo wrong")
	var mm *sshclient.HostKeyMismatchError
	require.True(t, errors.As(err, &mm), "got %v", err)
	assert.Equal(t, ssh.FingerprintSHA256(newKey.PublicKey()), mm.Presented)
	_, err = p.Handle(wrong).Exec(context.Background(), "echo wrong")
	require.ErrorIs(t, err, ErrUnreachable)
	require.True(t, errors.As(err, &mm))
}

func TestInvalidateClosesImmediately(t *testing.T) {
	srv, _ := newHoldServer(t)
	p := newPool(t, Options{})
	h := p.Handle(targetFor(srv))

	inFlight := background(func() error { _, err := h.Exec(context.Background(), "hold"); return err })
	eventually(t, func() bool { return srv.Running() == 1 }, "in-flight session runs")

	p.Invalidate(serverID)
	err := waitErr(t, inFlight)
	require.Error(t, err)
	assert.NotErrorIs(t, err, context.Canceled)
	eventually(t, func() bool { return srv.Conns() == 0 }, "the connection is closed")

	_, err = h.Exec(context.Background(), "echo again")
	require.NoError(t, err)
	assert.Equal(t, 2, srv.Accepts())
	p.Invalidate("unknown-server") // no-op
}

func TestCloseIsIdempotentAndFailsLaterCalls(t *testing.T) {
	srv, _ := newHoldServer(t)
	p := New(Options{MaxSessionsPerConn: 1, MaxConnsPerServer: 1})
	h := p.Handle(targetFor(srv))

	holding := background(func() error { _, err := h.Exec(context.Background(), "hold"); return err })
	eventually(t, func() bool { return srv.Running() == 1 }, "the only slot is taken")
	waiting := background(func() error { _, err := h.Exec(context.Background(), "echo queued"); return err })
	time.Sleep(20 * time.Millisecond)

	require.NoError(t, p.Close())
	require.NoError(t, p.Close())
	assert.Error(t, waitErr(t, holding))
	assert.ErrorIs(t, waitErr(t, waiting), ErrPoolClosed)

	_, err := h.Exec(context.Background(), "echo after")
	assert.ErrorIs(t, err, ErrPoolClosed)
	_, err = h.Ensure(context.Background(), EnsureOpts{Force: true})
	assert.ErrorIs(t, err, ErrPoolClosed)
	_, err = h.DialUnix(context.Background(), sshclient.DockerSocketPath)
	assert.ErrorIs(t, err, ErrPoolClosed)
	eventually(t, func() bool { return srv.Conns() == 0 }, "connections closed")
}

func TestReaperClosesIdleOverflowConnections(t *testing.T) {
	srv, hold := newHoldServer(t)
	p := newPool(t, Options{MaxSessionsPerConn: 1, OverflowIdle: 50 * time.Millisecond, PrimaryIdle: time.Hour, ReapInterval: 10 * time.Millisecond})
	h := p.Handle(targetFor(srv))

	a := background(func() error { _, err := h.Exec(context.Background(), "hold"); return err })
	b := background(func() error { _, err := h.Exec(context.Background(), "hold"); return err })
	eventually(t, func() bool { return srv.Running() == 2 }, "two sessions on two connections")
	assert.Equal(t, 2, srv.Accepts())

	hold.release()
	require.NoError(t, waitErr(t, a))
	require.NoError(t, waitErr(t, b))
	eventually(t, func() bool { return srv.Conns() == 1 }, "the idle overflow connection is reaped")
	// The server sees the close first; the pool drops the connection when its reader ends.
	eventually(t, func() bool { return len(p.stats(serverID)) == 1 }, "the pool drops it")
	assert.True(t, p.stats(serverID)[0].Primary)
}

func TestReaperForgetsUnusedEntries(t *testing.T) {
	srv := sshtest.New(t)
	p := newPool(t, Options{OverflowIdle: 10 * time.Millisecond, PrimaryIdle: 10 * time.Millisecond, ReapInterval: 5 * time.Millisecond})
	_, err := p.Handle(targetFor(srv)).Exec(context.Background(), "echo x")
	require.NoError(t, err)
	eventually(t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.entries) == 0
	}, "the entry (and its credentials) are dropped once idle")
}

func TestDockerClientUsesPrimaryWithoutSlot(t *testing.T) {
	api := sshtest.ServeHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Api-Version", "1.47")
		_, _ = io.WriteString(w, "OK")
	}))
	srv, hold := newHoldServer(t, sshtest.WithStreamLocal(api))
	p := newPool(t, Options{MaxSessionsPerConn: 1, MaxConnsPerServer: 1})
	h := p.Handle(targetFor(srv))

	holding := background(func() error { _, err := h.Exec(context.Background(), "hold"); return err })
	eventually(t, func() bool { return srv.Running() == 1 }, "the only session slot is taken")

	dc, err := h.DockerClient()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ping, err := dc.Ping(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.47", ping.APIVersion)
	assert.Equal(t, 1, srv.Accepts())
	assert.Equal(t, 1, srv.PeakSessions())
	require.NoError(t, dc.Close())
	eventually(t, func() bool {
		s := p.stats(serverID)
		return len(s) == 1 && s[0].Tunnels == 0
	}, "closing the Docker client releases its tunnels")

	hold.release()
	require.NoError(t, waitErr(t, holding))
}

func TestPTYHoldsSlotUntilClosed(t *testing.T) {
	srv := sshtest.New(t)
	p := newPool(t, Options{MaxSessionsPerConn: 1, MaxConnsPerServer: 1})
	h := p.Handle(targetFor(srv))

	pty, err := h.NewPTY(context.Background(), 80, 24)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = h.Exec(ctx, "echo blocked")
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	pty.Close()
	out, err := h.Exec(context.Background(), "echo free")
	require.NoError(t, err)
	assert.Equal(t, "free", out)
	assert.Equal(t, 1, srv.Accepts(), "closing the PTY keeps the connection")
}

func TestCancelledExecSignalsAndFreesSlot(t *testing.T) {
	srv := sshtest.New(t)
	p := newPool(t, Options{MaxSessionsPerConn: 1, MaxConnsPerServer: 1})
	h := p.Handle(targetFor(srv))

	ctx, cancel := context.WithCancel(context.Background())
	var lines atomic.Int32
	streaming := background(func() error {
		return h.ExecStream(ctx, "block", func(string) { lines.Add(1) })
	})
	eventually(t, func() bool { return srv.Running() == 1 }, "stream runs")
	cancel()
	assert.ErrorIs(t, waitErr(t, streaming), context.Canceled)
	eventually(t, func() bool { return len(srv.Signals()) == 1 }, "the remote process is sent SIGTERM")
	assert.Equal(t, []string{"TERM"}, srv.Signals())

	out, err := h.Exec(context.Background(), "echo next")
	require.NoError(t, err)
	assert.Equal(t, "next", out)
}

func TestUploadAndExecInput(t *testing.T) {
	srv := sshtest.New(t)
	p := newPool(t, Options{})
	h := p.Handle(targetFor(srv))

	require.NoError(t, h.Upload(context.Background(), "'/tmp/paas-compose-1/.env'", []byte("A=1\n")))
	got, ok := srv.File("/tmp/paas-compose-1/.env")
	require.True(t, ok)
	assert.Equal(t, "A=1\n", string(got))

	_, err := h.ExecInput(context.Background(), "cat > /tmp/in", strings.NewReader("stdin"))
	require.NoError(t, err)
	got, _ = srv.File("/tmp/in")
	assert.Equal(t, "stdin", string(got))
	for _, c := range srv.Commands() {
		assert.NotContains(t, c, "A=1")
	}
}

func TestPingRedialsAfterSilentDeath(t *testing.T) {
	srv := sshtest.New(t)
	p := newPool(t, Options{KeepaliveInterval: time.Hour, KeepaliveTimeout: 50 * time.Millisecond})
	h := p.Handle(targetFor(srv))
	_, err := h.Ensure(context.Background(), EnsureOpts{})
	require.NoError(t, err)

	// The reused connection stops answering; Ping drops it and measures a
	// fresh one instead of reporting the server down.
	srv.SetKeepaliveReplies(false)
	go func() {
		time.Sleep(20 * time.Millisecond)
		srv.SetKeepaliveReplies(true)
	}()
	_, err = h.Ping(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, srv.Accepts())
}

func TestTargetWithoutServerID(t *testing.T) {
	p := newPool(t, Options{})
	_, err := p.Handle(Target{}).Exec(context.Background(), "echo x")
	assert.Error(t, err)
}

func TestQueuedCallerWakesWhenTheOverflowBackoffEnds(t *testing.T) {
	srv, hold := newHoldServer(t)
	var dials atomic.Int32
	p := newPool(t, Options{
		MaxSessionsPerConn: 1,
		MaxConnsPerServer:  2,
		BackoffMin:         200 * time.Millisecond,
		BackoffMax:         200 * time.Millisecond,
		Dialer: func(ctx context.Context, cfg sshclient.Config) (*sshclient.Client, error) {
			if dials.Add(1) == 2 {
				return nil, errors.New("ssh: handshake failed: EOF") // the first overflow dial fails once
			}
			return sshclient.Dial(ctx, cfg)
		},
	})
	h := p.Handle(targetFor(srv))

	// A long-lived session (an open log tab) takes the primary's only slot.
	holder := background(func() error { _, err := h.Exec(context.Background(), "hold"); return err })
	eventually(t, func() bool { return srv.Running() == 1 }, "the long-lived session runs")

	// The next caller's overflow dial fails; it queues behind the backoff and
	// must dial again when the backoff ends, not wait for a session to end.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	out, err := h.Exec(ctx, "echo after backoff")
	require.NoError(t, err)
	assert.Equal(t, "after backoff", out)
	assert.Less(t, time.Since(start), 2*time.Second)
	assert.Equal(t, int32(3), dials.Load())

	hold.release()
	require.NoError(t, waitErr(t, holder))
}

func TestTrustOnFirstUseChecksTheStoredPinFirst(t *testing.T) {
	ctx := context.Background()

	t.Run("a key pinned since the caller read the row is enforced before authentication", func(t *testing.T) {
		impostor := sshtest.New(t)
		p := newPool(t, Options{})
		stored := sshclient.MarshalHostKey(sshtest.NewEd25519Signer(t).PublicKey())
		tgt := targetFor(impostor) // PinnedHostKey "": read before the first pin
		tgt.LoadPin = func(context.Context) (string, error) { return stored, nil }
		tgt.OnFirstUse = func(context.Context, string) (bool, error) {
			t.Error("trusted on first use while a key is pinned")
			return true, nil
		}

		_, err := p.Handle(tgt).Exec(ctx, "echo hi")
		var mm *sshclient.HostKeyMismatchError
		require.ErrorAs(t, err, &mm)
		assert.Equal(t, ssh.FingerprintSHA256(impostor.HostKey()), mm.Presented)
		assert.Zero(t, impostor.AuthAttempts(), "refused before any credential is sent")
		assert.NotContains(t, impostor.Commands(), "echo hi")
	})

	t.Run("nothing stored: trust on first use as before", func(t *testing.T) {
		srv := sshtest.New(t)
		p := newPool(t, Options{})
		var firstUse atomic.Int32
		tgt := targetFor(srv)
		tgt.LoadPin = func(context.Context) (string, error) { return "", nil }
		tgt.OnFirstUse = func(context.Context, string) (bool, error) { firstUse.Add(1); return true, nil }

		info, err := p.Handle(tgt).Ensure(ctx, EnsureOpts{})
		require.NoError(t, err)
		assert.True(t, info.NewlyPinned)
		assert.Equal(t, int32(1), firstUse.Load())
	})

	t.Run("a stored pin that cannot be read fails closed", func(t *testing.T) {
		srv := sshtest.New(t)
		p := newPool(t, Options{})
		tgt := targetFor(srv)
		tgt.LoadPin = func(context.Context) (string, error) { return "", errors.New("db down") }
		tgt.OnFirstUse = func(context.Context, string) (bool, error) {
			t.Error("trusted on first use without knowing the stored pin")
			return true, nil
		}

		_, err := p.Handle(tgt).Exec(ctx, "echo hi")
		require.ErrorContains(t, err, "read the pinned SSH host key")
		assert.Zero(t, srv.Accepts())

		// With a pin of its own, the caller is still protected: it dials pinned.
		p2 := newPool(t, Options{})
		tgt.PinnedHostKey = sshclient.MarshalHostKey(srv.HostKey())
		out, err := p2.Handle(tgt).Exec(ctx, "echo pinned")
		require.NoError(t, err)
		assert.Equal(t, "pinned", out)
	})
}

func TestHandleWithAReplacedPinCannotDisturbThePool(t *testing.T) {
	ctx := context.Background()
	srv := sshtest.New(t)
	p := newPool(t, Options{BackoffMin: time.Minute})
	var stored atomic.Value
	stored.Store(sshclient.MarshalHostKey(srv.HostKey()))
	target := func(pin string) Target {
		tgt := targetFor(srv)
		tgt.PinnedHostKey = pin
		tgt.LoadPin = func(context.Context) (string, error) { return stored.Load().(string), nil }
		return tgt
	}

	// A deploy starts with the original pin.
	old := p.Handle(target(stored.Load().(string)))
	_, err := old.Exec(ctx, "echo build")
	require.NoError(t, err)

	// The server is rebuilt and an operator replaces the pin, which
	// invalidates the pool (server.Service.ResetHostKey).
	rebuilt := sshtest.NewEd25519Signer(t)
	srv.SetHostKey(rebuilt)
	newPin := sshclient.MarshalHostKey(rebuilt.PublicKey())
	stored.Store(newPin)
	p.Invalidate(serverID)

	// The deploy's deferred cleanup runs first, with the replaced pin: the
	// pool dials with the stored pin, and only the cleanup fails.
	_, err = old.Exec(ctx, "echo cleanup")
	var mm *sshclient.HostKeyMismatchError
	require.ErrorAs(t, err, &mm)
	assert.Contains(t, err.Error(), "was replaced while this operation ran")
	assert.NotErrorIs(t, err, ErrUnreachable)

	current := p.Handle(target(newPin))
	out, err := current.Exec(ctx, "echo metrics")
	require.NoError(t, err)
	assert.Equal(t, "metrics", out)

	// Again, with a connection up: still only the stale call fails.
	_, err = old.Exec(ctx, "echo cleanup")
	require.ErrorAs(t, err, &mm)
	out, err = current.Exec(ctx, "echo test connection")
	require.NoError(t, err, "no backoff, no retired connection")
	assert.Equal(t, "test connection", out)

	assert.Equal(t, 2, srv.Accepts(), "one connection before the reset and one after")
	assert.NotContains(t, srv.Commands(), "echo cleanup")
	for _, c := range p.stats(serverID) {
		assert.False(t, c.Retired)
	}
}

func TestPinThatDiffersFromTheStoredOneRetiresAtMostOnce(t *testing.T) {
	// The stored pin changed without an Invalidate (the break-glass SQL,
	// then trust on first use): a caller still holding the older pin must
	// not retire and redial in a loop.
	ctx := context.Background()
	srv := sshtest.New(t)
	p := newPool(t, Options{})
	current := sshclient.MarshalHostKey(srv.HostKey())
	tgt := targetFor(srv)
	tgt.LoadPin = func(context.Context) (string, error) { return current, nil }
	tgt.PinnedHostKey = current
	_, err := p.Handle(tgt).Exec(ctx, "echo up")
	require.NoError(t, err)

	outdated := tgt
	outdated.PinnedHostKey = sshclient.MarshalHostKey(sshtest.NewEd25519Signer(t).PublicKey())
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err = p.Handle(outdated).Exec(callCtx, "echo outdated")
	var mm *sshclient.HostKeyMismatchError
	require.ErrorAs(t, err, &mm)
	assert.NoError(t, callCtx.Err(), "it fails at once rather than looping until its deadline")
	assert.Equal(t, 2, srv.Accepts(), "one retirement, one redial with the stored pin")

	_, err = p.Handle(tgt).Exec(ctx, "echo still up")
	require.NoError(t, err)
	assert.Equal(t, 2, srv.Accepts())
}

// A tunnel closes its write side alone, as docker exec's stdin needs: the
// far end reads to EOF and still answers (D-160's Reset over SSH).
func TestTunnelCloseWriteEndsInputOnly(t *testing.T) {
	srv := sshtest.New(t, sshtest.WithStreamLocal(func(_ string, ch io.ReadWriteCloser) {
		defer ch.Close()
		in, _ := io.ReadAll(ch)
		_, _ = ch.Write(append([]byte("got:"), in...))
	}))
	p := newPool(t, Options{})
	conn, err := p.Handle(targetFor(srv)).DialUnix(context.Background(), "/var/run/docker.sock")
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.Write([]byte("pw"))
	require.NoError(t, err)
	cw, ok := conn.(interface{ CloseWrite() error })
	require.True(t, ok, "the Docker client finds CloseWrite on the tunnel")
	require.NoError(t, cw.CloseWrite())
	read := make(chan string, 1)
	go func() { out, _ := io.ReadAll(conn); read <- string(out) }()
	select {
	case out := <-read:
		assert.Equal(t, "got:pw", out)
	case <-time.After(5 * time.Second):
		t.Fatal("the far end never saw the input end")
	}
}
