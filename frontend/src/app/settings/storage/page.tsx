"use client";

import { useState } from "react";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, Plus } from "lucide-react";
import { useAuth, useOrgRole } from "@/context/AuthContext";
import { ApiError } from "@/lib/api/client";
import {
  projectsQuery,
  serversQuery,
  workspaceServicesQuery,
} from "@/lib/queries";
import { sinceShort } from "@/lib/time";
import { cn } from "@/lib/utils";
import { placeable } from "@/components/servers/agent";
import {
  DEFAULT_SIZE_LIMIT_BYTES,
  sharedVolumeService,
  type SharedVolume,
} from "@/lib/api/sharedvolume.service";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  SectionHead,
  SettingsFrame,
  cancelButton,
  card,
  dangerButton,
  dialogContent,
  dialogFooter,
  dialogHeader,
  dialogText,
  dialogTitle,
  fieldInput,
  fieldLabel,
  help,
  primaryButton,
  smallButton,
} from "@/components/settings/settings-frame";

const QUERY_KEY = ["shared-volumes"];
const GIB = 1024 ** 3;
const NAME = /^[a-z0-9][a-z0-9-]{0,39}$/;

const gib = (bytes: number) => (bytes / GIB).toFixed(1);
const message = (err: unknown, fallback: string) =>
  err instanceof ApiError && err.status < 500 ? err.message : fallback;
const validLimit = (g: string) => Number(g) > 0;

/** Settings → Storage (board: Storage): the workspace's shared volumes
 *  (D-38). Owners create, resize and delete them; every member sees them. */
export default function StorageSettingsPage() {
  const { user } = useAuth();
  const isOwner = useOrgRole() === "owner";
  const [now] = useState(() => Date.now());
  const volumes = useQuery({
    queryKey: QUERY_KEY,
    queryFn: () => sharedVolumeService.list(),
  });
  const services = useQuery(workspaceServicesQuery);
  const projects = useQuery(projectsQuery);
  const [creating, setCreating] = useState(false);
  const [resizing, setResizing] = useState<SharedVolume | null>(null);
  const [deleting, setDeleting] = useState<SharedVolume | null>(null);

  /** "storefront / web", and the service's Volumes card. */
  const mountOf = (serviceId: string, fallback: string) => {
    const s = services.data?.find((x) => x.id === serviceId);
    const project = s?.project_id
      ? projects.data?.find((p) => p.id === s.project_id)
      : undefined;
    return {
      label: project ? `${project.name} / ${s?.name}` : (s?.name ?? fallback),
      href: s?.project_id
        ? `/projects/${s.project_id}/services/${s.id}?tab=settings#set-storage`
        : undefined,
    };
  };

  const owners = "Workspace owners create them.";
  return (
    <SettingsFrame
      title="Storage"
      gap="gap-6"
      action={
        isOwner && (
          <button
            type="button"
            className={primaryButton}
            onClick={() => setCreating(true)}
          >
            <Plus className="h-[15px] w-[15px]" aria-hidden="true" />
            New shared volume
          </button>
        )
      }
    >
      <p className="max-w-[900px] text-[13px] leading-[19px] text-fg/70">
        Shared volumes hold files that several services use, such as uploaded
        images. Each belongs to {user?.orgName} and lives on one server. Any of
        the workspace’s services on that server can mount it, read-write or
        read-only, from{" "}
        <Link href="/projects" className="inline-link">a service’s Settings → Volumes</Link>.{" "}
        <span className="text-fg">
          A server’s nightly backup includes its volumes, when it has one (
          <Link href="/settings/backups" className="inline-link">
            Settings → Backups
          </Link>
          ).
        </span>
      </p>

      <section aria-labelledby="vol-h" className="flex flex-col gap-3">
        <SectionHead
          id="vol-h"
          title="Shared volumes"
          count={volumes.data?.length ?? "–"}
          note="Usage is measured every 15 minutes."
        />
        {volumes.isLoading ? (
          <div
            className={cn(card, "h-44 motion-safe:animate-pulse")}
            aria-label="Loading the shared volumes"
          />
        ) : volumes.error ? (
          <div
            role="alert"
            className="flex flex-wrap items-center gap-2.5 rounded-[10px] border border-danger/35 bg-surface px-4 py-3.5 text-[13px]"
          >
            <AlertTriangle
              className="h-4 w-4 flex-none text-status-failed"
              aria-hidden="true"
            />
            <span className="flex-[1_1_320px] text-status-failed-fg">
              Could not load the shared volumes:{" "}
              {message(volumes.error, "the panel did not answer")}. Your volumes
              and their data are not affected.
            </span>
            <button
              type="button"
              className={smallButton}
              onClick={() => volumes.refetch()}
            >
              Retry
            </button>
          </div>
        ) : volumes.data?.length === 0 ? (
          <div className="flex flex-wrap items-center gap-x-6 gap-y-3 rounded-[10px] border border-dashed border-subtle bg-surface px-4 py-5">
            <div className="flex min-w-0 flex-[1_1_420px] flex-col gap-0.5">
              <span className="font-medium text-fg">No shared volumes yet</span>
              <span className="text-[13px] leading-[19px] text-fg/70">
                {isOwner
                  ? "Create one when two services need the same files, for example uploads that web serves and a worker resizes."
                  : `${owners} Ask one when two services need the same files.`}
              </span>
            </div>
            {isOwner && (
              <button
                type="button"
                className={cn(smallButton, "h-8 bg-surface text-[13px] rounded-lg")}
                onClick={() => setCreating(true)}
              >
                <Plus className="h-3.5 w-3.5" aria-hidden="true" />
                New shared volume
              </button>
            )}
          </div>
        ) : (
          volumes.data?.map((v) => (
            <VolumeCard
              key={v.id}
              v={v}
              now={now}
              isOwner={isOwner}
              mountOf={mountOf}
              onResize={() => setResizing(v)}
              onDelete={() => setDeleting(v)}
            />
          ))
        )}
      </section>

      <section
        aria-labelledby="lim-h"
        className={cn(card, "flex flex-col gap-2 p-4")}
      >
        <h2
          id="lim-h"
          className="text-[15px] font-semibold leading-[22px] text-fg"
        >
          About the size limit
        </h2>
        <ul className="flex max-w-[900px] list-disc flex-col gap-1 pl-[18px] text-[13px] leading-[19px] text-fg/70">
          <li>
            It is a warning, not a cap: nothing stops an app writing past it.
          </li>
          <li>
            At 80% of the limit the workspace’s alert webhook says so (
            <Link href="/settings/alerts" className="inline-link">
              Alerts
            </Link>
            ). Past 100% the volume shows here in red.
          </li>
          <li>
            For files the whole internet reads, such as user uploads,
            S3-compatible object storage scales further than one server’s disk.
          </li>
        </ul>
      </section>

      {isOwner && (
        <>
          <CreateDialog open={creating} onClose={() => setCreating(false)} />
          <LimitDialog target={resizing} onClose={() => setResizing(null)} />
          <DeleteDialog target={deleting} onClose={() => setDeleting(null)} />
        </>
      )}
    </SettingsFrame>
  );
}

interface VolumeCardProps {
  v: SharedVolume;
  now: number;
  isOwner: boolean;
  mountOf: (
    serviceId: string,
    fallback: string,
  ) => { label: string; href?: string };
  onResize: () => void;
  onDelete: () => void;
}

function VolumeCard({
  v,
  now,
  isOwner,
  mountOf,
  onResize,
  onDelete,
}: VolumeCardProps) {
  const measured = v.used_bytes !== null;
  const pct = measured
    ? Math.round(((v.used_bytes ?? 0) / v.size_limit_bytes) * 100)
    : 0;
  const over = pct > 100;
  const warn = pct >= 80;
  const mounted = v.mounts.length > 0;
  const tone = over
    ? "text-status-failed-fg"
    : warn
      ? "text-status-pending-fg"
      : "text-muted";
  const when = v.used_at ? ` · measured ${sinceShort(v.used_at, now)}` : "";
  const note = !measured
    ? "Not measured yet: Docker creates it when a service that mounts it is deployed."
    : over
      ? `${pct}% · over its limit, which nothing enforces${when}`
      : warn
        ? `${pct}% · past the 80% alert line${when}`
        : `${pct}%${when}`;
  const docker = `paas-shared-${v.id}`;

  return (
    <article
      aria-labelledby={`vol-${v.id}`}
      className={cn(card, "flex flex-col gap-3.5 p-4")}
    >
      <div className="flex flex-wrap items-center gap-2.5">
        <h3
          id={`vol-${v.id}`}
          className="font-mono text-[15px] font-medium leading-[22px] text-fg"
        >
          {v.name}
        </h3>
        <span className="rounded-full border border-subtle px-2 font-mono text-xs leading-[22px] text-fg/85">
          {v.server_id ? v.server_name : "panel host"}
        </span>
        <span className="text-xs text-muted" title={docker}>
          Docker volume{" "}
          <span className="font-mono">{docker.slice(0, 20)}…</span>
        </span>
        <span className="flex-1" />
        {isOwner && (
          <span className="inline-flex flex-wrap items-center gap-2">
            {mounted && (
              <span id={`why-${v.id}`} className="text-xs text-muted">
                Unmount it everywhere to delete it
              </span>
            )}
            <button
              type="button"
              className={smallButton}
              aria-label={`Change limit of ${v.name}`}
              onClick={onResize}
            >
              Change limit
            </button>
            <button
              type="button"
              className={cn(smallButton, mounted && "cursor-not-allowed")}
              disabled={mounted}
              aria-describedby={mounted ? `why-${v.id}` : undefined}
              aria-label={`Delete ${v.name}`}
              onClick={onDelete}
            >
              Delete
            </button>
          </span>
        )}
      </div>

      <div className="flex max-w-[640px] flex-col gap-1.5">
        <div className="flex justify-between gap-2 text-xs leading-5">
          <span className="text-fg/70">Used</span>
          <span className="font-mono text-fg">
            {measured ? gib(v.used_bytes ?? 0) : "–"} of{" "}
            {gib(v.size_limit_bytes)} GiB
          </span>
        </div>
        <div
          role="img"
          aria-label={
            measured
              ? `${v.name}: ${pct}% of its limit used`
              : `${v.name}: not measured yet`
          }
          className="flex h-2.5 overflow-hidden rounded-xs bg-raised"
        >
          <span
            className={
              over
                ? "bg-status-failed"
                : warn
                  ? "bg-status-pending"
                  : "bg-muted"
            }
            style={{ width: `${Math.min(100, pct)}%` }}
          />
        </div>
        <span className={cn("text-xs leading-5", tone)}>{note}</span>
      </div>

      <div className="flex flex-wrap items-center gap-2 border-t border-subtle pt-3 text-xs text-fg/70">
        <span className="text-muted">Mounted by</span>
        {v.mounts.map((m) => {
          const where = mountOf(m.service_id, m.service_name);
          const inner = (
            <>
              {where.label}
              <span className="font-mono text-fg/70">{m.target}</span>
              <span className="text-muted">
                {m.read_only ? "read-only" : "read-write"}
              </span>
            </>
          );
          const chip =
            "inline-flex min-h-6 min-w-0 items-center gap-2 rounded-md border border-subtle px-2 py-0.5 text-fg [overflow-wrap:anywhere]";
          return where.href ? (
            <Link
              key={m.service_id + m.target}
              href={where.href}
              className={cn(chip, "hover:border-strong")}
            >
              {inner}
            </Link>
          ) : (
            <span key={m.service_id + m.target} className={chip}>
              {inner}
            </span>
          );
        })}
        {!mounted && <span>No service mounts it yet.</span>}
      </div>
    </article>
  );
}

function CreateDialog({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const servers = useQuery({ ...serversQuery, select: placeable, enabled: open });
  const [name, setName] = useState("");
  const [serverId, setServerId] = useState<string | null>(null);
  const [limit, setLimit] = useState(String(DEFAULT_SIZE_LIMIT_BYTES / GIB));
  // The workspace's servers, then the panel host (which every workspace may use).
  const options = [
    ...(servers.data ?? []).map((s) => ({
      id: s.id,
      label: s.shared ? `${s.name} (shared by ${s.shared_by})` : s.name,
    })),
    { id: "", label: "panel host (the control-plane server)" },
  ];
  const server = serverId ?? options[0]?.id ?? "";
  const create = useMutation({
    mutationFn: () =>
      sharedVolumeService.create({
        name,
        server_id: server,
        size_limit_bytes: Math.round(Number(limit) * GIB),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: QUERY_KEY });
      toast.success(`${name} created`);
      close();
    },
  });
  const close = () => {
    onClose();
    setName("");
    setServerId(null);
    setLimit(String(DEFAULT_SIZE_LIMIT_BYTES / GIB));
    create.reset();
  };
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => !o && !create.isPending && close()}
    >
      <DialogContent className={cn(dialogContent, "max-w-[480px]")}>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate();
          }}
        >
          <DialogHeader className={dialogHeader}>
            <DialogTitle className={dialogTitle}>New shared volume</DialogTitle>
            <DialogDescription className={dialogText}>
              Docker creates it on the server when the first service that mounts
              it is deployed.
            </DialogDescription>
          </DialogHeader>
          <div className="mx-5 mt-4 flex flex-col gap-4">
            <div className="flex flex-col gap-1.5">
              <label htmlFor="nv-name" className={fieldLabel}>
                Name
              </label>
              <input
                id="nv-name"
                required
                value={name}
                onChange={(e) => setName(e.target.value.toLowerCase())}
                aria-describedby="nv-name-help"
                className={cn(fieldInput, "font-mono")}
              />
              <span
                id="nv-name-help"
                className={cn(
                  help,
                  name && !NAME.test(name) && "text-status-failed-fg",
                )}
              >
                Lowercase letters, digits and dashes, up to 40. Services refer
                to it by this name.
              </span>
            </div>
            <div className="flex flex-col gap-1.5">
              <label htmlFor="nv-server" className={fieldLabel}>
                Server
              </label>
              <select
                id="nv-server"
                value={server}
                onChange={(e) => setServerId(e.target.value)}
                aria-describedby="nv-server-help"
                className={fieldInput}
              >
                {options.map((o) => (
                  <option key={o.id || "panel"} value={o.id}>
                    {o.label}
                  </option>
                ))}
              </select>
              <span id="nv-server-help" className={help}>
                Only services on this server can mount it. It can’t move later.
              </span>
            </div>
            <div className="flex flex-col gap-1.5">
              <label htmlFor="nv-limit" className={fieldLabel}>
                Size limit, GiB
              </label>
              <input
                id="nv-limit"
                inputMode="decimal"
                required
                value={limit}
                onChange={(e) => setLimit(e.target.value)}
                className={cn(fieldInput, "w-32")}
              />
            </div>
          </div>
          {create.error && (
            <p
              role="alert"
              className="mx-5 mt-3 rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg"
            >
              {message(create.error, "The volume was not created.")}
            </p>
          )}
          <div className={dialogFooter}>
            <button type="button" className={cancelButton} onClick={close}>
              Cancel
            </button>
            <button
              type="submit"
              className={primaryButton}
              disabled={
                !NAME.test(name) || !validLimit(limit) || create.isPending
              }
            >
              {create.isPending ? "Creating…" : "Create volume"}
            </button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function LimitDialog({
  target,
  onClose,
}: {
  target: SharedVolume | null;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [limit, setLimit] = useState<string | null>(null);
  const value =
    limit ??
    (target
      ? String(Math.round((target.size_limit_bytes / GIB) * 10) / 10)
      : "");
  const save = useMutation({
    mutationFn: () =>
      sharedVolumeService.updateLimit(
        target!.id,
        Math.round(Number(value) * GIB),
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: QUERY_KEY });
      toast.success("Size limit changed");
      close();
    },
  });
  const close = () => {
    onClose();
    setLimit(null);
    save.reset();
  };
  const used =
    target?.used_bytes !== null && target
      ? `${gib(target.used_bytes ?? 0)} GiB used now.`
      : "Not measured yet.";
  return (
    <Dialog
      open={target !== null}
      onOpenChange={(o) => !o && !save.isPending && close()}
    >
      <DialogContent className={cn(dialogContent, "max-w-[480px]")}>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            save.mutate();
          }}
        >
          <DialogHeader className={dialogHeader}>
            <DialogTitle className={dialogTitle}>
              Size limit of <span className="font-mono">{target?.name}</span>
            </DialogTitle>
            <DialogDescription className={dialogText}>
              {used} The alert fires at 80% of the new limit. The data is not
              touched.
            </DialogDescription>
          </DialogHeader>
          <div className="mx-5 mt-4 flex flex-col gap-1.5">
            <label htmlFor="cl-limit" className={fieldLabel}>
              Size limit, GiB
            </label>
            <input
              id="cl-limit"
              inputMode="decimal"
              required
              value={value}
              onChange={(e) => setLimit(e.target.value)}
              className={cn(fieldInput, "w-32")}
            />
          </div>
          {save.error && (
            <p
              role="alert"
              className="mx-5 mt-3 rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg"
            >
              {message(save.error, "The limit was not changed.")}
            </p>
          )}
          <div className={dialogFooter}>
            <button type="button" className={cancelButton} onClick={close}>
              Cancel
            </button>
            <button
              type="submit"
              className={primaryButton}
              disabled={!validLimit(value) || save.isPending}
            >
              {save.isPending ? "Saving…" : "Save limit"}
            </button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function DeleteDialog({
  target,
  onClose,
}: {
  target: SharedVolume | null;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [typed, setTyped] = useState("");
  const remove = useMutation({
    mutationFn: () => sharedVolumeService.delete(target!.id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: QUERY_KEY });
      toast.success(`${target?.name} and its data deleted`);
      close();
    },
  });
  const close = () => {
    onClose();
    setTyped("");
    remove.reset();
  };
  return (
    <Dialog
      open={target !== null}
      onOpenChange={(o) => !o && !remove.isPending && close()}
    >
      <DialogContent
        role="alertdialog"
        className={dialogContent}
        showClose={false}
      >
        <form
          onSubmit={(e) => {
            e.preventDefault();
            remove.mutate();
          }}
        >
          <DialogHeader className={cn(dialogHeader, "gap-1.5")}>
            <DialogTitle className={dialogTitle}>
              Delete <span className="font-mono">{target?.name}</span>?
            </DialogTitle>
            <DialogDescription className={dialogText}>
              This deletes the volume and every file in it from{" "}
              <span className="font-mono text-xs text-fg">
                {target?.server_id ? target.server_name : "the panel host"}
              </span>
              . It can’t be undone here; if the server has backups, the volume’s
              earlier copies stay in them until they are pruned.
            </DialogDescription>
          </DialogHeader>
          <div className="mx-5 mt-3.5 flex flex-col gap-1.5">
            <label htmlFor="dv-confirm" className={fieldLabel}>
              Type <span className="font-mono text-fg">{target?.name}</span> to
              confirm
            </label>
            <input
              id="dv-confirm"
              autoComplete="off"
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              className={cn(fieldInput, "font-mono")}
            />
          </div>
          {remove.error && (
            <p
              role="alert"
              className="mx-5 mt-3 rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg"
            >
              {message(remove.error, "The volume was not deleted.")}
            </p>
          )}
          <div className={dialogFooter}>
            <button
              type="button"
              className={cancelButton}
              disabled={remove.isPending}
              onClick={close}
            >
              Cancel
            </button>
            <button
              type="submit"
              className={dangerButton}
              disabled={typed !== target?.name || remove.isPending}
            >
              {remove.isPending ? "Deleting…" : "Delete volume and data"}
            </button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
