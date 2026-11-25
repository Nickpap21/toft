#!/bin/sh
# Makes sure this host's Docker has Toft's builder as Toft wants it, and
# writes the builder's definition into the calling client's own config
# ($DOCKER_CONFIG), so that "docker buildx build --builder $TOFT_BUILDER"
# and a compose build with BUILDX_BUILDER=$TOFT_BUILDER run on it (D-170).
#
# The builder is a BuildKit container on a network of its own whose bridge
# is of the host firewall's toft-u class (server/firewall.nft): what a
# build fetches, and what its RUN steps open, leave from that bridge and
# not from the host.
#
# Run before every build, by the panel, over SSH and by the agent: it
# changes only what differs. Environment, all of it Toft's (never a
# tenant's):
#   TOFT_BUILDER         the builder's name, also its network's
#   TOFT_BUILDER_BRIDGE  the network's Linux bridge
#   TOFT_BUILDER_IMAGE   moby/buildkit, by digest
#   TOFT_BUILDER_MEMORY  its memory limit in bytes; unset: half of the
#                        host's memory
#   TOFT_BUILDER_CGROUP  its cgroup parent; unset: Docker's default
#   TOFT_BUILDER_CACHE   the most its cache may use (BuildKit's
#                        maxUsedSpace: bytes, "20GB" or "10%")
#
# Prints lines for the build's log. Exits 20 to 24, the reason on stderr,
# when the build must not run. lock.sh comes first: refuse and lock are its.

B=$TOFT_BUILDER
BRIDGE=$TOFT_BUILDER_BRIDGE
IMAGE=$TOFT_BUILDER_IMAGE
CGROUP=${TOFT_BUILDER_CGROUP:-}
CACHE=$TOFT_BUILDER_CACHE

docker buildx version >/dev/null 2>&1 ||
	refuse 20 "Docker's buildx plugin is missing on this server: install docker-buildx-plugin, then deploy again."

# The network: Toft's, a bridge of that name, IPv4 only. One that differs
# is refused, never joined and never changed.
netfmt='{{.Driver}}|{{index .Options "com.docker.network.bridge.name"}}|{{index .Labels "paas.managed"}}|{{index .Labels "paas.network.kind"}}|{{.EnableIPv6}}'
netwant="bridge|$BRIDGE|true|build|false"
netgot() { docker network inspect -f "$netfmt" "$B" 2>/dev/null; }
if got=$(netgot); then
	[ "$got" = "$netwant" ] ||
		refuse 21 "The network $B on this server isn't the one Toft makes for builds. Remove or rename it, then deploy again."
elif ! out=$(docker network create --driver bridge --ipv6=false \
	-o com.docker.network.bridge.name="$BRIDGE" \
	--label paas.managed=true --label paas.network.kind=build "$B" 2>&1); then
	# Another build may have made it first.
	[ "$(netgot || true)" = "$netwant" ] || refuse 22 "Couldn't create the build network $B: $out"
fi

mem=${TOFT_BUILDER_MEMORY:-}
if [ -z "$mem" ]; then
	kib=$(sed -n 's/^MemTotal: *\([0-9]*\) kB/\1/p' /proc/meminfo)
	mem=$((kib * 512))
fi
if [ "$mem" -lt 1073741824 ]; then
	echo "Builds on this server are limited to $((mem >> 20)) MiB of memory, half of what it has for apps. A build that needs more is stopped."
fi

# The container buildx runs the builder in: 0 when it is on the network and
# was made for these settings, 1 when it differs, 2 when there is none.
spec="$IMAGE $B $mem $CGROUP $CACHE"
ctr="buildx_buildkit_${B}0"
fits() {
	got=$(docker container inspect -f '{{.HostConfig.NetworkMode}}|{{range .Config.Env}}{{println .}}{{end}}' "$ctr" 2>/dev/null) || return 2
	case "$got" in
	"$B|"*"TOFT_BUILDER_SPEC=$spec
"*) return 0 ;;
	esac
	return 1
}

# A builder that fits is used as it is, beside the builds on it. Making one,
# and replacing one that differs, is one check's at a time and waits for the
# builds that run to end: it has the lock alone from here to its end. Two
# first builds make one builder, and none is removed under a build. A
# container that differs is removed: buildx starts it again from the
# definition below. Its cache is a named volume and stays.
if ! fits; then
	lock -x "Builds are running on this server's builder, and its settings have to change before the next one. Deploy again when they have finished."
	fits || [ $? = 2 ] || docker rm -f "$ctr" >/dev/null
fi

toml=$(mktemp)
trap 'rm -f "$toml"' EXIT
printf '[worker.oci]\n  gc = true\n  maxUsedSpace = "%s"\n' "$CACHE" >"$toml"

# The definition is the client's. Whatever this config held under the name
# goes first: the builder's container and its cache are not touched.
docker buildx rm --keep-state --keep-daemon "$B" >/dev/null 2>&1 || true
set -- --name "$B" --driver docker-container --buildkitd-config "$toml" \
	--driver-opt "image=$IMAGE" --driver-opt "network=$B" \
	--driver-opt "memory=$mem" --driver-opt "memory-swap=$mem" \
	--driver-opt "env.TOFT_BUILDER_SPEC=$spec"
[ -z "$CGROUP" ] || set -- "$@" --driver-opt "cgroup-parent=$CGROUP"
docker buildx create "$@" >/dev/null

# When the whole server runs out of memory, the kernel stops a build before
# anything else on it: buildkitd gets the highest OOM score, and a build's
# steps inherit it. The driver has no option for it and a restart of the
# container loses it, so the builder is started here and the score written
# before every build.
if ! out=$(docker buildx inspect --bootstrap "$B" 2>&1 >/dev/null) ||
	! out=$(docker exec "$ctr" sh -c 'p=$(pidof buildkitd) && for i in $p; do echo 1000 >/proc/$i/oom_score_adj || exit 1; done' 2>&1); then
	refuse 23 "Couldn't start the builder $B: $out"
fi
