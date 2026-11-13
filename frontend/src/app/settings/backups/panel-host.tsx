"use client";

import { useRef, useState } from "react";
import Link from "next/link";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, Check, Copy } from "lucide-react";
import { ApiError } from "@/lib/api/client";
import {
  backupPath,
  backupService,
  type BackupDestination,
  type BackupInput,
} from "@/lib/api/server.service";
import { setupQuery } from "@/lib/api/instance.service";
import { formatDateTime, formatDateTimeZone, cn } from "@/lib/utils";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  cancelButton,
  card,
  dangerButton,
  dialogContent,
  dialogHeader,
  dialogText,
  dialogTitle,
  fieldInput,
  fieldLabel,
  primaryButton,
  smallButton,
} from "@/components/settings/settings-frame";
import { backupState, destinationOf, type BackupTone } from "./backups-view";

const PATH = backupPath(null);
export const PANEL_KEY = ["backups", "panel"];

const CHIP: Record<BackupTone, string> = {
  ok: "bg-status-running-soft text-status-running-fg",
  fail: "bg-status-failed-soft text-status-failed-fg",
  wait: "bg-status-pending-soft text-status-pending-fg",
  off: "bg-raised text-fg/70",
};
const message = (err: unknown, fallback: string) =>
  err instanceof ApiError && err.status < 500 ? err.message : fallback;
const EMPTY: BackupInput = {
  endpoint: "",
  bucket: "",
  access_key: "",
  secret_key: "",
  schedule: "03:00",
};
/** The stored destination as an input: an empty secret key keeps the stored one. */
const stored = (d: BackupDestination): BackupInput => ({
  endpoint: d.endpoint,
  bucket: d.bucket,
  access_key: d.access_key,
  secret_key: "",
  schedule: d.schedule,
});

/** The panel host's backup (board: Backup, Panel host): instance operators
 *  see and set it. */
export function PanelHost({
  dest,
  now,
  editing,
  onEdit,
}: {
  dest: BackupDestination | null;
  now: number;
  editing: boolean;
  onEdit: (on: boolean) => void;
}) {
  const queryClient = useQueryClient();
  const [form, setForm] = useState<BackupInput>(EMPTY);
  const [password, setPassword] = useState<string | null>(null);
  const [stopping, setStopping] = useState(false);
  const state = backupState(dest, now);
  // No run of this server's to show: none at all, or none since a restore
  // (D-164), whatever the old server's last one says.
  const waiting =
    state.tone === "wait" || (!!dest && !dest.paused && !!dest.restored_at);
  const chip =
    state.tone === "wait" ? "Waiting for the first backup" : state.label;

  // The form is filled however editing starts (the card's button or the table's).
  const [wasEditing, setWasEditing] = useState(false);
  if (editing !== wasEditing) {
    setWasEditing(editing);
    if (editing) setForm(dest ? stored(dest) : EMPTY);
  }
  const open = () => onEdit(true);
  const save = useMutation({
    mutationFn: () => backupService.put(PATH, form),
    onSuccess: (saved) => {
      const { repository_password, ...rest } = saved;
      queryClient.setQueryData(PANEL_KEY, rest);
      queryClient.invalidateQueries({ queryKey: ["inbox"] });
      // The recovery kit names these settings: the one stored may be out of date now.
      queryClient.invalidateQueries({ queryKey: setupQuery.queryKey });
      onEdit(false);
      if (repository_password) setPassword(repository_password);
      if (saved.apply_error)
        toast.warning(
          `Saved, but not written to the panel host yet: ${saved.apply_error}`,
          { duration: 15_000 },
        );
      else if (!repository_password) toast.success("Panel host backup saved");
    },
  });
  const heading = useRef<HTMLHeadingElement>(null);
  // Resume (D-163): the stored destination again, written to the host.
  const resume = useMutation({
    mutationFn: (d: BackupDestination) => backupService.put(PATH, stored(d)),
    onSuccess: (saved) => {
      queryClient.setQueryData(PANEL_KEY, saved);
      // The notice goes, and its button with it: focus stays in the card.
      if (!saved.paused) heading.current?.focus();
      if (saved.apply_error)
        toast.warning(
          `Not written to the panel host yet: ${saved.apply_error}`,
          { duration: 15_000 },
        );
      else toast.success("Backups resumed");
    },
  });
  const set = (patch: Partial<BackupInput>) =>
    setForm((f) => ({ ...f, ...patch }));

  return (
    <section
      id="panel-host"
      aria-labelledby="ph-h"
      className={card}
    >
      <div className="flex flex-col gap-1 border-b border-subtle px-4 py-3">
        <div className="flex flex-wrap items-center gap-x-2.5 gap-y-2">
          <h2
            id="ph-h"
            ref={heading}
            tabIndex={-1}
            className="text-[15px] font-semibold leading-[22px] text-fg focus-visible:outline-none"
          >
            Panel host
          </h2>
          <span className="font-mono text-xs text-fg/70">panel</span>
          <span className="rounded-full border border-subtle px-2 text-xs leading-5 text-fg/85">
            Instance operators
          </span>
          <span className="flex-1" />
          <span
            className={cn(
              "inline-flex h-[22px] items-center gap-1.5 rounded-full px-2 text-xs font-medium",
              CHIP[state.tone],
            )}
          >
            <span
              className="h-1.5 w-1.5 rounded-full bg-current"
              aria-hidden="true"
            />
            {chip}
          </span>
        </div>
        <span className="text-xs leading-[18px] text-muted">
          The panel host’s apps, and the panel’s own database: every workspace’s
          accounts, members, settings and encrypted secrets. Only instance
          operators see and change this.
        </span>
      </div>

      {editing ? (
        <form
          className="flex flex-col gap-3.5 px-4 pb-4 pt-3.5"
          onSubmit={(e) => {
            e.preventDefault();
            save.mutate();
          }}
        >
          <div className="grid grid-cols-1 gap-x-4 gap-y-3.5 sm:grid-cols-2">
            <div className="flex flex-col gap-1.5">
              <label htmlFor="bk-endpoint" className={fieldLabel}>
                Endpoint
              </label>
              <input
                id="bk-endpoint"
                type="url"
                required
                value={form.endpoint}
                onChange={(e) => set({ endpoint: e.target.value })}
                placeholder="https://fsn1.your-objectstorage.com"
                aria-describedby="bk-endpoint-help"
                className={cn(fieldInput, "font-mono")}
              />
              <span
                id="bk-endpoint-help"
                className="text-xs leading-[17px] text-muted"
              >
                https:// and the host only, no path.
              </span>
            </div>
            <div className="flex flex-col gap-1.5">
              <label htmlFor="bk-bucket" className={fieldLabel}>
                Bucket
              </label>
              <input
                id="bk-bucket"
                required
                value={form.bucket}
                onChange={(e) => set({ bucket: e.target.value.toLowerCase() })}
                placeholder="founders-backups"
                aria-describedby="bk-bucket-help"
                className={cn(fieldInput, "font-mono")}
              />
              <span
                id="bk-bucket-help"
                className="text-xs leading-[17px] text-muted"
              >
                Backups go under <span className="font-mono">toft/</span> in it,
                one folder per server, so servers can share a bucket. A missing
                bucket is created if the key may.
              </span>
            </div>
            <div className="flex flex-col gap-1.5">
              <label htmlFor="bk-access" className={fieldLabel}>
                Access key
              </label>
              <input
                id="bk-access"
                required
                autoComplete="off"
                value={form.access_key}
                onChange={(e) => set({ access_key: e.target.value })}
                className={cn(fieldInput, "font-mono")}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <label htmlFor="bk-secret" className={fieldLabel}>
                Secret key
              </label>
              <input
                id="bk-secret"
                type="password"
                autoComplete="new-password"
                required={!dest}
                placeholder={dest ? "Stored, not shown" : ""}
                value={form.secret_key}
                onChange={(e) => set({ secret_key: e.target.value })}
                aria-describedby="bk-secret-help"
                className={cn(fieldInput, "font-mono")}
              />
              <span
                id="bk-secret-help"
                className="text-xs leading-[17px] text-muted"
              >
                {dest
                  ? "Leave empty to keep the stored one. Never shown again."
                  : "Never shown again."}
              </span>
            </div>
            <div className="flex flex-col gap-1.5">
              <label htmlFor="bk-at" className={fieldLabel}>
                Daily at
              </label>
              <input
                id="bk-at"
                type="time"
                required
                value={form.schedule}
                onChange={(e) => set({ schedule: e.target.value })}
                aria-describedby="bk-at-help"
                className={cn(fieldInput, "w-32 font-mono")}
              />
              <span
                id="bk-at-help"
                className="text-xs leading-[17px] text-muted"
              >
                Server time. The run starts within 15 minutes of it.
              </span>
            </div>
          </div>
          <p className="text-xs leading-[17px] text-muted">
            {dest
              ? "Saving writes the new settings to the panel host at once. The repository password stays the same; a new bucket starts a new set of copies."
              : "After saving, the panel shows the repository password, and the recovery kit holds it from then on. Have your password manager open."}
          </p>
          {save.error && (
            <p
              role="alert"
              className="rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg"
            >
              {message(save.error, "The backup destination was not saved.")}
            </p>
          )}
          <div className="flex flex-wrap items-center gap-2">
            <button
              type="submit"
              className={primaryButton}
              disabled={save.isPending}
            >
              {save.isPending ? "Saving…" : "Save"}
            </button>
            <button
              type="button"
              className={cn(cancelButton, "bg-surface")}
              onClick={() => {
                onEdit(false);
                save.reset();
              }}
            >
              Cancel
            </button>
          </div>
        </form>
      ) : !dest ? (
        <div className="flex flex-col gap-3.5 px-4 pb-4 pt-3.5">
          <p className="text-[13px] leading-[19px] text-fg/85">
            The panel host is not backed up. A lost disk would lose every
            workspace’s accounts and settings, and the apps that run there.
          </p>
          <ol className="flex list-decimal flex-col gap-1 pl-[18px] text-[13px] leading-[19px] text-fg/70">
            <li>
              Create a bucket and an S3 access key at your provider, such as
              Hetzner Object Storage in the same region, on separate hardware.
              Toft can’t create them.
            </li>
            <li>
              Enter them here. The settings are written to the panel host at
              once.
            </li>
            <li>
              Store the recovery kit, which then holds the repository password,
              in your password manager.
            </li>
          </ol>
          <div>
            <button type="button" className={primaryButton} onClick={open}>
              Set up backups
            </button>
          </div>
        </div>
      ) : (
        <div
          className={cn(
            "flex flex-col gap-3.5 px-4 pb-4",
            dest.paused || waiting ? "pt-3.5" : "pt-1.5",
          )}
        >
          {dest.paused && (
            <div className="flex flex-col gap-2.5 rounded-lg border border-danger/40 bg-ground px-3 py-2.5 text-[13px] leading-[19px]">
              <div className="flex items-start gap-2">
                <AlertTriangle
                  className="mt-0.5 h-4 w-4 flex-none text-status-failed"
                  aria-hidden="true"
                />
                <span className="flex flex-col gap-0.5">
                  <span className="font-medium text-fg">
                    Backups are not running on this server
                  </span>
                  <span className="text-fg/70">
                    {dest.restored_at ? (
                      <>
                        This panel was restored from a backup on{" "}
                        <span className="font-mono text-xs text-fg">
                          {formatDateTimeZone(dest.restored_at)}
                        </span>
                        . A restore leaves backups paused, so a restore drill
                        never writes into the real panel’s backups. Resume them
                        only on the server you keep.
                      </>
                    ) : (
                      "The panel host has no backup settings, so the nightly run does nothing."
                    )}{" "}
                    Resuming uses the stored destination again, with no key to
                    enter. A backup then starts at{" "}
                    <span className="font-mono text-xs text-fg">
                      {dest.schedule}
                    </span>{" "}
                    server time, or within 15 minutes if that has passed today.
                  </span>
                </span>
              </div>
              {resume.error && (
                <p
                  role="alert"
                  className="rounded-lg bg-status-failed-soft px-3 py-2 text-status-failed-fg"
                >
                  {message(resume.error, "Backups were not resumed.")}
                </p>
              )}
              <div>
                <button
                  type="button"
                  className={primaryButton}
                  disabled={resume.isPending}
                  onClick={() => resume.mutate(dest)}
                >
                  {resume.isPending ? "Resuming…" : "Resume backups"}
                </button>
              </div>
            </div>
          )}
          {waiting && (
            <div className="flex flex-col gap-2.5">
              <p className="text-[13px] leading-[19px] text-fg/85">
                {dest.restored_at ? (
                  <>
                    Backups are resumed.{" "}
                    {dest.last_ok_at && (
                      <>
                        The last backup recorded here, on{" "}
                        <span className="font-mono text-xs text-fg">
                          {formatDateTimeZone(dest.last_ok_at)}
                        </span>
                        , is from before the restore.{" "}
                      </>
                    )}
                    This server’s first backup starts at{" "}
                    <span className="font-mono text-xs text-fg">
                      {dest.schedule}
                    </span>{" "}
                    server time, or within 15 minutes if that has passed
                    today.
                  </>
                ) : (
                  <>
                    Saved and written to the panel host. The first backup
                    starts at{" "}
                    <span className="font-mono text-xs text-fg">
                      {dest.schedule}
                    </span>{" "}
                    server time, or within 15 minutes if that has passed
                    today. It uploads everything, so it takes longer than the
                    ones after it.
                  </>
                )}
              </p>
              <p className="text-xs text-muted">
                To check the settings now instead of waiting, run one on the
                panel host (below, under Restoring).
              </p>
            </div>
          )}
          <dl
            className={cn(
              "flex flex-col text-[13px]",
              waiting && "border-t border-subtle",
            )}
          >
            {!waiting && (
              <Row k="Last backup">
                {dest.paused ? (
                  <span className="text-fg/85">
                    {dest.last_ok_at ? (
                      <>
                        Last success{" "}
                        <span className="font-mono text-xs text-fg">
                          {formatDateTimeZone(dest.last_ok_at)}
                        </span>
                        {dest.restored_at && ", before the restore"}
                      </>
                    ) : (
                      "None has succeeded yet"
                    )}
                  </span>
                ) : state.failing ? (
                  <span className="text-status-failed-fg">
                    {state.label} {state.note}:{" "}
                    {dest.last_error ||
                      "see /var/lib/toft/backup/last.json on the panel host"}
                  </span>
                ) : (
                  <span className="text-fg">
                    {state.note} backed up, no errors
                  </span>
                )}
              </Row>
            )}
            <Row k="Destination">
              <span className="flex flex-col gap-0.5">
                <span className="break-all font-mono text-xs text-fg">
                  {destinationOf(dest)}
                </span>
                <span className="text-xs text-muted">
                  Access key{" "}
                  <span className="font-mono text-fg/70">
                    {dest.access_key}
                  </span>
                  . The secret key is stored encrypted and never shown.
                </span>
              </span>
            </Row>
            <Row k="Daily at">
              <span className="text-fg/85">
                <span className="font-mono text-xs text-fg">
                  {dest.schedule}
                </span>{" "}
                server time. The run starts within 15 minutes of it.
              </span>
            </Row>
            <Row k="Repository password">
              <span className="text-fg/85">
                Made when this was set up on{" "}
                <span className="font-mono text-xs text-fg">
                  {formatDateTime(dest.created_at).slice(0, 10)}
                </span>
                . The panel keeps it encrypted, and the recovery kit holds it.
              </span>
            </Row>
            <Row k="Keeps">
              <span className="text-fg/85">
                7 daily, 4 weekly and 6 monthly copies
              </span>
            </Row>
          </dl>
          <div className="flex items-start gap-2.5 rounded-lg bg-status-pending-soft px-3 py-2.5 text-[13px] leading-[19px] text-status-pending-fg">
            <AlertTriangle
              className="mt-0.5 h-4 w-4 flex-none"
              aria-hidden="true"
            />
            <span>
              The panel’s <span className="font-mono text-xs">.env</span> file
              is not in this backup. Without its{" "}
              <span className="font-mono text-xs">ENCRYPTION_KEY</span>, a
              restored database’s secrets can’t be decrypted. The recovery
              kit holds the key and the repository password: keep it in your
              password manager.{" "}
              <Link
                href="/instance/kit"
                className="touch-target inline-flex items-center whitespace-nowrap font-medium underline underline-offset-2"
              >
                Show kit
              </Link>
            </span>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              className={cn(smallButton, "h-8 px-3 text-[13px] font-medium rounded-lg")}
              onClick={open}
            >
              Edit
            </button>
            <span className="flex-1" />
            <button
              type="button"
              className="inline-flex h-8 items-center rounded-lg border border-danger/45 px-3 text-[13px] font-medium text-status-failed-fg hover:bg-status-failed-soft"
              onClick={() => setStopping(true)}
            >
              Stop backups…
            </button>
          </div>
        </div>
      )}

      <PasswordDialog password={password} onClose={() => setPassword(null)} />
      <StopDialog
        open={stopping}
        dest={dest}
        onClose={() => setStopping(false)}
      />
    </section>
  );
}

function Row({ k, children }: { k: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-wrap gap-x-3 gap-y-0.5 border-t border-subtle py-2 first:border-t-0">
      <dt className="w-[150px] flex-none text-muted">{k}</dt>
      <dd className="min-w-0 flex-[1_1_280px]">{children}</dd>
    </div>
  );
}

function PasswordDialog({
  password,
  onClose,
}: {
  password: string | null;
  onClose: () => void;
}) {
  const [copied, setCopied] = useState(false);
  return (
    <Dialog
      open={password !== null}
      onOpenChange={(o) => !o && (onClose(), setCopied(false))}
    >
      <DialogContent
        className={cn(dialogContent, "max-w-[560px]")}
        showClose={false}
        onInteractOutside={(e) => e.preventDefault()}
      >
        <DialogHeader className={cn(dialogHeader, "gap-1.5")}>
          <DialogTitle className={dialogTitle}>
            Store the panel host’s repository password
          </DialogTitle>
          <DialogDescription className={dialogText}>
            It decrypts this host’s backups. The panel keeps it encrypted and
            the host keeps it in a root-only file, but if both are lost, a
            copy in your password manager is the only way to read the
            backups: this password, or the recovery kit, which holds it.
          </DialogDescription>
        </DialogHeader>
        <div className="mx-5 mt-4 flex flex-col gap-1.5">
          <label htmlFor="pw-value" className={fieldLabel}>
            Repository password
          </label>
          <div className="flex items-start gap-2">
            <textarea
              id="pw-value"
              readOnly
              rows={2}
              value={password ?? ""}
              onFocus={(e) => e.target.select()}
              className="min-w-0 flex-1 resize-none rounded-lg border border-strong bg-ground px-2.5 py-2 font-mono text-xs leading-5 text-fg [overflow-wrap:anywhere]"
            />
            <button
              type="button"
              className={cancelButton}
              onClick={() =>
                password &&
                navigator.clipboard.writeText(password).then(
                  () => setCopied(true),
                  () => toast.error("Couldn't copy: select it and copy it"),
                )
              }
            >
              {copied ? (
                <Check className="mr-1.5 h-3.5 w-3.5" aria-hidden="true" />
              ) : (
                <Copy className="mr-1.5 h-3.5 w-3.5" aria-hidden="true" />
              )}
              {copied ? "Copied" : "Copy"}
            </button>
          </div>
        </div>
        <p className="mx-5 mt-3.5 rounded-lg bg-status-pending-soft px-3 py-2.5 text-[13px] leading-[19px] text-status-pending-fg">
          The recovery kit now holds this password, with the panel’s
          encryption key, which the backup doesn’t hold: a restore needs both.
          Store the kit now: one stored earlier doesn’t hold this password.{" "}
          {/* A new tab: this dialog doesn’t open again. */}
          <Link
            href="/instance/kit"
            target="_blank"
            className="touch-target inline-flex items-center whitespace-nowrap font-medium underline underline-offset-2"
          >
            Show kit<span className="sr-only"> (opens in a new tab)</span>
          </Link>
        </p>
        <div className="flex justify-end gap-2 px-5 pb-5 pt-[18px]">
          <button
            type="button"
            className={primaryButton}
            onClick={() => {
              onClose();
              setCopied(false);
            }}
          >
            Done, I stored it
          </button>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function StopDialog({
  open,
  dest,
  onClose,
}: {
  open: boolean;
  dest: BackupDestination | null;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const stop = useMutation({
    mutationFn: () => backupService.delete(PATH),
    onSuccess: () => {
      queryClient.setQueryData(PANEL_KEY, null);
      queryClient.invalidateQueries({ queryKey: ["inbox"] });
      queryClient.invalidateQueries({ queryKey: setupQuery.queryKey });
      toast.success(
        "Panel host backups stopped. The copies already in the bucket stay.",
      );
      onClose();
      stop.reset();
    },
  });
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => !o && !stop.isPending && (onClose(), stop.reset())}
    >
      <DialogContent
        role="alertdialog"
        className={dialogContent}
        showClose={false}
      >
        <DialogHeader className={cn(dialogHeader, "gap-1.5")}>
          <DialogTitle className={dialogTitle}>
            Stop backing up the panel host?
          </DialogTitle>
          <DialogDescription className={dialogText}>
            From tonight, nothing backs up the panel’s database or the apps on
            the panel host, and no alert says so.
          </DialogDescription>
        </DialogHeader>
        <ul className="mx-5 mt-3 flex list-disc flex-col gap-1 pl-[18px] text-[13px] leading-[19px] text-fg/70">
          <li>
            The copies already in{" "}
            <span className="font-mono text-xs text-fg">
              {dest ? destinationOf(dest).split("/").slice(1).join("/") : ""}
            </span>{" "}
            stay. Delete them at your provider if you want them gone.
          </li>
          <li>
            Setting it up again makes a new repository password, which can’t
            open those copies. Use another bucket then, or delete them first.
          </li>
        </ul>
        {stop.error && (
          <p
            role="alert"
            className="mx-5 mt-3 rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg"
          >
            {message(stop.error, "Backups were not stopped.")}
          </p>
        )}
        <div className="flex flex-wrap justify-end gap-2 px-5 pb-5 pt-[18px]">
          <button
            type="button"
            className={cancelButton}
            disabled={stop.isPending}
            onClick={onClose}
          >
            Cancel
          </button>
          <button
            type="button"
            className={dangerButton}
            disabled={stop.isPending}
            onClick={() => stop.mutate()}
          >
            {stop.isPending ? "Stopping…" : "Stop backups"}
          </button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
