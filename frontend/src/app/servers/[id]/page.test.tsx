import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import ServerPage from "./page";

// A failed read on a server's page is not an answer: V2's Capacity card and
// V10's "Nothing needs attention", and a shared server's invented zeros.

vi.mock("next/navigation", () => ({
  useParams: () => ({ id: "s1" }),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  usePathname: () => "/servers/s1",
  useSearchParams: () => new URLSearchParams(),
}));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ isLoading: false, user: { id: "u1", orgId: "o1", orgName: "QA Lab", orgs: [] } }),
  useOrgRole: () => "owner",
  useIsOperator: () => false,
}));

const server = { id: "s1", name: "web-1", status: "active", provisioned_at: "2026-01-01T00:00:00Z", ip_address: "10.0.0.1", port: 22, user: "root", created_at: "2026-01-01T00:00:00Z" };
const shared = { ...server, shared: true, shared_by: "Acme", shared_by_org_id: "o2", total_cpu_cores: 2, total_memory_mb: 2048 };
const capacity = { server_id: "s1", effective_cpu_cores: 4, effective_memory_mb: 8192, allocated_cpu_cores: 1, allocated_memory_mb: 2048, live_cpu_percent: 0, live_memory_mb: 0, capacity_discovered: true, share: false, workspaces: [] };
const ok = (v: unknown) => ({ status: 200, body: JSON.stringify({ data: v }) });
const boom = { status: 500, body: JSON.stringify({ error: "internal error", status: 500 }) };
const missing = { status: 404, body: JSON.stringify({ error: "not found", status: 404 }) };

type Answer = { status: number; body: string };
function serve(over: Record<string, Answer>) {
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    const a = over[new URL(url, "http://x").pathname] ?? ok(null);
    return new Response(a.body, { status: a.status });
  }));
}
afterEach(() => {
  vi.unstubAllGlobals();
  window.location.hash = "";
});

function show() {
  vi.stubGlobal("IntersectionObserver", class { observe() {} disconnect() {} });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><ServerPage /></QueryClientProvider>);
}

describe("a server this workspace owns, with its capacity read failed", () => {
  const answers = { "/api/v1/servers/s1": ok(server), "/api/v1/servers/s1/capacity-summary": boom, "/api/v1/servers/s1/fit": ok(null), "/api/v1/servers/s1/shares": ok([]) };

  it("its Capacity card says so with Retry, and doesn't say it's reading", async () => {
    serve(answers);
    show();
    const card = await screen.findByRole("region", { name: "Capacity" });
    expect(await within(card).findByText("Couldn’t load capacity.")).toBeInTheDocument();
    expect(within(card).getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
    expect(screen.queryByText("Reading capacity…")).toBeNull();
  });

  it("Needs attention names what wasn't checked, not that nothing needs it", async () => {
    serve(answers);
    show();
    expect(await screen.findByText("Couldn’t check capacity")).toBeInTheDocument();
    expect(screen.queryByText("Nothing needs attention")).toBeNull();
  });

  it("Retry reads it again", async () => {
    serve(answers);
    show();
    const card = await screen.findByRole("region", { name: "Capacity" });
    const retry = await within(card).findByRole("button", { name: /^Retry/ });
    serve({ ...answers, "/api/v1/servers/s1/capacity-summary": ok(capacity) });
    fireEvent.click(retry);
    expect(await within(card).findByText("1 of 4 vCPU")).toBeInTheDocument();
    expect(screen.queryByText("Couldn’t check capacity")).toBeNull();
  });
});

describe("a server this workspace owns, with its shares read failed", () => {
  const answers = { "/api/v1/servers/s1": ok(server), "/api/v1/servers/s1/capacity-summary": ok(capacity), "/api/v1/servers/s1/fit": ok(null), "/api/v1/servers/s1/shares": boom };

  it("Capacity says it can't show what's set aside, not that it's free, and Retry reads it", async () => {
    serve(answers);
    show();
    const card = await screen.findByRole("region", { name: "Capacity" });
    const failed = await within(card).findByText("Couldn’t load what’s set aside for shares.");
    expect(failed).toBeInTheDocument();
    serve({ ...answers, "/api/v1/servers/s1/shares": ok([]) });
    fireEvent.click(within(card).getByRole("button", { name: /^Retry/ }));
    await vi.waitFor(() => expect(within(card).queryByText("Couldn’t load what’s set aside for shares.")).toBeNull());
  });

  it("the Remove dialog says it couldn't read who shares the server", async () => {
    serve(answers);
    window.location.hash = "#remove";
    show();
    fireEvent.click(await screen.findByRole("button", { name: "Remove server…" }));
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText("Couldn’t load who shares this server.")).toBeInTheDocument();
  });
});

describe("a server this workspace owns, Needs attention's all-clear", () => {
  const answers = { "/api/v1/servers/s1": ok({ ...server, ip_address: "127.0.0.1" }), "/api/v1/servers/s1/capacity-summary": ok(capacity), "/api/v1/servers/s1/fit": ok(null), "/api/v1/servers/s1/shares": ok([]), "/api/v1/servers/s1/services": ok([]) };
  const pendingAt = (path: string) =>
    vi.stubGlobal("fetch", vi.fn(async (url: string) => {
      const p = new URL(url, "http://x").pathname;
      if (p === path) return new Promise<Response>(() => {});
      const a = (answers as Record<string, Answer>)[p] ?? ok(null);
      return new Response(a.body, { status: a.status });
    }));

  it("waits for a read still going", async () => {
    pendingAt("/api/v1/servers/s1/capacity-summary");
    show();
    expect(await screen.findByText("Checking…")).toBeInTheDocument();
    expect(screen.queryByText("Nothing needs attention")).toBeNull();
  });

  it("gives it when every read has answered", async () => {
    serve(answers);
    show();
    expect(await screen.findByText("Nothing needs attention")).toBeInTheDocument();
    expect(screen.queryByText("Checking…")).toBeNull();
  });
});

describe("Remove server, when the server's services couldn't be read", () => {
  const answers = { "/api/v1/servers/s1": ok(server), "/api/v1/servers/s1/capacity-summary": ok(capacity), "/api/v1/servers/s1/fit": ok(null), "/api/v1/servers/s1/shares": ok([]) };
  const open = async () => {
    window.location.hash = "#remove";
    show();
    fireEvent.click(await screen.findByRole("button", { name: "Remove server…" }));
    return screen.findByRole("dialog");
  };

  it("says so with Retry, and won't confirm", async () => {
    serve({ ...answers, "/api/v1/servers/s1/services": boom });
    const dialog = await open();
    expect(await within(dialog).findByText("Couldn’t load the services on this server.")).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Remove server" })).toBeDisabled();
  });

  it("won't confirm while the read is going, and doesn't say the server is empty", async () => {
    vi.stubGlobal("fetch", vi.fn(async (url: string) => {
      const p = new URL(url, "http://x").pathname;
      if (p === "/api/v1/servers/s1/services") return new Promise<Response>(() => {});
      const a = (answers as Record<string, Answer>)[p] ?? ok(null);
      return new Response(a.body, { status: a.status });
    }));
    const dialog = await open();
    expect(within(dialog).getByRole("button", { name: "Remove server" })).toBeDisabled();
  });

  it("asks for the service's removal once it has read them", async () => {
    serve({ ...answers, "/api/v1/servers/s1/services": ok([{ id: "a", name: "web", project_name: "shop" }]) });
    const dialog = await open();
    expect(await within(dialog).findByText(/1 service still runs here/)).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Remove server and 1 service" })).toBeDisabled();
  });
});

describe("a server shared with this workspace", () => {
  const answers = { "/api/v1/servers/s1": missing, "/api/v1/servers": ok([shared]) };

  it("says the share's reservations couldn't be read, and invents no zero", async () => {
    serve({ ...answers, "/api/v1/servers/s1/capacity-summary": boom, "/api/v1/servers/s1/services": ok([]) });
    show();
    expect(await screen.findByText("Couldn’t load what this share has reserved.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
    expect(screen.queryByText(/^0 of/)).toBeNull();
  });

  it("says its services couldn't be read, and Loading… doesn't stay", async () => {
    serve({ ...answers, "/api/v1/servers/s1/capacity-summary": ok(capacity), "/api/v1/servers/s1/services": boom });
    show();
    expect(await screen.findByText("Couldn’t load the services here.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
    expect(screen.queryByText("Loading…")).toBeNull();
    expect(screen.queryByText(/^None yet/)).toBeNull();
  });
});

describe("a server while it is read", () => {
  it("announces its skeleton", async () => {
    vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>(() => {})));
    show();
    expect(await screen.findByRole("status", { name: "Loading the server" })).toBeInTheDocument();
  });
});
