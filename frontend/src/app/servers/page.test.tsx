import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import ServersPage from "./page";

// A failed read is never drawn as an answer: V2's "0 of 0 vCPU" and a
// "Reading capacity…" that never ends; V12's error with nothing to press.

let role = "owner";
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  usePathname: () => "/servers",
  useSearchParams: () => new URLSearchParams(),
}));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ isLoading: false, user: { id: "u1", orgId: "o1", orgName: "QA Lab", orgs: [] } }),
  useOrgRole: () => role,
  useIsOperator: () => false,
}));

const server = { id: "s1", name: "web-1", status: "active", provisioned_at: "2026-01-01T00:00:00Z", ip_address: "10.0.0.1", created_at: "2026-01-01T00:00:00Z" };
const capacity = {
  server_id: "s1",
  effective_cpu_cores: 4,
  effective_memory_mb: 8192,
  allocated_cpu_cores: 1,
  allocated_memory_mb: 2048,
  live_cpu_percent: 10,
  live_memory_mb: 1024,
  capacity_discovered: true,
  share: false,
  disk_used_mb: 90,
  disk_total_mb: 100,
  workspaces: [],
};
const fit = { capacity_known: true, fits: true, cpu_ok: true, memory_ok: true, share: false, effective_cpu_cores: 4, effective_memory_mb: 8192, allocated_cpu_cores: 1, allocated_memory_mb: 2048 };
const ok = (v: unknown) => ({ status: 200, body: JSON.stringify({ data: v }) });
const boom = { status: 500, body: JSON.stringify({ error: "internal error", status: 500 }) };

type Answer = { status: number; body: string };
function serve(over: Record<string, Answer> = {}) {
  const answers: Record<string, Answer> = {
    "/api/v1/servers": ok([server]),
    "/api/v1/servers/s1/capacity-summary": ok(capacity),
    "/api/v1/servers/s1/fit": ok(fit),
    "/api/v1/servers/s1/backup": ok(null),
    ...over,
  };
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    const a = answers[new URL(url, "http://x").pathname] ?? ok(null);
    return new Response(a.body, { status: a.status });
  }));
}
afterEach(() => {
  vi.unstubAllGlobals();
  role = "owner";
});

function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><ServersPage /></QueryClientProvider>);
}

describe("Servers", () => {
  it("says the capacity read failed, with Retry, and invents no zero", async () => {
    serve({ "/api/v1/servers/s1/capacity-summary": boom });
    show();
    expect(await screen.findByText("Couldn’t load the capacity of web-1.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
    expect(screen.queryByText("Reading capacity…")).toBeNull();
    expect(screen.queryByText(/0 of 0/)).toBeNull();
  });

  it("leaves the totals as — when a server's capacity failed", async () => {
    serve({ "/api/v1/servers/s1/capacity-summary": boom });
    show();
    await screen.findByText("Couldn’t load the capacity of web-1.");
    const totals = await screen.findByRole("region", { name: "Capacity" });
    expect(within(totals).getAllByText("—")).toHaveLength(2);
    expect(within(totals).getAllByText("Couldn’t read every server")).toHaveLength(2);
    expect(within(totals).queryByText(/of 0/)).toBeNull();
  });

  it("Retry reads the capacity again", async () => {
    let answers = boom;
    vi.stubGlobal("fetch", vi.fn(async (url: string) => {
      const path = new URL(url, "http://x").pathname;
      const a = path === "/api/v1/servers" ? ok([server]) : path.endsWith("/capacity-summary") ? answers : path.endsWith("/fit") ? ok(fit) : ok(null);
      return new Response(a.body, { status: a.status });
    }));
    show();
    const retry = await screen.findByRole("button", { name: /^Retry/ });
    answers = ok(capacity);
    fireEvent.click(retry);
    expect((await screen.findAllByText("1 of 4 vCPU")).length).toBeGreaterThan(0);
    expect(screen.queryByText("Couldn’t load the capacity of web-1.")).toBeNull();
  });

  it("while reading says so, without a zero or a failure", async () => {
    vi.stubGlobal("fetch", vi.fn(async (url: string) => {
      const path = new URL(url, "http://x").pathname;
      if (path.endsWith("/capacity-summary")) return new Promise<Response>(() => {});
      const a = path === "/api/v1/servers" ? ok([server]) : path.endsWith("/fit") ? ok(fit) : ok(null);
      return new Response(a.body, { status: a.status });
    }));
    show();
    expect(await screen.findByText("Reading capacity…")).toBeInTheDocument();
    expect(screen.queryByText("Couldn’t load the capacity of web-1.")).toBeNull();
    expect(screen.queryByText(/0 of 0/)).toBeNull();
  });

  it("the servers read failing has Retry and no empty state", async () => {
    serve({ "/api/v1/servers": boom });
    show();
    expect(await screen.findByRole("heading", { name: "Couldn’t load this page", level: 2 })).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("Toft couldn’t read this.");
    expect(screen.getByRole("alert")).not.toHaveTextContent("internal error");
    expect(screen.queryByText("No servers yet")).toBeNull();
    serve();
    fireEvent.click(screen.getByRole("button", { name: /^Retry/ }));
    expect(await screen.findByRole("heading", { name: "web-1" })).toBeInTheDocument();
  });

  it("a server's other reads failing leave its block standing", async () => {
    serve({ "/api/v1/servers/s1/fit": boom, "/api/v1/servers/s1/backup": boom });
    show();
    expect((await screen.findAllByText("1 of 4 vCPU")).length).toBeGreaterThan(0);
    expect(screen.queryByText("Couldn’t load the capacity of web-1.")).toBeNull();
  });

  // V9: a viewer's Maintenance has no Free or Delete, so nothing here offers them.
  it("offers a viewer Review, not a step they can't take", async () => {
    role = "viewer";
    serve();
    show();
    const row = await screen.findByRole("link", { name: /^Review ?of web-1$/ });
    expect(row).toHaveAttribute("href", "/servers/s1#disk");
    expect(screen.queryByRole("link", { name: /^Free space/ })).toBeNull();
  });

  it("offers an owner the action", async () => {
    serve();
    show();
    expect(await screen.findByRole("link", { name: /^Free space… ?of web-1$/ })).toHaveAttribute("href", "/servers/s1#disk");
  });
});
