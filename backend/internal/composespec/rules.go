// Package composespec loads Docker Compose files with compose-go, confined to
// one directory, and inspects the typed model for anything that gives a
// deployment power over the host it runs on (D-19). It is pure: no database,
// no Docker. internal/deploypolicy applies exceptions and enforcement modes on
// top of it.
package composespec

import (
	"encoding/json"
	"fmt"
)

// Rule names one deploy-policy rule. The string value is part of the API: it
// is stored in service_deploy_policy_exceptions.allowed_rules and returned to
// clients.
type Rule string

// Rules an instance operator can grant as a per-service exception.
const (
	RulePrivileged         Rule = "privileged"          // privileged: true (service, build or hook)
	RuleCapAdd             Rule = "cap_add"             // any added Linux capability
	RuleGroupAdd           Rule = "group_add"           // supplementary groups (group_add)
	RuleDevices            Rule = "devices"             // devices, device_cgroup_rules, gpus, device reservations
	RuleHostNamespace      Rule = "host_namespace"      // pid/ipc/uts/userns_mode/cgroup: host
	RuleNetworkModeHost    Rule = "network_mode_host"   // host networking, host/macvlan/ipvlan networks, host build network
	RuleContainerNamespace Rule = "container_namespace" // joining another container's namespaces (container:<name>)
	RuleRuntime            Rule = "runtime"             // runtime: or isolation: overrides
	RuleSecurityOpt        Rule = "security_opt"        // anything but no-new-privileges
	RuleSysctls            Rule = "sysctls"             // sysctls outside the namespaced set Docker allows
	RuleCgroupParent       Rule = "cgroup_parent"       // cgroup_parent
	RuleDockerSocket       Rule = "docker_socket"       // mounting a container engine socket, use_api_socket
	RuleHostBindMount      Rule = "host_bind_mount"     // bind mounts of host paths outside the project
	RuleExternalVolume     Rule = "external_volume"     // external named volumes (another stack's data)
	RuleHostExec           Rule = "host_exec"           // commands run on the host (pre-deploy commands, compose providers)
	// RuleControlPlane is not a property of the configuration: while apps on
	// the control-plane server share its network (TOFT_APP_NETWORKS=legacy),
	// deploys there are limited to instance operators (D-19 stopgap).
	// Granting it lets the service be deployed there by its workspace's
	// developers and by git-push webhooks. internal/deployment checks it.
	RuleControlPlane Rule = "control_plane"
	// RuleNetRaw is not a property of the configuration either: the hardened
	// runtime drops NET_RAW from every container (D-93); granting it keeps
	// raw sockets for the service (tools that capture or craft packets;
	// ping works without them). internal/deployment checks it.
	RuleNetRaw Rule = "net_raw"
)

// Rules that can never be granted.
const (
	// RuleReservedLabel: paas.*, traefik.* and com.docker.compose.* labels are
	// set by the platform; a deploy that sets them could hijack routing or
	// impersonate another service's containers.
	RuleReservedLabel Rule = "reserved_label"
	// RuleReservedName: container names starting with "paas-" belong to the
	// platform and to other services.
	RuleReservedName Rule = "reserved_name"
	// RuleExternalRef: the compose file reads a file outside its own
	// directory (include, extends, env_file, label_file, build context,
	// symlinks), which on a single-server install is the control plane's
	// filesystem.
	RuleExternalRef Rule = "external_ref"
	// RuleReservedEnv: environment variables that steer the docker and
	// compose CLIs (DOCKER_*, COMPOSE_*, PATH, HOME, ...) cannot be set on a
	// compose service, because the platform runs the CLI with them; and a
	// compose file (or a .env file it is interpolated with) cannot read them,
	// because their value at deploy time is the platform's, which the check
	// does not see.
	RuleReservedEnv Rule = "reserved_env"
	// RuleComposeInvalid: the file could not be loaded, so it cannot be
	// checked.
	RuleComposeInvalid Rule = "compose_invalid"
	// RuleConfigPath: a config file path that would be written outside the
	// service's config directory.
	RuleConfigPath Rule = "config_path"
	// RuleAppNetwork: under isolated app networks (TOFT_APP_NETWORKS=isolated)
	// a compose stack may join only its own networks and, through the
	// control plane's network name, its environment's network. Joining any
	// other existing network (external networks, explicit names, the
	// default bridge, build networks, a reused bridge interface) could put
	// it next to another tenant's containers or the control plane.
	RuleAppNetwork Rule = "app_network"
)

// RuleMemoryLimit: a service or compose service without a memory limit (D-41).
// Grantable, so it sits outside the never-grantable block above.
const RuleMemoryLimit Rule = "memory_limit"

// Violation is one thing a deployment is not allowed to do.
type Violation struct {
	Rule Rule `json:"rule"`
	// Service is the compose service the violation is in, when there is one.
	Service string `json:"service,omitempty"`
	// Field is the path to the offending setting, e.g.
	// "services.web.volumes[0]" or "pre_deploy_commands".
	Field   string `json:"field"`
	Message string `json:"message"`
	// Value is the offending setting (a capability list, a host path, a
	// sysctl and its value, a command), and Image what the compose service
	// runs: its image, or its build context and Dockerfile. Set for
	// grantable rules; a deploy policy exception approves them (K3).
	Value string `json:"value,omitempty"`
	Image string `json:"image,omitempty"`
	// State and Fingerprint are the deploy policy's (internal/deploypolicy):
	// whether an exception covers the violation, and the hash of its rule,
	// service, field, value and image taken before secrets are redacted.
	State       string `json:"state,omitempty"`
	Fingerprint string `json:"-"`
	// AltFingerprint is the same hash in the other form a pin may hold:
	// keyed with the instance's HMAC key, or plain SHA-256 (D-126).
	AltFingerprint string `json:"-"`
}

// MarshalJSON adds the rule's explanation (RuleInfo: title, why, fix and
// whether an operator can grant it), so every client shows the same text.
func (v Violation) MarshalJSON() ([]byte, error) {
	type plain Violation // no MarshalJSON: no recursion
	out := struct {
		plain
		Title     string `json:"title,omitempty"`
		Why       string `json:"why,omitempty"`
		Fix       string `json:"fix,omitempty"`
		Grantable bool   `json:"grantable"`
	}{plain: plain(v)}
	for _, info := range ruleInfos {
		if info.Rule == v.Rule {
			out.Title, out.Why, out.Fix, out.Grantable = info.Description, info.Why, info.Fix, info.Grantable
		}
	}
	return json.Marshal(out)
}

// String renders the violation for deploy logs: "field: message".
func (v Violation) String() string {
	if v.Field == "" {
		return v.Message
	}
	return v.Field + ": " + v.Message
}

// RuleInfo describes a rule for clients: what it covers (Description), the
// harm it prevents (Why) and what to do instead (Fix).
type RuleInfo struct {
	Rule        Rule   `json:"rule"`
	Grantable   bool   `json:"grantable"`
	Description string `json:"description"`
	Why         string `json:"why"`
	Fix         string `json:"fix"`
}

var ruleInfos = []RuleInfo{
	{Rule: RulePrivileged, Grantable: true, Description: "Privileged containers (privileged: true on a service, a build or a hook)",
		Why: "A privileged container has every Linux capability and sees the server's devices, so it can take over the whole server.",
		Fix: "Remove privileged: true. If the app needs one specific permission, add only that one."},
	{Rule: RuleCapAdd, Grantable: true, Description: "Added Linux capabilities (cap_add)",
		Why: "Capabilities give a container powers normally kept for root on the server, such as reconfiguring the network or loading kernel programs.",
		Fix: "Remove cap_add, or keep only the capability the app cannot run without."},
	{Rule: RuleGroupAdd, Grantable: true, Description: "Supplementary groups (group_add)",
		Why: "Joining a group the server knows, such as docker or disk, can open the server's files and devices to the container.",
		Fix: "Remove group_add, or use a group that exists only inside the image."},
	{Rule: RuleDevices, Grantable: true, Description: "Host devices (devices, device_cgroup_rules, gpus)",
		Why: "A device from the server (a disk, a GPU, a serial port) lets the container use that hardware directly, outside its sandbox.",
		Fix: "Remove the device."},
	{Rule: RuleHostNamespace, Grantable: true, Description: "Host PID, IPC, UTS, user or cgroup namespace",
		Why: "Sharing one of the server's namespaces lets the container see or signal every process on the server, or change its hostname or limits.",
		Fix: "Remove the pid, ipc, uts, userns_mode or cgroup setting that says host."},
	{Rule: RuleNetworkModeHost, Grantable: true, Description: "Host networking (network_mode: host, host/macvlan/ipvlan networks)",
		Why: "On the server's own network the container skips the firewall and Traefik: it can listen on any port and reach every service on the server.",
		Fix: "Remove the host network, and publish the app through its domains (Domains tab) instead."},
	{Rule: RuleContainerNamespace, Grantable: true, Description: "Joining another container's namespaces (container:<name>)",
		Why: "Joining another container's namespaces or volumes lets this service read or change another service's processes, traffic or data.",
		Fix: "Reach other services by their network alias, or use service:<name> for a service of the same stack."},
	{Rule: RuleRuntime, Grantable: true, Description: "Container runtime or isolation overrides",
		Why: "Another runtime or isolation mode changes how the container is sandboxed from the server.",
		Fix: "Remove runtime and isolation; the platform chooses them."},
	{Rule: RuleSecurityOpt, Grantable: true, Description: "security_opt other than no-new-privileges",
		Why: "security_opt can switch off AppArmor, seccomp or SELinux, the walls between the container and the server.",
		Fix: "Remove it; only no-new-privileges is allowed."},
	{Rule: RuleSysctls, Grantable: true, Description: "Non-namespaced sysctls",
		Why: "These sysctls change kernel settings for the whole server, not only for this container.",
		Fix: "Remove the sysctl, or have an operator set it on the server."},
	{Rule: RuleCgroupParent, Grantable: true, Description: "cgroup_parent",
		Why: "cgroup_parent moves the container out of the CPU and memory limits the platform gives it.",
		Fix: "Remove cgroup_parent, and set limits in the service's settings."},
	{Rule: RuleDockerSocket, Grantable: true, Description: "Container engine socket (docker.sock, use_api_socket)",
		Why: "Whoever controls the Docker socket controls Docker, and with it root on the server: they can start a privileged container.",
		Fix: "Remove the socket mount."},
	{Rule: RuleHostBindMount, Grantable: true, Description: "Bind mounts of host paths",
		Why: "A container that mounts a folder of the server can read and change everything in it; mounting / or /etc would give it the whole server.",
		Fix: "For files several services share, such as uploads, mount a shared volume (Settings → Storage, then the service's Volumes). For data only this service uses, a named volume (Named vol under Volumes; in a compose file, a named volume instead of a path). For files the internet reads, object storage (S3-compatible) scales further."},
	{Rule: RuleExternalVolume, Grantable: true, Description: "External named volumes",
		Why: "An external volume can hold another service's or another workspace's data.",
		Fix: "Declare the volume without external: true, so it belongs to this stack."},
	{Rule: RuleHostExec, Grantable: true, Description: "Commands run on the host (pre-deploy commands, compose providers)",
		Why: "These commands run as root on the server itself, outside any container.",
		Fix: "Move the step into the image, or into the migrate command, which runs inside the app's container."},
	{Rule: RuleControlPlane, Grantable: true, Description: "Deploys to the control-plane server by developers and git-push webhooks, while apps there share its network",
		Why: "While apps on the panel's own server share its network, an app there could reach the panel's services.",
		Fix: "Deploy the service to a remote server, or switch the panel's server to isolated app networks."},
	{Rule: RuleNetRaw, Grantable: true, Description: "Raw sockets (NET_RAW), which the hardened runtime drops",
		Why: "Raw sockets let a container craft packets: spoof addresses on its network or sniff what reaches it.",
		Fix: "Most apps never need them, and ping works without them. Only tools that capture or craft packets do, such as tcpdump, arping or nmap."},
	{Rule: RuleReservedLabel, Grantable: false, Description: "Reserved labels (paas.*, traefik.*, com.docker.compose.*)",
		Why: "These labels decide routing and which service a container belongs to: setting them could take over another site's domain or pass for another service.",
		Fix: "Remove them. For routing, choose the compose service and port under Settings → Domain Routing."},
	{Rule: RuleReservedName, Grantable: false, Description: "Reserved container names (paas-*)",
		Why: "Containers named paas-* belong to the platform and to other services.",
		Fix: "Remove container_name, or use a name that does not start with paas-."},
	{Rule: RuleExternalRef, Grantable: false, Description: "Files outside the compose project (include, extends, env_file, build context, symlinks)",
		Why: "The compose file would read files from elsewhere on the server, outside its own project.",
		Fix: "Keep every file the compose file uses inside its repository or project."},
	{Rule: RuleReservedEnv, Grantable: false, Description: "Environment variables that steer the docker and compose CLIs",
		Why: "These variables steer the docker and compose commands the deploy runs, not the app.",
		Fix: "Rename the variable."},
	{Rule: RuleComposeInvalid, Grantable: false, Description: "Compose files that cannot be loaded",
		Why: "A file that cannot be loaded cannot be checked, so it is not deployed.",
		Fix: "Fix the compose file; the message says what is wrong."},
	{Rule: RuleConfigPath, Grantable: false, Description: "Config file paths outside the service's config directory",
		Why: "The file would be written outside the service's own config folder on the server.",
		Fix: "Use an absolute path in the container without .. (for example /etc/nginx/conf.d/default.conf)."},
	{Rule: RuleAppNetwork, Grantable: false, Description: "Docker networks the compose stack does not own (isolated app networks only)",
		Why: "Under isolated app networks a stack joins only its own networks, so it cannot reach other environments or workspaces.",
		Fix: "Remove the network, or use the environment's network as the message says."},
	{Rule: RuleMemoryLimit, Grantable: true, Description: "Services without a memory limit",
		Why: "A container without a memory limit can use all of the server's memory and take every other service on it down.",
		Fix: "Set a memory limit in the service's settings (the suggestion on the service page is based on what it used). For a compose service, set it on the service page or with mem_limit in the compose file."},
}

// Rules returns every rule with whether it can be granted.
func Rules() []RuleInfo {
	out := make([]RuleInfo, len(ruleInfos))
	copy(out, ruleInfos)
	return out
}

// Grantable reports whether an instance operator may grant r as an exception.
// Unknown rules are not grantable.
func Grantable(r Rule) bool {
	for _, info := range ruleInfos {
		if info.Rule == r {
			return info.Grantable
		}
	}
	return false
}

// Known reports whether r is a rule this package can emit.
func Known(r Rule) bool {
	for _, info := range ruleInfos {
		if info.Rule == r {
			return true
		}
	}
	return false
}

// ViolationCode is the error code clients receive with a 422 response that
// lists violations.
const ViolationCode = "deploy_policy_violation"

// ViolationsError is returned when the deploy policy refuses a configuration
// or a deploy. Handlers turn it into a 422 with Body.
type ViolationsError struct {
	Violations []Violation
}

func (e *ViolationsError) Error() string {
	if len(e.Violations) == 0 {
		return "the deploy policy does not allow this"
	}
	msg := "the deploy policy does not allow this: " + e.Violations[0].String()
	if n := len(e.Violations) - 1; n == 1 {
		msg += " (and 1 more)"
	} else if n > 1 {
		msg += fmt.Sprintf(" (and %d more)", n)
	}
	return msg
}

// ViolationsBody is the data payload of a 422 deploy_policy_violation.
type ViolationsBody struct {
	Code       string      `json:"code"`
	Violations []Violation `json:"violations"`
}

// Body returns the 422 payload.
func (e *ViolationsError) Body() ViolationsBody {
	vs := e.Violations
	if vs == nil {
		vs = []Violation{}
	}
	return ViolationsBody{Code: ViolationCode, Violations: vs}
}
