import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { expect, it } from "vitest";

// DESIGN.md → Typography and Space, shape, density: font sizes, radii and
// shadows come from the scales, the same ones the boards use.
const src = join(__dirname, "..", "..");

function files(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const path = join(dir, e.name);
    if (e.isDirectory()) return files(path);
    return /\.tsx?$/.test(e.name) && !/\.test\.tsx?$/.test(e.name) ? [path] : [];
  });
}

function offScale(pattern: RegExp, allowed: (cls: string, file: string) => boolean): string[] {
  return files(src).flatMap((f) => {
    const rel = f.slice(src.length + 1);
    return readFileSync(f, "utf8")
      .split("\n")
      .flatMap((line, i) => [...line.matchAll(pattern)].filter((m) => !allowed(m[1], rel)).map((m) => `${rel}:${i + 1} ${m[1]}`));
  });
}

// text-xs is 12 and text-sm 14; the rest are spelled in px.
const UI_SIZES = new Set(["10", "11", "13", "15", "17", "20", "22", "26"]);
// A document's title (docs reader, editor, history) is on the document scale.
const DOC_TITLE = "28";

it("uses only the type scale", () => {
  const found = offScale(/(?<![\w-])(text-(?:\[[\d.]+px\]|base|lg|xl|[2-9]xl))(?![\w-])/g, (cls, file) => {
    if (cls === "text-xl") return true;
    const px = /\[([\d.]+)px\]/.exec(cls)?.[1];
    return px !== undefined && (UI_SIZES.has(px) || (px === DOC_TITLE && file.startsWith("components/docs/")));
  });
  expect(found).toEqual([]);
});

it("uses only the radius scale", () => {
  // 2 xs, 4 sm (or bare rounded), 6 md, 8 lg, 10 cards, 12 xl, full.
  const found = offScale(/(?<![\w-])(rounded(?:-[trblse]{1,2})?-(?:\[\d+px\]|[2-9]xl))(?![\w-])/g, (cls) => cls.endsWith("-[10px]"));
  expect(found).toEqual([]);
});

it("uses only the elevation tokens", () => {
  // An inset bar (shadow-[inset_…]) marks a selected row; it is not elevation.
  const found = offScale(/(?<![\w-])(shadow-(?:xs|sm|md|lg|xl|2xl|\[(?!inset)[^\]]+\]))(?![\w-])/g, () => false);
  expect(found).toEqual([]);
});
