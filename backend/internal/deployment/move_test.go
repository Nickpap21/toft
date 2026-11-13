package deployment_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/domain"
	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Moving a service to another server (move.go) ───────────────────────────

// moveOrch is the orchestrator of one server; it records the services it
// removes there.
type moveOrch struct {
	mockOrchestrator
	server    string
	ops       *moveOps
	imagesErr error // RemoveServiceImages's
}

func (o *moveOrch) Stop(_ context.Context, serviceID string) error {
	o.ops.add("remove " + serviceID + " from " + o.server)
	return nil
}

func (o *moveOrch) RemoveServiceImages(_ context.Context, serviceID, _ string) error {
	o.ops.add("remove the images of " + serviceID + " from " + o.server)
	return o.imagesErr
}

type moveOps struct {
	mu  sync.Mutex
	ops []string
}

func (m *moveOps) add(op string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ops = append(m.ops, op)
}

func (m *moveOps) list() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.ops...)
}

// moveFactory gives each server its own orchestrator: "panel" for the
// control-plane server, else the server's name.
type moveFactory struct {
	orchs map[string]*moveOrch
}

func (f *moveFactory) ForServer(srv *server.Server) (deployment.Orchestrator, func(), error) {
	name := "panel"
	if srv != nil {
		name = srv.Name
	}
	return f.orchs[name], func() {}, nil
}

type recordingMover struct {
	mu    sync.Mutex
	moved []string
	err   error
}

func (m *recordingMover) CompleteMove(_ context.Context, serviceID, serverID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.moved = append(m.moved, serviceID+" → "+serverID)
	return nil
}

// MovedHostnames: the panel host is 203.0.113.10, srv-new 198.51.100.7.
func (m *recordingMover) MovedHostnames(_ context.Context, hostnames []string, from, to string) []string {
	ip := map[string]string{"": "203.0.113.10", "srv-new": "198.51.100.7"}
	out := make([]string, 0, len(hostnames))
	for _, h := range hostnames {
		out = append(out, domain.MoveTemporary(h, ip[from], ip[to]))
	}
	return out
}

func (m *recordingMover) recorded() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.moved...)
}

// moveSetup is a move of svc-1 from the panel host to the server "new".
func moveSetup(newRunErr error) (*moveFactory, *moveOps, deployment.TriggerInput) {
	ops := &moveOps{}
	f := &moveFactory{orchs: map[string]*moveOrch{
		"panel": {server: "panel", ops: ops},
		"new":   {server: "new", ops: ops, mockOrchestrator: mockOrchestrator{runErr: newRunErr}},
	}}
	in := deployment.TriggerInput{
		ServiceID: "svc-1", ServiceName: "web", SourceType: "docker-image", DockerImage: "nginx:alpine", Port: 80,
		ServerID: "srv-new", Server: &server.Server{ID: "srv-new", Name: "new"},
		MoveFrom: &deployment.MoveSource{},
	}
	return f, ops, in
}

func TestMove_RecordsTheServerThenLeavesTheOldOne(t *testing.T) {
	f, ops, in := moveSetup(nil)
	repo := &mockRepo{createdID: "dep-move"}
	updater := &mockStatusUpdater{}
	mover := &recordingMover{}
	svc := newTestService(repo, f, updater)
	svc.WithServiceMover(mover)

	_, err := svc.Trigger(context.Background(), in)
	require.NoError(t, err)
	waitForStatus(t, repo, deployment.StatusRunning, 2*time.Second)

	assert.Equal(t, []string{"svc-1 → srv-new"}, mover.recorded())
	assert.Equal(t, []string{"remove svc-1 from panel", "remove the images of svc-1 from panel"}, ops.list(), "removed from the old server only, images too")
	assert.Equal(t, []string{"running"}, updater.recorded())
	assert.True(t, repo.hasLog("→ Moving from the panel host to new"))
	assert.True(t, repo.hasLog("■ Removed it from the panel host"))
}

// D-150: the move's deploy routes the temporary address under the new
// server's, so the name works on it from the moment it runs there.
func TestMove_TemporaryAddressMovesWithIt(t *testing.T) {
	f, _, in := moveSetup(nil)
	in.Domains = []string{"shop.example.com", "web-production-shop.203-0-113-10.sslip.io"}
	repo := &mockRepo{createdID: "dep-move"}
	svc := newTestService(repo, f, &mockStatusUpdater{})
	svc.WithServiceMover(&recordingMover{})

	_, err := svc.Trigger(context.Background(), in)
	require.NoError(t, err)
	waitForStatus(t, repo, deployment.StatusRunning, 2*time.Second)
	assert.True(t, repo.hasLog("→ Its temporary address moves with it: web-production-shop.198-51-100-7.sslip.io"))
	assert.False(t, repo.hasLog("moves with it: shop.example.com"), "a person's own domain stays as it is")
}

func TestMove_FailureRemovesTheNewOneAndChangesNothing(t *testing.T) {
	t.Run("the deploy fails", func(t *testing.T) {
		f, ops, in := moveSetup(errors.New("ContainerStart: OCI error"))
		repo := &mockRepo{createdID: "dep-move-fail"}
		updater := &mockStatusUpdater{}
		mover := &recordingMover{}
		svc := newTestService(repo, f, updater)
		svc.WithServiceMover(mover)

		_, err := svc.Trigger(context.Background(), in)
		require.NoError(t, err)
		waitForStatus(t, repo, deployment.StatusFailed, 2*time.Second)

		assert.Empty(t, mover.recorded(), "the service keeps its server")
		assert.Equal(t, []string{"remove svc-1 from new", "remove the images of svc-1 from new"}, ops.list(), "the old server is left alone")
		assert.Empty(t, updater.recorded(), "it still runs on the old server: its status is unchanged")
		assert.True(t, repo.hasLog("↩ Not moved: it stays on the panel host, as it was"))
	})

	t.Run("the move cannot be recorded", func(t *testing.T) {
		f, ops, in := moveSetup(nil)
		repo := &mockRepo{createdID: "dep-move-record"}
		updater := &mockStatusUpdater{}
		svc := newTestService(repo, f, updater)
		svc.WithServiceMover(&recordingMover{err: errors.New("connection reset")})

		_, err := svc.Trigger(context.Background(), in)
		require.NoError(t, err)
		waitForStatus(t, repo, deployment.StatusFailed, 2*time.Second)

		assert.Equal(t, []string{"remove svc-1 from new", "remove the images of svc-1 from new"}, ops.list())
		assert.Empty(t, updater.recorded())
		assert.True(t, repo.hasLog("✗ Could not record the move to new: connection reset"))
	})
}

// Images that cannot be removed from the old server are a warning: the move
// went through, and the containers there are gone.
func TestMove_ImagesLeftBehindAreOnlyAWarning(t *testing.T) {
	f, _, in := moveSetup(nil)
	f.orchs["panel"].imagesErr = errors.New("disk I/O error")
	repo := &mockRepo{createdID: "dep-move-images"}
	mover := &recordingMover{}
	svc := newTestService(repo, f, &mockStatusUpdater{})
	svc.WithServiceMover(mover)

	_, err := svc.Trigger(context.Background(), in)
	require.NoError(t, err)
	waitForStatus(t, repo, deployment.StatusRunning, 2*time.Second)

	assert.Equal(t, []string{"svc-1 → srv-new"}, mover.recorded())
	assert.True(t, repo.hasLog("⚠ Could not remove its images from the panel host: disk I/O error"))
	assert.True(t, repo.hasLog("■ Removed it from the panel host"))
}

func TestMove_RefusedWithoutAMover(t *testing.T) {
	f, _, in := moveSetup(nil)
	svc := newTestService(&mockRepo{createdID: "dep-move-none"}, f, &mockStatusUpdater{})
	_, err := svc.Trigger(context.Background(), in)
	require.Error(t, err)
}
