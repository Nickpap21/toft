import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import AgentsPage from "./page";

// A failed read on Agents isn't "nobody is waiting" or "every project follows".

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  usePathname: () => "/settings/agents",
  useSearchParams: () => new URLSearchParams(),
}));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ isLoading: false, user: { id: "u1", orgId: "o1", orgName: "QA Lab", orgs: [] } }),
  useOrgRole: () => "owner",
  useIsOperator: () => false,
}));

const ok = (v: unknown) => ({ status: 200, body: JSON.stringify({ data: v }) });
const boom = { status: 500, body: JSON.stringify({ error: "internal error", status: 500 }) };
type Answer = { status: number; body: string };

function serve(over: Record<string, Answer | "pending"> = {}) {
  const answers: Record<string, Answer | "pending"> = {
    "/api/v1/agents/pause": ok({}),
    "/api/v1/agents/policy": ok({ cells: [], projects: [] }),
    ...over,
  };
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    const a = answers[new URL(url, "http://x").pathname] ?? ok([]);
    if (a === "pending") return new Promise<Response>(() => {});
    return new Response(a.body, { status: a.status });
  }));
}
afterEach(() => vi.unstubAllGlobals());

function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><AgentsPage /></QueryClientProvider>);
}

describe("Settings → Agents, a read that fails", () => {
  it("says the waiting requests couldn't be read, with Retry", async () => {
    serve({ "/api/v1/agents/approvals": boom });
    show();
    expect(await screen.findByText("Couldn’t load the waiting requests.")).toBeInTheDocument();
  });

  it("Project rules doesn't say every project follows the workspace", async () => {
    serve({ "/api/v1/agents/policy": boom });
    show();
    expect(await screen.findByText("Couldn’t load the project rules.")).toBeInTheDocument();
    expect(screen.queryByText(/Every project follows/)).toBeNull();
  });

  it("Projects with their own rules prints no count until the policy has answered", async () => {
    serve({ "/api/v1/agents/policy": boom });
    show();
    expect(await screen.findByText("Couldn’t load the project rules.")).toBeInTheDocument();
    const heading = screen.getByRole("heading", { name: /Projects with their own rules/ });
    expect(heading).toHaveTextContent("—");
    expect(heading).not.toHaveTextContent("0");
  });

  it("Add a project doesn't say every project is listed while the projects are read", async () => {
    serve({ "/api/v1/projects": "pending" });
    show();
    await userEvent.click(await screen.findByRole("button", { name: "Add a project" }));
    expect(await screen.findByRole("menuitem", { name: "Loading…" })).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: "Every project is listed" })).toBeNull();
  });

  it("Add a project doesn't say every project is listed", async () => {
    serve({ "/api/v1/projects": boom });
    show();
    await userEvent.click(await screen.findByRole("button", { name: "Add a project" }));
    expect(await screen.findByRole("menuitem", { name: "Couldn’t load the projects" })).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: "Every project is listed" })).toBeNull();
  });
});

describe("Settings → Agents, while it reads", () => {
  it("announces each skeleton", async () => {
    serve({ "/api/v1/agents/policy": "pending", "/api/v1/agents/connections": "pending", "/api/v1/org/audit-log": "pending" });
    show();
    expect((await screen.findAllByRole("status", { name: "Loading" })).length).toBeGreaterThan(3);
  });
});
