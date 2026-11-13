import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { expect, it } from "vitest";

// legacy-bridge.css mapped Tailwind's slate and indigo onto the themes until
// R2; it is gone (D-50). Pages take their colours from the theme tokens, so a
// slate or indigo class would no longer follow the theme.
const src = join(__dirname, "..", "..");

function files(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const path = join(dir, e.name);
    if (e.isDirectory()) return files(path);
    return /\.(tsx?|css)$/.test(e.name) && path !== __filename ? [path] : [];
  });
}

it("uses no slate or indigo colour classes", () => {
  const found = files(src).flatMap((f) =>
    readFileSync(f, "utf8")
      .split("\n")
      .flatMap((line, i) => (/(slate|indigo)-\d/.test(line) ? [`${f.slice(src.length + 1)}:${i + 1}`] : [])),
  );
  expect(found).toEqual([]);
});

// A class naming a theme colour that globals.css doesn't map draws nothing:
// Bug's red tile went blank when --env-red left the environment colours (D-142).
it("maps every status, environment and issue-type colour class to a token", () => {
  const theme = readFileSync(join(src, "app", "globals.css"), "utf8");
  const missing = files(src).flatMap((f) =>
    [...readFileSync(f, "utf8").matchAll(/\b(?:bg|text|border|ring|fill|stroke|outline|from|to|divide)-((?:status|env|type)-[a-z]+(?:-[a-z]+)*)/g)]
      .map((m) => m[1])
      .filter((name) => !theme.includes(`--color-${name}:`))
      .map((name) => `${f.slice(src.length + 1)}: ${name}`),
  );
  expect([...new Set(missing)]).toEqual([]);
});
