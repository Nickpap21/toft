package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/internal/testenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// insideBuildCmd, run by a real sh: a path a link takes out of the build
// folder prints "outside"; one that stays in it prints nothing.
func TestInsideBuildCmd(t *testing.T) {
	if exec.Command("realpath", "-m", "/").Run() != nil {
		testenv.Skip(t, "no GNU realpath -m here")
	}
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "app"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "app", "Dockerfile"), []byte("FROM scratch\n"), 0o644))
	require.NoError(t, os.Symlink("/etc", filepath.Join(root, "link")))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "evil"), 0o755))
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(root, "evil", "Dockerfile")))
	require.NoError(t, os.Symlink("app", filepath.Join(root, "alias")))
	run := func(paths ...string) string {
		out, err := exec.Command("sh", "-c", insideBuildCmd(root, paths...)).CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	assert.Empty(t, run(root+"/app", root+"/app/Dockerfile"), "a plain directory")
	assert.Empty(t, run(root+"/alias", root+"/missing/Dockerfile"), "a link inside, a path not there yet")
	assert.Equal(t, "outside", run(root+"/link"), "a directory linked out")
	assert.Equal(t, "outside", run(root+"/app", root+"/evil/Dockerfile"), "a Dockerfile linked out")
}

// A repository's link out of the build folder fails every remote build
// before docker or nixpacks reads through it.
func TestRemoteBuildsRefuseALinkOutOfTheBuildFolder(t *testing.T) {
	ctx := context.Background()
	h := &fakeHost{exec: func(_ context.Context, cmd string) (string, error) {
		switch {
		case strings.HasPrefix(cmd, "mktemp"):
			return "/tmp/paas-build-1", nil
		case strings.Contains(cmd, "realpath"):
			return "outside\n", nil
		}
		return "", nil
	}}
	o := newTestRemote(h)
	log := func(string) {}
	for _, dir := range []string{"", "app"} {
		_, err := o.BuildFromGit(ctx, "https://example.com/r.git", "main", dir, "img:1", nil, nil, log)
		assert.ErrorIs(t, err, service.ErrInvalidConfig, "BuildFromGit %q", dir)
		for _, nix := range []*deployment.NixpacksCommands{nil, {}} {
			err = o.BuildFromArchive(ctx, strings.NewReader("tar"), dir, "img:1", nix, nil, nil, log)
			assert.ErrorIs(t, err, service.ErrInvalidConfig, "BuildFromArchive %q", dir)
		}
	}
	_, err := o.BuildFromGitNixpacks(ctx, "https://example.com/r.git", "main", "app", "img:1", deployment.NixpacksCommands{}, nil, nil, log)
	assert.ErrorIs(t, err, service.ErrInvalidConfig, "BuildFromGitNixpacks")
	for _, c := range h.recorded() {
		assert.NotContains(t, c.cmd, "docker build", "a build ran")
		assert.NotContains(t, c.cmd, "nixpacks build", "a build ran")
	}
}

// linksOutCmd, run by a real sh: it prints the links that leave the build
// folder (another deploy's folder beside it, a file of the server's) or
// lead nowhere, and none that stay in it.
func TestLinksOutCmd(t *testing.T) {
	if exec.Command("realpath", "-e", "/").Run() != nil {
		testenv.Skip(t, "no GNU realpath -e here")
	}
	tmp := t.TempDir()
	dir, other := filepath.Join(tmp, "paas-build-a"), filepath.Join(tmp, "paas-build-b")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	require.NoError(t, os.MkdirAll(other, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(other, "secret"), []byte("another deploy's\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "main.js"), nil, 0o644))
	run := func() []string {
		out, err := exec.Command("sh", "-c", linksOutCmd(dir)).CombinedOutput()
		require.NoError(t, err, string(out))
		lines := strings.Fields(string(out))
		sort.Strings(lines)
		for i, l := range lines {
			lines[i] = strings.TrimPrefix(l, "/")
		}
		return lines
	}
	require.NoError(t, os.Symlink("src/main.js", filepath.Join(dir, "index.js")))
	require.NoError(t, os.Symlink("..", filepath.Join(dir, "src", "up")))
	require.NoError(t, os.Symlink(dir, filepath.Join(dir, "self")))
	assert.Empty(t, run(), "links that stay in the folder")

	require.NoError(t, os.Symlink("../paas-build-b/secret", filepath.Join(dir, "Procfile")))
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(dir, "src", "passwd")))
	require.NoError(t, os.Symlink("nowhere", filepath.Join(dir, "dangling")))
	require.NoError(t, os.Symlink(other, filepath.Join(dir, "src", "other")))
	assert.Equal(t, []string{"Procfile", "dangling", "src/other", "src/passwd"}, run())

	// A link named with a space only still prints something the SSH
	// client's trim leaves.
	blank := filepath.Join(tmp, "paas-build-c")
	require.NoError(t, os.Mkdir(blank, 0o755))
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(blank, " ")))
	out, err := exec.Command("sh", "-c", linksOutCmd(blank)).CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Equal(t, "/", strings.TrimSpace(string(out)))

	_, err = exec.Command("sh", "-c", linksOutCmd(filepath.Join(tmp, "missing"))).CombinedOutput()
	assert.Error(t, err, "a folder that can't be read is not passed as clean")
}

// On the panel's host, nixpacks never reads through a link out of the
// unpacked source.
func TestLocalNixpacksRefusesALinkOutOfTheBuildFolder(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "app", Typeflag: tar.TypeSymlink, Linkname: "/", Mode: 0o777}))
	require.NoError(t, tw.Close())
	l := &LocalOrchestrator{log: zap.NewNop()}
	err := l.BuildFromArchive(context.Background(), &buf, "app", "img:1", &deployment.NixpacksCommands{}, nil, nil, func(string) {})
	assert.ErrorIs(t, err, service.ErrInvalidConfig)
}
