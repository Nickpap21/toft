import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import AuthorizePage from "./page";

// A failed read of the workspace's projects isn't "has no projects yet".

let projects: { id: string; name: string }[] | null = null;

vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ user: { name: "Nick", orgId: "o1", orgs: [{ org_id: "o1", name: "Founders", role: "owner" }] }, logout: vi.fn() }),
}));
vi.mock("@/lib/api/agents.service", () => ({
  agentService: { authorizeRequest: async () => ({ name: "Claude Code", kind: "dcr", scopes: [], requested: [], redirect_host: "127.0.0.1:3000", verified: "" }), authorize: vi.fn() },
}));
vi.mock("@/lib/api/client", async (importOriginal) => {
  const orig = await importOriginal<typeof import("@/lib/api/client")>();
  return {
    ...orig,
    fetchAPI: async (path: string) => {
      if (path === "/api/v1/projects") {
        if (!projects) throw new orig.ApiError("boom", 500, { error: "boom" });
        return projects;
      }
      return {};
    },
  };
});

function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><AuthorizePage /></QueryClientProvider>);
}

describe("The consent screen's project list", () => {
  beforeEach(() => {
    projects = null;
  });

  it("says it couldn't load, with Retry, not that the workspace has none", async () => {
    mount();
    expect(await screen.findByText("Couldn’t load Founders’ projects.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
    expect(screen.queryByText(/has no projects yet/)).toBeNull();
  });

  it("Retry reads again, and the projects replace the failure", async () => {
    mount();
    const retry = await screen.findByRole("button", { name: /^Retry/ });
    projects = [{ id: "p1", name: "shop" }];
    fireEvent.click(retry);
    expect(await screen.findByText("shop")).toBeInTheDocument();
  });

  it("an empty workspace still says so", async () => {
    projects = [];
    mount();
    expect(await screen.findByText(/Founders has no projects yet/)).toBeInTheDocument();
  });
});
