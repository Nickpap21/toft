package webhook

import (
	"context"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	domainPkg "github.com/Nickpap21/toft/backend/internal/domain"
	envPkg "github.com/Nickpap21/toft/backend/internal/environment"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// oneService is a ServiceFinder with one service.
type oneService struct{ svc service.Service }

func (f oneService) Get(context.Context, string) (*service.Service, error) {
	s := f.svc
	return &s, nil
}
func (oneService) FindByProjectRepoAndBranch(context.Context, string, string, string, string) ([]service.Service, error) {
	return nil, nil
}

// liveDeployment is the deployments table with the live deployment in it.
type liveDeployment struct {
	deployment.Repository
	d deployment.Deployment
}

func (r liveDeployment) GetByID(_ context.Context, id string) (*deployment.Deployment, error) {
	if id != r.d.ID {
		return nil, nil
	}
	d := r.d
	return &d, nil
}
func (liveDeployment) ListPulls(context.Context, []string) (map[string]deployment.PullRef, error) {
	return map[string]deployment.PullRef{}, nil
}

type copies bool

func (c copies) IsPreviewCopy(context.Context, string) (bool, error) { return bool(c), nil }

type noVars struct{}

// oneEnvironment is the environments table with one in it.
type oneEnvironment struct {
	envPkg.Repository
	env envPkg.Environment
}

func (r oneEnvironment) GetByID(context.Context, string) (*envPkg.Environment, error) {
	e := r.env
	return &e, nil
}

func (noVars) ListAllForDeployment(context.Context, string) ([]string, error) {
	return []string{"A=1"}, nil
}

// A temporary name's certificate redeploys the service on the image its
// live deployment runs, by Toft (D-150); a service mid-deploy, stopped, a
// preview's copy or a compose stack is left for its next deploy, and so is
// one in a protected environment or with settings saved since its live
// deployment (D-159).
func TestCertificateRedeploy(t *testing.T) {
	temp := "web-production-shop.203-0-113-10.sslip.io"
	live := deployment.Deployment{ID: "dep-live", ServiceID: "s1", ImageTag: "paas-0123-abc", CommitSHA: "abc123", CommitMessage: "Ship it"}
	running := service.Service{ID: "s1", Name: "web", SourceType: service.SourceTypeGit, Port: 8080, EnvironmentID: "e1",
		RuntimeStatus: "running", ActiveDeploymentID: "dep-live", PreDeployCommands: "make seed"}
	key := []byte("instance-hmac-key")
	// deployed is what the live deployment shipped, now the settings saved
	// since; envName's environment holds the service.
	handlerFor := func(deployed, now service.Service, copy bool, env envPkg.Environment) *Handler {
		was := deployment.NewService(nil, nil, nil, zap.NewNop())
		was.WithServiceReader(oneService{deployed})
		was.WithHashKey(key)
		fp, err := was.SettingsFingerprint(context.Background(), "s1", "", []string{"A=1"})
		require.NoError(t, err)
		require.Len(t, fp, 64)
		d := live
		d.SettingsFingerprint = fp
		deps := deployment.NewService(liveDeployment{d: d}, nil, nil, zap.NewNop())
		deps.WithPreviews(nil, nil, copies(copy))
		deps.WithServiceReader(oneService{now})
		deps.WithHashKey(key)
		return &Handler{serviceSvc: oneService{now}, deploymentSvc: deps, projects: fakeProjects{}, envVarProvider: noVars{}, log: zap.NewNop(),
			envSvc:    envPkg.NewService(oneEnvironment{env: env}),
			domainSvc: domainPkg.NewService(certDomains{domains: []domainPkg.Domain{{Hostname: temp}}}, nil, zap.NewNop())}
	}
	staging := envPkg.Environment{ID: "e1", Name: "staging"}
	handler := func(svc service.Service, copy bool) *Handler { return handlerFor(svc, svc, copy, staging) }

	in, skip := handler(running, false).certificateRedeploy(context.Background(), "s1")
	require.NotNil(t, in, skip)
	assert.Equal(t, "paas-0123-abc", in.RollbackImageTag, "the image it runs: no build, no pull")
	assert.Equal(t, "abc123", in.CommitSHA)
	assert.Empty(t, in.PreDeployCommands, "as a rollback: no pre-deploy commands")
	assert.Equal(t, deployment.OriginSystem, in.Origin)
	assert.Empty(t, in.ActorID)
	assert.Equal(t, "Toft · certificate issued: switch to HTTPS", in.TriggeredBy)
	assert.Equal(t, []string{"A=1"}, in.EnvVars)
	assert.Equal(t, []string{temp}, in.Domains)
	assert.Equal(t, "staging", in.EnvironmentName)

	// A person decides (D-159): a protected environment's deploys are
	// confirmed by one, and only a person's deploy ships settings saved
	// since the live deployment, or a deployment that recorded none.
	in, skip = handlerFor(running, running, false, envPkg.Environment{ID: "e1", Name: "production", Protected: true}).
		certificateRedeploy(context.Background(), "s1")
	assert.Nil(t, in)
	assert.Contains(t, skip, "protected")
	for name, change := range map[string]func(*service.Service){
		"a port":          func(s *service.Service) { s.Port = 9090 },
		"a memory limit":  func(s *service.Service) { s.MemoryLimit = "512m" },
		"a basic auth":    func(s *service.Service) { s.BasicAuthPassword = "hunter2" },
		"a start command": func(s *service.Service) { s.StartCommand = "serve --debug" },
	} {
		saved := running
		change(&saved)
		in, skip := handlerFor(running, saved, false, staging).certificateRedeploy(context.Background(), "s1")
		assert.Nil(t, in, name)
		assert.Contains(t, skip, "settings saved since", name)
	}
	// What a deploy doesn't ship isn't a change: its state, and settings of
	// deploys (deploy on push).
	moved := running
	moved.DeployOnPush, moved.Down, moved.LastDeployFailed = true, true, true
	in, skip = handlerFor(running, moved, false, staging).certificateRedeploy(context.Background(), "s1")
	require.NotNil(t, in, skip)
	unrecorded := handler(running, false)
	unrecorded.deploymentSvc = deployment.NewService(liveDeployment{d: live}, nil, nil, zap.NewNop())
	unrecorded.deploymentSvc.WithPreviews(nil, nil, copies(false))
	unrecorded.deploymentSvc.WithServiceReader(oneService{running})
	unrecorded.deploymentSvc.WithHashKey(key)
	in, skip = unrecorded.certificateRedeploy(context.Background(), "s1")
	assert.Nil(t, in, "a deployment from before 0072 recorded no settings")
	assert.Contains(t, skip, "not recorded")

	deploying := running
	deploying.Deploying = true
	stopped := running
	stopped.RuntimeStatus = "stopped"
	compose := running
	compose.SourceType = service.SourceTypeDockerCompose
	gone := running
	gone.ActiveDeploymentID = "dep-other"
	for name, h := range map[string]*Handler{
		"mid-deploy":     handler(deploying, false),
		"stopped":        handler(stopped, false),
		"preview copy":   handler(running, true),
		"compose stack":  handler(compose, false), // no file: it doesn't load
		"no live record": handler(gone, false),
	} {
		in, skip := h.certificateRedeploy(context.Background(), "s1")
		assert.Nil(t, in, name)
		assert.NotEmpty(t, skip, name)
	}

	// An inline stack whose every image is pinned by digest redeploys: its
	// up's pull can't change what runs. It reads TOFT_URL, as Ghost does.
	digest := "@sha256:" + strings.Repeat("a1", 32)
	stack := running
	stack.SourceType = service.SourceTypeDockerCompose
	stack.ComposeContent = "services:\n  db:\n    image: mysql:8.4" + digest + "\n  web:\n    image: ghost:6" + digest +
		"\n    environment:\n      url: \"${TOFT_URL:?Ghost needs the service’s address}\"\n"
	in, skip = handler(stack, false).certificateRedeploy(context.Background(), "s1")
	require.NotNil(t, in, skip)
	assert.Empty(t, in.RollbackImageTag, "a compose rollback brings its file up again")
	assert.Equal(t, stack.ComposeContent, in.ComposeContent)
	assert.Equal(t, deployment.OriginSystem, in.Origin)

	floating := stack
	floating.ComposeContent = "services:\n  db:\n    image: mysql:8.4" + digest + "\n  web:\n    image: ghost:6\n"
	build := stack
	build.ComposeContent = "services:\n  web:\n    build: .\n    image: shop" + digest + "\n"
	repo := stack
	repo.ComposeRepoURL = "https://example.com/infra.git"
	for name, svc := range map[string]service.Service{"a floating tag": floating, "a build": build, "from a repository": repo} {
		in, skip := handler(svc, false).certificateRedeploy(context.Background(), "s1")
		assert.Nil(t, in, name)
		assert.NotEmpty(t, skip, name)
	}
	_, skip = handler(floating, false).certificateRedeploy(context.Background(), "s1")
	assert.Contains(t, skip, "web", "names the service that isn't pinned")
}
