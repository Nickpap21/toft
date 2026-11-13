import { describe, expect, it } from "vitest";
import { formatDateTime, formatDateTimeZone, initials } from "./utils";

describe("initials", () => {
  it("takes two words split on spaces, dots, underscores and hyphens, or the first two letters", () => {
    expect(initials("Local Admin")).toBe("LA");
    expect(initials("admin")).toBe("AD");
    expect(initials("dimitra.konstantinidou")).toBe("DK");
    expect(initials("nick2121@example.com")).toBe("NI");
    expect(initials("")).toBe("?");
  });
});

describe("formatDateTimeZone", () => {
  it("is formatDateTime with the viewer's time zone after it, kept on the time's line", () => {
    const at = "2026-11-09T12:00:00Z";
    expect(formatDateTimeZone(at)).toMatch(new RegExp(`^${formatDateTime(at)}\u00a0[A-Z][A-Z0-9+:-]+$`));
  });
});
