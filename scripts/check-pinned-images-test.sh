#!/bin/sh
# Self-test for check-pinned-images.sh: each bad line must fail, each good
# line must pass, and the real scripts must pass.
set -eu

cd "$(dirname "$0")/.."
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
digest=$(printf 'a%.0s' $(seq 64))

expect() { # expect pass|fail LINE
	printf '%s\n' "$2" >"$dir/t.sh"
	if scripts/check-pinned-images.sh "$dir/t.sh" >/dev/null; then got=pass; else got=fail; fi
	[ "$got" = "$1" ] || { echo "FAIL: expected $1 for: $2"; exit 1; }
}

expect fail 'docker plugin install grafana/loki-docker-driver:latest --alias loki'
expect fail 'docker plugin install grafana/loki-docker-driver --alias loki'
expect fail 'docker plugin install grafana/loki-docker-driver:3 --alias loki'
expect fail 'docker plugin install grafana/loki-docker-driver:3.7.9-amd64 --alias loki'
expect fail 'docker pull postgres:16'
expect fail 'docker pull alpine'
expect fail 'docker run --rm -v "$x:/x" arigaio/atlas:main schema apply'
expect fail 'docker run --rm \
  arigaio/atlas@sha256:abc'
expect fail '  image: traefik:v3'
expect pass 'amd64) docker plugin install grafana/loki-docker-driver:3.7.9-amd64@sha256:'"$digest"' --alias loki ;;'
expect pass 'docker pull postgres:16.6-alpine'
expect pass 'docker run --rm \
  arigaio/atlas:1.3.3-community@sha256:'"$digest"' schema apply'
expect pass 'docker run --rm -v "$vol:/v" -v "$dir:/src:ro" "$restic_image" restore'
expect pass 'docker pull --quiet "$image"'
expect pass 'image="ghcr.io/$OWNER/paas-backend:latest"'
expect pass '# docker pull postgres:latest'
expect pass '  image: ghcr.io/x/y:${IMAGE_TAG:-latest}'

scripts/check-pinned-images.sh

# The provisioner installs the plugin on servers: the same refs as install.sh.
refs() { grep -o 'grafana/loki-docker-driver[^ ]*' "$1" | sort; }
[ -n "$(refs install.sh)" ] && [ "$(refs install.sh)" = "$(refs backend/internal/server/provisioner.go)" ] ||
	{ echo "FAIL: install.sh and provisioner.go install different Loki plugins"; exit 1; }
echo "ok"
