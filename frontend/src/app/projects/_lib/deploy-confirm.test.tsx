import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Service } from "@/lib/api/service.service";
import { RollbackDialog } from "../[id]/services/[serviceId]/_components/rollback-dialog";
import { DeployConfirm } from "./deploy-confirm";

// The protected deploy confirm (7g step 8, board ReworkDeployConfirm).

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
  usePathname: () => "/projects/p1/services/s1",
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const sha = (s: string) => s.padEnd(40, "0");
const hour = (h: number) => new Date(Date.now() - h * 3_600_000).toISOString();
const live = { id: "d-live", service_id: "s1", status: "running", image_tag: "img:9b7d3f0", commit_sha: sha("9b7d3f0"), commit_message: "Merge !40: Structured request logging", error_msg: "", created_at: hour(3), started_at: hour(3) };
const old = { ...live, id: "d-old", image_tag: "img:41c0d2e", commit_sha: sha("41c0d2e"), commit_message: "Merge !39: Idempotency keys", created_at: hour(26), started_at: hour(26) };
const posts: { path: string; body: unknown }[] = [];

vi.mock("@/lib/api/client", async (importOriginal) => {
  const orig = await importOriginal<typeof import("@/lib/api/client")>();
  return {
    ...orig,
    fetchVersioned: async (path: string) => ({
      data:
        path === "/api/v1/services/s1/env-vars"
          ? [{ key: "LOG_LEVEL", value: "debug", created_at: hour(30), updated_at: hour(1), updated_by: "admin" }]
          : [],
      version: "v1",
    }),
    fetchAPI: async (path: string, init?: RequestInit) => {
      if (init?.method === "POST") {
        posts.push({ path, body: init.body ? JSON.parse(String(init.body)) : null });
        return { id: "d-new" };
      }
      if (path.startsWith("/api/v1/services/s1/deployments?")) return [live, old];
      if (path === "/api/v1/services/s1/domains")
        return [{ hostname: "shop.example", routed_at: null, created_at: hour(1), created_by: "alex" }];
      if (path.includes("/compare?")) {
        const head = { sha: sha("c58e0a1"), parents: [], author_name: "alex", author_email: "", authored_at: hour(1), committed_at: hour(1), subject: "Merge !45: Paginate GET /orders", checks: { state: "success", total: 1, passed: 1, failed: 0, pending: 0 } };
        return { head: { name: "main", kind: "branch", sha: head.sha }, commits: [head], commits_truncated: false };
      }
      if (path === "/api/v1/projects/p1/overview") return { environments: [], services: [], recent_deployments: [] };
      if (path === "/api/v1/projects/p1/environments") return [{ id: "e1", project_id: "p1", name: "production", color: "gray", protected: true }];
      if (path === "/api/v1/servers") return [{ id: "srv-1", name: "fsn1-prod-01" }];
      return [];
    },
  };
});

const service = {
  id: "s1",
  name: "api",
  project_id: "p1",
  environment_id: "e1",
  server_id: "srv-1",
  source_type: "git",
  repository_id: "r1",
  git_branch: "main",
  active_deployment_id: "d-live",
  effective_deploy_strategy: "rolling",
  health_check_path: "/healthz",
  health_check_retries: 5,
} as unknown as Service;
const env = { name: "production", color: "gray" as const, protected: true };

function wrap(ui: React.ReactNode) {
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{ui}</QueryClientProvider>);
}

beforeEach(() => {
  posts.length = 0;
});

describe("DeployConfirm", () => {
  it("opens on Cancel, lists what goes live and confirms the commit it showed", async () => {
    const onConfirm = vi.fn();
    wrap(<DeployConfirm open service={service} env={env} pending={false} onCancel={vi.fn()} onConfirm={onConfirm} />);
    expect(screen.getByRole("alertdialog")).toBeTruthy();
    await waitFor(() => expect(document.activeElement?.textContent).toBe("Cancel"));
    const deploy = await screen.findByRole("button", { name: "Deploy c58e0a1 to production" });
    expect(screen.getByText("1 commit goes live")).toBeTruthy();
    expect(screen.getByText("✓ checks passed")).toBeTruthy();
    expect(screen.getByText("LOG_LEVEL")).toBeTruthy();
    expect(screen.getByText("shop.example", { selector: "li span" })).toBeTruthy();
    fireEvent.click(deploy);
    expect(onConfirm).toHaveBeenCalledWith(sha("c58e0a1"));
  });

  it("Redeploy lists today's variable changes under Also goes live", async () => {
    wrap(<DeployConfirm open service={service} env={env} mode="redeploy" target={live as never} pending={false} onCancel={vi.fn()} onConfirm={vi.fn()} />);
    expect(await screen.findByRole("button", { name: "Redeploy 9b7d3f0" })).toBeTruthy();
    expect(await screen.findByText("LOG_LEVEL")).toBeTruthy();
    expect(screen.getByText("→ debug")).toBeTruthy();
  });
});

describe("RollbackDialog", () => {
  it("in a protected environment, confirms with DeployConfirm and rolls back", async () => {
    wrap(<RollbackDialog service={service} target={old as never} live={live as never} onClose={vi.fn()} />);
    const go = await screen.findByRole("button", { name: "Roll back to 41c0d2e" });
    expect(screen.getByRole("alertdialog").textContent).toContain("Roll back api in");
    fireEvent.click(go);
    await waitFor(() => expect(posts.map((p) => p.path)).toEqual(["/api/v1/services/s1/deployments/d-old/rollback"]));
  });
});
