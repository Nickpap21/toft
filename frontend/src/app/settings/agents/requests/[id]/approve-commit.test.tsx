import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import AgentRequestPage from "./page";

// Approve sends the commit the page showed under Deploys, as a protected
// deploy's Confirm does: a push during the wait doesn't ship unseen.

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
  usePathname: () => "/settings/agents/requests/a1",
  useParams: () => ({ id: "a1" }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
vi.mock("@/context/AuthContext", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/context/AuthContext")>()),
  useAuth: () => ({ user: { name: "sam", orgName: "Acme", role: "owner" } }),
  useOrgRole: () => "owner",
}));

const sha = (s: string) => s.padEnd(40, "0");
const hour = (h: number) => new Date(Date.now() - h * 3_600_000).toISOString();
const live = { id: "d-live", service_id: "s1", status: "running", image_tag: "", commit_sha: sha("9b7d3f0"), commit_message: "Merge !40", error_msg: "", created_at: hour(3), started_at: hour(3) };
const service = { id: "s1", name: "api", project_id: "p1", environment_id: "e1", server_id: "srv-1", source_type: "git", repository_id: "r1", git_branch: "main", active_deployment_id: "d-live", effective_deploy_strategy: "rolling" };
const approval = (body: Record<string, unknown>) => ({
  id: "a1", connection_id: "c1", agent: "Claude Code", user_id: "u1", user_name: "alex", user_role: "developer", action: "deploy", tool: "deploy",
  service_id: "s1", environment_id: "e1", project_id: "p1", target: "storefront / api in production", env_class: "protected",
  request: { method: "POST", path: "/api/v1/services/s1/deployments", body }, reason: "Fixes it", plan: "", plan_steps: [], rollback: null, state: "waiting",
  expires_at: new Date(Date.now() + 1_800_000).toISOString(), decided_by: "", decided_at: null, note: "", executed_at: null, result: null, created_at: hour(0),
});

let current = approval({});
const posts: { path: string; body: unknown }[] = [];

vi.mock("@/lib/api/client", async (importOriginal) => {
  const orig = await importOriginal<typeof import("@/lib/api/client")>();
  return {
    ...orig,
    fetchAPI: async (path: string, init?: RequestInit) => {
      if (init?.method === "POST") {
        posts.push({ path, body: JSON.parse(String(init.body)) });
        return { ...current, state: "approved", decided_by: "sam", decided_at: hour(0) };
      }
      if (path === "/api/v1/agents/approvals/a1") return current;
      if (path === "/api/v1/services/s1") return service;
      if (path.startsWith("/api/v1/services/s1/deployments?")) return [live];
      if (path.includes("/compare?")) {
        const head = { sha: sha("c58e0a1"), parents: [], author_name: "alex", author_email: "", authored_at: hour(1), committed_at: hour(1), subject: "Merge !45" };
        return { head: { name: "main", kind: "branch", sha: head.sha }, commits: [head], commits_truncated: false };
      }
      if (path === "/api/v1/projects/p1/overview") return { environments: [], services: [], recent_deployments: [] };
      return [];
    },
  };
});

function open() {
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <AgentRequestPage />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  posts.length = 0;
});

describe("Approve on a deploy", () => {
  it("sends the commit Deploys shows when the request names none", async () => {
    current = approval({});
    open();
    await screen.findAllByText("c58e0a1");
    await act(() => Promise.resolve());
    fireEvent.click(screen.getByRole("button", { name: "Approve and deploy" }));
    await waitFor(() => expect(posts).toEqual([{ path: "/api/v1/agents/approvals/a1/approve", body: { note: "", commit: sha("c58e0a1") } }]));
  });

  it("sends no commit when the request names its own", async () => {
    current = approval({ commit_sha: sha("c58e0a1") });
    open();
    await screen.findAllByText("c58e0a1");
    await act(() => Promise.resolve());
    fireEvent.click(screen.getByRole("button", { name: "Approve and deploy" }));
    await waitFor(() => expect(posts).toEqual([{ path: "/api/v1/agents/approvals/a1/approve", body: { note: "" } }]));
  });
});
