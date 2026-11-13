import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import type { CommitCheck, PullRequestDetail } from "@/lib/api/code.service";
import { CodeContext, type CodeCtx } from "@/components/code/repo";
import PullPage from "./page";

let tab = "";
vi.mock("next/navigation", () => ({ useParams: () => ({ number: "47", id: "p1" }), useSearchParams: () => new URLSearchParams(tab), useRouter: () => ({ push: vi.fn() }) }));
vi.mock("@/context/AuthContext", () => ({ useAuth: () => ({ user: { id: "u1", name: "Sam" } }), useOrgRole: () => "owner" }));
vi.mock("@/lib/api/org.service", () => ({ orgService: { listMembers: vi.fn(async () => []) } }));

const previewGet = vi.fn();
vi.mock("@/lib/api/preview.service", () => ({
  previewService: { of: () => ({ get: previewGet, start: vi.fn(), stop: vi.fn(), redeploy: vi.fn(), approve: vi.fn() }), settings: vi.fn(async () => ({ environment_id: "e1", max_per_project: 5 })) },
}));

const sha = "a".repeat(40);
const cmp = { ahead: 1, behind: 0, mergeable: true, conflicts: [], fast_forward: true, head: { name: "feat", kind: "branch", sha }, base: { name: "main", kind: "branch", sha }, merge_base: sha, commits: [], commits_truncated: false, stats: { files: 1, additions: 1, deletions: 0 }, files: [], files_truncated: false };
const detail = (x: Partial<PullRequestDetail> = {}) =>
  ({ number: 47, title: "Fix", body: "", html: "", state: "open", author: { id: "u1", name: "Sam" }, base_branch: "main", head_branch: "feat", head_sha: sha, head_exists: true, compare: cmp, deploys: [], issues: [], docs: [], comments: 0, ahead: 1, behind: 0, conflicts: false, created_at: "2026-11-01T10:00:00Z", updated_at: "2026-11-01T10:00:00Z", ...x }) as unknown as PullRequestDetail;

const pullRead = vi.fn();
const timeline = vi.fn();
const pullDiff = vi.fn();
const merge = vi.fn();
const updatePull = vi.fn();

const ctx = { projectId: "p1", base: "/projects/p1/code/shop", repo: { id: "r1", name: "shop" }, repos: [], detail: { protect_default_branch: false, default_branch: "main" }, api: { id: "r1", pull: pullRead, timeline, pullDiff, merge, checks: vi.fn(async () => ({ checks: [], summary: null })), comment: vi.fn(), updatePull, commit: vi.fn(async () => null) } } as unknown as CodeCtx;

let client: QueryClient;
function tree() {
  return (
    <QueryClientProvider client={client}>
      <CodeContext.Provider value={ctx}>
        <PullPage />
      </CodeContext.Provider>
    </QueryClientProvider>
  );
}

beforeEach(() => {
  tab = "";
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  for (const f of [pullRead, timeline, pullDiff, merge, previewGet, updatePull]) f.mockReset();
  pullRead.mockResolvedValue(detail());
  timeline.mockResolvedValue({ data: [], next: null });
  pullDiff.mockResolvedValue({ data: [], next: null });
  previewGet.mockRejectedValue(new ApiError("none", 404, null));
});

describe("across the tabs", () => {
  it("keeps the comment being written and the merge's Delete branch choice", async () => {
    const view = render(tree());
    const box = (await screen.findByRole("textbox", { name: "Add a comment" })) as HTMLTextAreaElement;
    fireEvent.change(box, { target: { value: "Looks right, one question" } });
    fireEvent.click(screen.getByRole("checkbox", { name: "Delete branch after merge" }));

    tab = "tab=files";
    view.rerender(tree());
    await screen.findByText(/Changes from/);
    expect(screen.queryByRole("textbox", { name: "Add a comment" })).toBeNull();

    tab = "";
    view.rerender(tree());
    expect(((await screen.findByRole("textbox", { name: "Add a comment" })) as HTMLTextAreaElement).value).toBe("Looks right, one question");
    expect((screen.getByRole("checkbox", { name: "Delete branch after merge" }) as HTMLInputElement).checked).toBe(false);
  });
});

describe("the Edit buttons", () => {
  const edits = () => screen.getAllByRole("button", { name: "Edit" });

  it("gives focus back to the title's Edit on Cancel", async () => {
    const user = userEvent.setup();
    render(tree());
    await screen.findByRole("heading", { level: 1 });
    await user.click(edits()[0]);
    expect(screen.getByRole("textbox", { name: "Title" })).toHaveFocus();
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(edits()[0]).toHaveFocus();
  });

  it("gives focus back to the description's Edit on Cancel", async () => {
    const user = userEvent.setup();
    render(tree());
    await screen.findByRole("heading", { level: 1 });
    await user.click(edits()[1]);
    expect(screen.getByRole("textbox", { name: "Description" })).toHaveFocus();
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(edits()[1]).toHaveFocus();
  });

  it("gives focus back to Edit after a save closes the form", async () => {
    updatePull.mockResolvedValue(detail());
    const user = userEvent.setup();
    render(tree());
    await screen.findByRole("heading", { level: 1 });
    await user.click(edits()[0]);
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(edits()[0]).toHaveFocus());
  });

  it.each([
    ["title", 0],
    ["description", 1],
  ])("leaves focus where it went when the %s's save ends after the person moved on", async (_, i) => {
    let finish = () => {};
    updatePull.mockReturnValue(new Promise((resolve) => (finish = () => resolve(detail()))));
    const user = userEvent.setup();
    render(tree());
    await screen.findByRole("heading", { level: 1 });
    await user.click(edits()[i]);
    await user.click(screen.getByRole("button", { name: "Save" }));
    const box = screen.getByRole("textbox", { name: "Add a comment" });
    box.focus();
    finish();
    await waitFor(() => expect(edits()).toHaveLength(2));
    expect(box).toHaveFocus();
  });

  it("leaves focus alone until a form has been opened", async () => {
    render(tree());
    await screen.findByRole("heading", { level: 1 });
    await screen.findByRole("textbox", { name: "Add a comment" });
    expect(document.activeElement).toBe(document.body);
  });
});

describe("a read that fails", () => {
  it("says so on the Files tab, with Retry, rather than showing no changes", async () => {
    pullDiff.mockRejectedValueOnce(new ApiError("the git server is busy", 503, { code: "git_busy" }));
    tab = "tab=files";
    render(tree());
    expect(await screen.findByText("Couldn’t load the changes.")).toBeTruthy();
    pullDiff.mockResolvedValueOnce({ data: [{ path: "a.txt", status: "modified", additions: 1, deletions: 0, patch: "", binary: false }], next: null });
    fireEvent.click(screen.getByRole("button", { name: /^Retry/ }));
    await waitFor(() => expect(screen.queryByText("Couldn’t load the changes.")).toBeNull());
    expect(await screen.findByText("a.txt")).toBeTruthy();
  });

  it("says so in the conversation, with Retry, rather than dropping it", async () => {
    timeline.mockRejectedValueOnce(new ApiError("internal", 500, null));
    render(tree());
    expect(await screen.findByText("Couldn’t load the conversation.")).toBeTruthy();
    timeline.mockResolvedValueOnce({ data: [{ type: "comment", id: "c1", body: "hi", html: "<p>hi</p>", actor: { id: "u1", name: "Sam" }, created_at: "2026-11-01T10:00:00Z" }], next: null });
    fireEvent.click(screen.getByRole("button", { name: /^Retry/ }));
    await waitFor(() => expect(screen.queryByText("Couldn’t load the conversation.")).toBeNull());
  });
});

describe("the page after a change", () => {
  it("rereads the preview when it merges", async () => {
    merge.mockResolvedValue({});
    render(tree());
    const button = await screen.findByRole("button", { name: "Squash and merge" });
    await waitFor(() => expect(previewGet).toHaveBeenCalledTimes(1));
    fireEvent.click(button);
    await waitFor(() => expect(previewGet.mock.calls.length).toBeGreaterThan(1));
  });
});

describe("the Checks card", () => {
  const check = (x: Partial<CommitCheck>) => ({ name: "ci/lint", state: "failure", description: "2 errors", details_url: "https://ci.example.com/1", deployment: null, reporter: null, started_at: null, completed_at: null, created_at: "2026-11-01T10:00:00Z", updated_at: "2026-11-01T10:00:00Z", ...x }) as CommitCheck;

  it("shows the way to a failing check as text, not only on hover", async () => {
    pullRead.mockResolvedValue(detail({ head_checks: { sha, summary: null, checks: [check({}), check({ name: "toft/deploy/production/web", state: "running", details_url: null, deployment: { id: "d1", service_id: "s1" } })] } as unknown as PullRequestDetail["head_checks"] }));
    render(tree());
    const details = await screen.findByRole("link", { name: "Details ↗" });
    expect(details.getAttribute("href")).toBe("https://ci.example.com/1");
    expect(screen.getByRole("link", { name: "Follow" }).getAttribute("href")).toBe("/projects/p1/services/s1?tab=deployments&deployment=d1");
  });
});
