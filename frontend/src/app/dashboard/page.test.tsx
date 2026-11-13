import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import HomePage from "./page";

// H3, H12, X4: Home's cards and health strip don't state what they couldn't
// read. A secondary read failing shows its own row; the page stays. H6: Needs
// you stays above the setup layout at every width.

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ user: { id: "u-1", orgName: "Founders" }, token: "t" }),
  useOrgRole: () => "viewer",
  useIsOperator: () => false,
}));

const FAIL = Symbol("500");
const HIDDEN = { setup_hidden_at: "2026-11-01T00:00:00Z" };
const dashboard = { open_alerts: [], deploys_24h: { total: 0, succeeded: 0, failed: 0, median_seconds: 0 }, deploying: [], recent_deployments: [], failed_deploys: [], project_deploys: [], activity: [] };
const need = { key: "no_backup:panel", kind: "no_backup", category: "servers", title: "The panel host", detail: "1 database, no backups", at: "2026-11-01T10:00:00Z", project_id: null, project: "", severity: "warning", snoozes: true };
let answers: Record<string, unknown>;

const json = (status: number, v: unknown) => new Response(JSON.stringify(v), { status });

beforeEach(() => {
  answers = {
    "/api/v1/dashboard": dashboard,
    "/api/v1/workspace/services": [],
    "/api/v1/projects": [{ id: "p1", name: "shop" }],
    "/api/v1/servers": [],
    "/api/v1/inbox/needs-you": [],
    "/api/v1/org/audit-log": [],
    "/api/v1/org/settings": HIDDEN,
    "/api/v1/instance/panel-host": { role: "apps", app_networks: "isolated" },
  };
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    const a = answers[new URL(url, "http://x").pathname];
    if (a === FAIL) return json(500, { error: "boom: pq internal detail", status: 500 });
    return json(200, { data: a ?? [] });
  }));
});
afterEach(() => vi.unstubAllGlobals());

function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><HomePage /></QueryClientProvider>);
}
const strip = () => within(screen.getByRole("list", { name: "System health" }));
const card = (name: string) => screen.getByRole("region", { name });

describe("Home when a read fails", () => {
  it("Needs you says it couldn't load, with Retry, and draws no count", async () => {
    answers["/api/v1/inbox/needs-you"] = FAIL;
    mount();
    expect(await screen.findByText("Couldn’t load what needs you.")).toBeInTheDocument();
    expect(within(card("Needs you")).getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
    expect(within(card("Needs you")).getByText("—")).toBeInTheDocument();
    expect(screen.queryByText("Nothing needs you.")).toBeNull();
  });

  it("Needs you is drawn during setup too when its read failed", async () => {
    answers["/api/v1/org/settings"] = {};
    answers["/api/v1/inbox/needs-you"] = FAIL;
    mount();
    expect(await screen.findByText("Couldn’t load what needs you.")).toBeInTheDocument();
  });

  it("the main read failing says so with Retry, and no API message", async () => {
    answers["/api/v1/dashboard"] = FAIL;
    mount();
    expect(await screen.findByRole("heading", { name: "Couldn’t load this page", level: 2 })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
    expect(screen.queryByText(/pq internal/)).toBeNull();
  });

  it("servers failing: the strip says Unknown, not Only the panel host, with Retry", async () => {
    answers["/api/v1/servers"] = FAIL;
    mount();
    expect(await screen.findByText("Couldn’t load the servers.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
    expect(strip().getByText("Unknown")).toBeInTheDocument();
    expect(screen.queryByText("Only the panel host")).toBeNull();
  });

  it("services failing: the strip says Unknown, projects keep their names, and no service counts", async () => {
    answers["/api/v1/workspace/services"] = FAIL;
    mount();
    expect(await screen.findByText("Couldn’t load the services.")).toBeInTheDocument();
    expect(strip().getByText("Unknown")).toBeInTheDocument();
    expect(screen.queryByText("No services yet")).toBeNull();
    expect(screen.queryByText("No services")).toBeNull();
    expect(within(card("Projects")).getByText("shop")).toBeInTheDocument();
    expect(within(card("Projects")).queryByText(/0 services/)).toBeNull();
  });

  it("Projects failing shows its own row, not No projects yet", async () => {
    answers["/api/v1/projects"] = FAIL;
    mount();
    expect(await screen.findByText("Couldn’t load the projects.")).toBeInTheDocument();
    expect(within(card("Projects")).getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
    expect(screen.queryByText("No projects yet.")).toBeNull();
    expect(screen.queryByText(/^0 projects/)).toBeNull();
  });

  it("Activity failing shows its own row, not No activity yet", async () => {
    answers["/api/v1/org/audit-log"] = FAIL;
    mount();
    expect(await screen.findByText("Couldn’t load the activity.")).toBeInTheDocument();
    expect(screen.queryByText(/^No activity yet/)).toBeNull();
  });

  it("Retry on a failed card reads it again", async () => {
    answers["/api/v1/inbox/needs-you"] = FAIL;
    mount();
    await screen.findByText("Couldn’t load what needs you.");
    answers["/api/v1/inbox/needs-you"] = [];
    fireEvent.click(within(card("Needs you")).getByRole("button", { name: /^Retry/ }));
    expect(await screen.findByText("Nothing needs you.")).toBeInTheDocument();
  });
});

describe("Home when a read behind the setup checklist fails", () => {
  it.each(["/api/v1/org/settings", "/api/v1/org/members", "/api/v1/servers", "/api/v1/workspace/services", "/api/v1/instance/panel-host"])(
    "%s failing keeps normal Home, with no Welcome and no setup rows",
    async (failing) => {
      answers["/api/v1/org/settings"] = {};
      answers[failing] = FAIL;
      mount();
      expect(await screen.findByRole("list", { name: "System health" })).toBeInTheDocument();
      expect(screen.queryByRole("heading", { name: "Welcome to Toft" })).toBeNull();
      expect(screen.queryByText("Deploy your first app")).toBeNull();
      expect(screen.queryByText("Finish setting up")).toBeNull();
    },
  );

  it("stays the setup layout once every read has answered", async () => {
    answers["/api/v1/org/settings"] = {};
    mount();
    expect(await screen.findByRole("heading", { name: "Welcome to Toft" })).toBeInTheDocument();
    expect(screen.getByText("Finish setting up")).toBeInTheDocument();
  });
});

describe("Needs you's place on Home", () => {
  it("has no phone order class during setup, so it stays above the checklist", async () => {
    answers["/api/v1/org/settings"] = {};
    answers["/api/v1/inbox/needs-you"] = [need];
    mount();
    await screen.findByText("The panel host");
    expect(card("Needs you").className).not.toContain("max-lg:order-1");
  });

  it("keeps it once the page is the two columns", async () => {
    answers["/api/v1/inbox/needs-you"] = [need];
    mount();
    await screen.findByText("The panel host");
    expect(card("Needs you").className).toContain("max-lg:order-1");
  });
});
