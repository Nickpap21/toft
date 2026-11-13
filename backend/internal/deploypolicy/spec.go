package deploypolicy

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/configfile"
	"github.com/Nickpap21/toft/backend/internal/envvar"
)

// ServiceSpec is the part of a service's configuration that is not a compose
// file: single-container volumes, pre-deploy commands and config file paths.
type ServiceSpec struct {
	// Volumes are checked for single-container services only; compose
	// services declare theirs in the compose file.
	Volumes           []VolumeSpec
	PreDeployCommands []string
	ConfigFilePaths   []string
}

// CheckServiceSpec returns the violations in spec:
//
//   - a volume with a host path is a bind mount of the host (docker_socket
//     when it is an engine socket, host_bind_mount otherwise);
//   - every pre-deploy command runs as a shell command on the host, so any
//     command at all is host_exec;
//   - a config file path that fails configfile.ValidatePath is config_path.
func CheckServiceSpec(spec ServiceSpec) []Violation {
	var vs []Violation
	for i, v := range spec.Volumes {
		if strings.TrimSpace(v.HostPath) == "" {
			continue
		}
		// The message says what the setting does; the rule's explanation
		// (RuleInfo) says why it matters, and the caller says whether it is
		// refused, a warning or allowed by an exception.
		field := fmt.Sprintf("volumes[%d].host_path", i)
		where := fmt.Sprintf("volume %d mounts", i+1)
		into := ""
		value := v.HostPath
		if t := strings.TrimSpace(v.Target); t != "" {
			into = " at " + t + " in the container"
			value += " → " + t
		}
		if composespec.IsEngineSocket(v.HostPath) {
			vs = append(vs, Violation{Rule: composespec.RuleDockerSocket, Field: field, Value: value,
				Message: fmt.Sprintf("%s the server's container engine socket %s%s", where, v.HostPath, into)})
			continue
		}
		vs = append(vs, Violation{Rule: composespec.RuleHostBindMount, Field: field, Value: value,
			Message: fmt.Sprintf("%s the server folder %s%s", where, v.HostPath, into)})
	}
	var cmds []string
	for _, c := range spec.PreDeployCommands {
		if t := strings.TrimSpace(c); t != "" {
			cmds = append(cmds, t)
		}
	}
	if len(cmds) > 0 {
		vs = append(vs, Violation{Rule: composespec.RuleHostExec, Field: "pre_deploy_commands", Value: strings.Join(cmds, "\n"),
			Message: fmt.Sprintf("%d pre-deploy command(s) run as root on the server", len(cmds))})
	}
	for i, p := range spec.ConfigFilePaths {
		if err := configfile.ValidatePath(p); err != nil {
			vs = append(vs, Violation{Rule: composespec.RuleConfigPath, Field: fmt.Sprintf("config_files[%d]", i), Message: err.Error()})
		}
	}
	return vs
}

// splitEnv turns KEY=VALUE pairs into a map and the list of keys, in order.
func splitEnv(kvs []string) (map[string]string, []string) {
	m := make(map[string]string, len(kvs))
	keys := make([]string, 0, len(kvs))
	for _, kv := range kvs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if _, dup := m[k]; !dup {
			keys = append(keys, k)
		}
		m[k] = v
	}
	return m, keys
}

// invalidKeys returns the keys that are not valid env var names, sorted.
func invalidKeys(keys []string) []string {
	var bad []string
	for _, k := range keys {
		if !envvar.ValidKey(k) {
			bad = append(bad, k)
		}
	}
	sort.Strings(bad)
	return bad
}

// checkComposeEnv returns violations for env var keys of a compose service:
// keys the docker and compose CLIs read as their own settings, and keys that
// are not valid names (they are written into the compose .env file, where a
// newline or quote could add other variables).
func checkComposeEnv(keys []string) []Violation {
	var vs []Violation
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	for _, k := range sorted {
		switch {
		case !envvar.ValidKey(k):
			vs = append(vs, Violation{Rule: composespec.RuleReservedEnv, Field: "env_vars." + k,
				Message: fmt.Sprintf("%q is not a valid environment variable name and would corrupt the compose .env file; rename it", k)})
		case composespec.IsReservedCLIEnv(k):
			vs = append(vs, Violation{Rule: composespec.RuleReservedEnv, Field: "env_vars." + k,
				Message: fmt.Sprintf("%s is read by docker compose as its own setting; rename it", k)})
		}
	}
	return vs
}

func invalidKeyWarnings(keys []string) []string {
	var ws []string
	for _, k := range invalidKeys(keys) {
		ws = append(ws, fmt.Sprintf("environment variable %q is not a valid name (letters, digits and _, not starting with a digit); rename it", k))
	}
	return ws
}

// SplitLines splits newline-separated commands into trimmed non-empty lines.
func SplitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// ComposeProjectName mirrors the orchestrators' compose project name
// (docker.composeProjectName) without importing that package.
func ComposeProjectName(serviceID, serviceName string) string {
	id := serviceID
	if len(id) > 8 {
		id = id[:8]
	}
	name := sanitizeName(serviceName)
	if name == "" {
		return "paas-" + strings.ToLower(id)
	}
	return "paas-" + name + "-" + strings.ToLower(id)
}

func sanitizeName(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 30 {
		s = s[:30]
	}
	return s
}
