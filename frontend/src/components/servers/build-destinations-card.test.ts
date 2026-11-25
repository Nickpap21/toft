import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/client";
import { destinationError, destinationText } from "./build-destinations-card";

describe("a build destination", () => {
  it("is written as the firewall takes it", () => {
    expect(destinationText({ address: "10.0.5.20", port: 5000 })).toBe("10.0.5.20:5000");
    expect(destinationText({ address: "10.0.6.0/24", port: null })).toBe("10.0.6.0/24");
  });

  it("says why a change was refused, at its field", () => {
    const refused = (status: number, message: string, data: object) => new ApiError(message, status, { error: message, data });
    expect(destinationError(refused(400, "only addresses inside 10.0.0.0/8", { code: "build_destination_invalid", field: "address" }), "x")).toEqual({
      field: "address",
      text: "Only addresses inside 10.0.0.0/8.",
    });
    expect(destinationError(refused(409, "10.0.0.5 holds one of this server's own addresses", { code: "build_destination_server_address" }), "x")).toEqual({
      field: "address",
      text: "10.0.0.5 holds one of this server's own addresses.",
    });
    expect(destinationError(refused(409, "a server's list holds 100 build destinations", { code: "build_destinations_full" }), "x").field).toBe("");
  });

  it("never shows a server's own words for a list it didn't take", () => {
    const err = new ApiError("the build destinations were not applied: ssh: connection refused", 502, {
      error: "…",
      data: { code: "build_destinations_not_applied" },
    });
    expect(destinationError(err, "x").text).toBe("The server didn’t take the list, so nothing was saved. Check that it is online, then try again.");
    expect(destinationError(new ApiError("pq: boom", 500, null), "The destination could not be added.").text).toBe("The destination could not be added.");
    expect(destinationError(new Error("offline"), "fallback").text).toBe("fallback");
  });
});
