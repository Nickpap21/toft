package docker

import (
	"context"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/rollout/fakedocker"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/netplan"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

const (
	slotSvcID = "1234abcd-0000-0000-0000-000000000001"
	otherSvc  = "9999ffff-0000-0000-0000-000000000002"
)

var (
	slotBlue  = rollout.BlueContainerName(slotSvcID, "web", "")
	slotGreen = rollout.GreenContainerName(slotSvcID, "web", "")
)

func svcLabels(serviceID string) map[string]string {
	return map[string]string{"paas.service_id": serviceID}
}

// runSlot is runContainerBlueGreen for service slotSvcID ("web") with an
// image tag and access control.
func runSlot(sw *rollout.SlotSwitch, f *fakedocker.Docker, image string, ac service.AccessControl) error {
	return runContainerBlueGreen(sw, f.Client(), rollout.NetConfig{}, netplan.LegacyPlan(slotSvcID, "web", "", ""), slotSvcID, "web", "", image,
		[]string{"web.example.com"}, nil, 3000, "", "", nil, "", "", ac, rollout.Security{}, nil)
}

// runContainerBlueGreen starts a slot the way Rollout's start step does,
// with the service settings tests used to pass before the move to rollout.
// With no sw the switch completes as soon as the new slot has started.
func runContainerBlueGreen(sw *rollout.SlotSwitch, dc *client.Client, cfg rollout.NetConfig, plan netplan.Plan, serviceID, serviceName, envName, imageTag string, domains []string, envVars []string, port int, _, _ string, volumes []service.VolumeMount, startCommand, lokiURL string, ac service.AccessControl, sec rollout.Security, warn func(string)) error {
	vols := make([]rollout.Volume, 0, len(volumes))
	for _, v := range volumes {
		vols = append(vols, rollout.Volume{Name: v.Name, Target: v.Target, HostPath: v.HostPath, ReadOnly: v.ReadOnly, Shared: v.Shared})
	}
	spec := rollout.Spec{Slot: rollout.Slot{ServiceID: serviceID}, Port: port}
	finish := sw == nil
	if finish {
		sw = &rollout.SlotSwitch{}
	}
	err := rollout.StartSlot(context.Background(), dc, cfg, plan, rollout.Slot{
		ServiceID: serviceID, ServiceName: serviceName, EnvName: envName,
		Image:     imageTag,
		Labels:    slotLabels(spec, deployment.Routing{Domains: domains, Access: ac}),
		Env:       envVars,
		Command:   startCommand,
		Volumes:   vols,
		LokiURL:   lokiURL,
		BasicAuth: rollout.BasicAuth{User: ac.BasicAuthUser, Password: ac.BasicAuthPassword},
		Security:  sec,
	}, sw, warn)
	if err != nil || !finish {
		return err
	}
	return rollout.FinishSwitch(context.Background(), dc, serviceID, sw.New, true)
}

// routeLabels are the labels Rollout gives a slot with this routing.
func routeLabels(serviceID string, domains []string, port int, ac service.AccessControl) map[string]string {
	return slotLabels(rollout.Spec{Slot: rollout.Slot{ServiceID: serviceID}, Port: port}, deployment.Routing{Domains: domains, Access: ac})
}

func TestRunContainerBlueGreenLeavesThePreviousSlotServing(t *testing.T) {
	f := fakedocker.New(t)
	f.Add(slotBlue, container.StateRunning, svcLabels(slotSvcID))
	f.Add("paas-other-9999ffff-blue", container.StateRunning, svcLabels(otherSvc))

	sw := &rollout.SlotSwitch{}
	require.NoError(t, runSlot(sw, f, "app:2", service.AccessControl{}))

	assert.Equal(t, []string{"create " + slotGreen, "start " + slotGreen}, f.RecordedOps(), "the previous slot is not touched")
	assert.Equal(t, slotGreen, sw.New)
	assert.Equal(t, []string{slotBlue}, sw.Previous)
	assert.Equal(t, container.StateRunning, f.ByName(slotBlue).State)
	assert.Equal(t, slotSvcID, f.LabelsOf(slotGreen)["paas.service_id"])
}

func TestRunContainerBlueGreenFirstDeploy(t *testing.T) {
	f := fakedocker.New(t)
	sw := &rollout.SlotSwitch{}
	require.NoError(t, runSlot(sw, f, "app:1", service.AccessControl{}))

	assert.Equal(t, slotBlue, sw.New)
	assert.Empty(t, sw.Previous, "nothing was serving")
}

func TestRunContainerBlueGreenKeepsARenamedServicesContainerServing(t *testing.T) {
	f := fakedocker.New(t)
	orphan := rollout.BlueContainerName(slotSvcID, "old-name", "")
	f.Add(orphan, container.StateRunning, svcLabels(slotSvcID))
	f.Add(rollout.BlueContainerName(slotSvcID, "older-name", ""), container.StateExited, svcLabels(slotSvcID))

	sw := &rollout.SlotSwitch{}
	require.NoError(t, runSlot(sw, f, "app:2", service.AccessControl{}))

	assert.Equal(t, slotBlue, sw.New)
	assert.Equal(t, []string{orphan}, sw.Previous, "only running containers served")
	assert.Equal(t, container.StateRunning, f.ByName(orphan).State, "retired only when the switch completes")
}

func TestRunContainerBlueGreenReplacesAStaleSlot(t *testing.T) {
	f := fakedocker.New(t)
	f.Add(slotBlue, container.StateRunning, svcLabels(slotSvcID))
	f.Add(slotGreen, container.StateExited, svcLabels(slotSvcID)) // a failed earlier attempt

	sw := &rollout.SlotSwitch{}
	require.NoError(t, runSlot(sw, f, "app:3", service.AccessControl{}))

	assert.Equal(t, []string{"stop " + slotGreen, "remove " + slotGreen, "create " + slotGreen, "start " + slotGreen}, f.RecordedOps())
	assert.Equal(t, []string{slotBlue}, sw.Previous)
}

func TestRunContainerBlueGreenWithoutASlotSwitchCompletesIt(t *testing.T) {
	f := fakedocker.New(t)
	f.Add(slotBlue, container.StateRunning, svcLabels(slotSvcID))

	require.NoError(t, runSlot(nil, f, "app:2", service.AccessControl{}))

	assert.Equal(t, []string{"create " + slotGreen, "start " + slotGreen, "stop " + slotBlue, "remove " + slotBlue}, f.RecordedOps(),
		"the previous slot is retired only once the new one started")
	assert.Equal(t, []string{slotGreen}, f.Names())
}

func TestRunContainerBlueGreenRecordsTheNewSlotWhenItFailsToStart(t *testing.T) {
	f := fakedocker.New(t)
	f.Add(slotBlue, container.StateRunning, svcLabels(slotSvcID))
	f.StartErr = true

	sw := &rollout.SlotSwitch{}
	err := runSlot(sw, f, "app:2", service.AccessControl{})
	require.Error(t, err)
	assert.Equal(t, slotGreen, sw.New, "the created container is known, so the pipeline can remove it")
	assert.Equal(t, []string{slotBlue}, sw.Previous)
	assert.Equal(t, container.StateRunning, f.ByName(slotBlue).State)
}

func TestRunContainerBlueGreenKeepsTheBasicAuthEntry(t *testing.T) {
	f := fakedocker.New(t)
	ac := service.AccessControl{BasicAuthUser: "admin", BasicAuthPassword: "s3cret"}
	prev := routeLabels(slotSvcID, []string{"web.example.com"}, 3000, ac)
	f.Add(slotBlue, container.StateRunning, prev)

	require.NoError(t, runSlot(&rollout.SlotSwitch{}, f, "app:2", ac))

	got := f.LabelsOf(slotGreen)
	for k, v := range prev {
		assert.Equal(t, v, got[k], "label %s: both slots carry the same Traefik labels", k)
	}
	assert.Len(t, got, len(prev))
}

func TestRemoteStartContainerUsesTheSSHDockerTunnel(t *testing.T) {
	f := fakedocker.New(t)
	f.Add(slotGreen, container.StateExited, svcLabels(slotSvcID))
	o := newRemoteOrchestrator(&fakeHost{docker: f}, rollout.NetConfig{}, zap.NewNop())

	require.NoError(t, o.StartContainer(context.Background(), f.ByName(slotGreen).ID))
	assert.Equal(t, container.StateRunning, f.ByName(slotGreen).State)
}

func TestRunContainerBlueGreenWithBothSlotsRunningReplacesTheOneNotServing(t *testing.T) {
	labelled := func(dep string) map[string]string {
		return map[string]string{rollout.LabelServiceID: slotSvcID, rollout.LabelDeploymentID: dep}
	}

	t.Run("the newer slot serves: a switch that could not retire the older one", func(t *testing.T) {
		f := fakedocker.New(t)
		f.Add(slotBlue, container.StateRunning, labelled("dep-1"))
		f.Add(slotGreen, container.StateRunning, labelled("dep-2")) // created later

		sw := &rollout.SlotSwitch{}
		require.NoError(t, runSlot(sw, f, "app:3", service.AccessControl{}))

		assert.Equal(t, []string{"stop " + slotBlue, "remove " + slotBlue, "create " + slotBlue, "start " + slotBlue}, f.RecordedOps(),
			"the newer green slot is never stopped")
		assert.Equal(t, slotBlue, sw.New)
		assert.Equal(t, []string{slotGreen}, sw.Previous)
		assert.Equal(t, container.StateRunning, f.ByName(slotGreen).State)
	})

	t.Run("the active deployment's slot serves: a failed switch whose new slot could not be removed", func(t *testing.T) {
		f := fakedocker.New(t)
		f.Add(slotBlue, container.StateRunning, labelled("dep-1"))
		f.Add(slotGreen, container.StateRunning, labelled("dep-2-failed"))

		sw := &rollout.SlotSwitch{ActiveDeployment: "dep-1"}
		require.NoError(t, runSlot(sw, f, "app:3", service.AccessControl{}))

		assert.Equal(t, []string{"stop " + slotGreen, "remove " + slotGreen, "create " + slotGreen, "start " + slotGreen}, f.RecordedOps())
		assert.Equal(t, []string{slotBlue}, sw.Previous)
	})
}

func TestRunContainerBlueGreenIgnoresImageLabelledContainers(t *testing.T) {
	// A tenant's compose container whose image claims this service: it is
	// not one of the service's slots, so it is not "serving", its basic-auth
	// entry is never checked and the switch never treats it as previous.
	f := fakedocker.New(t)
	ac := service.AccessControl{BasicAuthUser: "admin", BasicAuthPassword: "s3cret"}
	slow, err := bcrypt.GenerateFromPassword([]byte("s3cret"), bcrypt.MinCost+1)
	require.NoError(t, err)
	spoofed := map[string]string{
		rollout.LabelServiceID:                 slotSvcID,
		rollout.LabelComposeProject:            "attacker",
		rollout.BasicAuthUsersLabel(slotSvcID): "admin:" + string(slow),
		"traefik.http.routers.x.rule":          "Host(`evil.example.com`)",
		"traefik.http.services.paas-x.lb":      "1",
	}
	f.Add("attacker-web-1", container.StateRunning, spoofed)

	sw := &rollout.SlotSwitch{}
	require.NoError(t, runSlot(sw, f, "app:1", ac))

	assert.Empty(t, sw.Previous, "a first deploy: nothing of the service was serving")
	assert.NotEqual(t, spoofed[rollout.BasicAuthUsersLabel(slotSvcID)], f.LabelsOf(slotBlue)[rollout.BasicAuthUsersLabel(slotSvcID)])
	assert.False(t, sw.RoutingChanged)

	require.NoError(t, rollout.FinishSwitch(context.Background(), f.Client(), slotSvcID, slotBlue, true))
	assert.ElementsMatch(t, []string{"attacker-web-1", slotBlue}, f.Names(), "finishing the switch leaves it alone")
	require.NoError(t, rollout.AbortSwitch(context.Background(), f.Client(), slotSvcID, "attacker-web-1"))
	assert.NotNil(t, f.ByName("attacker-web-1"), "and so does aborting one")
}

func TestRunContainerBlueGreenReportsARoutingChange(t *testing.T) {
	ac := service.AccessControl{BasicAuthUser: "admin", BasicAuthPassword: "s3cret"}
	cases := []struct {
		name    string
		domains []string
		port    int
		ac      service.AccessControl
		changed bool
	}{
		{"same routing", []string{"web.example.com"}, 3000, ac, false},
		{"same routing, one more domain", []string{"web.example.com", "www.example.com"}, 3000, ac, false},
		{"another domain", []string{"new.example.com"}, 3000, ac, true},
		{"another port", []string{"web.example.com"}, 8080, ac, true},
		{"another password", []string{"web.example.com"}, 3000, service.AccessControl{BasicAuthUser: "admin", BasicAuthPassword: "new"}, true},
		{"basic auth removed", []string{"web.example.com"}, 3000, service.AccessControl{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := fakedocker.New(t)
			f.Add(slotBlue, container.StateRunning, routeLabels(slotSvcID, []string{"web.example.com"}, 3000, ac))
			sw := &rollout.SlotSwitch{}
			require.NoError(t, runContainerBlueGreen(sw, f.Client(), rollout.NetConfig{}, netplan.LegacyPlan(slotSvcID, "web", "", ""), slotSvcID, "web", "", "app:2",
				tc.domains, nil, tc.port, "", "", nil, "", "", tc.ac, rollout.Security{}, nil))
			assert.Equal(t, tc.changed, sw.RoutingChanged)
		})
	}
}

// A shared volume (D-38) is created by name if missing and mounted as a
// named volume, read-only when set, next to the service's own volumes.
func TestRunContainerMountsASharedVolume(t *testing.T) {
	f := fakedocker.New(t)
	volumes := []service.VolumeMount{
		{Name: "images", Target: "/app/images", ReadOnly: true, Shared: "paas-shared-1111"},
		{Name: "data", Target: "/data"},
	}
	require.NoError(t, runContainerBlueGreen(nil, f.Client(), rollout.NetConfig{}, netplan.LegacyPlan(slotSvcID, "web", "", ""),
		slotSvcID, "web", "", "app:1", nil, nil, 3000, "", "", volumes, "", "", service.AccessControl{}, rollout.Security{}, nil))

	var c *fakedocker.Container
	for _, fc := range f.Containers {
		if fc.Image == "app:1" {
			c = fc
		}
	}
	require.NotNil(t, c)
	require.Len(t, c.Mounts, 2)
	assert.Equal(t, mount.Mount{Type: mount.TypeVolume, Source: "paas-shared-1111", Target: "/app/images", ReadOnly: true}, c.Mounts[0])
	assert.Equal(t, "paas-vol-1234abcd-data", c.Mounts[1].Source, "the service's own volume is unchanged")
	assert.Contains(t, f.Ops, "volume create paas-shared-1111")
}

func TestRunContainerBlueGreenRecreateStopsThePreviousSlotFirst(t *testing.T) {
	f := fakedocker.New(t)
	f.Add(slotBlue, container.StateRunning, svcLabels(slotSvcID))
	f.Add("paas-other-9999ffff-blue", container.StateRunning, svcLabels(otherSvc))
	// An image can carry the service's label; it is not a slot.
	f.Add("tenant-db", container.StateRunning, svcLabels(slotSvcID))

	sw := &rollout.SlotSwitch{Recreate: true}
	require.NoError(t, runSlot(sw, f, "mysql:8", service.AccessControl{}))

	assert.Equal(t, []string{"stop " + slotBlue, "create " + slotGreen, "start " + slotGreen}, f.RecordedOps(),
		"stopped before the new one is created, and kept")
	assert.Equal(t, []string{slotBlue}, sw.Stopped)
	assert.Equal(t, []string{slotBlue}, sw.Previous)
	assert.False(t, sw.RoutingChanged)
	assert.Equal(t, container.StateExited, f.ByName(slotBlue).State)
	assert.Equal(t, container.StateRunning, f.ByName("tenant-db").State)
}

func TestRunContainerBlueGreenRecreateKeepsTheStoppedSlotWhenTheNewOneFails(t *testing.T) {
	f := fakedocker.New(t)
	f.Add(slotBlue, container.StateRunning, svcLabels(slotSvcID))
	f.StartErr = true

	sw := &rollout.SlotSwitch{Recreate: true}
	require.Error(t, runSlot(sw, f, "mysql:8", service.AccessControl{}))
	assert.Equal(t, []string{slotBlue}, sw.Stopped, "the pipeline starts it again")
	assert.NotNil(t, f.ByName(slotBlue))
}

// TestSlotIsHardened: a service's container gets the hardened runtime
// (D-93), its own process limit, and raw sockets only with net_raw.
func TestSlotIsHardened(t *testing.T) {
	f := fakedocker.New(t)
	require.NoError(t, runContainerBlueGreen(nil, f.Client(), rollout.NetConfig{}, netplan.LegacyPlan(slotSvcID, "web", "", ""), slotSvcID, "web", "", "app:1",
		nil, nil, 3000, "", "", nil, "", "", service.AccessControl{}, rollout.Security{}, nil))
	c := f.Containers[len(f.Containers)-1]
	assert.Equal(t, []string{"no-new-privileges:true"}, c.SecurityOpt)
	assert.Equal(t, []string{"CAP_NET_RAW"}, c.CapDrop, "the client sends it normalised")
	require.NotNil(t, c.PidsLimit)
	assert.Equal(t, int64(rollout.DefaultPidsLimit), *c.PidsLimit)

	require.NoError(t, runContainerBlueGreen(nil, f.Client(), rollout.NetConfig{}, netplan.LegacyPlan(slotSvcID, "web", "", ""), slotSvcID, "web", "", "app:2",
		nil, nil, 3000, "", "", nil, "", "", service.AccessControl{}, rollout.Security{PidsLimit: 512, NetRaw: true}, nil))
	c = f.Containers[len(f.Containers)-1]
	assert.Empty(t, c.CapDrop, "net_raw keeps raw sockets")
	assert.Equal(t, int64(512), *c.PidsLimit)
}

func TestSandboxedSlotRunsUnderGvisor(t *testing.T) {
	f := fakedocker.New(t)
	require.NoError(t, runContainerBlueGreen(nil, f.Client(), rollout.NetConfig{}, netplan.LegacyPlan(slotSvcID, "web", "", ""), slotSvcID, "web", "", "app:1",
		nil, nil, 3000, "", "", nil, "", "", service.AccessControl{}, rollout.Security{Runtime: rollout.RuntimeSandboxed}, nil))
	c := f.Containers[len(f.Containers)-1]
	assert.Equal(t, "runsc", c.Runtime, "a Sandboxed service (D-95)")
	assert.Equal(t, []string{"no-new-privileges:true"}, c.SecurityOpt, "and the Standard hardening too")
	assert.Equal(t, []string{"CAP_NET_RAW"}, c.CapDrop)
	require.NotNil(t, c.PidsLimit)

	require.NoError(t, runContainerBlueGreen(nil, f.Client(), rollout.NetConfig{}, netplan.LegacyPlan(slotSvcID, "web", "", ""), slotSvcID, "web", "", "app:2",
		nil, nil, 3000, "", "", nil, "", "", service.AccessControl{}, rollout.Security{}, nil))
	assert.Equal(t, "", f.Containers[len(f.Containers)-1].Runtime, "Standard leaves Docker's default, runc")
}
