import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Service } from "@/lib/api/service.service";
import { Domains } from "./domains";

// Settings › Domains' Add only · Add and deploy (D-145, 7g step 8): Add and
// deploy hands the deploy to the page's, which confirms a protected one.

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
const posts: string[] = [];
let listFails = false;
vi.mock("@/lib/api/client", async (importOriginal) => {
  const orig = await importOriginal<typeof import("@/lib/api/client")>();
  return {
    ...orig,
    fetchAPI: async (path: string, init?: RequestInit) => {
      if (init?.method === "POST") {
        posts.push(path);
        return { id: "dom-1", hostname: "shop.example" };
      }
      if (path.endsWith("/domains/check")) return { server: { address: "203.0.113.10" }, domains: [], routing_known: false, routed_removed: [] };
      if (listFails) throw new orig.ApiError("boom", 500, { error: "boom" });
      return [];
    },
  };
});

const service = { id: "s1", name: "api", port: 8080, source_type: "git", runtime_status: "running" } as unknown as Service;

function open(onDeploy: () => void) {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <Domains service={service} serviceId="s1" canDeploy deployLabel="Deploy main" deployBusy={false} onDeploy={onDeploy} deployConfirms adding onAdding={vi.fn()}>
        {null}
      </Domains>
    </QueryClientProvider>,
  );
  fireEvent.change(screen.getByRole("textbox", { name: "Domain" }), { target: { value: "shop.example" } });
}

beforeEach(() => {
  posts.length = 0;
  listFails = false;
});

describe("the domains list when its read fails", () => {
  it("says so, with Retry, and no No domains yet", async () => {
    listFails = true;
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <Domains service={service} serviceId="s1" canDeploy deployLabel="Deploy main" deployBusy={false} onDeploy={vi.fn()} deployConfirms adding={false} onAdding={vi.fn()}>
          {null}
        </Domains>
      </QueryClientProvider>,
    );
    expect(await screen.findByText("Couldn’t load the domains of api.")).toBeTruthy();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeTruthy();
    expect(screen.queryByText("No domains yet.")).toBeNull();
  });
});

describe("Add a domain", () => {
  it("Add and deploy adds it, then deploys through the page's deploy", async () => {
    const onDeploy = vi.fn();
    open(onDeploy);
    fireEvent.click(screen.getByRole("button", { name: "Add and deploy api…" }));
    await waitFor(() => expect(onDeploy).toHaveBeenCalledTimes(1));
    expect(posts).toEqual(["/api/v1/services/s1/domains"]);
  });

  it("Add only adds it and deploys nothing", async () => {
    const onDeploy = vi.fn();
    open(onDeploy);
    fireEvent.click(screen.getByRole("button", { name: "Add only" }));
    await waitFor(() => expect(posts).toEqual(["/api/v1/services/s1/domains"]));
    expect(onDeploy).not.toHaveBeenCalled();
  });
});
