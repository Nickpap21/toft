// Package hostupdate is a host's updates run (D-134): install the pending
// updates with Docker held back, update Docker, or reboot. The panel starts
// it over SSH and through its host helper, and a server's agent starts it
// itself (agentproto.SubsystemHostUpdate), with the same lines: the run is
// the transient systemd unit Unit, which outlives whoever started it, and
// keeps its state in Dir for StatusScript to read.
package hostupdate

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Actions.
const (
	ActionInstall = "install"
	ActionDocker  = "docker"
	ActionReboot  = "reboot"
)

// Outcomes and steps the run writes.
const (
	OutcomeRunning   = "running"
	OutcomeRebooting = "rebooting"
	OutcomeSucceeded = "succeeded"
	OutcomeFailed    = "failed"

	StepStarting = "starting"
	StepLists    = "lists"
	StepInstall  = "install"
	StepReboot   = "reboot"
	StepDone     = "done"
)

// Unit is the run's transient unit. Its fixed name is the lock: systemd
// refuses a second while one is loaded.
const Unit = "toft-host-update.service"

// Dir holds the script, the run's state and its output on the host.
const Dir = "/var/lib/toft/host-update"

// DockerPackages are Docker's packages, from Docker's repository or the
// distribution's: the install holds back those installed, and the Docker
// update installs them.
var DockerPackages = []string{"docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin", "docker-compose-plugin",
	"docker-ce-rootless-extras", "docker.io", "containerd", "runc"}

// IsDockerPackage reports whether name is one of DockerPackages.
func IsDockerPackage(name string) bool { return slices.Contains(DockerPackages, name) }

// OutputLines is how much of the run's output StatusScript prints.
const OutputLines = 200

var idRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Valid reports whether id and action can go into StartLine.
func Valid(id, action string) bool {
	switch action {
	case ActionInstall, ActionDocker, ActionReboot:
		return idRe.MatchString(id)
	}
	return false
}

// Script is the run, as sh "$Dir/run.sh" <id> <action>. It writes its state
// to state (key:value lines, replaced whole) at each step and its output to
// out. Docker is held for the install by a pin file passed for that run
// only (-o Dir::Etc::Preferences), never apt-mark hold, which would outlive
// it; /etc/apt/preferences, which that option replaces, is copied in first.
// A changed config file keeps the host's copy, and apt waits for another
// apt's lock instead of failing. needrestart restarts the services whose
// libraries changed; its own defaults leave docker alone. No autoremove:
// it can take what someone installed by hand.
var Script = `D=${TOFT_HOST_UPDATE_DIR:-` + Dir + `}
id=$1 action=$2
step=starting outcome=running packages=0 err= finished=
started=$(date +%s)
boot=$(cat /proc/sys/kernel/random/boot_id 2>/dev/null)
put() {
	printf 'id:%s\naction:%s\nstep:%s\noutcome:%s\nstarted:%s\nfinished:%s\nboot:%s\npackages:%s\nerror:%s\n' \
		"$id" "$action" "$step" "$outcome" "$started" "$finished" "$boot" "$packages" "$err" >"$D/state.new" && mv "$D/state.new" "$D/state"
}
fail() {
	e=$(grep '^E: ' "$D/out" | tail -n 1)
	err=$(printf '%s%s' "$1" "${e:+: ${e#E: }}" | tr -d '\r\n' | cut -c1-400)
	outcome=failed finished=$(date +%s)
	put
	exit 1
}
: >"$D/out"
exec >>"$D/out" 2>&1 </dev/null
put
export DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a LC_ALL=C
apt="apt-get -y -q -o DPkg::Lock::Timeout=600 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold"
dockerpkgs() {
	dpkg-query -W -f='${Status} ${Package}\n' ` + strings.Join(DockerPackages, " ") + ` 2>/dev/null | awk '$3 == "installed" {print $4}'
}
count() { grep -c '^Inst ' || true; }
engine() { docker version --format '{{.Server.Version}}' 2>/dev/null; }
case $action in
reboot)
	step=reboot outcome=rebooting
	put
	systemctl reboot || fail "The reboot couldn't start"
	exit 0
	;;
install)
	# An install an earlier run left half done first: the pin below holds
	# the versions installed now.
	dpkg --configure -a || fail "dpkg couldn't finish an earlier install"
	pin=$D/hold.pref
	: >"$pin"
	if [ -f /etc/apt/preferences ]; then cat /etc/apt/preferences >>"$pin"; echo >>"$pin"; fi
	for p in $(dockerpkgs); do
		printf 'Package: %s\nPin: version %s\nPin-Priority: 1001\n\n' "$p" "$(dpkg-query -W -f='${Version}' "$p")" >>"$pin"
	done
	apt="$apt -o Dir::Etc::Preferences=$pin"
	step=lists; put
	apt-get -q -o DPkg::Lock::Timeout=600 update || fail "apt couldn't refresh its package lists"
	packages=$($apt -s dist-upgrade | count)
	step=install; put
	$apt dist-upgrade || fail "apt couldn't install the updates"
	;;
docker)
	pkgs=$(dockerpkgs)
	[ -n "$pkgs" ] || fail "Docker wasn't installed from a package"
	dpkg --configure -a || fail "dpkg couldn't finish an earlier install"
	step=lists; put
	apt-get -q -o DPkg::Lock::Timeout=600 update || fail "apt couldn't refresh its package lists"
	packages=$($apt -s install --only-upgrade $pkgs | count)
	step=install; put
	$apt install --only-upgrade $pkgs || fail "apt couldn't update Docker"
	# The package restarts Docker as it installs, unless something stops
	# it (a policy-rc.d): then restart it here, or say the old one runs.
	pkg=docker-ce
	dpkg-query -W -f='${Status}' docker-ce 2>/dev/null | grep -q 'ok installed' || pkg=docker.io
	want=$(dpkg-query -W -f='${Version}' $pkg | sed 's/^[0-9]*://; s/-[^-]*$//')
	if [ "$(engine)" != "$want" ]; then
		echo "Docker $(engine) still runs: restarting it on $want."
		systemctl restart docker || fail "Docker didn't restart"
		i=0
		while [ "$(engine)" != "$want" ] && [ $i -lt 60 ]; do sleep 1; i=$((i + 1)); done
		[ "$(engine)" = "$want" ] || fail "Docker runs $(engine) after the update, not $want"
	fi
	;;
*)
	fail "Unknown action"
	;;
esac
step=done outcome=succeeded finished=$(date +%s)
put
`

// StartLine starts the run, Script on its stdin, and prints "started", or
// "busy" when one is loaded already. The script is renamed into place, so a
// run reading the last one keeps reading it. id and action must be Valid.
func StartLine(id, action string) (string, error) {
	if !Valid(id, action) {
		return "", fmt.Errorf("invalid run %q %q", id, action)
	}
	return `D=${TOFT_HOST_UPDATE_DIR:-` + Dir + `}
command -v systemd-run >/dev/null 2>&1 || { echo "this host has no systemd-run"; exit 1; }
if systemctl is-active --quiet ` + Unit + `; then echo busy; exit 0; fi
mkdir -p "$D" && chmod 700 "$D" && cat >"$D/run.sh.new" && mv "$D/run.sh.new" "$D/run.sh" || exit 1
if ! systemd-run --unit=` + Unit + ` --collect --quiet --description='Toft: host updates (` + action + `)' /bin/sh "$D/run.sh" ` + id + ` ` + action + `; then
	if systemctl is-active --quiet ` + Unit + `; then echo busy; exit 0; fi
	exit 1
fi
echo started
`, nil
}

// StatusScript prints whether the unit runs, the host's boot ID and clock,
// the last run's state and its output's last lines after a separator line.
var StatusScript = `D=${TOFT_HOST_UPDATE_DIR:-` + Dir + `}
printf 'active:%s\n' "$(systemctl is-active ` + Unit + ` 2>/dev/null)"
printf 'nowboot:%s\n' "$(cat /proc/sys/kernel/random/boot_id 2>/dev/null)"
printf 'now:%s\n' "$(date +%s)"
cat "$D/state" 2>/dev/null
echo ` + separator + `
tail -n ` + strconv.Itoa(OutputLines) + ` "$D/out" 2>/dev/null | cut -c1-500
`

const separator = "--output--"

// Status is what StatusScript printed, its times on the panel's clock.
type Status struct {
	// Active: the unit runs (or activates).
	Active bool
	// NowBoot is the host's boot now, Boot the one the run started in.
	NowBoot, Boot string
	// ID is "" before any run.
	ID, Action, Step, Outcome string
	Started                   time.Time
	Finished                  *time.Time
	Packages                  int
	Error                     string
	Output                    []string
}

// Parse reads StatusScript's output at now, the panel's clock, correcting
// the host's times for a host clock that is off.
func Parse(out string, now time.Time) (Status, error) {
	var st Status
	st.Output = []string{}
	head, tail, ok := strings.Cut(out, separator+"\n")
	if !ok {
		head, _, ok = strings.Cut(out, separator)
	}
	if !ok {
		return st, fmt.Errorf("the status read printed no %s line", separator)
	}
	var hostNow, started, finished int64
	for _, line := range strings.Split(head, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch key {
		case "active":
			st.Active = val == "active" || val == "activating" || val == "reloading"
		case "nowboot":
			st.NowBoot = val
		case "now":
			hostNow, _ = strconv.ParseInt(val, 10, 64)
		case "id":
			st.ID = val
		case "action":
			st.Action = val
		case "step":
			st.Step = val
		case "outcome":
			st.Outcome = val
		case "started":
			started, _ = strconv.ParseInt(val, 10, 64)
		case "finished":
			finished, _ = strconv.ParseInt(val, 10, 64)
		case "boot":
			st.Boot = val
		case "packages":
			st.Packages, _ = strconv.Atoi(val)
		case "error":
			st.Error = val
		}
	}
	skew := time.Duration(0)
	if hostNow > 0 {
		skew = now.Sub(time.Unix(hostNow, 0))
	}
	if started > 0 {
		st.Started = time.Unix(started, 0).Add(skew).UTC()
	}
	if finished > 0 {
		t := time.Unix(finished, 0).Add(skew).UTC()
		st.Finished = &t
	}
	for _, line := range strings.Split(strings.TrimRight(tail, "\n"), "\n") {
		line = screenLine(line)
		if line != "" || len(st.Output) > 0 {
			st.Output = append(st.Output, line)
		}
	}
	return st, nil
}

// screenLine is what a terminal would show of an output line: dpkg redraws
// its progress with carriage returns, so the text after the last one;
// other control characters are dropped.
func screenLine(line string) string {
	line = strings.TrimRight(line, "\r")
	if i := strings.LastIndexByte(line, '\r'); i >= 0 {
		line = line[i+1:]
	}
	line = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' || r == 0x7f {
			return -1
		}
		return r
	}, line)
	return strings.ToValidUTF8(line, "�")
}
