import { describe, expect, it } from "vitest";
import type { DeployTarget } from "@/lib/api/code.service";
import { andList, byAttention, checkDuration, checkTitle, deployLines, summaryText } from "./checks";

describe("checks (D-77)", () => {
  it("names Toft's deploy checks after their service and environment", () => {
    expect(checkTitle("toft/deploy/production/web")).toEqual({ title: "Deploy web", env: "production", deploy: true });
    expect(checkTitle("toft/deploy/worker")).toEqual({ title: "Deploy worker", deploy: true });
    expect(checkTitle("ci/test")).toEqual({ title: "ci/test", deploy: false });
  });

  it("says the checks together", () => {
    expect(summaryText({ state: "success", total: 4, passed: 4, failed: 0, pending: 0 })).toBe("All 4 passed");
    expect(summaryText({ state: "success", total: 1, passed: 1, failed: 0, pending: 0 })).toBe("Passed");
    expect(summaryText({ state: "failure", total: 4, passed: 2, failed: 1, pending: 1 })).toBe("1 failing, 1 running, 2 passed");
  });

  it("times a check, until now while it runs", () => {
    const start = "2026-10-19T10:00:00Z";
    expect(checkDuration({ started_at: start, completed_at: "2026-10-19T10:01:51Z" }, 0)).toBe("1m 51s");
    expect(checkDuration({ started_at: start, completed_at: null }, Date.parse("2026-10-19T10:00:48Z"))).toBe("48s");
    expect(checkDuration({ started_at: null, completed_at: null }, 0)).toBe("");
  });

  it("says what a push to a branch deploys, by environment and server", () => {
    const t = (service_name: string, branch: string, env: string | null, server: string | null, deploy_on_push = true): DeployTarget => ({
      service_id: service_name, service_name, branch, environment_name: env, environment_color: null, server_name: server, base_directory: "/", deploy_on_push,
    });
    const targets = [
      t("web", "main", "production", "fsn1-prod-01"),
      t("api", "main", "production", "fsn1-prod-01"),
      t("worker", "main", "production", null),
      t("cron", "main", "production", "fsn1-prod-01", false),
      t("web", "develop", "staging", "fsn1-prod-01"),
    ];
    expect(deployLines(targets, "main")).toEqual(["web and api to production on fsn1-prod-01", "worker to production on the panel’s server"]);
    expect(deployLines(targets, "nope")).toEqual([]);
    expect(andList(["a", "b", "c"])).toBe("a, b and c");
  });
});

describe("byAttention", () => {
  it("puts what failed first, then what runs", () => {
    const c = (name: string, state: "success" | "failure" | "running" | "skipped") => ({ name, state });
    expect(byAttention([c("a", "success"), c("b", "running"), c("c", "failure"), c("d", "skipped")]).map((x) => x.name)).toEqual(["c", "b", "a", "d"]);
  });
});
