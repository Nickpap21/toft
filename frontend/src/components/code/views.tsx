"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { ChevronDown, ChevronRight } from "lucide-react";
import type { ThemedToken } from "shiki/core";
import type { GitFileDiff, repoService } from "@/lib/api/code.service";
import { cn } from "@/lib/utils";
import { languageFor, tokenize } from "./highlight";
import { codeHref, useCode } from "./repo";

type Api = ReturnType<typeof repoService>;

/** A file's lines with their numbers (each a #L<n> link), highlighted once
 *  its language has loaded (CodeFile). */
export function CodeLines({ text, path }: { text: string; path: string }) {
  const lines = (text.endsWith("\n") ? text.slice(0, -1) : text).split("\n");
  const [tokens, setTokens] = useState<{ text: string; lines: ThemedToken[][] } | null>(null);
  useEffect(() => {
    let live = true;
    tokenize(text, languageFor(path)).then(
      (t) => live && t && setTokens({ text, lines: t }),
      () => {}, // plain text it is
    );
    return () => {
      live = false;
    };
  }, [text, path]);
  const lit = tokens?.text === text ? tokens.lines : null;
  return (
    <div role="group" tabIndex={0} aria-label="File contents" className="focus-inset overflow-x-auto py-2">
      <table className="border-collapse font-mono text-[13px] leading-5">
        <tbody>
          {lines.map((line, i) => (
            <tr key={i} id={`L${i + 1}`} className="target:bg-status-pending-soft">
              <td className="w-[1%] min-w-12 select-none pl-4 pr-3 text-right align-top text-fg/70">
                <a href={`#L${i + 1}`} className="hover:text-fg" tabIndex={-1}>
                  {i + 1}
                </a>
              </td>
              <td className="whitespace-pre pl-1 pr-6 text-fg">
                {lit?.[i]
                  ? lit[i].map((t, j) => (
                      <span key={j} style={{ color: t.color, fontStyle: t.fontStyle === 1 ? "italic" : undefined }}>
                        {t.content}
                      </span>
                    ))
                  : line || " "}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

const IMAGE_TYPES = ["image/png", "image/jpeg", "image/gif", "image/webp"];
const MAX_IMAGE = 5 << 20;

/** A raster image from the repository as a data: URL, never a document of
 *  the panel's origin (D-73); null for anything else. */
export async function imageURL(api: Api, ref: string, path: string): Promise<string | null> {
  const blob = await api.raw(ref, path);
  if (!IMAGE_TYPES.includes(blob.type) || blob.size > MAX_IMAGE) return null;
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(r.result as string);
    r.onerror = () => reject(r.error);
    r.readAsDataURL(blob);
  });
}

/** Markdown the server rendered and sanitised (gitstore.Markdown): links
 *  and images within the repository (data-toft-path) point at the Code
 *  pages and load as data: URLs. */
export function Markdown({ html, refName }: { html: string; refName: string }) {
  const { base, api } = useCode();
  const el = useRef<HTMLDivElement>(null);
  const router = useRouter();
  useEffect(() => {
    const root = el.current;
    if (!root) return;
    let live = true;
    root.querySelectorAll<HTMLAnchorElement>("a[data-toft-path]").forEach((a) => {
      const p = a.dataset.toftPath ?? "";
      a.href = p && a.dataset.toftDir === undefined ? codeHref(base, "blob", refName, p) : codeHref(base, "", refName, p);
    });
    // The project's issue keys and this repository's pull requests (D-81).
    const project = base.split("/code/")[0];
    root.querySelectorAll<HTMLAnchorElement>("a[data-toft-issue]").forEach((a) => {
      a.href = `${project}/issues/${a.dataset.toftIssue}`;
    });
    root.querySelectorAll<HTMLAnchorElement>("a[data-toft-pull]").forEach((a) => {
      a.href = `${base}/pulls/${a.dataset.toftPull}`;
    });
    root.querySelectorAll<HTMLImageElement>("img[data-toft-path]").forEach((img) => {
      const p = img.dataset.toftPath;
      if (!p) return;
      imageURL(api, refName, p).then(
        (src) => live && src && (img.src = src),
        () => {},
      );
    });
    return () => {
      live = false;
    };
  }, [html, base, api, refName]);
  return (
    <div
      ref={el}
      className="toft-md"
      onClick={(e) => {
        // Links within the repository stay in the app.
        const a = (e.target as HTMLElement).closest("a[data-toft-path], a[data-toft-issue], a[data-toft-pull]");
        if (a instanceof HTMLAnchorElement && !e.metaKey && !e.ctrlKey && !e.shiftKey && e.button === 0) {
          e.preventDefault();
          router.push(a.pathname + a.search);
        }
      }}
      dangerouslySetInnerHTML={{ __html: html }}
    />
  );
}

export interface DiffRow {
  kind: "hunk" | "context" | "add" | "del" | "note";
  old?: number;
  new?: number;
  text: string;
}

/** The rows of a unified patch (from its first @@ line), with old and new
 *  line numbers. */
export function parsePatch(patch: string): DiffRow[] {
  const rows: DiffRow[] = [];
  let o = 0;
  let n = 0;
  const lines = patch.endsWith("\n") ? patch.slice(0, -1).split("\n") : patch.split("\n");
  for (const line of lines) {
    const h = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(line);
    if (h) {
      o = Number(h[1]);
      n = Number(h[2]);
      rows.push({ kind: "hunk", text: line });
    } else if (line.startsWith("+")) rows.push({ kind: "add", new: n++, text: line.slice(1) });
    else if (line.startsWith("-")) rows.push({ kind: "del", old: o++, text: line.slice(1) });
    else if (line.startsWith("\\")) rows.push({ kind: "note", text: line.slice(2) });
    else rows.push({ kind: "context", old: o++, new: n++, text: line.slice(1) });
  }
  return rows;
}

const rowBg: Record<DiffRow["kind"], string> = {
  hunk: "bg-status-building-soft",
  context: "",
  add: "bg-status-running-soft",
  del: "bg-status-failed-soft",
  note: "",
};
const sign: Record<DiffRow["kind"], string> = { hunk: "", context: " ", add: "+", del: "−", note: "" };

/** A file's patch, unified, with both line numbers (CodeCommit). */
export function DiffView({ patch, path }: { patch: string; path: string }) {
  const rows = parsePatch(patch);
  return (
    <div role="group" tabIndex={0} aria-label={`Changes in ${path}`} className="focus-inset overflow-x-auto">
      <table className="w-full border-collapse font-mono text-[13px] leading-5">
        <tbody>
          {rows.map((r, i) => (
            <tr key={i} className={rowBg[r.kind]}>
              <td className="w-[1%] min-w-10 select-none px-2 text-right text-fg/70">{r.old ?? ""}</td>
              <td className="w-[1%] min-w-10 select-none px-2 text-right text-fg/70">{r.new ?? ""}</td>
              <td
                aria-hidden={r.kind === "context" ? true : undefined}
                className={cn(
                  "w-[1%] select-none pl-2 pr-1",
                  r.kind === "add" && "text-status-running-fg",
                  r.kind === "del" && "text-status-failed-fg",
                )}
              >
                {sign[r.kind]}
              </td>
              <td
                className={cn(
                  "whitespace-pre pr-4",
                  r.kind === "hunk" ? "text-status-building-fg" : r.kind === "note" ? "italic text-muted" : r.kind === "context" ? "text-fg/80" : "text-fg",
                )}
              >
                {r.text || " "}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

const STATUS: Record<string, { label: string; tone: string }> = {
  added: { label: "Added", tone: "bg-status-running-soft text-status-running-fg" },
  modified: { label: "Modified", tone: "bg-status-building-soft text-status-building-fg" },
  deleted: { label: "Deleted", tone: "bg-status-failed-soft text-status-failed-fg" },
  renamed: { label: "Renamed", tone: "bg-status-pending-soft text-status-pending-fg" },
  copied: { label: "Copied", tone: "bg-status-pending-soft text-status-pending-fg" },
  type_changed: { label: "Type changed", tone: "bg-status-pending-soft text-status-pending-fg" },
};

/** One changed file with its patch, which folds (CodeCommit,
 *  PullRequestChanges); view links to the file. */
export function DiffFile({ f, view, open: initiallyOpen = true }: { f: GitFileDiff; view?: string; open?: boolean }) {
  const [open, setOpen] = useState(initiallyOpen);
  const s = STATUS[f.status] ?? STATUS.modified;
  return (
    <section aria-label={f.path} className="overflow-hidden rounded-[10px] border border-subtle bg-surface">
      <div className={cn("flex flex-wrap items-center gap-2.5 bg-raised/50 px-3 py-[9px] text-[13px]", open && "border-b border-subtle")}>
        <button
          type="button"
          aria-expanded={open}
          aria-label={`${open ? "Hide" : "Show"} ${f.path}`}
          onClick={() => setOpen(!open)}
          className="touch-target flex h-6 w-6 items-center justify-center rounded-sm text-fg/70 hover:bg-raised hover:text-fg pointer-coarse:w-11"
        >
          {open ? <ChevronDown className="h-3.5 w-3.5" aria-hidden="true" /> : <ChevronRight className="h-3.5 w-3.5" aria-hidden="true" />}
        </button>
        <span className={cn("inline-flex h-5 items-center rounded-sm px-[7px] text-[11px] font-medium", s.tone)}>{s.label}</span>
        <span className="min-w-0 flex-[1_1_260px] break-all font-mono text-[13px] text-fg">{f.old_path ? `${f.old_path} → ${f.path}` : f.path}</span>
        <span className="font-mono text-xs">
          <span className="text-status-running-fg">+{f.additions}</span> <span className="text-status-failed-fg">−{f.deletions}</span>
        </span>
        {view && (
          <Link href={view} className="text-xs text-link hover:underline">
            View file
          </Link>
        )}
      </div>
      {open &&
        (f.binary ? (
          <p className="px-4 py-3.5 text-[13px] text-fg/70">Binary file, not shown.</p>
        ) : f.patch ? (
          <>
            <DiffView patch={f.patch} path={f.path} />
            {f.patch_truncated && <p className="border-t border-subtle px-4 py-2.5 text-xs text-muted">The diff is cut at 512 KB.</p>}
          </>
        ) : (
          <p className="px-4 py-3.5 text-[13px] text-fg/70">{f.status === "renamed" ? "Renamed without changes." : "No changes to its content."}</p>
        ))}
    </section>
  );
}
