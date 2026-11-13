"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { EditorContent, useEditor, type Editor } from "@tiptap/react";
import StarterKit from "@tiptap/starter-kit";
import { Markdown } from "@tiptap/markdown";
import { TaskItem, TaskList } from "@tiptap/extension-list";
import { Placeholder } from "@tiptap/extensions";
import {
  Bold, ChevronDown, CircleDot, Code, FileText, GitPullRequest, Heading, Info, Italic, Layers, Link2, List, ListChecks, ListOrdered,
  SquareTerminal, Braces,
} from "lucide-react";
import { useIsOperator } from "@/context/AuthContext";
import { useCanWriteIssues } from "@/components/issues/issue-detail";
import { ApiError, notFound } from "@/lib/api/client";
import { Read } from "@/components/shell/unreachable";
import { docPageQuery, docService, docTreeQuery, type DocPage, type DocTreePage } from "@/lib/api/doc.service";
import { projectQuery, workspaceServicesQuery } from "@/lib/queries";
import { ago } from "@/lib/time";
import { cn } from "@/lib/utils";
import { buildTree, type TreeNode } from "./body";
import { EditorContext, Note, parseInfo, ToftBlock } from "./editor-nodes";

/** Editing a page, or writing a new one under parentId (board: DocsEditor). */
export function DocEditor({ projectId, pageId, parentId }: { projectId: string; pageId?: string; parentId?: string }) {
  const read = useQuery({ ...docPageQuery(projectId, pageId ?? ""), enabled: !!pageId });
  const canWrite = useCanWriteIssues();
  if (!canWrite) return <p className="px-4 py-10 text-sm text-muted md:px-10">Viewers read the docs; owners and developers write them.</p>;
  if (!pageId) return <Editing key="new" projectId={projectId} parentId={parentId} />;
  if (!read.data && notFound(read.error)) return <p className="px-4 py-10 text-sm text-muted md:px-10">This page doesn’t exist, or isn’t this project’s.</p>;
  return (
    <div className={read.data ? "contents" : "min-w-0 flex-1 px-4 py-10 md:px-10"}>
      <Read query={read} what="this page" heading="h1" loading={<div role="status" aria-label="Loading this page" className="h-64 rounded-[10px] bg-surface motion-safe:animate-pulse" />}>
        {(page) => <Editing key={page.id} projectId={projectId} page={page} parentId={parentId} />}
      </Read>
    </div>
  );
}

type SlashItem = { id: string; label: string; detail?: string; icon: React.ComponentType<{ className?: string; strokeWidth?: number }>; tint?: string };

const SLASH_TOFT: SlashItem[] = [
  { id: "service", label: "Live service", detail: "Status, commit and server of a service, kept current", icon: Layers, tint: "text-link" },
  { id: "run", label: "Command", detail: "Readers run it inside one of the project’s services", icon: SquareTerminal, tint: "text-status-running-fg" },
  { id: "issue", label: "Issue", detail: "Shows its status as it moves", icon: CircleDot, tint: "text-status-building-fg" },
  { id: "pull", label: "Pull request", detail: "Shows checks and whether it merged", icon: GitPullRequest, tint: "text-env-violet-fg" },
  { id: "page", label: "Page", detail: "Link another page in these docs", icon: FileText, tint: "text-fg/70" },
];
const SLASH_TEXT: SlashItem[] = [
  { id: "heading", label: "Heading", icon: Heading },
  { id: "bullet", label: "Bulleted list", icon: List },
  { id: "ordered", label: "Numbered list", icon: ListOrdered },
  { id: "tasks", label: "Checklist", icon: ListChecks },
  { id: "code", label: "Code block", icon: Braces },
  { id: "note", label: "Note", icon: Info },
];
const SLASH = [...SLASH_TOFT, ...SLASH_TEXT];

function Editing({ projectId, page, parentId }: { projectId: string; page?: DocPage; parentId?: string }) {
  const router = useRouter();
  const qc = useQueryClient();
  const isOperator = useIsOperator();
  const [now] = useState(() => Date.now());
  const { data: project } = useQuery(projectQuery(projectId));
  const { data: workspace } = useQuery(workspaceServicesQuery);
  const { data: tree } = useQuery(docTreeQuery(projectId));
  const services = useMemo(() => (workspace ?? []).filter((s) => s.project_id === projectId), [workspace, projectId]);
  const [title, setTitle] = useState(page?.title ?? "");
  const [dirty, setDirty] = useState(false);
  const [outline, setOutline] = useState<{ text: string; linked: string[] }>({ text: "", linked: [] });
  type Menu = { top: number; left: number; index: number } | null;
  const [slash, setSlashState] = useState<Menu>(null);
  // The editor's key handler reads the menu outside React's render.
  const slashRef = useRef<Menu>(null);
  const setSlash = (m: Menu) => {
    slashRef.current = m;
    setSlashState(m);
  };
  const [linkAt, setLinkAt] = useState<string | null>(null);
  const [pickPage, setPickPage] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const firstService = services[0]?.id ?? "";
  const base = `/projects/${projectId}/docs`;

  const editor = useEditor({
    immediatelyRender: false,
    extensions: [
      StarterKit.configure({
        heading: { levels: [2, 3] },
        link: { openOnClick: false, autolink: true, isAllowedUri: (url, ctx) => url.startsWith("page:") || ctx.defaultValidate(url) },
      }),
      TaskList,
      TaskItem.configure({ nested: true }),
      Placeholder.configure({ placeholder: "Write, or type / for a block" }),
      ToftBlock,
      Note,
      Markdown,
    ],
    content: page?.body ?? "",
    contentType: "markdown",
    editorProps: {
      attributes: { class: "toft-md toft-doc toft-editor min-h-[240px] outline-none", "aria-label": "Page text" },
      handleKeyDown: (_view, e) => {
        const s = slashRef.current;
        if (!s) return false;
        if (e.key === "ArrowDown" || e.key === "ArrowUp") {
          const d = e.key === "ArrowDown" ? 1 : -1;
          setSlash({ ...s, index: (s.index + d + SLASH.length) % SLASH.length });
          return true;
        }
        if (e.key === "Enter") {
          choose(SLASH[s.index].id);
          return true;
        }
        if (e.key === "Escape") {
          setSlash(null);
          return true;
        }
        return false;
      },
    },
    onUpdate: ({ editor }) => {
      setDirty(true);
      watch(editor);
    },
    onSelectionUpdate: ({ editor }) => watch(editor),
    onCreate: ({ editor }) => describe(editor),
  });

  // The slash menu opens on a paragraph holding only "/".
  function watch(ed: Editor) {
    describe(ed);
    const { $from, empty } = ed.state.selection;
    if (empty && $from.parent.type.name === "paragraph" && $from.parent.textContent === "/") {
      const c = ed.view.coordsAtPos($from.pos);
      if (!slashRef.current) setSlash({ top: c.bottom + 6, left: Math.min(c.left, window.innerWidth - 356), index: 0 });
    } else if (slashRef.current) {
      setSlash(null);
    }
  }

  // The rail's outline and links, from the document.
  function describe(ed: Editor) {
    const heads: string[] = [];
    const linked = new Set<string>();
    ed.state.doc.descendants((n) => {
      if (n.type.name === "heading" && n.attrs.level === 2) heads.push(n.textContent);
      if (n.type.name === "toftBlock") {
        const { kind, arg } = parseInfo(String(n.attrs.info));
        if ((kind === "service" || kind === "run") && arg) linked.add(arg);
      }
    });
    setOutline({ text: heads.join("\n"), linked: [...linked] });
  }

  function choose(id: string) {
    if (!editor) return;
    setSlash(null);
    const { $from } = editor.state.selection;
    const chain = editor.chain().focus().deleteRange({ from: $from.start(), to: $from.end() });
    switch (id) {
      case "service":
        chain.insertContent({ type: "toftBlock", attrs: { info: `toft-service ${firstService}`, body: "" } }).run();
        break;
      case "run":
        chain.insertContent({ type: "toftBlock", attrs: { info: `toft-run ${firstService}`, body: "" } }).run();
        break;
      case "issue":
      case "pull":
        chain.insertContent({ type: "toftBlock", attrs: { info: `toft-${id} `, body: "" } }).run();
        break;
      case "page":
        chain.run();
        setPickPage(true);
        break;
      case "heading":
        chain.setNode("heading", { level: 2 }).run();
        break;
      case "bullet":
        chain.toggleBulletList().run();
        break;
      case "ordered":
        chain.toggleOrderedList().run();
        break;
      case "tasks":
        chain.toggleTaskList().run();
        break;
      case "code":
        chain.toggleCodeBlock().run();
        break;
      case "note":
        chain.insertContent({ type: "note", attrs: { kind: "note" }, content: [{ type: "paragraph" }] }).run();
        break;
    }
  }

  const insertBlock = (kind: string) => {
    if (!editor) return;
    const info = kind === "service" || kind === "run" ? `toft-${kind} ${firstService}` : `toft-${kind} `;
    editor.chain().focus().insertContent({ type: "toftBlock", attrs: { info, body: "" } }).run();
  };

  const save = useMutation({
    mutationFn: async () => {
      const body = editor?.getMarkdown() ?? "";
      const svc = docService(projectId);
      if (page) return svc.update(page.id, { title, body, base_version: page.version });
      return svc.create({ title, body, parent_id: parentId });
    },
    onSuccess: (p) => {
      setDirty(false);
      qc.setQueryData(docPageQuery(projectId, p.id).queryKey, p);
      qc.invalidateQueries({ queryKey: ["docs", projectId, "tree"] });
      qc.invalidateQueries({ queryKey: projectQuery(projectId).queryKey });
      router.push(`${base}/${p.id}`);
    },
    onError: (err) => {
      const code = err instanceof ApiError ? (err.body as { data?: { code?: string } } | null)?.data?.code : undefined;
      if (code === "version_conflict") {
        toast.error("Someone saved this page while you edited it. Copy what you wrote, then reload the page to see theirs.", { duration: 10_000 });
      } else {
        toast.error(err instanceof ApiError ? err.message : "Couldn’t save the page");
      }
    },
  });
  const saveRef = useRef(save);
  useEffect(() => {
    saveRef.current = save;
  });

  const move = useMutation({
    mutationFn: (parent: string) => docService(projectId).update(page!.id, { parent_id: parent }),
    onSuccess: (p) => {
      qc.setQueryData(docPageQuery(projectId, p.id).queryKey, p);
      qc.invalidateQueries({ queryKey: ["docs", projectId, "tree"] });
      toast.success("Moved");
    },
    onError: (err) => toast.error(err instanceof ApiError ? err.message : "Couldn’t move the page"),
  });
  const remove = useMutation({
    mutationFn: () => docService(projectId).remove(page!.id),
    onSuccess: () => {
      setDirty(false);
      qc.invalidateQueries({ queryKey: ["docs", projectId] });
      qc.invalidateQueries({ queryKey: projectQuery(projectId).queryKey });
      toast.success("Deleted. It can be restored from Deleted pages for 30 days.");
      router.push(base);
    },
    onError: (err) => toast.error(err instanceof ApiError ? err.message : "Couldn’t delete the page"),
  });

  // ⌘S saves; leaving with unsaved changes asks first.
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "s") {
        e.preventDefault();
        if (!saveRef.current.isPending) saveRef.current.mutate();
      }
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, []);
  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  const exclude = new Set<string>();
  if (page && tree) {
    const walk = (n: TreeNode) => {
      exclude.add(n.page.id);
      n.children.forEach(walk);
    };
    const find = (ns: TreeNode[]): TreeNode | undefined => ns.find((n) => n.page.id === page.id) ?? ns.map((n) => find(n.children)).find(Boolean);
    const self = find(buildTree(tree));
    if (self) walk(self);
  }
  const parents = (tree ?? []).filter((p) => !exclude.has(p.id));
  const linked = outline.linked.map((id) => services.find((s) => s.id === id)?.name ?? "a missing service");
  const heads = outline.text ? outline.text.split("\n") : [];
  const name = project?.name ?? "";
  const valid = title.trim() !== "";

  const fmt: { label: string; icon: React.ComponentType<{ className?: string; strokeWidth?: number }>; on: () => void; active?: boolean }[] = editor
    ? [
        { label: "Bold", icon: Bold, on: () => editor.chain().focus().toggleBold().run(), active: editor.isActive("bold") },
        { label: "Italic", icon: Italic, on: () => editor.chain().focus().toggleItalic().run(), active: editor.isActive("italic") },
        { label: "Code", icon: Code, on: () => editor.chain().focus().toggleCode().run(), active: editor.isActive("code") },
        { label: "Link", icon: Link2, on: () => setLinkAt(String(editor.getAttributes("link").href ?? "")), active: editor.isActive("link") },
        { label: "Bulleted list", icon: List, on: () => editor.chain().focus().toggleBulletList().run(), active: editor.isActive("bulletList") },
        { label: "Numbered list", icon: ListOrdered, on: () => editor.chain().focus().toggleOrderedList().run(), active: editor.isActive("orderedList") },
        { label: "Checklist", icon: ListChecks, on: () => editor.chain().focus().toggleTaskList().run(), active: editor.isActive("taskList") },
      ]
    : [];
  const style = editor?.isActive("heading", { level: 2 }) ? "h2" : editor?.isActive("heading", { level: 3 }) ? "h3" : "p";

  return (
    <EditorContext.Provider value={{ services, project: name, isOperator }}>
      <div className="flex min-w-0 flex-1 flex-wrap items-start">
        <article aria-label={page ? `Editing ${page.title}` : "A new page"} className="flex min-w-0 max-w-[800px] flex-[999_1_520px] flex-col gap-4 px-4 pb-16 pt-5 md:px-10">
          <div className="sticky top-0 z-10 -mx-4 flex flex-wrap items-center gap-2.5 bg-ground px-4 py-1 text-[13px] text-muted md:-mx-10 md:px-10">
            <span className="[overflow-wrap:anywhere]">{page ? page.path.map((p) => `${p.title} / `).join("") || "Docs /" : (tree?.find((p) => p.id === parentId)?.title ?? "Docs") + " /"}</span>
            <span className="flex-1" />
            {dirty ? (
              <span className="inline-flex items-center gap-1.5 text-fg/70">
                <span aria-hidden="true" className="h-1.5 w-1.5 rounded-full bg-status-pending" />
                {page ? `Unsaved changes since version ${page.version} (${page.updated_by?.name ?? "someone"}, ${ago(page.updated_at, now)})` : "Not saved yet"}
              </span>
            ) : (
              page && <span>Version {page.version}</span>
            )}
            <Link href={page ? `${base}/${page.id}` : base} className="touch-target inline-flex h-8 items-center rounded-lg px-2.5 text-fg/70 hover:bg-raised hover:text-fg">
              {dirty ? "Discard" : "Close"}
            </Link>
            <button
              type="button"
              onClick={() => save.mutate()}
              disabled={!valid || save.isPending || (!dirty && !!page && title === page.title)}
              className="touch-target inline-flex h-8 items-center rounded-lg bg-accent px-3.5 font-medium text-on-accent disabled:opacity-50"
            >
              Save
            </button>
          </div>

          <div>
            <label htmlFor="doc-title" className="sr-only">
              Page title
            </label>
            <textarea
              id="doc-title"
              value={title}
              rows={1}
              onChange={(e) => {
                setTitle(e.target.value.replace(/\n/g, " "));
                setDirty(true);
              }}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  editor?.commands.focus("start");
                }
              }}
              placeholder="Untitled"
              maxLength={200}
              className="w-full resize-none bg-transparent text-[28px] font-semibold leading-9 tracking-[-0.015em] text-fg outline-none [field-sizing:content] placeholder:text-muted"
            />
          </div>

          <div role="group" aria-label="Formatting" className="flex flex-wrap items-center gap-0.5 rounded-lg border border-subtle bg-surface p-1">
            <label className="touch-target relative inline-flex h-[30px] items-center rounded-md hover:bg-raised">
              <span className="sr-only">Text style</span>
              <select
                value={style}
                onChange={(e) => {
                  const v = e.target.value;
                  if (!editor) return;
                  if (v === "p") editor.chain().focus().setParagraph().run();
                  else editor.chain().focus().setHeading({ level: v === "h2" ? 2 : 3 }).run();
                }}
                className="touch-target h-[30px] appearance-none bg-transparent pl-2 pr-6 text-[13px] text-fg outline-none"
              >
                <option value="p">Paragraph</option>
                <option value="h2">Heading</option>
                <option value="h3">Subheading</option>
              </select>
              <ChevronDown aria-hidden="true" className="pointer-events-none absolute right-1.5 h-3 w-3 text-muted" strokeWidth={2} />
            </label>
            <span aria-hidden="true" className="mx-[3px] h-[18px] w-px bg-[var(--border)]" />
            {fmt.map((b) => (
              <button
                key={b.label}
                type="button"
                aria-label={b.label}
                aria-pressed={b.active}
                onClick={b.on}
                className={cn("touch-target flex h-[30px] w-7 items-center justify-center rounded-md hover:bg-raised hover:text-fg pointer-coarse:w-11", b.active ? "bg-raised text-fg" : "text-fg/70")}
              >
                <b.icon className="h-[15px] w-[15px]" strokeWidth={2} />
              </button>
            ))}
            <span aria-hidden="true" className="mx-[3px] h-[18px] w-px bg-[var(--border)]" />
            {[
              { kind: "service", label: "Service", icon: Layers },
              { kind: "run", label: "Command", icon: SquareTerminal },
              { kind: "issue", label: "Issue", icon: CircleDot },
            ].map((b) => (
              <button
                key={b.kind}
                type="button"
                aria-label={`Insert ${b.label.toLowerCase()} block`}
                onClick={() => insertBlock(b.kind)}
                className="touch-target inline-flex h-[30px] items-center gap-1.5 rounded-md px-[7px] text-[13px] text-fg hover:bg-raised"
              >
                <b.icon className="h-3.5 w-3.5 text-fg/70" strokeWidth={1.9} />
                {b.label}
              </button>
            ))}
          </div>

          {linkAt !== null && (
            <form
              className="flex flex-wrap items-center gap-2 rounded-lg border border-subtle bg-surface p-2"
              onSubmit={(e) => {
                e.preventDefault();
                const href = linkAt.trim();
                const c = editor?.chain().focus().extendMarkRange("link");
                if (href) c?.setLink({ href }).run();
                else c?.unsetLink().run();
                setLinkAt(null);
              }}
            >
              <label htmlFor="doc-link" className="text-xs text-muted">
                Link to
              </label>
              <input
                id="doc-link"
                autoFocus
                value={linkAt}
                onChange={(e) => setLinkAt(e.target.value)}
                placeholder="https://… (empty removes it)"
                className="touch-target h-8 min-w-0 flex-1 rounded-md border border-strong bg-raised px-2 text-[13px] text-fg"
              />
              <button type="submit" className="touch-target h-8 rounded-md bg-accent px-3 text-[13px] font-medium text-on-accent">
                Apply
              </button>
              <button type="button" onClick={() => setLinkAt(null)} className="touch-target h-8 rounded-md px-2 text-[13px] text-fg/70 hover:bg-raised">
                Cancel
              </button>
            </form>
          )}

          <EditorContent editor={editor} />

          {slash && (
            <div
              role="listbox"
              aria-label="Insert a block"
              style={{ top: slash.top, left: Math.max(8, slash.left) }}
              className="fixed z-50 flex w-[340px] max-w-[calc(100vw-16px)] flex-col overflow-hidden rounded-xl border border-strong bg-surface shadow-dialog"
            >
              <div className="px-3.5 pb-1 pt-2.5 text-xs font-medium text-muted">Linked to this project</div>
              <ul className="pb-1">
                {SLASH_TOFT.map((m) => {
                  const i = SLASH.indexOf(m);
                  return (
                    <li key={m.id}>
                      <button
                        type="button"
                        role="option"
                        aria-selected={slash.index === i}
                        onMouseDown={(e) => {
                          e.preventDefault();
                          choose(m.id);
                        }}
                        className={cn("mx-1.5 flex min-h-11 w-[calc(100%-12px)] items-center gap-2.5 rounded-lg px-2.5 py-1 text-left", slash.index === i && "bg-accent-soft")}
                      >
                        <span className={cn("flex h-7 w-7 flex-none items-center justify-center rounded-md bg-raised", m.tint)}>
                          <m.icon className="h-[15px] w-[15px]" strokeWidth={1.9} />
                        </span>
                        <span className="flex min-w-0 flex-col">
                          <span className="text-[13px] font-medium text-fg">{m.label}</span>
                          <span className="text-xs leading-4 text-fg/70">{m.detail}</span>
                        </span>
                      </button>
                    </li>
                  );
                })}
              </ul>
              <div className="border-t border-subtle px-3.5 pb-1 pt-2 text-xs font-medium text-muted">Text</div>
              <ul className="grid grid-cols-2 pb-1.5">
                {SLASH_TEXT.map((m) => {
                  const i = SLASH.indexOf(m);
                  return (
                    <li key={m.id}>
                      <button
                        type="button"
                        role="option"
                        aria-selected={slash.index === i}
                        onMouseDown={(e) => {
                          e.preventDefault();
                          choose(m.id);
                        }}
                        className={cn("mx-1.5 flex h-[34px] w-[calc(100%-12px)] items-center gap-2 rounded-lg px-2.5 text-left text-[13px] text-fg/80", slash.index === i && "bg-accent-soft")}
                      >
                        <m.icon className="h-3.5 w-3.5 text-muted" strokeWidth={2} />
                        {m.label}
                      </button>
                    </li>
                  );
                })}
              </ul>
              <div className="flex flex-wrap gap-3.5 border-t border-subtle px-3.5 py-2 text-xs text-muted">
                <span>
                  <kbd className="font-mono text-fg/75">↑↓</kbd> move
                </span>
                <span>
                  <kbd className="font-mono text-fg/75">↵</kbd> insert
                </span>
                <span>
                  <kbd className="font-mono text-fg/75">esc</kbd> close
                </span>
              </div>
            </div>
          )}

          {pickPage && (
            <PagePicker
              pages={(tree ?? []).filter((p) => p.id !== page?.id)}
              onPick={(p) => {
                setPickPage(false);
                editor?.chain().focus().insertContent({ type: "text", text: p.title, marks: [{ type: "link", attrs: { href: `page:${p.id}` } }] }).insertContent(" ").run();
              }}
              onClose={() => setPickPage(false)}
            />
          )}
        </article>

        <aside className="flex min-w-0 flex-[1_1_240px] flex-col gap-[22px] px-4 pb-10 pt-2 text-[13px] md:pl-2 md:pr-6 md:pt-7">
          {heads.length > 0 && (
            <div className="flex flex-col gap-1.5">
              <span className="text-xs font-medium text-muted">On this page</span>
              {heads.map((h, i) => (
                <span key={i} className="text-fg/70">
                  {h}
                </span>
              ))}
            </div>
          )}
          <div className="flex flex-col gap-2">
            <span className="text-xs font-medium text-muted">Linked to</span>
            {linked.length > 0 ? (
              <span className="flex items-center gap-2 text-fg">
                <Layers aria-hidden="true" className="h-3.5 w-3.5 text-muted" strokeWidth={1.9} />
                {linked.join(", ")}
              </span>
            ) : (
              <span className="text-fg/70">No service yet.</span>
            )}
            <span className="text-xs leading-[17px] text-muted">From the blocks on this page. Remove a block to unlink its service.</span>
          </div>
          <div className="flex flex-col gap-1.5">
            <span className="text-xs font-medium text-muted">Out-of-date warning</span>
            <span className="leading-[19px] text-fg/80">
              Shown to readers when {linked.length ? linked.join(", ") : "a linked service"} gets a new image, build or server, or new variables or secrets, after the last verification.
            </span>
            <span className="leading-[19px] text-fg/70">
              {page?.verified_at ? (
                <>
                  Last verified <span className="font-mono text-xs">{page.verified_at.slice(0, 10)}</span>
                  {page.verified_by ? ` by ${page.verified_by.name}` : ""}.
                </>
              ) : (
                "Never verified."
              )}{" "}
              Saving does not verify the page.
            </span>
          </div>
          <div className="flex flex-col gap-1.5">
            <span className="text-xs font-medium text-muted">Who can run commands</span>
            <span className="leading-[19px] text-fg/80">
              Owners and developers of {name}; commands on a server, instance operators with their password. API keys can’t run them yet.
            </span>
          </div>
          <div className="flex flex-col gap-1.5 border-t border-subtle pt-3.5 text-fg/70">
            {page?.created_by && (
              <span>
                Owner <span className="text-fg">{page.created_by.name}</span>
              </span>
            )}
            {page ? (
              <span>
                Version <span className="font-mono text-xs text-fg">{page.version}</span>, saving makes <span className="font-mono text-xs text-fg">{page.version + 1}</span>
              </span>
            ) : (
              <span>Saving makes version 1.</span>
            )}
            {page && (
              <label className="mt-1 flex flex-col gap-1">
                <span className="text-xs text-muted">In</span>
                <select
                  value={page.parent_id ?? ""}
                  onChange={(e) => move.mutate(e.target.value)}
                  disabled={move.isPending}
                  className="touch-target h-8 rounded-md border border-strong bg-raised px-2 text-[13px] text-fg"
                >
                  <option value="">The top of the docs</option>
                  {parents.map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.title}
                    </option>
                  ))}
                </select>
              </label>
            )}
            {page && (
              <button
                type="button"
                onClick={() => (confirmDelete ? remove.mutate() : setConfirmDelete(true))}
                onBlur={() => setConfirmDelete(false)}
                disabled={remove.isPending}
                className={cn("touch-target mt-2 h-8 self-start rounded-md px-2 text-[13px]", confirmDelete ? "bg-status-failed-soft text-status-failed-fg" : "text-status-failed-fg hover:bg-raised")}
              >
                {confirmDelete ? "Delete it? Click again" : "Delete page"}
              </button>
            )}
          </div>
        </aside>
      </div>
    </EditorContext.Provider>
  );
}

/** Choosing a page to link. */
function PagePicker({ pages, onPick, onClose }: { pages: DocTreePage[]; onPick: (p: DocTreePage) => void; onClose: () => void }) {
  const [q, setQ] = useState("");
  const shown = pages.filter((p) => p.title.toLowerCase().includes(q.trim().toLowerCase())).slice(0, 12);
  return (
    <div role="dialog" aria-label="Link a page" className="flex w-[340px] max-w-full flex-col gap-1 rounded-xl border border-strong bg-surface p-2 shadow-menu">
      <input
        autoFocus
        value={q}
        onChange={(e) => setQ(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Escape") onClose();
          if (e.key === "Enter" && shown[0]) onPick(shown[0]);
        }}
        placeholder="Find a page"
        className="h-8 rounded-md border border-strong bg-raised px-2 text-[13px] text-fg"
      />
      <ul className="flex flex-col">
        {shown.map((p) => (
          <li key={p.id}>
            <button type="button" onClick={() => onPick(p)} className="touch-target flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-[13px] text-fg hover:bg-raised">
              <FileText className="h-3.5 w-3.5 text-muted" aria-hidden="true" />
              {p.title}
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}
