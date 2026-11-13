import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import AddServerPage from "./page";

vi.mock("@/context/AuthContext", () => ({ useOrgRole: () => "owner" }));
// router.replace doesn't land by itself: the search params change only when
// a test says so, as a slow navigation would.
let search = new URLSearchParams();
const replace = vi.fn();
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace, push: vi.fn() }),
  useSearchParams: () => search,
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const createAgent = vi.fn();
const joinToken = vi.fn();
vi.mock("@/lib/api/server.service", () => ({
  serverService: {
    createAgent: () => createAgent(),
    joinToken: (id: string) => joinToken(id),
    get: async () => waiting,
    lastProvision: async () => null,
  },
}));

const waiting = { id: "s-1", name: "server-2", connection: "agent", agent: null, provisioned_at: null };
const join = { command: "curl -fsSL https://paas.example/install | sh", expires_at: new Date(Date.now() + 3_600_000).toISOString() };

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const tree = () => (
    <QueryClientProvider client={client}>
      <AddServerPage />
    </QueryClientProvider>
  );
  const r = render(tree());
  return () => r.rerender(tree());
}

describe("Add a server", () => {
  beforeEach(() => {
    search = new URLSearchParams();
    replace.mockReset();
    // Answers a moment later, as the network does.
    createAgent.mockReset().mockImplementation(() => new Promise((r) => setTimeout(() => r({ ...waiting, join }), 5)));
    joinToken.mockReset().mockResolvedValue(join);
  });

  it("makes one command per visit while the URL catches up", async () => {
    const rerender = renderPage();
    await act(() => new Promise((r) => setTimeout(r, 100)));
    expect(createAgent).toHaveBeenCalledTimes(1);
    expect(replace).toHaveBeenCalledWith("/servers/new?server=s-1");

    // The URL lands: the page keeps the command it got.
    search = new URLSearchParams("server=s-1");
    rerender();
    expect(await screen.findByText(join.command)).toBeTruthy();
    expect(createAgent).toHaveBeenCalledTimes(1);
    expect(joinToken).not.toHaveBeenCalled();

    // Back to /servers/new (Verify's Back): a new one, once.
    search = new URLSearchParams();
    rerender();
    await act(() => new Promise((r) => setTimeout(r, 100)));
    expect(createAgent).toHaveBeenCalledTimes(2);
  });

  it("says that a reload's new command ends the earlier one", async () => {
    search = new URLSearchParams("server=s-1");
    renderPage();
    expect(await screen.findByText(join.command)).toBeTruthy();
    expect(joinToken).toHaveBeenCalledTimes(1);
    expect(screen.getByText("Opening this page makes a new command. An earlier one stops working.")).toBeTruthy();
    // Nothing was copied here: the command may be running already.
    expect(screen.getByText("Waiting for the server to connect. This page moves on by itself.")).toBeTruthy();
  });
});
