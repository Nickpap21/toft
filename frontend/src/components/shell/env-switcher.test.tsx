import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { Environment } from "@/lib/api/environment.service";
import type { WorkspaceService } from "@/lib/api/service.service";
import { ALL, type useEnvContext } from "./env-context";
import { EnvSwitcher } from "./env-switcher";

// The services read is quiet in the sidebar: a failed one is drawn as
// nothing, never as "No services".

vi.mock("next/navigation", () => ({
  usePathname: () => "/projects/p1",
  useRouter: () => ({ push: vi.fn() }),
}));
vi.mock("@/context/AuthContext", () => ({ useOrgRole: () => "viewer" }));

const environments = [{ id: "e1", project_id: "p1", name: "production", color: "gray", protected: true, created_at: "", updated_at: "" }] as Environment[];
const ctx = { envId: ALL, env: undefined, set: vi.fn() } as unknown as ReturnType<typeof useEnvContext>;

async function openMenu(services: WorkspaceService[] | undefined) {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <EnvSwitcher projectId="p1" environments={environments} services={services} current={undefined} shown={undefined} ctx={ctx} variant="side" />
    </QueryClientProvider>,
  );
  await userEvent.click(screen.getByRole("button", { name: /switch environment/ }));
  return screen.findByRole("menu");
}

describe("EnvSwitcher", () => {
  it("claims nothing about an environment when the services weren't read", async () => {
    const menu = await openMenu(undefined);
    expect(within(menu).queryByText("No services")).toBeNull();
    expect(within(menu).queryByText(/\d services/)).toBeNull();
  });

  it("says No services once the read has answered", async () => {
    const menu = await openMenu([]);
    expect(within(menu).getByText("No services")).toBeTruthy();
    expect(within(menu).getByText("0 services")).toBeTruthy();
  });
});
