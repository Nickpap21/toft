#!/bin/sh
# Reports what an app container can reach on the host it runs on (D-37,
# private/specs/2026-10-12-host-firewall-design.md). Run it on a
# server, before and after Provision (or toft upgrade on the control plane).
# It starts one short-lived probe container on NETWORK and changes no rules.
#
#   scripts/host-firewall-check.sh [NETWORK] [SSH_PORT]
#
# NETWORK defaults to paas-network (where apps run under legacy); under
# isolated app networks, pass a paas-env-* network. SSH_PORT defaults to the
# port sshd listens on (22 if that cannot be read): checking a port nothing
# listens on would pass whatever the firewall does.
# Optional: HOST_IP (default: the host's first address), DOMAIN (an app's
# domain, checked over https), PROBE_IMAGE (default alpine:3).
#
# Exits 1 when something the host firewall should block is reachable, or
# something it must leave alone is not.
set -eu

NET="${1:-paas-network}"
SSH_PORT="${2:-$(ss -Htlnp 2>/dev/null | awk '/"sshd"/ {n = split($4, a, ":"); print a[n]; exit}')}"
SSH_PORT="${SSH_PORT:-22}"
HOST_IP="${HOST_IP:-$(hostname -I 2>/dev/null | awk '{print $1}')}"
GATEWAY=$(docker network inspect -f '{{range .IPAM.Config}}{{.Gateway}}{{end}}' "$NET")
[ -n "$GATEWAY" ] || { echo "network $NET has no IPv4 gateway" >&2; exit 2; }

docker run --rm --network "$NET" \
    -e GATEWAY="$GATEWAY" -e HOST_IP="$HOST_IP" -e SSH_PORT="$SSH_PORT" -e DOMAIN="${DOMAIN:-}" \
    "${PROBE_IMAGE:-alpine:3}" sh -c '
fail=0
# want: "blocked" or "open". "blocked" also passes when nothing listens.
probe() {
    want=$1; what=$2; shift 2
    if "$@" >/dev/null 2>&1; then got=open; else got=blocked; fi
    if [ "$got" = "$want" ]; then mark=ok; else mark=FAIL; fail=1; fi
    printf "%-5s %-48s %-8s (want %s)\n" "$mark" "$what" "$got" "$want"
}
# Any HTTP answer counts, a 401 or 404 included: the question is whether
# the domain is reachable, and busybox wget fails on every 4xx and 5xx.
answers() { out=$(wget -q -T 5 -O /dev/null "$1" 2>&1) || echo "$out" | grep -q "server returned error"; }
probe blocked "host sshd via the gateway ($GATEWAY:$SSH_PORT)" nc -z -w 3 "$GATEWAY" "$SSH_PORT"
[ -n "$HOST_IP" ] && probe blocked "host sshd via its address ($HOST_IP:$SSH_PORT)" nc -z -w 3 "$HOST_IP" "$SSH_PORT"
probe blocked "cloud metadata (169.254.169.254:80)" nc -z -w 3 169.254.169.254 80
probe open "Traefik via the gateway ($GATEWAY:443)" nc -z -w 3 "$GATEWAY" 443
probe open "DNS (resolve example.com)" nslookup example.com
probe open "internet (http://example.com)" wget -q -T 5 -O /dev/null http://example.com
[ -n "$DOMAIN" ] && probe open "the app domain (https://$DOMAIN)" answers "https://$DOMAIN"
exit $fail
'
