import { describe, expect, it } from "vitest";
import { passwordTooLong } from "./password";

describe("passwordTooLong", () => {
    it("counts UTF-8 bytes, not characters", () => {
        expect(passwordTooLong("a".repeat(72))).toBe(false);
        expect(passwordTooLong("a".repeat(73))).toBe(true);
        expect(passwordTooLong("é".repeat(36))).toBe(false);
        expect(passwordTooLong("é".repeat(37))).toBe(true);
    });
});
