import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Service } from "@/lib/api/service.service";
import { DeploymentDetail } from "./deployment-detail";

// S3: a live log whose stream failed says so and offers Retry; it doesn't go
// on "following live". The deployment and its stored log, when their reads
// fail, say so too: not a skeleton for ever, not "no such deployment", not
// "No lines."

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
vi.mock("../rollback-dialog", () => ({ RollbackDialog: () => null }));
vi.mock("@/components/issues/deploy-work", () => ({ DeployWork: () => null }));
vi.mock("@/components/inbox/needs-you", () => ({ OpenFailureIssue: () => null }));

let status: (path: string) => number = () => 200;
let list: unknown[] = [];
const streamEvents = vi.fn();
vi.mock("@/lib/api/client", async (importOriginal) => {
  const orig = await importOriginal<typeof import("@/lib/api/client")>();
  return {
    ...orig,
    fetchAPI: async (path: string) => {
      const code = status(path);
      if (code !== 200) throw new orig.ApiError("boom", code, { error: "boom" });
      return path.includes("/deployments?") ? list : [];
    },
    streamEvents: (...args: unknown[]) => streamEvents(...args),
  };
});

const service = { id: "s1", name: "web", source_type: "docker-image" } as Service;
const building = { id: "d1", status: "building", image_tag: "web:1", error_msg: "", created_at: "2026-11-09T09:00:00Z", started_at: "2026-11-09T09:00:01Z" };
const finished = { ...building, status: "running", finished_at: "2026-11-09T09:02:00Z" };

function renderPage() {
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <DeploymentDetail service={service} projectId="p1" deploymentId="d1" canDeploy={false} onDeploy={vi.fn()} deployBusy={false} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  status = () => 200;
  list = [building];
  streamEvents.mockReset();
  streamEvents.mockImplementation(() => new Promise(() => {}));
});

describe("A deployment's page when a read fails", () => {
  it("S3: a refused live log says it failed, with Retry, instead of following live", async () => {
    streamEvents.mockRejectedValue(new Error("refused"));
    renderPage();
    expect(await screen.findByText("Couldn’t load the live log.")).toBeTruthy();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeTruthy();
    expect(screen.queryByText("following live")).toBeNull();
  });

  it("S3: Retry opens the stream again and follows it", async () => {
    streamEvents.mockRejectedValueOnce(new Error("refused"));
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: /^Retry/ }));
    expect(await screen.findByText("following live")).toBeTruthy();
    expect(streamEvents).toHaveBeenCalledTimes(2);
    expect(screen.queryByText("Couldn’t load the live log.")).toBeNull();
  });

  it("the deployments read failing is a failure, not a skeleton for ever", async () => {
    status = (p) => (p.includes("/deployments?") ? 500 : 200);
    renderPage();
    expect(await screen.findByRole("heading", { name: "Couldn’t load this page", level: 2 })).toBeTruthy();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeTruthy();
    expect(screen.queryByRole("status")).toBeNull();
    expect(screen.queryByText(/has no such deployment/)).toBeNull();
  });

  it("a deployment the list hasn't got failing with a 500 is not no such deployment", async () => {
    list = [];
    status = (p) => (p.endsWith("/deployments/d1") ? 500 : 200);
    renderPage();
    expect(await screen.findByRole("heading", { name: "Couldn’t load this page", level: 2 })).toBeTruthy();
    expect(screen.queryByText(/has no such deployment/)).toBeNull();
  });

  it("a 404 is the answer: no such deployment, with no Retry", async () => {
    list = [];
    status = (p) => (p.endsWith("/deployments/d1") ? 404 : 200);
    renderPage();
    expect(await screen.findByText(/web has no such deployment/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /^Retry/ })).toBeNull();
  });

  it("a stored log that failed says so, with no No lines or 0 lines", async () => {
    list = [finished];
    status = (p) => (p.endsWith("/logs") ? 500 : 200);
    renderPage();
    expect(await screen.findByText("Couldn’t load the log.")).toBeTruthy();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeTruthy();
    expect(screen.queryByText("No lines.")).toBeNull();
    expect(screen.queryByText("0 lines")).toBeNull();
  });

  it("a failed deployment whose log failed claims no step was not reached", async () => {
    list = [{ ...finished, status: "failed", error_msg: "exit 1" }];
    status = (p) => (p.endsWith("/logs") ? 500 : 200);
    renderPage();
    await screen.findByText("Couldn’t load the log.");
    expect(screen.queryAllByText("not reached")).toHaveLength(0);
  });
});
