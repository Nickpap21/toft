import { describe, expect, it } from "vitest";
import type { DiskReport } from "@/lib/api/server.service";
import { bar, cleanRow, gb, keptRows, lastAutoNote, policyLine, safeRows, volumeNote } from "./disk-view";

const GB = 1e9;
const values = { build_cache_limit_bytes: 16 * GB, auto_clean: ["untagged", "old_builds"] as const, alert_percent: 85, alert_resolve_percent: 80 };

function report(over: Partial<DiskReport> = {}): DiskReport {
  return {
    read_at: "2026-10-31T14:00:00Z",
    size_bytes: 160 * GB,
    used_bytes: 115 * GB,
    free_bytes: 45 * GB,
    classes: [
      { kind: "in_use", removable: false, automatic: false, count: 24, bytes: 12.9 * GB, examples: [] },
      { kind: "rollback", removable: false, automatic: false, count: 30, bytes: 3.1 * GB, examples: [] },
      { kind: "panel_releases", removable: false, automatic: false, count: 0, bytes: 0, examples: [] },
      { kind: "untagged", removable: true, automatic: true, count: 279, bytes: 71.1 * GB, examples: [] },
      { kind: "old_builds", removable: true, automatic: true, count: 38, bytes: 1.2 * GB, examples: ["api", "worker"] },
      { kind: "old_panel_releases", removable: true, automatic: false, count: 0, bytes: 0, examples: [] },
      { kind: "unused_pulled", removable: true, automatic: false, count: 5, bytes: 4.2 * GB, examples: ["mysql:8.0", "postgres:15", "nginx:1.25", "redis:7", "caddy:2"] },
    ],
    build_cache: { bytes: 14.4 * GB, reclaimable_bytes: 14 * GB, over_limit_bytes: 0, limit_bytes: 16 * GB },
    volumes: [],
    volumes_in_use: { count: 10, bytes: 0.8 * GB },
    containers_bytes: 0.1 * GB,
    other_bytes: 7.2 * GB,
    rollback_kept: [
      { service_id: "a", service_name: "api", kept: 10, images: 10, bytes: GB },
      { service_id: "w", service_name: "worker", kept: 10, images: 10, bytes: GB },
    ],
    other_workspaces: { services: 0, rollback_images: 0, rollback_bytes: 0 },
    policy: { build_cache_limit_bytes: null, auto_clean: null, alert_percent: 90, effective: { ...values, auto_clean: [...values.auto_clean], alert_percent: 90, alert_resolve_percent: 85 }, defaults: { ...values, auto_clean: [...values.auto_clean] } },
    clean: null,
    last_clean: null,
    ...over,
  };
}

describe("disk view", () => {
  it("writes sizes as the board does", () => {
    expect([gb(76.5 * GB), gb(160 * GB), gb(0.4 * GB), gb(205e6), gb(0)]).toEqual(["76.5 GB", "160 GB", "400 MB", "205 MB", "0 GB"]);
  });

  it("lists the removable classes in the clean's order, empty ones left out", () => {
    const rows = safeRows(report());
    expect(rows.map((r) => [r.k, r.count, r.v, r.auto])).toEqual([
      ["Untagged images", "279", "71.1 GB", true],
      ["Old builds", "38", "1.2 GB", true],
      ["Pulled images no service uses", "5", "4.2 GB", false],
    ]);
    expect(rows[1].note).toContain("of api and worker");
    expect(rows[2].note).toBe("mysql:8.0, postgres:15, nginx:1.25, redis:7 and caddy:2. A deploy that needs one pulls it again.");
    const more = report();
    more.classes[6] = { ...more.classes[6], count: 12 };
    expect(safeRows(more)[2].note).toMatch(/^mysql:8.0, .* and 7 more\./);
  });

  it("splits the disk into safe, kept and the rest", () => {
    const b = bar(report());
    expect(gb(b.safe)).toBe("76.5 GB");
    expect(gb(b.kept)).toBe("31.3 GB");
    expect(gb(b.rest)).toBe("7.2 GB");
    expect(b.safePct).toBe("47.8%");
  });

  it("says when the build cache is over its limit", () => {
    const r = report({ build_cache: { bytes: 20 * GB, reclaimable_bytes: 20 * GB, over_limit_bytes: 4 * GB, limit_bytes: 16 * GB } });
    expect(keptRows(r, false).find((k) => k.k === "Build cache")?.note).toBe("4 GB over the 16 GB limit. Toft trims that after the next build or clean.");
    expect(keptRows(report(), false).find((k) => k.k === "Kept for rollback")?.note).toContain("last 10 deploys of api and worker");
    expect(keptRows(report(), false).find((k) => k.k === "Kept for rollback")?.note).not.toContain("other workspaces");
    const shared = report({ other_workspaces: { services: 2, rollback_images: 4, rollback_bytes: 1.2 * GB } });
    expect(keptRows(shared, false).find((k) => k.k === "Kept for rollback")?.note).toContain("2 services from other workspaces keep 1.2 GB.");
  });

  it("sums the policy up in one line", () => {
    expect(policyLine(report().policy)).toBe("Policy: build cache 16 GB · untagged images and old builds cleaned daily · alert at 90%");
    const off = report().policy;
    off.effective.auto_clean = [];
    expect(policyLine(off)).toBe("Policy: build cache 16 GB · nothing cleaned automatically · alert at 90%");
  });

  it("names the last automatic clean in the card's head", () => {
    const c = { id: "c", kinds: ["untagged" as const], build_cache_all: false, automatic: true, outcome: "succeeded" as const, step: "done" as const, freed_bytes: 0.3 * GB, removed: 4, skipped: 0, started_at: "2026-10-31T04:12:00Z", started_by: "", finished_at: "2026-10-31T04:12:30Z", error: "" };
    expect(lastAutoNote(report({ last_clean: c }))).toMatch(/^cleaned automatically 2026-10-31 \d\d:12, freed 300 MB$/);
    expect(lastAutoNote(report({ last_clean: { ...c, freed_bytes: 0 } }))).toMatch(/, nothing to free$/);
    expect(lastAutoNote(report({ last_clean: { ...c, automatic: false } }))).toBe("");
    // A failed one is the status row's.
    expect(lastAutoNote(report({ last_clean: { ...c, outcome: "failed" } }))).toBe("");
  });

  it("explains each kind of volume", () => {
    expect(volumeNote({ name: "x_pg", bytes: 1, created_at: null, kind: "compose_orphan", service_name: "paas-old-cccccccc" })).toContain("paas-old-cccccccc");
    expect(volumeNote({ name: "a".repeat(64), bytes: 1, created_at: "2026-04-03T10:00:00Z", kind: "anonymous", service_name: "" })).toMatch(/^Anonymous, created 2026-04-0\d\./);
  });

  it("follows a clean and says how it ended", () => {
    const base = { id: "c", kinds: ["untagged" as const], build_cache_all: false, automatic: false, step: "untagged" as const, freed_bytes: 41.2 * GB, removed: 180, skipped: 0, started_at: "2026-10-31T14:02:00Z", started_by: "Nikos", finished_at: null, error: "" };
    const run = cleanRow({ ...base, outcome: "running" }, Date.parse("2026-10-31T14:02:20Z"), "fsn1");
    expect([run.label, run.state, run.meta]).toEqual(["Freeing space", "41.2 GB so far", "untagged images · 180 images removed"]);
    const done = cleanRow({ ...base, outcome: "succeeded", skipped: 2, finished_at: "2026-10-31T14:02:48Z" }, 0, "fsn1");
    expect([done.label, done.state]).toEqual(["Freed 41.2 GB", "in 48 s"]);
    expect(done.detail).toContain("2 images were left");
    const failed = cleanRow({ ...base, outcome: "failed", error: "the server stopped answering", finished_at: "2026-10-31T14:02:22Z" }, 0, "fsn1");
    expect([failed.label, failed.state]).toEqual(["Stopped after 41.2 GB", "the server stopped answering"]);
  });
});
