import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import ProjectServicesPage from "./page";

// A project's Services tab when one of its reads fails (P7).

vi.mock("next/navigation", () => ({
  useParams: () => ({ id: "p1" }),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
  usePathname: () => "/projects/p1/services",
}));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ isLoading: false, user: { id: "u-1", orgName: "Founders" } }),
  useOrgRole: () => "owner",
  useIsOperator: () => false,
}));
vi.mock("@/context/BreadcrumbContext", () => ({ useBreadcrumbs: () => ({ set: vi.fn() }) }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const OVERVIEW = "/api/v1/projects/p1/overview";
const SERVICES = "/api/v1/projects/p1/services";
const ENVIRONMENTS = "/api/v1/projects/p1/environments";
const SHARED = "/api/v1/projects/p1/shared-services";
const WORKSPACE = "/api/v1/shared-services";
const PREVIEWS = "/api/v1/projects/p1/previews";

let failing: string[] = [];
let previewsOn = false;

function route(path: string): unknown {
  switch (path.split("?")[0]) {
    case "/api/v1/projects/p1":
      return { id: "p1", name: "shop", description: "", user_id: "u-1", created_at: "2026-06-03T09:00:00Z", updated_at: "" };
    case ENVIRONMENTS:
      return [{ id: "e1", project_id: "p1", name: "production", color: "gray", protected: true, created_at: "", updated_at: "" }];
    case OVERVIEW:
      return { services: [], recent_deployments: [] };
    case SERVICES:
      return [{ id: "s1", name: "web", scope: "environment", project_id: "p1", environment_id: "e1", server_id: "", type: "web", source_type: "docker-image", docker_image: "nginx:1.27", runtime_status: "running", active_deployment_id: "" }];
    case "/api/v1/projects/p1/previews":
      return { data: [] };
    case "/api/v1/projects/p1/previews/settings":
      return { environment_id: previewsOn ? "e1" : null, environment_name: "", domain: null, max_per_project: 5 };
  }
  return [];
}

vi.mock("@/lib/api/client", async (orig) => {
  const actual = await orig<typeof import("@/lib/api/client")>();
  const answer = async (path: string) => {
    if (failing.includes(path.split("?")[0])) throw new actual.ApiError("boom", 500, { error: "boom" });
    return route(path);
  };
  return { ...actual, fetchAPI: answer, fetchPage: answer };
});

function renderPage() {
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <ProjectServicesPage />
    </QueryClientProvider>,
  );
}

const cardOf = async (name: string) => (await screen.findByRole("heading", { name })).closest("section")!;

beforeEach(() => {
  failing = [];
  previewsOn = false;
});

describe("project services when a read fails", () => {
  it.each([["services", SERVICES], ["environments", ENVIRONMENTS]])(
    "draws no environment card and no zero on a failed read of the %s, and Retry brings them",
    async (_name, path) => {
      failing = [path];
      renderPage();
      const failed = await screen.findByRole("heading", { name: "Couldn’t load this page", level: 2 });
      expect(failed).toBeTruthy();
      expect(screen.queryByText(/0 services/)).toBeNull();
      expect(screen.queryByText(/No services in/)).toBeNull();

      failing = [];
      fireEvent.click(within(failed.closest("section")!).getByRole("button", { name: /^Retry/ }));
      expect(await cardOf("production")).toBeTruthy();
      expect(await screen.findByRole("link", { name: "web" })).toBeTruthy();
    },
  );

  it("says a last deploy it couldn't read is unknown, not never", async () => {
    failing = [OVERVIEW];
    renderPage();
    expect(await screen.findByText("Couldn’t load each service’s last deploy.")).toBeTruthy();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeTruthy();
    const row = await screen.findByRole("row", { name: /^web/ });
    expect(within(row).getAllByRole("cell")[2].textContent).toBe("—");
    expect(screen.queryByText("never")).toBeNull();
    expect(screen.queryByText("No domain")).toBeNull();
  });

  it("gives the project services a failed row, not nothing", async () => {
    failing = [SHARED];
    renderPage();
    expect(await screen.findByText("Couldn’t load the project services.")).toBeTruthy();
    expect(await cardOf("production")).toBeTruthy();
  });

  it("gives the workspace services a failed row, not an empty sentence", async () => {
    failing = [WORKSPACE];
    renderPage();
    const card = await cardOf("Workspace services");
    expect(await within(card).findByText("Couldn’t load the workspace services.")).toBeTruthy();
    expect(within(card).queryByText(/no shared services/)).toBeNull();
    expect(within(card).queryByText(/None runs/)).toBeNull();
  });

  it("doesn't say no pull request has a preview when the previews failed to load, and Retry brings them", async () => {
    previewsOn = true;
    failing = [PREVIEWS];
    renderPage();
    const card = await cardOf("Previews");
    expect(await within(card).findByText("Couldn’t load the previews.")).toBeTruthy();
    expect(within(card).queryByText(/No open pull request/)).toBeNull();

    failing = [];
    fireEvent.click(within(card).getByRole("button", { name: /^Retry/ }));
    expect(await screen.findByText(/No open pull request has a preview yet/)).toBeTruthy();
  });

  it("gives the previews a failed row when their settings failed, not nothing", async () => {
    failing = [`${PREVIEWS}/settings`];
    renderPage();
    const card = await cardOf("Previews");
    expect(await within(card).findByText("Couldn’t load the previews.")).toBeTruthy();
    expect(within(card).queryByText(/No open pull request/)).toBeNull();
  });
});
