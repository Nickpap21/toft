package gitstore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// Import and mirrors (D-79): git here reads from and pushes to other hosts,
// while the container keeps no network. Every remote URL the executor runs
// is the backend's egress, http://egress.toft/<token>/, reached through the
// SOCKS proxy the backend serves on EgressSocket; the backend maps the
// token to the real remote and its credentials, so git never holds either.
//
//	POST /v1/repos/{id}/remote/ls      the remote's default branch and counts
//	POST /v1/repos/{id}/remote/fetch   its branches and tags, pruned; what moved
//	POST /v1/repos/{id}/remote/push    every branch and tag, forced and pruned

// EgressSocket is where the backend's egress proxy listens, beside Socket in
// the volume both containers mount.
const EgressSocket = "/run/toft-git/egress.sock"

// egressURL is the only remote URL the executor takes.
var egressURL = regexp.MustCompile(`^http://egress\.toft/[0-9a-f]{32}/$`)

// RemoteRequest is a remote operation's body: the egress URL, and for a
// fetch whether to point HEAD at the remote's default branch (an import).
type RemoteRequest struct {
	URL     string `json:"url"`
	SetHead string `json:"set_head,omitempty"`
}

// RemoteRefs is what ls found: the branch the remote's HEAD names ("" when
// it names none) and how many branches and tags it has.
type RemoteRefs struct {
	Head     string `json:"head"`
	Branches int    `json:"branches"`
	Tags     int    `json:"tags"`
}

// FetchResult is what a fetch moved, as a push reports it.
type FetchResult struct {
	Updated []RefUpdate `json:"updated"`
}

// PushResult is how many branches and tags a push created, moved or
// deleted on the remote.
type PushResult struct {
	Moved int `json:"moved"`
}

// remoteConfig is every remote operation's policy: only the egress, over
// the proxy, no redirect, no credential helper, no prompt, every object
// checked, nothing but the refs named, no maintenance started in the
// background, and a transfer that stops moving for ten minutes given up.
func remoteConfig(socket string) []string {
	return []string{
		"-c", "http.proxy=socks5h://localhost" + socket,
		"-c", "protocol.allow=never",
		"-c", "protocol.http.allow=always",
		"-c", "http.followRedirects=false",
		"-c", "credential.helper=",
		"-c", "http.lowSpeedLimit=1000",
		"-c", "http.lowSpeedTime=600",
		"-c", "fetch.fsckObjects=true",
		"-c", "transfer.fsckObjects=true",
		"-c", "fetch.recurseSubmodules=false",
		"-c", "submodule.recurse=false",
		"-c", "gc.auto=0",
		"-c", "maintenance.auto=false",
	}
}

func (e *Executor) handleRemote(w http.ResponseWriter, r *http.Request) {
	id, op := r.PathValue("id"), r.PathValue("op")
	dir, err := RepoPath(e.root, id)
	if err != nil {
		writeErr(w, browseErr(http.StatusBadRequest, "bad_repository", err.Error()))
		return
	}
	var req RemoteRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req); err != nil || !egressURL.MatchString(req.URL) {
		writeErr(w, browseErr(http.StatusBadRequest, "bad_url", "the remote must be the panel's egress"))
		return
	}
	if req.SetHead != "" && !ValidRef(req.SetHead) {
		writeErr(w, errBadRef)
		return
	}
	if err := e.init(r.Context(), dir); err != nil {
		writeErr(w, err)
		return
	}
	release, err := e.acquire(r.Context(), id, "", "")
	if err != nil {
		writeErr(w, browseErr(http.StatusServiceUnavailable, "git_busy", err.Error()))
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(r.Context(), e.maxTime)
	defer cancel()
	var v any
	switch op {
	case "ls":
		v, err = e.lsRemote(ctx, dir, req.URL)
	case "fetch":
		v, err = e.fetch(ctx, dir, req)
	case "push":
		v, err = e.pushMirror(ctx, dir, req.URL)
	default:
		err = browseErr(http.StatusNotFound, "unknown_op", "unknown remote operation")
	}
	if err != nil {
		writeErr(w, remoteErr(err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// remoteErr carries git's own words back: they say what the remote
// refused, which the backend puts in words of its own when it can.
func remoteErr(err error) error {
	var g *gitFailed
	if errors.As(err, &g) {
		return browseErr(http.StatusUnprocessableEntity, "remote_failed", lastLines(g.stderr, 3))
	}
	return err
}

// lastLines is the last n non-empty lines of s, joined by "; ".
func lastLines(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}

func (e *Executor) remote(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return out(ctx, dir, nil, append(remoteConfig(e.egress), args...)...)
}

// lsRemote reads the remote's HEAD and counts its branches and tags.
func (e *Executor) lsRemote(ctx context.Context, dir, remote string) (RemoteRefs, error) {
	b, err := e.remote(ctx, dir, "ls-remote", "--symref", "--end-of-options", remote, "HEAD", "refs/heads/*", "refs/tags/*")
	if err != nil {
		return RemoteRefs{}, err
	}
	var refs RemoteRefs
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if target, ok := strings.CutPrefix(line, "ref: "); ok {
			if name, ok := strings.CutSuffix(target, "\tHEAD"); ok {
				refs.Head = strings.TrimPrefix(name, "refs/heads/")
			}
			continue
		}
		_, name, ok := strings.Cut(line, "\t")
		switch {
		case !ok || strings.HasSuffix(name, "^{}"):
		case strings.HasPrefix(name, "refs/heads/") && name != "refs/heads/HEAD":
			refs.Branches++
		case strings.HasPrefix(name, "refs/tags/"):
			refs.Tags++
		}
	}
	if !ValidRef(refs.Head) {
		refs.Head = ""
	}
	return refs, nil
}

// fetch brings the remote's branches and tags in, deleting those it no
// longer has, and reports what moved; with SetHead (an import) HEAD then
// names that branch, or the one fixHead picks.
func (e *Executor) fetch(ctx context.Context, dir string, req RemoteRequest) (FetchResult, error) {
	before, err := refsOf(ctx, dir)
	if err != nil {
		return FetchResult{}, err
	}
	if _, err := e.remote(ctx, dir, "fetch", "--prune", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", "--quiet",
		"--end-of-options", req.URL, "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*", "^refs/heads/HEAD"); err != nil {
		return FetchResult{}, err
	}
	after, err := refsOf(ctx, dir)
	if err != nil {
		return FetchResult{}, err
	}
	res := FetchResult{Updated: diffRefs(before, after)}
	if req.SetHead != "" {
		if err := setHead(ctx, dir, req.SetHead); err == nil {
			return res, nil
		}
	}
	return res, fixHead(ctx, dir)
}

// pushMirror makes the remote's branches and tags this repository's.
func (e *Executor) pushMirror(ctx context.Context, dir, remote string) (PushResult, error) {
	b, err := e.remote(ctx, dir, "push", "--porcelain", "--force", "--prune", "--no-verify",
		"--end-of-options", remote, "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
	if err != nil {
		var g *gitFailed
		if errors.As(err, &g) {
			// The refs the remote refused say more than git's last line.
			var refused []string
			for _, l := range strings.Split(g.stdout, "\n") {
				if strings.HasPrefix(l, "!\t") {
					refused = append(refused, strings.TrimPrefix(l, "!\t"))
				}
			}
			if len(refused) > 0 {
				g.stderr = strings.Join(refused, "\n") + "\n" + g.stderr
			}
		}
		return PushResult{}, err
	}
	var res PushResult
	for _, l := range strings.Split(string(b), "\n") {
		if len(l) > 1 && l[1] == '\t' && strings.ContainsRune(" +-*", rune(l[0])) {
			res.Moved++
		}
	}
	return res, nil
}

// refsOf is the repository's branches and tags: ref → object.
func refsOf(ctx context.Context, dir string) (map[string]string, error) {
	b, err := out(ctx, dir, nil, "for-each-ref", "--format=%(objectname) %(refname)", "refs/heads", "refs/tags")
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		if sha, name, ok := strings.Cut(l, " "); ok {
			refs[name] = sha
		}
	}
	return refs, nil
}

// diffRefs is what moved from before to after, as RefUpdates (all zeros for
// a ref created or deleted), at most 1,000 like a push's report.
func diffRefs(before, after map[string]string) []RefUpdate {
	zero := strings.Repeat("0", 40)
	out := []RefUpdate{}
	for name, sha := range after {
		if old := before[name]; old != sha {
			if old == "" {
				old = zero
			}
			out = append(out, RefUpdate{Ref: name, Old: old, New: sha})
		}
	}
	for name, sha := range before {
		if _, ok := after[name]; !ok {
			out = append(out, RefUpdate{Ref: name, Old: sha, New: zero})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	if len(out) > 1000 {
		out = out[:1000]
	}
	return out
}
