import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { NewServiceForm } from "./new-service";

// A service built from the project's own repository deploys its default
// branch, which an import keeps as the remote had it (often master), until
// another is typed.

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useSearchParams: () => new URLSearchParams("source=git"),
  usePathname: () => "/projects/p1/services/new",
}));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ user: { id: "u-1", orgName: "Founders" }, token: "t" }),
  useOrgRole: () => "owner",
  useIsOperator: () => false,
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }));

const shop = { id: "r1", name: "shop", full_name: "founders/shop", ssh_url: "", https_url: "", created_at: "2026-11-11T12:00:00Z", protect_default_branch: false, open_pulls: 0, pull_mirror: null };

vi.mock("@/lib/api/client", async (importOriginal) => {
  const orig = await importOriginal<typeof import("@/lib/api/client")>();
  return {
    ...orig,
    fetchAPI: async (path: string) => {
      if (path === "/api/v1/git") return { enabled: true };
      if (path === "/api/v1/projects/p1/repositories") return [shop];
      if (path === "/api/v1/projects/p1/repositories/r1") return { ...shop, default_branch: "master", empty: false, branch_count: 3, tag_count: 0, pulls: { open: 0, merged: 0, closed: 0 } };
      if (path === "/api/v1/projects/p1") return { id: "p1", name: "shop" };
      if (path === "/api/v1/projects/p1/environments") return [{ id: "e1", project_id: "p1", name: "production", color: "gray", protected: false }];
      if (path.includes("/projects/p1/overview")) return { environments: [] };
      if (path === "/api/v1/instance/panel-host") return { role: "apps", app_networks: "isolated" };
      if (path === "/api/v1/servers") return [{ id: "s1", name: "fsn1-apps", ip_address: "203.0.113.7", shared: false, connection: "ssh" }];
      if (path.startsWith("/api/v1/resources/default-limit")) return { kind: "app", memory_mb: 512 };
      if (path.includes("/environments/e1/overview")) return { network: "toft-e1" };
      return [];
    },
  };
});

function renderForm() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <NewServiceForm projectId="p1" />
    </QueryClientProvider>,
  );
}

describe("The branch of the project's own repository", () => {
  it("is the repository's default branch, not main", async () => {
    renderForm();
    expect(await screen.findByText("It deploys from master; pushes deploy it again.")).toBeInTheDocument();
    expect((screen.getByLabelText("Branch") as HTMLInputElement).value).toBe("master");
    expect(screen.getByText("founders/shop · master")).toBeInTheDocument();
  });

  it("is the one typed, once typed", async () => {
    renderForm();
    await screen.findByText("It deploys from master; pushes deploy it again.");
    fireEvent.change(screen.getByLabelText("Branch"), { target: { value: "release" } });
    expect(screen.getByText("It deploys from release; pushes deploy it again.")).toBeInTheDocument();
  });
});
