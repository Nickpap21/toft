import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { MirrorsCard, WebhooksCard } from "./mirrors";
import { CodeContext, type CodeCtx } from "./repo";

// A list that failed to load is never "No webhooks" or "No push mirrors".

vi.mock("@/context/AuthContext", () => ({ useAuth: () => ({ user: { id: "u1" } }), useOrgRole: () => "owner" }));
vi.mock("@/components/reauth", () => ({ useReauth: () => vi.fn() }));

function show(ui: React.ReactNode, api: Record<string, unknown>) {
  const ctx = { api: { id: "r1", ...api }, base: "/projects/p1/code/shop", projectId: "p1", detail: { default_branch: "main" }, repo: { id: "r1", name: "shop" }, repos: [] } as unknown as CodeCtx;
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <CodeContext.Provider value={ctx}>{ui}</CodeContext.Provider>
    </QueryClientProvider>,
  );
}

describe("a repository's settings when a read fails", () => {
  it("Webhooks says it failed, with Retry, and that there are none only once it has read them", async () => {
    const webhooks = vi.fn<() => Promise<unknown[]>>().mockRejectedValueOnce(new Error("boom")).mockResolvedValue([]);
    show(<WebhooksCard />, { webhooks });

    expect(await screen.findByText("Couldn’t load the webhooks.")).toBeInTheDocument();
    expect(screen.queryByText(/No webhooks/)).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: /^Retry/ }));
    expect(await screen.findByText(/No webhooks/)).toBeInTheDocument();
  });

  it("Mirrors says it failed, with Retry, never that there are no push mirrors", async () => {
    show(<MirrorsCard />, { mirrors: () => Promise.reject(new Error("boom")) });

    expect(await screen.findByText("Couldn’t load the mirrors.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
    expect(screen.queryByText(/No push mirrors/)).toBeNull();
  });
});
