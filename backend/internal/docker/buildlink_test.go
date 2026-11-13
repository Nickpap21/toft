package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
		_, err := o.BuildFromGit(ctx, "https://example.com/r.git", "main", dir, "img:1", nil, log)
		assert.ErrorIs(t, err, service.ErrInvalidConfig, "BuildFromGit %q", dir)
		for _, nix := range []*deployment.NixpacksCommands{nil, {}} {
			err = o.BuildFromArchive(ctx, strings.NewReader("tar"), dir, "img:1", nix, nil, log)
			assert.ErrorIs(t, err, service.ErrInvalidConfig, "BuildFromArchive %q", dir)
		}
	}
	_, err := o.BuildFromGitNixpacks(ctx, "https://example.com/r.git", "main", "app", "img:1", deployment.NixpacksCommands{}, nil, log)
	assert.ErrorIs(t, err, service.ErrInvalidConfig, "BuildFromGitNixpacks")
	for _, c := range h.recorded() {
		assert.NotContains(t, c.cmd, "docker build", "a build ran")
		assert.NotContains(t, c.cmd, "nixpacks build", "a build ran")
	}
}

// On the panel's host, nixpacks never reads through a link out of the
// unpacked source.
func TestLocalNixpacksRefusesALinkOutOfTheBuildFolder(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "app", Typeflag: tar.TypeSymlink, Linkname: "/", Mode: 0o777}))
	require.NoError(t, tw.Close())
	l := &LocalOrchestrator{log: zap.NewNop()}
	err := l.BuildFromArchive(context.Background(), &buf, "app", "img:1", &deployment.NixpacksCommands{}, nil, func(string) {})
	assert.ErrorIs(t, err, service.ErrInvalidConfig)
}
