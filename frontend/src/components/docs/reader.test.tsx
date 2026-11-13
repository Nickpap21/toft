import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, waitFor } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { DocReader } from "./reader";

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }));
vi.mock("@/components/issues/issue-detail", () => ({ IssueMarkdown: () => null, useCanWriteIssues: () => false }));
vi.mock("@/lib/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/client")>()),
  fetchAPI: async (path: string) => {
    if (path.includes("/docs/"))
      return { id: "a1", project_id: "p1", title: "Restore the database", html: "", path: [], blocks: [], services: [], referenced_by: [], stale: false, version: 1, verified_at: null, verified_by: null, created_at: "2026-11-01T00:00:00Z", updated_at: "2026-11-01T00:00:00Z", created_by: { name: "kim" }, updated_by: { name: "kim" }, deleted_at: null };
    return { id: "p1", name: "shop" };
  },
}));

// WCAG 2.4.2: several open pages stay apart in the browser's tabs.
it("titles the browser tab with the page", async () => {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <DocReader projectId="p1" pageId="a1" />
    </QueryClientProvider>,
  );
  await waitFor(() => expect(document.title).toBe("Restore the database · Toft"));
});
