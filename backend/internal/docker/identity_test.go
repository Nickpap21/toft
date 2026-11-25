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

// A look-alike is a container compose made that carries a slot's whole
// identity: the target's paas.service_id from its image and one of the
// target's slot names. Compose's own project label gives it away.
func lookAlike(f *fakedocker.Docker, name string) {
	f.Add(name, container.StateRunning, map[string]string{
		"paas.service_id":            slotSvcID,
		"com.docker.compose.project": rollout.ComposeProjectName(otherSvc, "evil"),
		"com.docker.compose.service": "app",
	})
}

// The admin-password reset and a command with no container named go to
// the first container found, and a post-deploy command to the first one
// listed: the look-alike is listed first, as Docker lists the newest.
func TestALookAlikeGetsNoCommandOfTheService(t *testing.T) {
	ctx := context.Background()
	f := fakedocker.New(t)
	lookAlike(f, slotGreen)
	f.Add(slotBlue, container.StateRunning, svcLabels(slotSvcID))

	got, err := runningContainer(ctx, f.Client(), slotSvcID, "web", "")
	require.NoError(t, err)
	assert.Equal(t, slotBlue, got, "the password reset's container")

	names, err := listServiceContainerNames(ctx, f.Client(), slotSvcID, "web")
	require.NoError(t, err)
	assert.Equal(t, []string{slotBlue}, names, "a post-deploy command runs in the first of these")

	states, err := serviceContainerList(ctx, f.Client(), slotSvcID, "web")
	require.NoError(t, err)
	for _, st := range containerStatesFrom(states, slotSvcID) {
		assert.Equal(t, slotBlue, st.Name)
		assert.True(t, st.Slot)
	}
}

// With no slot of its own running the service has no container, whatever
// holds its names.
func TestALookAlikeAloneIsNoContainerOfTheService(t *testing.T) {
	ctx := context.Background()
	f := fakedocker.New(t)
	lookAlike(f, slotBlue)

	_, err := runningContainer(ctx, f.Client(), slotSvcID, "web", "")
	assert.ErrorIs(t, err, service.ErrNotRunning)
	assert.Equal(t, "", resolveContainer(ctx, f.Client(), slotSvcID, "web"), "the live log has nothing to follow")
	assert.Empty(t, rollout.RunningSlots(ctx, f.Client(), slotSvcID, "web", ""))
}

// The live log follows the service's own slot: not a newer container
// under its other slot's name, and not one under the name its slot would
// have outside an environment.
func TestResolveContainerPassesOverALookAlike(t *testing.T) {
	ctx := context.Background()
	f := fakedocker.New(t)
	f.Add(slotBlue, container.StateRunning, svcLabels(slotSvcID))
	lookAlike(f, slotGreen)
	assert.Equal(t, slotBlue, resolveContainer(ctx, f.Client(), slotSvcID, "web"))

	f = fakedocker.New(t)
	envSlot := rollout.BlueContainerName(slotSvcID, "web", "production")
	f.Add(envSlot, container.StateRunning, svcLabels(slotSvcID))
	lookAlike(f, slotBlue)
	assert.Equal(t, envSlot, resolveContainer(ctx, f.Client(), slotSvcID, "web"))
}

func TestRemoteActiveContainerNamePassesOverALookAlike(t *testing.T) {
	proj := rollout.ComposeProjectName(otherSvc, "evil")
	// Docker's answers: a container's compose project only where the
	// command's format asks for it.
	project := func(cmd, proj string) string {
		if strings.Contains(cmd, rollout.LabelComposeProject) {
			return " " + proj
		}
		return ""
	}
	own := true
	h := &fakeHost{exec: func(_ context.Context, cmd string) (string, error) {
		switch {
		case strings.Contains(cmd, "label=paas.service_id=") && own:
			return slotGreen + project(cmd, proj) + "\n" + slotBlue + project(cmd, "") + "\n", nil
		case strings.Contains(cmd, "label=paas.service_id="):
			return slotGreen + project(cmd, proj) + "\n", nil
		case strings.Contains(cmd, "docker inspect") && strings.Contains(cmd, slotGreen):
			return "true" + project(cmd, proj) + "\n", nil // running, and compose's
		}
		return "", nil
	}}
	assert.Equal(t, slotBlue, remoteActiveContainerName(context.Background(), h, slotSvcID, "web"))

	own = false
	assert.Equal(t, "", remoteActiveContainerName(context.Background(), h, slotSvcID, "web"),
		"by the label and by the slot's name it is still compose's container")
}

func TestStopLeavesALookAlikeAlone(t *testing.T) {
	f := fakedocker.New(t)
	f.Add(slotBlue, container.StateRunning, svcLabels(slotSvcID))
	lookAlike(f, slotGreen)
	lookAlike(f, rollout.ContainerNameForService(slotSvcID))

	require.NoError(t, stopAllSlots(context.Background(), f.Client(), slotSvcID))
	assert.ElementsMatch(t, []string{slotGreen, rollout.ContainerNameForService(slotSvcID)}, f.Names())
}

// Status, health and metrics count a container by attribute: a look-alike
// counts for nobody, neither the service whose identity it carries nor the
// stack that made it.
func TestALookAlikeIsAttributedToNobody(t *testing.T) {
	proj := rollout.ComposeProjectName(otherSvc, "evil")
	a := newAttributor([]service.MetricsTarget{{ID: slotSvcID, Name: "web"}, {ID: otherSvc, Name: "evil"}})

	id, _ := a.attribute(hostContainer{Name: slotGreen, ServiceID: slotSvcID, ComposeProject: proj, ComposeService: "app", Image: "evil:1"})
	assert.Equal(t, "", id)
	id, _ = a.attribute(hostContainer{Name: slotBlue, ServiceID: slotSvcID, Image: "app:1"})
	assert.Equal(t, slotSvcID, id)
	id, svc := a.attribute(hostContainer{Name: proj + "-app-1", ComposeProject: proj, ComposeService: "app", Image: "evil:1"})
	assert.Equal(t, otherSvc, id)
	assert.Equal(t, "app", svc)
}

// The check of a stack's routes passes over no container of the stack. A
// service named "svc" has the project paas-svc-<id8>, so compose's own
// name for its container is the pre-blue/green name of the "service" its
// image's label spells; and a container_name can be any slot's.
func TestStackRouteViolationsCheckAContainerWithASlotsIdentity(t *testing.T) {
	route := testRoute("web")
	for _, tc := range []struct{ name, serviceName, container, label string }{
		{"compose's own name", "svc", rollout.ComposeProjectName(testServiceID, "svc") + "-web-1", testServiceID[:8] + "-web-1"},
		{"a slot's name", "stack", slotBlue, slotSvcID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proj := rollout.ComposeProjectName(testServiceID, tc.serviceName)
			require.True(t, rollout.LooksLikeSlot(map[string]string{"paas.service_id": tc.label}, tc.container))
			labels := map[string]string{
				rollout.LabelComposeProject: proj, "com.docker.compose.service": "web", "paas.service_id": tc.label,
				"traefik.http.routers.evil.rule": "Host(`panel.example.com`)",
			}
			for k, v := range composeRouteLabels(testServiceID, netEnv.Name, route) {
				labels[k] = v
			}
			list := []container.Summary{{Names: []string{"/" + tc.container}, Labels: labels}}
			assert.Equal(t, []string{tc.container + ": traefik.http.routers.evil.rule"},
				rollout.StackRouteViolations(list, proj, route.Service, routeLabelKeys(testServiceID, netEnv.Name, route)))
			assert.False(t, rollout.IsPlatformSlot(labels, tc.container))
		})
	}
}
