package server

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/Nickpap21/toft/backend/internal/hoststats"
)

// Shell runs commands on the server being provisioned: Exec returns the
// combined, trimmed output (a non-zero exit is an error) and ExecStream
// delivers it line by line. shellFor adapts the server's pooled SSH
// connection, the instance package the panel's host helper (D-97); tests
// use a fake to check that provisioning stays idempotent.
type Shell interface {
	Exec(cmd string) (string, error)
	ExecStream(cmd string, logFn func(string)) error
	// ExecInput is Exec with stdin: for secrets, which a command line would
	// show in the server's process list.
	ExecInput(cmd string, stdin io.Reader) (string, error)
}

type shell = Shell

// ctxExecer is the part of sshpool.RemoteHost the provisioner uses.
type ctxExecer interface {
	Exec(ctx context.Context, cmd string) (string, error)
	ExecStream(ctx context.Context, cmd string, logFn func(string)) error
	ExecInput(ctx context.Context, cmd string, stdin io.Reader) (string, error)
}

// shellFor returns a shell that runs every command on h under ctx.
func shellFor(ctx context.Context, h ctxExecer) shell {
	return hostShell{ctx: ctx, h: h}
}

type hostShell struct {
	ctx context.Context
	h   ctxExecer
}

func (s hostShell) Exec(cmd string) (string, error) { return s.h.Exec(s.ctx, cmd) }

func (s hostShell) ExecInput(cmd string, stdin io.Reader) (string, error) {
	return s.h.ExecInput(s.ctx, cmd, stdin)
}

func (s hostShell) ExecStream(cmd string, logFn func(string)) error {
	return s.h.ExecStream(s.ctx, cmd, logFn)
}

// provisionOpts are the settings of one provisioning run.
type provisionOpts struct {
	ACMEEmail string
	// SSHPort is the server's SSH port, which the firewall keeps open.
	SSHPort int
	// AppNetworksIsolated is TOFT_APP_NETWORKS=isolated: the address pool
	// is written on a fresh server and the proxy joins every app network.
	AppNetworksIsolated bool
	// AddressPool is the app network address pool (a validated IPv4 CIDR).
	AddressPool string
	// DNS, when set, gives Traefik the letsencrypt-dns resolver (DNS-01)
	// that wildcard domains need. Without it, credentials left from an
	// earlier provider are removed.
	DNS *DNSConfig
	// BackupEnv is the server's backup env file (D-43), or "" when it has
	// no backup destination.
	BackupEnv string
	// SyncRoutes, set where the server's agent writes the routes (D-113),
	// asks it to write them all (ensureTraefik).
	SyncRoutes func() (int, error)
	// LocalLogs is set for an agent server, whose agent ships the logs
	// (D-113): the Loki plugin is not installed, and goes once no
	// container uses it.
	LocalLogs bool
}

// provision runs an idempotent setup on the server behind sc:
//  0. Refuse a server that misses one of D-29's requirements
//  1. Install Docker (if missing)
//  2. Under isolated app networks, give a fresh Docker the app address pool
//  3. Create paas-network, the proxy's own network (if missing)
//  4. Start a per-server Traefik container with Let's Encrypt ACME (if not running)
//  5. Under isolated app networks, connect Traefik to every app network
//  6. UFW and fail2ban, then the host firewall for app containers (D-37)
//  7. The backup script and timer, and the backup destination (D-43)
//
// logFn receives human-readable progress lines for streaming to the caller.
// The caller has connected already (Service.Provision).
//
// A run that sets up everything but a firewall still succeeds (D-37: a
// deploy must not fail on a server that worked yesterday), with warnings
// naming what it left out; its last line says so.
func provision(sc shell, opts provisionOpts, logFn func(string)) (warnings []string, err error) {
	acmeEmail, sshPort := opts.ACMEEmail, opts.SSHPort
	// D-29, before anything changes on the machine.
	req, err := checkRequirements(sc)
	if err != nil {
		return nil, err
	}
	if !req.Met {
		return nil, fmt.Errorf("%w: %s", ErrRequirementsNotMet, req.unmet())
	}
	logFn("Requirements met: " + req.summary())
	if err := ensureDocker(sc, logFn); err != nil {
		return nil, fmt.Errorf("Docker install: %w", err)
	}
	if opts.AppNetworksIsolated {
		if err := ensureAddressPool(sc, opts.AddressPool, logFn); err != nil {
			// Non-fatal: Docker's default pools work for a few dozen networks.
			logFn(addressPoolLine + err.Error())
			warnings = append(warnings, warningAddressPool)
		}
	}
	if err := ensureLokiDriver(sc, opts.LocalLogs, logFn); err != nil {
		// Non-fatal: Loki log driver is optional. Log the warning but continue.
		logFn("Warning: Loki log driver setup failed (non-fatal): " + err.Error())
	}
	if err := ensureNetwork(sc, logFn); err != nil {
		return nil, fmt.Errorf("network setup: %w", err)
	}
	if err := ensureTraefik(sc, acmeEmail, opts.DNS, opts.SyncRoutes, logFn); err != nil {
		return nil, fmt.Errorf("Traefik setup: %w", err)
	}
	if opts.AppNetworksIsolated {
		if err := ensureProxyNetworks(sc, logFn); err != nil {
			// Non-fatal: each deploy connects the proxy to its own network.
			logFn("Warning: attaching Traefik to app networks failed (non-fatal): " + err.Error())
		}
	}
	if err := ensureFirewall(sc, sshPort, logFn); err != nil {
		// Non-fatal: a server missing a firewall can still run the PaaS.
		logFn("Warning: firewall setup failed (non-fatal): " + err.Error())
		warnings = append(warnings, warningFirewall)
	}
	if err := ensureHostFirewall(sc, logFn); err != nil {
		// Non-fatal (D-37): a deploy must not fail on a server that worked
		// yesterday. The warning says what is open.
		logFn("Warning: app containers can reach this host's services (sshd, cloud metadata): host firewall setup failed (non-fatal): " + err.Error())
		warnings = append(warnings, warningHostFirewall)
	}
	if err := ensureBackup(sc, opts.BackupEnv, logFn); err != nil {
		// Non-fatal: the backup_failed alert says when none succeeds.
		logFn("Warning: backup setup failed (non-fatal): " + err.Error())
		warnings = append(warnings, warningBackup)
	}
	if err := ensureAutoUpdates(sc, logFn); err != nil {
		logFn("Warning: automatic security updates setup failed (non-fatal): " + err.Error())
		warnings = append(warnings, warningAutoUpdates)
	}

	if len(warnings) > 0 {
		logFn("Server provisioned with warnings, missing " + missingList(warnings) +
			": fix what the warnings above say, then Re-provision")
		return warnings, nil
	}
	logFn("Server provisioned successfully!")
	return nil, nil
}

// What a run that finished with warnings left out (server_provision_runs.
// warnings, the API's ProvisionRun.warnings).
const (
	warningFirewall     = "the firewall (ufw, fail2ban)"
	warningHostFirewall = "the host firewall for app containers"
	warningBackup       = "nightly backups"
	warningAutoUpdates  = "automatic security updates"
	warningAddressPool  = "the app network address pool"
)

// addressPoolLine starts the log line that says why the address pool isn't
// set and how to set it (ensureAddressPool's error); the run shows that as
// its AddressPoolFix.
const addressPoolLine = "Warning: app network address pool not set (non-fatal): "

// addressPoolFix is how to give the server's Docker the address pool, from
// run's log, when the run left it out; "" otherwise, or when the line has
// gone with the log's oldest text.
func addressPoolFix(run *ProvisionRun) string {
	if !slices.Contains(run.Warnings, warningAddressPool) {
		return ""
	}
	for line := range strings.Lines(run.Log) {
		if fix, ok := strings.CutPrefix(strings.TrimRight(line, "\n"), addressPoolLine); ok {
			return fix
		}
	}
	return ""
}

// missingList is "a, b and c", as the frontend's missingList says it.
func missingList(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// ensureDocker installs Docker via get.docker.com if the docker binary is absent.
func ensureDocker(sc shell, logFn func(string)) error {
	out, _ := sc.Exec("docker --version 2>&1")
	if strings.Contains(out, "Docker version") {
		logFn("Docker already installed: " + out)
		return nil
	}

	logFn("Installing Docker (this may take a minute)...")
	// Downloaded first: piped into sh, a download that fails is an empty
	// script that exits 0, and the run went on without Docker.
	if err := sc.ExecStream(dockerInstall, logFn); err != nil {
		return err
	}
	if out, _ := sc.Exec("docker --version 2>&1"); !strings.Contains(out, "Docker version") {
		return fmt.Errorf("Docker isn't there after its install script ran (docker --version: %q)", strings.TrimSpace(out))
	}
	return nil
}

// dockerInstall runs get.docker.com's script from a file, so a failed
// download fails the install.
const dockerInstall = `f=$(mktemp) && trap 'rm -f "$f"' EXIT && { curl -fsSL https://get.docker.com -o "$f" && sh "$f"; } 2>&1`

// daemonConfigPath is Docker's daemon configuration file.
const daemonConfigPath = "/etc/docker/daemon.json"

// addressPoolPattern is what a validated pool looks like (an IPv4 CIDR);
// anything else is never written into a shell command.
var addressPoolPattern = regexp.MustCompile(`^[0-9]{1,3}(\.[0-9]{1,3}){3}/[0-9]{1,2}$`)

// ensureAddressPool gives Docker the app network address pool, in /24s,
// when it has none: only when daemon.json does not exist and no container
// runs (a fresh install), because the change needs a Docker restart, which
// restarts running containers. An existing daemon.json is never changed.
// Otherwise the error says how to set it by hand: app networks are created
// without a subnet (rollout.ensureNetwork, composespec refuses fixed ones),
// so without the pool every one comes from Docker's defaults, about 30
// networks in 172.17-31/16 and 192.168/16, which can run out under isolated
// networks and can clash with ranges the server reaches through a gateway.
func ensureAddressPool(sc shell, pool string, logFn func(string)) error {
	if !addressPoolPattern.MatchString(pool) {
		return fmt.Errorf("invalid address pool %q", pool)
	}
	cfg := fmt.Sprintf(`{"default-address-pools":[{"base":"%s","size":24}]}`, pool)
	const why = "Docker gives app networks its default ranges (172.17-31/16, 192.168/16: about 30 networks), which can run out or clash with this server's LAN or VPN. "
	const after = " (this restarts every container), then Re-provision"
	exists, _ := sc.Exec("test -e " + daemonConfigPath + " && echo exists || echo absent")
	if strings.TrimSpace(exists) == "exists" {
		set, _ := sc.Exec("grep -q default-address-pools " + daemonConfigPath + " && echo set || echo unset")
		if strings.TrimSpace(set) == "set" {
			logFn("Docker address pools already set in " + daemonConfigPath)
			return nil
		}
		return fmt.Errorf("%s has no default-address-pools, so %sAdd \"default-address-pools\":[{\"base\":\"%s\",\"size\":24}] "+
			"to its top-level object, run systemctl restart docker%s", daemonConfigPath, why, pool, after)
	}
	running, _ := sc.Exec("docker ps -q | head -1")
	if strings.TrimSpace(running) != "" {
		return fmt.Errorf("containers are running, so Docker was not restarted to set it, and %sRun "+
			"echo '%s' > %s && systemctl restart docker%s", why, cfg, daemonConfigPath, after)
	}
	cmd := fmt.Sprintf("mkdir -p /etc/docker && printf '%%s\n' '%s' > %s && systemctl restart docker", cfg, daemonConfigPath)
	if _, err := sc.Exec(cmd); err != nil {
		return err
	}
	logFn("Set Docker's app network address pool to " + pool + " (/24 networks)")
	return nil
}

// managedNetworkName is the shape of an app network name; anything else in
// the listing is never put in a command.
var managedNetworkName = regexp.MustCompile(`^paas-(env|proj|org)-[0-9a-f-]{1,64}$`)

// ensureProxyNetworks connects paas-traefik to every app network (labelled
// paas.managed=true) it is not on yet. Running it again connects nothing.
func ensureProxyNetworks(sc shell, logFn func(string)) error {
	out, err := sc.Exec("docker network ls --filter label=paas.managed=true --format '{{.Name}}'")
	if err != nil {
		return err
	}
	on, err := sc.Exec("docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' paas-traefik")
	if err != nil {
		return err
	}
	missing := missingProxyNetworks(out, on)
	for _, n := range missing {
		if _, err := sc.Exec("docker network connect " + n + " paas-traefik 2>&1"); err != nil {
			return fmt.Errorf("connect paas-traefik to %s: %w", n, err)
		}
	}
	logFn(fmt.Sprintf("Proxy attached to %d more app network(s)", len(missing)))
	return nil
}

// missingProxyNetworks returns the app networks in managed (one name per
// line) that are not in on (space separated), sorted. Names that do not
// look like app networks are skipped.
func missingProxyNetworks(managed, on string) []string {
	have := map[string]bool{}
	for _, n := range strings.Fields(on) {
		have[n] = true
	}
	var out []string
	for _, n := range strings.Fields(managed) {
		if managedNetworkName.MatchString(n) && !have[n] {
			have[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// ensureNetwork creates paas-network, the proxy's own network, if it does
// not exist. Under isolated app networks apps never join it.
func ensureNetwork(sc shell, logFn func(string)) error {
	out, _ := sc.Exec("docker network ls --filter name=paas-network --format '{{.Name}}'")
	if strings.TrimSpace(out) == "paas-network" {
		logFn("paas-network already exists")
		return nil
	}

	_, err := sc.Exec("docker network create paas-network 2>&1")
	if err != nil {
		return err
	}
	logFn("Created paas-network")
	return nil
}

// traefikDynamicConfig is the file provider's config: shared middlewares, so
// service containers can reference secure-headers@file and
// redirect-to-https@file without cross-container label resolution.
const traefikDynamicConfig = `http:
  middlewares:
    secure-headers:
      headers:
        stsSeconds: 31536000
        stsIncludeSubdomains: true
        forceSTSHeader: true
        frameDeny: true
        contentTypeNosniff: true
    redirect-to-https:
      redirectScheme:
        scheme: https
        permanent: true
`

// traefikConfigLabel holds the hash of the configuration a provisioned
// Traefik was started with (traefikConfig).
const traefikConfigLabel = "paas.traefik.config"

// traefikAcmeVolume holds the certificates. Only a Traefik the provisioner
// started mounts it, which tells it apart from one it adopted.
const traefikAcmeVolume = "paas-traefik-acme"

// Where the server's agent writes the routes (D-113), Traefik reads every
// file in traefikRoutesDir: toft.yml (traefikDynamicConfig, written here),
// the agent's toft-agent.yml (the API's router, behind a password only the
// agent holds) and one file per service. It doesn't mount the Docker
// socket. Its API's entry point is published on the server's loopback, for
// the agent to see when Traefik serves a rollout's new container.
const (
	// traefikImage is pulled before the running Traefik is removed, so a
	// failed pull leaves it serving.
	traefikImage        = "traefik:v3.6.10@sha256:c549d482c55d7a797398562064f35428cc53e748d84d7190997930e7b31bcc32"
	traefikRoutesDir    = "/opt/paas-traefik/dynamic"
	traefikProvidersFor = `  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -v %s:/acme \
  -v /opt/paas-traefik/dynamic.yml:/etc/traefik/dynamic.yml:ro%s \
  ` + traefikImage + ` \
  --api.dashboard=false \
  --providers.docker=true \
  --providers.docker.network=paas-network \
  --providers.docker.exposedbydefault=false \
  --providers.file.filename=/etc/traefik/dynamic.yml \
  --providers.file.watch=true \`
	traefikProvidersFiles = `  -p 127.0.0.1::8082 \
  -v %s:/acme \
  -v ` + traefikRoutesDir + `:/etc/traefik/dynamic:ro%s \
  ` + traefikImage + ` \
  --api=true \
  --api.dashboard=false \
  --providers.file.directory=/etc/traefik/dynamic \
  --providers.file.watch=true \
  --entrypoints.toft-api.address=:8082 \`
)

// traefikConfig returns the docker run command for the server's Traefik and
// the hash of everything it is started with: the command, the dynamic
// config and the DNS provider's credentials (whose files Traefik reads).
// Changing any of them changes the hash. dns is validated here. files: the
// agent writes the routes.
func traefikConfig(acmeEmail string, dns *DNSConfig, files bool) (cmd, hash string, err error) {
	dnsDocker, dnsTraefik, err := dnsArgs(dns, acmeEmail)
	if err != nil {
		return "", "", err
	}
	providers, env := traefikProvidersFor, "  -e DOCKER_API_VERSION=1.44 \\\n"
	if files {
		providers, env = traefikProvidersFiles, ""
	}
	run := fmt.Sprintf(`docker run -d \
  --name paas-traefik \
  --restart unless-stopped \
  --log-driver local --log-opt max-size=20m --log-opt max-file=5 \
  --network paas-network \
`+env+`  -p 80:80 \
  -p 443:443 \
`+providers+`
  --entrypoints.web.address=:80 \
  --entrypoints.web.http.redirections.entrypoint.to=websecure \
  --entrypoints.web.http.redirections.entrypoint.scheme=https \
  --entrypoints.web.http.redirections.entrypoint.priority=1 \
  --entrypoints.websecure.address=:443 \
  --certificatesresolvers.letsencrypt.acme.email=%s \
  --certificatesresolvers.letsencrypt.acme.storage=/acme/acme.json \
  --certificatesresolvers.letsencrypt.acme.httpchallenge=true \
  --certificatesresolvers.letsencrypt.acme.httpchallenge.entrypoint=web%s \
  --log.level=INFO \
  --log.format=json \
  --accesslog=true \
  --accesslog.bufferingsize=100 \
  `+traefikMetricsEntrypoint+` \
  --metrics.prometheus=true \
  --metrics.prometheus.entrypoint=metrics \
  --metrics.prometheus.addServicesLabels=true \
  --metrics.prometheus.buckets=`+hoststats.LatencyBounds+` 2>&1`, traefikAcmeVolume, dnsDocker, acmeEmail, dnsTraefik)

	h := sha256.New()
	h.Write([]byte(run + "\n" + traefikDynamicConfig))
	if dns != nil {
		for _, k := range sortedKeys(dns.Credentials) {
			h.Write([]byte("\n" + k + "=" + dns.Credentials[k]))
		}
	}
	hash = hex.EncodeToString(h.Sum(nil))
	// The label goes in after the hash is taken; the hash is a valid label value.
	cmd = strings.Replace(run, "--restart unless-stopped \\\n", "--restart unless-stopped \\\n  --label "+traefikConfigLabel+"="+hash+" \\\n", 1)
	return cmd, hash, nil
}

// ensureTraefik runs the server's Traefik with the configuration the panel
// has for it. A running Traefik started with that configuration is left
// alone. One the provisioner started with another (a new DNS provider, a
// rotated credential, a new Toft release) is recreated: its certificates
// stay in their volume and its routes pause for a few seconds. One the
// provisioner adopted is never changed.
//
// syncRoutes, set where the server's agent writes the routes (D-113), has
// it write every service's file first; Traefik then reads those files. When
// it fails, Traefik goes on reading what it reads: the containers' labels,
// or the files the agent wrote before (a frozen agent refuses to write).
func ensureTraefik(sc shell, acmeEmail string, dns *DNSConfig, syncRoutes func() (int, error), logFn func(string)) error {
	files := false
	if syncRoutes != nil {
		if n, err := syncRoutes(); err != nil {
			args, _ := sc.Exec(`docker container inspect --format '{{json .Args}}' paas-traefik 2>/dev/null`)
			if files = strings.Contains(args, traefikFileFlag); files {
				logFn("Warning: the agent could not write this server's routes now, so Traefik goes on reading the files it wrote before: " + err.Error())
			} else {
				logFn("Warning: the agent could not write this server's routes, so Traefik goes on reading them from the containers' labels: " + err.Error())
			}
		} else {
			files = true
			logFn(fmt.Sprintf("The agent wrote the routes of %d services; Traefik reads them from its files", n))
		}
	}
	cmd, hash, err := traefikConfig(acmeEmail, dns, files)
	if err != nil {
		return err
	}

	out, _ := sc.Exec("docker ps --filter name=paas-traefik --format '{{.Status}}'")
	if status := strings.TrimSpace(out); strings.HasPrefix(status, "Up") {
		info, _ := sc.Exec(`docker container inspect --format '{{index .Config.Labels "` + traefikConfigLabel + `"}}|{{range .Mounts}}{{.Name}},{{end}}' paas-traefik`)
		label, mounts, _ := strings.Cut(strings.TrimSpace(info), "|")
		switch {
		case label == hash:
			logFn("Traefik already running: " + status)
			return nil
		case !strings.Contains(","+mounts, ","+traefikAcmeVolume+","):
			logFn("Traefik already running (" + status + "), but Toft did not start it, so it is left as it is: " +
				"the DNS provider and Toft's Traefik settings are not applied to it")
			return nil
		}
		logFn("Traefik's configuration changed: restarting it, so this server's routes pause for a few seconds (certificates are kept)")
		return startTraefik(sc, cmd, dns, files, logFn)
	}

	// If something is already on port 80/443, check whether it's a Docker container.
	portCheck, _ := sc.Exec("ss -tlnp 2>/dev/null | grep -E '(:80[^0-9]|:443[^0-9])' || true")
	if strings.Contains(portCheck, "docker-proxy") {
		containerInfo, _ := sc.Exec(`docker ps --format '{{.Names}}|{{.Image}}' --filter publish=80 2>/dev/null | head -1`)
		containerInfo = strings.TrimSpace(containerInfo)
		if containerInfo != "" {
			parts := strings.SplitN(containerInfo, "|", 2)
			existingName := parts[0]
			existingImage := ""
			if len(parts) == 2 {
				existingImage = parts[1]
			}
			if strings.Contains(strings.ToLower(existingImage), "traefik") {
				logFn(fmt.Sprintf("Found existing Traefik container '%s' — adopting it...", existingName))
				if existingName != "paas-traefik" {
					_, _ = sc.Exec(fmt.Sprintf("docker rename %s paas-traefik 2>/dev/null || true", existingName))
				}
				_, _ = sc.Exec("docker network connect paas-network paas-traefik 2>/dev/null || true")
				logFn("Traefik adopted as paas-traefik and connected to paas-network")
				return nil
			}
			logFn(fmt.Sprintf("Container '%s' (image: %s) is using ports 80/443.", existingName, existingImage))
			logFn(fmt.Sprintf("Stop it first:  docker stop %s", existingName))
		} else {
			logFn("A Docker container is using ports 80/443 (docker-proxy detected).")
			logFn("Find it with:  docker ps --format '{{.Names}}\\t{{.Ports}}' | grep ':80->'")
			logFn("Then stop it and re-provision.")
		}
		return fmt.Errorf("ports 80/443 are held by an existing Docker container — see log for details")
	} else if strings.TrimSpace(portCheck) != "" {
		logFn("WARNING: port 80/443 in use by a system process:")
		for _, line := range strings.Split(portCheck, "\n") {
			if strings.TrimSpace(line) != "" {
				logFn("  " + strings.TrimSpace(line))
			}
		}
		logFn("Stop the service first (e.g. systemctl stop nginx) then re-provision.")
		return fmt.Errorf("port 80 or 443 is bound by a system process — stop it first")
	}

	return startTraefik(sc, cmd, dns, files, logFn)
}

// startTraefik writes Traefik's files, then replaces any paas-traefik
// container with one started by cmd (traefikConfig).
func startTraefik(sc shell, cmd string, dns *DNSConfig, files bool, logFn func(string)) error {
	logFn("Writing Traefik dynamic config...")
	if _, err := sc.Exec("mkdir -p " + traefikRoutesDir); err != nil {
		return fmt.Errorf("create traefik dirs: %w", err)
	}
	writeCmd := fmt.Sprintf("cat > /opt/paas-traefik/dynamic.yml << 'DYNEOF'\n%sDYNEOF", traefikDynamicConfig)
	if files {
		// Renamed into place: Traefik ignores the .tmp file, and a part
		// written file it read would stop it loading the directory.
		writeCmd = fmt.Sprintf("cat > %[1]s/.toft.yml.tmp << 'DYNEOF'\n%[2]sDYNEOF\nmv -f %[1]s/.toft.yml.tmp %[1]s/toft.yml", traefikRoutesDir, traefikDynamicConfig)
	}
	if _, err := sc.Exec(writeCmd); err != nil {
		return fmt.Errorf("write dynamic config: %w", err)
	}
	if err := writeDNSCredentials(sc, dns); err != nil {
		return err
	}

	if out, err := sc.Exec("docker pull -q " + traefikImage + " 2>&1"); err != nil {
		return fmt.Errorf("pull Traefik (the running one is left as it is): %s: %w", strings.TrimSpace(out), err)
	}
	_, _ = sc.Exec("docker rm -f paas-traefik 2>/dev/null || true")
	logFn("Starting Traefik v3.6.10 with Let's Encrypt ACME...")
	out, err := sc.Exec(cmd)
	if trimmed := strings.TrimSpace(out); trimmed != "" {
		logFn("docker: " + trimmed)
	}
	if err != nil {
		return fmt.Errorf("start Traefik: %w", err)
	}
	logFn("Traefik v3.6.10 started successfully")
	// Its access log goes to the container's log now, capped by Docker's
	// local driver; the file it wrote here was never rotated.
	_, _ = sc.Exec("rm -rf /opt/paas-traefik/logs")
	return nil
}

// ensureFirewall enables UFW (allowing SSH + HTTP/HTTPS, denying everything else
// inbound by default) and installs fail2ban to rate-limit SSH brute-force attempts.
// Idempotent — safe to call multiple times. Best-effort: SSH is always allowed
// before the firewall is enabled, so a failure here cannot lock out access.
func ensureFirewall(sc shell, sshPort int, logFn func(string)) error {
	out, _ := sc.Exec("command -v ufw >/dev/null 2>&1 && echo yes || echo no")
	if strings.TrimSpace(out) == "no" {
		logFn("Installing ufw...")
		if err := sc.ExecStream("export DEBIAN_FRONTEND=noninteractive; apt-get update -q && apt-get install -yq ufw 2>&1", logFn); err != nil {
			return fmt.Errorf("install ufw: %w", err)
		}
	}

	status, _ := sc.Exec("ufw status 2>/dev/null | head -1")
	if strings.Contains(status, "inactive") || strings.TrimSpace(status) == "" {
		logFn("Configuring firewall (allow SSH, HTTP, HTTPS; deny everything else inbound)...")
		// SSH must be allowed on its actual configured port — srv.Port may not be 22 —
		// otherwise "default deny incoming" locks out the very session provisioning runs over.
		cmds := []string{
			fmt.Sprintf("ufw allow %d/tcp", sshPort),
			"ufw allow 80/tcp",
			"ufw allow 443/tcp",
			"ufw default deny incoming",
			"ufw default allow outgoing",
			"ufw --force enable",
		}
		for _, c := range cmds {
			if _, err := sc.Exec(c); err != nil {
				return fmt.Errorf("%s: %w", c, err)
			}
		}
		// Checked rather than trusted, as the host firewall's table is
		// (hostFirewallLoaded); LC_ALL=C: ufw translates its status.
		if now, _ := sc.Exec("LC_ALL=C ufw status 2>/dev/null | head -1"); strings.TrimSpace(now) != "Status: active" {
			return fmt.Errorf("ufw --force enable ran but ufw is not active (ufw status)")
		}
		logFn("Firewall enabled.")
	} else {
		logFn("Firewall already active — leaving existing rules untouched.")
	}

	f2b, _ := sc.Exec("systemctl is-active fail2ban 2>/dev/null")
	if strings.TrimSpace(f2b) != "active" {
		logFn("Installing fail2ban for SSH brute-force protection...")
		cmd := `set -e
export DEBIAN_FRONTEND=noninteractive
apt-get install -yq fail2ban 2>&1
cat > /etc/fail2ban/jail.d/sshd.local << 'F2BEOF'
[sshd]
enabled = true
maxretry = 5
bantime = 30m
findtime = 10m
F2BEOF
systemctl enable --now fail2ban`
		if err := sc.ExecStream(cmd, logFn); err != nil {
			return fmt.Errorf("fail2ban: %w", err)
		}
		logFn("fail2ban installed and active.")
	} else {
		logFn("fail2ban already active.")
	}

	return nil
}

// The host firewall (D-37): what app containers may reach on the host they
// run on. toft upgrade installs the same two files on the control plane.
var (
	//go:embed firewall.nft
	hostFirewallRules string
	//go:embed toft-firewall.service
	hostFirewallUnit string
)

const (
	hostFirewallRulesPath = "/etc/toft/firewall.nft"
	hostFirewallUnitPath  = "/etc/systemd/system/toft-firewall.service"
)

// ensureHostFirewall installs nftables if missing, checks the rules against
// this kernel before touching anything, writes the rules and their boot unit
// when they differ, and loads them. On a host that already has both files
// and the table, it changes nothing.
func ensureHostFirewall(sc shell, logFn func(string)) error {
	out, _ := sc.Exec("command -v nft >/dev/null 2>&1 && echo yes || echo no")
	if strings.TrimSpace(out) != "yes" {
		logFn("Installing nftables...")
		if err := sc.ExecStream("export DEBIAN_FRONTEND=noninteractive; apt-get update -q && apt-get install -yq nftables 2>&1", logFn); err != nil {
			return fmt.Errorf("install nftables: %w", err)
		}
	}
	if _, err := sc.ExecInput("nft -c -f -", strings.NewReader(hostFirewallRules)); err != nil {
		return fmt.Errorf("this kernel refuses the rules, nothing was changed: %w", err)
	}

	rulesChanged, err := writeIfChanged(sc, hostFirewallRulesPath, hostFirewallRules)
	if err != nil {
		return err
	}
	unitChanged, err := writeIfChanged(sc, hostFirewallUnitPath, hostFirewallUnit)
	if err != nil {
		return err
	}
	if unitChanged {
		if _, err := sc.Exec("systemctl daemon-reload"); err != nil {
			return fmt.Errorf("systemctl daemon-reload: %w", err)
		}
	}
	if enabled, _ := sc.Exec("systemctl is-enabled toft-firewall 2>/dev/null"); strings.TrimSpace(enabled) != "enabled" {
		if _, err := sc.Exec("systemctl enable toft-firewall"); err != nil {
			return fmt.Errorf("systemctl enable toft-firewall: %w", err)
		}
	}
	if rulesChanged || !hostFirewallLoaded(sc) {
		if _, err := sc.Exec("systemctl restart toft-firewall"); err != nil {
			return fmt.Errorf("systemctl restart toft-firewall: %w", err)
		}
		if !hostFirewallLoaded(sc) {
			return fmt.Errorf("toft-firewall ran but the table is not loaded (systemctl status toft-firewall)")
		}
		logFn("Host firewall loaded: app containers reach this host only on 80, 443 and DNS, and no cloud metadata")
		return nil
	}
	logFn("Host firewall already loaded")
	return nil
}

// hostFirewallLoaded reports whether the toft table is in the kernel.
func hostFirewallLoaded(sc shell) bool {
	out, _ := sc.Exec("nft list table ip toft >/dev/null 2>&1 && echo loaded || echo missing")
	return strings.TrimSpace(out) == "loaded"
}

// writeIfChanged writes content to path (a constant, never input) through
// stdin unless the file already holds it, and reports whether it wrote.
func writeIfChanged(sc shell, path, content string) (bool, error) {
	sum := sha256.Sum256([]byte(content))
	have, _ := sc.Exec("sha256sum " + path + " 2>/dev/null | cut -d' ' -f1")
	if strings.TrimSpace(have) == hex.EncodeToString(sum[:]) {
		return false, nil
	}
	if _, err := sc.ExecInput("mkdir -p "+filepath.Dir(path)+" && cat > "+path, strings.NewReader(content)); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

// ensureAutoUpdates enables unattended-upgrades so security patches install
// automatically without manual intervention. Idempotent — the periodic-apt
// config is rewritten unconditionally on every call since that write is itself
// idempotent; only the (potentially slow) package install is skipped when the
// binary is already present. This avoids a false "already enabled" positive
// from checking only the systemd unit state, which can be enabled by a stock
// cloud image while the periodic apt config still has automatic upgrades off.
func ensureAutoUpdates(sc shell, logFn func(string)) error {
	out, _ := sc.Exec("command -v unattended-upgrade >/dev/null 2>&1 && echo yes || echo no")
	if strings.TrimSpace(out) == "no" {
		logFn("Installing unattended-upgrades...")
		cmd := `set -e
export DEBIAN_FRONTEND=noninteractive
apt-get update -q && apt-get install -yq unattended-upgrades 2>&1`
		if err := sc.ExecStream(cmd, logFn); err != nil {
			return fmt.Errorf("install unattended-upgrades: %w", err)
		}
	}

	logFn("Enabling automatic security updates...")
	cmd := `set -e
printf 'APT::Periodic::Update-Package-Lists "1";\nAPT::Periodic::Unattended-Upgrade "1";\n' > /etc/apt/apt.conf.d/20auto-upgrades
systemctl enable --now unattended-upgrades`
	return sc.ExecStream(cmd, logFn)
}

// ensureLokiDriver installs the Grafana Loki Docker log driver plugin if not already present.
// This is idempotent — running it multiple times is safe (the plugin install uses || true).
// Without this plugin, containers with loki log driver config will fail to start.
// On an agent server (local) it removes the plugin instead (removeLokiDriver).
func ensureLokiDriver(sc shell, local bool, logFn func(string)) error {
	out, _ := sc.Exec("docker plugin ls --format '{{.Name}}' 2>/dev/null")
	has := strings.Contains(out, "loki")
	if local {
		if !has {
			return nil
		}
		return removeLokiDriver(sc, logFn)
	}
	if has {
		logFn("Loki Docker log driver already installed")
		return nil
	}

	logFn("Installing Loki Docker log driver plugin...")
	// By digest: the plugin publishes one tag per architecture (X.Y.Z-amd64,
	// X.Y.Z-arm64), no plain X.Y.Z, and a tag can be moved. The same two as
	// install.sh: scripts/check-pinned-images-test.sh compares them.
	cmd := `case $(dpkg --print-architecture) in
amd64) docker plugin install grafana/loki-docker-driver:3.7.9-amd64@sha256:88f98af1110ba24b7b2eddad28ac445037327c712912aec3596b85702ea7b56f --alias loki --grant-all-permissions ;;
arm64) docker plugin install grafana/loki-docker-driver:3.7.9-arm64@sha256:c78bdbb26c5936e5d5258b8efa7a55e49e126a235461e9dc91ad455bebd9ef26 --alias loki --grant-all-permissions ;;
esac 2>&1 || true`
	return sc.ExecStream(cmd, logFn)
}

// lokiDriverUsersCmd prints each container's name and log driver, stopped
// ones included: Docker lists no container by its log driver.
const lokiDriverUsersCmd = `ids=$(docker ps -aq); [ -z "$ids" ] || docker inspect --format '{{.Name}} {{.HostConfig.LogConfig.Type}}' $ids`

// removeLokiDriver removes the Loki plugin from an agent server once no
// container, running or stopped, logs to it: Docker can't start a
// container whose log driver is gone. Until then each deploy moves one more
// service to the local driver.
func removeLokiDriver(sc shell, logFn func(string)) error {
	out, err := sc.Exec(lokiDriverUsersCmd)
	if err != nil {
		return fmt.Errorf("list the containers' log drivers: %w", err)
	}
	users := 0
	for _, line := range strings.Split(out, "\n") {
		if _, driver, ok := strings.Cut(strings.TrimSpace(line), " "); ok && strings.HasPrefix(driver, "loki") {
			users++
		}
	}
	if users > 0 {
		logFn(fmt.Sprintf("Loki Docker log driver kept until no container logs to it (%d do); each moves to the agent's shipping when next deployed", users))
		return nil
	}
	logFn("Removing the Loki Docker log driver plugin: this server's agent ships the logs")
	return sc.ExecStream("docker plugin rm -f loki 2>&1", logFn)
}

// dnsDir holds a server's DNS provider credentials, one root-only file per
// variable, mounted read-only into Traefik.
const dnsDir = "/opt/paas-traefik/dns"

// dnsArgs returns the docker run options for dns (the credentials mount, and
// <NAME>_FILE for each variable, which lego reads) and the Traefik flags of
// the letsencrypt-dns resolver, each starting with a line continuation.
// Without dns it returns nothing. It validates the names and the provider
// (validateDNSConfig), so they are safe in a command line.
func dnsArgs(dns *DNSConfig, acmeEmail string) (dockerArgs, traefikArgs string, err error) {
	if dns == nil {
		return "", "", nil
	}
	if err := validateDNSConfig(dns.Provider, dns.Credentials); err != nil {
		return "", "", err
	}
	var d strings.Builder
	d.WriteString(" \\\n  -v " + dnsDir + ":/etc/traefik/dns:ro")
	for _, k := range sortedKeys(dns.Credentials) {
		d.WriteString(" \\\n  -e " + k + "_FILE=/etc/traefik/dns/" + k)
	}
	t := " \\\n  --certificatesresolvers.letsencrypt-dns.acme.email=" + acmeEmail +
		" \\\n  --certificatesresolvers.letsencrypt-dns.acme.storage=/acme/acme.json" +
		" \\\n  --certificatesresolvers.letsencrypt-dns.acme.dnschallenge=true" +
		" \\\n  --certificatesresolvers.letsencrypt-dns.acme.dnschallenge.provider=" + dns.Provider
	return d.String(), t, nil
}

// writeDNSCredentials writes dns's variables to dnsDir, one root-only file
// each, through stdin. Without dns it removes dnsDir. dns was validated by
// dnsArgs.
func writeDNSCredentials(sc shell, dns *DNSConfig) error {
	if dns == nil {
		if _, err := sc.Exec("rm -rf " + dnsDir); err != nil {
			return fmt.Errorf("remove DNS credentials: %w", err)
		}
		return nil
	}
	if _, err := sc.Exec("rm -rf " + dnsDir + " && mkdir -m 700 " + dnsDir); err != nil {
		return fmt.Errorf("create DNS credentials directory: %w", err)
	}
	for _, k := range sortedKeys(dns.Credentials) {
		if _, err := sc.ExecInput("umask 077 && cat > "+dnsDir+"/"+k, strings.NewReader(dns.Credentials[k])); err != nil {
			return fmt.Errorf("write DNS credential %s: %w", k, err)
		}
	}
	return nil
}

// sortedKeys returns m's keys in order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
