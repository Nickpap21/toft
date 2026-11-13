#!/usr/bin/env node
// Fails when UI code uses a raw colour instead of a design token.
// frontend-kit template. Config: scripts/token-check.json (next to this file).
//
//   npm run lint:tokens            # all files under config.include
//   npm run lint:tokens -- --changed   # only files changed vs git HEAD (staged, unstaged, untracked)
//
// Flags: hex colours (#fff, #1a2b3c80), rgb()/rgba()/hsl()/hsla()/oklch()/oklab()/lab()/lch()
// literals, and (forbidTailwindPalette) Tailwind's built-in palette classes such as
// bg-red-500 or text-white, which bypass the theme. Files in config.allow are the token
// sources and legitimate exceptions. Put `token-check-ignore` in a comment on a line to
// allow that one line, and say why next to it.
import { execSync } from "node:child_process";
import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { dirname, extname, join, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const app = resolve(here, "..");
const cfgPath = join(here, "token-check.json");
const cfg = {
  include: ["src"],
  extensions: [".ts", ".tsx", ".js", ".jsx", ".css", ".scss", ".vue", ".svelte", ".astro", ".html"],
  allow: ["src/styles/tokens.css"],
  forbidTailwindPalette: true,
  ...(existsSync(cfgPath) ? JSON.parse(readFileSync(cfgPath, "utf8")) : {}),
};

const toPosix = (p) => p.split(sep).join("/");
const globToRe = (g) =>
  new RegExp(
    "^" +
      toPosix(g)
        .replace(/[.+^${}()|[\]\\]/g, "\\$&")
        .replace(/\*\*\/?/g, "\u0000")
        .replace(/\*/g, "[^/]*")
        .replace(/\u0000/g, ".*") +
      "$",
  );
const allowRes = cfg.allow.map(globToRe);
const allowed = (rel) => allowRes.some((r) => r.test(rel));

const PALETTE =
  "slate|gray|zinc|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose";
const UTIL =
  "bg|text|border|border-[trblxyse]|ring|ring-offset|outline|fill|stroke|from|via|to|divide|placeholder|accent|caret|decoration|shadow";
const rules = [
  { name: "hex colour", re: /(?<![\w&/-])#(?:[0-9a-fA-F]{3,4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})\b/g },
  { name: "colour function", re: /\b(?:rgba?|hsla?|oklch|oklab|lab|lch)\(\s*[\d.]/g },
];
if (cfg.forbidTailwindPalette) {
  rules.push({
    name: "Tailwind palette class",
    re: new RegExp(`(?<![\\w-])(?:[a-z-]+:)*(?:${UTIL})-(?:(?:${PALETTE})-\\d{2,3}|white|black)(?:\\/\\d+)?(?![\\w-])`, "g"),
  });
}

function walk(dir, out) {
  if (!existsSync(dir)) return out;
  for (const name of readdirSync(dir)) {
    if (name === "node_modules" || name.startsWith(".")) continue;
    const p = join(dir, name);
    const st = statSync(p);
    if (st.isDirectory()) walk(p, out);
    else out.push(p);
  }
  return out;
}

function changedFiles() {
  const run = (c) => {
    try {
      return execSync(c, { cwd: app, encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] });
    } catch {
      return "";
    }
  };
  const root = run("git rev-parse --show-toplevel").trim();
  if (!root) return [];
  const list = run("git diff --name-only HEAD") + run("git ls-files --others --exclude-standard --full-name");
  return [...new Set(list.split("\n").filter(Boolean))].map((f) => join(root, f)).filter((f) => existsSync(f) && f.startsWith(app + sep));
}

const files = (process.argv.includes("--changed") ? changedFiles() : cfg.include.flatMap((d) => walk(join(app, d), [])))
  .filter((f) => cfg.extensions.includes(extname(f)))
  .filter((f) => cfg.include.some((d) => toPosix(relative(app, f)).startsWith(toPosix(d).replace(/\/$/, "") + "/")));

let count = 0;
for (const f of files) {
  const rel = toPosix(relative(app, f));
  if (allowed(rel)) continue;
  readFileSync(f, "utf8")
    .split("\n")
    .forEach((line, i) => {
      if (line.includes("token-check-ignore") || /^\s*\*/.test(line)) return;
      // ignore comments: /* ... */ on the line and // to end of line (not inside URLs)
      const code = line.replace(/\/\*.*?\*\//g, (c) => " ".repeat(c.length)).replace(/(^|\s)\/\/.*$/, "$1");
      for (const r of rules) {
        for (const m of code.matchAll(r.re)) {
          count++;
          console.log(`${rel}:${i + 1}:${m.index + 1}  ${r.name}: ${m[0].trim()}`);
        }
      }
    });
}
if (count) {
  console.log(`\n${count} raw colour(s). Use the design tokens (see DESIGN.md), or allow the file in scripts/token-check.json, or mark one line with a token-check-ignore comment that says why.`);
  process.exit(1);
}
console.log(`token-check: ${files.length} file(s), no raw colours`);
