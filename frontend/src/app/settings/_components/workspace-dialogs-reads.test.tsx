import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { InviteDialog } from "./workspace-dialogs";

// "No former members to suggest" over a read that failed is a false claim.

vi.mock("@/context/AuthContext", () => ({ useAuth: () => ({ user: { id: "u1" } }) }));

function serve(status: number, body: unknown) {
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(body), { status })));
}
afterEach(() => vi.unstubAllGlobals());

function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <InviteDialog open workspace="QA Lab" onClose={() => {}} />
    </QueryClientProvider>,
  );
  fireEvent.click(screen.getByRole("button", { name: "Has an account here" }));
}

describe("Invite, Has an account here", () => {
  it("doesn't say there are no former members when the list failed to load", async () => {
    serve(500, { error: "internal error", status: 500 });
    show();
    expect(await screen.findByText("Could not load former members; type the email.")).toBeTruthy();
    expect(screen.queryByText(/No former members/)).toBeNull();
  });

  it("says there are none when the list answered empty", async () => {
    serve(200, { data: [] });
    show();
    expect(await screen.findByText("No former members to suggest; type the email.")).toBeTruthy();
  });
});
