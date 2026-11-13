import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ProjectEnvSwitcher } from "./project-sidebar";

// A service page names the service's own environment (D-55). Unread, it
// names none: not the one the project was last looked at in.

vi.mock("next/navigation", () => ({
  useParams: () => ({ id: "p1" }),
  usePathname: () => "/projects/p1/services/s1",
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useSearchParams: () => new URLSearchParams("env=e2"),
}));
vi.mock("@/context/AuthContext", () => ({ useAuth: () => ({ token: "t", user: { id: "u1", orgName: "QA" } }), useOrgRole: () => "viewer", useIsOperator: () => false }));

const environments = [
  { id: "e1", project_id: "p1", name: "production", color: "gray", protected: true, created_at: "", updated_at: "" },
  { id: "e2", project_id: "p1", name: "staging", color: "amber", protected: false, created_at: "", updated_at: "" },
];
const ok = (v: unknown) => new Response(JSON.stringify({ data: v }), { status: 200 });

function serve(services: () => Response) {
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    const p = new URL(url, "http://x").pathname;
    if (p === "/api/v1/projects/p1") return ok({ id: "p1", name: "shop" });
    if (p === "/api/v1/projects/p1/environments") return ok(environments);
    if (p === "/api/v1/workspace/services") return services();
    return ok([]);
  }));
}
afterEach(() => vi.unstubAllGlobals());

function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><ProjectEnvSwitcher variant="side" /></QueryClientProvider>);
  return client;
}

describe("the environment switcher on a service page", () => {
  it("names the service's environment once the services are read", async () => {
    serve(() => ok([{ id: "s1", name: "web", project_id: "p1", environment_id: "e1", scope: "environment", runtime_status: "running" }]));
    show();
    expect(await screen.findByText("production")).toBeInTheDocument();
    expect(screen.queryByText("staging")).toBeNull();
  });

  it("names none while the services read has failed, not the context's", async () => {
    serve(() => new Response(JSON.stringify({ error: "boom", status: 500 }), { status: 500 }));
    const client = show();
    await vi.waitFor(() => expect(client.getQueryState(["workspace-services"])?.status).toBe("error"));
    await vi.waitFor(() => expect(client.getQueryData(["environments", "p1"])).toBeTruthy());
    await new Promise((r) => setTimeout(r, 0));
    expect(screen.queryByText("staging")).toBeNull();
    expect(screen.queryByText("production")).toBeNull();
    expect(screen.queryByText("All environments")).toBeNull();
  });
});
