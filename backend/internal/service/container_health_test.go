package service

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestValidateContainerHealth(t *testing.T) {
	ok := []struct {
		mode, command string
		source        SourceType
	}{
		{"", "", SourceTypeGit},
		{"image", "", SourceTypeGit},
		{"off", "", SourceTypeDockerImage},
		{"command", "pgrep -f worker", SourceTypeGit},
		{"command", strings.Repeat("x", 1024), SourceTypeGit},
		{"command", strings.Repeat("é", 1024), SourceTypeGit}, // characters, not bytes
		{"image", "", SourceTypeDockerCompose},
		{"", "", SourceTypeDockerCompose},
	}
	for _, tc := range ok {
		assert.NoError(t, validateContainerHealth(tc.mode, tc.command, tc.source), "%+v", tc)
	}
	bad := []struct {
		mode, command string
		source        SourceType
	}{
		{"none", "", SourceTypeGit},
		{"Image", "", SourceTypeGit},
		{"command", "", SourceTypeGit},
		{"command", "   ", SourceTypeGit},
		{"command", strings.Repeat("x", 1025), SourceTypeGit},
		{"command", "true\nrm -rf /", SourceTypeGit},
		{"command", "true\r", SourceTypeGit},
		{"command", "true\x00", SourceTypeGit},
		{"off", "", SourceTypeDockerCompose},
		{"command", "true", SourceTypeDockerCompose},
	}
	for _, tc := range bad {
		assert.ErrorIs(t, validateContainerHealth(tc.mode, tc.command, tc.source), ErrInvalidConfig, "%+v", tc)
	}
}

func TestUpdateConfig_ContainerHealth(t *testing.T) {
	ptr := func(s string) *string { return &s }
	ctx := context.Background()

	t.Run("a command is stored and read back", func(t *testing.T) {
		repo := &fakeRepo{svc: &Service{ID: "s1", Name: "worker", SourceType: SourceTypeGit}}
		m := NewServiceManager(repo, zap.NewNop())
		svc, err := m.UpdateConfig(ctx, "s1", UpdateConfigInput{ContainerHealth: ptr("command"), ContainerHealthCommand: ptr("pgrep -f worker")})
		require.NoError(t, err)
		assert.Equal(t, "command", repo.checkMode)
		assert.Equal(t, "pgrep -f worker", repo.checkCommand)
		assert.Equal(t, "command", svc.ContainerHealth)
		assert.Equal(t, "pgrep -f worker", svc.ContainerHealthCommand)
	})

	t.Run("the mode alone keeps the stored command", func(t *testing.T) {
		repo := &fakeRepo{svc: &Service{ID: "s1", Name: "worker", SourceType: SourceTypeGit}, checkMode: "off", checkCommand: "pgrep -f worker"}
		m := NewServiceManager(repo, zap.NewNop())
		_, err := m.UpdateConfig(ctx, "s1", UpdateConfigInput{ContainerHealth: ptr("command")})
		require.NoError(t, err)
		assert.Equal(t, "command", repo.checkMode)
		assert.Equal(t, "pgrep -f worker", repo.checkCommand)
	})

	t.Run("command mode without a command is refused and nothing is saved", func(t *testing.T) {
		repo := &fakeRepo{svc: &Service{ID: "s1", Name: "worker", SourceType: SourceTypeGit}, checkMode: "image"}
		m := NewServiceManager(repo, zap.NewNop())
		_, err := m.UpdateConfig(ctx, "s1", UpdateConfigInput{ContainerHealth: ptr("command")})
		require.ErrorIs(t, err, ErrInvalidConfig)
		assert.Equal(t, "image", repo.checkMode)
		assert.Equal(t, 0, repo.updated)
	})

	t.Run("a compose stack keeps the image's", func(t *testing.T) {
		repo := &fakeRepo{svc: &Service{ID: "s1", Name: "stack", SourceType: SourceTypeDockerCompose}}
		m := NewServiceManager(repo, zap.NewNop())
		_, err := m.UpdateConfig(ctx, "s1", UpdateConfigInput{ContainerHealth: ptr("off")})
		require.ErrorIs(t, err, ErrInvalidConfig)
		assert.Equal(t, "", repo.checkMode)
	})

	t.Run("absent leaves it alone", func(t *testing.T) {
		repo := &fakeRepo{svc: &Service{ID: "s1", Name: "worker", SourceType: SourceTypeGit}, checkMode: "off"}
		m := NewServiceManager(repo, zap.NewNop())
		_, err := m.UpdateConfig(ctx, "s1", UpdateConfigInput{Name: "worker"})
		require.NoError(t, err)
		assert.Equal(t, "off", repo.checkMode)
	})
}
