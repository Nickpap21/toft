import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import ServiceDetailPage from "./page";

// A Deploy clicked before the environments have loaded must not skip a
// protected environment's confirm (D-55, step 8's finding): the page reads
// protection first, then confirms or deploys.

vi.mock("next/navigation", () => ({
  useParams: () => ({ id: "p1", serviceId: "s1" }),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useSearchParams: () => new URLSearchParams(search),
  usePathname: () => "/projects/p1/services/s1",
}));
vi.mock("@/context/AuthContext", () => ({ useOrgRole: () => "owner" }));
vi.mock("@/context/BreadcrumbContext", () => ({ useBreadcrumbs: () => ({ set: vi.fn() }) }));
vi.mock("sonner", () => ({ toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }) }));
// The page's own deploy(), not its tabs: the header is a bare Deploy button
// and the confirm a bare dialog.
vi.mock("./_components/service-header", () => ({
  ServiceHeader: ({ onDeploy }: { onDeploy: () => void }) => (
    <button type="button" onClick={onDeploy}>Deploy</button>
  ),
}));
vi.mock("@/app/projects/_lib/deploy-confirm", () => ({
  DeployConfirm: ({ open }: { open: boolean }) => (open ? <div role="alertdialog">confirm</div> : null),
}));
vi.mock("./_components/tabs/overview-tab", () => ({ OverviewTab: () => null }));
vi.mock("./_components/tabs/deployments-tab", () => ({ DeploymentsTab: () => null }));
vi.mock("./_components/tabs/logs-tab", () => ({ LogsTab: () => null }));
vi.mock("./_components/tabs/service-variables", () => ({ ServiceVariables: () => null }));
vi.mock("./_components/tabs/settings-tab", () => ({ SettingsTab: () => null }));
vi.mock("@/components/ui/service-drawer", () => ({ ServiceDrawer: () => null }));
vi.mock("./_components/deploy-version-dialog", () => ({ DeployVersionDialog: () => null }));

let search = "";
let releaseEnvs: (protectedEnv: boolean) => void = () => {};
const posts: string[] = [];
let serviceStatus = 200;
let deploymentsStatus = 200;
vi.mock("@/lib/api/client", async (importOriginal) => {
  const orig = await importOriginal<typeof import("@/lib/api/client")>();
  return {
    ...orig,
    fetchAPI: async (path: string, init?: RequestInit) => {
      if (init?.method === "POST") {
        posts.push(path);
        return { id: "d-new" };
      }
      if (path === "/api/v1/projects/p1/environments")
        return new Promise((resolve) => {
          releaseEnvs = (p) => resolve([{ id: "e1", project_id: "p1", name: "production", color: "gray", protected: p, preview: false }]);
        });
      if (path.startsWith("/api/v1/services/s1/deployments") && deploymentsStatus !== 200) throw new orig.ApiError("boom", deploymentsStatus, { error: "boom" });
      if (path === "/api/v1/services/s1" && serviceStatus !== 200) throw new orig.ApiError("forbidden", serviceStatus, { error: "forbidden" });
      if (path === "/api/v1/services/s1")
        return { id: "s1", name: "web", project_id: "p1", environment_id: "e1", server_id: "", source_type: "docker-image", docker_image: "nginx:1.27", runtime_status: "running", active_deployment_id: "" };
      if (path === "/api/v1/projects/p1") return { id: "p1", name: "shop" };
      return [];
    },
    streamEvents: () => () => {},
  };
});

function wrap() {
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <ServiceDetailPage />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  posts.length = 0;
  serviceStatus = 200;
  deploymentsStatus = 200;
  search = "";
});

// S1: the Deploys count that failed to load is "—", not nothing.
describe("the tab bar when the deployments read fails", () => {
  it("shows the Deploys count as a dash", async () => {
    deploymentsStatus = 500;
    wrap();
    const tab = await screen.findByRole("button", { name: /^Deploys/ });
    await waitFor(() => expect(tab.textContent).toBe("Deploys—"));
  });

  it("shows no count while none are known to be there", async () => {
    wrap();
    const tab = await screen.findByRole("button", { name: /^Deploys/ });
    await new Promise((r) => setTimeout(r, 0));
    expect(tab.textContent).toBe("Deploys");
  });
});

// S11: a read Retry can't change links back to Services instead.
describe("service page whose read is refused or gone", () => {
  it.each([403, 404])("a %i links back to Services, with no Retry", async (status) => {
    serviceStatus = status;
    wrap();
    expect((await screen.findByRole("link", { name: "Back to Services" })).getAttribute("href")).toBe("/projects/p1/services");
    expect(screen.queryByRole("button", { name: /^Retry/ })).toBeNull();
  });
});

describe("service page Deploy before the environments load", () => {
  it("confirms in a protected environment, deploying nothing until then", async () => {
    wrap();
    fireEvent.click(await screen.findByRole("button", { name: "Deploy" }));
    await new Promise((r) => setTimeout(r, 0));
    expect(posts).toEqual([]);
    releaseEnvs(true);
    expect(await screen.findByRole("alertdialog")).toBeTruthy();
    expect(posts).toEqual([]);
  });

  it("deploys once protection is known not to apply", async () => {
    wrap();
    fireEvent.click(await screen.findByRole("button", { name: "Deploy" }));
    releaseEnvs(false);
    await waitFor(() => expect(posts).toEqual(["/api/v1/services/s1/deployments"]));
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });
});

describe("service page", () => {
  it("announces its loading skeleton", () => {
    wrap();
    expect(screen.getByRole("status").textContent).toBe("Loading the service");
  });

  // WCAG 2.4.2: several open services stay apart in the browser's tabs.
  it.each([
    ["", "Overview · web · Toft"],
    ["tab=deployments", "Deploys · web · Toft"],
    ["tab=deployments&deployment=d1", "Deployment · web · Toft"],
    ["tab=logs", "Logs · web · Toft"],
    ["tab=variables", "Variables · web · Toft"],
    ["tab=secrets", "Variables · web · Toft"],
    ["tab=settings", "Settings · web · Toft"],
  ])("titles the browser tab for ?%s", async (query, title) => {
    search = query;
    wrap();
    await waitFor(() => expect(document.title).toBe(title));
  });
});
