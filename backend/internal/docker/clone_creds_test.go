package docker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/testenv"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A local build clone keeps the token off git's command line, where ps shows
// it, including a token saved without a username.
func TestLocalCloneKeepsTheTokenOffTheCommandLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	for _, tc := range []struct{ repo, creds string }{
		{"https://bot:ghp_secret@github.com/acme/app.git", "https://bot:ghp_secret@github.com"},
		{"https://ghp_secret@github.com/acme/app.git", "https://ghp_secret:@github.com"},
	} {
		dir := t.TempDir()
		record := filepath.Join(dir, "calls")
		script := "#!/bin/sh\necho \"ARGS $*\" >> \"" + record + "\"\n" +
			"for a in \"$@\"; do case \"$a\" in credential.helper=store\\ --file=*) echo \"CREDS $(cat \"${a#credential.helper=store --file=}\")\" >> \"" + record + "\" ;; esac; done\n"
		if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		tmp := t.TempDir() // the clone and credential file, this test's alone
		t.Setenv("TMPDIR", tmp)

		_, cleanup, err := cloneRepository(context.Background(), tc.repo, "main", "", func(string) {})
		if err != nil {
			t.Fatal(err)
		}
		cleanup()
		if left, _ := filepath.Glob(filepath.Join(tmp, "paas-git-credentials-*")); len(left) > 0 {
			t.Errorf("credential file left behind: %v", left)
		}
		b, err := os.ReadFile(record)
		if err != nil {
			t.Fatal(err)
		}
		calls := string(b)
		if strings.Contains(strings.SplitN(calls, "\n", 2)[0], "ghp_secret") {
			t.Errorf("the token reached a command line: %s", calls)
		}
		if !strings.Contains(calls, "https://github.com/acme/app.git") || !strings.Contains(calls, "CREDS "+tc.creds+"\n") {
			t.Errorf("want the plain URL and the credentials %q in a file:\n%s", tc.creds, calls)
		}
	}
}

// A build reads the commit its clone checked out, and logs it as the clone's
// last line; output it cannot read is a warning, and the build goes on.
func TestClonedCommitReadsTheCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		testenv.Skip(t, "needs git")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "--quiet", "-b", "main"},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--quiet", "--allow-empty", "-m", "Bump pgx to v5.7", "-m", "The body is not the subject."},
	} {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	want, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	require.NoError(t, err)

	var lines []string
	logFn := func(l string) { lines = append(lines, l) }
	c := clonedCommit("main", logFn, func() (string, error) { return gitHead(context.Background(), dir) })
	assert.Equal(t, deployment.Commit{SHA: strings.TrimSpace(string(want)), Subject: "Bump pgx to v5.7"}, c)
	assert.Equal(t, []string{"Cloned main at " + c.SHA[:7]}, lines)

	lines = nil
	c = clonedCommit("main", logFn, func() (string, error) { return gitHead(context.Background(), t.TempDir()) })
	assert.Empty(t, c)
	require.Len(t, lines, 1)
	assert.True(t, strings.HasPrefix(lines[0], "⚠ Could not read the commit checked out"), lines[0])

	_, err = parseHeadCommit("fatal: not a git repository\n")
	assert.Error(t, err)
	sha256 := strings.Repeat("c", 64)
	got, err := parseHeadCommit(sha256 + "\n")
	require.NoError(t, err)
	assert.Equal(t, deployment.Commit{SHA: sha256}, got, "a SHA-256 repository, and a commit with no subject")
}
