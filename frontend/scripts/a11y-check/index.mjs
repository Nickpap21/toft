#!/usr/bin/env node
// The accessibility gate (npm run a11y, after npm run build; CI runs it).
//
// Serves the production build with the API on the panel's own origin, as the
// release image does, answers every /api/v1 read from ./fixtures.mts and refuses
// every write. Two passes:
//
//   1. Every page below, in Graphite and Paper, at 1440 and 390: axe (WCAG 2.x
//      A and AA), a console error or page error, the page's own text being
//      there, and at 390 the page itself not scrolling sideways.
//   2. Every page with its reads failing (500): a failed read must say so, and
//      must not be drawn as an empty answer. Backstops toft/empty-needs-read.
//   3. Every read of those pages failing alone, the others answering, at one
//      theme and width: if <main> lost a heading, a row or a term, it must say
//      a read failed. Pass 2 can't tell: one banner stands for all its reads.
//
//   npm run a11y                     passes 1 and 2
//   npm run a11y -- --each-read      pass 3 as well, most of the time (CI: a release and daily)
//   npm run a11y -- /settings /cycles    only the pages whose path has one of these
//
// Needs Playwright's Chromium: npx playwright install chromium
import { chromium } from "playwright";
import { spawn } from "node:child_process";
import { cpSync, existsSync, readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { createServer } from "node:net";
import { availableParallelism } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { DOC, ENV, ISSUE, PROJECT, PULL, REPO, REPO_ID, REQUEST, SERVER, SERVICE, DEPLOYMENT, SHA, SESSION_PATHS, answer, emptyRepository, signedInUser } from "./fixtures.mts";

const frontend = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..");
const PORT = await freePort();
const BASE = `http://127.0.0.1:${PORT}`;
const WORKERS = Math.min(availableParallelism(), 8);
const THEMES = ["graphite", "paper"];
const WIDTHS = [1440, 390];
const HEIGHT = { 1440: 900, 390: 844 };
const SCAN_TAGS = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"];
// The check's own session: nothing about it is real. exp is 2100-01-01.
const TOKEN = `e30.${btoa(JSON.stringify({ exp: 4102444800 }))}.a11y`;

const P = `/projects/${PROJECT}`;
const S = `${P}/services/${SERVICE}`;
const R = `${P}/code/${REPO}`;

// `see` is text the page draws itself, inside <main>, so a blank page, an error
// page or a sign-in redirect can't pass; `act` puts the page in the state to
// scan; `over` answers some API paths differently, with HOLD for a read that
// never answers; `writes` is the answer every write gets, in place of 405;
// `readsFailed` says why a page has no failed state in pass 2.
const HOLD = Symbol("hold");
const PAGES = [
  { path: "/", see: "Sign in to Toft", signedOut: true, readsFailed: "the sign-in page reads nothing a failure would change" },
  { path: "/dashboard", see: "Needs you" },
  { path: "/dashboard", name: "palette", see: "Needs you", act: palette, readsFailed: "the palette draws on the shell's reads; Home covers their failure" },
  { path: "/inbox", see: "orders-worker failed to deploy" },
  { path: "/issues/mine", see: "open, assigned to you" },
  { path: "/cycles", see: "Cycle settings" },
  { path: "/cycles/14", see: "Cycle settings" },
  { path: "/projects", see: "Sorted by what needs you first" },
  { path: P, see: "Needs attention" },
  { path: `${P}?env=${ENV}`, name: "current environment", see: "Needs attention" },
  { path: `${P}/services`, see: "orders-worker" },
  { path: `${P}/settings`, see: "Copy project ID" },
  { path: `${P}/environments/${ENV}`, see: "Shared variables" },
  { path: S, name: "overview", see: "Live deployment" },
  { path: S, name: "loading", see: { role: "status" }, over: { [`/api/v1/services/${SERVICE}`]: HOLD }, readsFailed: "this entry is the page before its reads answer" },
  { path: `${S}?tab=deployments`, name: "deploys", see: "newest first" },
  { path: `${S}?tab=logs`, name: "logs", see: "payment webhook retried" },
  { path: `${S}?tab=settings`, name: "settings", see: "Shown everywhere in the panel at once" },
  { path: `${S}?tab=deployments&deployment=${DEPLOYMENT}`, name: "deployment", see: "All deploys" },
  { path: `${P}/issues`, name: "list", see: "Carts vanish" },
  { path: `${P}/issues?layout=board`, name: "board", see: "1 check failing" },
  { path: `${P}/issues?layout=timeline`, name: "timeline", see: "Dated and started issues" },
  { path: `${P}/issues/${ISSUE}`, see: "Reproduce with a 1-minute session" },
  { path: `${P}/docs`, name: "first page", see: "Tell support the window" },
  { path: `${P}/docs/${DOC}`, see: "Tell support the window" },
  { path: R, see: "The shop and its checkout." },
  { path: R, name: "empty repository", see: "Push an existing repository", over: { [`/api/v1/projects/${PROJECT}/repositories/${REPO_ID}`]: emptyRepository }, readsFailed: "the same page as above, with other rows" },
  { path: `${R}/blob?path=internal/cart/session.go`, see: "line 12 of the session store" },
  { path: `${R}/commits`, see: "Commits on" },
  { path: `${R}/commits/${SHA}`, see: "The cart outlives the session" },
  { path: `${R}/pipelines`, see: "What each push and merge did" },
  { path: `${R}/pulls`, see: "Show saved addresses" },
  { path: `${R}/pulls/${PULL}`, see: "Looks right." },
  { path: `${R}/pulls/${PULL}?tab=files`, name: "files", see: "The cart outlives the session" },
  { path: `${R}/pulls/${PULL}?tab=checks`, name: "checks", see: "on the head commit" },
  { path: "/services/new?source=image", name: "name taken", see: "Create only", act: nameTaken, writes: { status: 409, json: { error: "That name is taken.", code: "project_name_taken" } }, readsFailed: "this entry is the form's refused-name state" },
  { path: "/domains", see: "Waiting for DNS" },
  { path: "/servers", see: "reserved is what services" },
  { path: `/servers/${SERVER}`, see: "Test connection" },
  { path: "/shared", see: "grafana" },
  { path: "/audit-log", see: "entries in the last 7 days" },
  { path: "/settings", see: "role changes apply" },
  { path: "/settings", name: "no servers", see: "Add one on Servers", over: { "/api/v1/servers": { data: [] }, "/api/v1/org/isolation": { data: { default_isolation: "standard", servers: [] } } }, readsFailed: "the same page as above, with other rows" },
  { path: "/settings/agents", see: "Pause all agents" },
  { path: `/settings/agents/requests/${REQUEST}`, see: "Its reason, in its own words" },
  { path: "/settings/api-keys", see: "ci-deploys" },
  { path: "/settings/alerts", see: "the health check on /health never answered" },
  { path: "/settings/storage", see: "of 10.0 GiB" },
  { path: "/settings/backups", see: "acme-toft-backups" },
  { path: "/settings/account", see: "alex@work-laptop" },
  { path: "/instance", see: "toft-api" },
  { path: "/instance/kit", see: "When to store it again" },
  { path: "/instance/kit", name: "out of date", see: "When to store it again", act: showKit, writes: { status: 200, json: { data: { token: "r".repeat(40), expires_in: 600 } } }, over: { "/api/v1/instance/setup": { data: { in_progress: false, domain: "panel.acme.test", domain_set: true, role: "apps", role_set: true, kit_acknowledged_at: "2026-10-18T10:00:00Z", kit_out_of_date: true, finished_at: "2026-10-18T10:00:00Z", app: "" } } }, readsFailed: "this entry is the kit after a password confirmation; the entry above covers its reads" },
  { path: "/instance/server", see: "Peak" },
  { path: "/instance/maintenance", see: "openssl" },
  { path: "/instance/accounts", see: "Sam Okafor" },
  { path: "/instance/accounts", name: "reset link", see: "Sam Okafor", act: resetLink, writes: { status: 200, json: { data: { token: "r".repeat(80), expires_at: new Date(Date.now() + 864e5).toISOString() } } }, readsFailed: "this entry is the dialog after a write" },
  { path: "/instance/deploy-policy", see: "checked on save and again at deploy" },
  { path: "/instance/audit-log", see: "Search loaded entries" },
];

/** The new-service form after the API refused its name. */
async function nameTaken(page) {
  await page.fill("#ns-name", "Storefront");
  await page.fill("#ns-image", "nginx:1.27");
  await page.getByRole("button", { name: "Create only" }).click();
  await page.locator("#ns-name-help").getByText("already exists").waitFor({ timeout: 10000 });
}

/** The reset-link dialog, showing a long link. */
async function resetLink(page) {
  await page.getByRole("button", { name: "New reset link for Sam Okafor" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.locator('input[type="password"]').fill("the operator's password");
  await dialog.getByRole("button", { name: "Make reset link" }).click();
  await dialog.locator("#reset-link").waitFor({ timeout: 10000 });
}

/** The recovery kit, once the password prompt it opens with is answered. */
async function showKit(page) {
  const dialog = page.getByRole("dialog");
  await dialog.locator('input[type="password"]').fill("the operator's password");
  await dialog.getByRole("button", { name: "Confirm" }).click();
  await page.getByRole("button", { name: "Download the kit" }).waitFor({ timeout: 10000 });
}

/** The command palette open, with its first row selected. */
async function palette(page) {
  await page.keyboard.press("Control+k");
  await page.getByRole("dialog", { name: "Command palette" }).waitFor();
  await page.keyboard.press("ArrowDown");
}

function freePort() {
  return new Promise((resolved) => {
    const s = createServer().listen(0, "127.0.0.1", () => {
      const { port } = s.address();
      s.close(() => resolved(port));
    });
  });
}

function serve() {
  const dir = join(frontend, ".next", "standalone");
  const server = join(dir, "server.js");
  if (!existsSync(server)) die("no production build: run npm run build first");
  // What the Dockerfile copies next to server.js.
  cpSync(join(frontend, ".next", "static"), join(dir, ".next", "static"), { recursive: true });
  cpSync(join(frontend, "public"), join(dir, "public"), { recursive: true });
  return spawn(process.execPath, [server], { env: { ...process.env, NODE_ENV: "production", PORT: String(PORT), HOSTNAME: "127.0.0.1" }, stdio: ["ignore", "ignore", "inherit"] });
}

async function up() {
  for (let i = 0; i < 100; i++) {
    if (await fetch(BASE).then(() => true, () => false)) return;
    await new Promise((r) => setTimeout(r, 100));
  }
  die(`the production server didn't answer on ${BASE}`);
}

function die(msg) {
  console.error(`a11y: ${msg}`);
  process.exit(1);
}

const axeSource = readFileSync(createRequire(import.meta.url).resolve("axe-core/axe.min.js"), "utf8");

/** Text or a role and name the page draws itself. */
const locate = (page, see) => (typeof see === "string" ? page.locator("main").getByText(see, { exact: false }) : page.locator("main").getByRole(see.role, see.name ? { name: see.name } : {})).filter({ visible: true }).first();

async function axe(page, report) {
  // By evaluate, not a script tag: the panel's CSP refuses inline scripts.
  await page.evaluate(axeSource);
  const { violations } = await page.evaluate((values) => window.axe.run(document, { runOnly: { type: "tag", values }, resultTypes: ["violations"] }), SCAN_TAGS);
  for (const v of violations) for (const n of v.nodes) report(`${v.id} [${v.impact}] ${n.target.join(" ")}`);
}

async function sideways(page, report) {
  const over = await page.evaluate(() => {
    const d = document.documentElement;
    const m = document.querySelector("main");
    return { document: d.scrollWidth - d.clientWidth, main: m ? m.scrollWidth - m.clientWidth : 0 };
  });
  for (const [what, by] of Object.entries(over)) if (by > 0) report(`scrolls sideways: ${what} by ${by}px`);
}

async function open(browser, { pg, theme, width, failing, report }) {
  const context = await browser.newContext({ viewport: { width, height: HEIGHT[width] }, reducedMotion: "reduce" });
  if (!pg.signedOut) await context.addCookies([{ name: "paas_auth", value: "1", url: BASE }]);
  await context.addInitScript(({ theme, token, user }) => {
    localStorage.setItem("paas_theme", theme);
    if (user) {
      localStorage.setItem("paas_token", token);
      localStorage.setItem("paas_user", JSON.stringify(user));
    }
  }, { theme, token: TOKEN, user: pg.signedOut ? null : signedInUser });

  const inflight = new Set();
  const seen = { reads: new Set(), failed: false };
  await context.route("**/api/v1/**", (route) => {
    const req = route.request();
    const url = new URL(req.url());
    if (req.method() !== "GET") return route.fulfill(pg.writes ?? { status: 405, json: { error: "The accessibility check refuses writes." } });
    const over = pg.over?.[url.pathname];
    if (over === HOLD) return;
    if (over) return route.fulfill({ json: over });
    // A stream stays open and quiet, as one does between events.
    if (url.pathname === "/api/v1/events") return;
    if (failing?.(url.pathname)) {
      seen.failed = true;
      return route.fulfill({ status: 500, json: { error: "internal error", status: 500, title: "Internal Server Error" } });
    }
    const res = answer(url, theme);
    if (!res) {
      report(`no fixture for GET ${url.pathname}${url.search}`);
      return route.fulfill({ status: 500, json: { error: "no fixture" } });
    }
    if (!SESSION_PATHS.includes(url.pathname) && res.contentType !== "text/event-stream") seen.reads.add(url.pathname);
    return route.fulfill(res);
  });

  const page = await context.newPage();
  const answers = (r) => {
    const path = new URL(r.url()).pathname;
    return path.startsWith("/api/v1/") && path !== "/api/v1/events" && pg.over?.[path] !== HOLD;
  };
  page.on("request", (r) => answers(r) && inflight.add(r));
  page.on("requestfinished", (r) => inflight.delete(r));
  page.on("requestfailed", (r) => inflight.delete(r));
  page.on("pageerror", (e) => report(`page error: ${e.message.split("\n")[0]}`));
  page.on("console", (m) => {
    if (m.type() !== "error") return;
    // Pass 2's 500s are logged by the browser; those are the point.
    if ((failing || pg.writes) && m.text().startsWith("Failed to load resource")) return;
    report(`console error: ${m.text().slice(0, 300)}`);
  });
  return { context, page, inflight, seen };
}

/** The page's reads answered and the fonts in, then `beats` of 100 ms for React. */
async function settle(page, inflight, report, beats = 3) {
  await page.evaluate(() => document.fonts.ready);
  const start = Date.now();
  let quiet = 0;
  while (quiet < beats) {
    if (Date.now() - start >= 15000) return report(`reads never went quiet in 15s: ${[...inflight].map((r) => new URL(r.url()).pathname).join(", ")}`);
    await page.waitForTimeout(100);
    quiet = inflight.size === 0 ? quiet + 1 : 0;
  }
}

/** What <main> draws, to compare runs: how many headings, rows and terms, its
 *  failed states, any empty answer, and its lines (digits blanked: a time ago
 *  moves between runs) to name what went. A value that falls back to "—" is
 *  no loss here: the row stands and doesn't claim anything. */
const drawn = (page) =>
  page.evaluate(() => ({
    lines: [...new Set((document.querySelector("main")?.innerText ?? "").split("\n").map((l) => l.trim().replace(/\d+/g, "#")).filter(Boolean))],
    failed: document.querySelectorAll('main [role="alert"],main [data-read-state="failed"]').length,
    empty: [...document.querySelectorAll('[data-read-state="empty"]')].map((e) => e.textContent.trim().slice(0, 80)),
    units: [...document.querySelectorAll("main :is(h1,h2,h3,h4,h5,h6,li,tr,dt)")].filter((e) => e.checkVisibility()).length,
  }));

async function scan(browser, job) {
  const { pg, theme, width } = job;
  const report = (what) => job.problems.push(what);
  const { context, page, inflight, seen } = await open(browser, { pg, theme, width, report });
  try {
    await page.goto(`${BASE}${pg.path}`, { waitUntil: "domcontentloaded", timeout: 30000 });
    await locate(page, pg.see).waitFor({ state: "visible", timeout: 15000 }).catch(async () => report(`didn't draw "${pg.see.name ?? pg.see}" (landed on ${new URL(page.url()).pathname}): ${(await page.locator("body").innerText().catch(() => "")).slice(0, 200).replaceAll("\n", " | ")}`));
    await settle(page, inflight, report);
    if (pg.act) {
      await pg.act(page);
      await settle(page, inflight, report);
    }
    if (job.baseline) job.baseline = { reads: seen.reads, ...(await drawn(page)) };
    await axe(page, report);
    if (width === 390) await sideways(page, report);
  } finally {
    await context.close();
  }
}

async function scanFailing(browser, job) {
  const { pg, theme, width } = job;
  const report = (what) => job.problems.push(what);
  const { context, page, inflight } = await open(browser, { pg, theme, width, failing: (path) => !SESSION_PATHS.includes(path), report });
  try {
    await page.goto(`${BASE}${pg.path}`, { waitUntil: "domcontentloaded", timeout: 30000 });
    await page.waitForSelector("main, form, h1", { timeout: 15000 }).catch(() => report("drew nothing"));
    // One retry after a second (the query client's default) before a read fails.
    await page.waitForTimeout(1500);
    await page.waitForFunction(() => document.querySelector('main [role="alert"], main [data-read-state="failed"]'), undefined, { timeout: 8000 }).catch(() => {});
    await settle(page, inflight, report);
    const now = await drawn(page);
    for (const e of now.empty) report(`a failed read is drawn as empty: "${e}"`);
    if (!now.failed) report("reads failed and the page doesn't say so");
    await axe(page, report);
    await sideways(page, report);
  } finally {
    await context.close();
  }
}

async function scanOneRead(browser, job) {
  const { pg, theme, width, read, baseline } = job;
  const report = (what) => job.problems.push(what);
  const { context, page, inflight, seen } = await open(browser, { pg, theme, width, failing: (path) => path === read, report });
  try {
    await page.goto(`${BASE}${pg.path}`, { waitUntil: "domcontentloaded", timeout: 30000 });
    await page.waitForSelector("main, form, h1", { timeout: 15000 }).catch(() => report("drew nothing"));
    for (const start = Date.now(); !seen.failed && Date.now() - start < 10000; ) await page.waitForTimeout(50);
    // Longer than the query client's second before its one retry.
    await settle(page, inflight, report, 13);
    const now = await drawn(page);
    // By count: an empty state's own text can quote the read that failed.
    if (now.empty.length > baseline.empty.length) for (const e of now.empty) if (!baseline.empty.includes(e)) report(`a failed read is drawn as empty: "${e}"`);
    if (now.units < baseline.units && now.failed <= baseline.failed) {
      const lost = baseline.lines.filter((l) => !now.lines.includes(l));
      report(`took ${baseline.units - now.units} heading(s), row(s) or term(s) away and doesn't say so: "${lost.slice(0, 4).join('", "').slice(0, 200)}"`);
    }
  } finally {
    await context.close();
  }
}

const only = process.argv.slice(2).filter((a) => a.startsWith("/"));
const eachRead = process.argv.includes("--each-read");
const wanted = only.length ? PAGES.filter((p) => only.some((o) => p.path.includes(o))) : PAGES;
const jobs = wanted.flatMap((pg) => THEMES.flatMap((theme) => WIDTHS.map((width) => ({ pg, theme, width, run: scan, problems: [], baseline: !pg.readsFailed && theme === "graphite" && width === 390 }))));
const failed = wanted.filter((pg) => !pg.readsFailed).map((pg) => ({ pg, theme: "graphite", width: 390, run: scanFailing, problems: [], pass: "reads failing" }));

const started = Date.now();
const server = serve();
let alone = [];
try {
  await up();
  const browser = await chromium.launch();
  const work = (queue) =>
    Promise.all(Array.from({ length: WORKERS }, async () => {
      for (let job; (job = queue.shift()); ) await job.run(browser, job).catch((e) => job.problems.push(`crashed: ${e.message.split("\n")[0]}`));
    }));
  await work([...jobs, ...failed]);
  // Pass 3 runs on what pass 1 saw each page read and draw.
  if (eachRead) alone = jobs.filter((j) => j.baseline?.reads).flatMap(({ pg, theme, width, baseline }) => [...baseline.reads].map((read) => ({ pg, theme, width, read, baseline, run: scanOneRead, problems: [], pass: `${read} failing` })));
  await work([...alone]);
  await browser.close();
} finally {
  server.kill();
}

const all = [...jobs, ...failed, ...alone];
let count = 0;
for (const { pg, theme, width, problems, pass } of all) {
  for (const p of new Set(problems)) {
    count++;
    console.log(`FAIL ${pg.path}${pg.name ? ` (${pg.name})` : ""} ${theme} ${width}${pass ? ` ${pass}` : ""}: ${p}`);
  }
}
const seconds = Math.round((Date.now() - started) / 1000);
console.log(`a11y: ${all.length} scans of ${wanted.length} pages in ${seconds}s, ${WORKERS} at a time, ${count} problem(s)`);
process.exit(count ? 1 : 0);
