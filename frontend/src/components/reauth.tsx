"use client";

import { createContext, useCallback, useContext, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
  cancelButton, dialogContent, dialogHeader, dialogText, dialogTitle, fieldInput, fieldLabel, help, primaryButton,
} from "@/components/settings/settings-frame";
import { waitFor } from "@/components/shell/auth";
import { ApiError, reauthProof } from "@/lib/api/client";
import { serverService } from "@/lib/api/server.service";
import { cn } from "@/lib/utils";

/** What a host shell says when its password confirmation was cancelled. */
export const NEEDS_PASSWORD = "The host shell needs your password.";

/** Resolves true once this tab's session has its password confirmed (at
 *  once while a confirmation lasts), false when the person cancels. `why`
 *  is the dialog's sentence; host shells and host keys by default. ask
 *  names what is confirmed, as a title and a button. */
type Confirm = (why?: string, ask?: Ask) => Promise<boolean>;
type Ask = {
  title?: string;
  button?: string;
  /** Where focus goes at close when nothing had it at open, or what had it
   *  is gone: a prompt that opens with its page (DialogContent's returnFocus). */
  returnFocus?: React.RefObject<HTMLElement | null>;
};

const HOST_WHY = "Host shells and host keys act on the server itself, so Toft asks for your password first.";

/** Granting or revoking a deploy policy exception (D-53): what it changes,
 *  not the host shells' sentence. */
export const GRANT_EXCEPTION_WHY = "An exception lets this service deploy what the deploy policy refuses, so Toft asks for your password first.";
export const REVOKE_EXCEPTION_WHY = "Revoking an exception changes what this service may deploy, so Toft asks for your password first.";

const ReauthContext = createContext<Confirm>(async () => false);

/** Host shells and host-key replacement need the operator's password,
 *  confirmed once per 10 minutes in each tab (D-23, D-53). Call the hook's
 *  function before one and stop when it resolves false. */
export function useReauth(): Confirm {
  return useContext(ReauthContext);
}

export function ReauthProvider({ children }: { children: React.ReactNode }) {
  // Everyone waiting for the dialog; two asks at once share one prompt.
  const [waiting, setWaiting] = useState<((ok: boolean) => void)[]>([]);
  const [why, setWhy] = useState(HOST_WHY);
  const [ask, setAsk] = useState<Ask>({});
  const confirm = useCallback<Confirm>((reason, a) => {
    if (reauthProof()) return Promise.resolve(true);
    setWhy(reason ?? HOST_WHY);
    setAsk(a ?? {});
    return new Promise<boolean>((resolve) => setWaiting((w) => [...w, resolve]));
  }, []);
  const done = (ok: boolean) => {
    for (const resolve of waiting) resolve(ok);
    setWaiting([]);
  };

  return (
    <ReauthContext.Provider value={confirm}>
      {children}
      <ReauthDialog open={waiting.length > 0} why={why} ask={ask} onDone={done} />
    </ReauthContext.Provider>
  );
}

export function reauthError(err: unknown): string {
  if (!(err instanceof ApiError)) return "Couldn’t reach Toft. Try again.";
  const code = (err.body as { data?: { code?: unknown } } | null)?.data?.code;
  if (code === "wrong_password") return "Your password is incorrect.";
  if (err.status === 429) return `Too many attempts. Try again in ${waitFor(err)}.`;
  const msg = err.message;
  return msg.charAt(0).toUpperCase() + msg.slice(1) + (msg.endsWith(".") ? "" : ".");
}

function ReauthDialog({ open, why, ask, onDone }: { open: boolean; why: string; ask: Ask; onDone: (ok: boolean) => void }) {
  const [password, setPassword] = useState("");
  const confirm = useMutation({
    mutationFn: (pw: string) => serverService.confirmPassword(pw),
    onSuccess: () => finish(true),
  });
  const finish = (ok: boolean) => {
    setPassword("");
    confirm.reset();
    onDone(ok);
  };

  return (
    <Dialog open={open} onOpenChange={(o) => !o && !confirm.isPending && finish(false)}>
      <DialogContent showClose={false} className={dialogContent} returnFocus={ask.returnFocus}>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (password) confirm.mutate(password);
          }}
        >
          <DialogHeader className={cn(dialogHeader, "gap-1.5")}>
            <DialogTitle className={dialogTitle}>{ask.title ?? "Confirm your password"}</DialogTitle>
            <DialogDescription className={dialogText}>
              {why}
            </DialogDescription>
          </DialogHeader>
          <div className="mx-5 mt-4 flex flex-col gap-1.5">
            <label htmlFor="reauth-pw" className={fieldLabel}>
              Your password
            </label>
            <input
              id="reauth-pw"
              type="password"
              autoComplete="current-password"
              autoFocus
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              aria-describedby="reauth-pw-help"
              className={cn(fieldInput, "h-[34px]")}
            />
            <span id="reauth-pw-help" className={help}>
              Good for 10 minutes in this tab.
            </span>
          </div>
          {confirm.error && (
            <p
              role="alert"
              className="mx-5 mt-3 rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg"
            >
              {reauthError(confirm.error)}
            </p>
          )}
          <div className="flex flex-wrap justify-end gap-2 px-5 pb-5 pt-[18px]">
            <button type="button" className={cancelButton} disabled={confirm.isPending} onClick={() => finish(false)}>
              Cancel
            </button>
            <button type="submit" className={primaryButton} disabled={confirm.isPending || !password}>
              {confirm.isPending ? "Confirming…" : (ask.button ?? "Confirm")}
            </button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
