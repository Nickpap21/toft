package docker

import (
	"testing"

	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/rollout/fakedocker"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/netplan"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

var (
	netEnv   = netplan.Env("0e000000-0000-0000-0000-0000000000e1", "0a000000-0000-0000-0000-0000000000a1", "0b000000-0000-0000-0000-0000000000b1")
	netOther = netplan.Env("0e000000-0000-0000-0000-0000000000e2", "0a000000-0000-0000-0000-0000000000a1", "0b000000-0000-0000-0000-0000000000b1")
)

func envPlan() netplan.Plan {
	return netplan.Plan{ServiceID: slotSvcID, ServiceName: "web", Alias: "web", Home: netEnv}
}

// hostWithProxy is a fake host with the control plane's network and the
// proxy on it.
func hostWithProxy(t *testing.T) (*fakedocker.Docker, *fakedocker.Container) {
	f := fakedocker.New(t)
	f.AddNetwork("paas-network", nil)
	px := f.Add("paas-traefik", container.StateRunning, nil)
	f.Attach(px, "paas-network")
	return f, px
}

func TestRunContainerBlueGreenIsolated(t *testing.T) {
	f, _ := hostWithProxy(t)
	f.AddNetwork("paas-env-0e000000-0000-0000-0000-0000000000e1", netEnv.Labels())
	f.AddNetwork("paas-env-0e000000-0000-0000-0000-0000000000e2", netOther.Labels())
	old := f.Add(slotBlue, container.StateRunning, svcLabels(slotSvcID))
	f.Attach(old, "paas-env-0e000000-0000-0000-0000-0000000000e1", "pg")

	plan := netplan.Plan{ServiceID: slotSvcID, ServiceName: "web", Alias: "pg", Home: netplan.Project("0a000000-0000-0000-0000-0000000000a1", "0b000000-0000-0000-0000-0000000000b1"), Publish: []netplan.Network{netEnv, netOther}}
	f.AddNetwork(plan.Home.Name, plan.Home.Labels())
	err := runContainerBlueGreen(nil, f.Client(), rollout.NetConfig{}, plan, slotSvcID, "web", "", "app:2",
		[]string{"web.example.com"}, nil, 3000, "", "", nil, "", "", service.AccessControl{}, rollout.Security{}, nil)
	require.NoError(t, err)

	assert.Equal(t, []string{"paas-env-0e000000-0000-0000-0000-0000000000e1", "paas-env-0e000000-0000-0000-0000-0000000000e2", "paas-proj-0a000000-0000-0000-0000-0000000000a1"}, f.NetworksOf(slotGreen))
	assert.Equal(t, []string{"pg"}, f.AliasesOn(slotGreen, "paas-proj-0a000000-0000-0000-0000-0000000000a1"))
	labels := f.LabelsOf(slotGreen)
	assert.Equal(t, "paas-proj-0a000000-0000-0000-0000-0000000000a1", labels[netplan.LabelContainerNetwork])
	assert.Equal(t, "paas-proj-0a000000-0000-0000-0000-0000000000a1", labels["traefik.docker.network"])

	// Published before the previous slot is retired.
	got := f.RecordedOps()
	idx := func(op string) int {
		for i, o := range got {
			if o == op {
				return i
			}
		}
		return -1
	}
	require.NotEqual(t, -1, idx("connect paas-env-0e000000-0000-0000-0000-0000000000e2 "+slotGreen))
	assert.Less(t, idx("connect paas-env-0e000000-0000-0000-0000-0000000000e2 "+slotGreen), idx("remove "+slotBlue))
}

func TestRunContainerBlueGreenLegacyKeepsTheOldNetworking(t *testing.T) {
	f := fakedocker.New(t)
	require.NoError(t, runContainerBlueGreen(nil, f.Client(), rollout.NetConfig{}, netplan.LegacyPlan(slotSvcID, "web", "web", "cp-net"),
		slotSvcID, "web", "", "app:1", []string{"web.example.com"}, nil, 3000, "", "", nil, "", "", service.AccessControl{}, rollout.Security{}, nil))
	assert.Equal(t, []string{"cp-net"}, f.NetworksOf(slotBlue))
	assert.Equal(t, []string{"web"}, f.AliasesOn(slotBlue, "cp-net"))
	labels := f.LabelsOf(slotBlue)
	assert.NotContains(t, labels, netplan.LabelContainerNetwork)
	assert.NotContains(t, labels, "traefik.docker.network")
}

func TestRunContainerBlueGreenRefusesAnotherServicesPlan(t *testing.T) {
	f := fakedocker.New(t)
	plan := envPlan()
	plan.ServiceID = otherSvc
	assert.Error(t, runContainerBlueGreen(nil, f.Client(), rollout.NetConfig{}, plan, slotSvcID, "web", "", "app:1",
		nil, nil, 0, "", "", nil, "", "", service.AccessControl{}, rollout.Security{}, nil))
	assert.Error(t, runContainerBlueGreen(nil, f.Client(), rollout.NetConfig{}, netplan.Plan{}, slotSvcID, "web", "", "app:1",
		nil, nil, 0, "", "", nil, "", "", service.AccessControl{}, rollout.Security{}, nil))
}

func TestComposeOverrides(t *testing.T) {
	route := testRoute("web")
	legacy, err := composeOverrides(testServiceID, rollout.NetConfig{}, route, deployment.ComposeNetworks{Home: "paas-network"}, nil)
	require.NoError(t, err)
	require.Len(t, legacy, 1)
	assert.Equal(t, "docker-compose.route.yml", legacy[0].name)
	assert.Contains(t, string(legacy[0].content), "name: paas-network")

	netOverride := []byte("networks: {}\n")
	iso, err := composeOverrides(testServiceID, rollout.NetConfig{}, route, deployment.ComposeNetworks{
		Isolated: true, Home: "paas-env-0e000000-0000-0000-0000-0000000000e1", Override: netOverride, RouteKey: "front",
	}, nil)
	require.NoError(t, err)
	require.Len(t, iso, 2)
	assert.Equal(t, "docker-compose.networks.yml", iso[0].name, "the network override comes first")
	var doc struct {
		Services map[string]struct {
			Labels   map[string]string   `yaml:"labels"`
			Networks map[string]struct{} `yaml:"networks"`
		} `yaml:"services"`
		Networks map[string]struct {
			External bool   `yaml:"external"`
			Name     string `yaml:"name"`
		} `yaml:"networks"`
	}
	require.NoError(t, yaml.Unmarshal(iso[1].content, &doc))
	assert.Contains(t, doc.Services["web"].Networks, "front", "the routed service uses the stack's key for its environment network")
	assert.NotContains(t, doc.Services["web"].Networks, "paas-network")
	assert.Equal(t, "paas-env-0e000000-0000-0000-0000-0000000000e1", doc.Networks["front"].Name)
	assert.Equal(t, "paas-env-0e000000-0000-0000-0000-0000000000e1", doc.Services["web"].Labels["traefik.docker.network"])

	_, err = composeOverrides(testServiceID, rollout.NetConfig{}, route, deployment.ComposeNetworks{Isolated: true, Home: "paas-network"}, nil)
	assert.Error(t, err, "isolated compose never joins the control plane's network")

	none, err := composeOverrides(testServiceID, rollout.NetConfig{}, nil, deployment.ComposeNetworks{Isolated: true, Home: "paas-env-0e000000-0000-0000-0000-0000000000e1"}, nil)
	require.NoError(t, err)
	assert.Empty(t, none)
}

// On the panel's server every container of a stack joins the apps' slice
// (D-26, D-62); elsewhere no slice override is written.
func TestComposeOverridesSlice(t *testing.T) {
	nets := deployment.ComposeNetworks{Isolated: true, Home: "paas-env-0e000000-0000-0000-0000-0000000000e1", Services: []string{"web", "worker"}}
	slice := func(out []overrideFile) []byte {
		for _, f := range out {
			if f.name == "docker-compose.slice.yml" {
				return f.content
			}
		}
		return nil
	}
	out, err := composeOverrides(testServiceID, rollout.NetConfig{AppsSlice: "toft-apps.slice"}, nil, nets, nil)
	require.NoError(t, err)
	var doc struct {
		Services map[string]struct {
			CgroupParent string `yaml:"cgroup_parent"`
		} `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal(slice(out), &doc))
	assert.Equal(t, "toft-apps.slice", doc.Services["web"].CgroupParent)
	assert.Equal(t, "toft-apps.slice", doc.Services["worker"].CgroupParent)

	out, err = composeOverrides(testServiceID, rollout.NetConfig{}, nil, nets, nil)
	require.NoError(t, err)
	assert.Nil(t, slice(out))
}

// On an agent server every container of a stack logs to Docker's local
// driver, capped (D-113); elsewhere no logging override is written.
func TestComposeOverridesLogging(t *testing.T) {
	nets := deployment.ComposeNetworks{Isolated: true, Home: "paas-env-0e000000-0000-0000-0000-0000000000e1", Services: []string{"web", "worker"}}
	logging := func(out []overrideFile) []byte {
		for _, f := range out {
			if f.name == "docker-compose.logging.yml" {
				return f.content
			}
		}
		return nil
	}
	out, err := composeOverrides(testServiceID, rollout.NetConfig{LocalLogs: true}, nil, nets, nil)
	require.NoError(t, err)
	var doc struct {
		Services map[string]struct {
			Logging struct {
				Driver  string            `yaml:"driver"`
				Options map[string]string `yaml:"options"`
			} `yaml:"logging"`
		} `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal(logging(out), &doc))
	for _, svc := range []string{"web", "worker"} {
		assert.Equal(t, "local", doc.Services[svc].Logging.Driver)
		assert.Equal(t, map[string]string{"max-size": "20m", "max-file": "5"}, doc.Services[svc].Logging.Options)
	}

	out, err = composeOverrides(testServiceID, rollout.NetConfig{}, nil, nets, nil)
	require.NoError(t, err)
	assert.Nil(t, logging(out))
}

func TestComposeGuardSwitchesOffRoutingOnUnroutedServices(t *testing.T) {
	out, err := composeOverrides(testServiceID, rollout.NetConfig{}, testRoute("web"), deployment.ComposeNetworks{
		Isolated: true, Home: netEnv.Name, Services: []string{"db", "web", "worker"},
	}, nil)
	require.NoError(t, err)
	require.Len(t, out, 3) // the guard, the route and the hardened runtime
	assert.Equal(t, "docker-compose.guard.yml", out[0].name)
	var doc struct {
		Services map[string]struct {
			Labels map[string]string `yaml:"labels"`
		} `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal(out[0].content, &doc))
	assert.Equal(t, "false", doc.Services["db"].Labels["traefik.enable"])
	assert.Equal(t, "false", doc.Services["worker"].Labels["traefik.enable"])
	assert.NotContains(t, doc.Services, "web", "the routed service is routed by the platform")

	// Legacy: every container is on the control plane's network, which
	// Traefik joins, so the guard applies there too.
	legacy, err := composeOverrides(testServiceID, rollout.NetConfig{}, testRoute("web"), deployment.ComposeNetworks{
		Home: "paas-network", Services: []string{"db", "web"},
	}, nil)
	require.NoError(t, err)
	require.Len(t, legacy, 3)
	assert.Equal(t, "docker-compose.guard.yml", legacy[0].name)
	doc.Services = nil
	require.NoError(t, yaml.Unmarshal(legacy[0].content, &doc))
	assert.Equal(t, "false", doc.Services["db"].Labels["traefik.enable"])
	assert.NotContains(t, doc.Services, "web")
}

func TestStackRouteViolations(t *testing.T) {
	proj := rollout.ComposeProjectName(testServiceID, "stack")
	route := testRoute("web")
	allowed := routeLabelKeys(testServiceID, netEnv.Name, route)
	own := composeRouteLabels(testServiceID, netEnv.Name, route)
	web := map[string]string{rollout.LabelComposeProject: proj, "com.docker.compose.service": "web"}
	for k, v := range own {
		web[k] = v
	}
	db := map[string]string{rollout.LabelComposeProject: proj, "com.docker.compose.service": "db", "traefik.enable": "false",
		"traefik.http.routers.x.rule": "Host(`panel.example.com`)"}
	list := []container.Summary{
		{Names: []string{"/" + proj + "-web-1"}, Labels: web},
		{Names: []string{"/" + proj + "-db-1"}, Labels: db},
	}
	assert.Empty(t, rollout.StackRouteViolations(list, proj, route.Service, allowed),
		"the platform's own labels pass, and a disabled service's image routers are inert")

	web["traefik.http.routers.evil.rule"] = "Host(`panel.example.com`)"
	web["traefik.http.routers.evil.service"] = "api@internal"
	got := rollout.StackRouteViolations(list, proj, route.Service, allowed)
	assert.Equal(t, []string{proj + "-web-1: traefik.http.routers.evil.rule", proj + "-web-1: traefik.http.routers.evil.service"}, got)

	db["traefik.enable"] = "true"
	assert.Contains(t, rollout.StackRouteViolations(list, proj, route.Service, allowed), proj+"-db-1: traefik.enable")
}

func TestRunContainerRefusesARoutedImageWithTraefikLabels(t *testing.T) {
	f, _ := hostWithProxy(t)
	f.AddNetwork(netEnv.Name, netEnv.Labels())
	f.SetImageLabels("evil:1", map[string]string{"traefik.http.routers.x.rule": "Host(`panel.example.com`)"})
	err := runContainerBlueGreen(nil, f.Client(), rollout.NetConfig{}, envPlan(), slotSvcID, "web", "", "evil:1",
		[]string{"web.example.com"}, nil, 3000, "", "", nil, "", "", service.AccessControl{}, rollout.Security{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "traefik.http.routers.x.rule")

	// Unrouted (no domains): traefik.enable=false makes them inert.
	require.NoError(t, runContainerBlueGreen(nil, f.Client(), rollout.NetConfig{}, envPlan(), slotSvcID, "web", "", "evil:1",
		nil, nil, 0, "", "", nil, "", "", service.AccessControl{}, rollout.Security{}, nil))
	// Legacy too: Traefik joins the control plane's network, which every
	// legacy app is on.
	f2 := fakedocker.New(t)
	f2.SetImageLabels("evil:1", map[string]string{"traefik.http.routers.x.rule": "Host(`x`)"})
	err = runContainerBlueGreen(nil, f2.Client(), rollout.NetConfig{}, netplan.LegacyPlan(slotSvcID, "web", "", ""), slotSvcID, "web", "", "evil:1",
		[]string{"web.example.com"}, nil, 3000, "", "", nil, "", "", service.AccessControl{}, rollout.Security{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "traefik.http.routers.x.rule")
	require.NoError(t, runContainerBlueGreen(nil, f2.Client(), rollout.NetConfig{}, netplan.LegacyPlan(slotSvcID, "web", "", ""), slotSvcID, "web", "", "evil:1",
		nil, nil, 0, "", "", nil, "", "", service.AccessControl{}, rollout.Security{}, nil), "unrouted legacy containers get traefik.enable=false")
}

// TestComposeSecurityOverride: every service of a stack gets the hardened
// runtime (D-93); a net_raw exception keeps raw sockets.
func TestComposeSecurityOverride(t *testing.T) {
	type svc struct {
		SecurityOpt []string `yaml:"security_opt"`
		CapDrop     []string `yaml:"cap_drop"`
		PidsLimit   int64    `yaml:"pids_limit"`
		Runtime     string   `yaml:"runtime"`
		Volumes     []string `yaml:"volumes"`
		Deploy      struct {
			Resources struct {
				Limits map[string]int64 `yaml:"limits"`
			} `yaml:"resources"`
		} `yaml:"deploy"`
	}
	var doc struct {
		Services map[string]svc `yaml:"services"`
	}
	// db's file declares deploy.resources.limits: the process limit goes
	// there too, or compose refuses the two different values.
	out, err := composeSecurityOverride([]string{"db", "web"}, map[string]deployment.ComposeLimit{"db": {DeployLimits: true}, "web": {MemoryMB: 64}}, rollout.Security{})
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(out, &doc))
	for _, name := range []string{"db", "web"} {
		got := doc.Services[name]
		assert.Equal(t, []string{"no-new-privileges:true"}, got.SecurityOpt, name)
		assert.Equal(t, []string{"NET_RAW"}, got.CapDrop, name)
		assert.Equal(t, int64(2048), got.PidsLimit, name)
	}
	assert.Equal(t, map[string]int64{"pids": 2048}, doc.Services["db"].Deploy.Resources.Limits)
	assert.Nil(t, doc.Services["web"].Deploy.Resources.Limits, "no block the file lacks: compose would compare it with the file's cpus or mem_limit")

	out, err = composeSecurityOverride([]string{"web"}, nil, rollout.Security{PidsLimit: 512, NetRaw: true})
	require.NoError(t, err)
	doc.Services = nil
	require.NoError(t, yaml.Unmarshal(out, &doc))
	assert.Nil(t, doc.Services["web"].CapDrop)
	assert.Equal(t, int64(512), doc.Services["web"].PidsLimit)
	assert.Empty(t, doc.Services["web"].Runtime, "Standard sets no runtime")

	out, err = composeSecurityOverride([]string{"db", "web"}, nil, rollout.Security{Runtime: rollout.RuntimeSandboxed})
	require.NoError(t, err)
	doc.Services = nil
	require.NoError(t, yaml.Unmarshal(out, &doc))
	for _, name := range []string{"db", "web"} {
		assert.Equal(t, "runsc", doc.Services[name].Runtime, "every service of a Sandboxed stack runs under gVisor (D-95)")
		assert.Equal(t, []string{"NET_RAW"}, doc.Services[name].CapDrop, name)
	}
	assert.Empty(t, doc.Services["web"].Volumes, "Sandboxed keeps Docker's resolv.conf")

	out, err = composeSecurityOverride([]string{"web"}, nil, rollout.Security{Runtime: rollout.RuntimeStrict})
	require.NoError(t, err)
	doc.Services = nil
	require.NoError(t, yaml.Unmarshal(out, &doc))
	assert.Equal(t, "runsc-strict", doc.Services["web"].Runtime)
	assert.Equal(t, []string{"/etc/toft-agent/resolv.conf:/etc/resolv.conf:ro"}, doc.Services["web"].Volumes, "names come from the agent's relay (D-115)")
	out, err = composeSecurityOverride(nil, nil, rollout.Security{})
	require.NoError(t, err)
	assert.Nil(t, out, "no service list, no override (as the limits)")
}
