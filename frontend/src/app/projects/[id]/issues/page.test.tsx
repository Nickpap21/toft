import { useSyncExternalStore } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import IssuesPage from "./page";

let search = new URLSearchParams();
const listeners = new Set<() => void>();
const replace = (href: string) => {
  search = new URLSearchParams(href.split("?")[1] ?? "");
  listeners.forEach((l) => l());
};
vi.mock("next/navigation", () => ({
  useParams: () => ({ id: "p1" }),
  useRouter: () => ({ replace }),
  useSearchParams: () =>
    useSyncExternalStore(
      (l) => {
        listeners.add(l);
        return () => listeners.delete(l);
      },
      () => search,
    ),
}));
vi.mock("@/context/AuthContext", () => ({ useAuth: () => ({ user: { id: "u1" } }), useOrgRole: () => "owner" }));
vi.mock("@/components/shell/env-context", () => ({ useEnvContext: () => ({ env: null }) }));
vi.mock("@/components/issues/new-issue", () => ({ NewIssueDialog: ({ open }: { open: boolean }) => (open ? <div role="dialog" aria-label="New issue" /> : null) }));
vi.mock("@/components/issues/issue-detail", () => ({
  useCanWriteIssues: () => true,
  useIssueRefresh: () => () => {},
  IssuePanel: ({ onClose, focus }: { onClose: () => void; focus?: boolean }) => (
    <button type="button" autoFocus={focus} onClick={onClose}>
      Close the issue panel
    </button>
  ),
}));

const issue = (number: number, status: string, title: string) => ({
  key: `STO-${number}`, number, title, status, priority: "medium", assignee: null, labels: [], work: null, parent: null, blocked_by: [], progress: null, due_on: null, estimate: null,
  updated_at: "2026-11-08T10:00:00Z", type: { id: "task", name: "Task", icon: "check", color: "blue", kind: "standard" },
});
const counts = { backlog: 0, todo: 1, in_progress: 1, in_review: 0, shipped: 0, cancelled: 0 };

function stubAPI(failing: () => string[] = () => []) {
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof globalThis.fetch>(async (input) => {
      const url = new URL(String(input), location.origin);
      const ok = (data: unknown, next?: string | null) => new Response(JSON.stringify(next === undefined ? { data } : { data, next }), { status: 200 });
      const refused = () => new Response(JSON.stringify({ error: "boom" }), { status: 500 });
      if (url.pathname.endsWith("/issue-counts")) return failing().includes("counts") ? refused() : ok(counts);
      if (url.pathname.endsWith("/issue-timeline")) return failing().includes("timeline") ? refused() : ok({ items: [], blocks: [], cycles: [], undated: 0 });
      if (url.pathname.endsWith("/issue-labels") && failing().includes("labels")) return refused();
      if (url.pathname.endsWith("/p1/issues")) {
        const status = url.searchParams.get("status") ?? "";
        if (failing().includes(status)) return new Response(JSON.stringify({ error: "boom" }), { status: 500 });
        return ok(status === "in_progress" ? [issue(114, status, "Search ranks")] : status === "todo" ? [issue(120, status, "Deploy hangs")] : [], null);
      }
      if (url.pathname.endsWith("/cycles")) return ok({ enabled: false, estimates: "off", cycles: [] });
      if (url.pathname.endsWith("/projects/p1")) return ok({ id: "p1", name: "storefront" });
      return ok([]);
    }),
  );
}

function show(url = "") {
  search = new URLSearchParams(url);
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <IssuesPage />
    </QueryClientProvider>,
  );
}

afterEach(() => vi.unstubAllGlobals());

describe("Issues", () => {
  it("says on one line which group failed, and retries it", async () => {
    let failing = ["in_progress"];
    stubAPI(() => failing);
    show();

    expect(await screen.findByText("Couldn’t load In progress.")).toBeInTheDocument();
    expect(screen.getByText("Deploy hangs")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Couldn’t load this page" })).not.toBeInTheDocument();

    failing = [];
    await userEvent.click(screen.getByRole("button", { name: /^Retry/ }));
    expect(await screen.findByText("Search ranks")).toBeInTheDocument();
    expect(screen.queryByText("Couldn’t load In progress.")).not.toBeInTheDocument();
  });

  // I1: a failed read isn't an empty plan.
  it("says the Timeline failed, with Retry, and not that there's nothing to draw", async () => {
    let failing = ["timeline"];
    stubAPI(() => failing);
    show("layout=timeline");

    expect(await screen.findByRole("heading", { name: "Couldn’t load this page", level: 2 })).toBeInTheDocument();
    expect(screen.queryByText(/Nothing to draw yet/)).not.toBeInTheDocument();

    failing = [];
    await userEvent.click(screen.getByRole("button", { name: /^Retry/ }));
    expect(await screen.findByText(/Nothing to draw yet/)).toBeInTheDocument();
  });

  // I2: a column whose rows failed says so, in place of "N more".
  it("says which Board column failed, with Retry, and draws no “more” for it", async () => {
    let failing = ["in_progress"];
    stubAPI(() => failing);
    show("layout=board");

    expect(await screen.findByText("Couldn’t load In progress.")).toBeInTheDocument();
    expect(await screen.findByText("Deploy hangs")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "1 more" })).not.toBeInTheDocument();

    failing = [];
    await userEvent.click(screen.getByRole("button", { name: /^Retry/ }));
    expect(await screen.findByText("Search ranks")).toBeInTheDocument();
    expect(screen.queryByText("Couldn’t load In progress.")).not.toBeInTheDocument();
  });

  it("draws a Board column’s count that failed as —, never 0", async () => {
    stubAPI(() => ["counts"]);
    show("layout=board");

    await screen.findByText("Deploy hangs");
    const column = screen.getByRole("region", { name: "Todo" });
    await waitFor(() => expect(column).toHaveTextContent("—"));
  });

  it("doesn’t say there are no labels when the labels failed to load", async () => {
    stubAPI(() => ["labels"]);
    show();
    await screen.findByText("Search ranks");

    await userEvent.click(screen.getByRole("button", { name: "Filter" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Label" }));

    expect(await screen.findByRole("menuitem", { name: "Couldn’t load the labels. Retry" })).toBeInTheDocument();
    expect(screen.queryByText("No labels yet")).not.toBeInTheDocument();
  });

  // I21: the panel comes after every row in tab order.
  it("moves the focus into the panel when a row opens it from the keyboard, and not from the mouse", async () => {
    const wide = vi.spyOn(window, "matchMedia").mockImplementation(() => ({ matches: true }) as MediaQueryList);
    stubAPI();
    show();
    const row = await screen.findByRole("link", { name: "Search ranks" });

    await userEvent.click(row);
    expect(row).toHaveFocus();
    await userEvent.click(screen.getByRole("button", { name: "Close the issue panel" }));
    await waitFor(() => expect(search.has("issue")).toBe(false));

    row.focus();
    await userEvent.keyboard("{Enter}");
    expect(await screen.findByRole("button", { name: "Close the issue panel" })).toHaveFocus();
    wide.mockRestore();
  });

  it("opens New issue on C", async () => {
    stubAPI();
    show();
    await screen.findByText("Search ranks");

    await userEvent.keyboard("c");
    expect(await screen.findByRole("dialog", { name: "New issue" })).toBeInTheDocument();
  });

  it("does not open New issue through an open menu", async () => {
    stubAPI();
    show();
    await screen.findByText("Search ranks");

    await userEvent.click(screen.getByRole("button", { name: "Filter" }));
    await screen.findByRole("menu");
    await userEvent.keyboard("c");

    // hidden: an open menu marks the rest of the page aria-hidden.
    expect(screen.queryByRole("dialog", { name: "New issue", hidden: true })).not.toBeInTheDocument();
  });

  it("gives the focus back to the selected row when the panel closes", async () => {
    stubAPI();
    show("issue=114");
    const row = await screen.findByRole("link", { name: "Search ranks" });

    await userEvent.click(screen.getByRole("button", { name: "Close the issue panel" }));

    expect(row).toHaveFocus();
    expect(search.has("issue")).toBe(false);
  });

  it("empties the search box when a tab drops the search", async () => {
    stubAPI();
    show();
    await screen.findByText("Search ranks");
    const box = screen.getByRole("searchbox", { name: "Search issues" });

    await userEvent.type(box, "login");
    expect(box).toHaveValue("login");
    expect(box).toHaveFocus();
    expect(search.get("q")).toBe("login");

    await userEvent.click(screen.getByRole("button", { name: /^Active/ }));

    expect(search.has("q")).toBe(false);
    await waitFor(() => expect(box).toHaveValue(""));
  });
});
