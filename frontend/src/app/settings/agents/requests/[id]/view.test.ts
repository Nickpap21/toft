import { describe, expect, it } from "vitest";
import type { AgentApproval } from "@/lib/api/agents.service";
import { alsoAllows, approveHint, approveLabel, chip, decision, facts, heading, isLong, keyOf, markOf, planSteps, requestVersion, rollbackTo, runSteps, startedDeployment, stepOf, stepOutput, targetParts, title } from "./view";

const a = (over: Partial<AgentApproval> = {}): AgentApproval => ({
  id: "ap1", connection_id: "c1", agent: "Claude Code", user_id: "u1", user_name: "admin", user_role: "owner", action: "deploy", tool: "deploy",
  service_id: "s1", environment_id: "e1", project_id: "p1", target: "storefront / api in production", env_class: "protected",
  request: { method: "POST", path: "/api/v1/services/s1/deployments", body: { git_ref: "main" } },
  reason: "Fixes STO-118.", plan: "1. Deploy main.\n2. Watch it.", plan_steps: [], rollback: null, state: "waiting", expires_at: "2026-10-22T14:36:00Z",
  decided_by: "", decided_at: null, note: "", executed_at: null, result: null, created_at: "2026-10-22T14:06:00Z", ...over,
});
const NOW = Date.parse("2026-10-22T14:22:00Z");

describe("agent request", () => {
  it("titles and chips by state", () => {
    expect(title(a())).toBe("Claude Code wants to deploy storefront / api in production");
    expect(title(a({ tool: "rollback" }))).toBe("Claude Code wants to roll back storefront / api in production");
    const w = chip(a(), NOW);
    expect([w.label, w.tone]).toEqual(["Waiting for a person", "pending"]);
    expect(w.hint).toMatch(/^expires \d\d:\d\d, in 14 min$/);
    expect(chip(a({ expires_at: "2026-10-22T14:21:00Z" }), NOW).hint).toMatch(/in 1 min$/);
    expect(chip(a({ state: "cancelled" }), NOW).label).toBe("Turned down");
    expect(chip(a({ state: "denied" }), NOW).tone).toBe("failed");
  });

  it("says what happened, not only the decision", () => {
    const by = { state: "approved" as const, decided_by: "sam", decided_at: "2026-10-22T14:10:00Z" };
    const failed = { status: "failed", error_msg: "health check failed" };
    const refused = chip(a({ ...by, result: { status: 409, body: { error: "paused" } } }), NOW, "sam");
    expect([refused.label, refused.tone]).toEqual(["Approved · didn’t start", "failed"]);
    expect(refused.hint).toMatch(/^by you at \d\d:\d\d$/);
    const started = a({ ...by, result: { status: 202, body: { data: { id: "d1" } } } });
    expect(chip(started, NOW, "", failed)).toMatchObject({ label: "Approved · failed", tone: "failed" });
    expect(chip(started, NOW, "", { status: "running", error_msg: "" })).toMatchObject({ label: "Approved", tone: "running" });
    expect(chip(started, NOW)).toMatchObject({ label: "Approved", tone: "running" });
    expect(decision(started, NOW, "", failed)).toMatch(/^sam approved at \d\d:\d\d, but the deploy failed: health check failed\.$/);
    expect(decision(started, NOW, "", { status: "failed", error_msg: "" })).toMatch(/, but the deploy failed\.$/);
    expect(decision(started, NOW, "", { status: "running", error_msg: "" })).toMatch(/\. The deploy started as /);
    // A step that ran and failed did start.
    const step = a({ ...by, tool: "run_runbook_step", result: { status: 200, body: { data: { lines: ["boom"], error: "exit status 1" } } } });
    expect(chip(step, NOW)).toMatchObject({ label: "Approved · failed", tone: "failed" });
  });

  it("calls a reason or plan long past a few lines' worth", () => {
    expect(isLong("short")).toBe(false);
    expect(isLong("x".repeat(180))).toBe(false);
    expect(isLong("x".repeat(181))).toBe(true);
    expect(isLong("1\n2\n3\n4")).toBe(false);
    expect(isLong("1\n2\n3\n4\n5")).toBe(true);
  });

  it("says each state's line beside its chip (board ReworkApproval)", () => {
    const by = { decided_by: "sam", decided_at: "2026-10-22T14:10:00Z" };
    expect(chip(a({ state: "approved", ...by, result: { status: 202 } }), NOW, "sam").hint).toMatch(/^by you at \d\d:\d\d$/);
    expect(chip(a({ state: "approved", ...by, result: { status: 202 } }), NOW, "ada").hint).toMatch(/^by sam at \d\d:\d\d$/);
    expect(chip(a({ state: "approved", ...by, result: null }), NOW, "sam").hint).toMatch(/^approved by you at \d\d:\d\d$/);
    expect(chip(a({ state: "denied", ...by }), NOW).hint).toMatch(/^by sam at \d\d:\d\d$/);
    expect(chip(a({ state: "expired" }), NOW).hint).toMatch(/^at \d\d:\d\d: nothing ran$/);
    expect(chip(a({ state: "cancelled", decided_at: by.decided_at }), NOW).hint).toMatch(/^at \d\d:\d\d$/);
  });

  it("splits the target for the heading and the meta line", () => {
    expect(targetParts("storefront / api in production")).toEqual({ project: "storefront", what: "api", env: "production" });
    expect(targetParts("storefront / production")).toEqual({ project: "storefront", what: "production", env: "" });
    expect(targetParts("storefront")).toEqual({ project: "", what: "storefront", env: "" });
    expect(heading(a())).toEqual({ text: "Claude Code wants to deploy api to", env: "production" });
    expect(heading(a({ tool: "rollback" }))).toEqual({ text: "Claude Code wants to roll back api in", env: "production" });
    expect(heading(a({ tool: "create_branch", target: "storefront", request: { method: "POST", path: "", body: { name: "x" } } }))).toEqual({ text: "Claude Code wants to make the branch x in storefront", env: "" });
  });

  it("reads what a deploy request names as the version step 8's block compares (D-143)", () => {
    expect(requestVersion(a())).toEqual({ gitRef: "main" });
    expect(requestVersion(a({ request: { method: "POST", path: "", body: { git_ref: "main", commit_sha: "a81c0de1234" } } }))).toEqual({ commitSha: "a81c0de1234", gitRef: "main" });
    expect(requestVersion(a({ request: { method: "POST", path: "", body: { image_tag: "ghcr.io/acme/api:1.4" } } }))).toEqual({ imageTag: "ghcr.io/acme/api:1.4" });
    // None at all: the block names what the service is set to deploy (QA P).
    expect(requestVersion(a({ request: { method: "POST", path: "", body: {} } }))).toBeUndefined();
    expect(rollbackTo(a({ tool: "rollback", request: { method: "POST", path: "/api/v1/services/s1/deployments/9b7d3f00-aaaa/rollback" } }))).toBe("9b7d3f00-aaaa");
    expect(rollbackTo(a())).toBe("");
  });

  it("says what a deploy's approval also allows (D-146)", () => {
    const rb = { to_deployment_id: "d0", to_commit: "9b7d3f0aa", commit: "", live_at: null, until: null, used_at: null, active: false };
    expect(alsoAllows(a())).toBeNull();
    expect(alsoAllows(a({ rollback: rb }))?.v).toBe("Rolling back to 9b7d3f0 for 30 minutes after this deploy goes live");
    // The page names the commit it deploys, as the board does.
    expect(alsoAllows(a({ rollback: rb }), NOW, "a81c0de")).toEqual({ v: "Rolling back to 9b7d3f0 for 30 minutes after a81c0de goes live", more: "once, by Claude Code, while a81c0de is what’s live" });
    const live = alsoAllows(a({ state: "approved", rollback: { ...rb, until: "2026-10-22T14:50:00Z", active: true } }));
    expect(live?.v).toMatch(/^Rolling back to 9b7d3f0 until \d\d:\d\d$/);
    expect(live?.more).toBe("once, by Claude Code, while this deploy is what’s live");
    const ended = { ...rb, until: "2026-10-22T14:50:00Z" };
    expect(alsoAllows(a({ state: "approved", rollback: ended }), NOW)?.v).toBe("Rolling back to 9b7d3f0: no longer");
    expect(alsoAllows(a({ state: "approved", rollback: ended }), Date.parse(ended.until))?.v).toMatch(/^Rolling back to 9b7d3f0 until \d\d:\d\d: over$/);
    expect(alsoAllows(a({ state: "approved", rollback: ended }), Date.parse("2026-11-02T12:00:00Z"))?.v).toMatch(/ until 2026-10-22 \d\d:\d\d: over$/);
    expect(alsoAllows(a({ state: "approved", rollback: { ...rb, until: "2026-10-22T14:50:00Z", used_at: "2026-10-22T14:31:00Z" } }))?.more).toBe("without asking again");
    expect(facts(a({ rollback: rb }), (id) => id).some((f) => f.k === "Also allows")).toBe(true);

    const step = { text: "Deploy main.", tool: "deploy", service_id: "s1", mark: "this_request" as const };
    expect(approveHint(a({ rollback: rb, plan_steps: [step] }))).toMatch(/and lets it roll back to 9b7d3f0 without asking again.*; the plan marks which\.$/);
    expect(approveHint(a({ rollback: { ...rb, to_commit: "" } }))).toMatch(/roll back to what is live now without asking again.* waits again\.$/);
    expect(approveHint(a())).toContain("Anything else it asks waits again");
    expect(approveHint(a())).toContain("approved by you");
    expect(approveHint(a(), false)).toBe("Approving runs this deploy once, as Claude Code for admin. Anything else it asks waits again.");
    expect(markOf("allowed_by_this_approval", a({ rollback: rb }))?.label).toBe("allowed by this approval");
    expect(markOf("allowed_by_this_approval", a({ rollback: { ...rb, until: "2026-10-22T14:50:00Z", active: true } }))?.label).toMatch(/^allowed until \d\d:\d\d$/);
    expect(markOf("will_ask_again", a())?.ask).toBe(true);

    const went = a({ state: "approved", decided_by: "sam", decided_at: "2026-10-22T14:10:00Z", result: { status: 202, body: { data: { id: "d1" } } },
      rollback: { ...rb, commit: "a81c0deff", live_at: "2026-10-22T14:20:00Z", until: "2026-10-22T14:50:00Z", active: true } });
    expect(decision(went, NOW, "sam")).toMatch(/^You approved at \d\d:\d\d\. a81c0de went live at \d\d:\d\d\. Claude Code may roll back to 9b7d3f0 until \d\d:\d\d without asking\.$/);
    expect(decision(went, Date.parse("2026-11-02T12:00:00Z"))).toMatch(/^sam approved at 2026-10-22 \d\d:\d\d\. a81c0de went live at 2026-10-22 \d\d:\d\d\./);
    expect(decision({ ...went, rollback: { ...went.rollback!, active: false } }, NOW)).not.toContain("may roll back");
    expect(decision({ ...went, rollback: { ...went.rollback!, active: false, used_at: "2026-10-22T14:31:00Z" } }, NOW))
      .toMatch(/ Claude Code rolled back to 9b7d3f0 at \d\d:\d\d without asking\.$/);
    expect(decision({ ...went, rollback: { ...went.rollback!, commit: "", to_commit: "" } }, NOW))
      .toMatch(/ It went live at \d\d:\d\d\. Claude Code may roll back to the deployment before it until /);
    expect(decision({ ...went, rollback: { ...went.rollback!, live_at: null, until: null, active: false } }, NOW)).toMatch(/^sam approved at \d\d:\d\d\. The deploy started as Claude Code for admin\.$/);
  });

  it("lists a numbered plan, else keeps it as text", () => {
    expect(planSteps("1. Deploy main.\n2. Watch it.\n")).toEqual(["Deploy main.", "Watch it."]);
    expect(planSteps("Deploy, then watch.")).toBeNull();
    expect(planSteps("")).toBeNull();
  });

  it("says what Toft runs from the request only", () => {
    const f = facts(a(), (id) => `/d/${id}`);
    expect(f.map((x) => [x.k, x.v, x.more])).toEqual([
      ["Service", "storefront / api", "in production (protected)"],
      ["Deploys", "main", "at its latest commit when it runs"],
    ]);
    const sha = facts(a({ request: { method: "POST", path: "", body: { git_ref: "main", commit_sha: "a81c0de1234" } } }), String);
    expect([sha[1].v, sha[1].more]).toEqual(["a81c0de", "from main"]);
    const rb = facts(a({ tool: "rollback", request: { method: "POST", path: "/api/v1/services/s1/deployments/9b7d3f00-aaaa/rollback" } }), (id) => `/d/${id}`);
    expect([rb[1].k, rb[1].v, rb[1].href]).toEqual(["Rolls back to", "9b7d3f00", "/d/9b7d3f00-aaaa"]);
  });

  it("reads the outcome of a decision", () => {
    const ok = a({ state: "approved", decided_by: "sam", decided_at: "2026-10-22T14:11:00Z", note: "Go ahead", result: { status: 202, body: { data: { id: "dep1" } } } });
    expect(startedDeployment(ok)).toBe("dep1");
    expect(decision(ok, NOW)).toMatch(/^sam approved at \d\d:\d\d\. The deploy started as Claude Code for admin\.$/);
    const bad = a({ state: "approved", decided_by: "sam", decided_at: "2026-10-22T14:11:00Z", result: { status: 409, body: { error: "agents are paused" } } });
    expect(startedDeployment(bad)).toBe("");
    expect(decision(bad, NOW)).toMatch(/^sam approved at \d\d:\d\d, but the deploy didn’t start: agents are paused\.$/);
    expect(decision(a({ state: "denied", decided_by: "sam", decided_at: "2026-10-22T14:09:00Z" }), NOW)).toMatch(/^sam denied at \d\d:\d\d, so nothing ran\. Claude Code was told; it may ask again with another reason\.$/);
    expect(decision(a({ state: "cancelled", decided_at: "2026-10-22T14:09:00Z", note: "Agents were paused." }), NOW)).toMatch(/^Turned down at \d\d:\d\d, so nothing ran\. Pausing agents or revoking the connection turns down its waiting requests\.$/);
    expect(decision(a({ state: "expired" }), NOW)).toBe("Nobody decided within 30 minutes, so nothing ran. Claude Code was told; it can ask again.");
    expect(decision(a())).toBeNull();
  });

  it("titles, facts and buttons for set_secret, never with a value", () => {
    const sec = a({ tool: "set_secret", action: "secrets", target: "billing-api / api in staging",
      request: { method: "PUT", path: "/api/v1/services/s1/secrets/STRIPE_WEBHOOK_SECRET", body: {} } });
    expect(title(sec)).toBe("Claude Code wants to set the secret STRIPE_WEBHOOK_SECRET on billing-api / api in staging");
    expect(keyOf(a({ request: { method: "POST", path: "/api/v1/services/s1/secrets", body: { key: "NEW_KEY" } } }))).toBe("NEW_KEY");
    expect(approveLabel(sec)).toBe("Approve and set the secret");
    const f = facts(sec, String, { updated_at: "2026-10-12T10:00:00Z", updated_by: "sam" });
    expect(f.map((x) => [x.k, x.v])).toEqual([
      ["Service", "billing-api / api"], ["Secret", "STRIPE_WEBHOOK_SECRET"], ["Value", "Hidden"], ["Takes effect", "At the next deploy"],
    ]);
    expect(f[1].more).toBe("replaces the value sam set on 12 Oct");
    // The policy's class names where it lands; a database in staging reads protected.
    expect(facts(a({ ...sec, env_class: "other" }), String)[0].more).toBe("in staging (other environment)");
    expect(facts(a({ ...sec, env_class: "protected" }), String)[0].more).toBe("in staging (protected)");
    expect(facts(sec, String, null)[1].more).toBe("a new secret");
  });

  it("reads a runbook step's page, step and version, and finds its command", () => {
    const run = a({ tool: "run_runbook_step", action: "runbooks", target: "storefront / worker in production",
      request: { method: "POST", path: "/api/v1/projects/p1/docs/pg1/steps/2/run", body: { version: 12 } } });
    expect(stepOf(run)).toEqual({ project: "p1", page: "pg1", step: 2, version: 12 });
    expect(title(run, "Clear a stuck mail queue")).toBe("Claude Code wants to run step 3 of “Clear a stuck mail queue” in storefront / worker in production");
    expect(approveLabel(run)).toBe("Approve and run step 3");
    expect(facts(run, String, undefined, { container: "storefront-worker-prod-2", server: "fsn1-prod-01" }).slice(0, 2).map((x) => [x.k, x.v, x.more])).toEqual([
      ["Runs in", "storefront / worker", "in production (protected): its first container, as the image’s user"],
      ["Container", "storefront-worker-prod-2", "on fsn1-prod-01"],
    ]);
    const id = "67201ad6-a7fc-490a-ab2d-7a95baa8d136";
    const md = `Intro\n\n\`\`\`toft-service ${id}\n\`\`\`\n\n\`\`\`toft-run ${id}\nwget -qO- http://localhost/healthz\n\`\`\`\n\n\`\`\`toft-run ${id} host\ndocker stop a \\\n  b\n\`\`\`\n\n\`\`\`toft-run ${id}\n\n\`\`\`\n`;
    expect(runSteps(md)).toEqual([
      { command: "wget -qO- http://localhost/healthz", host: false },
      { command: "docker stop a \\\n  b", host: true },
    ]);
    const ran = a({ ...run, state: "approved", decided_by: "sam", decided_at: "2026-10-22T14:11:00Z", result: { status: 200, body: { data: { outcome: "ok", lines: ["moved 4812"] } } } });
    expect(stepOutput(ran)).toEqual({ lines: ["moved 4812"], error: "" });
    expect(decision(ran)).toMatch(/\. The step ran as Claude Code for admin\.$/);
  });

  it("says Running… while an approved request runs", () => {
    const running = a({ state: "approved", decided_by: "sam", decided_at: "2026-10-22T14:11:00Z", result: null });
    expect(decision(running)).toMatch(/\. Running as Claude Code for admin…$/);
    expect(chip(running, NOW).label).toBe("Running");
  });

  it("titles the other tools from their requests", () => {
    const req = (tool: string, path: string, body: Record<string, unknown>) =>
      title(a({ tool, target: "storefront", service_id: null, request: { method: "POST", path, body } }));
    expect(req("create_branch", "/api/v1/projects/p1/repositories/r1/branches", { name: "agent/x" })).toBe("Claude Code wants to make the branch agent/x in storefront");
    expect(req("open_pr", "/api/v1/projects/p1/repositories/r1/pulls", { title: "Fix" })).toBe("Claude Code wants to open the pull request “Fix” in storefront");
    expect(req("comment", "/api/v1/projects/p1/repositories/r1/pulls/46/comments", { body: "hi" })).toBe("Claude Code wants to comment on !46 in storefront");
    expect(req("comment", "/api/v1/projects/p1/issues/131/comments", { body: "hi" })).toBe("Claude Code wants to comment on issue 131 in storefront");
    expect(req("create_preview", "/api/v1/projects/p1/repositories/r1/pulls/46/preview", {})).toBe("Claude Code wants to start the preview of !46 in storefront");
    const masked = facts(a({ tool: "set_env_var", request: { method: "POST", path: "/api/v1/services/s1/env-vars", body: { key: "K", value: "v", masked: true } } }), String);
    expect(masked.find((f) => f.k === "Value")?.v).toBe("Masked");
  });
});
