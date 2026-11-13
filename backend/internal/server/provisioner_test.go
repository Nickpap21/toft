package server

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeShell records every command and answers through answer ("" and no
// error when answer is nil or returns "").
type fakeShell struct {
	answer func(cmd string) string
	cmds   []string
	inputs []string // stdin of each ExecInput
	fails  func(cmd string) bool
}

// goodHost is a server that meets D-29 with no Docker yet.
const goodHost = "os:Ubuntu 24.04.1 LTS\ndistro:ubuntu debian\nuid:0 root\narch:x86_64\nkernel:6.8.0-45-generic\nsystemd:systemd 255 (255.4-1ubuntu8)\ncgroup:cgroup2fs\ndockercli:\ndocker:\nlisten:\npublished:\n"

func (f *fakeShell) Exec(cmd string) (string, error) {
	f.cmds = append(f.cmds, cmd)
	var out string
	if f.answer != nil {
		out = f.answer(cmd)
	}
	// A fake host meets the requirements unless its answer says otherwise.
	if out == "" && strings.HasPrefix(cmd, requirementsMarker) {
		out = goodHost
	}
	// Docker's install installs it, unless its answer says otherwise.
	if out == "" && strings.HasPrefix(cmd, "docker --version") && issued(f.cmds, "get.docker.com") {
		out = "Docker version 29.1.0, build 1234567"
	}
	if f.fails != nil && f.fails(cmd) {
		return out, errors.New("exit status 1")
	}
	return out, nil
}

// ExecInput records the command and what was sent on stdin.
func (f *fakeShell) ExecInput(cmd string, stdin io.Reader) (string, error) {
	in, _ := io.ReadAll(stdin)
	f.inputs = append(f.inputs, string(in))
	return f.Exec(cmd)
}

func (f *fakeShell) ExecStream(cmd string, logFn func(string)) error {
	out, err := f.Exec(cmd)
	if out != "" {
		logFn(out)
	}
	return err
}

// mutating are the commands that change a server. A host provisioned before
// must see none of them again (AGENTS.md: the provisioner stays idempotent).
var mutating = []string{
	"get.docker.com",
	"docker plugin install",
	"docker plugin rm",
	"docker network create",
	"docker network connect",
	"docker rename",
	"docker rm -f",
	"docker run",
	"cat > /opt/paas-traefik",
	"mkdir -p /opt/paas-traefik",
	"apt-get install",
	"ufw allow",
	"ufw default",
	"ufw --force enable",
	"fail2ban/jail.d",
	"> /etc/docker/daemon.json",
	"systemctl restart docker",
	"cat > " + hostFirewallRulesPath,
	"cat > " + hostFirewallUnitPath,
	"systemctl daemon-reload",
	"systemctl enable toft-firewall",
	"systemctl restart toft-firewall",
	"cat > " + backupScriptPath,
	"cat > " + backupServicePath,
	"cat > " + backupTimerPath,
	"systemctl enable --now toft-backup.timer",
}

func issued(cmds []string, fragment string) bool {
	for _, c := range cmds {
		if strings.Contains(c, fragment) {
			return true
		}
	}
	return false
}

func TestProvisionFreshHostRunsEverySetupStep(t *testing.T) {
	// Nothing installed: every check answers "", but what was set up shows
	// as it is.
	sh := &fakeShell{answer: func(cmd string) string {
		switch {
		case strings.HasPrefix(cmd, "LC_ALL=C ufw status"):
			return "Status: active"
		case strings.HasPrefix(cmd, "nft list table ip toft"):
			return "loaded"
		}
		return ""
	}}
	var lines []string
	warnings, err := provision(sh, provisionOpts{ACMEEmail: "ops@example.com", SSHPort: 2222}, func(l string) { lines = append(lines, l) })
	require.NoError(t, err)
	assert.Empty(t, warnings)

	for _, m := range mutating {
		switch m {
		case "docker network connect", "docker rename":
			continue // only when adopting an existing Traefik
		case "> /etc/docker/daemon.json", "systemctl restart docker":
			continue // only under isolated app networks
		case "docker plugin rm":
			continue // only on agent servers
		}
		assert.True(t, issued(sh.cmds, m), "fresh host should run %q", m)
	}
	assert.True(t, issued(sh.cmds, "ufw allow 2222/tcp"), "the SSH port stays open")
	assert.True(t, issued(sh.cmds, "--certificatesresolvers.letsencrypt.acme.email=ops@example.com"))
	assert.Equal(t, "Server provisioned successfully!", lines[len(lines)-1])
}

func TestProvisionIsIdempotentOnAProvisionedHost(t *testing.T) {
	host := &provisionedHost{}
	sh := &fakeShell{answer: host.answer}
	var lines []string
	warnings, err := provision(sh, provisionOpts{ACMEEmail: "ops@example.com", SSHPort: 22}, func(l string) { lines = append(lines, l) })
	require.NoError(t, err)
	assert.Empty(t, warnings)

	for _, m := range mutating {
		assert.False(t, issued(sh.cmds, m), "a second run must not run %q", m)
	}
	assert.Contains(t, lines, "Docker already installed: Docker version 29.1.0, build 1234567")
	assert.Contains(t, lines, "paas-network already exists")
	assert.Contains(t, lines, "Traefik already running: Up 3 days")
	assert.Contains(t, lines, "Firewall already active — leaving existing rules untouched.")
	assert.Contains(t, lines, "Server provisioned successfully!")
}

// On an agent server (D-113) the Loki plugin is never installed, and goes
// only once no container, running or stopped, logs to it; without it
// there is nothing to do, so a second run changes nothing.
func TestProvisionAgentServerRemovesTheLokiPluginOnceUnused(t *testing.T) {
	opts := provisionOpts{ACMEEmail: "ops@example.com", SSHPort: 22, LocalLogs: true}
	run := func(plugin, drivers string) ([]string, []string) {
		host := &provisionedHost{}
		sh := &fakeShell{answer: func(cmd string) string {
			switch {
			case strings.HasPrefix(cmd, "docker plugin ls"):
				return plugin
			case cmd == lokiDriverUsersCmd:
				return drivers
			}
			return host.answer(cmd)
		}}
		var lines []string
		_, err := provision(sh, opts, func(l string) { lines = append(lines, l) })
		require.NoError(t, err)
		return sh.cmds, lines
	}

	cmds, lines := run("loki:latest", "/paas-web-blue loki\n/paas-api-blue local\n")
	assert.Contains(t, cmds, lokiDriverUsersCmd)
	assert.False(t, issued(cmds, "docker plugin rm"), "a container still logs to it")
	assert.Contains(t, lines, "Loki Docker log driver kept until no container logs to it (1 do); each moves to the agent's shipping when next deployed")

	cmds, _ = run("loki:latest", "/paas-web-blue local\n/old-thing json-file\n")
	assert.Contains(t, cmds, "docker plugin rm -f loki 2>&1")

	cmds, _ = run("", "")
	for _, m := range mutating {
		assert.False(t, issued(cmds, m), "an agent server without the plugin must not run %q", m)
	}
	assert.NotContains(t, cmds, lokiDriverUsersCmd)

	fresh := &fakeShell{}
	_, err := provision(fresh, opts, func(string) {})
	require.NoError(t, err)
	assert.False(t, issued(fresh.cmds, "docker plugin install"), "an agent server never gets the plugin")
}

// A host whose ufw refuses (the drill's non-root user): the run goes on and
// ends with a warning naming the firewall, never the success line.
func TestProvisionFinishesWithWarningsWhenUfwFails(t *testing.T) {
	host := &provisionedHost{}
	sh := &fakeShell{
		answer: func(cmd string) string {
			if strings.HasPrefix(cmd, "ufw status") {
				return "" // ufw prints nothing to a non-root user
			}
			return host.answer(cmd)
		},
		fails: func(cmd string) bool { return strings.HasPrefix(cmd, "ufw ") },
	}
	var lines []string
	warnings, err := provision(sh, provisionOpts{ACMEEmail: "ops@example.com", SSHPort: 22}, func(l string) { lines = append(lines, l) })
	require.NoError(t, err)
	assert.Equal(t, []string{"the firewall (ufw, fail2ban)"}, warnings)
	assert.True(t, containsLine(lines, "Warning: firewall setup failed (non-fatal): ufw allow 22/tcp: exit status 1"))
	assert.NotContains(t, lines, "Server provisioned successfully!")
	assert.Equal(t, "Server provisioned with warnings, missing the firewall (ufw, fail2ban): fix what the warnings above say, then Re-provision", lines[len(lines)-1])

	// Both firewalls missing: both named.
	both := &fakeShell{answer: sh.answer, fails: func(cmd string) bool { return strings.HasPrefix(cmd, "ufw ") || strings.HasPrefix(cmd, "nft -c") }}
	warnings, err = provision(both, provisionOpts{ACMEEmail: "ops@example.com", SSHPort: 22}, func(string) {})
	require.NoError(t, err)
	assert.Equal(t, []string{"the firewall (ufw, fail2ban)", "the host firewall for app containers"}, warnings)
}

// Backups and automatic security updates that couldn't be set up are
// warnings too: a server quietly without patches or backups isn't done.
func TestProvisionFinishesWithWarningsWhenBackupsOrUpdatesFail(t *testing.T) {
	host := &provisionedHost{}
	sh := &fakeShell{
		answer: func(cmd string) string {
			if strings.HasPrefix(cmd, "command -v unattended-upgrade") {
				return "no"
			}
			return host.answer(cmd)
		},
		fails: func(cmd string) bool {
			return strings.Contains(cmd, "unattended-upgrades") || strings.Contains(cmd, backupEnvPath)
		},
	}
	var lines []string
	warnings, err := provision(sh, provisionOpts{ACMEEmail: "ops@example.com", SSHPort: 22}, func(l string) { lines = append(lines, l) })
	require.NoError(t, err)
	assert.Equal(t, []string{"nightly backups", "automatic security updates"}, warnings)
	assert.NotContains(t, lines, "Server provisioned successfully!")
	assert.Equal(t, "Server provisioned with warnings, missing nightly backups and automatic security updates: fix what the warnings above say, then Re-provision", lines[len(lines)-1])
	assert.Equal(t, "a, b and c", missingList([]string{"a", "b", "c"}))
}

// ufw's commands exit 0 but it isn't active: checked, not trusted.
func TestEnsureFirewallFailsWhenUfwIsNotActiveAfterEnable(t *testing.T) {
	sh := &fakeShell{answer: func(cmd string) string {
		switch {
		case strings.HasPrefix(cmd, "command -v ufw"):
			return "yes"
		case strings.Contains(cmd, "ufw status"):
			return "Status: inactive"
		}
		return ""
	}}
	err := ensureFirewall(sh, 22, func(string) {})
	require.ErrorContains(t, err, "ufw is not active")
	assert.True(t, issued(sh.cmds, "ufw --force enable"))
	assert.False(t, issued(sh.cmds, "fail2ban"), "nothing after the failed check")
}

func TestProvisionStopsOnAFailedDockerInstall(t *testing.T) {
	sh := &failingStream{}
	_, err := provision(sh, provisionOpts{SSHPort: 22}, func(string) {})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Docker install")
	assert.False(t, issued(sh.cmds, "docker network create"), "nothing after a failed install")
}

// The toft-vm10 drill (D-153): with no DNS, get.docker.com's download
// failed, and piped into sh that was an empty script exiting 0; the run
// went on and stopped at "docker: command not found". The download now
// fails the install, and Docker must answer after it.
func TestProvisionStopsWhenDockerIsMissingAfterItsInstall(t *testing.T) {
	sh := &fakeShell{answer: func(cmd string) string {
		if strings.HasPrefix(cmd, "docker --version") {
			return "bash: line 1: docker: command not found"
		}
		return ""
	}}
	_, err := provision(sh, provisionOpts{SSHPort: 22}, func(string) {})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Docker isn't there after its install script ran")
	assert.False(t, issued(sh.cmds, "docker network create"), "nothing after a failed install")
	assert.True(t, issued(sh.cmds, `curl -fsSL https://get.docker.com -o "$f" && sh "$f"`), "the script runs from a file, never piped")
	assert.False(t, issued(sh.cmds, "get.docker.com | sh"))
}

type failingStream struct{ fakeShell }

func (f *failingStream) ExecStream(cmd string, _ func(string)) error {
	f.cmds = append(f.cmds, cmd)
	return errors.New("exit status 1")
}

// ctxRecorder is a ctxExecer that records the context of every call.
type ctxRecorder struct {
	ctxs      []context.Context
	installed bool // Docker's install ran
}

func (r *ctxRecorder) Exec(ctx context.Context, cmd string) (string, error) {
	r.ctxs = append(r.ctxs, ctx)
	if strings.HasPrefix(cmd, requirementsMarker) {
		return goodHost, nil
	}
	if strings.HasPrefix(cmd, "docker --version") && r.installed {
		return "Docker version 29.1.0", nil
	}
	return "", nil
}

func (r *ctxRecorder) ExecInput(ctx context.Context, _ string, _ io.Reader) (string, error) {
	r.ctxs = append(r.ctxs, ctx)
	return "", nil
}

func (r *ctxRecorder) ExecStream(ctx context.Context, cmd string, _ func(string)) error {
	r.ctxs = append(r.ctxs, ctx)
	r.installed = r.installed || strings.Contains(cmd, "get.docker.com")
	return nil
}

func TestShellForRunsEveryCommandUnderItsContext(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "provision")
	rec := &ctxRecorder{}
	_, err := provision(shellFor(ctx, rec), provisionOpts{SSHPort: 22}, func(string) {})
	require.NoError(t, err)
	require.NotEmpty(t, rec.ctxs)
	for _, c := range rec.ctxs {
		assert.Equal(t, "provision", c.Value(key{}))
	}
}

func TestProvisionLegacyLeavesAppNetworksAlone(t *testing.T) {
	sh := &fakeShell{}
	_, err := provision(sh, provisionOpts{SSHPort: 22, AddressPool: "10.203.0.0/16"}, func(string) {})
	require.NoError(t, err)
	assert.False(t, issued(sh.cmds, "daemon.json"), "legacy never touches daemon.json")
	assert.False(t, issued(sh.cmds, "label=paas.managed"))
}

func TestProvisionIsolatedFreshHostSetsThePoolAndAttachesTheProxy(t *testing.T) {
	sh := &fakeShell{answer: func(cmd string) string {
		switch {
		case strings.HasPrefix(cmd, "docker network ls --filter label=paas.managed"):
			return "paas-env-0a1b\npaas-proj-2c3d\nevil;rm -rf /\n"
		case strings.HasPrefix(cmd, "docker inspect -f"):
			return "paas-network paas-proj-2c3d "
		}
		return ""
	}}
	var lines []string
	opts := provisionOpts{SSHPort: 22, AppNetworksIsolated: true, AddressPool: "10.203.0.0/16"}
	_, err := provision(sh, opts, func(l string) { lines = append(lines, l) })
	require.NoError(t, err)
	assert.True(t, issued(sh.cmds, `'{"default-address-pools":[{"base":"10.203.0.0/16","size":24}]}' > /etc/docker/daemon.json && systemctl restart docker`))
	assert.True(t, issued(sh.cmds, "docker network connect paas-env-0a1b paas-traefik"))
	assert.False(t, issued(sh.cmds, "docker network connect paas-proj-2c3d"), "already connected")
	assert.False(t, issued(sh.cmds, "evil"), "names that are not app networks never reach a command")
	assert.Contains(t, lines, "Proxy attached to 1 more app network(s)")

	// The pool is written before Traefik starts (the restart would stop it).
	var poolAt, traefikAt int
	for i, c := range sh.cmds {
		if strings.Contains(c, "daemon.json") && strings.Contains(c, "restart") {
			poolAt = i
		}
		if strings.HasPrefix(c, "docker run -d") {
			traefikAt = i
		}
	}
	assert.Less(t, poolAt, traefikAt)
}

func TestProvisionIsolatedIsIdempotentOnAProvisionedHost(t *testing.T) {
	host := &provisionedHost{}
	sh := &fakeShell{answer: func(cmd string) string {
		switch {
		case strings.HasPrefix(cmd, "test -e /etc/docker/daemon.json"):
			return "exists"
		case strings.HasPrefix(cmd, "grep -q default-address-pools"):
			return "set"
		case strings.HasPrefix(cmd, "docker network ls --filter label=paas.managed"):
			return "paas-env-0a1b"
		case strings.HasPrefix(cmd, "docker inspect -f"):
			return "paas-network paas-env-0a1b"
		}
		return host.answer(cmd)
	}}
	opts := provisionOpts{ACMEEmail: "ops@example.com", SSHPort: 22, AppNetworksIsolated: true, AddressPool: "10.203.0.0/16"}
	_, err := provision(sh, opts, func(string) {})
	require.NoError(t, err)
	for _, m := range mutating {
		assert.False(t, issued(sh.cmds, m), "a second run must not run %q", m)
	}
}

func TestEnsureAddressPoolNeverRestartsABusyOrConfiguredDocker(t *testing.T) {
	busy := &fakeShell{answer: func(cmd string) string {
		if strings.HasPrefix(cmd, "docker ps -q") {
			return "abc123"
		}
		return "absent"
	}}
	err := ensureAddressPool(busy, "10.203.0.0/16", func(string) {})
	require.Error(t, err)
	assert.False(t, issued(busy.cmds, "restart"))
	assert.Contains(t, err.Error(), `Run echo '{"default-address-pools":[{"base":"10.203.0.0/16","size":24}]}' > /etc/docker/daemon.json && systemctl restart docker`)

	other := &fakeShell{answer: func(cmd string) string {
		switch {
		case strings.HasPrefix(cmd, "test -e"):
			return "exists"
		case strings.HasPrefix(cmd, "grep"):
			return "unset"
		}
		return ""
	}}
	err = ensureAddressPool(other, "10.203.0.0/16", func(string) {})
	require.Error(t, err)
	assert.False(t, issued(other.cmds, "restart"), "an existing daemon.json is never changed")
	assert.Contains(t, err.Error(), `has no default-address-pools`)
	assert.Contains(t, err.Error(), `Add "default-address-pools":[{"base":"10.203.0.0/16","size":24}] to its top-level object, run systemctl restart docker`)

	// In a run, it is a warning: the server shows under Needs attention.
	host := &provisionedHost{}
	run := &fakeShell{answer: func(cmd string) string {
		switch {
		case strings.HasPrefix(cmd, "test -e /etc/docker/daemon.json"):
			return "exists"
		case strings.HasPrefix(cmd, "grep -q default-address-pools"):
			return "unset"
		}
		return host.answer(cmd)
	}}
	var lines []string
	warnings, err := provision(run, provisionOpts{ACMEEmail: "ops@example.com", SSHPort: 22, AppNetworksIsolated: true, AddressPool: "10.203.0.0/16"}, func(l string) { lines = append(lines, l) })
	require.NoError(t, err)
	assert.Equal(t, []string{"the app network address pool"}, warnings)
	assert.True(t, containsLine(lines, "Warning: app network address pool not set (non-fatal): /etc/docker/daemon.json has no default-address-pools"))

	// The run shows the fix the log gave (the server page's Needs attention).
	last := &ProvisionRun{Warnings: warnings, Log: strings.Join(lines, "\n") + "\n"}
	assert.Equal(t, `/etc/docker/daemon.json has no default-address-pools, so Docker gives app networks its default ranges (172.17-31/16, 192.168/16: about 30 networks), which can run out or clash with this server's LAN or VPN. Add "default-address-pools":[{"base":"10.203.0.0/16","size":24}] to its top-level object, run systemctl restart docker (this restarts every container), then Re-provision`,
		addressPoolFix(last))
	assert.Empty(t, addressPoolFix(&ProvisionRun{Warnings: []string{"nightly backups"}, Log: last.Log}), "only while the run left the pool out")

	assert.Error(t, ensureAddressPool(&fakeShell{}, "10.0.0.0/16'; reboot; '", func(string) {}))
}

// With a DNS provider, each credential goes over stdin into a root-only
// file, Traefik gets <NAME>_FILE and the DNS-01 resolver, and no value
// appears on a command line. Without one, the files are removed and
// Traefik's command is as before.
func TestProvisionDNSProvider(t *testing.T) {
	sh := &fakeShell{}
	dns := &DNSConfig{Provider: "cloudflare", Credentials: map[string]string{"CF_DNS_API_TOKEN": "tok-s3cret", "CF_ZONE_API_TOKEN": "zone-s3cret"}}
	_, err := provision(sh, provisionOpts{ACMEEmail: "ops@example.com", SSHPort: 22, DNS: dns}, func(string) {})
	require.NoError(t, err)

	var run string
	for _, c := range sh.cmds {
		assert.NotContains(t, c, "s3cret", "a credential reached a command line")
		if strings.HasPrefix(c, "docker run -d") && strings.Contains(c, "paas-traefik") {
			run = c
		}
	}
	// The other inputs are the host firewall's (its rules, checked then
	// written, and its unit) and the backup script's and units.
	assert.ElementsMatch(t, []string{"tok-s3cret", "zone-s3cret", hostFirewallRules, hostFirewallRules, hostFirewallUnit,
		backupScript, backupServiceUnit, backupTimerUnit}, sh.inputs)
	assert.Contains(t, sh.cmds, "umask 077 && cat > /opt/paas-traefik/dns/CF_DNS_API_TOKEN")
	assert.Contains(t, run, "-v /opt/paas-traefik/dns:/etc/traefik/dns:ro \\\n")
	assert.Contains(t, run, "-e CF_DNS_API_TOKEN_FILE=/etc/traefik/dns/CF_DNS_API_TOKEN")
	assert.Contains(t, run, "--certificatesresolvers.letsencrypt-dns.acme.dnschallenge.provider=cloudflare \\\n")
	assert.Contains(t, run, "--certificatesresolvers.letsencrypt.acme.httpchallenge=true", "HTTP-01 stays for the other domains")

	plain := &fakeShell{}
	_, err = provision(plain, provisionOpts{ACMEEmail: "ops@example.com", SSHPort: 22}, func(string) {})
	require.NoError(t, err)
	assert.Contains(t, plain.cmds, "rm -rf /opt/paas-traefik/dns")
	for _, c := range plain.cmds {
		assert.NotContains(t, c, "letsencrypt-dns")
	}
}
