package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func intPtr(n int) *int { return &n }

// A new web app checks its health over HTTP from its first deploy (issue
// #12): GET / every attempt, five attempts. Workers, databases, caches,
// admin tools, catalog add-ons, compose stacks and an app with no port to
// ask don't; an explicit 0 at create keeps it off.
func TestCreateTurnsTheHealthCheckOnForWebApps(t *testing.T) {
	ctx := context.Background()
	create := func(in CreateInput) *CreateInput {
		t.Helper()
		repo := &recordingRepo{}
		in.Name = "svc"
		_, err := NewServiceManager(repo, zap.NewNop()).Create(ctx, "env-1", "", in)
		require.NoError(t, err)
		return repo.lastCreate
	}
	retries := func(in *CreateInput) int {
		if in.HealthCheckRetries == nil {
			return 0
		}
		return *in.HealthCheckRetries
	}

	for name, in := range map[string]CreateInput{
		"git app":   {Type: TypeWeb, SourceType: SourceTypeGit, GitRepoURL: "https://github.com/acme/web.git", Port: 3000},
		"image app": {Type: TypeWeb, SourceType: SourceTypeDockerImage, DockerImage: "ghcr.io/acme/api:1", Port: 8080},
	} {
		got := create(in)
		assert.Equal(t, 5, retries(got), name)
		assert.Equal(t, "/", got.HealthCheckPath, name)
	}
	for name, in := range map[string]CreateInput{
		"worker":   {Type: TypeWorker, SourceType: SourceTypeDockerImage, DockerImage: "ghcr.io/acme/jobs:1"},
		"database": {Type: TypeWeb, SourceType: SourceTypeDockerImage, DockerImage: "postgres:16"},
		"cache":    {Type: TypeWeb, SourceType: SourceTypeDockerImage, DockerImage: "redis:7"},
		"add-on":   {Type: TypeWeb, SourceType: SourceTypeDockerImage, DockerImage: "ghcr.io/acme/addon:1", AddonSlug: "acme-addon"},
		"compose":  {Type: TypeWeb, SourceType: SourceTypeDockerCompose, ComposeContent: "services:\n  web:\n    image: nginx\n"},
		"off":      {Type: TypeWeb, SourceType: SourceTypeDockerImage, DockerImage: "ghcr.io/acme/api:1", Port: 8080, HealthCheckRetries: intPtr(0)},
		"no port":  {Type: TypeWeb, SourceType: SourceTypeDockerImage, DockerImage: "ghcr.io/acme/api:1"},
	} {
		assert.Equal(t, 0, retries(create(in)), name)
	}
	got := create(CreateInput{Type: TypeWeb, SourceType: SourceTypeDockerImage, DockerImage: "ghcr.io/acme/api:1",
		HealthCheckPath: "/healthz", HealthCheckRetries: intPtr(2)})
	assert.Equal(t, 2, retries(got), "what the caller sends is kept")
	assert.Equal(t, "/healthz", got.HealthCheckPath)
}

// The ranges the panel's inputs show are the API's too: timeout 1–60 s (0
// is the default, 5), attempts 0–10, a path that starts with "/", and an
// answer below 400 or below 500 passes.
func TestHealthCheckRangesAreEnforced(t *testing.T) {
	ctx := context.Background()
	bad := map[string]func(*CreateInput, *UpdateConfigInput){
		"timeout -1": func(c *CreateInput, u *UpdateConfigInput) { c.HealthCheckTimeout, u.HealthCheckTimeout = -1, -1 },
		"timeout 61": func(c *CreateInput, u *UpdateConfigInput) { c.HealthCheckTimeout, u.HealthCheckTimeout = 61, 61 },
		"retries -1": func(c *CreateInput, u *UpdateConfigInput) {
			c.HealthCheckRetries, u.HealthCheckRetries = intPtr(-1), -1
		},
		"retries 11": func(c *CreateInput, u *UpdateConfigInput) {
			c.HealthCheckRetries, u.HealthCheckRetries = intPtr(11), 11
		},
		"path without /":   func(c *CreateInput, u *UpdateConfigInput) { c.HealthCheckPath, u.HealthCheckPath = "health", "health" },
		"pass below 450":   func(_ *CreateInput, u *UpdateConfigInput) { u.HealthCheckPassBelow = intPtr(450) },
		"pass below 0 (u)": func(_ *CreateInput, u *UpdateConfigInput) { u.HealthCheckPassBelow = intPtr(0) },
	}
	for name, set := range bad {
		c := CreateInput{Name: "web", Type: TypeWeb, SourceType: SourceTypeDockerImage, DockerImage: "ghcr.io/acme/api:1"}
		u := UpdateConfigInput{Port: 3000}
		set(&c, &u)
		if c.HealthCheckTimeout != 0 || c.HealthCheckRetries != nil || c.HealthCheckPath != "" {
			repo := &recordingRepo{}
			_, err := NewServiceManager(repo, zap.NewNop()).Create(ctx, "env-1", "", c)
			assert.ErrorIs(t, err, ErrInvalidConfig, "create: %s", name)
			assert.Nil(t, repo.lastCreate, "create: %s", name)
		}
		repo := &recordingRepo{fakeRepo: fakeRepo{svc: &Service{ID: "s1", Name: "web"}}}
		_, err := NewServiceManager(repo, zap.NewNop()).UpdateConfig(ctx, "s1", u)
		assert.ErrorIs(t, err, ErrInvalidConfig, "update: %s", name)
		assert.Nil(t, repo.lastUpdate, "update: %s", name)
	}

	for _, ok := range []UpdateConfigInput{
		{Port: 3000},
		{Port: 3000, HealthCheckTimeout: 1, HealthCheckRetries: 10, HealthCheckPath: "/", HealthCheckPassBelow: intPtr(400)},
		{Port: 3000, HealthCheckTimeout: 60, HealthCheckRetries: 0, HealthCheckPath: "/up?full=1", HealthCheckPassBelow: intPtr(500)},
	} {
		repo := &recordingRepo{fakeRepo: fakeRepo{svc: &Service{ID: "s1", Name: "web"}}}
		_, err := NewServiceManager(repo, zap.NewNop()).UpdateConfig(ctx, "s1", ok)
		require.NoError(t, err, "%+v", ok)
		if ok.HealthCheckTimeout == 0 {
			assert.Equal(t, 5, repo.lastUpdate.HealthCheckTimeout, "0 saves the default")
		}
	}
}
