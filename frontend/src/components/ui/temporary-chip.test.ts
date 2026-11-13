import { describe, expect, it } from "vitest";
import { certificateRecorded, domainURL } from "./temporary-chip";

describe("domainURL", () => {
  it("opens a temporary name over HTTP until its certificate is issued", () => {
    const host = "web-production-shop.203-0-113-10.sslip.io";
    expect(domainURL({ hostname: host, temporary: true, certificate_at: null })).toBe(`http://${host}`);
    expect(domainURL({ hostname: host, temporary: true, certificate_at: "2026-11-03T10:00:00Z" })).toBe(`https://${host}`);
    expect(domainURL({ hostname: "shop.example.com", temporary: false, certificate_at: null })).toBe("https://shop.example.com");
  });
});

describe("certificateRecorded", () => {
  const check = { domains: [{ id: "d1", certificate: "valid" }, { id: "d2", certificate: "none" }] };
  it("is true when the check saw a valid certificate the list shows pending", () => {
    expect(certificateRecorded(check, [{ id: "d1", certificate_at: null }])).toBe(true);
  });
  it("is false when the list already has it, or the check saw none", () => {
    expect(certificateRecorded(check, [{ id: "d1", certificate_at: "2026-11-03T10:00:00Z" }, { id: "d2", certificate_at: null }])).toBe(false);
    expect(certificateRecorded(check, undefined)).toBe(false);
  });
});
