import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { Service } from "@/lib/api/service.service";
import { ServiceRowActions } from "./service-actions";

// A row's Deploy, for a service in an environment the row doesn't know
// yet, waits for it: it never deploys past a protected one's confirm (D-55).

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn() }) }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
const posts: string[] = [];
vi.mock("@/lib/api/client", async (importOriginal) => {
  const orig = await importOriginal<typeof import("@/lib/api/client")>();
  return {
    ...orig,
    fetchAPI: async (path: string, init?: RequestInit) => {
      if (init?.method === "POST") posts.push(path);
      return [];
    },
  };
});

const web = { id: "s1", name: "web", project_id: "p1", environment_id: "e1", server_id: "", source_type: "docker-image", runtime_status: "running" } as Service;

describe("a service row's Deploy", () => {
  it("is disabled while the service’s environment is unknown", async () => {
    render(
      <QueryClientProvider client={new QueryClient()}>
        <ServiceRowActions service={web} serverName="its server" canDeploy />
      </QueryClientProvider>,
    );
    const deploy = screen.getByRole("button", { name: /^Deploy/ });
    fireEvent.click(deploy);
    await new Promise((r) => setTimeout(r, 0));
    expect(posts).toEqual([]);
    expect((deploy as HTMLButtonElement).disabled).toBe(true);
  });
});
