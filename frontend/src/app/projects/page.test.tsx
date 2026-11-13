import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import ProjectsPage from "./page";

// All projects when one of its reads fails (P7): nothing about a project's
// services is said without having read them.

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
  usePathname: () => "/projects",
}));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ isLoading: false, user: { id: "u-1", orgName: "Founders" } }),
  useOrgRole: () => "owner",
  useIsOperator: () => false,
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const PROJECTS = "/api/v1/projects";
const SERVICES = "/api/v1/workspace/services";
const ENVIRONMENTS = "/api/v1/workspace/environments";

let failing: string[] = [];

function route(path: string): unknown {
  switch (path.split("?")[0]) {
    case PROJECTS:
      return [{ id: "p1", name: "shop", description: "", user_id: "u-1", created_at: "", updated_at: "" }];
    case SERVICES:
      return [{ id: "s1", name: "web", scope: "environment", project_id: "p1", environment_id: "e1", server_id: "", runtime_status: "running" }];
    case ENVIRONMENTS:
      return [{ id: "e1", project_id: "p1", name: "production", color: "gray", protected: true, created_at: "", updated_at: "" }];
    case "/api/v1/dashboard":
      return { project_deploys: [] };
  }
  return [];
}

vi.mock("@/lib/api/client", async (orig) => {
  const actual = await orig<typeof import("@/lib/api/client")>();
  return {
    ...actual,
    fetchAPI: async (path: string) => {
      if (failing.includes(path.split("?")[0])) throw new actual.ApiError("boom", 500, { error: "boom" });
      return route(path);
    },
  };
});

function renderPage() {
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <ProjectsPage />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  failing = [];
});

describe("all projects when a read fails", () => {
  it("doesn't say a project has no services on a failed read of them, and Retry brings them", async () => {
    failing = [SERVICES];
    renderPage();
    expect(await screen.findByText("Couldn’t load the services.")).toBeTruthy();
    expect(screen.getByRole("link", { name: /shop/ })).toBeTruthy();
    expect(screen.getByText(/1 in Founders · — services/)).toBeTruthy();
    expect(screen.queryByText("No services")).toBeNull();
    expect(screen.queryByText("no services")).toBeNull();
    expect(screen.queryByText(/0 services/)).toBeNull();

    failing = [];
    fireEvent.click(screen.getByRole("button", { name: /^Retry/ }));
    expect(await screen.findByText(/1 in Founders · 1 service/)).toBeTruthy();
    expect(await screen.findByText("1 running")).toBeTruthy();
  });

  it("doesn't say production is empty on a failed read of the environments", async () => {
    failing = [ENVIRONMENTS];
    renderPage();
    expect(await screen.findByText("Couldn’t load the environments.")).toBeTruthy();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeTruthy();
    expect(screen.queryByText(/Nothing in/)).toBeNull();
  });

  it("says the projects failed to load, with Retry and no empty state", async () => {
    failing = [PROJECTS];
    renderPage();
    expect(await screen.findByRole("heading", { name: "Couldn’t load this page", level: 2 })).toBeTruthy();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeTruthy();
    expect(screen.queryByText("Start with a project")).toBeNull();

    failing = [];
    fireEvent.click(screen.getByRole("button", { name: /^Retry/ }));
    expect(await screen.findByRole("link", { name: /shop/ })).toBeTruthy();
  });
});
