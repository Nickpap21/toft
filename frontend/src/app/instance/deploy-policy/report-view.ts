// Settings → Deploy policy's report, sorted into the board's sections
// (board: DeployPolicy). Kept apart from the page so it is tested.
import type { DeployPolicyReport, DeployPolicyReportEntry } from "@/lib/api/deploypolicy.service";

export interface WorkspaceGroup {
  orgId: string;
  orgName: string;
  entries: DeployPolicyReportEntry[];
}

export interface Note {
  entry: DeployPolicyReportEntry;
  kind: "env" | "at-deploy";
  text: string;
  code: string;
}

export interface Sections {
  /** Services whose next deploy is (or would be) refused, by workspace. */
  refused: WorkspaceGroup[];
  refusedCount: number;
  /** Services with a changed value an exception hasn't approved (D-124). */
  waitingCount: number;
  /** Services with an exception. */
  excepted: DeployPolicyReportEntry[];
  notes: Note[];
}

export function sections(report: DeployPolicyReport): Sections {
  const byOrg = new Map<string, WorkspaceGroup>();
  const excepted: DeployPolicyReportEntry[] = [];
  const notes: Note[] = [];
  let refusedCount = 0;
  let waitingCount = 0;
  for (const e of report.services) {
    // A value an exception approved before it changed waits for an
    // operator in the exceptions table, not here.
    const violations = e.violations.filter((v) => v.state !== "pending");
    if (violations.length < e.violations.length) waitingCount++;
    if (violations.length > 0) {
      refusedCount++;
      const key = e.org_id || "none";
      const g = byOrg.get(key) ?? { orgId: e.org_id, orgName: e.org_name, entries: [] };
      g.entries.push({ ...e, violations });
      byOrg.set(key, g);
    }
    if (e.allowed_rules.length > 0) excepted.push(e);
    if (e.invalid_env_keys.length > 0) {
      notes.push({
        entry: e,
        kind: "env",
        text:
          e.invalid_env_keys.length === 1
            ? "An environment variable name to fix: letters, digits and _ only, not starting with a digit."
            : "Environment variable names to fix: letters, digits and _ only, not starting with a digit.",
        code: e.invalid_env_keys.join(", "),
      });
    }
    if (e.compose_checked_at_deploy) {
      notes.push({ entry: e, kind: "at-deploy", text: "The compose file comes from the repository, so it is checked at deploy time, after the clone.", code: "" });
    }
  }
  return { refused: [...byOrg.values()], refusedCount, waitingCount, excepted, notes };
}

/** "2 in Founders · 1 in acme". */
export function perWorkspace(groups: WorkspaceGroup[]): string {
  return groups.map((g) => `${g.entries.length} in ${g.orgName || "no workspace"}`).join(" · ");
}

/** "1 variable name · 1 checked at deploy". */
export function notesSummary(notes: Note[]): string {
  const env = notes.filter((n) => n.kind === "env").length;
  const atDeploy = notes.filter((n) => n.kind === "at-deploy").length;
  const parts: string[] = [];
  if (env) parts.push(`${env} variable ${env === 1 ? "name" : "names"}`);
  if (atDeploy) parts.push(`${atDeploy} checked at deploy`);
  return parts.join(" · ") || "Nothing to note";
}

/** "storefront / production · fsn1-prod-01". */
export function whereOf(e: DeployPolicyReportEntry): string {
  const place = [e.project_name, e.environment_name].filter(Boolean).join(" / ");
  const server = e.server_name || "panel host";
  return place ? `${place} · ${server}` : server;
}

export interface ExceptionValues {
  /** What the exception approved, then (pending) what differs from it. */
  values: { text: string; pending: boolean }[];
  /** legacy: granted before v0.25.0, any value. */
  state: "pinned" | "pending" | "legacy";
}

/** An exception's values for the Exceptions table (D-124): the values and
 *  images it pinned, and the ones a deploy would now bring that it didn't. */
export function exceptionValues(e: DeployPolicyReportEntry): ExceptionValues {
  if (e.allowed_values === null) return { values: [], state: "legacy" };
  const pinned = new Set<string>();
  const values: ExceptionValues["values"] = [];
  const add = (text: string | undefined, pending: boolean) => {
    if (text && !pinned.has(text)) {
      pinned.add(text);
      values.push({ text, pending });
    }
  };
  for (const v of e.allowed_values) add(v.value, false);
  for (const v of e.allowed_values) add(v.image, false);
  const pending = [...e.allowed, ...e.violations, ...(e.last_deploy_check?.violations ?? [])].filter(
    (v) => v.state === "pending",
  );
  for (const v of pending) {
    add(v.value, true);
    add(v.image, true);
  }
  return { values, state: pending.length > 0 ? "pending" : "pinned" };
}
