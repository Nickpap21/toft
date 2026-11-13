import { describe, expect, it, vi } from "vitest";
import { redirect } from "next/navigation";
import DeploymentsRedirect from "./page";

vi.mock("next/navigation", () => ({ redirect: vi.fn() }));

describe("the old deployments address", () => {
  const params = Promise.resolve({ id: "p1", serviceId: "s1" });

  it("keeps the deployment it was linked to", async () => {
    await DeploymentsRedirect({ params, searchParams: Promise.resolve({ deployment: "d1" }) });
    expect(redirect).toHaveBeenCalledWith("/projects/p1/services/s1?tab=deployments&deployment=d1");
  });

  it("lands on the list without one", async () => {
    await DeploymentsRedirect({ params, searchParams: Promise.resolve({}) });
    expect(redirect).toHaveBeenCalledWith("/projects/p1/services/s1?tab=deployments");
  });
});
