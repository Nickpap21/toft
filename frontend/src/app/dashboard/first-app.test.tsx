import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, renderHook, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { DashboardData } from "@/lib/api/dashboard.service";
import { Welcome, useFinishSetup } from "./welcome";

// Home's Deploy your first app under Only Toft (D-62): the panel's server
// runs no apps, so with no server of its own the card says so and leads to
// Add a server; with one, it names that server.

let role = "control_plane";
// Which query never answers, as while it loads.
let pending: "" | "host" | "servers" = "";
let orgRole = "owner";
let operator = false;
let servers: { id: string; name: string; shared: boolean; connection: string }[] = [];
let failing = "";
// What a path answers, in place of the defaults below.
let answers: Record<string, unknown> = {};

vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ user: { id: "u-1", orgName: "Founders" } }),
  useOrgRole: () => orgRole,
  useIsOperator: () => operator,
}));
vi.mock("@/lib/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/client")>()),
  fetchAPI: async (path: string) => {
    if (path === failing) throw new Error("boom");
    if (path in answers) return answers[path];
    if (path === "/api/v1/instance/panel-host") return pending === "host" ? new Promise(() => {}) : { role, app_networks: "isolated" };
    if (path === "/api/v1/servers") return pending === "servers" ? new Promise(() => {}) : servers;
    return [];
  },
}));

const data = { deploying: [], failed_deploys: [] } as unknown as DashboardData;

function renderHome(home = data) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <Welcome rows={[]} services={[]} data={home} now={0} />
    </QueryClientProvider>,
  );
}

function finishSetup(live?: Parameters<typeof useFinishSetup>[0]) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderHook(() => useFinishSetup(live), {
    wrapper: ({ children }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>,
  });
}

describe("Deploy your first app under Only Toft", () => {
  beforeEach(() => {
    role = "control_plane";
    pending = "";
    orgRole = "owner";
    operator = false;
    servers = [];
  });

  it.each(["host", "servers"] as const)("names no server while the %s query loads", async (which) => {
    pending = which;
    role = "apps";
    renderHome();
    expect(await screen.findByRole("heading", { name: "Deploy your first app" })).toBeTruthy();
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByText(/it runs on/)).toBeNull();
    expect(screen.queryByText(/apps need a server of their own/)).toBeNull();
  });

  it("with no server, says apps need one and leads an owner to Add a server", async () => {
    renderHome();
    expect(await screen.findByText("apps need a server of their own")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Add a server" }).getAttribute("href")).toBe("/servers/new");
    expect(screen.queryByRole("link", { name: "Your code" })).toBeNull();
    expect(screen.queryByText(/it runs on this server/)).toBeNull();
  });

  it("tells someone who isn't an owner who adds servers", async () => {
    orgRole = "developer";
    renderHome();
    expect(await screen.findByText(/Owners of Founders add servers/)).toBeTruthy();
    expect(screen.queryByRole("link", { name: "Add a server" })).toBeNull();
  });

  it("with an app server, names it rather than the panel's", async () => {
    servers = [{ id: "s1", name: "fsn1-apps", shared: false, connection: "ssh" }];
    renderHome();
    expect(await screen.findByText("fsn1-apps")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Your code" })).toBeTruthy();
    expect(screen.queryByText(/it runs on this server/)).toBeNull();
  });
});

describe("Deploy your first app for a viewer", () => {
  beforeEach(() => {
    role = "apps";
    pending = "";
    orgRole = "viewer";
    operator = false;
    servers = [];
  });

  it("shows the ways and the catalog without links, and says who creates services", async () => {
    renderHome();
    expect(await screen.findByText("Your code")).toBeTruthy();
    expect(screen.getByText("Uptime Kuma")).toBeTruthy();
    expect(screen.queryAllByRole("link")).toHaveLength(0);
    expect(screen.getByText("Owners and developers of Founders create services.")).toBeTruthy();
  });

  it("keeps the links for a developer", async () => {
    orgRole = "developer";
    renderHome();
    expect((await screen.findByRole("link", { name: "An image" })).getAttribute("href")).toBe("/services/new?source=image");
    expect(screen.getByRole("link", { name: "Uptime Kuma" }).getAttribute("href")).toBe("/services/new?template=uptime-kuma");
    expect(screen.queryByText(/create services/)).toBeNull();
  });
});

describe("The failed first deploy", () => {
  it("leads to the service's settings beside its log", async () => {
    role = "apps";
    pending = "";
    orgRole = "owner";
    operator = false;
    servers = [];
    renderHome({
      deploying: [],
      failed_deploys: [{ id: "d-1", project_id: "p-1", service_id: "s-1", service_name: "whoami", error_summary: "no such image" }],
    } as unknown as DashboardData);
    expect((await screen.findByRole("link", { name: "Settings" })).getAttribute("href")).toBe("/projects/p-1/services/s-1?tab=settings");
    expect(screen.getByRole("link", { name: "Its log" })).toBeTruthy();
  });
});

describe("Finish setting up", () => {
  beforeEach(() => {
    role = "control_plane";
    pending = "";
    orgRole = "owner";
    operator = false;
    servers = [];
  });

  it.each(["/api/v1/instance/setup", "/api/v1/instance/panel", "/api/v1/instance/backup"])("isn't known for an operator while %s has no answer", async (path) => {
    operator = true;
    failing = path;
    const { result } = finishSetup();
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.known).toBe(false);
  });

  it("is known once every read has answered, an operator's too", async () => {
    operator = true;
    failing = "";
    const { result } = finishSetup();
    await waitFor(() => expect(result.current.known).toBe(true));
  });

  it("doesn't wait for the reads only operators make", async () => {
    operator = false;
    failing = "";
    const { result } = finishSetup();
    await waitFor(() => expect(result.current.known).toBe(true));
  });

  it("doesn't count a server still waiting for its agent", async () => {
    servers = [{ id: "s2", name: "server-2", shared: false, connection: "agent" }];
    const { result } = finishSetup();
    await waitFor(() => expect(result.current.rows.find((r) => r.key === "servers")?.sub).toBe("Needed: this one runs only Toft."));
    expect(result.current.rows.find((r) => r.key === "servers")?.done).toBe(false);
  });

  it("counts a server that has joined", async () => {
    servers = [{ id: "s1", name: "fsn1-apps", shared: false, connection: "ssh" }];
    const { result } = finishSetup();
    await waitFor(() => expect(result.current.rows.find((r) => r.key === "servers")?.sub).toBe("1 server added"));
    expect(result.current.rows.find((r) => r.key === "servers")?.done).toBe(true);
  });

  it("sets Toft's address on the setup step", async () => {
    operator = true;
    const { result } = finishSetup();
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.rows.find((r) => r.key === "address")?.act?.href).toBe("/setup/address");
  });

  // D-163: the row asks again; a checklist that was complete stays away, Needs you has the row.
  it("asks for an out-of-date recovery kit again, without bringing a complete checklist back", async () => {
    operator = true;
    failing = "";
    servers = [{ id: "s1", name: "fsn1-apps", shared: false, connection: "ssh" }];
    answers = {
      "/api/v1/instance/setup": { domain: "panel.example", kit_acknowledged_at: "2026-10-18T10:00:00Z", kit_out_of_date: true },
      "/api/v1/instance/panel": { certificate: { domain: "panel.example" } },
      "/api/v1/instance/backup": { bucket: "toft-panel" },
      "/api/v1/org/members": [{}, {}],
    };
    const { result } = finishSetup({ name: "whoami" } as Parameters<typeof useFinishSetup>[0]);
    await waitFor(() => expect(result.current.known).toBe(true));
    answers = {};
    const kit = result.current.rows.find((r) => r.key === "kit");
    expect([kit?.done, kit?.act]).toEqual([false, { label: "Store again", href: "/instance/kit" }]);
    expect(kit?.sub).toBe("Out of date: the encryption key or the backup settings changed after it was stored on 2026-10-18.");
    expect(result.current.rows.filter((r) => !r.done).map((r) => r.key)).toEqual(["kit"]);
    expect(result.current.complete).toBe(true);
  });
});
