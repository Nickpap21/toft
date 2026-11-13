import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Server } from "@/lib/api/server.service";
import { AgentDialogs } from "./agent-card";

const server = { id: "s1", name: "web-1", agent: { status: "connected" } } as unknown as Server;

afterEach(() => vi.unstubAllGlobals());

describe("Revoke, when the server's services can't be read", () => {
  it("says it couldn't check who loses name lookups, with Retry", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: "boom", status: 500 }), { status: 500 })));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}><AgentDialogs server={server} kind="revoke" onClose={() => {}} /></QueryClientProvider>);
    expect(await screen.findByText("Couldn’t load the services that use this agent.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
  });
});
