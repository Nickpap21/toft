package gitstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/testenv"
)

// fakeRemote is another host's git over HTTPS (git http-backend), wanting
// user and password, with a bare repository per name under its root.
func fakeRemote(t *testing.T, user, password string) (*httptest.Server, string) {
	return fakeRemoteChunked(t, user, password, new(atomic.Bool))
}

// fakeRemoteChunked is fakeRemote, noting in chunked when a request came
// chunked.
func fakeRemoteChunked(t *testing.T, user, password string, chunked *atomic.Bool) (*httptest.Server, string) {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		testenv.Skip(t, "git is not installed")
	}
	if b, err := exec.Command(gitPath, "--exec-path").Output(); err != nil {
		testenv.Skip(t, "git has no exec path")
	} else if _, err := os.Stat(filepath.Join(strings.TrimSpace(string(b)), "git-http-backend")); err != nil {
		testenv.Skip(t, "git http-backend is not installed (alpine: git-daemon)")
	}
	root := t.TempDir()
	backend := &cgi.Handler{Path: gitPath, Args: []string{"http-backend"}, Env: []string{
		"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1", "HOME=" + root,
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.receivepack", "GIT_CONFIG_VALUE_0=true",
	}}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != user || p != password {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "who are you", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/moved.git/info/refs" {
			http.Redirect(w, r, "/elsewhere.git/info/refs", http.StatusMovedPermanently)
			return
		}
		r.Header.Del("Authorization") // http-backend would take it as REMOTE_USER only
		if r.ContentLength < 0 {
			// The proxy sends a large push chunked, as git does; Go's CGI
			// host refuses chunked bodies, so read it whole here.
			chunked.Store(true)
			b, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			r.Body, r.ContentLength, r.TransferEncoding = io.NopCloser(bytes.NewReader(b)), int64(len(b)), nil
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, root
}

// TestImportAndMirrorThroughTheEgress runs the executor's remote operations
// with the real git client through the backend's egress proxy against a
// TLS remote: git reaches it only through the proxy, the proxy adds the
// credentials and does TLS, and a fetch and a push report what moved.
func TestImportAndMirrorThroughTheEgress(t *testing.T) {
	if _, err := CheckVersion(context.Background()); err != nil {
		testenv.Skip(t, "needs git %d.%d or newer: %v", MinVersion[0], MinVersion[1], err)
	}
	var chunked atomic.Bool
	remote, remoteRoot := fakeRemoteChunked(t, "x-access-token", "s3cret", &chunked)

	// The original: main and feature, a tag, HEAD on main.
	w := t.TempDir()
	gitIn(t, w, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(w, "a.txt"), []byte("one\n"), 0o644)
	gitIn(t, w, "add", ".")
	gitIn(t, w, "commit", "-qm", "First")
	gitIn(t, w, "tag", "v1.0.0")
	gitIn(t, w, "checkout", "-qb", "feature")
	os.WriteFile(filepath.Join(w, "b.txt"), []byte("two\n"), 0o644)
	gitIn(t, w, "add", ".")
	gitIn(t, w, "commit", "-qm", "Second")
	orig := filepath.Join(remoteRoot, "shop.git")
	gitIn(t, remoteRoot, "init", "-q", "--bare", "-b", "main", orig)
	gitIn(t, w, "push", "-q", orig, "main", "feature", "v1.0.0")
	gitIn(t, remoteRoot, "init", "-q", "--bare", filepath.Join(remoteRoot, "copy.git"))

	sockDir, err := os.MkdirTemp("", "te")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	egressSock := filepath.Join(sockDir, "e.sock")
	gitSock := filepath.Join(sockDir, "g.sock")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eg := NewEgress(remote.Client().Transport)
	go eg.Serve(ctx, egressSock)

	root := t.TempDir()
	ex := NewExecutor(root).WithEgress(egressSock)
	ln, err := net.Listen("unix", gitSock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: ex.Handler()}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	c := NewClient(gitSock)
	const id = "4d2c1e7a-9b3f-4a60-8e15-2f7c9d0b6a38"
	dir, _ := RepoPath(root, id)

	// ls, then fetch with the default branch: an import.
	u, op, end, err := eg.Open(remote.URL+"/shop.git", "x-access-token", "s3cret", "git-upload-pack")
	if err != nil {
		t.Fatal(err)
	}
	var refs RemoteRefs
	if err := c.Remote(ctx, id, "ls", RemoteRequest{URL: u}, &refs); err != nil {
		t.Fatalf("ls: %v", err)
	}
	if refs != (RemoteRefs{Head: "main", Branches: 2, Tags: 1}) {
		t.Fatalf("ls = %+v", refs)
	}
	var fr FetchResult
	if err := c.Remote(ctx, id, "fetch", RemoteRequest{URL: u, SetHead: "main"}, &fr); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	end()
	if status, _, _ := op.Result(); status != http.StatusOK {
		t.Errorf("remote status = %d", status)
	}
	if len(fr.Updated) != 3 || fr.Updated[0].Ref != "refs/heads/feature" || strings.Trim(fr.Updated[0].Old, "0") != "" {
		t.Fatalf("updated = %+v", fr.Updated)
	}
	if h := gitIn(t, dir, "symbolic-ref", "HEAD"); h != "refs/heads/main" {
		t.Errorf("HEAD = %s", h)
	}
	// The remote's refs/pull or anything else isn't fetched; a branch the
	// remote deleted is pruned.
	gitIn(t, orig, "update-ref", "refs/pull/1/head", "refs/heads/main")
	gitIn(t, orig, "branch", "-D", "feature")
	// 3 MiB that doesn't compress, so the mirror's push below goes chunked
	// through the proxy (git's http.postBuffer is 1 MiB).
	big := make([]byte, 3<<20)
	rand.Read(big)
	gitIn(t, w, "checkout", "-q", "main")
	os.WriteFile(filepath.Join(w, "big.bin"), big, 0o644)
	gitIn(t, w, "add", ".")
	gitIn(t, w, "commit", "-qm", "Big")
	gitIn(t, w, "push", "-q", orig, "main")
	u, _, end, _ = eg.Open(remote.URL+"/shop.git", "x-access-token", "s3cret", "git-upload-pack")
	if err := c.Remote(ctx, id, "fetch", RemoteRequest{URL: u}, &fr); err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	end()
	if len(fr.Updated) != 2 || fr.Updated[0].Ref != "refs/heads/feature" || strings.Trim(fr.Updated[0].New, "0") != "" ||
		fr.Updated[1].Ref != "refs/heads/main" {
		t.Fatalf("pruned and moved = %+v", fr.Updated)
	}
	if out := gitIn(t, dir, "for-each-ref", "--format=%(refname)"); out != "refs/heads/main\nrefs/tags/v1.0.0" {
		t.Fatalf("refs = %q", out)
	}

	// A push mirror: everything goes, and what the remote has that this
	// repository doesn't is deleted.
	copyDir := filepath.Join(remoteRoot, "copy.git")
	gitIn(t, w, "push", "-q", copyDir, "feature:stale")
	u, _, end, _ = eg.Open(remote.URL+"/copy.git", "x-access-token", "s3cret", "git-receive-pack")
	var pr PushResult
	if err := c.Remote(ctx, id, "push", RemoteRequest{URL: u}, &pr); err != nil {
		t.Fatalf("push: %v", err)
	}
	end()
	if !chunked.Load() {
		t.Error("the push wasn't chunked: the test no longer covers a large push")
	}
	if pr.Moved != 3 {
		t.Errorf("moved = %d, want main and the tag created, stale deleted", pr.Moved)
	}
	if out := gitIn(t, copyDir, "for-each-ref", "--format=%(refname)"); out != "refs/heads/main\nrefs/tags/v1.0.0" {
		t.Fatalf("mirror's refs = %q", out)
	}

	// A token for the other service, wrong credentials, a redirect, and an
	// operation that ended are all refused.
	u, _, end, _ = eg.Open(remote.URL+"/copy.git", "x-access-token", "s3cret", "git-upload-pack")
	if err := c.Remote(ctx, id, "push", RemoteRequest{URL: u}, &pr); err == nil {
		t.Error("a fetch token pushed")
	}
	end()
	u, op, end, _ = eg.Open(remote.URL+"/shop.git", "x-access-token", "wrong", "git-upload-pack")
	err = c.Remote(ctx, id, "ls", RemoteRequest{URL: u}, &refs)
	end()
	var be *BrowseError
	if !errors.As(err, &be) || be.Code != "remote_failed" {
		t.Fatalf("wrong credentials: %v", err)
	}
	if status, _, _ := op.Result(); status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", status)
	}
	u, op, end, _ = eg.Open(remote.URL+"/moved.git", "x-access-token", "s3cret", "git-upload-pack")
	_ = c.Remote(ctx, id, "ls", RemoteRequest{URL: u}, &refs)
	end()
	if status, loc, _ := op.Result(); status != http.StatusMovedPermanently || loc != "/elsewhere.git/info/refs" {
		t.Errorf("redirect = %d %q", status, loc)
	}
	if err := c.Remote(ctx, id, "ls", RemoteRequest{URL: u}, &refs); err == nil {
		t.Error("an ended operation still reached the remote")
	}
	if err := c.Remote(ctx, id, "ls", RemoteRequest{URL: remote.URL + "/shop.git"}, &refs); !errors.As(err, &be) || be.Code != "bad_url" {
		t.Errorf("a remote URL that isn't the egress: %v", err)
	}
	if _, _, _, err := eg.Open("http://example.com/x.git", "", "", "git-upload-pack"); err == nil {
		t.Error("an http remote was accepted")
	}
}
