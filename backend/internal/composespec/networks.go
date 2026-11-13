package composespec

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"gopkg.in/yaml.v3"
)

// AppNetworkPolicy is how compose stacks may use networks.
type AppNetworkPolicy struct {
	// Isolated is TOFT_APP_NETWORKS=isolated: CheckAppNetworks applies.
	Isolated bool
	// Legacy are the control plane's network names (paas-network and
	// TRAEFIK_NETWORK). Under isolation an external network with one of
	// these names is the stack's way of saying "my environment's network":
	// the deploy points it at that network instead (NetworkOverride).
	Legacy []string
}

// NetworkOverrideFile is the name of the platform's network override.
const NetworkOverrideFile = "docker-compose.networks.yml"

// reservedNetworkName reports whether a network name or key belongs to the
// platform (paas-* or toft-*).
func reservedNetworkName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return strings.HasPrefix(n, "paas-") || strings.HasPrefix(n, "toft-")
}

func isLegacy(name string, legacy []string) bool {
	for _, l := range legacy {
		if l != "" && name == l {
			return true
		}
	}
	return false
}

// allowedNetworkDriverOpts are the bridge options a stack network may set.
// Everything else can change how the bridge is built, down to reusing an
// existing bridge interface (com.docker.network.bridge.name), which would
// put the stack on another network's segment.
var allowedNetworkDriverOpts = map[string]bool{"com.docker.network.driver.mtu": true}

// CheckAppNetworks is the isolated-mode network rule (RuleAppNetwork, never
// grantable): a stack's top-level networks must be its own (named
// <project>_<key>, bridge driver, default IPAM, no driver options but the
// MTU, no fixed subnets, no key starting with paas- or toft-), or external
// networks named like the control plane's network (legacy), which the
// deploy points at the environment's network. A service's network_mode may
// only be none or service:<name> (any other value names a network Docker
// would join), and builds may only use the default network or none.
func CheckAppNetworks(p *types.Project, legacy []string) []Violation {
	if p == nil {
		return nil
	}
	var out []Violation
	add := func(service, field, format string, args ...any) {
		out = append(out, Violation{Rule: RuleAppNetwork, Service: service, Field: field, Message: fmt.Sprintf(format, args...)})
	}
	for _, key := range sortedKeys(p.Networks) {
		nw := p.Networks[key]
		field := "networks." + key
		name := nw.Name
		if name == "" {
			name = key
		}
		if reservedNetworkName(key) && !(bool(nw.External) && isLegacy(name, legacy)) {
			add("", field, "network keys starting with paas- or toft- are reserved for the platform")
			continue
		}
		if bool(nw.External) {
			if !isLegacy(name, legacy) {
				add("", field+".external", "external network %q is not allowed: a stack joins only its own networks and, by naming %s, its environment's network", name, strings.Join(nonEmpty(legacy), " or "))
			}
			continue
		}
		if own := p.Name + "_" + key; name != own {
			add("", field+".name", "network name %q is outside this project (it would be %q); an explicit name can join an existing network", name, own)
		}
		if d := strings.ToLower(nw.Driver); d != "" && d != "bridge" {
			add("", field+".driver", "network driver %q is not allowed; stack networks are bridges", nw.Driver)
		}
		for _, opt := range sortedKeys(nw.DriverOpts) {
			if !allowedNetworkDriverOpts[opt] {
				add("", field+".driver_opts."+opt, "network driver option %q is not allowed", opt)
			}
		}
		if nw.Ipam.Driver != "" && nw.Ipam.Driver != "default" {
			add("", field+".ipam.driver", "IPAM driver %q is not allowed", nw.Ipam.Driver)
		}
		if len(nw.Ipam.Config) > 0 {
			add("", field+".ipam.config", "fixed subnets are not allowed; Docker assigns them from the app address pool")
		}
	}
	for _, name := range sortedServiceNames(p) {
		svc, ok := p.Services[name]
		if !ok {
			svc = p.DisabledServices[name]
		}
		for field, val := range map[string]string{"network_mode": svc.NetworkMode, "net": svc.Net} {
			// Any other value is passed to Docker as a network name: the
			// default bridge, the control plane's networks, another stack's.
			v := strings.TrimSpace(val)
			switch {
			case v == "", strings.EqualFold(v, "none"), strings.HasPrefix(v, types.ServicePrefix):
			default:
				add(name, "services."+name+"."+field, "%s %q is not allowed; a service may use only the stack's own networks, none, or service:<name>", field, val)
			}
		}
		if svc.Build != nil {
			switch n := strings.ToLower(strings.TrimSpace(svc.Build.Network)); n {
			case "", "default", "none", "host":
				// host is refused by Check (network_mode_host).
			default:
				add(name, "services."+name+".build.network", "builds may use only the default network or none, not %q", svc.Build.Network)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Field < out[j].Field })
	return out
}

func nonEmpty(vals []string) []string {
	var out []string
	for _, v := range vals {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// LegacyNetworkKeys returns, sorted, the keys of p's external networks
// named like the control plane's network (legacy): the ones the deploy
// points at the environment's network under isolation.
func LegacyNetworkKeys(p *types.Project, legacy []string) []string {
	if p == nil {
		return nil
	}
	var keys []string
	for _, key := range sortedKeys(p.Networks) {
		nw := p.Networks[key]
		name := nw.Name
		if name == "" {
			name = key
		}
		if bool(nw.External) && isLegacy(name, legacy) {
			keys = append(keys, key)
		}
	}
	return keys
}

type networkOverride struct {
	Networks map[string]overrideNetwork `yaml:"networks"`
}

type overrideNetwork struct {
	External bool   `yaml:"external"`
	Name     string `yaml:"name"`
}

// NetworkOverride returns the override file that points every network in
// keys at home, the environment's network. It is generated with a YAML
// marshaller from typed data; keys come from the loaded model (compose does
// not interpolate keys) and home from netplan.
func NetworkOverride(keys []string, home string) ([]byte, error) {
	if home == "" || strings.ContainsAny(home, "$ \t\n") {
		return nil, fmt.Errorf("invalid home network %q", home)
	}
	nets := make(map[string]overrideNetwork, len(keys))
	for _, k := range keys {
		nets[k] = overrideNetwork{External: true, Name: home}
	}
	out, err := yaml.Marshal(networkOverride{Networks: nets})
	if err != nil {
		return nil, fmt.Errorf("marshal network override: %w", err)
	}
	return append([]byte("# Generated by the platform: the stack's environment network (TOFT_APP_NETWORKS=isolated).\n"), out...), nil
}

// IsolateNetworks prepares an isolated deploy of the compose file in src
// (without overrides of its own): it loads the file, finds the external
// networks named like the control plane's network (legacy), generates the
// override that points them at home, and loads the file again with that
// override, exactly as docker compose will combine them, to re-check the
// result. The re-check refuses anything the network rule refuses
// (CheckAppNetworks with no legacy names left), and any external network
// that does not end up named home.
//
// The override is nil when the file names no legacy network. A
// non-nil error means the file could not be loaded; violations mean the
// deploy must not go ahead.
//
// services are the file's services, every profile included: the deploy
// switches routing off on all but the routed one.
func IsolateNetworks(ctx context.Context, src Source, home string, legacy []string) (override []byte, keys, services []string, violations []Violation, err error) {
	src.Overrides = nil
	project, vs, err := Load(ctx, src)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if project == nil {
		return nil, nil, nil, vs, nil
	}
	if vs := CheckAppNetworks(project, legacy); len(vs) > 0 {
		return nil, nil, nil, vs, nil
	}
	services = sortedServiceNames(project)
	keys = LegacyNetworkKeys(project, legacy)
	if len(keys) > 0 {
		if override, err = NetworkOverride(keys, home); err != nil {
			return nil, nil, nil, nil, err
		}
		src.Overrides = []Override{{Name: NetworkOverrideFile, Content: override}}
		project, vs, err = Load(ctx, src)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		if project == nil {
			return nil, nil, nil, vs, nil
		}
	}
	// The result: own networks, and external networks named home only.
	vs = CheckAppNetworks(project, []string{home})
	for _, key := range sortedKeys(project.Networks) {
		nw := project.Networks[key]
		if bool(nw.External) && nw.Name != home {
			vs = append(vs, Violation{Rule: RuleAppNetwork, Field: "networks." + key,
				Message: fmt.Sprintf("network %q would not be the environment's network after the rewrite", nw.Name)})
		}
	}
	if len(vs) > 0 {
		return nil, nil, nil, vs, nil
	}
	return override, keys, services, nil, nil
}
