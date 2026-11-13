package docker

import (
	"context"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/rollout/fakedocker"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestSlotLabelsCarryTheDeploymentID(t *testing.T) {
	plain := slotLabels(rollout.Spec{Slot: rollout.Slot{ServiceID: slotSvcID}}, deployment.Routing{})
	assert.Equal(t, slotSvcID, plain[rollout.LabelServiceID])
	_, ok := plain[rollout.LabelDeploymentID]
	assert.False(t, ok, "no deployment, no label")

	labelled := slotLabels(rollout.Spec{DeploymentID: "dep-7", Slot: rollout.Slot{ServiceID: slotSvcID}, Port: 80},
		deployment.Routing{Domains: []string{"a.example.com"}})
	assert.Equal(t, "dep-7", labelled[rollout.LabelDeploymentID])
	assert.Equal(t, slotSvcID, labelled[rollout.LabelServiceID])
	assert.Equal(t, "true", labelled["traefik.enable"])
}

func TestContainerStatesFrom(t *testing.T) {
	created := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	got := containerStatesFrom([]container.Summary{
		{
			ID: "c1", Names: []string{"/" + slotGreen}, Image: "paas/x:dep-2", State: container.StateRunning, Created: created.Unix(),
			Labels: map[string]string{rollout.LabelServiceID: slotSvcID, rollout.LabelDeploymentID: "dep-2"},
		},
		{ID: "c2", Names: []string{"/web-1"}, Image: "nginx", State: container.StateExited, Labels: map[string]string{rollout.LabelComposeProject: "paas-web-1234abcd"}},
		{ID: "c3", Names: []string{"/evil-web-1"}, State: container.StateRunning, Labels: map[string]string{rollout.LabelServiceID: slotSvcID}},
	}, slotSvcID)
	require.Len(t, got, 3)
	assert.Equal(t, deployment.ContainerState{
		ID: "c1", Name: slotGreen, Image: "paas/x:dep-2", State: "running", Created: time.Unix(created.Unix(), 0),
		DeploymentID: "dep-2", ServiceID: slotSvcID, Slot: true,
	}, got[0])
	assert.False(t, got[1].Slot)
	assert.False(t, got[2].Slot, "the label without a slot's reserved name is an image's")
	assert.True(t, got[0].Running())
	assert.Equal(t, "paas-web-1234abcd", got[1].ComposeProject)
	assert.True(t, got[1].Created.IsZero(), "an unknown creation time stays unknown")
	assert.False(t, got[1].Running())
}

func TestInspectServiceContainers(t *testing.T) {
	f := fakedocker.New(t)
	f.Add(slotBlue, container.StateRunning, svcLabels(slotSvcID))
	f.Add(slotGreen, container.StateExited, svcLabels(slotSvcID))
	f.Add("web-1", container.StateRunning, map[string]string{rollout.LabelComposeProject: rollout.ComposeProjectName(slotSvcID, "web")})
	f.Add("legacy-1", container.StateRunning, map[string]string{rollout.LabelComposeProject: "paas-1234abcd"})
	f.Add("paas-other-9999ffff-blue", container.StateRunning, svcLabels(otherSvc))
	f.Add("traefik", container.StateRunning, nil)
	// A tenant's compose container whose image carries this service's label.
	spoofed := svcLabels(slotSvcID)
	spoofed[rollout.LabelComposeProject] = "attacker"
	f.Add("attacker-web-1", container.StateRunning, spoofed)

	o := &LocalOrchestrator{dockerClient: f.Client(), log: zap.NewNop()}
	got, err := o.InspectServiceContainers(context.Background(), slotSvcID, "web")
	require.NoError(t, err)
	var names []string
	for _, c := range got {
		names = append(names, c.Name)
	}
	assert.ElementsMatch(t, []string{slotBlue, slotGreen, "web-1", "legacy-1"}, names)

	require.NoError(t, o.RemoveContainer(context.Background(), f.ByName(slotGreen).ID))
	assert.Equal(t, []string{"stop " + slotGreen, "remove " + slotGreen}, f.RecordedOps())
	require.NoError(t, o.RemoveContainer(context.Background(), "gone"), "a container that is already gone is not an error")
}

func TestRemoteInspectUsesTheSSHDockerTunnel(t *testing.T) {
	f := fakedocker.New(t)
	f.Add(slotBlue, container.StateRunning, svcLabels(slotSvcID))
	o := newRemoteOrchestrator(&fakeHost{docker: f}, rollout.NetConfig{}, zap.NewNop())

	got, err := o.InspectServiceContainers(context.Background(), slotSvcID, "web")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, slotBlue, got[0].Name)
	require.NoError(t, o.RemoveContainer(context.Background(), got[0].ID))
	assert.Empty(t, f.Names())

	_, err = newTestRemote(&fakeHost{}).InspectServiceContainers(context.Background(), slotSvcID, "web")
	assert.Error(t, err, "no Docker tunnel: the server could not be checked")
}
