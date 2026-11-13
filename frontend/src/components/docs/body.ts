import type { DocChange, DocPage, DocService, DocTreePage } from "@/lib/api/doc.service";

/** A page's HTML in pieces: markup, and the place of each of its blocks,
 *  which the server leaves as <pre><code class="language-toft-…"> in the
 *  order of page.blocks (D-86). */
export type Piece = { html: string } | { block: number; raw: string };

const FENCE = /<pre><code class="language-toft-[^"]*">[\s\S]*?<\/code><\/pre>\n?/g;

export function splitBlocks(html: string): Piece[] {
  const out: Piece[] = [];
  let last = 0;
  let n = 0;
  for (const m of html.matchAll(FENCE)) {
    if (m.index > last) out.push({ html: html.slice(last, m.index) });
    out.push({ block: n++, raw: m[0] });
    last = m.index + m[0].length;
  }
  if (last < html.length) out.push({ html: html.slice(last) });
  return out;
}

/** The server's links made the panel's: issues, and pages of the docs.
 *  Checklists are the reader's own to tick (never saved), each box labelled
 *  by its item's text, which ends before any block the item holds. */
export function prepareHTML(html: string, projectId: string): string {
  return html
    .replace(/href="" data-toft-issue="(\d+)"/g, `href="/projects/${projectId}/issues/$1" data-toft-issue="$1"`)
    .replace(/href="" data-toft-page="([0-9a-f-]{36})"/g, `href="/projects/${projectId}/docs/$1" data-toft-page="$1"`)
    .replace(/<input checked="" disabled="" type="checkbox"/g, '<input checked="" type="checkbox"')
    .replace(/<input disabled="" type="checkbox"/g, '<input type="checkbox"')
    .replace(/<input (?:checked="" )?type="checkbox">[\s\S]*?(?=<\/li>|<\/p>|\n<(?:ul|ol|pre|blockquote|table|h[1-6]|hr|p)\b)/g, '<label class="toft-task">$&</label>');
}

/** The page's second-level headings, for On this page. */
export function contents(html: string): { id: string; text: string }[] {
  const out: { id: string; text: string }[] = [];
  for (const m of html.matchAll(/<h2 id="([^"]+)">([\s\S]*?)<\/h2>/g)) {
    out.push({ id: m[1], text: decode(m[2].replace(/<[^>]+>/g, "")) });
  }
  return out;
}

function decode(s: string): string {
  return s
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/&quot;/g, '"')
    .replace(/&#39;/g, "'")
    .replace(/&amp;/g, "&");
}

/** What one change means, as a sentence part: "postgres changed from
 *  postgres:16.2 to postgres:16.4". */
export function changeText(svc: string, c: DocChange): string {
  const what = c.field === "secret" ? "secret" : "variable";
  switch (c.field) {
    case "image":
      return `${svc} changed from ${c.from ?? "nothing"} to ${c.to ?? "nothing"}`;
    case "build":
      return `${svc}’s build or start settings changed`;
    case "server":
      return `${svc} moved from ${c.from ?? "?"} to ${c.to ?? "?"}`;
    default:
      if (!c.from) return `${svc}’s ${what} ${c.name} was added`;
      if (!c.to) return `${svc}’s ${what} ${c.name} was removed`;
      return `${svc}’s ${what} ${c.name} was rewritten`;
  }
}

/** Every change of the page's services, each with its service. */
export function allChanges(services: DocService[]): { svc: DocService; change: DocChange }[] {
  return services.flatMap((svc) => svc.changes.map((change) => ({ svc, change })));
}

/** The banner's sentence: the first change, the deploy it went live in and
 *  how long after the page was last verified. */
export function staleSentence(page: DocPage, project: string): string {
  const changes = allChanges(page.services);
  if (changes.length === 0) return "";
  const { svc, change } = changes[0];
  let s = changeText(`${project} / ${svc.name}`, change);
  if (svc.deployment) {
    const ref = svc.deployment.commit ? svc.deployment.commit.slice(0, 7) : svc.deployment.id.slice(0, 7);
    s += ` in deploy ${ref}`;
    const since = new Date(page.verified_at ?? page.created_at).getTime();
    const days = Math.round((new Date(svc.deployment.finished_at).getTime() - since) / 86_400_000);
    if (days >= 1) s += `, ${days === 1 ? "a day" : `${days} days`} after this page was last ${page.verified_at ? "verified" : "written"}`;
  }
  s += ".";
  if (changes.length > 1) s += ` ${changes.length - 1} more ${changes.length === 2 ? "change" : "changes"} since.`;
  return s;
}

export interface TreeNode {
  page: DocTreePage;
  children: TreeNode[];
}

/** The flat list as a tree, each level in its order. */
export function buildTree(pages: DocTreePage[]): TreeNode[] {
  const byParent = new Map<string, DocTreePage[]>();
  const ids = new Set(pages.map((p) => p.id));
  for (const p of pages) {
    const parent = p.parent_id && ids.has(p.parent_id) ? p.parent_id : "";
    byParent.set(parent, [...(byParent.get(parent) ?? []), p]);
  }
  const level = (parent: string): TreeNode[] =>
    (byParent.get(parent) ?? [])
      .slice()
      .sort((a, b) => a.position - b.position)
      .map((page) => ({ page, children: level(page.id) }));
  return level("");
}

/** The first page in the tree's order, where the Docs tab opens. */
export function firstPage(pages: DocTreePage[]): DocTreePage | undefined {
  return buildTree(pages)[0]?.page;
}
