import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { LogsTab } from "./logs-tab";

// A Grafana registered as the workspace's while it runs in a project's
// environment (before D-162): Needs attention has its row, and Move to
// workspace moves it and then deploys it; Toft never moves it on its own.

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  usePathname: () => "/shared",
}));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ isLoading: false, user: { id: "u-1", name: "alex", orgName: "Founders" } }),
  useOrgRole: () => role,
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }));

const sent: string[] = [];
let role = "owner";
/** Overrides a read: its answer, a read that never answers, or one that fails. */
let answer: Record<string, () => unknown> = {};
const pending = () => new Promise(() => {});
const fail = () => {
  throw new Error("boom");
};
const WS = "/api/v1/workspace/services";
const OBS = "/api/v1/observability";
const LOGS = "/api/v1/observability/logs";
const BY_PROJECT = "/api/v1/observability/projects";
const orgSvc = (id: string, addon_slug: string, server_id = "s1") => ({
  id,
  name: id,
  scope: "org",
  project_id: "",
  environment_id: "",
  server_id,
  runtime_status: "running",
  addon_slug,
  docker_image: `grafana/${id}`,
  source_type: "docker-image",
});
const fullStack = () => [orgSvc("loki", "loki"), orgSvc("grafana", "grafana"), orgSvc("prometheus", "prometheus")];
const noUrls = () => ({ id: "o1", org_id: "org-1", created_at: "", updated_at: "" });
const storefront = () => [{ project_id: "p1", name: "storefront", loki_url: "", grafana_url: "" }];

function route(path: string): unknown {
  switch (path.split("?")[0]) {
    case "/api/v1/observability":
      return { id: "o1", org_id: "org-1", grafana_service_id: "g1", created_at: "", updated_at: "" };
    case "/api/v1/workspace/services":
      return [
        { id: "g1", name: "grafana", scope: "environment", project_id: "p1", environment_id: "e1", server_id: "", runtime_status: "running", addon_slug: "grafana", docker_image: "grafana/grafana" },
      ];
    case "/api/v1/projects":
      return [{ id: "p1", name: "storefront" }];
    case "/api/v1/workspace/environments":
      return [{ id: "e1", project_id: "p1", name: "production" }];
  }
  return [];
}

vi.mock("@/lib/api/client", async (orig) => ({
  ...(await orig<typeof import("@/lib/api/client")>()),
  fetchAPI: async (path: string, init?: RequestInit) => {
    if (init?.method && init.method !== "GET") sent.push(`${init.method} ${path}`);
    const read = answer[path.split("?")[0]];
    return read ? read() : route(path);
  },
}));

beforeEach(() => {
  sent.length = 0;
  role = "owner";
  answer = {};
});

function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <LogsTab />
    </QueryClientProvider>,
  );
}

describe("Logs and metrics", () => {
  it("asks to move or unregister a project's Grafana registered as the workspace's", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <LogsTab />
      </QueryClientProvider>,
    );
    const row = (await screen.findByRole("link", { name: "grafana" })).closest("tr")!;
    expect(row.textContent).toContain("it runs in storefront’s production environment");
    expect(within(row).getByRole("button", { name: "Unregister grafana" })).toBeTruthy();

    fireEvent.click(within(row).getByRole("button", { name: "Move to workspace: grafana" }));
    fireEvent.click(await screen.findByRole("button", { name: "Move and redeploy" }));
    await waitFor(() =>
      expect(sent).toEqual(["POST /api/v1/services/g1/move-to-workspace", "POST /api/v1/services/g1/deployments"]),
    );
  });

  it("says who can do what in Needs attention", async () => {
    const say = async (r: string) => {
      role = r;
      show();
      const text = (await screen.findByRole("link", { name: "grafana" })).closest("tr")!.textContent;
      cleanup();
      return text;
    };
    expect(await say("owner")).toContain("the workspace’s: move it to the workspace, or unregister it.");
    expect(await say("developer")).toContain("the workspace’s: move it to the workspace; an owner can unregister it.");
    expect(await say("viewer")).toContain("the workspace’s: a developer or owner can move it; an owner can unregister it.");
  });

  it("claims no empty stack, and no project without services, until the services are read", async () => {
    answer = { [WS]: pending, [BY_PROJECT]: storefront };
    show();
    const row = (await screen.findByRole("link", { name: "Settings of storefront" })).closest("div")!;
    expect(row.textContent).not.toContain("No services");
    expect(screen.getByRole("status", { name: "Loading logs and metrics" })).toBeTruthy();
    expect(screen.queryByText(/No logs or metrics stack/)).toBeNull();
    expect(screen.queryByRole("link", { name: "Add Loki" })).toBeNull();
    expect(screen.queryByText("Not deployed.")).toBeNull();
  });

  it("offers Retry when the services can't be read, and only then tells stack from none", async () => {
    answer = { [WS]: fail, [OBS]: noUrls };
    show();
    const retry = await screen.findByRole("button", { name: /^Retry/ });
    expect(screen.queryByText(/No logs or metrics stack/)).toBeNull();
    answer[WS] = () => [];
    fireEvent.click(retry);
    expect(await screen.findByText(/No logs or metrics stack/)).toBeTruthy();
  });

  it("won't call the stack empty when the config that registers it can't be read", async () => {
    answer = { [WS]: () => [], [OBS]: fail };
    show();
    expect(await screen.findByRole("button", { name: /^Retry/ })).toBeTruthy();
    expect(screen.queryByText(/No logs or metrics stack/)).toBeNull();
  });

  it("asserts nothing about addresses it couldn't read, and gets them on Retry", async () => {
    answer = { [WS]: fullStack, [OBS]: fail, [LOGS]: () => [{ server_id: "s1", services: [] }] };
    show();
    await screen.findByRole("heading", { name: "The stack" });
    expect(await screen.findByText("Couldn’t read where the logs go.")).toBeTruthy();
    expect(screen.queryByText(/No address yet/)).toBeNull();
    expect(screen.queryByText(/ships? to Loki/)).toBeNull();
    expect(screen.queryByRole("button", { name: /Set an/ })).toBeNull();
    answer[OBS] = noUrls;
    fireEvent.click(screen.getByRole("button", { name: /^Retry/ }));
    expect(await screen.findByText(/No address yet/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Set an address…" })).toBeTruthy();
    expect(screen.queryByText("Couldn’t read where the logs go.")).toBeNull();
  });

  it("offers Retry on a failed Logs read", async () => {
    answer = { [WS]: fullStack, [OBS]: noUrls, [LOGS]: fail };
    show();
    expect(await screen.findByText("Couldn’t read where the logs go.")).toBeTruthy();
    answer[LOGS] = () => [];
    fireEvent.click(screen.getByRole("button", { name: /^Retry/ }));
    await waitFor(() => expect(screen.queryByText("Couldn’t read where the logs go.")).toBeNull());
  });

  it("offers Retry on a failed By project read", async () => {
    answer = { [WS]: fullStack, [OBS]: noUrls, [BY_PROJECT]: fail };
    show();
    expect(await screen.findByText("Couldn’t read each project’s logs and metrics.")).toBeTruthy();
    answer[BY_PROJECT] = storefront;
    fireEvent.click(screen.getByRole("button", { name: /^Retry/ }));
    expect(await screen.findByRole("link", { name: "Settings of storefront" })).toBeTruthy();
  });

  const addressed = () => ({ ...noUrls(), loki_url: "https://loki.founders.example" });
  const appSvc = (id: string, server_id: string) => ({ ...orgSvc(id, "", server_id), scope: "environment" });

  it("counts a service on Loki's old address as shipping, with no banner", async () => {
    answer = {
      [WS]: () => [orgSvc("loki", "loki"), appSvc("web", "s1")],
      [OBS]: addressed,
      [LOGS]: () => [{ server_id: "s1", services: [{ service_id: "web", containers: [{ name: "web", driver: "loki", loki_url: "http://127.0.0.1:39100/loki/api/v1/push" }] }] }],
    };
    show();
    expect(await screen.findByText("Loki, old address")).toBeTruthy();
    expect(screen.getByText("2 of 2 services ship to Loki")).toBeTruthy();
  });

  it("lists three servers in the banner as a sentence", async () => {
    answer = {
      [WS]: () => [orgSvc("loki", "loki", "s0"), appSvc("a", "s1"), appSvc("b", "s2"), appSvc("c", "s3")],
      [OBS]: addressed,
      [LOGS]: () => ["s1", "s2", "s3"].map((server_id, i) => ({ server_id, services: [{ service_id: "abc"[i], containers: [{ name: "x", driver: "json-file" }] }] })),
      "/api/v1/servers": () => ["s1", "s2", "s3"].map((id) => ({ id, name: `box-${id}` })),
    };
    show();
    expect(await screen.findByText("Some services on box-s1, box-s2, and box-s3 don’t ship their logs yet")).toBeTruthy();
  });
});
