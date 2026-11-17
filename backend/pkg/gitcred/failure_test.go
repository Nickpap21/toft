package gitcred

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/testenv"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// git's own output for a branch that is not there.
func TestFailureNamesGitsReason(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		testenv.Skip(t, "needs git")
	}
	src := t.TempDir()
	out, err := exec.Command("git", "-C", src, "init", "--quiet", "-b", "main").CombinedOutput()
	require.NoError(t, err, string(out))
	out, err = exec.Command("git", "-C", src, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--quiet", "--allow-empty", "-m", "init").CombinedOutput()
	require.NoError(t, err, string(out))

	out, err = exec.Command("git", "clone", "--depth", "1", "--branch", "nope", "--progress", "--", "file://"+src, t.TempDir()).CombinedOutput()
	require.Error(t, err)
	got := Failure("git clone failed", out, err)
	assert.Contains(t, got.Error(), "git clone failed: Remote branch nope not found in upstream origin (exit status 128)")
	var ee *exec.ExitError
	assert.True(t, errors.As(got, &ee), "the exit error stays in the chain")
}

func TestFailureReason(t *testing.T) {
	for name, tc := range map[string]struct{ out, want string }{
		"last fatal wins": {
			"Cloning into 'x'...\nerror: first\nfatal: could not read Username for 'https://github.com': terminal prompts disabled\n",
			"could not read Username for 'https://github.com': terminal prompts disabled",
		},
		"no fatal: none, not a progress line": {"Cloning into 'x'...\rReceiving objects: 10%\rReceiving objects: 20%", ""},
		"nothing":                             {"", ""},
		"user:token in the URL":               {"fatal: unable to access 'https://bot:ghp_secret@github.com/a/b.git/': 403", "unable to access 'https://github.com/a/b.git/': 403"},
		"token only, ssh, odd password":       {"fatal: ssh://git:p@ss@host.example/a/b.git not found", "ssh://host.example/a/b.git not found"},
	} {
		t.Run(name, func(t *testing.T) {
			got := Reason([]byte(tc.out))
			assert.Equal(t, tc.want, got)
			assert.NotContains(t, got, "ghp_secret")
		})
	}
	assert.LessOrEqual(t, len([]rune(Reason([]byte("fatal: "+strings.Repeat("x", 1000))))), maxReason+1)
}
