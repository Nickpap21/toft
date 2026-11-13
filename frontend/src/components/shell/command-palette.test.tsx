import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ThemeProvider } from "@/components/theme-provider";
import { STATUS } from "@/components/issues/meta";
import { CommandPaletteProvider } from "./command-palette-provider";
import { SearchButton } from "./search-button";
import { SidebarProvider } from "./sidebar-state";

const push = vi.fn();
let pathname = "/dashboard";
vi.mock("next/navigation", () => ({ useRouter: () => ({ push }), usePathname: () => pathname }));

const trigger = vi.fn(() => Promise.resolve({ id: "d1" }));
vi.mock("@/lib/api/deployment.service", () => ({
  deploymentService: { trigger: (...args: unknown[]) => trigger(...(args as [])) },
}));

const switchOrg = vi.fn(() => Promise.resolve());
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({
    token: "t",
    user: { orgId: "o1", orgName: "Founders", role: "owner", orgs: [{ org_id: "o1", name: "Founders", role: "owner" }, { org_id: "o2", name: "Client A", role: "developer" }] },
    switchOrg,
    logout: vi.fn(),
  }),
  useIsOperator: () => false,
}));

vi.mock("@/lib/api/project.service", () => ({
  projectService: { list: () => Promise.resolve([{ id: "p1", name: "storefront", description: "" }]) },
}));
const listForWorkspace = vi.fn(() =>
  Promise.resolve([
    { id: "s1", name: "web", project_id: "p1", environment_id: "e1", scope: "environment", runtime_status: "running", source_type: "git", addon_slug: "", docker_image: "", git_repo_url: "", git_branch: "main", environment_name: "production" },
    { id: "s2", name: "grafana", project_id: "", environment_id: "", scope: "org", runtime_status: "idle", source_type: "docker-image", addon_slug: "grafana", docker_image: "", git_repo_url: "" },
  ]),
);
vi.mock("@/lib/api/service.service", () => ({
  serviceService: { listForWorkspace: () => listForWorkspace() },
}));
const issues = [
  { project_id: "p1", project: "storefront", key: "STO-120", number: 120, title: "Deploy hangs when healthcheck path 404s", status: "todo", priority: "high" },
  { project_id: "p1", project: "storefront", key: "STO-104", number: 104, title: "Support SEPA at checkout", status: "in_review", priority: "high" },
];
const pulls = [{ project_id: "p1", project: "storefront", repository: "storefront", number: 41, title: "Rate-limit login attempts", state: "merged" }];
const docs = [{ id: "d1", project_id: "p1", project: "storefront", title: "Restore Postgres from backup", snippet: "Use this when…" }];
const search = vi.fn((q: string, only?: string) => {
  const hit = (t: { title: string }) => t.title.toLowerCase().includes(q.toLowerCase());
  return Promise.resolve({ issues: only === "pulls" ? [] : issues.filter(hit), pulls: only === "issues" ? [] : pulls.filter(hit), docs: only ? [] : docs.filter(hit) });
});
vi.mock("@/lib/api/issue.service", () => ({ searchWorkspace: (q: string, only?: string) => search(q, only) }));
let prodProtected = false;
vi.mock("@/lib/api/environment.service", () => ({
  environmentService: {
    listWorkspace: () => Promise.resolve([{ id: "e1", project_id: "p1", name: "production", color: "gray", protected: prodProtected, preview: false }]),
  },
}));
vi.mock("@/lib/api/server.service", () => ({
  serverService: { list: () => Promise.resolve([{ id: "v1", name: "fsn1-prod-01", ip_address: "203.0.113.10" }]) },
}));

function renderShell() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <ThemeProvider>
        <SidebarProvider>
          <CommandPaletteProvider>
            <SearchButton />
            <div className="xterm">
              <textarea aria-label="Terminal input" />
            </div>
          </CommandPaletteProvider>
        </SidebarProvider>
      </ThemeProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  pathname = "/dashboard";
  prodProtected = false;
  trigger.mockClear();
  push.mockClear();
  switchOrg.mockClear();
  listForWorkspace.mockClear();
});

describe("command palette", () => {
  it.each([["{Control>}k{/Control}"], ["{Meta>}k{/Meta}"]])("opens with %s, named and described", async (keys) => {
    renderShell();
    await userEvent.keyboard(keys);
    const dialog = await screen.findByRole("dialog", { name: "Command palette" });
    expect(dialog).toHaveAccessibleDescription(/find a project, service, server, issue or pull request/);
    expect(screen.getByRole("combobox", { name: "Search or run a command" })).toHaveFocus();
  });

  it("lists only issues after #, and opens one on Enter (D-81)", async () => {
    renderShell();
    await userEvent.click(screen.getByRole("button", { name: /Search or run a command/ }));
    const input = await screen.findByRole("combobox", { name: "Search or run a command" });
    await userEvent.type(input, "#deploy");
    await screen.findByRole("option", { name: /STO-120.*Deploy hangs/ });
    expect(search).toHaveBeenLastCalledWith("deploy", "issues");
    expect(screen.queryByRole("option", { name: /Go to Home/ })).toBeNull();
    await userEvent.keyboard("{Enter}");
    expect(push).toHaveBeenCalledWith("/projects/p1/issues/120");
  });

  it("tints an issue's status as every other surface does", async () => {
    renderShell();
    await userEvent.click(screen.getByRole("button", { name: /Search or run a command/ }));
    await userEvent.type(await screen.findByRole("combobox", { name: "Search or run a command" }), "#SEPA");
    expect(await screen.findByText("In review")).toHaveClass(STATUS.in_review.tint);
  });

  it("finds pages in a plain search, and opens one (D-86)", async () => {
    renderShell();
    await userEvent.click(screen.getByRole("button", { name: /Search or run a command/ }));
    const input = await screen.findByRole("combobox", { name: "Search or run a command" });
    await userEvent.type(input, "restore");
    await userEvent.click(await screen.findByRole("option", { name: /Restore Postgres from backup/ }));
    expect(push).toHaveBeenCalledWith("/projects/p1/docs/d1");
  });

  it("filters as you type and navigates on Enter", async () => {
    renderShell();
    await userEvent.click(screen.getByRole("button", { name: /Search or run a command/ }));
    const input = await screen.findByRole("combobox", { name: "Search or run a command" });
    await userEvent.type(input, "fsn1");
    await screen.findByRole("option", { name: /fsn1-prod-01/ });
    expect(screen.queryByRole("option", { name: /Go to Home/ })).toBeNull();
    await waitFor(() => expect(screen.getByText("1 result")).toBeInTheDocument());
    await userEvent.keyboard("{Enter}");
    expect(push).toHaveBeenCalledWith("/servers/v1");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("searches every service of the workspace from one request", async () => {
    renderShell();
    await userEvent.click(screen.getByRole("button", { name: /Search or run a command/ }));
    const input = await screen.findByRole("combobox", { name: "Search or run a command" });
    await userEvent.type(input, "grafana");
    await screen.findByRole("option", { name: /^grafana/ });
    expect(listForWorkspace).toHaveBeenCalledTimes(1);
  });

  it("moves with the arrow keys", async () => {
    renderShell();
    await userEvent.keyboard("{Control>}k{/Control}");
    await userEvent.type(await screen.findByRole("combobox"), "go to");
    await userEvent.keyboard("{ArrowDown}{Enter}");
    // "Go to Home" is first; ArrowDown moves to "Go to Inbox".
    expect(push).toHaveBeenCalledWith("/inbox");
  });

  it("runs a non-navigation action", async () => {
    renderShell();
    await userEvent.keyboard("{Control>}k{/Control}");
    await userEvent.type(await screen.findByRole("combobox"), "Client A");
    await userEvent.keyboard("{Enter}");
    expect(switchOrg).toHaveBeenCalledWith("o2");
  });

  it("says when nothing matches", async () => {
    renderShell();
    await userEvent.keyboard("{Control>}k{/Control}");
    await userEvent.type(await screen.findByRole("combobox"), "zzzzqqq");
    expect(await screen.findByText("No results for “zzzzqqq”")).toBeInTheDocument();
  });

  it("closes on Escape and returns focus to the trigger", async () => {
    renderShell();
    const trigger = screen.getByRole("button", { name: /Search or run a command/ });
    await userEvent.click(trigger);
    await screen.findByRole("dialog");
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(trigger).toHaveFocus();
  });

  it("leaves Ctrl+K to the terminal", async () => {
    renderShell();
    await userEvent.click(screen.getByRole("textbox", { name: "Terminal input" }));
    await userEvent.keyboard("{Control>}k{/Control}");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("deploys at once from a deploy row", async () => {
    renderShell();
    await userEvent.keyboard("{Control>}k{/Control}");
    await userEvent.type(await screen.findByRole("combobox"), "deploy web");
    expect(await screen.findByRole("option", { name: /Deploy storefront \/ web from main/ })).toHaveAttribute("aria-selected", "true");
    await userEvent.keyboard("{Enter}");
    await waitFor(() => expect(trigger).toHaveBeenCalledWith("s1", {}, false));
    expect(push).not.toHaveBeenCalled();
  });

  it("never deploys a protected environment's service at once: Enter opens its confirm", async () => {
    prodProtected = true;
    renderShell();
    await userEvent.keyboard("{Control>}k{/Control}");
    await userEvent.type(await screen.findByRole("combobox"), "deploy web");
    expect(await screen.findByRole("option", { name: /Deploy storefront \/ web from main…/ })).toHaveAttribute("aria-selected", "true");
    await userEvent.keyboard("{Enter}");
    await waitFor(() => expect(push).toHaveBeenCalledWith("/projects/p1/services/s1?confirm=deploy"));
    expect(trigger).not.toHaveBeenCalled();
  });

  it("opens scoped to the page's project, and Tab removes the scope", async () => {
    pathname = "/projects/p1/services/s1";
    renderShell();
    await userEvent.keyboard("{Control>}k{/Control}");
    await screen.findByRole("option", { name: /Deploy storefront \/ web/ });
    expect(screen.getByText("Across the project")).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /fsn1-prod-01/ })).toBeNull();
    await userEvent.keyboard("{Tab}");
    await screen.findByRole("option", { name: /fsn1-prod-01/ });
    expect(screen.getByRole("combobox")).toHaveFocus();
  });

  it("Tab scopes to the selected row's project", async () => {
    renderShell();
    await userEvent.keyboard("{Control>}k{/Control}");
    await userEvent.type(await screen.findByRole("combobox"), "storefront");
    await userEvent.keyboard("{Tab}");
    await screen.findByText("Across the project");
    expect(screen.getByRole("combobox")).toHaveValue("");
    await userEvent.keyboard("{Backspace}");
    await waitFor(() => expect(screen.queryByText("Across the project")).toBeNull());
  });
});
