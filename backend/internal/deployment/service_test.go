package deployment_test

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/testenv"

	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/deploypolicy"
	"github.com/Nickpap21/toft/backend/internal/netplan"
	"github.com/Nickpap21/toft/backend/internal/resources"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// ─── Mocks ────────────────────────────────────────────────────────────────────

type mockRepo struct {
	fingerprint   string   // SetSettingsFingerprint's last
	reserved      *float64 // SetReservedMemory's last
	mu            sync.Mutex
	createdID     string
	statusHistory []deployment.Status
	logMessages   []string
	createErr     error
	updateErr     error
	imageTags     []string // UpdateStatus's imageTag, in order
	// MarkInterrupted: interrupted is returned, excluded records the IDs
	// it was told to leave alone.
	interrupted []deployment.Deployment
	excluded    []string
	// ListUnreconciledInterrupted returns unchecked.
	unchecked []deployment.Deployment
	// composeCommits are SetComposeCommit's SHAs, in order.
	composeCommits []string
	// commits are SetCommit's commits, and commitSHAs UpdateStatus's
	// commitSHA, in order.
	commits    []deployment.Commit
	commitSHAs []string
	// handedOff is what MarkHandedOff recorded, by deployment ID (cleared
	// by ClearHandOff); ListHandedOff returns handedOffList.
	handedOff     map[string][]byte
	handOffClears int
	handedOffList []deployment.HandedOff
}

func (m *mockRepo) Adopt(context.Context, string, string, string, string, string, time.Time) (bool, error) {
	return false, nil
}

func (m *mockRepo) MarkHandedOff(_ context.Context, id, _ string, input []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.handedOff == nil {
		m.handedOff = map[string][]byte{}
	}
	m.handedOff[id] = input
	return nil
}

func (m *mockRepo) ClearHandOff(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.handedOff, id)
	m.handOffClears++
	return nil
}

func (m *mockRepo) DeployRunningOnServer(context.Context, string) (bool, error) { return false, nil }

func (m *mockRepo) IsHandedOff(_ context.Context, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.handedOff[id]
	return ok, nil
}

func (m *mockRepo) ListHandedOff(context.Context) ([]deployment.HandedOff, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.handedOffList, nil
}

func (m *mockRepo) CountAgentLogLines(_ context.Context, _, prefix string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, msg := range m.logMessages {
		if !strings.HasPrefix(msg, prefix) {
			n++
		}
	}
	return n, nil
}

func (m *mockRepo) ListPulls(context.Context, []string) (map[string]deployment.PullRef, error) {
	return map[string]deployment.PullRef{}, nil
}

func (m *mockRepo) SetReservedMemory(_ context.Context, _ string, memoryMB float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reserved = &memoryMB
	return nil
}

func (m *mockRepo) SetSettingsFingerprint(_ context.Context, _, fingerprint string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fingerprint = fingerprint
	return nil
}

func (m *mockRepo) Create(_ context.Context, _, _, _ string, _ int64) (*deployment.Deployment, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	return &deployment.Deployment{ID: m.createdID, Status: deployment.StatusPending}, nil
}

func (m *mockRepo) UpdateStatus(_ context.Context, _ string, status deployment.Status, imageTag, commitSHA, _ string) (*deployment.Deployment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statusHistory = append(m.statusHistory, status)
	m.imageTags = append(m.imageTags, imageTag)
	m.commitSHAs = append(m.commitSHAs, commitSHA)
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	return &deployment.Deployment{Status: status}, nil
}

func (m *mockRepo) SetComposeCommit(_ context.Context, _, sha string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.composeCommits = append(m.composeCommits, sha)
	return nil
}

func (m *mockRepo) SetCommit(_ context.Context, _, sha, subject string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.commits = append(m.commits, deployment.Commit{SHA: sha, Subject: subject})
	return nil
}

func (m *mockRepo) InsertLog(_ context.Context, _, message string) (*deployment.Log, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logMessages = append(m.logMessages, message)
	return &deployment.Log{Message: message}, nil
}

func (m *mockRepo) ListByService(_ context.Context, _ string) ([]deployment.Deployment, error) {
	return nil, nil
}
func (m *mockRepo) ListByServicePaginated(_ context.Context, _ string, _ int32, _ *time.Time, _ string) ([]deployment.Deployment, error) {
	return nil, nil
}
func (m *mockRepo) GetByID(_ context.Context, _ string) (*deployment.Deployment, error) {
	return &deployment.Deployment{}, nil
}
func (m *mockRepo) ListLogs(_ context.Context, _ string) ([]deployment.Log, error) {
	return nil, nil
}
func (m *mockRepo) ListLogsAfter(_ context.Context, _ string, _ int64) ([]deployment.Log, error) {
	return nil, nil
}
func (m *mockRepo) ListLogsFiltered(_ context.Context, _, _ string) ([]deployment.Log, error) {
	return nil, nil
}
func (m *mockRepo) ListServiceIDs(_ context.Context) ([]string, error) {
	return nil, nil
}
func (m *mockRepo) DeleteOldDeploymentsForService(_ context.Context, _ string, _ int) error {
	return nil
}
func (m *mockRepo) MarkInterrupted(_ context.Context, _ string, excludeIDs []string) ([]deployment.Deployment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.excluded = append([]string(nil), excludeIDs...)
	return m.interrupted, nil
}
func (m *mockRepo) ListUnreconciledInterrupted(_ context.Context, _ string) ([]deployment.Deployment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.unchecked, nil
}
func (m *mockRepo) RecordRecovery(_ context.Context, _, _, _ string) error {
	return nil
}
func (m *mockRepo) LatestIDForService(_ context.Context, _ string) (string, error) {
	return m.createdID, nil
}

// ─── Orchestrator mock ────────────────────────────────────────────────────────

// TestMain shortens the wait between health check attempts: the pipeline
// tests run the real rollout (rollout.Run) on a fake host.
func TestMain(m *testing.M) {
	rollout.HealthInterval = time.Millisecond
	os.Exit(m.Run())
}

type mockOrchestrator struct {
	runVolumes  []rollout.Volume // the new slot's volumes
	runLoopback rollout.Loopback
	commit      deployment.Commit // what BuildFromGit checked out
	buildErr    error
	runErr      error
	composeErr  error

	mu       sync.Mutex
	built    bool
	ran      bool
	stopped  bool
	logLines []string
	// runDeploymentID is the deployment ID of the rollout that started the slot.
	runDeploymentID string

	prepareErr error
	// calls records network-relevant calls in order: "prepare <home>",
	// "ephemeral <network>" (RunOnce), "run <home>" (Start), "compose".
	calls       []string
	runPlan     netplan.Plan
	runSecurity rollout.Security
	runCheck    rollout.ContainerCheck
	nets        deployment.ComposeNetworks
	composeIn   string
	// archived is what BuildFromArchive read of the tar, by path, and the
	// build arguments it got.
	archived  map[string]string
	buildArgs map[string]*string
}

func (m *mockOrchestrator) record(call string) {
	m.mu.Lock()
	m.calls = append(m.calls, call)
	m.mu.Unlock()
}

func (m *mockOrchestrator) recorded() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.calls...)
}

func (m *mockOrchestrator) PrepareNetworks(_ context.Context, plan netplan.Plan, _ func(string)) error {
	m.record("prepare " + plan.Home.Name)
	return m.prepareErr
}

func (m *mockOrchestrator) BuildFromGit(_ context.Context, _, _, _, _ string, _ map[string]*string, logFn func(string)) (deployment.Commit, error) {
	m.mu.Lock()
	m.built = true
	m.mu.Unlock()
	logFn("Cloning repo...")
	logFn("Building image...")
	return m.commit, m.buildErr
}

func (m *mockOrchestrator) BuildFromArchive(_ context.Context, source io.Reader, _, _ string, _ *deployment.NixpacksCommands, buildArgs map[string]*string, logFn func(string)) error {
	files := map[string]string{}
	tr := tar.NewReader(source)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(tr)
		files[h.Name] = string(b)
	}
	m.mu.Lock()
	m.built, m.archived, m.buildArgs = true, files, buildArgs
	m.mu.Unlock()
	logFn("Building image...")
	return m.buildErr
}

func (m *mockOrchestrator) BuildFromGitNixpacks(_ context.Context, _, _, _, _ string, _ deployment.NixpacksCommands, _ map[string]*string, _ func(string)) (deployment.Commit, error) {
	return deployment.Commit{}, nil
}

// Rollout runs the real rollout with m as its host.
func (m *mockOrchestrator) Rollout(ctx context.Context, spec rollout.Spec, _ deployment.Routing, hooks rollout.Hooks) rollout.Result {
	return rollout.Run(ctx, m, spec, hooks)
}

func (m *mockOrchestrator) Pull(_ context.Context, _ string, _ *rollout.RegistryAuth, _ func(string)) error {
	return nil
}

func (m *mockOrchestrator) Start(_ context.Context, spec rollout.Spec, _ *rollout.SlotSwitch) error {
	m.record("run " + spec.Plan.Home.Name)
	m.mu.Lock()
	m.ran = true
	m.runSecurity = spec.Slot.Security
	m.runCheck = spec.Slot.Check
	m.runVolumes = spec.Slot.Volumes
	m.runLoopback = spec.Slot.Loopback
	m.runPlan = spec.Plan
	m.runDeploymentID = spec.DeploymentID
	m.mu.Unlock()
	return m.runErr
}

func (m *mockOrchestrator) WaitReady(context.Context, string, int, func(string)) error { return nil }

func (m *mockOrchestrator) Health(context.Context, string, int, rollout.HealthCheck) error {
	return nil
}

func (m *mockOrchestrator) Slots(context.Context, string) ([]string, error) { return nil, nil }

func (m *mockOrchestrator) Exec(context.Context, string, string, func(string)) error { return nil }

func (m *mockOrchestrator) FinishSwitch(context.Context, string, string, bool) error { return nil }
func (m *mockOrchestrator) AwaitHealthy(context.Context, string, bool) error         { return nil }

func (m *mockOrchestrator) AbortSwitch(context.Context, string, string) error { return nil }

func (m *mockOrchestrator) RestorePrevious(context.Context, string, []string) error { return nil }

func (m *mockOrchestrator) RunCompose(_ context.Context, _ string, _, _, content string, _ map[string]string, _ *deployment.ComposeRoute, nets deployment.ComposeNetworks, _ map[string]deployment.ComposeLimit, _ *deployment.RegistryAuth, _ rollout.Hooks) error {
	m.record("compose")
	m.mu.Lock()
	m.nets, m.composeIn = nets, content
	m.mu.Unlock()
	return m.composeErr
}

func (m *mockOrchestrator) RunComposeFromRepo(_ context.Context, _ string, _, _, _, _ string, _ map[string]string, _ *deployment.ComposeRoute, nets deployment.ComposeNetworks, _ map[string]deployment.ComposeLimit, _ *deployment.RegistryAuth, _ rollout.Hooks) error {
	m.record("compose")
	m.mu.Lock()
	m.nets = nets
	m.mu.Unlock()
	return nil
}

func (m *mockOrchestrator) Stop(_ context.Context, _ string) error {
	m.stopped = true
	return nil
}

func (m *mockOrchestrator) StopCompose(_ context.Context, _, _, _ string) error {
	return nil
}

func (m *mockOrchestrator) StreamLogs(_ context.Context, _ string, _ int, _ string, _ func(string)) error {
	return nil
}

func (m *mockOrchestrator) ExecCommands(_ context.Context, _ []string, _ func(string)) error {
	return nil
}

func (m *mockOrchestrator) ExecInContainer(_ context.Context, _, _, _ string, _ func(string)) error {
	return nil
}

func (m *mockOrchestrator) ListContainers(_ context.Context, _, _ string) ([]string, error) {
	return nil, nil
}

func (m *mockOrchestrator) RunOnce(_ context.Context, spec rollout.Spec, _ []string, _ bool, _ func(string)) error {
	m.record("ephemeral " + spec.Plan.Home.Name)
	return nil
}

// ─── Factory mock ─────────────────────────────────────────────────────────────

type mockFactory struct {
	orch    deployment.Orchestrator
	factErr error
}

func (f *mockFactory) ForServer(_ *server.Server) (deployment.Orchestrator, func(), error) {
	if f.factErr != nil {
		return nil, func() {}, f.factErr
	}
	return f.orch, func() {}, nil
}

// ─── ServiceStatusUpdater mock ────────────────────────────────────────────────

type mockStatusUpdater struct {
	mu        sync.Mutex
	statuses  []string
	updateErr error
	// networks are SetComposeNetworkService's names, in order.
	networks []string
}

func (m *mockStatusUpdater) SetComposeNetworkService(_ context.Context, _, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.networks = append(m.networks, name)
	return nil
}

func (m *mockStatusUpdater) UpdateRuntimeStatus(_ context.Context, _, status, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statuses = append(m.statuses, status)
	return m.updateErr
}

func (m *mockStatusUpdater) SwapDockerImage(context.Context, string, string, string) error {
	return nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func newTestService(repo deployment.Repository, factory deployment.OrchestratorFactory, updater deployment.ServiceStatusUpdater) *deployment.Service {
	return deployment.NewService(repo, factory, updater, zap.NewNop())
}

func waitForStatus(t *testing.T, repo *mockRepo, want deployment.Status, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		repo.mu.Lock()
		for _, s := range repo.statusHistory {
			if s == want {
				repo.mu.Unlock()
				return
			}
		}
		repo.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for status %q; got %v", want, repo.statusHistory)
}

// ─── Tests ────────────────────────────────────────────────────────────────────

func TestTrigger_CreatesDeploymentRecord(t *testing.T) {
	repo := &mockRepo{createdID: "dep-1"}
	orch := &mockOrchestrator{}
	factory := &mockFactory{orch: orch}
	updater := &mockStatusUpdater{}

	svc := newTestService(repo, factory, updater)
	d, err := svc.Trigger(context.Background(), deployment.TriggerInput{
		ServiceID:  "svc-1",
		SourceType: "git",
		GitRepoURL: "https://github.com/example/repo",
		GitBranch:  "main",
	})

	require.NoError(t, err)
	assert.Equal(t, "dep-1", d.ID)
}

func TestTrigger_ReturnsErrorWhenRepoCreateFails(t *testing.T) {
	repo := &mockRepo{createErr: errors.New("db down")}
	svc := newTestService(repo, &mockFactory{orch: &mockOrchestrator{}}, &mockStatusUpdater{})

	_, err := svc.Trigger(context.Background(), deployment.TriggerInput{ServiceID: "svc-1", SourceType: "git"})
	require.Error(t, err)
}

func TestPipeline_GitSuccess(t *testing.T) {
	repo := &mockRepo{createdID: "dep-git-ok"}
	orch := &mockOrchestrator{}
	updater := &mockStatusUpdater{}
	svc := newTestService(repo, &mockFactory{orch: orch}, updater)

	_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
		ServiceID:  "svc-1",
		SourceType: "git",
		GitRepoURL: "https://github.com/example/repo",
		GitBranch:  "main",
		Port:       3000,
	})
	require.NoError(t, err)

	waitForStatus(t, repo, deployment.StatusRunning, 2*time.Second)

	assert.True(t, orch.built, "expected BuildFromGit to be called")
	assert.True(t, orch.ran, "expected Run to be called")

	updater.mu.Lock()
	defer updater.mu.Unlock()
	assert.Contains(t, updater.statuses, "running")
}

// liveReader is a ServiceReader for a service that runs deployment
// "dep-prev" (status is its runtime status).
type liveReader struct{ status string }

func (r liveReader) Get(_ context.Context, id string) (*service.Service, error) {
	return &service.Service{ID: id, RuntimeStatus: r.status, ActiveDeploymentID: "dep-prev"}, nil
}

// A deploy that fails before it changes what runs leaves a live service as it
// is: its previous version serves on, and the metrics and alerts keep
// watching it (they follow runtime status running).
func TestPipeline_FailureBeforeAnyChangeKeepsTheLiveVersion(t *testing.T) {
	for name, input := range map[string]deployment.TriggerInput{
		"build fails":      {ServiceID: "svc-1", SourceType: "git", GitRepoURL: "https://github.com/example/repo", GitBranch: "main"},
		"missing variable": {ServiceID: "svc-1", SourceType: "docker-image", DockerImage: "nginx", RequiredEnvVars: []string{"DATABASE_URL"}},
	} {
		t.Run(name, func(t *testing.T) {
			repo := &mockRepo{createdID: "dep-early-fail"}
			orch := &mockOrchestrator{buildErr: errors.New("syntax error")}
			updater := &mockStatusUpdater{}
			svc := newTestService(repo, &mockFactory{orch: orch}, updater)
			svc.WithServiceReader(liveReader{status: "running"})

			_, err := svc.Trigger(context.Background(), input)
			require.NoError(t, err)
			waitForStatus(t, repo, deployment.StatusFailed, 2*time.Second)

			assert.False(t, orch.ran)
			assert.Empty(t, updater.recorded(), "the service's status and active deployment are left as they are")
		})
	}
}

// A service that was not live (a first deploy, or one already failing) goes
// to error, as before.
func TestPipeline_FailureBeforeAnyChangeOfAServiceNotLive(t *testing.T) {
	repo := &mockRepo{createdID: "dep-early-fail-idle"}
	updater := &mockStatusUpdater{}
	svc := newTestService(repo, &mockFactory{orch: &mockOrchestrator{buildErr: errors.New("syntax error")}}, updater)
	svc.WithServiceReader(liveReader{status: "error"})

	_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
		ServiceID: "svc-1", SourceType: "git", GitRepoURL: "https://github.com/example/repo", GitBranch: "main",
	})
	require.NoError(t, err)
	waitForStatus(t, repo, deployment.StatusFailed, 2*time.Second)
	assert.Equal(t, []string{"error"}, updater.recorded())
}

// docker compose up may leave a stack partly updated: the service goes to
// error even when it was live.
func TestPipeline_ComposeUpFailureMarksTheServiceFailing(t *testing.T) {
	repo := &mockRepo{createdID: "dep-compose-fail"}
	updater := &mockStatusUpdater{}
	svc := newTestService(repo, &mockFactory{orch: &mockOrchestrator{composeErr: errors.New("port is already allocated")}}, updater)
	svc.WithServiceReader(liveReader{status: "running"})

	_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
		ServiceID: "svc-1", ServiceName: "stack", SourceType: "docker-compose",
		ComposeContent: "services:\n  web:\n    image: nginx\n",
	})
	require.NoError(t, err)
	waitForStatus(t, repo, deployment.StatusFailed, 2*time.Second)
	assert.Equal(t, []string{"error"}, updater.recorded())
}

func TestPipeline_GitBuildFailure(t *testing.T) {
	repo := &mockRepo{createdID: "dep-build-fail"}
	orch := &mockOrchestrator{buildErr: errors.New("syntax error")}
	updater := &mockStatusUpdater{}
	svc := newTestService(repo, &mockFactory{orch: orch}, updater)

	_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
		ServiceID:  "svc-1",
		SourceType: "git",
		GitRepoURL: "https://github.com/example/repo",
		GitBranch:  "main",
	})
	require.NoError(t, err) // Trigger itself succeeds; pipeline fails async

	waitForStatus(t, repo, deployment.StatusFailed, 2*time.Second)

	assert.False(t, orch.ran, "Run should not be called after build failure")

	updater.mu.Lock()
	defer updater.mu.Unlock()
	assert.Contains(t, updater.statuses, "error")
}

// A git build records the commit it checked out in place of the one it was
// started with (a push's, which the branch may have moved past), also when
// the build then fails; one it could not read leaves the push's.
func TestPipeline_GitRecordsTheBuiltCommit(t *testing.T) {
	pushed := strings.Repeat("a", 40)
	built := deployment.Commit{SHA: strings.Repeat("b", 40), Subject: "Bump pgx to v5.7"}
	for name, tc := range map[string]struct {
		commit   deployment.Commit
		buildErr error
		final    deployment.Status
		want     []deployment.Commit
		wantSHA  string
	}{
		"build succeeds":  {built, nil, deployment.StatusRunning, []deployment.Commit{built}, built.SHA},
		"build fails":     {built, errors.New("syntax error"), deployment.StatusFailed, []deployment.Commit{built}, built.SHA},
		"commit not read": {deployment.Commit{}, nil, deployment.StatusRunning, nil, pushed},
	} {
		t.Run(name, func(t *testing.T) {
			repo := &mockRepo{createdID: "dep-commit"}
			svc := newTestService(repo, &mockFactory{orch: &mockOrchestrator{commit: tc.commit, buildErr: tc.buildErr}}, &mockStatusUpdater{})
			in := gitDeploy()
			in.CommitSHA = pushed
			_, err := svc.Trigger(context.Background(), in)
			require.NoError(t, err)
			waitForStatus(t, repo, tc.final, 2*time.Second)

			repo.mu.Lock()
			defer repo.mu.Unlock()
			assert.Equal(t, tc.want, repo.commits)
			require.NotEmpty(t, repo.commitSHAs)
			assert.Equal(t, tc.wantSHA, repo.commitSHAs[len(repo.commitSHAs)-1], "the final status keeps it")
		})
	}
}

func TestPipeline_GitRunFailure(t *testing.T) {
	repo := &mockRepo{createdID: "dep-run-fail"}
	orch := &mockOrchestrator{runErr: errors.New("port already in use")}
	updater := &mockStatusUpdater{}
	svc := newTestService(repo, &mockFactory{orch: orch}, updater)

	_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
		ServiceID:  "svc-1",
		SourceType: "git",
		GitRepoURL: "https://github.com/example/repo",
		GitBranch:  "main",
	})
	require.NoError(t, err)

	waitForStatus(t, repo, deployment.StatusFailed, 2*time.Second)

	assert.True(t, orch.built)
	assert.True(t, orch.ran)

	updater.mu.Lock()
	defer updater.mu.Unlock()
	assert.Contains(t, updater.statuses, "error")
}

// sharedMounts returns mounts, or err, for every service.
type sharedMounts struct {
	mounts []service.VolumeMount
	err    error
}

func (s sharedMounts) MountsForDeploy(context.Context, string, string) ([]service.VolumeMount, error) {
	return s.mounts, s.err
}

// A deploy mounts the service's shared volumes next to its own; a shared
// volume on another server fails the deploy before anything runs.
func TestPipeline_MountsSharedVolumes(t *testing.T) {
	own := service.VolumeMount{Name: "data", Target: "/data"}
	shared := service.VolumeMount{Name: "images", Target: "/app/images", ReadOnly: true, Shared: "paas-shared-1"}

	repo := &mockRepo{createdID: "dep-shared"}
	orch := &mockOrchestrator{}
	svc := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
	svc.WithSharedMounts(sharedMounts{mounts: []service.VolumeMount{shared}})
	_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
		ServiceID: "svc-1", SourceType: "docker-image", DockerImage: "nginx:alpine", Port: 80, Volumes: []service.VolumeMount{own},
	})
	require.NoError(t, err)
	waitForStatus(t, repo, deployment.StatusRunning, 2*time.Second)
	orch.mu.Lock()
	assert.Equal(t, []rollout.Volume{
		{Name: own.Name, Target: own.Target},
		{Name: shared.Name, Target: shared.Target, ReadOnly: true, Shared: shared.Shared},
	}, orch.runVolumes)
	orch.mu.Unlock()
	repo.mu.Lock()
	assert.Contains(t, strings.Join(repo.logMessages, "\n"), "→ shared volume images at /app/images")
	repo.mu.Unlock()

	repo = &mockRepo{createdID: "dep-shared-wrong"}
	orch = &mockOrchestrator{}
	svc = newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
	svc.WithSharedMounts(sharedMounts{err: errors.New("a service can only mount shared volumes on its own server")})
	_, err = svc.Trigger(context.Background(), deployment.TriggerInput{ServiceID: "svc-1", SourceType: "docker-image", DockerImage: "nginx:alpine", Port: 80})
	require.NoError(t, err)
	waitForStatus(t, repo, deployment.StatusFailed, 2*time.Second)
	orch.mu.Lock()
	assert.False(t, orch.ran, "nothing runs")
	orch.mu.Unlock()
}

// loopbackPorts gives the services it names their 127.0.0.1 port, 0 the
// others, as the repository does for the catalog Loki (D-148).
type loopbackPorts map[string]int

func (l loopbackPorts) AssignLoopbackPort(_ context.Context, serviceID string) (int, error) {
	return l[serviceID], nil
}

// Loki gets its 127.0.0.1 port at every deploy, whatever started it: a
// rollback's input carries no addon slug (D-148). Other services get none.
func TestPipeline_LokiGetsItsLoopbackPortOnEveryDeploy(t *testing.T) {
	for id, want := range map[string]rollout.Loopback{"loki-1": {Port: 39100, Target: 3100}, "app-1": {}} {
		repo := &mockRepo{createdID: "dep-" + id}
		orch := &mockOrchestrator{}
		svc := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
		svc.WithLoopbackPorts(loopbackPorts{"loki-1": 39100})
		_, err := svc.Trigger(context.Background(), deployment.TriggerInput{ServiceID: id, SourceType: "docker-image", DockerImage: "grafana/loki:3", Port: 3100, RollbackImageTag: "grafana/loki:3"})
		require.NoError(t, err)
		waitForStatus(t, repo, deployment.StatusRunning, 2*time.Second)
		orch.mu.Lock()
		assert.Equal(t, want, orch.runLoopback, id)
		orch.mu.Unlock()
	}
}

func TestPipeline_DockerImageSuccess(t *testing.T) {
	repo := &mockRepo{createdID: "dep-img-ok"}
	orch := &mockOrchestrator{}
	updater := &mockStatusUpdater{}
	svc := newTestService(repo, &mockFactory{orch: orch}, updater)

	_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
		ServiceID:   "svc-1",
		SourceType:  "docker-image",
		DockerImage: "nginx:alpine",
		Port:        80,
	})
	require.NoError(t, err)

	waitForStatus(t, repo, deployment.StatusRunning, 2*time.Second)

	assert.False(t, orch.built, "BuildFromGit should NOT be called for docker-image source")
	assert.True(t, orch.ran)

	updater.mu.Lock()
	defer updater.mu.Unlock()
	assert.Contains(t, updater.statuses, "running")
}

func TestPipeline_OrchestratorFactoryFailure(t *testing.T) {
	repo := &mockRepo{createdID: "dep-factory-fail"}
	factory := &mockFactory{factErr: errors.New("ssh unreachable")}
	updater := &mockStatusUpdater{}
	svc := newTestService(repo, factory, updater)

	_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
		ServiceID:  "svc-1",
		SourceType: "git",
	})
	require.NoError(t, err) // Trigger succeeds; pipeline fails async

	waitForStatus(t, repo, deployment.StatusFailed, 2*time.Second)

	updater.mu.Lock()
	defer updater.mu.Unlock()
	assert.Contains(t, updater.statuses, "error")
}

func TestPipeline_StatusSequence_Git(t *testing.T) {
	repo := &mockRepo{createdID: "dep-seq"}
	orch := &mockOrchestrator{}
	updater := &mockStatusUpdater{}
	svc := newTestService(repo, &mockFactory{orch: orch}, updater)

	_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
		ServiceID:  "svc-1",
		SourceType: "git",
		GitRepoURL: "https://github.com/example/repo",
		GitBranch:  "main",
	})
	require.NoError(t, err)

	waitForStatus(t, repo, deployment.StatusRunning, 2*time.Second)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	// Must transition: building → running (in that order)
	require.GreaterOrEqual(t, len(repo.statusHistory), 2)
	assert.Equal(t, deployment.StatusBuilding, repo.statusHistory[0])
	assert.Equal(t, deployment.StatusRunning, repo.statusHistory[len(repo.statusHistory)-1])
}

// ─── Deploy policy and control-plane stopgap ─────────────────────────────────

type fakePolicy struct {
	mu           sync.Mutex
	deploy       deploypolicy.Decision
	repo         deploypolicy.Decision
	deploys      []deploypolicy.DeployCheck
	repoRuns     []deploypolicy.RepoCheck
	controlPlane map[string]bool // services granted the control_plane exception
	// repoDotEnv is the compose file directory's .env as CheckComposeRepo
	// saw it ("<none>" when there was none).
	repoDotEnv []string
	netRaw     map[string]bool
}

func (f *fakePolicy) AllowsControlPlane(_ context.Context, serviceID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.controlPlane[serviceID]
}

func (f *fakePolicy) AllowsNetRaw(_ context.Context, serviceID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.netRaw[serviceID]
}

func (f *fakePolicy) CheckDeploy(_ context.Context, in deploypolicy.DeployCheck) deploypolicy.Decision {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deploys = append(f.deploys, in)
	return f.deploy
}

func (f *fakePolicy) CheckComposeRepo(_ context.Context, in deploypolicy.RepoCheck) deploypolicy.Decision {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repoRuns = append(f.repoRuns, in)
	dotEnv, err := os.ReadFile(filepath.Join(in.RepoDir, filepath.Dir(in.ComposeFilePath), ".env"))
	if err != nil {
		dotEnv = []byte("<none>")
	}
	f.repoDotEnv = append(f.repoDotEnv, string(dotEnv))
	return f.repo
}

type fakeOperators map[string]bool

func (f fakeOperators) IsOperator(_ context.Context, id string) (bool, error) { return f[id], nil }

func blocked(rule composespec.Rule, field string) deploypolicy.Decision {
	return deploypolicy.Decision{
		Mode:       deploypolicy.ModeEnforce,
		Violations: []composespec.Violation{{Rule: rule, Field: field, Message: "not allowed"}},
		Blocked:    true,
	}
}

func TestTrigger_PolicyViolationCreatesNoDeployment(t *testing.T) {
	repo := &mockRepo{createdID: "dep-x"}
	orch := &mockOrchestrator{}
	svc := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
	policy := &fakePolicy{deploy: blocked(composespec.RuleHostBindMount, "volumes[0].host_path")}
	svc.WithPolicyChecker(policy)

	_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
		ServiceID:   "svc-1",
		SourceType:  "docker-image",
		DockerImage: "nginx",
		Volumes:     []service.VolumeMount{{Name: "x", Target: "/x", HostPath: "/etc"}},
		EnvVars:     []string{"A=1"},
	})

	var pv *composespec.ViolationsError
	require.True(t, errors.As(err, &pv), "got %v", err)
	assert.Equal(t, "volumes[0].host_path", pv.Violations[0].Field)
	require.Len(t, policy.deploys, 1)
	assert.Equal(t, "/etc", policy.deploys[0].Volumes[0].HostPath)
	assert.Equal(t, []string{"A=1"}, policy.deploys[0].EnvVars)

	time.Sleep(50 * time.Millisecond)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	assert.Empty(t, repo.statusHistory, "no deployment record was created or run")
	assert.False(t, orch.ran)
}

func TestTrigger_WarnModeLogsAndDeploys(t *testing.T) {
	repo := &mockRepo{createdID: "dep-warn"}
	orch := &mockOrchestrator{}
	svc := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
	d := blocked(composespec.RuleHostExec, "pre_deploy_commands")
	d.Mode, d.Blocked = deploypolicy.ModeWarn, false
	svc.WithPolicyChecker(&fakePolicy{deploy: d})

	_, err := svc.Trigger(context.Background(), deployment.TriggerInput{ServiceID: "svc-1", SourceType: "docker-image", DockerImage: "nginx"})
	require.NoError(t, err)
	waitForStatus(t, repo, deployment.StatusRunning, 2*time.Second)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	found := false
	for _, m := range repo.logMessages {
		if strings.Contains(m, "warn mode") && strings.Contains(m, "pre_deploy_commands") {
			found = true
		}
	}
	assert.True(t, found, "warn-mode violation is written to the deploy log: %v", repo.logMessages)
}

// infraRepo creates a local git repository with files committed on main, to
// stand in for an infra repo; it returns its file:// URL.
func infraRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		testenv.Skip(t, "git not installed")
	}
	src := t.TempDir()
	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = src
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	runGit("init", "-q", "-b", "main")
	for name, content := range files {
		p := filepath.Join(src, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	runGit("add", ".")
	runGit("commit", "-q", "-m", "init")
	return "file://" + src
}

func TestPipeline_ComposeRepoViolationFailsAfterClone(t *testing.T) {
	src := infraRepo(t, map[string]string{"docker-compose.yml": "services:\n  a:\n    image: x\n"})

	repo := &mockRepo{createdID: "dep-repo"}
	orch := &composeTrackingOrchestrator{}
	svc := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
	policy := &fakePolicy{repo: blocked(composespec.RuleDockerSocket, "services.a.volumes[0]")}
	svc.WithPolicyChecker(policy)

	_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
		ServiceID:      "svc-1",
		ServiceName:    "infra",
		SourceType:     "docker-compose",
		ComposeRepoURL: src,
		GitBranch:      "main",
	})
	require.NoError(t, err)
	waitForStatus(t, repo, deployment.StatusFailed, 5*time.Second)

	policy.mu.Lock()
	require.Len(t, policy.repoRuns, 1)
	assert.Equal(t, "docker-compose.yml", policy.repoRuns[0].ComposeFilePath)
	assert.True(t, policy.repoRuns[0].Local)
	policy.mu.Unlock()

	orch.mu.Lock()
	assert.False(t, orch.composeRan, "compose never runs after a policy violation")
	orch.mu.Unlock()

	repo.mu.Lock()
	defer repo.mu.Unlock()
	found := false
	for _, m := range repo.logMessages {
		if strings.Contains(m, "services.a.volumes[0]") {
			found = true
		}
	}
	assert.True(t, found, "the deploy log names the field: %v", repo.logMessages)
}

// A deploy from a compose repository records the commit it checked out, so
// a rollback gets the same files. A pinned deploy records the pinned commit.
func TestPipeline_ComposeRepoRecordsTheCommitCheckedOut(t *testing.T) {
	src := infraRepo(t, map[string]string{"docker-compose.yml": "services:\n  a:\n    image: alpine\n"})
	out, err := exec.Command("git", "-C", strings.TrimPrefix(src, "file://"), "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	head := strings.TrimSpace(string(out))

	for name, pin := range map[string]string{"branch tip": "", "pinned": head} {
		t.Run(name, func(t *testing.T) {
			repo := &mockRepo{createdID: "dep-commit"}
			svc := newTestService(repo, &mockFactory{orch: &composeTrackingOrchestrator{}}, &mockStatusUpdater{})
			_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
				ServiceID:      "svc-1",
				ServiceName:    "infra",
				SourceType:     "docker-compose",
				ComposeRepoURL: src,
				GitBranch:      "main",
				CommitSHA:      "1111111111111111111111111111111111111111", // a push to another repository
				CheckoutSHA:    pin,
			})
			require.NoError(t, err)
			waitForStatus(t, repo, deployment.StatusRunning, 5*time.Second)

			repo.mu.Lock()
			defer repo.mu.Unlock()
			assert.Equal(t, []string{head}, repo.composeCommits)
			assert.Contains(t, strings.Join(repo.logMessages, "\n"), "compose files at commit "+head)
		})
	}
}

type composeTrackingOrchestrator struct {
	mockOrchestrator
	composeRan bool
}

func (o *composeTrackingOrchestrator) RunComposeFromRepo(_ context.Context, _ string, _, _, _, _ string, _ map[string]string, _ *deployment.ComposeRoute, _ deployment.ComposeNetworks, _ map[string]deployment.ComposeLimit, _ *deployment.RegistryAuth, _ rollout.Hooks) error {
	o.mu.Lock()
	o.composeRan = true
	o.mu.Unlock()
	return nil
}

// A remote deploy replaces the compose file directory's .env with the
// service's env vars. That file is written into the clone before the policy
// check, so the check (and compose, for files included from that directory)
// never reads the repository's own .env there. Local deploys keep it.
func TestPipeline_ComposeRepoRemoteStagesDotEnvBeforeTheCheck(t *testing.T) {
	src := infraRepo(t, map[string]string{
		"envs/dev/docker-compose.yml": "include:\n  - svc.yml\n",
		"envs/dev/svc.yml":            "services:\n  app:\n    image: alpine\n    privileged: ${LOCK:-true}\n",
		"envs/dev/.env":               "LOCK=false\n",
	})
	for name, srv := range map[string]*server.Server{"remote": {ID: "srv-1"}, "local": nil} {
		t.Run(name, func(t *testing.T) {
			repo := &mockRepo{createdID: "dep-stage"}
			svc := newTestService(repo, &mockFactory{orch: &composeTrackingOrchestrator{}}, &mockStatusUpdater{})
			policy := &fakePolicy{repo: deploypolicy.Decision{Mode: deploypolicy.ModeEnforce}}
			svc.WithPolicyChecker(policy)

			_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
				ServiceID:       "svc-1",
				ServiceName:     "infra",
				SourceType:      "docker-compose",
				ComposeRepoURL:  src,
				ComposeFilePath: "envs/dev/docker-compose.yml",
				GitBranch:       "main",
				EnvVars:         []string{"A=it's $x", "DOCKER_HOST=tcp://evil"},
				Server:          srv,
			})
			require.NoError(t, err)
			waitForStatus(t, repo, deployment.StatusRunning, 5*time.Second)

			policy.mu.Lock()
			defer policy.mu.Unlock()
			require.Len(t, policy.repoDotEnv, 1)
			if srv == nil {
				assert.Equal(t, "LOCK=false\n", policy.repoDotEnv[0], "a local deploy reads the repository's .env")
				return
			}
			want, err := composespec.DotEnvFile(map[string]string{"A": "it's $x"})
			require.NoError(t, err)
			assert.Equal(t, string(want), policy.repoDotEnv[0], "the check reads the .env the remote deploy writes")
		})
	}
}

func TestControlPlaneStopgap(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(&mockRepo{createdID: "d"}, &mockFactory{orch: &mockOrchestrator{}}, &mockStatusUpdater{})
	policy := &fakePolicy{controlPlane: map[string]bool{"granted": true}}
	svc.WithPolicyChecker(policy)
	svc.WithControlPlaneStopgap(true, fakeOperators{"op": true})

	// Interactive, control-plane server, not an operator: refused.
	_, err := svc.Trigger(ctx, deployment.TriggerInput{ServiceID: "s", SourceType: "docker-image", ActorID: "dev"})
	assert.ErrorIs(t, err, deployment.ErrControlPlaneOperatorOnly)
	// No actor at all on an interactive deploy: refused.
	assert.ErrorIs(t, svc.AuthorizeTarget(ctx, deployment.OriginInteractive, "", "s", nil), deployment.ErrControlPlaneOperatorOnly)

	// Operators and remote servers are allowed.
	assert.NoError(t, svc.AuthorizeTarget(ctx, deployment.OriginInteractive, "op", "s", nil))
	assert.NoError(t, svc.AuthorizeTarget(ctx, deployment.OriginInteractive, "dev", "s", &server.Server{ID: "srv"}))
	assert.NoError(t, svc.AuthorizeTarget(ctx, deployment.OriginWebhook, "", "s", &server.Server{ID: "srv"}))

	// A git push to the control-plane server is refused: the webhook secret
	// is shared by the whole instance, so there is no person to check.
	assert.ErrorIs(t, svc.AuthorizeTarget(ctx, deployment.OriginWebhook, "", "s", nil), deployment.ErrControlPlaneWebhook)

	// The operator-granted control_plane exception lifts the stopgap for
	// that service only, for developers and pushes alike.
	assert.NoError(t, svc.AuthorizeTarget(ctx, deployment.OriginWebhook, "", "granted", nil))
	assert.NoError(t, svc.AuthorizeTarget(ctx, deployment.OriginInteractive, "dev", "granted", nil))
	assert.ErrorIs(t, svc.AuthorizeTarget(ctx, deployment.OriginWebhook, "", "other", nil), deployment.ErrControlPlaneWebhook)

	// Stopgap off (app networks isolated): everyone may deploy.
	svc.WithControlPlaneStopgap(false, nil)
	assert.NoError(t, svc.AuthorizeTarget(ctx, deployment.OriginInteractive, "dev", "s", nil))
	assert.NoError(t, svc.AuthorizeTarget(ctx, deployment.OriginWebhook, "", "s", nil))
}

type panelRole bool

func (r panelRole) PanelHostRunsApps(context.Context) bool { return bool(r) }

// Under Control plane only (D-62) nothing deploys to the panel host: not an
// operator, not a granted exception, not a push. Other servers are unaffected.
func TestPanelRoleControlPlaneOnly(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(&mockRepo{createdID: "d"}, &mockFactory{orch: &mockOrchestrator{}}, &mockStatusUpdater{})
	svc.WithPolicyChecker(&fakePolicy{controlPlane: map[string]bool{"granted": true}})
	svc.WithControlPlaneStopgap(false, fakeOperators{"op": true})
	svc.WithPanelRole(panelRole(false))

	_, err := svc.Trigger(ctx, deployment.TriggerInput{ServiceID: "s", SourceType: "docker-image", ActorID: "op"})
	assert.ErrorIs(t, err, deployment.ErrPanelHostNoApps)
	assert.ErrorIs(t, svc.AuthorizeTarget(ctx, deployment.OriginInteractive, "op", "granted", nil), deployment.ErrPanelHostNoApps)
	assert.ErrorIs(t, svc.AuthorizeTarget(ctx, deployment.OriginWebhook, "", "s", nil), deployment.ErrPanelHostNoApps)
	assert.NoError(t, svc.AuthorizeTarget(ctx, deployment.OriginWebhook, "", "s", &server.Server{ID: "srv"}))

	svc.WithPanelRole(panelRole(true))
	assert.NoError(t, svc.AuthorizeTarget(ctx, deployment.OriginInteractive, "dev", "s", nil))
}

// A refused git-push deploy is recorded as a failed deployment whose log
// says why, so a push is never silently ignored; nothing runs.
func TestTrigger_RefusedWebhookIsRecordedAsFailed(t *testing.T) {
	cases := map[string]struct {
		setup   func(svc *deployment.Service)
		input   deployment.TriggerInput
		wantErr error
		logHas  string
	}{
		"control-plane stopgap": {
			setup: func(svc *deployment.Service) {
				svc.WithControlPlaneStopgap(true, fakeOperators{})
			},
			input:   deployment.TriggerInput{ServiceID: "s", SourceType: "git"},
			wantErr: deployment.ErrControlPlaneWebhook,
			logHas:  "git-push deploys to the control-plane server are refused",
		},
		"server could not be loaded": {
			input:   deployment.TriggerInput{ServiceID: "s", SourceType: "git", ServerID: "srv-gone"},
			wantErr: deployment.ErrServerUnavailable,
			logHas:  "could not be loaded",
		},
		"deploy policy": {
			setup: func(svc *deployment.Service) {
				svc.WithPolicyChecker(&fakePolicy{deploy: blocked(composespec.RuleHostExec, "pre_deploy_commands[0]")})
			},
			input:  deployment.TriggerInput{ServiceID: "s", SourceType: "git", PreDeployCommands: []string{"id"}},
			logHas: "pre_deploy_commands[0]",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			repo := &mockRepo{createdID: "dep-push"}
			orch := &mockOrchestrator{}
			updater := &mockStatusUpdater{}
			svc := newTestService(repo, &mockFactory{orch: orch}, updater)
			if tc.setup != nil {
				tc.setup(svc)
			}
			in := tc.input
			in.Origin = deployment.OriginWebhook
			in.CommitSHA = "abc123"

			d, err := svc.Trigger(context.Background(), in)
			require.Error(t, err)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
			} else {
				var pv *composespec.ViolationsError
				assert.True(t, errors.As(err, &pv), "got %v", err)
			}
			require.NotNil(t, d, "the refused push is recorded")
			assert.Equal(t, "dep-push", d.ID)

			time.Sleep(30 * time.Millisecond)
			repo.mu.Lock()
			defer repo.mu.Unlock()
			assert.Equal(t, []deployment.Status{deployment.StatusFailed}, repo.statusHistory)
			assert.Contains(t, strings.Join(repo.logMessages, "\n"), tc.logHas)
			assert.Contains(t, repo.logMessages[0], "git push was refused")
			orch.mu.Lock()
			assert.False(t, orch.built || orch.ran, "nothing runs")
			orch.mu.Unlock()
			updater.mu.Lock()
			assert.Empty(t, updater.statuses, "the service's runtime status is left alone")
			updater.mu.Unlock()
		})
	}
}

// An interactive deploy refused before it starts creates no deployment.
func TestTrigger_InteractiveRefusalsCreateNoDeployment(t *testing.T) {
	cases := map[string]struct {
		input   deployment.TriggerInput
		wantErr error
	}{
		"server could not be loaded": {
			input:   deployment.TriggerInput{ServiceID: "s", SourceType: "git", ServerID: "srv-gone"},
			wantErr: deployment.ErrServerUnavailable,
		},
		"compose rollback": {
			input:   deployment.TriggerInput{ServiceID: "s", SourceType: "docker-compose", RollbackImageTag: "paas/s:old"},
			wantErr: deployment.ErrRollbackUnsupported,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			repo := &mockRepo{createdID: "dep-x"}
			svc := newTestService(repo, &mockFactory{orch: &mockOrchestrator{}}, &mockStatusUpdater{})
			in := tc.input
			in.Origin = deployment.OriginInteractive
			d, err := svc.Trigger(context.Background(), in)
			assert.ErrorIs(t, err, tc.wantErr)
			assert.Nil(t, d)
			time.Sleep(30 * time.Millisecond)
			repo.mu.Lock()
			defer repo.mu.Unlock()
			assert.Empty(t, repo.statusHistory)
		})
	}
}

type listingOrchestrator struct {
	mockOrchestrator
	containers []string
	execTarget string
}

func (o *listingOrchestrator) Rollout(ctx context.Context, spec rollout.Spec, _ deployment.Routing, hooks rollout.Hooks) rollout.Result {
	return rollout.Run(ctx, o, spec, hooks)
}

func (o *listingOrchestrator) Slots(context.Context, string) ([]string, error) {
	return o.containers, nil
}

func (o *listingOrchestrator) Exec(_ context.Context, name, _ string, _ func(string)) error {
	o.mu.Lock()
	o.execTarget = name
	o.mu.Unlock()
	return nil
}

func TestPostDeployCommand_ForeignContainerFails(t *testing.T) {
	repo := &mockRepo{createdID: "dep-pd"}
	orch := &listingOrchestrator{containers: []string{"paas-web-12345678-blue"}}
	svc := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})

	_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
		ServiceID:          "svc-1",
		SourceType:         "docker-image",
		DockerImage:        "nginx",
		PostDeployCommands: []deployment.PostDeployCommand{{ServiceName: "paas-postgres", Command: "psql -c 'select 1'"}},
	})
	require.NoError(t, err)
	waitForStatus(t, repo, deployment.StatusFailed, 2*time.Second)
	orch.mu.Lock()
	defer orch.mu.Unlock()
	assert.Empty(t, orch.execTarget, "never exec into another service's container")
}

// ─── Compose routing (D-19: the platform routes compose domains) ─────────────

type routeTrackingOrchestrator struct {
	mockOrchestrator
	composeRan bool
	route      *deployment.ComposeRoute
	env        map[string]string
}

func (o *routeTrackingOrchestrator) RunCompose(_ context.Context, _ string, _, _, _ string, env map[string]string, route *deployment.ComposeRoute, _ deployment.ComposeNetworks, _ map[string]deployment.ComposeLimit, _ *deployment.RegistryAuth, _ rollout.Hooks) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.composeRan = true
	o.route = route
	o.env = env
	return nil
}

func TestPipeline_ComposeRouting(t *testing.T) {
	access := service.AccessControl{BasicAuthUser: "admin", BasicAuthPassword: "pw", IPAllowlist: []string{"10.0.0.0/8"}}
	cases := map[string]struct {
		chosen    string
		services  []string // from the policy's load of the file; nil = unknown
		domains   []string
		port      int
		want      string // compose service routed; "" = no route
		wantFail  bool
		logSubstr string
	}{
		"automatic, one service":         {services: []string{"web"}, domains: []string{"a.example.com"}, port: 8080, want: "web", logSubstr: "the file's only service"},
		"automatic, several services":    {services: []string{"db", "web"}, domains: []string{"a.example.com"}, port: 8080, logSubstr: "several services (db, web)"},
		"automatic, services unknown":    {domains: []string{"a.example.com"}, port: 8080, logSubstr: "not known"},
		"chosen":                         {chosen: "web", services: []string{"db", "web"}, domains: []string{"a.example.com", "b.example.com"}, port: 3000, want: "web", logSubstr: "chosen in Settings → Domain Routing"},
		"chosen, services unknown":       {chosen: "web", domains: []string{"a.example.com"}, port: 3000, want: "web"},
		"chosen service not in the file": {chosen: "api", services: []string{"web"}, domains: []string{"a.example.com"}, port: 3000, wantFail: true, logSubstr: `"api", which is not in the compose file`},
		"automatic, unroutable name":     {services: []string{"_web"}, domains: []string{"a.example.com"}, port: 8080, logSubstr: `cannot route to compose service "_web"`},
		"no domains":                     {services: []string{"web"}, port: 8080},
		"no port":                        {services: []string{"web"}, domains: []string{"a.example.com"}, logSubstr: "set the port compose service web listens on"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			repo := &mockRepo{createdID: "dep-route"}
			orch := &routeTrackingOrchestrator{}
			updater := &mockStatusUpdater{}
			svc := newTestService(repo, &mockFactory{orch: orch}, updater)
			if tc.services != nil {
				svc.WithPolicyChecker(&fakePolicy{deploy: deploypolicy.Decision{Mode: deploypolicy.ModeEnforce, ComposeServices: tc.services}})
			}
			_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
				ServiceID:           "11111111-2222-3333-4444-555555555555",
				ServiceName:         "stack",
				SourceType:          "docker-compose",
				ComposeContent:      "services:\n  web:\n    image: nginx\n",
				ComposeRouteService: tc.chosen,
				Domains:             tc.domains,
				Port:                tc.port,
				EnvVars:             []string{"A=1"},
				AccessControl:       access,
			})
			require.NoError(t, err)
			if tc.wantFail {
				waitForStatus(t, repo, deployment.StatusFailed, 2*time.Second)
			} else {
				waitForStatus(t, repo, deployment.StatusRunning, 2*time.Second)
				// Its success records the service it routed, "" none: where
				// other services reach the stack (connect_to).
				assert.Eventually(t, func() bool {
					updater.mu.Lock()
					defer updater.mu.Unlock()
					return len(updater.networks) > 0
				}, 2*time.Second, 10*time.Millisecond)
			}
			updater.mu.Lock()
			if tc.wantFail {
				assert.Empty(t, updater.networks, "a failed deploy records nothing")
			} else {
				assert.Equal(t, []string{tc.want}, updater.networks)
			}
			updater.mu.Unlock()

			orch.mu.Lock()
			defer orch.mu.Unlock()
			if tc.wantFail {
				assert.False(t, orch.composeRan, "compose never runs with a routing error")
			} else {
				require.True(t, orch.composeRan)
				want := map[string]string{"A": "1"}
				if len(tc.domains) > 0 {
					want["TOFT_URL"] = "https://" + tc.domains[0] // D-147: the stack's address
				}
				assert.Equal(t, want, orch.env, "compose gets exactly the service's env vars and its address")
				if tc.want == "" {
					assert.Nil(t, orch.route)
				} else {
					require.NotNil(t, orch.route)
					assert.Equal(t, tc.want, orch.route.Service)
					assert.Equal(t, tc.port, orch.route.Port)
					assert.Equal(t, tc.domains, orch.route.Domains)
					assert.Equal(t, access, orch.route.Access)
				}
			}
			if tc.logSubstr != "" {
				repo.mu.Lock()
				defer repo.mu.Unlock()
				assert.Contains(t, strings.Join(repo.logMessages, "\n"), tc.logSubstr)
			}
		})
	}
}

// ─── Deployments are read through their own service ──────────────────────────

type ownedRepo struct {
	mockRepo
	deployments map[string]*deployment.Deployment
}

func (r *ownedRepo) GetByID(_ context.Context, id string) (*deployment.Deployment, error) {
	if d, ok := r.deployments[id]; ok {
		return d, nil
	}
	return nil, errors.New("no rows")
}

func TestGetDeploymentForService(t *testing.T) {
	repo := &ownedRepo{deployments: map[string]*deployment.Deployment{
		"dep-a": {ID: "dep-a", ServiceID: "svc-a", ImageTag: "paas/svc-a:1"},
	}}
	svc := newTestService(repo, &mockFactory{orch: &mockOrchestrator{}}, &mockStatusUpdater{})
	ctx := context.Background()

	d, err := svc.GetDeploymentForService(ctx, "svc-a", "dep-a")
	require.NoError(t, err)
	assert.Equal(t, "paas/svc-a:1", d.ImageTag)

	_, err = svc.GetDeploymentForService(ctx, "svc-b", "dep-a")
	assert.ErrorIs(t, err, deployment.ErrDeploymentNotFound, "another service's deployment is not found")
	_, err = svc.GetDeploymentForService(ctx, "svc-a", "missing")
	assert.ErrorIs(t, err, deployment.ErrDeploymentNotFound)
}

// ─── Blue/green switch (the previous slot is retired after the checks) ───────

// newSlotName is the slot slotOrchestrator's Start starts.
const newSlotName = "paas-web-svc-1-green"

// slotOrchestrator is a mockOrchestrator whose rollout host manages
// blue/green slots, like rollout.DockerHost. Its Start reports newSlotName
// as the new slot and previous as what was serving. It records the switch's
// steps in order.
type slotOrchestrator struct {
	mockOrchestrator
	previous       []string
	routingChanged bool
	healthErr      error
	execErr        error
	finishErr      error
	abortErr       error
	restoreErr     error
	readyErr       error
	readyPort      int
	steps          []string
	activeSeen     string // the SlotSwitch's ActiveDeployment as Start saw it
}

func (o *slotOrchestrator) step(s string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.steps = append(o.steps, s)
}

func (o *slotOrchestrator) recordedSteps() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.steps...)
}

func (o *slotOrchestrator) Rollout(ctx context.Context, spec rollout.Spec, _ deployment.Routing, hooks rollout.Hooks) rollout.Result {
	return rollout.Run(ctx, o, spec, hooks)
}

func (o *slotOrchestrator) Start(_ context.Context, _ rollout.Spec, sw *rollout.SlotSwitch) error {
	sw.New = newSlotName
	sw.Previous = append([]string(nil), o.previous...)
	sw.RoutingChanged = o.routingChanged
	o.mu.Lock()
	o.activeSeen = sw.ActiveDeployment
	o.mu.Unlock()
	if sw.Recreate {
		// As StartSlot: the previous slots stop before the new one is
		// created, and a stopped container defines no route.
		sw.RoutingChanged = false
		for _, p := range o.previous {
			o.step("stop " + p)
			sw.Stopped = append(sw.Stopped, p)
		}
	}
	o.step("run")
	return o.runErr
}

func (o *slotOrchestrator) Health(_ context.Context, name string, _ int, _ rollout.HealthCheck) error {
	o.step("health " + name)
	return o.healthErr
}

func (o *slotOrchestrator) Slots(context.Context, string) ([]string, error) {
	return append(append([]string(nil), o.previous...), newSlotName), nil
}

func (o *slotOrchestrator) Exec(_ context.Context, name, _ string, _ func(string)) error {
	o.step("exec " + name)
	return o.execErr
}

func (o *slotOrchestrator) AwaitHealthy(context.Context, string, bool) error { return nil }

func (o *slotOrchestrator) FinishSwitch(_ context.Context, _, keep string, _ bool) error {
	o.step("finish " + keep)
	return o.finishErr
}

func (o *slotOrchestrator) AbortSwitch(_ context.Context, _, name string) error {
	o.step("abort " + name)
	return o.abortErr
}

func (o *slotOrchestrator) RestorePrevious(_ context.Context, _ string, names []string) error {
	o.step("restore " + strings.Join(names, ","))
	return o.restoreErr
}

func (o *slotOrchestrator) WaitReady(_ context.Context, name string, port int, _ func(string)) error {
	o.step("ready " + name)
	o.mu.Lock()
	o.readyPort = port
	o.mu.Unlock()
	return o.readyErr
}

// checkedDeploy is a deploy of svc-1 with a health check of one attempt
// (rollout's tests cover the retries) and a post-deploy command.
func checkedDeploy(sourceType string) deployment.TriggerInput {
	return deployment.TriggerInput{
		ServiceID:          "svc-1",
		ServiceName:        "web",
		SourceType:         sourceType,
		GitRepoURL:         "https://github.com/example/repo",
		GitBranch:          "main",
		DockerImage:        "nginx:alpine",
		Port:               3000,
		HealthCheckPath:    "/health",
		HealthCheckTimeout: 2,
		HealthCheckRetries: 1,
		PostDeployCommands: []deployment.PostDeployCommand{{Command: "./migrate"}},
	}
}

func (m *mockRepo) hasLog(fragment string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, l := range m.logMessages {
		if strings.Contains(l, fragment) {
			return true
		}
	}
	return false
}

func (m *mockStatusUpdater) recorded() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.statuses...)
}

func TestPipeline_BlueGreenRetiresThePreviousSlotAfterTheChecks(t *testing.T) {
	for _, source := range []string{"git", "docker-image"} {
		t.Run(source, func(t *testing.T) {
			repo := &mockRepo{createdID: "dep-bg-" + source}
			orch := &slotOrchestrator{previous: []string{"paas-web-svc-1-blue"}}
			updater := &mockStatusUpdater{}
			_, err := newTestService(repo, &mockFactory{orch: orch}, updater).Trigger(context.Background(), checkedDeploy(source))
			require.NoError(t, err)

			require.Eventually(t, func() bool { return repo.hasLog("Deployment complete") }, 2*time.Second, 5*time.Millisecond)
			assert.Equal(t, []string{"run", "health " + newSlotName, "exec " + newSlotName, "finish " + newSlotName}, orch.recordedSteps(),
				"only the new slot is checked, the post-deploy command runs in it, and the previous slot is retired last")
			assert.Equal(t, []string{"running"}, updater.recorded())
			assert.True(t, repo.hasLog("retired the previous container: paas-web-svc-1-blue"))
		})
	}
}

func TestPipeline_BlueGreenFailureKeepsThePreviousSlotServing(t *testing.T) {
	cases := []struct {
		name  string
		orch  *slotOrchestrator
		steps []string
	}{
		{
			name:  "health check fails",
			orch:  &slotOrchestrator{healthErr: errors.New("connection refused")},
			steps: []string{"run", "health " + newSlotName, "abort " + newSlotName},
		},
		{
			name:  "post-deploy command fails",
			orch:  &slotOrchestrator{execErr: errors.New("exit status 1")},
			steps: []string{"run", "health " + newSlotName, "exec " + newSlotName, "abort " + newSlotName},
		},
		{
			name:  "the new slot fails to start",
			orch:  &slotOrchestrator{mockOrchestrator: mockOrchestrator{runErr: errors.New("ContainerStart: OCI error")}},
			steps: []string{"run", "abort " + newSlotName},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.orch.previous = []string{"paas-web-svc-1-blue"}
			repo := &mockRepo{createdID: "dep-bg-fail"}
			updater := &mockStatusUpdater{}
			_, err := newTestService(repo, &mockFactory{orch: tc.orch}, updater).Trigger(context.Background(), checkedDeploy("docker-image"))
			require.NoError(t, err)

			waitForStatus(t, repo, deployment.StatusFailed, 2*time.Second)
			assert.Equal(t, tc.steps, tc.orch.recordedSteps(), "the new slot is removed and the previous one is never retired")
			assert.Empty(t, updater.recorded(), "the service's runtime status and active deployment stay on the previous deployment")
			assert.True(t, repo.hasLog("previous container keeps serving (paas-web-svc-1-blue)"))
			repo.mu.Lock()
			assert.NotContains(t, repo.statusHistory, deployment.StatusRunning)
			repo.mu.Unlock()
		})
	}
}

func TestPipeline_BlueGreenFirstDeployFailureLeavesTheContainer(t *testing.T) {
	repo := &mockRepo{createdID: "dep-bg-first"}
	orch := &slotOrchestrator{healthErr: errors.New("connection refused")} // nothing was serving
	updater := &mockStatusUpdater{}
	_, err := newTestService(repo, &mockFactory{orch: orch}, updater).Trigger(context.Background(), checkedDeploy("docker-image"))
	require.NoError(t, err)

	require.Eventually(t, func() bool { return len(updater.recorded()) > 0 }, 2*time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"error"}, updater.recorded(), "as before: nothing else runs")
	assert.Equal(t, []string{"run", "health " + newSlotName}, orch.recordedSteps(), "kept so its logs can be read")
}

func TestPipeline_BlueGreenRetireFailureStillSucceeds(t *testing.T) {
	repo := &mockRepo{createdID: "dep-bg-retire"}
	orch := &slotOrchestrator{previous: []string{"paas-web-svc-1-blue"}, finishErr: errors.New("SSH connection lost")}
	updater := &mockStatusUpdater{}
	_, err := newTestService(repo, &mockFactory{orch: orch}, updater).Trigger(context.Background(), checkedDeploy("git"))
	require.NoError(t, err)

	require.Eventually(t, func() bool { return repo.hasLog("Deployment complete") }, 2*time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"running"}, updater.recorded(), "the new slot passed its checks and serves")
	assert.True(t, repo.hasLog("Could not retire the previous container: SSH connection lost"))
}

func TestPipeline_BlueGreenNewSlotThatStoppedFailsTheDeploy(t *testing.T) {
	// No health check, so the container exiting right after it started
	// shows only when the switch completes: the deploy fails and the
	// previous slot keeps serving.
	repo := &mockRepo{createdID: "dep-bg-exited"}
	orch := &slotOrchestrator{
		previous:  []string{"paas-web-svc-1-blue"},
		finishErr: fmt.Errorf("%w (%s), so the previous one was left in place", rollout.ErrNewSlotNotRunning, newSlotName),
	}
	updater := &mockStatusUpdater{}
	in := checkedDeploy("docker-image")
	in.HealthCheckRetries = 0
	_, err := newTestService(repo, &mockFactory{orch: orch}, updater).Trigger(context.Background(), in)
	require.NoError(t, err)

	waitForStatus(t, repo, deployment.StatusFailed, 2*time.Second)
	assert.Equal(t, []string{"run", "exec " + newSlotName, "finish " + newSlotName, "abort " + newSlotName}, orch.recordedSteps())
	assert.Empty(t, updater.recorded(), "the previous deployment stays active")
	assert.True(t, repo.hasLog("the new container is not running"))
	assert.True(t, repo.hasLog("previous container keeps serving (paas-web-svc-1-blue)"))
	repo.mu.Lock()
	assert.NotContains(t, repo.statusHistory, deployment.StatusRunning)
	repo.mu.Unlock()
}

// ─── Stop then start (D-42) ──────────────────────────────────────────────────

func recreateDeploy() deployment.TriggerInput {
	in := checkedDeploy("docker-image")
	in.Recreate = true
	return in
}

func TestPipeline_RecreateStopsThePreviousSlotFirst(t *testing.T) {
	repo := &mockRepo{createdID: "dep-rc"}
	orch := &slotOrchestrator{previous: []string{"paas-web-svc-1-blue"}, routingChanged: true}
	updater := &mockStatusUpdater{}
	_, err := newTestService(repo, &mockFactory{orch: orch}, updater).Trigger(context.Background(), recreateDeploy())
	require.NoError(t, err)

	require.Eventually(t, func() bool { return repo.hasLog("Deployment complete") }, 2*time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"stop paas-web-svc-1-blue", "run", "ready " + newSlotName, "health " + newSlotName, "exec " + newSlotName, "finish " + newSlotName},
		orch.recordedSteps(), "the routing cannot conflict with a stopped container, so the retire waits for the post-deploy commands")
	assert.Equal(t, []string{"running"}, updater.recorded())
	assert.Equal(t, 3000, orch.readyPort)
	assert.True(t, repo.hasLog("→ Stop then start"))
	assert.True(t, repo.hasLog("■ Stopped the previous container: paas-web-svc-1-blue"))
	assert.True(t, repo.hasLog("→ Waiting for the new container to accept connections on port 3000"))
	assert.True(t, repo.hasLog("✓ Ready"))
}

func TestPipeline_RecreateFailureStartsThePreviousSlotAgain(t *testing.T) {
	cases := []struct {
		name  string
		orch  *slotOrchestrator
		steps []string
		logs  []string
	}{
		{
			name:  "health check fails",
			orch:  &slotOrchestrator{healthErr: errors.New("connection refused")},
			steps: []string{"stop paas-web-svc-1-blue", "run", "ready " + newSlotName, "health " + newSlotName, "abort " + newSlotName, "restore paas-web-svc-1-blue"},
		},
		{
			name:  "post-deploy command fails",
			orch:  &slotOrchestrator{execErr: errors.New("exit status 1")},
			steps: []string{"stop paas-web-svc-1-blue", "run", "ready " + newSlotName, "health " + newSlotName, "exec " + newSlotName, "abort " + newSlotName, "restore paas-web-svc-1-blue"},
		},
		{
			name: "the new slot exits before it is ready",
			orch: &slotOrchestrator{readyErr: &rollout.NotReadyError{
				Reason:    "the new container exited (code 1) before it was ready",
				LastLines: []string{"[ERROR] [MY-000067] [Server] unknown variable 'no-such-option'."},
			}},
			steps: []string{"stop paas-web-svc-1-blue", "run", "ready " + newSlotName, "abort " + newSlotName, "restore paas-web-svc-1-blue"},
			logs: []string{
				"✗ the new container exited (code 1) before it was ready",
				"  │ [ERROR] [MY-000067] [Server] unknown variable 'no-such-option'.",
			},
		},
		{
			name:  "the new slot fails to start",
			orch:  &slotOrchestrator{mockOrchestrator: mockOrchestrator{runErr: errors.New("ContainerStart: OCI error")}},
			steps: []string{"stop paas-web-svc-1-blue", "run", "abort " + newSlotName, "restore paas-web-svc-1-blue"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.orch.previous = []string{"paas-web-svc-1-blue"}
			repo := &mockRepo{createdID: "dep-rc-fail"}
			updater := &mockStatusUpdater{}
			_, err := newTestService(repo, &mockFactory{orch: tc.orch}, updater).Trigger(context.Background(), recreateDeploy())
			require.NoError(t, err)

			waitForStatus(t, repo, deployment.StatusFailed, 2*time.Second)
			assert.Equal(t, tc.steps, tc.orch.recordedSteps(), "the new slot is removed before the previous one starts again")
			assert.Empty(t, updater.recorded(), "the previous deployment serves again")
			assert.True(t, repo.hasLog("↩ Started the previous container again (paas-web-svc-1-blue)"))
			for _, l := range tc.logs {
				assert.True(t, repo.hasLog(l), l)
			}
		})
	}
}

func TestPipeline_RecreateFailureThatCannotRestore(t *testing.T) {
	t.Run("the new slot cannot be removed: the previous one stays stopped", func(t *testing.T) {
		repo := &mockRepo{createdID: "dep-rc-abort"}
		orch := &slotOrchestrator{previous: []string{"paas-web-svc-1-blue"}, healthErr: errors.New("connection refused"), abortErr: errors.New("SSH connection lost")}
		updater := &mockStatusUpdater{}
		_, err := newTestService(repo, &mockFactory{orch: orch}, updater).Trigger(context.Background(), recreateDeploy())
		require.NoError(t, err)

		require.Eventually(t, func() bool { return len(updater.recorded()) > 0 }, 2*time.Second, 5*time.Millisecond)
		assert.Equal(t, []string{"stop paas-web-svc-1-blue", "run", "ready " + newSlotName, "health " + newSlotName, "abort " + newSlotName}, orch.recordedSteps(),
			"never two containers on the same data")
		assert.Equal(t, []string{"error"}, updater.recorded())
		assert.True(t, repo.hasLog("stays stopped while the new one may still run"))
	})

	t.Run("the previous slot does not start", func(t *testing.T) {
		repo := &mockRepo{createdID: "dep-rc-restore"}
		orch := &slotOrchestrator{previous: []string{"paas-web-svc-1-blue"}, healthErr: errors.New("connection refused"), restoreErr: errors.New("the container is gone")}
		updater := &mockStatusUpdater{}
		_, err := newTestService(repo, &mockFactory{orch: orch}, updater).Trigger(context.Background(), recreateDeploy())
		require.NoError(t, err)

		require.Eventually(t, func() bool { return len(updater.recorded()) > 0 }, 2*time.Second, 5*time.Millisecond)
		assert.Equal(t, []string{"error"}, updater.recorded(), "nothing serves")
		assert.True(t, repo.hasLog("✗ Could not start the previous container again: the container is gone"))
	})
}

func TestPipeline_RollingNeverStopsThePreviousSlotFirst(t *testing.T) {
	repo := &mockRepo{createdID: "dep-rolling"}
	orch := &slotOrchestrator{previous: []string{"paas-web-svc-1-blue"}}
	_, err := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{}).Trigger(context.Background(), checkedDeploy("docker-image"))
	require.NoError(t, err)

	require.Eventually(t, func() bool { return repo.hasLog("Deployment complete") }, 2*time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"run", "health " + newSlotName, "exec " + newSlotName, "finish " + newSlotName}, orch.recordedSteps())
	assert.False(t, repo.hasLog("Stop then start"))
}

func TestPipeline_BlueGreenRoutingChangeRetiresThePreviousSlotAfterTheHealthCheck(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		repo := &mockRepo{createdID: "dep-bg-route"}
		orch := &slotOrchestrator{previous: []string{"paas-web-svc-1-blue"}, routingChanged: true}
		updater := &mockStatusUpdater{}
		_, err := newTestService(repo, &mockFactory{orch: orch}, updater).Trigger(context.Background(), checkedDeploy("docker-image"))
		require.NoError(t, err)

		require.Eventually(t, func() bool { return repo.hasLog("Deployment complete") }, 2*time.Second, 5*time.Millisecond)
		assert.Equal(t, []string{"run", "health " + newSlotName, "finish " + newSlotName, "exec " + newSlotName}, orch.recordedSteps(),
			"retired once healthy, before the post-deploy commands, and only once")
		assert.True(t, repo.hasLog("The routing changed"))
		assert.Equal(t, []string{"running"}, updater.recorded())
	})

	t.Run("a post-deploy command fails afterwards", func(t *testing.T) {
		repo := &mockRepo{createdID: "dep-bg-route-fail"}
		orch := &slotOrchestrator{previous: []string{"paas-web-svc-1-blue"}, routingChanged: true, execErr: errors.New("exit status 1")}
		updater := &mockStatusUpdater{}
		_, err := newTestService(repo, &mockFactory{orch: orch}, updater).Trigger(context.Background(), checkedDeploy("docker-image"))
		require.NoError(t, err)

		waitForStatus(t, repo, deployment.StatusFailed, 2*time.Second)
		assert.Equal(t, []string{"run", "health " + newSlotName, "finish " + newSlotName, "exec " + newSlotName}, orch.recordedSteps(),
			"the new slot is the only one left, so it is not removed")
		assert.True(t, repo.hasLog("already retired, so the new one keeps running"))
		assert.Equal(t, []string{"error"}, updater.recorded())
	})

	t.Run("the health check fails: nothing was retired", func(t *testing.T) {
		repo := &mockRepo{createdID: "dep-bg-route-unhealthy"}
		orch := &slotOrchestrator{previous: []string{"paas-web-svc-1-blue"}, routingChanged: true, healthErr: errors.New("connection refused")}
		_, err := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{}).Trigger(context.Background(), checkedDeploy("docker-image"))
		require.NoError(t, err)

		waitForStatus(t, repo, deployment.StatusFailed, 2*time.Second)
		assert.Equal(t, []string{"run", "health " + newSlotName, "abort " + newSlotName}, orch.recordedSteps())
	})
}

// activeReader is a ServiceReader that knows one service's active deployment.
type activeReader struct{ active string }

func (r activeReader) Get(_ context.Context, id string) (*service.Service, error) {
	return &service.Service{ID: id, ActiveDeploymentID: r.active}, nil
}

func TestPipeline_BlueGreenTellsRunTheActiveDeployment(t *testing.T) {
	repo := &mockRepo{createdID: "dep-bg-active"}
	orch := &slotOrchestrator{previous: []string{"paas-web-svc-1-blue"}}
	svc := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
	svc.WithServiceReader(activeReader{active: "dep-prev"})
	_, err := svc.Trigger(context.Background(), checkedDeploy("docker-image"))
	require.NoError(t, err)

	require.Eventually(t, func() bool { return repo.hasLog("Deployment complete") }, 2*time.Second, 5*time.Millisecond)
	orch.mu.Lock()
	defer orch.mu.Unlock()
	assert.Equal(t, "dep-prev", orch.activeSeen, "so the rollout keeps that deployment's slot when both are running")
}

// ─── Restart recovery: the pipeline's side ───────────────────────────────────

func TestPipeline_RolloutCarriesTheDeploymentID(t *testing.T) {
	repo := &mockRepo{createdID: "dep-ctx"}
	orch := &mockOrchestrator{}
	_, err := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{}).Trigger(context.Background(), deployment.TriggerInput{
		ServiceID: "svc-1", SourceType: "docker-image", DockerImage: "nginx",
	})
	require.NoError(t, err)
	waitForStatus(t, repo, deployment.StatusRunning, 2*time.Second)

	orch.mu.Lock()
	defer orch.mu.Unlock()
	assert.Equal(t, "dep-ctx", orch.runDeploymentID, "the orchestrator labels the containers with it")
}

// gatedOrchestrator blocks in BuildFromGit until release is closed, then
// fails the build with buildErr.
type gatedOrchestrator struct {
	mockOrchestrator
	building chan struct{}
	release  chan struct{}
}

func newGatedOrchestrator(buildErr error) *gatedOrchestrator {
	return &gatedOrchestrator{
		mockOrchestrator: mockOrchestrator{buildErr: buildErr},
		building:         make(chan struct{}),
		release:          make(chan struct{}),
	}
}

func (o *gatedOrchestrator) BuildFromGit(ctx context.Context, a, b, c, d string, e map[string]*string, logFn func(string)) (deployment.Commit, error) {
	close(o.building)
	<-o.release
	return o.mockOrchestrator.BuildFromGit(ctx, a, b, c, d, e, logFn)
}

func gitDeploy() deployment.TriggerInput {
	return deployment.TriggerInput{ServiceID: "svc-1", SourceType: "git", GitRepoURL: "https://github.com/example/repo", GitBranch: "main"}
}

func TestMarkInterruptedSkipsDeploysRunningHere(t *testing.T) {
	repo := &mockRepo{createdID: "dep-live", interrupted: []deployment.Deployment{{ID: "dep-old-1"}, {ID: "dep-old-2"}}}
	orch := newGatedOrchestrator(nil)
	svc := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
	_, err := svc.Trigger(context.Background(), gitDeploy())
	require.NoError(t, err)
	<-orch.building
	assert.Equal(t, 1, svc.InFlightCount())

	marked, err := svc.MarkInterrupted(context.Background())
	require.NoError(t, err)
	assert.Len(t, marked, 2)
	repo.mu.Lock()
	assert.Equal(t, []string{"dep-live"}, repo.excluded, "a deploy this process runs is not interrupted")
	interruptedLines := 0
	for _, l := range repo.logMessages {
		if strings.Contains(l, "Interrupted: the control plane restarted") {
			interruptedLines++
		}
	}
	repo.mu.Unlock()
	assert.Equal(t, 2, interruptedLines, "one log line per marked deployment")

	close(orch.release)
	waitForStatus(t, repo, deployment.StatusRunning, 2*time.Second)
	require.Eventually(t, func() bool { return svc.InFlightCount() == 0 }, time.Second, 5*time.Millisecond)
}

func TestMarkInterruptedAlsoReturnsDeploysAnEarlierStartDidNotFinishChecking(t *testing.T) {
	repo := &mockRepo{
		interrupted: []deployment.Deployment{{ID: "dep-now"}},
		// dep-now is also its service's newest with the bare marker.
		unchecked: []deployment.Deployment{{ID: "dep-now"}, {ID: "dep-earlier-start"}},
	}
	svc := newTestService(repo, &mockFactory{orch: &mockOrchestrator{}}, &mockStatusUpdater{})

	marked, err := svc.MarkInterrupted(context.Background())
	require.NoError(t, err)
	var ids []string
	for _, d := range marked {
		ids = append(ids, d.ID)
	}
	assert.Equal(t, []string{"dep-now", "dep-earlier-start"}, ids)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	interruptedLines := 0
	for _, l := range repo.logMessages {
		if strings.Contains(l, "Interrupted: the control plane restarted") {
			interruptedLines++
		}
	}
	assert.Equal(t, 1, interruptedLines, "only the deployments marked now get the line")
}

func TestBeginShutdownLeavesAFailingDeployForRecovery(t *testing.T) {
	repo := &mockRepo{createdID: "dep-shutdown"}
	orch := newGatedOrchestrator(errors.New("ssh: pool closed"))
	updater := &mockStatusUpdater{}
	svc := newTestService(repo, &mockFactory{orch: orch}, updater)
	_, err := svc.Trigger(context.Background(), gitDeploy())
	require.NoError(t, err)
	<-orch.building

	svc.BeginShutdown()
	close(orch.release)

	require.Eventually(t, func() bool { return repo.hasLog("recovered when it restarts (ssh: pool closed)") }, 2*time.Second, 5*time.Millisecond)
	require.Eventually(t, func() bool { return svc.InFlightCount() == 0 }, time.Second, 5*time.Millisecond)
	repo.mu.Lock()
	assert.Equal(t, []deployment.Status{deployment.StatusBuilding}, repo.statusHistory, "left in progress: not marked failed")
	repo.mu.Unlock()
	assert.Empty(t, updater.recorded(), "the service's runtime status is left for recovery")
}

func TestBeginShutdownLeavesAFailedSwitchForRecovery(t *testing.T) {
	repo := &mockRepo{createdID: "dep-shutdown-switch"}
	orch := &slotOrchestrator{previous: []string{"paas-web-svc-1-blue"}, healthErr: errors.New("ssh: pool closed")}
	updater := &mockStatusUpdater{}
	svc := newTestService(repo, &mockFactory{orch: orch}, updater)
	svc.BeginShutdown()
	_, err := svc.Trigger(context.Background(), checkedDeploy("docker-image"))
	require.NoError(t, err)

	require.Eventually(t, func() bool { return repo.hasLog("recovered when it restarts") }, 2*time.Second, 5*time.Millisecond)
	require.Eventually(t, func() bool { return svc.InFlightCount() == 0 }, time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"run", "health " + newSlotName}, orch.recordedSteps(), "the new slot is left for recovery to identify")
	assert.Empty(t, updater.recorded())
}

func TestBeginShutdownStillRecordsSuccess(t *testing.T) {
	repo := &mockRepo{createdID: "dep-shutdown-ok"}
	updater := &mockStatusUpdater{}
	svc := newTestService(repo, &mockFactory{orch: &mockOrchestrator{}}, updater)
	svc.BeginShutdown()
	_, err := svc.Trigger(context.Background(), gitDeploy())
	require.NoError(t, err)
	waitForStatus(t, repo, deployment.StatusRunning, 2*time.Second)
}

// repoReadingOrchestrator records what a repository compose deploy got: the
// compose file's content in the clone, and the env vars.
type repoReadingOrchestrator struct {
	mockOrchestrator
	composeFile string
	env         map[string]string
}

func (o *repoReadingOrchestrator) RunComposeFromRepo(_ context.Context, _ string, _, _, repoDir, file string, env map[string]string, _ *deployment.ComposeRoute, _ deployment.ComposeNetworks, _ map[string]deployment.ComposeLimit, _ *deployment.RegistryAuth, _ rollout.Hooks) error {
	b, err := os.ReadFile(filepath.Join(repoDir, file))
	o.mu.Lock()
	defer o.mu.Unlock()
	o.composeFile, o.env = string(b), env
	return err
}

// commitTo adds a commit to the repository infraRepo made and returns its SHA.
func commitTo(t *testing.T, repoURL string, files map[string]string) string {
	t.Helper()
	dir := strings.TrimPrefix(repoURL, "file://")
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	git("add", ".")
	git("commit", "-q", "-m", "next")
	return git("rev-parse", "HEAD")
}

func TestPipeline_ComposeChecksOutCheckoutSHAAndRecordsImageTag(t *testing.T) {
	src := infraRepo(t, map[string]string{"docker-compose.yml": "services:\n  a:\n    image: x:${IMAGE_TAG}\n"})
	old := commitTo(t, src, map[string]string{"docker-compose.yml": "services:\n  a:\n    image: old:${IMAGE_TAG}\n"})
	commitTo(t, src, map[string]string{"docker-compose.yml": "services:\n  a:\n    image: new:${IMAGE_TAG}\n"})

	repo := &mockRepo{createdID: "dep-pin"}
	orch := &repoReadingOrchestrator{}
	svc := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
	_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
		ServiceID: "svc-1", ServiceName: "infra", SourceType: "docker-compose",
		ComposeRepoURL: src, GitBranch: "main",
		CommitSHA: old, CheckoutSHA: old,
		EnvVars: []string{"IMAGE_TAG=v1.2.3", "A=1"},
	})
	require.NoError(t, err)
	waitForStatus(t, repo, deployment.StatusRunning, 5*time.Second)

	orch.mu.Lock()
	assert.Contains(t, orch.composeFile, "image: old:", "the stack's files come from CheckoutSHA, not the branch tip")
	assert.Equal(t, "v1.2.3", orch.env["IMAGE_TAG"])
	orch.mu.Unlock()
	repo.mu.Lock()
	defer repo.mu.Unlock()
	assert.Equal(t, "v1.2.3", repo.imageTags[len(repo.imageTags)-1], "the deployment records the IMAGE_TAG it ran with")
}

// A webhook's CommitSHA is a commit of the service's git repository, which
// need not be the compose repository: without CheckoutSHA the branch tip
// is deployed, as before.
func TestPipeline_ComposeWithoutCheckoutSHADeploysTheBranchTip(t *testing.T) {
	src := infraRepo(t, map[string]string{"docker-compose.yml": "services:\n  a:\n    image: first\n"})
	commitTo(t, src, map[string]string{"docker-compose.yml": "services:\n  a:\n    image: tip\n"})

	repo := &mockRepo{createdID: "dep-tip"}
	orch := &repoReadingOrchestrator{}
	svc := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
	_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
		ServiceID: "svc-1", ServiceName: "infra", SourceType: "docker-compose",
		ComposeRepoURL: src, GitBranch: "main",
		CommitSHA: "0123456789abcdef0123456789abcdef01234567", // from another repository
		Origin:    deployment.OriginWebhook,
	})
	require.NoError(t, err)
	waitForStatus(t, repo, deployment.StatusRunning, 5*time.Second)

	orch.mu.Lock()
	defer orch.mu.Unlock()
	assert.Contains(t, orch.composeFile, "image: tip")
	repo.mu.Lock()
	defer repo.mu.Unlock()
	assert.Equal(t, "", repo.imageTags[len(repo.imageTags)-1], "no IMAGE_TAG, nothing recorded")
}

type dnsServers map[string]bool

func (d dnsServers) HasDNSProvider(_ context.Context, id string) (bool, error) { return d[id], nil }

func TestTrigger_WildcardDomainsNeedADNSProvider(t *testing.T) {
	cases := map[string]struct {
		server  *server.Server
		checker deployment.DNSProviderChecker
		want    error
	}{
		"control-plane server":           {nil, dnsServers{}, deployment.ErrWildcardNeedsDNS},
		"remote server without provider": {&server.Server{ID: "srv-1"}, dnsServers{}, deployment.ErrWildcardNeedsDNS},
		"remote server with provider":    {&server.Server{ID: "srv-1"}, dnsServers{"srv-1": true}, nil},
		"no checker wired":               {&server.Server{ID: "srv-1"}, nil, deployment.ErrWildcardNeedsDNS},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			repo := &mockRepo{createdID: "dep-w"}
			svc := newTestService(repo, &mockFactory{orch: &mockOrchestrator{}}, &mockStatusUpdater{})
			if tc.checker != nil {
				svc.WithDNSChecker(tc.checker)
			}
			_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
				ServiceID: "s", SourceType: "docker-image", DockerImage: "nginx", Port: 80,
				Server: tc.server, ServerID: serverIDOf(tc.server), Domains: []string{"app.example.com", "*.example.com"},
				Origin: deployment.OriginInteractive,
			})
			if tc.want == nil {
				require.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

type sandboxServers map[string]string // server ID → Sandboxed state; "strict:"+ID → the strict network's

func (s sandboxServers) Sandboxed(_ context.Context, id string) (server.Level, string, error) {
	return s.level("sandboxed", id)
}

func (s sandboxServers) SandboxedStrict(_ context.Context, id string) (server.Level, string, error) {
	return s.level("sandboxed_strict", "strict:"+id)
}

func (s sandboxServers) level(level, key string) (server.Level, string, error) {
	state, ok := s[key]
	if !ok {
		state = "unknown"
	}
	return server.Level{ID: level, State: state, Reason: "the probe's reason"}, "", nil
}

type isolationReader struct{ effective, serverID string }

func (r isolationReader) Get(_ context.Context, id string) (*service.Service, error) {
	return &service.Service{ID: id, ServerID: r.serverID, EffectiveIsolation: r.effective, PidsLimit: 512}, nil
}

// A Sandboxed service (D-95) deploys only to a server whose gVisor runs,
// the panel's own included (D-97), and is refused before anything starts
// everywhere else.
func TestTrigger_SandboxedNeedsGvisorOnTheServer(t *testing.T) {
	srv := &server.Server{ID: "srv-1", Name: "fsn1-prod-01"}
	cases := map[string]struct {
		effective string
		server    *server.Server
		checker   deployment.SandboxChecker
		want      string // "" deploys
	}{
		"standard anywhere":    {service.IsolationStandard, nil, nil, ""},
		"sandboxed, available": {service.IsolationSandboxed, srv, sandboxServers{"srv-1": "available"}, ""},
		"panel's own, gVisor":  {service.IsolationSandboxed, nil, sandboxServers{"": "available"}, ""},
		"panel's own, none":    {service.IsolationSandboxed, nil, sandboxServers{"": "not_installed"}, "the panel's own server has no gVisor: an instance operator installs it on Instance → Server"},
		"panel's own, unread":  {service.IsolationSandboxed, nil, sandboxServers{}, "the panel's own server hasn't been checked yet: Check again under Isolation on Instance → Server"},
		"no gVisor":            {service.IsolationSandboxed, srv, sandboxServers{"srv-1": "not_installed"}, "fsn1-prod-01 has no gVisor"},
		"failing smoke test":   {service.IsolationSandboxed, srv, sandboxServers{"srv-1": "failing"}, "the probe's reason"},
		"never probed":         {service.IsolationSandboxed, srv, sandboxServers{}, "hasn't been checked yet"},
		"no checker wired":     {service.IsolationSandboxed, srv, nil, "can't check servers"},
		"strict, available":    {service.IsolationSandboxedStrict, srv, sandboxServers{"strict:srv-1": "available"}, ""},
		"strict, gVisor only": {service.IsolationSandboxedStrict, srv, sandboxServers{"srv-1": "available", "strict:srv-1": "not_installed"},
			"fsn1-prod-01 has no strict network yet: an instance operator installs gVisor again"},
		"strict, no agent": {service.IsolationSandboxedStrict, srv, sandboxServers{"srv-1": "available", "strict:srv-1": "unavailable"},
			"can't run Sandboxed with a strict network: the probe's reason"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			repo := &mockRepo{createdID: "dep-s"}
			orch := &mockOrchestrator{}
			svc := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
			svc.WithServiceReader(isolationReader{effective: tc.effective, serverID: serverIDOf(tc.server)})
			if tc.checker != nil {
				svc.WithSandboxChecker(tc.checker)
			}
			_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
				ServiceID: "s", SourceType: "docker-image", DockerImage: "nginx", Port: 80,
				Server: tc.server, ServerID: serverIDOf(tc.server), Origin: deployment.OriginInteractive,
			})
			if tc.want != "" {
				require.ErrorIs(t, err, deployment.ErrSandboxUnavailable)
				assert.Contains(t, err.Error(), tc.want)
				assert.Empty(t, repo.statusHistory, "refused before a deployment exists")
				return
			}
			require.NoError(t, err)
			waitForStatus(t, repo, deployment.StatusRunning, 5*time.Second)
			orch.mu.Lock()
			defer orch.mu.Unlock()
			wantRuntime := map[string]string{service.IsolationSandboxed: "runsc", service.IsolationSandboxedStrict: "runsc-strict"}[tc.effective]
			assert.Equal(t, wantRuntime, orch.runSecurity.Runtime)
			assert.Equal(t, int64(512), orch.runSecurity.PidsLimit)
		})
	}
}

type checkReader struct{ mode, command string }

func (r checkReader) Get(_ context.Context, id string) (*service.Service, error) {
	return &service.Service{ID: id, ContainerHealth: r.mode, ContainerHealthCommand: r.command}, nil
}

// The service's container check goes into the slot: off, a command, or
// the image's (the zero value), from the service the deploy was built from
// or, without one, the service now.
func TestTrigger_ContainerCheck(t *testing.T) {
	cases := map[string]struct {
		reader   checkReader
		settings *service.Service
		want     rollout.ContainerCheck
	}{
		"image":            {checkReader{service.ContainerHealthImage, ""}, nil, rollout.ContainerCheck{}},
		"off":              {checkReader{service.ContainerHealthOff, "x"}, nil, rollout.ContainerCheck{Off: true}},
		"command":          {checkReader{service.ContainerHealthCommand, "pgrep worker"}, nil, rollout.ContainerCheck{Command: "pgrep worker"}},
		"the deploy's own": {checkReader{service.ContainerHealthOff, ""}, &service.Service{ID: "s", ContainerHealth: service.ContainerHealthImage}, rollout.ContainerCheck{}},
		// A push deploy whose Get failed carries the list's service: read again.
		"settings without it": {checkReader{service.ContainerHealthOff, ""}, &service.Service{ID: "s"}, rollout.ContainerCheck{Off: true}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			repo := &mockRepo{createdID: "dep-c"}
			orch := &mockOrchestrator{}
			svc := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
			svc.WithServiceReader(tc.reader)
			_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
				ServiceID: "s", SourceType: "docker-image", DockerImage: "nginx", Port: 80, Origin: deployment.OriginInteractive,
				Settings: tc.settings,
			})
			require.NoError(t, err)
			waitForStatus(t, repo, deployment.StatusRunning, 5*time.Second)
			orch.mu.Lock()
			defer orch.mu.Unlock()
			assert.Equal(t, tc.want, orch.runCheck)
		})
	}
}

// A Sandboxed service's push deploy that is refused is recorded as failed,
// with the reason in its log.
func TestTrigger_SandboxedWebhookRefusalIsRecorded(t *testing.T) {
	repo := &mockRepo{createdID: "dep-w"}
	svc := newTestService(repo, &mockFactory{orch: &mockOrchestrator{}}, &mockStatusUpdater{})
	svc.WithServiceReader(isolationReader{effective: service.IsolationSandboxed})
	svc.WithSandboxChecker(sandboxServers{"srv-1": "not_installed"})
	d, err := svc.Trigger(context.Background(), deployment.TriggerInput{
		ServiceID: "s", SourceType: "docker-image", DockerImage: "nginx", Port: 80,
		Server: &server.Server{ID: "srv-1", Name: "fsn1-prod-01"}, ServerID: "srv-1", Origin: deployment.OriginWebhook,
	})
	require.ErrorIs(t, err, deployment.ErrSandboxUnavailable)
	require.NotNil(t, d, "the refused push is a failed deployment")
}

// Nothing deploys to or from a server whose agent kept its own state over
// a restored panel (D-113); a push is recorded as refused.
func TestTrigger_FrozenServerRefusesDeploys(t *testing.T) {
	now := time.Now()
	frozen := &server.Server{ID: "srv-1", Name: "fsn1", AgentFrozenAt: &now}
	repo := &mockRepo{createdID: "dep-f"}
	svc := newTestService(repo, &mockFactory{orch: &mockOrchestrator{}}, &mockStatusUpdater{})
	in := deployment.TriggerInput{ServiceID: "s", SourceType: "docker-image", DockerImage: "nginx", Port: 80,
		Server: frozen, ServerID: "srv-1", Origin: deployment.OriginInteractive}
	_, err := svc.Trigger(context.Background(), in)
	require.ErrorIs(t, err, server.ErrServerFrozen)
	assert.Empty(t, repo.statusHistory)

	in.Origin = deployment.OriginWebhook
	d, err := svc.Trigger(context.Background(), in)
	require.ErrorIs(t, err, server.ErrServerFrozen)
	require.NotNil(t, d, "the refused push is a failed deployment")

	in.Origin, in.Server, in.ServerID = deployment.OriginInteractive, nil, ""
	in.MoveFrom = &deployment.MoveSource{ServerID: "srv-1", Server: frozen}
	_, err = svc.Trigger(context.Background(), in)
	require.ErrorIs(t, err, server.ErrServerFrozen, "nor a move off it")
}

// Nothing deploys, or moves, to a server still waiting for its first agent
// (D-153); once an agent joined it or it was provisioned, it deploys.
func TestTrigger_WaitingServerRefusesDeploys(t *testing.T) {
	now := time.Now()
	waiting := &server.Server{ID: "srv-1", Name: "server-1", Connection: server.ConnectionAgent}
	repo := &mockRepo{createdID: "dep-w"}
	svc := newTestService(repo, &mockFactory{orch: &mockOrchestrator{}}, &mockStatusUpdater{})
	in := deployment.TriggerInput{ServiceID: "s", SourceType: "docker-image", DockerImage: "nginx", Port: 80,
		Server: waiting, ServerID: "srv-1", Origin: deployment.OriginInteractive}
	_, err := svc.Trigger(context.Background(), in)
	require.ErrorIs(t, err, server.ErrServerNotReady)
	assert.Empty(t, repo.statusHistory)

	in.Origin = deployment.OriginWebhook
	d, err := svc.Trigger(context.Background(), in)
	require.ErrorIs(t, err, server.ErrServerNotReady)
	require.NotNil(t, d, "the refused push is a failed deployment")

	in.Origin, in.MoveFrom = deployment.OriginInteractive, &deployment.MoveSource{ServerID: "srv-0"}
	_, err = svc.Trigger(context.Background(), in)
	require.ErrorIs(t, err, server.ErrServerNotReady, "nor a move onto it")

	for name, srv := range map[string]*server.Server{
		"joined":      {ID: "srv-1", Name: "fsn1", Connection: server.ConnectionAgent, AgentKey: "ssh-ed25519 AAAA"},
		"provisioned": {ID: "srv-1", Name: "fsn1", Connection: server.ConnectionAgent, ProvisionedAt: &now},
		"ssh":         {ID: "srv-1", Name: "fsn1", Connection: server.ConnectionSSH},
	} {
		in.MoveFrom, in.Server = nil, srv
		_, err = svc.Trigger(context.Background(), in)
		assert.NotErrorIs(t, err, server.ErrServerNotReady, name)
	}
}

// updatingHosts is a deployment.HostUpdateGate: the hosts updating Docker
// or rebooting.
type updatingHosts map[string]bool

func (u updatingHosts) HostUpdating(_ context.Context, serverID string) (bool, error) {
	return u[serverID], nil
}

// Nothing deploys to a host while its Docker updates or it reboots (D-134),
// the panel's own ("") included; another host deploys.
func TestTrigger_UpdatingHostRefusesDeploys(t *testing.T) {
	repo := &mockRepo{createdID: "dep-u"}
	svc := newTestService(repo, &mockFactory{orch: &mockOrchestrator{}}, &mockStatusUpdater{})
	svc.WithHostUpdates(updatingHosts{"srv-1": true, "": true})
	in := deployment.TriggerInput{ServiceID: "s", SourceType: "docker-image", DockerImage: "nginx", Port: 80,
		Server: &server.Server{ID: "srv-1", Name: "fsn1"}, ServerID: "srv-1", Origin: deployment.OriginInteractive}
	_, err := svc.Trigger(context.Background(), in)
	require.ErrorIs(t, err, deployment.ErrServerUpdating)

	in.Server, in.ServerID = nil, ""
	_, err = svc.Trigger(context.Background(), in)
	require.ErrorIs(t, err, deployment.ErrServerUpdating, "nor to the panel's own server")

	in.MoveFrom = &deployment.MoveSource{ServerID: "srv-1", Server: &server.Server{ID: "srv-1", Name: "fsn1"}}
	in.Server, in.ServerID = &server.Server{ID: "srv-2", Name: "fsn2"}, "srv-2"
	_, err = svc.Trigger(context.Background(), in)
	require.ErrorIs(t, err, deployment.ErrServerUpdating, "nor a move off it")

	in.MoveFrom = nil
	_, err = svc.Trigger(context.Background(), in)
	require.NotErrorIs(t, err, deployment.ErrServerUpdating)
}

// updateStartsAfterCheck is a gate whose host begins updating Docker after
// the first check: between precheck and the deployment's record.
type updateStartsAfterCheck struct{ calls int }

func (u *updateStartsAfterCheck) HostUpdating(context.Context, string) (bool, error) {
	u.calls++
	return u.calls > 1, nil
}

// A Docker update that began while the deploy was being recorded fails the
// deploy before its pipeline starts.
func TestTrigger_UpdateStartedWhileRecordingFailsTheDeploy(t *testing.T) {
	repo := &mockRepo{createdID: "dep-r"}
	svc := newTestService(repo, &mockFactory{orch: &mockOrchestrator{}}, &mockStatusUpdater{})
	svc.WithHostUpdates(&updateStartsAfterCheck{})
	in := deployment.TriggerInput{ServiceID: "s", SourceType: "docker-image", DockerImage: "nginx", Port: 80,
		Server: &server.Server{ID: "srv-1", Name: "fsn1"}, ServerID: "srv-1", Origin: deployment.OriginInteractive}
	_, err := svc.Trigger(context.Background(), in)
	require.ErrorIs(t, err, deployment.ErrServerUpdating)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	assert.Equal(t, []deployment.Status{deployment.StatusFailed}, repo.statusHistory)
}

func serverIDOf(s *server.Server) string {
	if s == nil {
		return ""
	}
	return s.ID
}

// ─── Memory limits (D-41) ────────────────────────────────────────────────────

// fakeResources is the resources service: the panel's compose limits, and
// the defaults AddComposeDefaults adds to them.
type fakeResources struct {
	mu       sync.Mutex
	panel    map[string]resources.ComposeLimit
	defaults map[string]resources.DefaultAdded
	refs     []resources.ServiceRef
	adds     int // AddComposeDefaults calls
}

func (f *fakeResources) MemoryCheckFor(_ context.Context, ref resources.ServiceRef) (*resources.MemoryCheck, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refs = append(f.refs, ref)
	return &resources.MemoryCheck{SingleLimit: ref.MemoryLimit, Compose: ref.SourceType == "docker-compose"}, nil
}

func (f *fakeResources) AddComposeDefaults(context.Context, string, composespec.Source) (map[string]resources.DefaultAdded, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adds++
	for name, d := range f.defaults {
		f.panel[name] = resources.ComposeLimit{MemoryMB: d.MemoryMB}
	}
	return f.defaults, nil
}

func (f *fakeResources) ComposeLimits(context.Context, string) (map[string]resources.ComposeLimit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.panel, nil
}

// limitsOrchestrator records the panel limits compose runs with.
type limitsOrchestrator struct {
	mockOrchestrator
	limits map[string]deployment.ComposeLimit
}

func (o *limitsOrchestrator) RunCompose(_ context.Context, _ string, _, _, _ string, _ map[string]string, _ *deployment.ComposeRoute, _ deployment.ComposeNetworks, limits map[string]deployment.ComposeLimit, _ *deployment.RegistryAuth, _ rollout.Hooks) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.limits = limits
	return nil
}

func (o *limitsOrchestrator) RunComposeFromRepo(_ context.Context, _ string, _, _, _, _ string, _ map[string]string, _ *deployment.ComposeRoute, _ deployment.ComposeNetworks, limits map[string]deployment.ComposeLimit, _ *deployment.RegistryAuth, _ rollout.Hooks) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.limits = limits
	return nil
}

func TestTrigger_MemoryCheckOnSingleContainerDeploys(t *testing.T) {
	repo := &mockRepo{createdID: "dep-mem"}
	svc := newTestService(repo, &mockFactory{orch: &mockOrchestrator{}}, &mockStatusUpdater{})
	policy := &fakePolicy{}
	res := &fakeResources{}
	svc.WithPolicyChecker(policy)
	svc.WithResources(res)

	_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
		ServiceID: "svc-1", SourceType: "docker-image", DockerImage: "postgres:16", AddonSlug: "postgres", MemoryLimit: "1g",
	})
	require.NoError(t, err)
	waitForStatus(t, repo, deployment.StatusRunning, 2*time.Second)

	policy.mu.Lock()
	defer policy.mu.Unlock()
	require.Len(t, policy.deploys, 1)
	require.NotNil(t, policy.deploys[0].Memory)
	assert.Equal(t, "1g", policy.deploys[0].Memory.SingleLimit)
	assert.Equal(t, []resources.ServiceRef{{ID: "svc-1", SourceType: "docker-image", MemoryLimit: "1g", AddonSlug: "postgres", DockerImage: "postgres:16"}}, res.refs)
}

func TestPipeline_InlineComposeRunsWithThePanelLimits(t *testing.T) {
	repo := &mockRepo{createdID: "dep-inline-limits"}
	orch := &limitsOrchestrator{}
	svc := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
	cpu := 0.5
	policy := &fakePolicy{deploy: deploypolicy.Decision{Mode: deploypolicy.ModeEnforce,
		ComposeServices: []string{"db", "web"}, ComposeMemoryMB: map[string]int64{"db": 256, "web": 0},
		ComposeDeployLimits: map[string]bool{"web": true}}}
	res := &fakeResources{panel: map[string]resources.ComposeLimit{"web": {MemoryMB: 768, CPUCores: &cpu}}}
	svc.WithPolicyChecker(policy)
	svc.WithResources(res)

	_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
		ServiceID: "svc-1", ServiceName: "stack", SourceType: "docker-compose",
		ComposeContent: "services:\n  web:\n    image: nginx\n  db:\n    image: postgres\n    mem_limit: 256m\n",
	})
	require.NoError(t, err)
	waitForStatus(t, repo, deployment.StatusRunning, 2*time.Second)

	orch.mu.Lock()
	assert.Equal(t, map[string]deployment.ComposeLimit{"web": {MemoryMB: 768, CPUCores: 0.5, DeployLimits: true}}, orch.limits,
		"the override mirrors the file's deploy.resources.limits form")
	orch.mu.Unlock()
	policy.mu.Lock()
	require.NotNil(t, policy.deploys[0].Memory)
	assert.True(t, policy.deploys[0].Memory.Compose)
	policy.mu.Unlock()
	res.mu.Lock()
	require.NotNil(t, res.refs[0].Compose, "the inline file gives the suggestion its images")
	res.mu.Unlock()

	repo.mu.Lock()
	defer repo.mu.Unlock()
	logs := strings.Join(repo.logMessages, "\n")
	assert.Contains(t, logs, "→ db: memory limit 256m (compose file)")
	assert.Contains(t, logs, "→ web: memory limit 768m (panel), cpu 0.5")
}

// A new repository stack's compose services without a limit get their kind's
// default before the check, so its first deploy is not refused for them.
func TestPipeline_ComposeRepoAddsDefaultsBeforeTheCheck(t *testing.T) {
	src := infraRepo(t, map[string]string{"docker-compose.yml": "services:\n  a:\n    image: x\n  b:\n    image: y\n    mem_limit: 256m\n"})
	repo := &mockRepo{createdID: "dep-repo-limits"}
	orch := &limitsOrchestrator{}
	svc := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
	policy := &fakePolicy{repo: deploypolicy.Decision{Mode: deploypolicy.ModeEnforce,
		ComposeServices: []string{"a", "b"}, ComposeMemoryMB: map[string]int64{"a": 0, "b": 256},
		ComposeDeployLimits: map[string]bool{"a": true}}}
	res := &fakeResources{panel: map[string]resources.ComposeLimit{},
		defaults: map[string]resources.DefaultAdded{"a": {MemoryMB: 512, Kind: resources.KindApp}}}
	svc.WithPolicyChecker(policy)
	svc.WithResources(res)

	_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
		ServiceID: "svc-1", ServiceName: "infra", SourceType: "docker-compose", ComposeRepoURL: src, GitBranch: "main",
		Settings: &service.Service{ID: "svc-1", SourceType: "docker-compose"},
	})
	require.NoError(t, err)
	waitForStatus(t, repo, deployment.StatusRunning, 5*time.Second)

	policy.mu.Lock()
	require.Len(t, policy.deploys, 1)
	assert.Nil(t, policy.deploys[0].Memory, "a repository file is checked after the clone")
	require.Len(t, policy.repoRuns, 1)
	require.NotNil(t, policy.repoRuns[0].Memory)
	policy.mu.Unlock()
	res.mu.Lock()
	require.Len(t, res.refs, 1)
	require.NotNil(t, res.refs[0].Compose)
	assert.Equal(t, "docker-compose.yml", res.refs[0].Compose.File)
	res.mu.Unlock()
	orch.mu.Lock()
	assert.Equal(t, map[string]deployment.ComposeLimit{"a": {MemoryMB: 512, DeployLimits: true}}, orch.limits)
	orch.mu.Unlock()

	repo.mu.Lock()
	defer repo.mu.Unlock()
	// The stack had no limits when the deploy started; it runs with the
	// default, and the next deploy's room check compares with that.
	require.NotNil(t, repo.reserved)
	assert.Equal(t, 512.0, *repo.reserved, "the default it was given is what it reserves")
	logs := strings.Join(repo.logMessages, "\n")
	assert.Contains(t, logs, "→ a: memory limit 512m (default for app); change it on the service page")
	assert.Contains(t, logs, "→ b: memory limit 256m (compose file)")
}

// noExceptions is a deploy policy repository with no exceptions.
type noExceptions struct{ deploypolicy.Repository }

func (noExceptions) GetException(context.Context, string) (*deploypolicy.Exception, error) {
	return nil, deploypolicy.ErrNotFound
}

func (noExceptions) SaveLastCheck(context.Context, string, string, []deploypolicy.PinnedValue) error {
	return nil
}

// A compose file symlinked out of the repository is never read: the memory
// steps load the file only after the repository scan, which refuses it.
func TestPipeline_ComposeRepoSymlinkNotLoadedForMemory(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside.yml")
	require.NoError(t, os.WriteFile(outside, []byte("services:\n  a:\n    image: x\n"), 0o644))
	src := infraRepo(t, map[string]string{"README": "infra\n"})
	dir := strings.TrimPrefix(src, "file://")
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "docker-compose.yml")))
	for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", "link"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}

	repo := &mockRepo{createdID: "dep-symlink"}
	orch := &limitsOrchestrator{}
	svc := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
	res := &fakeResources{panel: map[string]resources.ComposeLimit{},
		defaults: map[string]resources.DefaultAdded{"a": {MemoryMB: 512, Kind: resources.KindApp}}}
	svc.WithPolicyChecker(deploypolicy.NewService(noExceptions{}, deploypolicy.ModeEnforce, nil))
	svc.WithResources(res)

	_, err := svc.Trigger(context.Background(), deployment.TriggerInput{
		ServiceID: "11111111-2222-3333-4444-555555555555", ServiceName: "infra", SourceType: "docker-compose",
		ComposeRepoURL: src, GitBranch: "main",
	})
	require.NoError(t, err)
	waitForStatus(t, repo, deployment.StatusFailed, 5*time.Second)

	res.mu.Lock()
	assert.Zero(t, res.adds, "no defaults from a file outside the repository")
	assert.Empty(t, res.refs, "no memory check load either")
	res.mu.Unlock()
	repo.mu.Lock()
	defer repo.mu.Unlock()
	assert.Contains(t, strings.Join(repo.logMessages, "\n"), "symlink points outside the repository")
}
