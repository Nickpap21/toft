package deployment

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// ─── planRecovery ────────────────────────────────────────────────────────────

const (
	recSvc = "11111111-0000-0000-0000-000000000001"
	recDep = "dddddddd-0000-0000-0000-000000000001"
)

var recStarted = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func interrupted() Deployment {
	started := recStarted
	return Deployment{ID: recDep, ServiceID: recSvc, Status: StatusFailed, StartedAt: &started, CreatedAt: recStarted.Add(-time.Second)}
}

// slot is a running slot container of recSvc created at created, labelled
// with deploymentID when it is not empty.
func slot(id, deploymentID, image string, created time.Time) ContainerState {
	return ContainerState{ID: id, Name: "paas-web-11111111-" + id, Image: image, State: "running", Created: created, DeploymentID: deploymentID, ServiceID: recSvc, Slot: true}
}

// spoofed is a running container that is not one of recSvc's slots but
// carries its paas.service_id label (and deploymentID, when set), as a
// tenant's image can.
func spoofed(id, deploymentID string, created time.Time) ContainerState {
	return ContainerState{ID: id, Name: "attacker-web-" + id, Image: "attacker/exfil:latest", State: "running", Created: created,
		DeploymentID: deploymentID, ServiceID: recSvc, ComposeProject: "attacker"}
}

func stopped(c ContainerState) ContainerState {
	c.State = "exited"
	return c
}

func TestPlanRecovery(t *testing.T) {
	before := recStarted.Add(-time.Hour)
	during := recStarted.Add(30 * time.Second)
	gitImage := gitImageTag(recSvc, recDep)
	notStarted := interrupted()
	notStarted.StartedAt = nil

	type want struct {
		status    string // "" = left as it is
		active    *string
		remove    []string
		start     []string
		imageTag  string
		detail    string // a fragment of the detail; "" = no detail
		attention bool
	}
	none, self := "", recDep
	cases := []struct {
		name string
		in   recoveryInput
		want want
	}{
		{
			name: "not the newest deployment: only marked",
			in:   recoveryInput{Deployment: interrupted(), Latest: false, Containers: []ContainerState{slot("a", recDep, "img", during)}},
			want: want{},
		},
		{
			name: "a pending deploy that never started changed nothing",
			in:   recoveryInput{Deployment: notStarted, Latest: true},
			want: want{detail: "had not started"},
		},
		{
			name: "nothing running",
			in:   recoveryInput{Deployment: interrupted(), Latest: true, Containers: []ContainerState{stopped(slot("a", recDep, "img", during))}},
			want: want{status: "error", active: &none, detail: "no container of the service is running"},
		},
		{
			name: "stop then start, interrupted with the previous one stopped: it starts again, the new one goes",
			in: recoveryInput{Deployment: interrupted(), Latest: true, ActiveDeployment: "dep-prev", Containers: []ContainerState{
				stopped(slot("old", "dep-prev", "mysql:8", before)),
				{ID: "new", Name: "paas-web-11111111-new", Image: "mysql:8", State: "restarting", DeploymentID: recDep, ServiceID: recSvc, Slot: true},
			}},
			want: want{status: "running", remove: []string{"new"}, start: []string{"old"}, detail: "so it was started again"},
		},
		{
			name: "stop then start: only the active deployment's container is started",
			in: recoveryInput{Deployment: interrupted(), Latest: true, ActiveDeployment: "dep-prev", Containers: []ContainerState{
				stopped(slot("older", "dep-older", "mysql:8", before)),
				stopped(spoofed("spoof", "dep-prev", before)),
			}},
			want: want{status: "error", active: &none, detail: "no container of the service is running"},
		},
		{
			name: "nothing running and no active deployment",
			in: recoveryInput{Deployment: interrupted(), Latest: true, Containers: []ContainerState{
				stopped(slot("old", "dep-prev", "mysql:8", before)),
			}},
			want: want{status: "error", active: &none, detail: "no container of the service is running"},
		},
		{
			name: "only the previous container, by its label",
			in:   recoveryInput{Deployment: interrupted(), Latest: true, Containers: []ContainerState{slot("old", "dep-prev", "img", before)}},
			want: want{status: "running", detail: "nothing changed on the server"},
		},
		{
			name: "only the previous container, by its label, the new one stopped",
			in: recoveryInput{Deployment: interrupted(), Latest: true, Containers: []ContainerState{
				slot("old", "dep-prev", "nginx:1", before), stopped(slot("new", recDep, "nginx:2", during)),
			}},
			want: want{status: "running", detail: "the previous container paas-web-11111111-old is still serving"},
		},
		{
			name: "a lone container is never identified by its creation time: created before",
			in:   recoveryInput{Deployment: interrupted(), Latest: true, Containers: []ContainerState{slot("old", "", "nginx:1", before)}},
			want: want{status: "running", detail: "which deploy started it is not known", attention: true},
		},
		{
			name: "only the new container, by its label",
			in:   recoveryInput{Deployment: interrupted(), Latest: true, Containers: []ContainerState{slot("new", recDep, "nginx:2", during)}},
			want: want{status: "running", active: &self, imageTag: "nginx:2", detail: "this deployment is now the active one", attention: true},
		},
		{
			name: "only the new container, by its git image",
			in:   recoveryInput{Deployment: interrupted(), Latest: true, Containers: []ContainerState{slot("new", "", gitImage, before)}},
			want: want{status: "running", active: &self, imageTag: gitImage, detail: "may not have completed", attention: true},
		},
		{
			name: "a lone container is never identified by its creation time: created after",
			in:   recoveryInput{Deployment: interrupted(), Latest: true, Containers: []ContainerState{slot("new", "", "nginx:2", during)}},
			want: want{status: "running", detail: "which deploy started it is not known", attention: true},
		},
		{
			name: "an image-labelled container alone is not the service's: nothing of it runs",
			in:   recoveryInput{Deployment: interrupted(), Latest: true, Containers: []ContainerState{spoofed("x", "", during)}},
			want: want{status: "error", active: &none, detail: "no container of the service is running"},
		},
		{
			name: "an image-labelled container claiming the deployment is not the service's either",
			in:   recoveryInput{Deployment: interrupted(), Latest: true, Containers: []ContainerState{spoofed("x", recDep, during)}},
			want: want{status: "error", active: &none, detail: "no container of the service is running"},
		},
		{
			name: "the new slot next to an image-labelled container: the slot is not removed",
			in: recoveryInput{Deployment: interrupted(), Latest: true, Containers: []ContainerState{
				slot("new", recDep, "nginx:2", during), spoofed("x", "dep-other", before),
			}},
			want: want{status: "running", active: &self, imageTag: "nginx:2", detail: "is the only one running", attention: true},
		},
		{
			name: "the previous slot next to an image-labelled container",
			in: recoveryInput{Deployment: interrupted(), Latest: true, Containers: []ContainerState{
				slot("old", "dep-prev", "nginx:1", before), spoofed("x", recDep, during),
			}},
			want: want{status: "running", detail: "nothing changed on the server"},
		},
		{
			name: "one container of unknown origin",
			in:   recoveryInput{Deployment: interrupted(), Latest: true, Containers: []ContainerState{slot("x", "", "nginx:2", time.Time{})}},
			want: want{status: "running", detail: "which deploy started it is not known", attention: true},
		},
		{
			name: "mid-switch, new and previous told apart by label: the new one is removed",
			in: recoveryInput{Deployment: interrupted(), Latest: true, Containers: []ContainerState{
				slot("new", recDep, "img", during), slot("old", "dep-prev", "img", before),
			}},
			want: want{status: "running", remove: []string{"new"}, detail: "mid-switch: the new container paas-web-11111111-new was removed and the previous container paas-web-11111111-old keeps serving"},
		},
		{
			name: "mid-switch, told apart by git image and creation time",
			in: recoveryInput{Deployment: interrupted(), Latest: true, Containers: []ContainerState{
				slot("old", "", "paas/x:prev", before), slot("new", "", gitImage, during),
			}},
			want: want{status: "running", remove: []string{"new"}, detail: "mid-switch"},
		},
		{
			name: "two running, both created after the deploy started: ambiguous, nothing removed",
			in: recoveryInput{Deployment: interrupted(), Latest: true, Containers: []ContainerState{
				slot("a", "", "nginx", during), slot("b", "", "nginx", during.Add(time.Second)),
			}},
			want: want{status: "running", detail: "both blue/green slots are running", attention: true},
		},
		{
			name: "two running, both older: nothing removed",
			in: recoveryInput{Deployment: interrupted(), Latest: true, Containers: []ContainerState{
				slot("a", "dep-1", "nginx", before), slot("b", "dep-2", "nginx", before),
			}},
			want: want{status: "running", detail: "which one is new is not known", attention: true},
		},
		{
			name: "two running, one of unknown origin: nothing removed",
			in: recoveryInput{Deployment: interrupted(), Latest: true, Containers: []ContainerState{
				slot("new", recDep, "nginx", during), slot("x", "", "nginx", time.Time{}),
			}},
			want: want{status: "running", detail: "neither was removed", attention: true},
		},
		{
			name: "three running",
			in: recoveryInput{Deployment: interrupted(), Latest: true, Containers: []ContainerState{
				slot("a", recDep, "i", during), slot("b", "dep-1", "i", before), slot("c", "", "i", before),
			}},
			want: want{status: "running", detail: "3 containers (paas-web-11111111-a, paas-web-11111111-b, paas-web-11111111-c) of the service are running", attention: true},
		},
		{
			name: "another service's container is not counted",
			in: recoveryInput{Deployment: interrupted(), Latest: true, Containers: []ContainerState{
				slot("old", "dep-prev", "img", before),
				{ID: "c", Name: "paas-web-11111111-web-1", State: "running", ComposeProject: "paas-web-11111111"},
			}},
			want: want{status: "running", detail: "nothing changed"},
		},
		{
			name: "compose, something running: never removes anything",
			in: recoveryInput{Deployment: interrupted(), Latest: true, Compose: true, Containers: []ContainerState{
				{ID: "c1", Name: "web-1", State: "running", ComposeProject: "paas-web-11111111"},
				{ID: "c2", Name: "db-1", State: "exited", ComposeProject: "paas-web-11111111"},
			}},
			want: want{status: "running", detail: "partially updated (1 container (web-1) running)", attention: true},
		},
		{
			name: "compose, nothing running",
			in: recoveryInput{Deployment: interrupted(), Latest: true, Compose: true, Containers: []ContainerState{
				{ID: "c2", Name: "db-1", State: "exited", ComposeProject: "paas-web-11111111"},
			}},
			want: want{status: "error", detail: "partially updated (no container running)", attention: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := planRecovery(tc.in)
			if tc.want.status == "" {
				assert.Nil(t, got.RuntimeStatus)
			} else if assert.NotNil(t, got.RuntimeStatus) {
				assert.Equal(t, tc.want.status, *got.RuntimeStatus)
			}
			assert.Equal(t, tc.want.active, got.ActiveDeploymentID)
			assert.Equal(t, tc.want.remove, got.RemoveContainerIDs)
			assert.Equal(t, tc.want.start, got.StartContainerIDs)
			assert.Equal(t, tc.want.imageTag, got.ImageTag)
			if tc.want.detail == "" {
				assert.Empty(t, got.Detail)
			} else {
				assert.Contains(t, got.Detail, tc.want.detail)
			}
			assert.Equal(t, tc.want.attention, got.NeedsAttention)
		})
	}
}

func TestContainerOriginLabelDecides(t *testing.T) {
	d := interrupted()
	// The label wins over a creation time that says otherwise.
	assert.Equal(t, originOld, containerOrigin(slot("x", "dep-other", "img", recStarted.Add(time.Minute)), d, true))
	assert.Equal(t, originNew, containerOrigin(slot("x", recDep, "img", recStarted.Add(-time.Hour)), d, true))
	// Another deployment's git image does not make a container old: a
	// rollback runs one.
	assert.Equal(t, originNew, containerOrigin(slot("x", "", gitImageTag(recSvc, "dep-other"), recStarted.Add(time.Minute)), d, true))
	assert.Equal(t, originUnknown, containerOrigin(slot("x", "", gitImageTag(recSvc, "dep-other"), recStarted.Add(time.Minute)), d, false),
		"without byCreated, the creation time decides nothing")
	assert.Equal(t, originNew, containerOrigin(slot("x", "", gitImageTag(recSvc, recDep), recStarted.Add(-time.Hour)), d, false), "d's git image")
}

// ─── ReconcileAfterRestart ───────────────────────────────────────────────────

type recoveryRecord struct{ imageTag, summary string }

// recRepo is the part of Repository recovery uses.
type recRepo struct {
	Repository
	mu       sync.Mutex
	latest   map[string]string // service ID → newest deployment ID
	recorded map[string]recoveryRecord
	logs     map[string][]string
	// handedOffList is what ListHandedOff returns.
	handedOffList []HandedOff
	// deps is what ListByService returns, newest first.
	deps []Deployment
}

func (r *recRepo) ListByService(context.Context, string) ([]Deployment, error) { return r.deps, nil }

func (r *recRepo) ListHandedOff(context.Context) ([]HandedOff, error) { return r.handedOffList, nil }

func newRecRepo(latest map[string]string) *recRepo {
	return &recRepo{latest: latest, recorded: map[string]recoveryRecord{}, logs: map[string][]string{}}
}

func (r *recRepo) LatestIDForService(_ context.Context, serviceID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.latest[serviceID], nil
}

func (r *recRepo) RecordRecovery(ctx context.Context, id, imageTag, summary string) error {
	if err := ctx.Err(); err != nil {
		return err // a database call fails on a cancelled context
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recorded[id] = recoveryRecord{imageTag, summary}
	return nil
}

func (r *recRepo) InsertLog(_ context.Context, id, msg string) (*Log, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs[id] = append(r.logs[id], msg)
	return &Log{Message: msg}, nil
}

func (r *recRepo) record(id string) (recoveryRecord, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.recorded[id]
	return rec, ok
}

func (r *recRepo) logsOf(id string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.logs[id]...)
}

type statusCall struct{ serviceID, status, active string }

type recStatus struct {
	mu    sync.Mutex
	calls []statusCall
	swaps []string // SwapDockerImage's, "service: from → image"
	// networks are SetComposeNetworkService's, by service.
	networks map[string]string
}

func (s *recStatus) SetComposeNetworkService(_ context.Context, serviceID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.networks == nil {
		s.networks = map[string]string{}
	}
	s.networks[serviceID] = name
	return nil
}

func (s *recStatus) UpdateRuntimeStatus(ctx context.Context, serviceID, status, active string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, statusCall{serviceID, status, active})
	return nil
}

func (s *recStatus) SwapDockerImage(_ context.Context, serviceID, from, image string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.swaps = append(s.swaps, serviceID+": "+from+" → "+image)
	return nil
}

func (s *recStatus) recorded() []statusCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]statusCall(nil), s.calls...)
}

type recServices map[string]*service.Service

func (m recServices) Get(_ context.Context, id string) (*service.Service, error) {
	if svc, ok := m[id]; ok {
		return svc, nil
	}
	return nil, errors.New("service not found")
}

type recServers map[string]*server.Server

func (m recServers) GetServer(_ context.Context, id string) (*server.Server, error) {
	if srv, ok := m[id]; ok {
		return srv, nil
	}
	return nil, errors.New("server not found")
}

// recOrch is an Orchestrator that only inspects and removes containers.
type recOrch struct {
	Orchestrator
	containers map[string][]ContainerState // by service ID
	removeErr  error
	startErr   error
	delay      time.Duration // each inspection takes this long
	onInspect  func()        // called during each inspection
	onRemove   func()        // called during each removal
	// removalHonoursCtx: a removal fails when its context ends (otherwise
	// it completes regardless, as one that had already finished).
	removalHonoursCtx bool

	mu        sync.Mutex
	inspected []string
	removed   []string
	started   []string
	active    atomic.Int32
	maxActive atomic.Int32
}

func (o *recOrch) InspectServiceContainers(_ context.Context, serviceID, _ string) ([]ContainerState, error) {
	n := o.active.Add(1)
	defer o.active.Add(-1)
	for {
		m := o.maxActive.Load()
		if n <= m || o.maxActive.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(o.delay)
	if o.onInspect != nil {
		o.onInspect()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.inspected = append(o.inspected, serviceID)
	return o.containers[serviceID], nil
}

func (o *recOrch) RemoveContainer(ctx context.Context, id string) error {
	if o.onRemove != nil {
		o.onRemove()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.removed = append(o.removed, id)
	if o.removeErr != nil {
		return o.removeErr
	}
	if o.removalHonoursCtx {
		return ctx.Err()
	}
	return nil
}

func (o *recOrch) StartContainer(_ context.Context, id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.started = append(o.started, id)
	return o.startErr
}

func (o *recOrch) startedIDs() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.started...)
}

func (o *recOrch) seen() (inspected, removed []string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.inspected...), append([]string(nil), o.removed...)
}

// recFactory hands out orch; its first failures calls fail like an
// unreachable server.
type recFactory struct {
	orch     Orchestrator
	failures int

	mu      sync.Mutex
	calls   int
	servers []*server.Server
}

func (f *recFactory) ForServer(srv *server.Server) (Orchestrator, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.servers = append(f.servers, srv)
	if f.calls <= f.failures {
		return nil, nil, errors.New("SSH connect to server \"web-1\": unreachable")
	}
	return f.orch, func() {}, nil
}

func (f *recFactory) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type recoveryRig struct {
	svc     *Service
	repo    *recRepo
	status  *recStatus
	orch    *recOrch
	factory *recFactory
}

func newRecoveryRig(t *testing.T, latest map[string]string, services recServices, containers map[string][]ContainerState) *recoveryRig {
	t.Helper()
	old := recoveryRetryDelays
	recoveryRetryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { recoveryRetryDelays = old })

	rig := &recoveryRig{
		repo:   newRecRepo(latest),
		status: &recStatus{},
		orch:   &recOrch{containers: containers},
	}
	rig.factory = &recFactory{orch: rig.orch}
	rig.svc = NewService(rig.repo, rig.factory, rig.status, zap.NewNop())
	rig.svc.WithServiceReader(services)
	rig.svc.WithServerReader(recServers{"srv-1": {ID: "srv-1", Name: "web-1"}})
	return rig
}

func webService(id, active string) *service.Service {
	return &service.Service{ID: id, Name: "web", SourceType: service.SourceTypeDockerImage, ActiveDeploymentID: active, RuntimeStatus: "running"}
}

func TestReconcileRemovesTheIdentifiedNewSlotMidSwitch(t *testing.T) {
	d := interrupted()
	rig := newRecoveryRig(t, map[string]string{recSvc: recDep}, recServices{recSvc: webService(recSvc, "dep-prev")},
		map[string][]ContainerState{recSvc: {
			slot("new", recDep, "nginx:2", recStarted.Add(10*time.Second)),
			slot("old", "dep-prev", "nginx:1", recStarted.Add(-time.Hour)),
		}})

	rig.svc.ReconcileAfterRestart(context.Background(), []Deployment{d})

	_, removed := rig.orch.seen()
	assert.Equal(t, []string{"new"}, removed)
	assert.Equal(t, []statusCall{{recSvc, "running", "dep-prev"}}, rig.status.recorded(), "the previous deployment stays active")
	rec, ok := rig.repo.record(recDep)
	require.True(t, ok)
	assert.True(t, strings.HasPrefix(rec.summary, "interrupted: the control plane restarted during this deploy; the restart came mid-switch"), rec.summary)
	assert.Empty(t, rec.imageTag)
	assert.Contains(t, strings.Join(rig.repo.logsOf(recDep), "\n"), "↩ Recovery: the restart came mid-switch")
}

func TestReconcileStartsThePreviousContainerAStopThenStartDeployStopped(t *testing.T) {
	d := interrupted()
	containers := func() map[string][]ContainerState {
		return map[string][]ContainerState{recSvc: {
			stopped(slot("old", "dep-prev", "mysql:8", recStarted.Add(-time.Hour))),
			stopped(slot("new", recDep, "mysql:8", recStarted.Add(10*time.Second))),
		}}
	}

	t.Run("started again once the new one is removed", func(t *testing.T) {
		rig := newRecoveryRig(t, map[string]string{recSvc: recDep}, recServices{recSvc: webService(recSvc, "dep-prev")}, containers())
		rig.svc.ReconcileAfterRestart(context.Background(), []Deployment{d})

		_, removed := rig.orch.seen()
		assert.Equal(t, []string{"new"}, removed)
		assert.Equal(t, []string{"old"}, rig.orch.startedIDs())
		assert.Equal(t, []statusCall{{recSvc, "running", "dep-prev"}}, rig.status.recorded(), "the previous deployment stays active")
		assert.Contains(t, strings.Join(rig.repo.logsOf(recDep), "\n"), "↩ Recovery: the restart came mid-deploy")
	})

	t.Run("not started while the new one could not be removed", func(t *testing.T) {
		rig := newRecoveryRig(t, map[string]string{recSvc: recDep}, recServices{recSvc: webService(recSvc, "dep-prev")}, containers())
		rig.orch.removeErr = errors.New("SSH connection lost")
		rig.svc.ReconcileAfterRestart(context.Background(), []Deployment{d})

		assert.Empty(t, rig.orch.startedIDs(), "two containers on the same data are worse than none")
		assert.Equal(t, []statusCall{{recSvc, "error", "dep-prev"}}, rig.status.recorded())
		rec, _ := rig.repo.record(recDep)
		assert.Contains(t, rec.summary, "could not be started again (the new container could not be removed: SSH connection lost)")
	})

	t.Run("a start that fails leaves the service in error", func(t *testing.T) {
		rig := newRecoveryRig(t, map[string]string{recSvc: recDep}, recServices{recSvc: webService(recSvc, "dep-prev")}, containers())
		rig.orch.startErr = errors.New("ContainerStart: port is already allocated")
		rig.svc.ReconcileAfterRestart(context.Background(), []Deployment{d})

		assert.Equal(t, []statusCall{{recSvc, "error", "dep-prev"}}, rig.status.recorded())
		assert.Contains(t, strings.Join(rig.repo.logsOf(recDep), "\n"), "⚠ Recovery: the restart came mid-deploy")
	})
}

// A service whose agent still runs an earlier, handed-off deploy is left to
// it: starting the previous slot mid-rollout would put two containers on
// the same data.
func TestReconcileLeavesAServiceItsAgentIsDeploying(t *testing.T) {
	rig := newRecoveryRig(t, map[string]string{recSvc: recDep}, recServices{recSvc: webService(recSvc, "dep-prev")}, map[string][]ContainerState{recSvc: {
		stopped(slot("old", "dep-prev", "mysql:8", recStarted.Add(-time.Hour))),
	}})
	rig.repo.handedOffList = []HandedOff{{Deployment: Deployment{ID: "dep-agent", ServiceID: recSvc}}}
	rig.svc.ReconcileAfterRestart(context.Background(), []Deployment{interrupted()})

	_, removed := rig.orch.seen()
	assert.Empty(t, removed)
	assert.Empty(t, rig.orch.startedIDs())
	assert.Empty(t, rig.status.recorded())
	assert.Contains(t, strings.Join(rig.repo.logsOf(recDep), "\n"), "left to finish")
}

func TestReconcileRemovesNothingUnlessIdentified(t *testing.T) {
	d := interrupted()
	during := recStarted.Add(10 * time.Second)
	rig := newRecoveryRig(t, map[string]string{recSvc: recDep}, recServices{recSvc: webService(recSvc, "dep-prev")},
		map[string][]ContainerState{recSvc: {slot("a", "", "nginx", during), slot("b", "", "nginx", during)}})

	rig.svc.ReconcileAfterRestart(context.Background(), []Deployment{d})

	inspected, removed := rig.orch.seen()
	assert.Equal(t, []string{recSvc}, inspected)
	assert.Empty(t, removed)
	assert.Equal(t, []statusCall{{recSvc, "running", "dep-prev"}}, rig.status.recorded())
	rec, _ := rig.repo.record(recDep)
	assert.Contains(t, rec.summary, "both blue/green slots are running")
	assert.Contains(t, strings.Join(rig.repo.logsOf(recDep), "\n"), "⚠ Recovery:")
}

func TestReconcileMakesTheLoneNewContainerActive(t *testing.T) {
	d := interrupted()
	gitImage := gitImageTag(recSvc, recDep)
	rig := newRecoveryRig(t, map[string]string{recSvc: recDep}, recServices{recSvc: webService(recSvc, "dep-prev")},
		map[string][]ContainerState{recSvc: {slot("new", recDep, gitImage, recStarted.Add(10*time.Second))}})

	rig.svc.ReconcileAfterRestart(context.Background(), []Deployment{d})

	assert.Equal(t, []statusCall{{recSvc, "running", recDep}}, rig.status.recorded())
	rec, _ := rig.repo.record(recDep)
	assert.Equal(t, gitImage, rec.imageTag, "recorded so the deployment can be rolled back to")
}

// A Deploy a version a restart cut short, whose previous container still
// serves: the saved image goes back to the live one (followLive).
func TestReconcileRestoresTheSavedImage(t *testing.T) {
	d := interrupted()
	web := webService(recSvc, "dep-prev")
	web.DockerImage = "nginx:2"
	rig := newRecoveryRig(t, map[string]string{recSvc: recDep}, recServices{recSvc: web},
		map[string][]ContainerState{recSvc: {slot("old", "dep-prev", "nginx:1", recStarted.Add(-time.Hour))}})
	rig.repo.deps = []Deployment{{ID: recDep, Status: StatusFailed}, {ID: "dep-prev", Status: StatusRunning, ImageTag: "nginx:1"}}

	rig.svc.ReconcileAfterRestart(context.Background(), []Deployment{d})

	rig.status.mu.Lock()
	defer rig.status.mu.Unlock()
	assert.Equal(t, []string{recSvc + ": nginx:2 → nginx:1"}, rig.status.swaps)
}

func TestReconcileOnlyTheNewestDeploymentOfAService(t *testing.T) {
	older := interrupted()
	older.ID = "dddddddd-0000-0000-0000-00000000000a"
	older.CreatedAt = recStarted.Add(-time.Hour)
	newer := interrupted()
	otherSvc := "22222222-0000-0000-0000-000000000002"
	replaced := Deployment{ID: "eeeeeeee-0000-0000-0000-000000000001", ServiceID: otherSvc, StartedAt: newer.StartedAt, CreatedAt: recStarted}

	rig := newRecoveryRig(t,
		map[string]string{recSvc: recDep, otherSvc: "a-later-deploy"},
		recServices{recSvc: webService(recSvc, "dep-prev"), otherSvc: webService(otherSvc, "")},
		map[string][]ContainerState{recSvc: {slot("old", "dep-prev", "nginx", recStarted.Add(-time.Hour))}})

	rig.svc.ReconcileAfterRestart(context.Background(), []Deployment{older, newer, replaced})

	inspected, _ := rig.orch.seen()
	assert.Equal(t, []string{recSvc}, inspected, "a deployment that is not its service's newest is never inspected")
	_, ok := rig.repo.record(older.ID)
	assert.False(t, ok, "an older interrupted deployment keeps the summary it was marked with")
	_, ok = rig.repo.record(replaced.ID)
	assert.False(t, ok)
	assert.Equal(t, []statusCall{{recSvc, "running", "dep-prev"}}, rig.status.recorded())
}

// A restart while one deploy of a service ran and another waited for it
// (waitTurn): the one that ran is checked, though the one that waited is
// the newer, and that one is recorded as not started.
func TestReconcileChecksTheDeployThatRanNotTheOneThatWaited(t *testing.T) {
	ran := interrupted()
	waited := interrupted()
	waited.ID = "dddddddd-0000-0000-0000-00000000000b"
	waited.StartedAt = nil
	waited.CreatedAt = ran.CreatedAt.Add(time.Minute)
	// The newest that counts is the one that ran (LatestDeploymentIDForService).
	rig := newRecoveryRig(t, map[string]string{recSvc: recDep}, recServices{recSvc: webService(recSvc, "dep-prev")},
		map[string][]ContainerState{recSvc: {slot("old", "dep-prev", "nginx", recStarted.Add(-time.Hour))}})

	rig.svc.ReconcileAfterRestart(context.Background(), []Deployment{ran, waited})

	inspected, _ := rig.orch.seen()
	assert.Equal(t, []string{recSvc}, inspected)
	assert.Equal(t, []statusCall{{recSvc, "running", "dep-prev"}}, rig.status.recorded())
	rec, ok := rig.repo.record(recDep)
	require.True(t, ok, "the deploy that ran is the one checked")
	assert.Contains(t, rec.summary, "still serving")
	rec, _ = rig.repo.record(waited.ID)
	assert.Contains(t, rec.summary, "had not started")
}

func TestReconcileADeployThatNeverStarted(t *testing.T) {
	d := interrupted()
	d.StartedAt = nil
	rig := newRecoveryRig(t, map[string]string{recSvc: recDep}, recServices{recSvc: webService(recSvc, "dep-prev")}, nil)

	rig.svc.ReconcileAfterRestart(context.Background(), []Deployment{d})

	assert.Zero(t, rig.factory.callCount(), "nothing ran, so nothing is inspected")
	assert.Empty(t, rig.status.recorded())
	rec, _ := rig.repo.record(recDep)
	assert.Contains(t, rec.summary, "had not started")
}

func TestReconcileRetriesAnUnreachableServer(t *testing.T) {
	d := interrupted()
	svc := webService(recSvc, "dep-prev")
	svc.ServerID = "srv-1"
	rig := newRecoveryRig(t, map[string]string{recSvc: recDep}, recServices{recSvc: svc},
		map[string][]ContainerState{recSvc: {slot("old", "dep-prev", "nginx", recStarted.Add(-time.Hour))}})
	rig.factory.failures = 2

	rig.svc.ReconcileAfterRestart(context.Background(), []Deployment{d})

	assert.Equal(t, 3, rig.factory.callCount())
	for _, srv := range rig.factory.servers {
		require.NotNil(t, srv, "never the local Docker daemon for a remote service")
		assert.Equal(t, "srv-1", srv.ID)
	}
	logs := strings.Join(rig.repo.logsOf(recDep), "\n")
	assert.Equal(t, 2, strings.Count(logs, "trying again"), logs)
	assert.Equal(t, []statusCall{{recSvc, "running", "dep-prev"}}, rig.status.recorded())
	rec, _ := rig.repo.record(recDep)
	assert.Contains(t, rec.summary, "nothing changed on the server")
}

func TestReconcileGivesUpOnAnUnreachableServer(t *testing.T) {
	d := interrupted()
	svc := webService(recSvc, "dep-prev")
	svc.ServerID = "srv-1"
	rig := newRecoveryRig(t, map[string]string{recSvc: recDep}, recServices{recSvc: svc}, nil)
	rig.factory.failures = 100

	rig.svc.ReconcileAfterRestart(context.Background(), []Deployment{d})

	assert.Equal(t, 1+len(recoveryRetryDelays), rig.factory.callCount())
	assert.Empty(t, rig.status.recorded(), "the runtime status is left as it was")
	rec, _ := rig.repo.record(recDep)
	assert.Contains(t, rec.summary, "what runs could not be checked")
	assert.Contains(t, rec.summary, "unreachable")
}

func TestReconcileStopsWhenCancelled(t *testing.T) {
	d := interrupted()
	rig := newRecoveryRig(t, map[string]string{recSvc: recDep}, recServices{recSvc: webService(recSvc, "")}, nil)
	recoveryRetryDelays = []time.Duration{time.Hour}
	rig.factory.failures = 100

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		rig.svc.ReconcileAfterRestart(ctx, []Deployment{d})
		close(done)
	}()
	require.Eventually(t, func() bool { return rig.factory.callCount() == 1 }, time.Second, time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("recovery kept waiting after its ctx ended")
	}
	assert.Empty(t, rig.status.recorded())
}

func TestReconcileRemovalFailureKeepsBothSlots(t *testing.T) {
	d := interrupted()
	rig := newRecoveryRig(t, map[string]string{recSvc: recDep}, recServices{recSvc: webService(recSvc, "dep-prev")},
		map[string][]ContainerState{recSvc: {
			slot("new", recDep, "nginx:2", recStarted.Add(10*time.Second)),
			slot("old", "dep-prev", "nginx:1", recStarted.Add(-time.Hour)),
		}})
	rig.orch.removeErr = errors.New("ContainerRemove: conflict")

	rig.svc.ReconcileAfterRestart(context.Background(), []Deployment{d})

	assert.Equal(t, []statusCall{{recSvc, "running", "dep-prev"}}, rig.status.recorded())
	rec, _ := rig.repo.record(recDep)
	assert.Contains(t, rec.summary, "could not be removed (ContainerRemove: conflict), so both slots keep running")
}

func TestReconcileLeavesAServiceToALaterDeploy(t *testing.T) {
	// A deploy started while the containers were being checked.
	d := interrupted()
	rig := newRecoveryRig(t, map[string]string{recSvc: recDep}, recServices{recSvc: webService(recSvc, "dep-prev")},
		map[string][]ContainerState{recSvc: {
			slot("new", recDep, "nginx:2", recStarted.Add(10*time.Second)),
			slot("old", "dep-prev", "nginx:1", recStarted.Add(-time.Hour)),
		}})
	rig.orch.onInspect = func() {
		rig.repo.mu.Lock()
		rig.repo.latest[recSvc] = "a-new-deploy"
		rig.repo.mu.Unlock()
	}

	rig.svc.ReconcileAfterRestart(context.Background(), []Deployment{d})

	_, removed := rig.orch.seen()
	assert.Empty(t, removed)
	assert.Empty(t, rig.status.recorded())
}

func TestReconcileComposeNeverRemoves(t *testing.T) {
	d := interrupted()
	svc := webService(recSvc, "dep-prev")
	svc.SourceType = service.SourceTypeDockerCompose
	rig := newRecoveryRig(t, map[string]string{recSvc: recDep}, recServices{recSvc: svc},
		map[string][]ContainerState{recSvc: {{ID: "c1", Name: "web-1", State: "running", ComposeProject: "paas-web-11111111"}}})

	rig.svc.ReconcileAfterRestart(context.Background(), []Deployment{d})

	_, removed := rig.orch.seen()
	assert.Empty(t, removed)
	assert.Equal(t, []statusCall{{recSvc, "running", "dep-prev"}}, rig.status.recorded())
}

func TestReconcileBoundsConcurrency(t *testing.T) {
	var marked []Deployment
	latest := map[string]string{}
	services := recServices{}
	for i := 0; i < 12; i++ {
		svcID := strings.Replace(recSvc, "1111", string(rune('a'+i))+"111", 1)
		depID := strings.Replace(recDep, "dddd", string(rune('a'+i))+"ddd", 1)
		d := interrupted()
		d.ID, d.ServiceID = depID, svcID
		marked = append(marked, d)
		latest[svcID] = depID
		services[svcID] = webService(svcID, "")
	}
	rig := newRecoveryRig(t, latest, services, nil)
	rig.orch.delay = 10 * time.Millisecond

	rig.svc.ReconcileAfterRestart(context.Background(), marked)

	inspected, _ := rig.orch.seen()
	assert.Len(t, inspected, 12)
	assert.LessOrEqual(t, rig.orch.maxActive.Load(), int32(recoveryConcurrency))
	assert.Greater(t, rig.orch.maxActive.Load(), int32(1), "services are checked in parallel")
}

func TestReconcileWithoutAnInspector(t *testing.T) {
	d := interrupted()
	rig := newRecoveryRig(t, map[string]string{recSvc: recDep}, recServices{recSvc: webService(recSvc, "dep-prev")}, nil)
	rig.factory.orch = struct{ Orchestrator }{} // cannot list containers

	rig.svc.ReconcileAfterRestart(context.Background(), []Deployment{d})

	assert.Equal(t, 1, rig.factory.callCount(), "not retried: it would not help")
	assert.Empty(t, rig.status.recorded())
	rec, _ := rig.repo.record(recDep)
	assert.Contains(t, rec.summary, "could not be checked")
}

func midSwitchRig(t *testing.T) *recoveryRig {
	return newRecoveryRig(t, map[string]string{recSvc: recDep}, recServices{recSvc: webService(recSvc, "dep-prev")},
		map[string][]ContainerState{recSvc: {
			slot("new", recDep, "nginx:2", recStarted.Add(10*time.Second)),
			slot("old", "dep-prev", "nginx:1", recStarted.Add(-time.Hour)),
		}})
}

func TestReconcileRecordsAFinishedRemovalEvenWhenStoppedRightAfter(t *testing.T) {
	// The shutdown lands just as the new slot was removed: the outcome is
	// still recorded, rather than lost to a cancelled context.
	rig := midSwitchRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rig.orch.onRemove = cancel

	rig.svc.ReconcileAfterRestart(ctx, []Deployment{interrupted()})

	_, removed := rig.orch.seen()
	assert.Equal(t, []string{"new"}, removed)
	assert.Equal(t, []statusCall{{recSvc, "running", "dep-prev"}}, rig.status.recorded())
	rec, ok := rig.repo.record(recDep)
	require.True(t, ok)
	assert.Contains(t, rec.summary, "the restart came mid-switch")
	assert.Contains(t, strings.Join(rig.repo.logsOf(recDep), "\n"), "↩ Recovery: the restart came mid-switch")
}

func TestReconcileCutShortRecordsNothingSoTheNextStartChecksAgain(t *testing.T) {
	rig := midSwitchRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rig.orch.onRemove = cancel
	rig.orch.removalHonoursCtx = true

	rig.svc.ReconcileAfterRestart(ctx, []Deployment{interrupted()})

	assert.Empty(t, rig.status.recorded())
	_, ok := rig.repo.record(recDep)
	assert.False(t, ok, "the summary stays the bare marker, which the next start selects again")
}
