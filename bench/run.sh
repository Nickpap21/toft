#!/bin/sh
# Measures one platform on one fresh server (D-131); bench/README.md has the
# method and the servers. Run it here, not on the server:
#
#   bench/run.sh toft|coolify|dokploy root@SERVER_IP ROUND
#
# Needs ssh, curl, jq and perl (for millisecond times). Writes
# bench/results/ROUND-PLATFORM.json and the raw readings beside it.
set -eu

[ $# -eq 3 ] || { echo "usage: bench/run.sh toft|coolify|dokploy root@SERVER_IP ROUND" >&2; exit 2; }
case "$1" in
toft | coolify | dokploy) ;;
*) echo "run.sh: unknown platform $1" >&2; exit 2 ;;
esac
PLATFORM=$1 HOST=$2 ROUND=$3 IP=${2#*@}
DIR=$(cd "$(dirname "$0")" && pwd)
OUT=$DIR/results/$ROUND-$PLATFORM
EMAIL=${BENCH_EMAIL:-bench@example.com}
# The panel account's password: upper and lower case, digits and a symbol,
# which every platform's rules accept. Shown here, never written to results.
PASSWORD=${BENCH_PASSWORD:-B-$(openssl rand -hex 12)-x}
case "$PASSWORD" in *[\'\ ]*) echo "run.sh: BENCH_PASSWORD can't hold a quote or a space" >&2; exit 2 ;; esac
mkdir -p "$DIR/results"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

now() { perl -MTime::HiRes=time -e 'printf "%.3f\n", time'; }
since() { perl -MTime::HiRes=time -e 'printf "%.1f\n", time - $ARGV[0]' "$1"; }
on() { ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ServerAliveInterval=15 "$HOST" "$@"; }
log() { printf '%s %s\n' "$(date +%T)" "$*" >&2; }
put() { echo "$1 $2" >>"$TMP/put"; log "$1 = $2"; }
median() { sort -n | awk '{a[NR]=$1} END{print NR%2 ? a[(NR+1)/2] : (a[NR/2]+a[NR/2+1])/2}'; }
mean() { awk '{s+=$1} END{printf "%.2f\n", s/NR}'; }

# The app's container hostname, through the platform's proxy (whoami prints it).
hostname_of() { curl -sk --max-time 5 --resolve "$1:443:$IP" "https://$1/" 2>/dev/null | sed -n 's/^Hostname: //p'; }
# Waits until the app answers with a hostname other than $2 (any, at first).
wait_new() {
	deadline=$(($(date +%s) + 1200))
	while h=$(hostname_of "$1") && { [ -z "$h" ] || [ "$h" = "$2" ]; }; do
		[ "$(date +%s)" -lt "$deadline" ] || { log "$1 still not answering after 20 minutes"; exit 1; }
		sleep 0.5
	done
}
# Waits until 20 answers in a row come from one container other than $2, so
# the previous one is out of rotation, and prints its hostname.
settle() {
	deadline=$(($(date +%s) + 300)) n=0 last=
	while [ "$n" -lt 20 ]; do
		[ "$(date +%s)" -lt "$deadline" ] || { log "$1 still answering from more than one container after 5 minutes"; exit 1; }
		h=$(hostname_of "$1")
		if [ -z "$h" ] || [ "$h" = "$2" ]; then n=0; elif [ "$h" = "$last" ]; then n=$((n + 1)); else n=1; fi
		last=$h
		sleep 0.1
	done
	echo "$last"
}

# install, panel_up, account, setup, deploy_image, deploy_git and redeploy.
# shellcheck source=/dev/null # the platform is chosen at run time
. "$DIR/$PLATFORM.sh"

log "checking $HOST"
if on 'command -v docker' >/dev/null; then
	echo "run.sh: $HOST has Docker already; measure a fresh server" >&2
	exit 1
fi
put os "$(on '. /etc/os-release && echo "$VERSION_ID"')"
put cpus "$(on nproc)"
put mem_total_kib "$(on "awk '/^MemTotal:/{print \$2}' /proc/meminfo")"
on 'mkdir -p /root/bench && cat >/root/bench/sample.sh' <"$DIR/sample.sh"

log "the fresh server: 5 minutes of readings"
on sh /root/bench/sample.sh idle 300 >"$OUT-fresh.txt"
fresh=$(cut -d' ' -f1 "$OUT-fresh.txt" | median)
disk=$(on sh /root/bench/sample.sh disk)

log "installing $PLATFORM"
t=$(now)
install
deadline=$(($(date +%s) + 1200))
until panel_up; do
	[ "$(date +%s)" -lt "$deadline" ] || { log "the panel still not answering after 20 minutes"; exit 1; }
	sleep 1
done
put install_s "$(since "$t")"

log "account: $EMAIL, password $PASSWORD"
account
setup

log "settling for 10 minutes, then 10 minutes of readings"
sleep 600
on sh /root/bench/sample.sh idle 600 >"$OUT-idle.txt"
put idle_mem_mib "$(cut -d' ' -f1 "$OUT-idle.txt" | median | awk -v f="$fresh" '{printf "%.0f\n", ($1-f)/1024}')"
put idle_cpu_pct "$(cut -d' ' -f2 "$OUT-idle.txt" | mean)"
put disk_mib "$((($(on sh /root/bench/sample.sh disk) - disk) / 1048576))"
on "docker stats --no-stream --format '{{.Name}} {{.MemUsage}} {{.CPUPerc}}'; docker system df" >"$OUT-containers.txt"

for kind in image git; do
	host=$kind.$IP.sslip.io
	log "first deploy: $kind at $host"
	t=$(now)
	id=$(deploy_$kind "$host")
	wait_new "$host" ""
	put "first_${kind}_s" "$(since "$t")"
	old=$(settle "$host" "")
	for i in 1 2 3 4 5; do
		log "redeploy $i of 5: $kind"
		on rm -f /root/bench/stop
		on sh /root/bench/sample.sh probe "$host" </dev/null >"$TMP/probe" &
		t=$(now)
		redeploy "$id" "$kind"
		wait_new "$host" "$old"
		put "redeploy_${kind}_s" "$(since "$t")"
		# Failures as the previous container goes count too: the probe runs
		# until it has stopped, and 5 s on for the proxy to let it go.
		new=$(settle "$host" "$old")
		on sh /root/bench/sample.sh gone "$old" "$new"
		old=$new
		sleep 5
		on touch /root/bench/stop
		wait
		[ -s "$TMP/probe" ] || { log "the probe on the server ended without a count"; exit 1; }
		put "redeploy_${kind}_down_s" "$(cat "$TMP/probe")"
	done
done

jq -Rn --arg p "$PLATFORM" --arg r "$ROUND" '
	reduce (inputs | capture("^(?<k>\\S+) (?<v>.*)$")) as $l ({platform: $p, round: $r};
		.[$l.k] += [$l.v | tonumber? // .])' "$TMP/put" >"$OUT.json"
log "done: $OUT.json"
log "the dashboard, from the fourth server (bench/README.md):"
printf '  PLATFORM=%s ROUND=%s PANEL=%s LOGIN=%s PAGE=%s EMAIL=%s PASSWORD=%s\n' \
	"$PLATFORM" "$ROUND" "$PANEL" "$LOGIN" "$PAGE" "$EMAIL" "$PASSWORD" >&2
