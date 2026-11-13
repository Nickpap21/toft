import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import EnvironmentPage from "./page";

// GET /environments/:id/services also returns the project and workspace
// services the environment reaches. They are not its own: the page must not
// list them, and its Delete card must not count them among what goes.

vi.mock("next/navigation", () => ({
  useParams: () => ({ id: "p1", envId: "e1" }),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  usePathname: () => "/projects/p1/environments/e1",
}));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({
    isLoading: false,
    user: { id: "u-1", orgName: "Founders" },
  }),
  useOrgRole: () => "owner",
  useIsOperator: () => false,
}));
vi.mock("@/context/BreadcrumbContext", () => ({
  useBreadcrumbs: () => ({ set: vi.fn() }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const svc = (id: string, scope: string, extra = {}) => ({
  id,
  name: id,
  scope,
  project_id: scope === "org" ? "" : "p1",
  environment_id: scope === "environment" ? "e1" : undefined,
  type: "web",
  source_type: "docker-image",
  port: 80,
  runtime_status: "running",
  created_at: "",
  updated_at: "",
  ...extra,
});

vi.mock("@/lib/api/project.service", () => ({
  projectService: {
    get: async () => ({
      id: "p1",
      name: "storefront",
      description: "",
      user_id: "u-1",
      created_at: "",
      updated_at: "",
    }),
  },
}));
vi.mock("@/lib/api/environment.service", () => ({
  ENV_COLORS: ["amber", "violet", "blue", "teal", "gray"],
  environmentService: {
    list: async () => [
      {
        id: "e1",
        project_id: "p1",
        name: "production",
        color: "gray",
        protected: true,
        created_at: "2026-10-01T00:00:00Z",
        updated_at: "",
      },
    ],
  },
}));
vi.mock("@/lib/api/service.service", () => ({
  serviceService: {
    listForEnvironment: async () =>
      servicesFail
        ? Promise.reject(refused())
        : noServices
        ? []
        : [
            svc("web", "environment"),
            svc("redis", "project", { addon_slug: "redis" }),
            svc("loki", "org", { addon_slug: "loki" }),
          ],
    data: async () => [
      {
        service_id: "web",
        service_name: "web",
        server_name: "",
        read: true,
        volumes: [{ name: "paas-vol-web-data", bytes: 3 * 1024 ** 3, from: "volume" }],
      },
    ],
  },
}));
vi.mock("@/lib/api/server.service", () => ({
  serverService: { list: async () => [] },
}));
vi.mock("@/lib/api/envvar.service", async (orig) => ({
  ...(await orig<typeof import("@/lib/api/envvar.service")>()),
  envvarService: {
    list: () => listVars(),
    bulkUpsert: (...args: unknown[]) => bulkUpsert(...args),
  },
}));
vi.mock("@/lib/api/secret.service", () => ({
  secretService: { list: () => listSecrets() },
}));
vi.mock("@/lib/api/client", async (orig) => ({
  ...(await orig<typeof import("@/lib/api/client")>()),
  fetchAPI: async () => overview,
}));

let noServices = false;
let servicesFail = false;

// The environment's shared variables, as each test sets them.
let sharedVars: { key: string; value: string }[] = [];
const readVars = async () => ({ data: sharedVars, version: "v1" });
let listVars: () => Promise<{ data: { key: string; value: string }[]; version: string }> = readVars;
let listSecrets: () => Promise<{ key: string }[]> = async () => [];
const bulkUpsert = vi.fn();
const refused = () => new ApiError("Toft answered 500", 500, {});

// GET /environments/:id/overview, as each test sets it.
let overview: { network: string; services: unknown[] } = { network: "", services: [] };

let client: QueryClient;
function renderPage() {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <EnvironmentPage />
    </QueryClientProvider>,
  );
}

const sharedCard = async () =>
  (await screen.findByRole("heading", { name: "Shared variables" })).closest("section")!;
const secretsCard = async () =>
  (await screen.findByRole("heading", { name: "Shared secrets" })).closest("section")!;
const editValue = async (key: string, value: string) => {
  fireEvent.click(await screen.findByRole("button", { name: `Edit ${key}` }));
  fireEvent.change(screen.getByLabelText(`Value of ${key}`), { target: { value } });
};
const echoSave = (version: string) =>
  bulkUpsert.mockImplementation(async (_scope, _version, vars) => ({ data: vars, version }));

afterEach(() => {
  noServices = false;
  servicesFail = false;
  sharedVars = [];
  listVars = readVars;
  listSecrets = async () => [];
  bulkUpsert.mockReset();
});

describe("Environment page", () => {
  it("shows a failed read of the shared variables instead of an empty list, with no Add or Save", async () => {
    listVars = () => Promise.reject(refused());
    renderPage();
    const card = await sharedCard();
    expect(await within(card).findByRole("heading", { name: "Couldn’t load this page", level: 2 })).toBeTruthy();
    expect(within(card).queryByText(/No shared variables yet/)).toBeNull();
    expect(within(card).queryByRole("button", { name: "Add variable" })).toBeNull();
    expect(within(card).queryByRole("button", { name: "Save variables" })).toBeNull();

    listVars = readVars;
    fireEvent.click(within(card).getByRole("button", { name: /^Retry/ }));
    expect(await within(card).findByText(/No shared variables yet/)).toBeTruthy();
    expect(within(card).getByRole("button", { name: "Add variable" })).toBeTruthy();
  });

  it("shows no empty state and no Add while the shared variables are being read", async () => {
    listVars = () => new Promise(() => {});
    renderPage();
    const card = await sharedCard();
    expect(within(card).getByRole("status")).toBeTruthy();
    expect(within(card).queryByText(/No shared variables yet/)).toBeNull();
    expect(within(card).queryByRole("button", { name: "Add variable" })).toBeNull();
  });

  it("shows a failed read of the shared secrets instead of an empty list, with no Add", async () => {
    listSecrets = () => Promise.reject(refused());
    renderPage();
    const card = await secretsCard();
    expect(await within(card).findByRole("heading", { name: "Couldn’t load this page", level: 2 })).toBeTruthy();
    expect(within(card).queryByText(/No shared secrets yet/)).toBeNull();
    expect(within(card).queryByRole("button", { name: "Add secret" })).toBeNull();
  });

  it("shows a dash, not a count, for what a service shares while a read has failed", async () => {
    sharedVars = [{ key: "LOG_LEVEL", value: "info" }];
    overview = {
      network: "",
      services: [{ service_id: "web", own_variables: 0, own_secrets: 0, overrides: ["A", "B"], domains: [], last_deployment: null }],
    };
    listSecrets = () => Promise.reject(refused());
    renderPage();
    await within(await secretsCard()).findByRole("heading", { name: "Couldn’t load this page", level: 2 });
    const card = (await screen.findByRole("heading", { name: "What each service sets" })).closest("section")!;
    const row = await within(card).findByRole("row", { name: /^web/ });
    expect(within(row).getAllByRole("cell")[1].textContent).toBe("Shared —");
    overview = { network: "", services: [] };
  });

  it("doesn't say what a new service gets while a read has failed", async () => {
    noServices = true;
    listSecrets = () => Promise.reject(refused());
    renderPage();
    await within(await secretsCard()).findByRole("heading", { name: "Couldn’t load this page", level: 2 });
    const none = await screen.findByText(/No services in production yet/);
    expect(none.textContent).toBe("No services in production yet.");
  });

  it("Save sends the version it read, then the one the PUT returned", async () => {
    sharedVars = [{ key: "LOG_LEVEL", value: "info" }];
    echoSave("v2");
    renderPage();
    await editValue("LOG_LEVEL", "debug");
    fireEvent.click(screen.getByRole("button", { name: "Save variables" }));
    await waitFor(() => expect(bulkUpsert).toHaveBeenCalledTimes(1));
    expect(bulkUpsert.mock.calls[0][1]).toBe("v1");

    await editValue("LOG_LEVEL", "warn");
    fireEvent.click(screen.getByRole("button", { name: "Save variables" }));
    await waitFor(() => expect(bulkUpsert).toHaveBeenCalledTimes(2));
    expect(bulkUpsert.mock.calls[1][1]).toBe("v2");
  });

  it("Save sends the version the typed rows came from, not one a background read brought", async () => {
    sharedVars = [{ key: "LOG_LEVEL", value: "info" }];
    echoSave("v2");
    renderPage();
    await editValue("LOG_LEVEL", "debug");

    sharedVars = [{ key: "LOG_LEVEL", value: "info" }, { key: "OTHER", value: "1" }];
    listVars = async () => ({ data: sharedVars, version: "v9" });
    await client.invalidateQueries({ queryKey: ["envvars"] });

    fireEvent.click(screen.getByRole("button", { name: "Save variables" }));
    await waitFor(() => expect(bulkUpsert).toHaveBeenCalledTimes(1));
    expect(bulkUpsert.mock.calls[0][1]).toBe("v1");
  });

  it("on a 409 says nothing was saved, keeps what was typed and can read them again", async () => {
    sharedVars = [{ key: "LOG_LEVEL", value: "info" }];
    bulkUpsert.mockRejectedValueOnce(new ApiError("changed", 409, { code: "changed_since_read" }));
    renderPage();
    await editValue("LOG_LEVEL", "debug");
    fireEvent.click(screen.getByRole("button", { name: "Save variables" }));

    expect(await screen.findByText(/changed since you opened this page, so nothing was saved/)).toBeTruthy();
    expect(screen.getByDisplayValue("debug")).toBeTruthy();
    expect((screen.getByRole("button", { name: "Save variables" }) as HTMLButtonElement).disabled).toBe(true);

    sharedVars = [{ key: "LOG_LEVEL", value: "info" }, { key: "OTHER", value: "1" }];
    listVars = async () => ({ data: sharedVars, version: "v9" });
    fireEvent.click(screen.getByRole("button", { name: "Read them again" }));
    expect(await screen.findByText("OTHER")).toBeTruthy();
    expect(screen.getByDisplayValue("debug")).toBeTruthy();
    await waitFor(() => expect(screen.queryByText(/changed since you opened this page/)).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("button", { name: "Save variables" })));

    echoSave("v10");
    fireEvent.click(screen.getByRole("button", { name: "Save variables" }));
    await waitFor(() => expect(bulkUpsert).toHaveBeenCalledTimes(2));
    expect(bulkUpsert.mock.calls[1][1]).toBe("v9");
    expect(bulkUpsert.mock.calls[1][2]).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ key: "LOG_LEVEL", value: "debug" }),
        expect.objectContaining({ key: "OTHER", value: "1" }),
      ]),
    );
  });

  it("doesn't say the environment has no services or server on a failed read of them, and Retry brings them", async () => {
    servicesFail = true;
    renderPage();
    const card = (await screen.findByRole("heading", { name: "What each service sets" })).closest("section")!;
    expect(await within(card).findByText("Couldn’t load the services.")).toBeTruthy();
    expect(within(card).queryByText(/No services in/)).toBeNull();
    expect(screen.queryByText("none yet")).toBeNull();
    expect(screen.getByText("Server").nextElementSibling?.textContent).toBe("—");
    expect(screen.queryByText(/Removes production with its shared variables/)).toBeNull();
    expect(screen.getByText(/Removes production and every service in it/)).toBeTruthy();

    servicesFail = false;
    fireEvent.click(within(card).getByRole("button", { name: /^Retry/ }));
    expect(await within(card).findByRole("link", { name: "web" })).toBeTruthy();
  });

  it("lists only the environment's own services, and deleting it names only those", async () => {
    renderPage();
    const table = (
      await screen.findByRole("heading", { name: "What each service sets" })
    ).closest("section")!;
    expect(
      await within(table).findByRole("link", { name: "web" }),
    ).toBeTruthy();
    expect(within(table).queryByRole("link", { name: "redis" })).toBeNull();
    expect(within(table).queryByRole("link", { name: "loki" })).toBeNull();
    // The project service it reaches gets none of its shared keys.
    expect(
      within(table).getByText(/redis is a project service: it gets none of production’s shared keys/),
    ).toBeTruthy();

    expect(screen.getByText(/Removes its 1 service from/)).toBeTruthy();
    expect(screen.getByText(/redis and the workspace services stay/)).toBeTruthy();

    // Each volume it deletes, with its size.
    expect(
      await screen.findByText("web: paas-vol-web-data · 3.0 GiB"),
    ).toBeTruthy();
  });

  it("counts what each service sets itself and names its overrides, from the overview", async () => {
    overview = {
      network: "",
      services: [
        { service_id: "web", own_variables: 2, own_secrets: 1, overrides: ["DATABASE_URL"], domains: [], last_deployment: null },
      ],
    };
    renderPage();
    const table = (
      await screen.findByRole("heading", { name: "What each service sets" })
    ).closest("section")!;
    expect(await within(table).findByText("2 variables · 1 secret")).toBeTruthy();
    expect(within(table).getByText("DATABASE_URL")).toBeTruthy();
    expect(
      screen.getByText(/Services also set their own:/).textContent,
    ).toBe(
      "Services also set their own: 2 variables, 1 secret across 1 service, 1 of them overriding a shared key. What each service sets",
    );
    overview = { network: "", services: [] };
  });

  it("opens a saved variable with Edit, and the save bar counts the change until Cancel", async () => {
    sharedVars = [{ key: "LOG_LEVEL", value: "info" }];
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Edit LOG_LEVEL" }));
    const value = screen.getByLabelText("Value of LOG_LEVEL");
    fireEvent.change(value, { target: { value: "debug" } });
    expect(screen.getByText("1 unsaved change:")).toBeTruthy();
    expect(screen.getByText("Saved value: info")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByText("1 unsaved change:")).toBeNull();
    expect(screen.getByText("info")).toBeTruthy();
    sharedVars = [];
  });

  it("opens the shared secret form from an Add secret button when there are none", async () => {
    renderPage();
    const add = await screen.findByRole("button", { name: "Add secret" });
    expect(screen.queryByPlaceholderText("SENTRY_AUTH_TOKEN")).toBeNull();
    fireEvent.click(add);
    expect(screen.getByPlaceholderText("SENTRY_AUTH_TOKEN")).toBe(document.activeElement);
  });
});
