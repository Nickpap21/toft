import { describe, expect, it, vi } from "vitest";
import { redirect } from "next/navigation";
import ObservabilityRedirect from "./page";

vi.mock("next/navigation", () => ({ redirect: vi.fn() }));

describe("/observability", () => {
  it("redirects to the Logs and metrics tab", () => {
    ObservabilityRedirect();
    expect(redirect).toHaveBeenCalledWith("/shared?tab=logs");
  });
});
