import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WorkspaceService } from "@/lib/api/service.service";
import { DeployButton } from "./deploy";

// The Domains page's Deploy <service> (7g step 8): a protected
// environment's service confirms first, as everywhere else (D-55).

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn() }) }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

let prodProtected = true;
const posts: string[] = [];
vi.mock("@/lib/api/client", async (importOriginal) => {
  const orig = await importOriginal<typeof import("@/lib/api/client")>();
  return {
    ...orig,
    fetchAPI: async (path: string, init?: RequestInit) => {
      if (init?.method === "POST") {
        posts.push(path);
        return { id: "d-new" };
      }
      if (path === "/api/v1/workspace/environments") return [{ id: "e1", project_id: "p1", name: "production", color: "gray", protected: prodProtected, preview: false }];
      if (path === "/api/v1/services/s1")
        return { id: "s1", name: "web", project_id: "p1", environment_id: "e1", server_id: "", source_type: "docker-image", docker_image: "nginx:1.27", active_deployment_id: "" };
      if (path === "/api/v1/projects/p1/overview") return { environments: [], services: [], recent_deployments: [] };
      return [];
    },
  };
});

const web = { id: "s1", name: "web", project_id: "p1", environment_id: "e1", server_id: "", scope: "environment", source_type: "docker-image" } as WorkspaceService;

function wrap() {
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <DeployButton service={web} className="" />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  posts.length = 0;
  prodProtected = true;
});

describe("Domains page Deploy", () => {
  it("opens the protected confirm and deploys only from it", async () => {
    wrap();
    fireEvent.click(screen.getByRole("button", { name: "Deploy web" }));
    const go = await screen.findByRole("button", { name: /^Deploy .*to production$/ });
    expect(screen.getByRole("alertdialog")).toBeTruthy();
    expect(posts).toEqual([]);
    fireEvent.click(go);
    await waitFor(() => expect(posts).toEqual(["/api/v1/services/s1/deployments"]));
  });

  it("deploys at once in an unprotected environment", async () => {
    prodProtected = false;
    wrap();
    fireEvent.click(screen.getByRole("button", { name: "Deploy web" }));
    await waitFor(() => expect(posts).toEqual(["/api/v1/services/s1/deployments"]));
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });
});
