package docker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

const testServiceID = "0a1b2c3d-1111-2222-3333-444455556666"

// ─── The environment docker compose runs with ────────────────────────────────

func TestRemoteCompose_ExactCommand(t *testing.T) {
	got := remoteCompose("--project-directory '/tmp/paas-compose-Ab12' -f '/tmp/paas-compose-Ab12/docker-compose.yml' --project-name 'paas-web-0a1b2c3d' up -d 2>&1")
	want := `env -i PATH="$PATH" HOME="$HOME" ${DOCKER_HOST:+"DOCKER_HOST=$DOCKER_HOST"} ${XDG_RUNTIME_DIR:+"XDG_RUNTIME_DIR=$XDG_RUNTIME_DIR"} ` +
		`docker compose --project-directory '/tmp/paas-compose-Ab12' -f '/tmp/paas-compose-Ab12/docker-compose.yml' --project-name 'paas-web-0a1b2c3d' up -d 2>&1`
	assert.Equal(t, want, got)
}

// The prefix, run by a real POSIX shell with an SSH-like environment, leaves
// exactly PATH, HOME and (when set) DOCKER_HOST and XDG_RUNTIME_DIR.
func TestRemoteCleanEnv_InAShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	envBin, err := exec.LookPath("env")
	if err != nil {
		t.Skip("no env")
	}
	run := func(t *testing.T, sessionEnv []string) []string {
		t.Helper()
		cmd := exec.Command(sh, "-c", remoteCleanEnv(envBin))
		cmd.Env = sessionEnv
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		sort.Strings(lines)
		return lines
	}
	sshSession := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/root", "USER=root", "SHELL=/bin/bash", "LANG=C.UTF-8",
		"SSH_CONNECTION=10.0.0.1 5000 10.0.0.2 22", "MAIL=/var/mail/root", "PROFILE_EXPORT=from-bashrc",
	}

	t.Run("without docker variables", func(t *testing.T) {
		assert.Equal(t, []string{"HOME=/root", "PATH=/usr/local/bin:/usr/bin:/bin"}, run(t, sshSession))
	})
	t.Run("empty docker variables are left out", func(t *testing.T) {
		assert.Equal(t, []string{"HOME=/root", "PATH=/usr/local/bin:/usr/bin:/bin"},
			run(t, append(sshSession, "DOCKER_HOST=", "XDG_RUNTIME_DIR=")))
	})
	t.Run("with docker variables, one with a space", func(t *testing.T) {
		got := run(t, append(sshSession, "DOCKER_HOST=unix:///run/user/1000/docker.sock", "XDG_RUNTIME_DIR=/run/user/1000 x"))
		assert.Equal(t, []string{
			"DOCKER_HOST=unix:///run/user/1000/docker.sock",
			"HOME=/root",
			"PATH=/usr/local/bin:/usr/bin:/bin",
			"XDG_RUNTIME_DIR=/run/user/1000 x",
		}, got)
	})
}

func TestComposeProcessEnv_OnlyPlatformBasicsAndServiceVars(t *testing.T) {
	backend := map[string]string{
		"PATH": "/usr/bin", "HOME": "/root", "DOCKER_HOST": "unix:///var/run/docker.sock",
		"JWT_SECRET": "s3cret", "DATABASE_URL": "postgres://panel", "ENCRYPTION_KEY": "k",
	}
	getenv := func(k string) string { return backend[k] }
	got := composeProcessEnv(getenv, map[string]string{"B": "2", "A": "1"})
	assert.Equal(t, []string{"PATH=/usr/bin", "HOME=/root", "DOCKER_HOST=unix:///var/run/docker.sock", "A=1", "B=2"}, got)

	empty := composeProcessEnv(func(string) string { return "" }, nil)
	assert.NotNil(t, empty, "a nil Env would make exec inherit the backend's environment")
	assert.Empty(t, empty)
}

func TestLocalCompose_NeverInheritsTheBackendEnvironment(t *testing.T) {
	t.Setenv("JWT_SECRET", "panel-secret")
	t.Setenv("HOME", "/home/panel")
	cmd := localCompose(context.Background(), t.TempDir(), nil, "version")
	require.NotNil(t, cmd.Env)
	assert.NotContains(t, strings.Join(cmd.Env, "\n"), "panel-secret")
	assert.Contains(t, cmd.Env, "HOME=/home/panel")
	assert.Equal(t, []string{"compose", "version"}, cmd.Args[1:])
}

// ─── The routing override ────────────────────────────────────────────────────

func testRoute(name string) *deployment.ComposeRoute {
	return &deployment.ComposeRoute{
		Service: name,
		Port:    8080,
		Domains: []string{"app.example.com", "www.example.com"},
		Access:  service.AccessControl{BasicAuthUser: "admin", BasicAuthPassword: "pw", IPAllowlist: []string{"10.0.0.0/8", "192.168.1.1/32"}},
	}
}

type overrideDoc struct {
	Services map[string]struct {
		Labels   map[string]string         `yaml:"labels"`
		Networks map[string]map[string]any `yaml:"networks"`
	} `yaml:"services"`
	Networks map[string]map[string]any `yaml:"networks"`
}

func TestComposeRouteOverride_Labels(t *testing.T) {
	out, err := composeRouteOverride(testServiceID, "paas-network", "paas-network", testRoute("web"))
	require.NoError(t, err)

	var doc overrideDoc
	require.NoError(t, yaml.Unmarshal(out, &doc))
	require.Len(t, doc.Services, 1)
	web, ok := doc.Services["web"]
	require.True(t, ok)
	l := web.Labels

	assert.Equal(t, "true", l["traefik.enable"])
	assert.Equal(t, "paas-network", l["traefik.docker.network"])
	assert.Equal(t, "8080", l["traefik.http.services.paas-svc-0a1b2c3d.loadbalancer.server.port"])
	for i, host := range []string{"app.example.com", "www.example.com"} {
		https := "traefik.http.routers.paas-0a1b2c3d-" + string(rune('0'+i))
		assert.Equal(t, "Host(`"+host+"`)", l[https+".rule"])
		assert.Equal(t, "websecure", l[https+".entrypoints"])
		assert.Equal(t, "letsencrypt", l[https+".tls.certresolver"])
		assert.Equal(t, "secure-headers@file,paas-0a1b2c3d-basicauth,paas-0a1b2c3d-ipallow", l[https+".middlewares"])
		assert.Equal(t, "paas-svc-0a1b2c3d", l[https+".service"], "explicit service on every router")
		assert.Equal(t, "Host(`"+host+"`)", l[https+"-http.rule"])
		assert.Equal(t, "web", l[https+"-http.entrypoints"])
		assert.Equal(t, "paas-svc-0a1b2c3d", l[https+"-http.service"], "explicit service on every router")
	}
	assert.Equal(t, "10.0.0.0/8,192.168.1.1/32", l["traefik.http.middlewares.paas-0a1b2c3d-ipallow.ipallowlist.sourcerange"])

	// The bcrypt hash is written with every "$" doubled.
	users := l["traefik.http.middlewares.paas-0a1b2c3d-basicauth.basicauth.users"]
	require.True(t, strings.HasPrefix(users, "admin:$$2a$$"), users)
	assert.NotRegexp(t, `[^$]\$[^$]|[^$]\$$`, users, "no single $ left")
	for key, val := range l {
		assert.NotRegexp(t, `(^|[^$])\$([^$]|$)`, strings.ReplaceAll(val, "$$", ""), key)
	}

	assert.Equal(t, map[string]map[string]any{"paas-network": {}, "default": {}}, web.Networks)
	assert.Equal(t, map[string]map[string]any{"paas-network": {"external": true, "name": "paas-network"}}, doc.Networks)
}

func TestComposeRouteOverride_ComposeLoadsItAsTheSameValues(t *testing.T) {
	for _, name := range []string{"web", "web.v2", "api_1", "1.2", "on", "null"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			base, err := yaml.Marshal(map[string]any{
				"services": map[string]any{
					name:     map[string]any{"image": "nginx", "labels": []string{"app.tier=front"}},
					"worker": map[string]any{"image": "busybox"},
				},
			})
			require.NoError(t, err)
			override, err := composeRouteOverride(testServiceID, "paas-network", "paas-network", testRoute(name))
			require.NoError(t, err)

			project, err := loader.LoadWithContext(context.Background(), types.ConfigDetails{
				WorkingDir: dir,
				ConfigFiles: []types.ConfigFile{
					{Filename: filepath.Join(dir, "docker-compose.yml"), Content: base},
					{Filename: filepath.Join(dir, "docker-compose.route.yml"), Content: override},
				},
				// Interpolation sees nothing that could stand in for "$2a".
				Environment: map[string]string{"2a": "X", "2y": "X"},
			}, func(o *loader.Options) {
				o.SetProjectName("paas-test-0a1b2c3d", true)
				o.SkipResolveEnvironment = true
			})
			require.NoError(t, err)

			require.Len(t, project.Services, 2, "the override adds to the user's service; it never creates one")
			svc, ok := project.Services[name]
			require.True(t, ok, "service %q keeps its name as a map key", name)
			assert.Equal(t, "nginx", svc.Image)
			assert.Equal(t, "front", svc.Labels["app.tier"], "the user's own labels stay")
			assert.Equal(t, "paas-network", svc.Labels["traefik.docker.network"])

			users := svc.Labels["traefik.http.middlewares.paas-0a1b2c3d-basicauth.basicauth.users"]
			hash, found := strings.CutPrefix(users, "admin:")
			require.True(t, found, users)
			require.NoError(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte("pw")), "the hash survives interpolation intact")

			assert.Contains(t, svc.Networks, "paas-network")
			assert.Contains(t, svc.Networks, "default")
			nw, ok := project.Networks["paas-network"]
			require.True(t, ok)
			assert.True(t, bool(nw.External))
			assert.Equal(t, "paas-network", nw.Name)

			other := project.Services["worker"]
			assert.Empty(t, other.Labels, "other services get nothing")
		})
	}
}

func TestComposeRouteOverride_Refuses(t *testing.T) {
	for _, name := range []string{"", "-web", "web: {privileged: true}", "${HOME}", "a b", "web\nx", "we'b", strings.Repeat("a", 64)} {
		_, err := composeRouteOverride(testServiceID, "paas-network", "paas-network", testRoute(name))
		assert.Error(t, err, "%q", name)
	}
	r := testRoute("web")
	r.Port = 0
	_, err := composeRouteOverride(testServiceID, "paas-network", "paas-network", r)
	assert.Error(t, err)
	r = testRoute("web")
	r.Domains = nil
	_, err = composeRouteOverride(testServiceID, "paas-network", "paas-network", r)
	assert.Error(t, err)
	_, err = composeRouteOverride("short", "paas-network", "paas-network", testRoute("web"))
	assert.Error(t, err)
	_, err = composeRouteOverride(testServiceID, "paas-network", "paas-network", nil)
	assert.Error(t, err)
}

func TestComposeRouteOverride_NoAccessControl(t *testing.T) {
	r := testRoute("web")
	r.Access = service.AccessControl{}
	out, err := composeRouteOverride(testServiceID, "", "", r)
	require.NoError(t, err)
	assert.NotContains(t, string(out), "$")
	var doc overrideDoc
	require.NoError(t, yaml.Unmarshal(out, &doc))
	assert.Equal(t, "secure-headers@file", doc.Services["web"].Labels["traefik.http.routers.paas-0a1b2c3d-0.middlewares"])
	assert.Equal(t, "paas-network", doc.Services["web"].Labels["traefik.docker.network"], "the default network")
}

func TestCopyRepoLeavesOutGitAndKeepsModesAndLinks(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	must := func(err error) {
		t.Helper()
		require.NoError(t, err)
	}
	must(os.MkdirAll(filepath.Join(src, ".git"), 0o755))
	must(os.WriteFile(filepath.Join(src, ".git", "config"), []byte("url = https://u:token@host/r.git"), 0o644))
	must(os.MkdirAll(filepath.Join(src, "nginx"), 0o755))
	must(os.WriteFile(filepath.Join(src, "nginx", "nginx.conf"), []byte("events {}"), 0o644))
	must(os.WriteFile(filepath.Join(src, "run.sh"), []byte("#!/bin/sh"), 0o755))
	must(os.Symlink("nginx/nginx.conf", filepath.Join(src, "current.conf")))

	must(copyRepo(src, dst))

	_, err := os.Stat(filepath.Join(dst, ".git"))
	assert.True(t, os.IsNotExist(err), ".git is not copied")
	b, err := os.ReadFile(filepath.Join(dst, "nginx", "nginx.conf"))
	must(err)
	assert.Equal(t, "events {}", string(b))
	info, err := os.Stat(filepath.Join(dst, "run.sh"))
	must(err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm(), "modes are kept")
	link, err := os.Readlink(filepath.Join(dst, "current.conf"))
	must(err)
	assert.Equal(t, "nginx/nginx.conf", link, "symlinks are copied as links")
}

func TestLocalComposeReleases(t *testing.T) {
	o := &LocalOrchestrator{dataDir: t.TempDir(), log: zap.NewNop()}
	first, err := o.newComposeRelease("svc-1")
	require.NoError(t, err)
	second, err := o.newComposeRelease("svc-1")
	require.NoError(t, err)
	assert.NotEqual(t, first, second, "a new directory per deploy")
	assert.Equal(t, filepath.Join(o.dataDir, "compose", "svc-1"), filepath.Dir(second))
	info, err := os.Stat(second)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())

	o.pruneComposeReleases("svc-1", second)
	_, err = os.Stat(first)
	assert.True(t, os.IsNotExist(err), "older releases are removed")
	_, err = os.Stat(second)
	assert.NoError(t, err, "the current one stays")

	_, err = (&LocalOrchestrator{}).newComposeRelease("svc-1")
	assert.Error(t, err, "no data dir: refuse rather than write somewhere relative")
}
