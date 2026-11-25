package composespec_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// A stack never names a container as the platform names its own: its
// services' (paas-), its builders' (buildx_buildkit_) and the rest (toft).
func TestValidate_ReservedContainerNames(t *testing.T) {
	for name, refused := range map[string]bool{
		"paas-postgres":                         true,
		"buildx_buildkit_toft-build0":           true,
		"buildx_buildkit_toft-build-untrusted0": true,
		"BuildX_BuildKit_mine0":                 true,
		"toft-migrate-1":                        true,
		"toft_agent":                            true,
		"Toft":                                  true,
		"web":                                   false,
		"my-toft":                               false,
		"buildx":                                false,
		"buildkit":                              false,
		"web.1_x-y":                             false,
		// Docker drops one leading slash: the name it keeps is reserved.
		"/paas-traefik":                true,
		"/toft-migrate-1":              true,
		"/buildx_buildkit_toft-build0": true,
		"/web":                         true,
		"//paas-traefik":               true,
		" paas-traefik":                true,
		"paas-traefik ":                true,
		"web/paas-traefik":             true,
		"-web":                         true,
		"webé":                         true,
	} {
		vs := validateInline(t, fmt.Sprintf("services:\n  app:\n    image: alpine\n    container_name: %q\n", name), nil)
		if !refused {
			assert.Empty(t, vs, name)
			continue
		}
		if assert.Equal(t, []string{"reserved_name"}, rulesOf(vs), name) {
			assert.Equal(t, "services.app.container_name", vs[0].Field)
		}
	}
}

// Only a builder runs on a builder's network, in every network mode: a
// stack's container there would be held to a build's firewall rules and
// have the network the platform knows its builders by.
func TestValidate_BuilderNetworksAreReserved(t *testing.T) {
	for name, tc := range map[string]struct{ compose, field string }{
		"network_mode":           {"services:\n  app:\n    image: alpine\n    network_mode: toft-build\n", "services.app.network_mode"},
		"network_mode untrusted": {"services:\n  app:\n    image: alpine\n    network_mode: toft-build-untrusted\n", "services.app.network_mode"},
		"network_mode case":      {"services:\n  app:\n    image: alpine\n    network_mode: \" Toft-Build\"\n", "services.app.network_mode"},
		"external, by key":       {"services:\n  app:\n    image: alpine\n    networks: [toft-build]\nnetworks:\n  toft-build:\n    external: true\n", "networks.toft-build"},
		"external, by name":      {"services:\n  app:\n    image: alpine\n    networks: [b]\nnetworks:\n  b:\n    external: true\n    name: toft-build-untrusted\n", "networks.b"},
		"explicit name":          {"services:\n  app:\n    image: alpine\n    networks: [b]\nnetworks:\n  b:\n    name: toft-build\n", "networks.b"},
	} {
		vs := validateInline(t, tc.compose, nil)
		if assert.Equal(t, []string{"reserved_name"}, rulesOf(vs), name) {
			assert.Equal(t, tc.field, vs[0].Field, name)
		}
	}
	for name, compose := range map[string]string{
		"a word that is not all hex":   "services:\n  app:\n    image: alpine\n    networks: [b]\nnetworks:\n  b:\n    external: true\n    name: cafes\n",
		"hex shorter than an ID":       "services:\n  app:\n    image: alpine\n    networks: [b]\nnetworks:\n  b:\n    external: true\n    name: cafe\n",
		"a bridge name of its own":     "services:\n  app:\n    image: alpine\n    networks: [b]\nnetworks:\n  b:\n    driver_opts:\n      com.docker.network.bridge.name: my-toft-br\n",
		"a network of the stack's own": "services:\n  app:\n    image: alpine\n    networks: [toft-build]\nnetworks:\n  toft-build: {}\n",
		"another external network":     "services:\n  app:\n    image: alpine\n    networks: [b]\nnetworks:\n  b:\n    external: true\n    name: my-toft-build\n",
		"network_mode none":            "services:\n  app:\n    image: alpine\n    network_mode: none\n",
	} {
		assert.Empty(t, validateInline(t, compose, nil), name)
	}
}

// Docker takes a network's ID, or the start of one, wherever it takes a
// name, and a network can be put on a bridge by the bridge's name: neither
// is a way round the reserved names, in any network mode.
func TestValidate_NetworkIDsAndPlatformBridgesAreRefused(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789ABCDEF"
	for name, tc := range map[string]struct{ compose, field string }{
		"network_mode, an ID's start": {"services:\n  app:\n    image: alpine\n    network_mode: 3f9a\n", "services.app.network_mode"},
		"network_mode, one digit":     {"services:\n  app:\n    image: alpine\n    network_mode: \"7\"\n", "services.app.network_mode"},
		"network_mode, a whole ID":    {"services:\n  app:\n    image: alpine\n    network_mode: " + id + "\n", "services.app.network_mode"},
		"network_mode, spaced":        {"services:\n  app:\n    image: alpine\n    network_mode: \" CAFE \"\n", "services.app.network_mode"},
		"external, named by an ID":    {"services:\n  app:\n    image: alpine\n    networks: [b]\nnetworks:\n  b:\n    external: true\n    name: " + id + "\n", "networks.b"},
		"external, keyed by an ID":    {"services:\n  app:\n    image: alpine\n    networks: [" + id + "]\nnetworks:\n  " + id + ":\n    external: true\n", "networks." + id},
		"the builder's bridge":        {"services:\n  app:\n    image: alpine\n    networks: [b]\nnetworks:\n  b:\n    driver_opts:\n      com.docker.network.bridge.name: toft-ubuild\n", "networks.b.driver_opts.com.docker.network.bridge.name"},
		"a bridge of the platform's":  {"services:\n  app:\n    image: alpine\n    networks: [b]\nnetworks:\n  b:\n    driver_opts:\n      com.docker.network.bridge.name: \" Toft-x\"\n", "networks.b.driver_opts.com.docker.network.bridge.name"},
	} {
		vs := validateInline(t, tc.compose, nil)
		if assert.Equal(t, []string{"reserved_name"}, rulesOf(vs), name) {
			assert.Equal(t, tc.field, vs[0].Field, name)
		}
	}
}
