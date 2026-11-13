import { describe, expect, it } from "vitest";
import type { Environment } from "@/lib/api/environment.service";
import { ALL, resolveEnv, withEnv } from "./env-context";

const env = (id: string): Environment => ({ id, project_id: "p", name: id, color: "gray", protected: false, created_at: "", updated_at: "" });
const two = [env("prod"), env("staging")];

describe("resolveEnv", () => {
  it("the URL wins, then the stored choice, then All", () => {
    expect(resolveEnv("staging", "prod", two)).toBe("staging");
    expect(resolveEnv(null, "prod", two)).toBe("prod");
    expect(resolveEnv(null, "", two)).toBe(ALL);
    expect(resolveEnv(ALL, "prod", two)).toBe(ALL);
  });
  it("an id the project doesn't have falls through", () => {
    expect(resolveEnv("gone", "prod", two)).toBe("prod");
    expect(resolveEnv("gone", "other-project", two)).toBe(ALL);
  });
  it("a project with one environment opens on it", () => {
    expect(resolveEnv(null, "", [env("prod")])).toBe("prod");
    expect(resolveEnv(ALL, "", [env("prod")])).toBe(ALL);
  });
  it("before the environments load it trusts what it has", () => {
    expect(resolveEnv("x", "", undefined)).toBe("x");
    expect(resolveEnv(null, "", undefined)).toBe(ALL);
  });
});

describe("withEnv", () => {
  it("adds the parameter unless All", () => {
    expect(withEnv("/projects/p", ALL)).toBe("/projects/p");
    expect(withEnv("/projects/p", "e1")).toBe("/projects/p?env=e1");
    expect(withEnv("/projects/p/services?q=web", "e1")).toBe("/projects/p/services?q=web&env=e1");
  });
});
