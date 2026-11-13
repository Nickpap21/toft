import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { NewServiceForm } from "./new-service";

// F1 and the rest of the form's reads: one that failed is never drawn as an
// empty catalog, a form with only the panel host in it, or a "…" that never
// ends. The form can't create what it couldn't read about.

let search = "source=catalog";
const failing = new Set<string>();

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useSearchParams: () => new URLSearchParams(search),
  usePathname: () => "/projects/p1/services/new",
}));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ user: { id: "u-1", orgName: "Founders" }, token: "t" }),
  useOrgRole: () => "owner",
  useIsOperator: () => false,
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }));

const postgres = { slug: "postgres", name: "Postgres", kind: "image", category: "database", docker_image: "postgres:16", default_port: 5432, env_var_specs: [], volumes: [], network_alias: "postgres" };

vi.mock("@/lib/api/client", async (importOriginal) => {
  const orig = await importOriginal<typeof import("@/lib/api/client")>();
  return {
    ...orig,
    fetchAPI: async (path: string) => {
      if ([...failing].some((f) => path.startsWith(f))) throw new orig.ApiError("boom: pq internal detail", 500, { error: "boom" });
      if (path === "/api/v1/catalog") return [postgres];
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

async function fillImage() {
  fireEvent.change(await screen.findByPlaceholderText("ghcr.io/you/app:1.0.0"), { target: { value: "nginx:alpine" } });
  fireEvent.change(screen.getByPlaceholderText("my-app"), { target: { value: "probe" } });
}
const createOff = () => {
  for (const b of screen.getAllByRole("button", { name: "Create and deploy" })) expect((b as HTMLButtonElement).disabled).toBe(true);
};

beforeEach(() => {
  search = "source=catalog";
  failing.clear();
});

describe("The catalog", () => {
  it("that won't load says so with Retry, not an empty catalog", async () => {
    failing.add("/api/v1/catalog");
    renderForm();
    expect(await screen.findByText("Couldn’t load the catalog.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
    expect(screen.queryByRole("group", { name: "Category" })).toBeNull();
    expect(screen.queryByRole("group", { name: "Catalog entries" })).toBeNull();
  });

  it("draws the tiles once it reads, the visible text in each name", async () => {
    renderForm();
    expect(await screen.findByRole("button", { name: "Postgres postgres:16 · 5432" })).toBeInTheDocument();
    expect(screen.queryByText("Couldn’t load the catalog.")).toBeNull();
  });
});

describe("The form's other reads", () => {
  beforeEach(() => {
    search = "source=image";
  });

  it("servers failing: says so, offers none, and Create stays off", async () => {
    failing.add("/api/v1/servers");
    const { container } = renderForm();
    await fillImage();
    expect(await screen.findByText("Couldn’t load the servers.")).toBeInTheDocument();
    expect(container.querySelector('input[name="server"]')).toBeNull();
    expect(screen.queryByText("panel host")).toBeNull();
    expect(screen.queryByText(/the panel host once it’s created/)).toBeNull();
    expect(screen.getByText("To create it, load the servers again.")).toBeInTheDocument();
    createOff();
  });

  it("the panel host's read failing: offers it nowhere, says so, and Create stays off", async () => {
    failing.add("/api/v1/instance/panel-host");
    const { container } = renderForm();
    await fillImage();
    expect(await screen.findByText("Couldn’t load the panel host.")).toBeInTheDocument();
    expect(container.querySelector('input[name="server"][value=""]')).toBeNull();
    expect(screen.queryByText(/on the panel host once it’s created/)).toBeNull();
    expect(screen.getByText("To create it, load the panel host again.")).toBeInTheDocument();
    createOff();
  });

  it("environments failing: says so, and Create stays off", async () => {
    failing.add("/api/v1/projects/p1/environments");
    renderForm();
    await fillImage();
    expect(await screen.findByText("Couldn’t load the environments.")).toBeInTheDocument();
    expect(screen.getByText("To create it, load the environments again.")).toBeInTheDocument();
    createOff();
  });

  it("the default memory limit failing: says so, with no … that never ends, and Create stays on", async () => {
    failing.add("/api/v1/resources/default-limit");
    renderForm();
    await fillImage();
    expect(await screen.findByText("Couldn’t load the default memory limit.")).toBeInTheDocument();
    expect(screen.queryByText("…")).toBeNull();
    await waitFor(() => {
      for (const b of screen.getAllByRole("button", { name: "Create and deploy" })) expect((b as HTMLButtonElement).disabled).toBe(false);
    });
  });
});
