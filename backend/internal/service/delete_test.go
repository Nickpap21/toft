package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	serverPkg "github.com/Nickpap21/toft/backend/internal/server"
)

// teardownOrch records what a delete removed from the server.
type teardownOrch struct {
	fakeOrch
	stopped, compose []string
	data             map[string][]string // service ID → volumes removed
	failStop         error
}

func (o *teardownOrch) Stop(_ context.Context, id string) error {
	o.stopped = append(o.stopped, id)
	return o.failStop
}
func (o *teardownOrch) StopCompose(_ context.Context, id, _, _ string) error {
	o.compose = append(o.compose, id)
	return o.failStop
}
func (o *teardownOrch) RemoveServiceData(_ context.Context, id, _ string, volumes []string) error {
	if o.data == nil {
		o.data = map[string][]string{}
	}
	o.data[id] = volumes
	return nil
}

type teardownFactory struct{ orch *teardownOrch }

func (f teardownFactory) ForServer(*serverPkg.Server) (ContainerOrchestrator, func(), error) {
	return f.orch, func() {}, nil
}

// deleteRepo holds services by ID and records deletes.
type deleteRepo struct {
	fakeRepo
	byID    map[string]*Service
	inScope []string
	deleted []string
	copies  map[string][]string
}

func (r *deleteRepo) ListCopies(_ context.Context, id string) ([]string, error) {
	return r.copies[id], nil
}

func (r *deleteRepo) GetByID(_ context.Context, id string) (*Service, error) {
	if s, ok := r.byID[id]; ok {
		return s, nil
	}
	return nil, errors.New("not found")
}
func (r *deleteRepo) ListIDsIn(context.Context, Scope, string) ([]string, error) {
	return r.inScope, nil
}
func (r *deleteRepo) Delete(_ context.Context, id string) error {
	r.deleted = append(r.deleted, id)
	return nil
}

type configRemover struct {
	ConfigFileSeeder
	removed []string
}

func (c *configRemover) RemoveFromHost(_ context.Context, id string, _ *serverPkg.Server) error {
	c.removed = append(c.removed, id)
	return nil
}

func TestDeleteRemovesEverythingFromTheServer(t *testing.T) {
	repo := &deleteRepo{byID: map[string]*Service{
		"aaaaaaaa-1": {ID: "aaaaaaaa-1", Name: "db", SourceType: SourceTypeDockerImage, Volumes: []VolumeMount{
			{Name: "data", Target: "/var/lib/postgresql/data"},
			{Name: "uploads", Target: "/srv", HostPath: "/var/www/uploads"}, // the server's folder, not the service's
		}},
		"bbbbbbbb-2": {ID: "bbbbbbbb-2", Name: "stack", SourceType: SourceTypeDockerCompose},
	}}
	orch := &teardownOrch{}
	cfg := &configRemover{}
	m := NewServiceManager(repo, zap.NewNop())
	m.WithOrchestration(nil, teardownFactory{orch})
	m.WithConfigFileSeeder(cfg)

	require.NoError(t, m.Delete(context.Background(), "aaaaaaaa-1"))
	require.NoError(t, m.Delete(context.Background(), "bbbbbbbb-2"))

	assert.Equal(t, []string{"aaaaaaaa-1"}, orch.stopped)
	assert.Equal(t, []string{"bbbbbbbb-2"}, orch.compose)
	assert.Equal(t, []string{"paas-vol-aaaaaaaa-data"}, orch.data["aaaaaaaa-1"], "named volumes go, bind-mounted folders stay")
	assert.Contains(t, orch.data, "bbbbbbbb-2", "a stack's volumes and images go too")
	assert.Equal(t, []string{"aaaaaaaa-1", "bbbbbbbb-2"}, cfg.removed)
	assert.Equal(t, []string{"aaaaaaaa-1", "bbbbbbbb-2"}, repo.deleted)
}

// A service previews copy takes its copies with it, first (D-97).
func TestDeleteRemovesPreviewCopiesFirst(t *testing.T) {
	repo := &deleteRepo{
		byID: map[string]*Service{
			"web":      {ID: "web", Name: "web", SourceType: SourceTypeDockerImage},
			"web-pr42": {ID: "web-pr42", Name: "web", SourceType: SourceTypeDockerImage},
		},
		copies: map[string][]string{"web": {"web-pr42"}},
	}
	orch := &teardownOrch{}
	m := NewServiceManager(repo, zap.NewNop())
	m.WithOrchestration(nil, teardownFactory{orch})
	m.WithConfigFileSeeder(&configRemover{})
	require.NoError(t, m.Delete(context.Background(), "web"))
	assert.Equal(t, []string{"web-pr42", "web"}, repo.deleted)
	assert.Equal(t, []string{"web-pr42", "web"}, orch.stopped)
}

// When the server cannot be cleaned, the rows stay: the delete can be
// retried, and nothing keeps running that the panel no longer knows.
func TestDeleteKeepsTheServiceWhenTheServerIsNotCleaned(t *testing.T) {
	repo := &deleteRepo{
		byID: map[string]*Service{
			"s1": {ID: "s1", Name: "one", SourceType: SourceTypeDockerImage},
			"s2": {ID: "s2", Name: "two", SourceType: SourceTypeDockerImage},
		},
		inScope: []string{"s1", "s2"},
	}
	orch := &teardownOrch{failStop: errors.New("ssh: connection refused")}
	m := NewServiceManager(repo, zap.NewNop())
	m.WithOrchestration(nil, teardownFactory{orch})

	err := m.Delete(context.Background(), "s1")
	assert.ErrorIs(t, err, ErrTeardown)
	assert.Contains(t, TeardownMessage(err), "remove it under Servers")

	err = m.DeleteIn(context.Background(), ScopeProject, "p1")
	assert.ErrorIs(t, err, ErrTeardown)
	assert.Equal(t, []string{"s1", "s1"}, orch.stopped, "a bulk delete stops at the first service it cannot remove")
	assert.Empty(t, repo.deleted)
}

type frozenServer struct{}

func (frozenServer) GetServer(_ context.Context, id string) (*serverPkg.Server, error) {
	now := time.Now()
	return &serverPkg.Server{ID: id, AgentFrozenAt: &now}, nil
}

// Nothing is removed from a server whose agent kept its own state over a
// restored panel (D-113): what runs there may be newer than the backup.
func TestDeleteLeavesAFrozenServerAlone(t *testing.T) {
	repo := &deleteRepo{byID: map[string]*Service{"s1": {ID: "s1", Name: "one", ServerID: "srv-1", SourceType: SourceTypeDockerImage}}}
	orch := &teardownOrch{}
	m := NewServiceManager(repo, zap.NewNop())
	m.WithOrchestration(frozenServer{}, teardownFactory{orch})

	err := m.Delete(context.Background(), "s1")
	assert.ErrorIs(t, err, serverPkg.ErrServerFrozen)
	assert.Empty(t, orch.stopped)
	assert.Empty(t, repo.deleted)
}

func TestForgetServicesOnServerTouchesNothingOnIt(t *testing.T) {
	repo := &forgetRepo{deleteRepo: deleteRepo{byID: map[string]*Service{}}, onServer: []string{"s1", "s2"}}
	orch := &teardownOrch{}
	m := NewServiceManager(repo, zap.NewNop())
	m.WithOrchestration(nil, teardownFactory{orch})

	require.NoError(t, m.ForgetServicesOnServer(context.Background(), "srv-1"))
	assert.Equal(t, []string{"s1", "s2"}, repo.deleted)
	assert.Empty(t, orch.stopped)
	assert.Error(t, m.ForgetServicesOnServer(context.Background(), ""), "never the control-plane server")
}

type forgetRepo struct {
	deleteRepo
	onServer []string
}

func (r *forgetRepo) ListIDsOnServer(context.Context, string) ([]string, error) {
	return r.onServer, nil
}
