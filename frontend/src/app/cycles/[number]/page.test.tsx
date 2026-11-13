import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import CyclePage from "./page";

vi.mock("next/navigation", () => ({ useParams: () => ({ number: "7" }), useRouter: () => ({ push: vi.fn(), replace: vi.fn() }) }));
vi.mock("@/context/AuthContext", () => ({ useAuth: () => ({ user: { id: "u1" } }), useOrgRole: () => "owner" }));
vi.mock("@/components/issues/add-to-cycle", () => ({ AddToCycleDialog: () => null }));

const days = Array.from({ length: 14 }, (_, i) => ({ day: `2026-11-${String(i + 1).padStart(2, "0")}`, scope: 18, started: 4, shipped: 2, scope_points: 0, started_points: 0, shipped_points: 0 }));
const cycle = {
  number: 7, name: "", state: "ended", starts_on: "2026-11-01", ends_on: "2026-11-14", days_left: 0, previous: null, next: null, added: 0, carried_in: 0,
  counts: { scope: 18, started: 4, shipped: 2, carried: 0, cancelled: 0, points: 0, shipped_points: 0 },
  days, projects: [{ id: "p1", name: "storefront", total: 18, shipped: 2 }], assignees: [],
};
const issue = { key: "STO-1", number: 1, title: "Search ranks", status: "todo", priority: "medium", project_id: "p1", project: "storefront", assignee: null, labels: [], type: { id: "t", name: "Task", icon: "check", color: "blue", kind: "standard" }, due_on: null, estimate: null, carried_from: null, carried_to: null };

function stubAPI(fail: (path: string) => boolean) {
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof globalThis.fetch>(async (input) => {
      const path = new URL(String(input), location.origin).pathname;
      if (fail(path)) return new Response(JSON.stringify({ error: "boom" }), { status: 500 });
      const ok = (data: unknown) => new Response(JSON.stringify({ data, next: null }), { status: 200 });
      if (path.endsWith("/cycles/7")) return ok(cycle);
      if (path.endsWith("/cycles/7/issues")) return ok([issue]);
      if (path.endsWith("/issue-settings")) return ok({ estimates: "off", cycles: { enabled: true, weeks: 2, start_day: 1, time_zone: "UTC" } });
      return ok([]);
    }),
  );
}
function mount() {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <CyclePage />
    </QueryClientProvider>,
  );
}
afterEach(() => vi.unstubAllGlobals());

describe("a cycle", () => {
  // I6: a failure must not read as an empty cycle.
  it("says its issues failed to load, with Retry, and counts — rather than 0", async () => {
    let failing = true;
    stubAPI((p) => failing && p.endsWith("/issues"));
    mount();

    expect(await screen.findByText("Couldn’t load this cycle’s issues.")).toBeInTheDocument();
    const tabs = within(screen.getByRole("navigation", { name: "Project" }));
    expect(tabs.getByRole("button", { name: /All projects/ })).toHaveTextContent("—");
    expect(tabs.getByRole("button", { name: /storefront/ })).toHaveTextContent("—");
    expect(tabs.getByRole("button", { name: /All projects/ })).not.toHaveTextContent("0");
    expect(screen.queryByText(/Nothing was planned/)).toBeNull();

    failing = false;
    await userEvent.click(screen.getByRole("button", { name: /^Retry/ }));
    expect(await screen.findByText("Search ranks")).toBeInTheDocument();
    expect(tabs.getByRole("button", { name: /All projects/ })).toHaveTextContent("1");
  });

  it("doesn’t say the workspace has no cycles when its settings failed to load", async () => {
    stubAPI((p) => p.endsWith("/issue-settings"));
    mount();

    await screen.findByText("Search ranks");
    expect(screen.queryByText(/doesn't use cycles now/)).toBeNull();
  });

  it("says the cycle list failed to load, with Retry", async () => {
    stubAPI((p) => p.endsWith("/workspace/cycles"));
    mount();

    expect(await screen.findByText("Couldn’t load the cycles.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
  });

  // I15: 11 px labels stay 11 px on a phone.
  it("draws the chart at its real width on a phone, with fewer date labels", async () => {
    let report: ResizeObserverCallback = () => {};
    vi.stubGlobal(
      "ResizeObserver",
      class {
        constructor(cb: ResizeObserverCallback) {
          report = cb;
        }
        observe() {
          report([{ contentRect: { width: 324 } } as ResizeObserverEntry], this as unknown as ResizeObserver);
        }
        unobserve() {}
        disconnect() {}
      },
    );
    stubAPI(() => false);
    mount();

    const chart = await screen.findByRole("img", { name: /18 issues in scope/ });
    await vi.waitFor(() => expect(chart.getAttribute("viewBox")).toBe("0 0 324 206"));
    const dates = [...chart.querySelectorAll("text")].filter((t) => /^[A-Z][a-z]{2} \d+$/.test(t.textContent ?? ""));
    expect(dates.length).toBeLessThan(8);
    expect(dates.length).toBeGreaterThan(1);
  });
});
