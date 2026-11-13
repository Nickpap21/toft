import { act, render } from "@testing-library/react";
import { useMutation, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";

vi.mock("@/components/theme-provider", () => ({ useTheme: () => ({ mode: "dark" }) }));
vi.mock("@/lib/api/client", () => ({}));

import { Providers } from "./providers";

// The count badges (Needs you, My issues, a project's issues and docs) read
// ["inbox"], ["project"] and ["projects"]: any mutation refreshes them.
describe("Providers", () => {
  it("refreshes the count sources after any successful mutation", async () => {
    let client!: QueryClient;
    let run!: () => Promise<unknown>;
    function Probe() {
      client = useQueryClient();
      const m = useMutation({ mutationFn: async () => "ok" });
      run = () => m.mutateAsync();
      return null;
    }
    render(<Providers><Probe /></Providers>);
    const spy = vi.spyOn(client, "invalidateQueries");

    await act(() => run());

    const keys = spy.mock.calls.map(([f]) => JSON.stringify(f?.queryKey));
    for (const key of [["inbox"], ["project"], ["projects"], ["project-services"], ["workspace-services"], ["project-overview"]]) {
      expect(keys).toContain(JSON.stringify(key));
    }
  });
});
