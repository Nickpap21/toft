"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ArrowDownLeft, ArrowUpRight, Check, ChevronRight, Download, LoaderCircle, Plus, RefreshCw, Trash2, TriangleAlert } from "lucide-react";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { ApiError } from "@/lib/api/client";
import { codeService, repoService, type Mirror, type Webhook, type WebhookEvent } from "@/lib/api/code.service";
import { useOrgRole } from "@/context/AuthContext";
import { useReauth } from "@/components/reauth";
import { LoadFailed } from "@/components/shell/unreachable";
import { cn } from "@/lib/utils";
import { card, field, outline, primary, small } from "@/app/projects/_lib/ui";
import { CopyButton } from "./parts";
import { ago, errorCode, mirrorBusy, useCode, webhookHref } from "./repo";

// Import, mirrors and outbound webhooks (D-79; boards CodeImport,
// RepositorySettings, RepositoryWebhook).

type Api = ReturnType<typeof repoService>;

const message = (err: unknown, fallback: string) => sentence(err instanceof ApiError ? err.message : err instanceof Error ? err.message : fallback);

/** The API's messages start lowercase, as Go's errors do; on a page, a
 *  sentence starts with a capital. */
export function sentence(s: string): string {
  return s ? s[0].toUpperCase() + s.slice(1) : s;
}

/** github.com/founders/shop from https://github.com/founders/shop.git. */
export function remoteName(url: string): string {
  return url.replace(/^https:\/\//, "").replace(/\.git$/, "");
}

/** The repository name an import URL suggests ("shop" from …/shop.git). */
export function nameFromURL(url: string): string {
  const last = url.trim().replace(/\/+$/, "").replace(/\.git$/, "").split("/").pop() ?? "";
  return last
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/g, "-")
    .replace(/^[^a-z0-9]+|[^a-z0-9]+$/g, "")
    .slice(0, 100);
}

function Checkbox({ checked, onChange, children, hint }: { checked: boolean; onChange: (v: boolean) => void; children: React.ReactNode; hint?: React.ReactNode }) {
  return (
    <label
      className={cn(
        "flex cursor-pointer gap-2.5 rounded-lg border p-3 transition-colors",
        checked ? "border-accent/45 bg-accent-soft/40" : "border-subtle bg-ground hover:border-strong",
      )}
    >
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} className="mt-[3px] h-4 w-4 flex-none accent-[var(--color-accent)]" />
      <span className="flex flex-col gap-0.5">
        <span className="text-[13px] font-medium text-fg">{children}</span>
        {hint && <span className="text-xs text-muted">{hint}</span>}
      </span>
    </label>
  );
}

function ErrorLine({ text }: { text: string }) {
  if (!text) return null;
  return (
    <p role="alert" className="text-[13px] text-status-failed-fg">
      {text}
    </p>
  );
}

// ─── import ──────────────────────────────────────────────────────────────────

/** Import a repository: into this empty one, or into a new one (named, from
 *  New repository's "Import from a URL"). */
export function ImportForm({
  api,
  projectId,
  named,
  initialURL = "",
  onDone,
  onCancel,
}: {
  api?: Api;
  projectId: string;
  named?: { slug: string };
  initialURL?: string;
  onDone: (repoName?: string) => void;
  onCancel: () => void;
}) {
  const queryClient = useQueryClient();
  const [url, setURL] = useState(initialURL);
  const [name, setName] = useState("");
  const [nameTouched, setNameTouched] = useState(false);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [keep, setKeep] = useState(false);
  const repoName = named ? (nameTouched ? name : nameFromURL(url)) : "";
  const validURL = /^https:\/\/[^/\s?#@]+\/[^\s?#]+$/.test(url.trim());
  const validName = !named || /^[a-z0-9]([a-z0-9._-]{0,98}[a-z0-9])?$/.test(repoName);
  const start = useMutation({
    mutationFn: async () => {
      let target = api;
      if (named) {
        const r = await codeService.createRepository(projectId, repoName);
        target = repoService(projectId, r.id);
      }
      await target!.importFrom({ url: url.trim(), username: username.trim() || undefined, password: password || undefined, keep });
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["repositories", projectId] });
      queryClient.invalidateQueries({ queryKey: ["repository", projectId] });
      toast.success(keep ? "Importing; it keeps pulling every 10 minutes after" : "Importing");
      onDone(repoName || undefined);
    },
  });
  const code = errorCode(start.error);
  const err = start.error
    ? code === "private_address"
      ? "That URL leads to an address that isn’t public, which Toft doesn’t reach."
      : message(start.error, "Couldn’t start the import")
    : "";
  return (
    <form
      className="flex flex-col gap-3.5"
      onSubmit={(e) => {
        e.preventDefault();
        if (validURL && validName) start.mutate();
      }}
    >
      <label className="flex flex-col gap-1.5 text-[13px] font-medium text-fg">
        Repository URL
        <input
          autoFocus
          value={url}
          onChange={(e) => setURL(e.target.value)}
          placeholder="https://github.com/owner/repository"
          spellCheck={false}
          autoComplete="off"
          aria-invalid={!!url && !validURL}
          aria-describedby="import-url-help"
          className={cn(field, "font-mono text-[13px]")}
        />
        <span id="import-url-help" className={cn("text-xs font-normal", url && !validURL ? "text-status-failed-fg" : "text-muted")}>
          The HTTPS clone URL: GitHub, GitLab, Bitbucket, Codeberg or any git host on a public address. SSH URLs aren’t supported yet.
        </span>
      </label>
      {named && (
        <label className="flex flex-col gap-1.5 text-[13px] font-medium text-fg">
          Name
          <span className="flex items-center overflow-hidden rounded-lg border border-strong bg-ground focus-within:ring-2 focus-within:ring-focus">
            <span className="pl-2.5 pr-1 font-mono text-[13px] text-muted">{named.slug}/</span>
            <input
              value={repoName}
              onChange={(e) => {
                setNameTouched(true);
                setName(e.target.value.toLowerCase());
              }}
              aria-invalid={!!repoName && !validName}
              spellCheck={false}
              autoComplete="off"
              className="h-9 min-w-0 flex-1 bg-transparent pr-2.5 font-mono text-[13px] text-fg outline-none"
            />
          </span>
          <span className={cn("text-xs font-normal", repoName && !validName ? "text-status-failed-fg" : "text-muted")}>
            Taken from the URL. Lowercase letters, digits, dots, dashes and underscores; it’s part of the clone URL.
          </span>
        </label>
      )}
      <fieldset className="flex flex-col gap-2.5 rounded-lg border border-subtle bg-ground/60 p-3">
        <legend className="sr-only">For a private repository</legend>
        <span className="text-[13px] font-medium text-fg">For a private repository</span>
        <div className="flex flex-wrap gap-3">
          <label className="flex min-w-[140px] flex-1 flex-col gap-1.5 text-xs text-fg/70">
            Username
            <input
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              placeholder="x-access-token"
              spellCheck={false}
              autoComplete="off"
              className={cn(field, "h-[34px] font-mono text-xs")}
            />
          </label>
          <label className="flex min-w-[180px] flex-[1.3] flex-col gap-1.5 text-xs text-fg/70">
            Token or password
            <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" className={cn(field, "h-[34px] text-xs")} />
          </label>
        </div>
        <span className="text-xs text-muted">
          Leave both empty for a public repository. A token is stored encrypted, sent only to that host, and never shown again; without “Keep pulling” it’s dropped once
          the import is done.
        </span>
      </fieldset>
      <Checkbox
        checked={keep}
        onChange={setKeep}
        hint="For while you switch over: pushes there arrive here and deploy as pushes do. This repository stays read-only until you stop mirroring in its settings."
      >
        Keep pulling from it every 10 minutes
      </Checkbox>
      <ErrorLine text={err} />
      <DialogFooter>
        <button type="button" onClick={onCancel} className="h-9 rounded-lg border border-subtle px-3 text-[13px] font-medium text-fg hover:bg-raised">
          Cancel
        </button>
        <button type="submit" disabled={!validURL || !validName || start.isPending} className={primary}>
          {start.isPending ? "Starting…" : "Import"}
        </button>
      </DialogFooter>
    </form>
  );
}

/** Import into this empty repository (board CodeImport, state=dialog). */
export function ImportDialog({ open, onClose, initialURL }: { open: boolean; onClose: () => void; initialURL?: string }) {
  const { projectId, api, repo } = useCode();
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-[540px]">
        <DialogHeader>
          <DialogTitle>Import a repository into {repo.name}</DialogTitle>
          <DialogDescription>Every branch and tag, with their full history. Pull requests, issues, releases and LFS files stay where they are.</DialogDescription>
        </DialogHeader>
        {open && <ImportForm api={api} projectId={projectId} initialURL={initialURL} onDone={() => onClose()} onCancel={onClose} />}
      </DialogContent>
    </Dialog>
  );
}

/** What an import is doing, in place of the empty repository's help
 *  (board CodeImport, states importing and failed). */
export function ImportProgress({ mirror }: { mirror: Mirror }) {
  const { api, projectId } = useCode();
  const queryClient = useQueryClient();
  const role = useOrgRole();
  const canWrite = role === "owner" || role === "developer";
  const [retrying, setRetrying] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, []);
  const refresh = () => queryClient.invalidateQueries({ queryKey: ["repository", projectId] });
  const cancel = useMutation({
    mutationFn: () => api.removeMirror(mirror.id),
    onSuccess: () => {
      refresh();
      queryClient.invalidateQueries({ queryKey: ["repositories", projectId] });
      toast.success("Import cancelled");
    },
    onError: (err) => toast.error(message(err, "Couldn’t cancel the import")),
  });
  const host = remoteName(mirror.url);
  if (mirror.state === "failed") {
    return (
      <section aria-labelledby="fail-h" className="flex flex-col gap-3.5 rounded-[10px] border border-status-failed/35 bg-surface p-5">
        <div className="flex items-start gap-3">
          <span aria-hidden="true" className="flex h-8 w-8 flex-none items-center justify-center rounded-lg bg-status-failed-soft text-status-failed-fg">
            <TriangleAlert className="h-4 w-4" />
          </span>
          <div className="flex min-w-0 flex-col gap-0.5">
            <h2 id="fail-h" className="text-[15px] font-semibold leading-[22px] text-fg">
              The import didn’t finish
            </h2>
            <p className="text-[13px] text-fg/70">
              From <code className="font-mono text-xs text-fg">{host}</code>
              {mirror.last_sync_at ? `, ${ago(mirror.last_sync_at, now)}` : ""}. Nothing was kept, so the repository is still empty.
            </p>
          </div>
        </div>
        {mirror.error && <p className="rounded-lg border border-subtle bg-ground px-3 py-2.5 text-[13px] text-status-failed-fg">{sentence(mirror.error)}{/[.!?]$/.test(mirror.error) ? "" : "."}</p>}
        {canWrite && (
          <div className="flex flex-wrap gap-2">
            <button type="button" onClick={() => setRetrying(true)} className={primary}>
              Try again…
            </button>
            <button type="button" onClick={() => cancel.mutate()} disabled={cancel.isPending} className={outline}>
              Push instead
            </button>
          </div>
        )}
        <ImportDialog open={retrying} onClose={() => setRetrying(false)} initialURL={mirror.url} />
      </section>
    );
  }
  const started = mirror.syncing_since ?? mirror.created_at;
  const secs = Math.max(0, Math.floor((now - new Date(started).getTime()) / 1000));
  const elapsed = secs < 60 ? `${secs}s` : `${Math.floor(secs / 60)}m ${secs % 60}s`;
  const r = mirror.remote;
  const steps: { done: boolean; text: React.ReactNode }[] = [
    {
      done: !!r,
      text: r ? (
        <>
          Reached {host.split("/")[0]}
          {mirror.username && (
            <>
              , signed in as <code className="font-mono text-xs">{mirror.username}</code>
            </>
          )}
        </>
      ) : (
        <>Reaching {host.split("/")[0]}</>
      ),
    },
    ...(r
      ? [
          {
            done: true,
            text: (
              <>
                Found {r.branches === 1 ? "1 branch" : `${r.branches} branches`} and {r.tags === 1 ? "1 tag" : `${r.tags} tags`}
                {r.default_branch && (
                  <>
                    ; the default branch is <code className="font-mono text-xs">{r.default_branch}</code>
                  </>
                )}
              </>
            ),
          },
          { done: false, text: <>Copying and checking every object</> },
        ]
      : []),
  ];
  return (
    <section aria-labelledby="imp-h" aria-live="polite" className={cn(card, "flex flex-col gap-4 p-5")}>
      <div className="flex items-start gap-3">
        <span aria-hidden="true" className="flex h-8 w-8 flex-none items-center justify-center rounded-lg bg-status-building-soft text-status-building-fg">
          <Download className="h-4 w-4" />
        </span>
        <div className="flex min-w-0 flex-col gap-0.5">
          <h2 id="imp-h" className="text-[15px] font-semibold leading-[22px] text-fg">
            Importing from <code className="break-all font-mono text-[13px]">{host}</code>
          </h2>
          <p className="text-[13px] text-fg/70">
            {mirror.syncing_since ? `Started ${elapsed} ago` : "Starting"}
            {mirror.created_by ? ` by ${mirror.created_by.name}` : ""}. It runs on the panel, so you can leave this page; Files opens on its default branch when it’s done.
          </p>
        </div>
      </div>
      <div role="progressbar" aria-label="Importing" className="relative h-1 overflow-hidden rounded-full bg-raised">
        <span className="absolute inset-y-0 left-0 w-2/5 rounded-full bg-status-building motion-safe:animate-[toft-slide_1.6s_ease-in-out_infinite]" />
      </div>
      <ol className="flex flex-col gap-2 text-[13px]">
        {steps.map((s, i) => (
          <li key={i} className={cn("flex items-center gap-2", s.done ? "text-fg/70" : "text-fg")}>
            {s.done ? (
              <Check className="h-3.5 w-3.5 flex-none text-status-running-fg" aria-hidden="true" />
            ) : (
              <LoaderCircle className="h-3.5 w-3.5 flex-none text-status-building-fg motion-safe:animate-spin" aria-hidden="true" />
            )}
            <span>{s.text}</span>
          </li>
        ))}
      </ol>
      <div className="flex flex-wrap items-center justify-between gap-3 border-t border-subtle pt-3.5">
        <span className="text-xs text-muted">
          {mirror.keep ? "Then it keeps pulling every 10 minutes, read-only, until you stop mirroring in Settings." : "Pushes are refused until it’s done."}
        </span>
        {canWrite && (
          <button type="button" onClick={() => cancel.mutate()} disabled={cancel.isPending} className={small}>
            Cancel import
          </button>
        )}
      </div>
    </section>
  );
}

/** A pull mirror's repository, on Files: where it pulls from, read-only
 *  (board CodeImport, state=mirror). */
export function MirrorBanner({ mirror }: { mirror: Mirror }) {
  const { api, projectId, base } = useCode();
  const queryClient = useQueryClient();
  const role = useOrgRole();
  const [now] = useState(() => Date.now());
  const sync = useMutation({
    mutationFn: () => api.syncMirror(mirror.id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["repository", projectId] });
      toast.success("Pulling now");
    },
    onError: (err) => toast.error(errorCode(err) === "sync_running" ? "It’s pulling already" : message(err, "Couldn’t start a sync")),
  });
  const failing = mirror.state === "failing";
  return (
    <div
      role="status"
      className={cn(
        "flex flex-wrap items-center gap-3 rounded-[10px] border px-4 py-3",
        failing ? "border-status-failed/35 bg-status-failed-soft/40" : "border-status-building/35 bg-status-building-soft/40",
      )}
    >
      <ArrowDownLeft className={cn("h-4 w-4 flex-none", failing ? "text-status-failed-fg" : "text-status-building-fg")} aria-hidden="true" />
      <span className="min-w-0 flex-[1_1_360px] text-[13px] text-fg/80">
        Mirror of{" "}
        <a href={mirror.url} target="_blank" rel="noreferrer noopener" className="font-mono text-xs inline-link">
          {remoteName(mirror.url)}
        </a>
        {mirror.state === "syncing"
          ? " · pulling now"
          : failing
            ? ` · failing: ${mirror.error ?? "the last pull failed"}`
            : mirror.last_ok_at
              ? ` · synced ${ago(mirror.last_ok_at, now)}`
              : ""}
        . Read-only here: push to the original, and it arrives within 10 minutes.
      </span>
      {(role === "owner" || role === "developer") && (
        <button type="button" onClick={() => sync.mutate()} disabled={sync.isPending || mirror.state === "syncing"} className={cn(small, "h-[30px] text-xs rounded-md")}>
          <RefreshCw className={cn("h-[13px] w-[13px]", mirror.state === "syncing" && "motion-safe:animate-spin")} aria-hidden="true" />
          Sync now
        </button>
      )}
      {role === "owner" && (
        <Link href={`${base}/settings`} className="text-xs text-link hover:underline">
          Mirror settings
        </Link>
      )}
    </div>
  );
}

// ─── settings: mirrors ───────────────────────────────────────────────────────

const stateLine = (m: Mirror, now: number): { text: string; tone: "ok" | "bad" | "busy" | "idle" } => {
  switch (m.state) {
    case "ok":
      return { text: m.last_ok_at ? `${m.direction === "push" ? "Pushed" : "Synced"} ${ago(m.last_ok_at, now)}` : "Synced", tone: "ok" };
    case "syncing":
      return { text: m.direction === "push" ? "Pushing now" : "Pulling now", tone: "busy" };
    case "importing":
      return { text: "Importing", tone: "busy" };
    case "pending":
      return { text: "Pushing in a moment", tone: "busy" };
    case "failed":
      return { text: "Import failed", tone: "bad" };
    default:
      // It hasn't worked since its last success.
      return { text: m.last_ok_at ? `Failing since ${clock(m.last_ok_at, now)}` : "Failing", tone: "bad" };
  }
};
/** "11:40" today, else "Oct 18". */
function clock(iso: string, now: number): string {
  const d = new Date(iso);
  return d.toDateString() === new Date(now).toDateString()
    ? d.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit" })
    : d.toLocaleDateString("en-US", { month: "short", day: "numeric" });
}

const toneClass = { ok: "text-status-running-fg", bad: "text-status-failed-fg", busy: "text-status-building-fg", idle: "text-muted" } as const;
const toneDot = { ok: "bg-status-running", bad: "bg-status-failed", busy: "bg-status-building", idle: "bg-muted" } as const;

function StateDot({ text, tone }: { text: string; tone: keyof typeof toneClass }) {
  return (
    <span className={cn("inline-flex items-center gap-1.5 text-xs", toneClass[tone])}>
      <span aria-hidden="true" className={cn("h-[7px] w-[7px] rounded-full", toneDot[tone], tone === "busy" && "motion-safe:animate-pulse")} />
      {text}
    </span>
  );
}

/** Mirrors on Repository settings: the pull mirror, if any, and push
 *  mirrors with Add push mirror (owners). */
export function MirrorsCard() {
  const { api, projectId } = useCode();
  const queryClient = useQueryClient();
  const confirmPassword = useReauth();
  const [adding, setAdding] = useState(false);
  const [stopping, setStopping] = useState<Mirror | null>(null);
  const mirrors = useQuery({
    queryKey: ["mirrors", api.id],
    queryFn: () => api.mirrors(),
    refetchInterval: (q) => ((q.state.data ?? []).some(mirrorBusy) ? 2500 : 30_000),
  });
  // Relative times count from when the list was read.
  const now = mirrors.dataUpdatedAt;
  const refresh = () => {
    queryClient.invalidateQueries({ queryKey: ["mirrors", api.id] });
    queryClient.invalidateQueries({ queryKey: ["repository", projectId] });
    queryClient.invalidateQueries({ queryKey: ["repositories", projectId] });
  };
  const sync = useMutation({
    mutationFn: (m: Mirror) => api.syncMirror(m.id),
    onSuccess: (m) => {
      refresh();
      toast.success(m.direction === "push" ? "Pushing now" : "Pulling now");
    },
    onError: (err) => toast.error(errorCode(err) === "sync_running" ? "A sync is running already" : message(err, "Couldn’t start a sync")),
  });
  const remove = useMutation({
    mutationFn: (m: Mirror) => api.removeMirror(m.id),
    onSuccess: (_, m) => {
      refresh();
      setStopping(null);
      toast.success(m.direction === "pull" ? "Stopped mirroring: the repository takes pushes now" : `Removed the mirror to ${remoteName(m.url)}`);
    },
    onError: (err) => toast.error(message(err, "Couldn’t remove the mirror")),
  });
  const all = mirrors.data ?? [];
  const pull = all.find((m) => m.direction === "pull" && m.state !== "importing" && m.state !== "failed");
  const pushes = all.filter((m) => m.direction === "push");
  return (
    <section aria-labelledby="mir-h" className={cn(card, "flex flex-col")}>
      <div className="flex flex-wrap items-start justify-between gap-3 px-5 pb-3.5 pt-[18px]">
        <div className="flex flex-[1_1_380px] flex-col gap-1">
          <h2 id="mir-h" className="text-[15px] font-semibold leading-[22px] text-fg">
            Mirrors
          </h2>
          <p className="text-[13px] text-fg/70">
            Copies of this repository on other hosts. A push mirror gets every branch and tag seconds after each push or merge here: GitHub Actions can run on it and
            report checks back.
          </p>
        </div>
        <button type="button" onClick={() => setAdding(true)} disabled={pushes.length >= 5} className={small}>
          <Plus className="h-3.5 w-3.5" aria-hidden="true" />
          Add push mirror
        </button>
      </div>

      {pull && (
        <div className="mx-5 mb-3.5 flex flex-col gap-2.5 rounded-lg border border-status-building/35 bg-status-building-soft/30 px-4 py-3.5">
          <div className="flex flex-wrap items-center justify-between gap-2.5">
            <div className="flex min-w-0 items-center gap-2.5">
              <span aria-hidden="true" className="flex h-7 w-7 flex-none items-center justify-center rounded-md bg-status-building-soft text-status-building-fg">
                <ArrowDownLeft className="h-[15px] w-[15px]" />
              </span>
              <span className="flex min-w-0 flex-col">
                <span className="text-[13px] font-semibold text-fg">
                  Pulls from <span className="break-all font-mono text-xs font-medium">{remoteName(pull.url)}</span>
                </span>
                <span className="text-xs text-muted">
                  Every 10 minutes
                  {pull.username && (
                    <>
                      {" "}
                      · as <code className="font-mono">{pull.username}</code>
                    </>
                  )}
                  {pull.created_by ? ` · imported ${ago(pull.created_at, now)} by ${pull.created_by.name}` : ""}
                </span>
              </span>
            </div>
            <StateDot {...stateLine(pull, now)} />
          </div>
          {pull.error && pull.state === "failing" && <p className="text-xs text-status-failed-fg">{sentence(pull.error)}</p>}
          <p className="text-xs text-fg/70">
            While it mirrors, this repository is read-only: pushes, merges and new pull requests are refused, so nothing here is overwritten. Push to the original
            instead. Stop mirroring to make it a normal repository; nothing is deleted.
          </p>
          <div className="flex gap-2">
            <button type="button" onClick={() => sync.mutate(pull)} disabled={sync.isPending || pull.state === "syncing"} className={cn(small, "h-[30px] text-xs rounded-md")}>
              <RefreshCw className="h-[13px] w-[13px]" aria-hidden="true" />
              Sync now
            </button>
            <button type="button" onClick={() => setStopping(pull)} className="h-[30px] rounded-md border border-subtle px-2.5 text-xs font-medium text-fg hover:bg-raised">
              Stop mirroring…
            </button>
          </div>
        </div>
      )}

      {pushes.length > 0 ? (
        <ul className="border-t border-subtle">
          {pushes.map((m, i) => {
            const line = stateLine(m, now);
            const failing = m.state === "failing";
            return (
              <li key={m.id} className={cn("flex flex-col gap-2 px-5 py-3.5", i > 0 && "border-t border-subtle")}>
                <div className="flex flex-wrap items-center justify-between gap-2.5">
                  <div className="flex min-w-0 items-center gap-2.5">
                    <span aria-hidden="true" className="flex h-7 w-7 flex-none items-center justify-center rounded-md bg-raised text-fg/70">
                      <ArrowUpRight className="h-[15px] w-[15px]" />
                    </span>
                    <span className="flex min-w-0 flex-col">
                      <span className="break-all font-mono text-xs font-medium text-fg">{remoteName(m.url)}</span>
                      <span className="text-xs text-muted">
                        {m.username ? `as ${m.username} · ` : ""}every branch and tag{m.created_by ? ` · added ${ago(m.created_at, now)} by ${m.created_by.name}` : ""}
                      </span>
                    </span>
                  </div>
                  <div className="flex items-center gap-3">
                    <StateDot {...line} />
                    <button
                      type="button"
                      aria-label={`Push to ${remoteName(m.url)} now`}
                      title="Sync now"
                      onClick={() => sync.mutate(m)}
                      disabled={m.state === "syncing"}
                      className="inline-flex h-[30px] w-[30px] items-center justify-center rounded-md border border-subtle bg-raised text-fg hover:bg-raised/70 disabled:opacity-50"
                    >
                      <RefreshCw className={cn("h-[13px] w-[13px]", m.state === "syncing" && "motion-safe:animate-spin")} aria-hidden="true" />
                    </button>
                    <button
                      type="button"
                      aria-label={`Remove the mirror to ${remoteName(m.url)}`}
                      title="Remove"
                      onClick={() => setStopping(m)}
                      className="inline-flex h-[30px] w-[30px] items-center justify-center rounded-md border border-subtle text-fg/70 hover:bg-raised hover:text-fg"
                    >
                      <Trash2 className="h-[13px] w-[13px]" aria-hidden="true" />
                    </button>
                  </div>
                </div>
                {failing && m.error && (
                  <div role="status" className="flex gap-2 sm:ml-[38px] rounded-lg border border-status-failed/35 bg-status-failed-soft/40 px-3 py-2.5 text-xs text-status-failed-fg">
                    <TriangleAlert className="mt-px h-3.5 w-3.5 flex-none" aria-hidden="true" />
                    <span className="flex flex-col gap-0.5">
                      <span>{sentence(m.error)}</span>
                      <span className="text-muted">
                        {m.failures === 1 ? "1 try so far" : `${m.failures} tries so far`}
                        {m.next_sync_at ? ` · next ${new Date(m.next_sync_at).getTime() <= now ? "now" : `in ${untilText(m.next_sync_at, now)}`}` : ""} · Sync now
                        tries at once
                      </span>
                    </span>
                  </div>
                )}
              </li>
            );
          })}
        </ul>
      ) : !mirrors.data ? (
        mirrors.error != null && (
          <div className="border-t border-subtle px-5 py-3.5">
            <LoadFailed what="the mirrors" onRetry={() => void mirrors.refetch()} />
          </div>
        )
      ) : (
        !pull && <p className="border-t border-subtle px-5 pb-[18px] pt-3.5 text-[13px] text-muted">No push mirrors. Add one to keep a copy on GitHub, GitLab or any host that takes a push over HTTPS.</p>
      )}

      <AddPushMirrorDialog open={adding} onClose={() => setAdding(false)} onAdded={refresh} confirmPassword={confirmPassword} />
      <Dialog open={!!stopping} onOpenChange={(o) => !o && setStopping(null)}>
        <DialogContent className="max-w-[460px]">
          <DialogHeader>
            <DialogTitle>{stopping?.direction === "pull" ? "Stop mirroring?" : "Remove this push mirror?"}</DialogTitle>
            <DialogDescription>
              {stopping?.direction === "pull"
                ? `The repository keeps every branch and tag it pulled from ${stopping ? remoteName(stopping.url) : ""}, takes pushes from now on, and stops following the original. Its stored token is deleted.`
                : `Toft stops pushing to ${stopping ? remoteName(stopping.url) : ""}. Nothing there is deleted; its stored token is.`}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <button type="button" onClick={() => setStopping(null)} className="h-9 rounded-lg border border-subtle px-3 text-[13px] font-medium text-fg hover:bg-raised">
              Cancel
            </button>
            <button
              type="button"
              disabled={remove.isPending}
              onClick={() => stopping && remove.mutate(stopping)}
              className="h-9 rounded-lg bg-danger px-3.5 text-[13px] font-semibold text-on-danger hover:bg-danger/90 disabled:opacity-50"
            >
              {stopping?.direction === "pull" ? "Stop mirroring" : "Remove mirror"}
            </button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  );
}

/** "4 min", "1 h": how long until iso. */
function untilText(iso: string, now: number): string {
  const m = Math.max(1, Math.round((new Date(iso).getTime() - now) / 60_000));
  return m < 60 ? `${m} min` : `${Math.round(m / 60)} h`;
}

function AddPushMirrorDialog({
  open,
  onClose,
  onAdded,
  confirmPassword,
}: {
  open: boolean;
  onClose: () => void;
  onAdded: () => void;
  confirmPassword: ReturnType<typeof useReauth>;
}) {
  const { api } = useCode();
  const [url, setURL] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const valid = /^https:\/\/[^/\s?#@]+\/[^\s?#]+$/.test(url.trim());
  const add = useMutation({
    mutationFn: async () => {
      if (!(await confirmPassword("A push mirror copies all of this repository’s code out of Toft, so Toft asks for your password first."))) {
        throw new Error("The mirror isn’t added without your password.");
      }
      return api.addPushMirror({ url: url.trim(), username: username.trim() || undefined, password: password || undefined });
    },
    onSuccess: (m) => {
      onAdded();
      toast.success(`Pushing to ${remoteName(m.url)}`);
      close();
    },
  });
  const close = () => {
    setURL("");
    setUsername("");
    setPassword("");
    add.reset();
    onClose();
  };
  const code = errorCode(add.error);
  const err = add.error
    ? code === "private_address"
      ? "That URL leads to an address that isn’t public, which Toft doesn’t reach."
      : code === "mirror_limit"
        ? "A repository has 5 push mirrors at most."
        : message(add.error, "Couldn’t add the mirror")
    : "";
  return (
    <Dialog open={open} onOpenChange={(o) => !o && close()}>
      <DialogContent className="max-w-[520px]">
        <form
          className="flex flex-col gap-3.5"
          onSubmit={(e) => {
            e.preventDefault();
            if (valid) add.mutate();
          }}
        >
          <DialogHeader>
            <DialogTitle>Add a push mirror</DialogTitle>
            <DialogDescription>Seconds after each push, merge or tag here, Toft pushes every branch and tag to this repository.</DialogDescription>
          </DialogHeader>
          <label className="flex flex-col gap-1.5 text-[13px] font-medium text-fg">
            Repository URL
            <input
              autoFocus
              value={url}
              onChange={(e) => setURL(e.target.value)}
              placeholder="https://github.com/owner/repository.git"
              spellCheck={false}
              autoComplete="off"
              aria-invalid={!!url && !valid}
              className={cn(field, "font-mono text-[13px]")}
            />
            <span className={cn("text-xs font-normal", url && !valid ? "text-status-failed-fg" : "text-muted")}>HTTPS on a public address. SSH URLs aren’t supported yet.</span>
          </label>
          <div className="flex flex-wrap gap-3">
            <label className="flex min-w-[140px] flex-1 flex-col gap-1.5 text-[13px] font-medium text-fg">
              Username
              <input value={username} onChange={(e) => setUsername(e.target.value)} placeholder="x-access-token" spellCheck={false} autoComplete="off" className={cn(field, "font-mono text-[13px]")} />
            </label>
            <label className="flex min-w-[180px] flex-[1.3] flex-col gap-1.5 text-[13px] font-medium text-fg">
              Token or password
              <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" className={field} />
            </label>
          </div>
          <p className="-mt-1.5 text-xs text-muted">
            On GitHub: a fine-grained token with Contents read and write on that one repository. It’s stored encrypted, used only by the panel, and never shown again.
          </p>
          <div role="note" className="flex gap-2.5 rounded-lg border border-env-amber/40 bg-env-amber-soft/50 px-3 py-2.5 text-xs text-env-amber-fg">
            <TriangleAlert className="mt-px h-[15px] w-[15px] flex-none" aria-hidden="true" />
            <span>
              Toft force-pushes, so that repository ends up exactly like this one: branches and tags it has that this one doesn’t are deleted. Use an empty
              repository, or one only Toft pushes to.
            </span>
          </div>
          <ErrorLine text={err} />
          <DialogFooter>
            <button type="button" onClick={close} className="h-9 rounded-lg border border-subtle px-3 text-[13px] font-medium text-fg hover:bg-raised">
              Cancel
            </button>
            <button type="submit" disabled={!valid || add.isPending} className={primary}>
              {add.isPending ? "Adding…" : "Add and push now"}
            </button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// ─── settings: webhooks ──────────────────────────────────────────────────────

export const EVENTS: { id: WebhookEvent; label: string; hint: string }[] = [
  { id: "push", label: "Pushes", hint: "A branch or tag created, moved or deleted, by a push, a merge or the mirror. Up to 20 commits each." },
  { id: "pull_request", label: "Pull requests", hint: "Opened, pushed to, closed, reopened or merged." },
];

export function eventsText(events: WebhookEvent[]): string {
  const p = events.includes("push"), r = events.includes("pull_request");
  return p && r ? "Pushes and pull requests" : p ? "Pushes" : "Pull requests";
}

/** The last delivery's answer, as a webhook's row shows it. */
export function lastAnswer(w: Webhook, now: number): { text: string; tone: "ok" | "bad" | "busy" | "idle" } {
  if (!w.active) return { text: "Paused", tone: "idle" };
  const d = w.last_delivery;
  if (!d) return { text: "Nothing sent yet", tone: "idle" };
  if (d.state === "pending") return { text: d.attempts > 0 ? `Retrying · ${d.response_status ?? "no answer"}` : "Sending", tone: "busy" };
  const when = d.delivered_at ? ago(d.delivered_at, now) : "";
  if (d.state === "delivered") return { text: `${d.response_status} · ${when}`, tone: "ok" };
  return { text: `${d.response_status ?? "no answer"} · failed ${when}`, tone: "bad" };
}

/** Webhooks on Repository settings (owners). */
export function WebhooksCard() {
  const { api, base } = useCode();
  const [adding, setAdding] = useState(false);
  const [secret, setSecret] = useState<{ secret: string; url: string } | null>(null);
  const hooks = useQuery({ queryKey: ["webhooks", api.id], queryFn: () => api.webhooks(), refetchInterval: 15_000 });
  const now = hooks.dataUpdatedAt;
  const list = hooks.data ?? [];
  return (
    <section aria-labelledby="hooks-h" className={cn(card, "flex flex-col")}>
      <div className="flex flex-wrap items-start justify-between gap-3 px-5 pb-3.5 pt-[18px]">
        <div className="flex flex-[1_1_380px] flex-col gap-1">
          <h2 id="hooks-h" className="text-[15px] font-semibold leading-[22px] text-fg">
            Webhooks
          </h2>
          <p className="text-[13px] text-fg/70">
            Toft posts to these URLs when a branch or tag moves or a pull request changes. Every request is signed with{" "}
            <code className="font-mono text-xs text-fg">X-Toft-Signature</code>.
          </p>
        </div>
        <button type="button" onClick={() => setAdding(true)} disabled={list.length >= 20} className={small}>
          <Plus className="h-3.5 w-3.5" aria-hidden="true" />
          Add webhook
        </button>
      </div>
      {list.length > 0 ? (
        <ul className="border-t border-subtle">
          {list.map((w, i) => {
            const a = lastAnswer(w, now);
            return (
              <li key={w.id} className={cn(i > 0 && "border-t border-subtle")}>
                <Link href={webhookHref(base, w.id)} className="flex flex-wrap items-center justify-between gap-2.5 px-5 py-[13px] hover:bg-raised/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-focus">
                  <span className="flex min-w-0 flex-[1_1_280px] flex-col">
                    <span className="truncate font-mono text-xs font-medium text-fg">{w.url}</span>
                    <span className="text-xs text-muted">{eventsText(w.events)}</span>
                  </span>
                  <span className="inline-flex items-center gap-3">
                    <StateDot {...a} />
                    <ChevronRight className="h-3.5 w-3.5 text-muted" aria-hidden="true" />
                  </span>
                </Link>
              </li>
            );
          })}
        </ul>
      ) : !hooks.data ? (
        hooks.error != null && (
          <div className="border-t border-subtle px-5 py-3.5">
            <LoadFailed what="the webhooks" onRetry={() => void hooks.refetch()} />
          </div>
        )
      ) : (
        <p className="border-t border-subtle px-5 pb-[18px] pt-3.5 text-[13px] text-muted">
          No webhooks. Add one to tell your CI, a chat channel or any service when this repository changes.
        </p>
      )}
      <AddWebhookDialog open={adding} onClose={() => setAdding(false)} onCreated={(s, url) => setSecret({ secret: s, url })} />
      <SecretDialog value={secret?.secret ?? ""} title="Webhook added. Copy its signing secret" open={!!secret} onClose={() => setSecret(null)} />
    </section>
  );
}

function AddWebhookDialog({ open, onClose, onCreated }: { open: boolean; onClose: () => void; onCreated: (secret: string, url: string) => void }) {
  const { api } = useCode();
  const queryClient = useQueryClient();
  const [url, setURL] = useState("");
  const [events, setEvents] = useState<WebhookEvent[]>(["push", "pull_request"]);
  const valid = /^https:\/\/[^/\s?#@]+([/?][^\s#]*)?$/.test(url.trim()) && events.length > 0;
  const create = useMutation({
    mutationFn: () => api.createWebhook({ url: url.trim(), events }),
    onSuccess: (w) => {
      queryClient.invalidateQueries({ queryKey: ["webhooks", api.id] });
      onCreated(w.secret, w.url);
      close();
    },
  });
  const close = () => {
    setURL("");
    setEvents(["push", "pull_request"]);
    create.reset();
    onClose();
  };
  const code = errorCode(create.error);
  const err = create.error
    ? code === "private_address"
      ? "That URL leads to an address that isn’t public, which Toft doesn’t reach."
      : message(create.error, "Couldn’t add the webhook")
    : "";
  return (
    <Dialog open={open} onOpenChange={(o) => !o && close()}>
      <DialogContent className="max-w-[520px]">
        <form
          className="flex flex-col gap-3.5"
          onSubmit={(e) => {
            e.preventDefault();
            if (valid) create.mutate();
          }}
        >
          <DialogHeader>
            <DialogTitle>Add a webhook</DialogTitle>
            <DialogDescription>
              Toft posts JSON to this URL. It carries a <code className="font-mono text-xs">text</code> line too, so Slack and Mattermost show it as it is.
            </DialogDescription>
          </DialogHeader>
          <label className="flex flex-col gap-1.5 text-[13px] font-medium text-fg">
            Payload URL
            <input
              autoFocus
              value={url}
              onChange={(e) => setURL(e.target.value)}
              placeholder="https://ci.example.com/hooks/toft"
              spellCheck={false}
              autoComplete="off"
              className={cn(field, "font-mono text-[13px]")}
            />
            <span className="text-xs font-normal text-muted">HTTPS on a public address.</span>
          </label>
          <fieldset className="flex flex-col gap-2">
            <legend className="pb-1.5 text-[13px] font-medium text-fg">Send</legend>
            {EVENTS.map((e) => (
              <Checkbox key={e.id} checked={events.includes(e.id)} onChange={(on) => setEvents(on ? [...events, e.id] : events.filter((x) => x !== e.id))} hint={e.hint}>
                {e.label}
              </Checkbox>
            ))}
          </fieldset>
          <p className="text-xs text-muted">
            Next, Toft shows the signing secret once. A failed delivery is retried four times over about 45 minutes; any delivery can be sent again from the webhook’s
            page.
          </p>
          <ErrorLine text={err} />
          <DialogFooter>
            <button type="button" onClick={close} className="h-9 rounded-lg border border-subtle px-3 text-[13px] font-medium text-fg hover:bg-raised">
              Cancel
            </button>
            <button type="submit" disabled={!valid || create.isPending} className={primary}>
              {create.isPending ? "Adding…" : "Add webhook"}
            </button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** A signing secret, shown once. */
export function SecretDialog({ open, onClose, value, title }: { open: boolean; onClose: () => void; value: string; title: string }) {
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-[520px]">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2.5">
            <span aria-hidden="true" className="flex h-7 w-7 items-center justify-center rounded-full bg-status-running-soft text-status-running-fg">
              <Check className="h-[15px] w-[15px]" />
            </span>
            {title}
          </DialogTitle>
          <DialogDescription>
            This is the only time it’s shown. Your receiver checks that <code className="font-mono text-xs">X-Toft-Signature</code> is{" "}
            <code className="font-mono text-xs">sha256=</code> and the HMAC-SHA256 of the body with it.
          </DialogDescription>
        </DialogHeader>
        <div className="flex items-center gap-2 rounded-lg border border-strong bg-ground px-3 py-2.5">
          <code className="min-w-0 flex-1 break-all font-mono text-[13px] text-fg">{value}</code>
          <CopyButton text={value} label="Copy the signing secret" className={cn(small, "h-[30px] text-xs rounded-md")}>
            Copy
          </CopyButton>
        </div>
        <DialogFooter>
          <button type="button" onClick={onClose} className={primary}>
            I’ve copied it
          </button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
