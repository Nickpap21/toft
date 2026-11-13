import { describe, expect, it } from "vitest";
import { byline, checksLine, checksSummary, commitWaits, headerLead, passwordLine, tabOf, whoMerges } from "./view";

const agent = { id: "u1", name: "sam", agent: "Claude Code" };
const person = { id: "u1", name: "sam" };

describe("a pull request's words (board ReworkPull)", () => {
  it("reads ?tab=changes as Files", () => {
    expect(tabOf("changes")).toBe("files");
    expect(tabOf("files")).toBe("files");
    expect(tabOf("checks")).toBe("checks");
    expect(tabOf("nonsense")).toBe("conversation");
    expect(tabOf(null)).toBe("conversation");
  });

  it("names an agent for its person in the header", () => {
    expect(byline(agent, "")).toBe("Claude Code, for sam");
    expect(byline(person, "")).toBe("sam");
    expect(byline(null, "A deleted account")).toBe("A deleted account");
    expect(headerLead({ state: "open", author: agent })).toBe("Claude Code, for sam, wants to merge");
    expect(headerLead({ state: "open", author: person })).toBe("sam wants to merge");
    expect(headerLead({ state: "merged", author: agent, merged_by: { id: "u2", name: "nikos" } })).toBe("nikos merged");
    expect(headerLead({ state: "closed", author: person })).toBe("sam proposed");
  });

  it("says who merges", () => {
    expect(whoMerges({ author: agent })).toBe("An owner or developer merges it. An agent’s pull request is never merged on its own say.");
    expect(whoMerges({ author: person })).toBe("An owner or developer merges it.");
  });

  it("says the head commit's checks in one line", () => {
    const sha = "7e41c2a0000000";
    expect(checksLine([], sha)).toBeNull();
    expect(checksLine([{ name: "ci/test", state: "success" }], sha)).toEqual({ state: "success", text: "ci/test passed on 7e41c2a" });
    expect(checksLine([{ name: "ci/test", state: "success" }, { name: "lint", state: "skipped" }], sha)).toEqual({ state: "success", text: "All 2 checks passed on 7e41c2a" });
    expect(checksLine([{ name: "ci/test", state: "failure" }, { name: "lint", state: "success" }], sha)).toEqual({ state: "failure", text: "ci/test failed on 7e41c2a" });
    expect(checksLine([{ name: "a", state: "failure" }, { name: "b", state: "cancelled" }], sha)?.text).toBe("2 checks failed on 7e41c2a");
    expect(checksLine([{ name: "ci/test", state: "running" }, { name: "lint", state: "success" }], sha)).toEqual({ state: "running", text: "ci/test is running on 7e41c2a" });
    expect(checksLine([{ name: "a", state: "pending" }, { name: "b", state: "running" }], sha)?.text).toBe("2 checks are running on 7e41c2a");
    expect(checksLine([{ name: "toft/deploy/staging/web", state: "success" }], sha)?.text).toBe("Deploy web passed on 7e41c2a");
  });

  it("counts preview checks that wait for approval as waiting", () => {
    const checks = [
      { name: "ci/test", state: "success" },
      { name: "toft/preview/web", state: "pending" },
      { name: "toft/preview/api", state: "pending" },
    ] as const;
    expect(checksSummary([...checks], true)).toBe("1 passed · 2 waiting");
    expect(checksSummary([...checks], false)).toBe("1 passed · 2 running");
    expect(checksSummary([{ name: "a", state: "failure" }, { name: "b", state: "running" }], false)).toBe("1 failing · 1 running");
    expect(checksSummary([{ name: "a", state: "success" }, { name: "b", state: "skipped" }], false)).toBe("All 2 passed");
  });

  it("shows the waiting card only with a commit to approve and a viewer who may approve", () => {
    const pv = { status: "needs_approval", untrusted: true, needs_approval: true } as const;
    expect(commitWaits(pv, true)).toBe(true);
    expect(commitWaits(pv, false)).toBe(false);
    expect(commitWaits({ ...pv, status: "running" }, true)).toBe(true); // a new commit after an approved one
    expect(commitWaits({ ...pv, status: "blocked" }, true)).toBe(false);
    expect(commitWaits({ status: "running", untrusted: true, needs_approval: false }, true)).toBe(false);
    expect(commitWaits({ status: "running", untrusted: false, needs_approval: true }, true)).toBe(false);
    expect(commitWaits(null, true)).toBe(false);
  });

  it("says when the agent's branch password expires", () => {
    const now = Date.parse("2026-11-07T12:00:00Z");
    expect(passwordLine("2026-11-08T11:00:00Z", now)).toBe("its branch password expires in 23h");
    expect(passwordLine("2026-11-07T12:00:30Z", now)).toBe("its branch password expires in under a minute");
    expect(passwordLine("2026-11-07T11:00:00Z", now)).toBe("its branch password expired; it asks again to push");
  });
});
