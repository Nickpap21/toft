import { renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";

const auth = vi.hoisted(() => ({ token: null as string | null, user: null as { orgId?: string } | null }));
vi.mock("@/context/AuthContext", () => ({ useAuth: () => auth }));

// Each stream the hook opens, by its abort signal; it stays open.
const streams = vi.hoisted(() => [] as AbortSignal[]);
const handlers = vi.hoisted(() => [] as ((type: string, data: string) => void)[]);
vi.mock("@/lib/api/client", () => ({
  streamEvents: (_path: string, onEvent: (type: string, data: string) => void, signal: AbortSignal) => {
    streams.push(signal);
    handlers.push(onEvent);
    return new Promise(() => {});
  },
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

import { useEventStream } from "./useEventStream";

describe("useEventStream", () => {
  it("opens one stream per session and workspace", () => {
    const client = new QueryClient();
    const { rerender, unmount } = renderHook(() => useEventStream(), {
      wrapper: ({ children }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>,
    });
    expect(streams).toHaveLength(0);

    // A sign-in navigates without a page load.
    auth.token = "tok";
    auth.user = { orgId: "o1" };
    rerender();
    expect(streams).toHaveLength(1);

    // The user refreshed, same workspace: the stream stays.
    auth.user = { orgId: "o1" };
    rerender();
    expect(streams).toHaveLength(1);

    // The server scoped the first stream to o1.
    auth.user = { orgId: "o2" };
    rerender();
    expect(streams).toHaveLength(2);
    expect(streams[0].aborted).toBe(true);
    expect(streams[1].aborted).toBe(false);

    unmount();
    expect(streams[1].aborted).toBe(true);
  });

  // Opens a stream for a signed-in user and returns its handler and the
  // keys invalidated so far.
  function open() {
    auth.token = "tok";
    auth.user = { orgId: "o1" };
    const client = new QueryClient();
    const spy = vi.spyOn(client, "invalidateQueries");
    renderHook(() => useEventStream(), {
      wrapper: ({ children }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>,
    });
    const handler = handlers[handlers.length - 1];
    const keys = () => spy.mock.calls.map(([f]) => JSON.stringify(f?.queryKey));
    return { handler, keys, spy };
  }

  it("refreshes the count sources on a deploy event", () => {
    const { handler, keys } = open();
    handler("deployment.running", JSON.stringify({ service_id: "s1", service_name: "web", project_id: "p1", status: "running" }));
    for (const key of [["inbox"], ["project"], ["projects"]]) expect(keys()).toContain(JSON.stringify(key));
  });

  // A "changed" event names what moved; each kind refreshes what reads it,
  // a project's keys only for the project it names.
  it.each([
    ["issues", [["project", "p1"], ["projects"], ["issues", "p1"], ["issue", "p1"], ["issue-counts", "p1"], ["inbox"]]],
    ["docs", [["project", "p1"], ["projects"], ["docs", "p1"], ["inbox"]]],
    ["alerts", [["inbox"], ["alerts-recent"]]],
    ["services", [["inbox"], ["workspace-services"], ["project-services"], ["project-overview"], ["dashboard"], ["env-services"], ["project-scoped-services"], ["service"]]],
    ["pulls", [["project", "p1"], ["projects"], ["repositories", "p1"], ["repository", "p1"], ["code-pulls"], ["code-pulls-page"]]],
  ])("a changed event of %s refreshes what reads it", (kind, expected) => {
    vi.useFakeTimers();
    const { handler, keys } = open();
    handler("changed", JSON.stringify({ kinds: [kind], project_id: "p1" }));
    vi.runAllTimers();
    expect(keys().sort()).toEqual(expected.map((k) => JSON.stringify(k)).sort());
    vi.useRealTimers();
  });

  it("gathers changed events, and refetches each key once", () => {
    vi.useFakeTimers();
    const { handler, keys, spy } = open();
    for (const n of [1, 2, 3]) handler("changed", JSON.stringify({ kinds: ["issues"], project_id: `p${n % 2}` }));
    expect(spy).not.toHaveBeenCalled();
    vi.advanceTimersByTime(250);
    expect(keys().filter((k) => k === JSON.stringify(["projects"]))).toHaveLength(1);
    expect(keys()).toContain(JSON.stringify(["issues", "p0"]));
    expect(keys()).toContain(JSON.stringify(["issues", "p1"]));
    vi.useRealTimers();
  });

  it("a changed event naming no project refreshes every project's", () => {
    vi.useFakeTimers();
    const { handler, keys } = open();
    handler("changed", JSON.stringify({ kinds: ["docs"], project_id: "p1" }));
    handler("changed", JSON.stringify({ kinds: ["docs"] }));
    vi.runAllTimers();
    expect(keys()).toContain(JSON.stringify(["docs"]));
    expect(keys()).not.toContain(JSON.stringify(["docs", "p1"]));
    expect(keys()).not.toContain(JSON.stringify(["issues"]));
    vi.useRealTimers();
  });

  it("ignores a changed event it cannot read", () => {
    vi.useFakeTimers();
    const { handler, spy } = open();
    handler("changed", "not json");
    handler("changed", JSON.stringify({ kinds: ["unheard-of"] }));
    handler("changed", "{}");
    vi.runAllTimers();
    expect(spy).not.toHaveBeenCalled();
    vi.useRealTimers();
  });

  it("refreshes the count sources after a reconnect", () => {
    const { handler, keys, spy } = open();
    handler("connected", "{}");
    expect(spy).not.toHaveBeenCalled();
    handler("connected", "{}");
    for (const key of [["inbox"], ["project"], ["projects"]]) expect(keys()).toContain(JSON.stringify(key));
  });
});
