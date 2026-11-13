#!/usr/bin/env bash
# Fails when an image the catalog deploys (backend/cmd/catalog-images: each
# image template's, and each compose template's services') can no longer be
# pulled anonymously, as a server pulls it. It asks the registry for the
# manifest only (the registry API with an anonymous token, so no docker and
# no one's credentials), no layers; each image gets three tries, for a
# registry's bad minute. An image without linux/amd64 and linux/arm64 fails
# too: servers are either, and the catalog offers what runs on both.
#
# Usage: scripts/check-catalog-images.sh      (from anywhere; needs go, curl, jq)
set -euo pipefail

cd "$(dirname "$0")/../backend"
images=$(go run ./cmd/catalog-images)
[ -n "$images" ] || { echo "no catalog images listed"; exit 1; }

accept='application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json'

# get URL prints its body, with an anonymous token when the registry asks
# for one (its WWW-Authenticate challenge names where to get it).
get() {
	local url=$1 challenge realm service token
	challenge=$(curl -sI --max-time 30 -H "Accept: $accept" "$url" | tr -d '\r' | sed -n 's/^[Ww][Ww][Ww]-[Aa]uthenticate: Bearer //p')
	if [ -z "$challenge" ]; then
		curl -fsSL --max-time 30 -H "Accept: $accept" "$url"
		return
	fi
	realm=$(sed -n 's/.*realm="\([^"]*\)".*/\1/p' <<<"$challenge")
	service=$(sed -n 's/.*service="\([^"]*\)".*/\1/p' <<<"$challenge")
	token=$(curl -fsSL --max-time 30 -G "$realm" --data-urlencode "service=$service" --data-urlencode "scope=repository:$repo:pull" | jq -r '.token // .access_token')
	curl -fsSL --max-time 30 -H "Accept: $accept" -H "Authorization: Bearer $token" "$url"
}

fail=0
for img in $images; do
	# registry/repo:tag@digest, as docker reads it: a digest wins over the
	# tag, and a first part with no dot, colon or localhost is Docker Hub's.
	ref=${img%@*} digest=""
	[[ $img == *@* ]] && digest=${img#*@}
	name=$ref tag=latest
	if [[ ${ref##*/} == *:* ]]; then tag=${ref##*:} name=${ref%:*}; fi
	first=${name%%/*}
	if [[ $name == */* && ($first == *.* || $first == *:* || $first == localhost) ]]; then
		registry=$first repo=${name#*/}
	else
		registry=registry-1.docker.io repo=$name
		[[ $repo == */* ]] || repo=library/$repo
	fi

	manifest=""
	for try in 1 2 3; do
		if manifest=$(get "https://$registry/v2/$repo/manifests/${digest:-$tag}" 2>&1); then break; fi
		echo "$img: try $try: ${manifest:-no answer}" >&2
		manifest=""
		[ "$try" = 3 ] || sleep $((try * 10))
	done
	if [ -z "$manifest" ]; then
		echo "::error::$img cannot be pulled"
		fail=1
		continue
	fi
	# An index names its platforms; a single manifest's is in its config.
	platforms=$(jq -r '.manifests[]?.platform | "\(.os)/\(.architecture)"' <<<"$manifest")
	if [ -z "$platforms" ]; then
		platforms=$(get "https://$registry/v2/$repo/blobs/$(jq -r .config.digest <<<"$manifest")" | jq -r '"\(.os)/\(.architecture)"') || true
	fi
	ok=1
	for p in linux/amd64 linux/arm64; do
		grep -qx "$p" <<<"$platforms" || { echo "::error::$img has no $p image"; ok=0 fail=1; }
	done
	[ "$ok" = 0 ] || echo "$img: ok"
done
exit $fail
