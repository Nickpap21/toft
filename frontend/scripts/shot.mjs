#!/usr/bin/env node
// Screenshot pages of the running app, and put a page next to its design board.
//
//   npm run shot -- /dashboard
//   npm run shot -- /projects /servers --board Projects
//   npm run shot -- /settings --theme paper --widths 1440
//
// Signs in to the app at TOFT_SHOT_BASE (default http://localhost:3000) with
// TOFT_SHOT_EMAIL and TOFT_SHOT_PASSWORD, a local development account. The
// session is kept in .shots/state.json (gitignored) and reused until it stops
// working. Never point this at production.
//
// Writes, under frontend/.shots/:
//   <route>-<width>.png      full page, for every route and width
//   <route>-vs-<Board>.png   with --board: the board's render
//                            (private/design/canvas/png/<Board>.png) on the left,
//                            the page at 1440 on the right
//
// Options:
//   --board <Name>     compare with private/design/canvas/png/<Name>.png
//   --theme <pref>     paas_theme value; default graphite, as the boards use
//   --widths <a,b>     default 1440,390 (phone)
//   --wait <ms>        extra wait after the page settles; default 1500
//   --base <url>       default $TOFT_SHOT_BASE or http://localhost:3000
//   --signed-out       capture without a session (sign-in, set-up, invites);
//                      needs no TOFT_SHOT_EMAIL or TOFT_SHOT_PASSWORD
//   --palette <query>  open ⌘K on the page, type <query> ("" for none) and
//                      capture the palette alone (board: Palette)
//   --a11y             also run axe-core (WCAG 2.x A/AA) on each route at the
//                      first width; writes <route>-a11y.json and prints a summary
//
// The theme is also saved to the account (PUT /me/theme), since the account's
// choice wins over the browser's once the page loads. The API is
// $NEXT_PUBLIC_API_URL, as the app's, or http://localhost:8080.
//
// Needs Playwright's Chromium once: npx playwright install chromium
import { chromium } from "playwright";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const frontend = resolve(here, "..");
const outDir = join(frontend, ".shots");
const statePath = join(outDir, "state.json");
const boardsDir = resolve(frontend, "..", "private", "design", "canvas", "png");
const HEIGHT = { 1440: 900, 390: 844 };

function parseArgs(argv) {
  const opts = { routes: [], board: "", theme: "graphite", widths: [1440, 390], wait: 1500, base: process.env.TOFT_SHOT_BASE || "http://localhost:3000", signedOut: false, palette: null, a11y: false };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    const next = () => {
      if (i + 1 >= argv.length) fail(`${a} needs a value`);
      return argv[++i];
    };
    if (a === "--board") opts.board = next().replace(/\.(dc\.html|png)$/, "");
    else if (a === "--theme") opts.theme = next();
    else if (a === "--widths") opts.widths = next().split(",").map((w) => Number(w.trim())).filter((w) => w > 0);
    else if (a === "--wait") opts.wait = Number(next());
    else if (a === "--base") opts.base = next();
    else if (a === "--signed-out") opts.signedOut = true;
    else if (a === "--palette") opts.palette = next();
    else if (a === "--a11y") opts.a11y = true;
    else if (a === "-h" || a === "--help") usage(0);
    else if (a.startsWith("--")) fail(`unknown option ${a}`);
    else opts.routes.push(a.startsWith("/") ? a : `/${a}`);
  }
  if (opts.routes.length === 0) usage(1);
  opts.base = opts.base.replace(/\/+$/, "");
  return opts;
}

function usage(code) {
  console.log("usage: npm run shot -- <route> [<route> ...] [--board <Name>] [--theme <pref>] [--widths 1440,390] [--wait <ms>] [--base <url>] [--signed-out] [--palette <query>] [--a11y]");
  process.exit(code);
}

function fail(msg) {
  console.error(`shot: ${msg}`);
  process.exit(1);
}

const slug = (route) => route.replace(/^\/+|\/+$/g, "").replace(/[^A-Za-z0-9._-]+/g, "_") || "root";

// The app keeps event streams open, so "networkidle" never comes: wait for the
// DOM, the fonts and a short settle instead.
async function settle(page, wait) {
  await page.waitForLoadState("domcontentloaded");
  await page.waitForSelector("main, form, h1", { timeout: 15000 }).catch(() => {});
  await page.evaluate(() => document.fonts.ready).catch(() => {});
  await page.waitForTimeout(wait);
}

async function signedIn(browser, base) {
  if (!existsSync(statePath)) return false;
  const context = await browser.newContext({ storageState: statePath });
  const page = await context.newPage();
  try {
    await page.goto(`${base}/dashboard`, { waitUntil: "domcontentloaded", timeout: 30000 });
    await page.waitForTimeout(1500);
    return new URL(page.url()).pathname !== "/";
  } catch {
    return false;
  } finally {
    await context.close();
  }
}

async function signIn(browser, base) {
  const email = process.env.TOFT_SHOT_EMAIL;
  const password = process.env.TOFT_SHOT_PASSWORD;
  if (!email || !password) {
    fail("set TOFT_SHOT_EMAIL and TOFT_SHOT_PASSWORD to a local development account (create one at /register on an empty instance)");
  }
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto(`${base}/`, { waitUntil: "domcontentloaded", timeout: 30000 });
  await page.waitForSelector("#email", { timeout: 15000 }).catch(() => fail(`no sign-in form at ${base}/ — is the app running?`));
  await page.fill("#email", email);
  await page.fill("#password", password);
  await page.click('button[type="submit"]');
  await page.waitForURL((u) => new URL(u).pathname !== "/", { timeout: 20000 }).catch(() => fail("sign-in failed: check TOFT_SHOT_EMAIL and TOFT_SHOT_PASSWORD"));
  mkdirSync(outDir, { recursive: true });
  await context.storageState({ path: statePath });
  await context.close();
}

/** Saves opts.theme to the shot account, which overrides the browser's. */
async function setAccountTheme(theme) {
  const api = process.env.NEXT_PUBLIC_API_URL || "http://localhost:8080";
  const state = JSON.parse(readFileSync(statePath, "utf8"));
  const token = (state.origins ?? []).flatMap((o) => o.localStorage ?? []).find((i) => i.name === "paas_token")?.value;
  const res = await fetch(`${api}/api/v1/me/theme`, {
    method: "PUT",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: JSON.stringify({ theme }),
  }).catch((e) => ({ ok: false, status: e.message }));
  if (!res.ok) console.warn(`shot: could not save the theme to the account (${res.status}); the page may use another`);
}

async function a11yScan(page, route) {
  const axe = readFileSync(createRequire(import.meta.url).resolve("axe-core/axe.min.js"), "utf8");
  await page.addScriptTag({ content: axe });
  const res = await page.evaluate(() =>
    axe.run(document, { runOnly: { type: "tag", values: ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"] }, resultTypes: ["violations"] }),
  );
  const v = res.violations.map((x) => ({ id: x.id, impact: x.impact, help: x.help, nodes: x.nodes.length, targets: x.nodes.slice(0, 5).map((n) => n.target.join(" ")) }));
  const file = join(outDir, `${slug(route)}-a11y.json`);
  writeFileSync(file, JSON.stringify(v, null, 2));
  const by = (lvl) => v.filter((x) => x.impact === lvl).length;
  console.log(`a11y ${route}: ${v.length} rule(s) violated (critical ${by("critical")}, serious ${by("serious")}, moderate ${by("moderate")}, minor ${by("minor")}) -> ${file}`);
  for (const x of v.slice(0, 8)) console.log(`  - [${x.impact}] ${x.id} x${x.nodes}: ${x.help} (e.g. ${x.targets[0]})`);
}

async function capture(browser, opts, route, width) {
  const context = await browser.newContext({
    storageState: opts.signedOut ? undefined : statePath,
    viewport: { width, height: HEIGHT[width] ?? 900 },
    deviceScaleFactor: 1,
  });
  // The theme preference is read before first paint, so set it before load.
  await context.addInitScript((theme) => {
    try {
      localStorage.setItem("paas_theme", theme);
    } catch {}
  }, opts.theme);
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message.split("\n")[0]));
  await page.goto(`${opts.base}${route}`, { waitUntil: "domcontentloaded", timeout: 30000 });
  await settle(page, opts.wait);
  for (const e of errors) console.warn(`shot: ${route} at ${width}px threw in the browser: ${e}`);
  // Hide Next's dev-mode badge. Added after hydration: an element inserted
  // before React hydrates breaks the page.
  await page.addStyleTag({ content: "nextjs-portal { display: none !important; }" }).catch(() => {});
  if (opts.palette !== null) {
    await page.keyboard.press("Control+k");
    const dialog = page.getByRole("dialog", { name: "Command palette" });
    await dialog.waitFor({ timeout: 10000 });
    await page.keyboard.type(opts.palette);
    await page.waitForTimeout(600);
    const file = join(outDir, `${slug(route)}-palette-${width}.png`);
    await dialog.screenshot({ path: file, animations: "disabled", caret: "hide" });
    await context.close();
    return file;
  }
  if (opts.a11y && width === opts.widths[0]) await a11yScan(page, route);
  // The app shell scrolls its <main>, not the document, so fullPage alone
  // stops at the viewport: grow the viewport by what <main> scrolls. (Not by
  // every scroll area: a log viewer sized to the viewport would grow too.)
  const extra = await page.evaluate(() => {
    const main = document.querySelector("main");
    return main ? Math.max(0, main.scrollHeight - main.clientHeight) : 0;
  });
  if (extra > 0) {
    await page.setViewportSize({ width, height: (HEIGHT[width] ?? 900) + extra });
    await page.waitForTimeout(300);
  }
  const landed = new URL(page.url()).pathname;
  const file = join(outDir, `${slug(route)}-${width}.png`);
  await page.screenshot({ path: file, fullPage: true, animations: "disabled", caret: "hide" });
  await context.close();
  if (landed !== route.split("?")[0]) console.warn(`shot: ${route} redirected to ${landed}`);
  return file;
}

async function compare(browser, boardFile, pageFile, out) {
  const data = (f) => `data:image/png;base64,${readFileSync(f).toString("base64")}`;
  const context = await browser.newContext({ viewport: { width: 2960, height: 900 }, deviceScaleFactor: 1 });
  const page = await context.newPage();
  await page.setContent(`<!doctype html><html><body style="margin:0;background:#2a2a2a;font:600 14px system-ui;color:#ddd">
    <div style="display:flex;gap:40px;padding:24px;align-items:flex-start">
      <figure style="margin:0"><figcaption style="padding:0 0 8px">Board</figcaption><img src="${data(boardFile)}" style="display:block;max-width:1440px"></figure>
      <figure style="margin:0"><figcaption style="padding:0 0 8px">Page</figcaption><img src="${data(pageFile)}" style="display:block;max-width:1440px"></figure>
    </div></body></html>`);
  await page.waitForFunction(() => [...document.images].every((i) => i.complete));
  await page.screenshot({ path: out, fullPage: true });
  await context.close();
}

const opts = parseArgs(process.argv.slice(2));
let boardFile = "";
if (opts.board) {
  boardFile = join(boardsDir, `${opts.board}.png`);
  if (!existsSync(boardFile)) fail(`no board render at ${boardFile}`);
}
mkdirSync(outDir, { recursive: true });
const browser = await chromium.launch();
try {
  if (!opts.signedOut) {
    if (!(await signedIn(browser, opts.base))) await signIn(browser, opts.base);
    await setAccountTheme(opts.theme);
  }
  for (const route of opts.routes) {
    const files = {};
    for (const width of opts.widths) {
      files[width] = await capture(browser, opts, route, width);
      console.log(files[width]);
    }
    if (boardFile) {
      const pageFile = files[1440] ?? (await capture(browser, opts, route, 1440));
      const out = join(outDir, `${slug(route)}${opts.palette !== null ? "-palette" : ""}-vs-${opts.board}.png`);
      await compare(browser, boardFile, pageFile, out);
      console.log(out);
    }
  }
} finally {
  await browser.close();
}
