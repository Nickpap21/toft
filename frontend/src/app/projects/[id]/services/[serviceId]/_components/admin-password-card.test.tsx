import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Service } from "@/lib/api/service.service";
import { AdminPasswordCard } from "./admin-password-card";

// D-160: the creator's copy of a catalog app's admin password, until
// dismissed. Only owners and developers ask for it; the value only after
// the password is confirmed; Dismiss deletes it.

let role = "owner";
let confirmed = true;
let catalogFails = false;
vi.mock("@/context/AuthContext", () => ({ useOrgRole: () => role }));
vi.mock("@/components/reauth", () => ({ useReauth: () => async () => confirmed }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const calls: string[] = [];
let pending: unknown = {
  pending: true, what: "admin_password", key: "GF_SECURITY_ADMIN_PASSWORD", reason: "created",
  created_at: "2026-11-06T09:00:00Z", expires_at: "2026-11-13T09:00:00Z",
};
vi.mock("@/lib/api/client", async (orig) => ({
  ...(await orig<typeof import("@/lib/api/client")>()),
  fetchAPI: async (path: string, init?: RequestInit) => {
    calls.push(`${init?.method ?? "GET"} ${path}`);
    if (path === "/api/v1/catalog" && catalogFails) throw new Error("boom");
    if (path === "/api/v1/catalog") return [{ slug: "grafana", name: "Grafana" }];
    if (path.endsWith("/admin-password/value")) return { what: "admin_password", key: "GF_SECURITY_ADMIN_PASSWORD", password: "s3cret-Value" };
    if (path.endsWith("/admin-password")) return init?.method === "DELETE" ? undefined : pending;
    return null;
  },
}));

const grafana = { id: "g1", name: "grafana", addon_slug: "grafana" } as Service;

function renderCard() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <AdminPasswordCard service={grafana} serviceId="g1" />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  calls.length = 0;
  role = "owner";
  confirmed = true;
  catalogFails = false;
});

describe("AdminPasswordCard", () => {
  it("still shows a waiting password when the catalog can't name the app", async () => {
    catalogFails = true;
    renderCard();
    expect(await screen.findByRole("heading", { name: "Your grafana admin password" })).toBeTruthy();
  });

  it("shows the value only after the password is confirmed, then deletes it on Done", async () => {
    renderCard();
    expect(await screen.findByRole("heading", { name: "Your Grafana admin password" })).toBeTruthy();
    expect(screen.queryByDisplayValue("s3cret-Value")).toBeNull();

    confirmed = false;
    fireEvent.click(screen.getByRole("button", { name: "Show password…" }));
    await vi.waitFor(() => expect(screen.getByRole("button", { name: "Show password…" }).hasAttribute("disabled")).toBe(false));
    expect(calls.some((c) => c.endsWith("/admin-password/value"))).toBe(false);

    confirmed = true;
    fireEvent.click(screen.getByRole("button", { name: "Show password…" }));
    expect(await screen.findByDisplayValue("s3cret-Value")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Done, I stored it" }));
    await vi.waitFor(() => expect(screen.queryByRole("heading", { name: "Your Grafana admin password" })).toBeNull());
    expect(calls).toContain("DELETE /api/v1/services/g1/admin-password");
  });

  it("asks before deleting a copy nobody has seen", async () => {
    pending = { pending: true, what: "admin_password", key: "GF_SECURITY_ADMIN_PASSWORD", reason: "created", created_at: "2026-11-06T09:00:00Z", expires_at: "2026-11-13T09:00:00Z" };
    renderCard();
    fireEvent.click(await screen.findByRole("button", { name: "Delete password…" }));
    fireEvent.click(await screen.findByRole("button", { name: "Keep it" }));
    expect(calls.some((c) => c.startsWith("DELETE"))).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Delete password…" }));
    fireEvent.click(await screen.findByRole("button", { name: "Delete password" }));
    await vi.waitFor(() => expect(calls).toContain("DELETE /api/v1/services/g1/admin-password"));
  });

  it("asks nothing for a viewer", async () => {
    role = "viewer";
    renderCard();
    await new Promise((r) => setTimeout(r, 20));
    expect(calls).toEqual([]);
  });

  it("renders nothing without a copy waiting", async () => {
    pending = { pending: false };
    const { container } = renderCard();
    await vi.waitFor(() => expect(calls).toContain("GET /api/v1/services/g1/admin-password"));
    expect(container.textContent).toBe("");
  });
});
