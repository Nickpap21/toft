package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// statusRepo answers RuntimeStatuses and counts the calls; GetByID is not
// implemented, so a per-dependency lookup would panic.
type statusRepo struct {
	Repository
	statuses map[string]ServiceStatus
	calls    int
}

func (r *statusRepo) RuntimeStatuses(_ context.Context, _ string, ids []string) (map[string]ServiceStatus, error) {
	r.calls++
	out := map[string]ServiceStatus{}
	for _, id := range ids {
		if st, ok := r.statuses[id]; ok {
			out[id] = st
		}
	}
	return out, nil
}

func TestCheckDependenciesUsesOneQuery(t *testing.T) {
	repo := &statusRepo{statuses: map[string]ServiceStatus{
		"db":    {Name: "postgres", RuntimeStatus: "running"},
		"cache": {Name: "redis", RuntimeStatus: "stopped"},
	}}
	m := NewServiceManager(repo, zap.NewNop())

	err := m.CheckDependencies(context.Background(), &Service{Dependencies: []string{"db", "cache", "gone"}})
	require.Error(t, err)
	assert.Equal(t, "dependencies not ready: redis (stopped), unknown (gone)", err.Error())
	assert.Equal(t, 1, repo.calls, "one query for every dependency")

	require.NoError(t, m.CheckDependencies(context.Background(), &Service{Dependencies: []string{"db"}}))
	require.NoError(t, m.CheckDependencies(context.Background(), &Service{}))
	assert.Equal(t, 2, repo.calls, "no query without dependencies")
}

// assignRepo answers GetByID and records moves.
type assignRepo struct {
	Repository
	serverID  string
	status    string
	deploying bool
	moved     string
}

func (r *assignRepo) GetByID(_ context.Context, id string) (*Service, error) {
	return &Service{ID: id, ServerID: r.serverID, RuntimeStatus: r.status, Deploying: r.deploying}, nil
}
func (r *assignRepo) AssignServer(_ context.Context, id, serverID string) (*Service, error) {
	r.moved = serverID
	return &Service{ID: id, ServerID: serverID}, nil
}

type mounts bool

func (m mounts) HasMounts(context.Context, string) (bool, error) { return bool(m), nil }

// A service that mounts a shared volume cannot move to another server; the
// volume stays where it is.
func TestAssignServerRefusesAServiceWithSharedMounts(t *testing.T) {
	repo := &assignRepo{serverID: "srv-1"}
	m := NewServiceManager(repo, zap.NewNop())
	m.WithSharedMounts(mounts(true))
	_, _, err := m.AssignServer(context.Background(), "svc-1", "srv-2")
	assert.ErrorIs(t, err, ErrMountsSharedVolume)
	assert.Empty(t, repo.moved)

	_, _, err = m.AssignServer(context.Background(), "svc-1", "srv-1")
	assert.NoError(t, err, "staying on the same server is fine")

	m.WithSharedMounts(mounts(false))
	_, _, err = m.AssignServer(context.Background(), "svc-1", "srv-2")
	assert.NoError(t, err)
	assert.Equal(t, "srv-2", repo.moved)
}

// A service that runs, or is deploying, is not reassigned: its containers
// would stay on the old server, out of Stop's and Delete's reach. One that
// runs nowhere is.
func TestAssignServerRefusesAServiceThatRuns(t *testing.T) {
	for _, tc := range []struct {
		status    string
		deploying bool
		refused   bool
	}{
		{status: "running", refused: true},
		{status: "error", refused: true},
		{status: "stopped", deploying: true, refused: true},
		{status: "idle"},
		{status: "stopped"},
	} {
		repo := &assignRepo{serverID: "srv-1", status: tc.status, deploying: tc.deploying}
		m := NewServiceManager(repo, zap.NewNop())
		_, _, err := m.AssignServer(context.Background(), "svc-1", "srv-2")
		if tc.refused {
			assert.ErrorIs(t, err, ErrMoveWhileRunning, tc.status)
			assert.Empty(t, repo.moved, tc.status)
		} else {
			assert.NoError(t, err, tc.status)
			assert.Equal(t, "srv-2", repo.moved, tc.status)
		}
		_, _, err = m.AssignServer(context.Background(), "svc-1", "srv-1")
		assert.NoError(t, err, "staying put is always fine (%s)", tc.status)
	}
	m := NewServiceManager(&assignRepo{serverID: "srv-1", status: "running"}, zap.NewNop())
	require.NoError(t, m.CompleteMove(context.Background(), "svc-1", "srv-2"), "a move's deploy records its server")
}
