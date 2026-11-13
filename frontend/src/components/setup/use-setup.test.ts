import { describe, expect, it } from "vitest";
import type { Setup } from "@/lib/api/instance.service";
import { finishedSteps, firstOpenStep, setupEntry } from "./use-setup";

const setup = (over: Partial<Setup> = {}): Setup => ({
  in_progress: true,
  domain: "",
  domain_set: false,
  awaiting_domain: false,
  role: "apps",
  role_set: false,
  kit_acknowledged_at: null,
  kit_out_of_date: false,
  finished_at: null,
  app: "",
  ...over,
});

describe("setupEntry", () => {
  it("shows the account step on an empty instance, signed in or not", () => {
    expect(setupEntry({ firstRun: true, signedIn: false, operator: false, setup: undefined })).toBe("account");
    expect(setupEntry({ firstRun: true, signedIn: undefined, operator: false, setup: undefined })).toBe("account");
  });
  it("waits while the status or the session is loading", () => {
    expect(setupEntry({ firstRun: undefined, signedIn: false, operator: false, setup: undefined })).toBeNull();
    expect(setupEntry({ firstRun: false, signedIn: undefined, operator: false, setup: undefined })).toBeNull();
  });
  it("sends anyone signed out to sign in, and back here after", () => {
    expect(setupEntry({ firstRun: false, signedIn: false, operator: false, setup: undefined })).toBe("/?next=/setup");
  });
  it("sends a signed-in non-operator Home", () => {
    expect(setupEntry({ firstRun: false, signedIn: true, operator: false, setup: undefined })).toBe("/dashboard");
  });
  it("sends the operator to the first open step, and Home once finished", () => {
    expect(setupEntry({ firstRun: false, signedIn: true, operator: true, setup: undefined })).toBeNull();
    expect(setupEntry({ firstRun: false, signedIn: true, operator: true, setup: setup() })).toBe("/setup/address");
    expect(setupEntry({ firstRun: false, signedIn: true, operator: true, setup: setup({ domain_set: true }) })).toBe("/setup/role");
    expect(setupEntry({ firstRun: false, signedIn: true, operator: true, setup: setup({ in_progress: false }) })).toBe("/dashboard");
  });
});

describe("firstOpenStep", () => {
  it("ends at the kit, whose Finish ends setup, once every step is done", () => {
    // An install part-way through the old wizard (backups, servers, invite) lands here too.
    const all = setup({ domain_set: true, role_set: true, kit_acknowledged_at: "2026-11-03T10:00:00Z" });
    expect(firstOpenStep(finishedSteps(all))).toBe("/setup/kit");
    expect(firstOpenStep(finishedSteps(setup({ domain_set: true, role_set: true })))).toBe("/setup/kit");
  });
});
