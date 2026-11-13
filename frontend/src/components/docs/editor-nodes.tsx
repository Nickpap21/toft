"use client";

import { createContext, useContext } from "react";
import { mergeAttributes, Node } from "@tiptap/core";
import { NodeViewContent, NodeViewWrapper, ReactNodeViewRenderer, type ReactNodeViewProps } from "@tiptap/react";
import { CircleDot, GitPullRequest, GripVertical, Info, Layers, Trash2 } from "lucide-react";
import type { WorkspaceService } from "@/lib/api/service.service";
import { statusLabel, statusTone, TONE_CLASSES } from "@/lib/status";
import { cn } from "@/lib/utils";

/** What the editor's blocks pick from: the project's services. */
export const EditorContext = createContext<{ services: WorkspaceService[]; project: string; isOperator: boolean }>({
  services: [],
  project: "",
  isOperator: false,
});

const KIND_LABEL: Record<string, string> = { service: "Live service", run: "Command", issue: "Issue", pull: "Pull request" };

/** A Toft block as the markdown fences it (D-86): its info string
 *  ("toft-run <service> host") and its text. Claims toft- fences before the
 *  code block does. */
export const ToftBlock = Node.create({
  name: "toftBlock",
  group: "block",
  atom: true,
  draggable: true,
  selectable: true,
  priority: 1000,
  addAttributes() {
    return {
      info: { default: "", parseHTML: (el) => el.getAttribute("data-info") ?? "", renderHTML: (a) => ({ "data-info": a.info }) },
      body: { default: "", parseHTML: (el) => el.getAttribute("data-body") ?? "", renderHTML: (a) => ({ "data-body": a.body }) },
    };
  },
  parseHTML() {
    return [{ tag: "div[data-toft-block]" }];
  },
  renderHTML({ HTMLAttributes }) {
    return ["div", mergeAttributes(HTMLAttributes, { "data-toft-block": "" })];
  },
  addNodeView() {
    return ReactNodeViewRenderer(BlockView);
  },
  markdownTokenName: "code",
  parseMarkdown: (token, h) => {
    const lang = String(token.lang ?? "").trim();
    if (!lang.startsWith("toft-") || !String(token.raw ?? "").startsWith("```")) return [];
    return h.createNode("toftBlock", { info: lang, body: String(token.text ?? "") });
  },
  renderMarkdown: (node) => {
    const body = String(node.attrs?.body ?? "").replace(/\n+$/, "");
    return "```" + node.attrs?.info + "\n" + (body ? body + "\n" : "") + "```";
  },
});

const NOTE = /^\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\]\s*/;

/** A note: a quote that starts with [!NOTE] (GitHub's alerts). */
export const Note = Node.create({
  name: "note",
  group: "block",
  content: "block+",
  defining: true,
  priority: 1000,
  addAttributes() {
    return { kind: { default: "note", parseHTML: (el) => el.getAttribute("data-note") ?? "note", renderHTML: (a) => ({ "data-note": a.kind }) } };
  },
  parseHTML() {
    return [{ tag: "aside[data-note]" }];
  },
  renderHTML({ HTMLAttributes }) {
    return ["aside", mergeAttributes(HTMLAttributes, { class: "toft-note" }), 0];
  },
  addNodeView() {
    return ReactNodeViewRenderer(NoteView);
  },
  markdownTokenName: "blockquote",
  parseMarkdown: (token, h) => {
    const tokens = [...(token.tokens ?? [])];
    const para = tokens[0];
    const first = para?.type === "paragraph" ? para.tokens?.[0] : undefined;
    const m = first?.type === "text" ? NOTE.exec(String(first.text ?? "")) : null;
    if (!para || !first || !m) return [];
    const rest = String(first.text).slice(m[0].length);
    const inline = rest ? [{ ...first, text: rest, raw: rest }, ...(para.tokens ?? []).slice(1)] : (para.tokens ?? []).slice(1);
    if (inline.length > 0) tokens[0] = { ...para, tokens: inline };
    else tokens.shift();
    const parse = h.parseBlockChildren ?? h.parseChildren;
    const content = parse(tokens);
    return h.createNode("note", { kind: m[1].toLowerCase() }, content.length ? content : [h.createNode("paragraph")]);
  },
  renderMarkdown: (node, h) => {
    const inner = h.renderChildren(node.content ?? [], "\n\n");
    const lines = inner.split("\n").map((l: string) => (l.trim() ? `> ${l}` : ">"));
    return [`> [!${String(node.attrs?.kind ?? "note").toUpperCase()}]`, ...lines].join("\n");
  },
});

function NoteView() {
  return (
    <NodeViewWrapper as="aside" className="toft-note my-0 flex gap-2.5 rounded-lg border border-subtle bg-surface px-3.5 py-2.5 text-sm leading-[22px]">
      <span contentEditable={false} className="mt-[3px] flex-none">
        <Info aria-hidden="true" className="h-4 w-4 text-status-building" strokeWidth={2} />
      </span>
      <NodeViewContent className="min-w-0 flex-1" />
    </NodeViewWrapper>
  );
}

/** The block's parts, from its info string. */
export function parseInfo(info: string): { kind: string; arg: string; host: boolean } {
  const f = info.trim().split(/\s+/);
  return { kind: (f[0] ?? "").replace(/^toft-/, ""), arg: f[1] ?? "", host: f[2] === "host" };
}

function BlockView({ node, updateAttributes, deleteNode, selected }: ReactNodeViewProps) {
  const { services, project, isOperator } = useContext(EditorContext);
  const info = String(node.attrs.info);
  const body = String(node.attrs.body);
  const { kind, arg, host } = parseInfo(info);
  const svc = services.find((s) => s.id === arg);
  const label = (s: WorkspaceService) => `${project} / ${s.name}${s.environment_name ? ` · ${s.environment_name}` : ""}`;
  const pick = (
    <select
      aria-label={kind === "run" ? "Service the command runs in" : "Service shown in this block"}
      value={arg}
      onChange={(e) => updateAttributes({ info: `toft-${kind} ${e.target.value}${host ? " host" : ""}` })}
      className="touch-target h-7 max-w-[220px] rounded-md border border-strong bg-raised px-1.5 text-xs text-fg"
    >
      {!svc && <option value={arg}>Choose a service</option>}
      {services.map((s) => (
        <option key={s.id} value={s.id}>
          {label(s)}
        </option>
      ))}
    </select>
  );
  const header = (
    <div contentEditable={false} className="flex flex-wrap items-center gap-2 border-b border-subtle bg-raised/60 py-1 pl-2 pr-1 text-xs text-muted">
      <GripVertical data-drag-handle aria-hidden="true" className="h-3 w-3 cursor-grab" strokeWidth={3} />
      {kind === "run" ? (
        <>
          <span className="font-mono">sh</span>
          <span aria-hidden="true">·</span>
          <label className="flex items-center gap-2">
            {host ? "Runs on the server of" : "Runs inside"}
            {pick}
          </label>
          {isOperator && (
            <label className="flex items-center gap-1.5">
              <input
                type="checkbox"
                checked={host}
                onChange={(e) => updateAttributes({ info: `toft-run ${arg}${e.target.checked ? " host" : ""}` })}
                className="h-3.5 w-3.5 accent-[var(--accent)]"
              />
              On the server
            </label>
          )}
        </>
      ) : (
        <>
          <span>{KIND_LABEL[kind] ?? info}</span>
          {kind === "service" && pick}
          {(kind === "issue" || kind === "pull") && (
            <input
              aria-label={kind === "issue" ? "Issue key" : "Pull request, as repository!number"}
              value={arg}
              placeholder={kind === "issue" ? "STO-12" : "repository!12"}
              onChange={(e) => updateAttributes({ info: `toft-${kind} ${e.target.value.trim()}` })}
              className="touch-target h-7 w-40 rounded-md border border-strong bg-raised px-2 font-mono text-xs text-fg"
            />
          )}
        </>
      )}
      <span className="flex-1" />
      <button
        type="button"
        aria-label={`Remove ${(KIND_LABEL[kind] ?? "this").toLowerCase()} block`}
        onClick={() => deleteNode()}
        className="flex h-7 w-7 items-center justify-center rounded-md text-muted hover:bg-raised hover:text-fg pointer-coarse:h-11 pointer-coarse:w-11"
      >
        <Trash2 className="h-[13px] w-[13px]" strokeWidth={2} />
      </button>
    </div>
  );

  return (
    <NodeViewWrapper
      data-drag-handle=""
      className={cn("my-0 overflow-hidden rounded-lg border bg-surface", selected ? "border-accent" : "border-subtle", kind === "run" && "bg-ground")}
    >
      {header}
      {kind === "service" && (
        <div contentEditable={false}>
          <div className="flex flex-wrap items-center gap-3.5 px-3.5 py-3">
            <span aria-hidden="true" className="flex h-8 w-8 items-center justify-center rounded-lg bg-raised text-fg/75">
              <Layers className="h-4 w-4" strokeWidth={1.8} />
            </span>
            {svc ? (
              <span className="flex flex-col gap-0.5">
                <span className="flex flex-wrap items-center gap-2">
                  <span className="font-semibold text-fg">{svc.name}</span>
                  <span className={cn("inline-flex items-center gap-1.5 text-xs font-medium", TONE_CLASSES[statusTone(svc.runtime_status)].text)}>
                    <span aria-hidden="true" className={cn("h-1.5 w-1.5 rounded-full", TONE_CLASSES[statusTone(svc.runtime_status)].dot)} />
                    {statusLabel(svc.runtime_status)}
                  </span>
                  <span className="rounded border border-strong px-1.5 text-[11px] leading-4 text-muted">live</span>
                </span>
                <span className="font-mono text-xs text-fg/70">{svc.docker_image || svc.git_branch || svc.source_type}</span>
              </span>
            ) : (
              <span className="text-sm text-muted">Choose the service readers see here.</span>
            )}
          </div>
          <p className="px-3.5 pb-2.5 text-xs text-muted">
            Readers see this as it is now. Linking {svc?.name ?? "a service"} also makes this page warn readers when it changes after the page was last verified.
          </p>
        </div>
      )}
      {kind === "run" && (
        <>
          <textarea
            aria-label="Command"
            value={body}
            rows={Math.max(1, body.split("\n").length)}
            spellCheck={false}
            onChange={(e) => updateAttributes({ body: e.target.value })}
            onKeyDown={(e) => {
              // The editor ignores keys typed here; ⌘S still saves.
              if (!((e.metaKey || e.ctrlKey) && e.key === "s")) e.stopPropagation();
            }}
            placeholder="wget -qO- http://localhost:8080/healthz"
            className="block w-full resize-none bg-transparent px-3.5 py-3 font-mono text-[13px] leading-[21px] text-fg/90 outline-none placeholder:text-muted"
          />
          <p contentEditable={false} className="border-t border-subtle px-3.5 pb-2.5 pt-2 text-xs leading-[18px] text-muted">
            {host
              ? "Instance operators run this on the server, after confirming their password. Everyone else can copy it."
              : `Readers run this with one click, inside the running ${svc?.name ?? ""} container. Commands on the server itself are for instance operators.`}
          </p>
        </>
      )}
      {(kind === "issue" || kind === "pull") && (
        <div contentEditable={false} className="flex items-center gap-2.5 px-3.5 py-3 text-sm text-muted">
          {kind === "issue" ? <CircleDot aria-hidden="true" className="h-4 w-4" /> : <GitPullRequest aria-hidden="true" className="h-4 w-4" />}
          Readers see its title and status as it moves.
        </div>
      )}
    </NodeViewWrapper>
  );
}
