#!/bin/sh
# Fails when a script Toft tells people to pipe into sh (curl ... | sudo sh)
# would run part of itself if its download were cut off. Such a script is
# one function, main, called only on its last line (main "$@"): every
# shorter download only defines functions, or is a syntax error, so sh runs
# nothing.
#
# Usage: scripts/check-truncation-safe.sh [file...]
#   (default: install.sh backend/internal/server/agent-install.sh)
#
# For each file it checks the last line is main "$@", then feeds every
# shorter prefix (cut at each line) to sh -x with an empty PATH: xtrace
# prints a "+" line for any command that runs, which fails the check.
set -eu

[ $# -gt 0 ] || { cd "$(dirname "$0")/.." && set -- install.sh backend/internal/server/agent-install.sh; }

sh=$(command -v sh)
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
fail=0
for f in "$@"; do
	[ "$(tail -n 1 "$f")" = 'main "$@"' ] || { echo "$f: the last line must be: main \"\$@\""; fail=1; continue; }
	n=$(wc -l <"$f")
	k=1
	while [ "$k" -lt "$n" ]; do
		head -n "$k" "$f" >"$dir/prefix"
		if (cd "$dir" && env -i PATH=/nonexistent "$sh" -x ./prefix 2>&1 </dev/null) | grep -q '^+'; then
			echo "$f: cut after line $k, sh runs part of it (put it inside main)"
			fail=1
			break
		fi
		k=$((k + 1))
	done
done
exit "$fail"
