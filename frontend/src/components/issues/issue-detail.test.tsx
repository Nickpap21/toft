import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { IssueRead } from "@/lib/api/issue.service";
import { IssueBody, IssuePanel } from "./issue-detail";

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn() }) }));
vi.mock("@/context/AuthContext", () => ({ useAuth: () => ({ user: { id: "u1" } }), useOrgRole: () => "owner" }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const issue = {
  key: "STO-7", number: 7, title: "Webhook retries", status: "in_review", priority: "medium", description: "", html: "", version: "v1",
  type: { id: "task", name: "Task", icon: "check", color: "blue", kind: "standard" },
  assignee: null, labels: [], parent: null, children: [], relations: [], docs: [], blocked_by: [], cycle: null, cycles: [], carried_from: null, progress: null,
  start_on: null, due_on: null, estimate: null, created_by: null, created_at: "2026-11-01T10:00:00Z", updated_at: "2026-11-01T10:00:00Z", status_changed_at: "2026-11-01T10:00:00Z",
  links: [{ kind: "pull_request", state: "merged", repository: "shop", number: 3, title: "Retry", head_branch: "b", merge_sha: "abcdef1", deploys: [] }],
} as unknown as IssueRead;
const comment = { id: "c1", type: "comment", kind: "comment", actor: { id: "u1", name: "Nikos" }, body: "hi", html: "<p>hi</p>", created_at: "2026-11-01T10:00:00Z" };

let calls: { method: string; path: string }[] = [];
function stubAPI(fail: (path: string) => boolean = () => false, timeline: unknown[] = []) {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof globalThis.fetch>(async (input, init) => {
      const path = new URL(String(input), location.origin).pathname;
      calls.push({ method: init?.method ?? "GET", path });
      if (fail(path)) return new Response(JSON.stringify({ error: "boom" }), { status: 500 });
      const ok = (data: unknown) => new Response(JSON.stringify({ data, next: null }), { status: 200 });
      if (path.endsWith("/timeline")) return ok(timeline);
      if (path.endsWith("/issues/7")) return ok(issue);
      if (path.endsWith("/cycles")) return ok({ enabled: false, estimates: "off", cycles: [] });
      return ok([]);
    }),
  );
}
function mount(ui: React.ReactNode) {
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{ui}</QueryClientProvider>);
}
afterEach(() => vi.unstubAllGlobals());

describe("the issue's History", () => {
  // I7: a failed read isn't an issue with no history.
  it("says it failed, with Retry, and not only what ships next", async () => {
    let failing = true;
    stubAPI((p) => failing && p.endsWith("/timeline"));
    mount(<IssueBody issue={issue} projectId="p1" project="storefront" page />);

    expect(await screen.findByText("Couldn’t load the history.")).toBeInTheDocument();
    expect(screen.queryByText(/once the deploy is live/)).toBeNull();

    failing = false;
    await userEvent.click(screen.getByRole("button", { name: /^Retry/ }));
    expect(await screen.findByText(/once the deploy is live/)).toBeInTheDocument();
    expect(screen.queryByText("Couldn’t load the history.")).toBeNull();
  });

  // I20: a delete asks first.
  it("asks before deleting a comment", async () => {
    stubAPI(() => false, [comment]);
    mount(<IssueBody issue={issue} projectId="p1" project="storefront" page />);
    const deleted = () => calls.filter((c) => c.method === "DELETE");

    await userEvent.click(await screen.findByRole("button", { name: "Delete the comment…" }));
    let dialog = await screen.findByRole("dialog", { name: "Delete this comment?" });
    expect(deleted()).toHaveLength(0);

    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(deleted()).toHaveLength(0);

    await userEvent.click(screen.getByRole("button", { name: "Delete the comment…" }));
    dialog = await screen.findByRole("dialog", { name: "Delete this comment?" });
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete comment" }));
    await waitFor(() => expect(deleted()).toEqual([{ method: "DELETE", path: "/api/v1/projects/p1/issues/7/comments/c1" }]));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });
});

describe("the issue panel", () => {
  it("says the issue failed to load, with Retry", async () => {
    let failing = true;
    stubAPI((p) => failing && p.endsWith("/issues/7"));
    mount(<IssuePanel projectId="p1" project="storefront" number={7} onClose={() => {}} />);

    expect(await screen.findByText("Couldn’t load this issue.")).toBeInTheDocument();
    failing = false;
    await userEvent.click(screen.getByRole("button", { name: /^Retry/ }));
    expect(await screen.findByRole("heading", { name: "Webhook retries" })).toBeInTheDocument();
  });

  // I21: opened from the keyboard, the panel takes the focus.
  it("takes the focus on its Close button when asked to", async () => {
    stubAPI();
    mount(<IssuePanel focus projectId="p1" project="storefront" number={7} onClose={() => {}} />);
    expect(screen.getByRole("button", { name: "Close the issue panel" })).toHaveFocus();
    await screen.findByRole("heading", { name: "Webhook retries" });
  });
});
