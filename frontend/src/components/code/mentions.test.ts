import { describe, expect, it } from "vitest";
import { handles, typing } from "./mentions";

describe("mentions", () => {
  it("take the email's local part, unless two members share it", () => {
    const hs = handles([
      { user_email: "Ann@shop.test", user_name: "Ann" },
      { user_email: "jo@a.test", user_name: "Jo A" },
      { user_email: "jo@b.test", user_name: "Jo B" },
    ]);
    expect(hs).toEqual([{ handle: "ann", name: "Ann" }]);
  });

  it("find the @word before the caret, not an email", () => {
    expect(typing("cc @an", 6)).toEqual({ start: 3, prefix: "an" });
    expect(typing("@", 1)).toEqual({ start: 0, prefix: "" });
    expect(typing("mail ann@shop", 13)).toBeNull();
    expect(typing("@ann done", 9)).toBeNull();
  });
});
