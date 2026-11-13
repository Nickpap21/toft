import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Service } from "@/lib/api/service.service";
import { ConfigFiles } from "./config-files";

// A failed refetch keeps the editor, and the draft in it, on screen.

let answer: () => Promise<unknown>;
vi.mock("@/lib/api/configfile.service", () => ({ configFileService: { list: () => answer() } }));

const file = { id: "f1", path: "/etc/app.conf", content: "a=1", updated_at: "2026-11-09T09:00:00Z" };

function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 0 } } });
  render(
    <QueryClientProvider client={client}>
      <ConfigFiles service={{ id: "s1", name: "web" } as Service} canDeploy onDeploy={vi.fn()} adding={false} onAdding={vi.fn()} />
    </QueryClientProvider>,
  );
  return client;
}

beforeEach(() => {
  answer = async () => [file];
});

describe("Config files", () => {
  it("keeps the editor and the draft when a refetch fails", async () => {
    const client = show();
    const editor = (await screen.findByRole("textbox")) as HTMLTextAreaElement;
    fireEvent.change(editor, { target: { value: "a=2" } });
    answer = () => Promise.reject(new Error("boom"));
    await client.refetchQueries({ queryKey: ["config-files", "s1"] });
    await waitFor(() => expect(client.getQueryState(["config-files", "s1"])?.status).toBe("error"));
    expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe("a=2");
    expect(screen.queryByText("Could not load the config files.")).toBeNull();
  });
});
