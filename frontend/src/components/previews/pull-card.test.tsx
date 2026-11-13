import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import type { PullRequestDetail, TimelineItem } from "@/lib/api/code.service";
import type { Preview } from "@/lib/api/preview.service";
import { CodeContext, type CodeCtx } from "@/components/code/repo";
import { PullPreviewCard, WaitingCard } from "./pull-card";

let role: "owner" | "viewer" = "owner";
vi.mock("@/context/AuthContext", () => ({ useAuth: () => ({ user: { id: "u1", name: "Sam" } }), useOrgRole: () => role }));

const get = vi.fn();
const approve = vi.fn();
const settings = vi.fn(async () => ({ environment_id: "e1", max_per_project: 5 }));
vi.mock("@/lib/api/preview.service", () => ({
  previewService: { of: () => ({ get, approve, start: vi.fn(), stop: vi.fn(), redeploy: vi.fn() }), settings: (...a: unknown[]) => settings(...(a as [])) },
}));

const pull = (x: Partial<PullRequestDetail> = {}) => ({ number: 47, state: "open", head_sha: "b".repeat(40), head_exists: true, ...x }) as PullRequestDetail;

const pv = (x: Partial<Preview>): Preview =>
  ({
    pull_number: 47, repository_id: "r1", repository: "shop", title: "t", branch: "feat", environment: { id: "e", name: "pr-47" }, copies: "staging",
    status: "running", untrusted: true, head_sha: "c".repeat(40), approved_sha: "a".repeat(40), approved_by: "Sam", needs_approval: false, reached: [], migration_files: [],
    migrations_unchecked: false, created_at: "2026-11-01T10:00:00Z", updated_at: "2026-11-01T10:00:00Z",
    services: [{ id: "s1", name: "web", server: "fsn1", status: "running", sha: "a".repeat(40), addresses: ["pr-47-web.example.com"], isolation: "sandboxed" }],
    ...x,
  }) as Preview;

let client: QueryClient;
const ctx = { projectId: "p1", repo: { id: "r1", name: "shop" }, base: "/projects/p1/code/shop", api: { id: "r1", commit: vi.fn(async () => null) } } as unknown as CodeCtx;

function show(ui: React.ReactNode) {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <CodeContext.Provider value={ctx}>{ui}</CodeContext.Provider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  role = "owner";
  get.mockReset();
  approve.mockReset();
});

describe("the Preview card's reads", () => {
  it("says a failed read failed, with Retry, and not that there's no preview", async () => {
    get.mockRejectedValueOnce(new ApiError("internal", 500, null));
    show(<PullPreviewCard pull={pull()} />);
    expect(await screen.findByText("Couldn’t load the preview.")).toBeTruthy();
    expect(screen.queryByText(/No preview yet/)).toBeNull();
    get.mockResolvedValueOnce(pv({}));
    fireEvent.click(screen.getByRole("button", { name: /^Retry/ }));
    expect(await screen.findByText(/Running/)).toBeTruthy();
  });

  it("says No preview yet only when there really is none", async () => {
    get.mockRejectedValue(new ApiError("no_preview", 404, null));
    show(<PullPreviewCard pull={pull()} />);
    expect(await screen.findByText(/No preview yet/)).toBeTruthy();
  });
});

describe("the chip and what the card offers", () => {
  it("a first commit waiting: needs approval, runs nothing, offers nothing to stop", async () => {
    get.mockResolvedValue(pv({ status: "needs_approval", needs_approval: true, approved_sha: undefined, environment: null, services: [] }));
    show(<PullPreviewCard pull={pull()} />);
    expect(await screen.findByText("Needs approval")).toBeTruthy();
    expect(screen.queryByText("Runs")).toBeNull();
    expect(screen.queryByRole("button", { name: "Stop" })).toBeNull();
  });

  it("shows the preview's own status while a commit waits", async () => {
    get.mockResolvedValue(pv({ status: "stopped", needs_approval: true, services: [] }));
    const stopped = show(<PullPreviewCard pull={pull()} />);
    expect(await screen.findByText("Stopped")).toBeTruthy();
    expect(screen.queryByText(/^Running/)).toBeNull();
    stopped.unmount();

    get.mockResolvedValue(pv({ status: "failed", needs_approval: true, services: [{ id: "s1", name: "web", server: "", status: "failed", sha: "a".repeat(40), deployment_id: "d1", addresses: [], isolation: "sandboxed" }] }));
    show(<PullPreviewCard pull={pull()} />);
    expect(await screen.findByText("Failed")).toBeTruthy();
    expect(screen.queryByText(/^Running/)).toBeNull();
  });

  it("a viewer keeps the running preview's address when a commit waits", async () => {
    role = "viewer";
    get.mockResolvedValue(pv({ needs_approval: true }));
    show(<PullPreviewCard pull={pull()} />);
    expect(await screen.findByRole("link", { name: /pr-47-web\.example\.com/ })).toBeTruthy();
    expect(screen.getByText(/keeps running aaaaaaa/)).toBeTruthy();
    expect(screen.queryByText(/approves the preview first/)).toBeNull();
  });

  it("a refused commit reads as refused, with the reason, and no build log", async () => {
    get.mockResolvedValue(pv({ status: "failed", approved_sha: "c".repeat(40), services: [{ id: "s1", name: "web", server: "", status: "failed", error: "Dockerfile gives RUN steps --mount", addresses: [], isolation: "sandboxed" }] }));
    show(<PullPreviewCard pull={pull()} />);
    expect(await screen.findByText("Toft refused to build web.")).toBeTruthy();
    expect(screen.getByText(/gives RUN steps --mount/)).toBeTruthy();
    expect(screen.queryByRole("link", { name: "Build log" })).toBeNull();
  });
  it("a refused second commit: names what still runs, and why the new one didn't", async () => {
    get.mockResolvedValue(
      pv({
        status: "running",
        approved_sha: "c".repeat(40),
        head_sha: "c".repeat(40),
        note: "Dockerfile gives RUN steps --mount",
        services: [{ id: "s1", name: "web", server: "", status: "running", sha: "a".repeat(40), error: "Dockerfile gives RUN steps --mount", deployment_id: "d1", addresses: ["pr-47-web.example.com"], isolation: "sandboxed" }],
      }),
    );
    show(<PullPreviewCard pull={pull()} />);
    expect(await screen.findByText("Running aaaaaaa")).toBeTruthy();
    expect(screen.queryByText("Running ccccccc")).toBeNull();
    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("Toft refused to build web from ccccccc. The previous commit keeps running.");
    expect(alert).toHaveTextContent("Dockerfile gives RUN steps --mount");
    expect(screen.getByRole("link", { name: "Logs" })).toBeTruthy();
  });
});

describe("the Removed card", () => {
  const removed = { kind: "preview_removed", created_at: "2026-11-01T10:00:00Z", data: { reason: "closed" } } as unknown as TimelineItem;

  it("promises a new preview on reopening only while the branch exists", async () => {
    get.mockRejectedValue(new ApiError("no_preview", 404, null));
    const gone = show(<PullPreviewCard pull={pull({ state: "closed", head_exists: false })} removed={removed} />);
    await screen.findAllByText(/Removed/);
    expect(screen.queryByText(/Reopening the pull request/)).toBeNull();
    gone.unmount();
    show(<PullPreviewCard pull={pull({ state: "closed", head_exists: true })} removed={removed} />);
    expect(await screen.findByText(/Reopening the pull request/)).toBeTruthy();
  });
});

describe("the waiting card", () => {
  const waiting = (x: Partial<Preview>) => pv({ status: "running", needs_approval: true, ...x });

  it("says what keeps running only when something does", async () => {
    get.mockResolvedValue(waiting({}));
    const running = show(<WaitingCard pull={pull()} items={[]} />);
    expect(await screen.findByText(/keeps running/)).toBeTruthy();
    running.unmount();

    get.mockResolvedValue(waiting({ status: "stopped", stopped_reason: "manual" }));
    const byHand = show(<WaitingCard pull={pull()} items={[]} />);
    expect(await screen.findByText(/stopped, so nothing runs/)).toBeTruthy();
    expect(screen.getByText(/use Start again/)).toBeTruthy();
    expect(screen.queryByText(/keeps running/)).toBeNull();
    byHand.unmount();

    get.mockResolvedValue(waiting({ status: "stopped", stopped_reason: "room" }));
    const forRoom = show(<WaitingCard pull={pull()} items={[]} />);
    expect(await screen.findByText(/Approving starts it again/)).toBeTruthy();
    forRoom.unmount();

    get.mockResolvedValue(waiting({ status: "failed", services: [] }));
    const failed = show(<WaitingCard pull={pull()} items={[]} />);
    expect(await screen.findByText(/last deploy failed/)).toBeTruthy();
    expect(screen.queryByText(/keeps running/)).toBeNull();
    failed.unmount();

    get.mockResolvedValue(pv({ status: "needs_approval", needs_approval: true, approved_sha: undefined, services: [] }));
    show(<WaitingCard pull={pull()} items={[]} />);
    expect(await screen.findByText(/Nothing runs until then/)).toBeTruthy();
  });

  it("rereads the pull request and the preview after a 409 on Approve", async () => {
    get.mockResolvedValue(waiting({}));
    approve.mockRejectedValue(new ApiError("that commit is no longer the head", 409, { code: "stale_sha" }));
    show(<WaitingCard pull={pull()} items={[]} />);
    const button = await screen.findByRole("button", { name: /Approve/ });
    const invalidate = vi.spyOn(client, "invalidateQueries");
    fireEvent.click(button);
    await waitFor(() => expect(invalidate).toHaveBeenCalledWith({ queryKey: ["pull", "r1", 47] }));
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["pull-preview", "r1", 47] });
  });
});
