import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import InstanceServerPage from "./page";

const GB = 1e9;
const server = {
  cpus: 4,
  memory_total_bytes: 8 * 2 ** 30,
  disk_total_bytes: 80.3 * GB,
  now: {
    cpu_percent: 1,
    load_1: 0,
    load_5: 0,
    load_15: 0,
    memory_used_bytes: 2 ** 30,
    memory_cache_bytes: 0,
    swap_total_bytes: 0,
    swap_used_bytes: 0,
    disk_used_bytes: 19.5 * GB,
    disk_free_bytes: 57.5 * GB,
  },
  samples: [],
};

vi.mock("next/navigation", () => ({ useRouter: () => ({ replace: vi.fn() }) }));
vi.mock("@/context/AuthContext", () => ({ useIsOperator: () => true }));
vi.mock("@/components/instance/instance-frame", () => ({
  InstanceFrame: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}));
vi.mock("@/components/servers/isolation-card", () => ({ IsolationCard: () => null }));
vi.mock("@/components/instance/watchdog-card", () => ({ WatchdogCard: () => null }));
vi.mock("@/lib/api/code.service", () => ({ gitInfoQuery: { queryKey: ["git"], queryFn: async () => null } }));
vi.mock("@/lib/api/server.service", () => ({
  backupPath: () => "",
  backupService: { get: async () => null },
  diskService: { get: async () => null },
}));
let failing: string | null = null;
vi.mock("@/lib/api/instance.service", () => {
  const none = (key: string) => ({
    queryKey: [key],
    queryFn: async () => {
      if (failing === key) throw new Error("boom");
      return null;
    },
  });
  return {
    panelHostQuery: none("host"),
    panelStatusQuery: none("status"),
    setupQuery: none("setup"),
    upgradeQuery: none("upgrade"),
    watchdogQuery: none("watchdog"),
    panelServerQuery: (range: string) => ({ queryKey: ["server", range], queryFn: async () => server }),
  };
});

describe("Instance → Server", () => {
  const show = () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <InstanceServerPage />
      </QueryClientProvider>,
    );
  };
  afterEach(() => {
    failing = null;
  });

  it.each([
    ["host", "Couldn’t load what the panel keeps."],
    ["setup", "Couldn’t load what the panel keeps."],
    ["host", "Couldn’t load the networks."],
    ["setup", "Couldn’t load the panel’s isolation."],
  ])("when %s fails, says %s with Retry", async (key, text) => {
    failing = key;
    show();
    const row = await screen.findByText(text);
    expect(row).toBeInTheDocument();
    expect(row.parentElement?.querySelector("button")).toHaveTextContent("Retry");
  });

  it("shows the disk in GB, with the API's free", async () => {
    show();
    const free = await screen.findByText("Free");
    await waitFor(() => expect(free.nextElementSibling).not.toHaveTextContent("—"));
    expect(free.nextElementSibling).toHaveTextContent("57.5 GB");
    expect(screen.getByText("19.5 GB")).toBeInTheDocument();
    expect(screen.getByText("of 80.3 GB")).toBeInTheDocument();
  });
});
