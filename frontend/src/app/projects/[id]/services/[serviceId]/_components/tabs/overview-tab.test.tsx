import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Service } from "@/lib/api/service.service";
import { memoryKillsText, OverviewTab } from "./overview-tab";

// S1, S2: a read that failed says so and offers Retry. It isn't drawn as an
// answer: not "Not deployed yet" on a running service, not a wait for the
// first samples of one that has run for days.

vi.mock("../admin-password-card", () => ({ AdminPasswordCard: () => null }));
vi.mock("../rollback-dialog", () => ({ RollbackDialog: () => null }));
vi.mock("@/components/docs/connected-work", () => ({ ConnectedWork: () => null }));
vi.mock("@/components/issues/deploy-work", () => ({ DeployWork: () => null }));
vi.mock("@/app/projects/_lib/redeploy", () => ({ useRedeploy: () => ({ redeploy: vi.fn(), dialog: null }) }));

let fails: (path: string) => boolean = () => false;
const answers: Record<string, unknown> = {};
vi.mock("@/lib/api/client", async (importOriginal) => {
  const orig = await importOriginal<typeof import("@/lib/api/client")>();
  return {
    ...orig,
    fetchAPI: async (path: string) => {
      if (fails(path)) throw new orig.ApiError("boom", 500, { error: "boom" });
      for (const [part, answer] of Object.entries(answers)) if (path.includes(part)) return answer;
      return [];
    },
    streamEvents: () => new Promise(() => {}),
  };
});

const web = { id: "s1", name: "web", environment_id: "e1", source_type: "docker-image", runtime_status: "running" } as Service;
const point = { timestamp: "2026-11-09T10:00:00Z", cpu_percent: 12, memory_mb: 80, throttled_pct: 0 };

function renderTab(service: Service = web) {
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <OverviewTab service={service} serviceId="s1" projectId="p1" canDeploy />
    </QueryClientProvider>,
  );
}
const dd = (term: string) => within(screen.getByText(term).parentElement!).getByRole("definition");

beforeEach(() => {
  fails = () => false;
  for (const k of Object.keys(answers)) delete answers[k];
});

describe("Overview when a read fails", () => {
  it("S1: the deployments read failing says so once, with no empty text", async () => {
    fails = (p) => p.includes("/deployments");
    renderTab();
    expect(await screen.findAllByText("Couldn’t load the deployments of web.")).toHaveLength(1);
    expect(screen.getAllByRole("button", { name: /^Retry/ })).toHaveLength(1);
    expect(within(screen.getByRole("region", { name: "Recent deployments" })).getByText("—")).toBeTruthy();
    expect(screen.queryByText("Not deployed yet")).toBeNull();
    expect(screen.queryByText("Deploy starts its first version.")).toBeNull();
    expect(screen.queryByText("No deployments yet.")).toBeNull();
    expect(screen.getByRole("heading", { name: "Live deployment" })).toBeTruthy();
  });

  it("S1: Retry reads again, and an answer of none is believed", async () => {
    fails = (p) => p.includes("/deployments");
    renderTab();
    const [retry] = await screen.findAllByRole("button", { name: /^Retry/ });
    fails = () => false;
    fireEvent.click(retry);
    expect(await screen.findByText("Not deployed yet")).toBeTruthy();
    expect(await screen.findByText("No deployments yet.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /^Retry/ })).toBeNull();
  });

  it("S1: the live deployment's own read failing is not Not deployed yet", async () => {
    answers["/deployments?"] = [{ id: "d1", status: "failed", image_tag: "web:1", created_at: "2026-11-09T09:00:00Z", error_msg: "" }];
    fails = (p) => p.endsWith("/deployments/d9");
    renderTab({ ...web, active_deployment_id: "d9" } as Service);
    expect(await screen.findAllByText("Couldn’t load the deployments of web.")).toHaveLength(1);
    expect(screen.queryByText("Not deployed yet")).toBeNull();
    expect(await screen.findByText("web:1")).toBeTruthy();
  });

  it("S2: the metrics read failing says so, with no wait for samples", async () => {
    fails = (p) => p.includes("/metrics");
    renderTab();
    expect(await screen.findByText("Couldn’t load the usage of web.")).toBeTruthy();
    expect(screen.queryByText(/Collecting the first samples/)).toBeNull();
    expect(screen.queryByText("Reading usage…")).toBeNull();
    const retry = screen.getByRole("button", { name: /^Retry/ });
    fails = () => false;
    answers["/metrics"] = [point];
    fireEvent.click(retry);
    expect(await screen.findByRole("img", { name: /web CPU over/ })).toBeTruthy();
  });

  it("S2: with no samples yet it still waits for the first", async () => {
    renderTab();
    expect(await screen.findByText(/Collecting the first samples: web is sampled every minute/)).toBeTruthy();
  });

  it("S2: the restarts count and a stack's memory limit aren't claimed when resources fail", async () => {
    answers["/metrics"] = [point];
    fails = (p) => p.includes("/resources");
    renderTab({ ...web, source_type: "docker-compose" } as Service);
    await screen.findByRole("img", { name: /CPU over/ });
    await vi.waitFor(() => expect(dd("Restarts, 24h").textContent).toBe("—"));
    expect(screen.queryByText("no limit")).toBeNull();
  });

  it("S2: uptime stops waiting when a stack's containers can't be read", async () => {
    fails = (p) => p.includes("/containers");
    renderTab({ ...web, source_type: "docker-compose" } as Service);
    await vi.waitFor(() => expect(dd("Uptime").textContent).toBe("—"));
  });

  it("the environment's services failing shows its own row, with no claim there are none", async () => {
    fails = (p) => p.includes("/environments/e1/services");
    renderTab({ ...web, connect_to: { host: "web-prod", port: 80 } } as Service);
    expect(await screen.findByText("Couldn’t load the other services in this environment.")).toBeTruthy();
    expect(screen.queryByText("No other services in this environment yet.")).toBeNull();
    expect(screen.getByText("web-prod:80")).toBeTruthy();
  });
});

describe("Usage's memory kills line", () => {
  it("says nothing without kills", () => {
    expect(memoryKillsText(0, { total: 747, reserved: 1946, left: 0, over: true })).toBe("");
  });

  it("counts the kills", () => {
    expect(memoryKillsText(1, null)).toBe("Killed for memory once in 7 days.");
    expect(memoryKillsText(4, { total: 747, reserved: 512, left: 235, over: false })).toBe("Killed for memory 4 times in 7 days.");
  });

  it("says why on the panel's server when its apps reserve more than they get", () => {
    expect(memoryKillsText(4, { total: 747, reserved: 1946, left: 0, over: true })).toBe(
      "Killed for memory 4 times in 7 days. The panel’s server leaves its apps 747 MiB between them and their limits add up to 1.9 GiB, so a service can be killed before it reaches its own limit.",
    );
  });
});
