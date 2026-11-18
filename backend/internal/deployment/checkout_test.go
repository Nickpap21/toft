package deployment

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/testenv"

	serviceManager "github.com/Nickpap21/toft/backend/internal/service"
)

func TestCheckoutRepoPinsCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		testenv.Skip(t, "git not installed")
	}
	src := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", src}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(content string) string {
		if err := os.WriteFile(filepath.Join(src, "compose.yml"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", ".")
		git("commit", "-q", "-m", content)
		return git("rev-parse", "HEAD")
	}
	git("init", "-q", "-b", "main")
	first := commit("v1")
	commit("v2")

	read := func(dir string) string {
		b, err := os.ReadFile(filepath.Join(dir, "compose.yml"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	pinned := t.TempDir()
	if out, err := checkoutRepo(context.Background(), testenv.GitURL(t, src), "main", first, pinned); err != nil {
		t.Fatalf("checkout at SHA: %v\n%s", err, out)
	}
	if got := read(pinned); got != "v1" {
		t.Fatalf("pinned checkout has %q, want v1", got)
	}

	tip := t.TempDir()
	if out, err := checkoutRepo(context.Background(), testenv.GitURL(t, src), "main", "", tip); err != nil {
		t.Fatalf("checkout at branch: %v\n%s", err, out)
	}
	if got := read(tip); got != "v2" {
		t.Fatalf("branch checkout has %q, want v2", got)
	}
}

// An ssh host that starts with "-" would be ssh's option: git refuses it,
// at the branch tip and at a commit. That "--" ends git's own options
// before the URL is asserted on the checkout's arguments
// (TestCheckoutRepoKeepsTheTokenOffTheCommandLine). CodeQL #49.
func TestCheckoutRepoRefusesAnSSHHostThatIsAnOption(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		testenv.Skip(t, "needs ssh")
	}
	_, marker := testenv.GitMarker(t)
	t.Setenv("TOFT_TEST_MARKER", marker)
	const url = "ssh://-oProxyCommand=touch $TOFT_TEST_MARKER/r.git"
	for _, sha := range []string{"", strings.Repeat("a", 40)} {
		out, _ := checkoutRepo(context.Background(), url, "main", sha, filepath.Join(t.TempDir(), "c"))
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("sha %q: git ran the command\n%s", sha, out)
		}
		if !strings.Contains(string(out), "strange hostname") {
			t.Errorf("sha %q: want git's refusal of the host, got\n%s", sha, out)
		}
	}
}

// A compose checkout uses https, http, ssh or git only: ext:: and fd:: would
// run or read what the URL names, and file:// or a path would clone the
// panel's own disk.
func TestCheckoutRepoRefusesOtherTransports(t *testing.T) {
	src, marker := testenv.GitMarker(t)
	for _, url := range []string{
		"ext::sh -c touch% " + marker,
		"fd::0",
		"file://" + src,
		src,
	} {
		for _, sha := range []string{"", strings.Repeat("a", 40)} {
			// fd::0 waits on stdin when git lets it through.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			out, err := checkoutRepo(ctx, url, "main", sha, filepath.Join(t.TempDir(), "c"))
			cancel()
			if _, statErr := os.Stat(marker); statErr == nil {
				t.Fatalf("%q: git ran the command\n%s", url, out)
			}
			if err == nil || !strings.Contains(string(out), "not allowed") {
				t.Errorf("%q (sha %q): want git's refusal of the transport, got %v\n%s", url, sha, err, out)
			}
		}
	}
}

func TestComposeDeployInputHelpers(t *testing.T) {
	for s, want := range map[string]bool{
		"0123456789abcdef0123456789ABCDEF01234567": true,
		"0123456": false, // short SHAs can't be fetched
		"0123456789abcdef0123456789abcdef0123456g": false,
		"": false,
	} {
		if got := isFullSHA(s); got != want {
			t.Errorf("isFullSHA(%q) = %v, want %v", s, got, want)
		}
	}

	for tag, want := range map[string]bool{
		"v1.2.3": true, "sha-0a1b2c3": true, "latest": true, "1_0": true,
		"": false, ".hidden": false, "-x": false, "a:b": false, "a b": false, "a\nB=c": false,
		strings.Repeat("a", 128): true, strings.Repeat("a", 129): false,
	} {
		if got := imageTagPattern.MatchString(tag); got != want {
			t.Errorf("image tag %q accepted = %v, want %v", tag, got, want)
		}
	}

	got := withEnv([]string{"A=1", "IMAGE_TAG=old", "IMAGE_TAGX=keep"}, "IMAGE_TAG", "v2")
	want := []string{"A=1", "IMAGE_TAGX=keep", "IMAGE_TAG=v2"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("withEnv = %v, want %v", got, want)
	}
}

// A push to git_repo_url records that repository's commit as CommitSHA; a
// rollback must check out the compose repository's own recorded commit.
func TestComposeRollbackCommit(t *testing.T) {
	const appSHA = "1111111111111111111111111111111111111111"
	const composeSHA = "2222222222222222222222222222222222222222"
	fromRepo := &serviceManager.Service{SourceType: serviceManager.SourceTypeDockerCompose, ComposeRepoURL: "https://example.com/infra", GitRepoURL: "https://example.com/app"}
	inline := &serviceManager.Service{SourceType: serviceManager.SourceTypeDockerCompose}

	for name, tc := range map[string]struct {
		svc  *serviceManager.Service
		dep  Deployment
		want string
	}{
		"the recorded compose commit":                   {fromRepo, Deployment{CommitSHA: appSHA, ComposeCommitSHA: composeSHA}, composeSHA},
		"never the push's commit of another repository": {fromRepo, Deployment{CommitSHA: appSHA}, ""},
		"an inline compose file has no repository":      {inline, Deployment{ComposeCommitSHA: composeSHA}, ""},
		"a malformed commit is not fetched":             {fromRepo, Deployment{ComposeCommitSHA: "2222"}, ""},
	} {
		if got := composeRollbackCommit(tc.svc, &tc.dep); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}
