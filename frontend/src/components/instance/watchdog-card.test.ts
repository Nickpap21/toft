import { describe, expect, it } from "vitest";
import type { Watchdog } from "@/lib/api/instance.service";
import { watchdogState } from "./watchdog-card";

const w = (over: Partial<Watchdog>): Watchdog => ({ url: "https://hc-ping.com/x", last_ping_at: null, last_status: null, last_error: "", held: false, ...over });
const now = Date.parse("2026-10-18T12:05:00Z");

describe("watchdogState", () => {
  it("says off, held back, failing, waiting or pinging", () => {
    expect(watchdogState(w({ url: "" }), now).chip).toBe("Off");
    expect(watchdogState(w({ held: true }), now).chip).toBe("Held back");
    expect(watchdogState(w({ last_error: "the URL answered 404", last_ping_at: "2026-10-18T12:00:00Z" }), now)).toMatchObject({ chip: "Failing", tone: "failed" });
    expect(watchdogState(w({}), now).chip).toBe("Waiting");
    expect(watchdogState(w({ last_ping_at: "2026-10-18T12:00:00Z", last_status: 200 }), now)).toMatchObject({ chip: "Pinging", value: "5 min ago" });
  });
});
