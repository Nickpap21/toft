package deployment_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// deletingRepo is the part of service.Repository a delete uses.
type deletingRepo struct{ service.Repository }

func (deletingRepo) GetByID(_ context.Context, id string) (*service.Service, error) {
	return &service.Service{ID: id, Name: "web"}, nil
}
func (deletingRepo) ListCopies(context.Context, string) ([]string, error) { return nil, nil }
func (deletingRepo) Delete(context.Context, string) error                 { return nil }

// stoppableBuild is an orchestrator whose build runs until its context
// ends, and then takes a moment to stop, or until finish is closed, when
// it succeeds.
type stoppableBuild struct {
	*mockOrchestrator
	building chan struct{}
	finish   chan struct{}
	mu       sync.Mutex
	cause    error // the build context's cause, once it ended
}

func (b *stoppableBuild) BuildFromGit(ctx context.Context, _, _, _, _ string, _ map[string]*string, logFn func(string)) (deployment.Commit, error) {
	logFn("Building image...")
	close(b.building)
	select {
	case <-ctx.Done():
		b.mu.Lock()
		b.cause = context.Cause(ctx)
		b.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		return deployment.Commit{}, ctx.Err()
	case <-b.finish:
		return deployment.Commit{}, nil
	}
}

func TestDelete_CancelsTheServicesDeployAndWaitsForIt(t *testing.T) {
	repo := &deadRepo{mockRepo: &mockRepo{createdID: "dep-build"}}
	orch := &stoppableBuild{mockOrchestrator: &mockOrchestrator{}, building: make(chan struct{}), finish: make(chan struct{})}
	deploys := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
	_, err := deploys.Trigger(context.Background(), deployment.TriggerInput{
		ServiceID: "svc-1", SourceType: "git", GitRepoURL: "https://github.com/example/repo", GitBranch: "main",
	})
	require.NoError(t, err)
	select {
	case <-orch.building:
	case <-time.After(2 * time.Second):
		t.Fatal("the build never started")
	}

	services := service.NewServiceManager(deletingRepo{}, zap.NewNop())
	services.WithDeployStopper(deploys)
	require.NoError(t, services.Delete(context.Background(), "svc-1"))
	running := deploys.InFlightCount()
	close(orch.finish) // a build still running ends now, and rolls out
	require.Eventually(t, func() bool { return deploys.InFlightCount() == 0 }, 2*time.Second, 5*time.Millisecond)

	orch.mu.Lock()
	cause := orch.cause
	orch.mu.Unlock()
	orch.mockOrchestrator.mu.Lock()
	ran := orch.ran
	orch.mockOrchestrator.mu.Unlock()
	assert.ErrorContains(t, cause, "delete", "the delete cancelled the build")
	assert.Zero(t, running, "the delete returned once the deploy's pipeline had ended")
	assert.False(t, ran, "no container was started after the delete")
	assert.Equal(t, "cancelled by the service's delete (at the build step)", repo.lastSummary())
	assert.True(t, repo.hasLog("✗ The deploy was cancelled by the service's delete"), "the log says why")
	assert.Empty(t, repo.deadWrite, "how the deploy ended is recorded on a live context")
}
