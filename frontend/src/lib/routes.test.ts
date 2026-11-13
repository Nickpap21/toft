import { describe, expect, it } from "vitest";
import { isBarePath, isPublicPath, safeHref, safeNext, signInHref } from "./routes";

describe("safeHref", () => {
  it("keeps http(s) URLs", () => {
    expect(safeHref("https://grafana.example.com/d/x?orgId=1")).toBe("https://grafana.example.com/d/x?orgId=1");
    expect(safeHref("http://loki:3100")).toBe("http://loki:3100");
  });
  it("drops anything that would run or is not a URL", () => {
    for (const u of ["javascript:alert(1)", "JavaScript:alert(1)", "data:text/html,hi", "vbscript:x", "//evil.example", "grafana.example.com", "", null, undefined]) {
      expect(safeHref(u)).toBeUndefined();
    }
  });
});

describe("isPublicPath", () => {
  it("knows the sign-in pages and invite links", () => {
    expect(isPublicPath("/")).toBe(true);
    expect(isPublicPath("/invite/abc")).toBe(true);
    expect(isPublicPath("/dashboard")).toBe(false);
    // The wizard's entry routes signed-out visitors to set-up or sign-in;
    // its steps need a session.
    expect(isPublicPath("/setup")).toBe(true);
    expect(isPublicPath("/setup/kit")).toBe(false);
  });

  it("frames the wizard's steps without the signed-in shell", () => {
    expect(isBarePath("/setup/kit")).toBe(true);
    expect(isBarePath("/register")).toBe(true);
    expect(isBarePath("/settings")).toBe(false);
  });
});

describe("safeNext", () => {
  it("follows a path on this origin, with its query and hash", () => {
    expect(safeNext("/projects/p1/services/s1?tab=logs#x")).toBe("/projects/p1/services/s1?tab=logs#x");
    expect(safeNext("/no-such-page")).toBe("/no-such-page");
  });
  it("goes Home for anything that could leave the origin", () => {
    for (const n of ["https://evil.example", "//evil.example", "/\\evil.example", "/\t/evil.example", "/.//evil.example", "/./\\evil.example", "/a/..//evil.example", "/%2e//evil.example", "javascript:alert(1)", "dashboard", "", null, undefined]) {
      expect(safeNext(n)).toBe("/dashboard");
    }
  });
});

describe("signInHref", () => {
  it("keeps the page someone was sent to", () => {
    expect(signInHref("/projects/p1?tab=logs")).toBe("/?next=%2Fprojects%2Fp1%3Ftab%3Dlogs");
  });
  it("needs no next for Home or a public page", () => {
    expect(signInHref("/dashboard")).toBe("/");
    expect(signInHref("/invite/abc")).toBe("/");
  });
});
