package deployment_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/envvar"
	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// QA #11: Deploy a version that fails, a rollback, Stop, Deploy. The failed
// version doesn't stay the saved image, the rollback's becomes it, and the
// Deploy that follows brings the stopped service back up on what was live.
func TestSavedImageFollowsWhatWentLive(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	var orgID, serviceID string
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`).Scan(&orgID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO services (scope, scoped_org_id, name, source_type, docker_image, port)
		VALUES ('org', $1, 'whoami', 'docker-image', 'traefik/whoami:v1.10', 80) RETURNING id::text`, orgID).Scan(&serviceID))

	mgr := service.NewServiceManager(service.NewPostgresRepository(db, crypto.NewKeys("test-encryption-key", "")), zap.NewNop())
	repo := deployment.NewPostgresRepository(db)
	hosts := &swapFactory{orch: &mockOrchestrator{}}
	deploys := deployment.NewService(repo, hosts, mgr, zap.NewNop())

	saved := func() *service.Service {
		s, err := mgr.Get(ctx, serviceID)
		require.NoError(t, err)
		return s
	}
	// savedIs waits for the service's saved image, which follows the
	// deploy's status.
	savedIs := func(image, msg string) {
		t.Helper()
		require.Eventually(t, func() bool { return saved().DockerImage == image }, 5*time.Second, 10*time.Millisecond, msg)
	}
	// deploy runs in as the handler would, and waits for its status.
	deploy := func(in deployment.TriggerInput, want deployment.Status) *deployment.Deployment {
		t.Helper()
		in.ServiceID, in.SourceType, in.Port = serviceID, "docker-image", 80
		in.Settings = saved()
		d, err := deploys.Trigger(ctx, in)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			got, err := repo.GetByID(ctx, d.ID)
			return err == nil && got.Status == want
		}, 5*time.Second, 10*time.Millisecond)
		got, err := repo.GetByID(ctx, d.ID)
		require.NoError(t, err)
		return got
	}

	v110 := deploy(deployment.TriggerInput{DockerImage: "traefik/whoami:v1.10"}, deployment.StatusRunning)
	_, err := mgr.UpdateDockerImage(ctx, serviceID, "traefik/whoami:v1.12.0")
	require.NoError(t, err)
	deploy(deployment.TriggerInput{DockerImage: "traefik/whoami:v1.12.0"}, deployment.StatusRunning)

	// Deploy a version: the handler saves the tag, then the deploy fails.
	_, err = mgr.UpdateDockerImage(ctx, serviceID, "traefik/whoami:v1.11-amd64")
	require.NoError(t, err)
	hosts.set(&mockOrchestrator{runErr: errors.New("exec format error")})
	deploy(deployment.TriggerInput{DockerImage: "traefik/whoami:v1.11-amd64"}, deployment.StatusFailed)
	hosts.set(&mockOrchestrator{})
	savedIs("traefik/whoami:v1.12.0", "a version whose deploy failed is not what Deploy runs next")

	deploy(deployment.TriggerInput{RollbackImageTag: v110.ImageTag}, deployment.StatusRunning)
	savedIs("traefik/whoami:v1.10", "the rollback's image is the saved one")

	require.NoError(t, mgr.Stop(ctx, serviceID))
	again := deploy(deployment.TriggerInput{DockerImage: saved().DockerImage}, deployment.StatusRunning)
	require.Equal(t, "traefik/whoami:v1.10", again.ImageTag)
	require.Eventually(t, func() bool {
		s := saved()
		return s.RuntimeStatus == "running" && s.ActiveDeploymentID == again.ID
	}, 5*time.Second, 10*time.Millisecond, "Deploy brings a stopped service back up")

	require.NoError(t, mgr.SwapDockerImage(ctx, serviceID, "traefik/whoami:v1.12.0", "traefik/whoami:v1.11-amd64"))
	require.Equal(t, "traefik/whoami:v1.10", saved().DockerImage, "a save made since the deploy started stays")

	// Stopped, so nothing is live: a failed version goes back to the latest
	// that went live.
	require.NoError(t, mgr.Stop(ctx, serviceID))
	_, err = mgr.UpdateDockerImage(ctx, serviceID, "traefik/whoami:v1.11-amd64")
	require.NoError(t, err)
	hosts.set(&mockOrchestrator{runErr: errors.New("exec format error")})
	deploy(deployment.TriggerInput{DockerImage: "traefik/whoami:v1.11-amd64"}, deployment.StatusFailed)
	savedIs("traefik/whoami:v1.10", "the latest successful version, when nothing is live")
}

// An image saved with another registry's server, username and password
// stays saved with them when its first deploy fails: the saved image
// never goes back across registries, whose credentials the swap can't
// know. A password saved since a Deploy a version is never replaced by
// the one its put-back restores.
func TestSavedImageNeverFollowsAcrossRegistries(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	keys := crypto.NewKeys("test-encryption-key", "")
	var orgID, serviceID string
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`).Scan(&orgID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO services (scope, scoped_org_id, name, source_type, docker_image, port, registry_server, registry_username)
		VALUES ('org', $1, 'api', 'docker-image', 'registry.a.example/api:1', 80, 'registry.a.example', 'ua') RETURNING id::text`, orgID).Scan(&serviceID))
	// save is a Settings save of image, registry server, username and password.
	save := func(image, server, username, password string) {
		t.Helper()
		sealed, err := keys.Seal(crypto.AD("services.registry_password", serviceID), password)
		require.NoError(t, err)
		_, err = db.Exec(ctx, `UPDATE services SET docker_image = $2, registry_server = $3, registry_username = $4, registry_password = $5 WHERE id = $1`,
			serviceID, image, server, username, sealed)
		require.NoError(t, err)
	}
	save("registry.a.example/api:1", "registry.a.example", "ua", "pa")

	mgr := service.NewServiceManager(service.NewPostgresRepository(db, keys), zap.NewNop())
	repo := deployment.NewPostgresRepository(db)
	hosts := &swapFactory{orch: &mockOrchestrator{}}
	deploys := deployment.NewService(repo, hosts, mgr, zap.NewNop())
	saved := func() []string {
		s, err := mgr.Get(ctx, serviceID)
		require.NoError(t, err)
		return []string{s.DockerImage, s.RegistryServer, s.RegistryUsername, s.RegistryPassword}
	}
	deploy := func(want deployment.Status) {
		t.Helper()
		s, err := mgr.Get(ctx, serviceID)
		require.NoError(t, err)
		d, err := deploys.Trigger(ctx, deployment.TriggerInput{ServiceID: serviceID, SourceType: "docker-image", Port: 80, DockerImage: s.DockerImage, Settings: s})
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			got, err := repo.GetByID(ctx, d.ID)
			return err == nil && got.Status == want
		}, 5*time.Second, 10*time.Millisecond)
	}

	deploy(deployment.StatusRunning)
	save("registry.b.example/api:2", "registry.b.example", "ub", "pb")
	hosts.set(&mockOrchestrator{runErr: errors.New("pull access denied")})
	deploy(deployment.StatusFailed)
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, []string{"registry.b.example/api:2", "registry.b.example", "ub", "pb"}, saved(),
		"the image saved with its registry's credentials stays, with them")

	// A Deploy a version of another registry's image cleared the password;
	// one saved since stays, and so does the image it was saved with.
	_, err := mgr.UpdateDockerImage(ctx, serviceID, "registry.c.example/api:3")
	require.NoError(t, err)
	save("registry.c.example/api:3", "registry.c.example", "uc", "pc")
	require.NoError(t, mgr.PutBackDockerImage(ctx, serviceID, "registry.c.example/api:3", "registry.b.example/api:2", "pb"))
	require.Equal(t, []string{"registry.c.example/api:3", "registry.c.example", "uc", "pc"}, saved())
}

// A compose stack's own IMAGE_TAG (what Deploy a version saves) goes back
// to the live deployment's when a deploy of another fails; an IMAGE_TAG
// only the environment sets never gets a service-level copy.
func TestComposeImageTagFollowsWhatWentLive(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	keys := crypto.NewKeys("test-encryption-key", "")
	var orgID, serviceID string
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`).Scan(&orgID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO services (scope, scoped_org_id, name, source_type, compose_content)
		VALUES ('org', $1, 'stack', 'docker-compose', $2) RETURNING id::text`, orgID, "services:\n  web:\n    image: nginx:${IMAGE_TAG}\n").Scan(&serviceID))

	mgr := service.NewServiceManager(service.NewPostgresRepository(db, keys), zap.NewNop())
	vars := envvar.NewService(envvar.NewPostgresRepository(db, keys), zap.NewNop())
	repo := deployment.NewPostgresRepository(db)
	hosts := &swapFactory{orch: &mockOrchestrator{}}
	deploys := deployment.NewService(repo, hosts, mgr, zap.NewNop())
	deploys.WithEnvVarSwapper(vars)

	own := func() string {
		list, err := vars.List(ctx, serviceID)
		require.NoError(t, err)
		for _, v := range list {
			if v.Key == "IMAGE_TAG" {
				return v.Value
			}
		}
		return ""
	}
	deploy := func(tag string, want deployment.Status) {
		t.Helper()
		s, err := mgr.Get(ctx, serviceID)
		require.NoError(t, err)
		d, err := deploys.Trigger(ctx, deployment.TriggerInput{
			ServiceID: serviceID, ServiceName: "stack", SourceType: "docker-compose",
			ComposeContent: s.ComposeContent, EnvVars: []string{"IMAGE_TAG=" + tag}, Settings: s,
		})
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			got, err := repo.GetByID(ctx, d.ID)
			return err == nil && got.Status == want
		}, 5*time.Second, 10*time.Millisecond)
	}

	_, _, err := vars.Set(ctx, serviceID, "IMAGE_TAG", "1.27")
	require.NoError(t, err)
	deploy("1.27", deployment.StatusRunning)

	_, _, err = vars.Set(ctx, serviceID, "IMAGE_TAG", "1.28-bad")
	require.NoError(t, err)
	hosts.set(&mockOrchestrator{composeErr: errors.New("manifest unknown")})
	deploy("1.28-bad", deployment.StatusFailed)
	require.Eventually(t, func() bool { return own() == "1.27" }, 5*time.Second, 10*time.Millisecond,
		"the service's own IMAGE_TAG goes back to the live one")

	require.NoError(t, vars.Delete(ctx, serviceID, "IMAGE_TAG"))
	deploy("1.28-bad", deployment.StatusFailed) // the environment's IMAGE_TAG
	time.Sleep(100 * time.Millisecond)
	require.Empty(t, own(), "no service-level IMAGE_TAG is created")
}

// swapFactory hands out one host, which a test replaces between deploys.
type swapFactory struct {
	mu   sync.Mutex
	orch deployment.Orchestrator
}

func (f *swapFactory) set(o deployment.Orchestrator) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.orch = o
}

func (f *swapFactory) ForServer(*server.Server) (deployment.Orchestrator, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.orch, func() {}, nil
}
