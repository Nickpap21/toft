package docker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Nickpap21/toft/backend/internal/service"
)

// A build folder that is itself a link to another folder of the
// repository (base_directory "app", with app -> sub) is checked through
// the link: a link under sub that leaves it is refused as it would be
// without one.
func TestNoLinksOutThroughALinkedBuildFolder(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(repo, "sub"), 0o700))
	require.NoError(t, os.Symlink("sub", filepath.Join(repo, "app")))

	require.NoError(t, os.WriteFile(filepath.Join(repo, "sub", "package.json"), []byte("{}"), 0o600))
	require.NoError(t, os.Symlink("package.json", filepath.Join(repo, "sub", "inside")))
	require.NoError(t, noLinksOut(filepath.Join(repo, "app")), "a link that stays in the folder is fine")

	require.NoError(t, os.Symlink("/proc/1/environ", filepath.Join(repo, "sub", "Procfile")))
	err := noLinksOut(filepath.Join(repo, "app"))
	require.ErrorIs(t, err, service.ErrInvalidConfig)
	assert.ErrorContains(t, err, "Procfile is a link that doesn't stay in the build folder")
	require.ErrorIs(t, noLinksOut(filepath.Join(repo, "sub")), service.ErrInvalidConfig, "and the folder itself, as before")
}
