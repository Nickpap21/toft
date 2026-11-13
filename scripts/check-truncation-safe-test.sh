#!/bin/sh
# Self-test for check-truncation-safe.sh: each bad script must fail, the
# good one must pass, and the real scripts must pass.
set -eu

cd "$(dirname "$0")/.."
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT

expect() { # expect pass|fail SCRIPT
	printf '%s\n' "$2" >"$dir/t.sh"
	if scripts/check-truncation-safe.sh "$dir/t.sh" >/dev/null; then got=pass; else got=fail; fi
	[ "$got" = "$1" ] || { echo "FAIL: expected $1 for: $2"; exit 1; }
}

expect pass '#!/bin/sh
# comment
say() { echo "$1"; }
main() {
set -eu
say hi
}

main "$@"'
expect fail '#!/bin/sh
set -eu
main() {
echo hi
}
main "$@"'
expect fail '#!/bin/sh
main() {
echo hi
}
echo bye
main "$@"'
expect fail '#!/bin/sh
main() {
echo hi
}
main'
expect fail '#!/bin/sh
echo hi'

scripts/check-truncation-safe.sh
echo "ok"
