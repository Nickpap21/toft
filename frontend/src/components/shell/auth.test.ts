import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/client";
import { inviteCode, signInError, waitFor } from "./auth";

const apiError = (status: number, data?: unknown) => new ApiError("server says no", status, { error: "server says no", data });

describe("signInError", () => {
  it("names each refusal the way the board does", () => {
    expect(signInError(apiError(401)).text).toMatch(/don’t match an account/);
    expect(signInError(apiError(403))).toMatchObject({ kind: "banned" });
    expect(signInError(apiError(429, { retry_after: 90 })).text).toBe("Too many sign-in attempts. Try again in 90 seconds.");
    expect(signInError(new TypeError("Failed to fetch")).text).toMatch(/Couldn’t reach Toft/);
  });
});

describe("waitFor", () => {
  it("reads retry_after, and falls back when there is none", () => {
    expect(waitFor(apiError(429, { retry_after: 1 }))).toBe("1 second");
    expect(waitFor(apiError(429, { retry_after: 600 }))).toBe("10 minutes");
    expect(waitFor(apiError(429))).toBe("a few seconds");
  });
});

describe("inviteCode", () => {
  it("reads only invite codes", () => {
    expect(inviteCode(apiError(400, { code: "invite_expired" }))).toBe("invite_expired");
    expect(inviteCode(apiError(409, { code: "server_in_use" }))).toBe("");
    expect(inviteCode(new Error("x"))).toBe("");
  });
});
