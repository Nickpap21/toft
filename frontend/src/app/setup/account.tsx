"use client";

import { useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ApiError } from "@/lib/api/client";
import { registrationStatusQuery } from "@/lib/api/instance.service";
import { useAuth } from "@/context/AuthContext";
import { possessive } from "@/lib/utils";
import { PASSWORD_MIN, PASSWORD_TOO_LONG, passwordTooLong } from "@/lib/password";
import { Notice } from "@/components/shell/auth-shell";
import { waitFor } from "@/components/shell/auth";
import {
  SetupFrame,
  nextStep,
  setupBody,
  setupCard,
  setupFoot,
  setupInput,
  setupLabel,
  setupPrimary,
} from "@/components/setup/setup-frame";

const help = "text-xs leading-[18px] text-muted";

/**
 * Step 1 on an empty instance (7g, board ReworkSetup): the first account owns
 * the first workspace and is the instance operator. The server lets one
 * through: anyone who loses the race gets registration_closed and goes to
 * sign in.
 */
export function AccountStep({ setupTokenRequired }: { setupTokenRequired: boolean }) {
  const [code, setCode] = useState("");
  const [name, setName] = useState("");
  const [orgName, setOrgName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  // Why the setup code was refused: it doesn't match, or its time is up.
  const [badCode, setBadCode] = useState<"wrong" | "expired" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const codeRef = useRef<HTMLInputElement>(null);
  const router = useRouter();
  const queryClient = useQueryClient();
  const { register } = useAuth();
  // register() signs in with the session the server returns, so there is no
  // second /auth call that could fail after the account exists.
  const signUp = useMutation({ mutationFn: register });
  const long = passwordTooLong(password);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setBadCode(null);
    if (long) return;
    try {
      await signUp.mutateAsync({
        name,
        email,
        password,
        ...(orgName.trim() ? { org_name: orgName.trim() } : {}),
        ...(code.trim() ? { setup_token: code.trim() } : {}),
      });
    } catch (err: unknown) {
      const errCode = err instanceof ApiError ? (err.body as { data?: { code?: string } } | null)?.data?.code : undefined;
      if (errCode === "registration_closed") {
        void queryClient.invalidateQueries({ queryKey: registrationStatusQuery.queryKey });
        toast.error("This Toft is already set up. Sign in.");
        router.replace("/?next=/setup");
        return;
      }
      if (err instanceof ApiError && err.status === 403 && setupTokenRequired) {
        setBadCode(errCode === "setup_token_expired" ? "expired" : "wrong");
        codeRef.current?.focus();
        return;
      }
      if (err instanceof ApiError && err.status === 429) {
        setError(`Too many attempts. Try again in ${waitFor(err)}.`);
        return;
      }
      setError(err instanceof ApiError && err.status < 500 ? err.message : "Couldn’t reach Toft. Try again.");
      return;
    }
    // The account exists now: not a first run any more, without a refetch
    // that would flip this page on the way out.
    queryClient.setQueryData(registrationStatusQuery.queryKey, (s) => (s ? { ...s, first_run: false } : s));
    router.replace(nextStep("owner"));
  };

  return (
    <SetupFrame step="owner" title="Create your account">
      <form onSubmit={submit} className={setupCard}>
        <div className={setupBody}>
          {badCode && (
            <div id="a-code-error">
              <Notice kind="error">
                {badCode === "expired"
                  ? "That setup code has expired. Run the installer again on the server: it prints a new code and changes nothing else."
                  : "That setup code doesn’t match. Copy it again from the installer’s output."}
              </Notice>
            </div>
          )}
          {error && <Notice kind="error">{error}</Notice>}
          {setupTokenRequired && (
            <div className="flex flex-col gap-1.5">
              <label htmlFor="a-code" className={setupLabel}>Setup code</label>
              <input
                ref={codeRef}
                autoFocus
                id="a-code"
                name="setup_token"
                value={code}
                onChange={(e) => setCode(e.target.value)}
                required
                autoComplete="one-time-code"
                spellCheck={false}
                placeholder="XXXX-XXXX"
                aria-invalid={badCode !== null || undefined}
                aria-describedby={badCode ? "a-code-error a-code-help" : "a-code-help"}
                className={`${setupInput} font-mono`}
              />
              <p id="a-code-help" className={help}>
                The installer printed it. It works for 30 minutes; run the installer again for a new one.
              </p>
            </div>
          )}
          <div className="grid grid-cols-1 gap-3.5 sm:grid-cols-2">
            <div className="flex min-w-0 flex-col gap-1.5">
              <label htmlFor="a-name" className={setupLabel}>Your name</label>
              <input id="a-name" name="name" autoComplete="name" value={name} onChange={(e) => setName(e.target.value)} required minLength={2} maxLength={100} className={setupInput} />
            </div>
            <div className="flex min-w-0 flex-col gap-1.5">
              <label htmlFor="a-ws" className={setupLabel}>
                Workspace <span className="font-normal text-muted">optional</span>
              </label>
              <input
                id="a-ws"
                name="org_name"
                autoComplete="organization"
                value={orgName}
                onChange={(e) => setOrgName(e.target.value)}
                maxLength={100}
                placeholder={name.trim() ? `${possessive(name.trim())} workspace` : undefined}
                className={setupInput}
              />
            </div>
          </div>
          <div className="flex flex-col gap-1.5">
            <label htmlFor="a-mail" className={setupLabel}>Email</label>
            <input id="a-mail" type="email" name="email" autoComplete="email" value={email} onChange={(e) => setEmail(e.target.value)} required maxLength={254} className={`${setupInput} font-mono`} />
          </div>
          <div className="flex flex-col gap-1.5">
            <label htmlFor="a-pw" className={setupLabel}>Password</label>
            <input
              id="a-pw"
              type="password"
              name="password"
              autoComplete="new-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
              minLength={PASSWORD_MIN}
              aria-describedby="a-pw-help"
              aria-invalid={long || undefined}
              className={setupInput}
            />
            <p id="a-pw-help" className={long ? `${help} text-status-failed-fg` : help}>
              {long ? PASSWORD_TOO_LONG : `At least ${PASSWORD_MIN} characters. You’ll need it to change protected things.`}
            </p>
          </div>
          <p className={help}>This first account owns the workspace and looks after this Toft. Everyone after it joins by invite.</p>
        </div>
        <div className={`${setupFoot} justify-end`}>
          <button type="submit" disabled={signUp.isPending} className={setupPrimary}>
            {signUp.isPending ? "Creating account…" : "Create account"}
          </button>
        </div>
      </form>
    </SetupFrame>
  );
}
