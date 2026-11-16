#!/usr/bin/env bash
# Fails when docker/dockerfile:1 has moved on from the Dockerfile frontend
# every build is pinned to (dockerfileFrontend in
# backend/internal/docker/buildkit.go, D-167). The pin is what keeps a
# repository from choosing the frontend; this is what keeps the pin from
# going stale, fixes included. To move it: put the digest this prints in
# the constant, and run the backend's tests (the isolation tests build
# with it).
#
# Usage: scripts/dockerfile-frontend.sh      (from anywhere; needs docker
# with buildx)
set -euo pipefail

cd "$(dirname "$0")/../backend"
pinned=$(grep -o 'docker/dockerfile@sha256:[0-9a-f]\{64\}' internal/docker/buildkit.go)
current=docker/dockerfile@$(docker buildx imagetools inspect docker/dockerfile:1 --format '{{.Manifest.Digest}}')
if [ "$pinned" != "$current" ]; then
	echo "docker/dockerfile:1 is now $current"
	echo "builds are pinned to     $pinned (backend/internal/docker/buildkit.go)"
	exit 1
fi
echo "Dockerfile frontend: the pin is docker/dockerfile:1."
