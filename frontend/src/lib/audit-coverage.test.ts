import { readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";
import { describe, expect, it } from "vitest";
import { auditWords } from "@/app/instance/accounts/accounts-view";
import { AUDIT_ACTIONS } from "./audit";

// Every action the backend can write has words in the log it lands in: the
// instance log's auditWords, the workspace log's AUDIT_ACTIONS. Reads the Go
// source, so a new call fails here until it has them.
const BACKEND = join("..", "backend");

// Where each audit writer takes its action. Anything else that forwards an
// action reaches one of these with a non-literal, so DYNAMIC catches it.
const POSITION: Record<string, number> = {
  WriteAuditLog: 3,
  WriteAuditLogDetail: 3,
  RecordAuditLog: 3,
  WriteAudit: 2,
  audit: 2,
  Audit: 4,
  auditRepo: 1,
  auditUpdates: 4,
  auditAgent: 3,
  auditf: 3,
  audited: 2, // the kind: see actionOf
  record: -1, // differs by package: see RECORD
};

// The packages whose handler has an audit helper called record, and where it
// takes the action. Other record methods (internal/mcp's) aren't audit writers.
const RECORD: Record<string, number> = {
  "internal/configfile/handler.go": 1,
  "internal/domain/handler.go": 1,
  "internal/code/handler.go": 1,
  "internal/envvar/handler.go": 2,
  "internal/apikey/handler.go": 2,
};

const both = (what: string) => [`panel.${what}`, `server.${what}`];

// Every action argument that isn't a string literal, by file, writer and
// expression: what it can be, or [] when it only forwards its caller's action.
const DYNAMIC: Record<string, string[]> = {
  // Forwarding: the callers are scanned.
  "cmd/server/main.go .WriteAudit(action)": [],
  "cmd/server/main.go .WriteAuditLogDetail(action)": [],
  "internal/org/service.go .WriteAuditLog(action)": [],
  "internal/org/service.go .WriteAuditLogDetail(action)": [],
  "internal/instance/service.go .WriteAudit(action)": [],
  "internal/instance/service.go .audit(action)": [],
  "internal/configfile/handler.go .WriteAuditLogDetail(action)": [],
  "internal/domain/handler.go .WriteAuditLogDetail(action)": [],
  "internal/envvar/handler.go .WriteAuditLog(action)": [],
  "internal/code/handler.go .WriteAuditLogDetail(action)": [],
  "internal/code/handler_pulls.go .WriteAuditLogDetail(action)": [],
  "internal/apikey/handler.go .WriteAuditLogDetail(action)": [],
  "internal/server/updaterun.go .WriteAuditLogDetail(action)": [],
  "internal/server/agent.go .WriteAuditLogDetail(action)": [],
  "internal/preview/service.go .WriteAuditLogDetail(action)": [],
  'internal/observability/handler.go .WriteAuditLogDetail("observability."+kind+"_changed")': [],

  // Computed where they're written.
  "internal/server/updaterun.go .auditUpdates(action)": ["server.updates_succeeded", "server.updates_failed"],
  "internal/server/agent.go .auditAgent(action)": ["server.agent_joined", "server.agent_rejoined"],
  "internal/server/frozen.go .auditAgent(action)": ["server.agent_state_discarded", "server.agent_state_adopted"],
  "internal/code/handler_mirrors.go .auditRepo(action)": ["repository.mirror_removed", "repository.import_cancelled"],
  "internal/agent/service.go .WriteAuditLogDetail(action)": ["agent.resumed", "agent.paused"],
  "internal/agent/service.go .WriteAudit(action)": ["agents.resumed", "agents.paused"],
  'internal/agent/service.go .WriteAuditLogDetail("agent."+next)': ["agent.approved", "agent.denied"],
  'internal/deployment/handler.go .WriteAuditLog("deployment."+strings.ToLower(kind))': ["deployment.rollback", "deployment.redeploy"],
  "internal/observability/handler.go .audited(kind)": ["observability.loki_changed", "observability.grafana_changed", "observability.prometheus_changed"],
  "internal/instance/panelupdaterun.go .audit(action)": ["panel.updates_succeeded", "panel.updates_failed"],
  'internal/disk/service.go .Audit(auditAction(h.ID, "disk_clean_started"))': both("disk_clean_started"),
  'internal/disk/service.go .Audit(auditAction(h.ID, "disk_cleaned"))': both("disk_cleaned"),
  'internal/disk/service.go .Audit(auditAction(h.ID, "volume_removed"))': both("volume_removed"),
  'internal/disk/service.go .Audit(auditAction(h.ID, "disk_policy_changed"))': both("disk_policy_changed"),
};

// deploypolicy writes its rows itself, in the transaction that changes the
// exception; its actions are the Action constants in model.go.
const RAW_INSERT_FILE = "internal/deploypolicy/repository_postgres.go";

// sqlc's own: the writers above are what call these two.
const QUERY_INSERTS = ["WriteAuditEntry", "WriteInstanceAudit"];
const AUDIT_INSERT = /INSERT\s+INTO\s+(instance_)?audit_log\b/i;

function goFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = join(dir, e.name);
    if (e.isDirectory()) return goFiles(p);
    return e.name.endsWith(".go") && !/(_test|\.sql|\.gen)\.go$/.test(e.name) ? [p] : [];
  });
}

const QUOTES = "\"'`";

/** The index just past the Go string or rune literal that starts at i. */
function pastLiteral(src: string, i: number): number {
  for (let j = i + 1; j < src.length; j++) {
    if (src[i] !== "`" && src[j] === "\\") j++;
    else if (src[j] === src[i]) return j + 1;
  }
  return src.length;
}

/** src without its comments, so a writer named in one isn't a call. */
function withoutComments(src: string): string {
  let out = "";
  for (let i = 0; i < src.length; ) {
    if (QUOTES.includes(src[i])) {
      const end = pastLiteral(src, i);
      out += src.slice(i, end);
      i = end;
    } else if (src.startsWith("//", i)) {
      const end = src.indexOf("\n", i);
      i = end < 0 ? src.length : end;
    } else if (src.startsWith("/*", i)) {
      const end = src.indexOf("*/", i);
      i = end < 0 ? src.length : end + 2;
    } else {
      out += src[i++];
    }
  }
  return out;
}

/** The top-level arguments of the call whose "(" is at open. */
function callArgs(src: string, open: number): string[] {
  const args: string[] = [];
  let depth = 0;
  let start = open + 1;
  for (let i = open; i < src.length; i++) {
    const c = src[i];
    if (QUOTES.includes(c)) {
      i = pastLiteral(src, i) - 1;
    } else if ("([{".includes(c)) {
      depth++;
    } else if (")]}".includes(c)) {
      if (--depth === 0) {
        args.push(src.slice(start, i).trim());
        return args;
      }
    } else if (c === "," && depth === 1) {
      args.push(src.slice(start, i).trim());
      start = i + 1;
    }
  }
  return args;
}

type Log = "instance" | "workspace";

/** The disk advisor writes panel.* to the instance log and server.* to the
 *  workspace's (diskAudit, cmd/server/main.go). */
const logOf = (writer: string, action: string): Log =>
  writer === "WriteAudit" || writer === "audit" || action.startsWith("panel.") ? "instance" : "workspace";

/** h.audited's argument is the kind in "observability.<kind>_changed". */
const actionOf = (writer: string, literal: string) => (writer === "audited" ? `observability.${literal}_changed` : literal);

/** The values a file assigns to a computed action's variable that DYNAMIC
 *  doesn't list: `action = "a.b"`, `action, detail := "a.b", d`, and the
 *  `next` of "agent."+next. */
function unlistedValues(file: string, src: string, writer: string, expr: string): string[] {
  const computed = expr.match(/^(?:"([^"]*)"\+)?(\w+)$/);
  if (!computed) return [];
  const [, prefix = "", name] = computed;
  const listed = Object.entries(DYNAMIC).filter(([k]) => k.startsWith(`${file} `)).flatMap(([, values]) => values);
  const assigned = new RegExp(`(?<![.\\w])${name}\\b(?:\\s*,[^=\\n]*?)?\\s*:?=\\s*"([^"\\n]+)"`, "g");
  return [...src.matchAll(assigned)]
    .map((m) => actionOf(writer, prefix + m[1]))
    .filter((action) => !listed.includes(action))
    .map((action) => `${file}: ${action}`);
}

/** The sqlc queries that insert into an audit table. */
function queryInserts(): string[] {
  const dir = join(BACKEND, "db", "queries");
  return readdirSync(dir)
    .filter((f) => f.endsWith(".sql"))
    .flatMap((f) => readFileSync(join(dir, f), "utf8").split(/^-- name: /m).slice(1))
    .filter((query) => AUDIT_INSERT.test(query))
    .map((query) => query.split(/\s/)[0])
    .sort();
}

function scan() {
  const actions = new Map<string, Log>();
  const unknown: string[] = [];
  const unlisted: string[] = [];
  const used = new Set<string>();
  const rawInserts: string[] = [];
  let sites = 0;

  for (const path of [...goFiles(join(BACKEND, "internal")), ...goFiles(join(BACKEND, "cmd"))]) {
    const file = relative(BACKEND, path);
    const src = withoutComments(readFileSync(path, "utf8"));
    if (AUDIT_INSERT.test(src)) rawInserts.push(file);
    for (const m of src.matchAll(/\.(\w+)\(/g)) {
      const writer = m[1];
      if (!Object.hasOwn(POSITION, writer)) continue;
      if (writer === "record" && !Object.hasOwn(RECORD, file)) continue;
      const at = writer === "record" ? RECORD[file] : POSITION[writer];
      const arg = callArgs(src, m.index + m[0].length - 1)[at];
      sites++;
      const expr = (arg ?? "").replace(/\s+/g, " ");
      const literal = expr.match(/^"([^"\\]+)"$/)?.[1];
      if (literal) {
        const action = actionOf(writer, literal);
        actions.set(action, logOf(writer, action));
        continue;
      }
      const key = `${file} .${writer}(${expr})`;
      used.add(key);
      if (!Object.hasOwn(DYNAMIC, key)) {
        unknown.push(key);
        continue;
      }
      for (const action of DYNAMIC[key]) actions.set(action, logOf(writer, action));
      unlisted.push(...unlistedValues(file, src, writer, expr));
    }
  }

  const model = readFileSync(join(BACKEND, "internal", "deploypolicy", "model.go"), "utf8");
  for (const m of model.matchAll(/\bAction\w+\s*=\s*"([^"]+)"/g)) actions.set(m[1], "workspace");

  return { actions, unknown, unlisted, used, rawInserts, sites };
}

const { actions, unknown, unlisted, used, rawInserts, sites } = scan();
const inLog = (log: Log) => [...actions].filter(([, l]) => l === log).map(([a]) => a);

describe("the audit log's words cover what the backend writes", () => {
  it("finds the audit calls", () => {
    expect(sites).toBeGreaterThan(150);
    expect(actions.get("panel.disk_cleaned")).toBe("instance");
    expect(actions.get("server.disk_cleaned")).toBe("workspace");
    expect(actions.get("operator.granted")).toBe("instance");
    expect(actions.get("deployment.triggered")).toBe("workspace");
    expect(actions.get("deploy_policy.exception.granted")).toBe("workspace");
  });

  it("knows what every non-literal action can be", () => {
    expect(unknown, "A new audit call whose action isn't a string literal: add it to DYNAMIC with the actions it can be").toEqual([]);
    expect(Object.keys(DYNAMIC).filter((k) => !used.has(k)), "DYNAMIC has entries no call uses any more: remove them").toEqual([]);
    expect(unlisted, "A computed action is assigned a value DYNAMIC doesn't list: add it there").toEqual([]);
  });

  it("finds every direct insert into the audit tables", () => {
    expect(rawInserts, "A new direct INSERT into an audit table: go through a writer").toEqual([RAW_INSERT_FILE]);
    expect(queryInserts(), "A new query that inserts into an audit table: go through a writer").toEqual(QUERY_INSERTS);
  });

  it("has words for every instance-log action", () => {
    const missing = inLog("instance").filter((action) => auditWords({ action, detail: {} }).label === action);
    expect(missing, "Add these to auditWords (app/instance/accounts/accounts-view.ts)").toEqual([]);
  });

  it("has every workspace-log action in the catalogue", () => {
    const known = new Set(AUDIT_ACTIONS.map((a) => a.action));
    const missing = inLog("workspace").filter((action) => !known.has(action));
    expect(missing, "Add these to AUDIT_ACTIONS (lib/audit.ts)").toEqual([]);
  });
});
