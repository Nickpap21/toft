package deployment

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeGit puts a git on PATH that records each call's arguments and the
// credential file it was pointed at, then succeeds. It returns the record.
func fakeGit(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	dir := t.TempDir()
	record := filepath.Join(dir, "calls")
	script := `#!/bin/sh
echo "ARGS $*" >> "` + record + `"
for a in "$@"; do
  case "$a" in credential.helper=store\ --file=*) echo "CREDS $(cat "${a#credential.helper=store --file=}")" >> "` + record + `" ;; esac
done
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return record
}

// The token never reaches git's command line, where ps shows it: git reads
// it from a private file that is gone once the checkout ends. That holds for
// a token saved without a username too.
func TestCheckoutRepoKeepsTheTokenOffTheCommandLine(t *testing.T) {
	for _, tc := range []struct{ repo, creds string }{
		{"https://bot:ghp_secret@github.com/acme/infra.git", "https://bot:ghp_secret@github.com"},
		{"https://ghp_secret@github.com/acme/infra.git", "https://ghp_secret:@github.com"},
	} {
		for _, sha := range []string{"", "0123456789abcdef0123456789abcdef01234567"} {
			record := fakeGit(t)
			tmp := t.TempDir() // the credential file's directory, this test's alone
			t.Setenv("TMPDIR", tmp)
			if out, err := checkoutRepo(context.Background(), tc.repo, "main", sha, filepath.Join(t.TempDir(), "repo")); err != nil {
				t.Fatalf("%s @%q: %v\n%s", tc.repo, sha, err, out)
			}
			b, err := os.ReadFile(record)
			if err != nil {
				t.Fatal(err)
			}
			calls := string(b)
			for _, line := range strings.Split(strings.TrimSpace(calls), "\n") {
				if strings.HasPrefix(line, "ARGS ") && strings.Contains(line, "ghp_secret") {
					t.Errorf("the token reached a command line: %s", line)
				}
			}
			if !strings.Contains(calls, "https://github.com/acme/infra.git") {
				t.Errorf("git is not given the plain URL:\n%s", calls)
			}
			if !strings.Contains(calls, "CREDS "+tc.creds+"\n") {
				t.Errorf("the fetching call did not get the credentials %q:\n%s", tc.creds, calls)
			}
			if left, _ := filepath.Glob(filepath.Join(tmp, "paas-git-credentials-*")); len(left) > 0 {
				t.Errorf("credential file left behind: %v", left)
			}
		}
	}
}
