import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { PullRequestDetail } from "@/lib/api/code.service";
import { MergeBox } from "./pulls";
import { CodeContext, type CodeCtx } from "./repo";

vi.mock("@/context/AuthContext", () => ({ useAuth: () => ({ user: { id: "u1", name: "Sam" } }), useOrgRole: () => "owner" }));

const sha = "a".repeat(40);
const pull = (x: Partial<PullRequestDetail>) =>
  ({
    number: 47,
    title: "Promote",
    state: "open",
    author: { id: "u1", name: "Sam" },
    base_branch: "release",
    head_branch: "feat",
    head_sha: sha,
    head_exists: true,
    head_kept: false,
    compare: { ahead: 1, behind: 0, mergeable: true, conflicts: [], fast_forward: true, head: { name: "feat", kind: "branch", sha }, base: { name: "release", kind: "branch", sha }, merge_base: sha, commits: [], commits_truncated: false, stats: { files: 1, additions: 1, deletions: 0 }, files: [], files_truncated: false },
    deploys: [],
    issues: [],
    docs: [],
    ...x,
  }) as PullRequestDetail;

function box(p: PullRequestDetail) {
  const merge = vi.fn(async () => p);
  const ctx = { api: { id: "r1", merge }, base: "/projects/p1/code/shop", detail: { protect_default_branch: false, default_branch: "main" }, projectId: "p1", repo: { id: "r1", name: "shop" }, repos: [] } as unknown as CodeCtx;
  render(
    <QueryClientProvider client={new QueryClient()}>
      <CodeContext.Provider value={ctx}>
        <MergeBox pull={p} onDone={() => {}} choice={{ method: "squash", setMethod: () => {}, deleteBranch: true, setDeleteBranch: () => {} }} />
      </CodeContext.Provider>
    </QueryClientProvider>,
  );
  return merge;
}

describe("Delete branch in the Merge card", () => {
  it("offers it for an ordinary head and sends it", async () => {
    const merge = box(pull({}));
    expect(screen.getByRole("checkbox", { name: "Delete branch after merge" })).toBeTruthy();
    expect(screen.getByText(/its commits stay in !47/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Squash and merge" }));
    await waitFor(() => expect(merge).toHaveBeenCalledWith(47, expect.objectContaining({ delete_branch: true })));
  });

  it("isn't offered for a head that stays, and the merge sends false", async () => {
    const merge = box(pull({ head_branch: "main", head_kept: true }));
    expect(screen.queryByRole("checkbox", { name: "Delete branch after merge" })).toBeNull();
    expect(screen.queryByText(/its commits stay/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Squash and merge" }));
    await waitFor(() => expect(merge).toHaveBeenCalledWith(47, expect.objectContaining({ delete_branch: false })));
  });
});
