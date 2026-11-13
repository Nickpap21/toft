import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { ServerDrawer } from "./server-drawer";

vi.mock("@/context/AuthContext", () => ({ useIsOperator: () => false }));
vi.mock("@/components/reauth", () => ({ NEEDS_PASSWORD: "", useReauth: () => async () => false }));
vi.mock("@/components/ui/pty-terminal", () => ({ PtyTerminal: () => null }));
let statsFail = false;
vi.mock("@/lib/api/server.service", () => ({
  serverService: {
    getSystemStats: async () => {
      if (statsFail) throw new Error("boom");
      return {
        cpu_percent: 10,
        mem_used_mb: 4096,
        mem_total_mb: 8192,
        disk_used_mb: 4096,
        disk_total_mb: 8192,
        load_1: 0,
        load_5: 0,
        load_15: 0,
        uptime: "",
      };
    },
  },
}));

beforeEach(() => {
  statsFail = false;
});

const open = () =>
  render(
    <QueryClientProvider client={new QueryClient()}>
      <ServerDrawer serverId="s" serverName="web" open onClose={() => {}} />
    </QueryClientProvider>,
  );

// The same size reads differently in each bar, so each names its own unit.
it("writes memory in GiB and disk in GB", async () => {
  open();
  expect(await screen.findByText(/4\.0 GiB \/ 8\.0 GiB/)).toBeTruthy();
  expect(screen.getByText(/4\.3 GB \/ 8\.6 GB/)).toBeTruthy();
});

it("says the stats failed to load, with a Retry that brings them", async () => {
  statsFail = true;
  open();
  expect(await screen.findByText("Couldn’t load this server’s stats.")).toBeTruthy();

  statsFail = false;
  fireEvent.click(screen.getByRole("button", { name: /^Retry/ }));
  expect(await screen.findByText(/4\.0 GiB \/ 8\.0 GiB/)).toBeTruthy();
});
