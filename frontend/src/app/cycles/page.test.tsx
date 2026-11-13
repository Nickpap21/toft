import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import CyclesPage from "./page";

const replace = vi.fn();
vi.mock("next/navigation", () => ({ useRouter: () => ({ replace }) }));
afterEach(() => vi.unstubAllGlobals());

function stubAPI(answer: (state: string | null) => number[] | null) {
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof globalThis.fetch>(async (input) => {
      const data = answer(new URL(String(input), location.origin).searchParams.get("state"));
      if (!data) return new Response(JSON.stringify({ error: "boom" }), { status: 500 });
      return new Response(JSON.stringify({ data: data.map((number) => ({ number })), next: null }), { status: 200 });
    }),
  );
}
function mount() {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <CyclesPage />
    </QueryClientProvider>,
  );
}

describe("/cycles when a read fails", () => {
  // I5: no skeleton that never ends.
  it("says the current cycle failed to load, with Retry", async () => {
    let failing = true;
    stubAPI(() => (failing ? null : [4]));
    mount();

    expect(await screen.findByRole("heading", { name: "Couldn’t load this page", level: 1 })).toBeInTheDocument();
    expect(screen.queryByLabelText("Loading")).toBeNull();
    expect(screen.queryByText(/no cycles yet/)).toBeNull();

    failing = false;
    await userEvent.click(screen.getByRole("button", { name: /^Retry/ }));
    await vi.waitFor(() => expect(replace).toHaveBeenCalledWith("/cycles/4"));
  });

  it("says so too when the latest cycles failed, after no current one", async () => {
    stubAPI((state) => (state === "current" ? [] : null));
    mount();

    expect(await screen.findByRole("heading", { name: "Couldn’t load this page", level: 1 })).toBeInTheDocument();
    expect(screen.queryByText(/no cycles yet/)).toBeNull();
  });

  it("a failed refetch on a workspace with no cycles keeps the answer", async () => {
    let failing = false;
    stubAPI(() => (failing ? null : []));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}><CyclesPage /></QueryClientProvider>);
    expect(await screen.findByText(/The workspace has no cycles yet/)).toBeInTheDocument();
    failing = true;
    await client.refetchQueries({ queryKey: ["cycles", "current"] });
    await vi.waitFor(() => expect(client.getQueryState(["cycles", "current"])?.status).toBe("error"));
    expect(screen.getByText(/The workspace has no cycles yet/)).toBeInTheDocument();
    expect(screen.queryByLabelText("Loading")).toBeNull();
  });
});
