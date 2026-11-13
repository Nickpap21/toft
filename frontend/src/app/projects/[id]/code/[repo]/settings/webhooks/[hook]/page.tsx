"use client";

import { useState } from "react";
import Link from "next/link";
import { useParams, useRouter } from "next/navigation";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Check, ChevronDown, ChevronLeft, ChevronUp, Clock, RotateCcw, Send, X } from "lucide-react";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { ApiError, notFound } from "@/lib/api/client";
import { LoadFailed, NextPageFailed, readFailed, ReadFailed } from "@/components/shell/unreachable";
import type { WebhookDelivery, WebhookEvent } from "@/lib/api/code.service";
import { useOrgRole } from "@/context/AuthContext";
import { useDocumentTitle } from "@/lib/use-document-title";
import { cn } from "@/lib/utils";
import { EVENTS, SecretDialog } from "@/components/code/mirrors";
import { ago, errorCode, useCode } from "@/components/code/repo";
import { card, field, outline, small } from "@/app/projects/_lib/ui";

// One repository webhook (board RepositoryWebhook, owners; D-79): its
// deliveries with what was sent and answered, Redeliver and Send a ping,
// its settings, the signing secret and delete.
export default function WebhookPage() {
  const { hook: id } = useParams<{ hook: string }>();
  const { api, base, repo } = useCode();
  const owner = useOrgRole() === "owner";
  const queryClient = useQueryClient();
  const router = useRouter();
  const hook = useQuery({ queryKey: ["webhook", api.id, id], queryFn: () => api.webhook(id), enabled: owner, retry: false });
  const deliveries = useInfiniteQuery({
    queryKey: ["deliveries", api.id, id],
    queryFn: ({ pageParam }) => api.deliveries(id, pageParam || undefined),
    initialPageParam: "",
    getNextPageParam: (last) => last.next || undefined,
    enabled: owner,
    // Follow what waits or retries.
    refetchInterval: (q) => (q.state.data?.pages.some((p) => p.data.some((d) => d.state === "pending")) ? 3000 : 20_000),
  });
  // Relative times and waits count from when the deliveries were read.
  const now = deliveries.dataUpdatedAt || hook.dataUpdatedAt;
  useDocumentTitle(`Webhook · ${repo.name} · Toft`);
  const refresh = () => {
    queryClient.invalidateQueries({ queryKey: ["deliveries", api.id, id] });
    queryClient.invalidateQueries({ queryKey: ["webhook", api.id, id] });
    queryClient.invalidateQueries({ queryKey: ["webhooks", api.id] });
  };
  const ping = useMutation({
    mutationFn: () => api.pingWebhook(id),
    onSuccess: () => {
      refresh();
      toast.success("Ping sent");
    },
    onError: (err) => toast.error(errorCode(err) === "webhook_paused" ? "Turn the webhook on first" : err instanceof ApiError ? err.message : "Couldn’t send a ping"),
  });

  if (!owner) {
    return <p className={cn(card, "max-w-[640px] p-5 text-[13px] text-fg/70")}>Only owners see a repository’s webhooks.</p>;
  }
  if (hook.error && !notFound(hook.error) && !hook.data) {
    return <ReadFailed error={hook.error} onRetry={() => void hook.refetch()} />;
  }
  if (notFound(hook.error)) {
    return (
      <section className={cn(card, "flex max-w-[640px] flex-col gap-2 p-5")}>
        <h1 className="text-[15px] font-semibold text-fg">No such webhook</h1>
        <p className="text-[13px] text-fg/70">It may have been deleted.</p>
        <Link href={`${base}/settings`} className={cn(outline, "self-start")}>
          Repository settings
        </Link>
      </section>
    );
  }
  if (!hook.data) return <div role="status" aria-label="Loading" className="h-64 rounded-[10px] bg-surface motion-safe:animate-pulse" />;
  const w = hook.data;
  const host = (() => {
    try {
      return new URL(w.url).host;
    } catch {
      return w.url;
    }
  })();
  const list = deliveries.data?.pages.flatMap((p) => p.data) ?? [];
  return (
    <>
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div className="flex min-w-0 flex-col gap-1">
          <Link href={`${base}/settings`} className="inline-flex items-center gap-1 text-xs text-fg/70 hover:text-fg">
            <ChevronLeft className="h-3 w-3" aria-hidden="true" />
            Repository settings · Webhooks
          </Link>
          <h1 className="flex flex-wrap items-center gap-2.5 text-xl font-semibold leading-7 tracking-[-0.01em] text-fg">
            {host}
            <span
              className={cn(
                "inline-flex h-[22px] items-center gap-1.5 rounded-full px-2 text-xs font-medium tracking-normal",
                w.active ? "bg-status-running-soft text-status-running-fg" : "bg-raised text-fg/70",
              )}
            >
              <span aria-hidden="true" className={cn("h-1.5 w-1.5 rounded-full", w.active ? "bg-status-running" : "bg-muted")} />
              {w.active ? "On" : "Paused"}
            </span>
          </h1>
          <p className="text-[13px] text-fg/70">
            <span className="break-all font-mono text-xs text-fg">{w.url}</span>
            {w.created_by ? ` · added ${ago(w.created_at, now)} by ${w.created_by.name}` : ""}
          </p>
        </div>
        <button type="button" onClick={() => ping.mutate()} disabled={!w.active || ping.isPending} className={outline}>
          <Send className="h-3.5 w-3.5" aria-hidden="true" />
          Send a ping
        </button>
      </div>

      <div className="flex flex-wrap items-start gap-5">
        <section aria-labelledby="del-h" className={cn(card, "flex min-w-0 flex-[999_1_560px] flex-col")}>
          <div className="flex flex-wrap items-baseline justify-between gap-2 px-5 pb-3 pt-4">
            <h2 id="del-h" className="text-[15px] font-semibold leading-[22px] text-fg">
              Recent deliveries
            </h2>
            <span className="text-xs text-muted">Last 30 days, newest first</span>
          </div>
          {readFailed(deliveries) ? (
            <div className="border-t border-subtle px-5 py-3.5">
              <LoadFailed what="the deliveries" onRetry={() => void deliveries.refetch()} />
            </div>
          ) : deliveries.isLoading ? (
            <div role="status" aria-label="Loading" className="mx-5 mb-5 h-40 rounded-lg bg-raised/50 motion-safe:animate-pulse" />
          ) : list.length === 0 ? (
            <p className="border-t border-subtle px-5 py-4 text-[13px] text-muted">Nothing sent yet. Send a ping to check the URL.</p>
          ) : (
            <ul className="border-t border-subtle">
              {list.map((d, i) => (
                <DeliveryRow key={d.id} d={d} hookId={id} now={now} first={i === 0} active={w.active} onRedelivered={refresh} />
              ))}
            </ul>
          )}
          <NextPageFailed query={deliveries} what="older deliveries" className="border-t border-subtle px-4 py-3" />
          {deliveries.hasNextPage && (
            <div className="flex justify-center border-t border-subtle p-3">
              <button type="button" onClick={() => deliveries.fetchNextPage()} disabled={deliveries.isFetchingNextPage} className={small}>
                {deliveries.isFetchingNextPage ? "Loading…" : "Older"}
              </button>
            </div>
          )}
        </section>

        <aside className="flex min-w-0 flex-[1_1_340px] flex-col gap-4">
          <SettingsCard key={`${w.url} ${w.events.join()}`} url={w.url} events={w.events} active={w.active} onSaved={refresh} />
          <SecretCard rotatedAt={w.secret_rotated_at} rotatedBy={w.secret_rotated_by?.name} now={now} onRotated={refresh} />
          <DeleteCard url={w.url} onDeleted={() => router.push(`${base}/settings`)} />
        </aside>
      </div>
    </>
  );
}

const deliveryState = {
  delivered: { icon: Check, className: "bg-status-running-soft text-status-running-fg", label: "Delivered" },
  failed: { icon: X, className: "bg-status-failed-soft text-status-failed-fg", label: "Failed" },
  pending: { icon: Clock, className: "bg-env-amber-soft text-env-amber-fg", label: "Waiting" },
} as const;

function answerText(d: WebhookDelivery, now: number): { text: string; tone: string } {
  if (d.state === "pending") {
    const when = d.next_attempt_at ? Math.max(0, Math.round((new Date(d.next_attempt_at).getTime() - now) / 1000)) : 0;
    const wait = when < 60 ? `${when}s` : `${Math.floor(when / 60)}m ${when % 60}s`;
    return d.attempts > 0
      ? { text: `${d.response_status ?? "no answer"} · try ${d.attempts + 1} of 5 in ${wait}`, tone: "text-env-amber-fg" }
      : { text: "Sending", tone: "text-env-amber-fg" };
  }
  if (d.state === "delivered") return { text: `${d.response_status} · ${d.duration_ms ?? 0} ms`, tone: "text-status-running-fg" };
  return {
    text: `${d.response_status ?? "no answer"} after ${d.attempts === 1 ? "1 try" : `${d.attempts} tries`}${d.response_status && d.response_status < 500 && d.response_status !== 429 ? " · not retried" : ""}`,
    tone: "text-status-failed-fg",
  };
}

function DeliveryRow({
  d,
  hookId,
  now,
  first,
  active,
  onRedelivered,
}: {
  d: WebhookDelivery;
  hookId: string;
  now: number;
  first: boolean;
  active: boolean;
  onRedelivered: () => void;
}) {
  const { api } = useCode();
  const [open, setOpen] = useState(false);
  const detail = useQuery({ queryKey: ["delivery", api.id, hookId, d.id, d.attempts, d.state], queryFn: () => api.delivery(hookId, d.id), enabled: open });
  const redeliver = useMutation({
    mutationFn: () => api.redeliver(hookId, d.id),
    onSuccess: () => {
      onRedelivered();
      toast.success("Sending it again");
    },
    onError: (err) => toast.error(err instanceof ApiError ? err.message : "Couldn’t send it again"),
  });
  const st = deliveryState[d.state];
  const Icon = st.icon;
  const answer = answerText(d, now);
  const body = detail.data?.payload ? JSON.stringify(detail.data.payload, null, 2) : "";
  return (
    <li className={cn(!first && "border-t border-subtle", open && "bg-raised/30")}>
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        className="flex w-full flex-wrap items-center gap-3 px-5 py-[11px] text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-focus"
      >
        <span role="img" aria-label={st.label} className={cn("flex h-[22px] w-[22px] flex-none items-center justify-center rounded-full", st.className)}>
          <Icon className="h-3 w-3" strokeWidth={2.6} aria-hidden="true" />
        </span>
        <span className="flex min-w-0 flex-[1_1_260px] flex-col">
          <span className="flex flex-wrap items-center gap-1.5 text-[13px]">
            <span className="font-mono text-xs font-medium text-fg">{d.event}</span>
            <span className="text-fg/70 [overflow-wrap:anywhere]">{d.summary || (d.event === "ping" ? "a ping" : "being prepared")}</span>
          </span>
          <span className="font-mono text-[11px] text-muted">{d.id}</span>
        </span>
        <span className="flex flex-none flex-col items-end">
          <span className={cn("text-xs", answer.tone)}>{answer.text}</span>
          <span className="text-xs text-muted">{ago(d.created_at, now)}</span>
        </span>
        {open ? <ChevronUp className="h-3.5 w-3.5 text-muted" aria-hidden="true" /> : <ChevronDown className="h-3.5 w-3.5 text-muted" aria-hidden="true" />}
      </button>
      {open && (
        <div className="flex flex-col gap-3 px-5 pb-4 sm:pl-[54px]">
          {detail.isLoading ? (
            <div role="status" aria-label="Loading" className="h-32 rounded-lg bg-raised/50 motion-safe:animate-pulse" />
          ) : detail.data ? (
            <div className="flex flex-wrap gap-3">
              <div className="flex min-w-0 flex-[1_1_320px] flex-col overflow-hidden rounded-lg border border-subtle bg-ground">
                <div className="flex justify-between border-b border-subtle px-3 py-2 text-xs text-muted">
                  <span>Request</span>
                  <span className="font-mono">POST{body ? ` · ${(new Blob([body]).size / 1024).toFixed(1)} KiB` : ""}</span>
                </div>
                <pre role="group" tabIndex={0} aria-label="Request headers and payload" className="focus-inset max-h-[420px] overflow-auto whitespace-pre-wrap break-words px-3 py-2.5 font-mono text-xs leading-[18px] text-fg/85">
                  {Object.entries(detail.data.request_headers)
                    .map(([k, v]) => `${k}: ${v}`)
                    .join("\n")}
                  {body ? `\n\n${body}` : "The payload is built at the first attempt."}
                </pre>
              </div>
              <div className="flex min-w-0 flex-[1_1_220px] flex-col overflow-hidden rounded-lg border border-subtle bg-ground">
                <div className="flex justify-between border-b border-subtle px-3 py-2 text-xs text-muted">
                  <span>Response</span>
                  <span className={cn("font-mono", answer.tone)}>
                    {d.response_status ?? "—"}
                    {d.duration_ms != null ? ` · ${d.duration_ms} ms` : ""}
                  </span>
                </div>
                <pre role="group" tabIndex={0} aria-label="Response body" className="focus-inset max-h-[420px] min-h-[60px] overflow-auto whitespace-pre-wrap break-words px-3 py-2.5 font-mono text-xs leading-[18px] text-fg/85">
                  {detail.data.response_body || d.error || "No answer."}
                </pre>
                <p className="mt-auto border-t border-subtle px-3 py-2 text-xs text-muted">The first 2 KiB of the answer is kept.</p>
              </div>
            </div>
          ) : (
            readFailed(detail) && <LoadFailed what="the delivery" onRetry={() => void detail.refetch()} />
          )}
          <div className="flex flex-wrap items-center gap-2.5">
            <button type="button" onClick={() => redeliver.mutate()} disabled={!active || d.state === "pending" || redeliver.isPending} className={cn(small, "h-[30px] text-xs rounded-md")}>
              <RotateCcw className="h-[13px] w-[13px]" aria-hidden="true" />
              Redeliver
            </button>
            <span className="text-xs text-muted">
              Sends the same body and delivery ID again, so a receiver that dedupes on <code className="font-mono">X-Toft-Delivery</code> acts once.
            </span>
          </div>
        </div>
      )}
    </li>
  );
}

// Keyed by the saved URL and events (the page remounts it after a save), so
// the form starts from what is stored.
function SettingsCard({ url, events, active, onSaved }: { url: string; events: WebhookEvent[]; active: boolean; onSaved: () => void }) {
  const { api } = useCode();
  const { hook: id } = useParams<{ hook: string }>();
  const [u, setU] = useState(url);
  const [ev, setEv] = useState<WebhookEvent[]>(events);
  const dirty = u.trim() !== url || ev.length !== events.length || ev.some((e) => !events.includes(e));
  const save = useMutation({
    mutationFn: (change: { url?: string; events?: WebhookEvent[]; active?: boolean }) => api.updateWebhook(id, change),
    onSuccess: (_, change) => {
      onSaved();
      toast.success(change.active === undefined ? "Saved" : change.active ? "The webhook is on" : "Paused: nothing is sent");
    },
    onError: (err) => toast.error(errorCode(err) === "private_address" ? "That URL leads to an address that isn’t public." : err instanceof ApiError ? err.message : "Couldn’t save"),
  });
  return (
    <section aria-labelledby="set-h" className={cn(card, "flex flex-col gap-3.5 px-[18px] py-4")}>
      <h2 id="set-h" className="text-[15px] font-semibold leading-[22px] text-fg">
        Settings
      </h2>
      <label className="flex flex-col gap-1.5 text-[13px] font-medium text-fg">
        Payload URL
        <input value={u} onChange={(e) => setU(e.target.value)} spellCheck={false} autoComplete="off" className={cn(field, "h-[34px] font-mono text-xs")} />
      </label>
      <fieldset className="flex flex-col gap-2">
        <legend className="pb-1.5 text-[13px] font-medium text-fg">Send</legend>
        {EVENTS.map((e) => (
          <label key={e.id} className="flex items-center gap-2 text-[13px] text-fg">
            <input
              type="checkbox"
              checked={ev.includes(e.id)}
              onChange={(x) => setEv(x.target.checked ? [...ev, e.id] : ev.filter((y) => y !== e.id))}
              className="h-4 w-4 accent-[var(--color-accent)]"
            />
            {e.label}
          </label>
        ))}
      </fieldset>
      <div className="flex items-center justify-between gap-3 border-t border-subtle pt-3">
        <span className="flex flex-col">
          <span id="act-l" className="text-[13px] font-medium text-fg">
            Active
          </span>
          <span className="text-xs text-muted">Paused, nothing is sent and nothing queues.</span>
        </span>
        <button
          type="button"
          role="switch"
          aria-checked={active}
          aria-labelledby="act-l"
          disabled={save.isPending}
          onClick={() => save.mutate({ active: !active })}
          className={cn(
            "relative h-[22px] w-10 flex-none rounded-full transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-60",
            active ? "bg-accent" : "bg-strong",
          )}
        >
          <span className={cn("absolute top-[3px] h-4 w-4 rounded-full bg-on-accent transition-[translate] left-[3px]", active && "translate-x-[18px]")} />
        </button>
      </div>
      <div>
        <button type="button" onClick={() => save.mutate({ url: u.trim(), events: ev })} disabled={!dirty || ev.length === 0 || save.isPending} className={small}>
          Save
        </button>
      </div>
    </section>
  );
}

function SecretCard({ rotatedAt, rotatedBy, now, onRotated }: { rotatedAt: string; rotatedBy?: string; now: number; onRotated: () => void }) {
  const { api } = useCode();
  const { hook: id } = useParams<{ hook: string }>();
  const [confirming, setConfirming] = useState(false);
  const [secret, setSecret] = useState("");
  const rotate = useMutation({
    mutationFn: () => api.rotateWebhookSecret(id),
    onSuccess: (r) => {
      setConfirming(false);
      setSecret(r.secret);
      onRotated();
    },
    onError: (err) => toast.error(err instanceof ApiError ? err.message : "Couldn’t rotate the secret"),
  });
  const when = new Date(rotatedAt);
  const stamp = `${when.getFullYear()}-${String(when.getMonth() + 1).padStart(2, "0")}-${String(when.getDate()).padStart(2, "0")} ${String(when.getHours()).padStart(2, "0")}:${String(when.getMinutes()).padStart(2, "0")}`;
  return (
    <section aria-labelledby="sec-h" className={cn(card, "flex flex-col gap-2.5 px-[18px] py-4")}>
      <h2 id="sec-h" className="text-[15px] font-semibold leading-[22px] text-fg">
        Signing secret
      </h2>
      <p className="text-xs text-fg/70">
        Shown once, when the webhook was added or the secret rotated. Last rotated{" "}
        <span className="font-mono text-fg" title={ago(rotatedAt, now)}>
          {stamp}
        </span>
        {rotatedBy ? ` by ${rotatedBy}` : ""}.
      </p>
      <pre className="whitespace-pre-wrap rounded-lg border border-subtle bg-ground px-3 py-2.5 font-mono text-xs leading-[18px] text-fg/85">
        {'sig = "sha256=" + hex(hmac_sha256(secret, raw_body))\nconstant_time_equal(sig, header["X-Toft-Signature"])'}
      </pre>
      <div>
        <button type="button" onClick={() => setConfirming(true)} className={small}>
          Rotate secret…
        </button>
      </div>
      <p className="text-xs text-muted">The old secret stops at once: update the receiver right after.</p>
      <Dialog open={confirming} onOpenChange={setConfirming}>
        <DialogContent className="max-w-[460px]">
          <DialogHeader>
            <DialogTitle>Rotate the signing secret?</DialogTitle>
            <DialogDescription>The old one stops at once, with no grace period: deliveries are signed with the new one from now on, so update the receiver right after.</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <button type="button" onClick={() => setConfirming(false)} className="h-9 rounded-lg border border-subtle px-3 text-[13px] font-medium text-fg hover:bg-raised">
              Cancel
            </button>
            <button type="button" onClick={() => rotate.mutate()} disabled={rotate.isPending} className="h-9 rounded-lg bg-accent px-3.5 text-[13px] font-medium text-on-accent hover:bg-accent/90 disabled:opacity-50">
              {rotate.isPending ? "Rotating…" : "Rotate secret"}
            </button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <SecretDialog open={!!secret} value={secret} title="The new signing secret" onClose={() => setSecret("")} />
    </section>
  );
}

function DeleteCard({ url, onDeleted }: { url: string; onDeleted: () => void }) {
  const { api } = useCode();
  const { hook: id } = useParams<{ hook: string }>();
  const queryClient = useQueryClient();
  const [confirming, setConfirming] = useState(false);
  const remove = useMutation({
    mutationFn: () => api.deleteWebhook(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["webhooks", api.id] });
      toast.success("Webhook deleted");
      onDeleted();
    },
    onError: (err) => toast.error(err instanceof ApiError ? err.message : "Couldn’t delete the webhook"),
  });
  return (
    <section aria-labelledby="rm-h" className="flex flex-wrap items-center justify-between gap-2.5 rounded-[10px] border border-status-failed/35 bg-surface px-[18px] py-3.5">
      <span className="flex flex-col">
        <h2 id="rm-h" className="text-[13px] font-semibold leading-5 text-fg">
          Delete this webhook
        </h2>
        <span className="text-xs text-muted">Its deliveries go with it.</span>
      </span>
      <button type="button" onClick={() => setConfirming(true)} className="h-8 rounded-lg border border-status-failed/50 px-3 text-[13px] font-medium text-status-failed-fg hover:bg-status-failed-soft/40">
        Delete…
      </button>
      <Dialog open={confirming} onOpenChange={setConfirming}>
        <DialogContent className="max-w-[460px]">
          <DialogHeader>
            <DialogTitle>Delete this webhook?</DialogTitle>
            <DialogDescription>
              Toft stops posting to <span className="break-all font-mono text-xs">{url}</span>, and its deliveries go with it.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <button type="button" onClick={() => setConfirming(false)} className="h-9 rounded-lg border border-subtle px-3 text-[13px] font-medium text-fg hover:bg-raised">
              Cancel
            </button>
            <button type="button" onClick={() => remove.mutate()} disabled={remove.isPending} className="h-9 rounded-lg bg-danger px-3.5 text-[13px] font-semibold text-on-danger hover:bg-danger/90 disabled:opacity-50">
              {remove.isPending ? "Deleting…" : "Delete webhook"}
            </button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  );
}
