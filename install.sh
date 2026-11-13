#!/bin/sh
# Toft installer (D-61). Prepares a Linux server and starts Toft on it:
#
#   curl -fsSL <url>/install.sh | sudo sh -s -- [options]
#
#   --version vX.Y.Z      the release to install (default: the one this file came with)
#   --app SLUG            open New service with this catalog template after setup (D-59)
#   --registry-token T    a token that may pull Toft's images (TOFT_REGISTRY_TOKEN)
#   --registry-user U     its user name (TOFT_REGISTRY_USER)
#   --pool CIDR           the app networks' address pool (default: 10.203.0.0/16,
#                         or the next /16 free on this host)
#   --restore --kit FILE  on a fresh server: bring a lost panel back from its
#                         backup, with the recovery kit from Instance → Overview
#                         (D-64). The S3 secret key is asked for, or read from
#                         TOFT_RESTORE_SECRET_KEY.
#   --adopt               on a server deploy.sh set up: move its panel onto the
#                         installer (D-66), keeping its database, volumes,
#                         settings and apps, then upgrade it to --version. From
#                         then on it upgrades from Instance, not deploy.sh.
#   --no-git              install without git hosting (D-71): no git container,
#                         no repositories and nothing on port 2222. Undo it by
#                         removing TOFT_GIT=off from /etc/toft/secrets.env and
#                         running toft upgrade --to <this release> --force.
#
# Re-running it is safe: it changes only what is missing, never generates a
# secret again, and prints a new setup code only while nobody has an
# account. Upgrades go through `toft upgrade --to vX.Y.Z`. Change this file
# here; release builds ship it in the backend image's bundle.
# The whole script is main, run on its last line: a download cut off
# early defines nothing that runs, so sh runs no part of it.
main() {
set -eu

RELEASE="${TOFT_VERSION:-}"
OWNER=nickpap21
# The release key's public half, as backend/cmd/toft-agent/release.pub
# (D-106): it signs each release's image manifest.
RELEASE_KEY='-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAKTULe0LxsYl1qA0M9rxdF7cctyVeW6jlYQUYHHcF9uI=
-----END PUBLIC KEY-----'
APP=""
TOKEN="${TOFT_REGISTRY_TOKEN:-}"
TOKEN_USER="${TOFT_REGISTRY_USER:-toft}"
POOL="${TOFT_APP_NETWORK_POOL:-}"
RESTORE=""
ADOPT=""
KIT=""
NOGIT=""
SECRETS=/etc/toft/secrets.env
STATE=/var/lib/toft/upgrade/state.json

bold=$(printf '\033[1m') green=$(printf '\033[32m') red=$(printf '\033[31m') dim=$(printf '\033[2m') off=$(printf '\033[0m')
LOG=/var/log/toft-install.log
# On a terminal, a step that takes a while shows "· doing…" until its line
# replaces it.
tty=""
[ ! -t 1 ] || tty=1
clear() { [ -z "$tty" ] || printf '\r\033[K'; }
doing() { [ -z "$tty" ] || printf '%s· %s…%s' "$dim" "$1" "$off"; }
# detail goes to the log only (fd 3, opened before the checks).
detail() { printf '  %s\n' "$1" >&3; }
section() {
	clear
	printf '\n%s── %s%s\n' "$bold" "$1" "$off"
	printf '\n── %s\n' "$1" >&3
}
ok() {
	clear
	printf '%s✓%s %s%s%s\n' "$green" "$off" "$1" "${2:+  $dim}" "${2:+$2$off}"
	printf '✓ %s%s\n' "$1" "${2:+  $2}" >&3
}
die() {
	clear
	printf '%s✗ %s%s\n' "$red" "$1" "$off" >&2
	printf '✗ %s\n' "$1" >&3
	[ -z "${2:-}" ] || { printf '  %s\n' "$2" >&2; printf '  %s\n' "$2" >&3; }
	if [ -n "$changing" ] && [ -n "$RESTORE" ]; then
		printf '  %sRun this again with the same kit once that is fixed: it continues where it stopped.%s\n' "$dim" "$off" >&2
		printf '  %sA kit with another encryption key needs a fresh server.%s\n' "$dim" "$off" >&2
	elif [ -n "$changing" ]; then
		printf '  %sRun this again once that is fixed: it continues where it stopped.%s\n' "$dim" "$off" >&2
	else
		printf '  %sNothing was changed on this server.%s\n' "$dim" "$off" >&2
	fi
	died=1
	exit 1
}
changing="" died=""

while [ $# -gt 0 ]; do
	case "$1" in
	--version) RELEASE="$2"; shift 2 ;;
	--app) APP="$2"; shift 2 ;;
	--registry-token) TOKEN="$2"; shift 2 ;;
	--registry-user) TOKEN_USER="$2"; shift 2 ;;
	--pool) POOL="$2"; shift 2 ;;
	--restore) RESTORE=1; shift ;;
	--kit) KIT="$2"; shift 2 ;;
	--adopt) ADOPT=1; shift ;;
	--no-git) NOGIT=1; shift ;;
	*) echo "install.sh: unknown option $1" >&2; exit 2 ;;
	esac
done
case "$RELEASE" in
v[0-9]*.[0-9]*.[0-9]*) ;;
*) echo "install.sh: give the release to install, as --version vX.Y.Z" >&2; exit 2 ;;
esac
case "$APP" in
"" | [a-z0-9]*) ;;
*) echo "install.sh: --app takes a catalog slug such as typesense" >&2; exit 2 ;;
esac
[ "$(id -u)" = 0 ] || { echo "install.sh: run it as root (sudo sh)" >&2; exit 1; }

# The recovery kit's "Name:   value" lines (D-64).
kit() { sed -n "s/^$1: *//p" "$KIT" | head -n1; }
if [ -n "$RESTORE" ]; then
	[ -r "$KIT" ] || { echo "install.sh: --restore needs --kit FILE, the recovery kit" >&2; exit 2; }
	# Toft running here refuses, unless it is this restore's own, stopped part-way.
	if [ -e "$STATE" ] && [ ! -e /var/lib/toft/restoring ]; then
		echo "install.sh: Toft already runs here; restore onto a fresh server" >&2
		exit 1
	fi
	KIT_KEY=$(kit "Encryption key")
	KIT_REPO=$(kit "Backup repository")
	KIT_PASSWORD=$(kit "Repository password")
	KIT_ACCESS=$(kit "Access key" | sed 's/ (.*//')
	case "$KIT_KEY" in *[!0-9a-f]* | "") echo "install.sh: the kit's encryption key is missing or damaged" >&2; exit 2 ;; esac
	if [ -z "$KIT_REPO" ] || [ -z "$KIT_PASSWORD" ]; then
		echo "install.sh: the kit names no backup: there is nothing to restore from" >&2
		exit 2
	fi
	SECRET_KEY="${TOFT_RESTORE_SECRET_KEY:-}"
	# Ask only when a terminal is attached: /dev/tty exists without one.
	if [ -z "$SECRET_KEY" ] && (: </dev/tty) 2>/dev/null; then
		printf 'The S3 secret key for %s: ' "$KIT_ACCESS" >/dev/tty
		stty -echo </dev/tty
		read -r SECRET_KEY </dev/tty
		stty echo </dev/tty
		printf '\n' >/dev/tty
	fi
	[ -n "$SECRET_KEY" ] || { echo "install.sh: the restore needs the S3 secret key (TOFT_RESTORE_SECRET_KEY)" >&2; exit 2; }
	# A restore that stopped part-way continues; secrets for another panel stop it.
	if [ -e "$SECRETS" ] && [ "$(sed -n 's/^ENCRYPTION_KEY=//p' "$SECRETS")" != "$KIT_KEY" ]; then
		echo "install.sh: $SECRETS holds another panel's key; restore onto a fresh server" >&2
		exit 1
	fi
	mkdir -p /var/lib/toft && : >/var/lib/toft/restoring
fi

# A panel deploy.sh runs (D-66): its compose project and checkout, read from
# its running backend. deploy.sh wipes its env file after each deploy, so the
# settings are read from the containers too, further down.
if [ -n "$ADOPT" ]; then
	[ -z "$RESTORE" ] || { echo "install.sh: --adopt and --restore don't go together" >&2; exit 2; }
	[ ! -e "$STATE" ] || { echo "install.sh: this panel already runs on the installer: upgrade it with toft upgrade" >&2; exit 1; }
	command -v docker >/dev/null 2>&1 && [ "$(docker inspect -f '{{.State.Running}}' paas-backend 2>/dev/null)" = true ] ||
		{ echo "install.sh: --adopt needs the panel running here: no running paas-backend" >&2; exit 1; }
	ADOPT_PROJECT=$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project"}}' paas-backend)
	ADOPT_DIR=$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project.working_dir"}}' paas-backend)
	[ -n "$ADOPT_PROJECT" ] && [ -f "$ADOPT_DIR/docker-compose.yml" ] ||
		{ echo "install.sh: paas-backend wasn't started by docker compose from a checkout here" >&2; exit 1; }
	for c in paas-frontend paas-postgres paas-traefik; do
		[ "$(docker inspect -f '{{.State.Running}}' "$c" 2>/dev/null)" = true ] || { echo "install.sh: --adopt needs the whole panel running: $c isn't" >&2; exit 1; }
	done
fi

# Root only: Docker's and toft upgrade's output; never a secret or the code.
(umask 077 && : >>"$LOG")
exec 3>>"$LOG"
printf '\n══ install.sh %s, %s\n' "$RELEASE" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >&3
# Whatever stops it, set -e included, the log's path is the last line.
finish() {
	rc=$?
	clear
	if [ "$rc" != 0 ] && [ -z "$died" ]; then
		printf '%s✗ Stopped (exit %s). The last lines of the log:%s\n' "$red" "$rc" "$off" >&2
		tail -n 5 "$LOG" | sed 's/^/  /' >&2
	fi
	printf '\n%sFull log: %s%s\n' "$dim" "$LOG" "$off"
}
trap finish EXIT

# ── Checking this server (D-29) ──────────────────────────────────────────────
arch=$(uname -m)
case "$arch" in x86_64 | aarch64) ;; *) die "$arch is not supported: Toft runs on 64-bit Linux, amd64 or arm64" ;; esac
[ -r /etc/os-release ] || die "no /etc/os-release: this is not a Linux distribution Toft knows"
# shellcheck disable=SC1091
. /etc/os-release
[ -d /run/systemd/system ] || die "systemd is not running: Toft needs it for the host firewall and backups"
[ "$(stat -fc %T /sys/fs/cgroup 2>/dev/null)" = cgroup2fs ] || die "cgroup v2 is not mounted: Toft needs the unified hierarchy"
kernel=$(uname -r | cut -d- -f1)
kmaj=${kernel%%.*} krest=${kernel#*.} kmin=${krest%%.*}
[ "$kmaj" -gt 5 ] || { [ "$kmaj" -eq 5 ] && [ "$kmin" -ge 10 ]; } || die "kernel $kernel is too old: Toft needs 5.10 or newer"
# The release's signature is checked with ed25519 in openssl pkeyutl -rawin,
# OpenSSL 3.0 or newer (signed_digest).
openssl pkeyutl -help 2>&1 | grep -q -- -rawin ||
	die "OpenSSL 3.0 or newer is needed to check the release's signature; found: $(openssl version 2>/dev/null || echo none)" "Install OpenSSL 3 (the openssl package), then run this again."
case "$ID:${VERSION_ID:-0}" in
ubuntu:2[2-9].* | ubuntu:[3-9]*.* | debian:1[2-9]* | rocky:9* | rocky:1[0-9]* | almalinux:9* | almalinux:1[0-9]*) note="" ;;
*) note="(not tested here: best effort)" ;;
esac
detail "${PRETTY_NAME:-$ID} · $arch · systemd $(systemctl --version | sed -n '1s/[^0-9]*\([0-9]*\).*/\1/p') · cgroup v2 · kernel $kernel"

cpus=$(nproc)
mem_kib=$(sed -n 's/^MemTotal: *\([0-9]*\) kB/\1/p' /proc/meminfo)
mem=$(awk "BEGIN { printf \"%.1f\", $mem_kib / 1048576 }")
mkdir -p /var/lib
free_kib=$(df -Pk /var/lib | awk 'NR == 2 { print $4 }')
free=$(awk "BEGIN { printf \"%d\", $free_kib * 1024 / 1e9 }")
# The floor is 10 GiB, which is 10.7 GB: the refusal says the next whole GB.
[ "$free_kib" -ge 10485760 ] || die "only $free GB free under /var/lib: Toft needs 11 GB for its images, database and backups"
small=""
# The floor is backend/pkg/reservation's (3.5 GiB, what a 4 GB server passes).
[ "$cpus" -ge 2 ] && [ "$mem_kib" -ge 3670016 ] || small="(below 2 vCPU and 4 GB: this server can run the panel only)"
[ -z "$small" ] || note="${note:+$note }$small"
os=${NAME:-$ID}
os=${os% GNU/Linux}
os=${os% Linux}
ok "$os ${VERSION_ID:-} · kernel $kmaj.$kmin · $cpus vCPU · $mem GiB · $free GB free" "$note"

# What holds a port: the container publishing it (with or without a
# docker-proxy listening), else "nginx (pid 812)".
holder() {
	c=$(docker ps --filter "publish=$1" --format '{{.Names}}' 2>/dev/null | head -n1)
	if [ -n "$c" ]; then
		printf 'the container %s' "$c"
	else
		ss -Hltnp "sport = :$1" 2>/dev/null | sed -n 's/.*users:(("\([^"]*\)",pid=\([0-9]*\).*/\1 (pid \2)/p' | head -n1
	fi
}
# Ports: free, or held by Toft's own Traefik on a re-run.
ours=""
if command -v docker >/dev/null 2>&1 && [ "$(docker inspect -f '{{.State.Running}}' paas-traefik 2>/dev/null)" = true ]; then ours=1; fi
if [ -z "$ours" ]; then
	for port in 80 443; do
		h=$(holder "$port")
		[ -n "$h" ] || [ -n "$(ss -Hltn "sport = :$port" 2>/dev/null)" ] || continue
		die "port $port is in use${h:+ by $h}: Traefik needs 80 and 443" "Stop it or move it to another port, then run this again."
	done
fi
# git over SSH (D-28, D-71), unless this install leaves git out.
git_port=$(sed -n 's/^TOFT_GIT_SSH_PORT=//p' "$SECRETS" 2>/dev/null | tail -n1)
git_port=${git_port:-2222}
# An install without git stays so on a re-run.
! grep -qi '^TOFT_GIT=off' "$SECRETS" 2>/dev/null || NOGIT=1
if [ -z "$ours" ] && [ -z "$NOGIT" ]; then
	h=$(holder "$git_port")
	if [ -n "$h" ] || [ -n "$(ss -Hltn "sport = :$git_port" 2>/dev/null)" ]; then
		die "port $git_port is in use${h:+ by $h}: git over SSH needs it" "Put TOFT_GIT_SSH_PORT=<a free port> in $SECRETS, or run with --no-git to install without git."
	fi
fi
ip=$(curl -fsS4 --max-time 5 https://ifconfig.me 2>/dev/null || true)
case "$ip" in *[!0-9.]* | "") ip=$(ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p') ;; esac
ports=free
[ -z "$ours" ] || ports="held by Toft"
if [ -z "$NOGIT" ]; then ok "Ports 80, 443 and $git_port $ports"; else ok "Ports 80 and 443 $ports"; fi
detail "public IP ${ip:-unknown}"

# ── Docker 29 or newer, from Docker's own repository ─────────────────────────
changing=1
installed_docker=""
if ! command -v docker >/dev/null 2>&1; then
	doing "Installing Docker from Docker's repository"
	case "$ID" in
	ubuntu | debian)
		# needrestart (Ubuntu) would print and restart services after each install.
		export DEBIAN_FRONTEND=noninteractive NEEDRESTART_SUSPEND=1
		apt-get update -q >&3 2>&3
		apt-get install -yq ca-certificates curl gnupg nftables >&3 2>&3
		install -m 0755 -d /etc/apt/keyrings
		curl -fsSL "https://download.docker.com/linux/$ID/gpg" 2>&3 | gpg --dearmor --yes -o /etc/apt/keyrings/docker.gpg 2>&3
		chmod a+r /etc/apt/keyrings/docker.gpg
		echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/$ID ${VERSION_CODENAME:?} stable" >/etc/apt/sources.list.d/docker.list
		apt-get update -q >&3 2>&3
		apt-get install -yq docker-ce docker-ce-cli containerd.io docker-compose-plugin docker-buildx-plugin >&3 2>&3
		;;
	rocky | almalinux | rhel | centos)
		dnf -y install dnf-plugins-core nftables >&3 2>&3
		dnf config-manager --add-repo https://download.docker.com/linux/centos/docker-ce.repo >&3 2>&3
		dnf -y install docker-ce docker-ce-cli containerd.io docker-compose-plugin docker-buildx-plugin >&3 2>&3
		;;
	*) die "Docker is not installed, and this installer can't install it on $ID" "Install Docker 29 or newer (https://docs.docker.com/engine/install/), then run this again." ;;
	esac
	installed_docker=1
fi

# The app address pool (D-31): written only where Docker has no daemon.json
# yet, and only by this installer; an existing one is never edited.
if [ -n "$installed_docker" ] && [ ! -e /etc/docker/daemon.json ]; then
	if [ -z "$POOL" ]; then
		used=$(ip -4 route | awk '{ print $1 }')
		for n in 203 204 205 206 207 208 209 210; do
			echo "$used" | grep -q "^10\.$n\." || { POOL="10.$n.0.0/16"; break; }
		done
		[ -n "$POOL" ] || die "no free /16 between 10.203 and 10.210 on this host" "Choose one with --pool CIDR."
	fi
	mkdir -p /etc/docker
	printf '{"default-address-pools":[{"base":"%s","size":24}]}\n' "$POOL" >/etc/docker/daemon.json
fi
# apt starts Docker as soon as it is installed, before the pool above was
# written: restart it so the pool applies. A fresh Docker runs nothing yet.
if [ -n "$installed_docker" ]; then
	systemctl enable docker >/dev/null 2>&1
	systemctl restart docker
fi
docker_v=$(docker version --format '{{.Server.Version}}' 2>/dev/null) || die "Docker is installed but not running" "Start it (systemctl start docker), then run this again."
[ "${docker_v%%.*}" -ge 29 ] || die "Docker $docker_v found: Toft needs Docker 29 or newer" \
	"Upgrading Docker restarts every container on this server. Stop or move them first, upgrade Docker (https://docs.docker.com/engine/install/), then run this again."
from=""
[ -z "$installed_docker" ] || from=", from Docker's repository"
ok "Docker $docker_v$from"
docker compose version >/dev/null 2>&1 || die "Docker's compose plugin is missing" "Install docker-compose-plugin from Docker's repository, then run this again."
# nft loads the host firewall (D-37); a Docker installed before Toft may have come without it.
if ! command -v nft >/dev/null 2>&1; then
	case "$ID" in
	ubuntu | debian) DEBIAN_FRONTEND=noninteractive NEEDRESTART_SUSPEND=1 apt-get install -yq nftables >&3 2>&3 ;;
	*) dnf -y install nftables >&3 2>&3 ;;
	esac
fi

[ -n "$POOL" ] || POOL=$(sed -n 's/.*"base" *: *"\([0-9./]*\)".*/\1/p' /etc/docker/daemon.json 2>/dev/null | head -n1)

# ── Setting up this server for Toft (toft upgrade then installs the release) ─
# From the pool, like every app network: Docker picks a /24 nothing uses
# (its own docker0 bridge takes the first).
docker network inspect paas-network >/dev/null 2>&1 || docker network create paas-network >/dev/null
subnet=$(docker network inspect paas-network -f '{{range .IPAM.Config}}{{.Subnet}} {{end}}' | cut -d' ' -f1)
detail "Networks: paas-network $subnet${POOL:+, apps from $POOL}"

rand() { od -An -N32 -tx1 /dev/urandom | tr -d ' \n'; }
# KEY VALUE as an env-file line: single quotes keep a $ literal (a bcrypt
# hash) for compose, which reads an unquoted one as a variable.
envline() {
	case "$2" in
	*\'*) die "$1 holds a single quote, which the settings file can't keep" "Change it in the running panel's settings, then run this again." ;;
	*[\$\#\"\\\ ]* | *"	"*) printf "%s='%s'\n" "$1" "$2" ;;
	*) printf '%s=%s\n' "$1" "$2" ;;
	esac
}
if [ -n "$ADOPT" ] && [ ! -f "$SECRETS" ]; then
	# What compose gave the backend from deploy.sh's env file: its
	# environment, less what its image sets itself.
	install -d -m 0700 /etc/toft
	umask 077
	img_env=$(docker image inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$(docker inspect -f '{{.Image}}' paas-backend)")
	quoted=$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' paas-backend | grep "'" | cut -d= -f1 | head -n1)
	[ -z "$quoted" ] || die "$quoted holds a single quote, which the settings file can't keep" "Change it in the running panel's settings, then run this again."
	{
		printf '# Toft%ss settings and secrets, read by install.sh --adopt from the panel\n' "'"
		printf '# deploy.sh ran from %s, on %s. Root only (0600).\n' "$ADOPT_DIR" "$(date -u +%Y-%m-%d)"
		docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' paas-backend | while IFS= read -r line; do
			[ -n "$line" ] || continue
			printf '%s\n' "$img_env" | grep -qxF -- "$line" && continue
			key=${line%%=*}
			case "$key" in COMPOSE_PROJECT_NAME | GHCR_USER | IMAGE_TAG | TOFT_TRAEFIK_DIR | TOFT_UPGRADE_DIR | TOFT_APPS_SLICE) continue ;; esac
			envline "$key" "${line#*=}"
		done
		# The installer's own: the project and its volumes stay; the images
		# running now become the release "adopted", the rollback target.
		printf 'COMPOSE_PROJECT_NAME=%s\nGHCR_USER=%s\nIMAGE_TAG=adopted\nTOFT_TRAEFIK_DIR=/var/lib/toft/traefik\n' "$ADOPT_PROJECT" "$OWNER"
	} >"$SECRETS.tmp"
	umask 022
	grep -q '^ENCRYPTION_KEY=' "$SECRETS.tmp" && grep -q '^POSTGRES_PASSWORD=' "$SECRETS.tmp" ||
		{ rm -f "$SECRETS.tmp"; die "the running panel's settings lack ENCRYPTION_KEY or POSTGRES_PASSWORD"; }
	mv "$SECRETS.tmp" "$SECRETS"
	ok "Settings read from the running panel into $SECRETS" "(mode 0600)"
	adopted_now=1

	# The running release, as toft upgrade's rollback target: its images,
	# tagged, and its compose file, as a release of its own.
	for c in backend frontend; do
		docker tag "$(docker inspect -f '{{.Image}}' "paas-$c")" "ghcr.io/$OWNER/paas-$c:adopted"
	done
	adopted=/var/lib/toft/releases/adopted/bundle
	install -d "$adopted/traefik/dynamic" /var/lib/toft/traefik/dynamic
	cp "$ADOPT_DIR/docker-compose.yml" "$adopted/"
	# The panel's Traefik files move out of the checkout (D-65): Traefik's
	# middlewares and the routes the backend wrote.
	for f in dynamic.yml panel.yml; do
		[ ! -f "$ADOPT_DIR/traefik/dynamic/$f" ] || cp "$ADOPT_DIR/traefik/dynamic/$f" /var/lib/toft/traefik/dynamic/
	done
	[ ! -f "$ADOPT_DIR/traefik/dynamic/dynamic.yml" ] || cp "$ADOPT_DIR/traefik/dynamic/dynamic.yml" "$adopted/traefik/dynamic/"
	ln -sfn "$SECRETS" "$adopted/.env"
	[ ! -e /opt/toft ] || [ -L /opt/toft ] || die "/opt/toft exists and isn't the installer's link" "Move it away, then run this again."
	ln -sfn "$adopted" /opt/toft
	install -d -m 0700 /var/lib/toft/upgrade
	printf '{"version":"adopted","images":["ghcr.io/%s/paas-backend:adopted","ghcr.io/%s/paas-frontend:adopted"],"updated_at":"%s"}\n' \
		"$OWNER" "$OWNER" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$STATE"
	chmod 600 "$STATE"
	ok "The running panel kept as release \"adopted\"" "(what an unhealthy upgrade goes back to)"
fi
if [ ! -f "$SECRETS" ]; then
	install -d -m 0700 /etc/toft
	umask 077
	pg=$(rand)
	cat >"$SECRETS" <<EOF
# Toft's settings and secrets, written by install.sh. Root only (0600).
# ENCRYPTION_KEY decrypts every secret the panel stores: it is in the
# recovery kit (Settings → Instance); keep that copy off this server.
ENV=production
PORT=8080
LOG_LEVEL=info
COMPOSE_PROJECT_NAME=paas
GHCR_USER=$OWNER
IMAGE_TAG=$RELEASE
POSTGRES_USER=paas_user
POSTGRES_PASSWORD=$pg
POSTGRES_DB=paas_db
DATABASE_URL=postgresql://paas_user:$pg@postgres:5432/paas_db
JWT_SECRET=$(rand)
ENCRYPTION_KEY=${KIT_KEY:-$(rand)}
DOCKER_HOST=unix:///var/run/docker.sock
TRAEFIK_NETWORK=paas-network
TRAEFIK_CONTAINER=paas-traefik
TOFT_APP_NETWORKS=isolated
TOFT_APP_NETWORK_POOL=${POOL:-10.203.0.0/16}
TOFT_DATA_DIR=/var/paas
TOFT_TRAEFIK_DIR=/var/lib/toft/traefik
BASE_DOMAIN=
ACME_EMAIL=
BACKEND_PORT=127.0.0.1:8080
FRONTEND_PORT=127.0.0.1:3000
TRAEFIK_DASHBOARD_USERS=
EOF
	umask 022
	detail "Secrets written to $SECRETS (mode 0600)"
else
	chmod 600 "$SECRETS"
	[ -n "${adopted_now:-}" ] || detail "Secrets kept in $SECRETS"
fi
# Apps on this server run under toft-apps.slice, which toft upgrade sizes
# from the reservation (D-26, D-62).
grep -q '^TOFT_APPS_SLICE=' "$SECRETS" || printf 'TOFT_APPS_SLICE=toft-apps.slice\n' >>"$SECRETS"
install -d -m 0755 /var/paas

# set KEY VALUE in the secrets file, keeping every other line.
set_secret() {
	tmp="$SECRETS.tmp"
	grep -v "^$1=" "$SECRETS" >"$tmp" || true
	printf '%s=%s\n' "$1" "$2" >>"$tmp"
	chmod 600 "$tmp" && mv "$tmp" "$SECRETS"
}
get_secret() { sed -n "s/^$1=//p" "$SECRETS" | tail -n1; }
# Without git hosting (D-71): toft upgrade then leaves its compose file out.
[ -z "$NOGIT" ] || set_secret TOFT_GIT off
# A panel with no domain makes its apps' temporary addresses from this (D-150).
[ -z "$ip" ] || set_secret TOFT_PANEL_IP "$ip"

# A setup code only while nobody has an account (D-61): 8 characters of
# Crockford's base32, valid 30 minutes.
accounts=$(docker exec paas-postgres psql -U "$(get_secret POSTGRES_USER)" -d "$(get_secret POSTGRES_DB)" -tAc "SELECT EXISTS (SELECT 1 FROM users)" 2>/dev/null || true)
code=""
if [ "$accounts" != t ]; then
	code=$(od -An -N8 -tu1 /dev/urandom | awk '{ a = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"; for (i = 1; i <= NF; i++) printf "%s%s", substr(a, $i % 32 + 1, 1), (i == 4 ? "-" : "") }')
	set_secret TOFT_SETUP_TOKEN "$code"
	set_secret TOFT_SETUP_TOKEN_EXPIRES "$(date -u -d '+30 min' +%Y-%m-%dT%H:%M:%SZ)"
	[ -z "$APP" ] || set_secret TOFT_SETUP_APP "$APP"
	# A restore's panel is empty until the backup is loaded: the code keeps
	# its first account closed meanwhile, and nobody is shown it.
	[ -z "$RESTORE" ] || code=""
fi

if [ -n "$TOKEN" ]; then
	printf '%s' "$TOKEN" | docker login ghcr.io -u "$TOKEN_USER" --password-stdin >/dev/null 2>&1 ||
		die "the registry refused the token" "Check --registry-token: it needs to read the packages of $OWNER."
fi

doing "Installing Toft $RELEASE"
# The Loki log driver (D-48), as the provisioner installs it on servers, by
# digest: it has one tag per architecture, and a tag can be moved.
# Without it, a container started with the driver fails; non-fatal here.
docker plugin ls --format '{{.Name}}' 2>/dev/null | grep -q loki ||
	case $(dpkg --print-architecture) in
	amd64) docker plugin install grafana/loki-docker-driver:3.7.9-amd64@sha256:88f98af1110ba24b7b2eddad28ac445037327c712912aec3596b85702ea7b56f --alias loki --grant-all-permissions ;;
	arm64) docker plugin install grafana/loki-docker-driver:3.7.9-arm64@sha256:c78bdbb26c5936e5d5258b8efa7a55e49e126a235461e9dc91ad455bebd9ef26 --alias loki --grant-all-permissions ;;
	esac >/dev/null 2>&1 || true

# signed_digest DIR COMPONENT prints the digest DIR/toft-images, lines of
# "toft-image <release> <component> sha256:<hex>", names for $RELEASE's
# COMPONENT, once the release key's signature of it (DIR/toft-images.sig,
# base64) checks out. Exits 1 when it doesn't, 2 when it names none.
signed_digest() {
	printf '%s\n' "$RELEASE_KEY" >"$1/release.pub"
	base64 -d <"$1/toft-images.sig" >"$1/sig" 2>/dev/null || return 1
	openssl pkeyutl -verify -pubin -inkey "$1/release.pub" -rawin -in "$1/toft-images" -sigfile "$1/sig" 2>&1 |
		grep -q 'Signature Verified Successfully' || return 1
	awk -v v="$RELEASE" -v c="$2" '$1 == "toft-image" && $2 == v && $3 == c && length($4) == 71 && $4 ~ /^sha256:[0-9a-f]+$/ { d = $4 }
		END { if (d == "") exit 1; print d }' "$1/toft-images" || return 2
}
# toft itself, from the release's backend image by the digest the release's
# signed image manifest names (<release>-release, in the backend repository):
# whoever can push to the package can move a tag, not sign. toft upgrade
# checks both images again with the key built into it.
manifest="ghcr.io/$OWNER/paas-backend:$RELEASE-release"
docker pull --quiet --platform=linux/amd64 "$manifest" >&3 2>&3 ||
	die "could not pull $manifest, $RELEASE's signed image manifest" "Releases from before signed images have none and can't be installed. Toft's images are private for now: pass --registry-token with a token that may read them."
rel=$(mktemp -d)
cid=$(docker create --platform=linux/amd64 "$manifest" /none)
docker cp "$cid:/toft-images" "$rel/toft-images" >/dev/null
docker cp "$cid:/toft-images.sig" "$rel/toft-images.sig" >/dev/null
docker rm "$cid" >/dev/null
signed=0
digest=$(signed_digest "$rel" backend) || signed=$?
rm -rf "$rel"
case $signed in
0) ;;
1) die "$manifest is not signed by Toft's release key" "Someone may have replaced it: nothing from it was run." ;;
*) die "$manifest names no backend image for $RELEASE" ;;
esac
image="ghcr.io/$OWNER/paas-backend@$digest"
docker image inspect "$image" >/dev/null 2>&1 || docker pull --quiet "$image" >&3 2>&3 ||
	die "could not pull $image, the backend image signed for $RELEASE" "The registry no longer holds the image the release key signed."
cid=$(docker create "$image")
docker cp "$cid:/usr/local/bin/toft" /usr/local/bin/toft >/dev/null
docker rm "$cid" >/dev/null
chmod 0755 /usr/local/bin/toft

# toft upgrade with its steps in the log. A failure dies with its error
# (toft's "toft: " line to the end); what it finished without (its ✗
# lines on success: the host firewall, toft itself) is repeated by
# unfinished, after the line saying it started.
upgrade() {
	what=$1
	shift
	before=$(wc -l <"$LOG")
	if ! /usr/local/bin/toft upgrade "$@" >&3 2>&3; then
		why=$(tail -n +"$((before + 1))" "$LOG" | sed -n '/^toft: /,$p' | sed '1s/^toft: //')
		die "$what: ${why:-$(tail -n1 "$LOG")}"
	fi
}
unfinished() { tail -n +"$((before + 1))" "$LOG" | sed -n "s/^  ✗ \(.*\)/$red✗ \1$off/p" >&2; }

running=$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' "$STATE" 2>/dev/null || true)
if [ "$running" = "$RELEASE" ]; then
	# Same release: start what is missing, pick up a new setup code.
	# With git hosting's compose file, as toft upgrade runs it (D-71).
	files="-f docker-compose.yml"
	[ ! -f /opt/toft/docker-compose.git.yml ] || [ "$(get_secret TOFT_GIT)" = off ] || files="$files -f docker-compose.git.yml"
	# shellcheck disable=SC2086 # $files is two or four words
	(cd /opt/toft && TOFT_CONTROL_PLANE_SUBNET="$(docker network inspect paas-network -f '{{range .IPAM.Config}}{{.Subnet}},{{end}}' | sed 's/,$//')" \
		docker compose $files --env-file "$SECRETS" up -d --remove-orphans >&3 2>&3)
	ok "Toft $RELEASE already runs here"
else
	upgrade "Toft $RELEASE did not start" --to "$RELEASE"
	git=""
	[ "$(docker inspect -f '{{.State.Running}}' paas-git 2>/dev/null)" != true ] || git=", git"
	ok "Toft $RELEASE started: panel, API, Postgres, Traefik$git"
	unfinished
fi

# ── Restore (D-64): the latest snapshot onto this fresh install ──────────────
if [ -n "$RESTORE" ]; then
	section "Restoring from the backup"
	restic_image=$(sed -n 's/^RESTIC_IMAGE=//p' /usr/local/lib/toft/backup.sh)
	work=/var/lib/toft/restore
	rm -rf "$work" && install -d -m 0700 "$work"
	umask 077
	printf 'RESTIC_REPOSITORY=%s\nRESTIC_PASSWORD=%s\nAWS_ACCESS_KEY_ID=%s\nAWS_SECRET_ACCESS_KEY=%s\n' \
		"$KIT_REPO" "$KIT_PASSWORD" "$KIT_ACCESS" "$SECRET_KEY" >"$work/restic.env"
	umask 022
	# restic retries a refused key for 15 minutes before it says so: the
	# repository's config is read first, with a minute to answer.
	doing "Reading the backup at $KIT_REPO"
	docker image inspect "$restic_image" >/dev/null 2>&1 || docker pull --quiet "$restic_image" >&3 2>&3 || die "could not pull $restic_image"
	timeout 60 docker run --rm --env-file "$work/restic.env" "$restic_image" cat config >"$work/restic.log" 2>&1 || {
		why=$(sed -n 's/.*returned error, retrying after [^:]*: //p' "$work/restic.log" | head -n1)
		die "the backup could not be reached: ${why:-$(tail -c 300 "$work/restic.log" | tr '\n' ' ')}" "Check the kit and the secret key."
	}
	docker run --rm --env-file "$work/restic.env" -v "$work:/restore" "$restic_image" restore latest --target /restore >"$work/restic.log" 2>&1 ||
		die "the backup could not be read: $(tail -c 300 "$work/restic.log" | tr '\n' ' ')"
	ok "Latest snapshot read from $KIT_REPO"

	pg_user=$(get_secret POSTGRES_USER) pg_db=$(get_secret POSTGRES_DB)
	[ -s "$work/data/dumps/paas-postgres.dump" ] || die "the snapshot has no dump of the panel's database"
	docker stop paas-backend paas-frontend >/dev/null
	docker exec paas-postgres psql -q -U "$pg_user" -d postgres -c "DROP DATABASE IF EXISTS $pg_db WITH (FORCE)" -c "DROP DATABASE IF EXISTS atlas_dev" >/dev/null
	# pg_dumpall's output: its roles exist already here, which only warns.
	docker exec -i paas-postgres psql -q -U "$pg_user" -d postgres <"$work/data/dumps/paas-postgres.dump" >"$work/psql.log" 2>&1
	docker exec paas-postgres psql -q -U "$pg_user" -d "$pg_db" -tAc "SELECT 1 FROM users LIMIT 1" >/dev/null 2>&1 ||
		die "the panel's database did not load: $(grep -v 'already exists' "$work/psql.log" | tail -n 3 | tr '\n' ' ')"
	# The dump carries the old server's database password; this one's is new.
	docker exec paas-postgres psql -q -U "$pg_user" -d postgres -c "ALTER ROLE $pg_user PASSWORD '$(get_secret POSTGRES_PASSWORD)'" >/dev/null
	# A restored panel starts a new epoch (D-111): the servers' agents may
	# hold a newer desired state than the backup's.
	docker exec paas-postgres psql -q -U "$pg_user" -d "$pg_db" -c "DELETE FROM instance_settings WHERE key = 'panel_epoch'" >/dev/null
	# The restore's mark (D-163): the panel says this server's backups are
	# paused since, until an operator resumes them, and answers on this
	# server's IP address until its domain comes back to it. A mark the dump
	# carried from an earlier restore is replaced, its domain_reached_at
	# with it: the domain has to arrive here again.
	docker exec paas-postgres psql -q -U "$pg_user" -d "$pg_db" -c "INSERT INTO instance_settings (key, value) VALUES ('restore', jsonb_build_object('at', now())) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()" >/dev/null
	ok "Panel database restored: $(docker exec paas-postgres psql -U "$pg_user" -d "$pg_db" -tAc "SELECT count(*) FROM users") accounts, $(docker exec paas-postgres psql -U "$pg_user" -d "$pg_db" -tAc "SELECT count(*) FROM services") services"

	n=0
	for dir in "$work"/data/volumes/*/; do
		[ -d "$dir" ] || continue
		vol=$(basename "$dir")
		docker volume create "$vol" >/dev/null
		docker run --rm --entrypoint cp -v "$vol:/v" -v "$dir:/src:ro" "$restic_image" -a /src/. /v/
		n=$((n + 1))
	done
	# What the installer manages is this server's own; the rest goes back.
	# The old server's backup settings never do (D-163): /var/paas/backup is
	# where /var/lib/toft/backup points, and the snapshot of a panel adopted
	# from a paas-* compose project holds it.
	rm -rf "$work/data/binds/opt/toft" "$work/data/binds/etc/toft" "$work/data/binds/var/lib/toft" "$work/data/binds/var/paas/backup"
	[ ! -d "$work/data/binds" ] || cp -a "$work/data/binds/." /
	ok "$n volumes and the mounted folders restored"
	# The git repositories come back from their bundles (D-79), while the
	# backend is stopped; a snapshot from before bundles has their files.
	if [ -d "$work/data/volumes/paas-git/@bundles" ] && docker ps --format '{{.Names}}' | grep -qx paas-git; then
		docker exec paas-git toft-git restore >"$work/git.json" 2>"$work/git.err" ||
			die "the git repositories could not be restored: $(tail -c 300 "$work/git.err" | tr '\n' ' ')"
		ok "$(sed -n 's/.*"restored":\([0-9]*\).*/\1/p' "$work/git.json") git repositories restored from their bundles"
	fi
	rm -rf "$work"

	doing "Starting the restored panel"
	upgrade "the restored panel did not start" --to "$RELEASE" --force
	ok "The restored panel started"
	unfinished
	rm -f /var/lib/toft/restoring
	printf '\n  %sEveryone signs in again. Services on this server start at their next deploy.%s\n' "$dim" "$off"
	printf '  %sBackups are paused here. On the server you keep, resume them under Settings → Backups.%s\n' "$dim" "$off"
fi

# ── Done ─────────────────────────────────────────────────────────────────────
if [ -n "$ADOPT" ]; then
	printf '\n  %sThis panel now upgrades from Instance (or toft upgrade). deploy.sh refuses this server from now on;%s\n' "$dim" "$off"
	printf '  %sthe checkout in %s is no longer used.%s\n' "$dim" "$ADOPT_DIR" "$off"
fi
if [ -n "$code" ]; then
	section "Finish setup in a browser within 30 minutes"
	printf '  https://%s/setup\n' "${ip:-<this server>}"
	printf '  one-time code  %s%s%s\n' "$bold" "$code" "$off"
	printf '\n  %sYour browser warns about the certificate until setup gives the panel a domain.%s\n' "$dim" "$off"
	if [ -z "$NOGIT" ]; then open_ports="80, 443 and $git_port"; else open_ports="80 and 443"; fi
	printf '  %sIf the page doesn'"'"'t load, open ports %s in your provider'"'"'s firewall.%s\n' "$dim" "$open_ports" "$off"
else
	domain=$(docker exec paas-postgres psql -U "$(get_secret POSTGRES_USER)" -d "$(get_secret POSTGRES_DB)" -tAc "SELECT value->>'domain' FROM instance_settings WHERE key = 'panel'" 2>/dev/null || true)
	section "Toft is set up"
	if [ -n "$RESTORE" ] && [ -n "$domain" ]; then
		# The domain still points at the old server, or at nothing (D-163).
		printf '  https://%s\n' "${ip:-<this server>}"
		printf '\n  %sYour browser warns about the certificate. Once %s points at this server,%s\n' "$dim" "$domain" "$off"
		printf '  %sthe panel is at https://%s and stops answering here.%s\n' "$dim" "$domain" "$off"
	else
		printf '  https://%s\n' "${domain:-${ip:-<this server>}}"
	fi
fi
printf '\n%sRe-run any time: it changes only what is missing. Upgrade with%s toft upgrade --to vX.Y.Z\n' "$dim" "$off"
}

main "$@"
