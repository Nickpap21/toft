import { describe, expect, it } from "vitest";
import type { Deployment, LogEntry } from "@/lib/api/deployment.service";
import type { ProjectOverview } from "@/lib/api/environment.service";
import { reachedIndex, stageDefs } from "@/components/ui/deploy-stage-tracker";
import {
  deployAgain,
  deployLine,
  entryLevel,
  filterDeployments,
  groupLog,
  howDeploys,
  rowState,
  triggerText,
  lineText,
  noMatchText,
  previousKept,
  startedBy,
  steps,
  whileDeploying,
} from "./deployment-view";

const dep = (id: string, status: Deployment["status"], extra: Partial<Deployment> = {}): Deployment =>
  ({ id, service_id: "s", status, image_tag: "", commit_sha: "", error_msg: "", created_at: "2026-10-14T10:00:00Z", ...extra }) as Deployment;

let n = 0;
const line = (message: string, at = "2026-10-14T10:00:00Z"): LogEntry => ({ id: ++n, deployment_id: "d", level: "info", message, created_at: at });

// The image pipeline as it logs (a real local deploy of traefik/whoami).
const imageLog = [
  line("Build started", "2026-10-14T10:00:00Z"),
  line("→ image: traefik/whoami:v1.10", "2026-10-14T10:00:00Z"),
  line("[1/2] Pulling image traefik/whoami:v1.10...", "2026-10-14T10:00:00Z"),
  line("Status: Image is up to date for traefik/whoami:v1.10", "2026-10-14T10:00:01Z"),
  line("Starting container from image traefik/whoami:v1.10...", "2026-10-14T10:00:01Z"),
  line("→ health check: /health (timeout: 3s, retries: 3)", "2026-10-14T10:00:02Z"),
  line("✗ Health check failed: health check failed after 3 retries", "2026-10-14T10:00:06Z"),
  line("↩ The previous container keeps serving (paas-x-blue); the service's status is unchanged", "2026-10-14T10:00:06Z"),
];

describe("the pipeline's steps", () => {
  it("adds Health check before Live when the service has one", () => {
    expect(stageDefs("docker-image", false, true).map((s) => s.label)).toEqual(["Pull", "Deploy", "Health check", "Live"]);
    expect(stageDefs("git", false).map((s) => s.label)).toEqual(["Clone", "Build", "Deploy", "Live"]);
    expect(stageDefs("docker-compose", false, true).map((s) => s.label)).toEqual(["Deploy", "Live"]);
  });

  it("groups the log by the pipeline's markers, and ignores steps not shown", () => {
    const withCheck = groupLog(imageLog, stageDefs("docker-image", false, true));
    expect(withCheck.map((g) => g.lines.length)).toEqual([4, 1, 3, 0]);
    const without = groupLog(imageLog, stageDefs("docker-image", false));
    expect(without.map((g) => g.lines.length)).toEqual([4, 4, 0]);
    expect(reachedIndex(imageLog.map((l) => l.message), stageDefs("docker-image", false, true))).toBe(2);
  });

  it("marks where a failed deploy stopped, and times each step from its lines", () => {
    const d = dep("d", "failed", { started_at: "2026-10-14T10:00:00Z", finished_at: "2026-10-14T10:00:06Z" });
    const s = steps(d, groupLog(imageLog, stageDefs("docker-image", false, true)), 0);
    expect(s.map((x) => x.state)).toEqual(["done", "done", "failed", "todo"]);
    expect(s.map((x) => x.time)).toEqual(["1s", "1s", "4s", "not switched"]);
    expect(previousKept(imageLog)).toBe(true);
  });

  it("shows a running deploy's step as running, and later ones waiting", () => {
    const d = dep("d", "building", { started_at: "2026-10-14T10:00:00Z" });
    const s = steps(d, groupLog(imageLog.slice(0, 4), stageDefs("docker-image", false, true)), Date.parse("2026-10-14T10:00:30Z"));
    expect(s.map((x) => `${x.state} ${x.time}`)).toEqual(["running 30s", "todo waiting", "todo waiting", "todo waiting"]);
  });
});

describe("log lines", () => {
  it("reads the pipeline's marks and words as levels, since every line is stored as info", () => {
    expect(entryLevel(line("✗ Pull failed: unauthorized"))).toBe("error");
    expect(entryLevel(line("⚠ Could not remove the new container"))).toBe("warn");
    expect(entryLevel(line("⇄ ⚠ The control plane is shutting down"))).toBe("warn");
    expect(entryLevel(line("pull access denied for ghcr.io/x"))).toBe("warn");
    expect(entryLevel(line("→ env vars: 3"))).toBe("info");
  });

  it("keeps only the last value of carriage-return progress", () => {
    expect(lineText("remote: Counting objects: 0% (1/1153)\rremote: Counting objects: 100% (1153/1153), done.\r")).toBe(
      "remote: Counting objects: 100% (1153/1153), done.",
    );
  });
});

describe("the list", () => {
  const list = [
    dep("b", "building", { triggered_by: "Push · sam" }),
    dep("live", "running", { triggered_by: "Manual · Alex" }),
    dep("f", "failed", { triggered_by: "Push · sam", started_at: "2026-10-14T10:00:00Z" }),
    dep("x", "failed", { triggered_by: "Push · alex" }),
    dep("r", "running", { triggered_by: "API · Alex" }),
    dep("old", "running"),
  ];

  it("filters by the board's chips: a refused deploy is not a failed one (board ReworkDeploys)", () => {
    const all = filterDeployments(list, "all", "live");
    expect(all.counts).toEqual({ all: 6, live: 1, failed: 1, refused: 1 });
    expect(all.rows).toHaveLength(6);
    expect(filterDeployments(list, "failed", "live").rows.map((d) => d.id)).toEqual(["f"]);
    expect(filterDeployments(list, "refused", "live").rows.map((d) => d.id)).toEqual(["x"]);
    expect(noMatchText("refused")).toBe("No refused deploys.");
  });

  it("names a deploy refused before it started Refused, amber", () => {
    // The API leaves started_at out of one that never started.
    expect(rowState(dep("x", "failed"))).toEqual({ label: "Refused", tone: "pending" });
    expect(rowState(dep("f", "failed", { started_at: "2026-10-14T10:00:00Z" }))).toEqual({ label: "Failed", tone: "failed" });
  });

  it("reads a deploy the same on the Deploys tab and the project Overview", () => {
    // The Overview's row sends started_at as null where a Deployment leaves it out.
    const row = (id: string, status: Deployment["status"], started_at: string | null): ProjectOverview["recent_deployments"][number] => ({
      id, service_id: "s", service_name: "web", environment_name: "production", status, commit_sha: "", commit_message: "",
      triggered_by: "", image_tag: "", created_at: "2026-10-14T10:00:00Z", started_at, finished_at: null,
    });
    const at = "2026-10-14T10:00:00Z";
    expect(rowState(row("x", "failed", null))).toEqual(rowState(dep("x", "failed")));
    expect(rowState(row("x", "failed", null)).label).toBe("Refused");
    expect(rowState(row("f", "failed", at))).toEqual(rowState(dep("f", "failed", { started_at: at })));
    expect(rowState(row("r", "running", at), "r")).toEqual(rowState(dep("r", "running", { started_at: at }), "r"));
  });

  it("says who started each deploy and how it went, in the row's second line", () => {
    const at = "2026-10-14T10:00:00Z", end = "2026-10-14T10:01:51Z", now = Date.parse("2026-10-14T10:02:00Z");
    expect(deployLine(dep("a", "running", { triggered_by: "Merge · alex", started_at: at, finished_at: end }), now)).toBe("merge by alex · 1m 51s");
    expect(deployLine(dep("a", "failed", { triggered_by: "Push · sam", started_at: at, finished_at: end }), now)).toBe("push by sam · failed after 1m 51s");
    expect(deployLine(dep("a", "failed", { triggered_by: "Push · alex" }), now)).toBe("push by alex · refused by the deploy policy");
    expect(deployLine(dep("a", "building", { triggered_by: "Redeploy · admin", started_at: at }), now)).toBe("redeploy by admin · 2m 0s so far");
    expect(deployLine(dep("a", "pending", { triggered_by: "Manual · Alex" }), now)).toBe("deploy by Alex · queued");
    // Toft's own redeploy names Toft, not its reason (the 2026-11-04 sweep).
    expect(deployLine(dep("a", "running", { triggered_by: "Toft · certificate issued: switch to HTTPS", started_at: at, finished_at: end }), now)).toBe("redeploy by Toft · 1m 51s");
    expect(deployLine(dep("a", "running", { started_at: at, finished_at: end }), now)).toBe("1m 51s");
  });

  it("names the trigger in Details: a merge by its pull request", () => {
    expect(triggerText(dep("a", "building", { triggered_by: "Merge · alex", pull_request: { number: 45, repository_id: "r" } }))).toBe("merge of !45");
    expect(triggerText(dep("a", "failed", { triggered_by: "Push · sam" }))).toBe("push by sam");
    expect(triggerText(dep("a", "running"))).toBe("—");
  });

  it("says how the service deploys, as the board's aside", () => {
    const svc = { name: "api", source_type: "git", git_branch: "main", deploy_on_push: true, effective_deploy_strategy: "rolling",
      health_check_path: "/healthz", health_check_retries: 5, rollback_images_kept: 30 } as const;
    expect(howDeploys(svc, { name: "production", protected: true })).toEqual([
      ["On push", "A push to `main` deploys"],
      ["Switch", "Rolling: the new container passes `GET /healthz` before the old one stops"],
      ["Roll back", "To any of the last 30 images, with today’s variables"],
      ["Protected", "production asks before every deploy"],
    ]);
    expect(howDeploys({ ...svc, deploy_on_push: false, effective_deploy_strategy: "recreate", rollback_images_kept: 1 }, { name: "staging", protected: false })).toEqual([
      ["On push", "A push to `main` doesn’t deploy; Deploy does"],
      ["Switch", "Recreate: the old container stops, then the new one starts"],
      ["Roll back", "To the last image, with today’s variables"],
      ["Protected", "No: staging deploys at once"],
    ]);
    expect(howDeploys({ ...svc, source_type: "docker-image", health_check_retries: 0 }, undefined).map(([k, v]) => `${k}: ${v}`)).toEqual([
      "Switch: Rolling: the new container starts before the old one stops",
      "Roll back: To any of the last 30 images, with today’s variables",
    ]);
  });
});

describe("what the page says", () => {
  it("names who started a deployment", () => {
    expect(startedBy({ triggered_by: "Push · sam" })).toBe("pushed by sam");
    expect(startedBy({ triggered_by: "Rollback · Alex" })).toBe("rolled back by Alex");
    expect(startedBy({ triggered_by: "Redeploy · Alex" })).toBe("redeployed by Alex");
    expect(startedBy({ triggered_by: "Toft · certificate issued: switch to HTTPS" })).toBe("redeployed by Toft");
    // An agent acting for its person (D-100).
    expect(startedBy({ triggered_by: "Agent · Claude Code for Alex" })).toBe("deployed by Claude Code for Alex");
    expect(startedBy({ triggered_by: "Rollback · Agent · Claude Code for Alex" })).toBe("rolled back by Claude Code for Alex");
    expect(startedBy({})).toBe("");
  });

  it("offers Deploy again only where the deploy runs the same version", () => {
    const sha = "a".repeat(40);
    expect(deployAgain(dep("d", "failed", { compose_commit_sha: sha }), { source_type: "docker-compose" })).toEqual({ ref: "aaaaaaa", opts: { commitSha: sha } });
    expect(deployAgain(dep("d", "failed", { image_tag: "nginx:1.27" }), { source_type: "docker-image", docker_image: "nginx:1.27" })?.ref).toBe("nginx:1.27");
    expect(deployAgain(dep("d", "failed", { image_tag: "nginx:1.26" }), { source_type: "docker-image", docker_image: "nginx:1.27" })).toBeNull();
    // a git build takes its branch's tip, whatever commit was recorded
    expect(deployAgain(dep("d", "failed", { commit_sha: sha, image_tag: "paas-s:d" }), { source_type: "git" })).toBeNull();
  });

  it("says what the live version does during a deploy, by strategy", () => {
    expect(whileDeploying({ effective_deploy_strategy: "rolling", health_check_retries: 3 }, "9b7d3f0", "a81c0de")).toBe(
      "9b7d3f0 keeps serving until a81c0de passes its health check.",
    );
    expect(whileDeploying({ effective_deploy_strategy: "recreate" }, "It", "41c0d2e")).toBe(
      "It stops just before 41c0d2e starts, and starts again if 41c0d2e fails.",
    );
  });
});
