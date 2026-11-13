// The dashboard's load (D-131), from a fourth server in the same location as
// the panels, in Playwright's own image; bench/README.md has the command.
// Signs in once, then loads the main page ten times, each in a fresh context
// (a cold cache, the same session), and prints this round's result as JSON.
import { chromium } from "playwright";

const { PLATFORM, ROUND, PANEL, LOGIN, PAGE, EMAIL, PASSWORD } = process.env;
const browser = await chromium.launch();

const login = await browser.newContext({ ignoreHTTPSErrors: true });
const page = await login.newPage();
await page.goto(PANEL + LOGIN);
await page.locator("input[type=email], input[name=email]").first().fill(EMAIL);
await page.locator("input[type=password]").first().fill(PASSWORD);
await page.keyboard.press("Enter");
await page.waitForURL((url) => url.pathname !== LOGIN, { timeout: 30_000 });
const storageState = await login.storageState();
await login.close();

const result = { platform: PLATFORM, round: ROUND, load_ms: [], lcp_ms: [], page_kib: [], requests: [] };
for (let i = 0; i < 10; i++) {
  const context = await browser.newContext({ ignoreHTTPSErrors: true, storageState });
  const p = await context.newPage();
  const sizes = [];
  p.on("requestfinished", (r) => sizes.push(r.sizes()));
  await p.goto(PANEL + PAGE, { waitUntil: "load" });
  if (new URL(p.url()).pathname === LOGIN) throw new Error(`${PANEL + PAGE} sent the session back to sign-in`);
  // Data a page fetches after load counts too: ten seconds from the start.
  await p.waitForTimeout(Math.max(0, 10_000 - (await p.evaluate(() => performance.now()))));
  // Onboarding or setup that redirects would be measured instead of the page.
  if (new URL(p.url()).pathname !== PAGE) throw new Error(`${PANEL + PAGE} went to ${p.url()}`);
  const [nav, lcp] = await p.evaluate(async () => [
    performance.getEntriesByType("navigation")[0].loadEventEnd,
    await new Promise((resolve) =>
      new PerformanceObserver((list) => resolve(list.getEntries().at(-1).startTime)).observe({
        type: "largest-contentful-paint",
        buffered: true,
      }),
    ),
  ]);
  const bytes = (await Promise.all(sizes)).reduce((sum, s) => sum + s.responseHeadersSize + s.responseBodySize, 0);
  result.load_ms.push(Math.round(nav));
  result.lcp_ms.push(Math.round(lcp));
  result.page_kib.push(Math.round(bytes / 1024));
  result.requests.push(sizes.length);
  await context.close();
}
await browser.close();
console.log(JSON.stringify(result));
