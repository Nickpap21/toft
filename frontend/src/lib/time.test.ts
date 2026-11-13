import { describe, expect, it } from "vitest";
import { ago, sinceShort, untilHint } from "./time";
import { formatDateTime } from "./utils";

describe("sinceShort", () => {
  const now = new Date("2026-10-15T12:00:00").getTime();
  it("is relative within a day, then absolute", () => {
    expect(sinceShort(new Date(now - 20_000).toISOString(), now)).toBe("just now");
    expect(sinceShort(new Date(now - 18 * 60_000).toISOString(), now)).toBe("18 min ago");
    expect(sinceShort(new Date(now - 3 * 3_600_000).toISOString(), now)).toBe("3 h ago");
    const old = new Date(now - 30 * 3_600_000).toISOString();
    expect(sinceShort(old, now)).toBe(formatDateTime(old));
  });
});

describe("untilHint", () => {
  const now = new Date("2026-10-15T23:30:00").getTime();
  it("counts calendar days", () => {
    expect(untilHint(new Date("2026-10-15T23:50:00").toISOString(), now)).toBe("today");
    expect(untilHint(new Date("2026-10-16T00:10:00").toISOString(), now)).toBe("tomorrow");
    expect(untilHint(new Date("2026-10-22T09:00:00").toISOString(), now)).toBe("in 7 days");
  });
});

describe("ago", () => {
  const now = new Date("2026-10-15T12:00:00").getTime();
  it("says just now, never \"now ago\"", () => {
    expect(ago(new Date(now - 20_000).toISOString(), now)).toBe("just now");
    expect(ago(new Date(now - 4 * 60_000).toISOString(), now)).toBe("4m ago");
    expect(ago(new Date(now - 3 * 86_400_000).toISOString(), now)).toBe("3d ago");
  });
});
