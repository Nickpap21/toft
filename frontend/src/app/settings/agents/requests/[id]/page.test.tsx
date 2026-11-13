import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { toast } from "sonner";
import { ApiError } from "@/lib/api/client";
import AgentRequestPage from "./page";

// The agent request page: what it does when a read fails, what it says once
// decided, and what a person who can't decide reads.

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
  usePathname: () => "/settings/agents/requests/a1",
  useParams: () => ({ id: "a1" }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
let role = "owner";
vi.mock("@/context/AuthContext", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/context/AuthContext")>()),
  useAuth: () => ({ user: { name: "sam", orgName: "Acme", role } }),
  useOrgRole: () => role,
}));

const hour = (h: number) => new Date(Date.now() - h * 3_600_000).toISOString();
const service = { id: "s1", name: "api", project_id: "p1", environment_id: "e1", server_id: "srv-1", source_type: "image", repository_id: "", active_deployment_id: "", effective_deploy_strategy: "rolling" };
const approval = (over: Record<string, unknown> = {}) => ({
  id: "a1", connection_id: "c1", agent: "Claude Code", user_id: "u1", user_name: "alex", user_role: "developer", action: "deploy", tool: "deploy",
  service_id: "s1", environment_id: "e1", project_id: "p1", target: "storefront / api in production", env_class: "protected",
  request: { method: "POST", path: "/api/v1/services/s1/deployments", body: {} }, reason: "Fixes it", plan: "", plan_steps: [], rollback: null, state: "waiting",
  expires_at: new Date(Date.now() + 1_800_000).toISOString(), decided_by: "", decided_at: null, note: "", executed_at: null, result: null, created_at: hour(0), ...over,
});
const by = { state: "approved", decided_by: "sam", decided_at: hour(0) };
const started = { ...by, result: { status: 202, body: { data: { id: "d1" } } } };

let read: () => unknown = () => approval();
let post: () => unknown = () => approval(by);
let deployment = { id: "d1", service_id: "s1", status: "building", error_msg: "" };

vi.mock("@/lib/api/client", async (importOriginal) => {
  const orig = await importOriginal<typeof import("@/lib/api/client")>();
  return {
    ...orig,
    fetchAPI: async (path: string, init?: RequestInit) => {
      if (init?.method === "POST") return post();
      if (path === "/api/v1/agents/approvals/a1") return read();
      if (path === "/api/v1/services/s1") return service;
      if (path === "/api/v1/services/s1/deployments/d1") return deployment;
      if (path === "/api/v1/projects/p1/overview") return { environments: [], services: [], recent_deployments: [] };
      return [];
    },
  };
});

let client: QueryClient;
function open() {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <AgentRequestPage />
    </QueryClientProvider>,
  );
}
const heading = () => screen.findByRole("heading", { level: 1 });

beforeEach(() => {
  role = "owner";
  read = () => approval();
  post = () => approval(by);
  deployment = { id: "d1", service_id: "s1", status: "building", error_msg: "" };
  vi.mocked(toast.error).mockClear();
});
afterEach(() => {
  vi.useRealTimers();
});

describe("a read that fails", () => {
  it("leaves the page and the typed note up when a background read fails", async () => {
    open();
    expect((await heading()).textContent).toContain("Claude Code wants to deploy");
    const note = screen.getByLabelText(/^Note to Claude Code/) as HTMLInputElement;
    fireEvent.change(note, { target: { value: "watch checkout" } });
    for (const failure of [new ApiError("failed: approval", 500, null), new ApiError("approval not found", 404, null)]) {
      read = () => {
        throw failure;
      };
      await act(() => client.refetchQueries({ queryKey: ["agent-approval", "a1"] }));
      expect(screen.queryByText("No such request")).toBeNull();
      expect((screen.getByLabelText(/^Note to Claude Code/) as HTMLInputElement).value).toBe("watch checkout");
    }
  });

  it("says No such request only for a 404 with nothing loaded", async () => {
    read = () => {
      throw new ApiError("approval not found", 404, null);
    };
    open();
    expect((await heading()).textContent).toBe("No such request");
  });

  it("says it couldn't load, with Retry, for any other failure with nothing loaded", async () => {
    read = () => {
      throw new ApiError("failed: approval", 500, null);
    };
    open();
    expect((await heading()).textContent).toBe("Couldn’t load this page");
    read = () => approval();
    fireEvent.click(screen.getByRole("button", { name: /^Retry/ }));
    await screen.findByLabelText(/^Note to Claude Code/);
  });
});

describe("once decided", () => {
  it("says a refused run didn't start, in the card right under the header", async () => {
    read = () => approval({ ...by, result: { status: 409, body: { error: "a deploy is running" } } });
    open();
    const chip = await screen.findByText("Approved · didn’t start");
    expect(chip.className).toContain("status-failed");
    const card = screen.getByText(/but the deploy didn’t start: a deploy is running\.$/);
    expect(screen.getByRole("heading", { level: 1 }).compareDocumentPosition(card) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(card.compareDocumentPosition(screen.getByText("Fixes it")) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("follows a deploy that started and reads as failed when it fails", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true, toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    read = () => approval(started);
    open();
    expect(await screen.findByText("Approved")).toBeTruthy();
    deployment = { ...deployment, status: "failed", error_msg: "health check failed" };
    await act(() => vi.advanceTimersByTimeAsync(6000));
    expect(screen.getByText("Approved · failed").className).toContain("status-failed");
    expect(screen.getByText(/but the deploy failed: health check failed\.$/)).toBeTruthy();
  });

  it("stays Approved for a deploy that goes live", async () => {
    deployment = { ...deployment, status: "running" };
    read = () => approval(started);
    open();
    expect(await screen.findByText("Approved")).toBeTruthy();
    await waitFor(() => expect(client.getQueryData(["deployment", "d1"])).toBeTruthy());
    expect(screen.getByText("Approved").className).toContain("status-running");
  });
});

describe("a long reason and plan at phone width", () => {
  const long = "x ".repeat(200);
  it("clamps them with Show all, and shows them again", async () => {
    const plan = "y ".repeat(200);
    read = () => approval({ reason: long, plan });
    open();
    const reason = await screen.findByText(long.trim());
    const p = screen.getByText(plan.trim());
    expect(reason.className).toContain("max-md:line-clamp-3");
    expect(p.className).toContain("max-md:line-clamp-3");
    fireEvent.click(screen.getByRole("button", { name: "Show all" }));
    expect(reason.className).not.toContain("line-clamp");
    expect(p.className).not.toContain("line-clamp");
    expect(screen.getByRole("button", { name: "Show fewer" })).toBeTruthy();
  });

  it("leaves a short one alone", async () => {
    open();
    await heading();
    expect(screen.getByText("Fixes it").className).not.toContain("line-clamp");
    expect(screen.queryByRole("button", { name: "Show all" })).toBeNull();
  });
});

describe("deciding", () => {
  it("says a 500 didn't go through and to try again", async () => {
    post = () => {
      throw new ApiError("failed: approval", 500, null);
    };
    open();
    fireEvent.click(await screen.findByRole("button", { name: "Approve and deploy" }));
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("It didn’t go through. Try again."));
  });

  it("keeps the API's words when it says why", async () => {
    post = () => {
      throw new ApiError("approval was already decided or has expired", 409, null);
    };
    open();
    fireEvent.click(await screen.findByRole("button", { name: "Approve and deploy" }));
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("approval was already decided or has expired"));
  });

  it("moves focus to the decided card, since the buttons are gone", async () => {
    open();
    const approve = await screen.findByRole("button", { name: "Approve and deploy" });
    approve.focus();
    fireEvent.click(approve);
    const text = await screen.findByText(/^You approved at/);
    await waitFor(() => expect(document.activeElement).toBe(text.closest("[tabindex]")));
  });

  it("leaves focus where it is when someone else decides", async () => {
    open();
    const link = await screen.findByRole("link", { name: "the connection" });
    link.focus();
    read = () => approval({ state: "approved", decided_by: "kim", decided_at: hour(0) });
    await act(() => client.refetchQueries({ queryKey: ["agent-approval", "a1"] }));
    await screen.findByText(/^kim approved at/);
    expect(document.activeElement).toBe(link);
  });

  it("pins Approve and Deny to the very bottom on a phone", async () => {
    open();
    const bar = (await screen.findByRole("button", { name: "Deny" })).parentElement;
    expect(bar?.className).toContain("max-md:-bottom-4");
  });
});

describe("an unbreakable string", () => {
  const long = "x".repeat(300);
  const wraps = (el: Element) => expect(el.closest("[class*='overflow-wrap:anywhere']")).not.toBeNull();

  it("wraps in the header, the reason and the plan", async () => {
    read = () => approval({ agent: long, user_name: long, target: `${long} / api in production`, reason: long, plan_steps: [{ text: long, mark: "" }] });
    open();
    wraps(await heading());
    wraps(screen.getByText(/^Acting for/));
    wraps(screen.getByText(/^For x/));
    const written = screen.getAllByText(long);
    expect(written.length).toBeGreaterThanOrEqual(2);
    written.forEach(wraps);
  });

  it("wraps in a plan that isn't steps", async () => {
    read = () => approval({ plan: long });
    open();
    wraps(await screen.findByText(long));
  });

  it("wraps in the decided card and its note", async () => {
    read = () => approval({ ...by, agent: long, note: long });
    open();
    wraps(await screen.findByText(/^You approved at/));
    wraps(screen.getByText(/^Note to/));
  });
});

describe("someone who can't decide", () => {
  it("doesn't read as the one approving", async () => {
    role = "viewer";
    open();
    await screen.findByText("What Toft does if it’s approved");
    expect(document.body.textContent).not.toContain("if you approve");
    await waitFor(() => expect(document.body.textContent).toContain("as Claude Code for alex"));
    expect(document.body.textContent).not.toContain("approved by you");
    expect(screen.queryByRole("button", { name: "Approve and deploy" })).toBeNull();
  });
});
