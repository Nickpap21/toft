package docker

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/netplan"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/docker/docker/client"
	"gopkg.in/yaml.v3"
)

// ─── The environment docker compose runs with ────────────────────────────────
//
// The deploy policy checks a compose file interpolated with the service's env
// vars and nothing else of the platform's (composespec.IsPlatformEnv): a file
// that reads PATH, HOME, DOCKER_HOST or XDG_RUNTIME_DIR is refused. Every
// docker compose invocation must therefore run with exactly those four
// platform variables plus the service's env vars, locally and over SSH, or
// compose would interpolate a different file from the one that was checked
// (a remote SSH session adds USER, SHELL, LANG, SSH_CONNECTION and whatever
// the remote profile exports).

// logDroppedEnv says which env vars were not handed to docker compose.
func logDroppedEnv(dropped []string, logFn func(string)) {
	if len(dropped) == 0 {
		return
	}
	logFn(fmt.Sprintf("⚠ not passed to docker compose (names the docker CLI reserves, or invalid names): %s",
		strings.Join(dropped, ", ")))
}

// composeProcessEnv returns the environment of a local docker compose
// process: composespec.RunEnv from getenv (each only when set) and then the
// service's env vars (already filtered by composespec.ServiceEnv), sorted by
// name. Nothing else of the backend's environment (its database URL, JWT
// secret, encryption key, ...) reaches compose.
func composeProcessEnv(getenv func(string) string, envVars map[string]string) []string {
	env := make([]string, 0, len(composespec.RunEnv)+len(envVars))
	for _, key := range composespec.RunEnv {
		if val := getenv(key); val != "" {
			env = append(env, key+"="+val)
		}
	}
	keys := make([]string, 0, len(envVars))
	for k := range envVars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+envVars[k])
	}
	return env
}

// localComposeEnv is composeProcessEnv with the backend's own environment.
func localComposeEnv(envVars map[string]string) []string {
	return composeProcessEnv(os.Getenv, envVars)
}

// remoteCleanEnvPrefix runs the rest of a remote shell command with only
// PATH and HOME from the SSH session, plus DOCKER_HOST and XDG_RUNTIME_DIR
// when they are set and not empty. The quotes inside ${VAR:+"..."} keep a
// value with spaces in one word.
const remoteCleanEnvPrefix = `env -i PATH="$PATH" HOME="$HOME" ` +
	`${DOCKER_HOST:+"DOCKER_HOST=$DOCKER_HOST"} ` +
	`${XDG_RUNTIME_DIR:+"XDG_RUNTIME_DIR=$XDG_RUNTIME_DIR"} `

// remoteCleanEnv returns the remote shell command that runs command in the
// clean environment of remoteCleanEnvPrefix.
func remoteCleanEnv(command string) string {
	return remoteCleanEnvPrefix + command
}

// remoteCompose returns the remote shell command that runs
// `docker compose <args>` in a clean environment. Every docker compose
// command a RemoteOrchestrator runs goes through it. The service's env vars
// reach compose through the project's .env file, which the platform writes.
func remoteCompose(args string) string {
	return remoteCleanEnv("docker compose " + args)
}

// ─── Routing for compose services (D-19) ─────────────────────────────────────
//
// Compose files cannot set traefik.* labels (reserved by the deploy policy).
// The platform routes a compose service's domains itself: it writes an
// override file, passed as an extra -f after the user's file, that gives one
// compose service the Traefik labels a single-container service gets
// (buildTraefikLabels) and attaches it to the Traefik network.

// composeOverride is the platform's override file.
type composeOverride struct {
	Services map[string]overrideService `yaml:"services"`
	Networks map[string]overrideNetwork `yaml:"networks"`
}

type overrideService struct {
	Labels map[string]string `yaml:"labels"`
	// Networks keeps the service on the project's default network (where
	// its siblings are) and adds the Traefik network.
	Networks map[string]struct{} `yaml:"networks"`
}

type overrideNetwork struct {
	External bool   `yaml:"external"`
	Name     string `yaml:"name"`
}

// routerRulePattern matches the rule label of a Traefik router.
var routerRulePattern = regexp.MustCompile(`^traefik\.http\.routers\.([^.]+)\.rule$`)

// overrideFile is a platform override file passed to docker compose with an
// extra -f, after the user's file.
type overrideFile struct {
	name    string
	content []byte
}

// composeOverrides returns the platform's override files for a compose
// deploy, in the order they are passed: under isolated app networks the
// network override (the stack's references to the control plane's network
// point at its environment network; composespec.IsolateNetworks generated
// and re-checked it), then the guard (composeGuardOverride), then the
// routing override, which attaches the routed service to the environment
// network (isolated) or the control plane's network (legacy, as before).
func composeOverrides(serviceID string, cfg rollout.NetConfig, route *deployment.ComposeRoute, nets deployment.ComposeNetworks, limits map[string]deployment.ComposeLimit) ([]overrideFile, error) {
	var out []overrideFile
	key, network := cfg.WithDefaults().LegacyNetwork, cfg.WithDefaults().LegacyNetwork
	if nets.Isolated {
		if nets.Home == "" || !netplan.IsManagedName(nets.Home) {
			return nil, fmt.Errorf("compose deploy without an app network (got %q)", nets.Home)
		}
		if len(nets.Override) > 0 {
			out = append(out, overrideFile{name: composespec.NetworkOverrideFile, content: nets.Override})
		}
		network, key = nets.Home, nets.Home
		if nets.RouteKey != "" {
			key = nets.RouteKey
		}
	}
	if guard, err := composeGuardOverride(nets.Services, route); err != nil {
		return nil, err
	} else if guard != nil {
		out = append(out, overrideFile{name: "docker-compose.guard.yml", content: guard})
	}
	if route != nil {
		content, err := composeRouteOverride(serviceID, key, network, route)
		if err != nil {
			return nil, err
		}
		out = append(out, overrideFile{name: "docker-compose.route.yml", content: content})
	}
	// An override naming a service the file lacks makes compose fail, so only
	// the file's services get limits; with no service list, none do.
	known := map[string]deployment.ComposeLimit{}
	for _, svc := range nets.Services {
		if l, ok := limits[svc]; ok {
			known[svc] = l
		}
	}
	if lim, err := composeLimitsOverride(known); err != nil {
		return nil, err
	} else if lim != nil {
		out = append(out, overrideFile{name: "docker-compose.limits.yml", content: lim})
	}
	// The hardened runtime (D-93): every service of the stack can't gain
	// privileges, has no raw sockets unless granted and a process limit.
	if sec, err := composeSecurityOverride(nets.Services, limits, nets.Security); err != nil {
		return nil, err
	} else if sec != nil {
		out = append(out, overrideFile{name: "docker-compose.security.yml", content: sec})
	}
	// The stack's containers join the apps' slice too (users may not set
	// cgroup_parent themselves, composespec).
	if cfg.AppsSlice != "" && len(nets.Services) > 0 {
		slice := map[string]map[string]map[string]string{"services": {}}
		for _, svc := range nets.Services {
			slice["services"][svc] = map[string]string{"cgroup_parent": cfg.AppsSlice}
		}
		content, err := yaml.Marshal(slice)
		if err != nil {
			return nil, fmt.Errorf("encode the slice override: %w", err)
		}
		out = append(out, overrideFile{name: "docker-compose.slice.yml", content: content})
	}
	// On an agent server the stack logs to Docker's local driver, capped,
	// and the agent ships it (D-113), as slots do.
	if cfg.LocalLogs && len(nets.Services) > 0 {
		lc := rollout.LocalLogConfig()
		logging := map[string]map[string]map[string]any{"services": {}}
		for _, svc := range nets.Services {
			logging["services"][svc] = map[string]any{"logging": map[string]any{"driver": lc.Type, "options": lc.Config}}
		}
		content, err := yaml.Marshal(logging)
		if err != nil {
			return nil, fmt.Errorf("encode the logging override: %w", err)
		}
		out = append(out, overrideFile{name: "docker-compose.logging.yml", content: content})
	}
	return out, nil
}

type limitsOverride struct {
	Services map[string]limitsService `yaml:"services"`
}

type limitsService struct {
	MemLimit     int64         `yaml:"mem_limit,omitempty"`
	MemSwapLimit int64         `yaml:"memswap_limit,omitempty"`
	CPUs         float64       `yaml:"cpus,omitempty"`
	Deploy       *limitsDeploy `yaml:"deploy,omitempty"`
}

// limitsDeploy is deploy.resources.limits; the compose schema takes its
// memory as a string.
type limitsDeploy struct {
	Resources struct {
		Limits struct {
			Memory string `yaml:"memory,omitempty"`
			CPUs   string `yaml:"cpus,omitempty"`
		} `yaml:"limits"`
	} `yaml:"resources"`
}

// composeLimitsOverride sets the panel's memory (and CPU) limit on each
// compose service that has one (D-41); the panel's value wins over the
// file's. Swap equals memory, so a container is stopped at its limit
// rather than spilling into swap.
//
// It mirrors the file's form. Where the file declares
// deploy.resources.limits, the limits are set both there and at the top
// level: compose refuses different values in the two. Elsewhere only the
// top-level keys are set, because adding the block would make compose
// compare it with the file's own cpus or pids_limit.
func composeLimitsOverride(limits map[string]deployment.ComposeLimit) ([]byte, error) {
	if len(limits) == 0 {
		return nil, nil
	}
	o := limitsOverride{Services: map[string]limitsService{}}
	for name, l := range limits {
		if !composespec.ValidServiceName(name) {
			return nil, fmt.Errorf("compose service name %q is not valid", name)
		}
		if l.MemoryMB <= 0 && l.CPUCores <= 0 {
			continue
		}
		// A zero would override the file's own limit with "unlimited".
		var b int64
		if l.MemoryMB > 0 {
			b = l.MemoryMB << 20
		}
		ls := limitsService{MemLimit: b, MemSwapLimit: b, CPUs: l.CPUCores}
		if l.DeployLimits {
			ls.Deploy = &limitsDeploy{}
			if b > 0 {
				ls.Deploy.Resources.Limits.Memory = strconv.FormatInt(b, 10)
			}
			if l.CPUCores > 0 {
				ls.Deploy.Resources.Limits.CPUs = strconv.FormatFloat(l.CPUCores, 'f', -1, 64)
			}
		}
		o.Services[name] = ls
	}
	if len(o.Services) == 0 {
		return nil, nil
	}
	out, err := yaml.Marshal(o)
	if err != nil {
		return nil, fmt.Errorf("marshal compose limits: %w", err)
	}
	return append([]byte("# Generated by the platform: memory and CPU limits set in the panel (D-41).\n"), out...), nil
}

// guardOverride is the platform's routing guard: labels only.
type guardOverride struct {
	Services map[string]guardService `yaml:"services"`
}

type guardService struct {
	Labels map[string]string `yaml:"labels"`
}

// composeGuardOverride returns the override that sets traefik.enable=false
// on every service but the routed one (nil when there is none). A
// container's own labels win over its image's, so routers an image defines
// never go live on a network Traefik joins: the environment network
// (isolated) or the control plane's network (legacy).
func composeGuardOverride(services []string, route *deployment.ComposeRoute) ([]byte, error) {
	guarded := map[string]guardService{}
	for _, svc := range services {
		if route != nil && svc == route.Service {
			continue
		}
		guarded[svc] = guardService{Labels: map[string]string{"traefik.enable": "false"}}
	}
	if len(guarded) == 0 {
		return nil, nil
	}
	out, err := yaml.Marshal(guardOverride{Services: guarded})
	if err != nil {
		return nil, fmt.Errorf("marshal compose guard: %w", err)
	}
	return append([]byte("# Generated by the platform: no routing for the services that receive no domains.\n"), out...), nil
}

// routeLabelKeys returns the traefik.* label keys the routing override
// gives route.Service (composeRouteOverride), for rollout.CheckStackRoutes.
func routeLabelKeys(serviceID, network string, route *deployment.ComposeRoute) map[string]bool {
	keys := map[string]bool{}
	if route == nil {
		return keys
	}
	for k := range composeRouteLabels(serviceID, network, route) {
		keys[k] = true
	}
	return keys
}

// routedService is the compose service route sends the domains to, "" when
// none.
func routedService(route *deployment.ComposeRoute) string {
	if route == nil {
		return ""
	}
	return route.Service
}

// routedImageViolations returns the traefik.* label keys image carries that
// the routing override does not set (allowed): routes of its own, which would
// go live as soon as the routed service starts. ok is false when the image
// could not be inspected (a service built during up, or a pull that failed);
// the check after up still covers it.
func routedImageViolations(ctx context.Context, dc *client.Client, image string, allowed map[string]bool) (keys []string, ok bool) {
	img, err := dc.ImageInspect(ctx, image)
	if err != nil {
		return nil, false
	}
	if img.Config == nil {
		return nil, true
	}
	for _, k := range rollout.TraefikKeys(img.Config.Labels) {
		if !allowed[k] {
			keys = append(keys, k)
		}
	}
	return keys, true
}

// routedImageError is the refusal when the routed service's image would
// route itself, before anything of the stack changed.
func routedImageError(service, image string, keys []string) error {
	return fmt.Errorf("the image of %s (%s) carries traefik.* labels the platform did not set (%s); they would add routes of "+
		"their own, so nothing was started and the running stack keeps serving. Remove them from the image",
		service, image, strings.Join(keys, ", "))
}

// singleImage returns the one image name `compose config --images <service>`
// printed, or "" when it printed none or several (an older compose).
func singleImage(out string) string {
	lines := strings.Fields(strings.TrimSpace(out))
	if len(lines) != 1 {
		return ""
	}
	return lines[0]
}

// composeRouteLabels are the Traefik labels the routing override gives
// route.Service, before "$" escaping.
func composeRouteLabels(serviceID, network string, route *deployment.ComposeRoute) map[string]string {
	labels := buildTraefikLabels(serviceID, route.Domains, route.Port, route.Access)
	labels["traefik.docker.network"] = network
	svcName := traefikServiceName(serviceID)
	for key := range labels {
		if m := routerRulePattern.FindStringSubmatch(key); m != nil {
			labels["traefik.http.routers."+m[1]+".service"] = svcName
		}
	}
	return labels
}

// composeRouteOverride returns the override file that routes route.Domains
// to route.Service on route.Port, through the Traefik network named network,
// declared under key (the network's name, or the stack's own key for it when
// the stack already references it, so the service is not attached twice).
// It is generated with a YAML marshaller from typed data, never by
// concatenating user input; the compose service name is a map key (compose
// does not interpolate keys) and must pass composespec.ValidServiceName.
// Every "$" in a label value is written as "$$", because docker compose
// interpolates override files too (a bcrypt hash in a basic auth label is
// full of "$").
func composeRouteOverride(serviceID, key, network string, route *deployment.ComposeRoute) ([]byte, error) {
	if route == nil {
		return nil, fmt.Errorf("no route")
	}
	if !composespec.ValidServiceName(route.Service) {
		return nil, fmt.Errorf("compose service name %q cannot receive domains", route.Service)
	}
	if len(serviceID) < 8 {
		return nil, fmt.Errorf("invalid service id %q", serviceID)
	}
	if route.Port <= 0 || route.Port > 65535 {
		return nil, fmt.Errorf("invalid port %d", route.Port)
	}
	if len(route.Domains) == 0 {
		return nil, fmt.Errorf("no domains to route")
	}
	if network == "" {
		network = netplan.LegacyNetwork
	}
	if key == "" {
		key = network
	}

	labels := composeRouteLabels(serviceID, network, route)
	for key, val := range labels {
		labels[key] = strings.ReplaceAll(val, "$", "$$")
	}

	out, err := yaml.Marshal(composeOverride{
		Services: map[string]overrideService{
			route.Service: {
				Labels:   labels,
				Networks: map[string]struct{}{key: {}, "default": {}},
			},
		},
		Networks: map[string]overrideNetwork{
			key: {External: true, Name: network},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal compose override: %w", err)
	}
	return append([]byte("# Generated by the platform: routing for the service's domains (Settings → Domain Routing).\n"), out...), nil
}

type securityService struct {
	SecurityOpt []string    `yaml:"security_opt"`
	CapDrop     []string    `yaml:"cap_drop,omitempty"`
	PidsLimit   int64       `yaml:"pids_limit"`
	Runtime     string      `yaml:"runtime,omitempty"`
	Volumes     []string    `yaml:"volumes,omitempty"`
	Deploy      *pidsDeploy `yaml:"deploy,omitempty"`
}

// pidsDeploy is deploy.resources.limits.pids.
type pidsDeploy struct {
	Resources struct {
		Limits struct {
			Pids int64 `yaml:"pids"`
		} `yaml:"limits"`
	} `yaml:"resources"`
}

// composeSecurityOverride gives each of the stack's services the hardened
// runtime (D-93): no-new-privileges, NET_RAW dropped unless granted, and the
// service's process limit. The deploy policy refuses a file that loosens
// them (security_opt, cap_add, runtime), so merging only tightens. A
// Sandboxed stack's services all get gVisor (D-95); merged last, its
// runtime wins over one a granted exception left in the file. A strict one's
// (D-115) also mount the agent relay's resolv.conf.
//
// Like composeLimitsOverride it mirrors the file's form: where the file
// declares deploy.resources.limits (limits[svc].DeployLimits) the process
// limit goes there too, as compose refuses pids_limit beside a different
// deploy.resources.limits.pids, unset counting as different. Elsewhere only
// pids_limit is set: the block would make compose compare it with the
// file's own cpus or mem_limit.
func composeSecurityOverride(services []string, limits map[string]deployment.ComposeLimit, sec rollout.Security) ([]byte, error) {
	if len(services) == 0 {
		return nil, nil
	}
	out := map[string]map[string]securityService{"services": {}}
	for _, svc := range services {
		ss := securityService{SecurityOpt: []string{"no-new-privileges:true"}, CapDrop: sec.CapDrop(), PidsLimit: sec.Pids(), Runtime: sec.Runtime}
		if sec.Runtime == rollout.RuntimeStrict {
			ss.Volumes = []string{rollout.StrictResolvConf}
		}
		if limits[svc].DeployLimits {
			ss.Deploy = &pidsDeploy{}
			ss.Deploy.Resources.Limits.Pids = sec.Pids()
		}
		out["services"][svc] = ss
	}
	content, err := yaml.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode the security override: %w", err)
	}
	return append([]byte("# Generated by the platform: the hardened runtime (D-93).\n"), content...), nil
}
