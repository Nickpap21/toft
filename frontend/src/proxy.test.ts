import { NextRequest } from "next/server";
import { describe, expect, it } from "vitest";
import { proxy } from "./proxy";

describe("proxy: the Content-Security-Policy", () => {
  it("is enforced, with a fresh nonce on every request", () => {
    const one = proxy(new NextRequest("http://localhost:3000/"));
    const two = proxy(new NextRequest("http://localhost:3000/"));
    const csp = one.headers.get("Content-Security-Policy") ?? "";
    expect(one.headers.get("Content-Security-Policy-Report-Only")).toBeNull();
    expect(csp).toMatch(/script-src 'self' 'nonce-[A-Za-z0-9+/=]+' 'strict-dynamic'/);
    expect(csp).toContain("frame-ancestors 'none'");
    expect(csp).toContain("object-src 'none'");
    expect(two.headers.get("Content-Security-Policy")).not.toBe(csp);
  });

  it("is sent on signed-in pages too", () => {
    const req = new NextRequest("http://localhost:3000/dashboard", { headers: { cookie: "paas_auth=1" } });
    expect(proxy(req).headers.get("Content-Security-Policy")).toContain("default-src 'self'");
  });
});
