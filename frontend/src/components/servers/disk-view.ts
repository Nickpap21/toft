// The disk card's words and numbers (boards ServerDisk, ServerDiskActions; D-135).
import type { DiskClass, DiskClean, DiskKind, DiskPolicy, DiskReport, DiskVolume } from "@/lib/api/server.service";
import { formatDateTime } from "@/lib/utils";

/** Decimal units, as Docker and the limit's GB field count: "76.5 GB", "205 MB". */
export function gb(n: number): string {
  if (n >= 1e12) return `${(n / 1e12).toFixed(1).replace(/\.0$/, "")} TB`;
  if (n >= 1e9) {
    const v = n / 1e9;
    return `${v >= 100 ? Math.round(v) : v.toFixed(1).replace(/\.0$/, "")} GB`;
  }
  if (n >= 1e6) return `${Math.round(n / 1e6)} MB`;
  if (n > 0) return `${Math.max(1, Math.round(n / 1e3))} kB`;
  return "0 GB";
}

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

/** "a, b and c", "a, b and 5 more". */
function list(items: string[], total = items.length): string {
  const more = total - items.length;
  if (more > 0) return `${items.join(", ")} and ${more} more`;
  if (items.length < 2) return items.join("");
  return `${items.slice(0, -1).join(", ")} and ${items[items.length - 1]}`;
}

export const KIND_LABEL: Record<DiskKind, string> = {
  untagged: "Untagged images",
  old_builds: "Old builds",
  old_panel_releases: "Old panel releases",
  unused_pulled: "Pulled images no service uses",
};

/** The order the clean takes, and the dialog lists, them. */
export const CLEAN_ORDER: DiskKind[] = ["old_panel_releases", "untagged", "old_builds", "unused_pulled"];

/** The order the policy lists them: the defaults first. */
export const POLICY_ORDER: DiskKind[] = ["untagged", "old_builds", "old_panel_releases", "unused_pulled"];

const tagOf = (ref: string) => ref.slice(ref.lastIndexOf(":") + 1);

/** Why a removable class is safe, in the card and the dialog. */
export function safeNote(c: DiskClass, r: DiskReport): string {
  switch (c.kind) {
    case "untagged":
      return "No tag, and no container uses them, so nothing can roll back to them.";
    case "old_builds": {
      const named = c.examples.length ? ` of ${list(c.examples)}` : "";
      return `Builds${named} older than the deploys each service keeps for rollback, and those of deleted services. Those deploys can’t be rolled back to afterwards.`;
    }
    case "old_panel_releases": {
      const kept = r.classes.find((k) => k.kind === "panel_releases");
      const tags = (kept?.examples ?? []).map(tagOf);
      return tags.length
        ? `Releases other than ${list(tags)}. toft rollback only ever goes back one release.`
        : "Releases other than the running one and the one before. toft rollback only ever goes back one release.";
    }
    case "unused_pulled":
      return `${list(c.examples, c.count)}. A deploy that needs one pulls it again.`.replace(/^\. /, "");
    default:
      return "";
  }
}

export type Row = { k: string; count: string; v: string; note: string; bytes: number };

/** The removable classes that hold something, in the clean's order. */
export function safeRows(r: DiskReport): (Row & { kind: DiskKind; auto: boolean })[] {
  return CLEAN_ORDER.flatMap((kind) => {
    const c = r.classes.find((x) => x.kind === kind);
    if (!c || c.count === 0) return [];
    return [{ kind, auto: c.automatic, k: KIND_LABEL[kind], count: String(c.count), v: gb(c.bytes), note: safeNote(c, r), bytes: c.bytes }];
  });
}

/** What Toft keeps, and why. panel: the panel's own server. */
export function keptRows(r: DiskReport, panel: boolean): Row[] {
  const by = (k: DiskClass["kind"]) => r.classes.find((c) => c.kind === k);
  const rows: Row[] = [];
  const inUse = by("in_use");
  if (inUse && inUse.count > 0) {
    rows.push({ k: "In use", count: plural(inUse.count, "image"), v: gb(inUse.bytes), bytes: inUse.bytes,
      note: panel ? "The panel, its database and Traefik, and the services here, running or stopped." : "Used by the containers here, running or stopped." });
  }
  const rel = by("panel_releases");
  if (rel && rel.count > 0) {
    const tags = [...new Set(rel.examples.map(tagOf))];
    // More than one: installed without toft upgrade's state, so all are kept.
    rows.push(tags.length === 1
      ? { k: "The panel’s previous release", count: tags[0], v: gb(rel.bytes), bytes: rel.bytes, note: "toft rollback goes back to it without downloading anything." }
      : { k: "The panel’s releases", count: plural(rel.count, "image"), v: gb(rel.bytes), bytes: rel.bytes,
        note: "This panel wasn’t installed with install.sh, so Toft can’t tell which release runs and keeps them all." });
  }
  const rb = by("rollback");
  if (rb && rb.count > 0) {
    const names = r.rollback_kept.map((s) => s.service_name);
    const ns = [...new Set(r.rollback_kept.map((s) => s.kept))];
    const each = ns.length === 1 ? `last ${ns[0]} deploys` : "last deploys";
    const o = r.other_workspaces;
    const others = o.rollback_images > 0 ? ` ${plural(o.services, "service")} from other workspaces ${o.services === 1 ? "keeps" : "keep"} ${gb(o.rollback_bytes)}.` : "";
    rows.push({ k: "Kept for rollback", count: plural(rb.count, "image"), v: gb(rb.bytes), bytes: rb.bytes,
      note: `The ${each} of ${list(names.slice(0, 3), names.length) || "the services here"}.${others} Each service sets its own number in Settings → Resources.` });
  }
  const bc = r.build_cache;
  rows.push({ k: "Build cache", count: "", v: gb(bc.bytes), bytes: bc.bytes,
    note: bc.over_limit_bytes > 0
      ? `${gb(bc.over_limit_bytes)} over the ${gb(bc.limit_bytes)} limit. Toft trims that after the next build or clean.`
      : `Under the ${gb(bc.limit_bytes)} limit. Toft trims it after each build, keeping what builds used last.` });
  if (r.volumes_in_use.count > 0) {
    rows.push({ k: "Volumes in use", count: plural(r.volumes_in_use.count, "volume"), v: gb(r.volumes_in_use.bytes), bytes: r.volumes_in_use.bytes,
      note: panel ? "Apps’ data, and the panel’s database." : "Apps’ data, and volumes Toft didn’t make." });
  }
  rows.push({ k: "Containers and logs", count: "", v: gb(r.containers_bytes), bytes: r.containers_bytes, note: "What containers wrote outside their volumes, and their logs." });
  return rows;
}

/** The bar: safe to delete, kept, everything else, as shares of the disk. */
export function bar(r: DiskReport) {
  const safe = safeRows(r).reduce((n, x) => n + x.bytes, 0);
  const kept = keptRows(r, false).reduce((n, x) => n + x.bytes, 0);
  const rest = Math.max(0, r.used_bytes - safe - kept);
  const pct = (n: number) => (r.size_bytes > 0 ? `${Math.min(100, (n / r.size_bytes) * 100).toFixed(1)}%` : "0%");
  return { safe, kept, rest, safePct: pct(safe), keptPct: pct(kept), restPct: pct(rest) };
}

/** A volume's line under its name. */
export function volumeNote(v: DiskVolume): string {
  if (v.kind === "deleted_service") return "Its service was deleted, and no container mounts it.";
  if (v.kind === "compose_orphan") return `From the compose project ${v.service_name}, which no service deploys any more.`;
  const made = v.created_at ? `, created ${formatDateTime(v.created_at).slice(0, 10)}` : "";
  return `Anonymous${made}. No container mounts it, and Toft can’t tell whose it was.`;
}

/** The status row: the clean running, or how the last one ended. */
export function cleanRow(c: DiskClean, now: number, where: string) {
  const who = c.automatic ? "Toft, automatically" : c.started_by || "someone no longer here";
  const kinds = c.kinds.map((k) => KIND_LABEL[k].toLowerCase());
  if (c.build_cache_all) kinds.push("the build cache");
  const at = formatDateTime(c.started_at);
  const secs = Math.max(1, Math.round(((c.finished_at ? new Date(c.finished_at).getTime() : now) - new Date(c.started_at).getTime()) / 1000));
  const took = secs < 120 ? `${secs} s` : `${Math.round(secs / 60)} min`;
  if (c.outcome === "running") {
    const step = c.step === "reading" ? "reading the disk" : c.step === "build_cache" ? "the build cache" : c.step === "done" ? "finishing" : KIND_LABEL[c.step].toLowerCase();
    return { tone: "running" as const, label: "Freeing space", state: `${gb(c.freed_bytes)} so far`,
      detail: `Started by ${who} at ${at.slice(11)}. Nothing restarts. Deploys to ${where} wait until it’s done.`,
      meta: `${step} · ${plural(c.removed, "image")} removed` };
  }
  const left = c.skipped > 0 ? ` ${plural(c.skipped, "image was", "images were")} left: a container started from them during the clean.` : "";
  if (c.outcome === "succeeded") {
    return { tone: "ok" as const, label: `Freed ${gb(c.freed_bytes)}`, state: `in ${took}`,
      detail: `By ${who}.${left}${c.build_cache_all ? "" : " The build cache was left as it was."}`,
      meta: `${at} · ${list(kinds)}` };
  }
  return { tone: "bad" as const, label: `Stopped after ${gb(c.freed_bytes)}`, state: c.error || "failed",
    detail: `By ${who} at ${at.slice(11)}. Each image is removed whole, so nothing is left half-deleted. Read again once the server answers.`,
    meta: `${at} · ${took} · ${plural(c.removed, "image")} removed` };
}

/** The policy, closed, in one line (the card's footer). */
export function policyLine(p: DiskPolicy): string {
  const e = p.effective;
  const auto = e.auto_clean.length
    ? `${list(POLICY_ORDER.filter((k) => e.auto_clean.includes(k)).map((k) => KIND_LABEL[k].toLowerCase()))} cleaned daily`
    : "nothing cleaned automatically";
  return `Policy: build cache ${gb(e.build_cache_limit_bytes)} · ${auto} · alert at ${e.alert_percent}%`;
}

/** The card head's note on the last automatic clean, when one freed space;
 *  a failed one is the status row's. */
export function lastAutoNote(r: DiskReport): string {
  const c = r.last_clean;
  if (!c || !c.automatic || c.outcome !== "succeeded") return "";
  return `cleaned automatically ${formatDateTime(c.finished_at ?? c.started_at)}, ${c.freed_bytes > 0 ? `freed ${gb(c.freed_bytes)}` : "nothing to free"}`;
}
