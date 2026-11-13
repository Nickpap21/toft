import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AuthProvider } from "@/context/AuthContext";
import { formatDateTime } from "@/lib/utils";
import AccountSettingsPage from "./page";

vi.mock("sonner", () => ({ toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }) }));
vi.mock("next/navigation", () => ({ usePathname: () => "/settings/account" }));

const now = Math.floor(Date.now() / 1000);
const jwt = (iat: number) => `x.${btoa(JSON.stringify({ sub: "u-1", iat, exp: iat + 30 * 86400 }))}.y`;
const oldIat = now - 5 * 86400;
const freshIat = now;
const fresh = jwt(freshIat);

function stubAPI() {
  const fetch = vi.fn<typeof globalThis.fetch>(async (input) => {
    const path = new URL(String(input), location.origin).pathname;
    const ok = (data: unknown) => new Response(JSON.stringify({ data }), { status: 200 });
    if (path === "/api/v1/me") return ok({ id: "u-1", email: "a@example.test", name: "A", is_operator: false });
    if (path === "/api/v1/org/list") return ok([]);
    if (path === "/api/v1/me/sessions/sign-out-others") return ok({ message: "other sessions signed out", token: fresh });
    return new Response("{}", { status: 404 });
  });
  vi.stubGlobal("fetch", fetch);
}

afterEach(() => vi.unstubAllGlobals());

describe("Account → Sessions", () => {
  it("after signing out the other browsers, this one shows and uses the fresh session", async () => {
    localStorage.setItem("paas_token", jwt(oldIat));
    localStorage.setItem("paas_user", JSON.stringify({ id: "u-1", email: "a@example.test" }));
    stubAPI();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <AuthProvider>
          <AccountSettingsPage />
        </AuthProvider>
      </QueryClientProvider>,
    );

    const since = (iat: number) => formatDateTime(new Date(iat * 1000).toISOString());
    expect(await screen.findByText(since(oldIat))).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Sign out other browsers…" }));
    await userEvent.click(screen.getByRole("button", { name: "Sign out other browsers" }));

    expect(await screen.findByText(since(freshIat))).toBeInTheDocument();
    expect(screen.queryByText(since(oldIat))).not.toBeInTheDocument();
    expect(localStorage.getItem("paas_token")).toBe(fresh);
  });
});
