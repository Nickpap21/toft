/**
 * Test helpers that read the theme stylesheets the way a browser would:
 * collect the declarations that apply to one theme, substitute var(), and
 * evaluate color-mix(in oklab, …). Enough CSS for tokens.css, nothing more.
 */
import { readFileSync } from "node:fs";
import { join } from "node:path";

export type RGBA = { r: number; g: number; b: number; a: number };

export function readStyle(name: string): string {
  return readFileSync(join(__dirname, "..", "styles", name), "utf8");
}

/** Declarations from every top-level rule whose selector list names
 *  [data-theme="<theme>"] or [data-theme], in source order. Rules inside
 *  @media are skipped; @layer wrappers are looked through. */
export function declarationsFor(css: string, theme: string): Map<string, string> {
  const stripped = css.replace(/\/\*[\s\S]*?\*\//g, "").replace(/@media[^{]*\{(?:[^{}]*\{[^{}]*\})*[^{}]*\}/g, "");
  const out = new Map<string, string>();
  for (const m of stripped.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    const selectors = m[1].replace(/@layer\s+\w+\s*/g, "").split(",").map((s) => s.trim());
    if (!selectors.some((s) => s === `[data-theme="${theme}"]` || s === "[data-theme]")) continue;
    for (const decl of m[2].split(";")) {
      const i = decl.indexOf(":");
      if (i < 0) continue;
      const name = decl.slice(0, i).trim();
      if (name.startsWith("--")) out.set(name, decl.slice(i + 1).trim());
    }
  }
  return out;
}

export function substitute(value: string, vars: Map<string, string>, depth = 0): string {
  if (depth > 20) throw new Error(`var() cycle in ${value}`);
  const next = value.replace(/var\((--[\w-]+)\)/g, (_, name: string) => {
    const v = vars.get(name);
    if (v === undefined) throw new Error(`undefined ${name}`);
    return v;
  });
  return next === value ? next : substitute(next, vars, depth + 1);
}

export function hexToRgba(hex: string): RGBA {
  const n = parseInt(hex.slice(1), 16);
  return { r: ((n >> 16) & 255) / 255, g: ((n >> 8) & 255) / 255, b: (n & 255) / 255, a: 1 };
}

const toLinear = (c: number) => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
const fromLinear = (c: number) => (c <= 0.0031308 ? 12.92 * c : 1.055 * c ** (1 / 2.4) - 0.055);

function toOklab({ r, g, b }: RGBA): [number, number, number] {
  const [lr, lg, lb] = [toLinear(r), toLinear(g), toLinear(b)];
  const l = Math.cbrt(0.4122214708 * lr + 0.5363325363 * lg + 0.0514459929 * lb);
  const m = Math.cbrt(0.2119034982 * lr + 0.6806995451 * lg + 0.1073969566 * lb);
  const s = Math.cbrt(0.0883024619 * lr + 0.2817188376 * lg + 0.6299787005 * lb);
  return [
    0.2104542553 * l + 0.793617785 * m - 0.0040720468 * s,
    1.9779984951 * l - 2.428592205 * m + 0.4505937099 * s,
    0.0259040371 * l + 0.7827717662 * m - 0.808675766 * s,
  ];
}

function fromOklab([L, A, B]: [number, number, number], a: number): RGBA {
  const l = (L + 0.3963377774 * A + 0.2158037573 * B) ** 3;
  const m = (L - 0.1055613458 * A - 0.0638541728 * B) ** 3;
  const s = (L - 0.0894841775 * A - 1.291485548 * B) ** 3;
  const clamp = (x: number) => Math.min(1, Math.max(0, fromLinear(x)));
  return {
    r: clamp(4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s),
    g: clamp(-1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s),
    b: clamp(-0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s),
    a,
  };
}

/** color-mix(in oklab, x p%, y [q%]), with premultiplied alpha as in CSS. */
function mix(x: RGBA, p: number, y: RGBA): RGBA {
  const q = 1 - p;
  const a = x.a * p + y.a * q;
  if (a === 0) return { r: 0, g: 0, b: 0, a: 0 };
  const ox = toOklab(x);
  const oy = toOklab(y);
  const lab = [0, 1, 2].map((i) => (ox[i] * x.a * p + oy[i] * y.a * q) / a) as [number, number, number];
  return fromOklab(lab, a);
}

function splitTopLevel(s: string): string[] {
  const parts: string[] = [];
  let depth = 0;
  let start = 0;
  for (let i = 0; i < s.length; i++) {
    if (s[i] === "(") depth++;
    else if (s[i] === ")") depth--;
    else if (s[i] === "," && depth === 0) {
      parts.push(s.slice(start, i).trim());
      start = i + 1;
    }
  }
  parts.push(s.slice(start).trim());
  return parts;
}

export function parseColor(value: string): RGBA {
  const v = value.trim();
  if (v === "transparent") return { r: 0, g: 0, b: 0, a: 0 };
  if (/^#[0-9a-fA-F]{6}$/.test(v)) return hexToRgba(v);
  const m = /^color-mix\(([\s\S]*)\)$/.exec(v);
  if (!m) throw new Error(`cannot parse colour ${v}`);
  const [space, first, second] = splitTopLevel(m[1]);
  if (space !== "in oklab") throw new Error(`unsupported space ${space}`);
  const pct = (part: string) => {
    const pm = /\s(\d+(?:\.\d+)?)%$/.exec(part);
    return pm ? { color: part.slice(0, pm.index).trim(), p: Number(pm[1]) / 100 } : { color: part, p: null };
  };
  const a = pct(first);
  const b = pct(second);
  const p = a.p ?? (b.p !== null ? 1 - b.p : 0.5);
  return mix(parseColor(a.color), p, parseColor(b.color));
}

/** Evaluates a custom property for one theme. */
export function resolveToken(vars: Map<string, string>, name: string): RGBA {
  const raw = vars.get(name);
  if (raw === undefined) throw new Error(`undefined ${name}`);
  return parseColor(substitute(raw, vars));
}

export function over(top: RGBA, bottom: RGBA): RGBA {
  const a = top.a + bottom.a * (1 - top.a);
  const ch = (t: number, b: number) => (t * top.a + b * bottom.a * (1 - top.a)) / a;
  return { r: ch(top.r, bottom.r), g: ch(top.g, bottom.g), b: ch(top.b, bottom.b), a };
}

export function luminance({ r, g, b }: RGBA): number {
  return 0.2126 * toLinear(r) + 0.7152 * toLinear(g) + 0.0722 * toLinear(b);
}

export function contrast(x: RGBA, y: RGBA): number {
  const [hi, lo] = [luminance(x), luminance(y)].sort((p, q) => q - p);
  return (hi + 0.05) / (lo + 0.05);
}
