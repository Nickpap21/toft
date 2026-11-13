package instance_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/advisory"
	"github.com/Nickpap21/toft/backend/internal/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// The panel's own server's updates are read through the host helper with a
// server's script (D-133); a failed read keeps what the last one read, with
// why; the alerts judge it with Docker's advisories.
func TestPanelUpdates(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo(ada, bob)
	repo.operators[ada.id] = "first_run"
	out, fail := "apt:1\nInst liblzma5 [1] (2 Ubuntu:24.04/noble-updates, Ubuntu:24.04/noble-security [arm64])\n"+
		"lists:1000\nuurun:2000\nuuok:2000\nuuunit:loaded active\nreboot:1500\ndocker:29.4.3\nnow:3000\n", error(nil)
	probe := &fakeProbe{host: func(argv []string, _ io.Reader, _ func(string)) (string, error) {
		require.Equal(t, []string{"sh", "-c"}, argv[:2])
		require.Contains(t, argv[2], "| base64 -d | sh")
		return out, fail
	}}
	svc := instance.NewService(repo, zap.NewNop()).WithPanelMonitor(probe, &fakeStore{}, t.TempDir(), t.TempDir())
	svc.WithAdvisories(advisory.New("", zap.NewNop()))

	_, err := svc.CheckPanelUpdates(ctx, bob.id)
	require.ErrorIs(t, err, instance.ErrNotOperator)
	_, err = svc.PanelUpdates(ctx, bob.id)
	require.ErrorIs(t, err, instance.ErrNotOperator)
	u, err := svc.PanelUpdates(ctx, ada.id)
	require.NoError(t, err)
	assert.Nil(t, u, "never read")
	hs, err := svc.UpdateProblems(ctx)
	require.NoError(t, err)
	assert.Empty(t, hs)

	u, err = svc.CheckPanelUpdates(ctx, ada.id)
	require.NoError(t, err)
	assert.Equal(t, 1, u.Security)
	assert.True(t, u.Reboot.Required)
	assert.Equal(t, "29.4.3", u.Docker.Version)
	assert.Len(t, u.Docker.Advisories, 6)
	assert.Empty(t, u.Error)

	hs, err = svc.UpdateProblems(ctx)
	require.NoError(t, err)
	require.Len(t, hs, 1)
	assert.Equal(t, "", hs[0].ServerID, "the panel's server")
	assert.Equal(t, "1 security update waiting: unattended-upgrades ran since the package lists came down and didn't install them", hs[0].Security)
	assert.NotEmpty(t, hs[0].Reboot)
	assert.NotEmpty(t, hs[0].Docker)

	fail = errors.New("helper exited 1")
	u, err = svc.CheckPanelUpdates(ctx, ada.id)
	require.NoError(t, err)
	assert.Equal(t, 1, u.Security, "the last read is kept")
	require.NotNil(t, u.ReadAt)
	assert.True(t, u.CheckedAt.After(*u.ReadAt), "checked since, read before")
	assert.Contains(t, u.Error, "helper exited 1")
}
