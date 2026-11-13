package server

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/hoststats"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runningTraefik is a server whose paas-traefik is up, started with the
// label and mounts given.
func runningTraefik(label, mounts string) *fakeShell {
	return &fakeShell{answer: func(cmd string) string {
		switch {
		case strings.HasPrefix(cmd, "docker ps --filter name=paas-traefik"):
			return "Up 3 days"
		case strings.HasPrefix(cmd, "docker container inspect"):
			return label + "|" + mounts
		}
		return ""
	}}
}

func TestEnsureTraefikLeavesTheSameConfigurationAlone(t *testing.T) {
	_, hash, err := traefikConfig("ops@example.com", nil, false)
	require.NoError(t, err)
	sh := runningTraefik(hash, traefikAcmeVolume+",")
	var lines []string
	require.NoError(t, ensureTraefik(sh, "ops@example.com", nil, nil, func(l string) { lines = append(lines, l) }))
	for _, m := range mutating {
		assert.False(t, issued(sh.cmds, m), "must not run %q", m)
	}
	assert.Equal(t, []string{"Traefik already running: Up 3 days"}, lines)
}

// Saving a DNS provider and running Provision on a server whose Traefik
// runs must give Traefik the DNS-01 resolver (a pilot's wildcard steps).
func TestEnsureTraefikRecreatesItsOwnTraefikWhenTheConfigurationChanged(t *testing.T) {
	_, before, err := traefikConfig("ops@example.com", nil, false)
	require.NoError(t, err)
	for name, label := range map[string]string{"a DNS provider was added": before, "started before the label existed": ""} {
		t.Run(name, func(t *testing.T) {
			sh := runningTraefik(label, traefikAcmeVolume+",paas-traefik-logs,")
			dns := &DNSConfig{Provider: "cloudflare", Credentials: map[string]string{"CF_DNS_API_TOKEN": "tok-s3cret"}}
			var lines []string
			require.NoError(t, ensureTraefik(sh, "ops@example.com", dns, nil, func(l string) { lines = append(lines, l) }))

			assert.Contains(t, lines, "Traefik's configuration changed: restarting it, so this server's routes pause for a few seconds (certificates are kept)")
			_, after, _ := traefikConfig("ops@example.com", dns, false)
			order := []string{"cat > /opt/paas-traefik/dynamic.yml", "umask 077 && cat > " + dnsDir + "/CF_DNS_API_TOKEN",
				"docker rm -f paas-traefik", "--label " + traefikConfigLabel + "=" + after}
			at := -1
			for _, want := range order {
				i := indexOf(sh.cmds, want)
				require.GreaterOrEqual(t, i, 0, "%q is run", want)
				assert.Greater(t, i, at, "%q runs after the step before it: the old Traefik serves until its files are written", want)
				at = i
			}
			run := sh.cmds[indexOf(sh.cmds, "docker run -d")]
			assert.Contains(t, run, "dnschallenge.provider=cloudflare")
			assert.Contains(t, run, "--entrypoints.web.http.redirections.entrypoint.priority=1", "a service's own HTTP router wins over the redirect (D-150)")
			assert.NotContains(t, run, "s3cret")
		})
	}
}

// Provisioning a server whose Traefik reads the agent's files, while the
// agent can't write them (frozen after a restore), leaves Traefik on the
// files rather than restarting it onto the labels.
func TestEnsureTraefikKeepsTheFilesWhenTheAgentCantWriteThem(t *testing.T) {
	_, hash, err := traefikConfig("ops@example.com", nil, true)
	require.NoError(t, err)
	sh := runningTraefik(hash, traefikAcmeVolume+",")
	answer := sh.answer
	sh.answer = func(cmd string) string {
		if strings.Contains(cmd, "{{json .Args}}") {
			return `["` + traefikFileFlag + `/etc/traefik/dynamic"]`
		}
		return answer(cmd)
	}
	frozen := func() (int, error) { return 0, errors.New("epoch_frozen: the panel was restored from a backup") }
	var lines []string
	require.NoError(t, ensureTraefik(sh, "ops@example.com", nil, frozen, func(l string) { lines = append(lines, l) }))
	for _, m := range mutating {
		assert.False(t, issued(sh.cmds, m), "must not run %q", m)
	}
	assert.Contains(t, lines[0], "goes on reading the files it wrote before")
}

func TestEnsureTraefikNeverChangesATraefikItAdopted(t *testing.T) {
	sh := runningTraefik("", "traefik-certs,")
	var lines []string
	dns := &DNSConfig{Provider: "cloudflare", Credentials: map[string]string{"CF_DNS_API_TOKEN": "tok"}}
	require.NoError(t, ensureTraefik(sh, "ops@example.com", dns, nil, func(l string) { lines = append(lines, l) }))
	for _, m := range mutating {
		assert.False(t, issued(sh.cmds, m), "must not run %q", m)
	}
	assert.False(t, issued(sh.cmds, "rm -rf "+dnsDir))
	require.Len(t, lines, 1)
	assert.Contains(t, lines[0], "Toft did not start it")
}

func TestTraefikConfigHash(t *testing.T) {
	dns := func(token string) *DNSConfig {
		return &DNSConfig{Provider: "cloudflare", Credentials: map[string]string{"CF_DNS_API_TOKEN": token}}
	}
	_, a, _ := traefikConfig("ops@example.com", dns("one"), false)
	_, same, _ := traefikConfig("ops@example.com", dns("one"), false)
	_, rotated, _ := traefikConfig("ops@example.com", dns("two"), false)
	_, other, _ := traefikConfig("admin@example.com", dns("one"), false)
	_, none, _ := traefikConfig("ops@example.com", nil, false)
	assert.Equal(t, a, same)
	assert.NotEqual(t, a, rotated, "a rotated credential restarts Traefik, which reads it at start")
	assert.NotEqual(t, a, other)
	assert.NotEqual(t, a, none)

	cmd, _, err := traefikConfig("ops@example.com", dns("s3cret"), false)
	require.NoError(t, err)
	assert.NotContains(t, cmd, "s3cret")
	assert.NotContains(t, cmd, "--ping", "nothing uses ping on a remote server, and it would answer every app network")

	_, _, err = traefikConfig("ops@example.com", &DNSConfig{Provider: "bad provider;", Credentials: map[string]string{"X": "y"}}, false)
	assert.Error(t, err)
}

// Where the agent writes the routes (D-113), Provision has it write every
// service's file before Traefik is recreated to read them, without the
// Docker socket, and with toft.yml renamed into place; once it runs that
// way it is left alone. When the agent can't write them, Traefik keeps
// reading the containers' labels.
func TestEnsureTraefikSwitchesToTheAgentsRouteFiles(t *testing.T) {
	_, labels, err := traefikConfig("ops@example.com", nil, false)
	require.NoError(t, err)
	_, files, err := traefikConfig("ops@example.com", nil, true)
	require.NoError(t, err)

	sh := runningTraefik(labels, traefikAcmeVolume+",")
	synced := -1
	sync := func() (int, error) { synced = len(sh.cmds); return 6, nil }
	var lines []string
	require.NoError(t, ensureTraefik(sh, "ops@example.com", nil, sync, func(l string) { lines = append(lines, l) }))
	assert.Contains(t, lines, "The agent wrote the routes of 6 services; Traefik reads them from its files")
	rm := indexOf(sh.cmds, "docker rm -f paas-traefik")
	require.GreaterOrEqual(t, synced, 0)
	assert.Less(t, synced, rm, "the files are written before the Traefik reading labels stops")
	assert.Less(t, indexOf(sh.cmds, "mv -f "+traefikRoutesDir+"/.toft.yml.tmp "+traefikRoutesDir+"/toft.yml"), rm)
	run := sh.cmds[indexOf(sh.cmds, "docker run -d")]
	for _, want := range []string{"--label " + traefikConfigLabel + "=" + files, "--providers.file.directory=/etc/traefik/dynamic", "-p 127.0.0.1::8082", "--entrypoints.toft-api.address=:8082"} {
		assert.Contains(t, run, want)
	}
	for _, not := range []string{"docker.sock", "--providers.docker", "0.0.0.0:8082", "-p 8082"} {
		assert.NotContains(t, run, not)
	}

	sh = runningTraefik(files, traefikAcmeVolume+",")
	lines = nil
	require.NoError(t, ensureTraefik(sh, "ops@example.com", nil, sync, func(l string) { lines = append(lines, l) }))
	for _, m := range mutating {
		assert.False(t, issued(sh.cmds, m), "must not run %q", m)
	}

	sh = runningTraefik(files, traefikAcmeVolume+",")
	lines = nil
	failed := func() (int, error) { return 0, assert.AnError }
	require.NoError(t, ensureTraefik(sh, "ops@example.com", nil, failed, func(l string) { lines = append(lines, l) }))
	assert.Contains(t, sh.cmds[indexOf(sh.cmds, "docker run -d")], "--label "+traefikConfigLabel+"="+labels)
	assert.Contains(t, strings.Join(lines, "\n"), "Traefik goes on reading them from the containers' labels")
}

// Traefik's access log goes to its container's log, which Docker's local
// driver caps as it does every app's (rollout.LocalLogConfig): a file on
// the host grew without bound, and nothing rotated it.
func TestTraefikLogsAreCapped(t *testing.T) {
	for _, files := range []bool{false, true} {
		cmd, _, err := traefikConfig("ops@example.com", nil, files)
		require.NoError(t, err)
		assert.Contains(t, cmd, "--log-driver local --log-opt max-size=20m --log-opt max-file=5")
		assert.Contains(t, cmd, "--accesslog=true")
		assert.NotContains(t, cmd, "accesslog.filepath")
		assert.NotContains(t, cmd, ":/logs")
	}
}

// Traefik keeps each service's request durations in LatencyBounds'
// buckets (D-156), on an entrypoint only its own container reaches: bound
// to the container's loopback, never published, and with Traefik's own
// prometheus@internal router on that entrypoint alone. These fail if the
// page ever becomes reachable from the web, an app network or the host.
func TestTraefikMetricsAreOnTheContainersLoopbackOnly(t *testing.T) {
	for _, files := range []bool{false, true} {
		cmd, _, err := traefikConfig("ops@example.com", nil, files)
		require.NoError(t, err)
		for _, flag := range []string{
			"--metrics.prometheus=true",
			"--metrics.prometheus.entrypoint=metrics",
			"--metrics.prometheus.addServicesLabels=true",
			"--metrics.prometheus.buckets=" + hoststats.LatencyBounds,
			"--entrypoints.metrics.address=127.0.0.1:" + hoststats.LatencyPort + " ",
		} {
			assert.Contains(t, cmd, flag, "files=%v", files)
		}
		assert.Equal(t, traefikMetricsEntrypoint, "--entrypoints.metrics.address=127.0.0.1:"+hoststats.LatencyPort)

		// Published: only 80, 443 and, where the agent writes the routes,
		// the API's port on the host's loopback. Nothing on the host's
		// network either.
		published := regexp.MustCompile(`(?m)(?:^|\s)(?:-p|--publish|-P|--publish-all)(?:[ =](\S+))?`).FindAllStringSubmatch(cmd, -1)
		var ports []string
		for _, p := range published {
			ports = append(ports, p[1])
		}
		want := []string{"80:80", "443:443"}
		if files {
			want = []string{"80:80", "443:443", "127.0.0.1::8082"}
		}
		assert.ElementsMatch(t, want, ports, "files=%v", files)
		assert.NotContains(t, cmd, hoststats.LatencyPort+":")
		assert.NotContains(t, cmd, "--network host")
		assert.NotContains(t, cmd, "--net=host")

		// Every entrypoint's address: the metrics one is the only one on
		// the container's loopback, and the web ones are not it.
		addrs := map[string]string{}
		for _, m := range regexp.MustCompile(`--entrypoints\.([a-z-]+)\.address=(\S+)`).FindAllStringSubmatch(cmd, -1) {
			addrs[m[1]] = m[2]
		}
		wantAddrs := map[string]string{"web": ":80", "websecure": ":443", "metrics": "127.0.0.1:" + hoststats.LatencyPort}
		if files {
			wantAddrs["toft-api"] = ":8082"
		}
		assert.Equal(t, wantAddrs, addrs, "files=%v", files)

		// Without manual routing Traefik routes prometheus@internal on the
		// metrics entrypoint only; with it, a router of Toft's would have to.
		// No router Toft writes names it.
		assert.NotContains(t, cmd, "manualrouting")
		assert.NotContains(t, strings.ToLower(cmd), "prometheus@internal")
		assert.NotContains(t, traefikDynamicConfig, "@internal")
		assert.NotContains(t, cmd, "--metrics.prometheus.entrypoint=web")
		assert.NotContains(t, cmd, "--metrics.prometheus.entrypoint=websecure")
	}
}

// Provisioning removes the old access log once Traefik runs without it.
func TestStartTraefikRemovesTheOldAccessLog(t *testing.T) {
	sc := &fakeShell{answer: func(string) string { return "" }}
	cmd, _, err := traefikConfig("ops@example.com", nil, false)
	require.NoError(t, err)
	require.NoError(t, startTraefik(sc, cmd, nil, false, func(string) {}))
	last := sc.cmds[len(sc.cmds)-1]
	assert.Equal(t, "rm -rf /opt/paas-traefik/logs", last, "after Traefik started: %q", sc.cmds)
	for _, c := range sc.cmds {
		assert.NotContains(t, c, "mkdir -p /opt/paas-traefik/logs")
	}
}
