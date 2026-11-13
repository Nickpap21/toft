import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { toast } from "sonner";
import type { NeedsYouItem } from "@/lib/api/inbox.service";
import InboxPage from "./page";

// H1, X1: a read that failed is never drawn as an answer. "Nothing needs you"
// over a 500 is a false claim. H16: Mark done keeps the keyboard's place and
// says what happened.

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const FAIL = Symbol("500");
const HANG = Symbol("pending");
const NEEDS = "/api/v1/inbox/needs-you";
const UPDATES = "/api/v1/inbox/updates";
const MINE = "/api/v1/workspace/my-issues";
let answers: Record<string, unknown>;

const json = (status: number, v: unknown) => new Response(JSON.stringify(v), { status });

const item = (key: string, title: string) =>
  ({ key, kind: "deploy_failed", category: "deploys", title, detail: "Deploy failed", at: "2026-11-01T10:00:00Z", project_id: null, project: "", severity: "error", snoozes: false }) as unknown as NeedsYouItem;
const update = (id: string) => ({
  id, kind: "issue_assigned", category: "issues", created_at: "2026-11-01T10:00:00Z", read: true, project: "shop", project_id: "p1", detail: {},
  actor: { name: "Alex" }, issue: { key: `SHOP-${id}`, number: 1, title: `Issue ${id}` },
});

beforeEach(() => {
  answers = { [NEEDS]: [], [UPDATES]: [], [MINE]: [] };
  vi.mocked(toast.success).mockClear();
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === "POST") return json(200, { data: null });
    const a = answers[new URL(url, "http://x").pathname];
    if (a === HANG) return new Promise<Response>(() => {});
    if (a === FAIL) return json(500, { error: "boom: pq internal detail", status: 500 });
    return json(200, { data: a ?? [] });
  }));
});
afterEach(() => vi.unstubAllGlobals());

function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}><InboxPage /></QueryClientProvider>);
}
const card = (name: string) => screen.getByRole("region", { name });

describe("Older updates", () => {
  it("says the next page failed to load, with Retry, and keeps the rows", async () => {
    answers[UPDATES] = [update("1")];
    let pages = 0;
    vi.stubGlobal("fetch", vi.fn(async (url: string) => {
      const u = new URL(url, "http://x");
      if (u.pathname === UPDATES) {
        if (u.searchParams.get("cursor")) { pages++; return pages === 1 ? json(500, { error: "boom", status: 500 }) : json(200, { data: [update("2")], next: null }); }
        return json(200, { data: [update("1")], next: "n2" });
      }
      return json(200, { data: answers[u.pathname] ?? [] });
    }));
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "Older updates" }));
    expect(await screen.findByText("Couldn’t load older updates.")).toBeInTheDocument();
    expect(screen.getByText(/Issue 1/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Retry loading older updates" }));
    expect(await screen.findByText(/Issue 2/)).toBeInTheDocument();
    expect(screen.queryByText("Couldn’t load older updates.")).toBeNull();
  });
});

describe("Inbox when a read fails", () => {
  it("Needs you says it couldn't load, with Retry, and no count of 0", async () => {
    answers[NEEDS] = FAIL;
    mount();
    expect(await screen.findByText("Couldn’t load what needs you.")).toBeInTheDocument();
    expect(within(card("Needs you")).getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
    expect(within(card("Needs you")).getByText("—")).toBeInTheDocument();
    expect(screen.queryByText("Nothing needs you right now.")).toBeNull();
    expect(screen.queryByText(/pq internal/)).toBeNull();
  });

  it("Needs you claims nothing while it's still reading", async () => {
    answers[NEEDS] = HANG;
    mount();
    expect(await screen.findByRole("status", { name: "Loading what needs you" })).toBeInTheDocument();
    expect(screen.queryByText("Nothing needs you right now.")).toBeNull();
  });

  it("Updates says it couldn't load, with Retry", async () => {
    answers[UPDATES] = FAIL;
    mount();
    expect(await screen.findByText("Couldn’t load your updates.")).toBeInTheDocument();
    expect(within(card("Updates")).getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
    expect(within(card("Updates")).getByText("—")).toBeInTheDocument();
    expect(screen.queryByText(/^No updates/)).toBeNull();
  });

  it("Assigned to you says it couldn't load, with Retry, and no count of 0", async () => {
    answers[MINE] = FAIL;
    mount();
    expect(await screen.findByText("Couldn’t load your assigned issues.")).toBeInTheDocument();
    expect(within(card("Assigned to you")).getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
    expect(within(card("Assigned to you")).getByText("—")).toBeInTheDocument();
    expect(screen.queryByText("Nothing assigned to you is open.")).toBeNull();
  });

  it("a filter's count that needs a failed read is — and never 0", async () => {
    answers[NEEDS] = FAIL;
    mount();
    await screen.findByText("Couldn’t load what needs you.");
    expect(screen.getByRole("button", { name: /^Deploys/ })).toHaveTextContent("Deploys—");
  });

  it("Retry reads again, and the rows replace the failure", async () => {
    answers[NEEDS] = FAIL;
    mount();
    await screen.findByText("Couldn’t load what needs you.");
    answers[NEEDS] = [item("deploy:d1", "web")];
    fireEvent.click(within(card("Needs you")).getByRole("button", { name: /^Retry/ }));
    expect(await screen.findByText("web")).toBeInTheDocument();
    expect(screen.queryByText("Couldn’t load what needs you.")).toBeNull();
  });

  it("empty answers still say so", async () => {
    mount();
    expect(await screen.findByText("Nothing needs you right now.")).toBeInTheDocument();
    expect(await screen.findByText(/^No updates\./)).toBeInTheDocument();
    expect(await screen.findByText("Nothing assigned to you is open.")).toBeInTheDocument();
  });
});

// With the row gone, focus would fall to the body and the person restarts
// from the top of the page.
describe("Mark done", () => {
  it("moves focus to the next row's Mark done, and says it's done", async () => {
    answers[NEEDS] = [item("deploy:a", "web"), item("deploy:b", "api")];
    mount();
    const first = await screen.findByRole("button", { name: "Mark “web” done" });
    first.focus();
    answers[NEEDS] = [item("deploy:b", "api")];
    fireEvent.click(first);
    await waitFor(() => expect(screen.queryByRole("button", { name: "Mark “web” done" })).toBeNull());
    await waitFor(() => expect(screen.getByRole("button", { name: "Mark “api” done" })).toHaveFocus());
    expect(toast.success).toHaveBeenCalledWith("Marked done");
  });

  it("moves focus to the card's heading when it was the last row", async () => {
    answers[NEEDS] = [item("deploy:a", "web")];
    mount();
    const only = await screen.findByRole("button", { name: "Mark “web” done" });
    only.focus();
    answers[NEEDS] = [];
    fireEvent.click(only);
    await waitFor(() => expect(screen.getByRole("heading", { name: "Needs you" })).toHaveFocus());
  });

  it("does the same in Updates", async () => {
    answers[UPDATES] = [update("1"), update("2")];
    mount();
    const [first] = await screen.findAllByRole("button", { name: "Mark done" });
    first.focus();
    answers[UPDATES] = [update("2")];
    fireEvent.click(first);
    await waitFor(() => expect(screen.getAllByRole("button", { name: "Mark done" })).toHaveLength(1));
    await waitFor(() => expect(screen.getByRole("button", { name: "Mark done" })).toHaveFocus());
    expect(toast.success).toHaveBeenCalledWith("Marked done");
  });

  it("leaves focus alone if the person has moved on", async () => {
    answers[NEEDS] = [item("deploy:a", "web"), item("deploy:b", "api")];
    mount();
    const first = await screen.findByRole("button", { name: "Mark “web” done" });
    first.focus();
    answers[NEEDS] = [item("deploy:b", "api")];
    fireEvent.click(first);
    const filter = screen.getByRole("button", { name: /^Issues/ });
    filter.focus();
    await waitFor(() => expect(screen.queryByRole("button", { name: "Mark “web” done" })).toBeNull());
    expect(filter).toHaveFocus();
  });
});
