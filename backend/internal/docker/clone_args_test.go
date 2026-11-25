package docker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/internal/testenv"
)

// cloners are the two build clones: here, and on a server, whose shell the
// fake host runs locally.
func cloners(t *testing.T) map[string]func(ctx context.Context, url, branch string) (string, error) {
	return map[string]func(ctx context.Context, url, branch string) (string, error){
		"local": func(ctx context.Context, url, branch string) (string, error) {
			var out strings.Builder
			_, _, cleanup, err := cloneRepository(ctx, url, branch, "", func(l string) { out.WriteString(l + "\n") })
			if err == nil {
				cleanup()
			}
			return out.String(), err
		},
		"remote": func(ctx context.Context, url, branch string) (string, error) {
			var out strings.Builder
			h := &fakeHost{stream: func(ctx context.Context, cmd string) error {
				c := exec.CommandContext(ctx, "sh", "-c", cmd)
				c.WaitDelay = time.Second // a git left behind by the killed sh holds stdout
				b, err := c.Output()      // the command merges its stderr
				out.Write(b)
				return err
			}}
			err := newTestRemote(h).cloneOnRemote(ctx, url, branch, filepath.Join(t.TempDir(), "c"), func(l string) { out.WriteString(l + "\n") })
			if len(h.calls) > 0 {
				out.WriteString("command: " + h.calls[len(h.calls)-1].cmd + "\n")
			}
			return out.String(), err
		},
	}
}

// An ssh host that starts with "-" would be ssh's option: git refuses it, in
// a build's clone here and on a server. That "--" ends git's own options
// before the URL is asserted on each clone's arguments
// (TestLocalCloneKeepsTheTokenOffTheCommandLine,
// TestRemoteCloneEndsOptionsBeforeTheURL). CodeQL #49.
func TestBuildClonesRefuseAnSSHHostThatIsAnOption(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		testenv.Skip(t, "needs ssh")
	}
	_, marker := testenv.GitMarker(t)
	t.Setenv("TOFT_TEST_MARKER", marker)
	const url = "ssh://-oProxyCommand=touch $TOFT_TEST_MARKER/r.git"
	for name, clone := range cloners(t) {
		out, _ := clone(context.Background(), url, "main")
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("%s: git ran the command\n%s", name, out)
		}
		if !strings.Contains(out, "strange hostname") {
			t.Errorf("%s: want git's refusal of the host, got\n%s", name, out)
		}
	}
}

// A URL the validator accepts with shell syntax in its path, query or
// userinfo reaches the server's git as one quoted word: the server's shell
// runs nothing in it.
func TestRemoteCloneQuotesTheURL(t *testing.T) {
	_, marker := testenv.GitMarker(t)
	t.Setenv("TOFT_TEST_MARKER", marker)
	const run = "$(touch$IFS$TOFT_TEST_MARKER)"
	urls := []string{
		"https://example.invalid/r" + run + ".git",
		"https://example.invalid/r`touch$IFS$TOFT_TEST_MARKER`.git",
		"https://example.invalid/r.git;touch$IFS$TOFT_TEST_MARKER",
		"https://example.invalid/r.git?x=" + run,
		"https://u" + run + "@example.invalid/r.git",
		"https://u:p" + run + "@example.invalid/r.git",
		"ssh://u" + run + "@example.invalid/r.git",
		"git@example.invalid:r" + run + ".git",
		"git@example.invalid:r'" + run + "'.git",
	}
	// The marker works: the same command, unquoted, runs it.
	if err := exec.Command("sh", "-c", "echo "+run).Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the marker does not work: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	clone := cloners(t)["remote"]
	for _, url := range urls {
		if err := service.ValidateGitURL("git_repo_url", url); err != nil {
			t.Fatalf("%q: the validator refuses it, so no deploy clones it: %v", url, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		out, _ := clone(ctx, url, "main")
		cancel()
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("%q: the server's shell ran the command\n%s", url, out)
		}
	}
}

// A build's clone uses https, http, ssh or git only, here and on a server:
// ext:: and fd:: would run or read what the URL names, and file:// or a path
// would clone the host's own disk.
func TestBuildClonesRefuseOtherTransports(t *testing.T) {
	src, marker := testenv.GitMarker(t)
	for name, clone := range cloners(t) {
		for _, url := range []string{"ext::sh -c touch% " + marker, "fd::0", "file://" + src, src} {
			// fd::0 waits on stdin when git lets it through.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			out, err := clone(ctx, url, "main")
			cancel()
			if _, statErr := os.Stat(marker); statErr == nil {
				t.Fatalf("%s %q: git ran the command\n%s", name, url, out)
			}
			if err == nil || !strings.Contains(out+err.Error(), "not allowed") {
				t.Errorf("%s %q: want git's refusal of the transport, got %v\n%s", name, url, err, out)
			}
		}
	}
}
