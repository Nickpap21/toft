import { describe, expect, it } from "vitest";
import type { DocPage, DocTreePage } from "@/lib/api/doc.service";
import { buildTree, contents, prepareHTML, splitBlocks, staleSentence } from "./body";

describe("a doc page's HTML", () => {
  const html =
    '<p>Restore <a href="" data-toft-issue="17">STO-17</a></p>\n<pre><code class="language-toft-service"></code></pre>\n' +
    '<h2 id="user-content-1-stop">1. Stop &amp; wait</h2>\n<pre><code class="language-toft-run">docker stop api\n</code></pre>\n' +
    '<ul>\n<li><input disabled="" type="checkbox"> one</li>\n</ul>\n<a href="" data-toft-page="7a8b9c0d-1e2f-4a3b-8c4d-5e6f7a8b9c0d">x</a>';

  it("splits at the blocks, in order", () => {
    const pieces = splitBlocks(html);
    expect(pieces.map((p) => ("block" in p ? `#${p.block}` : "html"))).toEqual(["html", "#0", "html", "#1", "html"]);
  });

  it("links issues and pages, and lets the reader tick checklists", () => {
    const out = prepareHTML(html, "p1");
    expect(out).toContain('href="/projects/p1/issues/17"');
    expect(out).toContain('href="/projects/p1/docs/7a8b9c0d-1e2f-4a3b-8c4d-5e6f7a8b9c0d"');
    expect(out).toContain('<input type="checkbox"> one');
  });

  // The server's shapes (backend gitstore, goldmark): a box with no name
  // fails axe once the reader removes disabled (WCAG 1.3.1, 4.1.2).
  it("gives each checklist box its item's text as a label", () => {
    const box = '<input disabled="" type="checkbox">';
    const on = '<input checked="" disabled="" type="checkbox">';
    const label = (inner: string) => `<label class="toft-task">${inner}</label>`;
    expect(prepareHTML(`<li>${box} one</li>`, "p1")).toBe(`<li>${label('<input type="checkbox"> one')}</li>`);
    expect(prepareHTML(`<li>${on} two</li>`, "p1")).toBe(`<li>${label('<input checked="" type="checkbox"> two')}</li>`);
    expect(prepareHTML(`<li>\n<p>${box} loose</p>\n</li>`, "p1")).toBe(`<li>\n<p>${label('<input type="checkbox"> loose')}</p>\n</li>`);
    expect(prepareHTML(`<li>${box} soft\n<code>code</code> line</li>`, "p1")).toBe(`<li>${label('<input type="checkbox"> soft\n<code>code</code> line')}</li>`);
  });

  it("ends the label before what the item holds beneath its text", () => {
    const box = '<input disabled="" type="checkbox">';
    const out = '<label class="toft-task"><input type="checkbox"> a</label>';
    expect(prepareHTML(`<li>${box} a\n<ul>\n<li>${box} b</li>\n</ul>\n</li>`, "p1")).toBe(
      `<li>${out}\n<ul>\n<li><label class="toft-task"><input type="checkbox"> b</label></li>\n</ul>\n</li>`,
    );
    const fence = '<pre><code class="language-toft-run">x\n</code></pre>';
    expect(prepareHTML(`<li>${box} a\n${fence}\n</li>`, "p1")).toBe(`<li>${out}\n${fence}\n</li>`);
    expect(prepareHTML(`<li>${box} a\n<blockquote>\n<p>q</p>\n</blockquote>\n</li>`, "p1")).toBe(`<li>${out}\n<blockquote>\n<p>q</p>\n</blockquote>\n</li>`);
  });

  it("lists its sections", () => {
    expect(contents(html)).toEqual([{ id: "user-content-1-stop", text: "1. Stop & wait" }]);
  });
});

describe("the out-of-date banner", () => {
  it("names the change, the deploy and how long after", () => {
    const page = {
      verified_at: "2026-10-01T00:00:00Z",
      created_at: "2026-09-01T00:00:00Z",
      services: [
        {
          name: "postgres",
          changes: [
            { field: "image", name: "", from: "postgres:16.2", to: "postgres:16.4" },
            { field: "secret", name: "KEY", from: null, to: "2026-10-03T00:00:00Z" },
          ],
          deployment: { id: "d1", commit: "5e0a7c2aa", finished_at: "2026-10-03T00:00:00Z" },
        },
      ],
    } as unknown as DocPage;
    expect(staleSentence(page, "storefront")).toBe(
      "storefront / postgres changed from postgres:16.2 to postgres:16.4 in deploy 5e0a7c2, 2 days after this page was last verified. 1 more change since.",
    );
  });
});

describe("the tree", () => {
  it("orders each level, and puts orphans at the top", () => {
    const p = (id: string, parent: string | null, position: number) => ({ id, parent_id: parent, position }) as DocTreePage;
    const tree = buildTree([p("b", null, 1), p("a", null, 0), p("c", "a", 0), p("o", "gone", 0)]);
    expect(tree.map((n) => n.page.id)).toEqual(["a", "o", "b"]);
    expect(tree[0].children.map((n) => n.page.id)).toEqual(["c"]);
  });
});
