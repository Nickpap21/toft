import { describe, expect, it } from "vitest";
import type { Preview } from "@/lib/api/preview.service";
import { inboxItems } from "@/components/home/home";
import type { NeedsYouItem } from "@/lib/api/inbox.service";
import { addressExample, copiesLine, firstAddress, previewRowText, statusLabel, statusWhy } from "./view";

const pv = (x: Partial<Preview>): Preview =>
  ({
    pull_number: 42, repository_id: "r1", repository: "storefront", title: "t", branch: "feat", environment: { id: "e", name: "pr-42" },
    copies: "staging", status: "running", untrusted: false, head_sha: "9c2e1d4aaaa", needs_approval: false, reached: [],
    created_at: "2026-10-21T10:00:00Z", updated_at: "2026-10-21T10:00:00Z",
    services: [
      { id: "s1", name: "web", server: "fsn1", status: "running", addresses: ["pr-42-web.preview.example.com"] },
      { id: "s2", name: "api", server: "fsn1", status: "running", addresses: ["pr-42-api.preview.example.com"] },
    ],
    ...x,
  }) as Preview;

describe("previews' words", () => {
  it("says what a preview copies", () => {
    expect(copiesLine("staging", ["web", "api"], ["worker", "postgres", "redis"])).toBe(
      "A preview builds web and api from the pull request (they build from this repository), and reaches staging’s worker, postgres and redis by name, with staging’s variables and secrets. It writes to staging’s database, and doesn’t run staging’s migrate or pre-deploy commands.",
    );
    expect(copiesLine("staging", [], ["postgres"])).toContain("nothing to build");
  });

  it("shows an address under the domain, or sslip.io", () => {
    expect(addressExample("preview.example.com", "web", "storefront", "203.0.113.10")).toBe("pr-42-web.preview.example.com");
    expect(addressExample("", "web", "Store Front", "203.0.113.10")).toBe("web-pr42-store-front.203-0-113-10.sslip.io");
  });

  it("names each status and why", () => {
    expect(statusLabel(pv({ status: "stopped", stopped_reason: "room" }))).toBe("Stopped for room");
    expect(statusWhy(pv({ status: "stopped", stopped_reason: "room", note: "production’s api needed the memory on fsn1" }))).toBe(
      "production’s api needed the memory on fsn1",
    );
    expect(statusWhy(pv({ status: "needs_approval", opened_with_key: { key: "claude-code", owner: "Nikos" } }))).toBe("opened with an API key");
    expect(statusWhy(pv({ status: "waiting_room" }), 6)).toBe("6 previews at once");
    expect(statusWhy(pv({ status: "needs_approval", opened_with_key: { key: "", owner: "Nikos", agent: "Claude Code" } }))).toBe("opened by Claude Code, an agent");
    expect(statusWhy(pv({ status: "starting", services: [{ id: "s", name: "web", server: "", status: "starting", addresses: [], isolation: "standard" }] }))).toBe(
      "building web",
    );
    expect(previewRowText(pv({ status: "failed" }))).toBe("pr-42 · web, api · Failed");
    expect(firstAddress(pv({}))).toEqual({ url: "pr-42-web.preview.example.com", more: 1 });
  });

  it("puts a preview to approve in Needs you", () => {
    const [row] = inboxItems([
      {
        key: "preview:p1:3f1a2b9", kind: "preview_approval", category: "code", title: "Retry webhooks", detail: "storefront !45 · claude-code",
        at: "2026-10-21T10:00:00Z", project_id: "p1", project: "storefront", repository_id: "r1", repository: "storefront", pull_number: 45,
        sha: "3f1a2b9cccc",
      } as NeedsYouItem,
    ]);
    expect(row.kind).toBe("Preview to approve");
    expect(row.href).toBe("/projects/p1/code/storefront/pulls/45");
    // One action per row (D-140): the pull request's page approves.
    expect(row.action).toBe("Review");
  });
});
