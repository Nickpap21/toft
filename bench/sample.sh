#!/bin/sh
# Runs on the server being measured; bench/run.sh copies it there.
#
#   idle SECONDS   every 10 s, "<memory in use, KiB> <CPU busy, % of all cores>"
#   disk           bytes used on /
#   probe HOST     GET https://HOST/ through the server's own proxy, one request
#                  after another, until /root/bench/stop exists; then how many
#                  seconds had a failed request. Seconds, not requests: a quick
#                  error and a request left hanging then weigh the same.
#   gone OLD NEW   waits until no running container has hostname OLD; fails if
#                  none has NEW, which would mean hostnames aren't matching
set -eu

# busy and idle jiffies since boot, all cores: idle is idle + iowait. Steal
# is left out: it is other tenants' load on the host, not this server's.
cpu() { awk '/^cpu /{print $2+$3+$4+$7+$8, $5+$6}' /proc/stat; }

case "$1" in
idle)
	end=$(($(date +%s) + $2))
	# shellcheck disable=SC2046 # two fields, split on purpose
	set -- $(cpu)
	while [ "$(date +%s)" -lt "$end" ]; do
		sleep 10
		busy=$1 idle=$2
		# shellcheck disable=SC2046 # two fields, split on purpose
		set -- $(cpu)
		mem=$(awk '/^MemTotal:/{t=$2} /^MemAvailable:/{a=$2} END{print t-a}' /proc/meminfo)
		awk -v m="$mem" -v b=$(($1 - busy)) -v i=$(($2 - idle)) 'BEGIN{printf "%d %.2f\n", m, 100*b/(b+i)}'
	done
	;;
disk) df -B1 --output=used / | tail -n1 | tr -d ' ' ;;
probe)
	down=0 counted=0
	while [ ! -e /root/bench/stop ]; do
		from=$(date +%s)
		if ! curl -skf -o /dev/null --max-time 2 --resolve "$2:443:127.0.0.1" "https://$2/"; then
			to=$(date +%s)
			[ "$from" -gt "$counted" ] || from=$((counted + 1))
			[ "$to" -lt "$from" ] || { down=$((down + to - from + 1)); counted=$to; }
		fi
		sleep 0.1
	done
	echo "$down"
	;;
gone)
	running() { docker ps -q | xargs -r docker inspect --format '{{.Config.Hostname}}' 2>/dev/null; }
	running | grep -qx "$3" || { echo "sample.sh: no running container has hostname $3" >&2; exit 1; }
	end=$(($(date +%s) + 300))
	while running | grep -qx "$2"; do
		[ "$(date +%s)" -lt "$end" ] || { echo "sample.sh: $2 still running after 5 minutes" >&2; exit 1; }
		sleep 0.5
	done
	;;
*) echo "sample.sh: idle SECONDS | disk | probe HOST | gone OLD NEW" >&2; exit 2 ;;
esac
