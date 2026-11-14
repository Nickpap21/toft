import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PanelServerCard } from "./panel-server";

vi.mock("@/context/AuthContext", () => ({ useIsOperator: () => false }));

const host = { role: "apps", address: "10.0.0.1", cpu_cores: 4, memory_bytes: 8 * 2 ** 30, reservation: { apps_cpu: 2, apps_memory_bytes: 2 ** 30, enforced: true } };
const ok = (v: unknown) => ({ status: 200, body: JSON.stringify({ data: v }) });
const boom = { status: 500, body: JSON.stringify({ error: "boom", status: 500 }) };

function serve(answers: Record<string, { status: number; body: string }>) {
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    const a = answers[new URL(url, "http://x").pathname] ?? ok([]);
    return new Response(a.body, { status: a.status });
  }));
}
afterEach(() => vi.unstubAllGlobals());

function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><PanelServerCard /></QueryClientProvider>);
}

describe("the panel's server on Servers", () => {
  it("says it couldn't be read, with Retry, rather than leaving the list short", async () => {
    serve({ "/api/v1/instance/panel-host": boom });
    show();
    expect(await screen.findByText("Couldn’t load the panel’s server.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
  });

  it("doesn't say nothing runs there when its services couldn't be read", async () => {
    serve({ "/api/v1/instance/panel-host": ok(host), "/api/v1/workspace/services": boom });
    show();
    await screen.findByRole("heading", { name: "The panel’s server" });
    expect(await screen.findByText("—")).toBeInTheDocument();
    expect(screen.queryByText("Nothing of this workspace")).toBeNull();
  });

  it("doesn't say nothing runs there while the services are being read", async () => {
    vi.stubGlobal("fetch", vi.fn(async (url: string) => {
      const p = new URL(url, "http://x").pathname;
      if (p === "/api/v1/workspace/services") return new Promise<Response>(() => {});
      return new Response(JSON.stringify({ data: host }), { status: 200 });
    }));
    show();
    await screen.findByRole("heading", { name: "The panel’s server" });
    expect(screen.getByText("—")).toBeInTheDocument();
    expect(screen.queryByText("Nothing of this workspace")).toBeNull();
  });

  it("says what the services there reserve of what apps get, and warns past it", async () => {
    const reserving = (bytes: number) => ({ ...host, reservation: { ...host.reservation, apps_allocated_memory_bytes: bytes } });
    serve({ "/api/v1/instance/panel-host": ok(reserving(512 * 2 ** 20)) });
    show();
    const line = await screen.findByText(/512 MiB of 1\.0 GiB/);
    expect(line.textContent).toBe("Reserved 512 MiB of 1.0 GiB");
    expect(line.className).not.toContain("text-status-pending-fg");
  });

  it("warns when more is reserved there than apps get", async () => {
    serve({ "/api/v1/instance/panel-host": ok({ ...host, reservation: { ...host.reservation, apps_allocated_memory_bytes: 2 * 2 ** 30 } }) });
    show();
    const line = await screen.findByText(/2\.0 GiB of 1\.0 GiB/);
    expect(line.textContent).toBe("Reserved 2.0 GiB of 1.0 GiB: more than apps get, so a service can be killed for memory before it reaches its own limit.");
    expect(line.className).toContain("text-status-pending-fg");
  });
});
