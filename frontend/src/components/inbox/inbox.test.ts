import { describe, expect, it, vi } from "vitest";
import { inboxItems } from "@/components/home/home";
import { fetchAPI } from "@/lib/api/client";
import { inboxService, type InboxUpdate, type NeedsYouItem } from "@/lib/api/inbox.service";
import { dayOf, updateHref, updateText } from "./updates";

vi.mock("@/lib/api/client", () => ({ fetchAPI: vi.fn(async () => []), fetchPage: vi.fn() }));

describe("inboxService.needsYou", () => {
  it("asks for the open list, the done list, or every item", async () => {
    await inboxService.needsYou();
    await inboxService.needsYou(true);
    await inboxService.needsYou("any");
    expect(vi.mocked(fetchAPI).mock.calls.map((c) => c[0])).toEqual(["/api/v1/inbox/needs-you", "/api/v1/inbox/needs-you?done=true", "/api/v1/inbox/needs-you?done=any"]);
  });
});

const item =(x: Partial<NeedsYouItem>): NeedsYouItem =>
  ({ key: "k", kind: "alert", category: "deploys", title: "", detail: "", at: "2026-10-20T10:00:00Z", project_id: "p1", project: "storefront", ...x }) as NeedsYouItem;

describe("Needs you (D-88)", () => {
  it("says what each item is and what to do", () => {
    const rows = inboxItems([
      item({ key: "deploy:d1", kind: "deploy_failed", title: "billing-api / worker", detail: "Deploy failed · commit e41b07c", error: "missing go.sum entry", service_id: "s1", deployment_id: "d1", at: "2026-10-20T12:00:00Z" }),
      item({ key: "assigned:i1", kind: "assigned", category: "issues", title: "Retry", issue: { key: "STO-118", number: 118, title: "Retry", status: "todo", priority: "urgent" }, at: "2026-10-20T11:00:00Z" }),
      item({ key: "stale:pg:1", kind: "stale_page", category: "docs", title: "Restore Postgres", detail: "postgres changed", page_id: "pg", at: "2026-10-20T09:00:00Z" }),
      item({ key: "alert:a1", kind: "alert", category: "servers", title: "fsn1", detail: "86% used", alert_kind: "disk_full", server_id: "v1", at: "2026-10-20T08:00:00Z" }),
    ]);
    expect(rows.map((r) => [r.kind, r.action, r.href])).toEqual([
      ["Deploy failed", "View logs", "/projects/p1/services/s1?tab=deployments&deployment=d1"],
      ["Assigned, urgent", "Open issue", "/projects/p1/issues/118"],
      ["Runbook may be stale", "Review page", "/projects/p1/docs/pg"],
      ["Disk almost full", "Free space…", "/servers/v1#disk"],
    ]);
    expect(rows[0].meta).toBe("commit e41b07c");
    expect(rows[3].key).toBe("alert:a1");
  });
});

describe("updates (D-88)", () => {
  const u = (x: Partial<InboxUpdate>): InboxUpdate =>
    ({ id: "1", kind: "issue_status", category: "issues", actor: { id: "a", name: "sam" }, project_id: "p1", project: "storefront", detail: {}, created_at: "2026-10-20T10:00:00Z", read: false, done: false, ...x }) as InboxUpdate;
  it("say who did what to what", () => {
    const issue = { key: "STO-118", number: 118, title: "Webhook retries", status: "in_progress" };
    expect(updateText(u({ issue, detail: { from: "todo", to: "in_progress" } }))).toEqual({ who: "sam", text: "moved", what: "STO-118 Webhook retries", tail: " to In progress" });
    expect(updateText(u({ issue, detail: { to: "in_progress", automatic: true } })).tail).toBe(" moved to In progress automatically, from sam’s work");
    expect(updateText(u({ kind: "mention", pull: { repository: "storefront", number: 41, title: "Rate limits", state: "open" } })).what).toBe("!41 Rate limits");
    // An agent's mention names it beside its person (D-102).
    expect(updateText(u({ kind: "mention", issue, detail: { agent: "Claude Code" } })).who).toBe("Claude Code for sam");
    expect(updateHref(u({ kind: "page_edited", category: "docs", page: { id: "pg", title: "Runbook" } }))).toBe("/projects/p1/docs/pg");
  });
  it("group by day", () => {
    const now = new Date("2026-10-20T15:00:00").getTime();
    expect(dayOf(new Date("2026-10-20T09:00:00").toISOString(), now)).toBe("Today");
    expect(dayOf(new Date("2026-10-19T09:00:00").toISOString(), now)).toBe("Yesterday");
  });
});
