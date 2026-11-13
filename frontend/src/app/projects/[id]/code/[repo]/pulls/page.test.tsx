import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { PullRequest } from "@/lib/api/code.service";
import { CodeContext, type CodeCtx } from "@/components/code/repo";
import PullsPage from "./page";

vi.mock("next/navigation", () => ({ useRouter: () => ({ replace: vi.fn() }), useSearchParams: () => new URLSearchParams("") }));
vi.mock("@/context/AuthContext", () => ({ useAuth: () => ({ user: { id: "u1", name: "Sam" } }), useOrgRole: () => "owner" }));
vi.mock("@/components/code/parts", async (original) => ({ ...(await original<typeof import("@/components/code/parts")>()), RepoPicker: () => null }));

const row = (x: Partial<PullRequest>) =>
  ({ number: 47, title: "Fix", state: "open", author: { id: "u1", name: "sam" }, base_branch: "main", head_branch: "feat", head_sha: "a".repeat(40), comments: 0, ahead: 1, behind: 0, conflicts: false, created_at: "2026-11-01T10:00:00Z", updated_at: "2026-11-01T10:00:00Z", ...x }) as PullRequest;

function show(pulls: PullRequest[]) {
  const ctx = { api: { id: "r1", pulls: vi.fn(async () => ({ data: pulls, next: null })) }, base: "/projects/p1/code/shop", repo: { id: "r1", name: "shop" }, repos: [], detail: { pulls: { open: 1, merged: 0, closed: 0 }, empty: false, default_branch: "main" } } as unknown as CodeCtx;
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <CodeContext.Provider value={ctx}>
        <PullsPage />
      </CodeContext.Provider>
    </QueryClientProvider>,
  );
}

describe("the pull request list", () => {
  it("names an agent and who it's for", async () => {
    show([row({ author: { id: "u1", name: "sam", agent: "Claude Code" } as PullRequest["author"] })]);
    expect(await screen.findByText("Claude Code, for sam")).toBeTruthy();
  });

  it("names a person as before", async () => {
    show([row({})]);
    expect(await screen.findByText("sam")).toBeTruthy();
  });
});
