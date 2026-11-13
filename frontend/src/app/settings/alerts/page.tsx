"use client";

import { useState } from "react";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Activity, AlertTriangle, Archive, Check, CircleOff, Container, Copy, Database, HardDrive, MemoryStick, RotateCcw, Send, PackageCheck, Power, Server, ShieldAlert, Snowflake, SquareX, Undo2, Unplug, X, type LucideIcon, RefreshCwOff } from "lucide-react";
import { useAuth, useOrgRole } from "@/context/AuthContext";
import { ApiError } from "@/lib/api/client";
import {
  alertService,
  type AlertChannel,
  type AlertKind,
  type RecentAlert,
} from "@/lib/api/alert.service";
import { orgService } from "@/lib/api/org.service";
import { sinceShort } from "@/lib/time";
import { cn, formatDateTime } from "@/lib/utils";
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
  primaryButton,
  phoneLabel,
  phoneNote,
  phoneRow,
  phoneTd,
  smallButton,
  th,
} from "@/components/settings/settings-frame";
import {
  KIND_LABEL,
  RULES,
  alertState,
  alertSubject,
  channelStatus,
  clockOrDate,
  type Tone,
} from "./alerts-view";

const CHANNEL_KEY = ["alert-channel"];

const KIND_ICON: Record<AlertKind, LucideIcon> = {
  deploy_failed: X,
  oom_killed: MemoryStick,
  service_down: CircleOff,
  service_restarting: RotateCcw,
  service_unhealthy: Activity,
  container_exited: SquareX,
  server_unreachable: Server,
  disk_full: HardDrive,
  volume_full: Database,
  backup_failed: Archive,
  panel_container: Container,
  panel_certificate: ShieldAlert,
  panel_upgrade_failed: Undo2,
  mirror_failing: RefreshCwOff,
  agent_offline: Unplug,
  agent_frozen: Snowflake,
  security_updates: PackageCheck,
  reboot_required: Power,
  docker_advisory: ShieldAlert,
};

const PILL: Record<Tone, string> = {
  ok: "bg-status-running-soft text-status-running-fg",
  warn: "bg-status-pending-soft text-status-pending-fg",
  fail: "bg-status-failed-soft text-status-failed-fg",
  off: "bg-raised text-fg/70",
};
const STATE: Record<Tone, string> = {
  ok: "text-status-running-fg",
  warn: "text-status-pending-fg",
  fail: "text-status-failed-fg",
  off: "text-fg/85",
};

const message = (err: unknown, fallback: string) =>
  err instanceof ApiError && err.status < 500 ? err.message : fallback;

/** Settings → Alerts (board: Alerts): the workspace's webhook, whether it
 *  works, the last 30 days of alerts and what alerts. Owners only. */
export default function AlertsSettingsPage() {
  const { user, isLoading: authLoading } = useAuth();
  const isOwner = useOrgRole() === "owner";
  const workspace = user?.orgName ?? "";
  const channel = useQuery({
    queryKey: CHANNEL_KEY,
    queryFn: () => alertService.get(),
    enabled: isOwner,
  });
  const members = useQuery({
    queryKey: ["org-members"],
    queryFn: () => orgService.listMembers(),
    enabled: !authLoading && !isOwner,
    meta: { quietFailure: "Only the owners' names in a sentence that reads whole without them." },
  });
  const owners = (members.data ?? [])
    .filter((m) => m.role === "owner")
    .map((m) => m.user_name || m.user_email);

  return (
    <SettingsFrame title="Alerts" gap="gap-6">
      <p className="max-w-[760px] text-[13px] leading-[19px] text-fg/70">
        One webhook for this workspace. The panel posts when something breaks
        and again when it is fixed. Slack, Mattermost, Rocket.Chat and Google
        Chat incoming webhooks work as they are; for Discord, add{" "}
        <span className="font-mono text-xs text-fg">/slack</span> to the webhook
        URL.
      </p>

      {authLoading ? null : !isOwner ? (
        <div className={cn(card, "p-4 text-[13px] text-fg/70")}>
          Only workspace owners manage alerts.
          {owners.length > 0 &&
            ` Ask an owner of ${workspace} (${owners.join(", ")}) to change where alerts go.`}
        </div>
      ) : channel.isLoading ? (
        <div
          className={cn(card, "h-[360px] motion-safe:animate-pulse")}
          aria-label="Loading the alert webhook"
        />
      ) : channel.error ? (
        <div
          role="alert"
          className="flex flex-wrap items-center justify-between gap-3 rounded-[10px] border border-danger/35 bg-surface px-4 py-3.5"
        >
          <span className="flex items-center gap-2.5 text-[13px] text-status-failed-fg">
            <AlertTriangle className="h-4 w-4 flex-none" aria-hidden="true" />
            Could not load the alert webhook:{" "}
            {message(channel.error, "the panel did not answer")}. Nothing on
            this page has changed.
          </span>
          <button
            type="button"
            className={cn(smallButton, "h-8 px-3 text-[13px] font-medium rounded-lg")}
            onClick={() => channel.refetch()}
          >
            Retry
          </button>
        </div>
      ) : (
        <div className="flex flex-wrap items-start gap-6">
          <div className="flex min-w-0 flex-[999_1_560px] flex-col gap-6">
            <WebhookCard
              key={channel.data?.webhook_url ?? "none"}
              channel={channel.data ?? null}
              workspace={workspace}
            />
            <Recent exists={!!channel.data} workspace={workspace} />
          </div>
          <WhatAlerts />
        </div>
      )}
    </SettingsFrame>
  );
}

function WebhookCard({
  channel,
  workspace,
}: {
  channel: AlertChannel | null;
  workspace: string;
}) {
  const queryClient = useQueryClient();
  const [now] = useState(() => Date.now());
  const [url, setUrl] = useState(channel?.webhook_url ?? "");
  const [enabled, setEnabled] = useState(channel?.enabled ?? true);
  const [secret, setSecret] = useState<{
    value: string;
    rotated: boolean;
  } | null>(null);
  const [rotating, setRotating] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [tested, setTested] = useState<{ ok: boolean; text: string } | null>(
    null,
  );
  const status = channelStatus(channel, now);
  const refresh = () =>
    queryClient.invalidateQueries({ queryKey: CHANNEL_KEY });

  const rotated = useQuery({
    queryKey: ["audit-log", "alerts.secret_rotated"],
    queryFn: () =>
      orgService.listAuditLog({ action: "alerts.secret_rotated", limit: 1 }),
    enabled: !!channel,
  });
  const lastRotation = rotated.data?.[0];

  const save = useMutation({
    mutationFn: () => alertService.put(url.trim(), enabled),
    onSuccess: (saved) => {
      if (saved.signing_secret)
        setSecret({ value: saved.signing_secret, rotated: false });
      else toast.success("Alert webhook saved");
      refresh();
    },
  });
  const test = useMutation({
    mutationFn: () => alertService.test(),
    onSuccess: () =>
      setTested({
        ok: true,
        text: `Test message delivered at ${clockOrDate(new Date().toISOString(), Date.now())}.`,
      }),
    onError: (err) =>
      setTested({
        ok: false,
        text:
          err instanceof Error
            ? err.message
            : "The test message was not delivered.",
      }),
    onSettled: refresh,
  });
  const dirty =
    !channel ||
    url.trim() !== channel.webhook_url ||
    enabled !== channel.enabled;

  return (
    <section aria-labelledby="hook-h" className={card}>
      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3">
        <div className="flex items-center gap-2.5">
          <h2
            id="hook-h"
            className="text-[15px] font-semibold leading-[22px] text-fg"
          >
            Webhook
          </h2>
          <span
            className={cn(
              "inline-flex h-[22px] items-center gap-1.5 rounded-full px-2 text-xs font-medium",
              PILL[status.tone === "fail" ? "ok" : status.tone],
            )}
          >
            <span
              className="h-1.5 w-1.5 rounded-full bg-current"
              aria-hidden="true"
            />
            {status.label}
          </span>
        </div>
        <span
          className={cn(
            "text-xs",
            status.failed ? "text-status-failed-fg" : "text-muted",
          )}
        >
          {status.note}
        </span>
      </div>

      <form
        className="flex flex-col gap-4 p-4"
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate();
        }}
      >
        <div className="flex flex-col gap-1.5">
          <label htmlFor="al-url" className={fieldLabel}>
            Webhook URL
          </label>
          <input
            id="al-url"
            type="url"
            inputMode="url"
            required
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="https://hooks.slack.com/services/…"
            aria-describedby="al-url-help"
            className={cn(fieldInput, "font-mono")}
          />
          <span id="al-url-help" className="text-xs leading-[17px] text-muted">
            HTTPS only, on a public address. Every request carries{" "}
            <span className="font-mono text-fg/85">
              X-Toft-Signature: sha256=…
            </span>
            , an HMAC-SHA256 of the body with the signing secret.
          </span>
        </div>

        <label className="flex items-start gap-2.5 text-[13px]">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
            className="mt-[3px] h-4 w-4 accent-[var(--link)]"
          />
          <span className="flex flex-col gap-0.5">
            <span className="font-medium text-fg">Send alerts</span>
            <span className="text-xs text-muted">
              Untick to pause. The webhook and its secret are kept and nothing
              is sent.
            </span>
          </span>
        </label>

        {channel && (
          <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2.5 rounded-lg border border-subtle px-3 py-2.5">
            <span className="flex flex-col gap-0.5 text-[13px]">
              <span className="font-medium text-fg">Signing secret</span>
              <span className="text-xs text-muted">
                Shown once, when created or rotated.
                {lastRotation && (
                  <>
                    {" "}
                    Last rotated{" "}
                    <span className="font-mono">
                      {formatDateTime(lastRotation.created_at)}
                    </span>{" "}
                    by {lastRotation.actor}.
                  </>
                )}
              </span>
            </span>
            <button
              type="button"
              className={cn(smallButton, "h-8 px-3 text-[13px] font-medium rounded-lg")}
              onClick={() => setRotating(true)}
            >
              Rotate secret…
            </button>
          </div>
        )}

        {save.error && (
          <p
            role="alert"
            className="rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg"
          >
            {message(save.error, "The webhook was not saved.")}
          </p>
        )}

        <div className="flex flex-wrap items-center gap-2">
          <button
            type="submit"
            className={primaryButton}
            disabled={url.trim() === "" || !dirty || save.isPending}
          >
            {save.isPending ? "Saving…" : channel ? "Save" : "Create webhook"}
          </button>
          {!channel && (
            <span className="text-xs text-muted">
              Enter a URL to create the webhook. Its signing secret is shown
              once, next.
            </span>
          )}
          {channel && (
            <button
              type="button"
              className={cn(cancelButton, "gap-1.5 bg-surface")}
              disabled={test.isPending}
              onClick={() => test.mutate()}
            >
              <Send className="h-3.5 w-3.5" aria-hidden="true" />
              {test.isPending ? "Sending…" : "Send test"}
            </button>
          )}
          <span className="flex-1" />
          {channel && (
            <button
              type="button"
              className="inline-flex h-9 items-center rounded-lg border border-danger/45 px-3 text-[13px] font-medium text-status-failed-fg hover:bg-status-failed-soft"
              onClick={() => setRemoving(true)}
            >
              Remove webhook…
            </button>
          )}
        </div>
        {tested && (
          <p
            role="status"
            className={cn(
              "flex items-center gap-2 text-[13px]",
              tested.ok ? "text-status-running-fg" : "text-status-failed-fg",
            )}
          >
            {tested.ok ? (
              <Check className="h-4 w-4" aria-hidden="true" />
            ) : (
              <AlertTriangle className="h-4 w-4" aria-hidden="true" />
            )}
            {tested.text}
          </p>
        )}
      </form>

      <RotateDialog
        open={rotating}
        onClose={() => setRotating(false)}
        onRotated={(s) => {
          setRotating(false);
          setSecret({ value: s, rotated: true });
        }}
      />
      <SecretDialog secret={secret} onClose={() => setSecret(null)} />
      <RemoveDialog
        open={removing}
        workspace={workspace}
        onClose={() => setRemoving(false)}
      />
    </section>
  );
}

function Recent({ exists, workspace }: { exists: boolean; workspace: string }) {
  const [now] = useState(() => Date.now());
  const recent = useQuery({
    queryKey: ["alerts-recent"],
    queryFn: () => alertService.recent(),
    enabled: exists,
  });
  const rows = exists ? (recent.data ?? []) : [];
  return (
    <section aria-labelledby="recent-h" className="flex flex-col gap-3">
      <SectionHead
        id="recent-h"
        title="Recent alerts"
        count={exists ? (recent.data?.length ?? "–") : 0}
        note="Last 30 days, newest first"
      />
      <div role="group" tabIndex={0} aria-label="Recent alerts" className={cn(card, "overflow-x-auto")}>
        <table className="w-full table-fixed border-collapse text-[13px] [overflow-wrap:anywhere] max-sm:block sm:min-w-[760px]">
          <thead className="max-sm:hidden">
            <tr>
              <th scope="col" className={th}>
                Alert
              </th>
              <th scope="col" className={th}>
                On
              </th>
              <th scope="col" className={cn(th, "w-[150px]")}>
                Started
              </th>
              <th scope="col" className={cn(th, "w-[150px]")}>
                Now
              </th>
            </tr>
          </thead>
          <tbody className="max-sm:block">
            {!exists ? (
              <tr className="max-sm:block">
                <td colSpan={4} className={cn(phoneNote, "py-5 text-fg/70")}>
                  Nothing is sent: {workspace} has no webhook. Alerts start once
                  you create one.
                </td>
              </tr>
            ) : recent.isLoading ? (
              <tr className="max-sm:block">
                <td colSpan={4} className={cn(phoneNote, "py-3.5")}>
                  <span
                    className="block h-2.5 w-[60%] rounded bg-raised motion-safe:animate-pulse"
                    aria-label="Loading"
                  />
                </td>
              </tr>
            ) : recent.error ? (
              <tr className="max-sm:block">
                <td
                  colSpan={4}
                  role="alert"
                  className={cn(phoneNote, "py-4 text-status-failed-fg")}
                >
                  Could not load the recent alerts:{" "}
                  {message(recent.error, "the panel did not answer")}.
                </td>
              </tr>
            ) : rows.length === 0 ? (
              <tr className="max-sm:block">
                <td colSpan={4} className={cn(phoneNote, "py-5 text-fg/70")}>
                  Nothing has alerted in the last 30 days.
                </td>
              </tr>
            ) : (
              rows.map((a) => <AlertRow key={a.id} a={a} now={now} />)
            )}
          </tbody>
        </table>
      </div>
      <p className="text-xs leading-[17px] text-muted">
        Each alert is sent when it starts and again when it ends. A failed
        deploy or a memory kill is one message. Resolved alerts are kept for 30
        days.
      </p>
    </section>
  );
}

function AlertRow({ a, now }: { a: RecentAlert; now: number }) {
  const Icon = KIND_ICON[a.kind] ?? AlertTriangle;
  const state = alertState(a, now);
  const on = alertSubject(a);
  const tint =
    a.kind === "deploy_failed"
      ? "text-status-failed"
      : !a.resolved_at && !a.one_off
        ? "text-status-pending"
        : "text-muted";
  const recentStart = now - new Date(a.fired_at).getTime() < 86_400_000;
  return (
    <tr className={phoneRow}>
      <td className={cn(phoneTd, "max-sm:col-span-2")}>
        <span className="flex items-start gap-2.5">
          <Icon
            className={cn("mt-0.5 h-4 w-4 flex-none", tint)}
            aria-hidden="true"
          />
          <span className="flex min-w-0 flex-col">
            <span className="font-medium text-fg">
              {KIND_LABEL[a.kind] ?? a.kind}
            </span>
            {a.detail && (
              <span className="text-xs leading-5 text-fg/70">{a.detail}</span>
            )}
          </span>
        </span>
      </td>
      <td className={cn(phoneTd, "max-sm:col-span-2")}>
        <span className="flex flex-col">
          {on.href ? (
            <Link
              href={on.href}
              className="text-fg hover:underline"
            >
              {on.name}
            </Link>
          ) : (
            <span className="text-fg">{on.name}</span>
          )}
          <span className="text-xs leading-5 text-muted">{on.where}</span>
        </span>
      </td>
      <td className={cn(phoneTd, "whitespace-nowrap text-fg/70 max-sm:col-span-2")}>
        <span className={phoneLabel}>Started </span>
        <span
          title={formatDateTime(a.fired_at)}
          className={recentStart ? "" : "font-mono text-xs"}
        >
          {sinceShort(a.fired_at, now)}
        </span>
      </td>
      <td className={cn(phoneTd, "max-sm:col-span-2")}>
        <span className="flex flex-col">
          <span
            className={cn(
              "inline-flex items-center gap-1.5 font-medium",
              STATE[state.tone],
            )}
          >
            <span
              className="h-1.5 w-1.5 rounded-full bg-current"
              aria-hidden="true"
            />
            {state.label}
          </span>
          <span className="whitespace-nowrap text-xs leading-5 text-muted">
            {state.note}
          </span>
        </span>
      </td>
    </tr>
  );
}

function WhatAlerts() {
  return (
    <aside
      aria-labelledby="what-h"
      className={cn(card, "min-w-0 flex-[1_1_340px]")}
    >
      <div className="border-b border-subtle px-4 py-3">
        <h2
          id="what-h"
          className="text-[15px] font-semibold leading-[22px] text-fg"
        >
          What alerts
        </h2>
      </div>
      <dl className="flex flex-col py-1">
        {RULES.map((r, i) => (
          <div
            key={r.kind}
            className={cn(
              "flex flex-col gap-0.5 px-4 py-[9px]",
              i > 0 && "border-t border-subtle",
            )}
          >
            <dt className="text-[13px] font-medium text-fg">
              {KIND_LABEL[r.kind]}
            </dt>
            <dd className="text-xs leading-[17px] text-fg/70">{r.fires}</dd>
            <dd className="text-xs leading-[17px] text-muted">{r.resolves}</dd>
          </div>
        ))}
      </dl>
      <p className="border-t border-subtle px-4 pb-3.5 pt-2.5 text-xs leading-[17px] text-muted">
        Containers are checked every 60 s, disks every 5 minutes. Alerts about
        the control-plane server, <span className="font-mono">panel</span>, go
        to every workspace an instance operator owns.
      </p>
    </aside>
  );
}

function RotateDialog({
  open,
  onClose,
  onRotated,
}: {
  open: boolean;
  onClose: () => void;
  onRotated: (secret: string) => void;
}) {
  const queryClient = useQueryClient();
  const rotate = useMutation({
    mutationFn: () => alertService.rotateSecret(),
    onSuccess: (ch) => {
      queryClient.invalidateQueries({
        queryKey: ["audit-log", "alerts.secret_rotated"],
      });
      rotate.reset();
      if (ch.signing_secret) onRotated(ch.signing_secret);
    },
  });
  return (
    <Dialog
      open={open}
      onOpenChange={(o) =>
        !o && !rotate.isPending && (onClose(), rotate.reset())
      }
    >
      <DialogContent
        role="alertdialog"
        className={cn(dialogContent, "max-w-[500px]")}
        showClose={false}
      >
        <DialogHeader className={cn(dialogHeader, "gap-1.5")}>
          <DialogTitle className={dialogTitle}>
            Rotate the signing secret?
          </DialogTitle>
          <DialogDescription className={dialogText}>
            The current secret stops working at once. A receiver that checks{" "}
            <span className="font-mono text-xs text-fg">X-Toft-Signature</span>{" "}
            rejects every alert until you give it the new secret, which is shown
            once, next.
          </DialogDescription>
        </DialogHeader>
        {rotate.error && (
          <p
            role="alert"
            className="mx-5 mt-3 rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg"
          >
            {message(rotate.error, "The secret was not rotated.")}
          </p>
        )}
        <div className="flex flex-wrap justify-end gap-2 px-5 pb-5 pt-[18px]">
          <button
            type="button"
            className={cancelButton}
            disabled={rotate.isPending}
            onClick={onClose}
          >
            Cancel
          </button>
          <button
            type="button"
            className={dangerButton}
            disabled={rotate.isPending}
            onClick={() => rotate.mutate()}
          >
            {rotate.isPending ? "Rotating…" : "Rotate secret"}
          </button>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function SecretDialog({
  secret,
  onClose,
}: {
  secret: { value: string; rotated: boolean } | null;
  onClose: () => void;
}) {
  const [copied, setCopied] = useState(false);
  return (
    <Dialog
      open={secret !== null}
      onOpenChange={(o) => !o && (onClose(), setCopied(false))}
    >
      <DialogContent
        className={cn(dialogContent, "max-w-[560px]")}
        showClose={false}
        onInteractOutside={(e) => e.preventDefault()}
      >
        <DialogHeader className={cn(dialogHeader, "gap-1.5")}>
          <DialogTitle className={dialogTitle}>
            {secret?.rotated
              ? "Copy the new signing secret"
              : "Copy the signing secret"}
          </DialogTitle>
          <DialogDescription className={dialogText}>
            This is the only time it is shown. Paste it into every receiver that
            checks the signature.
          </DialogDescription>
        </DialogHeader>
        <div className="mx-5 mt-4 flex flex-col gap-1.5">
          <label htmlFor="al-secret" className={fieldLabel}>
            Signing secret
          </label>
          <div className="flex items-start gap-2">
            <textarea
              id="al-secret"
              readOnly
              rows={2}
              value={secret?.value ?? ""}
              onFocus={(e) => e.target.select()}
              className="min-w-0 flex-1 resize-none rounded-lg border border-strong bg-ground px-2.5 py-2 font-mono text-xs leading-5 text-fg [overflow-wrap:anywhere]"
            />
            <button
              type="button"
              className={cancelButton}
              onClick={() =>
                secret &&
                navigator.clipboard.writeText(secret.value).then(
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
        <div className={dialogFooter}>
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

function RemoveDialog({
  open,
  workspace,
  onClose,
}: {
  open: boolean;
  workspace: string;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const remove = useMutation({
    mutationFn: () => alertService.delete(),
    onSuccess: () => {
      queryClient.setQueryData(CHANNEL_KEY, null);
      toast.success("Alert webhook removed");
      onClose();
      remove.reset();
    },
  });
  return (
    <Dialog
      open={open}
      onOpenChange={(o) =>
        !o && !remove.isPending && (onClose(), remove.reset())
      }
    >
      <DialogContent
        role="alertdialog"
        className={dialogContent}
        showClose={false}
      >
        <DialogHeader className={cn(dialogHeader, "gap-1.5")}>
          <DialogTitle className={dialogTitle}>
            Remove the alert webhook?
          </DialogTitle>
          <DialogDescription className={dialogText}>
            No alerts will be sent for {workspace}: not for failed deploys,
            services that go down or full disks. The signing secret is deleted
            with it. To stop alerts for a while and keep the webhook, untick
            Send alerts instead.
          </DialogDescription>
        </DialogHeader>
        {remove.error && (
          <p
            role="alert"
            className="mx-5 mt-3 rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg"
          >
            {message(remove.error, "The webhook was not removed.")}
          </p>
        )}
        <div className="flex flex-wrap justify-end gap-2 px-5 pb-5 pt-[18px]">
          <button
            type="button"
            className={cancelButton}
            disabled={remove.isPending}
            onClick={onClose}
          >
            Cancel
          </button>
          <button
            type="button"
            className={dangerButton}
            disabled={remove.isPending}
            onClick={() => remove.mutate()}
          >
            {remove.isPending ? "Removing…" : "Remove webhook"}
          </button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
