#!/usr/bin/env bash
# Fails when moby/moby has a published security advisory that the list
# shipped with Toft (backend/internal/advisory/known.go) lacks (D-133).
# Panels fetch new ones daily, but one installed offline sees only that
# list, so a release must not ship without the latest advisory in it.
#
# Usage: scripts/docker-advisories.sh      (from anywhere; GITHUB_TOKEN, if
# set, lifts GitHub's rate limit for unauthenticated calls)
set -euo pipefail

cd "$(dirname "$0")/../backend"
auth=()
if [ -n "${GITHUB_TOKEN:-}" ]; then auth=(-H "Authorization: Bearer $GITHUB_TOKEN"); fi
published=$(curl -fsSL ${auth[@]+"${auth[@]}"} -H 'Accept: application/vnd.github+json' \
	'https://api.github.com/repos/moby/moby/security-advisories?per_page=100' |
	python3 -c 'import json, sys; [print(a["ghsa_id"]) for a in json.load(sys.stdin) if not a.get("withdrawn_at")]')
missing=$(comm -23 <(sort <<<"$published") <(grep -o 'GHSA-[a-z0-9]\{4\}-[a-z0-9]\{4\}-[a-z0-9]\{4\}' internal/advisory/known.go | sort -u))
if [ -n "$missing" ]; then
	echo "Docker advisories missing from backend/internal/advisory/known.go:"
	for id in $missing; do echo "  https://github.com/moby/moby/security/advisories/$id"; done
	exit 1
fi
echo "Docker advisories: all $(wc -l <<<"$published" | tr -d ' ') in the shipped list."
