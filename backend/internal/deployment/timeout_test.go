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

// deadRepo records each deployment's summary, and fails a test that writes
// on a context the deploy's time limit already ended (D-125).
type deadRepo struct {
	*mockRepo
	mu        sync.Mutex
	summaries []string
	deadWrite string // the first write made on a dead context
}

func (r *deadRepo) noteDead(ctx context.Context, what string) {
	if ctx.Err() == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deadWrite == "" {
		r.deadWrite = what
	}
}

func (r *deadRepo) UpdateStatus(ctx context.Context, id string, status deployment.Status, imageTag, commitSHA, summary string) (*deployment.Deployment, error) {
	r.noteDead(ctx, "status "+string(status))
	r.mu.Lock()
	r.summaries = append(r.summaries, summary)
	r.mu.Unlock()
	return r.mockRepo.UpdateStatus(ctx, id, status, imageTag, commitSHA, summary)
}

func (r *deadRepo) InsertLog(ctx context.Context, id, message string) (*deployment.Log, error) {
	r.noteDead(ctx, "log "+message)
	return r.mockRepo.InsertLog(ctx, id, message)
}

func (r *deadRepo) lastSummary() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.summaries) == 0 {
		return ""
	}
	return r.summaries[len(r.summaries)-1]
}

// limited is a live service with a deploy time limit of 5 (minutes, made
// milliseconds by the test).
type limited struct{}

func (limited) Get(_ context.Context, id string) (*service.Service, error) {
	return &service.Service{ID: id, RuntimeStatus: "running", ActiveDeploymentID: "dep-prev", DeployTimeoutMinutes: 5}, nil
}

// hangingBuild is an orchestrator whose build never ends on its own.
type hangingBuild struct{ *mockOrchestrator }

func (hangingBuild) BuildFromGit(ctx context.Context, _, _, _, _ string, _ map[string]*string, logFn func(string)) (deployment.Commit, error) {
	logFn("Building image...")
	<-ctx.Done()
	return deployment.Commit{}, ctx.Err()
}

func TestTimeout_ABuildThatNeverEndsFailsAtTheLimit(t *testing.T) {
	deployment.SetTimeoutUnit(t, 10*time.Millisecond)
	repo := &deadRepo{mockRepo: &mockRepo{createdID: "dep-hang"}}
	orch := hangingBuild{&mockOrchestrator{}}
	updater := &mockStatusUpdater{}
	svc := newTestService(repo, &mockFactory{orch: orch}, updater)
	svc.WithServiceReader(limited{})

	_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
		ServiceID: "svc-1", SourceType: "git", GitRepoURL: "https://github.com/example/repo", GitBranch: "main",
	})
	require.NoError(t, err)
	waitForStatus(t, repo.mockRepo, deployment.StatusFailed, 2*time.Second)
	require.Eventually(t, func() bool { return svc.InFlightCount() == 0 }, time.Second, 5*time.Millisecond)

	assert.Equal(t, "timed out after 5 minutes, the service's deploy time limit (at the build step)", repo.lastSummary())
	assert.True(t, repo.hasLog("✗ The deploy timed out after 5 minutes"), "the log says why")
	assert.Empty(t, repo.deadWrite, "how the deploy ended is recorded on a live context")
	assert.Empty(t, updater.recorded(), "a live service's status is left alone: its previous version serves")
}

// hangingHealth is an orchestrator whose new slot never answers its health
// check, started next to a previous slot. aborted is the context
// AbortSwitch ran on.
type hangingHealth struct {
	*mockOrchestrator
	mu      sync.Mutex
	aborted error // AbortSwitch's ctx.Err(); errAbortNotCalled until it runs
}

var errAbortNotCalled = context.DeadlineExceeded // anything non-nil

func (h *hangingHealth) Start(_ context.Context, _ rollout.Spec, sw *rollout.SlotSwitch) error {
	sw.New, sw.Previous = "svc-1-b", []string{"svc-1-a"}
	return nil
}

func (h *hangingHealth) Health(ctx context.Context, _ string, _ int, _ rollout.HealthCheck) error {
	<-ctx.Done()
	return ctx.Err()
}

func (h *hangingHealth) AbortSwitch(ctx context.Context, _, _ string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.aborted = ctx.Err()
	return nil
}

func (h *hangingHealth) Rollout(ctx context.Context, spec rollout.Spec, _ deployment.Routing, hooks rollout.Hooks) rollout.Result {
	return rollout.Run(ctx, h, spec, hooks)
}

func TestTimeout_ARolloutPastTheLimitRemovesTheNewSlot(t *testing.T) {
	deployment.SetTimeoutUnit(t, 10*time.Millisecond)
	repo := &deadRepo{mockRepo: &mockRepo{createdID: "dep-health"}}
	orch := &hangingHealth{mockOrchestrator: &mockOrchestrator{}, aborted: errAbortNotCalled}
	updater := &mockStatusUpdater{}
	svc := newTestService(repo, &mockFactory{orch: orch}, updater)
	svc.WithServiceReader(limited{})

	_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
		ServiceID: "svc-1", SourceType: "docker-image", DockerImage: "nginx:1.27", Port: 80,
		HealthCheckPath: "/", HealthCheckTimeout: 5, HealthCheckRetries: 3,
	})
	require.NoError(t, err)
	waitForStatus(t, repo.mockRepo, deployment.StatusFailed, 2*time.Second)
	require.Eventually(t, func() bool { return svc.InFlightCount() == 0 }, time.Second, 5*time.Millisecond)

	orch.mu.Lock()
	aborted := orch.aborted
	orch.mu.Unlock()
	assert.NoError(t, aborted, "the new slot is removed on a live context, past the limit")
	assert.True(t, repo.hasLog("The previous container keeps serving"))
	assert.Equal(t, "timed out after 5 minutes, the service's deploy time limit (at the health step)", repo.lastSummary())
	assert.Empty(t, repo.deadWrite)
	assert.Empty(t, updater.recorded(), "the service's status is unchanged: the previous version serves")
}
