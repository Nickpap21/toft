package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	serverPkg "github.com/Nickpap21/toft/backend/internal/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// upstream is a container's log: its history, then what is sent on live,
// until the stream's context ends or live is closed. It counts the streams
// started and stopped.
type upstream struct {
	mu      sync.Mutex
	starts  int
	stopped int
	history []string
	live    chan string
}

func (u *upstream) start(ctx context.Context, tail int, logFn func(string)) error {
	u.mu.Lock()
	u.starts++
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.stopped++
		u.mu.Unlock()
	}()
	for _, l := range u.history[max(0, len(u.history)-tail):] {
		logFn(l)
	}
	for {
		select {
		case l, ok := <-u.live:
			if !ok {
				return errors.New("the container stopped")
			}
			logFn(l)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (u *upstream) counts() (starts, stopped int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.starts, u.stopped
}

// viewer follows the hub's log in the background; watch returns once the
// shared log at "k" has viewers of its own.
type viewer struct {
	mu     sync.Mutex
	lines  []string
	done   chan error
	cancel context.CancelFunc
}

func watch(t *testing.T, h *logHub, u *upstream, tail int, slow chan struct{}, viewers int) *viewer {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	v := &viewer{done: make(chan error, 1), cancel: cancel}
	go func() {
		v.done <- h.follow(ctx, "k", tail, u.start, func(l string) {
			if slow != nil {
				<-slow
			}
			v.mu.Lock()
			v.lines = append(v.lines, l)
			v.mu.Unlock()
		})
	}()
	require.Eventually(t, func() bool { return viewersOf(h, "k") == viewers }, time.Second, time.Millisecond)
	return v
}

func (v *viewer) seen() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.lines...)
}

func (v *viewer) has(t *testing.T, want ...string) {
	t.Helper()
	require.Eventually(t, func() bool { return fmt.Sprint(v.seen()) == fmt.Sprint(want) }, time.Second, time.Millisecond, "got %q", v.seen())
}

func viewersOf(h *logHub, key string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if f := h.feeds[key]; f != nil {
		return len(f.viewers)
	}
	return 0
}

func TestLogHub_ViewersShareOneUpstream(t *testing.T) {
	u := &upstream{history: []string{"h1", "h2", "h3"}, live: make(chan string)}
	h := &logHub{}
	a := watch(t, h, u, 2, nil, 1)
	a.has(t, "h2", "h3")
	b := watch(t, h, u, 1, nil, 2)
	b.has(t, "h3")

	u.live <- "l1"
	a.has(t, "h2", "h3", "l1")
	b.has(t, "h3", "l1")
	starts, _ := u.counts()
	assert.Equal(t, 1, starts, "two viewers, one upstream")

	a.cancel()
	require.ErrorIs(t, <-a.done, context.Canceled)
	u.live <- "l2"
	b.has(t, "h3", "l1", "l2")
	_, stopped := u.counts()
	assert.Zero(t, stopped, "the first leaving keeps the upstream")

	b.cancel()
	<-b.done
	require.Eventually(t, func() bool { _, s := u.counts(); return s == 1 }, time.Second, time.Millisecond, "the last leaving stops it")

	c := watch(t, h, u, 3, nil, 1)
	c.has(t, "h1", "h2", "h3")
	starts, _ = u.counts()
	assert.Equal(t, 2, starts, "a viewer after everyone left starts a new one")
	c.cancel()
}

// A late viewer gets its own tail from the shared lines, live ones
// included; one asking for more than they hold starts a fresh upstream,
// which later viewers share.
func TestLogHub_LateViewerGetsItsTailFromTheSharedLines(t *testing.T) {
	u := &upstream{history: []string{"h1", "h2", "h3", "h4"}, live: make(chan string)}
	h := &logHub{}
	a := watch(t, h, u, 2, nil, 1)
	a.has(t, "h3", "h4")
	u.live <- "l1"
	a.has(t, "h3", "h4", "l1")

	b := watch(t, h, u, 3, nil, 2)
	b.has(t, "h3", "h4", "l1")
	starts, _ := u.counts()
	assert.Equal(t, 1, starts, "the shared lines cover a tail of 3")

	c := watch(t, h, u, 4, nil, 1)
	c.has(t, "h1", "h2", "h3", "h4")
	starts, _ = u.counts()
	assert.Equal(t, 2, starts, "a tail of 4 needs history the shared lines don't hold")
	d := watch(t, h, u, 4, nil, 2)
	d.has(t, "h1", "h2", "h3", "h4")
	starts, _ = u.counts()
	assert.Equal(t, 2, starts, "the fresh upstream is the one shared from then on")
	for _, v := range []*viewer{a, b, c, d} {
		v.cancel()
	}
	require.Eventually(t, func() bool { _, s := u.counts(); return s == 2 }, time.Second, time.Millisecond)
}

// A viewer that reads slower than the log is written is cut off; the
// others keep receiving.
func TestLogHub_SlowViewerIsCutOff(t *testing.T) {
	u := &upstream{live: make(chan string)}
	h := &logHub{}
	slow := make(chan struct{})
	s := watch(t, h, u, 1, slow, 1)
	fast := watch(t, h, u, 1, nil, 2)

	var want []string
	for i := range logKeep + logViewerBuffer + 2 {
		l := fmt.Sprint("l", i)
		u.live <- l
		want = append(want, l)
	}
	fast.has(t, want...)
	close(slow)
	select {
	case err := <-s.done:
		require.ErrorIs(t, err, ErrLogFellBehind)
	case <-time.After(time.Second):
		t.Fatal("the slow viewer was never cut off")
	}
	assert.Less(t, len(s.seen()), len(want))
	u.live <- "after"
	fast.has(t, append(want, "after")...)
	fast.cancel()
}

// The history a viewer asks for arrives at once, and never cuts it off,
// however long it takes to send.
func TestLogHub_AWholeTailFitsAViewersBuffer(t *testing.T) {
	history := make([]string, logKeep)
	for i := range history {
		history[i] = fmt.Sprint("h", i)
	}
	u := &upstream{history: history, live: make(chan string)}
	h := &logHub{}
	slow := make(chan struct{})
	v := watch(t, h, u, logKeep, slow, 1)
	require.Eventually(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		f := h.feeds["k"]
		return f != nil && len(f.lines) == logKeep
	}, time.Second, time.Millisecond)
	close(slow)
	v.has(t, history...)
	v.cancel()
}

// When the container's log ends, every viewer gets its last lines and how
// it ended.
func TestLogHub_UpstreamEndReachesEveryViewer(t *testing.T) {
	u := &upstream{history: []string{"h1"}, live: make(chan string)}
	h := &logHub{}
	a := watch(t, h, u, 1, nil, 1)
	b := watch(t, h, u, 1, nil, 2)
	u.live <- "last"
	close(u.live)
	for _, v := range []*viewer{a, b} {
		require.EqualError(t, <-v.done, "the container stopped")
		assert.Equal(t, []string{"h1", "last"}, v.seen())
	}
	assert.Zero(t, viewersOf(h, "k"))
}

// liveOrch is an orchestrator whose StreamLogs follows u, counting streams.
type liveOrch struct {
	fakeOrch
	u *upstream
}

func (o *liveOrch) StreamLogs(ctx context.Context, _ string, tail int, _ string, logFn func(string)) error {
	return o.u.start(ctx, tail, logFn)
}

type liveFactory struct{ orch *liveOrch }

func (f liveFactory) ForServer(*serverPkg.Server) (ContainerOrchestrator, func(), error) {
	return f.orch, func() {}, nil
}

// Two viewers of one service's container hold one stream; each still has
// its container checked, and its own timestamps choice.
func TestStreamLogs_ViewersShareOneStream(t *testing.T) {
	u := &upstream{history: []string{"2026-10-27T10:00:00.000000000Z one"}, live: make(chan string)}
	orch := &liveOrch{fakeOrch: fakeOrch{containers: []string{"paas-web-s1-blue"}}, u: u}
	m := NewServiceManager(&fakeRepo{svc: &Service{ID: "s1", Name: "web"}}, zap.NewNop())
	m.WithOrchestration(fakeServers{}, liveFactory{orch})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var mu sync.Mutex
	got := map[bool][]string{}
	for _, ts := range []bool{true, false} {
		go func() {
			_ = m.StreamLogs(ctx, "s1", LogStreamOptions{Tail: 10, Container: "paas-web-s1-blue", Timestamps: ts}, func(l string) {
				mu.Lock()
				got[ts] = append(got[ts], l)
				mu.Unlock()
			})
		}()
	}
	require.Eventually(t, func() bool { return viewersOf(&m.logs, "/s1/paas-web-s1-blue") == 2 }, time.Second, time.Millisecond)
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got[true]) == 1 && len(got[false]) == 1
	}, time.Second, time.Millisecond)
	// Counted once both have a line: the upstream starts in the hub's own
	// goroutine, so it may not have started when the viewers registered.
	starts, _ := u.counts()
	assert.Equal(t, 1, starts)
	mu.Lock()
	assert.Equal(t, "2026-10-27T10:00:00.000000000Z one", got[true][0])
	assert.Equal(t, "one", got[false][0])
	mu.Unlock()

	assert.ErrorIs(t, m.StreamLogs(ctx, "s1", LogStreamOptions{Tail: 10, Container: "paas-postgres"}, func(string) {}), ErrContainerNotInService,
		"a viewer of a shared log still has its container checked")
}
