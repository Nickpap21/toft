import { describe, expect, it } from "vitest";
import type { AlertChannel, RecentAlert } from "@/lib/api/alert.service";
import { alertState, alertSubject, channelStatus, span } from "./alerts-view";

const now = new Date("2026-10-15T14:10:00").getTime();
const at = (minutesAgo: number) => new Date(now - minutesAgo * 60_000).toISOString();
const channel = (over: Partial<AlertChannel>): AlertChannel => ({
  webhook_url: "https://hooks.slack.com/services/x",
  enabled: true,
  updated_at: "",
  last_delivery_at: null,
  last_delivery_status: null,
  last_delivery_error: "",
  ...over,
});
const alert = (over: Partial<RecentAlert>): RecentAlert => ({
  id: "a",
  kind: "service_down",
  subject: "svc",
  detail: "",
  fired_at: at(120),
  resolved_at: null,
  one_off: false,
  subject_name: "web",
  project_id: "p",
  project_name: "storefront",
  environment_name: "production",
  server_name: "fsn1",
  ...over,
});

describe("channelStatus", () => {
  it("says off, paused, on and how the last delivery went", () => {
    expect(channelStatus(null, now)).toMatchObject({ label: "Off", note: "No webhook: nothing is sent" });
    expect(channelStatus(channel({ enabled: false }), now).label).toBe("Paused");
    expect(channelStatus(channel({ last_delivery_at: at(8), last_delivery_status: 200 }), now).note).toBe(
      "Last delivery 14:02 · hooks.slack.com answered 200",
    );
    const failed = channelStatus(channel({ last_delivery_at: at(8), last_delivery_status: 404, last_delivery_error: "the receiver answered 404" }), now);
    expect(failed).toMatchObject({ failed: true, tone: "fail", note: "Last delivery 14:02 failed · hooks.slack.com answered 404" });
    expect(channelStatus(channel({ last_delivery_at: at(8), last_delivery_error: "dial tcp: i/o timeout" }), now).note).toBe(
      "Last delivery 14:02 failed · hooks.slack.com did not answer: dial tcp: i/o timeout",
    );
  });
});

describe("alertState", () => {
  it("is open for, resolved after, or one message", () => {
    expect(alertState(alert({}), now)).toMatchObject({ label: "Open", note: "for 2 h" });
    expect(alertState(alert({ fired_at: at(30), resolved_at: at(25) }), now)).toMatchObject({ label: "Resolved", note: "after 5 min" });
    expect(alertState(alert({ one_off: true, kind: "deploy_failed", resolved_at: at(120) }), now).label).toBe("One message");
  });
  it("spans minutes, hours and days", () => {
    expect(span(10_000)).toBe("1 min");
    expect(span(3 * 86_400_000)).toBe("3 d");
  });
});

describe("alertSubject", () => {
  it("names and links what an alert is on", () => {
    expect(alertSubject(alert({}))).toEqual({ name: "storefront / web", where: "fsn1", href: "/projects/p/services/svc" });
    expect(alertSubject(alert({ kind: "deploy_failed" })).href).toBe("/projects/p/services/svc?tab=deployments");
    expect(alertSubject(alert({ subject: "control-plane", kind: "disk_full" }))).toEqual({ name: "panel", where: "control-plane server" });
    expect(alertSubject(alert({ kind: "server_unreachable", subject: "srv", subject_name: "fsn1" }))).toEqual({
      name: "fsn1",
      where: "server",
      href: "/servers/srv",
    });
  });
});
