import { describe, expect, it } from "vitest";
import type { DeployPolicyReport, DeployPolicyReportEntry, PolicyViolation } from "@/lib/api/deploypolicy.service";
import { exceptionValues, notesSummary, perWorkspace, sections, whereOf } from "./report-view";

const v: PolicyViolation = { rule: "host_exec", field: "pre_deploy_commands[0]", message: "a command runs on the host" };
const entry = (over: Partial<DeployPolicyReportEntry>): DeployPolicyReportEntry => ({
  service_id: "s",
  service_name: "worker",
  org_id: "o1",
  org_name: "Founders",
  project_id: "p",
  project_name: "storefront",
  environment_name: "production",
  server_id: "",
  server_name: "",
  source_type: "git",
  violations: [],
  allowed: [],
  allowed_rules: [],
  compose_checked_at_deploy: false,
  invalid_env_keys: [],
  exception_reason: "",
  exception_granted_by: "",
  exception_granted_at: null,
  allowed_values: null,
  last_deploy_check: null,
  ...over,
});
const report = (services: DeployPolicyReportEntry[]): DeployPolicyReport => ({
  mode: "warn",
  generated_at: "",
  services,
  services_checked: 16,
  servers_checked: 2,
  rules: [],
});

describe("sections", () => {
  it("sorts entries into refused, exceptions and notes", () => {
    const s = sections(
      report([
        entry({ service_id: "a", violations: [v] }),
        entry({ service_id: "b", violations: [v], org_id: "o2", org_name: "acme" }),
        entry({ service_id: "c", violations: [v] }),
        entry({ service_id: "d", allowed_rules: ["group_add"] }),
        entry({ service_id: "e", invalid_env_keys: ["webhook-retry-max"], compose_checked_at_deploy: true }),
      ]),
    );
    expect(s.refusedCount).toBe(3);
    expect(perWorkspace(s.refused)).toBe("2 in Founders · 1 in acme");
    expect(s.excepted.map((e) => e.service_id)).toEqual(["d"]);
    expect(notesSummary(s.notes)).toBe("1 variable name · 1 checked at deploy");
    expect(s.notes[0].code).toBe("webhook-retry-max");
  });
  it("leaves a changed value to the exceptions table", () => {
    const pending: PolicyViolation = { ...v, state: "pending" };
    const s = sections(
      report([
        entry({ service_id: "a", violations: [pending], allowed_rules: ["host_exec"] }),
        entry({ service_id: "b", violations: [pending, { ...v, rule: "privileged" }], allowed_rules: ["host_exec"] }),
      ]),
    );
    expect(s.refusedCount).toBe(1);
    expect(s.waitingCount).toBe(2);
    expect(s.refused[0].entries.map((e) => [e.service_id, e.violations.map((x) => x.rule)])).toEqual([["b", ["privileged"]]]);
    expect(s.excepted.map((e) => e.service_id)).toEqual(["a", "b"]);
  });
  it("has nothing to note on a clean report", () => {
    expect(notesSummary(sections(report([])).notes)).toBe("Nothing to note");
  });
});

describe("whereOf", () => {
  it("names the place and the server", () => {
    expect(whereOf(entry({ server_name: "fsn1-prod-01" }))).toBe("storefront / production · fsn1-prod-01");
    expect(whereOf(entry({ project_name: "", environment_name: "" }))).toBe("panel host");
  });
});

describe("exceptionValues", () => {
  const pinned = { rule: "host_bind_mount", field: "volumes[0]", value: "/srv/a → /data", image: "agent:1", message: "m" };
  it("is legacy without values", () => {
    expect(exceptionValues(entry({ allowed_rules: ["host_bind_mount"] }))).toEqual({ values: [], state: "legacy" });
  });
  it("lists the pinned values and images once", () => {
    const e = entry({ allowed_rules: ["host_bind_mount"], allowed_values: [pinned, { ...pinned, value: "/srv/b → /b" }] });
    expect(exceptionValues(e)).toEqual({
      state: "pinned",
      values: [
        { text: "/srv/a → /data", pending: false },
        { text: "/srv/b → /b", pending: false },
        { text: "agent:1", pending: false },
      ],
    });
  });
  it("adds what a deploy would bring that it didn't approve", () => {
    const e = entry({
      allowed_rules: ["host_bind_mount"],
      allowed_values: [pinned],
      violations: [{ ...v, rule: "host_bind_mount", value: "/srv/a → /data", image: "agent:2", state: "pending" }],
    });
    expect(exceptionValues(e)).toEqual({
      state: "pending",
      values: [
        { text: "/srv/a → /data", pending: false },
        { text: "agent:1", pending: false },
        { text: "agent:2", pending: true },
      ],
    });
  });
});
