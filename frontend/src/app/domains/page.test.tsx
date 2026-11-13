import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import DomainsPage from "./page";

// Domains when one of its reads fails (P7): a failed read isn't "No domains".

let adding = false;
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useSearchParams: () => new URLSearchParams(adding ? "add" : ""),
  usePathname: () => "/domains",
}));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ isLoading: false, user: { id: "u-1", orgName: "Founders" } }),
  useOrgRole: () => "owner",
  useIsOperator: () => false,
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const DOMAINS = "/api/v1/domains";
const SERVERS = "/api/v1/servers";
const SERVICES = "/api/v1/workspace/services";

let failing: string[] = [];
let dns = "here";

function route(path: string): unknown {
  switch (path.split("?")[0]) {
    case DOMAINS:
      return [{ id: "d1", service_id: "s1", hostname: "shop.example.com", service_name: "web", project_id: "p1", project_name: "shop", environment_name: "production", server_id: "srv", scope: "environment", temporary: false, created_by: "" }];
    case SERVERS:
      return [{ id: "srv", name: "server-1", ip_address: "203.0.113.11", shared: false }];
    case SERVICES:
      return [{ id: "s1", name: "web", scope: "environment", project_id: "p1", environment_id: "e1", server_id: "srv", port: 80, runtime_status: "stopped" }];
    case "/api/v1/services/s1/domains/check":
      return { server: { name: "server-1", address: "203.0.113.11", wildcards_ok: false, owner: true }, routing_known: true, domains: [{ id: "d1", dns, addresses: ["203.0.113.11"], certificate: "none", routed: false }] };
  }
  return [];
}

vi.mock("@/lib/api/client", async (orig) => {
  const actual = await orig<typeof import("@/lib/api/client")>();
  return {
    ...actual,
    fetchAPI: async (path: string) => {
      if (failing.includes(path.split("?")[0])) throw new actual.ApiError("boom", 500, { error: "boom" });
      return route(path);
    },
  };
});

function renderPage() {
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <DomainsPage />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  failing = [];
  adding = false;
  dns = "here";
});

// WCAG 2.5.3: each name starts with the words the button shows.
it("names a waiting domain's buttons with what they read", async () => {
  dns = "none";
  renderPage();
  expect(await screen.findByRole("button", { name: "Copy record for shop.example.com" })).toBeTruthy();
  expect(await screen.findByRole("button", { name: "Check again: shop.example.com" })).toBeTruthy();
});

describe("domains when a read fails", () => {
  it("says the domains failed to load, with Retry and no empty state or No domains", async () => {
    failing = [DOMAINS];
    renderPage();
    const failed = await screen.findByRole("heading", { name: "Couldn’t load this page", level: 2 });
    expect(failed).toBeTruthy();
    expect(screen.queryByText("No domains yet")).toBeNull();
    expect(screen.queryByText(/No domains/)).toBeNull();
    expect(await screen.findByText("— · 203.0.113.11")).toBeTruthy();

    failing = [];
    fireEvent.click(within(failed.closest("section")!).getByRole("button", { name: /^Retry/ }));
    expect((await screen.findAllByText("shop.example.com")).length).toBeGreaterThan(0);
    expect(await screen.findByText("1 domain · 203.0.113.11")).toBeTruthy();
  });

  it("gives the servers a failed row where their card was", async () => {
    failing = [SERVERS];
    renderPage();
    expect(await screen.findByText("Couldn’t load the servers.")).toBeTruthy();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeTruthy();
  });

  it("doesn't say a service isn't running on a failed read of the services", async () => {
    failing = [SERVICES];
    renderPage();
    expect((await screen.findAllByText("shop.example.com")).length).toBeGreaterThan(0);
    expect(await screen.findByText("Couldn’t load the services.")).toBeTruthy();
    expect(screen.queryByText("the service isn’t running")).toBeNull();
  });

  it("says it does when the services were read", async () => {
    renderPage();
    expect(await screen.findByText("the service isn’t running")).toBeTruthy();
  });

  it("says which service's DNS check failed, with Retry, and doesn't say DNS was checked", async () => {
    failing = ["/api/v1/services/s1/domains/check"];
    renderPage();
    expect(await screen.findByText("Couldn’t load the DNS check of web.")).toBeTruthy();
    expect(screen.queryByText(/DNS checked/)).toBeNull();

    failing = [];
    fireEvent.click(screen.getByRole("button", { name: "Retry loading the DNS check of web" }));
    expect(await screen.findByText(/DNS checked/)).toBeTruthy();
    expect(screen.queryByText("Couldn’t load the DNS check of web.")).toBeNull();
  });

  it("the add dialog doesn't say no service has a port on a failed read of the services", async () => {
    adding = true;
    failing = [SERVICES];
    renderPage();
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText("Couldn’t load the services.")).toBeTruthy();
    expect(within(dialog).queryByText(/No service here has a port/)).toBeNull();
  });
});
