#!/usr/bin/env bash
# Fails when govulncheck finds a vulnerability the backend's code reaches
# that is not listed in backend/.govulnignore. govulncheck itself has no
# ignore list, and two advisories (docker/docker) have no fixed release, so
# without this the gate would be red for good and nobody would read it.
#
# Usage: scripts/govulncheck.sh            (from the repository root)
# The ignore file holds one advisory id per line (GO-YYYY-NNNN), with a
# comment saying why and when it can go.
set -euo pipefail

cd "$(dirname "$0")/../backend"
ignore_file=.govulnignore

go run golang.org/x/vuln/cmd/govulncheck@latest -json ./... > /tmp/govulncheck.$$.json
trap 'rm -f /tmp/govulncheck.$$.json' EXIT

python3 - "$ignore_file" /tmp/govulncheck.$$.json <<'EOF'
import json, re, sys

ignore_file, report = sys.argv[1], sys.argv[2]
ignored = set()
try:
    for line in open(ignore_file):
        line = line.split("#", 1)[0].strip()
        if line:
            ignored.add(line)
except FileNotFoundError:
    pass

titles, reached = {}, set()

def objects(text):
    dec = json.JSONDecoder()
    i, n = 0, len(text)
    while i < n:
        while i < n and text[i].isspace():
            i += 1
        if i >= n:
            return
        obj, j = dec.raw_decode(text, i)
        yield obj
        i = j

for obj in objects(open(report).read()):
    if "osv" in obj:
        titles[obj["osv"]["id"]] = obj["osv"].get("summary", "")
    f = obj.get("finding")
    # A finding with a function in its trace is one the code calls; the
    # others are only imported or required.
    if f and f.get("trace") and f["trace"][0].get("function"):
        reached.add(f["osv"])

failing = sorted(reached - ignored)
stale = sorted(ignored - reached)
for osv in sorted(reached):
    mark = "ignored " if osv in ignored else "FAIL    "
    print(f"{mark}{osv}  {titles.get(osv, '')}")
for osv in stale:
    print(f"stale   {osv}  (in {ignore_file}, no longer reached: remove it)")
if failing:
    print(f"\n{len(failing)} reachable vulnerabilit{'y' if len(failing) == 1 else 'ies'} not in {ignore_file}")
    sys.exit(1)
print(f"\nok: {len(reached)} reachable, all listed in {ignore_file}" if reached else "\nok: no reachable vulnerabilities")
EOF
