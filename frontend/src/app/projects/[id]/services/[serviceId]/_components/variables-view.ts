import { VALID_KEY } from "@/lib/api/envvar.service";

// The Variables tab's rules (board ReworkServiceVariables, D-145), apart from
// React so they can be tested: which keys the container gets, what isn't live
// yet, and reading a .env file. Step 8's deploy confirm lists the same
// changes under "Also goes live".

interface Keyed {
  key: string;
  created_at?: string;
  updated_at?: string;
  updated_by?: string;
}
interface Valued extends Keyed {
  value: string;
  masked?: boolean;
}

/** How many keys the container gets: its own and its environment's, a key
 *  counted once (an own key wins). */
export function containerKeyCount(lists: { key: string }[][]): number {
  return new Set(lists.flatMap((l) => l.map((k) => k.key))).size;
}

/** The environment's keys in their order, each with the service's own row for
 *  it if it has one, then the service's own keys the environment doesn't set.
 *  `last` keeps a row at the end whatever its key (a row being typed). */
export function mergeKeys<S extends { key: string }, O extends { key: string }>(
  shared: S[],
  own: O[],
  last: (o: O) => boolean = () => false,
): { key: string; shared?: S; own?: O }[] {
  const placed = new Set<O>();
  const out: { key: string; shared?: S; own?: O }[] = [];
  for (const s of shared) {
    const o = own.find((x) => x.key === s.key && !last(x));
    if (o) placed.add(o);
    // Shadowed by a row typed at the end: that row stands for it.
    else if (own.some((x) => x.key === s.key)) continue;
    out.push({ key: s.key, shared: s, own: o });
  }
  for (const o of own) {
    if (placed.has(o)) continue;
    out.push({ key: o.key, own: o, shared: shared.find((s) => s.key === o.key) });
  }
  return out;
}

interface Edited {
  key: string;
  value: string;
  masked?: boolean;
  saved?: { key: string };
  removed?: boolean;
  editing?: boolean;
}

/** After a Save refused because the list changed: the fresh read's rows with
 *  what was typed on top, for the person to review. A key changed or removed
 *  here stays so, one added here stays added, the rest is the fresh read. */
export function rebaseEdits<R extends Edited>(typed: R[], fresh: R[], dirty: (r: R) => boolean): R[] {
  const next = [...fresh];
  for (const r of typed.filter(dirty)) {
    const at = next.findIndex((n) => n.key === (r.saved?.key ?? r.key));
    if (at >= 0) next[at] = { ...next[at], key: r.key, value: r.value, masked: r.masked, removed: r.removed, editing: true };
    else if (!r.removed) next.push({ ...r, saved: undefined });
  }
  return next;
}

/** A saved change the live deployment runs without: "info → debug". */
export interface Pending {
  key: string;
  text: string;
  by?: string;
  at: string;
}

/** What changed since the live deployment started, newest first.
 *  Deployments don't record the values they shipped (D-143's full old → new
 *  waits for that), so the old value is shown only where it is known: an own
 *  key added over an environment key unchanged since. Otherwise the new value
 *  alone ("→ debug", the founders 2026-11-06). A secret is never shown. Keys
 *  removed since aren't known at all. */
export function pendingChanges(p: {
  own: Valued[];
  ownSecrets: Keyed[];
  env: Valued[];
  envSecrets: Keyed[];
  /** When the live deployment started; nothing is pending without one. */
  since?: string | null;
  envName: string;
}): Pending[] {
  if (!p.since) return [];
  const t = new Date(p.since).getTime();
  const after = (iso?: string) => !!iso && new Date(iso).getTime() > t;
  const shown = (v: Valued) => (v.masked ? "a hidden value" : v.value || "empty");
  const ownKeys = new Set([...p.own, ...p.ownSecrets].map((k) => k.key));
  const out: Pending[] = [];
  const push = (k: Keyed, text: string, at: string) =>
    out.push({ key: k.key, text, by: k.updated_by || undefined, at });

  for (const v of p.own) {
    const env = p.env.find((e) => e.key === v.key);
    if (after(v.created_at)) {
      if (env && !after(env.updated_at) && !env.masked && !v.masked)
        push(v, `${env.value || "empty"} → ${shown(v)}`, v.created_at!);
      else if (env) push(v, `→ ${shown(v)}`, v.created_at!);
      else push(v, `added as ${shown(v)}`, v.created_at!);
    } else if (after(v.updated_at)) push(v, `→ ${shown(v)}`, v.updated_at!);
  }
  for (const s of p.ownSecrets) {
    if (after(s.created_at)) push(s, "added", s.created_at!);
    else if (after(s.updated_at)) push(s, "replaced", s.updated_at!);
  }
  // The environment's changes reach the container only where it has no own key.
  for (const v of p.env) {
    if (ownKeys.has(v.key)) continue;
    if (after(v.created_at)) push(v, `added in ${p.envName} as ${shown(v)}`, v.created_at!);
    else if (after(v.updated_at)) push(v, `→ ${shown(v)} in ${p.envName}`, v.updated_at!);
  }
  for (const s of p.envSecrets) {
    if (ownKeys.has(s.key)) continue;
    if (after(s.created_at)) push(s, `added in ${p.envName}`, s.created_at!);
    else if (after(s.updated_at)) push(s, `replaced in ${p.envName}`, s.updated_at!);
  }
  return out.sort((a, b) => b.at.localeCompare(a.at));
}

/** A .env file's variables, the last of a repeated key winning, and the lines
 *  it refused (no "=", or a name that isn't a variable name).
 *  ponytail: one line per value; a quoted value spanning lines is refused,
 *  read it when someone imports one. */
export function parseDotenv(text: string): {
  vars: { key: string; value: string }[];
  refused: string[];
} {
  const found = new Map<string, string>();
  const refused: string[] = [];
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    const body = line.replace(/^export\s+/, "");
    const eq = body.indexOf("=");
    const key = eq > 0 ? body.slice(0, eq).trim() : "";
    if (!key || !VALID_KEY.test(key)) {
      refused.push(key || line);
      continue;
    }
    let value = body.slice(eq + 1).trim();
    const q = value[0];
    if (q === '"' || q === "'") {
      const end = value.indexOf(q, 1);
      if (q === '"') {
        const m = /^"((?:[^"\\]|\\.)*)"/.exec(value);
        if (!m) {
          refused.push(key);
          continue;
        }
        value = m[1].replace(/\\(.)/g, (_, c: string) => (c === "n" ? "\n" : c));
      } else if (end < 0) {
        refused.push(key);
        continue;
      } else value = value.slice(1, end);
    } else {
      value = value.replace(/\s+#.*$/, "");
    }
    found.delete(key);
    found.set(key, value);
  }
  return { vars: [...found].map(([key, value]) => ({ key, value })), refused };
}
