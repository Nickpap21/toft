package sshclient

import (
	"bytes"
	"context"
	"fmt"
	mathrand "math/rand/v2"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/testenv"
	"github.com/Nickpap21/toft/backend/pkg/sshclient/sshtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWrap(t *testing.T) {
	for name, tc := range map[string]struct {
		cmd, mark string
		limit     time.Duration
		want      string
	}{
		"an agent's connection": {"docker ps", "", time.Minute, "docker ps"},
		"no deadline":           {"docker ps", "M", 0, `export TOFT_EXEC=M; set -- "${SHELL:-sh}" -c 'docker ps'; exec "$@"`},
		"quotes":                {"echo 'a b'", "M", 0, `export TOFT_EXEC=M; set -- "${SHELL:-sh}" -c 'echo '\''a b'\'''; exec "$@"`},
		"a deadline": {"docker ps", "M", 90 * time.Second,
			`export TOFT_EXEC=M; set -- "${SHELL:-sh}" -c 'docker ps'; timeout -k 1 1 true 2>/dev/null && set -- timeout -k 10 90 "$@"; exec "$@"`},
		"part of a second": {"docker ps", "M", 1500 * time.Millisecond,
			`export TOFT_EXEC=M; set -- "${SHELL:-sh}" -c 'docker ps'; timeout -k 1 1 true 2>/dev/null && set -- timeout -k 10 2 "$@"; exec "$@"`},
	} {
		assert.Equal(t, tc.want, wrap(tc.cmd, tc.mark, tc.limit), name)
	}
}

// marks records the mark each command arrived with.
type marks struct {
	mu sync.Mutex
	m  map[string]string
}

func (m *marks) handler(next sshtest.ExecHandler) sshtest.ExecHandler {
	return func(s *sshtest.Session) int {
		m.mu.Lock()
		if m.m == nil {
			m.m = map[string]string{}
		}
		m.m[s.Command] = s.Mark
		m.mu.Unlock()
		return next(s)
	}
}

func (m *marks) of(cmd string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.m[cmd]
}

// A cancelled command is ended by its mark, from a second session: the
// signal and the close alone stop nothing on a root login.
func TestCancelReapsTheCommand(t *testing.T) {
	var seen marks
	var srv *sshtest.Server
	srv = sshtest.New(t, sshtest.WithExec(seen.handler(func(s *sshtest.Session) int { return srv.DefaultExec(s) })))
	c := dialT(t, testConfig(srv))

	_, err := c.ExecContext(context.Background(), "echo done")
	require.NoError(t, err)
	assert.NotEmpty(t, seen.of("echo done"), "every command carries a mark")
	assert.Empty(t, srv.Reaps(), "a command that finished needs no reaper")

	for name, run := range map[string]func(ctx context.Context) error{
		"exec":   func(ctx context.Context) error { _, err := c.ExecContext(ctx, "block"); return err },
		"stream": func(ctx context.Context) error { return c.ExecStreamContext(ctx, "block", func(string) {}) },
	} {
		before := len(srv.Reaps())
		ctx, cancel := context.WithCancel(context.Background())
		errc := make(chan error, 1)
		go func() { errc <- run(ctx) }()
		require.Eventually(t, func() bool { return srv.Running() == 1 }, 2*time.Second, 5*time.Millisecond, name)
		cancel()
		require.ErrorIs(t, <-errc, context.Canceled, name)
		reaps := srv.Reaps()
		require.Len(t, reaps, before+1, "%s: the reaper ran before the call returned", name)
		assert.Equal(t, seen.of("block"), reaps[before], name)
		require.Eventually(t, func() bool { return srv.Running() == 0 }, 2*time.Second, 5*time.Millisecond, name)
	}
	marksSeen := srv.Reaps()
	assert.NotEqual(t, marksSeen[0], marksSeen[1], "a mark per command")
}

// A reaper waits for the one before it only as long as its own run may
// take: cancelled calls on a server that stopped answering return together,
// not one reaper's timeout after another, each holding a session slot.
func TestReaperWaitsForAnotherOnlySoLong(t *testing.T) {
	oldGrace, oldSlack := reapGrace, reapSlack
	reapGrace, reapSlack = 0, 100*time.Millisecond
	t.Cleanup(func() { reapGrace, reapSlack = oldGrace, oldSlack })

	srv := sshtest.New(t)
	c := dialT(t, testConfig(srv))
	c.reaping <- struct{}{} // another reaper, which does not end

	done := make(chan struct{})
	go func() { c.reap(context.Background(), "mark"); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("still waiting for the other reaper")
	}
	assert.Empty(t, srv.Reaps())
}

// A reply over the cap closes the session, which stops the command no more
// than a cancel does.
func TestReplyTooLargeReapsTheCommand(t *testing.T) {
	var seen marks
	srv := sshtest.New(t, sshtest.WithExec(seen.handler(func(s *sshtest.Session) int {
		chunk := []byte(strings.Repeat("x", 32<<10))
		for {
			if _, err := s.Stdout.Write(chunk); err != nil {
				return 141
			}
		}
	})))
	c := dialT(t, testConfig(srv))

	_, err := c.ExecContext(context.Background(), "flood")
	require.ErrorIs(t, err, ErrReplyTooLarge)
	assert.Equal(t, []string{seen.of("flood")}, srv.Reaps())
}

// A server's agent ends a command's process group itself and matches its
// typed requests by name: its connection carries commands as they are.
func TestAgentConnectionIsLeftAlone(t *testing.T) {
	var raw []string
	var mu sync.Mutex
	var srv *sshtest.Server
	srv = sshtest.New(t, sshtest.WithExec(func(s *sshtest.Session) int {
		mu.Lock()
		raw = append(raw, s.Raw)
		mu.Unlock()
		return srv.DefaultExec(s)
	}))
	cfg := testConfig(srv)
	cfg.Conn = func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", srv.Addr)
	}
	c := dialT(t, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err := c.ExecContext(ctx, "echo hi")
	require.NoError(t, err)

	ctx, cancel = context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { _, err := c.ExecContext(ctx, "block"); errc <- err }()
	require.Eventually(t, func() bool { return srv.Running() == 1 }, 2*time.Second, 5*time.Millisecond)
	cancel()
	require.ErrorIs(t, <-errc, context.Canceled)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"echo hi", "block"}, raw)
	assert.Empty(t, srv.Reaps())
}

// A typed request is sent by its exact name on any connection.
func TestTypedRequestsAreNotWrapped(t *testing.T) {
	var raw []string
	var mu sync.Mutex
	srv := sshtest.New(t, sshtest.WithExec(func(s *sshtest.Session) int {
		mu.Lock()
		raw = append(raw, s.Raw)
		mu.Unlock()
		return 0
	}))
	c := dialT(t, testConfig(srv))
	_, err := c.SubsystemContext(context.Background(), "toft-agent-state", nil)
	require.NoError(t, err)
	require.NoError(t, c.ExecStreamInputContext(context.Background(), "toft-agent-rollout", nil, func(string) {}))
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"toft-agent-state", "toft-agent-rollout"}, raw)
}

// What the client wraps a command in changes nothing the command sees but
// TOFT_EXEC: its stdin, its output, its quoting and its exit status are its
// own, with a deadline (on a server with or without `timeout`) and without.
func TestWrappedCommandRunsAsGiven(t *testing.T) {
	srv := sshtest.New(t, sshtest.WithShell())
	c := dialT(t, testConfig(srv))

	for name, ctx := range map[string]context.Context{
		"no deadline": context.Background(),
		"a deadline":  deadlineCtx(t, time.Minute),
	} {
		out, err := c.ExecInputContext(ctx, `echo 'a  b' "$TOFT_EXEC"; cat; exit 3`, strings.NewReader("from stdin"))
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "exited with status 3", name)
		fields := strings.Fields(out)
		require.Len(t, fields, 5, "%s: %q", name, out)
		assert.True(t, strings.HasPrefix(out, "a  b "), "%s: %q", name, out)
		assert.Len(t, fields[2], 26, "%s: the mark", name)
		assert.Equal(t, "from stdin", fields[3]+" "+fields[4], name)

		dir := t.TempDir()
		path := filepath.Join(dir, "it's here")
		require.NoError(t, c.UploadContext(ctx, shellQuote(path), []byte("content")), name)
		got, err := os.ReadFile(path)
		require.NoError(t, err, name)
		assert.Equal(t, "content", string(got), name)
	}
}

func deadlineCtx(t *testing.T, d time.Duration) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

// procsWith lists the processes whose command line holds arg (Linux).
func procsWith(arg string) []string {
	var found []string
	files, _ := filepath.Glob("/proc/[0-9]*/cmdline")
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err == nil && bytes.Contains(b, []byte(arg)) {
			found = append(found, f+": "+strings.ReplaceAll(string(b), "\x00", " "))
		}
	}
	return found
}

// sleepArg is a sleep duration no other test's process has.
func sleepArg() string { return fmt.Sprint(1_000_000 + mathrand.IntN(8_000_000)) }

// The case the BuildKit drill found (private/drills/2026-11-16-buildkit):
// sshd passes no signal on for a root login and a closed session ends
// nothing that isn't writing. The cancelled call returns with the command,
// what it started in the background and what ignores SIGTERM all gone.
func TestCancelledCommandIsGoneFromTheServer(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the reaper reads /proc")
	}
	// The reaper is written for the servers Toft takes, Debian and Ubuntu:
	// busybox's grep (Alpine) has no -z.
	if exec.Command("grep", "-z", "--version").Run() != nil {
		testenv.Skip(t, "no GNU grep here")
	}
	old := reapGrace
	reapGrace = time.Second
	t.Cleanup(func() { reapGrace = old })

	for name, run := range map[string]func(c *Client, ctx context.Context, cmd string) error{
		"exec": func(c *Client, ctx context.Context, cmd string) error { _, err := c.ExecContext(ctx, cmd); return err },
		"stream": func(c *Client, ctx context.Context, cmd string) error {
			return c.ExecStreamContext(ctx, cmd, func(string) {})
		},
		// The command floods its reply a second in, with the rest still
		// running.
		"a reply too large": func(c *Client, _ context.Context, cmd string) error {
			_, err := c.ExecContext(context.Background(), "{ sleep 1; head -c 5000000 /dev/zero; } & "+cmd)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := sshtest.New(t, sshtest.WithShell())
			c := dialT(t, testConfig(srv))
			n := sleepArg()
			t.Cleanup(func() { _ = exec.Command("pkill", "-KILL", "-f", "sleep "+n).Run() })
			// Silent, in the background, and deaf to SIGTERM; then one more
			// the shell would start once the first ended.
			cmd := "sleep " + n + " & sh -c 'trap \"\" TERM; sleep " + n + "' & sleep " + n + "; sleep " + n

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			errc := make(chan error, 1)
			go func() { errc <- run(c, ctx, cmd) }()
			require.Eventually(t, func() bool { return len(procsWith("sleep\x00"+n)) >= 3 }, 5*time.Second, 20*time.Millisecond)
			cancel()
			select {
			case err := <-errc:
				require.Error(t, err)
			case <-time.After(15 * time.Second):
				t.Fatal("the call did not return")
			}
			assert.Empty(t, procsWith(n), "still running on the server")

			out, err := c.ExecContext(context.Background(), "echo still here")
			require.NoError(t, err, "the connection is still usable")
			assert.Equal(t, "still here", out)
		})
	}
}

// With no panel to cancel it (the connection is gone), a command that had
// a deadline is ended on the server by `timeout`, a margin after it.
func TestDeadlineEndsTheCommandWithoutThePanel(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	// GNU's timeout signals the command's whole process group; busybox's
	// (Alpine) only the shell, and what the shell started runs on.
	if exec.Command("timeout", "--version").Run() != nil {
		testenv.Skip(t, "no GNU timeout here")
	}
	old := timeoutMargin
	timeoutMargin = time.Second
	t.Cleanup(func() { timeoutMargin = old })

	srv := sshtest.New(t, sshtest.WithShell())
	c := dialT(t, testConfig(srv))
	n := sleepArg()
	t.Cleanup(func() { _ = exec.Command("pkill", "-KILL", "-f", "sleep "+n).Run() })

	errc := make(chan error, 1)
	go func() {
		_, err := c.ExecContext(deadlineCtx(t, 2*time.Second), "sleep "+n+"; sleep "+n)
		errc <- err
	}()
	require.Eventually(t, func() bool { return len(procsWith("sleep\x00"+n)) == 1 }, 5*time.Second, 20*time.Millisecond)
	srv.DropAll()
	require.Error(t, <-errc)
	require.NotEmpty(t, procsWith(n), "the dropped connection alone ended it: the test proves nothing")
	// 2 s of deadline and 1 s of margin from the start.
	require.Eventually(t, func() bool { return len(procsWith(n)) == 0 }, 6*time.Second, 50*time.Millisecond,
		"still running past its deadline and margin")
}

// A step's commands carry one mark (Marked), and what they left running when
// the connection was lost under them is ended from a new connection by
// Reaper: in the background, deaf to SIGTERM, all of it.
func TestReaperEndsWhatALostConnectionLeft(t *testing.T) {
	assert.Equal(t, "true", Reaper(context.Background()), "no mark, nothing to end")
	if runtime.GOOS != "linux" {
		t.Skip("the reaper reads /proc")
	}
	if exec.Command("grep", "-z", "--version").Run() != nil {
		testenv.Skip(t, "no GNU grep here")
	}
	old := reapGrace
	reapGrace = time.Second
	t.Cleanup(func() { reapGrace = old })

	srv := sshtest.New(t, sshtest.WithShell())
	c := dialT(t, testConfig(srv))
	n := sleepArg()
	t.Cleanup(func() { _ = exec.Command("pkill", "-KILL", "-f", "sleep "+n).Run() })

	step := Marked(context.Background())
	errc := make(chan error, 2)
	go func() { _, err := c.ExecContext(step, "sleep "+n+" & sleep "+n); errc <- err }()
	go func() {
		errc <- c.ExecStreamContext(step, "sh -c 'trap \"\" TERM; sleep "+n+"'", func(string) {})
	}()
	require.Eventually(t, func() bool { return len(procsWith("sleep\x00"+n)) == 3 }, 5*time.Second, 20*time.Millisecond)
	srv.DropAll()
	require.Error(t, <-errc)
	require.Error(t, <-errc)
	require.Len(t, procsWith("sleep\x00"+n), 3, "the dropped connection alone ended them: the test proves nothing")

	again := dialT(t, testConfig(srv))
	_, err := again.ExecContext(context.Background(), Reaper(step))
	require.NoError(t, err)
	assert.Empty(t, procsWith(n), "still running on the server")
	_, err = again.ExecContext(context.Background(), Reaper(step))
	require.NoError(t, err, "nothing left to end")
}
