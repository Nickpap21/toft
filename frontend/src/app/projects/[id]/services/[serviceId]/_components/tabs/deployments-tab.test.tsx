import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Service } from "@/lib/api/service.service";
import { DeploymentsTab } from "./deployments-tab";

// A failed poll leaves the deploys on screen; a Retry keeps the row it's in.

vi.mock("next/navigation", () => ({ useRouter: () => ({ replace: vi.fn() }), useSearchParams: () => new URLSearchParams() }));
vi.mock("../rollback-dialog", () => ({ RollbackDialog: () => null }));
vi.mock("@/app/projects/_lib/redeploy", () => ({ useRedeploy: () => ({ redeploy: vi.fn(), dialog: null }) }));

let answer: () => Promise<unknown>;
vi.mock("@/lib/api/client", async (original) => ({
  ...(await original<typeof import("@/lib/api/client")>()),
  fetchAPI: () => answer(),
}));

const service = { id: "s1", name: "web", source_type: "docker-image", runtime_status: "running" } as Service;
const list = [{ id: "d1", status: "running", image_tag: "web:1", commit_sha: "", created_at: "2026-11-09T09:00:00Z", finished_at: "2026-11-09T09:01:00Z" }];

function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <DeploymentsTab service={service} projectId="p1" env={undefined} deploymentId="" canDeploy onDeploy={vi.fn()} deployBusy={false} />
    </QueryClientProvider>,
  );
  return client;
}

beforeEach(() => {
  answer = async () => list;
});

describe("the Deploys tab", () => {
  it("keeps the deploys when its poll fails", async () => {
    const client = show();
    await screen.findByRole("heading", { name: "Deploys" });
    answer = () => Promise.reject(new Error("boom"));
    await client.refetchQueries({ queryKey: ["deployments", "s1", "all"] });
    await waitFor(() => expect(client.getQueryState(["deployments", "s1", "all"])?.status).toBe("error"));
    expect(screen.getByRole("heading", { name: "Deploys" })).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("says a first read failed, and Retry keeps the row and its focus", async () => {
    answer = () => Promise.reject(new Error("boom"));
    show();
    expect(await screen.findByText("Couldn't load the deploys of web.")).toBeInTheDocument();
    const retry = screen.getByRole("button", { name: "Retry" });
    retry.focus();
    answer = () => new Promise(() => {});
    retry.click();
    await new Promise((r) => setTimeout(r, 0));
    expect(retry.isConnected).toBe(true);
    expect(retry).toHaveFocus();
  });
});
