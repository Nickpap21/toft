import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { toast } from "sonner";
import { beforeEach, describe, expect, it, vi } from "vitest";
import ProjectSettingsPage from "./page";

// The project's Settings (board ReworkProjectSettings): the index in page
// order, Issues after Deploy on push (the founders, 2026-11-05); a save
// bar only once something changed; the last push; every member's key for
// an owner; Delete behind a typed-name dialog.

vi.mock("next/navigation", () => ({
  useParams: () => ({ id: "p1" }),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  usePathname: () => "/projects/p1/settings",
}));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ isLoading: false, user: { id: "u-1", name: "alex", orgName: "Founders" } }),
  useOrgRole: () => "owner",
  useIsOperator: () => false,
}));
vi.mock("@/context/BreadcrumbContext", () => ({
  useBreadcrumbs: () => ({ set: vi.fn() }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const sent: { path: string; body?: string }[] = [];
const now = Date.now();
let obs: unknown = {};

// Every read the page and its cards make, by path; [] for the rest.
function route(path: string): unknown {
  const p = path.split("?")[0];
  switch (p) {
    case "/api/v1/projects/p1":
      return { id: "p1", name: "storefront", description: "Customer-facing shop", user_id: "u-1", created_at: "2026-06-03T09:00:00Z", updated_at: "" };
    case "/api/v1/projects/p1/environments":
      return [
        { id: "e1", project_id: "p1", name: "production", color: "gray", protected: true, created_at: "", updated_at: "" },
        { id: "e2", project_id: "p1", name: "staging", color: "amber", protected: false, created_at: "", updated_at: "" },
      ];
    case "/api/v1/projects/p1/overview":
      return { services: [] };
    case "/api/v1/projects/p1/webhook":
      return {
        url: "https://toft.example/api/v1/webhooks/github/p1",
        secret: "whsec",
        last_push_at: new Date(now - 20 * 60_000).toISOString(),
        last_push_repo: "founders/storefront",
      };
    case "/api/v1/org/members":
      return [{ user_id: "u-1", user_name: "alex", user_email: "alex@toft.test", role: "owner" }];
    case "/api/v1/projects/p1/api-keys":
      return {
        data: [
          { id: "k1", name: "ci-deploy", prefix: "paas_1", scopes: ["deploy", "variables"], owner_id: "u-2", owner_name: "sam", last_used_at: null, created_at: "", expires_at: null },
          { id: "k2", name: "claude-code", prefix: "paas_2", scopes: ["code_read", "code_write"], owner_id: "u-1", owner_name: "alex", last_used_at: null, created_at: "", expires_at: null },
        ],
      };
    case "/api/v1/projects/p1/previews/settings":
      return { environment_id: "e2", environment_name: "staging", domain: null, max_per_project: 6 };
    case "/api/v1/projects/p1/previews":
      return { data: [] };
    case "/api/v1/agents/policy":
      return { cells: [], projects: [] };
    case "/api/v1/agents/connections":
      return [];
    case "/api/v1/projects/p1/observability":
      return obs;
    case "/api/v1/workspace/services":
      return [
        { id: "g1", name: "grafana", scope: "environment", project_id: "p2", environment_id: "e9", server_id: "", runtime_status: "running", addon_slug: "grafana" },
      ];
    case "/api/v1/projects/p1/issue-settings":
      return { key: "STORE", digits: 4, inherit_digits: true, workspace_digits: 4, aliases: [], next_key: "STORE-0043", hidden_type_ids: [], default_type_id: null, failed_deploy_issues: false };
  }
  return [];
}

// The reads a test has answer 500, by path.
let failing: string[] = [];
vi.mock("@/lib/api/client", async (orig) => {
  const actual = await orig<typeof import("@/lib/api/client")>();
  const read = (path: string) => {
    if (failing.includes(path.split("?")[0])) throw new actual.ApiError("boom", 500, { error: "boom" });
    return route(path);
  };
  return {
    ...actual,
    fetchAPI: async (path: string, init?: RequestInit) => {
      if (init?.method && init.method !== "GET") {
        sent.push({ path, body: init.body as string | undefined });
        return route(path);
      }
      return read(path);
    },
    fetchPage: async (path: string) => read(path),
  };
});

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <ProjectSettingsPage />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  sent.length = 0;
  obs = {};
  failing = [];
  // jsdom has none; the index's marking isn't under test here.
  vi.stubGlobal("IntersectionObserver", class { observe() {} disconnect() {} });
});

describe("Project settings", () => {
  it("indexes the sections in page order, Issues after Deploy on push", async () => {
    renderPage();
    const nav = await screen.findByRole("navigation", { name: "On this page" });
    await within(nav).findByRole("button", { name: /API keys/ });
    expect(within(nav).getAllByRole("button").map((b) => b.textContent)).toEqual([
      "General", "Previews", "Agents", "Deploy on push", "Issues", "Logs and metrics", "API keys2", "Delete storefront",
    ]);
  });

  it("shows General's save bar only once something changed", async () => {
    renderPage();
    const name = await screen.findByLabelText("Name");
    expect(screen.queryByText(/Unsaved change to Name/)).toBeNull();
    fireEvent.change(name, { target: { value: "shop" } });
    expect(screen.getByText("Unsaved change to Name")).toBeTruthy();
    fireEvent.change(name, { target: { value: "" } });
    expect(screen.getAllByText("A project needs a name.").length).toBeGreaterThan(0);
    fireEvent.click(screen.getAllByRole("button", { name: "Cancel" })[0]);
    expect((screen.getByLabelText("Name") as HTMLInputElement).value).toBe("storefront");
  });

  it("copies the project's ID from General", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Copy project ID" }));
    expect(writeText).toHaveBeenCalledWith("p1");
    await vi.waitFor(() => expect(toast.success).toHaveBeenCalledWith("Project ID copied"));
  });

  it("keeps the ID's capitals when the copy fails", async () => {
    Object.defineProperty(navigator, "clipboard", { value: { writeText: vi.fn().mockRejectedValue(new Error("denied")) }, configurable: true });
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Copy project ID" }));
    await vi.waitFor(() => expect(toast.error).toHaveBeenCalledWith("Couldn't copy the project ID"));
  });

  it("names the last push and every member's key", async () => {
    renderPage();
    expect(await screen.findByText("last push 20m ago · founders/storefront")).toBeTruthy();
    const keys = await screen.findByRole("table", { name: "API keys scoped to storefront" });
    expect(within(keys).getAllByRole("row").slice(1).map((r) => r.textContent)).toEqual([
      "ci-deployDeploy and change variablessam",
      "claude-codePush codealex",
    ]);
    expect(screen.getByText("2 scoped to storefront")).toBeTruthy();
  });

  it("saves At once from the Previews card, refusing 0", async () => {
    renderPage();
    const max = await screen.findByLabelText("At once");
    expect((max as HTMLInputElement).value).toBe("6");
    expect(screen.getByText("Stopped ones count. Past 6, a new one waits until one is removed; one waiting for approval doesn’t count.")).toBeTruthy();
    fireEvent.change(max, { target: { value: "0" } });
    const previews = screen.getByRole("form", { name: "Previews" });
    expect(within(previews).getByRole("switch", { name: "A preview for each open pull request" })).toBeTruthy();
    expect(within(previews).getByRole("button", { name: "Save" }).hasAttribute("disabled")).toBe(true);
    fireEvent.change(max, { target: { value: "8" } });
    expect(within(previews).getByText("Unsaved change to At once")).toBeTruthy();
    fireEvent.click(within(previews).getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(sent.find((s) => s.path.endsWith("/previews/settings"))).toBeTruthy());
    expect(JSON.parse(sent.find((s) => s.path.endsWith("/previews/settings"))!.body!)).toEqual({
      environment_id: "e2", domain: null, max_per_project: 8,
    });
  });

  // The workspace's Grafana may be a service in another project's
  // environment (QA, v0.38.0: it read "not found").
  it("finds the workspace's Grafana wherever it runs, and says what to do when it's gone", async () => {
    obs = { id: "o1", org_id: "org", grafana_service_id: "g1", prometheus_service_id: "gone", created_at: "", updated_at: "" };
    renderPage();
    const list = await screen.findByRole("list", { name: "Logs and metrics services" });
    const [grafana, prometheus] = await vi.waitFor(() => {
      const rows = within(list).getAllByRole("listitem");
      expect(rows[1].textContent).toContain("Running");
      return [rows[1], rows[0]];
    });
    expect(grafana.textContent).not.toMatch(/not found|Logs and metrics/);
    expect(within(prometheus).getByRole("link", { name: "Not found: set it on Logs and metrics" }).getAttribute("href")).toBe("/shared?tab=logs");
  });

  it("deletes only after the name is typed in the dialog", async () => {
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Delete storefront…" }));
    const dialog = await screen.findByRole("dialog");
    const del = within(dialog).getByRole("button", { name: "Delete storefront" });
    expect(del.hasAttribute("disabled")).toBe(true);
    fireEvent.change(within(dialog).getByLabelText(/to confirm/), { target: { value: "storefront" } });
    expect(del.hasAttribute("disabled")).toBe(false);
  });

  // P7: a card whose read failed says so; it isn't drawn as off, none or a
  // skeleton that never ends.
  const cardOf = async (name: string) => (await screen.findByRole("heading", { name })).closest<HTMLElement>("section, form")!;
  const P = "/api/v1/projects/p1";

  it("doesn't say previews are off when their settings failed to load, and Retry brings them", async () => {
    failing = [`${P}/previews/settings`];
    renderPage();
    expect(await screen.findByText("Couldn’t load the preview settings.")).toBeTruthy();
    const card = await cardOf("Previews");
    expect(within(card).queryByText(/Off:/)).toBeNull();
    expect(within(card).queryByRole("switch")).toBeNull();

    failing = [];
    fireEvent.click(within(card).getByRole("button", { name: /^Retry/ }));
    expect(await screen.findByRole("switch", { name: "A preview for each open pull request" })).toBeTruthy();
  });

  it("gives Deploy on push a failed row, not a skeleton that never ends", async () => {
    failing = [`${P}/webhook`];
    renderPage();
    const card = await cardOf("Deploy on push");
    expect(await within(card).findByText("Couldn’t load the webhook.")).toBeTruthy();
    expect(within(card).getByRole("button", { name: /^Retry/ })).toBeTruthy();
    expect(within(card).queryByRole("status")).toBeNull();
  });

  it("doesn't say no key is scoped when the keys failed to load", async () => {
    failing = [`${P}/api-keys`];
    renderPage();
    const card = await cardOf("API keys");
    expect(await within(card).findByText("Couldn’t load the API keys.")).toBeTruthy();
    expect(within(card).getByRole("button", { name: /^Retry/ })).toBeTruthy();
    expect(within(card).queryByText(/No key is scoped/)).toBeNull();
  });

  it("doesn't say logs and metrics are none yet when their read failed", async () => {
    failing = [`${P}/observability`];
    renderPage();
    const card = await cardOf("Logs and metrics");
    expect(await within(card).findByText("Couldn’t load the logs and metrics services.")).toBeTruthy();
    expect(within(card).queryByText("none yet")).toBeNull();
    expect(within(card).queryByText(/Add Loki/)).toBeNull();
  });

  it("says what it couldn't read in the Delete dialog, never none", async () => {
    failing = [`${P}/overview`, `${P}/api-keys`, `${P}/environments`];
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Delete storefront…" }));
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText("Couldn’t load the domains.")).toBeTruthy();
    expect(await within(dialog).findByText("Couldn’t load the API keys.")).toBeTruthy();
    expect(await within(dialog).findByText("Couldn’t load the environments.")).toBeTruthy();
    expect(within(dialog).queryByText("none")).toBeNull();
    expect(screen.queryByText(/0 environments/)).toBeNull();
  });

  it("doesn't draw every agent rule as Ask when the rules failed to load, and Retry brings them", async () => {
    failing = ["/api/v1/agents/policy"];
    renderPage();
    const card = await cardOf("Agents");
    expect(await within(card).findByText("Couldn’t load the agent rules.")).toBeTruthy();
    expect(within(card).queryByRole("table")).toBeNull();
    expect(within(card).queryByText(/rules, as Settings/)).toBeNull();

    failing = [];
    fireEvent.click(within(card).getByRole("button", { name: /^Retry/ }));
    expect(await screen.findByRole("table", { name: "Agent rules for storefront" })).toBeTruthy();
  });

  it("won't save a change that removes previews it couldn't list, and Retry lets it", async () => {
    failing = [`${P}/previews`];
    renderPage();
    const form = await cardOf("Previews");
    await within(form).findByText("— open");
    const on = await within(form).findByRole("switch", { name: "A preview for each open pull request" });
    await vi.waitFor(() => expect(on.hasAttribute("disabled")).toBe(false));
    fireEvent.click(on);
    expect(await within(form).findByText("Couldn’t load the open previews.")).toBeTruthy();
    expect(within(form).getByRole("button", { name: "Save" }).hasAttribute("disabled")).toBe(true);

    failing = [];
    fireEvent.click(within(form).getByRole("button", { name: /^Retry/ }));
    await vi.waitFor(() => expect(within(form).getByRole("button", { name: "Save" }).hasAttribute("disabled")).toBe(false));
  });
});
