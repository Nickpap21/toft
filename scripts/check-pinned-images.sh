#!/bin/sh
# Fails when install.sh fetches or runs a Docker image or plugin
# that is not pinned, so a host never gets whatever a floating tag points at
# on the day it was set up.
#
# Usage: scripts/check-pinned-images.sh [file...]   (default: install.sh)
#
# Scanned (backslash continuations joined, comment lines skipped):
#   docker plugin install REF, docker pull REF, docker create REF  (first non-option word)
#   docker run ...  (every word that looks like owner/name[:tag]; a bare
#                    name such as "alpine" is not seen: write owner/name)
#   image: REF      (compose)
# A REF is pinned when it is
#   - name@sha256:<64 hex> (any tag before the digest is fine), or
#   - name:TAG with TAG an exact version: 3.7.9, v1.2, 16.6-alpine, 3.7.9-amd64
#     (at least major.minor, as postgres:16.6 is exact; latest, main, stable,
#     a bare major such as 3, and no tag at all fail). Not for a plugin: it
#     runs on the host with every permission it asks for, so only a digest.
# Allowed expansions:
#   - a REF that starts with $, or a tag that is a variable (":$RELEASE",
#     ":${IMAGE_TAG:-latest}"): Toft's own images, whose tag is the release
#     the script was given or built, not a third party's moving tag.
set -eu

[ $# -gt 0 ] || { cd "$(dirname "$0")/.." && set -- install.sh; }

fail=0
for f in "$@"; do
	awk -v file="$f" -v q="'" '
	function bad(ln, r, why) {
		printf "%s:%d: %s: %s\n", file, ln, r, why
		failed = 1
	}
	function check(r, ln, digest,    i, rest, n, parts, tag) {
		if (r ~ /^\$/) return
		i = index(r, "@sha256:")
		if (i > 0) {
			rest = substr(r, i + 8)
			if (length(rest) != 64 || rest !~ /^[0-9a-f]+$/) bad(ln, r, "malformed sha256 digest")
			return
		}
		if (digest) { bad(ln, r, "a plugin needs a sha256 digest"); return }
		n = split(r, parts, "/")      # the tag is after the last "/", so registry:port is safe
		tag = parts[n]
		if (index(tag, ":") == 0) { bad(ln, r, "no tag"); return }
		sub(/^[^:]*:/, "", tag)
		if (tag ~ /\$/) return
		if (tag !~ /^v?[0-9]+\.[0-9]+(\.[0-9]+)?([-.+][A-Za-z0-9.]+)*$/) bad(ln, r, "floating tag \"" tag "\"")
	}
	function words(s, ln, mode,    w, n, i, x) {
		n = split(s, w, /[ \t]+/)
		for (i = 1; i <= n; i++) {
			x = w[i]
			gsub(/"/, "", x); gsub(q, "", x)
			if (x == "") continue
			if (mode != "all") {
				if (x ~ /^-/) continue
				check(x, ln, mode == "plugin")
				return
			}
			if (x ~ /^[a-z0-9][a-z0-9._-]*\/[A-Za-z0-9._\/:@${}-]*$/) check(x, ln)
		}
	}
	{
		if (start == 0) start = NR
		if (sub(/\\$/, "")) { buf = buf $0 " "; next }
		line = buf $0; buf = ""; ln = start; start = 0
		if (line ~ /^[ \t]*#/) next
		if (match(line, /docker plugin install[ \t]/)) words(substr(line, RSTART + RLENGTH), ln, "plugin")
		else if (match(line, /docker (pull|create)[ \t]/)) words(substr(line, RSTART + RLENGTH), ln, "first")
		else if (match(line, /docker run[ \t]/)) words(substr(line, RSTART + RLENGTH), ln, "all")
		else if (match(line, /^[ \t-]*image:[ \t]+/)) words(substr(line, RSTART + RLENGTH), ln, "first")
	}
	END { exit failed }
	' "$f" || fail=1
done
exit $fail
