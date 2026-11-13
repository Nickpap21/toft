import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { describe, expect, it } from "vitest";

const SRC = join(__dirname, "..");

function walk(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const p = join(dir, name);
    return statSync(p).isDirectory() ? walk(p) : [p];
  });
}

const sources = walk(SRC).filter((p) => /\.(ts|tsx)$/.test(p) && !/\.test\.tsx?$/.test(p));

/** Code written for R1 uses theme tokens only. Legacy page bodies keep
 *  slate, indigo and dark: until R2 rebuilds them. */
const TOKEN_ONLY = [
  /^components\/shell\//,
  /^components\/theme\//,
  /^components\/theme-provider\.tsx$/,
  /^components\/workspace-switcher\.tsx$/,
  /^components\/ui\/(button|dialog|dropdown-menu|input|textarea|status-badge|server-status-dot)\.tsx$/,
  /^app\/settings\/(layout|appearance\/page)\.tsx$/,
  /^lib\/status\.ts$/,
  // R2 projects (D-47).
  /^app\/projects\/(page\.tsx|_lib\/)/,
  /^app\/projects\/\[id\]\/(page|settings\/page|services\/new\/page)\.tsx$/,
  /^app\/shared\//,
];

describe("token-only files", () => {
  const files = sources.filter((p) => TOKEN_ONLY.some((re) => re.test(relative(SRC, p))));

  it("covers the primitives", () => {
    expect(files.length).toBeGreaterThanOrEqual(9);
  });

  it.each(files.map((p) => [relative(SRC, p), p]))("%s has no dark:, slate or indigo classes", (_, path) => {
    const text = readFileSync(path, "utf8");
    expect(text).not.toMatch(/\bdark:/);
    expect(text).not.toMatch(/\b(slate|indigo)-\d/);
  });
});

it("no file puts white text on an indigo fill", () => {
  // Mono's accent is near-white and Contrast's is light lavender: an accent
  // fill must use text-on-accent, never text-white.
  const offenders = sources.filter((p) =>
    readFileSync(p, "utf8")
      .split("\n")
      .some((line) => /bg-indigo-(500|600|700)\b/.test(line) && /\btext-white\b/.test(line)),
  );
  expect(offenders.map((p) => relative(SRC, p))).toEqual([]);
});
