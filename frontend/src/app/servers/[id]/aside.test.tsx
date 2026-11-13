import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { ProvisionRun, Server, ServerCapabilities } from "@/lib/api/server.service";
import { capabilityService } from "@/lib/api/server.service";
import { Firewall } from "./aside";

let caps: Partial<ServerCapabilities> | null = null;
let run: ProvisionRun | null = null;
vi.mock("@/lib/api/server.service", () => ({
  serverService: { lastProvision: async () => run },
  capabilityService: { get: async () => caps },
}));

const server = { id: "s1", port: 22, app_networks: "isolated" } as Server;
const ok: ProvisionRun = { started_at: "", started_by: "", finished_at: "2026-11-04T10:00:00Z", ok: true, error: "", log: "", warnings: [] };

function renderCard() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}><Firewall server={server} /></QueryClientProvider>);
}

describe("Firewall's app networks", () => {
  it("shows the pool Docker on the server reported", async () => {
    caps = { address_pools: [{ base: "10.50.0.0/16", size: 26 }] };
    run = ok;
    renderCard();
    expect(await screen.findByText("10.50.0.0/16 in /26s")).toBeTruthy();
    expect(screen.queryByText(/10\.203/)).toBeNull();
  });
  it("says not set, with the fix, when Docker has none", async () => {
    caps = { address_pools: [] };
    run = { ...ok, warnings: ["the app network address pool"] };
    renderCard();
    expect(await screen.findByText("not set")).toBeTruthy();
    expect(await screen.findByText(/Needs attention, on Overview, says how to set it/)).toBeTruthy();
    expect(screen.queryByText(/10\.203/)).toBeNull();
  });
  it("points at Re-provision when no run gave the fix", async () => {
    caps = { address_pools: [] };
    run = ok;
    renderCard();
    expect(await screen.findByText("not set")).toBeTruthy();
    expect(screen.getByText(/Re-provision says how to set it/)).toBeTruthy();
  });
  it("says it isn't known yet before a probe read it", async () => {
    caps = { kernel: "6.8.0" };
    run = ok;
    renderCard();
    const value = await screen.findByText("not known yet");
    expect(value.className).not.toContain("font-mono");
  });
  it("says it couldn’t be read when the capabilities read failed", async () => {
    caps = null;
    run = ok;
    const get = vi.spyOn(capabilityService, "get").mockRejectedValueOnce(new Error("boom"));
    renderCard();
    const value = await screen.findByText("couldn’t be read");
    expect(value.className).not.toContain("font-mono");
    expect(screen.queryByText("not known yet")).toBeNull();
    expect(screen.getByText(/Toft couldn’t read what this server can run/)).toBeTruthy();
    get.mockRestore();
  });
});
