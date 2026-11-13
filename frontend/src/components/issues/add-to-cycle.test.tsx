import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Cycle } from "@/lib/api/issue.service";
import { AddToCycleDialog } from "./add-to-cycle";

const cycle = { number: 7, name: "", state: "current", starts_on: "2026-11-01", ends_on: "2026-11-14", counts: { scope: 0, points: 0 } } as unknown as Cycle;

function stubAPI(fail: () => boolean) {
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof globalThis.fetch>(async (input) => {
      const path = new URL(String(input), location.origin).pathname;
      if (path.endsWith("/p1/issues") && fail()) return new Response(JSON.stringify({ error: "boom" }), { status: 500 });
      const ok = (data: unknown) => new Response(JSON.stringify({ data, next: null }), { status: 200 });
      if (path.endsWith("/projects")) return ok([{ id: "p1", name: "storefront", issue_key: "STO" }]);
      if (path.endsWith("/issue-settings")) return ok({ estimates: "off" });
      return ok([]);
    }),
  );
}
afterEach(() => vi.unstubAllGlobals());

describe("Add issues to a cycle", () => {
  it("says a project’s issues failed to load, with Retry, never that none match", async () => {
    let failing = true;
    stubAPI(() => failing);
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <AddToCycleDialog cycle={cycle} open onOpenChange={() => {}} onAdded={() => {}} />
      </QueryClientProvider>,
    );

    expect(await screen.findByText("Couldn’t load one project’s open issues.")).toBeInTheDocument();
    expect(screen.queryByText("No open issues match.")).toBeNull();

    failing = false;
    await userEvent.click(screen.getByRole("button", { name: /^Retry/ }));
    expect(await screen.findByText("No open issues match.")).toBeInTheDocument();
    expect(screen.queryByText("Couldn’t load one project’s open issues.")).toBeNull();
  });
});
