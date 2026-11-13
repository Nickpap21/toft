package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/apispec"
	serverPkg "github.com/Nickpap21/toft/backend/internal/server"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// statsOrch is an orchestrator that can sample its host's services.
type statsOrch struct {
	fakeOrch
	got   []MetricsTarget
	since int64
}

func (o *statsOrch) CollectServiceStats(_ context.Context, targets []MetricsTarget, since int64) (HostSample, error) {
	o.got, o.since = targets, since
	out := HostSample{Containers: map[string]map[string][]ContainerStats{}, Until: since + 60}
	for _, t := range targets {
		out.Containers[t.ID] = map[string][]ContainerStats{"": {{CPUPercent: 1, MemoryMB: 2}}}
	}
	return out, nil
}

// serverFactory records which server it was asked for.
type serverFactory struct {
	orch  ContainerOrchestrator
	asked []*serverPkg.Server
}

func (f *serverFactory) ForServer(srv *serverPkg.Server) (ContainerOrchestrator, func(), error) {
	f.asked = append(f.asked, srv)
	return f.orch, func() {}, nil
}

// namedServers returns a server for any ID and records the IDs.
type namedServers struct{ asked []string }

func (s *namedServers) GetServer(_ context.Context, id string) (*serverPkg.Server, error) {
	s.asked = append(s.asked, id)
	if id == "missing" {
		return nil, errors.New("not found")
	}
	return &serverPkg.Server{ID: id, Name: "srv " + id}, nil
}

func metricsManager(orch ContainerOrchestrator) (*ServiceManager, *serverFactory, *namedServers) {
	m := NewServiceManager(&fakeRepo{}, zap.NewNop())
	f := &serverFactory{orch: orch}
	servers := &namedServers{}
	m.WithOrchestration(servers, f)
	return m, f, servers
}

var metricsTargets = []MetricsTarget{
	{ID: "local-1", Name: "a"},
	{ID: "remote-1", Name: "b", ServerID: "srv-1"},
	{ID: "remote-2", Name: "c", ServerID: "srv-1"},
	{ID: "other-1", Name: "d", ServerID: "srv-2"},
}

func TestCollectServerStatsRemote(t *testing.T) {
	orch := &statsOrch{}
	m, f, servers := metricsManager(orch)

	got, err := m.CollectServerStats(context.Background(), "srv-1", metricsTargets, 1760000000)
	require.NoError(t, err)
	assert.Equal(t, []string{"srv-1"}, servers.asked)
	require.Len(t, f.asked, 1)
	assert.Equal(t, "srv-1", f.asked[0].ID)
	assert.Equal(t, []MetricsTarget{metricsTargets[1], metricsTargets[2]}, orch.got, "only that server's services")
	assert.Equal(t, int64(1760000000), orch.since, "the kill window starts where the last one ended")
	assert.Equal(t, HostSample{
		Containers: map[string]map[string][]ContainerStats{
			"remote-1": {"": {{CPUPercent: 1, MemoryMB: 2}}},
			"remote-2": {"": {{CPUPercent: 1, MemoryMB: 2}}},
		},
		Until: 1760000060,
	}, got)
}

func TestCollectServerStatsLocal(t *testing.T) {
	orch := &statsOrch{}
	m, f, servers := metricsManager(orch)

	got, err := m.CollectServerStats(context.Background(), "", metricsTargets, 0)
	require.NoError(t, err)
	assert.Empty(t, servers.asked, "the local host is not a server row")
	require.Len(t, f.asked, 1)
	assert.Nil(t, f.asked[0])
	assert.Equal(t, []MetricsTarget{metricsTargets[0]}, orch.got)
	assert.Len(t, got.Containers, 1)
}

func TestCollectServerStatsWithNoServicesThereDoesNotConnect(t *testing.T) {
	m, f, _ := metricsManager(&statsOrch{})
	got, err := m.CollectServerStats(context.Background(), "srv-9", metricsTargets, 0)
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Empty(t, f.asked)
}

// agentStatsOrch is an agent server's orchestrator whose agent keeps
// samples (or not: keeps).
type agentStatsOrch struct {
	statsOrch
	keeps bool
	after int64
}

func (o *agentStatsOrch) KeepsStats() bool { return o.keeps }

func (o *agentStatsOrch) AgentServiceStats(_ context.Context, targets []MetricsTarget, after int64) ([]HostSample, error) {
	o.got, o.after = targets, after
	return []HostSample{{At: time.Unix(after+60, 0)}}, nil
}

// agentServers returns every server as one connected through its agent.
type agentServers struct{}

func (agentServers) GetServer(_ context.Context, id string) (*serverPkg.Server, error) {
	return &serverPkg.Server{ID: id, Connection: serverPkg.ConnectionAgent}, nil
}

// newestRepo answers NewestMetricOn with newest.
type newestRepo struct {
	fakeRepo
	newest time.Time
}

func (r *newestRepo) NewestMetricOn(context.Context, string) (time.Time, error) { return r.newest, nil }

// metricsRepo records which chart query GET /metrics ran.
type metricsRepo struct {
	fakeRepo
	calls []string
	today time.Time
}

func (r *metricsRepo) ListMetrics(_ context.Context, _, compose string, since time.Time) ([]MetricPoint, error) {
	r.calls = append(r.calls, "minutes "+compose+" "+time.Since(since).Round(time.Hour).String())
	return []MetricPoint{}, nil
}

func (r *metricsRepo) ListDailyMetrics(_ context.Context, _, compose string, today time.Time) ([]MetricPoint, error) {
	r.calls = append(r.calls, "days "+compose)
	r.today = today
	return []MetricPoint{{Timestamp: today, CPUPercent: 1, MemoryMB: 2}}, nil
}

// range=7d reads the daily points as of today (midnight UTC); the other
// ranges, and an unknown one (1h), the minutes.
func TestHandler_MetricsRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &metricsRepo{fakeRepo: fakeRepo{svc: &Service{ID: "s1", Name: "web"}}}
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) { c.Set("orgID", "org-a"); c.Next() })
	NewHandler(NewServiceManager(repo, zap.NewNop()), nil, nil, nil, nil, zap.NewNop()).RegisterRoutes(g)
	get := func(query string) {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/services/s1/metrics"+query, nil))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var body any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		apispec.Check(t, http.MethodGet, "/api/v1/services/s1/metrics", w.Code, body)
	}
	get("?range=7d&compose_service=db")
	get("?range=24h")
	get("?range=30d")
	assert.Equal(t, []string{"days db", "minutes  24h0m0s", "minutes  1h0m0s"}, repo.calls)
	assert.Equal(t, time.Now().UTC().Truncate(24*time.Hour), repo.today)
}

// An agent server's samples are asked for after the given time, or else
// after the newest one stored; servers whose agent keeps none, and the
// local host, are left to CollectServerStats.
func TestCollectAgentStats(t *testing.T) {
	newest := time.Unix(1760000000, 0)
	orch := &agentStatsOrch{keeps: true}
	m := NewServiceManager(&newestRepo{newest: newest}, zap.NewNop())
	m.WithOrchestration(agentServers{}, &serverFactory{orch: orch})
	ctx := context.Background()

	got, kept, err := m.CollectAgentStats(ctx, "srv-1", metricsTargets, time.Time{})
	require.NoError(t, err)
	assert.True(t, kept)
	assert.Equal(t, int64(1760000000), orch.after, "after the newest sample stored")
	assert.Equal(t, []MetricsTarget{metricsTargets[1], metricsTargets[2]}, orch.got, "only that server's services")
	assert.Equal(t, []HostSample{{At: time.Unix(1760000060, 0)}}, got)

	_, _, err = m.CollectAgentStats(ctx, "srv-1", metricsTargets, time.Unix(1760000120, 0))
	require.NoError(t, err)
	assert.Equal(t, int64(1760000120), orch.after)

	orch.keeps = false
	_, kept, err = m.CollectAgentStats(ctx, "srv-1", metricsTargets, time.Time{})
	assert.False(t, kept, "an older agent")
	require.NoError(t, err)

	ssh, f, _ := metricsManager(&agentStatsOrch{keeps: true})
	_, kept, err = ssh.CollectAgentStats(ctx, "srv-1", metricsTargets, time.Time{})
	assert.False(t, kept, "an SSH server")
	require.NoError(t, err)
	_, kept, _ = ssh.CollectAgentStats(ctx, "", metricsTargets, time.Time{})
	assert.False(t, kept, "the local host")
	assert.Empty(t, f.asked, "neither connects for it")
}

func TestCollectServerStatsErrors(t *testing.T) {
	m, _, _ := metricsManager(&fakeOrch{})
	_, err := m.CollectServerStats(context.Background(), "srv-1", metricsTargets, 0)
	assert.ErrorContains(t, err, "does not support stats collection")

	m, f, _ := metricsManager(&statsOrch{})
	_, err = m.CollectServerStats(context.Background(), "missing", []MetricsTarget{{ID: "x", ServerID: "missing"}}, 0)
	assert.ErrorContains(t, err, "get server")
	assert.Empty(t, f.asked)

	_, err = NewServiceManager(&fakeRepo{}, zap.NewNop()).CollectServerStats(context.Background(), "", metricsTargets, 0)
	assert.ErrorContains(t, err, "orchestration not configured")
}
