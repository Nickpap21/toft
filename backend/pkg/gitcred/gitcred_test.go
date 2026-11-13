package gitcred

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/testenv"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplit(t *testing.T) {
	cases := []struct{ in, wantURL, wantCred string }{
		{"https://bot:ghp_abc@github.com/acme/app.git", "https://github.com/acme/app.git", "https://bot:ghp_abc@github.com"},
		{"https://u:p%40ss@gitea.local:3000/a/b", "https://gitea.local:3000/a/b", "https://u:p%40ss@gitea.local:3000"},
		// A token saved without a username: git sends it as the user with an
		// empty password, so the line says exactly that.
		{"https://ghp_abc@github.com/acme/app.git", "https://github.com/acme/app.git", "https://ghp_abc:@github.com"},
		{"https://github.com/acme/app.git", "https://github.com/acme/app.git", ""},
		{"git@github.com:acme/app.git", "git@github.com:acme/app.git", ""},
		{"ssh://git:pw@host/r.git", "ssh://git:pw@host/r.git", ""},
		{"file:///srv/r.git", "file:///srv/r.git", ""},
	}
	for _, tc := range cases {
		gotURL, gotCred := Split(tc.in)
		assert.Equal(t, tc.wantURL, gotURL, tc.in)
		assert.Equal(t, tc.wantCred, gotCred, tc.in)
	}
}

func TestLocalWritesAPrivateFileAndRemovesIt(t *testing.T) {
	cloneURL, args, cleanup, err := Local("https://bot:ghp_secret@github.com/acme/app.git")
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/acme/app.git", cloneURL)
	require.Len(t, args, 4)
	assert.NotContains(t, strings.Join(args, " "), "ghp_secret")
	path := strings.TrimPrefix(args[3], "credential.helper=store --file=")

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "https://bot:ghp_secret@github.com\n", string(b))

	cleanup()
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err), "cleanup removes the file")

	plain, args, cleanup, err := Local("https://github.com/acme/app.git")
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/acme/app.git", plain)
	assert.Empty(t, args)
	cleanup()
}

// Git itself reads the line back as the same user and password.
func TestGitReadsTheLineBack(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		testenv.Skip(t, "git not installed")
	}
	for in, want := range map[string]string{
		"https://bot:ghp_secret@github.com/a.git": "username=bot\npassword=ghp_secret\n",
		"https://ghp_secret@github.com/a.git":     "username=ghp_secret\npassword=\n",
	} {
		_, line := Split(in)
		path := filepath.Join(t.TempDir(), "creds")
		require.NoError(t, os.WriteFile(path, []byte(line+"\n"), 0o600))
		cmd := exec.Command("git", append(HelperArgs(path), "credential", "fill")...)
		cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.Output()
		require.NoError(t, err, in)
		assert.Contains(t, string(out), want, in)
	}
}
