import { describe, expect, it } from "vitest";
import type { WorkspaceDomain } from "@/lib/api/domain.service";
import { needOf, recordOf, type Fact, type Server } from "./domains-view";

const now = Date.parse("2026-10-14T12:00:00Z");
const d = (hostname: string, extra: Partial<WorkspaceDomain> = {}): WorkspaceDomain => ({
  id: "d", hostname, created_at: "2026-10-14T11:40:00Z", created_by: "sam", routed_at: null, certificate_at: null, temporary: false, service_id: "s",
  service_name: "web", scope: "environment", project_id: "p", project_name: "marketing-site", environment_name: "production", server_id: "srv",
  ...extra,
});
const f = (extra: Partial<Fact>): Fact => ({ id: "d", hostname: "x", dns: "here", addresses: ["203.0.113.11"], certificate: "valid", not_after: "2026-12-30T00:00:00Z", routed: true, ...extra });
const srv: Server = { name: "fsn1-prod-02", address: "203.0.113.11", wildcards_ok: false, owner: true };

describe("needOf", () => {
  it("tells a temporary name that points elsewhere to make way for an own domain", () => {
    const n = needOf(d("web-production-shop.198-51-100-7.sslip.io", { temporary: true }), f({ dns: "elsewhere", addresses: ["198.51.100.7"] }), srv, true, "fsn1-prod-02", now);
    expect(n?.todo).toContain("Remove it and add a domain of your own");
    expect(n?.todo).not.toContain("DNS host");
  });
  it("needs nothing from a domain that points here, is routed and renews", () => {
    expect(needOf(d("founders.example"), f({}), srv, true, "fsn1-prod-02", now)).toBeNull();
  });

  it("says where DNS points instead, and what it costs", () => {
    const n = needOf(d("www.founders.example"), f({ dns: "elsewhere", addresses: ["198.51.100.40"], not_after: "2026-10-26T00:00:00Z" }), srv, true, "fsn1-prod-02", now)!;
    expect(n.kind).toBe("DNS points elsewhere");
    expect(n.meta).toBe("marketing-site / web · production · fsn1-prod-02 · the record answers 198.51.100.40");
    expect(n.todo).toContain("Set the A record of www.founders.example to 203.0.113.11");
    expect(n.todo).toContain("expires on 2026-10-26");
  });

  it("waits for DNS and a deploy on a new domain of a running service", () => {
    const n = needOf(d("api.storefront.dev"), f({ dns: "none", addresses: [], certificate: "none", routed: false }), srv, true, "fsn1-prod-02", now)!;
    expect(n).toMatchObject({ kind: "Waiting for DNS and a deploy", primary: "deploy", secondary: "check" });
    expect(n.meta).toContain("added by sam");
  });

  it("waits for a deploy once DNS points here", () => {
    expect(needOf(d("a.example"), f({ certificate: "none", routed: false }), srv, true, "s", now)?.kind).toBe("Waiting for a deploy");
    // A stopped service isn't routed, and that's not this page's to fix.
    expect(needOf(d("a.example"), f({ certificate: "none", routed: false }), srv, false, "s", now)).toBeNull();
  });

  it("flags certificates that fail or aren't renewing", () => {
    expect(needOf(d("a.example"), f({ certificate: "invalid" }), srv, true, "s", now)?.kind).toBe("Certificate doesn’t verify");
    expect(needOf(d("a.example"), f({ not_after: "2026-10-26T00:00:00Z" }), srv, true, "s", now)?.kind).toBe("Certificate isn’t renewing");
  });

  it("refuses a wildcard on a server with no DNS provider first", () => {
    expect(needOf(d("*.founders.example"), f({}), srv, true, "fsn1-prod-02", now)).toMatchObject({ kind: "No DNS provider", primary: "dns" });
    expect(needOf(d("*.founders.example"), f({}), { ...srv, wildcards_ok: true }, true, "fsn1-prod-02", now)).toBeNull();
  });

  it("writes the record to create", () => {
    expect(recordOf("api.storefront.dev", "203.0.113.10")).toBe("api.storefront.dev  A  203.0.113.10");
  });
});
