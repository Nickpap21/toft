package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// Container logs (D-113): on an agent server the platform's containers log
// to Docker's local driver, capped per container (rollout.LocalLogConfig),
// and the agent ships each one's lines to its service's Loki (the desired
// state's DesiredService.Loki) with the Loki plugin's labels,
// container_name and service_id. Docker's files are the spool: while Loki
// or the agent is away the lines wait there, and a cursor per container in
// logsDir says where shipping resumes. Lines that rotate away first are
// lost; each such gap is logged and counted in the cursor.

const (
	// shipPoll is how often the containers are compared with the followers.
	shipPoll = 5 * time.Second
	// A batch is pushed at batchLines lines, batchBytes bytes, or
	// batchWait after its first line.
	batchLines = 500
	batchBytes = 1 << 20
	batchWait  = time.Second
	// lokiMaxAge is how old a line Loki still takes: its
	// reject_old_samples_max_age, which the catalog's Loki leaves at Loki's
	// default, 1w. Shipping starts no earlier, so a first start doesn't
	// send Docker's whole history only for Loki to refuse it.
	lokiMaxAge = 7 * 24 * time.Hour
)

func logsDir() string             { return filepath.Join(stateDir, "logs") }
func cursorFile(id string) string { return filepath.Join(logsDir(), id+".json") }

// cursor is logs/<container ID>.json: the time of the last line shipped,
// and how many lines of that time were (Docker's since keeps the lines of
// that very time). Seen 0 ships every line from Time on. Gaps counts the
// times lines rotated away before they were shipped.
type cursor struct {
	Time time.Time `json:"time"`
	Seen int       `json:"seen"`
	Gaps int       `json:"gaps,omitempty"`
}

func readCursor(id string) cursor {
	var c cursor
	if raw, err := os.ReadFile(cursorFile(id)); err == nil {
		_ = json.Unmarshal(raw, &c)
	}
	return c
}

func writeCursor(id string, c cursor) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(cursorFile(id), raw, 0o600); err != nil {
		return fmt.Errorf("record the log cursor: %w", err)
	}
	return nil
}

// shipTarget is where one container's lines go.
type shipTarget struct{ name, serviceID, loki string }

// followEnd is how a follower ended.
type followEnd int

const (
	endStopped followEnd = iota // stopped, or it couldn't start: the next sync decides
	endDrained                  // its stream ended: the container stopped, all shipped
	endUnread                   // its log driver isn't read here (the Loki plugin's, none)
)

// shipper follows each container to ship, one goroutine each (reading
// Docker's stream blocks), and pushes its lines to Loki.
type shipper struct {
	dc       *client.Client
	services func() []agentproto.DesiredService
	http     *http.Client
	poll     time.Duration
	wait     time.Duration
	retry    func(n int) time.Duration // the wait before the nth attempt in a row

	mu        sync.Mutex
	followers map[string]*follower // by container ID
	drained   map[string]bool      // stopped and shipped: followed again once it runs
	unread    map[string]bool
	wg        sync.WaitGroup
}

type follower struct {
	target shipTarget
	stop   context.CancelFunc
}

func newShipper(dc *client.Client) *shipper {
	return &shipper{
		dc:       dc,
		services: func() []agentproto.DesiredService { return heldDesired().Services },
		http:     &http.Client{Timeout: 30 * time.Second},
		poll:     shipPoll, wait: batchWait, retry: backoff,
		followers: map[string]*follower{}, drained: map[string]bool{}, unread: map[string]bool{},
	}
}

// watch ships until ctx ends, then waits for its followers.
func (s *shipper) watch(ctx context.Context) {
	defer s.wg.Wait()
	t := time.NewTicker(s.poll)
	defer t.Stop()
	for {
		if err := s.sync(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("could not compare the containers with the logs the agent ships", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// sync starts a follower for each platform container of a service with a
// Loki that has none, stops those whose container went or whose service
// ships elsewhere or no more (a new one starts once the old one ended),
// and drops the cursors of containers that are gone.
func (s *shipper) sync(ctx context.Context) error {
	lokis := map[string]string{}   // service ID → its Loki
	compose := map[string]string{} // a compose service's ID's first 8 characters → its ID
	for _, svc := range s.services() {
		if svc.Loki == "" {
			continue
		}
		lokis[svc.ID] = strings.TrimRight(svc.Loki, "/")
		if svc.Compose && len(svc.ID) >= 8 {
			compose[svc.ID[:8]] = svc.ID
		}
	}
	list, err := s.dc.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return fmt.Errorf("list the containers: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	present := make(map[string]bool, len(list))
	want := map[string]shipTarget{}
	for _, c := range list {
		present[c.ID] = true
		name := rollout.SummaryName(c)
		id := ""
		if rollout.IsPlatformSlot(c.Labels, name) {
			id = c.Labels[rollout.LabelServiceID]
		} else if m := composeProjectRe.FindStringSubmatch(c.Labels[rollout.LabelComposeProject]); m != nil {
			id = compose[m[1]]
		}
		if lokis[id] == "" || s.unread[c.ID] {
			continue
		}
		want[c.ID] = shipTarget{name: name, serviceID: id, loki: lokis[id]}
		if c.State == container.StateRunning {
			delete(s.drained, c.ID)
		}
	}
	for id, f := range s.followers {
		if want[id] != f.target {
			f.stop()
		}
	}
	for id, t := range want {
		if s.followers[id] != nil || s.drained[id] {
			continue
		}
		fctx, stop := context.WithCancel(ctx)
		s.followers[id] = &follower{target: t, stop: stop}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer stop()
			end := s.follow(fctx, id, t)
			s.mu.Lock()
			defer s.mu.Unlock()
			delete(s.followers, id) // this one: a follower is replaced only once it ended
			switch end {
			case endDrained:
				s.drained[id] = true
			case endUnread:
				s.unread[id] = true
			}
		}()
	}
	for id := range s.drained {
		if !present[id] {
			delete(s.drained, id)
		}
	}
	for id := range s.unread {
		if !present[id] {
			delete(s.unread, id)
		}
	}
	entries, _ := os.ReadDir(logsDir())
	for _, e := range entries {
		if id, ok := strings.CutSuffix(e.Name(), ".json"); ok && !present[id] {
			_ = os.Remove(filepath.Join(logsDir(), e.Name()))
		}
	}
	return nil
}

// follow ships one container's lines until ctx ends or its log stream
// does. A failure, Loki's or Docker's, is tried again from the cursor
// after a backoff; meanwhile the lines wait in Docker's files.
func (s *shipper) follow(ctx context.Context, id string, t shipTarget) followEnd {
	info, err := s.dc.ContainerInspect(ctx, id)
	if err != nil {
		return endStopped
	}
	if info.HostConfig != nil {
		if drv := info.HostConfig.LogConfig.Type; strings.HasPrefix(drv, "loki") || drv == "none" {
			return endUnread // the plugin ships these itself
		}
	}
	tty := info.Config != nil && info.Config.Tty
	for failures := 0; ; {
		shipped, err := s.ship(ctx, id, t, tty)
		if err == nil {
			return endDrained
		}
		if ctx.Err() != nil {
			return endStopped
		}
		if shipped {
			failures = 0
		}
		failures++
		wait := s.retry(failures)
		slog.Warn("could not ship a container's logs; trying again", "container", t.name, "loki", t.loki,
			"error", err, "retry_in", wait.Round(time.Millisecond))
		select {
		case <-ctx.Done():
			return endStopped
		case <-time.After(wait):
		}
	}
}

// ship reads the container's lines from its cursor on, following, and
// pushes them in batches, moving the cursor after each push. It returns
// nil once the stream ended and everything read was pushed, and whether it
// pushed anything.
func (s *shipper) ship(ctx context.Context, id string, t shipTarget, tty bool) (bool, error) {
	cur := readCursor(id)
	if oldest := time.Now().Add(-lokiMaxAge); cur.Time.Before(oldest) {
		cur = cursor{Time: oldest, Gaps: cur.Gaps} // none yet, or older than Loki takes
	}
	opts := container.LogsOptions{ShowStdout: true, ShowStderr: true, Follow: true, Timestamps: true,
		Since: cur.Time.Format(time.RFC3339Nano)}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel() // ends the stream, and with it the reader
	rc, err := s.dc.ContainerLogs(ctx, id, opts)
	if err != nil {
		return false, fmt.Errorf("read the logs: %w", err)
	}
	defer rc.Close()
	lines := make(chan string)
	readErr := make(chan error, 1)
	go func() { readErr <- readLogLines(ctx, rc, tty, lines) }()

	shipped, first, skip := false, true, cur.Seen
	next := cur // the cursor once batch is pushed
	batch := make([][2]string, 0, batchLines)
	size := 0
	timer := time.NewTimer(s.wait)
	timer.Stop()
	defer timer.Stop()
	flush := func() error {
		timer.Stop()
		if len(batch) == 0 {
			return nil
		}
		if err := s.push(ctx, t, batch); err != nil {
			return err
		}
		if err := writeCursor(id, next); err != nil {
			return err
		}
		shipped, batch, size = true, batch[:0], 0
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return shipped, ctx.Err()
		case <-timer.C:
			if err := flush(); err != nil {
				return shipped, err
			}
		case line, ok := <-lines:
			if !ok {
				if err := flush(); err != nil {
					return shipped, err
				}
				return shipped, <-readErr
			}
			ts, text, _ := strings.Cut(line, " ")
			at, err := time.Parse(time.RFC3339Nano, ts)
			if err != nil {
				continue // Docker leads every line with its time
			}
			// Since keeps the cursor's own line while Docker has it: its
			// absence means the lines after it may have rotated away too.
			if first && cur.Seen > 0 && at.After(cur.Time) {
				slog.Warn("log lines rotated out of Docker's files before they were shipped",
					"container", t.name, "after", cur.Time, "before", at, "gaps", cur.Gaps+1)
				cur = cursor{Time: at, Gaps: cur.Gaps + 1}
				next, skip = cur, 0
				if err := writeCursor(id, cur); err != nil {
					return shipped, err
				}
			}
			first = false
			if at.Before(cur.Time) {
				continue
			}
			if at.Equal(cur.Time) && skip > 0 {
				skip--
				continue // shipped before
			}
			if at.Equal(next.Time) {
				next.Seen++
			} else {
				next.Time, next.Seen = at, 1
			}
			batch = append(batch, [2]string{strconv.FormatInt(at.UnixNano(), 10), text})
			size += len(text)
			if len(batch) == 1 {
				timer.Reset(s.wait)
			}
			if len(batch) >= batchLines || size >= batchBytes {
				if err := flush(); err != nil {
					return shipped, err
				}
			}
		}
	}
}

// readLogLines sends each line of a log stream on lines, demultiplexed
// unless the container has a TTY, until the stream or ctx ends, then
// closes lines.
func readLogLines(ctx context.Context, rc io.Reader, tty bool, lines chan<- string) error {
	defer close(lines)
	src := rc
	if !tty {
		pr, pw := io.Pipe()
		defer pr.Close() // ends StdCopy when this stops first
		go func() {
			_, err := stdcopy.StdCopy(pw, pw, rc)
			pw.CloseWithError(err)
		}()
		src = pr
	}
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		select {
		case lines <- sc.Text():
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return sc.Err()
}

// lokiPush is the body of Loki's push API.
type lokiPush struct {
	Streams []lokiStream `json:"streams"`
}

type lokiStream struct {
	Stream map[string]string `json:"stream"`
	Values [][2]string       `json:"values"` // [unix nanoseconds, line]
}

// push sends one batch to Loki. A batch Loki refuses as such (400, as for
// lines older than it keeps, or 413) is dropped with a warning: sending it
// again would never succeed. Any other failure is returned.
func (s *shipper) push(ctx context.Context, t shipTarget, values [][2]string) error {
	body, err := json.Marshal(lokiPush{Streams: []lokiStream{{
		Stream: map[string]string{"container_name": t.name, "service_id": t.serviceID},
		Values: values,
	}}})
	if err != nil {
		return fmt.Errorf("encode the batch: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.loki+"/loki/api/v1/push", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("Loki's address: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("push to Loki: %w", err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch {
	case resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusBadRequest, resp.StatusCode == http.StatusRequestEntityTooLarge:
		slog.Warn("Loki refused a batch of log lines; they are dropped", "container", t.name,
			"status", resp.Status, "answer", strings.TrimSpace(string(answer)), "lines", len(values))
		return nil
	}
	return fmt.Errorf("Loki answered %s: %s", resp.Status, strings.TrimSpace(string(answer)))
}
