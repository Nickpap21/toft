import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import StorageSettingsPage from "./page";

vi.mock("next/navigation", () => ({ usePathname: () => "/settings/storage" }));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ isLoading: false, user: { id: "u-1", email: "a@example.test", orgName: "Founders" } }),
  useOrgRole: () => "owner",
  useIsOperator: () => false,
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
vi.mock("@/lib/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/client")>()),
  fetchAPI: async (path: string) =>
    path === "/api/v1/shared-volumes"
      ? [{ id: "v1", name: "images", server_id: "", server_name: "", size_limit_bytes: 1024 ** 3, used_bytes: null, used_at: null, mounts: [], created_at: "2026-11-01T00:00:00Z" }]
      : [],
}));

// WCAG 2.5.3: the name starts with the words the button shows.
it("names Change limit with what it reads", async () => {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <StorageSettingsPage />
    </QueryClientProvider>,
  );
  expect(await screen.findByRole("button", { name: "Change limit of images" })).toBeTruthy();
});
