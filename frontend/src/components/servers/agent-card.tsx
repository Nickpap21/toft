"use client";

// The node agent on the Server page (D-106; board ServerAgent): the
// Connection card of an agent server, the banner and "While the agent is
// away" when it isn't connected, "Move to the agent" on an SSH server, and
// the dialogs to move, join again, revoke, what to do when it is lost, and
// which state to keep when it froze after a restore (D-113).

import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, Check, Clock, Copy, Info, RotateCw, X } from "lucide-react";
import { ApiError } from "@/lib/api/client";
import { serverService, type AgentJoin, type Server } from "@/lib/api/server.service";
import { cn, formatDateTime } from "@/lib/utils";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
  cancelButton, card, dangerButton, dialogContent, dialogFooter, dialogHeader, dialogText, dialogTitle, fieldInput, fieldLabel, primaryButton, smallButton,
} from "@/components/settings/settings-frame";
import { useReauth } from "@/components/reauth";
import { LoadFailed, readFailed } from "@/components/shell/unreachable";
import { ProvisionLog } from "@/components/servers/provision-run";
import { joinNames } from "@/components/previews/view";
import { Row, settingsFoot, settingsHead, titleRow } from "@/app/servers/[id]/aside";
import { btnSm } from "@/app/projects/_lib/ui";
import { TONE, agentChip, agentStatus, awayFor, clock, desiredRow, frozenItems, frozenRow } from "./agent";

const h2 = "text-[15px] font-semibold leading-[22px] text-fg";
const AGENT_WHY = "Moving a server, or replacing its agent, decides how the panel reaches it as root, so Toft asks for your password first.";

/** Re-renders every few seconds, for "38 s ago". */
export function useNow(every = 5000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), every);
    return () => clearInterval(t);
  }, [every]);
  return now;
}


const fpCls = "break-all font-mono text-xs leading-[18px] text-fg";

/** The Connection card of a server reached through its agent. */
export function AgentConnection({
  server,
  canAdmin,
  isOwner,
  onDialog,
}: {
  server: Server;
  canAdmin: boolean;
  isOwner: boolean;
  onDialog: (d: AgentDialogKind) => void;
}) {
  const now = useNow();
  const qc = useQueryClient();
  const a = server.agent;
  const status = agentStatus(server) ?? "waiting";
  const chip = agentChip(status);
  // The same query as the page's Running here: names for the drift.
  const { data: services } = useQuery({ queryKey: ["server-services", server.id], queryFn: () => serverService.listServices(server.id) });
  const names = Object.fromEntries((services ?? []).map((x) => [x.id, x.name]));
  const desired = a?.frozen ? frozenRow(a.frozen, names) : desiredRow(a?.desired, status, names, now);
  const rotate = useMutation({
    mutationFn: () => serverService.rotateAgentKey(server.id),
    onSuccess: (s) => {
      qc.setQueryData(["server", server.id], s);
      toast.success("The agent has a new key", { description: s.agent?.key_fingerprint ?? undefined });
    },
    onError: (err: Error) => toast.error(err.message || "The key wasn’t rotated"),
  });

  let note = "";
  if (status === "connected" && a?.since) note = `Since ${formatDateTime(a.since).slice(0, 16)} · last heard from ${a.last_seen_at ? awayFor(a.last_seen_at, now) : "now"} ago${a.rtt_ms != null ? ` · ${a.rtt_ms} ms away` : ""}`;
  if (status === "reconnecting") note = `Dropped ${a?.since ? awayFor(a.since, now) : "a moment"} ago. The agent retries every few seconds, backing off to a minute.`;
  if (status === "upgrading") note = "Signature verified; restarting. If the new version doesn’t connect within 2 minutes, the agent goes back to the old one by itself.";
  if (status === "offline") note = a?.last_seen_at ? `Last heard from ${clock(a.last_seen_at)}${a.from_ip ? ` from ${a.from_ip}` : ""}.` : "Not heard from since the panel started.";
  if (status === "waiting") note = a?.token_expires_at ? `Join token expires in ${awayFor(new Date().toISOString(), Date.parse(a.token_expires_at))}.` : "No join token works now: make one.";
  if (status === "revoked") note = `${a?.since ? `Revoked ${formatDateTime(a.since).slice(0, 16)}. ` : ""}The agent’s key is no longer accepted; the agent stopped itself.`;
  const statusText =
    status === "offline" && a?.since ? `Offline for ${awayFor(a.since, now)}` : status === "connected" ? "Connected" : status === "upgrading" ? "Upgrading" : chip.label;

  return (
    <section aria-labelledby="conn-h" className={card}>
      <div className={settingsHead}>
        <div className={titleRow}>
          <h2 id="conn-h" className={h2}>Connection</h2>
          <span className="text-xs text-muted">Agent · outbound from the server</span>
        </div>
      </div>
      <dl>
        <Row k="Status" first>
          <span role="status" className={cn("inline-flex items-center gap-1.5 text-[13px]", TONE[chip.tone].text)}>
            <span aria-hidden="true" className={cn("h-[7px] w-[7px] rounded-full bg-current", chip.pulse && "motion-safe:animate-pulse")} />
            {statusText}
          </span>
          {note && <span className="text-xs leading-[17px] text-muted">{note}</span>}
          {status === "waiting" && canAdmin && (
            <span className="mt-[3px] flex flex-wrap items-center gap-2">
              <button type="button" className={cn(smallButton, "font-medium")} onClick={() => onDialog("rejoin")}>Show join command</button>
            </span>
          )}
        </Row>
        {status !== "waiting" && status !== "revoked" && (
          <>
            <Row k="Version">
              <span className="font-mono text-xs text-fg">
                {a?.version ?? "—"}
                {a?.arch ? ` · linux/${a.arch}` : ""}
              </span>
              <span className="text-xs leading-[17px] text-muted">Updates itself when the panel does.</span>
            </Row>
            <Row k="Desired state">
              <span className={cn("text-[13px]", desired.tone ? TONE[desired.tone].text : status === "connected" ? "text-fg" : "text-fg/85")}>{desired.text}</span>
              {desired.list.length > 0 && (
                <ul className="mt-0.5 flex flex-col gap-[3px] text-xs leading-[17px]">
                  {desired.list.map((d, i) => (
                    <li key={i}>
                      {d.who === null ? (
                        <span className="text-fg/70">A container</span>
                      ) : d.id ? (
                        <span className="text-fg/70">
                          Service <span className="break-all font-mono text-muted">{d.who}</span>
                        </span>
                      ) : (
                        <span className="break-all font-mono text-fg">{d.who}</span>
                      )}{" "}
                      <span className="text-fg/70">{d.what}</span>
                    </li>
                  ))}
                  {desired.more > 0 && <li className="text-muted">and {desired.more} more</li>}
                </ul>
              )}
              <span className="text-xs leading-[17px] text-muted">{desired.note}</span>
            </Row>
            <Row k="Routes">
              {a?.routes != null ? (
                <>
                  <span className="text-[13px] text-fg">{a.routes}, written by the agent</span>
                  <span className="text-xs leading-[17px] text-muted">
                    {a.frozen ? "Unchanged since the restore. " : ""}Traefik reads them from files the agent keeps, not from the Docker socket.
                    {a.frozen ? "" : " A deploy switches its route in one step."}
                  </span>
                </>
              ) : (
                <>
                  <span className="text-[13px] text-fg/85">From the containers’ labels</span>
                  <span className="text-xs leading-[17px] text-muted">Traefik reads them from the Docker socket. An agent from v0.19.0 writes them to files once it connects.</span>
                </>
              )}
            </Row>
            <Row k="Agent key">
              <span className={fpCls}>
                <span className="text-muted">ssh-ed25519</span> {a?.key_fingerprint ?? "—"}
              </span>
              <span className="text-xs leading-[17px] text-muted">
                Made on the server{a?.key_pinned_at ? `, pinned ${formatDateTime(a.key_pinned_at).slice(0, 10)}` : ""}. Nobody else holds it.
              </span>
              {isOwner && (
                <span className="mt-[3px] flex flex-wrap items-center gap-2">
                  <button type="button" className={cn(smallButton, "font-medium")} disabled={status !== "connected" || rotate.isPending} onClick={() => rotate.mutate()}>
                    {rotate.isPending ? "Rotating…" : "Rotate key"}
                  </button>
                  <span className="text-xs text-muted">owners</span>
                </span>
              )}
            </Row>
            <Row k="Panel key">
              <span className={fpCls}>
                <span className="text-muted">ssh-ed25519</span> {a?.panel_key_fingerprint ?? "—"}
              </span>
              <span className="text-xs leading-[17px] text-muted">The agent accepts only this panel.</span>
            </Row>
          </>
        )}
        <Row k="SSH fallback">
          {server.ssh_key_stored ? (
            <>
              <span className="font-mono text-xs text-fg">{server.user}@{server.ip_address}:{server.port}</span>
              <span className="text-xs leading-[17px] text-muted">Kept. Used only if you move the server back to SSH.</span>
            </>
          ) : (
            <span className="text-xs leading-[17px] text-muted">No SSH key kept. Moving to SSH asks for one.</span>
          )}
        </Row>
      </dl>
      {canAdmin && status !== "waiting" && (
        <div className={settingsFoot}>
          <span className="flex flex-wrap gap-2">
            <button type="button" className={btnSm} onClick={() => onDialog("move-ssh")}>Move to SSH…</button>
            <button type="button" className={btnSm} onClick={() => onDialog("rejoin")}>Join again…</button>
          </span>
          {status !== "revoked" && (
            <button type="button" className="touch-target h-[30px] rounded-md px-2.5 text-xs font-medium text-status-failed-fg hover:bg-status-failed-soft" onClick={() => onDialog("revoke")}>
              Revoke agent…
            </button>
          )}
        </div>
      )}
    </section>
  );
}

/** The banner above the page while the agent isn't connected. */
export function AgentBanner({ server, canAdmin, onDialog }: { server: Server; canAdmin: boolean; onDialog: (d: AgentDialogKind) => void }) {
  const now = useNow();
  const status = agentStatus(server);
  const a = server.agent;
  if (status === "connected" && a?.frozen)
    return (
      <section role="status" className={cn("flex gap-3 rounded-[10px] border px-4 py-3.5", TONE.pending.chip, "border-current/30")}>
        <AlertTriangle className="mt-px h-[18px] w-[18px] flex-none" aria-hidden="true" />
        <div className="flex min-w-0 flex-1 flex-col gap-1">
          <span className="text-sm font-semibold text-fg">The panel was restored from a backup; this server kept its own state</span>
          <span className="text-[13px] leading-[19px] text-fg/85">
            The agent was last told what to run by the panel before the restore,{" "}
            {a.frozen.changes.some((c) => c.kind !== "same") ? "and it holds a newer state than the backup." : "and kept its own state, though what runs matches the backup."} Apps
            keep serving, and logs, shells and metrics work. Deploys and removals here wait until {canAdmin ? "you choose" : "an owner or operator chooses"} which state to keep.
          </span>
          {canAdmin && (
            <span className="mt-1.5 flex flex-wrap items-center gap-2">
              <button type="button" className={cn(primaryButton, "h-8 px-3")} onClick={() => onDialog("adopt")}>Adopt this server’s state…</button>
              <button type="button" className={cn(smallButton, "h-8 px-3 text-[13px] font-medium rounded-lg")} onClick={() => onDialog("use-panel")}>Use the panel’s state…</button>
              <span className="text-xs text-muted">owners and operators</span>
            </span>
          )}
        </div>
      </section>
    );
  if (!status || status === "connected") return null;
  const since = a?.since ? awayFor(a.since, now) : "";
  const b = {
    reconnecting: { tone: "pending" as const, icon: RotateCw, title: `The agent dropped its connection${since ? ` ${since} ago` : ""}`, body: "Apps here keep serving. Deploys, logs and shells wait for it to come back; nothing needs doing yet. After 2 minutes it counts as offline." },
    upgrading: { tone: "building" as const, icon: Info, title: "Upgrading the agent", body: "Takes a few seconds. Running services aren’t restarted. A deploy started now waits for the new agent." },
    offline: { tone: "failed" as const, icon: AlertTriangle, title: `The agent has been offline${since ? ` for ${since}` : ""}`, body: `The server’s apps keep serving through its own Traefik, but the panel can’t deploy here, read logs, open shells or read metrics.${a?.last_seen_at ? ` It was last heard from at ${clock(a.last_seen_at)}.` : ""}` },
    waiting: { tone: "building" as const, icon: Clock, title: `Waiting for ${server.name}’s agent to join`, body: "Run the join command on the server as root. Nothing can deploy here until it connects." },
    revoked: { tone: "failed" as const, icon: X, title: "This server’s agent was revoked", body: "Its key no longer opens a connection. To use the server again, join it with a new token or move it to SSH." },
  }[status];
  const Icon = b.icon;
  return (
    <section role="status" className={cn("flex gap-3 rounded-[10px] border px-4 py-3.5", TONE[b.tone].chip, "border-current/30")}>
      <Icon className="mt-px h-[18px] w-[18px] flex-none" aria-hidden="true" />
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        <span className="text-sm font-semibold text-fg">{b.title}</span>
        <span className="text-[13px] leading-[19px] text-fg/85">{b.body}</span>
        {canAdmin && (status === "offline" || status === "waiting" || status === "revoked") && (
          <span className="mt-1.5 flex flex-wrap gap-2">
            {status === "offline" && <button type="button" className={cn(smallButton, "h-8 px-3 text-[13px] font-medium rounded-lg")} onClick={() => onDialog("lost-help")}>What to do</button>}
            {status !== "offline" && <button type="button" className={cn(smallButton, "h-8 px-3 text-[13px] font-medium rounded-lg")} onClick={() => onDialog("rejoin")}>{status === "waiting" ? "Show join command" : "Join again"}</button>}
            {server.ssh_key_stored && <button type="button" className={cn(smallButton, "h-8 px-3 text-[13px] font-medium rounded-lg")} onClick={() => onDialog("move-ssh")}>Use SSH for now</button>}
          </span>
        )}
      </div>
    </section>
  );
}

/** What still works while the agent is away (board ServerAgent, lost). */
export function AwayCard() {
  const ok = (what: string, how: string) => ({ what, how, ok: true });
  const no = (what: string, how: string) => ({ what, how, ok: false });
  const rows = [
    ok("Apps keep serving.", "Traefik, certificates and containers run on the server without the panel."),
    no("Deploys wait.", "A deploy started now is refused with “agent offline”; one already running fails cleanly and can be run again."),
    no("Logs and shells pause.", "Each tab says so and resumes when the agent connects; lines written meanwhile are read back from the server."),
    ok("Metrics are kept.", "The agent keeps a reading a minute, up to 48 hours, and sends them when it connects."),
    no("Backups", "keep running on the server’s own timer; the panel shows the last result it read."),
    ok("Nothing is lost.", "The agent reconnects by itself, with backoff, for as long as it runs."),
  ];
  return (
    <section aria-labelledby="away-h" className={card}>
      <div className="border-b border-subtle px-4 py-3">
        <h2 id="away-h" className={h2}>While the agent is away</h2>
      </div>
      <ul className="flex flex-col px-4 pb-2.5 pt-1 text-[13px] leading-[19px]">
        {rows.map((r, i) => (
          <li key={r.what} className={cn("flex gap-2.5 py-2", i > 0 && "border-t border-subtle")}>
            {r.ok ? <Check className="mt-[3px] h-3.5 w-3.5 flex-none text-status-running" aria-hidden="true" /> : <Clock className="mt-[3px] h-3.5 w-3.5 flex-none text-status-pending" aria-hidden="true" />}
            <span>
              <span className="font-medium text-fg">{r.what}</span> <span className="text-fg/70">{r.how}</span>
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}

/** Offered on an SSH server: move it to the agent. */
export function MoveToAgentCard({ onMove }: { onMove: () => void }) {
  return (
    <section aria-labelledby="mv-h" className={cn(card, "flex flex-wrap items-center gap-x-4 gap-y-3 p-4")}>
      <div className="flex min-w-0 flex-[1_1_320px] flex-col">
        <h2 id="mv-h" className={h2}>Move to the agent</h2>
        <p className="text-[13px] leading-[19px] text-muted">
          The server dials out on 443: no SSH handshake per deploy, nothing inbound to open, and deploys that finish when the panel restarts. Running
          services aren’t touched.
        </p>
      </div>
      <button type="button" className={cancelButton} onClick={onMove}>Move to the agent…</button>
    </section>
  );
}

/** A one-time command, with its copy button. */
export function JoinCommand({ join, expired }: { join: AgentJoin; expired?: boolean }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="flex items-start gap-2 rounded-lg border border-strong bg-ground py-2.5 pl-3.5 pr-2.5">
      <code className={cn("min-w-0 flex-1 break-all font-mono text-xs leading-[19px]", expired ? "text-muted" : "text-fg")}>{join.command}</code>
      <button
        type="button"
        disabled={expired}
        className="inline-flex h-7 flex-none items-center gap-1 rounded-md border border-subtle bg-raised px-2.5 text-xs text-fg hover:bg-raised/70 disabled:opacity-50"
        onClick={() =>
          navigator.clipboard?.writeText(join.command).then(
            () => setCopied(true),
            () => toast.error("Couldn’t copy: select it and copy it"),
          )
        }
      >
        {copied ? <Check className="h-3 w-3" aria-hidden="true" /> : <Copy className="h-3 w-3" aria-hidden="true" />}
        {copied ? "Copied" : "Copy"}
      </button>
    </div>
  );
}

export type AgentDialogKind = "adopt" | "use-panel" | "move-agent" | "move-ssh" | "lost-help" | "rejoin" | "revoke";

function errText(err: unknown, fallback: string): string {
  return err instanceof ApiError || err instanceof Error ? err.message || fallback : fallback;
}

/** The agent's dialogs; kind null closes them. */
export function AgentDialogs({ server, kind, onClose }: { server: Server; kind: AgentDialogKind | null; onClose: () => void }) {
  return (
    <Dialog open={!!kind} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className={cn(dialogContent, "max-w-[560px]")}>
        {(kind === "adopt" || kind === "use-panel") && <ChooseState server={server} adopt={kind === "adopt"} onClose={onClose} />}
        {kind === "move-agent" && <MoveToAgent server={server} onClose={onClose} />}
        {kind === "move-ssh" && <MoveToSSH server={server} onClose={onClose} />}
        {kind === "lost-help" && <LostHelp server={server} onClose={onClose} />}
        {kind === "rejoin" && <Rejoin server={server} onClose={onClose} />}
        {kind === "revoke" && <Revoke server={server} onClose={onClose} />}
      </DialogContent>
    </Dialog>
  );
}

function Steps({ items }: { items: { t: string; d?: string; code?: string }[] }) {
  return (
    <ol className="flex flex-col gap-2.5">
      {items.map((it, i) => (
        <li key={i} className="flex gap-2.5 text-[13px] leading-[19px]">
          <span className="flex h-[22px] w-[22px] flex-none items-center justify-center rounded-full border border-strong font-mono text-[11px] text-fg/70">{i + 1}</span>
          <span className="flex min-w-0 flex-1 flex-col gap-1">
            <span>
              <span className="font-medium text-fg">{it.t}</span> {it.d && <span className="text-fg/70">{it.d}</span>}
            </span>
            {it.code && <code className="block break-all rounded-md border border-subtle bg-ground px-2.5 py-[7px] font-mono text-xs text-fg">{it.code}</code>}
          </span>
        </li>
      ))}
    </ol>
  );
}

function Head({ title, desc }: { title: string; desc: string }) {
  return (
    <DialogHeader className={dialogHeader}>
      <DialogTitle className={dialogTitle}>{title}</DialogTitle>
      <DialogDescription className={dialogText}>{desc}</DialogDescription>
    </DialogHeader>
  );
}

const FROZEN_WHY = "Choosing which state to keep decides what the panel deploys to this server from now on, so Toft asks for your password first.";

function ChooseState({ server, adopt, onClose }: { server: Server; adopt: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const confirm = useReauth();
  const { data: services } = useQuery({ queryKey: ["server-services", server.id], queryFn: () => serverService.listServices(server.id) });
  const frozen = server.agent?.frozen;
  const items = frozen ? frozenItems(frozen, Object.fromEntries((services ?? []).map((x) => [x.id, x.name])), adopt) : [];
  const cta = adopt ? "Adopt this server’s state" : "Use the panel’s state";
  const choose = useMutation({
    mutationFn: async () => {
      if (!(await confirm(FROZEN_WHY, { title: cta, button: "Confirm" }))) throw new Error("");
      return serverService.chooseAgentState(server.id, adopt);
    },
    onSuccess: (s) => {
      qc.setQueryData(["server", server.id], s);
      qc.invalidateQueries({ queryKey: ["servers"] });
      qc.invalidateQueries({ queryKey: ["server-services", server.id] });
      toast.success(adopt ? `${server.name}’s state adopted` : `${server.name} follows the panel again`);
      onClose();
    },
  });
  return (
    <>
      <Head
        title={adopt ? `Adopt ${server.name}’s state` : `Use the panel’s state for ${server.name}`}
        desc={
          adopt
            ? "The panel takes what runs on this server as the truth, in place of what the backup says. Nothing on the server is stopped, restarted or re-routed."
            : "The agent drops the state it held and follows the restored panel. Nothing is stopped, removed or re-routed now: the differences stay under Desired state, and the next deploy of each service puts it in step."
        }
      />
      <div className="flex flex-col gap-3 px-5 pt-1">
        {!frozen ? (
          <p className="text-[13px] leading-[19px] text-fg/70">Someone already chose: this server follows the panel again.</p>
        ) : items.length > 0 ? (
          <Steps items={items} />
        ) : (
          <p className="text-[13px] leading-[19px] text-fg/70">What runs here matches the backup: nothing changes but whose orders the agent takes.</p>
        )}
        {choose.isError && choose.error.message && (
          <p role="alert" className="flex items-start gap-2 rounded-lg bg-status-failed-soft px-3 py-2.5 text-[13px] text-status-failed-fg">
            <AlertTriangle className="mt-0.5 h-4 w-4 flex-none" aria-hidden="true" />
            <span className="min-w-0 break-words">{errText(choose.error, "Nothing was changed.")} The server is still frozen.</span>
          </p>
        )}
      </div>
      <div className={cn(dialogFooter, "items-center")}>
        <span className="flex-[1_1_200px] text-xs text-muted">
          Owners and instance operators · audited as {adopt ? "server.agent_state_adopted" : "server.agent_state_discarded"}.
        </span>
        <button type="button" className={cancelButton} onClick={onClose} disabled={choose.isPending}>Cancel</button>
        <button type="button" className={primaryButton} onClick={() => choose.mutate()} disabled={choose.isPending || !frozen}>
          {choose.isPending ? (adopt ? "Adopting…" : "Switching…") : cta}
        </button>
      </div>
    </>
  );
}

function MoveToAgent({ server, onClose }: { server: Server; onClose: () => void }) {
  const qc = useQueryClient();
  const confirm = useReauth();
  const [keep, setKeep] = useState(true);
  const [lines, setLines] = useState<string[]>([]);
  const move = useMutation({
    mutationFn: async () => {
      if (!(await confirm(AGENT_WHY, { title: "Move to the agent", button: "Confirm and move" }))) throw new Error("");
      setLines([]);
      await serverService.installAgent(server.id, keep, (l) => setLines((ls) => [...ls, l]));
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["server", server.id] });
      qc.invalidateQueries({ queryKey: ["servers"] });
      toast.success(`${server.name} now uses its agent`);
      onClose();
    },
  });
  return (
    <>
      <Head title={`Move ${server.name} to the agent`} desc="The panel installs the agent over SSH and switches to it once it connects and its key is pinned. Services running here aren’t stopped or restarted." />
      <div className="flex flex-col gap-3 px-5 pt-1">
        <Steps
          items={[
            { t: "Install.", d: "Copies toft-agent from this panel to the server and starts toft-agent.service." },
            { t: "Join.", d: "The agent makes its own key on the server and connects out on 443; the panel pins it." },
            { t: "Switch.", d: "Deploys, logs, shells and metrics go through the agent. A deploy running now finishes over SSH first." },
            { t: "Keep SSH.", d: "The SSH key stays as a fallback unless you untick below." },
          ]}
        />
        <label className="flex items-start gap-2.5 text-[13px] leading-[19px] text-fg">
          <input type="checkbox" className="mt-0.5 h-4 w-4 flex-none accent-[var(--link)]" checked={keep} onChange={(e) => setKeep(e.target.checked)} disabled={move.isPending} />
          Keep the SSH key as a fallback
        </label>
        {lines.length > 0 && <ProvisionLog lines={lines} live={move.isPending} />}
        {move.isError && move.error.message && (
          <p role="alert" className="flex items-start gap-2 rounded-lg bg-status-failed-soft px-3 py-2.5 text-[13px] text-status-failed-fg">
            <AlertTriangle className="mt-0.5 h-4 w-4 flex-none" aria-hidden="true" />
            <span className="min-w-0 break-words">{move.error.message} The server still uses SSH.</span>
          </p>
        )}
      </div>
      <div className={cn(dialogFooter, "items-center")}>
        <span className="flex-[1_1_200px] text-xs text-muted">Owners and instance operators · audited</span>
        <button type="button" className={cancelButton} onClick={onClose} disabled={move.isPending}>Cancel</button>
        <button type="button" className={primaryButton} onClick={() => move.mutate()} disabled={move.isPending}>
          {move.isPending ? "Moving…" : "Install and move"}
        </button>
      </div>
    </>
  );
}

function MoveToSSH({ server, onClose }: { server: Server; onClose: () => void }) {
  const qc = useQueryClient();
  const confirm = useReauth();
  const [user, setUser] = useState(server.user || "root");
  const [port, setPort] = useState(server.port || 22);
  const [key, setKey] = useState("");
  const move = useMutation({
    mutationFn: async () => {
      if (!(await confirm(AGENT_WHY, { title: "Move to SSH", button: "Confirm and move" }))) throw new Error("");
      return serverService.moveToSSH(server.id, { user, port, ...(key.trim() ? { ssh_key: key } : {}) });
    },
    onSuccess: (s) => {
      qc.setQueryData(["server", server.id], s);
      qc.invalidateQueries({ queryKey: ["servers"] });
      toast.success(`${server.name} now uses SSH`);
      onClose();
    },
  });
  const needKey = !server.ssh_key_stored && !key.trim();
  // Refused while services here run with a strict network (D-115): only the
  // agent answers their name lookups.
  const refusal = move.error instanceof ApiError
    ? (move.error.body as { data?: { code?: string; services?: { id: string; name: string }[] } } | null)?.data
    : undefined;
  const { data: here } = useQuery({ queryKey: ["server-services", server.id], queryFn: () => serverService.listServices(server.id) });
  if (refusal?.code === "strict_network_in_use") {
    const strict = refusal.services ?? [];
    const n = strict.length;
    return (
      <>
        <Head
          title={`${server.name} can’t move to SSH yet`}
          desc={`${n === 1 ? "One service here runs" : `${COUNT[n] ?? n} services here run`} Sandboxed with a strict network, and ${n === 1 ? "its" : "their"} name lookups go to this server’s agent. Over SSH nothing would answer them. Choose Sandboxed for ${n === 1 ? "it" : "each"} and deploy it, then move the server.`}
        />
        <div className="px-5 pt-1">
          <Steps
            items={strict.map((x) => {
              const env = here?.find((h) => h.id === x.id)?.environment_name;
              return { t: x.name, d: `${env ? `${env} · ` : ""}Settings → Isolation` };
            })}
          />
        </div>
        <div className={cn(dialogFooter, "items-center")}>
          <span className="flex-[1_1_200px] text-xs text-muted">Revoking the agent stays possible: it doesn’t wait for this.</span>
          <button type="button" className={cancelButton} onClick={onClose}>Close</button>
        </div>
      </>
    );
  }
  return (
    <>
      <Head
        title={`Move ${server.name} to SSH`}
        desc="The panel reaches the server over SSH again, with the key it kept. Running services aren’t touched. The agent stays installed and connects again later; it is used only if you move back."
      />
      <div className="flex flex-col gap-3 px-5 pt-1">
        <Steps items={[{ t: "Check.", d: "Connects over SSH and checks the pinned host key before sending any credential." }, { t: "Switch.", d: "Every call goes over SSH from now on." }]} />
        <div className="flex flex-col gap-2.5 rounded-lg border border-subtle bg-ground p-3">
          <div className="grid grid-cols-[minmax(0,1fr)_72px] gap-2.5">
            <label className="flex flex-col gap-1.5">
              <span className={fieldLabel}>User</span>
              <input className={cn(fieldInput, "h-8 font-mono text-xs")} value={user} onChange={(e) => setUser(e.target.value)} />
            </label>
            <label className="flex flex-col gap-1.5">
              <span className={fieldLabel}>Port</span>
              <input className={cn(fieldInput, "h-8 font-mono text-xs")} inputMode="numeric" value={String(port || "")} onChange={(e) => setPort(parseInt(e.target.value.replace(/\D/g, ""), 10) || 0)} />
            </label>
          </div>
          <label className="flex flex-col gap-1.5">
            <span className={fieldLabel}>{server.ssh_key_stored ? "Private key (kept from before; replace it only if it changed)" : "Private key"}</span>
            <textarea
              className="h-16 resize-y rounded-md border border-strong bg-ground p-2 font-mono text-xs text-fg placeholder:text-muted"
              placeholder={server.ssh_key_stored ? "•••••••• stored key ••••••••" : "-----BEGIN OPENSSH PRIVATE KEY-----"}
              spellCheck={false}
              value={key}
              onChange={(e) => setKey(e.target.value)}
            />
          </label>
        </div>
        {move.isError && move.error.message && (
          <p role="alert" className="flex items-start gap-2 rounded-lg bg-status-failed-soft px-3 py-2.5 text-[13px] text-status-failed-fg">
            <AlertTriangle className="mt-0.5 h-4 w-4 flex-none" aria-hidden="true" />
            <span className="min-w-0 break-words">{errText(move.error, "The server wasn’t moved.")} It still uses its agent.</span>
          </p>
        )}
      </div>
      <div className={cn(dialogFooter, "items-center")}>
        <span className="flex-[1_1_200px] text-xs text-muted">Instance operators and owners · audited</span>
        <button type="button" className={cancelButton} onClick={onClose} disabled={move.isPending}>Cancel</button>
        <button type="button" className={primaryButton} onClick={() => move.mutate()} disabled={move.isPending || needKey || !port}>
          {move.isPending ? "Checking…" : "Check and move"}
        </button>
      </div>
    </>
  );
}

const COUNT = ["", "One", "Two", "Three", "Four", "Five", "Six", "Seven", "Eight", "Nine"];

function LostHelp({ server, onClose }: { server: Server; onClose: () => void }) {
  return (
    <>
      <Head title="When an agent is offline" desc="The server’s apps keep serving. Work through these on the server’s console, in order." />
      <div className="px-5 pt-1">
        <Steps
          items={[
            { t: "Is the server up?", d: "Your provider’s console shows it. If it’s off, start it: the agent starts with it." },
            { t: "Is the agent running?", code: "systemctl status toft-agent && journalctl -u toft-agent -n 50" },
            { t: "Can it reach the panel?", d: "A firewall or proxy between them shows here:", code: "toft-agent check" },
            { t: "Its key or files were lost?", d: "Join it again with a new token: workloads keep running and the old key stops working." },
            { t: "No console at all?", d: server.ssh_key_stored ? "Use SSH for now, or remove the server." : "Remove the server, or add an SSH key with Move to SSH." },
          ]}
        />
      </div>
      <div className={dialogFooter}>
        <button type="button" className={cancelButton} onClick={onClose}>Close</button>
      </div>
    </>
  );
}

function Rejoin({ server, onClose }: { server: Server; onClose: () => void }) {
  const qc = useQueryClient();
  const confirm = useReauth();
  // A pinned or revoked agent's server rejoins: it replaces a machine Toft trusted.
  const replaces = !!server.agent?.rejoin;
  const make = useMutation({
    mutationFn: async () => {
      if (replaces && !(await confirm(AGENT_WHY, { title: "Join again", button: "Confirm" }))) throw new Error("");
      return serverService.joinToken(server.id);
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["server", server.id] }),
  });
  return (
    <>
      <Head
        title={replaces ? `Join ${server.name} again` : `Join ${server.name}`}
        desc={
          replaces
            ? "For an agent whose key was lost or whose server was rebuilt. The old key stops working when the new one joins; running services aren’t touched."
            : "Run the command on the server as root. It installs the agent, which connects out to this panel."
        }
      />
      <div className="flex flex-col gap-3 px-5 pt-1">
        {make.data ? (
          <>
            <JoinCommand join={make.data} />
            <p className="text-xs leading-[18px] text-muted">
              Works once, expires at {clock(make.data.expires_at)}. Then compare the fingerprint the server prints with the one this page shows when it joins.
              Making it made older commands useless.
            </p>
          </>
        ) : (
          <p className="text-[13px] leading-[19px] text-fg/70">
            {replaces ? "Making a command asks for your password: it can replace the agent’s pinned key." : "Each command works once, for an hour."}
          </p>
        )}
        {make.isError && make.error.message && (
          <p role="alert" className="text-[13px] text-status-failed-fg">{errText(make.error, "No command was made.")}</p>
        )}
      </div>
      <div className={dialogFooter}>
        <button type="button" className={cancelButton} onClick={onClose}>{make.data ? "Done" : "Cancel"}</button>
        {!make.data && (
          <button type="button" className={primaryButton} onClick={() => make.mutate()} disabled={make.isPending}>
            {make.isPending ? "Making…" : "Make join command"}
          </button>
        )}
      </div>
    </>
  );
}

function Revoke({ server, onClose }: { server: Server; onClose: () => void }) {
  const qc = useQueryClient();
  const [uninstall, setUninstall] = useState(true);
  const connected = server.agent?.status === "connected";
  const revoke = useMutation({
    mutationFn: () => serverService.revokeAgent(server.id, uninstall && connected),
    onSuccess: (s) => {
      qc.setQueryData(["server", server.id], s);
      qc.invalidateQueries({ queryKey: ["servers"] });
      toast.success(`${server.name}’s agent revoked`);
      onClose();
    },
  });
  // Strict services lose their name lookups with the agent (D-115): a
  // warning, not a block, since a compromised server can't wait.
  const here = useQuery({ queryKey: ["server-services", server.id], queryFn: () => serverService.listServices(server.id) });
  const strict = (here.data ?? []).filter((s) => s.effective_isolation === "sandboxed_strict").map((s) => s.name);
  return (
    <>
      <Head
        title={`Revoke ${server.name}’s agent`}
        desc="The panel closes the connection and forgets the agent’s key. The agent stops itself; services here keep running and serving, but nothing can deploy to this server until it joins again or moves to SSH."
      />
      <div className="flex flex-col gap-3 px-5 pt-1">
        {readFailed(here) && <LoadFailed what="the services that use this agent" onRetry={() => void here.refetch()} />}
        {strict.length > 0 && (
          <Steps
            items={[{
              t: `${joinNames(strict)} ${strict.length === 1 ? "loses its" : "lose their"} name lookups.`,
              d: `${strict.length === 1 ? "It runs" : "They run"} Sandboxed with a strict network, and this server’s agent answers ${strict.length === 1 ? "its" : "their"} lookups. ${strict.length === 1 ? "It keeps" : "They keep"} serving what needs no name; join the agent again to bring names back.`,
            }]}
          />
        )}
        <label className="flex items-start gap-2.5 text-[13px] leading-[19px] text-fg">
          <input type="checkbox" className="mt-0.5 h-4 w-4 flex-none accent-[var(--link)]" checked={uninstall && connected} disabled={!connected} onChange={(e) => setUninstall(e.target.checked)} />
          <span>
            Also remove the agent from the server {connected ? "(while it’s still connected)" : <span className="text-muted">(only while it’s connected)</span>}
          </span>
        </label>
        {revoke.isError && <p role="alert" className="text-[13px] text-status-failed-fg">{errText(revoke.error, "The agent wasn’t revoked.")}</p>}
      </div>
      <div className={cn(dialogFooter, "items-center")}>
        <span className="flex-[1_1_200px] text-xs text-muted">Use this if the server or its key may be compromised.</span>
        <button type="button" className={cancelButton} onClick={onClose}>Cancel</button>
        <button type="button" className={dangerButton} onClick={() => revoke.mutate()} disabled={revoke.isPending}>
          {revoke.isPending ? "Revoking…" : "Revoke agent"}
        </button>
      </div>
    </>
  );
}
