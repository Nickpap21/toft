import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import InstanceAuditLogPage from "./page";

vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ user: { id: "u-1" } }),
  useIsOperator: () => true,
}));
vi.mock("@/components/instance/instance-frame", () => ({
  InstanceFrame: ({ children }: { children: ReactNode }) => <div>{children}</div>,
}));

const entry = (id: string, action: string) => ({ id, action, detail: {}, created_at: "2026-11-08T10:00:00Z" });

/** Answers each request with what `page` returns, keeping the URLs asked for. */
function stubLog(page: (url: URL) => { data: unknown[]; next?: string }) {
  const urls: URL[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof globalThis.fetch>(async (input) => {
      const url = new URL(String(input), location.origin);
      urls.push(url);
      return new Response(JSON.stringify(page(url)), { status: 200 });
    }),
  );
  return urls;
}

function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <InstanceAuditLogPage />
    </QueryClientProvider>,
  );
}

afterEach(() => vi.unstubAllGlobals());

describe("Instance → Audit log", () => {
  it("asks the server for the kind the filter shows, and for all without one", async () => {
    const urls = stubLog((url) => ({
      data: url.searchParams.get("kind") === "accounts" ? [entry("a", "operator.granted")] : [entry("p", "panel.domain_changed")],
    }));
    show();
    await waitFor(() => expect(urls).toHaveLength(1));
    expect(urls[0].searchParams.has("kind")).toBe(false);

    await userEvent.click(await screen.findByRole("button", { name: "Accounts" }));

    await waitFor(() => expect(urls.at(-1)?.searchParams.get("kind")).toBe("accounts"));
    await userEvent.click(screen.getByRole("button", { name: "The panel" }));
    await waitFor(() => expect(urls.at(-1)?.searchParams.get("kind")).toBe("panel"));
  });

  it("doesn't leave one filter's rows under another while that one loads", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof globalThis.fetch>((input) =>
        new URL(String(input), location.origin).searchParams.get("kind") === "accounts"
          ? new Promise(() => {})
          : Promise.resolve(new Response(JSON.stringify({ data: [entry("p", "recovery_kit.viewed")] }), { status: 200 })),
      ),
    );
    show();
    await screen.findByText("Viewed the recovery kit");

    await userEvent.click(screen.getByRole("button", { name: "Accounts" }));

    expect(await screen.findByLabelText("Loading")).toBeInTheDocument();
    expect(screen.queryByText("Viewed the recovery kit")).not.toBeInTheDocument();
  });

  it("never says there are no changes while older entries can be loaded", async () => {
    stubLog(() => ({ data: [], next: "older" }));
    show();

    await screen.findByRole("button", { name: "Show older" });
    expect(screen.queryByText(/No changes yet/)).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "The panel" }));
    expect(await screen.findByText("Nothing loaded yet. Show older to look further back.")).toBeInTheDocument();
    expect(screen.queryByText(/Nothing done to the panel yet/)).not.toBeInTheDocument();
  });

  it("says the empty lines once the whole log is loaded", async () => {
    stubLog(() => ({ data: [] }));
    show();

    expect(await screen.findByText(/No changes yet/)).toBeInTheDocument();
  });

  it("says search covers the loaded entries, and how to reach further back", async () => {
    stubLog(() => ({ data: [entry("p", "panel.domain_changed")], next: "older" }));
    show();

    const search = await screen.findByPlaceholderText("Search loaded entries");
    expect(search).toHaveAccessibleName("Search loaded entries");
    await screen.findByRole("button", { name: "Show older" });
    await userEvent.type(search, "zebra");

    expect(await screen.findByText("Nothing loaded matches “zebra”. Show older to search further back.")).toBeInTheDocument();
  });

  it("drops the further-back hint on the last page", async () => {
    stubLog(() => ({ data: [entry("p", "panel.domain_changed")] }));
    show();

    await userEvent.type(await screen.findByPlaceholderText("Search loaded entries"), "zebra");

    expect(await screen.findByText("Nothing loaded matches “zebra”.")).toBeInTheDocument();
  });
});
