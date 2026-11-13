import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import MyIssuesPage from "./page";

// H2, X1: a 500 on the page's only read isn't an empty list.

const EMPTY = "Nothing open is assigned to you. Issues assigned to you in any project show here.";

function serve(status: number, body: unknown) {
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(body), { status })));
}
afterEach(() => vi.unstubAllGlobals());

function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><MyIssuesPage /></QueryClientProvider>);
}

describe("My issues", () => {
  it("a failed read says so with Retry, not that nothing is assigned", async () => {
    serve(500, { error: "boom: pq internal detail", status: 500 });
    mount();
    expect(await screen.findByRole("heading", { name: "Couldn’t load this page", level: 2 })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
    expect(screen.queryByText(EMPTY)).toBeNull();
    expect(screen.queryByText(/pq internal/)).toBeNull();
  });

  it("Retry reads again, and the empty answer replaces the failure", async () => {
    serve(500, { error: "boom", status: 500 });
    mount();
    const retry = await screen.findByRole("button", { name: /^Retry/ });
    serve(200, { data: [] });
    fireEvent.click(retry);
    expect(await screen.findByText(EMPTY)).toBeInTheDocument();
  });

  it("shows the issues by status", async () => {
    serve(200, { data: [{ key: "SHOP-1", number: 1, title: "Fix it", status: "todo", priority: "high", project_id: "p1", project: "shop", updated_at: "2026-11-01T10:00:00Z" }] });
    mount();
    expect(await screen.findByText("Fix it")).toBeInTheDocument();
    expect(screen.getByText("1 open, assigned to you across 1 project")).toBeInTheDocument();
  });
});
