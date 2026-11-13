#!/bin/sh
# Installs toft-agent and joins this server to its Toft panel (D-106).
# Served by the panel at @PANEL@/api/v1/agent/install.sh, run as root:
#   printf 'Authorization: Bearer %s\n' toft_join_... | curl -fsSL -H @- @PANEL@/api/v1/agent/install.sh | sudo sh
# The panel writes the token in (TOKEN below), so it never appears in a
# command line `ps` shows other accounts (D-123): printf is the shell's
# own, and this script passes the token on by stdin and a root-only file.
# `sh -s -- --token toft_join_...` still works.
# On a panel without a domain the join command checks this script's
# SHA-256 before running it, and this script the agent's (D-106); both
# downloads and the agent's join go only to the panel certificate whose
# public key's SHA-256 is PIN, so nothing in between reads the token.
# It checks the server, downloads the agent from the panel, installs
# toft-agent.service and waits for it to connect. Nothing listens on this
# server: the agent dials out to the panel on 443.
# The whole script is main, run on its last line: a download cut off
# early defines nothing that runs, so sh runs no part of it.
main() {
set -eu

PANEL='@PANEL@'
PANEL_KEY='@PANEL_KEY@'
INSECURE='@INSECURE@'
PIN='@CERT_PIN@'
TOKEN='@TOKEN@'

while [ $# -gt 0 ]; do
	case "$1" in
	--token) TOKEN="${2:-}"; shift 2 ;;
	--token=*) TOKEN="${1#--token=}"; shift ;;
	*) echo "unknown option: $1" >&2; exit 2 ;;
	esac
done

fail() { echo "toft-agent install: $*" >&2; exit 1; }

[ -n "$TOKEN" ] || fail "--token is required (make one in the panel: Servers → Add)"
[ "$(id -u)" = 0 ] || fail "run as root (sudo sh)"
[ "$(uname -s)" = Linux ] || fail "Toft servers run Linux"
case "$(uname -m)" in
x86_64 | amd64) ARCH=amd64 ;;
aarch64 | arm64) ARCH=arm64 ;;
*) fail "unsupported architecture $(uname -m): amd64 or arm64" ;;
esac
[ -d /run/systemd/system ] || fail "systemd is required"
command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is required"
case "$ARCH" in
amd64) SUM='@AGENT_SHA256_AMD64@' ;;
arm64) SUM='@AGENT_SHA256_ARM64@' ;;
esac
[ -n "$SUM" ] || fail "the panel has no agent for linux/$ARCH"

CURL="curl -fsS --retry 3"
[ "$INSECURE" = 1 ] && CURL="$CURL -k --pinnedpubkey sha256//$PIN"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
echo "Downloading toft-agent (linux/$ARCH) from $PANEL..."
printf 'Authorization: Bearer %s\n' "$TOKEN" | $CURL -H @- -D "$TMP/headers" -o "$TMP/toft-agent" \
	"$PANEL/api/v1/agent/binary?arch=$ARCH" || {
	[ $? = 90 ] && fail "the panel's certificate changed since the join command was made (the panel restarted): make a new join command in the panel"
	fail "the panel refused the download: the token may be used or expired"
}
SERVER=$(tr -d '\r' <"$TMP/headers" | awk -F': ' 'tolower($1)=="x-toft-server"{print $2}' | tail -n 1)
[ -n "$SERVER" ] || fail "the panel didn't name this server"
echo "$SUM  $TMP/toft-agent" | sha256sum -c >/dev/null 2>&1 || fail "the downloaded agent isn't the one the panel vouched for: not installed"
chmod 755 "$TMP/toft-agent"
VERSION=$("$TMP/toft-agent" version) || fail "the downloaded agent doesn't run here"

DIR=/usr/local/lib/toft-agent/releases/$VERSION
mkdir -p "$DIR"
mv "$TMP/toft-agent" "$DIR/toft-agent"
ln -sfn "releases/$VERSION" /usr/local/lib/toft-agent/current.tmp
mv -T /usr/local/lib/toft-agent/current.tmp /usr/local/lib/toft-agent/current
ln -sfn /usr/local/lib/toft-agent/current/toft-agent /usr/local/bin/toft-agent

install -d -m 700 /etc/toft-agent
(umask 077 && printf '%s' "$TOKEN" >/etc/toft-agent/join-token)
set -- --panel "$PANEL" --server "$SERVER" --token-file /etc/toft-agent/join-token --panel-key "$PANEL_KEY"
[ "$INSECURE" = 1 ] && set -- "$@" --insecure-tls --panel-cert "$PIN"
exec /usr/local/bin/toft-agent join "$@"
}

main "$@"
