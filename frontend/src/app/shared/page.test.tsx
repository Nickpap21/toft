import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import SharedServicesPage from "./page";

// The Services tab: what reaches a workspace service comes from four other
// reads, and says nothing until they are in.

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  usePathname: () => "/shared",
  useSearchParams: () => new URLSearchParams(),
}));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ isLoading: false, user: { id: "u-1", name: "alex", orgName: "Founders" } }),
  useOrgRole: () => "owner",
}));

const WS = "/api/v1/workspace/services";
const ENVS = "/api/v1/workspace/environments";
const PROJECTS = "/api/v1/projects";
const DOMAINS = "/api/v1/domains";
const SERVERS = "/api/v1/servers";
const grafana = {
  id: "g1",
  name: "grafana",
  scope: "org",
  project_id: "",
  environment_id: "",
  server_id: "s1",
  runtime_status: "running",
  addon_slug: "grafana",
  docker_image: "grafana/grafana",
  source_type: "docker-image",
  connect_to: { host: "grafana", port: 3000 },
};
const web = { ...grafana, id: "w1", name: "web", scope: "environment", project_id: "p1", environment_id: "e1", addon_slug: "" };
const pending = () => new Promise(() => {});
const fail = () => {
  throw new Error("boom");
};

let answer: Record<string, () => unknown> = {};
const full = () => ({
  "/api/v1/shared-services": () => [grafana],
  [WS]: () => [grafana, web],
  [ENVS]: () => [{ id: "e1", project_id: "p1", name: "production" }],
  [PROJECTS]: () => [{ id: "p1", name: "storefront" }],
  [DOMAINS]: () => [],
  [SERVERS]: () => [{ id: "s1", name: "fsn1-prod-01" }],
});

vi.mock("@/lib/api/client", async (orig) => ({
  ...(await orig<typeof import("@/lib/api/client")>()),
  fetchAPI: async (path: string) => {
    const read = answer[path.split("?")[0]];
    return read ? read() : {};
  },
}));

function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <SharedServicesPage />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  answer = full();
});

describe("Workspace services", () => {
  it("says what reaches them once all four reads are in", async () => {
    show();
    expect(await screen.findByText("storefront production")).toBeTruthy();
    expect(screen.getByText("storefront, by alias")).toBeTruthy();
  });

  it.each([WS, ENVS, PROJECTS, DOMAINS, SERVERS])("states nothing about reach while %s is read", async (path) => {
    answer[path] = pending;
    show();
    await screen.findByRole("link", { name: "grafana" });
    expect(screen.getAllByText("Reading…")).toHaveLength(2);
    expect(screen.queryByText(/No project runs on/)).toBeNull();
    expect(screen.queryByText(/No project.s services run/)).toBeNull();
    expect(screen.queryByText(/, by alias/)).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it.each([WS, ENVS, PROJECTS, DOMAINS, SERVERS])("says so when %s fails, and states nothing about reach", async (path) => {
    answer[path] = fail;
    show();
    expect((await screen.findByRole("alert")).textContent).toContain("Couldn't read what reaches these services");
    expect(screen.getAllByText("Unknown")).toHaveLength(2);
    expect(screen.queryByText(/No project runs on/)).toBeNull();
    expect(screen.queryByText(/No project.s services run/)).toBeNull();
  });

  it("keeps the empty sentence out of the definition list", async () => {
    answer[WS] = () => [grafana];
    show();
    const none = await screen.findByText(/No project.s services run on a server yet/);
    expect(none.closest("dl")).toBeNull();
    expect(document.querySelector("dl")).toBeNull();
  });

  it("announces its skeleton", async () => {
    answer["/api/v1/shared-services"] = pending;
    show();
    expect(await screen.findByRole("status", { name: "Loading workspace services" })).toBeTruthy();
  });
});
