import { describe, expect, it } from "vitest";
import type { Deployment } from "@/lib/api/deployment.service";
import { deployTarget, deploymentRef, deploymentSeconds, exposedAt, deploymentState, kindText, latencyView, limitsText, logLevel, memoryText, rollbackTarget, sourceText, uptimeText } from "./service-view";

const dep = (id: string, status: Deployment["status"], extra: Partial<Deployment> = {}): Deployment =>
  ({ id, service_id: "s", status, image_tag: `paas-s:${id}`, commit_sha: "", error_msg: "", created_at: "", ...extra }) as Deployment;

describe("deployments", () => {
  it("calls only the active one Live, and earlier live ones Replaced", () => {
    expect(deploymentState(dep("a", "running"), "a").label).toBe("Live");
    expect(deploymentState(dep("b", "running"), "a")).toEqual({ label: "Replaced", tone: "neutral" });
    expect(deploymentState(dep("c", "failed"), "a").label).toBe("Failed");
    expect(deploymentState(dep("d", "pending"), "a").label).toBe("Queued");
    expect(deploymentState(dep("a", "running"), "a", false).label).toBe("Last live");
  });

  it("rolls back to the latest earlier deployment that went live", () => {
    const list = [dep("new", "building"), dep("live", "running"), dep("bad", "failed"), dep("old", "running"), dep("older", "running")];
    expect(rollbackTarget(list, "live")?.id).toBe("old");
    expect(rollbackTarget([dep("live", "running")], "live")).toBeNull();
    // the same image again is a redeploy, not a rollback
    const same = [dep("live", "running", { image_tag: "nginx:1.27" }), dep("prev", "running", { image_tag: "nginx:1.27" }), dep("old", "running", { image_tag: "nginx:1.26" })];
    expect(rollbackTarget(same, "live")?.id).toBe("old");
  });

  it("names a deployment by commit, else image tag, and times it", () => {
    expect(deploymentRef({ commit_sha: "9b7d3f0aa11", image_tag: "x" })).toBe("9b7d3f0");
    expect(deploymentRef({ commit_sha: "", image_tag: "registry.example/team/nginx:1.27-alpine" })).toBe("nginx:1.27-alpine");
    // A pinned image says its tag; the digest is the hover's.
    expect(deploymentRef({ commit_sha: "", image_tag: "mysql:8.0@sha256:0123456789abcdef0123456789abcdef" })).toBe("mysql:8.0");
    expect(deploymentSeconds({ started_at: "2026-10-13T10:00:00Z", finished_at: "2026-10-13T10:01:51Z" })).toBe(111);
    expect(deploymentSeconds({ started_at: "2026-10-13T10:00:00Z" })).toBeNull();
  });
});

describe("the header and logs", () => {
  it("reads source and kind like the board", () => {
    expect(sourceText({ source_type: "git", git_repo_url: "https://github.com/founders/storefront.git", git_branch: "main", base_directory: "services/api" }))
      .toBe("founders/storefront · main · services/api");
    expect(sourceText({ source_type: "docker-image", docker_image: "nginx:1.27-alpine" })).toBe("nginx:1.27-alpine");
    // The project's own repository (D-77), by name, or said when not loaded yet.
    expect(sourceText({ source_type: "git", git_repo_url: "", repository_id: "r1", git_branch: "main", base_directory: "services/api" }, "founders/storefront"))
      .toBe("founders/storefront · main · services/api");
    expect(sourceText({ source_type: "docker-compose", repository_id: "r1", git_branch: "main", compose_file_path: "compose.yaml" }))
      .toBe("the project’s repository · main · compose.yaml");
    expect(kindText({ source_type: "git", build_pack: "dockerfile" })).toBe("Git · Dockerfile");
    expect(kindText({ source_type: "docker-image", addon_slug: "postgres" })).toBe("Postgres");
    expect(kindText({ source_type: "docker-compose", addon_slug: "uptime-kuma" })).toBe("Uptime Kuma");
    expect(kindText({ source_type: "docker-compose", addon_slug: "n8n" })).toBe("n8n");
  });

  it("guesses a log line's level from its words", () => {
    expect(logLevel("pool: 18/20 connections, WARN consider DATABASE_POOL_MAX")).toBe("warn");
    expect(logLevel("level=error msg=\"dial tcp: refused\"")).toBe("error");
    expect(logLevel("GET /healthz 200 1ms")).toBe("");
    expect(logLevel("errors=0 retries=2")).toBe("");
  });
});

describe("limits", () => {
  it("writes memory in binary units, as the boards do", () => {
    expect(memoryText("512m")).toBe("512 MiB");
    expect(memoryText("1g")).toBe("1 GiB");
    expect(memoryText("1536m")).toBe("1.5 GiB");
    expect(memoryText("")).toBe("");
    expect(limitsText("1", "512m")).toBe("1 vCPU · 512 MiB");
    expect(limitsText("", "2g")).toBe("2 GiB");
    expect(limitsText("", "")).toBe("");
  });
});

describe("uptimeText", () => {
  it("writes the board's uptime: minutes, then hours and minutes, then days and hours", () => {
    expect(uptimeText(20_000)).toBe("under a minute");
    expect(uptimeText(42 * 60_000)).toBe("42m");
    expect(uptimeText((3 * 60 + 12) * 60_000)).toBe("3h 12m");
    expect(uptimeText((2 * 24 + 5) * 3_600_000 + 59 * 60_000)).toBe("2d 5h");
  });
});

describe("deployTarget", () => {
  it("names what Deploy deploys: the branch, the saved image's tag, a stack's own IMAGE_TAG", () => {
    expect(deployTarget({ source_type: "git", git_branch: "main" })).toBe("main");
    expect(deployTarget({ source_type: "docker-image", docker_image: "traefik/whoami:v1.10" })).toBe("whoami:v1.10");
    expect(deployTarget({ source_type: "docker-compose", docker_image: "" }, "1.27")).toBe("1.27");
    expect(deployTarget({ source_type: "docker-compose", docker_image: "" }, "7c1e2a9b4d5f60718293a4b5c6d7e8f901234567")).toBe("7c1e2a9b4d5f60718293a4b5…");
    expect(deployTarget({ source_type: "docker-compose", docker_image: "" }), "none of its own: the environment's, or none").toBe("");
  });
});

describe("exposedAt", () => {
  const tool = { kind: "tool" as const, source_type: "docker-image" as const, basic_auth_user: "", ip_allowlist: [] as string[] };
  it("names the address of an admin tool anyone can open", () => {
    expect(exposedAt(tool, ["pma.example.com", "pma2.example.com"])).toBe("pma.example.com");
  });
  it("says nothing once basic auth or an allowlist protects it, or without an address", () => {
    expect(exposedAt({ ...tool, basic_auth_user: "ops" }, ["pma.example.com"])).toBe("");
    expect(exposedAt({ ...tool, ip_allowlist: ["203.0.113.7/32"] }, ["pma.example.com"])).toBe("");
    expect(exposedAt(tool, [])).toBe("");
  });
  it("leaves apps and databases alone", () => {
    expect(exposedAt({ ...tool, kind: "app" }, ["www.example.com"])).toBe("");
    expect(exposedAt({ ...tool, kind: "database" }, ["db.example.com"])).toBe("");
  });
});

describe("latencyView", () => {
  const measured = { requests: 12340, p95_ms: 72.4, p95_bucket: { low_ms: 50, high_ms: 100 }, since: null, unmeasured: null };
  it("shows the interpolated p95 with the bucket's bounds in the title", () => {
    const v = latencyView(measured);
    expect(v.value).toBe("≈72 ms");
    expect(v.note).toBe("12,340 requests");
    expect(v.title).toBe("Between 50 ms and 100 ms, from Traefik's buckets; 12,340 requests in the last 24 hours");
  });
  it("writes small and large figures in their unit", () => {
    expect(latencyView({ ...measured, p95_ms: 4.75, p95_bucket: { low_ms: 0, high_ms: 5 } }).value).toBe("≈4.8 ms");
    expect(latencyView({ ...measured, p95_ms: 1340, p95_bucket: { low_ms: 1000, high_ms: 2500 } }).value).toBe("≈1.3 s");
    const over = latencyView({ ...measured, p95_ms: 10000, p95_bucket: { low_ms: 10000, high_ms: null } });
    expect(over.value).toBe("over 10 s");
    expect(over.title).toContain("Above 10 s, Traefik's top bucket");
  });
  it("shows the count only under 20 requests, and says when there were none", () => {
    expect(latencyView({ ...measured, requests: 7, p95_ms: null, p95_bucket: null })).toMatchObject({ value: "", note: "7 requests" });
    expect(latencyView({ ...measured, requests: 1, p95_ms: null, p95_bucket: null }).note).toBe("1 request");
    expect(latencyView({ ...measured, requests: 0, p95_ms: null, p95_bucket: null })).toMatchObject({ value: "", note: "no requests" });
  });
  it("says why it isn't measured, in one line", () => {
    const why = (unmeasured: "agent" | "adopted" | "traefik") => latencyView({ ...measured, requests: 0, p95_ms: null, p95_bucket: null, unmeasured }).note;
    expect(why("adopted")).toBe("not measured: Toft didn't start this server's Traefik");
    expect(why("agent")).toBe("not measured yet: this server's agent upgrades when it connects");
    expect(why("traefik")).toBe("not measured yet: this server's Traefik restarts to measure it");
  });
  it("names when the measured minutes start", () => {
    expect(latencyView({ ...measured, since: "2026-11-05T09:00:00Z" }).title).toMatch(/requests since /);
  });
});
