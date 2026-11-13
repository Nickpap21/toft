import { describe, expect, it } from "vitest";
import type { GitCommit } from "@/lib/api/code.service";
import { pendingChanges } from "../[id]/services/[serviceId]/_components/variables-view";
import { alsoGoesLive, checksText, commitRows, commitsLabel, concurrentLine, strategyLine } from "./deploy-confirm-view";

const now = new Date("2026-11-06T12:00:00Z").getTime();
const at = (h: number) => new Date(now - h * 3_600_000).toISOString();
const commit = (sha: string, subject: string, author: string): GitCommit => ({
  sha: sha.padEnd(40, "0"),
  parents: [],
  author_name: author,
  author_email: "",
  authored_at: at(1),
  committed_at: at(1),
  subject,
});

describe("commitRows", () => {
  it("lists newest first, and repeats a failed deploy's error for its commit", () => {
    const commits = [commit("3a90c11", "Bump pgx to v5.7", "sam"), commit("c58e0a1", "Merge !45: Paginate GET /orders", "alex")];
    const rows = commitRows(
      commits,
      [{ id: "d1", commit_sha: "3a90c11".padEnd(40, "0"), status: "failed", error_msg: "Health check failed: GET /healthz answered 503 on all 5 attempts.", created_at: at(26) }],
      now,
    );
    expect(rows.map((r) => r.sha)).toEqual(["c58e0a1", "3a90c11"]);
    expect(rows[0].failed).toBeUndefined();
    expect(rows[1]).toMatchObject({
      author: "sam",
      failed: { deploymentId: "d1", text: "Its own deploy failed here 1d ago. Health check failed: GET /healthz answered 503 on all 5 attempts." },
    });
  });

  it("counts the commits a deploy takes live and a roll back takes away", () => {
    expect(commitsLabel(3, false, "deploy")).toBe("3 commits go live");
    expect(commitsLabel(1, false, "deploy")).toBe("1 commit goes live");
    expect(commitsLabel(2, false, "rollback")).toBe("2 commits leave");
    expect(commitsLabel(1, false, "rollback")).toBe("1 commit leaves");
    expect(commitsLabel(250, true, "deploy")).toBe("250+ commits go live");
  });
});

describe("alsoGoesLive", () => {
  const since = at(3);
  const pending = pendingChanges({
    own: [{ key: "LOG_LEVEL", value: "debug", created_at: at(1), updated_at: at(1), updated_by: "admin" }],
    ownSecrets: [{ key: "JWT_SECRET", created_at: at(48), updated_at: at(2), updated_by: "admin" }],
    env: [{ key: "LOG_LEVEL", value: "info", created_at: at(48), updated_at: at(48) }],
    envSecrets: [],
    since,
    envName: "production",
  });
  const domains = [
    { hostname: "new.example", routed_at: null, created_at: at(1), created_by: "alex" },
    { hostname: "old.example", routed_at: at(40), created_at: at(48), created_by: "" },
  ];

  it("lists the variables via step 7's rule, a replaced secret, and a new unrouted domain", () => {
    expect(alsoGoesLive(pending, domains, since)).toEqual([
      { key: "LOG_LEVEL", text: "info → debug", by: "admin", at: at(1) },
      { key: "JWT_SECRET", text: "replaced", by: "admin", at: at(2) },
      { key: "new.example", text: "routed from this deploy", by: "alex", at: at(1) },
    ]);
  });

  it("lists the variables saved since the live deploy for a redeploy too", () => {
    // A redeploy runs the live image with today's variables (founders 2026-11-06).
    const rows = alsoGoesLive(pending, [], since);
    expect(rows.map((r) => r.key)).toEqual(["LOG_LEVEL", "JWT_SECRET"]);
  });

  it("with nothing live, lists every domain not yet routed", () => {
    expect(alsoGoesLive([], domains, null).map((r) => r.key)).toEqual(["new.example"]);
  });
});

describe("concurrentLine", () => {
  const dep = (service_id: string, service_name: string, environment_name: string, status: "building" | "running") => ({
    id: service_id,
    service_id,
    service_name,
    environment_name,
    status,
    commit_sha: "",
    commit_message: "",
    triggered_by: "Manual · alex",
    image_tag: "",
    created_at: new Date(now - 120_000).toISOString(),
    started_at: null,
    finished_at: null,
  });

  it("names another service deploying in the same environment", () => {
    const recent = [dep("web", "web", "production", "building"), dep("api", "api", "production", "building"), dep("w2", "worker", "staging", "building"), dep("db", "db", "production", "running")];
    expect(concurrentLine(recent, "api", "production", now)).toBe("web is deploying in production too (alex, 2m ago). The two don’t wait for each other.");
    expect(concurrentLine(recent, "web", "staging", now)).toBe("worker is deploying in staging too (alex, 2m ago). The two don’t wait for each other.");
    expect(concurrentLine(recent, "web", "preview", now)).toBe("");
  });
});

describe("strategyLine", () => {
  const rolling = { effective_deploy_strategy: "rolling" as const, health_check_retries: 5, health_check_path: "/healthz" };
  it("says what users see", () => {
    expect(strategyLine(rolling, "9b7d3f0", "c58e0a1", "deploy")).toBe(
      "Rolling: 9b7d3f0 keeps serving until c58e0a1 passes GET /healthz. If it doesn’t, nothing changes for users; afterwards you can roll back to 9b7d3f0 in one step.",
    );
    expect(strategyLine({ ...rolling, effective_deploy_strategy: "recreate" }, "9b7d3f0", "c58e0a1", "deploy")).toMatch(/^Stop then start: .*downtime\.$/);
    expect(strategyLine(rolling, "9b7d3f0", "9b7d3f0", "redeploy")).toBe(
      "Rolling: The running 9b7d3f0 keeps serving until its new container passes GET /healthz. If it doesn’t, nothing changes for users.",
    );
    expect(strategyLine(rolling, "", "c58e0a1", "deploy")).toBe("Nothing is live yet: if c58e0a1 fails its health check, nothing changes for users.");
  });

  it("names a tag once when the deploy pulls the one that is live", () => {
    expect(strategyLine(rolling, "nginx:1.27-alpine", "nginx:1.27-alpine", "deploy")).toBe(
      "Rolling: The running nginx:1.27-alpine keeps serving until its new container passes GET /healthz. If it doesn’t, nothing changes for users.",
    );
  });

  it("leaves the roll back to a line that says it already (the agent approval's)", () => {
    expect(strategyLine(rolling, "9b7d3f0", "c58e0a1", "deploy", false)).toBe(
      "Rolling: 9b7d3f0 keeps serving until c58e0a1 passes GET /healthz. If it doesn’t, nothing changes for users.",
    );
  });
});

it("checksText", () => {
  expect(checksText(null)).toBeNull();
  expect(checksText({ state: "success", total: 2, passed: 2, failed: 0, pending: 0 })?.text).toBe("✓ checks passed");
  expect(checksText({ state: "failure", total: 2, passed: 1, failed: 1, pending: 0 })?.text).toBe("✗ 1 check failed");
});
