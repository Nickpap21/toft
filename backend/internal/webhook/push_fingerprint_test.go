package webhook

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/code"
	"github.com/Nickpap21/toft/backend/internal/deployment"
	domainPkg "github.com/Nickpap21/toft/backend/internal/domain"
	envPkg "github.com/Nickpap21/toft/backend/internal/environment"
	"github.com/Nickpap21/toft/backend/internal/netplan"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	serverPkg "github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// pushFinder finds the service as the push's query does, without the
// fields only Get reads, and Gets it whole.
type pushFinder struct{ found, full service.Service }

func (f pushFinder) Get(context.Context, string) (*service.Service, error) {
	s := f.full
	return &s, nil
}
func (f pushFinder) FindByProjectRepoAndBranch(context.Context, string, string, string, string) ([]service.Service, error) {
	return []service.Service{f.found}, nil
}

// deployments is the deployments table for one deploy: it records what the
// deploy writes and gives it back as the service's live deployment.
type deployments struct {
	deployment.Repository
	mu sync.Mutex
	d  deployment.Deployment
}

func (r *deployments) Create(_ context.Context, serviceID, _, _ string, _ int64) (*deployment.Deployment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.d.ServiceID = serviceID
	return &deployment.Deployment{ID: r.d.ID, ServiceID: serviceID, Status: deployment.StatusPending}, nil
}
func (r *deployments) UpdateStatus(_ context.Context, _ string, status deployment.Status, imageTag, commitSHA, _ string) (*deployment.Deployment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.d.Status = status
	if imageTag != "" {
		r.d.ImageTag = imageTag
	}
	if commitSHA != "" {
		r.d.CommitSHA = commitSHA
	}
	d := r.d
	return &d, nil
}
func (r *deployments) SetSettingsFingerprint(_ context.Context, _, fp string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.d.SettingsFingerprint = fp
	return nil
}
func (r *deployments) GetByID(context.Context, string) (*deployment.Deployment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.d
	return &d, nil
}
func (r *deployments) InsertLog(_ context.Context, _, msg string) (*deployment.Log, error) {
	return &deployment.Log{Message: msg}, nil
}
func (r *deployments) SetCommit(context.Context, string, string, string) error { return nil }
func (r *deployments) ListPulls(context.Context, []string) (map[string]deployment.PullRef, error) {
	return map[string]deployment.PullRef{}, nil
}
func (r *deployments) LatestIDForService(context.Context, string) (string, error) { return r.d.ID, nil }
func (r *deployments) DeleteOldDeploymentsForService(context.Context, string, int) error {
	return nil
}

// builds is a Docker that builds and rolls out anything.
type builds struct{ deployment.Orchestrator }

func (builds) PrepareNetworks(context.Context, netplan.Plan, func(string)) error { return nil }
func (builds) BuildFromGit(_ context.Context, _, _, _, _ string, _ map[string]*string, _ func(string)) (deployment.Commit, error) {
	return deployment.Commit{SHA: "abc123", Subject: "Ship it"}, nil
}
func (builds) Rollout(context.Context, rollout.Spec, deployment.Routing, rollout.Hooks) rollout.Result {
	return rollout.Result{Changed: true, New: "web-blue"}
}

type onlyDocker struct{}

func (onlyDocker) ForServer(*serverPkg.Server) (deployment.Orchestrator, func(), error) {
	return builds{}, func() {}, nil
}

type runtimeStatus struct{}

func (runtimeStatus) UpdateRuntimeStatus(context.Context, string, string, string) error { return nil }
func (runtimeStatus) SwapDockerImage(context.Context, string, string, string) error     { return nil }
func (runtimeStatus) SetComposeNetworkService(context.Context, string, string) error    { return nil }

// A push deploy records the fingerprint of what it shipped, and it matches
// the service's settings now (D-159): once its temporary name's
// certificate is out, Toft redeploys it for HTTPS on its own.
func TestPushDeployRecordsMatchingFingerprint(t *testing.T) {
	temp := "web-staging-shop.203-0-113-10.sslip.io"
	full := service.Service{ID: "s1", Name: "web", ProjectID: projectA, EnvironmentID: "e1", SourceType: service.SourceTypeGit,
		GitRepoURL: "https://example.com/shop.git", GitBranch: "main", Port: 8080, MemoryLimit: "512m", DeployOnPush: true,
		RuntimeStatus: "running", ActiveDeploymentID: "dep-push", PidsLimit: 256}
	found := full
	found.PidsLimit = 0 // the find leaves it out; Get reads it

	repo := &deployments{d: deployment.Deployment{ID: "dep-push"}}
	deps := deployment.NewService(repo, onlyDocker{}, runtimeStatus{}, zap.NewNop())
	deps.WithServiceReader(oneService{full})
	deps.WithPreviews(nil, nil, copies(false))
	deps.WithHashKey([]byte("instance-hmac-key"))
	h := &Handler{serviceSvc: pushFinder{found: found, full: full}, deploymentSvc: deps, projects: fakeProjects{},
		envVarProvider: noVars{}, log: zap.NewNop(),
		envSvc:    envPkg.NewService(oneEnvironment{env: envPkg.Environment{ID: "e1", Name: "staging"}}),
		domainSvc: domainPkg.NewService(certDomains{domains: []domainPkg.Domain{{Hostname: temp}}}, nil, zap.NewNop())}

	require.NoError(t, h.DeployPush(context.Background(), code.Push{ProjectID: projectA, RepositoryID: "r1", Branch: "main",
		SHA: "abc123", Subject: "Ship it", EventID: 7}))
	require.Eventually(t, func() bool {
		d, _ := repo.GetByID(context.Background(), "dep-push")
		return d.SettingsFingerprint != ""
	}, 2*time.Second, 10*time.Millisecond, "the push deploy went live and recorded its settings")

	live, _ := repo.GetByID(context.Background(), "dep-push")
	assert.Equal(t, deployment.StatusRunning, live.Status)
	now, err := deps.SettingsFingerprint(context.Background(), "s1", "", []string{"A=1"})
	require.NoError(t, err)
	assert.Equal(t, now, live.SettingsFingerprint)
	in, skip := h.certificateRedeploy(context.Background(), "s1")
	require.NotNil(t, in, skip)
	assert.Equal(t, deployment.OriginSystem, in.Origin)
}
