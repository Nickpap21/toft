import { describe, expect, it } from "vitest";
import { containerKeyCount, mergeKeys, parseDotenv, pendingChanges, rebaseEdits } from "./variables-view";

const live = "2026-11-06T10:00:00Z";
const before = "2026-11-01T10:00:00Z";
const later = "2026-11-06T11:00:00Z";
const v = (key: string, value: string, created = before, updated = created, extra = {}) => ({
  key, value, created_at: created, updated_at: updated, updated_by: "admin", ...extra,
});

describe("mergeKeys", () => {
  it("keeps the environment's order with own rows in place, own-only keys after", () => {
    const rows = mergeKeys([{ key: "NODE_ENV" }, { key: "LOG_LEVEL" }], [{ key: "PORT" }, { key: "LOG_LEVEL" }]);
    expect(rows.map((r) => [r.key, !!r.shared, !!r.own])).toEqual([
      ["NODE_ENV", true, false],
      ["LOG_LEVEL", true, true],
      ["PORT", false, true],
    ]);
  });

  it("keeps a row being typed at the end, standing in for the shared key it names", () => {
    const typed = { key: "NODE_ENV", typed: true };
    const rows = mergeKeys([{ key: "NODE_ENV" }, { key: "TZ" }], [typed], (o) => !!o.typed);
    expect(rows.map((r) => r.key)).toEqual(["TZ", "NODE_ENV"]);
    expect(rows[1].shared?.key).toBe("NODE_ENV");
  });
});

describe("containerKeyCount", () => {
  it("counts a key once across own and shared variables and secrets", () => {
    expect(containerKeyCount([[{ key: "A" }, { key: "B" }], [{ key: "S" }], [{ key: "A" }, { key: "C" }], [{ key: "S" }]])).toBe(4);
  });
});

describe("pendingChanges", () => {
  const base = { own: [], ownSecrets: [], env: [], envSecrets: [], since: live, envName: "production" };

  it("names the old value of a key overriding an unchanged shared one", () => {
    const got = pendingChanges({ ...base, own: [v("LOG_LEVEL", "debug", later)], env: [v("LOG_LEVEL", "info")] });
    expect(got).toEqual([{ key: "LOG_LEVEL", text: "info → debug", by: "admin", at: later }]);
  });

  it("shows only the new value where the old one isn't known", () => {
    expect(pendingChanges({ ...base, own: [v("LOG_LEVEL", "debug", before, later)] })[0].text).toBe("→ debug");
    // The shared value changed after the deploy too: what ran isn't known.
    const both = pendingChanges({ ...base, own: [v("LOG_LEVEL", "debug", later)], env: [v("LOG_LEVEL", "warn", before, later)] });
    expect(both.map((p) => p.text)).toEqual(["→ debug"]);
  });

  it("says added for a new key, and never shows a masked value", () => {
    expect(pendingChanges({ ...base, own: [v("PAGE", "50", later)] })[0].text).toBe("added as 50");
    expect(pendingChanges({ ...base, own: [v("TOKEN", "x", before, later, { masked: true })] })[0].text).toBe("→ a hidden value");
  });

  it("calls a secret added or replaced, never its value", () => {
    const got = pendingChanges({ ...base, ownSecrets: [{ key: "NEW", created_at: later, updated_at: later }, { key: "OLD", created_at: before, updated_at: later }] });
    expect(got.map((p) => `${p.key} ${p.text}`).sort()).toEqual(["NEW added", "OLD replaced"]);
  });

  it("lists the environment's changes only where the service has no own key", () => {
    const got = pendingChanges({
      ...base,
      own: [v("TZ", "UTC")],
      env: [v("TZ", "CET", before, later), v("REGION", "eu", later)],
      envSecrets: [{ key: "DSN", created_at: before, updated_at: later }],
    });
    expect(got.map((p) => `${p.key} ${p.text}`).sort()).toEqual(["DSN replaced in production", "REGION added in production as eu"]);
  });

  it("has nothing pending without a live deployment, or for changes before it", () => {
    expect(pendingChanges({ ...base, since: null, own: [v("A", "1", later)] })).toEqual([]);
    expect(pendingChanges({ ...base, own: [v("A", "1")] })).toEqual([]);
  });
});

describe("parseDotenv", () => {
  it("reads names, export, comments, quotes and CRLF, the last repeat winning", () => {
    const got = parseDotenv('# c\r\nexport A=1\r\nB = two # note\nC="x\\ny"\nD=\'#lit\'\nA=3\n\nE=\n');
    expect(got.vars).toEqual([
      { key: "B", value: "two" },
      { key: "C", value: "x\ny" },
      { key: "D", value: "#lit" },
      { key: "A", value: "3" },
      { key: "E", value: "" },
    ]);
    expect(got.refused).toEqual([]);
  });

  it("refuses lines without = , bad names and unclosed quotes", () => {
    expect(parseDotenv('JUST TEXT\n1BAD=x\nmy-key=y\nQ="open\n').refused).toEqual(["JUST TEXT", "1BAD", "my-key", "Q"]);
  });
});

describe("rebaseEdits", () => {
  type Row = { key: string; value: string; saved?: { key: string }; removed?: boolean; editing?: boolean };
  const row = (key: string, value: string): Row => ({ key, value, saved: { key } });
  const dirty = (r: Row) => !r.saved || !!r.removed || r.key !== r.saved.key || r.value !== (r as { was?: string }).was;

  it("puts what was typed on the fresh read and keeps what others added", () => {
    const typed: Row[] = [
      { ...row("KEEP", "typed"), was: "old" } as Row,
      { ...row("GONE", "x"), removed: true, was: "x" } as Row,
      { key: "NEW", value: "1" },
    ];
    const fresh = [row("GONE", "theirs"), row("KEEP", "theirs"), row("THEIRS", "1")];
    const out = rebaseEdits(typed, fresh, dirty);
    expect(out.map((r) => [r.key, r.value, !!r.removed])).toEqual([
      ["GONE", "x", true],
      ["KEEP", "typed", false],
      ["THEIRS", "1", false],
      ["NEW", "1", false],
    ]);
  });

  it("re-adds an edit whose key was removed meanwhile; drops a removal of one that's gone", () => {
    const typed: Row[] = [
      { ...row("EDITED", "typed"), was: "old" } as Row,
      { ...row("REMOVED", "x"), removed: true, was: "x" } as Row,
    ];
    const out = rebaseEdits(typed, [] as Row[], dirty);
    expect(out.map((r) => [r.key, r.value, r.saved])).toEqual([["EDITED", "typed", undefined]]);
  });
});
