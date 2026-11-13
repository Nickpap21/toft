package service

import (
	"context"
	"errors"
	"sync"
)

// logViewerBuffer is how many lines a live log viewer may be behind before
// it is cut off, so a slow one never holds up the others (K11), on top of
// the most history a viewer asks for (logKeep): an upstream sends its
// history at once.
const logViewerBuffer = 256

// logKeep is how many of a shared log's lines are kept for viewers who join
// later: the most a viewer asks for (the handler's cap).
const logKeep = 1000

// ErrLogFellBehind ends a viewer's live log when it read slower than the
// container wrote.
var ErrLogFellBehind = errors.New("fell behind the log; reconnect")

// What a live log's error event says when the page should tell the operator
// more than that the read failed: the code before the colon is the contract,
// with no server's address in it (D-39).
var (
	// ErrLogNoContainer: the service has no container, running or stopped.
	ErrLogNoContainer = errors.New("no_container: this service has no container to follow")
	// ErrLogServerUnreachable: the service's server can't be connected to.
	ErrLogServerUnreachable = errors.New("server_unreachable: this service's server can't be reached")
)

// logHub shares one upstream live log per server, service and container
// among its viewers, so ten open tabs hold one SSH session, not ten (K11,
// D-125). Each viewer is authorized before it joins.
//
// ponytail: one lock for every shared log, per-feed locks if the panel
// ever follows enough logs at once for it to show.
type logHub struct {
	mu    sync.Mutex
	feeds map[string]*logFeed
}

// logFeed is one upstream stream and who follows it.
type logFeed struct {
	key     string
	tail    int      // the upstream's own tail: the first viewer's
	lines   []string // the last logKeep lines, oldest first
	viewers map[*logViewer]struct{}
	cancel  context.CancelFunc
	ended   bool
}

type logViewer struct {
	lines chan string
	end   chan error // how the log ended for this viewer; sent once
}

// follow sends logFn the last tail lines of the log at key, then its live
// lines, until ctx ends or the log does. The first viewer starts the
// upstream with start at its own tail, so its lines are exactly today's;
// a viewer who asks for more history than the shared log holds starts a
// fresh one, which later viewers share. The upstream stops when its last
// viewer leaves.
func (h *logHub) follow(ctx context.Context, key string, tail int, start func(ctx context.Context, tail int, logFn func(string)) error, logFn func(string)) error {
	h.mu.Lock()
	f := h.feeds[key]
	if f == nil || (tail > f.tail && len(f.lines) < tail) {
		f = h.startLocked(key, tail, start)
	}
	v := &logViewer{lines: make(chan string, logKeep+logViewerBuffer), end: make(chan error, 1)}
	backlog := append([]string(nil), f.lines[max(0, len(f.lines)-tail):]...)
	f.viewers[v] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.dropLocked(f, v)
		h.mu.Unlock()
	}()

	for _, line := range backlog {
		logFn(line)
	}
	for {
		select {
		case line := <-v.lines:
			logFn(line)
		case err := <-v.end:
			for {
				select {
				case line := <-v.lines:
					logFn(line)
				default:
					return err
				}
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// startLocked starts an upstream for key, which later viewers join. h.mu
// is held.
func (h *logHub) startLocked(key string, tail int, start func(ctx context.Context, tail int, logFn func(string)) error) *logFeed {
	if h.feeds == nil {
		h.feeds = map[string]*logFeed{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &logFeed{key: key, tail: tail, viewers: map[*logViewer]struct{}{}, cancel: cancel}
	h.feeds[key] = f
	go func() {
		defer cancel()
		err := start(ctx, tail, func(line string) { h.publish(f, line) })
		h.mu.Lock()
		defer h.mu.Unlock()
		f.ended = true
		if h.feeds[key] == f {
			delete(h.feeds, key)
		}
		for v := range f.viewers {
			v.end <- err
		}
		clear(f.viewers)
	}()
	return f
}

// publish hands a line to every viewer of f, and cuts off one whose buffer
// is full.
func (h *logHub) publish(f *logFeed, line string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f.lines = append(f.lines, line)
	if len(f.lines) > logKeep {
		f.lines = f.lines[len(f.lines)-logKeep:]
	}
	for v := range f.viewers {
		select {
		case v.lines <- line:
		default:
			v.end <- ErrLogFellBehind
			h.dropLocked(f, v)
		}
	}
}

// dropLocked removes v from f, and stops f's upstream once nobody follows
// it. h.mu is held.
func (h *logHub) dropLocked(f *logFeed, v *logViewer) {
	if _, ok := f.viewers[v]; !ok {
		return
	}
	delete(f.viewers, v)
	if len(f.viewers) == 0 && !f.ended {
		f.cancel()
		if h.feeds[f.key] == f {
			delete(h.feeds, f.key)
		}
	}
}
