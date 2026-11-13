package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"github.com/docker/docker/client"
	"golang.org/x/crypto/ssh"
)

// rollouts runs the rollouts the panel hands over (D-111), one at a time
// per service, and keeps each one's journal in rolloutDir: <id>.json, the
// request and how far it got, and <id>.log, its log lines. A rollout
// interrupted by a restart resumes from its journal when the agent starts.
type rollouts struct {
	dc      *client.Client
	routes  *routes
	metrics *metrics
	dns     bool // the DNS relay listens (D-115)

	mu     sync.Mutex
	live   map[string]*liveRollout // running or queued, by deployment ID
	queues map[string][]func()     // by service ID; the first one runs
	idle   chan struct{}           // closed while nothing runs

	resumed chan struct{} // closed once the interrupted rollouts are queued
}

// liveRollout tells attached panels about a running rollout's progress.
type liveRollout struct {
	mu      sync.Mutex
	changed chan struct{} // closed and replaced on each line and at the end
	done    bool
}

// record is <id>.json. Request is dropped once the rollout ends (it holds
// the service's environment); registry credentials are never written.
type record struct {
	ID        string
	ServiceID string
	Created   time.Time
	Request   *rollout.Request `json:",omitempty"`
	Progress  rollout.Progress
	// PID is a compose deploy's docker compose up (composeUp).
	PID int `json:",omitempty"`
	// Deadline is when the deploy's time limit ends the rollout (D-125):
	// Request.TimeLeft from when it arrived, kept so a resumed rollout
	// keeps it. Zero for no limit.
	Deadline time.Time `json:",omitempty"`
}

// keepRollouts is how long an ended rollout's journal stays for the panel.
const keepRollouts = 7 * 24 * time.Hour

// maxRequest bounds a rollout request (its environment is in it).
const maxRequest = 8 << 20

var rolloutIDRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

func rolloutDir() string { return filepath.Join(stateDir, "rollouts") }

// caps is what the agent announces to the panel: nothing without Docker.
func (m *rollouts) caps() string {
	if m == nil {
		return ""
	}
	caps := agentproto.CapRollout + " " + agentproto.CapDesired + " " + agentproto.CapRoutes + " " + agentproto.CapMetrics + " " + agentproto.CapDeadline + " " + agentproto.CapContainerCheck
	if m.dns {
		caps += " " + agentproto.CapDNS
	}
	return caps
}

func newRollouts() (*rollouts, error) {
	dc, err := client.NewClientWithOpts(client.WithHost("unix://"+dockerSock), client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("Docker client: %w", err)
	}
	m := &rollouts{dc: dc, live: map[string]*liveRollout{}, queues: map[string][]func(){}, idle: make(chan struct{}), resumed: make(chan struct{})}
	close(m.idle)
	m.routes = newRoutes(dc, m.busy, activeDeployments)
	m.metrics = newMetrics()
	return m, nil
}

// resume queues the rollouts a restart interrupted, oldest first, once
// Docker answers (after a reboot the agent can start before it), and
// removes the journals of those that ended long ago.
func (m *rollouts) resume(ctx context.Context) {
	defer close(m.resumed)
	recs := readRecords()
	slices.SortFunc(recs, func(a, b record) int { return a.Created.Compare(b.Created) })
	pending := recs[:0]
	for _, rec := range recs {
		if rec.Progress.Result != nil || rec.Request == nil {
			if info, err := os.Stat(recordFile(rec.ID)); err == nil && time.Since(info.ModTime()) > keepRollouts {
				_ = os.Remove(logFile(rec.ID)) // the log first: a log without its record reads as unknown
				_ = os.Remove(recordFile(rec.ID))
			}
			continue
		}
		pending = append(pending, rec)
	}
	if len(pending) == 0 {
		return
	}
	for wait := time.Second; ; wait = min(2*wait, 30*time.Second) {
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := m.dc.Ping(pctx)
		cancel()
		if err == nil {
			break
		}
		slog.Warn("waiting for Docker before resuming the interrupted rollouts", "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
	for _, rec := range pending {
		slog.Info("resuming a rollout the restart interrupted", "deployment_id", rec.ID, "step", rec.Progress.Step)
		m.mu.Lock()
		m.enqueueLocked(rec.ID, rec.ServiceID, *rec.Request, rec.Progress)
		m.mu.Unlock()
	}
}

// start records req and queues it, unless its deployment is already known:
// a resent request runs once.
func (m *rollouts) start(req rollout.Request) error {
	id := req.ID()
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, running := m.live[id]; running {
		return nil
	}
	if _, err := os.Stat(recordFile(id)); err == nil {
		return nil
	}
	stored := req
	stored.Spec.Auth = nil
	if req.Compose != nil {
		c := *req.Compose
		c.Auth = nil
		stored.Compose = &c
	}
	rec := record{ID: id, ServiceID: req.ServiceID(), Created: time.Now().UTC(), Request: &stored}
	if req.TimeLeft > 0 {
		rec.Deadline = rec.Created.Add(req.TimeLeft)
	}
	if err := writeRecord(rec); err != nil {
		return fmt.Errorf("record the rollout: %w", err)
	}
	appendLogLine(id, "→ The server's agent runs this rollout; it goes on if the panel restarts")
	m.enqueueLocked(id, rec.ServiceID, req, rollout.Progress{})
	return nil
}

// enqueueLocked runs the rollout after the service's earlier ones. m.mu is
// held.
func (m *rollouts) enqueueLocked(id, serviceID string, req rollout.Request, p rollout.Progress) {
	if len(m.live) == 0 {
		m.idle = make(chan struct{})
	}
	m.live[id] = &liveRollout{changed: make(chan struct{})}
	m.queues[serviceID] = append(m.queues[serviceID], func() { m.run(id, req, p) })
	if len(m.queues[serviceID]) == 1 {
		go m.drain(serviceID)
	}
}

// drain runs the service's queued rollouts in order.
func (m *rollouts) drain(serviceID string) {
	for {
		m.mu.Lock()
		next := m.queues[serviceID][0]
		m.mu.Unlock()
		next()
		m.mu.Lock()
		m.queues[serviceID] = m.queues[serviceID][1:]
		if len(m.queues[serviceID]) == 0 {
			delete(m.queues, serviceID)
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
	}
}

// run runs one rollout to its end on a ctx that only the deploy's time
// limit cancels (D-125): stopping the agent leaves it to resume.
func (m *rollouts) run(id string, req rollout.Request, p rollout.Progress) {
	rec, err := readRecord(id)
	if err != nil {
		slog.Error("could not read the rollout's record", "deployment_id", id, "error", err)
	}
	rec.ID, rec.ServiceID = id, req.ServiceID()
	ctx := context.Background()
	if !rec.Deadline.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, rec.Deadline)
		defer cancel()
	}
	var res rollout.Result
	if req.Compose != nil {
		res = timedOut(ctx, m.composeUp(ctx, id, *req.Compose, &rec))
		rec.Progress.Result, rec.Request = &res, nil
		if err := writeRecord(rec); err != nil {
			slog.Error("could not record the rollout's result", "deployment_id", id, "error", err)
		}
	} else {
		res = m.rollout(ctx, id, req, p, &rec)
	}
	slog.Info("rollout ended", "deployment_id", id, "failed_step", res.Step)
	m.notify(id, true)
	m.mu.Lock()
	delete(m.live, id)
	if len(m.live) == 0 {
		close(m.idle)
	}
	m.mu.Unlock()
	// The routes skip a service while its rollout runs: a stack's are
	// written now, and a failed switch's healed.
	m.routes.soon()
}

// timedOut words a rollout that failed at the deploy's time limit as such.
// The panel says it again with the limit's minutes; this is what it reads
// when it restarted meanwhile.
func timedOut(ctx context.Context, res rollout.Result) rollout.Result {
	if res.Step != "" && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		res.Err = "timed out at the deploy time limit (at the " + res.Step + " step): " + res.Err
	}
	return res
}

// rollout runs a Spec's rollout from p, saving its progress in rec.
func (m *rollouts) rollout(ctx context.Context, id string, req rollout.Request, p rollout.Progress, rec *record) rollout.Result {
	host := rollout.DockerHost{Client: m.dc, Net: req.Net, Warn: func(msg string) { slog.Warn(msg, "deployment_id", id) }}
	hooks := rollout.Hooks{
		Log: func(line string) {
			appendLogLine(id, line)
			m.notify(id, false)
		},
		Save: func(p rollout.Progress) {
			if p.Result != nil {
				r := timedOut(ctx, *p.Result)
				p.Result, rec.Request = &r, nil
			}
			rec.Progress = p
			if err := writeRecord(*rec); err != nil {
				slog.Error("could not record the rollout's progress", "deployment_id", id, "error", err)
			}
		},
	}
	// Where Traefik reads the agent's files, the agent switches the route
	// (D-113); where it reads labels, starting the slot did.
	if api := m.routes.liveAPI(context.Background()); api != "" {
		hooks.Route = func(ctx context.Context, newSlot string, logFn func(string)) error {
			return m.routes.route(ctx, api, req.ServiceID(), newSlot, logFn)
		}
	}
	return rollout.Resume(ctx, host, req.Spec, p, hooks)
}

func (m *rollouts) notify(id string, done bool) {
	m.mu.Lock()
	l := m.live[id]
	m.mu.Unlock()
	if l == nil {
		return
	}
	l.mu.Lock()
	l.done = l.done || done
	close(l.changed)
	l.changed = make(chan struct{})
	l.mu.Unlock()
}

// whenIdle returns a channel closed once no rollout runs.
func (m *rollouts) whenIdle() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.idle
}

// attach writes the rollout's log from line offset on, as Event lines, then
// its result. It follows a running rollout until it ends or the panel goes.
func (m *rollouts) attach(ch ssh.Channel, id string, offset int) error {
	f, err := os.Open(logFile(id))
	if err != nil {
		return errors.New(rollout.UnknownRolloutPrefix + id)
	}
	defer f.Close()
	enc := json.NewEncoder(ch)
	r := bufio.NewReaderSize(f, 64<<10)
	line := 0
	var partial string
	for {
		// The channel to wait on is taken before reading, so a line written
		// after the read is not missed.
		m.mu.Lock()
		l := m.live[id]
		m.mu.Unlock()
		var changed chan struct{}
		done := l == nil
		if l != nil {
			l.mu.Lock()
			changed, done = l.changed, l.done
			l.mu.Unlock()
		}
		for {
			s, err := r.ReadString('\n')
			if err != nil {
				partial += s // a line still being written
				break
			}
			s, partial = partial+s, ""
			if line >= offset {
				if err := enc.Encode(rollout.Event{Log: strings.TrimSuffix(s, "\n")}); err != nil {
					return nil // the panel went away
				}
			}
			line++
		}
		if done {
			rec, err := readRecord(id)
			if err != nil || rec.Progress.Result == nil {
				select {
				case <-m.resumed:
					// Not queued by resume either: nothing will finish it.
					return errors.New(rollout.UnknownRolloutPrefix + id)
				default:
					return errors.New("the rollout ended without a result: the agent restarted, and resumes it")
				}
			}
			_ = enc.Encode(rollout.Event{Result: rec.Progress.Result})
			return nil
		}
		<-changed
	}
}

// list is every rollout the agent holds.
func list() []rollout.Summary {
	recs := readRecords()
	out := make([]rollout.Summary, 0, len(recs))
	for _, rec := range recs {
		out = append(out, rollout.Summary{ID: rec.ID, ServiceID: rec.ServiceID, Step: rec.Progress.Step, Result: rec.Progress.Result})
	}
	return out
}

// readRequest reads one JSON line of at most maxRequest bytes into v.
func readRequest(r io.Reader, v any) error {
	line, err := bufio.NewReaderSize(io.LimitReader(r, maxRequest), 64<<10).ReadBytes('\n')
	if err != nil && (err != io.EOF || len(line) == 0) {
		return fmt.Errorf("read the request: %w", err)
	}
	if err := json.Unmarshal(line, v); err != nil {
		return fmt.Errorf("read the request: %w", err)
	}
	return nil
}

func checkRolloutID(id string) error {
	if !rolloutIDRe.MatchString(id) {
		return fmt.Errorf("%q is not a deployment ID", id)
	}
	return nil
}

func recordFile(id string) string { return filepath.Join(rolloutDir(), id+".json") }
func logFile(id string) string    { return filepath.Join(rolloutDir(), id+".log") }

func writeRecord(rec record) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode the record: %w", err)
	}
	if err := writeFileAtomic(recordFile(rec.ID), raw, 0o600); err != nil {
		return fmt.Errorf("write the record: %w", err)
	}
	return nil
}

func readRecord(id string) (record, error) {
	var rec record
	raw, err := os.ReadFile(recordFile(id))
	if err != nil {
		return rec, err
	}
	return rec, json.Unmarshal(raw, &rec)
}

func readRecords() []record {
	paths, _ := filepath.Glob(filepath.Join(rolloutDir(), "*.json"))
	recs := make([]record, 0, len(paths))
	for _, p := range paths {
		if rec, err := readRecord(strings.TrimSuffix(filepath.Base(p), ".json")); err == nil {
			recs = append(recs, rec)
		}
	}
	return recs
}

// appendLogLine adds one line to the rollout's log. A line break inside it
// would split it in two for an attach that counts lines.
func appendLogLine(id, line string) {
	f, err := os.OpenFile(logFile(id), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		slog.Error("could not write the rollout's log", "deployment_id", id, "error", err)
		return
	}
	_, _ = f.WriteString(strings.ReplaceAll(line, "\n", " ") + "\n")
	_ = f.Close()
}
