import { describe, expect, it } from "vitest";
import { nonHttpPortWarning } from "./health-check-test";

describe("nonHttpPortWarning", () => {
  it("warns on ports that do not speak HTTP, and only those", () => {
    expect(nonHttpPortWarning(3306)).toContain("MySQL");
    expect(nonHttpPortWarning(6379)).toContain("Redis");
    expect(nonHttpPortWarning(5432)).toContain("Postgres");
    expect(nonHttpPortWarning(80)).toBeNull();
    expect(nonHttpPortWarning(4000)).toBeNull();
  });
});
