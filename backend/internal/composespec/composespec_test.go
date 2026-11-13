package composespec_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const inlineRoot = "/srv/paas-inline"

func validateInline(t *testing.T, content string, env map[string]string) []composespec.Violation {
	t.Helper()
	return composespec.Validate(context.Background(), composespec.Source{
		Root:        inlineRoot,
		Content:     content,
		ProjectName: "paas-test-12345678",
		Env:         env,
	})
}

func rulesOf(vs []composespec.Violation) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, string(v.Rule))
	}
	sort.Strings(out)
	return out
}

func TestValidate_CleanComposePasses(t *testing.T) {
	vs := validateInline(t, `
services:
  web:
    image: nginx:alpine
    ports: ["8080:80"]
    volumes:
      - data:/var/lib/data
      - ./conf:/etc/app:ro
      - type: tmpfs
        target: /tmp
    labels:
      app.example/team: web
    security_opt: ["no-new-privileges:true"]
    sysctls:
      net.core.somaxconn: "1024"
    cap_drop: [ALL]
    network_mode: "service:sidecar"
  sidecar:
    image: busybox
  worker:
    image: busybox
    volumes_from: [web]
networks:
  paas-network:
    external: true
volumes:
  data: {}
`, nil)
	assert.Empty(t, vs)
}

func TestValidate_OneCasePerRule(t *testing.T) {
	cases := []struct {
		name    string
		service string // YAML under services.app (indented 4 spaces)
		extra   string // top-level YAML appended
		want    composespec.Rule
		field   string
	}{
		{"privileged", "privileged: true", "", composespec.RulePrivileged, "services.app.privileged"},
		{"cap_add", "cap_add: [SYS_ADMIN]", "", composespec.RuleCapAdd, "services.app.cap_add"},
		{"group_add", "group_add: [\"0\"]", "", composespec.RuleGroupAdd, "services.app.group_add"},
		{"devices", "devices: [\"/dev/kvm:/dev/kvm\"]", "", composespec.RuleDevices, "services.app.devices"},
		{"device_cgroup_rules", "device_cgroup_rules: [\"c 1:3 mr\"]", "", composespec.RuleDevices, "services.app.device_cgroup_rules"},
		{"gpus", "gpus: all", "", composespec.RuleDevices, "services.app.gpus"},
		{"pid host", "pid: host", "", composespec.RuleHostNamespace, "services.app.pid"},
		{"ipc host", "ipc: host", "", composespec.RuleHostNamespace, "services.app.ipc"},
		{"uts host", "uts: host", "", composespec.RuleHostNamespace, "services.app.uts"},
		{"userns host", "userns_mode: host", "", composespec.RuleHostNamespace, "services.app.userns_mode"},
		{"cgroup host", "cgroup: host", "", composespec.RuleHostNamespace, "services.app.cgroup"},
		{"network host", "network_mode: host", "", composespec.RuleNetworkModeHost, "services.app.network_mode"},
		{"network container", "network_mode: \"container:paas-postgres\"", "", composespec.RuleContainerNamespace, "services.app.network_mode"},
		{"pid container", "pid: \"container:paas-postgres\"", "", composespec.RuleContainerNamespace, "services.app.pid"},
		{"volumes_from container", "volumes_from: [\"container:paas-postgres\"]", "", composespec.RuleContainerNamespace, "services.app.volumes_from[0]"},
		{"runtime", "runtime: runc", "", composespec.RuleRuntime, "services.app.runtime"},
		{"seccomp unconfined", "security_opt: [\"seccomp:unconfined\"]", "", composespec.RuleSecurityOpt, "services.app.security_opt[0]"},
		{"apparmor unconfined", "security_opt: [\"apparmor=unconfined\"]", "", composespec.RuleSecurityOpt, "services.app.security_opt[0]"},
		{"sysctls kernel", "sysctls: {kernel.core_pattern: \"|/tmp/x\"}", "", composespec.RuleSysctls, "services.app.sysctls.kernel.core_pattern"},
		{"cgroup_parent", "cgroup_parent: /system.slice", "", composespec.RuleCgroupParent, "services.app.cgroup_parent"},
		{"use_api_socket", "use_api_socket: true", "", composespec.RuleDockerSocket, "services.app.use_api_socket"},
		{"docker.sock short", "volumes: [\"/var/run/docker.sock:/var/run/docker.sock\"]", "", composespec.RuleDockerSocket, "services.app.volumes[0]"},
		{"docker.sock long", "volumes: [{type: bind, source: /run/docker.sock, target: /var/run/docker.sock}]", "", composespec.RuleDockerSocket, "services.app.volumes[0]"},
		{"host bind mount", "volumes: [\"/etc:/host-etc:ro\"]", "", composespec.RuleHostBindMount, "services.app.volumes[0]"},
		{"home bind mount", "volumes: [\"~/.ssh:/root/.ssh\"]", "", composespec.RuleHostBindMount, "services.app.volumes[0]"},
		{"relative escape", "volumes: [\"../../etc:/x\"]", "", composespec.RuleHostBindMount, "services.app.volumes[0]"},
		{"volume driver bind", "volumes: [\"hostroot:/host\"]", "volumes:\n  hostroot:\n    driver: local\n    driver_opts: {type: none, o: bind, device: /}", composespec.RuleHostBindMount, "volumes.hostroot.driver_opts.device"},
		{"external volume", "volumes: [\"pg:/data\"]", "volumes:\n  pg:\n    external: true\n    name: paas-postgres-data", composespec.RuleExternalVolume, "volumes.pg.external"},
		{"volume explicit name", "volumes: [\"pg:/data\"]", "volumes:\n  pg:\n    name: control-panel-paas_postgres_data", composespec.RuleExternalVolume, "volumes.pg.name"},
		{"secret file outside", "secrets: [creds]", "secrets:\n  creds:\n    file: /etc/shadow", composespec.RuleHostBindMount, "secrets.creds.file"},
		{"config file outside", "configs: [c]", "configs:\n  c:\n    file: /app/.env", composespec.RuleHostBindMount, "configs.c.file"},
		{"env_file outside", "env_file: /app/.env", "", composespec.RuleExternalRef, "services.app.env_file[0]"},
		{"build context absolute", "build: {context: /}", "", composespec.RuleExternalRef, "services.app.build.context"},
		{"build context dotdot", "build: {context: ../..}", "", composespec.RuleExternalRef, "services.app.build.context"},
		{"build privileged", "build: {context: ., privileged: true}", "", composespec.RulePrivileged, "services.app.build.privileged"},
		{"build entitlement", "build: {context: ., entitlements: [security.insecure]}", "", composespec.RulePrivileged, "services.app.build.entitlements[0]"},
		{"build network host", "build: {context: ., network: host}", "", composespec.RuleNetworkModeHost, "services.app.build.network"},
		{"build ssh key", "build: {context: ., ssh: [\"deploy=/root/.ssh/id_rsa\"]}", "", composespec.RuleExternalRef, "services.app.build.ssh[0]"},
		{"build local cache", "build: {context: ., cache_to: [\"type=local,dest=/etc/cron.d\"]}", "", composespec.RuleExternalRef, "services.app.build.cache_to[0]"},
		{"label paas", "labels: {paas.service_id: other}", "", composespec.RuleReservedLabel, "services.app.labels.paas.service_id"},
		{"label paas deployment", "labels: {paas.deployment_id: other}", "", composespec.RuleReservedLabel, "services.app.labels.paas.deployment_id"},
		{"label traefik", "labels: [\"traefik.http.routers.x.rule=Host(`victim.example`)\"]", "", composespec.RuleReservedLabel, "services.app.labels.traefik.http.routers.x.rule"},
		{"label compose", "labels: {com.docker.compose.project: paas-other}", "", composespec.RuleReservedLabel, "services.app.labels.com.docker.compose.project"},
		{"label case", "labels: {Traefik.Enable: \"true\"}", "", composespec.RuleReservedLabel, "services.app.labels.Traefik.Enable"},
		{"build label", "build: {context: ., labels: {traefik.enable: \"true\"}}", "", composespec.RuleReservedLabel, "services.app.build.labels.traefik.enable"},
		{"container name", "container_name: paas-postgres", "", composespec.RuleReservedName, "services.app.container_name"},
		{"host network external", "networks: [hostnet]", "networks:\n  hostnet:\n    name: host\n    external: true", composespec.RuleNetworkModeHost, "networks.hostnet"},
		{"macvlan", "networks: [lan]", "networks:\n  lan:\n    driver: macvlan", composespec.RuleNetworkModeHost, "networks.lan.driver"},
		{"provider", "provider: {type: some-binary}", "", composespec.RuleHostExec, "services.app.provider"},
		{"privileged hook", "post_start: [{command: [id], privileged: true}]", "", composespec.RulePrivileged, "services.app.post_start[0].privileged"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			image := "    image: alpine\n"
			if strings.Contains(tc.service, "build:") || strings.Contains(tc.service, "provider:") {
				image = ""
			}
			content := "services:\n  app:\n" + image + "    " + tc.service + "\n" + tc.extra + "\n"
			vs := validateInline(t, content, nil)
			require.NotEmpty(t, vs, "expected a %s violation", tc.want)
			found := false
			for _, v := range vs {
				if v.Rule == tc.want && v.Field == tc.field {
					found = true
				}
			}
			assert.True(t, found, "want %s at %s, got %+v", tc.want, tc.field, vs)
		})
	}
}

func TestValidate_PrivilegedFalseIsFine(t *testing.T) {
	assert.Empty(t, validateInline(t, "services:\n  app:\n    image: alpine\n    privileged: false\n", nil))
}

// A pre_start hook is an init container. From compose-go v2.16 it takes a
// whole container specification (mounts, networks, capabilities, runtime),
// which Check doesn't look at: only v2.15's schema refusing those fields
// keeps them out. A compose-go upgrade that lets one through fails here.
func TestValidate_PreStartHookCantEscapeThePolicy(t *testing.T) {
	assert.Empty(t, validateInline(t, "services:\n  app:\n    image: alpine\n    pre_start: [{command: [\"true\"]}]\n", nil))
	for _, hook := range []string{
		`{image: alpine, volumes: ["/:/host"]}`,
		`{image: alpine, network_mode: host}`,
		`{image: alpine, cap_add: [SYS_ADMIN]}`,
		`{image: alpine, runtime: runc}`,
		`{image: alpine, pid: host}`,
		`{image: alpine, devices: ["/dev/sda:/dev/sda"]}`,
		`{image: alpine, use_api_socket: true}`,
		`{image: alpine, security_opt: ["seccomp=unconfined"]}`,
		`{image: alpine, ipc: host}`,
		`{image: alpine, uts: host}`,
		`{image: alpine, userns_mode: host}`,
		`{image: alpine, cgroup: host}`,
		`{image: alpine, cgroup_parent: /}`,
		`{image: alpine, network_mode: "container:paas-postgres"}`,
		`{image: alpine, volumes_from: ["container:paas-postgres"]}`,
		`{image: alpine, sysctls: ["kernel.domainname=x"]}`,
		`{image: alpine, group_add: ["0"]}`,
		`{image: alpine, device_cgroup_rules: ["a *:* rwm"]}`,
		`{image: alpine, gpus: all}`,
		`{image: alpine, labels: ["traefik.enable=true"]}`,
	} {
		t.Run(hook, func(t *testing.T) {
			assert.NotEmpty(t, validateInline(t, "services:\n  app:\n    image: alpine\n    pre_start: ["+hook+"]\n", nil))
		})
	}
}

func TestValidate_AnchorAndMergeKeyBypass(t *testing.T) {
	vs := validateInline(t, `
x-base: &base
  image: alpine
  privileged: true
services:
  app:
    <<: *base
`, nil)
	assert.Equal(t, []string{"privileged"}, rulesOf(vs))
}

func TestValidate_InterpolationDefaultBypass(t *testing.T) {
	vs := validateInline(t, "services:\n  app:\n    image: alpine\n    privileged: ${P:-true}\n", nil)
	assert.Equal(t, []string{"privileged"}, rulesOf(vs))
}

func TestValidate_EnvVarFlipsValueAtDeployTime(t *testing.T) {
	content := "services:\n  app:\n    image: alpine\n    network_mode: ${NET:-bridge}\n    volumes: [\"${SRC:-./data}:/data\"]\n"
	assert.Empty(t, validateInline(t, content, nil), "safe with the defaults")

	vs := validateInline(t, content, map[string]string{"NET": "host", "SRC": "/var/run/docker.sock"})
	assert.Equal(t, []string{"docker_socket", "network_mode_host"}, rulesOf(vs))
}

// docker compose interpolates with its own process environment as well as the
// service's env vars. The check must refuse a file that reads the platform's
// variables, or it checks a different file from the one compose runs.
func TestValidate_PlatformEnvIsRefused(t *testing.T) {
	cases := map[string]string{
		"home bind default":   "volumes: [\"${HOME:-.}:/h\"]",
		"home flips network":  "network_mode: ${HOME:+host}",
		"home flips pid":      "pid: ${HOME:+host}",
		"path flips userns":   "userns_mode: ${PATH:+host}",
		"docker plugin var":   "network_mode: ${DOCKER_CLI_PLUGIN_ORIGINAL_CLI_COMMAND:+host}",
		"otel set by the cli": "network_mode: ${OTEL_RESOURCE_ATTRIBUTES:+host}",
		"in an x- field too":  "x-note: ${XDG_RUNTIME_DIR}",
	}
	for name, field := range cases {
		t.Run(name, func(t *testing.T) {
			content := "services:\n  app:\n    image: alpine\n    " + field + "\n"
			// Setting OTEL_RESOURCE_ATTRIBUTES does not help: the docker CLI
			// rewrites it for its plugins.
			vs := validateInline(t, content, map[string]string{"OTEL_RESOURCE_ATTRIBUTES": ""})
			require.Equal(t, []string{"reserved_env"}, rulesOf(vs), "%+v", vs)
			assert.Contains(t, vs[0].Field, "${")
		})
	}
}

func TestValidate_OrdinaryVariablesAndEscapesAreFine(t *testing.T) {
	content := "services:\n  app:\n    image: \"nginx:${TAG:-latest}\"\n    labels: {app.project: \"${COMPOSE_PROJECT_NAME}\"}\n    command: [\"sh\", \"-c\", \"echo $$HOME\"]\n"
	assert.Empty(t, validateInline(t, content, nil))
}

func TestValidate_DisabledProfilesAreChecked(t *testing.T) {
	vs := validateInline(t, `
services:
  app:
    image: alpine
  evil:
    image: alpine
    profiles: [later]
    privileged: true
`, nil)
	assert.Equal(t, []string{"privileged"}, rulesOf(vs))
	assert.Equal(t, "evil", vs[0].Service)
}

func TestValidate_InlineCannotIncludeOrExtend(t *testing.T) {
	vs := validateInline(t, "include:\n  - ./other.yml\nservices:\n  app:\n    image: alpine\n", nil)
	assert.Equal(t, []string{"external_ref"}, rulesOf(vs))

	vs = validateInline(t, "services:\n  app:\n    extends:\n      file: /etc/compose.yml\n      service: x\n", nil)
	assert.Equal(t, []string{"external_ref"}, rulesOf(vs))
}

func TestValidate_InvalidComposeIsAViolation(t *testing.T) {
	vs := validateInline(t, "services:\n  app:\n    image: alpine\n    privileged: notabool\n", nil)
	assert.Equal(t, []string{"compose_invalid"}, rulesOf(vs))

	vs = validateInline(t, "services: [", nil)
	assert.Equal(t, []string{"compose_invalid"}, rulesOf(vs))
}

// A required variable with no value is refused with the file's own message.
func TestValidate_RequiredVariableGivesItsMessage(t *testing.T) {
	file := "services:\n  app:\n    image: alpine\n    mem_limit: 64m\n    environment:\n      URL: \"${TOFT_URL:?the app needs the service’s address, so add a domain}\"\n"
	vs := validateInline(t, file, nil)
	require.Len(t, vs, 1)
	assert.Equal(t, "compose_invalid", string(vs[0].Rule))
	assert.Equal(t, "the app needs the service’s address, so add a domain", vs[0].Message)
	assert.Empty(t, validateInline(t, file, map[string]string{"TOFT_URL": "https://a.example.com"}))
}

// The message of a file that cannot be loaded is not cut here: it may quote
// an interpolated secret, and the deploy policy must see all of it to redact
// it (it cuts long messages itself, afterwards).
func TestValidate_InvalidMessageIsNotCut(t *testing.T) {
	secret := "s3cr\"et\\value"
	vs := validateInline(t, "services:\n  web:\n    image: nginx\n    cpus: \""+strings.Repeat("x", 600)+"${S}\"\n", map[string]string{"S": secret})
	require.Equal(t, []string{"compose_invalid"}, rulesOf(vs))
	assert.Contains(t, vs[0].Message, strings.Repeat("x", 600)+strconv.Quote(secret)[1:len(strconv.Quote(secret))-1])
}

func TestLoad_ReturnsInterpolatedProject(t *testing.T) {
	p, vs, err := composespec.Load(context.Background(), composespec.Source{
		Root:        inlineRoot,
		Content:     "services:\n  app:\n    image: ${IMG}\n",
		ProjectName: "paas-test-12345678",
		Env:         map[string]string{"IMG": "nginx:1.27"},
	})
	require.NoError(t, err)
	assert.Empty(t, vs)
	require.NotNil(t, p)
	assert.Equal(t, "nginx:1.27", p.Services["app"].Image)
	assert.Equal(t, "paas-test-12345678", p.Name)
}

// ─── Repository-based compose ────────────────────────────────────────────────

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
}

func validateRepo(t *testing.T, root, file, projectDir string, env map[string]string) []composespec.Violation {
	t.Helper()
	scan, err := composespec.ScanRepo(root)
	require.NoError(t, err)
	if len(scan) > 0 {
		return scan
	}
	return composespec.Validate(context.Background(), composespec.Source{
		Root:        root,
		ProjectDir:  projectDir,
		File:        file,
		ProjectName: "paas-test-12345678",
		Env:         env,
		ReadDotEnv:  true,
	})
}

func TestRepo_RelativeFilesInsideRepoAreFine(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"deploy/docker-compose.yml": `
include:
  - ./db.yml
services:
  web:
    extends:
      file: base.yml
      service: base
    volumes: ["../nginx/site.conf:/etc/nginx/conf.d/site.conf:ro"]
    env_file: [app.env]
    build:
      context: ..
      dockerfile: deploy/Dockerfile
`,
		"deploy/base.yml":   "services:\n  base:\n    image: nginx:alpine\n",
		"deploy/db.yml":     "services:\n  db:\n    image: postgres:16\n    volumes: [\"./pgdata:/var/lib/postgresql/data\"]\n",
		"deploy/app.env":    "A=1\n",
		"nginx/site.conf":   "server {}\n",
		"deploy/Dockerfile": "FROM nginx\n",
	})
	vs := validateRepo(t, root, "deploy/docker-compose.yml", filepath.Join(root, "deploy"), nil)
	assert.Empty(t, vs)
}

func TestRepo_IncludeOutsideRepoIsRejected(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"docker-compose.yml": "include:\n  - ../outside.yml\nservices:\n  web:\n    image: nginx\n",
	})
	vs := validateRepo(t, root, "docker-compose.yml", "", nil)
	assert.Equal(t, []string{"external_ref"}, rulesOf(vs))
}

func TestRepo_ExtendsAbsoluteIsRejected(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"docker-compose.yml": "services:\n  web:\n    extends:\n      file: /etc/x.yml\n      service: x\n",
	})
	vs := validateRepo(t, root, "docker-compose.yml", "", nil)
	assert.Equal(t, []string{"external_ref"}, rulesOf(vs))
}

func TestRepo_IncludeEnvFileAndProjectDirAreRejected(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"docker-compose.yml": "include:\n  - path: ./sub/a.yml\nservices:\n  web:\n    image: nginx\n",
		// Nested and written with a YAML escape so a text search would miss it.
		"sub/a.yml": "include:\n  - path: ./b.yml\n    \"env\\x5ffile\": /app/.env\nservices:\n  a:\n    image: alpine\n",
		"sub/b.yml": "services:\n  b:\n    image: alpine\n",
	})
	vs := validateRepo(t, root, "docker-compose.yml", "", nil)
	require.Equal(t, []string{"external_ref"}, rulesOf(vs))
	assert.Contains(t, vs[0].Field, "include.env_file")
}

// The include restriction is enforced by the load itself, so a file too large
// for any text scan cannot carry an env_file.
func TestRepo_IncludeEnvFileInLargeFileIsRejected(t *testing.T) {
	root := t.TempDir()
	padding := "# " + strings.Repeat("x", 2<<20) + "\n"
	writeFiles(t, root, map[string]string{
		"docker-compose.yml": padding + "include:\n  - path: sub.yml\n    env_file: /etc/hostname\nservices:\n  web:\n    image: nginx\n",
		"sub.yml":            "services:\n  sub:\n    image: alpine\n",
	})
	vs := validateRepo(t, root, "docker-compose.yml", "", nil)
	require.Equal(t, []string{"external_ref"}, rulesOf(vs))
	assert.Equal(t, "include.env_file", vs[0].Field)

	writeFiles(t, root, map[string]string{
		"docker-compose.yml": padding + "include:\n  - path: sub.yml\n    project_directory: /\nservices:\n  web:\n    image: nginx\n",
	})
	vs = validateRepo(t, root, "docker-compose.yml", "", nil)
	require.Equal(t, []string{"external_ref"}, rulesOf(vs))
	assert.Equal(t, "include.project_directory", vs[0].Field)
}

func TestRepo_DotEnvReadingPlatformEnvIsRefused(t *testing.T) {
	compose := "services:\n  web:\n    image: nginx\n    network_mode: ${NET:-bridge}\n"
	for name, dotEnv := range map[string]string{
		"expansion": "NET=${HOME:+host}\n",
		"inherited": "HOME\nNET=${HOME:+host}\n",
		"quoted":    "NET=\"${PATH:+host}\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeFiles(t, root, map[string]string{"docker-compose.yml": compose, ".env": dotEnv})
			vs := validateRepo(t, root, "docker-compose.yml", "", nil)
			require.NotEmpty(t, vs)
			assert.Equal(t, "reserved_env", string(vs[0].Rule))
			assert.Contains(t, vs[0].Field, "./.env")
		})
	}
}

// compose reads <dir>/.env for an included file; ScanRepo covers every .env.
func TestRepo_IncludedDirDotEnvReadingPlatformEnvIsRefused(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"docker-compose.yml": "include:\n  - sub/compose.yml\n",
		"sub/compose.yml":    "services:\n  web:\n    image: nginx\n    network_mode: ${NET:-bridge}\n",
		"sub/.env":           "NET=${HOME:+host}\n",
	})
	vs, err := composespec.ScanRepo(root)
	require.NoError(t, err)
	require.Equal(t, []string{"reserved_env"}, rulesOf(vs))
	assert.Equal(t, "./sub/.env: ${HOME}", vs[0].Field)
}

func TestRepo_SymlinkOutsideIsRejected(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"docker-compose.yml": "services:\n  web:\n    image: nginx\n    volumes: [\"./data:/data\"]\n",
	})
	require.NoError(t, os.Symlink("/", filepath.Join(root, "data")))
	vs := validateRepo(t, root, "docker-compose.yml", "", nil)
	require.Equal(t, []string{"external_ref"}, rulesOf(vs))
	assert.Equal(t, "./data", vs[0].Field)
}

func TestRepo_SymlinkInsideIsFine(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"docker-compose.yml": "services:\n  web:\n    image: nginx\n    volumes: [\"./current:/data\"]\n",
		"releases/v1/x":      "x",
	})
	require.NoError(t, os.Symlink("releases/v1", filepath.Join(root, "current")))
	assert.Empty(t, validateRepo(t, root, "docker-compose.yml", "", nil))
}

func TestRepo_DotEnvDrivesInterpolation(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"docker-compose.yml": "services:\n  web:\n    image: nginx\n    privileged: ${PRIV:-false}\n",
		".env":               "PRIV=true\n",
	})
	assert.Equal(t, []string{"privileged"}, rulesOf(validateRepo(t, root, "docker-compose.yml", "", nil)))
	// The service's env vars win over the repo's .env.
	assert.Empty(t, validateRepo(t, root, "docker-compose.yml", "", map[string]string{"PRIV": "false"}))
}

func TestRepo_ComposePathOutsideRepo(t *testing.T) {
	root := t.TempDir()
	vs := composespec.Validate(context.Background(), composespec.Source{Root: root, File: "../etc/compose.yml", ProjectName: "p"})
	assert.Equal(t, []string{"external_ref"}, rulesOf(vs))
}

func TestRepo_LabelFileIsResolvedAndChecked(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"docker-compose.yml": "services:\n  web:\n    image: nginx\n    label_file: ./labels\n",
		"labels":             "traefik.http.routers.x.rule=Host(`victim.example`)\n",
	})
	vs := validateRepo(t, root, "docker-compose.yml", "", nil)
	assert.Equal(t, []string{"reserved_label"}, rulesOf(vs))

	writeFiles(t, root, map[string]string{
		"docker-compose.yml": "services:\n  web:\n    image: nginx\n    label_file: /app/labels\n",
	})
	vs = validateRepo(t, root, "docker-compose.yml", "", nil)
	assert.Equal(t, []string{"external_ref"}, rulesOf(vs))
}

func TestGrantable(t *testing.T) {
	assert.True(t, composespec.Grantable(composespec.RuleDockerSocket))
	assert.True(t, composespec.Grantable(composespec.RuleHostExec))
	assert.False(t, composespec.Grantable(composespec.RuleReservedLabel))
	assert.False(t, composespec.Grantable(composespec.RuleExternalRef))
	assert.False(t, composespec.Grantable(composespec.Rule("made_up")))
}

func TestIsEngineSocket(t *testing.T) {
	for _, p := range []string{"/var/run/docker.sock", "/run/docker.sock", "/run/containerd/containerd.sock", "/var/run/docker", "/home/u/.docker/run/docker.sock"} {
		assert.True(t, composespec.IsEngineSocket(p), p)
	}
	for _, p := range []string{"/", "/var/run", "/etc", "/srv/data"} {
		assert.False(t, composespec.IsEngineSocket(p), p)
	}
}

func TestRepo_DotEnvReservedKeys(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"docker-compose.yml": "services:\n  web:\n    image: nginx\n",
		".env":               "COMPOSE_PROFILES=evil\nOK=1\n",
	})
	vs := validateRepo(t, root, "docker-compose.yml", "", nil)
	require.Equal(t, []string{"reserved_env"}, rulesOf(vs))
	assert.Contains(t, vs[0].Field, "COMPOSE_PROFILES")
}

func TestIsPlatformEnv(t *testing.T) {
	for _, k := range []string{"HOME", "PATH", "DOCKER_HOST", "XDG_RUNTIME_DIR", "DOCKER_CLI_PLUGIN_ORIGINAL_CLI_COMMAND", "OTEL_RESOURCE_ATTRIBUTES", "COMPOSE_FILE"} {
		assert.True(t, composespec.IsPlatformEnv(k), k)
	}
	for _, k := range []string{"COMPOSE_PROJECT_NAME", "TAG", "OTEL_SERVICE_NAME", "home"} {
		assert.False(t, composespec.IsPlatformEnv(k), k)
	}
}

func TestIsReservedCLIEnv(t *testing.T) {
	for _, k := range []string{"DOCKER_CONFIG", "DOCKER_HOST", "COMPOSE_ENV_FILES", "PATH", "HOME", "LD_PRELOAD", "GIT_CONFIG_COUNT", "GIT_SSH_COMMAND", "SSH_AUTH_SOCK"} {
		assert.True(t, composespec.IsReservedCLIEnv(k), k)
	}
	for _, k := range []string{"DATABASE_URL", "GIT_SHA", "NODE_ENV", "docker_host", "MY_PATH"} {
		assert.False(t, composespec.IsReservedCLIEnv(k), k)
	}
}

func TestViolationsError(t *testing.T) {
	err := &composespec.ViolationsError{Violations: []composespec.Violation{
		{Rule: composespec.RulePrivileged, Field: "services.a.privileged", Message: "no"},
		{Rule: composespec.RuleCapAdd, Field: "services.a.cap_add", Message: "no"},
	}}
	assert.Equal(t, "the deploy policy does not allow this: services.a.privileged: no (and 1 more)", err.Error())
	assert.Equal(t, composespec.ViolationCode, err.Body().Code)
}

func TestInspectLimits_MemoryFromTheFile(t *testing.T) {
	src := composespec.Source{
		Root: inlineRoot, ProjectName: "paas-test-12345678",
		Content: `services:
  db:
    image: postgres
    mem_limit: 512m
  app:
    image: app
    deploy:
      resources:
        limits:
          memory: 1g
  worker:
    image: worker
  cache:
    image: redis
    mem_limit: 256m
    deploy:
      resources:
        limits:
          memory: 256m
  off:
    image: x
    profiles: [never]
    mem_limit: 64m
`,
	}
	// compose-go v2.15.0 refuses distinct values for both, so the brief's
	// 256m vs 128m cannot load: that file is the compose-invalid path.
	distinct := src
	distinct.Content = "services:\n  cache:\n    image: redis\n    mem_limit: 256m\n    deploy: {resources: {limits: {memory: 128m}}}\n"
	dvs, _, dlimits, _ := composespec.InspectLimits(context.Background(), distinct)
	assert.Equal(t, []string{"compose_invalid"}, rulesOf(dvs))
	assert.Nil(t, dlimits)

	// Equal values load; compose runs the service with mem_limit.
	p, _, err := composespec.Load(context.Background(), src)
	require.NoError(t, err)
	t.Logf("cache: MemLimit=%d deploy.limits.memory=%d", p.Services["cache"].MemLimit, p.Services["cache"].Deploy.Resources.Limits.MemoryBytes)
	require.EqualValues(t, 256<<20, p.Services["cache"].MemLimit, "compose runs the cache with mem_limit")

	vs, services, limits, deployLimits := composespec.InspectLimits(context.Background(), src)
	assert.Empty(t, vs)
	assert.Equal(t, []string{"app", "cache", "db", "off", "worker"}, services)
	assert.Equal(t, map[string]int64{"db": 512, "app": 1024, "worker": 0, "cache": 256, "off": 64}, limits)
	assert.Equal(t, map[string]bool{"app": true, "cache": true}, deployLimits,
		"the services that declare deploy.resources.limits; mem_limit alone (db, off) is not one")

	_, svcs := composespec.Inspect(context.Background(), src)
	assert.Equal(t, services, svcs)
}
