import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { toast } from "sonner";
import { NewServiceForm } from "./new-service";

// Create and deploy over capacity (QA #7): the create is refused with the
// deploy's 409 capacity_exceeded and nothing is created. The form says why
// as the service page's Deploy does, never "Request failed: 409", and
// offers Create and deploy anyway to a role that may bypass the check.

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useSearchParams: () => new URLSearchParams("source=image"),
  usePathname: () => "/projects/p1/services/new",
}));
let orgRole = "owner";
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ isLoading: false, user: { id: "u-1", orgName: "Founders" } }),
  useOrgRole: () => orgRole,
  useIsOperator: () => false,
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }));

const refusal = {
  error: "Not enough room on qa-node: needs 512 MiB of memory, 0 MiB left of 3.7 GiB.",
  detail: "Not enough room on qa-node: needs 512 MiB of memory, 0 MiB left of 3.7 GiB.",
  title: "Conflict",
  status: 409,
  data: {
    code: "capacity_exceeded",
    can_bypass: true,
    effective_cpu_cores: 2,
    effective_memory_mb: 3788,
    allocated_cpu_cores: 0,
    allocated_memory_mb: 3788,
    requested_cpu_cores: 0,
    requested_memory_mb: 512,
  },
};
const created: { path: string; body: Record<string, unknown> }[] = [];

vi.mock("@/lib/api/client", async (importOriginal) => {
  const orig = await importOriginal<typeof import("@/lib/api/client")>();
  return {
    ...orig,
    fetchAPI: async (path: string, init?: RequestInit) => {
      if (init?.method === "POST" && path.endsWith("/environments/e1/services")) {
        created.push({ path, body: JSON.parse(String(init.body)) });
        throw new orig.ApiError(refusal.error, 409, refusal);
      }
      if (path === "/api/v1/projects/p1") return { id: "p1", name: "shop", description: "" };
      if (path === "/api/v1/projects/p1/environments")
        return [{ id: "e1", project_id: "p1", name: "production", color: "gray", protected: false }];
      if (path.includes("/projects/p1/overview")) return { environments: [] };
      if (path === "/api/v1/servers")
        return [{ id: "srv-1", name: "qa-node", ip_address: "203.0.113.10", connection: "ssh", provisioned_at: "2026-11-01T00:00:00Z" }];
      if (path.includes("/servers/srv-1/fit"))
        return { capacity_known: true, fits: true, cpu_ok: true, memory_ok: true, share: false, effective_cpu_cores: 2, effective_memory_mb: 3788, allocated_cpu_cores: 0, allocated_memory_mb: 3328 };
      if (path.includes("/environments/e1/overview")) return { network: "toft-e1" };
      if (path.includes("default-limit") || path.includes("/resources/")) return { memory_mb: 512, kind: "app" };
      if (path.includes("/address")) return { hostname: "" };
      if (path === "/api/v1/org/settings") return { resource_bypass_min_role: "owner" };
      return [];
    },
  };
});

async function fill() {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <NewServiceForm projectId="p1" />
    </QueryClientProvider>,
  );
  fireEvent.change(await screen.findByPlaceholderText("ghcr.io/you/app:1.0.0"), { target: { value: "nginx:alpine" } });
  fireEvent.change(screen.getByPlaceholderText("my-app"), { target: { value: "probe" } });
}

describe("Create and deploy over capacity", () => {
  beforeEach(() => {
    orgRole = "owner";
  });

  // 512 MiB asked, 460 MiB left: the Summary warns before the click.
  it("warns in the Summary that Create and deploy asks first, for a role that may bypass", async () => {
    await fill();
    const line = await screen.findByText(
      "It needs 512 MiB and qa-node has 460 MiB left of 3.7 GiB: Create and deploy asks before creating it. Lower its memory or pick another server.",
    );
    expect(line.closest("p")?.querySelector('[aria-label="Warning"]')).toBeTruthy();
    expect(line.closest("p")?.className).toContain("text-status-pending-fg");
  });

  it("says Create and deploy won’t create it for a role that may not", async () => {
    orgRole = "developer";
    await fill();
    expect(
      await screen.findByText(
        "It needs 512 MiB and qa-node has 460 MiB left of 3.7 GiB: Create and deploy won’t create it. Lower its memory or pick another server.",
      ),
    ).toBeInTheDocument();
  });

  it("shows the refusal and offers Create and deploy anyway, and created nothing", async () => {
    await fill();
    const deploy = screen.getAllByRole("button", { name: "Create and deploy" })[0];
    await waitFor(() => expect(deploy).toBeEnabled());
    fireEvent.click(deploy);

    expect(await screen.findByText("needs 512 MiB, 0 MiB left of 3.7 GiB")).toBeInTheDocument();
    expect(screen.getByRole("dialog")).toHaveAccessibleDescription(/Nothing was created\./);
    expect(screen.getByRole("button", { name: "Create and deploy anyway" })).toBeInTheDocument();
    expect(created[0].body.deploy).toBe(true);
    expect(toast.error).not.toHaveBeenCalledWith(expect.stringContaining("Request failed"));
    expect(toast.error).not.toHaveBeenCalledWith(expect.stringContaining("created, but"));

    // Cancel returns focus to the Create and deploy that asked.
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => expect(deploy).toHaveFocus());
  });
});
