"use client";

import { useCallback, useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { ApiError } from "@/lib/api/client";
import { setupService } from "@/lib/api/instance.service";
import { useAuth } from "@/context/AuthContext";
import { formatDateTime } from "@/lib/utils";
import { useReauth } from "@/components/reauth";
import { useInstanceHost } from "@/components/shell/auth-shell";
import { setupBack, setupLead } from "@/components/setup/setup-frame";

// The recovery kit as the wizard's last step and Instance → Recovery kit
// both show it (D-62, D-163): after a password confirmation, the key never
// on the screen.

const WHY = "The recovery kit holds the key to every secret the panel stores, so Toft asks for your password first.";
const button =
  "inline-flex h-9 pointer-coarse:h-11 items-center rounded-lg border border-strong/60 bg-raised px-3 text-[13px] font-medium text-fg hover:bg-surface focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-link";

export const kitFailure = (err: unknown) => (err instanceof ApiError && err.status < 500 ? err.message : "Couldn’t reach Toft. Try again.");

/** 409 kit_changed: the key or the backup settings changed after the kit was read. */
export const kitChanged = (err: unknown) => err instanceof ApiError && (err.body as { data?: { code?: string } } | null)?.data?.code === "kit_changed";

/** The kit, read once the password is confirmed. The prompt opens with the
 *  page; `ask` opens it again after a cancel, and reads the kit again after
 *  a failed read or a change, behind a new prompt when the confirmation ran
 *  out. `heading` takes focus when the prompt closes with nothing to give it
 *  back to: it opened with the page, or its button left with the answer. */
export function useRecoveryKit(heading: React.RefObject<HTMLElement | null>) {
  const confirm = useReauth();
  // "ask" until the password prompt answers; "no" when it was cancelled.
  const [confirmed, setConfirmed] = useState<"ask" | "yes" | "no">("ask");
  // Counts the confirmations: each reads the kit anew, never the one before.
  const [read, setRead] = useState(0);
  const ask = useCallback(
    () =>
      confirm(WHY, { returnFocus: heading }).then((ok) => {
        setConfirmed(ok ? "yes" : "no");
        if (ok) setRead((n) => n + 1);
      }),
    [confirm, heading],
  );
  useEffect(() => {
    void ask();
  }, [ask]);
  // Never cached past this page: it holds the encryption key.
  const query = useQuery({
    queryKey: ["instance", "recovery-kit", read],
    queryFn: setupService.recoveryKit,
    enabled: confirmed === "yes",
    gcTime: 0,
    staleTime: Infinity,
    retry: false,
  });
  return { confirmed, ask, query, kit: confirmed === "yes" ? (query.data ?? null) : null };
}

/** What the kit is for, then what it holds with Download and Copy, or why
 *  it isn't shown yet. `buttons` are the page's own outside the wizard, so
 *  their height and focus ring match their neighbours'. */
export function KitFile({
  confirmed,
  ask,
  query,
  kit,
  buttons = { main: button, quiet: setupBack },
}: ReturnType<typeof useRecoveryKit> & { buttons?: { main: string; quiet: string } }) {
  const { user } = useAuth();
  const host = useInstanceHost();
  const [today] = useState(() => formatDateTime(Date.now()).slice(0, 10));
  const lead = (
    <p className={setupLead}>
      If this server is lost, the kit and a backup bring Toft back on a new one. It holds the key your secrets are encrypted with: without
      it, nobody can read them again, Toft included.
    </p>
  );
  if (!kit)
    return (
      <>
        {lead}
        <div className="flex flex-col items-start gap-2.5 rounded-lg border border-subtle bg-ground p-3">
          <p className="text-[13px] text-fg/70">
            {confirmed === "no"
              ? "Toft shows the kit once you confirm your password."
              : confirmed === "ask"
                ? "Confirm your password to see the kit."
                : query.error
                  ? kitFailure(query.error)
                  : "Loading the kit…"}
          </p>
          {confirmed === "no" && (
            <button type="button" onClick={() => void ask()} className={buttons.main}>
              Show the kit…
            </button>
          )}
          {confirmed === "yes" && query.error && (
            <button type="button" onClick={() => void ask()} className={buttons.main}>
              Try again
            </button>
          )}
        </div>
      </>
    );
  const download = () => {
    const url = URL.createObjectURL(new Blob([kit.text], { type: "text/plain;charset=utf-8" }));
    const a = Object.assign(document.createElement("a"), { href: url, download: kit.filename });
    a.click();
    URL.revokeObjectURL(url);
  };
  return (
    <>
      {lead}
      {/* The key stays off the screen: Download and Copy carry it. */}
      <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 rounded-lg border border-subtle bg-ground p-3 font-mono text-xs leading-[18px] text-fg/85 [overflow-wrap:anywhere]">
        <dt className="text-muted">panel</dt>
        <dd>{kit.domain || host}</dd>
        <dt className="text-muted">key</dt>
        <dd>
          <span aria-hidden="true">●●●●●●●●●●●●●●●●●●●●●●●●</span>
          <span className="sr-only">hidden; it is in the file</span>
        </dd>
        {kit.backup && (
          <>
            <dt className="text-muted">backup</dt>
            <dd>{kit.backup.repository}</dd>
          </>
        )}
        <dt className="text-muted">created</dt>
        <dd>
          {today}
          {user?.email ? ` by ${user.email}` : ""}
        </dd>
      </dl>
      <div className="flex flex-wrap gap-2">
        <button type="button" className={buttons.main} onClick={download}>
          Download the kit
        </button>
        <button
          type="button"
          className={buttons.quiet}
          onClick={() =>
            navigator.clipboard.writeText(kit.text).then(
              () => toast.success("Kit copied"),
              () => toast.error("Couldn’t copy. Use Download."),
            )
          }
        >
          Copy
        </button>
      </div>
    </>
  );
}

/** The acknowledgement both pages record; off until the kit is shown. */
export function KitStored({ checked, disabled, onChange }: { checked: boolean; disabled: boolean; onChange: (checked: boolean) => void }) {
  return (
    <label className={`flex items-start gap-2.5 text-[13px] pointer-coarse:min-h-11 pointer-coarse:items-center ${disabled ? "cursor-not-allowed opacity-60" : "cursor-pointer"}`}>
      <input
        type="checkbox"
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
        className="mt-0.5 h-4 w-4 flex-none accent-[var(--link)]"
      />
      <span>I stored it somewhere other than this server, such as a password manager.</span>
    </label>
  );
}
