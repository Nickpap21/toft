#!/usr/bin/env bash
# Fails when moby/buildkit:latest has moved on from the BuildKit every
# build runs on (Image in backend/internal/builder/builder.go, D-170). The
# pin is what keeps a server from running whatever the tag points at on the
# day its builder was made; this is what keeps the pin from going stale,
# fixes included. To move it: put the version and the digest this prints in
# the constant, and run the backend's tests (the isolation tests build on
# it).
#
# Usage: scripts/buildkit-image.sh      (from anywhere; needs docker with
# buildx)
set -euo pipefail

cd "$(dirname "$0")/../backend"
pinned=$(grep -o 'moby/buildkit:v[0-9.]*@sha256:[0-9a-f]\{64\}' internal/builder/builder.go)
current=$(docker buildx imagetools inspect moby/buildkit:latest --format '{{.Manifest.Digest}}')
if [ "${pinned#*@}" != "$current" ]; then
	echo "moby/buildkit:latest is now $current"
	echo "builds run on             $pinned (backend/internal/builder/builder.go)"
	exit 1
fi
echo "BuildKit: the pin is moby/buildkit:latest."
