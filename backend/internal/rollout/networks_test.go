package rollout

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/netplan"
	"github.com/Nickpap21/toft/backend/internal/rollout/fakedocker"
	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ops(f *fakedocker.Docker, dry bool) *netOps {
	return newNetOps(f.Client(), NetConfig{}, dry, nil)
}

func TestEnsureNetworkCreatesOnceWithLabels(t *testing.T) {
	f := fakedocker.New(t)
	o := ops(f, false)
	require.NoError(t, o.ensureNetwork(context.Background(), netEnv))
	require.NoError(t, o.ensureNetwork(context.Background(), netEnv))
	assert.Equal(t, []string{"netcreate paas-env-0e000000-0000-0000-0000-0000000000e1"}, f.RecordedOps())
	assert.Equal(t, netEnv.Labels(), f.Network("paas-env-0e000000-0000-0000-0000-0000000000e1").Labels)
	assert.Equal(t, []string{"paas-env-0e000000-0000-0000-0000-0000000000e1"}, o.report.Created)
}

func TestEnsureNetworkRefusesANetworkThePanelDidNotCreate(t *testing.T) {
	for name, labels := range map[string]map[string]string{
		"no labels":       nil,
		"another env":     netOther.Labels(),
		"spoofed managed": {netplan.LabelManaged: "true"},
	} {
		t.Run(name, func(t *testing.T) {
			f := fakedocker.New(t)
			f.AddNetwork("paas-env-0e000000-0000-0000-0000-0000000000e1", labels)
			err := ops(f, false).ensureNetwork(context.Background(), netEnv)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "not created by the panel")
		})
	}
}

func TestEnsureNetworkRace(t *testing.T) {
	f := fakedocker.New(t)
	f.NetCreateRace = true
	require.NoError(t, ops(f, false).ensureNetwork(context.Background(), netEnv), "another creator won with the same labels")
}

func TestEnsureNetworkAddressPoolHint(t *testing.T) {
	f := fakedocker.New(t)
	f.NetCreateErr = "could not find an available, non-overlapping IPv4 address pool among the defaults to assign to the network"
	err := ops(f, false).ensureNetwork(context.Background(), netEnv)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "default-address-pools")
}

func TestEnsureNetworkDryRunCreatesNothing(t *testing.T) {
	f := fakedocker.New(t)
	o := ops(f, true)
	require.NoError(t, o.ensureNetwork(context.Background(), netEnv))
	assert.Empty(t, f.RecordedOps())
	assert.Equal(t, []string{"paas-env-0e000000-0000-0000-0000-0000000000e1"}, o.report.Created)
}

func TestEnsureNetworkRefusesAnInvalidNetwork(t *testing.T) {
	f := fakedocker.New(t)
	assert.Error(t, ops(f, false).ensureNetwork(context.Background(), netplan.Network{Name: "paas-network", Kind: netplan.KindEnv, EnvironmentID: "0e000000-0000-0000-0000-0000000000e1"}))
}

func TestConnectTreatsAnAttachedContainerAsSuccess(t *testing.T) {
	f := fakedocker.New(t)
	f.AddNetwork("paas-env-0e000000-0000-0000-0000-0000000000e1", netEnv.Labels())
	c := f.Add(slotBlue, container.StateRunning, svcLabels(slotSvcID))
	f.ConnectErr = true
	ref := containerRef{id: c.ID, name: c.Name, networks: map[string]bool{}}
	require.NoError(t, ops(f, false).connect(context.Background(), "paas-env-0e000000-0000-0000-0000-0000000000e1", ref, []string{"web"}))
	f.ConnectErr = false
	require.NoError(t, ops(f, false).connect(context.Background(), "paas-env-0e000000-0000-0000-0000-0000000000e1", ref, []string{"web"}), "already attached: skipped")
	assert.Equal(t, []string{"connect paas-env-0e000000-0000-0000-0000-0000000000e1 " + slotBlue}, f.RecordedOps())
}

func TestPrepareAttachesTheProxyOwnContainersAndPeers(t *testing.T) {
	f, _ := hostWithProxy(t)
	own := f.Add(slotBlue, container.StateRunning, svcLabels(slotSvcID))
	f.Attach(own, "paas-network", "web")
	impostor := f.Add("evil-1", container.StateRunning, svcLabels(slotSvcID))
	f.Attach(impostor, "paas-network")

	dbID := "aaaa1111-0000-0000-0000-000000000003"
	db := f.Add(BlueContainerName(dbID, "db", "production"), container.StateRunning, svcLabels(dbID))
	f.Attach(db, "paas-network", "db")

	stackID := "bbbb2222-0000-0000-0000-000000000004"
	proj := ComposeProjectName(stackID, "stack")
	routed := f.Add(proj+"-api-1", container.StateRunning, map[string]string{LabelComposeProject: proj, "com.docker.compose.service": "api"})
	f.Attach(routed, "paas-network")
	private := f.Add(proj+"-cache-1", container.StateRunning, map[string]string{LabelComposeProject: proj, "com.docker.compose.service": "cache"})
	f.AddNetwork(proj+"_default", nil)
	f.Attach(private, proj+"_default")

	plan := envPlan()
	plan.Attach = []netplan.Endpoint{
		{ServiceID: dbID, ServiceName: "db", Alias: "postgres"},
		{ServiceID: stackID, ServiceName: "stack", Compose: true},
	}
	require.NoError(t, ops(f, false).prepare(context.Background(), plan))

	assert.Contains(t, f.NetworksOf("paas-traefik"), "paas-env-0e000000-0000-0000-0000-0000000000e1")
	assert.Equal(t, []string{"web"}, f.AliasesOn(slotBlue, "paas-env-0e000000-0000-0000-0000-0000000000e1"))
	assert.Equal(t, []string{"postgres"}, f.AliasesOn(db.Name, "paas-env-0e000000-0000-0000-0000-0000000000e1"), "a peer joins with its own alias")
	assert.Equal(t, []string{"api"}, f.AliasesOn(routed.Name, "paas-env-0e000000-0000-0000-0000-0000000000e1"), "a compose peer joins with its compose service name")
	assert.NotContains(t, f.NetworksOf(private.Name), "paas-env-0e000000-0000-0000-0000-0000000000e1", "stack-private containers stay private")
	assert.NotContains(t, f.NetworksOf("evil-1"), "paas-env-0e000000-0000-0000-0000-0000000000e1", "a label from an image attaches nothing")
}

func TestPrepareWithoutAProxyOnlyWarns(t *testing.T) {
	f := fakedocker.New(t)
	var lines []string
	o := newNetOps(f.Client(), NetConfig{}, false, func(l string) { lines = append(lines, l) })
	require.NoError(t, o.prepare(context.Background(), envPlan()))
	assert.Contains(t, strings.Join(lines, "\n"), "proxy container paas-traefik not found")
}

func TestPrepareIsANoOpUnderLegacy(t *testing.T) {
	f := fakedocker.New(t)
	require.NoError(t, ops(f, false).prepare(context.Background(), netplan.LegacyPlan(slotSvcID, "web", "web", "")))
	assert.Empty(t, f.RecordedOps())
	assert.Error(t, ops(f, false).prepare(context.Background(), netplan.Plan{}), "an empty plan is an error")
}

func TestPublishSkipsMissingAndForeignNetworks(t *testing.T) {
	f := fakedocker.New(t)
	f.AddNetwork("paas-env-0e000000-0000-0000-0000-0000000000e2", netOther.Labels())
	f.AddNetwork("paas-env-0e000000-0000-0000-0000-0000000000e3", nil) // not the panel's
	c := f.Add(slotBlue, container.StateRunning, svcLabels(slotSvcID))
	plan := netplan.Plan{ServiceID: slotSvcID, Home: netplan.Project("0a000000-0000-0000-0000-0000000000a1", "0b000000-0000-0000-0000-0000000000b1"),
		Publish: []netplan.Network{netOther, netplan.Env("0e000000-0000-0000-0000-0000000000e3", "0a000000-0000-0000-0000-0000000000a1", "0b000000-0000-0000-0000-0000000000b1"), netplan.Env("0e000000-0000-0000-0000-0000000000e4", "0a000000-0000-0000-0000-0000000000a1", "0b000000-0000-0000-0000-0000000000b1")}}
	var lines []string
	o := newNetOps(f.Client(), NetConfig{}, false, func(l string) { lines = append(lines, l) })
	o.publish(context.Background(), plan, containerRef{id: c.ID, name: c.Name, networks: map[string]bool{}}, []string{"pg"})
	assert.Equal(t, []string{"paas-env-0e000000-0000-0000-0000-0000000000e2"}, f.NetworksOf(slotBlue))
	assert.Equal(t, []string{"pg"}, f.AliasesOn(slotBlue, "paas-env-0e000000-0000-0000-0000-0000000000e2"))
	require.Len(t, lines, 1)
	assert.Contains(t, lines[0], "paas-env-0e000000-0000-0000-0000-0000000000e3 exists but was not created by the panel")
}

func TestAttachProxyToManagedConnectsOnlyMissingNetworks(t *testing.T) {
	f, px := hostWithProxy(t)
	f.AddNetwork("paas-env-0e000000-0000-0000-0000-0000000000e1", netEnv.Labels())
	f.AddNetwork("paas-env-0e000000-0000-0000-0000-0000000000e2", netOther.Labels())
	f.AddNetwork("lookalike", map[string]string{netplan.LabelManaged: "true"})
	f.Attach(px, "paas-env-0e000000-0000-0000-0000-0000000000e1")
	n, err := ops(f, false).attachProxyToManaged(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, []string{"connect paas-env-0e000000-0000-0000-0000-0000000000e2 paas-traefik"}, f.RecordedOps())
}

func TestCollectGarbageRemovesOnlyEmptyUnusedNetworks(t *testing.T) {
	f, px := hostWithProxy(t)
	f.AddNetwork("paas-env-0e000000-0000-0000-0000-0000000000e1", netEnv.Labels()) // a plan's home
	f.AddNetwork("paas-env-0e000000-0000-0000-0000-0000000000e2", netOther.Labels())
	f.Attach(px, "paas-env-0e000000-0000-0000-0000-0000000000e2")
	busy := netplan.Env("0e000000-0000-0000-0000-0000000000e3", "0a000000-0000-0000-0000-0000000000a1", "0b000000-0000-0000-0000-0000000000b1")
	f.AddNetwork(busy.Name, busy.Labels())
	c := f.Add("someone", container.StateRunning, nil)
	f.Attach(c, busy.Name)
	f.AddNetwork("my-net", nil)

	o := ops(f, false)
	require.NoError(t, o.collectGarbage(context.Background(), []netplan.Plan{envPlan()}))
	assert.Equal(t, []string{"disconnect paas-env-0e000000-0000-0000-0000-0000000000e2 paas-traefik", "netremove paas-env-0e000000-0000-0000-0000-0000000000e2"}, f.RecordedOps())
	assert.Equal(t, []string{"paas-env-0e000000-0000-0000-0000-0000000000e2"}, o.report.Removed)
}

// migrationHost is a host before isolation: every app container on the
// control plane's network.
func migrationHost(t *testing.T) (*fakedocker.Docker, []netplan.Plan) {
	f, _ := hostWithProxy(t)
	for _, name := range []string{"paas-backend", "paas-postgres"} {
		f.Attach(f.Add(name, container.StateRunning, nil), "paas-network")
	}
	f.Attach(f.Add(slotBlue, container.StateRunning, svcLabels(slotSvcID)), "paas-network", "web")

	stackID := "bbbb2222-0000-0000-0000-000000000004"
	proj := ComposeProjectName(stackID, "stack")
	f.Attach(f.Add(proj+"-api-1", container.StateRunning, map[string]string{LabelComposeProject: proj, "com.docker.compose.service": "api"}), "paas-network")

	orphanID := "dddd4444-0000-0000-0000-000000000005"
	f.Attach(f.Add(BlueContainerName(orphanID, "gone", ""), container.StateRunning, svcLabels(orphanID)), "paas-network")
	f.Attach(f.Add("evil-1", container.StateRunning, svcLabels(slotSvcID)), "paas-network")
	f.Attach(f.Add("grafana-by-hand", container.StateRunning, nil), "paas-network")

	f.Attach(f.Add(BlueContainerName("ffff6666-0000-0000-0000-000000000007", "noplan", ""), container.StateRunning,
		svcLabels("ffff6666-0000-0000-0000-000000000007")), "paas-network")
	f.AddNetwork("paas-internal", nil)
	f.Attach(f.Add("paas-postgres-sidecar-by-atlas", container.StateRunning, nil), "paas-internal")
	return f, []netplan.Plan{
		envPlan(),
		{ServiceID: stackID, ServiceName: "stack", Compose: true, Home: netEnv},
		{ServiceID: "eeee5555-0000-0000-0000-000000000006", ServiceName: "idle", Home: netOther},
	}
}

// hostIDs are the services of a migrationHost: the planned ones and
// "noplan", whose plan could not be built.
func hostIDs(plans []netplan.Plan) []string {
	ids := []string{"ffff6666-0000-0000-0000-000000000007"}
	for _, p := range plans {
		ids = append(ids, p.ServiceID)
	}
	return ids
}

func TestReconcileHostIsolate(t *testing.T) {
	f, plans := migrationHost(t)
	report, err := ReconcileHost(context.Background(), f.Client(), NetConfig{}, plans, hostIDs(plans), netplan.MigrationIsolate, nil)
	require.NoError(t, err)

	assert.Equal(t, []string{"paas-env-0e000000-0000-0000-0000-0000000000e1"}, report.Created, "a service with nothing running gets no network yet")
	assert.Equal(t, []string{"paas-env-0e000000-0000-0000-0000-0000000000e1"}, f.NetworksOf(slotBlue))
	assert.Equal(t, []string{"web"}, f.AliasesOn(slotBlue, "paas-env-0e000000-0000-0000-0000-0000000000e1"))
	stackAPI := ComposeProjectName("bbbb2222-0000-0000-0000-000000000004", "stack") + "-api-1"
	assert.Equal(t, []string{"paas-env-0e000000-0000-0000-0000-0000000000e1"}, f.NetworksOf(stackAPI))
	assert.ElementsMatch(t, []string{"paas-env-0e000000-0000-0000-0000-0000000000e1", "paas-network"}, f.NetworksOf("paas-traefik"), "the proxy is never detached")
	for _, name := range []string{"paas-backend", "paas-postgres", "evil-1", "grafana-by-hand"} {
		assert.Equal(t, []string{"paas-network"}, f.NetworksOf(name), name)
	}
	orphan := BlueContainerName("dddd4444-0000-0000-0000-000000000005", "gone", "")
	assert.Equal(t, []string{"paas-network"}, f.NetworksOf(orphan), "an orphan is never cut off from every network")
	require.Len(t, report.Orphans, 1)
	assert.Contains(t, report.Orphans[0], orphan)
	noplan := BlueContainerName("ffff6666-0000-0000-0000-000000000007", "noplan", "")
	assert.Equal(t, []string{"paas-network"}, f.NetworksOf(noplan), "a service whose plan failed keeps its network")
	assert.Len(t, report.Unknown, 3, "evil-1, grafana-by-hand and the container on paas-internal")
	require.Len(t, report.Errors, 1)
	assert.Contains(t, report.Errors[0], noplan)
}

// One machine registered as two server entries (two workspaces before
// shared servers): reconciling one entry meets the other entry's live
// containers. They are not orphans (the report must never suggest docker rm
// -f for them), and they stay on their network for their own entry's
// reconcile.
func TestReconcileHostLeavesAnotherEntrysContainers(t *testing.T) {
	f, plans := migrationHost(t)
	otherEntry := "aaaa9999-0000-0000-0000-000000000009"
	theirs := BlueContainerName(otherEntry, "northwind-be", "prod")
	f.Attach(f.Add(theirs, container.StateRunning, svcLabels(otherEntry)), "paas-network")

	// Every service that exists: this host's and the other entry's.
	exists := append(hostIDs(plans), otherEntry)
	report, err := ReconcileHost(context.Background(), f.Client(), NetConfig{}, plans, exists, netplan.MigrationIsolate, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"paas-network"}, f.NetworksOf(theirs), "left where it is")
	for _, o := range report.Orphans {
		assert.NotContains(t, o, theirs, "a live service's container is not an orphan")
	}
	found := false
	for _, e := range report.Errors {
		if strings.Contains(e, theirs) && strings.Contains(e, "another entry for this machine") {
			found = true
		}
	}
	assert.True(t, found, "reported as another entry's: %v", report.Errors)
}

func TestReconcileHostReportChangesNothing(t *testing.T) {
	f, plans := migrationHost(t)
	report, err := ReconcileHost(context.Background(), f.Client(), NetConfig{}, plans, hostIDs(plans), netplan.MigrationReport, nil)
	require.NoError(t, err)
	assert.Empty(t, f.RecordedOps())
	assert.Equal(t, []string{"paas-env-0e000000-0000-0000-0000-0000000000e1"}, report.Created)
	assert.Len(t, report.Detached, 2, "the slot and the stack's routed container would be detached")
}

func TestReconcileHostAttachKeepsTheControlPlaneNetwork(t *testing.T) {
	f, plans := migrationHost(t)
	report, err := ReconcileHost(context.Background(), f.Client(), NetConfig{}, plans, hostIDs(plans), netplan.MigrationAttach, nil)
	require.NoError(t, err)
	assert.Empty(t, report.Detached)
	assert.ElementsMatch(t, []string{"paas-env-0e000000-0000-0000-0000-0000000000e1", "paas-network"}, f.NetworksOf(slotBlue))
	for _, op := range f.RecordedOps() {
		assert.False(t, strings.HasPrefix(op, "disconnect"), op)
	}
}

func TestReconcileHostLeavesAContainerWhoseHomeFailed(t *testing.T) {
	f, plans := migrationHost(t)
	f.AddNetwork("paas-env-0e000000-0000-0000-0000-0000000000e1", nil) // squatted: the home cannot be prepared
	report, err := ReconcileHost(context.Background(), f.Client(), NetConfig{}, plans, hostIDs(plans), netplan.MigrationIsolate, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"paas-network"}, f.NetworksOf(slotBlue), "never cut off from every network")
	assert.NotEmpty(t, report.Errors)
}

func TestReconcileHostOffDoesNothing(t *testing.T) {
	f, plans := migrationHost(t)
	_, err := ReconcileHost(context.Background(), f.Client(), NetConfig{}, plans, hostIDs(plans), netplan.MigrationOff, nil)
	require.NoError(t, err)
	assert.Empty(t, f.RecordedOps())
}

func TestCollectGarbageKeepsNewNetworksAndStoppedMembers(t *testing.T) {
	f, _ := hostWithProxy(t)
	fresh := f.AddNetwork("paas-env-0e000000-0000-0000-0000-0000000000e9", netplan.Env("0e000000-0000-0000-0000-0000000000e9", "", "").Labels())
	fresh.Created = time.Now() // a deploy just prepared it
	stoppedNet := netplan.Env("0e000000-0000-0000-0000-0000000000e8", "", "")
	f.AddNetwork(stoppedNet.Name, stoppedNet.Labels())
	f.Attach(f.Add("exited-app", container.StateExited, nil), stoppedNet.Name)

	o := ops(f, false)
	require.NoError(t, o.collectGarbage(context.Background(), nil))
	assert.Empty(t, f.RecordedOps())
}
