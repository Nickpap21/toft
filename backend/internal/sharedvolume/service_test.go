package sharedvolume

import (
	"context"
	"errors"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// memRepo is an in-memory Repository: volumes by ID, mounts by service.
type memRepo struct {
	vols   map[string]*Volume
	mounts map[string][]Mount
	usage  map[string]int64
}

func newMemRepo() *memRepo {
	return &memRepo{vols: map[string]*Volume{}, mounts: map[string][]Mount{}, usage: map[string]int64{}}
}

func (r *memRepo) mountsOf(id string) []MountedBy {
	out := []MountedBy{}
	for svc, ms := range r.mounts {
		for _, m := range ms {
			if m.VolumeID == id {
				out = append(out, MountedBy{ServiceID: svc, Target: m.Target, ReadOnly: m.ReadOnly})
			}
		}
	}
	return out
}

func (r *memRepo) List(_ context.Context, orgID string) ([]Volume, error) {
	out := []Volume{}
	for _, v := range r.vols {
		if v.orgID == orgID {
			c := *v
			c.Mounts = r.mountsOf(v.ID)
			out = append(out, c)
		}
	}
	return out, nil
}
func (r *memRepo) Get(_ context.Context, orgID, id string) (*Volume, error) {
	v, ok := r.vols[id]
	if !ok || v.orgID != orgID {
		return nil, ErrNotFound
	}
	c := *v
	return &c, nil
}
func (r *memRepo) Create(_ context.Context, orgID string, in CreateInput) (*Volume, error) {
	for _, v := range r.vols {
		if v.orgID == orgID && v.Name == in.Name {
			return nil, ErrNameTaken
		}
	}
	v := &Volume{ID: "vol-" + in.Name, orgID: orgID, ServerID: in.ServerID, Name: in.Name, SizeLimitBytes: in.SizeLimitBytes}
	r.vols[v.ID] = v
	return v, nil
}
func (r *memRepo) UpdateLimit(_ context.Context, orgID, id string, limit int64) (*Volume, error) {
	v, ok := r.vols[id]
	if !ok || v.orgID != orgID {
		return nil, ErrNotFound
	}
	v.SizeLimitBytes = limit
	return v, nil
}
func (r *memRepo) Delete(_ context.Context, orgID, id string) error {
	if len(r.mountsOf(id)) > 0 {
		return ErrInUse
	}
	delete(r.vols, id)
	return nil
}
func (r *memRepo) ServiceMounts(_ context.Context, serviceID string) ([]Mount, error) {
	out := []Mount{}
	for _, m := range r.mounts[serviceID] {
		v := r.vols[m.VolumeID]
		m.Name, m.serverID, m.orgID = v.Name, v.ServerID, v.orgID
		out = append(out, m)
	}
	return out, nil
}
func (r *memRepo) ReplaceServiceMounts(_ context.Context, serviceID, _ string, mounts []Mount) error {
	r.mounts[serviceID] = append([]Mount(nil), mounts...)
	return nil
}
func (r *memRepo) All(context.Context) ([]Volume, error) {
	out := []Volume{}
	for _, v := range r.vols {
		out = append(out, *v)
	}
	return out, nil
}
func (r *memRepo) RecordUsage(_ context.Context, id string, bytes int64) error {
	r.usage[id] = bytes
	return nil
}

// services: svc-a (git, server srv-1) and svc-compose (compose, srv-1) in
// org-a; svc-b in org-b.
type fakeServices struct{}

var services = map[string]struct {
	org string
	svc service.Service
}{
	"svc-a":       {"org-a", service.Service{ID: "svc-a", ServerID: "srv-1", SourceType: service.SourceTypeGit}},
	"svc-compose": {"org-a", service.Service{ID: "svc-compose", ServerID: "srv-1", SourceType: service.SourceTypeDockerCompose}},
	"svc-local":   {"org-a", service.Service{ID: "svc-local", SourceType: service.SourceTypeGit}},
	"svc-b":       {"org-b", service.Service{ID: "svc-b", ServerID: "srv-1", SourceType: service.SourceTypeGit}},
}

func (fakeServices) CheckOwnership(_ context.Context, id, orgID string) (bool, error) {
	s, ok := services[id]
	return ok && s.org == orgID, nil
}
func (fakeServices) Get(_ context.Context, id string) (*service.Service, error) {
	s, ok := services[id]
	if !ok {
		return nil, errors.New("not found")
	}
	c := s.svc
	return &c, nil
}

type fakeServers struct{}

func (fakeServers) CheckOwnership(_ context.Context, id, orgID string) (bool, error) {
	return (id == "srv-1" || id == "srv-w") && orgID == "org-a", nil
}

// CheckReady: srv-w is still waiting for its first agent.
func (fakeServers) CheckReady(_ context.Context, id string) error {
	if id == "srv-w" {
		return server.ErrServerNotReady
	}
	return nil
}

type fakeHosts struct {
	removed []string
	sizes   map[string]map[string]int64 // server → volume → bytes
	down    map[string]bool
}

func (h *fakeHosts) Remove(_ context.Context, serverID, name string) error {
	if h.down[serverID] {
		return errors.New("unreachable")
	}
	h.removed = append(h.removed, name)
	return nil
}
func (h *fakeHosts) Sizes(_ context.Context, serverID string, _ []string) (map[string]int64, error) {
	if h.down[serverID] {
		return nil, errors.New("unreachable")
	}
	return h.sizes[serverID], nil
}

func newSvc() (*Service, *memRepo, *fakeHosts) {
	repo, hosts := newMemRepo(), &fakeHosts{sizes: map[string]map[string]int64{}, down: map[string]bool{}}
	return NewService(repo, fakeServices{}, fakeServers{}, hosts, zap.NewNop()), repo, hosts
}

func TestCreate(t *testing.T) {
	s, _, _ := newSvc()
	ctx := context.Background()
	v, err := s.Create(ctx, "org-a", CreateInput{Name: "northwind-images", ServerID: "srv-1"})
	require.NoError(t, err)
	assert.Equal(t, DefaultSizeLimitBytes, v.SizeLimitBytes, "10 GiB unless given")

	_, err = s.Create(ctx, "org-a", CreateInput{Name: "northwind-images", ServerID: "srv-1"})
	assert.ErrorIs(t, err, ErrNameTaken)
	_, err = s.Create(ctx, "org-b", CreateInput{Name: "x", ServerID: "srv-1"})
	assert.ErrorIs(t, err, ErrUnknownServer, "a server of another workspace")
	for _, bad := range []string{"", "Images", "-x", "a b", "a_b", "a/../b"} {
		_, err = s.Create(ctx, "org-a", CreateInput{Name: bad})
		assert.ErrorIs(t, err, ErrInvalid, bad)
	}
	_, err = s.Create(ctx, "org-a", CreateInput{Name: "neg", SizeLimitBytes: -1})
	assert.ErrorIs(t, err, ErrInvalid)
	_, err = s.Create(ctx, "org-a", CreateInput{Name: "panel-files"})
	assert.NoError(t, err, "the control-plane server needs no ownership check")
}

func TestMounts(t *testing.T) {
	s, _, _ := newSvc()
	ctx := context.Background()
	v, _ := s.Create(ctx, "org-a", CreateInput{Name: "images", ServerID: "srv-1"})
	local, _ := s.Create(ctx, "org-a", CreateInput{Name: "panel-files"})
	theirs, err := s.Create(ctx, "org-b", CreateInput{Name: "theirs"})
	require.NoError(t, err)

	got, _, err := s.SetServiceMounts(ctx, "org-a", "svc-a", "v", []Mount{{VolumeID: v.ID, Target: " /app/images ", ReadOnly: true}})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "/app/images", got[0].Target)
	assert.Equal(t, "images", got[0].Name)

	for name, tc := range map[string]struct {
		org, svc string
		mounts   []Mount
		want     error
	}{
		"another workspace's service": {"org-b", "svc-a", []Mount{{VolumeID: theirs.ID, Target: "/x"}}, ErrNotFound},
		"another workspace's volume":  {"org-a", "svc-a", []Mount{{VolumeID: theirs.ID, Target: "/x"}}, ErrNotFound},
		"a volume on another server":  {"org-a", "svc-a", []Mount{{VolumeID: local.ID, Target: "/x"}}, ErrWrongServer},
		"a compose service":           {"org-a", "svc-compose", []Mount{{VolumeID: v.ID, Target: "/x"}}, ErrCompose},
		"a relative path":             {"org-a", "svc-a", []Mount{{VolumeID: v.ID, Target: "app"}}, ErrInvalid},
		"a path with ..":              {"org-a", "svc-a", []Mount{{VolumeID: v.ID, Target: "/app/../etc"}}, ErrInvalid},
		"the same path twice":         {"org-a", "svc-a", []Mount{{VolumeID: v.ID, Target: "/a"}, {VolumeID: v.ID, Target: "/a"}}, ErrInvalid},
	} {
		_, _, err := s.SetServiceMounts(ctx, tc.org, tc.svc, "v", tc.mounts)
		assert.ErrorIs(t, err, tc.want, name)
	}
	_, _, err = s.SetServiceMounts(ctx, "org-a", "svc-compose", "v", []Mount{})
	assert.NoError(t, err, "clearing a compose service's mounts is fine")

	deploy, err := s.MountsForDeploy(ctx, "svc-a", "srv-1")
	require.NoError(t, err)
	assert.Equal(t, []service.VolumeMount{{Name: "images", Target: "/app/images", ReadOnly: true, Shared: "paas-shared-" + v.ID}}, deploy)
	_, err = s.MountsForDeploy(ctx, "svc-a", "")
	assert.ErrorIs(t, err, ErrWrongServer, "a deploy to another server fails")

	has, err := s.HasMounts(ctx, "svc-a")
	require.NoError(t, err)
	assert.True(t, has)
}

func TestDelete(t *testing.T) {
	s, _, hosts := newSvc()
	ctx := context.Background()
	v, _ := s.Create(ctx, "org-a", CreateInput{Name: "images", ServerID: "srv-1"})
	_, _, err := s.SetServiceMounts(ctx, "org-a", "svc-a", "v", []Mount{{VolumeID: v.ID, Target: "/data"}})
	require.NoError(t, err)

	assert.ErrorIs(t, s.Delete(ctx, "org-a", v.ID), ErrInUse)
	assert.Empty(t, hosts.removed, "a mounted volume's data is never removed")

	_, _, err = s.SetServiceMounts(ctx, "org-a", "svc-a", "v", []Mount{})
	require.NoError(t, err)
	hosts.down["srv-1"] = true
	assert.Error(t, s.Delete(ctx, "org-a", v.ID))
	vols, _ := s.List(ctx, "org-a")
	assert.Len(t, vols, 1, "an unreachable server keeps the volume listed")

	hosts.down["srv-1"] = false
	require.NoError(t, s.Delete(ctx, "org-a", v.ID))
	assert.Equal(t, []string{"paas-shared-" + v.ID}, hosts.removed)
	assert.ErrorIs(t, s.Delete(ctx, "org-b", v.ID), ErrNotFound)
}

func TestMeasure(t *testing.T) {
	s, repo, hosts := newSvc()
	ctx := context.Background()
	a, _ := s.Create(ctx, "org-a", CreateInput{Name: "images", ServerID: "srv-1"})
	b, _ := s.Create(ctx, "org-a", CreateInput{Name: "new"})
	hosts.sizes["srv-1"] = map[string]int64{"paas-shared-" + a.ID: 42}

	usage, err := s.Measure(ctx)
	require.NoError(t, err)
	byID := map[string]Usage{}
	for _, u := range usage {
		byID[u.ID] = u
	}
	assert.Equal(t, Usage{ID: a.ID, Name: "images", SizeLimitBytes: DefaultSizeLimitBytes, UsedBytes: 42, Measured: true}, byID[a.ID])
	assert.False(t, byID[b.ID].Measured, "not created yet")
	assert.Equal(t, map[string]int64{a.ID: 42}, repo.usage)

	hosts.down["srv-1"] = true
	usage, err = s.Measure(ctx)
	require.NoError(t, err)
	assert.Len(t, usage, 2, "an unreachable server's volumes are still listed")
}
