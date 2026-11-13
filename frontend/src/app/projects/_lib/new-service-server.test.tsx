import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { toast } from "sonner";
import { NewServiceForm } from "./new-service";

// Under Only Toft the panel's server runs no apps (D-62): the form never
// offers it, and with no other server it says so and leads to Add a server.

let role = "control_plane";
// false: the panel-host query never answers, as while it loads.
let hostAnswers = true;
const asked: string[] = [];
let servers: { id: string; name: string; ip_address: string; shared: boolean; connection: string }[] = [];
let projects: { id: string; name: string }[] = [];

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useSearchParams: () => new URLSearchParams("source=image"),
  usePathname: () => "/services/new",
}));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ user: { id: "u-1", orgName: "Founders" }, token: "t" }),
  useOrgRole: () => "owner",
  useIsOperator: () => false,
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
vi.mock("@/lib/api/client", async (importOriginal) => {
  const orig = await importOriginal<typeof import("@/lib/api/client")>();
  return {
    ...orig,
    fetchAPI: async (path: string, init?: RequestInit) => {
      asked.push(path);
      if (path === "/api/v1/projects" && init?.method === "POST")
        throw new orig.ApiError("a project with this name already exists in this workspace", 409, { data: { code: "project_name_taken" } });
      if (path === "/api/v1/projects") return projects;
      if (path === "/api/v1/instance/panel-host")
        return hostAnswers ? { role, app_networks: "isolated" } : new Promise(() => {});
      if (path === "/api/v1/servers") return servers;
      if (path.startsWith("/api/v1/resources/default-limit")) return { kind: "app", memory_mb: 512 };
      return [];
    },
  };
});

function renderForm() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <NewServiceForm projectId="" first />
    </QueryClientProvider>,
  );
}

describe("New service's server under Only Toft", () => {
  beforeEach(() => {
    role = "control_plane";
    hostAnswers = true;
    asked.length = 0;
    servers = [];
  });

  it("offers no server, and names none, until the panel host answers", async () => {
    hostAnswers = false;
    role = "apps";
    const { container } = renderForm();
    await waitFor(() => expect(asked).toContain("/api/v1/servers"));
    fireEvent.change(await screen.findByPlaceholderText("ghcr.io/you/app:1.0.0"), { target: { value: "nginx:alpine" } });
    fireEvent.change(screen.getByPlaceholderText("my-app"), { target: { value: "probe" } });
    expect(container.querySelector('input[name="server"]')).toBeNull();
    expect(screen.queryByText(/panel host/)).toBeNull();
    expect(screen.queryByText(/runs only Toft/)).toBeNull();
    for (const b of screen.getAllByRole("button", { name: "Create and deploy" })) expect((b as HTMLButtonElement).disabled).toBe(true);
  });

  it("doesn't offer the panel host, and with no server leads to Add a server", async () => {
    const { container } = renderForm();
    const add = await screen.findByRole("link", { name: "Add a server" });
    expect(add.getAttribute("href")).toBe("/servers/new");
    expect(container.querySelector('input[name="server"]')).toBeNull();
    expect(screen.getByText(/Toft’s own server runs only Toft, so the service needs a server of its own/)).toBeTruthy();
    for (const b of screen.getAllByRole("button", { name: "Create and deploy" })) expect((b as HTMLButtonElement).disabled).toBe(true);
  });

  it("preselects the app server, never the panel host", async () => {
    servers = [{ id: "s1", name: "fsn1-apps", ip_address: "203.0.113.7", shared: false, connection: "ssh" }];
    const { container } = renderForm();
    await screen.findAllByText("fsn1-apps");
    const radios = [...container.querySelectorAll<HTMLInputElement>('input[name="server"]')];
    expect(radios).toHaveLength(1);
    expect(radios[0].checked).toBe(true);
    expect(screen.queryByText("panel host")).toBeNull();
  });

  it("still offers the panel host when it runs apps", async () => {
    role = "apps";
    const { container } = renderForm();
    await screen.findAllByText("panel host");
    expect(container.querySelectorAll('input[name="server"]')).toHaveLength(1);
  });
});

describe("A first app's name the workspace already has", () => {
  beforeEach(() => {
    role = "apps";
    hostAnswers = true;
    asked.length = 0;
    servers = [];
    projects = [{ id: "p-9", name: "whoami" }];
    vi.mocked(toast.error).mockClear();
  });

  async function submitTaken() {
    renderForm();
    fireEvent.change(await screen.findByPlaceholderText("ghcr.io/you/app:1.0.0"), { target: { value: "nginx:alpine" } });
    const name = screen.getByPlaceholderText("my-app");
    fireEvent.change(name, { target: { value: "whoami" } });
    await screen.findAllByText("panel host");
    fireEvent.click(screen.getAllByRole("button", { name: "Create and deploy" })[0]);
    await waitFor(() => expect(name.getAttribute("aria-invalid")).toBe("true"));
    return name;
  }

  it("says so on the Name field with a link to the project, not in a toast", async () => {
    const name = await submitTaken();
    expect(screen.getByText(/A project named whoami already exists/)).toBeTruthy();
    expect((await screen.findByRole("link", { name: "Open it" })).getAttribute("href")).toBe("/projects/p-9");
    expect(toast.error).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(name);
    for (const b of screen.getAllByRole("button", { name: "Create and deploy" })) expect((b as HTMLButtonElement).disabled).toBe(true);

    fireEvent.change(name, { target: { value: "whoami-2" } });
    expect(name.getAttribute("aria-invalid")).toBe("false");
    expect(screen.queryByText(/already exists/)).toBeNull();
  });

  it("still says so when the project can't be found to link to", async () => {
    projects = [];
    await submitTaken();
    expect(await screen.findByText(/A project named whoami already exists\. Pick another name\./)).toBeTruthy();
    expect(screen.queryByRole("link", { name: "Open it" })).toBeNull();
  });
});
