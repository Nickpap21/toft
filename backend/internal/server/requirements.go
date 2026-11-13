package server

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// D-29's server requirements: 64-bit Linux on amd64 or arm64, Debian or
// Ubuntu (the install uses apt; D-153), signed in as root (Toft 1.0 runs its
// install without sudo; D-153), systemd,
// cgroup v2, kernel 5.10 or newer, Docker Engine 29 or newer (or none yet,
// which provisioning installs), and ports 80 and 443 free for Traefik.
// Add a server shows them before anything is installed, and provisioning
// refuses a server that misses one before it changes anything. Toft never
// upgrades Docker itself: a non-patch upgrade restarts every container.

// requirementsMarker starts the script, so fake shells can answer it.
const requirementsMarker = "# toft requirements"

// requirementsScript prints one "key:value" line per fact. Read-only.
const requirementsScript = requirementsMarker + `
. /etc/os-release 2>/dev/null
echo "os:${PRETTY_NAME:-}"
echo "distro:${ID:-} ${ID_LIKE:-}"
echo "uid:$(id -u) $(id -un)"
echo "arch:$(uname -m)"
echo "kernel:$(uname -r)"
echo "systemd:$(systemctl --version 2>/dev/null | head -n1)"
echo "cgroup:$( [ -f /sys/fs/cgroup/cgroup.controllers ] && echo cgroup2fs || stat -fc %T /sys/fs/cgroup 2>/dev/null)"
echo "dockercli:$(docker --version 2>/dev/null)"
echo "docker:$(docker version --format '{{.Server.Version}}' 2>/dev/null)"
echo "listen:$( (ss -ltnpH 2>/dev/null || netstat -ltnp 2>/dev/null) | awk '$4 ~ /:(80|443)$/ {print $NF}' | tr '\n' ' ')"
echo "published:$( (docker ps --filter publish=80 --format '{{.Names}}|{{.Image}}'; docker ps --filter publish=443 --format '{{.Names}}|{{.Image}}') 2>/dev/null | sort -u | tr '\n' ' ')"
`

// Requirement is one of D-29's checks: what was found and whether it is met.
type Requirement struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Found string `json:"found"`
	Met   bool   `json:"met"`
	// Fix says what to do when it isn't met.
	Fix string `json:"fix,omitempty"`
}

// Requirements is a server checked against D-29.
type Requirements struct {
	Met    bool          `json:"met"`
	OS     string        `json:"os"`
	Checks []Requirement `json:"checks"`
}

// ErrRequirementsNotMet: provisioning refused a server that misses one of
// D-29's requirements, before changing anything on it.
var ErrRequirementsNotMet = errors.New("the server doesn't meet Toft's requirements")

var (
	kernelRe = regexp.MustCompile(`^(\d+)\.(\d+)`)
	userRe   = regexp.MustCompile(`\(\("([^"]+)"`)
)

func parseRequirements(raw string) Requirements {
	facts := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), ":"); ok {
			facts[k] = strings.TrimSpace(v)
		}
	}
	r := Requirements{OS: facts["os"], Checks: make([]Requirement, 0, 6)}
	add := func(id, label, found string, met bool, fix string) {
		if met {
			fix = ""
		}
		r.Checks = append(r.Checks, Requirement{ID: id, Label: label, Found: found, Met: met, Fix: fix})
	}

	arch := map[string]string{"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[facts["arch"]]
	osName := facts["os"]
	if osName == "" {
		osName = "Linux"
	}
	add("os", "64-bit Linux", strings.TrimSpace(osName+" · "+orDash(arch, facts["arch"])), arch != "",
		"Toft runs on 64-bit Linux on amd64 or arm64.")

	distro := strings.Fields(facts["distro"])
	add("distro", "Debian or Ubuntu", osName, slices.Contains(distro, "debian") || slices.Contains(distro, "ubuntu"),
		"Toft installs Docker, its firewall and security updates with apt: use a Debian or Ubuntu server.")

	uid, user, _ := strings.Cut(facts["uid"], " ")
	signedIn := "root"
	if uid != "0" {
		signedIn = strings.TrimSpace(user + " (uid " + orDash(uid, "?") + ")")
	}
	add("root", "Signed in as root", signedIn, uid == "0",
		"Toft 1.0 signs in as root and runs its install without sudo: add the key for root, then check again.")

	sd := facts["systemd"]
	add("systemd", "systemd", orDash(strings.Join(firstFields(sd, 2), " "), "not found"), strings.HasPrefix(sd, "systemd "),
		"Toft needs systemd: its firewall and backups run as systemd units.")

	cg := facts["cgroup"]
	cgFound := map[string]string{"cgroup2fs": "v2, unified hierarchy", "tmpfs": "v1"}[cg]
	add("cgroup", "cgroup v2", orDash(cgFound, cg), cg == "cgroup2fs",
		"Toft needs cgroup v2 for memory limits and their kills. Boot with systemd.unified_cgroup_hierarchy=1, or use a newer distribution.")

	kernel := facts["kernel"]
	kernelOK := false
	if m := kernelRe.FindStringSubmatch(kernel); m != nil {
		major, _ := strconv.Atoi(m[1])
		minor, _ := strconv.Atoi(m[2])
		kernelOK = major > 5 || (major == 5 && minor >= 10)
	}
	add("kernel", "Kernel 5.10 or newer", orDash(kernel, "not read"), kernelOK, "Upgrade the kernel to 5.10 or newer, then check again.")

	dv, cli := facts["docker"], facts["dockercli"]
	switch {
	case dv == "" && cli == "":
		add("docker", "Docker Engine 29 or newer", "not installed: provisioning installs it", true, "")
	case dv == "":
		add("docker", "Docker Engine 29 or newer", "installed, but the daemon isn't running", false,
			"Start Docker on the server (systemctl start docker), then check again.")
	default:
		major, _ := strconv.Atoi(strings.SplitN(dv, ".", 2)[0])
		add("docker", "Docker Engine 29 or newer", dv, major >= 29,
			"Docker "+dv+" is too old: Toft needs Docker Engine 29 or newer. Upgrading Docker restarts every container on the machine, so Toft won't do it for you. Upgrade it on the server, then check again.")
	}

	ports, portsOK, portsFix := portsFact(facts["listen"], facts["published"])
	add("ports", "Ports 80 and 443", ports, portsOK, portsFix)

	r.Met = true
	for _, c := range r.Checks {
		r.Met = r.Met && c.Met
	}
	return r
}

// portsFact says who holds ports 80 and 443: nobody, Toft's Traefik (or a
// Traefik container provisioning adopts), another container, or a process.
func portsFact(listen, published string) (found string, met bool, fix string) {
	if strings.TrimSpace(listen) == "" && strings.TrimSpace(published) == "" {
		return "free", true, ""
	}
	var others []string
	for _, c := range strings.Fields(published) {
		name, image, _ := strings.Cut(c, "|")
		if name == "paas-traefik" || strings.Contains(image, "traefik") {
			continue
		}
		others = append(others, name)
	}
	if len(others) > 0 {
		return "used by container " + strings.Join(others, ", "), false,
			"Ports 80 and 443 are Traefik's. Stop or move " + strings.Join(others, ", ") + ", then check again."
	}
	if strings.TrimSpace(published) != "" {
		return "used by Traefik", true, ""
	}
	procs := map[string]bool{}
	for _, l := range strings.Fields(listen) {
		if m := userRe.FindStringSubmatch(l); m != nil {
			procs[m[1]] = true
		} else if _, p, ok := strings.Cut(l, "/"); ok {
			procs[p] = true // netstat: pid/name
		}
	}
	var names []string
	for p := range procs {
		if p != "docker-proxy" {
			names = append(names, p)
		}
	}
	if len(names) == 0 {
		return "used by Traefik", true, ""
	}
	return "used by " + strings.Join(names, ", "), false,
		"Ports 80 and 443 are Traefik's. Stop " + strings.Join(names, ", ") + " (for example systemctl disable --now " + names[0] + "), then check again."
}

func firstFields(s string, n int) []string {
	f := strings.Fields(s)
	if len(f) > n {
		f = f[:n]
	}
	return f
}

func orDash(v, fallback string) string {
	if v != "" {
		return v
	}
	if fallback != "" {
		return fallback
	}
	return "—"
}

// unmet names the requirements that are not met, for an error.
func (r Requirements) unmet() string {
	var s []string
	for _, c := range r.Checks {
		if !c.Met {
			s = append(s, c.Label+" ("+c.Found+")")
		}
	}
	return strings.Join(s, "; ")
}

// summary is the provisioning log's line: "Ubuntu 24.04.4 LTS · amd64,
// kernel 6.8.0, cgroup v2, Docker 29.8.0".
func (r Requirements) summary() string {
	found := map[string]string{}
	for _, c := range r.Checks {
		found[c.ID] = c.Found
	}
	docker := "no Docker yet"
	if !strings.HasPrefix(found["docker"], "not installed") {
		docker = "Docker " + found["docker"]
	}
	kernel := strings.SplitN(found["kernel"], "-", 2)[0]
	return found["os"] + ", kernel " + kernel + ", cgroup v2, " + docker
}

// checkRequirements reads the server through sc and returns them; the
// provisioner calls it first.
func checkRequirements(sc shell) (Requirements, error) {
	out, err := sc.Exec(requirementsScript)
	if err != nil {
		return Requirements{}, fmt.Errorf("read the server's system: %w", err)
	}
	return parseRequirements(out), nil
}

// CheckRequirements connects to the server (pinning its host key on the
// first connection) and checks it against D-29. Read-only.
func (s *Service) CheckRequirements(ctx context.Context, id string) (*Requirements, error) {
	srv, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get server: %w", err)
	}
	r, err := checkRequirements(shellFor(ctx, s.SSH(srv)))
	if err != nil {
		return nil, err
	}
	return &r, nil
}
