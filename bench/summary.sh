#!/bin/sh
# The results table from bench/results/*.json (D-131), or from the folder
# given: each metric's median over every round, with its range in brackets.
set -eu
cd "${1:-$(dirname "$0")/results}"
jq -rs '
	def median: sort | if length % 2 == 1 then .[length / 2 | floor] else (.[length / 2 - 1] + .[length / 2]) / 2 end;
	def r: . * 10 | round / 10;
	def cell: if length == 0 then "–" else "\(median | r) (\(min | r)–\(max | r))" end;
	[
		["install_s", "Install (s)"],
		["idle_mem_mib", "Idle memory (MiB)"],
		["idle_cpu_pct", "Idle CPU (% of 2 vCPU)"],
		["disk_mib", "Disk (MiB)"],
		["first_image_s", "First deploy, image (s)"],
		["first_git_s", "First deploy, build (s)"],
		["redeploy_image_s", "Redeploy, image (s)"],
		["redeploy_git_s", "Redeploy, build (s)"],
		["redeploy_image_down_s", "Seconds with failed requests per redeploy, image"],
		["redeploy_git_down_s", "Seconds with failed requests per redeploy, build"],
		["load_ms", "Dashboard load (ms)"],
		["lcp_ms", "Dashboard largest paint (ms)"],
		["page_kib", "Dashboard transfer (KiB)"],
		["requests", "Dashboard requests"]
	] as $metrics
	| group_by(.platform) as $platforms
	| "| | " + ($platforms | map("\(.[0].platform) \(map(.version[0]? // empty)[0] // "")") | join(" | ")) + " |",
	  "|---" + ($platforms | map("|---") | add) + "|",
	  ($metrics[] as [$key, $label] | "| \($label) | " + ($platforms | map([.[][$key][]?] | cell) | join(" | ")) + " |"),
	  "",
	  "Rounds: " + ($platforms | map("\(.[0].platform) \(map(.round) | unique | length)") | join(", "))
' ./*.json
