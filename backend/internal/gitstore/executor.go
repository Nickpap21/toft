package gitstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Executor serves git to the backend over the socket (cmd/toft-git). Its
// API is internal: it ships in the same image as its only caller.
//
//	GET    /v1/health
//	POST   /v1/repos/{id}/init                 an empty bare repository; idempotent
//	DELETE /v1/repos/{id}                      the repository and everything in it
//	POST   /v1/repos/{id}/{service}?mode=…     upload-pack or receive-pack:
//	       advertise (HTTP info/refs), stateless (HTTP), stream (SSH)
//	GET    /v1/repos/{id}/browse/{op}?…        the browser's reads, as JSON (browse.go)
//	GET    /v1/repos/{id}/raw?ref=…&path=…     a file's bytes
//	GET    /v1/repos/{id}/archive?ref=…        a commit's tree as a tar, for a deploy (archive.go)
//	POST   /v1/repos/{id}/merge|head|pull-ref  pull requests' writes (pulls.go)
//	POST   /v1/repos/{id}/create-branch        a new branch, never moving one (D-102)
//	POST   /v1/repos/{id}/remote/ls|fetch|push import and mirrors (remote.go)
//
// A push (receive-pack, not advertise) runs with HooksDir's hooks: the
// request's Toft-Protect-Default: 1 makes pre-receive refuse updates to the
// branch Toft-Protect-Branch names (query-escaped; required with it), never
// HEAD's, Toft-Read-Only: <url> every update (a pull mirror's
// repository, D-79), and the refs it moved come back in Toft-Git-Updated.
// A service call streams the request body to git's stdin and git's stdout
// back as the response, both at once (the SSH protocol is interactive).
// The response's trailers carry git's exit code (Toft-Git-Exit) and the
// start of its stderr, base64 (Toft-Git-Stderr). Git-Protocol is passed on
// as GIT_PROTOCOL. Toft-User and Toft-Workspace name who a service call is
// for, and count it against their limits (acquire); a refusal is a 503
// whose text/plain body says which limit.
type Executor struct {
	root   string
	hooks  string // core.hooksPath for pushes
	egress string // the backend's egress proxy, for import and mirrors
	// ops caps git programs running at once; held counts them per
	// repository, account and workspace (acquire).
	ops  chan struct{}
	mu   sync.Mutex
	held map[string]int
	// wait is how long a call waits for a free slot before answering 503;
	// maxTime and idle end a call that runs too long or stops moving;
	// sessionIdle and pushIdle end a client's git session that stops moving;
	// negotiate ends one whose pack hasn't begun by then (pktWatch).
	wait, maxTime, idle, sessionIdle, pushIdle, negotiate time.Duration
}

// Limits on git programs. Initial values, not measured: the container's own
// CPU and memory limits bound them together.
const (
	maxOps     = 16
	maxRepoOps = 6
	// A client's session (SSH, HTTP, an agent's push credential) names its
	// account and the repository's workspace in Toft-User and
	// Toft-Workspace (Client.Serve), so neither holds every slot. The
	// backend's own calls (deploy archives, mirrors, the panel's reads and
	// pull requests' writes) name neither: only the two limits above count
	// them. Two accounts together hold 8 of the clients' 12, so a third
	// still finds a slot.
	maxUserOps      = 4
	maxWorkspaceOps = 8
	// Clients together hold at most this many, so deploys and mirrors
	// always find a slot however many accounts are cloning.
	maxClientOps = maxOps - 4
)

// NewExecutor serves the repositories under root.
func NewExecutor(root string) *Executor {
	return &Executor{
		root:    root,
		hooks:   HooksDir,
		egress:  EgressSocket,
		ops:     make(chan struct{}, maxOps),
		held:    map[string]int{},
		wait:    30 * time.Second,
		maxTime: 2 * time.Hour,
		idle:    10 * time.Minute,
		// Initial values, not measured. git sends a keepalive every 5 s
		// while it packs, so a client that sends nothing for sessionIdle
		// has stopped; a push's client compresses its pack, silently,
		// before it sends the first byte of it. A real negotiation takes
		// seconds, even on a big repository over a slow link; one that
		// trickles is cut off at negotiate, so pushIdle need not be short.
		sessionIdle: time.Minute,
		pushIdle:    10 * time.Minute,
		negotiate:   2 * time.Minute,
	}
}

// WithSessionIdle ends a client's session that stops moving after d
// instead of a minute (tests).
func (e *Executor) WithSessionIdle(d time.Duration) *Executor {
	e.sessionIdle = d
	return e
}

// WithHooks runs pushes with the hooks in dir instead of HooksDir (tests).
func (e *Executor) WithHooks(dir string) *Executor {
	e.hooks = dir
	return e
}

// WithEgress reaches other hosts through the proxy on socket instead of
// EgressSocket (tests).
func (e *Executor) WithEgress(socket string) *Executor {
	e.egress = socket
	return e
}

func (e *Executor) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("POST /v1/repos/{id}/init", e.handleInit)
	mux.HandleFunc("DELETE /v1/repos/{id}", e.handleDelete)
	mux.HandleFunc("POST /v1/repos/{id}/{service}", e.handleService)
	mux.HandleFunc("GET /v1/repos/{id}/browse/{op}", e.handleBrowse)
	mux.HandleFunc("GET /v1/repos/{id}/raw", e.handleRaw)
	mux.HandleFunc("GET /v1/repos/{id}/archive", e.handleArchive)
	mux.HandleFunc("POST /v1/repos/{id}/remote/{op}", e.handleRemote)
	for _, op := range []string{"merge", "head", "pull-ref", "create-branch"} {
		mux.HandleFunc("POST /v1/repos/{id}/"+op, e.handleWrite(op))
	}
	return mux
}

func (e *Executor) handleInit(w http.ResponseWriter, r *http.Request) {
	dir, err := RepoPath(e.root, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := e.init(r.Context(), dir); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// init makes dir an empty bare repository unless it is one already: no
// template (no sample hooks), main as the first branch.
func (e *Executor) init(ctx context.Context, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd := command(ctx, "", "init", "--bare", "--quiet", "--template=", "--initial-branch=main", "--end-of-options", dir)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return errors.New("git init: " + err.Error() + ": " + stderr.String())
	}
	return nil
}

func (e *Executor) handleDelete(w http.ResponseWriter, r *http.Request) {
	dir, err := RepoPath(e.root, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (e *Executor) handleService(w http.ResponseWriter, r *http.Request) {
	id, service, mode := r.PathValue("id"), r.PathValue("service"), r.URL.Query().Get("mode")
	dir, err := RepoPath(e.root, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	args := []string{service}
	switch {
	case !Services[service]:
		http.Error(w, "unknown service", http.StatusNotFound)
		return
	case mode == "advertise":
		args = append(args, "--stateless-rpc", "--http-backend-info-refs")
	case mode == "stateless":
		args = append(args, "--stateless-rpc")
	case mode != "stream":
		http.Error(w, "unknown mode", http.StatusBadRequest)
		return
	}
	args = append(args, "--end-of-options", dir)
	// A push runs Toft's hooks, and post-receive notes what it moved here.
	pushLog := ""
	var hookEnv []string
	if service == "receive-pack" && mode != "advertise" {
		if r.Header.Get("Toft-Protect-Default") == "1" {
			// Protection without the branch's name (the previous release's
			// backend, while an upgrade restarts the two) refuses the push.
			b, err := url.QueryUnescape(r.Header.Get("Toft-Protect-Branch"))
			if err != nil || b == "" {
				http.Error(w, "Toft-Protect-Default needs Toft-Protect-Branch", http.StatusBadRequest)
				return
			}
			hookEnv = append(hookEnv, envProtect+"="+b)
		}
		// A limit the hook can't be given refuses the push: dropping it
		// would let the push through (an agent's branch, a pull mirror).
		for _, h := range [][2]string{{"Toft-Read-Only", envReadOnly}, {"Toft-Push-Only", envPushOnly}} {
			if v := r.Header.Get(h[0]); v != "" {
				if !printable(v) {
					http.Error(w, h[0]+" must be printable ASCII", http.StatusBadRequest)
					return
				}
				hookEnv = append(hookEnv, h[1]+"="+v)
			}
		}
		f, err := os.CreateTemp("", "push-*.log")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		f.Close()
		pushLog = f.Name()
		defer os.Remove(pushLog)
		args = append([]string{"-c", "core.hooksPath=" + e.hooks}, args...)
	}

	// A repository made before its first use (or whose init was missed)
	// is made now; init is idempotent.
	if err := e.init(r.Context(), dir); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	release, err := e.acquire(r.Context(), id, r.Header.Get("Toft-User"), r.Header.Get("Toft-Workspace"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer release()

	ctx, cancel := context.WithTimeout(r.Context(), e.maxTime)
	defer cancel()
	// A client that sends nothing, before or between git's steps, gives
	// its slot back.
	idleFor := e.sessionIdle
	if service == "receive-pack" && mode != "advertise" {
		idleFor = e.pushIdle
	}
	idle := time.AfterFunc(idleFor, cancel)
	defer idle.Stop()
	moved := func() { idle.Reset(idleFor) }
	// A client that keeps moving, but slowly, gives its slot back too if it
	// hasn't finished negotiating within e.negotiate: upload-pack has begun
	// its pack (the first side-band packet, a progress line or the 5 s
	// keepalive) or receive-pack has every command (their flush).
	negotiating := time.AfterFunc(e.negotiate, cancel)
	defer negotiating.Stop()
	var stdin io.Reader = r.Body
	var stdout io.Writer = w
	switch {
	case mode == "advertise":
	case service == "upload-pack":
		stdout = io.MultiWriter(w, &pktWatch{deadline: negotiating, begun: func(h []byte) bool { return len(h) == 5 && h[4] >= 1 && h[4] <= 3 }})
	default:
		stdin = io.TeeReader(r.Body, &pktWatch{deadline: negotiating, begun: func(h []byte) bool { return string(h) == "0000" }})
	}
	// git's stdin and stdout are the request and the response: once the
	// call ends, a read blocked on a client that sends nothing (or a write
	// on one that reads nothing) must end too, or Run waits on it and the
	// slot is never given back.
	rc := http.NewResponseController(w)
	stop := context.AfterFunc(ctx, func() {
		_ = rc.SetReadDeadline(time.Now())
		_ = rc.SetWriteDeadline(time.Now().Add(5 * time.Second))
	})
	defer stop()

	stderr := &capped{max: 4096}
	cmd := command(ctx, r.Header.Get("Git-Protocol"), args...)
	if pushLog != "" {
		cmd.Env = append(cmd.Env, envPushLog+"="+pushLog)
		cmd.Env = append(cmd.Env, hookEnv...)
	}
	cmd.Stdin = &activeReader{r: stdin, moved: moved}
	cmd.Stderr = stderr

	// Both directions at once: an SSH client answers what git says first.
	_ = http.NewResponseController(w).EnableFullDuplex()
	w.Header().Set("Trailer", "Toft-Git-Exit, Toft-Git-Stderr, Toft-Git-Updated")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_ = http.NewResponseController(w).Flush()
	cmd.Stdout = &flushWriter{w: stdout, rc: http.NewResponseController(w), moved: moved}

	code := 0
	if err := cmd.Run(); err != nil {
		code = -1
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() >= 0 {
			code = exit.ExitCode()
		}
		if ctx.Err() != nil {
			stderr.Write([]byte("\ntoft: the git operation ran too long or stopped moving"))
		}
	}
	if service == "receive-pack" && mode != "advertise" && code == 0 {
		hctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
		if err := fixHead(hctx, dir); err != nil {
			stderr.Write([]byte("\ntoft: point HEAD at a branch: " + err.Error()))
		}
		cancel()
	}
	if pushLog != "" {
		if moved := readPushLog(pushLog); len(moved) > 0 {
			b, _ := json.Marshal(moved)
			w.Header().Set("Toft-Git-Updated", base64.StdEncoding.EncodeToString(b))
		}
	}
	w.Header().Set("Toft-Git-Exit", strconv.Itoa(code))
	w.Header().Set("Toft-Git-Stderr", base64.StdEncoding.EncodeToString(stderr.Bytes()))
}

// printable: one line of printable ASCII, 2,048 bytes at most.
func printable(s string) bool {
	if len(s) > 2048 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// Refusals a client's git shows (SSH's stderr, HTTP's text/plain 503).
var (
	errUserBusy      = fmt.Errorf("your account has %d git operations running at once, Toft's limit for one account: try again when one has finished", maxUserOps)
	errWorkspaceBusy = fmt.Errorf("this workspace has %d git operations running at once, Toft's limit for one workspace: try again when one has finished", maxWorkspaceOps)
)

// acquire takes a slot for repository id, and for user and workspace when
// they are set, waiting up to e.wait for the global limit; a repository,
// account or workspace at its own limit is refused at once, with the reason.
func (e *Executor) acquire(ctx context.Context, id, user, workspace string) (func(), error) {
	type limit struct {
		key string
		max int
		err error
	}
	var limits []limit
	if user != "" {
		limits = append(limits, limit{"user:" + user, maxUserOps, errUserBusy})
	}
	if workspace != "" {
		limits = append(limits, limit{"workspace:" + workspace, maxWorkspaceOps, errWorkspaceBusy})
	}
	if user != "" || workspace != "" {
		limits = append(limits, limit{"clients", maxClientOps, ErrBusy})
	}
	limits = append(limits, limit{"repo:" + id, maxRepoOps, ErrBusy})
	e.mu.Lock()
	for _, l := range limits {
		if e.held[l.key] >= l.max {
			e.mu.Unlock()
			return nil, l.err
		}
	}
	for _, l := range limits {
		e.held[l.key]++
	}
	e.mu.Unlock()
	unhold := func() {
		e.mu.Lock()
		for _, l := range limits {
			if e.held[l.key]--; e.held[l.key] == 0 {
				delete(e.held, l.key)
			}
		}
		e.mu.Unlock()
	}
	t := time.NewTimer(e.wait)
	defer t.Stop()
	select {
	case e.ops <- struct{}{}:
		return func() { <-e.ops; unhold() }, nil
	case <-t.C:
	case <-ctx.Done():
	}
	unhold()
	return nil, ErrBusy
}

// capped keeps the first max bytes written to it.
type capped struct {
	bytes.Buffer
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.Len(); room > 0 {
		c.Buffer.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// activeReader and flushWriter tell the idle timer that bytes moved;
// flushWriter also sends each write at once.
type activeReader struct {
	r     io.Reader
	moved func()
}

func (a *activeReader) Read(p []byte) (int, error) {
	n, err := a.r.Read(p)
	if n > 0 {
		a.moved()
	}
	return n, err
}

// pktWatch follows a stream of git's pkt-lines and stops deadline at the
// first packet begun accepts (given its 4-byte length and, if it has one,
// the first byte of its payload), or at a raw pack's PACK. A stream that
// isn't pkt-lines is no longer watched, and deadline still runs.
type pktWatch struct {
	deadline *time.Timer
	begun    func(head []byte) bool
	head     []byte
	skip     int
	done     bool
}

func (p *pktWatch) Write(b []byte) (int, error) {
	n := len(b)
	for len(b) > 0 && !p.done {
		if p.skip > 0 {
			k := min(p.skip, len(b))
			p.skip, b = p.skip-k, b[k:]
			continue
		}
		p.head, b = append(p.head, b[0]), b[1:]
		if len(p.head) < 4 {
			continue
		}
		size, err := strconv.ParseUint(string(p.head[:4]), 16, 16)
		switch {
		case string(p.head) == "PACK":
			p.done = true
			p.deadline.Stop()
		case err != nil:
			p.done = true
		case size > 4 && len(p.head) < 5:
		case p.begun(p.head):
			p.done = true
			p.deadline.Stop()
		default:
			p.skip = max(int(size)-len(p.head), 0)
			p.head = p.head[:0]
		}
	}
	return n, nil
}

type flushWriter struct {
	w     io.Writer
	rc    *http.ResponseController
	moved func()
}

func (f *flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if err == nil {
		err = f.rc.Flush()
	}
	f.moved()
	return n, err
}
