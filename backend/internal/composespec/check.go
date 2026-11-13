package composespec

import (
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
)

// ReservedLabelPrefixes are label key prefixes only the platform may set.
var ReservedLabelPrefixes = []string{"paas.", "traefik.", "com.docker.compose."}

// ReservedContainerNamePrefix is the container name prefix the platform uses
// for its own containers (paas-postgres, paas-traefik, ...) and for every
// service's containers.
const ReservedContainerNamePrefix = "paas-"

// IsReservedLabel reports whether key is a label only the platform may set.
// The comparison ignores case and surrounding space.
func IsReservedLabel(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	for _, p := range ReservedLabelPrefixes {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// engineSocketNames are file names of container engine API sockets.
var engineSocketNames = map[string]bool{
	"docker.sock":     true,
	"containerd.sock": true,
	"podman.sock":     true,
	"buildkitd.sock":  true,
	"crio.sock":       true,
	"dockershim.sock": true,
}

// engineSocketDirs are directories that hold an engine socket and nothing a
// container needs.
var engineSocketDirs = map[string]bool{
	"/var/run/docker":     true,
	"/run/docker":         true,
	"/var/run/containerd": true,
	"/run/containerd":     true,
	"/run/podman":         true,
	"/var/run/podman":     true,
	"/run/buildkit":       true,
}

// IsEngineSocket reports whether a host path is a container engine socket or
// a directory that exists to hold one.
func IsEngineSocket(p string) bool {
	clean := path.Clean(filepath.ToSlash(p))
	if engineSocketNames[path.Base(clean)] {
		return true
	}
	return engineSocketDirs[clean]
}

// hostMountRule classifies a host path that is mounted into a container.
func hostMountRule(p string) Rule {
	if IsEngineSocket(p) {
		return RuleDockerSocket
	}
	return RuleHostBindMount
}

// namespacedSysctl reports whether Docker accepts the sysctl as namespaced
// (it applies to the container only). Docker refuses the others anyway; they
// are reported so the message names the field.
func namespacedSysctl(key string) bool {
	switch {
	case strings.HasPrefix(key, "net."),
		strings.HasPrefix(key, "fs.mqueue."),
		strings.HasPrefix(key, "kernel.shm"),
		strings.HasPrefix(key, "kernel.msg"),
		key == "kernel.sem":
		return true
	}
	return false
}

// allowedSecurityOpt reports whether a security_opt entry only tightens the
// container. Everything else (seccomp, apparmor and SELinux label overrides,
// systempaths=unconfined) can loosen it.
func allowedSecurityOpt(opt string) bool {
	o := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(opt), "=", ":"))
	return o == "no-new-privileges" || o == "no-new-privileges:true"
}

// Check inspects a loaded project and returns every violation, sorted by
// field. root is the Source.Root the project was loaded from: paths inside it
// are the deployment's own files.
func Check(p *types.Project, root string) []Violation {
	if p == nil {
		return nil
	}
	c := checker{root: filepath.Clean(root), project: p}
	for _, name := range sortedServiceNames(p) {
		svc, ok := p.Services[name]
		if !ok {
			svc = p.DisabledServices[name]
		}
		c.service(svc)
	}
	c.volumes()
	c.networks()
	c.fileObjects("secrets", secretsAsFiles(p.Secrets))
	c.fileObjects("configs", configsAsFiles(p.Configs))
	sort.SliceStable(c.out, func(i, j int) bool { return c.out[i].Field < c.out[j].Field })
	return c.out
}

func sortedServiceNames(p *types.Project) []string {
	names := make([]string, 0, len(p.Services)+len(p.DisabledServices))
	for n := range p.Services {
		names = append(names, n)
	}
	for n := range p.DisabledServices {
		if _, dup := p.Services[n]; !dup {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

type checker struct {
	root    string
	project *types.Project
	out     []Violation
}

func (c *checker) add(rule Rule, service, field, format string, args ...any) {
	c.addValue(rule, service, field, "", format, args...)
}

// addValue adds a violation with the offending value; a grantable rule's
// violations need one (K3).
func (c *checker) addValue(rule Rule, service, field, value, format string, args ...any) {
	c.out = append(c.out, Violation{Rule: rule, Service: service, Field: field, Message: fmt.Sprintf(format, args...),
		Value: value, Image: c.image(service)})
}

// image is what a compose service runs: its image, or its build context
// and Dockerfile relative to the project (the clone's directory changes
// with every deploy). "" outside a service.
func (c *checker) image(service string) string {
	if service == "" {
		return ""
	}
	s, ok := c.project.Services[service]
	if !ok {
		s = c.project.DisabledServices[service]
	}
	if s.Image != "" || s.Build == nil {
		return s.Image
	}
	return "build " + c.relative(s.Build.Context) + " " + c.relative(s.Build.Dockerfile)
}

func (c *checker) relative(p string) string {
	if c.inside(p) {
		if rel, err := filepath.Rel(c.root, p); err == nil {
			return filepath.ToSlash(rel)
		}
	}
	return p
}

// asJSON renders a structured value (a GPU request, a provider) the same
// way every time: struct fields in order, map keys sorted.
func asJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func (c *checker) inside(p string) bool {
	return filepath.IsAbs(p) && Within(c.root, p)
}

func (c *checker) service(s types.ServiceConfig) {
	n := s.Name
	f := func(field string) string { return "services." + n + "." + field }

	if s.Privileged {
		c.addValue(RulePrivileged, n, f("privileged"), "true", "privileged containers are not allowed")
	}
	if len(s.CapAdd) > 0 {
		c.addValue(RuleCapAdd, n, f("cap_add"), strings.Join(s.CapAdd, ", "), "adding capabilities is not allowed (%s)", strings.Join(s.CapAdd, ", "))
	}
	if len(s.GroupAdd) > 0 {
		c.addValue(RuleGroupAdd, n, f("group_add"), strings.Join(s.GroupAdd, ", "), "adding supplementary groups is not allowed (%s); a host group ID can unlock host files and devices", strings.Join(s.GroupAdd, ", "))
	}
	if len(s.Devices) > 0 {
		devices := make([]string, len(s.Devices))
		for i, d := range s.Devices {
			devices[i] = strings.TrimSuffix(d.Source+":"+d.Target+":"+d.Permissions, ":")
		}
		c.addValue(RuleDevices, n, f("devices"), strings.Join(devices, ", "), "host devices are not allowed")
	}
	if len(s.DeviceCgroupRules) > 0 {
		c.addValue(RuleDevices, n, f("device_cgroup_rules"), strings.Join(s.DeviceCgroupRules, ", "), "device cgroup rules are not allowed")
	}
	if len(s.Gpus) > 0 {
		c.addValue(RuleDevices, n, f("gpus"), asJSON(s.Gpus), "GPU devices are not allowed")
	}
	if s.Deploy != nil && s.Deploy.Resources.Reservations != nil && len(s.Deploy.Resources.Reservations.Devices) > 0 {
		c.addValue(RuleDevices, n, f("deploy.resources.reservations.devices"), asJSON(s.Deploy.Resources.Reservations.Devices), "device reservations are not allowed")
	}

	for field, val := range map[string]string{"pid": s.Pid, "ipc": s.Ipc, "uts": s.Uts, "userns_mode": s.UserNSMode, "cgroup": s.Cgroup} {
		v := strings.ToLower(strings.TrimSpace(val))
		switch {
		case v == "host":
			c.addValue(RuleHostNamespace, n, f(field), val, "the host %s namespace is not allowed", field)
		case strings.HasPrefix(v, types.ContainerPrefix):
			c.addValue(RuleContainerNamespace, n, f(field), val, "joining another container's namespace is not allowed (%s)", val)
		}
	}
	for field, val := range map[string]string{"network_mode": s.NetworkMode, "net": s.Net} {
		v := strings.ToLower(strings.TrimSpace(val))
		switch {
		case v == "host":
			c.addValue(RuleNetworkModeHost, n, f(field), val, "host networking is not allowed")
		case strings.HasPrefix(v, types.ContainerPrefix):
			c.addValue(RuleContainerNamespace, n, f(field), val, "joining another container's network is not allowed (%s)", val)
		}
	}
	for i, vf := range s.VolumesFrom {
		if strings.HasPrefix(strings.TrimSpace(vf), types.ContainerPrefix) {
			c.addValue(RuleContainerNamespace, n, fmt.Sprintf("%s[%d]", f("volumes_from"), i), vf, "mounting another container's volumes is not allowed (%s)", vf)
		}
	}

	if s.Runtime != "" {
		c.addValue(RuleRuntime, n, f("runtime"), s.Runtime, "runtime overrides are not allowed (%s)", s.Runtime)
	}
	if s.Isolation != "" && s.Isolation != "default" {
		c.addValue(RuleRuntime, n, f("isolation"), s.Isolation, "isolation overrides are not allowed (%s)", s.Isolation)
	}
	for i, opt := range s.SecurityOpt {
		if !allowedSecurityOpt(opt) {
			c.addValue(RuleSecurityOpt, n, fmt.Sprintf("%s[%d]", f("security_opt"), i), opt, "security_opt %q is not allowed; only no-new-privileges is", opt)
		}
	}
	for _, key := range sortedKeys(s.Sysctls) {
		if !namespacedSysctl(key) {
			c.addValue(RuleSysctls, n, f("sysctls."+key), key+"="+s.Sysctls[key], "non-namespaced sysctls are not allowed")
		}
	}
	if s.CgroupParent != "" {
		c.addValue(RuleCgroupParent, n, f("cgroup_parent"), s.CgroupParent, "cgroup_parent is not allowed")
	}
	if s.UseAPISocket {
		c.addValue(RuleDockerSocket, n, f("use_api_socket"), "true", "use_api_socket exposes the Docker API to the container")
	}
	if s.Provider != nil {
		c.addValue(RuleHostExec, n, f("provider"), asJSON(s.Provider), "provider services run a program on the host (%s)", s.Provider.Type)
	}
	for _, hooks := range []struct {
		name  string
		hooks []types.ServiceHook
	}{{"pre_start", s.PreStart}, {"post_start", s.PostStart}, {"pre_stop", s.PreStop}} {
		for i, h := range hooks.hooks {
			if h.Privileged {
				c.addValue(RulePrivileged, n, fmt.Sprintf("%s[%d].privileged", f(hooks.name), i), asJSON(h.Command), "privileged hooks are not allowed")
			}
		}
	}

	if s.ContainerName != "" && strings.HasPrefix(strings.ToLower(s.ContainerName), ReservedContainerNamePrefix) {
		c.add(RuleReservedName, n, f("container_name"), "container names starting with %q are reserved for the platform", ReservedContainerNamePrefix)
	}
	c.labels(n, f("labels"), s.Labels)
	if s.Deploy != nil {
		c.labels(n, f("deploy.labels"), s.Deploy.Labels)
	}

	for i, v := range s.Volumes {
		c.serviceVolume(n, fmt.Sprintf("%s[%d]", f("volumes"), i), v)
	}
	for i, ef := range s.EnvFiles {
		if ef.Path != "" && !c.inside(ef.Path) {
			c.add(RuleExternalRef, n, fmt.Sprintf("%s[%d]", f("env_file"), i), "env file %s is outside the project", ef.Path)
		}
	}
	if s.Extends != nil && s.Extends.File != "" && !c.inside(s.Extends.File) {
		c.add(RuleExternalRef, n, f("extends.file"), "extends file %s is outside the project", s.Extends.File)
	}
	if s.Build != nil {
		c.build(n, f("build"), s.Build)
	}
}

func (c *checker) labels(service, field string, labels types.Labels) {
	for _, key := range sortedKeys(labels) {
		if !IsReservedLabel(key) {
			continue
		}
		msg := "label %q is reserved for the platform (paas.*, traefik.* and com.docker.compose.* cannot be set)"
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(key)), "traefik.") {
			msg += "; the platform routes the service's domains itself: choose the compose service and port under Settings → Domain Routing, and remove the traefik.* labels"
		}
		c.add(RuleReservedLabel, service, field+"."+key, msg, key)
	}
}

func (c *checker) serviceVolume(service, field string, v types.ServiceVolumeConfig) {
	switch v.Type {
	case types.VolumeTypeBind:
		if c.inside(v.Source) {
			// The deployment's own files (a config shipped in the repo).
			// ScanRepo has already refused symlinks that leave the repo.
			return
		}
		rule := hostMountRule(v.Source)
		value := v.Source + " → " + v.Target
		if rule == RuleDockerSocket {
			c.addValue(rule, service, field, value, "mounting the container engine socket (%s) is not allowed", v.Source)
			return
		}
		c.addValue(rule, service, field, value, "bind-mounting host path %s is not allowed", v.Source)
	case types.VolumeTypeVolume, types.VolumeTypeTmpfs, types.VolumeTypeImage, "":
		// Named, anonymous and tmpfs volumes. Named volumes are checked
		// where they are declared (top-level volumes).
	default:
		c.addValue(RuleHostBindMount, service, field, v.Type+" "+v.Source+" → "+v.Target, "volume type %q is not allowed", v.Type)
	}
}

func (c *checker) build(service, field string, b *types.BuildConfig) {
	localContext := ""
	if b.Context != "" && !strings.Contains(b.Context, "://") && !isRemoteContext(b.Context) {
		localContext = b.Context
		if !c.inside(b.Context) {
			c.add(RuleExternalRef, service, field+".context", "build context %s is outside the project", b.Context)
		}
	}
	if b.Dockerfile != "" {
		df := b.Dockerfile
		if !filepath.IsAbs(df) && localContext != "" {
			df = filepath.Join(localContext, df)
		}
		if (filepath.IsAbs(df) && !c.inside(df)) || (!filepath.IsAbs(df) && hasDotDot(df)) {
			c.add(RuleExternalRef, service, field+".dockerfile", "Dockerfile %s is outside the project", b.Dockerfile)
		}
	}
	for _, name := range sortedKeys(b.AdditionalContexts) {
		v := b.AdditionalContexts[name]
		if strings.Contains(v, "://") || strings.HasPrefix(v, types.ServicePrefix) || isRemoteContext(v) {
			continue
		}
		if !c.inside(v) {
			c.add(RuleExternalRef, service, field+".additional_contexts."+name, "build context %s is outside the project", v)
		}
	}
	for i, key := range b.SSH {
		if key.Path == "" {
			continue
		}
		if (filepath.IsAbs(key.Path) && !c.inside(key.Path)) || (!filepath.IsAbs(key.Path) && hasDotDot(key.Path)) {
			c.add(RuleExternalRef, service, fmt.Sprintf("%s.ssh[%d]", field, i), "SSH key %s is outside the project", key.Path)
		}
	}
	for _, list := range []struct {
		name    string
		entries types.StringList
	}{{"cache_from", b.CacheFrom}, {"cache_to", b.CacheTo}} {
		for i, e := range list.entries {
			if strings.Contains(strings.ToLower(e), "type=local") {
				c.add(RuleExternalRef, service, fmt.Sprintf("%s.%s[%d]", field, list.name, i), "local build caches read and write host paths")
			}
		}
	}
	if b.Privileged {
		c.addValue(RulePrivileged, service, field+".privileged", "true", "privileged builds are not allowed")
	}
	for i, ent := range b.Entitlements {
		e := strings.ToLower(strings.TrimSpace(ent))
		if e == "network.host" {
			c.addValue(RuleNetworkModeHost, service, fmt.Sprintf("%s.entitlements[%d]", field, i), ent, "host networking during the build is not allowed")
		} else {
			c.addValue(RulePrivileged, service, fmt.Sprintf("%s.entitlements[%d]", field, i), ent, "build entitlement %q is not allowed", ent)
		}
	}
	if strings.EqualFold(strings.TrimSpace(b.Network), "host") {
		c.addValue(RuleNetworkModeHost, service, field+".network", b.Network, "host networking during the build is not allowed")
	}
	if b.Isolation != "" && b.Isolation != "default" {
		c.addValue(RuleRuntime, service, field+".isolation", b.Isolation, "isolation overrides are not allowed (%s)", b.Isolation)
	}
	c.labels(service, field+".labels", b.Labels)
}

func (c *checker) volumes() {
	for _, name := range sortedKeys(c.project.Volumes) {
		v := c.project.Volumes[name]
		field := "volumes." + name
		if v.External {
			c.addValue(RuleExternalVolume, "", field+".external", firstNonEmpty(v.Name, name), "external volumes can hold another stack's data (%s)", firstNonEmpty(v.Name, name))
		} else if own := c.project.Name + "_" + name; v.Name != "" && v.Name != own {
			// An explicit name reuses whatever volume already has it, as
			// external: true would (compose only warns).
			c.addValue(RuleExternalVolume, "", field+".name", v.Name, "volume name %q is outside this project (it would be %q); an explicit name can reuse another stack's volume", v.Name, own)
		}
		driver := strings.ToLower(v.Driver)
		if driver == "" || driver == "local" {
			device := v.DriverOpts["device"]
			o := strings.ToLower(v.DriverOpts["o"])
			typ := strings.ToLower(v.DriverOpts["type"])
			if device != "" && (strings.Contains(o, "bind") || typ == "none" || typ == "") {
				if !c.inside(device) {
					rule := hostMountRule(device)
					c.addValue(rule, "", field+".driver_opts.device", device, "a volume backed by host path %s is a bind mount", device)
				}
			}
		}
		for _, key := range sortedKeys(v.Labels) {
			if IsReservedLabel(key) {
				c.add(RuleReservedLabel, "", field+".labels."+key, "label %q is reserved for the platform", key)
			}
		}
	}
}

func (c *checker) networks() {
	for _, name := range sortedKeys(c.project.Networks) {
		nw := c.project.Networks[name]
		field := "networks." + name
		driver := strings.ToLower(nw.Driver)
		switch {
		case strings.EqualFold(nw.Name, "host") || (bool(nw.External) && nw.Name == "" && strings.EqualFold(name, "host")):
			c.addValue(RuleNetworkModeHost, "", field, firstNonEmpty(nw.Name, name), "attaching to the host network is not allowed")
		case driver == "host" || driver == "macvlan" || driver == "ipvlan":
			c.addValue(RuleNetworkModeHost, "", field+".driver", nw.Driver, "network driver %q attaches to the host's network", nw.Driver)
		}
		for _, key := range sortedKeys(nw.Labels) {
			if IsReservedLabel(key) {
				c.add(RuleReservedLabel, "", field+".labels."+key, "label %q is reserved for the platform", key)
			}
		}
	}
}

type fileObject struct {
	file   string
	labels types.Labels
}

func secretsAsFiles(m types.Secrets) map[string]fileObject {
	out := make(map[string]fileObject, len(m))
	for k, v := range m {
		out[k] = fileObject{file: v.File, labels: v.Labels}
	}
	return out
}

func configsAsFiles(m types.Configs) map[string]fileObject {
	out := make(map[string]fileObject, len(m))
	for k, v := range m {
		out[k] = fileObject{file: v.File, labels: v.Labels}
	}
	return out
}

// fileObjects checks top-level secrets and configs: a file source is a bind
// mount of that host file into the container.
func (c *checker) fileObjects(kind string, objs map[string]fileObject) {
	for _, name := range sortedKeys(objs) {
		o := objs[name]
		field := kind + "." + name
		if o.file != "" && !c.inside(o.file) {
			rule := hostMountRule(o.file)
			c.addValue(rule, "", field+".file", o.file, "%s file %s is outside the project", strings.TrimSuffix(kind, "s"), o.file)
		}
		for _, key := range sortedKeys(o.labels) {
			if IsReservedLabel(key) {
				c.add(RuleReservedLabel, "", field+".labels."+key, "label %q is reserved for the platform", key)
			}
		}
	}
}

// isRemoteContext mirrors compose-go's test for git and HTTP build contexts.
func isRemoteContext(v string) bool {
	for _, prefix := range []string{"https://", "http://", "git://", "ssh://", "github.com/", "git@"} {
		if strings.HasPrefix(v, prefix) {
			return true
		}
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
