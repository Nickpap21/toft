import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import ProjectPage from "./page";

// A project's Overview when one of its reads fails (P7): the card says so
// with a Retry, and none of them is drawn as "nothing here".

vi.mock("next/navigation", () => ({
  useParams: () => ({ id: "p1" }),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
  usePathname: () => "/projects/p1",
}));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ token: "t", isLoading: false, user: { id: "u-1", orgName: "Founders" } }),
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
const NEEDS = "/api/v1/inbox/needs-you";
const PREVIEWS = "/api/v1/projects/p1/previews";
const URGENT_COUNTS = "/api/v1/projects/p1/issue-counts";

let failing: string[] = [];
let status = "running";
let hanging: string[] = [];
let repoAnswers: unknown = [];
const GIT = "/api/v1/git";
const REPOS = "/api/v1/projects/p1/repositories";

function route(path: string): unknown {
  switch (path.split("?")[0]) {
    case GIT:
      return { enabled: repoAnswers !== null, ssh_port: 2222 };
    case REPOS:
      return repoAnswers ?? [];
    case "/api/v1/projects/p1":
      return { id: "p1", name: "shop", description: "", user_id: "u-1", created_at: "2026-06-03T09:00:00Z", updated_at: "", open_issues: 2 };
    case ENVIRONMENTS:
      return [
        { id: "e1", project_id: "p1", name: "production", color: "gray", protected: true, created_at: "", updated_at: "" },
        { id: "e2", project_id: "p1", name: "staging", color: "amber", protected: false, created_at: "", updated_at: "" },
      ];
    case OVERVIEW:
      return { services: [], recent_deployments: [] };
    case SERVICES:
      return [{ id: "s1", name: "web", scope: "environment", project_id: "p1", environment_id: "e1", server_id: "", type: "web", source_type: "docker-image", runtime_status: status, active_deployment_id: "" }];
    case URGENT_COUNTS:
      return { backlog: 0, todo: 0, in_progress: 0, in_review: 0, done: 0, canceled: 0 };
    case "/api/v1/projects/p1/cycles":
      return { enabled: false, cycles: [] };
    case PREVIEWS:
    case "/api/v1/projects/p1/issues":
      return { data: [] };
  }
  return [];
}

vi.mock("@/lib/api/client", async (orig) => {
  const actual = await orig<typeof import("@/lib/api/client")>();
  const answer = async (path: string) => {
    if (hanging.includes(path.split("?")[0])) return new Promise(() => {});
    if (failing.includes(path.split("?")[0])) throw new actual.ApiError("boom", 500, { error: "boom" });
    return route(path);
  };
  return { ...actual, fetchAPI: answer, fetchPage: answer };
});

function renderPage() {
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <ProjectPage />
    </QueryClientProvider>,
  );
}

const cardOf = async (name: string) => (await screen.findByRole("heading", { name })).closest("section")!;

beforeEach(() => {
  failing = [];
  status = "running";
  hanging = [];
  repoAnswers = null;
});

describe("Connected work", () => {
  it("claims nothing about pull requests until the repositories are read", async () => {
    repoAnswers = [];
    hanging = [REPOS];
    renderPage();
    const card = await cardOf("Connected work");
    expect(within(card).getByText("Open pull requests · —")).toBeTruthy();
    expect(within(card).queryByText("None open.")).toBeNull();
  });

  it("says the repositories failed, with Retry that reads them again", async () => {
    repoAnswers = [];
    failing = [REPOS];
    renderPage();
    const card = await cardOf("Connected work");
    expect(await within(card).findByText("Couldn’t load the repositories.")).toBeTruthy();
    failing = [];
    fireEvent.click(within(card).getByRole("button", { name: /^Retry/ }));
    expect(await within(card).findByText("None open.")).toBeTruthy();
    expect(within(card).queryByText("Couldn’t load the repositories.")).toBeNull();
  });

  it("with git off, has no repositories, and says none open", async () => {
    repoAnswers = null;
    renderPage();
    const card = await cardOf("Connected work");
    expect(await within(card).findByText("None open.")).toBeTruthy();
  });
});

describe("a Retry on the project page keeps the row and its focus", () => {
  it("on a composite read", async () => {
    failing = [SERVICES];
    renderPage();
    const card = await cardOf("Services");
    const retry = await within(card).findByRole("button", { name: /^Retry/ });
    retry.focus();
    failing = [];
    hanging = [SERVICES];
    fireEvent.click(retry);
    await waitFor(() => expect(within(card).getByRole("button", { name: /^Retry/ })).toBe(retry));
    await new Promise((r) => setTimeout(r, 0));
    expect(retry.isConnected).toBe(true);
    expect(retry).toHaveFocus();
  });

  it("on a row of its own", async () => {
    failing = [SHARED];
    renderPage();
    const failed = await screen.findByText("Couldn’t load the project services.");
    const retry = within(failed.parentElement!).getByRole("button", { name: /^Retry/ });
    retry.focus();
    failing = [];
    hanging = [SHARED];
    fireEvent.click(retry);
    await new Promise((r) => setTimeout(r, 0));
    expect(retry.isConnected).toBe(true);
    expect(retry).toHaveFocus();
    expect(screen.getByText("Couldn’t load the project services.")).toBeTruthy();
  });
});

describe("project overview when a read fails", () => {
  it("doesn't call a failed read of the deploys nothing deployed, and Retry brings the answer", async () => {
    failing = [OVERVIEW];
    renderPage();
    const card = await cardOf("Recent deploys");
    expect(await within(card).findByText("Couldn’t load the recent deploys.")).toBeTruthy();
    expect(within(card).queryByText(/Nothing deployed/)).toBeNull();
    expect(await screen.findByRole("link", { name: /^web/ })).toBeTruthy();

    failing = [];
    fireEvent.click(within(card).getByRole("button", { name: /^Retry/ }));
    expect(await within(card).findByText("Nothing deployed yet.")).toBeTruthy();
  });

  it("shows no count and no health line on a failed read of the services", async () => {
    failing = [SERVICES];
    renderPage();
    const card = await cardOf("Services");
    expect(await within(card).findByText("Couldn’t load the services.")).toBeTruthy();
    expect(within(card).getByRole("button", { name: /^Retry/ })).toBeTruthy();
    expect(within(card).getByText("— in all environments")).toBeTruthy();
    expect(within(card).queryByText(/^0 in/)).toBeNull();
    expect(within(card).queryByText(/No services in/)).toBeNull();
    expect(screen.queryByText("none failing")).toBeNull();
    expect(screen.queryByText("no services yet")).toBeNull();
    expect(screen.queryByText("No services")).toBeNull();
  });

  it("doesn't call the services none when the environments failed to load", async () => {
    failing = [ENVIRONMENTS];
    renderPage();
    const services = await cardOf("Services");
    expect(await within(services).findByText("Couldn’t load the services.")).toBeTruthy();
    expect(within(services).queryByText(/No services in/)).toBeNull();
    const envs = await cardOf("Environments");
    expect(within(envs).getByText("Couldn’t load the environments.")).toBeTruthy();
    expect(within(envs).getByText("—")).toBeTruthy();
  });

  it("keeps Needs attention on screen when its read fails, without saying nothing needs you", async () => {
    failing = [NEEDS];
    renderPage();
    const card = await cardOf("Needs attention");
    expect(await within(card).findByText("Couldn’t load what needs attention.")).toBeTruthy();
    expect(within(card).getByRole("button", { name: /^Retry/ })).toBeTruthy();
    expect(within(card).queryByText(/needs you/)).toBeNull();
  });

  it("gives the workspace services a failed row, not an empty sentence", async () => {
    failing = [WORKSPACE];
    renderPage();
    const card = await cardOf("Workspace services");
    expect(await within(card).findByText("Couldn’t load the workspace services.")).toBeTruthy();
    expect(within(card).queryByText(/no shared services/)).toBeNull();
    expect(within(card).queryByText(/None runs/)).toBeNull();
  });

  it("says so when the project services or the previews failed to load", async () => {
    failing = [SHARED, PREVIEWS];
    renderPage();
    expect(await within(await cardOf("Services")).findByText("Couldn’t load the project services.")).toBeTruthy();
    expect(await within(await cardOf("Environments")).findByText("Couldn’t load the previews.")).toBeTruthy();
  });

  it("doesn't count urgent issues it couldn't read as none", async () => {
    failing = [URGENT_COUNTS];
    renderPage();
    const card = await cardOf("Connected work");
    expect(await within(card).findByText("Urgent issues · — of 2 open")).toBeTruthy();
    expect(within(card).queryByText(/0 of 2/)).toBeNull();
    expect(within(card).queryByText("None urgent.")).toBeNull();
    expect(within(card).getByText("Couldn’t load the urgent issues.")).toBeTruthy();
  });
});

// P11: the tile takes the project's health, as Projects and the sidebar do.
describe("project overview tile", () => {
  it("is red over a failing service", async () => {
    status = "error";
    renderPage();
    const tile = await screen.findByText("S");
    await waitFor(() => expect(tile.className).toContain("bg-status-failed-soft"));
    expect(tile.className).not.toContain("bg-status-running-soft");
  });

  it("is green while its services run", async () => {
    renderPage();
    const tile = await screen.findByText("S");
    await waitFor(() => expect(tile.className).toContain("bg-status-running-soft"));
  });
});

// "1 failing" says which, and takes you there.
describe("the health line", () => {
  it("names each failing service, with a link to it", async () => {
    status = "unhealthy";
    renderPage();
    const line = (await screen.findByText("1 failing:")).closest("p")!;
    const web = within(line).getByRole("link", { name: "web" });
    expect(web.getAttribute("href")).toBe("/projects/p1/services/s1");
  });

  it("names none while every service runs", async () => {
    renderPage();
    const line = (await screen.findByText("none failing")).closest("p")!;
    expect(within(line).queryByRole("link")).toBeNull();
  });
});
