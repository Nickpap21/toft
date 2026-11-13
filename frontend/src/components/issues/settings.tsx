"use client";

import { useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Archive, ArchiveRestore, MoreHorizontal, Plus } from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { ApiError, changedSinceRead } from "@/lib/api/client";
import { focusAfterReread } from "@/lib/focus-after-reread";
import {
  issueService,
  issueTypeService,
  issueTypesQuery,
  type IssueType,
  type IssueTypeRef,
  type ProjectIssueSettingsRead,
} from "@/lib/api/issue.service";
import { cn } from "@/lib/utils";
import { joinNames } from "@/components/previews/view";
import { btnSm, card, field, ghostSm, h2, help, label, primarySm, saveBar, small, sum } from "@/app/projects/_lib/ui";
import { CardHead } from "@/components/settings/settings-frame";
import { LoadFailed, readFailed } from "@/components/shell/unreachable";
import { TYPE_COLORS, TYPE_ICONS, TypeIcon } from "./deep";

// Where issues are set up (D-83): the workspace's types and digits, and a
// project's key, digits and types.

const err = (e: unknown, fallback: string) =>
  e instanceof ApiError ? e.message : fallback;
const example = (key: string, digits: number) =>
  `${key || "KEY"}-${"1".padStart(Math.max(digits, 1), "0")}`;

/** Project settings → Issues: the key (renamed with the old one kept), the
 *  digits, the types it uses and its default type. Owners change it. */
export function ProjectIssuesCard({
  projectId,
  isOwner,
}: {
  projectId: string;
  isOwner: boolean;
}) {
  const qc = useQueryClient();
  const svc = issueService(projectId);
  const settings = useQuery({
    queryKey: ["issue-settings", projectId],
    queryFn: () => svc.settings(),
  });
  const types = useQuery(issueTypesQuery);
  // Once a save has been refused the form stays as typed, whatever a
  // background read brings, until a save goes through: the key stops
  // following the data.
  const [heldKey, setHeldKey] = useState<string | null>(null);
  if (!settings.data && settings.error)
    return (
      <div className={cn(card, "px-5 py-4")}>
        <LoadFailed what="the issue settings" onRetry={() => void settings.refetch()} />
      </div>
    );
  if (!types.data && types.error)
    return (
      <div className={cn(card, "px-5 py-4")}>
        <LoadFailed what="the issue types" onRetry={() => void types.refetch()} />
      </div>
    );
  if (!settings.data || !types.data)
    return (
      <div
        role="status"
        aria-label="Loading"
        className="h-56 rounded-[10px] bg-surface motion-safe:animate-pulse"
      />
    );
  const formKey = heldKey ?? JSON.stringify(settings.data);
  return (
    <Form
      key={formKey}
      s={settings.data}
      types={types.data.filter((t) => !t.archived)}
      isOwner={isOwner}
      onConflict={() => setHeldKey(formKey)}
      onCancel={() => setHeldKey(null)}
      reread={async () => {
        const fresh = await settings.refetch();
        return fresh.isError ? undefined : fresh.data;
      }}
      save={(input) =>
        svc.saveSettings(input).then((out) => {
          setHeldKey(null);
          qc.setQueryData(["issue-settings", projectId], out);
          qc.invalidateQueries({ queryKey: ["issues", projectId] });
          qc.invalidateQueries({ queryKey: ["project", projectId] });
          return out;
        })
      }
    />
  );
}

function Form({
  s,
  types,
  isOwner,
  onConflict,
  onCancel,
  reread,
  save,
}: {
  s: ProjectIssueSettingsRead;
  types: IssueType[];
  isOwner: boolean;
  onConflict: () => void;
  onCancel: () => void;
  reread: () => Promise<ProjectIssueSettingsRead | undefined>;
  save: (
    input: Parameters<ReturnType<typeof issueService>["saveSettings"]>[0],
  ) => Promise<ProjectIssueSettingsRead>;
}) {
  const [key, setKey] = useState(s.key);
  const [digits, setDigits] = useState<string>(
    s.inherit_digits ? "inherit" : String(s.digits),
  );
  const [hidden, setHidden] = useState<string[]>(s.hidden_type_ids);
  const [def, setDef] = useState(s.default_type_id ?? "");
  const [failures, setFailures] = useState(s.failed_deploy_issues);
  // The read the hidden types were chosen on: a save of them sends its
  // version, and a new read keeps what was changed since it.
  const [read, setRead] = useState(s);
  const [conflict, setConflict] = useState(false);
  const shown = digits === "inherit" ? s.workspace_digits : Number(digits);
  const hiddenChanged = hidden.join() !== s.hidden_type_ids.join();
  const changed =
    key !== s.key ||
    digits !== (s.inherit_digits ? "inherit" : String(s.digits)) ||
    def !== (s.default_type_id ?? "") ||
    hiddenChanged ||
    failures !== s.failed_deploy_issues;
  const mutate = useMutation({
    mutationFn: () => {
      const input = {
        ...(key !== s.key ? { key } : {}),
        digits: digits === "inherit" ? null : Number(digits),
        default_type_id: def || null,
        failed_deploy_issues: failures,
      };
      return save(hiddenChanged ? { ...input, hidden_type_ids: hidden, version: read.version } : input);
    },
    onSuccess: () => toast.success("Issue settings saved"),
    onError: (e) => {
      if (changedSinceRead(e)) {
        setConflict(true);
        onConflict();
        return;
      }
      toast.error(err(e, "Couldn't save the issue settings"));
    },
  });
  const readAgain = async () => {
    const fresh = await reread();
    if (!fresh) {
      toast.error("Couldn’t read the settings. Try again.");
      return;
    }
    const added = hidden.filter((t) => !read.hidden_type_ids.includes(t));
    const removed = read.hidden_type_ids.filter((t) => !hidden.includes(t));
    setHidden([
      ...fresh.hidden_type_ids.filter((t) => !removed.includes(t)),
      ...added.filter((t) => !fresh.hidden_type_ids.includes(t)),
    ]);
    // A field left alone here takes the new read; one changed here stays.
    const digitsOf = (r: ProjectIssueSettingsRead) => (r.inherit_digits ? "inherit" : String(r.digits));
    if (key === read.key) setKey(fresh.key);
    if (digits === digitsOf(read)) setDigits(digitsOf(fresh));
    if (def === (read.default_type_id ?? "")) setDef(fresh.default_type_id ?? "");
    if (failures === read.failed_deploy_issues) setFailures(fresh.failed_deploy_issues);
    setRead(fresh);
    setConflict(false);
    focusAfterReread("iss-save", "s-issues");
  };
  const keyErr =
    key === s.key
      ? ""
      : !key
        ? "Its issues need a key."
        : /^[A-Z][A-Z0-9]{1,9}$/.test(key)
          ? ""
          : "2 to 10 letters or digits, starting with a letter.";
  const changes = [
    key !== s.key ? "Key" : "",
    digits !== (s.inherit_digits ? "inherit" : String(s.digits)) ? "Digits" : "",
    hiddenChanged ? "Types" : "",
    def !== (s.default_type_id ?? "") ? "New issues start as" : "",
    failures !== s.failed_deploy_issues ? "Open an issue when a deploy fails" : "",
  ].filter(Boolean);
  return (
    <form
      id="s-issues"
      tabIndex={-1}
      aria-labelledby="iss-h"
      className={cn(card, "focus:outline-none")}
      onSubmit={(e) => {
        e.preventDefault();
        if (changed && !keyErr && !conflict) mutate.mutate();
      }}
    >
      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3">
        <h2 id="iss-h" className={h2}>
          Issues
        </h2>
        <span className={sum}>
          read{" "}
          <span className="font-mono">
            {example(key.toUpperCase(), shown)}
          </span>
          {s.next_key && (
            <>
              {" "}
              · the next is{" "}
              <span className="font-mono">{s.next_key}</span>
            </>
          )}
        </span>
      </div>
      <div className="flex flex-col gap-3.5 p-4">
        <div className="grid gap-3.5 sm:grid-cols-2">
          <div className="flex min-w-0 flex-col gap-1.5">
            <label htmlFor="iss-key" className={label}>
              Key
            </label>
            <input
              id="iss-key"
              value={key}
              disabled={!isOwner}
              maxLength={10}
              placeholder="Set by the first issue"
              aria-describedby="iss-key-help"
              aria-invalid={!!keyErr || undefined}
              onChange={(e) =>
                setKey(e.target.value.toUpperCase().replace(/[^A-Z0-9]/g, ""))
              }
              className={cn(field, "font-mono text-[13px] uppercase", keyErr && "border-status-failed")}
            />
            <p id="iss-key-help" className={cn(help, keyErr && "text-status-failed-fg")}>
              {keyErr || "2 to 10 letters or digits, starting with a letter."}
              {s.aliases.length > 0 && (
                <> Former keys still work: {s.aliases.join(", ")}.</>
              )}
              {s.key && key && key !== s.key && (
                <> {s.key}-… keeps working too.</>
              )}
            </p>
          </div>
          <div className="flex min-w-0 flex-col gap-1.5">
            <label htmlFor="iss-digits" className={label}>
              Digits
            </label>
            <select
              id="iss-digits"
              value={digits}
              disabled={!isOwner}
              aria-describedby="iss-digits-help"
              onChange={(e) => setDigits(e.target.value)}
              className={cn(field, "font-mono text-[13px]")}
            >
              <option value="inherit">
                The workspace’s (
                {example(key.toUpperCase(), s.workspace_digits)})
              </option>
              {[0, 1, 2, 3, 4, 5, 6].map((d) => (
                <option key={d} value={d}>
                  {d === 0 ? "No padding" : `${d} digits`} (
                  {example(key.toUpperCase(), d)})
                </option>
              ))}
            </select>
            <p id="iss-digits-help" className={help}>
              Only how keys look: branches name them with or without zeros.
            </p>
          </div>
        </div>
        <fieldset className="flex min-w-0 flex-col">
          <legend className={cn(label, "mb-1.5 font-normal")}>Types it uses</legend>
          <span className="flex flex-wrap gap-1.5">
            {types.map((t) => {
              const on = !hidden.includes(t.id);
              return (
                <label
                  key={t.id}
                  className={cn(
                    "touch-target inline-flex min-h-8 cursor-pointer items-center gap-2 rounded-lg border px-2.5 text-[13px] has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-focus",
                    on ? "border-strong text-fg" : "border-subtle text-muted",
                    !isOwner && "cursor-default",
                  )}
                >
                  <input
                    type="checkbox"
                    checked={on}
                    disabled={!isOwner}
                    onChange={(e) =>
                      setHidden((h) =>
                        e.target.checked
                          ? h.filter((x) => x !== t.id)
                          : [...h, t.id],
                      )
                    }
                    className="h-3.5 w-3.5 accent-[var(--accent)] focus-visible:outline-none"
                  />
                  {t.name}
                </label>
              );
            })}
          </span>
        </fieldset>
        <div className="grid gap-3.5 sm:grid-cols-2">
          <div className="flex min-w-0 flex-col gap-1.5">
            <label htmlFor="iss-def" className={label}>
              New issues start as
            </label>
            <select
              id="iss-def"
              value={def}
              disabled={!isOwner}
              onChange={(e) => setDef(e.target.value)}
              className={field}
            >
              <option value="">
                The workspace’s first (
                {types.find((t) => t.kind === "standard")?.name ?? "Task"})
              </option>
              {types
                .filter((t) => t.kind !== "subtask" && !hidden.includes(t.id))
                .map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.name}
                  </option>
                ))}
            </select>
          </div>
        </div>
        <div className="flex items-center justify-between gap-4">
          <div className="flex min-w-0 flex-col">
            <span id="iss-fail-t" className="text-[13px] font-medium text-fg">
              Open an issue when a deploy fails
            </span>
            <span id="iss-fail-d" className={help}>
              One per service while its deploys keep failing: a high-priority
              Bug labelled with the service. Further failures add to it, and it
              moves to Shipped when a deploy of the service goes live.
            </span>
          </div>
          <button
            type="button"
            role="switch"
            aria-checked={failures}
            aria-labelledby="iss-fail-t"
            aria-describedby="iss-fail-d"
            disabled={!isOwner}
            onClick={() => setFailures(!failures)}
            className={cn(
              "relative h-5 w-9 flex-none rounded-full transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-60",
              "before:absolute before:-inset-x-1 before:-inset-y-3 before:content-['']",
              failures ? "bg-accent" : "bg-strong",
            )}
          >
            <span
              className={cn(
                "absolute left-[3px] top-[3px] h-3.5 w-3.5 rounded-full transition-[translate]",
                failures ? "translate-x-4 bg-on-accent" : "bg-fg/70",
              )}
            />
          </button>
        </div>
      </div>
      {conflict && (
        <div role="alert" className={cn(saveBar, "bg-status-failed-soft text-status-failed-fg")}>
          <span>
            These settings changed since you opened this page, so nothing was saved. What you chose is still here.
          </span>
          <button type="button" className={btnSm} onClick={() => void readAgain()}>
            Read them again
          </button>
        </div>
      )}
      {isOwner && changed && (
        <div className={saveBar}>
          <span>
            Unsaved {changes.length === 1 ? "change" : "changes"} to{" "}
            {joinNames(changes)}
          </span>
          <span className="flex items-center gap-2">
            <button
              type="button"
              className={ghostSm}
              disabled={mutate.isPending}
              onClick={() => {
                setKey(s.key);
                setDigits(s.inherit_digits ? "inherit" : String(s.digits));
                setHidden(s.hidden_type_ids);
                setDef(s.default_type_id ?? "");
                setFailures(s.failed_deploy_issues);
                setRead(s);
                setConflict(false);
                onCancel();
              }}
            >
              Cancel
            </button>
            <button
              id="iss-save"
              type="submit"
              disabled={!!keyErr || mutate.isPending || conflict}
              className={primarySm}
            >
              {mutate.isPending ? "Saving…" : "Save"}
            </button>
          </span>
        </div>
      )}
      {!isOwner && (
        <p className={saveBar}>
          Owners change the key, digits, types and failed deploy issues.
        </p>
      )}
    </form>
  );
}

/** Settings → Workspace: issue types and the digits keys show by default. */
export function WorkspaceIssuesCard({ isOwner }: { isOwner: boolean }) {
  const qc = useQueryClient();
  const types = useQuery(issueTypesQuery);
  const settings = useQuery({
    queryKey: ["workspace-issue-settings"],
    queryFn: () => issueTypeService.settings(),
  });
  const refresh = () =>
    qc.invalidateQueries({ queryKey: issueTypesQuery.queryKey });
  const digits = useMutation({
    mutationFn: (d: number) => issueTypeService.saveSettings({ digits: d }),
    onSuccess: (out) => {
      qc.setQueryData(["workspace-issue-settings"], out);
      toast.success("Digits saved");
    },
    onError: (e) => toast.error(err(e, "Couldn't save the digits")),
  });
  const update = useMutation({
    mutationFn: ({
      id,
      ...input
    }: {
      id: string;
      name?: string;
      icon?: IssueTypeRef["icon"];
      color?: IssueTypeRef["color"];
      archived?: boolean;
    }) => issueTypeService.update(id, input),
    onSuccess: refresh,
    onError: (e) => toast.error(err(e, "Couldn't change the type")),
  });
  const [draft, setDraft] = useState<{
    name: string;
    kind: IssueTypeRef["kind"];
    icon: IssueTypeRef["icon"];
    color: IssueTypeRef["color"];
  }>({ name: "", kind: "standard", icon: "chore", color: "gray" });
  const add = useMutation({
    mutationFn: () => issueTypeService.add(draft),
    onSuccess: () => {
      refresh();
      setDraft({ name: "", kind: "standard", icon: "chore", color: "gray" });
    },
    onError: (e) => toast.error(err(e, "Couldn't add the type")),
  });
  const [adding, setAdding] = useState(false);
  const renaming = useRef<string | null>(null);
  const live = (types.data ?? []).filter((t) => !t.archived).length;
  return (
    <section aria-labelledby="wiss-h" className={card}>
      <CardHead id="wiss-h" title="Issues" sum={`${types.data ? live : "—"} ${live === 1 ? "type" : "types"} every project can use`}>
        {isOwner && (
          <button type="button" aria-expanded={adding} aria-controls="add-type" onClick={() => setAdding(!adding)} className={btnSm}>
            Add type
          </button>
        )}
      </CardHead>
      {readFailed(settings) ? (
        <div className="px-4 pt-4">
          <LoadFailed what="the key format" onRetry={() => void settings.refetch()} />
        </div>
      ) : (
      <div className="flex max-w-[352px] flex-col gap-1.5 px-4 pb-1.5 pt-4">
        <label htmlFor="ws-digits" className={label}>
          Keys look like
        </label>
        <select
          id="ws-digits"
          value={settings.data?.digits ?? 3}
          disabled={!isOwner || !settings.data}
          onChange={(e) => digits.mutate(Number(e.target.value))}
          aria-describedby="ws-digits-help"
          className={field}
        >
          {[0, 1, 2, 3, 4, 5, 6].map((d) => (
            <option key={d} value={d}>
              {example("VLB", d)} ({d === 0 ? "no padding" : d === 1 ? "1 digit" : `${d} digits`})
            </option>
          ))}
        </select>
        <p id="ws-digits-help" className={help}>
          A project can choose its own.
        </p>
      </div>
      )}
      {readFailed(types) ? (
        <div className="px-4 py-3">
          <LoadFailed what="the issue types" onRetry={() => void types.refetch()} />
        </div>
      ) : (
      <table className="w-full border-collapse text-[13px] max-sm:block">
        <thead className="max-sm:hidden">
          <tr>
            <th scope="col" className="w-[38%] px-4 py-2 text-left text-xs font-medium text-muted">
              Type
            </th>
            <th scope="col" className="px-4 py-2 text-left text-xs font-medium text-muted">
              Behaviour
            </th>
            <th scope="col" className="w-14 px-4 py-2">
              <span className="sr-only">Actions</span>
            </th>
          </tr>
        </thead>
        <tbody className="max-sm:block">
          {(types.data ?? []).map((t) => (
            <tr
              key={t.id}
              className="border-t border-subtle max-sm:grid max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:items-center max-sm:gap-x-3 max-sm:px-4 max-sm:py-1.5"
            >
              <td className="px-4 py-2 align-middle max-sm:p-0">
                <span className="flex items-center gap-1.5">
                  <DropdownMenu>
                    <DropdownMenuTrigger
                      disabled={!isOwner}
                      aria-label={`${t.name}'s icon and colour`}
                      className="touch-target flex-none rounded focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
                    >
                      <TypeIcon type={t} size={20} />
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="start" className="w-56">
                      <div className="grid grid-cols-5 gap-1 p-1">
                        {TYPE_ICONS.map((icon) => (
                          <DropdownMenuItem
                            key={icon}
                            onSelect={() => update.mutate({ id: t.id, icon })}
                            className="justify-center p-1.5"
                          >
                            <TypeIcon type={{ ...t, icon }} size={18} />
                          </DropdownMenuItem>
                        ))}
                      </div>
                      <div className="grid grid-cols-7 gap-1 border-t border-subtle p-1">
                        {TYPE_COLORS.map((color) => (
                          <DropdownMenuItem
                            key={color}
                            onSelect={() => update.mutate({ id: t.id, color })}
                            aria-label={color}
                            className="justify-center p-1.5"
                          >
                            <TypeIcon type={{ ...t, color }} size={16} />
                          </DropdownMenuItem>
                        ))}
                      </div>
                    </DropdownMenuContent>
                  </DropdownMenu>
                  <input
                    id={`type-${t.id}`}
                    aria-label={`${t.name}'s name`}
                    defaultValue={t.name}
                    disabled={!isOwner}
                    maxLength={40}
                    onBlur={(e) => {
                      // An emptied name isn't saved: show the saved one again.
                      if (!e.target.value.trim()) e.target.value = t.name;
                      else if (e.target.value !== t.name) update.mutate({ id: t.id, name: e.target.value });
                    }}
                    className={cn(
                      "h-8 min-w-0 flex-1 rounded-md border border-transparent bg-transparent px-1.5 text-[13px] font-medium text-fg hover:border-subtle focus-visible:border-strong focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:hover:border-transparent",
                      t.archived && "text-muted",
                    )}
                  />
                </span>
              </td>
              <td
                className={cn(
                  "px-4 py-2 align-middle text-muted max-sm:col-span-2 max-sm:row-start-2 max-sm:p-0 max-sm:pb-1",
                  t.kind === "standard" && !t.archived && "max-sm:hidden",
                )}
              >
                {[t.kind === "container" ? "Holds issues, ships with them" : t.kind === "subtask" ? "Always has a parent" : "", t.archived ? "Archived" : ""]
                  .filter(Boolean)
                  .join(" · ")}
              </td>
              <td className="px-4 py-2 text-right align-middle max-sm:col-start-2 max-sm:row-start-1 max-sm:p-0">
                {isOwner && (
                  <DropdownMenu>
                    <DropdownMenuTrigger
                      aria-label={`Actions for ${t.name}: rename, ${t.archived ? "restore" : "archive"}`}
                      className="touch-target inline-flex h-9 w-9 items-center justify-center rounded-lg text-fg/70 hover:bg-raised hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus data-[state=open]:bg-raised max-sm:h-11 max-sm:w-11"
                    >
                      <MoreHorizontal className="h-4 w-4" aria-hidden="true" />
                    </DropdownMenuTrigger>
                    <DropdownMenuContent
                      align="end"
                      onCloseAutoFocus={(e) => {
                        // Rename hands focus to the name, not back to ⋯.
                        if (renaming.current !== t.id) return;
                        e.preventDefault();
                        renaming.current = null;
                        document.getElementById(`type-${t.id}`)?.focus();
                      }}
                    >
                      <DropdownMenuItem onSelect={() => (renaming.current = t.id)}>Rename</DropdownMenuItem>
                      <DropdownMenuItem onSelect={() => update.mutate({ id: t.id, archived: !t.archived })}>
                        {t.archived ? (
                          <ArchiveRestore className="h-3.5 w-3.5" aria-hidden="true" />
                        ) : (
                          <Archive className="h-3.5 w-3.5" aria-hidden="true" />
                        )}
                        {t.archived ? "Restore" : "Archive"}
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      )}
      {isOwner && adding && (
        <form
          id="add-type"
          className="flex flex-wrap items-center gap-2 border-t border-subtle px-4 py-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (draft.name.trim()) add.mutate();
          }}
        >
          <TypeIcon type={{ ...draft, name: draft.name || "New type" }} size={20} />
          <input
            autoFocus
            aria-label="New type's name"
            placeholder="New type, as Incident"
            value={draft.name}
            maxLength={40}
            onChange={(e) => setDraft({ ...draft, name: e.target.value })}
            className={cn(field, "h-8 w-48")}
          />
          <select
            aria-label="What it does"
            value={draft.kind}
            onChange={(e) =>
              setDraft({
                ...draft,
                kind: e.target.value as IssueTypeRef["kind"],
              })
            }
            className={cn(field, "h-8 w-auto")}
          >
            <option value="standard">Standard</option>
            <option value="container">Holds issues</option>
            <option value="subtask">Sub-task</option>
          </select>
          <select
            aria-label="Icon"
            value={draft.icon}
            onChange={(e) =>
              setDraft({
                ...draft,
                icon: e.target.value as IssueTypeRef["icon"],
              })
            }
            className={cn(field, "h-8 w-auto")}
          >
            {TYPE_ICONS.map((i) => (
              <option key={i} value={i}>
                {i}
              </option>
            ))}
          </select>
          <select
            aria-label="Colour"
            value={draft.color}
            onChange={(e) =>
              setDraft({
                ...draft,
                color: e.target.value as IssueTypeRef["color"],
              })
            }
            className={cn(field, "h-8 w-auto")}
          >
            {TYPE_COLORS.map((c) => (
              <option key={c} value={c}>
                {c}
              </option>
            ))}
          </select>
          <button type="submit" disabled={!draft.name.trim() || add.isPending} className={small}>
            <Plus className="h-3.5 w-3.5" aria-hidden="true" />
            Add type
          </button>
        </form>
      )}
      {!isOwner && <p className={saveBar}>Owners change the types and how keys look.</p>}
    </section>
  );
}
