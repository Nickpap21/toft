import { describe, expect, it } from "vitest";
import { statusLabel, statusTone } from "./status";

describe("statusTone", () => {
  it.each([
    // runtime
    ["idle", "neutral"],
    ["deploying", "building"],
    ["running", "running"],
    ["stopped", "pending"],
    ["error", "failed"],
    // deployment
    ["pending", "pending"],
    ["queued", "pending"],
    ["building", "building"],
    ["failed", "failed"],
    ["cancelled", "neutral"],
    // server and domain
    ["active", "running"],
    ["verified", "running"],
  ])("%s → %s", (status, tone) => {
    expect(statusTone(status)).toBe(tone);
  });

  it("treats unknown and missing statuses as neutral", () => {
    for (const s of ["interrupted", "Deployed", "", null, undefined]) expect(statusTone(s)).toBe("neutral");
  });
});

describe("statusLabel", () => {
  it("labels known statuses and passes others through", () => {
    expect(statusLabel("error")).toBe("Error");
    expect(statusLabel("Deployed")).toBe("Deployed");
    expect(statusLabel(undefined)).toBe("Unknown");
  });
});
