import { useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { toast } from "sonner";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import type { CommitCheck, MergeMethod, PullRequestDetail } from "@/lib/api/code.service";
import { ReportChecksCard } from "./checks";
import { CheckoutCard, MergeBox } from "./pulls";
import { CodeContext, type CodeCtx } from "./repo";

vi.mock("@/context/AuthContext", () => ({ useAuth: () => ({ user: { id: "u1", name: "Sam" } }), useOrgRole: () => "owner" }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const sha = "a".repeat(40);
const cmp = { ahead: 1, behind: 0, mergeable: true, conflicts: [], fast_forward: true, head: { name: "feat", kind: "branch", sha }, base: { name: "main", kind: "branch", sha }, merge_base: sha, commits: [], commits_truncated: false, stats: { files: 1, additions: 1, deletions: 0 }, files: [], files_truncated: false };
const pull = (x: Partial<PullRequestDetail> = {}) =>
  ({ number: 47, title: "Fix", state: "open", author: { id: "u1", name: "Sam" }, base_branch: "main", head_branch: "feat", head_sha: sha, head_exists: true, compare: cmp, deploys: [], issues: [], docs: [], ...x }) as PullRequestDetail;

const merge = vi.fn();
const checks = vi.fn();
const onDone = vi.fn();

function Box({ p }: { p: PullRequestDetail }) {
  const [method, setMethod] = useState<MergeMethod>("squash");
  const [deleteBranch, setDeleteBranch] = useState(true);
  return <MergeBox pull={p} onDone={onDone} choice={{ method, setMethod, deleteBranch, setDeleteBranch }} />;
}

function show(p: PullRequestDetail) {
  const ctx = { api: { id: "r1", merge, checks, updatePull: vi.fn() }, base: "/projects/p1/code/shop", projectId: "p1", detail: { protect_default_branch: false, default_branch: "main" }, repo: { id: "r1", name: "shop" }, repos: [] } as unknown as CodeCtx;
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <CodeContext.Provider value={ctx}>
        <Box p={p} />
      </CodeContext.Provider>
    </QueryClientProvider>,
  );
}

const check = (x: Partial<CommitCheck>) => ({ name: "toft/deploy/production/web", state: "running", description: "", details_url: null, deployment: { id: "d1", service_id: "s1" }, reporter: null, started_at: null, completed_at: null, created_at: "2026-11-01T10:00:00Z", updated_at: "2026-11-01T10:00:00Z", ...x }) as CommitCheck;

beforeEach(() => {
  merge.mockReset();
  checks.mockReset();
  onDone.mockReset();
  vi.mocked(toast.error).mockReset();
});

describe("a refused merge", () => {
  it("rereads the pull request on every 409, not only a moved head", async () => {
    merge.mockRejectedValue(new ApiError("this pull request is merged or closed", 409, { code: "pull_closed" }));
    show(pull());
    fireEvent.click(screen.getByRole("button", { name: "Squash and merge" }));
    await waitFor(() => expect(onDone).toHaveBeenCalled());
    expect(toast.error).toHaveBeenCalledWith("this pull request is merged or closed");
  });

  it("says Couldn’t merge for a failure, not the backend's internal words", async () => {
    merge.mockRejectedValue(new ApiError("failed to read the repository", 500, null));
    show(pull());
    fireEvent.click(screen.getByRole("button", { name: "Squash and merge" }));
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("Couldn’t merge"));
    expect(onDone).not.toHaveBeenCalled();
  });
});

describe("after the merge", () => {
  const merged = pull({ state: "merged", merge_sha: "b".repeat(40), merged_at: "2020-01-01T10:00:00Z", merged_by: { id: "u1", name: "Sam" }, merge_method: "squash" });

  it("links the deployment it started", async () => {
    checks.mockResolvedValue({ checks: [check({})], summary: null, sha: "b".repeat(40) });
    show(merged);
    const link = await screen.findByRole("link", { name: "Deploy web" });
    expect(link.getAttribute("href")).toBe("/projects/p1/services/s1?tab=deployments&deployment=d1");
    expect(checks).toHaveBeenCalledWith("b".repeat(40));
  });

  it("links each service's Deploys when there are several", async () => {
    checks.mockResolvedValue({
      checks: [check({}), check({ name: "toft/deploy/production/api", deployment: { id: "d2", service_id: "s2" } }), check({ name: "ci/test", deployment: null })],
      summary: null,
      sha: "b".repeat(40),
    });
    show(merged);
    expect((await screen.findByRole("link", { name: "Deploy api" })).getAttribute("href")).toBe("/projects/p1/services/s2?tab=deployments");
    expect(screen.getByRole("link", { name: "Deploy web" }).getAttribute("href")).toBe("/projects/p1/services/s1?tab=deployments");
    expect(screen.queryByRole("link", { name: "ci/test" })).toBeNull();
  });
});

describe("other states of the card", () => {
  it("says to push a deleted branch again to reopen it", () => {
    show(pull({ state: "closed", head_exists: false }));
    expect(screen.getByText(/Push it again to reopen this/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Reopen pull request" })).toBeNull();
  });

  it("links a failing check's line to the Checks tab", () => {
    show(pull({ head_checks: { sha, summary: null, checks: [check({ name: "ci/lint", state: "failure", deployment: null })] } as unknown as PullRequestDetail["head_checks"] }));
    expect(screen.getByRole("link", { name: /ci\/lint failed/ }).getAttribute("href")).toBe("/projects/p1/code/shop/pulls/47?tab=checks");
  });

  it("makes the conflict commands reachable by keyboard", () => {
    show(pull({ compare: { ...cmp, mergeable: false, conflicts: ["a.txt"] } as PullRequestDetail["compare"] }));
    const pre = screen.getByRole("group", { name: /Commands to bring main/ });
    expect(pre.getAttribute("tabindex")).toBe("0");
  });

  it("makes the check-out commands reachable by keyboard", () => {
    render(<CheckoutCard pull={pull()} />);
    const pre = screen.getByRole("group", { name: /Commands to check out feat/ });
    expect(pre.getAttribute("tabindex")).toBe("0");
  });

  it("makes the Report checks command reachable by keyboard", () => {
    render(<ReportChecksCard />);
    const pre = screen.getByRole("group", { name: "Command to report a check" });
    expect(pre.getAttribute("tabindex")).toBe("0");
  });
});
