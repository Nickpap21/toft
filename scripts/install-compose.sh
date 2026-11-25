#!/usr/bin/env bash
# Installs a Docker Compose release as the compose plugin of this user's
# docker, in ~/.docker/cli-plugins: the docker CLI looks there first, and
# the backend's tests link it into each Docker config they build with. CI
# runs it because a runner's own Compose is whatever its image has, and
# builder.FirstUp builds a stack only with one that keeps a service's
# variables out of the build's environment (not 2.38.0 to 2.39.2).
#
# Usage: scripts/install-compose.sh <version> <sha256>   that release, whose
#                                                        binary has that checksum
#        scripts/install-compose.sh latest               the newest release, by
#                                                        the checksum it publishes
# Linux on x86_64 only (a GitHub runner); needs curl and docker.
set -euo pipefail

version=${1:?usage: install-compose.sh <version> <sha256> | latest}
releases=https://github.com/docker/compose/releases
asset=docker-compose-linux-x86_64
if [ "$version" = latest ]; then
	# The page of the newest release ends in its tag.
	tag=$(curl -fsSL -o /dev/null -w '%{url_effective}' "$releases/latest")
	tag=${tag##*/}
	version=${tag#v}
	sum=$(curl -fsSL "$releases/download/$tag/$asset.sha256" | cut -d' ' -f1)
else
	tag=v$version
	sum=${2:?a pinned release needs its sha256}
fi

dir=$HOME/.docker/cli-plugins
mkdir -p "$dir"
tmp=$(mktemp "$dir/docker-compose.XXXXXX")
trap 'rm -f "$tmp"' EXIT
curl -fsSL -o "$tmp" "$releases/download/$tag/$asset"
echo "$sum  $tmp" | sha256sum -c - >/dev/null
chmod +x "$tmp"
mv "$tmp" "$dir/docker-compose"

got=$(docker compose version --short)
if [ "$got" != "$version" ]; then
	echo "docker compose is $got after installing $version"
	exit 1
fi
echo "Docker Compose $got"
