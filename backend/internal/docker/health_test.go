package docker

import (
	"context"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestContainerState(t *testing.T) {
	cases := []struct {
		state, status string
		want          service.ContainerState
	}{
		{"running", "Up 3 minutes", service.ContainerState{Name: "c", State: "running"}},
		{"running", "Up 3 minutes (unhealthy)", service.ContainerState{Name: "c", State: "running", Health: "unhealthy"}},
		{"running", "Up 5 seconds (health: starting)", service.ContainerState{Name: "c", State: "running", Health: "starting"}},
		{"exited", "Exited (137) 2 minutes ago", service.ContainerState{Name: "c", State: "exited", ExitCode: 137}},
		{"exited", "Exited (0) 1 hour ago", service.ContainerState{Name: "c", State: "exited"}},
		{"restarting", "Restarting (1) 4 seconds ago", service.ContainerState{Name: "c", State: "restarting"}},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, containerState("c", tc.state, tc.status), tc.status)
	}
}

// A remote server's listing: the slot of a target, a stopped compose
// container of another target, a container a tenant's image labelled as the
// first target (not a slot: never counted), and Traefik.
func TestRemoteServiceStatesAttributesContainers(t *testing.T) {
	const svcA = "0a1b2c3d-1111-2222-3333-444455556666"
	const svcB = "9f8e7d6c-1111-2222-3333-444455556666"
	slot := rollout.BlueContainerName(svcA, "api", "")
	project := rollout.ComposeProjectName(svcB, "worker")
	listing := `"` + slot + `"	"` + svcA + `"	""	"running"	"Up 2 hours (unhealthy)"
"` + project + `-jobs-1"	""	"` + project + `"	"exited"	"Exited (1) 3 minutes ago"
"impostor"	"` + svcA + `"	""	"running"	"Up 1 hour"
"paas-traefik"	""	""	"running"	"Up 3 days"`
	h := &fakeHost{exec: func(context.Context, string) (string, error) { return listing, nil }}
	o := newRemoteOrchestrator(h, rollout.NetConfig{}, zap.NewNop())

	states, err := o.ServiceStates(context.Background(), []service.MetricsTarget{
		{ID: svcA, Name: "api"}, {ID: svcB, Name: "worker"},
	})
	require.NoError(t, err)
	assert.Equal(t, []service.ContainerState{{Name: slot, State: "running", Health: "unhealthy"}}, states[svcA])
	assert.Equal(t, []service.ContainerState{{Name: project + "-jobs-1", State: "exited", ExitCode: 1}}, states[svcB])
	assert.Len(t, states, 2)
}

func TestParseDF(t *testing.T) {
	out := `Filesystem     1024-blocks      Used Available Capacity Mounted on
/dev/sda1         40581564  36023848   4541332      89% /
/dev/sda1         40581564  36023848   4541332      89% /
/dev/sdb1        103081248   2060012  95761980       3% /var/lib/docker`
	assert.Equal(t, []service.DiskUsage{{Mount: "/", UsedPercent: 89}, {Mount: "/var/lib/docker", UsedPercent: 3}}, parseDF(out))
	assert.Empty(t, parseDF(""))
}

func TestUsedPercentMatchesDF(t *testing.T) {
	// df rounds up: 36023848 used, 4541332 available → 88.8% → 89%.
	pct, ok := usedPercent(36023848, 4541332)
	assert.True(t, ok)
	assert.Equal(t, 89, pct)
	_, ok = usedPercent(0, 0)
	assert.False(t, ok)
}

// Where each target's containers log, read from docker inspect's names
// ("/name") with the same attribution as the states: the impostor that
// labelled itself is not counted.
func TestRemoteLogDriversAttributesContainers(t *testing.T) {
	const svcA = "0a1b2c3d-1111-2222-3333-444455556666"
	const svcB = "9f8e7d6c-1111-2222-3333-444455556666"
	slot := rollout.BlueContainerName(svcA, "api", "")
	project := rollout.ComposeProjectName(svcB, "worker")
	listing := `"/` + slot + `"	"` + svcA + `"	""	"loki"	"https://loki.example.com/loki/api/v1/push"
"/` + project + `-jobs-1"	""	"` + project + `"	"json-file"	""
"/impostor"	"` + svcA + `"	""	"json-file"	""`
	var asked string
	h := &fakeHost{exec: func(_ context.Context, cmd string) (string, error) { asked = cmd; return listing, nil }}
	o := newRemoteOrchestrator(h, rollout.NetConfig{}, zap.NewNop())

	logs, err := o.LogDrivers(context.Background(), []service.MetricsTarget{{ID: svcA, Name: "api"}, {ID: svcB, Name: "worker"}})
	require.NoError(t, err)
	assert.Equal(t, []service.ContainerLog{{Name: slot, Driver: "loki", LokiURL: "https://loki.example.com/loki/api/v1/push"}}, logs[svcA])
	assert.Equal(t, []service.ContainerLog{{Name: project + "-jobs-1", Driver: "json-file"}}, logs[svcB])
	assert.Contains(t, asked, `{{"\t"}}`, "docker inspect needs template tabs")
}

func TestHasLokiPlugin(t *testing.T) {
	assert.True(t, hasLokiPlugin([]string{"loki:latest true"}))
	assert.False(t, hasLokiPlugin([]string{"loki:latest false"}), "a disabled plugin can't take logs")
	assert.False(t, hasLokiPlugin([]string{"", "vieux/sshfs:latest true"}))
}
