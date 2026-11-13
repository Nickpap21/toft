import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { DiskCard } from "./disk-card";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const GB = 1e9;
const values = { build_cache_limit_bytes: 16 * GB, auto_clean: [], alert_percent: 85, alert_resolve_percent: 80 };

vi.mock("@/lib/api/server.service", () => ({
  serverService: {},
  diskService: {
    // The root-reserved 3.3 GB are in neither used nor free.
    get: async () => ({
      read_at: "2026-10-31T14:00:00Z",
      size_bytes: 80.3 * GB,
      used_bytes: 19.5 * GB,
      free_bytes: 57.5 * GB,
      classes: [],
      build_cache: { bytes: 0, reclaimable_bytes: 0, over_limit_bytes: 0, limit_bytes: 16 * GB },
      volumes: [],
      volumes_in_use: { count: 0, bytes: 0 },
      containers_bytes: 0,
      other_bytes: 0,
      rollback_kept: [],
      other_workspaces: { services: 0, rollback_images: 0, rollback_bytes: 0 },
      policy: { build_cache_limit_bytes: null, auto_clean: null, alert_percent: null, effective: values, defaults: values },
      clean: null,
      last_clean: null,
    }),
  },
}));

describe("DiskCard", () => {
  it("shows the API's free, not size minus used", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <DiskCard serverId={null} canWrite={false} />
      </QueryClientProvider>,
    );
    expect(await screen.findByText(/57\.5 GB free/)).toBeInTheDocument();
    expect(screen.queryByText(/60\.8 GB free/)).toBeNull();
  });
});
