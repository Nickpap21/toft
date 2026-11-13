import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import WorkspaceSettingsPage from "./page";

// W1: a failed read of a card's settings says so, with Retry, and no default
// value or endless skeleton stands in for what the workspace has set.

let role = "owner";
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  usePathname: () => "/settings",
  useSearchParams: () => new URLSearchParams(),
}));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ isLoading: false, user: { id: "u1", orgId: "o1", orgName: "QA Lab", orgs: [] } }),
  useOrgRole: () => role,
}));

const ok = (v: unknown) => ({ status: 200, body: JSON.stringify({ data: v }) });
const boom = { status: 500, body: JSON.stringify({ error: "internal error", status: 500 }) };
type Answer = { status: number; body: string };
const READS: Record<string, Answer> = {
  "/api/v1/org": ok({ id: "o1", name: "QA Lab" }),
  "/api/v1/org/settings": ok({ resource_bypass_min_role: "owner" }),
  "/api/v1/org/isolation": ok({ default_isolation: "standard", servers: [] }),
  "/api/v1/workspace/issue-types": ok([{ id: "t1", name: "Task", kind: "standard", icon: "chore", color: "gray", archived: false }]),
  "/api/v1/workspace/issue-settings": ok({ digits: 3, cycles: { enabled: true, weeks: 2, start_day: 1, time_zone: "UTC" }, estimates: "fibonacci" }),
};

function serve(over: Record<string, Answer | "pending">) {
  const answers = { ...READS, ...over };
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    const a = answers[new URL(url, "http://x").pathname] ?? ok([]);
    if (a === "pending") return new Promise<Response>(() => {});
    return new Response(a.body, { status: a.status });
  }));
}
afterEach(() => {
  vi.unstubAllGlobals();
  role = "owner";
});

function show() {
  vi.stubGlobal("IntersectionObserver", class { observe() {} disconnect() {} });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><WorkspaceSettingsPage /></QueryClientProvider>);
}
const region = (name: string) => screen.findByRole("region", { name });

describe("Settings → Workspace, a read that fails", () => {
  it.each(["/api/v1/org", "/api/v1/org/settings"])("General says so with Retry when %s fails, no skeleton", async (path) => {
    serve({ [path]: boom });
    show();
    const card = await region("General");
    expect(await within(card).findByText("Couldn’t load the workspace’s settings.")).toBeInTheDocument();
    expect(within(card).getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
    expect(within(card).queryByRole("status")).toBeNull();
    expect(within(card).queryByLabelText("Workspace name")).toBeNull();
  });

  it("a viewer isn't told who can deploy past capacity when it couldn't be read", async () => {
    role = "viewer";
    serve({ "/api/v1/org/settings": boom });
    show();
    const card = await region("General");
    expect(await within(card).findByText("Couldn’t load the workspace’s settings.")).toBeInTheDocument();
    expect(within(card).queryByText("Who can deploy past a server’s capacity")).toBeNull();
  });

  it("Members and Invites keep their rows when a refetch fails", async () => {
    const member = { id: "m1", user_id: "u2", user_name: "Sam Reed", user_email: "sam@x.io", role: "developer", created_at: "2026-01-01T00:00:00Z" };
    serve({ "/api/v1/org/members": ok([member]) });
    vi.stubGlobal("IntersectionObserver", class { observe() {} disconnect() {} });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}><WorkspaceSettingsPage /></QueryClientProvider>);
    await screen.findByText("Sam Reed");
    serve({ "/api/v1/org/members": boom });
    await client.refetchQueries({ queryKey: ["org-members"] });
    expect(client.getQueryState(["org-members"])?.status).toBe("error");
    expect(screen.getByText("Sam Reed")).toBeInTheDocument();
    expect(screen.queryByText(/Could not load the members/)).toBeNull();
  });

  it("Retry reads General again", async () => {
    serve({ "/api/v1/org/settings": boom });
    show();
    const card = await region("General");
    const retry = await within(card).findByRole("button", { name: /^Retry/ });
    serve({});
    fireEvent.click(retry);
    expect(await within(card).findByLabelText("Workspace name")).toBeInTheDocument();
  });

  it("Isolation doesn't mark Standard or say no servers", async () => {
    serve({ "/api/v1/org/isolation": boom });
    show();
    const card = await region("Isolation");
    expect(await within(card).findByText("Couldn’t load the isolation level.")).toBeInTheDocument();
    expect(within(card).getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
    expect(within(card).queryByText(/no servers yet/)).toBeNull();
    expect(within(card).queryByRole("button", { name: "Standard", pressed: true })).toBeNull();
  });

  it("Issues doesn't say 0 types", async () => {
    serve({ "/api/v1/workspace/issue-types": boom });
    show();
    const card = await region("Issues");
    expect(await within(card).findByText("Couldn’t load the issue types.")).toBeInTheDocument();
    expect(within(card).getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
    expect(within(card).queryByText(/^0 types/)).toBeNull();
    expect(within(card).getByText(/^— types/)).toBeInTheDocument();
  });

  it("Issues doesn't show the default digits when they couldn't be read", async () => {
    serve({ "/api/v1/workspace/issue-settings": boom });
    show();
    const card = await region("Issues");
    expect(await within(card).findByText("Couldn’t load the key format.")).toBeInTheDocument();
    expect(within(card).queryByLabelText("Keys look like")).toBeNull();
  });

  it.each([
    ["Cycles", "Couldn’t load the cycles."],
    ["Estimates", "Couldn’t load the estimates."],
  ])("%s says so with Retry, and doesn't read Off", async (name, text) => {
    serve({ "/api/v1/workspace/issue-settings": boom });
    show();
    const card = await region(name);
    expect(await within(card).findByText(text)).toBeInTheDocument();
    expect(within(card).getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
    expect(within(card).queryByText(/^Off/)).toBeNull();
  });
});

describe("Settings → Workspace, while it reads", () => {
  it("announces General's skeleton", async () => {
    serve({ "/api/v1/org": "pending", "/api/v1/org/settings": "pending" });
    show();
    const card = await region("General");
    expect(await within(card).findByRole("status", { name: "Loading the workspace’s settings" })).toBeInTheDocument();
  });

  it("announces a table's skeleton row", async () => {
    serve({ "/api/v1/org/members": "pending" });
    show();
    expect(await screen.findAllByRole("status", { name: "Loading" })).not.toHaveLength(0);
  });
});
