#!/bin/sh
# The lock of one of a host's builders (D-170), the start of ensure.sh and
# of the command a build runs under. A build holds it with the other builds for as
# long as it runs; ensure.sh takes it alone to make the builder or replace
# it, so neither happens under a build. Environment, Toft's as ensure.sh's:
#   TOFT_BUILDER_LOCK  the file, in a folder only root writes to
#   TOFT_BUILDER_WAIT  the seconds to wait for it
set -eu

refuse() { code=$1; shift; echo "$*" >&2; exit "$code"; }

# lock -s|-x words: takes the lock on descriptor 9, with others (-s) or
# alone (-x), and exits 24 with words when it isn't free in time. A host
# without flock, a developer's Mac, has no lock.
lock() {
	command -v flock >/dev/null 2>&1 || return 0
	[ -e "$TOFT_BUILDER_LOCK" ] ||
		{ mkdir -p "${TOFT_BUILDER_LOCK%/*}" && : >"$TOFT_BUILDER_LOCK"; } 2>/dev/null || true
	[ -r "$TOFT_BUILDER_LOCK" ] || refuse 24 "Couldn't make the builder's lock $TOFT_BUILDER_LOCK on this server."
	exec 9<"$TOFT_BUILDER_LOCK"
	waited=0
	until flock "$1" -n 9; do
		[ "$waited" -lt "$TOFT_BUILDER_WAIT" ] || refuse 24 "$2"
		waited=$((waited + 1))
		sleep 1
	done
}
