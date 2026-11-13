import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import AuditLogPage from "./page";

vi.mock("next/navigation", () => ({ useSearchParams: () => new URLSearchParams() }));
vi.mock("@/context/AuthContext", () => ({ useAuth: () => ({ user: { orgName: "Founders" } }) }));

const target = { kind: "service", id: "s-1", name: "storefront / api", deleted: false, project_id: "p-1", service_id: "s-1", environment: "production" };
const entry = (id: string, actor: string, extra = {}) => ({
  id, actor, actor_id: "u-1", actor_email: "a@toft.test", action: "deployment.triggered", resource: "x", target, detail: {},
  created_at: "2026-11-08T10:00:00Z", agent_id: null, agent_name: null, api_key_id: null, api_key_name: null, ...extra,
});
const answer = (status: number, body: unknown) => new Response(JSON.stringify(body), { status });
const boom = () => answer(500, { error: "boom" });
const total = () => answer(200, { data: { total: 1 } });
const isCount = (url: URL) => url.pathname.endsWith("/count");

function stubAPI(api: (url: URL) => Response | Promise<Response>) {
  vi.stubGlobal("fetch", vi.fn<typeof globalThis.fetch>(async (input) => api(new URL(String(input), location.origin))));
}

function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <AuditLogPage />
    </QueryClientProvider>,
  );
  return client;
}

afterEach(() => vi.unstubAllGlobals());

describe("Audit log failures", () => {
  it("keeps the rows when older entries fail, and retries beside them", async () => {
    let fails = true;
    stubAPI((url) => {
      if (isCount(url)) return total();
      if (!url.searchParams.get("cursor")) return answer(200, { data: [entry("a", "alex")], next: "c" });
      return fails ? boom() : answer(200, { data: [entry("b", "older-person")], next: null });
    });
    show();

    await userEvent.click(await screen.findByRole("button", { name: "Older entries" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("Could not load older entries: boom.");
    expect(screen.getByText("alex")).toBeInTheDocument();
    fails = false;
    await userEvent.click(screen.getByRole("button", { name: /^Retry/ }));
    expect(await screen.findByText("older-person")).toBeInTheDocument();
    expect(screen.getByText("alex")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("keeps the rows when refetching them fails", async () => {
    let fails = false;
    stubAPI((url) => (isCount(url) ? total() : fails ? boom() : answer(200, { data: [entry("a", "alex")], next: null })));
    const client = show();
    await screen.findByText("alex");

    fails = true;
    await act(async () => {
      await client.refetchQueries({ queryKey: ["audit-log-page"] });
      await new Promise((r) => setTimeout(r, 20));
    });

    expect(screen.getByText("alex")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("reports a failed next page in the footer even with no rows above it", async () => {
    stubAPI((url) => {
      if (isCount(url)) return total();
      return url.searchParams.get("cursor") ? boom() : answer(200, { data: [], next: "c" });
    });
    show();

    await userEvent.click(await screen.findByRole("button", { name: "Older entries" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("Could not load older entries: boom.");
    expect(screen.getByText(/Nothing loaded yet/)).toBeInTheDocument();
  });

  it("gives the whole table to the error when the first page fails", async () => {
    stubAPI((url) => (isCount(url) ? total() : boom()));
    show();

    expect(await screen.findByRole("alert")).toHaveTextContent("Could not load the audit log: boom.");
    expect(screen.queryByRole("button", { name: "Older entries" })).not.toBeInTheDocument();
  });

  it("says the count is unavailable instead of counting forever", async () => {
    stubAPI((url) => (isCount(url) ? boom() : answer(200, { data: [entry("a", "alex")], next: null })));
    show();

    expect(await screen.findByText("Count unavailable")).toBeInTheDocument();
    expect(screen.queryByText("Counting…")).not.toBeInTheDocument();
  });
});

describe("Audit log rows", () => {
  it("names a key, and doesn't call a revoked one a key twice", async () => {
    stubAPI((url) =>
      isCount(url)
        ? total()
        : answer(200, {
            data: [
              entry("a", "alex", { api_key_id: "k-1", api_key_name: "release-ci" }),
              entry("b", "sam", { api_key_id: "k-2", api_key_name: null }),
            ],
            next: null,
          }),
    );
    show();

    expect(await screen.findByText("with key release-ci")).toBeInTheDocument();
    expect(screen.getByText("with a revoked key")).toBeInTheDocument();
  });
});

describe("Audit log loading", () => {
  it("announces the skeleton", () => {
    vi.stubGlobal("fetch", vi.fn<typeof globalThis.fetch>(() => new Promise(() => {})));
    show();

    expect(screen.getByRole("status", { name: "Loading the audit log" })).toBeInTheDocument();
  });
});
