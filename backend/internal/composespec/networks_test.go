package composespec_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	netHome    = "paas-env-0f0e0d0c-0000-0000-0000-000000000001"
	netProject = "paas-web-1234abcd"
)

var netLegacy = []string{"paas-network", "cp-net"}

func inlineSrc(content string) composespec.Source {
	return composespec.Source{Root: "/nonexistent/toft-test", Content: content, ProjectName: netProject}
}

func isolatedViolations(t *testing.T, content string) []composespec.Violation {
	t.Helper()
	src := inlineSrc(content)
	src.AppNetworks = composespec.AppNetworkPolicy{Isolated: true, Legacy: netLegacy}
	vs, _ := composespec.Inspect(context.Background(), src)
	var out []composespec.Violation
	for _, v := range vs {
		if v.Rule == composespec.RuleAppNetwork {
			out = append(out, v)
		}
	}
	return out
}

func TestAppNetworkRuleOnlyAppliesWhenIsolated(t *testing.T) {
	content := "services:\n  app:\n    image: nginx\n    networks: [shared]\nnetworks:\n  shared:\n    external: true\n    name: monitoring\n"
	vs, _ := composespec.Inspect(context.Background(), inlineSrc(content))
	assert.Empty(t, vs, "legacy keeps accepting external networks")
	assert.NotEmpty(t, isolatedViolations(t, content))
	assert.False(t, composespec.Grantable(composespec.RuleAppNetwork))
}

func TestAppNetworkRuleAllows(t *testing.T) {
	for name, content := range map[string]string{
		"no networks":           "services:\n  app:\n    image: nginx\n",
		"own network":           "services:\n  app:\n    image: nginx\n    networks: [back]\nnetworks:\n  back: {}\n",
		"own network mtu":       "services:\n  app:\n    image: nginx\n    networks: [back]\nnetworks:\n  back:\n    driver: bridge\n    driver_opts:\n      com.docker.network.driver.mtu: \"1400\"\n",
		"legacy by key":         "services:\n  app:\n    image: nginx\n    networks: [paas-network]\nnetworks:\n  paas-network:\n    external: true\n",
		"legacy by name":        "services:\n  app:\n    image: nginx\n    networks: [front]\nnetworks:\n  front:\n    external: true\n    name: paas-network\n",
		"custom traefik net":    "services:\n  app:\n    image: nginx\n    networks: [front]\nnetworks:\n  front:\n    external: true\n    name: cp-net\n",
		"network_mode none":     "services:\n  app:\n    image: nginx\n    network_mode: none\n",
		"network_mode service":  "services:\n  app:\n    image: nginx\n    network_mode: \"service:side\"\n  side:\n    image: alpine\n",
		"build default network": "services:\n  app:\n    build: {context: ., network: default}\n",
	} {
		t.Run(name, func(t *testing.T) {
			assert.Empty(t, isolatedViolations(t, content))
		})
	}
}

func TestAppNetworkRuleRefuses(t *testing.T) {
	cases := map[string]struct{ content, field string }{
		"other external":                  {"services:\n  app:\n    image: nginx\n    networks: [x]\nnetworks:\n  x:\n    external: true\n    name: monitoring\n", "networks.x.external"},
		"another env":                     {"services:\n  app:\n    image: nginx\n    networks: [x]\nnetworks:\n  x:\n    external: true\n    name: paas-env-other\n", "networks.x.external"},
		"external by key":                 {"services:\n  app:\n    image: nginx\n    networks: [paas-postgres]\nnetworks:\n  paas-postgres:\n    external: true\n", "networks.paas-postgres"},
		"explicit name":                   {"services:\n  app:\n    image: nginx\n    networks: [x]\nnetworks:\n  x:\n    name: paas-proj-123\n", "networks.x.name"},
		"explicit plain name":             {"services:\n  app:\n    image: nginx\n    networks: [x]\nnetworks:\n  x:\n    name: other-stack_default\n", "networks.x.name"},
		"reserved key":                    {"services:\n  app:\n    image: nginx\n    networks: [paas-x]\nnetworks:\n  paas-x: {}\n", "networks.paas-x"},
		"reused bridge":                   {"services:\n  app:\n    image: nginx\n    networks: [x]\nnetworks:\n  x:\n    driver_opts:\n      com.docker.network.bridge.name: docker0\n", "networks.x.driver_opts.com.docker.network.bridge.name"},
		"overlay":                         {"services:\n  app:\n    image: nginx\n    networks: [x]\nnetworks:\n  x:\n    driver: overlay\n", "networks.x.driver"},
		"ipam driver":                     {"services:\n  app:\n    image: nginx\n    networks: [x]\nnetworks:\n  x:\n    ipam:\n      driver: dhcp\n", "networks.x.ipam.driver"},
		"default bridge":                  {"services:\n  app:\n    image: nginx\n    network_mode: bridge\n", "services.app.network_mode"},
		"build network":                   {"services:\n  app:\n    build: {context: ., network: paas-network}\n", "services.app.build.network"},
		"legacy external.name form other": {"services:\n  app:\n    image: nginx\n    networks: [x]\nnetworks:\n  x:\n    external:\n      name: monitoring\n", "networks.x.external"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			vs := isolatedViolations(t, tc.content)
			require.NotEmpty(t, vs)
			var fields []string
			for _, v := range vs {
				fields = append(fields, v.Field)
			}
			assert.Contains(t, fields, tc.field)
		})
	}
}

func TestIsolateNetworksRewritesTheLegacyNetwork(t *testing.T) {
	for name, content := range map[string]string{
		"by key":         "services:\n  app:\n    image: nginx\n    networks: [paas-network, back]\n  db:\n    image: postgres\n    networks: [back]\nnetworks:\n  paas-network:\n    external: true\n  back: {}\n",
		"by name":        "services:\n  app:\n    image: nginx\n    networks: [front]\nnetworks:\n  front:\n    external: true\n    name: paas-network\n",
		"external.name":  "services:\n  app:\n    image: nginx\n    networks: [front]\nnetworks:\n  front:\n    external:\n      name: paas-network\n",
		"custom name":    "services:\n  app:\n    image: nginx\n    networks: [front]\nnetworks:\n  front:\n    external: true\n    name: cp-net\n",
		"anchors/merges": "x-net: &net\n  external: true\n  name: paas-network\nservices:\n  app:\n    image: nginx\n    networks: [front]\nnetworks:\n  front:\n    <<: *net\n",
	} {
		t.Run(name, func(t *testing.T) {
			override, keys, services, vs, err := composespec.IsolateNetworks(context.Background(), inlineSrc(content), netHome, netLegacy)
			require.NoError(t, err)
			require.Empty(t, vs)
			require.Len(t, keys, 1)
			require.NotNil(t, override)
			assert.Contains(t, services, "app")
			assert.Contains(t, string(override), "name: "+netHome)

			// Loaded with the override, the network is the home network.
			src := inlineSrc(content)
			src.Overrides = []composespec.Override{{Name: composespec.NetworkOverrideFile, Content: override}}
			p, lvs, err := composespec.Load(context.Background(), src)
			require.NoError(t, err)
			require.Empty(t, lvs)
			nw := p.Networks[keys[0]]
			assert.True(t, bool(nw.External))
			assert.Equal(t, netHome, nw.Name)
		})
	}
}

func TestIsolateNetworksWithoutLegacyNetworkNeedsNoOverride(t *testing.T) {
	override, keys, _, vs, err := composespec.IsolateNetworks(context.Background(),
		inlineSrc("services:\n  app:\n    image: nginx\n"), netHome, netLegacy)
	require.NoError(t, err)
	assert.Empty(t, vs)
	assert.Empty(t, keys)
	assert.Nil(t, override)
}

func TestIsolateNetworksRefusesWhatTheRuleRefuses(t *testing.T) {
	_, _, _, vs, err := composespec.IsolateNetworks(context.Background(),
		inlineSrc("services:\n  app:\n    image: nginx\n    network_mode: bridge\n"), netHome, netLegacy)
	require.NoError(t, err)
	require.NotEmpty(t, vs)
	assert.Equal(t, composespec.RuleAppNetwork, vs[0].Rule)
}

func TestNetworkOverrideRejectsABadHome(t *testing.T) {
	for _, home := range []string{"", "a b", "${X}"} {
		_, err := composespec.NetworkOverride([]string{"k"}, home)
		assert.Error(t, err, home)
	}
	out, err := composespec.NetworkOverride([]string{"paas-network"}, netHome)
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(out), "external: true"))
}

func TestLoadRefusesAnOverrideOutsideTheProjectDirectory(t *testing.T) {
	src := inlineSrc("services:\n  app:\n    image: nginx\n")
	src.Overrides = []composespec.Override{{Name: "../x.yml", Content: []byte("services: {}\n")}}
	_, _, err := composespec.Load(context.Background(), src)
	assert.ErrorIs(t, err, composespec.ErrInvalid)
}
