import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { CodeContext, type CodeCtx } from "@/components/code/repo";
import PipelinesPage from "./page";

// A failed poll leaves the list on screen; a first read that failed says so
// with Retry; a page that won't load says so under the list.

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn() }), useSearchParams: () => new URLSearchParams("") }));
vi.mock("@/components/code/checks", async (original) => ({ ...(await original<typeof import("@/components/code/checks")>()), ReportChecksCard: () => null }));

const row = (id: string) => ({ id, kind: "push", before: "a".repeat(40), after: "b".repeat(40), subject: `push ${id}`, branch: "main", commits: 1, forced: false, dispatched_at: "2026-11-09T09:00:00Z", created_at: "2026-11-09T09:00:00Z", checks: { checks: [] }, deploys: [] });

function show(pipelines: () => Promise<unknown>) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const ctx = { api: { id: "r1", pipelines, branches: vi.fn(async () => ({ data: [] })) }, base: "/projects/p1/code/shop", repo: { id: "r1", name: "shop" }, repos: [], detail: { empty: false, deploys: [] } } as unknown as CodeCtx;
  render(
    <QueryClientProvider client={client}>
      <CodeContext.Provider value={ctx}>
        <PipelinesPage />
      </CodeContext.Provider>
    </QueryClientProvider>,
  );
  return client;
}

describe("Pipelines", () => {
  it("keeps the list when a poll fails", async () => {
    let answer: () => Promise<unknown> = async () => ({ data: [row("1")], next: null });
    const client = show(() => answer());
    await screen.findByText("push 1");
    answer = () => Promise.reject(new Error("boom"));
    await client.refetchQueries({ type: "active" });
    await waitFor(() => expect(client.getQueryCache().getAll().some((q) => q.state.status === "error")).toBe(true));
    expect(screen.getByText("push 1")).toBeInTheDocument();
    expect(screen.queryByText(/Couldn’t/)).toBeNull();
  });

  it("says a first read failed, with Retry that brings the list", async () => {
    let answer: () => Promise<unknown> = () => Promise.reject(new Error("boom"));
    show(() => answer());
    expect(await screen.findByText("Couldn’t load the pipelines.")).toBeInTheDocument();
    answer = async () => ({ data: [row("1")], next: null });
    fireEvent.click(screen.getByRole("button", { name: /^Retry/ }));
    expect(await screen.findByText("push 1")).toBeInTheDocument();
  });

  it("says an older page failed, keeping the rows", async () => {
    let page = 0;
    show((_b?: unknown, cursor?: unknown) => {
      if (!cursor) return Promise.resolve({ data: [row("1")], next: "n2" });
      page++;
      return page === 1 ? Promise.reject(new Error("boom")) : Promise.resolve({ data: [row("2")], next: null });
    });
    fireEvent.click(await screen.findByRole("button", { name: "Older" }));
    expect(await screen.findByText("Couldn’t load older pipelines.")).toBeInTheDocument();
    expect(screen.getByText("push 1")).toBeInTheDocument();
  });
});
