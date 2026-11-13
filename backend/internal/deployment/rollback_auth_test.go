package deployment_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// authPulls is a host that records the images it pulled with a password.
type authPulls struct {
	*mockOrchestrator
	mu     sync.Mutex
	pulled []string
}

func (o *authPulls) Pull(_ context.Context, image string, auth *rollout.RegistryAuth, _ func(string)) error {
	if auth != nil && auth.Password != "" {
		o.mu.Lock()
		o.pulled = append(o.pulled, image)
		o.mu.Unlock()
	}
	return nil
}

func (o *authPulls) Rollout(ctx context.Context, spec rollout.Spec, _ deployment.Routing, hooks rollout.Hooks) rollout.Result {
	return rollout.Run(ctx, o, spec, hooks)
}

// Every rollback, the handler's and the certificate redeploy's, pulls with
// the saved registry login only an image on the service's own registry: an
// old deployment of an image on another host gets none (SEC-011). The
// migrate command pulls the image with the login when there is one.
func TestRollbackPullsWithTheSavedLoginOnlyOnItsRegistry(t *testing.T) {
	saved := &service.Service{SourceType: service.SourceTypeDockerImage, DockerImage: "ghcr.io/acme/app:2",
		RegistryServer: "ghcr.io", RegistryUsername: "u", RegistryPassword: "saved"}
	login := &deployment.RegistryAuth{Server: "ghcr.io", Username: "u", Password: "saved"}
	// handler is the rollback handler's input: no DockerImage.
	handler := func(image string) deployment.TriggerInput {
		return deployment.TriggerInput{SourceType: "docker-image", RollbackImageTag: image, RegistryAuth: login, Settings: saved}
	}
	// certificate is the certificate redeploy's: the service's whole
	// deploy input, run again as a rollback to the live image.
	certificate := func(image string) deployment.TriggerInput {
		return deployment.TriggerInput{SourceType: "docker-image", DockerImage: saved.DockerImage, RollbackImageTag: image,
			RegistryAuth: login, Settings: saved, Origin: deployment.OriginSystem}
	}
	build := &service.Service{SourceType: service.SourceTypeGit, GitRepoURL: "https://example.com/app.git", RegistryPassword: "saved"}
	hub := &service.Service{SourceType: service.SourceTypeDockerImage, DockerImage: "acme/app:2", RegistryPassword: "saved"}
	hubRollback := func(image string) deployment.TriggerInput {
		return deployment.TriggerInput{SourceType: "docker-image", RollbackImageTag: image, RegistryAuth: login, Settings: hub}
	}

	for name, tc := range map[string]struct {
		in    deployment.TriggerInput
		login bool
	}{
		"a rollback to another registry":                        {handler("evil.example/x:1"), false},
		"a rollback to Docker Hub":                              {handler("nginx:1.27"), false},
		"a rollback to a host named like the registry":          {handler("ghcr.io.evil.example/acme/app:1"), false},
		"a rollback to an uppercase host":                       {handler("Evil/x:1"), false},
		"a rollback on the same registry":                       {handler("ghcr.io/acme/app:1"), true},
		"a rollback by digest on the same registry":             {handler("ghcr.io/acme/app@sha256:0123"), true},
		"the certificate redeploy of another registry's image":  {certificate("evil.example/x:1"), false},
		"the certificate redeploy of the same registry's image": {certificate("ghcr.io/acme/app:1"), true},
		"a rollback without the service it was built from":      {deployment.TriggerInput{SourceType: "docker-image", RollbackImageTag: "ghcr.io/acme/app:1", RegistryAuth: login}, false},
		"a build's rollback, whose tag names no registry": {deployment.TriggerInput{SourceType: "git", GitRepoURL: build.GitRepoURL,
			RollbackImageTag: "paas-web-1234abcd:abc123", RegistryAuth: login, Settings: build}, true},
		// Docker reads an uppercase first component as a host, never as a
		// Docker Hub user; docker.io and index.docker.io are Docker Hub.
		"a Docker Hub service's rollback to an uppercase host": {hubRollback("Evil/x:1"), false},
		"a Docker Hub service's rollback to docker.io":         {hubRollback("docker.io/acme/app:1"), true},
		"a Docker Hub service's rollback to index.docker.io":   {hubRollback("index.docker.io/library/nginx:1.27"), true},
	} {
		t.Run(name, func(t *testing.T) {
			repo := &mockRepo{createdID: "dep-1"}
			orch := &authPulls{mockOrchestrator: &mockOrchestrator{}}
			svc := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
			in := tc.in
			in.ServiceID, in.Port, in.MigrateCommand = "svc-1", 80, "migrate"
			_, err := svc.Trigger(context.Background(), in)
			require.NoError(t, err)
			waitForStatus(t, repo, deployment.StatusRunning, 2*time.Second)

			orch.mu.Lock()
			defer orch.mu.Unlock()
			if tc.login {
				assert.Equal(t, []string{in.RollbackImageTag}, orch.pulled, "the migrate pull has the login")
			} else {
				assert.Empty(t, orch.pulled, "no pull with the saved login")
			}
		})
	}
}
