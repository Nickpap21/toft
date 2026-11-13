// Package hoststats is the command that samples a server for the metrics
// collector: the panel runs it over SSH, and a server's agent runs it
// itself every minute and keeps the output while the panel is away
// (D-113). internal/docker parses its output (parseHostStats).
package hoststats

import (
	"strconv"
	"strings"

	"github.com/Nickpap21/toft/backend/internal/rollout"
)

// Separator separates the sections of Cmd's output. It cannot be a line of
// any section: a container name starts with a letter or digit, a listing
// or kill line with a quote, a counter or restart line with a hex ID or
// "host", and the window's end with "until".
const Separator = "@@"

// Cmd samples a server in one exec, in six sections:
//
//  1. The running containers: names, the three labels attribution reads
//     and the full ID.
//  2. docker stats: one CPU and memory sample of every running container.
//  3. Each running container's cgroup v2 counters, from its cgroup under
//     the systemd driver or else the cgroupfs one: "<id>\t<nr_periods>\t
//     <nr_throttled>\t<cpu.pressure some avg60>", "-" for a file the kernel
//     does not have (PSI needs a kernel built with it); then "host\t<some
//     avg60 of /proc/pressure/cpu>". memory.peak is not read: it counts
//     page cache, so a database's peak would be its cache, not its use.
//  4. Memory kills (docker events) from since to now on the server's own
//     clock, then "until\t<now>" once docker events succeeded; since 0, or
//     one ahead of the server's clock, reads the last minute. --until in the
//     past makes docker events return at once.
//  5. Each running container's restart count: "<id> <RestartCount>".
//  6. The request latency histograms of Traefik's Toft services, from its
//     loopback metrics entrypoint (ParseLatency), or "unmeasured\t<why>".
//     The sections come in the order they were added, so a panel that
//     predates one reads those before it as it always did.
//
// The listing and the kills print every field as a JSON string, or a
// number: label values come from images, which tenants choose, and may hold
// tabs or newlines that would otherwise forge lines or sections. A kill
// carries the name and labels from the event because a container killed
// for memory that did not restart is gone from the listing.
//
// A failure to read counters, kills, restart counts or latency never fails
// the sample: the shell writes "-" for an unreadable file, the kill and
// restart sections' exit statuses are ignored, and the latency section
// prints why it read none.
func Cmd(since int64) string {
	return `avg60() { set -- $(cat "$1" 2>/dev/null); case $3 in avg60=*) echo "${3#avg60=}" ;; *) echo - ;; esac; }; ` +
		`docker ps --no-trunc --format '{{json .Names}}\t{{json (.Label "` + rollout.LabelServiceID + `")}}\t{{json (.Label "` + rollout.LabelComposeProject + `")}}\t{{json (.Label "` + rollout.LabelComposeService + `")}}\t{{json .ID}}'` +
		` && echo ` + Separator +
		` && docker stats --no-stream --format '{{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}'` +
		` && echo ` + Separator +
		` && for id in $(docker ps -q --no-trunc); do` +
		` d=/sys/fs/cgroup/system.slice/docker-$id.scope; [ -d "$d" ] || d=/sys/fs/cgroup/docker/$id;` +
		` np=-; nt=-;` +
		` set -- $(cat "$d/cpu.stat" 2>/dev/null);` +
		` while [ $# -ge 2 ]; do case $1 in nr_periods) np=$2 ;; nr_throttled) nt=$2 ;; esac; shift 2; done;` +
		` printf '%s\t%s\t%s\t%s\n' "$id" "$np" "$nt" "$(avg60 "$d/cpu.pressure")";` +
		` done && printf 'host\t%s\n' "$(avg60 /proc/pressure/cpu)"` +
		` && echo ` + Separator +
		` && now=$(date +%s) && s=` + strconv.FormatInt(since, 10) +
		` && { [ "$s" -gt 0 ] && [ "$s" -le "$now" ] || s=$((now-60)); }` +
		` && { docker events --since "$s" --until "$now" --filter event=oom --format '` + KillFormat + `' && printf 'until\t%s\n' "$now" || true; }` +
		` && echo ` + Separator +
		` && { ids=$(docker ps -q --no-trunc); [ -z "$ids" ] || docker inspect --format '{{.Id}} {{.RestartCount}}' $ids 2>/dev/null || true; }` +
		` && echo ` + Separator +
		` && ` + latencyCmd
}

// KillFormat prints a kill as a listing line (the name and labels from the
// event's attributes, then the ID) followed by the time, unix seconds. The
// tabs are template output ({{"\t"}}): unlike docker ps and docker stats,
// docker events does not turn a literal \t in --format into a tab.
const KillFormat = `{{json (index .Actor.Attributes "name")}}` +
	`{{"\t"}}{{json (index .Actor.Attributes "` + rollout.LabelServiceID + `")}}` +
	`{{"\t"}}{{json (index .Actor.Attributes "` + rollout.LabelComposeProject + `")}}` +
	`{{"\t"}}{{json (index .Actor.Attributes "` + rollout.LabelComposeService + `")}}` +
	`{{"\t"}}{{json .Actor.ID}}{{"\t"}}{{.Time}}`

// Until returns the end of the kill window Cmd's output reports: the since
// of the next sample. 0 when docker events failed, so the next sample reads
// the window again. Only the kill section, the fourth, is read: a kill line
// holds JSON strings, so it cannot start with "until".
func Until(out string) int64 {
	section := 0
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimRight(l, "\r")
		if strings.TrimSpace(l) == Separator {
			section++
			continue
		}
		if section != 3 {
			continue
		}
		if v, ok := strings.CutPrefix(l, "until\t"); ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}
