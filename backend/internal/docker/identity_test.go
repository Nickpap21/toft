package docker

import (
	"context"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/rollout/fakedocker"

	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An impostor is a tenant's container whose image carries another
// service's paas.service_id label. Its name is not a slot name.
const impostor = "evil-app-1"

func TestRunContainerBlanksIdentityLabelsTheImageCarries(t *testing.T) {
	f := fakedocker.New(t)
	f.SetImageLabels("app:1", map[string]string{
		"paas.service_id":            otherSvc,
		"paas.deployment_id":         "dep-of-someone-else",
		"com.docker.compose.project": "paas-victim-9999ffff",
	})
	require.NoError(t, runSlot(nil, f, "app:1", service.AccessControl{}))
	labels := f.LabelsOf(slotBlue)
	assert.Equal(t, slotSvcID, labels["paas.service_id"])
	assert.Equal(t, "", labels["paas.deployment_id"])
	assert.Equal(t, "", labels["com.docker.compose.project"])
}

func TestStopAllSlotsLeavesAnImpostorAlone(t *testing.T) {
	f := fakedocker.New(t)
	f.Add(slotBlue, container.StateRunning, svcLabels(slotSvcID))
	f.Add(impostor, container.StateRunning, svcLabels(slotSvcID))
	f.Add(rollout.ContainerNameForService(slotSvcID)+"-old", container.StateRunning, nil)

	require.NoError(t, stopAllSlots(context.Background(), f.Client(), slotSvcID))
	assert.ElementsMatch(t, []string{impostor, rollout.ContainerNameForService(slotSvcID) + "-old"}, f.Names(),
		"only the service's own slot is removed; a longer name than the legacy one is not the legacy container")
}

func TestListServiceContainerNamesLeavesAnImpostorOut(t *testing.T) {
	f := fakedocker.New(t)
	f.Add(slotBlue, container.StateRunning, svcLabels(slotSvcID))
	f.Add(impostor, container.StateRunning, svcLabels(slotSvcID))
	f.Add("paas-web-1234abcd-db-1", container.StateRunning, map[string]string{"com.docker.compose.project": rollout.ComposeProjectName(slotSvcID, "web")})

	names, err := listServiceContainerNames(context.Background(), f.Client(), slotSvcID, "web")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{slotBlue, "paas-web-1234abcd-db-1"}, names)
}

func TestResolveContainerFallbackLeavesAnImpostorOut(t *testing.T) {
	f := fakedocker.New(t)
	f.Add(impostor, container.StateRunning, svcLabels(slotSvcID))
	assert.Equal(t, "", resolveContainer(context.Background(), f.Client(), slotSvcID, ""))

	envSlot := rollout.BlueContainerName(slotSvcID, "web", "production")
	f.Add(envSlot, container.StateRunning, svcLabels(slotSvcID))
	assert.Equal(t, envSlot, resolveContainer(context.Background(), f.Client(), slotSvcID, ""))
}

func TestRemoteActiveContainerNameLeavesAnImpostorOut(t *testing.T) {
	h := &fakeHost{exec: func(_ context.Context, cmd string) (string, error) {
		if strings.Contains(cmd, "label=paas.service_id=") {
			return impostor + "\n" + slotGreen + "\n", nil
		}
		return "", nil
	}}
	assert.Equal(t, slotGreen, remoteActiveContainerName(context.Background(), h, slotSvcID, "web"))

	h.exec = func(_ context.Context, cmd string) (string, error) {
		if strings.Contains(cmd, "label=paas.service_id=") {
			return impostor + "\n", nil
		}
		return "", nil
	}
	assert.Equal(t, "", remoteActiveContainerName(context.Background(), h, slotSvcID, "web"))
}

// A slot created before image labels were blanked can still carry another
// service's compose project label: it is never part of that stack.
func TestASlotWithAComposeLabelIsNotListedWithTheStack(t *testing.T) {
	f := fakedocker.New(t)
	proj := rollout.ComposeProjectName(otherSvc, "victim")
	f.Add(slotBlue, container.StateRunning, map[string]string{
		"paas.service_id": slotSvcID, "com.docker.compose.project": proj, "com.docker.compose.service": "db",
	})
	f.Add(proj+"-db-1", container.StateRunning, map[string]string{"com.docker.compose.project": proj, "com.docker.compose.service": "db"})

	names, err := listServiceContainerNames(context.Background(), f.Client(), otherSvc, "victim")
	require.NoError(t, err)
	assert.Equal(t, []string{proj + "-db-1"}, names)
}
