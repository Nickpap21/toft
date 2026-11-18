package testenv

import (
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// GitURL serves the repository at dir (an absolute path) over HTTP with
// git's http-backend, as a git host does, and returns its URL, whose path
// is dir. Deploy clones refuse file:// and local paths (gitcred.ProtocolArgs),
// so a test's repository is reached this way. The server stops when the
// test ends.
func GitURL(t testing.TB, dir string) string {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		Skip(t, "git is not installed")
	}
	if b, err := exec.Command(gitPath, "--exec-path").Output(); err != nil {
		Skip(t, "git has no exec path")
	} else if _, err := os.Stat(filepath.Join(strings.TrimSpace(string(b)), "git-http-backend")); err != nil {
		Skip(t, "git http-backend is not installed (alpine: git-daemon)")
	}
	home := t.TempDir()
	srv := httptest.NewServer(&cgi.Handler{Path: gitPath, Args: []string{"http-backend"}, Env: []string{
		"GIT_PROJECT_ROOT=/", "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1", "HOME=" + home,
	}})
	t.Cleanup(srv.Close)
	return srv.URL + dir
}

// GitMarker is a local repository with one commit on main, and a path that a
// command git was tricked into running would create.
func GitMarker(t testing.TB) (src, marker string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		Skip(t, "git is not installed")
	}
	src = t.TempDir()
	if out, err := exec.Command("git", "-C", src, "init", "-q", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", src, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	return src, filepath.Join(t.TempDir(), "ran")
}
