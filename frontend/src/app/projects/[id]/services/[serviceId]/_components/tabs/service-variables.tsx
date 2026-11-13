"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Clock, MoreHorizontal, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { EnvChip } from "@/components/ui/env-chip";
import {
  VALID_KEY,
  changedSinceRead,
  envvarService,
  environmentScope,
  serviceScope,
  varsOf,
  type EnvVar,
  type VarScope,
} from "@/lib/api/envvar.service";
import { secretService } from "@/lib/api/secret.service";
import { serviceService, type Service } from "@/lib/api/service.service";
import { environmentService, type Environment, type EnvColor } from "@/lib/api/environment.service";
import type { Deployment } from "@/lib/api/deployment.service";
import { ApiError } from "@/lib/api/client";
import { LoadFailed, readFailed, ReadFailed } from "@/components/shell/unreachable";
import { focusAfterReread } from "@/lib/focus-after-reread";
import { cn } from "@/lib/utils";
import { mergeKeys, parseDotenv, pendingChanges, rebaseEdits } from "../variables-view";

// The Variables tab (board: private/design/canvas/ReworkServiceVariables.dc.html,
// D-145): everything the service's container gets, the environment's shared
// keys and its own, each row naming where it comes from; what isn't live yet;
// variables edited in place with one Save bar; write-once secrets.

const KEY = VALID_KEY;
const card = "min-w-0 overflow-hidden rounded-[10px] border border-subtle bg-surface";
const cardHead = "flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3";
const h2 = "text-[15px] font-semibold leading-[22px] text-fg";
const sum = "text-xs text-muted";
const mono = "font-mono text-xs";
const focus = "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus";
const btnSm = `touch-target inline-flex h-[30px] items-center gap-1.5 whitespace-nowrap rounded-md border border-strong bg-raised px-2.5 text-xs font-medium text-fg hover:bg-raised/70 disabled:opacity-50 ${focus}`;
const ghost = `touch-target inline-flex h-[30px] items-center gap-1.5 whitespace-nowrap rounded-md px-2.5 text-xs font-medium text-fg/80 hover:bg-raised hover:text-fg disabled:opacity-50 ${focus}`;
const priSm = `touch-target inline-flex h-[30px] items-center whitespace-nowrap rounded-md bg-accent px-2.5 text-xs font-medium text-on-accent hover:bg-accent/90 disabled:opacity-50 ${focus}`;
const input =
  "h-8 w-full rounded-md border bg-ground px-2.5 font-mono text-xs text-fg placeholder:text-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus";
// List tables: header row on wide screens, a card per row below 640 px.
const table = "w-full border-collapse text-[13px] max-sm:block sm:table-fixed [&>tbody]:max-sm:block";
const th = "border-b border-subtle px-4 py-2 text-left text-xs font-medium text-muted";
const tr = "max-sm:grid max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:gap-x-3 max-sm:gap-y-1 max-sm:border-t max-sm:border-subtle max-sm:px-4 max-sm:py-3 max-sm:first:border-t-0";
const td = "border-t border-subtle px-4 py-2.5 align-middle [tr:first-child>&]:border-t-0 max-sm:border-0 max-sm:p-0";
const tdBtn = `${td} sm:py-1.5`;
const badge = "mr-2 text-xs text-status-pending-fg";
const DOTS = "••••••••";
// The copy dialogs' fields.
const field =
  "h-8 w-full rounded-md border bg-ground px-2.5 font-mono text-xs text-fg placeholder:text-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus read-only:opacity-80";

/** "2h ago" within a day, else "2026-09-12": the boards' dates. */
export function when(iso: string | undefined, now: number): string {
  if (!iso) return "";
  const s = Math.max(0, (now - new Date(iso).getTime()) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return iso.slice(0, 10);
}

/** One of the service's own variables as edited. id is local; saved is what
 *  it held when loaded. */
interface Row {
  id: number;
  key: string;
  value: string;
  masked: boolean;
  saved?: { key: string; value: string; masked: boolean };
  removed?: boolean;
  /** A saved row opened with Edit. */
  editing?: boolean;
  /** Added with Add variable: kept at the end while its name is typed. */
  typed?: boolean;
}

// Row IDs for React keys: unique for the page's life.
let rowSeq = 0;
const newRowId = () => ++rowSeq;

// Unsaved edits per service, kept while the page is open, so switching tabs
// doesn't lose them; version is the read they were typed on.
const drafts = new Map<string, { rows: Row[]; from: EnvVar[] | undefined; version: string | undefined }>();

const toRows = (list: EnvVar[] | undefined): Row[] =>
  (list ?? []).map((v) => ({
    id: newRowId(),
    key: v.key,
    value: v.value,
    masked: !!v.masked,
    saved: { key: v.key, value: v.value, masked: !!v.masked },
  }));

const isDirty = (r: Row) =>
  !r.saved ||
  !!r.removed ||
  r.key !== r.saved.key ||
  r.value !== r.saved.value ||
  r.masked !== r.saved.masked;

/** Variables merged into the list: an existing key takes the value, a new
 *  one is added; both open as unsaved edits. */
function mergeInto(rows: Row[], incoming: { key: string; value: string; masked?: boolean }[]): Row[] {
  const next = [...rows];
  for (const v of incoming) {
    const at = next.findIndex((r) => !r.removed && r.key === v.key);
    if (at >= 0) next[at] = { ...next[at], value: v.value, masked: !!v.masked, editing: true };
    else next.push({ id: newRowId(), key: v.key, value: v.value, masked: !!v.masked, editing: true });
  }
  return next;
}

const plural = (n: number, one: string) => `${n} ${one}${n === 1 ? "" : "s"}`;

interface ServiceVariablesProps {
  service: Service;
  serviceId: string;
  projectId: string;
  /** The project's name, for who can read the variables. */
  projectName?: string;
  /** The service's environment; none for a project or workspace service. */
  env?: Environment;
  /** "secrets" opens the page at the Secrets card (old ?tab=secrets links). */
  view: "variables" | "secrets";
  canDeploy: boolean;
  /** The deployment serving now, for what isn't live yet. */
  live?: Deployment | null;
  /** Unsaved variables: the header's Deploy steps back (D-145). */
  onDirty?: (dirty: boolean) => void;
}

export function ServiceVariables({
  service,
  serviceId,
  projectId,
  projectName,
  env,
  view,
  canDeploy,
  live,
  onDirty,
}: ServiceVariablesProps) {
  const queryClient = useQueryClient();
  const scope = serviceScope(serviceId);
  const envScope = service.environment_id ? environmentScope(service.environment_id) : null;
  const name = service.name;
  const envName = env?.name ?? "the environment";

  const { data: fetched, dataUpdatedAt: varsAt, error: varsError, refetch: refetchVars } = useQuery({
    queryKey: ["envvars", scope.kind, scope.id],
    queryFn: () => envvarService.list(scope),
  });
  const vars = fetched?.data;
  const { data: secrets, error: secretsError, refetch: refetchSecrets } = useQuery({
    queryKey: ["secrets", scope.kind, scope.id],
    queryFn: () => secretService.list(scope),
  });
  const { data: envVarsData, error: envVarsError, refetch: refetchEnvVars } = useQuery({
    queryKey: ["envvars", "environment", envScope?.id ?? ""],
    queryFn: () => envvarService.list(envScope!),
    select: varsOf,
    enabled: !!envScope,
  });
  const { data: envSecretsData, error: envSecretsError, refetch: refetchEnvSecrets } = useQuery({
    queryKey: ["secrets", "environment", envScope?.id ?? ""],
    queryFn: () => secretService.list(envScope!),
    enabled: !!envScope,
  });
  const envVars = envScope ? (envVarsData ?? []) : [];
  const envSecrets = envScope ? (envSecretsData ?? []) : [];
  const now = varsAt;
  // Each card shows its list only once every read behind it has answered: a
  // failed or pending read is never an empty list, and never something to save.
  const varsReady = !!vars && (!envScope || !!envVarsData);
  const secretsReady = !!secrets && (!envScope || !!envSecretsData);
  const varsFailed = (!vars && varsError) || (envScope && !envVarsData && envVarsError) || null;
  const secretsFailed = (!secrets && secretsError) || (envScope && !envSecretsData && envSecretsError) || null;

  // ── The editable list ──────────────────────────────────────────────────────
  const [rows, setRows] = useState<Row[]>(() => drafts.get(serviceId)?.rows ?? []);
  const [syncedFrom, setSyncedFrom] = useState<EnvVar[] | undefined | null>(
    () => drafts.get(serviceId)?.from ?? null,
  );
  // The version of the read these rows came from: what Save sends. A
  // background read doesn't move it while there are edits, so a save of rows
  // from before someone else's change is refused rather than undoing it.
  const [rowsVersion, setRowsVersion] = useState<string | undefined>(() => drafts.get(serviceId)?.version);
  // Reload the rows when the saved list changes, unless there are edits.
  if (vars !== syncedFrom) {
    setSyncedFrom(vars);
    if (!rows.some(isDirty) || syncedFrom === null) {
      setRows(toRows(vars));
      setRowsVersion(fetched?.version);
    }
  }
  useEffect(() => {
    drafts.set(serviceId, { rows, from: syncedFrom ?? undefined, version: rowsVersion });
  }, [rows, syncedFrom, rowsVersion, serviceId]);

  // A name's error shows once its field has been left (a fresh row doesn't
  // start in red); a saved row's shows at once.
  const [touched, setTouched] = useState<Set<number>>(() => new Set());
  const [shown, setShown] = useState<Set<number>>(() => new Set());

  const listed = rows.filter((r) => !(r.removed && !r.saved));
  const added = listed.filter((r) => !r.saved);
  const removed = listed.filter((r) => r.removed);
  const edited = listed.filter((r) => r.saved && !r.removed && isDirty(r));
  const changes = added.length + removed.length + edited.length;
  const keyCount = (k: string) => rows.filter((r) => !r.removed && r.key === k).length;
  const ownSecretKeys = new Set((secrets ?? []).map((s) => s.key));
  const keyError = (r: Row): string => {
    if (r.removed) return "";
    if (r.key === "") return "A name is required.";
    if (!KEY.test(r.key))
      return `Use letters, digits and _, for example ${r.key
        .toUpperCase()
        .replace(/[^A-Z0-9_]/g, "_")
        .replace(/^(\d)/, "_$1")}.`;
    if (keyCount(r.key) > 1) return "Another variable has this name.";
    if (ownSecretKeys.has(r.key))
      return `${name} has a secret with this name: a key is either a variable or a secret.`;
    return "";
  };
  const invalid = rows.filter((r) => keyError(r) !== "");

  useEffect(() => {
    if (changes === 0) return;
    const handler = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", handler);
    return () => window.removeEventListener("beforeunload", handler);
  }, [changes]);
  useEffect(() => {
    onDirty?.(changes > 0);
  }, [changes, onDirty]);
  useEffect(() => () => onDirty?.(false), [onDirty]);

  // The Save refused because the variables changed since they were read.
  const [conflict, setConflict] = useState(false);
  const readAgain = async () => {
    const fresh = await refetchVars();
    if (!fresh.data || fresh.isError) {
      toast.error("Couldn’t read the variables. Try again.");
      return;
    }
    setRows((rs) => rebaseEdits(rs, toRows(fresh.data.data), isDirty));
    setRowsVersion(fresh.data.version);
    setConflict(false);
    focusAfterReread("vv-save", "vv-h");
  };
  const save = useMutation({
    mutationFn: () =>
      envvarService.bulkUpsert(
        scope,
        rowsVersion!,
        rows
          .filter((r) => !r.removed)
          .map((r) => ({ key: r.key, value: r.value, is_secret: false, masked: r.masked })),
      ),
    onSuccess: (saved) => {
      toast.success(`Variables saved: ${name} gets them at its next deploy`);
      setRows(toRows(saved.data));
      setRowsVersion(saved.version);
      setSyncedFrom(saved.data);
      setTouched(new Set());
      setConflict(false);
      queryClient.setQueryData(["envvars", scope.kind, scope.id], saved);
    },
    onError: (err: unknown) => {
      if (changedSinceRead(err)) {
        setConflict(true);
        return;
      }
      toast.error(err instanceof ApiError ? err.message : "Failed to save the variables");
    },
  });
  const update = (id: number, patch: Partial<Row>) =>
    setRows((rs) => rs.map((r) => (r.id === id ? { ...r, ...patch } : r)));
  const focusSoon = (id: string) => requestAnimationFrame(() => document.getElementById(id)?.focus());
  const addRow = () => {
    const id = newRowId();
    setRows((rs) => [...rs, { id, key: "", value: "", masked: false, typed: true }]);
    focusSoon(`var-key-${id}`);
  };
  const override = (v: EnvVar) => {
    const id = newRowId();
    setRows((rs) => [...rs, { id, key: v.key, value: v.value, masked: !!v.masked }]);
    focusSoon(`var-val-${id}`);
  };

  // ── Dialogs ────────────────────────────────────────────────────────────────
  const [copying, setCopying] = useState(false);
  const [importing, setImporting] = useState(false);
  const [copyingKeys, setCopyingKeys] = useState(false);
  // A key to add as the service's own copy, "" for a new one, null closed.
  const [addingSecret, setAddingSecret] = useState<string | null>(null);
  const [replacing, setReplacing] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<string | null>(null);
  const refreshSecrets = () =>
    queryClient.invalidateQueries({ queryKey: ["secrets", scope.kind, scope.id] });
  const removeSecret = useMutation({
    mutationFn: (key: string) => secretService.delete(scope, key),
    onSuccess: (_v, key) => {
      toast.success(`${key} deleted`);
      setDeleting(null);
      refreshSecrets();
    },
    onError: () => toast.error("Failed to delete the secret"),
  });

  // Old ?tab=secrets links land on the Secrets card.
  const secretsHeading = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    if (view !== "secrets") return;
    secretsHeading.current?.scrollIntoView({ block: "start" });
    secretsHeading.current?.focus({ preventScroll: true });
  }, [view]);

  // ── What the container gets ────────────────────────────────────────────────
  const savedVarKeys = new Set((vars ?? []).map((v) => v.key));
  const envVarKeys = new Set(envVars.map((v) => v.key));
  const envSecretKeys = new Set(envSecrets.map((s) => s.key));
  // A key is either a variable or a secret: an own key of the other kind
  // hides the shared one from its table.
  const varList = mergeKeys(
    envVars.filter((v) => !ownSecretKeys.has(v.key)),
    listed,
    (r) => !!r.typed,
  );
  const secretList = mergeKeys(
    envSecrets.filter((s) => !savedVarKeys.has(s.key)),
    secrets ?? [],
  );
  const ownKeys = new Set([...savedVarKeys, ...ownSecretKeys]);
  const overrides = [...ownKeys].filter((k) => envVarKeys.has(k) || envSecretKeys.has(k)).sort();
  const sharedUsed = [...new Set([...envVarKeys, ...envSecretKeys])].filter((k) => !ownKeys.has(k)).length;
  const total = ownKeys.size + sharedUsed;
  const variableCount = varList.filter((x) => !x.own?.removed && (x.own ? x.own.saved : true)).length;
  const secretCount = secretList.length;

  const pending = live
    ? pendingChanges({
        own: vars ?? [],
        ownSecrets: secrets ?? [],
        env: envVars,
        envSecrets,
        since: live.started_at ?? live.created_at,
        envName,
      })
    : [];

  const sourceOf = (key: string, kind: "variable" | "secret", own: boolean) => {
    if (!own) return { text: `from ${envName}`, tone: "text-muted" };
    const theirs = kind === "variable" ? envVarKeys.has(key) : envSecretKeys.has(key);
    const other = kind === "variable" ? envSecretKeys.has(key) : envVarKeys.has(key);
    if (theirs) return { text: `overrides ${envName}’s`, tone: "text-fg/85" };
    if (other) return { text: `overrides ${envName}’s ${kind === "variable" ? "secret" : "variable"}`, tone: "text-fg/85" };
    return { text: "own", tone: "text-fg/85" };
  };

  const valueText = (value: string, masked: boolean, id: number) =>
    masked && !shown.has(id) ? DOTS : value || <span className="text-muted">empty</span>;
  const showToggle = (id: number, label: string) => (
    <button
      type="button"
      className={cn("touch-target ml-2 rounded-sm font-sans text-xs text-link hover:underline", focus)}
      aria-label={`${shown.has(id) ? "Hide" : "Show"} ${label}`}
      onClick={() =>
        setShown((s) => {
          const n = new Set(s);
          if (n.has(id)) n.delete(id);
          else n.add(id);
          return n;
        })
      }
    >
      {shown.has(id) ? "Hide" : "Show"}
    </button>
  );

  // ── Render ─────────────────────────────────────────────────────────────────
  const more = (items: { label: string; onSelect: () => void }[]) => (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button type="button" className={cn(ghost, "w-[30px] justify-center px-0 pointer-coarse:min-w-11 sm:hidden")} aria-label="More actions">
          <MoreHorizontal className="h-4 w-4" aria-hidden="true" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {items.map((i) => (
          <DropdownMenuItem key={i.label} onSelect={i.onSelect}>
            {i.label}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );

  const pendingBanner = pending.length > 0 && (
    <div className="flex gap-2.5 rounded-lg border border-status-pending/25 bg-status-pending-soft/40 px-3.5 py-2.5 text-[13px] leading-[19px] text-fg/85">
      <Clock className="mt-0.5 h-[15px] w-[15px] flex-none text-status-pending-fg" aria-hidden="true" />
      <p>
        <span className="font-medium text-fg">
          {pending.length === 1 ? "1 change isn’t live yet:" : `${pending.length} changes aren’t live yet:`}
        </span>{" "}
        {pending.slice(0, 5).map((p, i) => (
          <span key={p.key + p.text}>
            {i > 0 && "; "}
            <span className={mono}>{p.key}</span> {p.text}
            {p.by ? `, by ${p.by}` : ""} {when(p.at, now)}
          </span>
        ))}
        {pending.length > 5 && `; and ${pending.length - 5} more`}. The next deploy of {name} applies{" "}
        {pending.length === 1 ? "it" : "them"}. <span className="text-muted">Removed keys aren’t listed.</span>
      </p>
    </div>
  );

  return (
    <div className="flex flex-wrap items-start gap-6">
      <div className="flex min-w-0 flex-[999_1_560px] flex-col gap-4">
        <p className="text-[13px] leading-5 text-fg/85">
          {env ? (
            <>
              Everything {name}&apos;s container gets in{" "}
              <EnvChip env={{ name: env.name, color: env.color as EnvColor, protected: env.protected }} className="align-[-3px]" />: the
              environment&apos;s shared variables and secrets, and {name}&apos;s own. An own key with the same name
              wins. Changes apply on {name}&apos;s next deploy.
            </>
          ) : (
            <>
              Everything {name}&apos;s container gets. {name} is a{" "}
              {service.scope === "org" ? "workspace" : "project"} service, so no environment&apos;s shared
              variables reach it. Changes apply on {name}&apos;s next deploy.
            </>
          )}
        </p>
        {pendingBanner}

        {/* Variables */}
        <section className={card} aria-labelledby="vv-h">
          <div className={cardHead}>
            <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1">
              <h2 id="vv-h" tabIndex={-1} className={h2}>
                Variables
              </h2>
              <span className={sum}>
                {variableCount > 0 && `${variableCount} · `}plain text, anyone in {projectName ?? "the workspace"} can
                read them
              </span>
            </div>
            {canDeploy && varsReady && (
              <div className="flex flex-wrap items-center gap-2">
                <button type="button" className={cn(ghost, "max-sm:hidden")} onClick={() => setCopying(true)}>
                  Copy from…
                </button>
                <button type="button" className={cn(ghost, "max-sm:hidden")} onClick={() => setImporting(true)}>
                  Import .env
                </button>
                <button type="button" className={btnSm} onClick={addRow}>
                  Add variable
                </button>
                {more([
                  { label: "Copy from…", onSelect: () => setCopying(true) },
                  { label: "Import .env", onSelect: () => setImporting(true) },
                ])}
              </div>
            )}
          </div>
          {varsFailed ? (
            <div className="p-3">
              <ReadFailed
                heading="h2"
                error={varsFailed}
                onRetry={() => {
                  if (!vars) void refetchVars();
                  if (envScope && !envVarsData) void refetchEnvVars();
                }}
              />
            </div>
          ) : !varsReady ? (
            <div className="mx-4 my-3.5 h-8 rounded-md bg-raised motion-safe:animate-pulse" role="status" aria-label="Loading the variables" />
          ) : varList.length === 0 ? (
            <p className="px-4 py-3.5 text-[13px] text-fg/70">
              {name} gets no variables yet. Add one for a value only {name} needs
              {env ? `, or set shared ones on ${envName}` : ""}.
            </p>
          ) : (
            <table className={table}>
              <thead className="max-sm:hidden">
                <tr>
                  <th scope="col" className={cn(th, "w-[30%]")}>
                    Key
                  </th>
                  <th scope="col" className={th}>
                    Value
                  </th>
                  <th scope="col" className={cn(th, "w-[170px]")}>
                    Source
                  </th>
                  <th scope="col" className={cn(th, "w-[150px]")}>
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {varList.map(({ key, shared, own: r }) => {
                  if (!r) {
                    const v = shared!;
                    const src = sourceOf(key, "variable", false);
                    return (
                      <tr key={`shared-${key}`} className={tr}>
                        <td className={cn(tdBtn, mono, "text-fg [overflow-wrap:anywhere] max-sm:col-start-1 max-sm:row-start-1")}>
                          {key}
                        </td>
                        <td className={cn(tdBtn, mono, "text-fg/85 [overflow-wrap:anywhere] max-sm:col-span-2 max-sm:row-start-2")}>
                          {valueText(v.value, !!v.masked, -1)}
                        </td>
                        <td className={cn(tdBtn, "text-xs [overflow-wrap:anywhere] max-sm:col-span-2 max-sm:row-start-3", src.tone)}>
                          {src.text}
                        </td>
                        <td className={cn(tdBtn, "whitespace-nowrap text-right max-sm:col-start-2 max-sm:row-start-1")}>
                          {canDeploy && (
                            <button type="button" className={ghost} aria-label={`Override ${key}`} onClick={() => override(v)}>
                              Override
                            </button>
                          )}
                        </td>
                      </tr>
                    );
                  }
                  const touchedOrSaved = !!r.saved || touched.has(r.id);
                  const err = touchedOrSaved ? keyError(r) : "";
                  const isNew = !r.saved;
                  const open = canDeploy && !r.removed && (isNew || !!r.editing);
                  const dirty = !isNew && !r.removed && isDirty(r);
                  const valueDirty = !!r.saved && !r.removed && r.value !== r.saved.value;
                  const label = r.key || "the new variable";
                  const src = sourceOf(r.key, "variable", true);
                  const errId = `var-err-${r.id}`;
                  return (
                    <tr
                      key={r.id}
                      className={cn(tr, (isNew || dirty) && "bg-status-pending-soft/20", r.removed && "opacity-60")}
                    >
                      <td className={cn(tdBtn, "max-sm:col-start-1 max-sm:row-start-1")}>
                        {open ? (
                          <div className="flex flex-col gap-1">
                            <label htmlFor={`var-key-${r.id}`} className="sr-only">
                              Key
                            </label>
                            <input
                              id={`var-key-${r.id}`}
                              value={r.key}
                              spellCheck={false}
                              placeholder="NAME"
                              aria-invalid={!!err}
                              aria-describedby={err ? errId : undefined}
                              onChange={(e) => update(r.id, { key: e.target.value })}
                              onBlur={() => setTouched((t) => new Set(t).add(r.id))}
                              className={cn(input, err ? "border-status-failed" : "border-strong/70")}
                            />
                            {err && (
                              <p id={errId} className="text-xs text-status-failed-fg">
                                {err}
                              </p>
                            )}
                          </div>
                        ) : (
                          <div className="flex flex-col gap-1">
                            <span className={cn(mono, "text-fg [overflow-wrap:anywhere]", r.removed && "line-through")}>{r.key}</span>
                            {err && <p className="text-xs text-status-failed-fg">{err}</p>}
                          </div>
                        )}
                      </td>
                      <td className={cn(tdBtn, "max-sm:col-span-2 max-sm:row-start-2")}>
                        {open ? (
                          <div className="flex flex-col gap-1">
                            <label htmlFor={`var-val-${r.id}`} className="sr-only">
                              Value of {label}
                            </label>
                            <div className="flex flex-col items-start gap-1">
                              <input
                                id={`var-val-${r.id}`}
                                type={r.masked ? "password" : "text"}
                                value={r.value}
                                spellCheck={false}
                                autoComplete="off"
                                onChange={(e) => update(r.id, { value: e.target.value })}
                                className={cn(input, valueDirty ? "border-status-pending" : "border-strong/70")}
                              />
                              <label className="touch-target flex flex-none items-center gap-1.5 text-xs text-muted">
                                <input
                                  type="checkbox"
                                  checked={r.masked}
                                  onChange={(e) => update(r.id, { masked: e.target.checked })}
                                  className="h-4 w-4 accent-[var(--link)]"
                                />
                                Masked
                              </label>
                            </div>
                            {valueDirty && !r.masked && !r.saved!.masked && (
                              <p className="text-xs text-status-pending-fg">Saved value: {r.saved!.value || "empty"}</p>
                            )}
                          </div>
                        ) : (
                          <span className={cn(mono, "text-fg/85 [overflow-wrap:anywhere]", r.removed && "line-through")}>
                            {valueText(r.value, r.masked, r.id)}
                            {r.masked && showToggle(r.id, r.key)}
                          </span>
                        )}
                        {shared && (
                          <span className="mt-0.5 block text-[11px] text-muted [overflow-wrap:anywhere]">
                            {envName}&apos;s: {shared.masked ? DOTS : shared.value || "empty"}
                          </span>
                        )}
                      </td>
                      <td className={cn(tdBtn, "text-xs [overflow-wrap:anywhere] max-sm:col-span-2 max-sm:row-start-3", src.tone)}>
                        {src.text}
                      </td>
                      <td className={cn(tdBtn, "whitespace-nowrap text-right max-sm:col-start-2 max-sm:row-start-1")}>
                        {!canDeploy ? null : r.removed ? (
                          <>
                            <span className={badge}>Removed</span>
                            <button
                              type="button"
                              className={ghost}
                              aria-label={`Undo removing ${r.key}`}
                              onClick={() => update(r.id, { removed: false })}
                            >
                              Undo
                            </button>
                          </>
                        ) : open ? (
                          <>
                            {(isNew || dirty) && <span className={badge}>Not saved</span>}
                            <button
                              type="button"
                              className={ghost}
                              aria-label={`Remove ${label}`}
                              onClick={() =>
                                isNew ? setRows((rs) => rs.filter((x) => x.id !== r.id)) : update(r.id, { removed: true })
                              }
                            >
                              Remove
                            </button>
                          </>
                        ) : (
                          <button
                            type="button"
                            className={ghost}
                            aria-label={`Edit ${r.key}`}
                            onClick={() => {
                              update(r.id, { editing: true });
                              focusSoon(`var-val-${r.id}`);
                            }}
                          >
                            Edit
                          </button>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
          {conflict && (
            <div role="alert" className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-t border-subtle bg-status-failed-soft px-4 py-2.5 text-[13px] text-status-failed-fg">
              <p className="min-w-0 flex-[1_1_360px]">
                These variables changed since you opened this page, so nothing was saved. What you typed is still here.
              </p>
              <button type="button" className={btnSm} onClick={() => void readAgain()}>
                Read them again
              </button>
            </div>
          )}
          {canDeploy && varsReady && rowsVersion && changes > 0 && (
            <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-t border-subtle px-4 py-2.5 text-[13px] text-fg/70">
              <p className="min-w-0 flex-[1_1_360px]">
                <span className="font-medium text-fg">
                  {changes === 1 ? "1 unsaved change:" : `${changes} unsaved changes:`}
                </span>{" "}
                {[
                  ...added.map((r) => [r.key || "a new variable", "added"]),
                  ...edited.map((r) => [r.saved!.key, "changed"]),
                  ...removed.map((r) => [r.saved!.key, "removed"]),
                ].map(([k, what], i) => (
                  <span key={i}>
                    {i > 0 && ", "}
                    <span className={cn(mono, "[overflow-wrap:anywhere]")}>{k}</span> {what}
                  </span>
                ))}
                .{" "}
                {invalid.length > 0
                  ? `Can’t save until ${invalid.map((r) => r.key || "the unnamed row").join(", ")} ${invalid.length === 1 ? "is" : "are"} fixed.`
                  : `${name} gets ${changes === 1 ? "it" : "them"} on its next deploy.`}
              </p>
              <span className="flex items-center gap-2">
                <button
                  type="button"
                  className={ghost}
                  onClick={() => {
                    setRows(toRows(vars));
                    setRowsVersion(fetched?.version);
                    setConflict(false);
                    setTouched(new Set());
                  }}
                  disabled={save.isPending}
                >
                  Cancel
                </button>
                <button
                  id="vv-save"
                  type="button"
                  className={priSm}
                  onClick={() => save.mutate()}
                  disabled={invalid.length > 0 || save.isPending || conflict}
                >
                  {save.isPending ? "Saving…" : "Save variables"}
                </button>
              </span>
            </div>
          )}
        </section>

        {/* Secrets */}
        <section className={card} aria-labelledby="vs-h">
          <div className={cardHead}>
            <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1">
              <h2 id="vs-h" ref={secretsHeading} tabIndex={-1} className={cn(h2, "focus:outline-none")}>
                Secrets
              </h2>
              <span className={sum}>
                {secretCount > 0 && `${secretCount} · `}write-once: nobody can read a value back
              </span>
            </div>
            {canDeploy && secretsReady && (
              <div className="flex flex-wrap items-center gap-2">
                <button type="button" className={cn(ghost, "max-sm:hidden")} onClick={() => setCopyingKeys(true)}>
                  Copy keys from…
                </button>
                <button type="button" className={btnSm} onClick={() => setAddingSecret("")}>
                  Add secret
                </button>
                {more([{ label: "Copy keys from…", onSelect: () => setCopyingKeys(true) }])}
              </div>
            )}
          </div>
          {secretsFailed ? (
            <div className="p-3">
              <ReadFailed
                heading="h2"
                error={secretsFailed}
                onRetry={() => {
                  if (!secrets) void refetchSecrets();
                  if (envScope && !envSecretsData) void refetchEnvSecrets();
                }}
              />
            </div>
          ) : !secretsReady ? (
            <div className="mx-4 my-3.5 h-8 rounded-md bg-raised motion-safe:animate-pulse" role="status" aria-label="Loading the secrets" />
          ) : secretList.length === 0 ? (
            <p className="px-4 py-3.5 text-[13px] text-fg/70">
              {name} gets no secrets. A secret is for a value nobody should read back, such as an API key: it is
              stored encrypted and set in the container at the deploy.
            </p>
          ) : (
            <table className={table}>
              <thead className="max-sm:hidden">
                <tr>
                  <th scope="col" className={cn(th, "w-[30%]")}>
                    Key
                  </th>
                  <th scope="col" className={th}>
                    Set
                  </th>
                  <th scope="col" className={cn(th, "w-[170px]")}>
                    Source
                  </th>
                  <th scope="col" className={cn(th, "w-[150px]")}>
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {secretList.map(({ key, shared, own }) => {
                  const s = (own ?? shared)!;
                  const src = sourceOf(key, "secret", !!own);
                  return (
                    <tr key={key} className={tr}>
                      <td className={cn(tdBtn, mono, "text-fg [overflow-wrap:anywhere] max-sm:col-start-1 max-sm:row-start-1")}>
                        {key}
                      </td>
                      <td className={cn(tdBtn, "text-[13px] text-fg/70 max-sm:col-span-2 max-sm:row-start-2")}>
                        {when(s.updated_at, now)}
                        {s.updated_by && ` · ${s.updated_by}`}
                      </td>
                      <td className={cn(tdBtn, "text-xs [overflow-wrap:anywhere] max-sm:col-span-2 max-sm:row-start-3", src.tone)}>
                        {src.text}
                      </td>
                      <td className={cn(tdBtn, "whitespace-nowrap text-right max-sm:col-start-2 max-sm:row-start-1")}>
                        {canDeploy && (
                          <span className="inline-flex items-center gap-1">
                            <button
                              type="button"
                              className={ghost}
                              aria-label={`Replace ${key}`}
                              onClick={() => (own ? setReplacing(key) : setAddingSecret(key))}
                            >
                              Replace…
                            </button>
                            {own && (
                              <button
                                type="button"
                                aria-label={`Delete ${key}`}
                                onClick={() => setDeleting(key)}
                                className={cn(ghost, "w-[30px] justify-center px-0 text-muted hover:text-status-failed-fg pointer-coarse:min-w-11")}
                              >
                                <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
                              </button>
                            )}
                          </span>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
          {env && secretsReady && secretList.some((x) => !x.own) && (
            <p className="border-t border-subtle px-4 py-2.5 text-xs text-muted">
              Replacing one of {envName}&apos;s here makes {name}&apos;s own copy; the others keep {envName}&apos;s.
            </p>
          )}
        </section>
      </div>

      <aside className={cn("flex min-w-0 flex-[1_1_300px] flex-col gap-6", !(varsReady && secretsReady) && "hidden")}>
        <section aria-labelledby="vh-h" className={card}>
          <div className="border-b border-subtle px-4 py-3">
            <h2 id="vh-h" className={h2}>
              Where they come from
            </h2>
          </div>
          <dl className="text-[13px]">
            {[
              ...(env
                ? [[`from ${envName}`, `${envName}’s shared variables and secrets, which every service there gets. ${sharedUsed} of these ${total}.`]]
                : []),
              ["own", `Set here, for ${name} only. ${ownKeys.size - overrides.length}.`],
              ...(env
                ? [[
                    "overrides",
                    `${name}’s own value for a key ${envName} also sets. ${overrides.length}${overrides.length ? `: ${overrides.join(", ")}` : ""}.`,
                  ]]
                : []),
            ].map(([k, v], i) => (
              <div key={k} className={cn("grid grid-cols-[110px_minmax(0,1fr)] gap-3 px-4 py-2", i > 0 && "border-t border-subtle")}>
                <dt className="text-muted">{k}</dt>
                <dd className="text-fg [overflow-wrap:anywhere]">{v}</dd>
              </div>
            ))}
          </dl>
          <div className="flex flex-col gap-1 border-t border-subtle px-4 py-3 text-[13px] text-muted">
            {env && service.environment_id ? (
              <>
                <span>Shared ones are edited on the environment</span>
                <Link
                  href={`/projects/${projectId}/environments/${service.environment_id}`}
                  className="text-link hover:underline pointer-coarse:inline-flex pointer-coarse:min-h-11 pointer-coarse:items-center"
                >
                  {envName}&apos;s variables
                </Link>
              </>
            ) : (
              <span>
                {name} is a {service.scope === "org" ? "workspace" : "project"} service, so no environment&apos;s
                shared variables reach it.
              </span>
            )}
          </div>
        </section>
      </aside>

      <CopyFromDialog
        open={copying}
        onClose={() => setCopying(false)}
        name={name}
        projectId={projectId}
        serviceId={serviceId}
        current={new Map(listed.filter((r) => !r.removed).map((r) => [r.key, r.value]))}
        onMerge={(incoming) => {
          setRows((rs) => mergeInto(rs, incoming));
          setCopying(false);
          toast.success(`${plural(incoming.length, "variable")} copied: review and save`);
        }}
      />
      <ImportEnvDialog
        open={importing}
        onClose={() => setImporting(false)}
        name={name}
        current={new Map(listed.filter((r) => !r.removed).map((r) => [r.key, r.value]))}
        secretKeys={ownSecretKeys}
        onMerge={(incoming) => {
          setRows((rs) => mergeInto(rs, incoming));
          setImporting(false);
          toast.success(`${plural(incoming.length, "variable")} imported: review and save`);
        }}
      />
      <AddSecretDialog
        open={addingSecret !== null}
        fixedKey={addingSecret || undefined}
        envName={envName}
        onClose={() => setAddingSecret(null)}
        name={name}
        serviceId={serviceId}
        taken={new Set([...ownSecretKeys, ...savedVarKeys])}
        onDone={refreshSecrets}
      />
      <ReplaceSecretDialog keyName={replacing} onClose={() => setReplacing(null)} name={name} serviceId={serviceId} onDone={refreshSecrets} />
      <CopyKeysDialog
        open={copyingKeys}
        onClose={() => setCopyingKeys(false)}
        name={name}
        projectId={projectId}
        serviceId={serviceId}
        have={ownSecretKeys}
        onDone={refreshSecrets}
      />
      <Dialog
        open={!!deleting}
        onOpenChange={(o) => {
          if (!o && !removeSecret.isPending) setDeleting(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {deleting}?</DialogTitle>
            <DialogDescription>
              {name} keeps using it until its next deploy, which starts without it.
              {deleting && envSecretKeys.has(deleting) ? ` From then ${envName}'s ${deleting} applies.` : ""} The value
              can&apos;t be recovered.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleting(null)} disabled={removeSecret.isPending}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={() => deleting && removeSecret.mutate(deleting)}
              disabled={removeSecret.isPending}
            >
              {removeSecret.isPending ? "Deleting…" : "Delete secret"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

/** Import .env: paste a file or pick one; what it adds, changes and refuses
 *  is listed before anything joins the unsaved list. */
function ImportEnvDialog({
  open,
  onClose,
  name,
  current,
  secretKeys,
  onMerge,
}: {
  open: boolean;
  onClose: () => void;
  name: string;
  current: Map<string, string>;
  secretKeys: Set<string>;
  onMerge: (vars: { key: string; value: string }[]) => void;
}) {
  const [text, setText] = useState("");
  const parsed = parseDotenv(text);
  const asSecret = parsed.vars.filter((v) => secretKeys.has(v.key)).map((v) => v.key);
  const usable = parsed.vars.filter((v) => !secretKeys.has(v.key) && current.get(v.key) !== v.value);
  const adds = usable.filter((v) => !current.has(v.key));
  const changes = usable.filter((v) => current.has(v.key));
  const close = () => {
    onClose();
    setText("");
  };
  const list = (label: string, keys: string[]) =>
    keys.length > 0 && (
      <p>
        <span className="text-fg">{label}:</span> <span className={mono}>{keys.join(", ")}</span>
      </p>
    );
  return (
    <Dialog open={open} onOpenChange={(o) => !o && close()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Import .env into {name}</DialogTitle>
          <DialogDescription>
            Paste the file or pick it. The variables join the unsaved list, for you to review and save.
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (usable.length) onMerge(usable);
            setText("");
          }}
        >
          <label htmlFor="dotenv-text" className="text-[13px] font-medium text-fg">
            Contents
          </label>
          <textarea
            id="dotenv-text"
            value={text}
            onChange={(e) => setText(e.target.value)}
            rows={8}
            spellCheck={false}
            placeholder={"NODE_ENV=production\nPORT=8080"}
            className={cn(input, "h-auto border-strong/70 py-2")}
          />
          <label className="flex flex-col gap-1 text-xs text-muted">
            Or pick a file
            <input
              type="file"
              accept=".env,text/plain"
              className="text-xs text-fg file:mr-2 file:rounded-md file:border file:border-strong file:bg-raised file:px-2 file:py-1 file:text-xs file:text-fg"
              onChange={async (e) => {
                const f = e.target.files?.[0];
                if (f) setText(await f.text());
              }}
            />
          </label>
          {text.trim() && (
            <div className="flex flex-col gap-1 text-[13px] text-fg/70" aria-live="polite">
              {list("Adds", adds.map((v) => v.key))}
              {list("Changes", changes.map((v) => v.key))}
              {list("Refused, not a variable name", parsed.refused)}
              {list(`Refused, ${name} has a secret with this name`, asSecret)}
              {usable.length === 0 && <p>Nothing new to add.</p>}
            </div>
          )}
          <DialogFooter className="mt-1">
            <Button type="button" variant="outline" onClick={close}>
              Cancel
            </Button>
            <Button type="submit" disabled={usable.length === 0}>
              {usable.length ? `Add ${plural(usable.length, "variable")}` : "Add"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export function CopyFromDialog({
  open,
  onClose,
  name,
  projectId,
  serviceId,
  current,
  onMerge,
}: {
  open: boolean;
  onClose: () => void;
  name: string;
  projectId: string;
  serviceId: string;
  current: Map<string, string>;
  onMerge: (vars: EnvVar[]) => void;
}) {
  const [source, setSource] = useState("");
  // A workspace service (no project) copies from the other workspace services.
  const servicesRead = useQuery({
    queryKey: projectId
      ? ["project-services", projectId]
      : ["org-scoped-services"],
    queryFn: () =>
      projectId
        ? serviceService.listForProject(projectId)
        : serviceService.listOrgScoped(),
    enabled: open,
    staleTime: 30_000,
  });
  const { data: services } = servicesRead;
  const { data: environments } = useQuery({
    queryKey: ["environments", projectId],
    queryFn: () => environmentService.list(projectId),
    enabled: open && !!projectId,
    staleTime: 60_000,
  });
  const others = (services ?? []).filter((s) => s.id !== serviceId);
  const sourceScope = serviceScope(source);
  const theirsRead = useQuery({
    queryKey: ["envvars", sourceScope.kind, sourceScope.id],
    queryFn: () => envvarService.list(sourceScope),
    select: varsOf,
    enabled: open && !!source,
  });
  const { data: theirs } = theirsRead;
  const sourceName = others.find((s) => s.id === source)?.name ?? "";
  const newOnes = (theirs ?? []).filter((v) => !current.has(v.key));
  const replacing = (theirs ?? []).filter(
    (v) => current.has(v.key) && current.get(v.key) !== v.value,
  );
  const same = (theirs ?? []).length - newOnes.length - replacing.length;
  const envOf = (envId?: string, scope?: string) =>
    envId
      ? (environments?.find((e) => e.id === envId)?.name ?? "")
      : scope === "project"
        ? "project-wide"
        : "workspace";

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (!o) {
          onClose();
          setSource("");
        }
      }}
    >
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Copy variables from another service</DialogTitle>
          <DialogDescription>
            Adds its variables to this list, unsaved. Keys {name} already has
            take the copied value. Secrets are never copied.
          </DialogDescription>
        </DialogHeader>
        <label
          htmlFor="copy-source"
          className="text-[13px] font-medium text-fg"
        >
          Service
        </label>
        <select
          id="copy-source"
          value={source}
          onChange={(e) => setSource(e.target.value)}
          className="h-9 w-full rounded-md border border-strong/70 bg-ground px-2.5 text-[13px] text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
        >
          <option value="">Choose a service…</option>
          {others.map((s) => (
            <option key={s.id} value={s.id}>
              {s.name} · {envOf(s.environment_id, s.scope)}
            </option>
          ))}
        </select>
        {readFailed(servicesRead) && (
          <LoadFailed what="the services to copy from" onRetry={() => void servicesRead.refetch()} />
        )}
        {source && readFailed(theirsRead) && (
          <LoadFailed what={`the variables of ${sourceName}`} onRetry={() => void theirsRead.refetch()} />
        )}
        {source && theirs && (
          <p className="text-[13px] leading-5 text-fg/85">
            {theirs.length === 0 ? (
              <>{sourceName} has no variables.</>
            ) : (
              <>
                {sourceName} has {theirs.length}{" "}
                {theirs.length === 1 ? "variable" : "variables"}:{" "}
                {newOnes.length} new to {name}
                {replacing.length > 0 && (
                  <>
                    , {replacing.length} replace {name}&apos;s values (
                    <span className={mono}>
                      {replacing.map((v) => v.key).join(", ")}
                    </span>
                    )
                  </>
                )}
                {same > 0 && <>, {same} already the same</>}.
              </>
            )}
          </p>
        )}
        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => {
              onClose();
              setSource("");
            }}
          >
            Cancel
          </Button>
          <Button
            disabled={!theirs || newOnes.length + replacing.length === 0}
            onClick={() => {
              onMerge([...newOnes, ...replacing]);
              setSource("");
            }}
          >
            Copy {newOnes.length + replacing.length || ""}{" "}
            {newOnes.length + replacing.length === 1 ? "variable" : "variables"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ─── Secrets ──────────────────────────────────────────────────────────────────

function AddSecretDialog({
  open,
  onClose,
  name,
  serviceId,
  taken,
  fixedKey,
  envName,
  onDone,
}: {
  open: boolean;
  onClose: () => void;
  name: string;
  serviceId: string;
  taken: Set<string>;
  /** Replacing the environment's secret: the service's own copy of this key. */
  fixedKey?: string;
  envName: string;
  onDone: () => void;
}) {
  const [typedKey, setKey] = useState("");
  const key = fixedKey ?? typedKey;
  const [value, setValue] = useState("");
  const keyErr =
    key && !KEY.test(key)
      ? "Letters, digits and _, not starting with a digit."
      : key && taken.has(key)
        ? `${name} already has a variable or secret named ${key}.`
        : "";
  const create = useMutation({
    mutationFn: () => secretService.create(serviceScope(serviceId), key, value),
    onSuccess: () => {
      toast.success(`${key} added: ${name} gets it at its next deploy`);
      close();
      onDone();
    },
    onError: (err: unknown) =>
      toast.error(
        err instanceof ApiError ? err.message : "Failed to add the secret",
      ),
  });
  const close = () => {
    onClose();
    setKey("");
    setValue("");
  };
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (!o && !create.isPending) close();
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {fixedKey ? `Replace ${fixedKey} for ${name}` : `Add a secret to ${name}`}
          </DialogTitle>
          <DialogDescription>
            {fixedKey
              ? `This makes ${name}'s own ${fixedKey}; ${envName}'s stays as it is for the other services. `
              : ""}
            Write-only: once saved, nobody can read the value back, including
            you. It is set in {name}&apos;s container on the next deploy.
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (key && value && !keyErr) create.mutate();
          }}
        >
          <div className="flex flex-col gap-1.5">
            <label
              htmlFor="secret-key"
              className="text-[13px] font-medium text-fg"
            >
              Key
            </label>
            <Input
              id="secret-key"
              autoFocus={!fixedKey}
              readOnly={!!fixedKey}
              value={key}
              onChange={(e) => setKey(e.target.value)}
              spellCheck={false}
              aria-invalid={!!keyErr}
              aria-describedby="secret-key-help"
              className="font-mono"
            />
            <p
              id="secret-key-help"
              className={cn(
                "text-xs",
                keyErr ? "text-status-failed-fg" : "text-muted",
              )}
            >
              {keyErr || "Letters, digits and _, not starting with a digit."}
            </p>
          </div>
          <div className="flex flex-col gap-1.5">
            <label
              htmlFor="secret-value"
              className="text-[13px] font-medium text-fg"
            >
              Value
            </label>
            <Input
              id="secret-value"
              autoFocus={!!fixedKey}
              type="password"
              autoComplete="off"
              value={value}
              onChange={(e) => setValue(e.target.value)}
              className="font-mono"
            />
          </div>
          <DialogFooter className="mt-1">
            <Button
              type="button"
              variant="outline"
              onClick={close}
              disabled={create.isPending}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              disabled={!key || !value || !!keyErr || create.isPending}
            >
              {create.isPending ? "Saving…" : fixedKey ? "Replace secret" : "Add secret"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export function ReplaceSecretDialog({
  keyName,
  onClose,
  name,
  serviceId,
  target,
  onDone,
}: {
  keyName: string | null;
  onClose: () => void;
  name: string;
  serviceId: string;
  /** Whose secret; the service by default. */
  target?: VarScope;
  onDone: () => void;
}) {
  const [value, setValue] = useState("");
  const replace = useMutation({
    mutationFn: () =>
      secretService.replace(target ?? serviceScope(serviceId), keyName!, value),
    onSuccess: () => {
      toast.success(`${keyName} replaced: ${name} gets it at its next deploy`);
      close();
      onDone();
    },
    onError: (err: unknown) =>
      toast.error(
        err instanceof ApiError ? err.message : "Failed to replace the secret",
      ),
  });
  const close = () => {
    onClose();
    setValue("");
  };
  return (
    <Dialog
      open={!!keyName}
      onOpenChange={(o) => {
        if (!o && !replace.isPending) close();
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Replace {keyName}</DialogTitle>
          <DialogDescription>
            The old value is overwritten and can&apos;t be recovered. {name}{" "}
            keeps the old one until its next deploy.
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (value) replace.mutate();
          }}
        >
          <label
            htmlFor="secret-new"
            className="text-[13px] font-medium text-fg"
          >
            New value
          </label>
          <Input
            id="secret-new"
            autoFocus
            type="password"
            autoComplete="off"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            className="font-mono"
          />
          <DialogFooter className="mt-1">
            <Button
              type="button"
              variant="outline"
              onClick={close}
              disabled={replace.isPending}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={!value || replace.isPending}>
              {replace.isPending ? "Replacing…" : "Replace secret"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export function CopyKeysDialog({
  open,
  onClose,
  name,
  projectId,
  serviceId,
  target,
  have,
  onDone,
}: {
  open: boolean;
  onClose: () => void;
  name: string;
  projectId: string;
  /** The service left out of the choices ("" none). */
  serviceId: string;
  /** Where the secrets go; the service by default. */
  target?: VarScope;
  have: Set<string>;
  onDone: () => void;
}) {
  const [source, setSource] = useState("");
  const [values, setValues] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  // A workspace service (no project) copies from the other workspace services.
  const servicesRead = useQuery({
    queryKey: projectId
      ? ["project-services", projectId]
      : ["org-scoped-services"],
    queryFn: () =>
      projectId
        ? serviceService.listForProject(projectId)
        : serviceService.listOrgScoped(),
    enabled: open,
    staleTime: 30_000,
  });
  const { data: services } = servicesRead;
  const others = (services ?? []).filter((s) => s.id !== serviceId);
  const sourceScope = serviceScope(source);
  const theirsRead = useQuery({
    queryKey: ["secrets", sourceScope.kind, sourceScope.id],
    queryFn: () => secretService.list(sourceScope),
    enabled: open && !!source,
  });
  const { data: theirs } = theirsRead;
  const missing = useMemo(
    () => (theirs ?? []).filter((s) => !have.has(s.key)),
    [theirs, have],
  );
  const filled = missing.filter((s) => values[s.key]);
  const close = () => {
    onClose();
    setSource("");
    setValues({});
  };
  const create = async () => {
    setBusy(true);
    let done = 0;
    try {
      for (const s of filled) {
        await secretService.create(
          target ?? serviceScope(serviceId),
          s.key,
          values[s.key],
        );
        done++;
      }
      toast.success(
        `${done} ${done === 1 ? "secret" : "secrets"} added to ${name}`,
      );
      close();
    } catch (err) {
      toast.error(
        `${done} added, then: ${err instanceof ApiError ? err.message : "a secret could not be added"}`,
      );
    } finally {
      setBusy(false);
      onDone();
    }
  };
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (!o && !busy) close();
      }}
    >
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Copy secret keys from another service</DialogTitle>
          <DialogDescription>
            Values can&apos;t be read back, so only the keys are copied: type
            each value. Keys {name} already has are left out.
          </DialogDescription>
        </DialogHeader>
        <select
          aria-label="Service"
          value={source}
          onChange={(e) => {
            setSource(e.target.value);
            setValues({});
          }}
          className="h-9 w-full rounded-md border border-strong/70 bg-ground px-2.5 text-[13px] text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
        >
          <option value="">Choose a service…</option>
          {others.map((s) => (
            <option key={s.id} value={s.id}>
              {s.name}
            </option>
          ))}
        </select>
        {readFailed(servicesRead) && (
          <LoadFailed what="the services to copy from" onRetry={() => void servicesRead.refetch()} />
        )}
        {source && readFailed(theirsRead) && (
          <LoadFailed what="that service’s secret keys" onRetry={() => void theirsRead.refetch()} />
        )}
        {source &&
          theirs &&
          (missing.length === 0 ? (
            <p className="text-[13px] text-muted">
              {name} already has every secret key this service has.
            </p>
          ) : (
            <div className="flex max-h-72 flex-col gap-2 overflow-y-auto pr-1">
              {missing.map((s) => (
                <label
                  key={s.key}
                  className="grid grid-cols-[minmax(0,1fr)_minmax(0,1.3fr)] items-center gap-2"
                >
                  <span className="truncate font-mono text-xs text-fg">
                    {s.key}
                  </span>
                  <input
                    type="password"
                    autoComplete="off"
                    placeholder="value, or leave empty to skip"
                    value={values[s.key] ?? ""}
                    onChange={(e) =>
                      setValues((v) => ({ ...v, [s.key]: e.target.value }))
                    }
                    className={cn(field, "border-strong/70")}
                  />
                </label>
              ))}
            </div>
          ))}
        <DialogFooter>
          <Button variant="outline" onClick={close} disabled={busy}>
            Cancel
          </Button>
          <Button disabled={!theirs || filled.length === 0 || busy} onClick={create}>
            {busy
              ? "Adding…"
              : `Add ${filled.length || ""} ${filled.length === 1 ? "secret" : "secrets"}`}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
