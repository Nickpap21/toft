import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { CodeContext, type CodeCtx } from "@/components/code/repo";
import CommitsPage from "./page";

// A failed refetch leaves the commits on screen.

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn() }), useSearchParams: () => new URLSearchParams("") }));
vi.mock("@/components/code/parts", async (original) => ({ ...(await original<typeof import("@/components/code/parts")>()), RefPicker: () => null }));

const commit = { sha: "c".repeat(40), subject: "Fix the build", author_name: "Sam", author_email: "s@x.io", committed_at: "2026-11-09T09:00:00Z", checks: null };

function show(commits: () => Promise<unknown>) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const ctx = { api: { id: "r1", commits }, base: "/projects/p1/code/shop", repo: { id: "r1", name: "shop" }, repos: [], detail: { empty: false, default_branch: "main" } } as unknown as CodeCtx;
  render(
    <QueryClientProvider client={client}>
      <CodeContext.Provider value={ctx}>
        <CommitsPage />
      </CodeContext.Provider>
    </QueryClientProvider>,
  );
  return client;
}

describe("Commits", () => {
  it("keeps the commits when a refetch fails", async () => {
    let answer: () => Promise<unknown> = async () => ({ data: [commit], next: null });
    const client = show(() => answer());
    await screen.findByText("Fix the build");
    answer = () => Promise.reject(new Error("boom"));
    await client.refetchQueries({ type: "active" });
    await waitFor(() => expect(client.getQueryCache().getAll().some((q) => q.state.status === "error")).toBe(true));
    expect(screen.getByText("Fix the build")).toBeInTheDocument();
    expect(screen.queryByText("Couldn’t read the commits")).toBeNull();
  });
});
