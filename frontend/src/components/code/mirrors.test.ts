import { describe, expect, it } from "vitest";
import type { Webhook } from "@/lib/api/code.service";
import { eventsText, lastAnswer, nameFromURL, remoteName } from "./mirrors";
import { mirrorBusy } from "./repo";

describe("import and mirrors", () => {
  it("names a remote and suggests a repository name from its URL", () => {
    expect(remoteName("https://github.com/founders/shop.git")).toBe("github.com/founders/shop");
    expect(nameFromURL("https://github.com/founders/Shop.git")).toBe("shop");
    expect(nameFromURL("https://gitlab.example.com/a/b/My Site/")).toBe("my-site");
    expect(nameFromURL("https://github.com/x/.hidden.git")).toBe("hidden");
  });
  it("follows a mirror while it waits or runs", () => {
    const m = (state: string) => ({ state }) as Parameters<typeof mirrorBusy>[0];
    expect(mirrorBusy(m("importing"))).toBe(true);
    expect(mirrorBusy(m("syncing"))).toBe(true);
    expect(mirrorBusy(m("pending"))).toBe(true);
    expect(mirrorBusy(m("ok"))).toBe(false);
    expect(mirrorBusy(m("failed"))).toBe(false);
    expect(mirrorBusy(null)).toBe(false);
  });
});

describe("webhooks", () => {
  const now = Date.parse("2026-10-19T12:00:00Z");
  // created_by, actor: the spec's $ref with nullable, which the generated
  // types read as never null; the API sends null for a deleted account.
  const hook = (over: Partial<Webhook>): Webhook =>
    ({
    id: "w",
    url: "https://ci.example.com/hook",
    events: ["push"],
    active: true,
    created_at: "2026-10-19T10:00:00Z",
    created_by: null,
    secret_rotated_at: "2026-10-19T10:00:00Z",
    secret_rotated_by: null,
    last_delivery: null,
    ...over,
  }) as unknown as Webhook;
  type Delivery = NonNullable<Webhook["last_delivery"]>;
  const delivery = (over: Partial<Delivery>): Delivery =>
    ({
    id: "d",
    event: "push" as const,
    summary: "",
    state: "delivered" as const,
    attempts: 1,
    response_status: 200,
    duration_ms: 120,
    error: null,
    created_at: "2026-10-19T11:58:00Z",
    delivered_at: "2026-10-19T11:58:00Z",
    next_attempt_at: null,
    actor: null,
    ...over,
  }) as unknown as Delivery;
  it("says what it sends", () => {
    expect(eventsText(["push", "pull_request"])).toBe("Pushes and pull requests");
    expect(eventsText(["pull_request"])).toBe("Pull requests");
  });
  it("reads its last answer as the row shows it", () => {
    expect(lastAnswer(hook({ active: false }), now)).toEqual({ text: "Paused", tone: "idle" });
    expect(lastAnswer(hook({}), now)).toEqual({ text: "Nothing sent yet", tone: "idle" });
    expect(lastAnswer(hook({ last_delivery: delivery({}) }), now)).toEqual({ text: "200 · 2m ago", tone: "ok" });
    expect(lastAnswer(hook({ last_delivery: delivery({ state: "failed", response_status: 404 }) }), now)).toEqual({ text: "404 · failed 2m ago", tone: "bad" });
    expect(lastAnswer(hook({ last_delivery: delivery({ state: "pending", response_status: 503 }) }), now).tone).toBe("busy");
  });
});
