import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { toast } from "sonner";
import type { Server } from "@/lib/api/server.service";
import { AgentConnection, AgentDialogs } from "./agent-card";

vi.mock("sonner", () => ({ toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }) }));

const server = { id: "s1", name: "web-1", agent: { status: "connected" } } as unknown as Server;

afterEach(() => {
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

describe("Connection, for an agent that refused the panel's version", () => {
  const behind = {
    id: "s1", name: "web-1", connection: "agent",
    agent: { status: "connected", version: "v0.17.0", arch: "amd64", upgrade: { panel_version: "v0.17.1", reason: "refused", detail: "<b>not signed</b>", at: "2026-10-22T14:02:00Z", compose_refused: true } },
  } as unknown as Server;
  const offering = { ...behind, agent: { ...behind.agent, upgrade: { ...behind.agent!.upgrade!, offering: true, reason: null, detail: null, at: null } } } as Server;
  const show = (canAdmin: boolean, s = behind) => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return { client, ...render(<QueryClientProvider client={client}><AgentConnection server={s} canAdmin={canAdmin} isOwner={canAdmin} onDialog={() => {}} /></QueryClientProvider>) };
  };

  it("says why in the agent's words, as text, and offers the upgrade again", async () => {
    const calls: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
      calls.push(`${init?.method ?? "GET"} ${new URL(url, "http://x").pathname}`);
      return String(url).includes("/agent/upgrade")
        ? new Response(JSON.stringify({ data: offering }), { status: 202 })
        : new Response(JSON.stringify({ data: [] }), { status: 200 });
    }));
    const { container, client } = show(true);
    expect(screen.getByText("Behind the panel, which runs v0.17.1")).toBeInTheDocument();
    expect(screen.getByText("<b>not signed</b>")).toBeInTheDocument();
    expect(container.querySelector("code b")).toBeNull();
    expect(screen.getByText(/Compose deploys to this server are refused until its agent runs v0\.17\.1\./)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Offer the upgrade again" }));
    await waitFor(() => expect(calls).toContain("POST /api/v1/servers/s1/agent/upgrade"));
    await waitFor(() => expect(client.getQueryData<Server>(["server", "s1"])?.agent?.upgrade?.offering).toBe(true));
    expect(toast).not.toHaveBeenCalled();
    expect(toast.error).not.toHaveBeenCalled();
  });
  it("shows an offer that runs until the agent answers it", () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ data: [] }), { status: 200 })));
    show(true, offering);
    expect(screen.getByText("The panel is offering v0.17.1 to the agent now.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Offering…" })).toBeDisabled();
    expect(screen.queryByText("<b>not signed</b>")).toBeNull();
  });
  it("says so in a toast when an offer runs already", async () => {
    const msg = "the panel is offering this agent its version already: its answer shows on the Connection card";
    vi.stubGlobal("fetch", vi.fn(async (url: string) =>
      String(url).includes("/agent/upgrade")
        ? new Response(JSON.stringify({ error: msg, status: 409, data: { code: "agent_upgrade_running" } }), { status: 409 })
        : new Response(JSON.stringify({ data: [] }), { status: 200 }),
    ));
    show(true);
    fireEvent.click(screen.getByRole("button", { name: "Offer the upgrade again" }));
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith(msg));
  });
  it("has no button for someone who can't offer it", () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ data: [] }), { status: 200 })));
    show(false);
    expect(screen.getByText("Behind the panel, which runs v0.17.1")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Offer the upgrade again" })).toBeNull();
    expect(screen.getByText("An owner or instance operator can offer it again here.")).toBeInTheDocument();
  });
});

describe("Revoke, when the server's services can't be read", () => {
  it("says it couldn't check who loses name lookups, with Retry", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: "boom", status: 500 }), { status: 500 })));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}><AgentDialogs server={server} kind="revoke" onClose={() => {}} /></QueryClientProvider>);
    expect(await screen.findByText("Couldn’t load the services that use this agent.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
  });
});
