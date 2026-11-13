import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { formatDateTime } from "@/lib/utils";
import AccountsPage from "./page";

vi.mock("@/components/instance/instance-frame", () => ({
  InstanceFrame: ({ children }: { children: ReactNode }) => <>{children}</>,
}));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ user: { id: "u-me" }, isLoading: false }),
  useIsOperator: () => true,
}));

// 11:00 UTC on the 4th: the string's own date is the 5th, a viewer's from
// UTC-11 to UTC+12 is the 4th, so the test doesn't depend on the machine's zone.
const grantedAt = "2026-10-05T01:00:00+14:00";

afterEach(() => vi.unstubAllGlobals());

describe("Instance → Accounts", () => {
  it("dates a grant the same on the Operators card and the account's row", async () => {
    const person = { email: "a@example.test", name: "A", is_active: true };
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof globalThis.fetch>(async (input) => {
        const path = new URL(String(input), location.origin).pathname;
        const ok = (data: unknown) => new Response(JSON.stringify({ data }), { status: 200 });
        if (path === "/api/v1/instance/operators") {
          return ok([{ ...person, user_id: "u-1", source: "api", granted_by: null, granted_at: grantedAt }]);
        }
        if (path === "/api/v1/instance/users") {
          return ok({
            deleted: 0,
            users: [{ ...person, id: "u-1", is_operator: true, created_at: "2025-01-02T10:00:00Z", deactivated_at: null, deactivated_by: "", last_login_at: null }],
          });
        }
        return new Response("{}", { status: 404 });
      }),
    );

    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <AccountsPage />
      </QueryClientProvider>,
    );

    const day = formatDateTime(grantedAt).slice(0, 10);
    expect(await screen.findByText(`Operator · since ${day}`)).toBeInTheDocument();
    expect(screen.getByText(day)).toBeInTheDocument();
  });

  // WCAG 2.5.3: the button's name starts with the words it shows.
  it("names the reset button with what it reads", async () => {
    const person = { is_active: true, is_operator: false, created_at: "2025-01-02T10:00:00Z", deactivated_at: null, deactivated_by: "", last_login_at: null };
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof globalThis.fetch>(async (input) => {
        const path = new URL(String(input), location.origin).pathname;
        const ok = (data: unknown) => new Response(JSON.stringify({ data }), { status: 200 });
        if (path === "/api/v1/instance/operators") return ok([]);
        if (path === "/api/v1/instance/users") {
          return ok({
            deleted: 0,
            users: [
              { ...person, id: "u-1", email: "sam@example.test", name: "sam", reset_expires_at: "2099-01-01T00:00:00Z" },
              { ...person, id: "u-2", email: "kim@example.test", name: "kim", reset_expires_at: null },
            ],
          });
        }
        return new Response("{}", { status: 404 });
      }),
    );

    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <AccountsPage />
      </QueryClientProvider>,
    );

    expect(await screen.findByRole("button", { name: "New reset link for sam" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reset password for kim" })).toBeInTheDocument();
  });
});
