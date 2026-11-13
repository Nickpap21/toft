"use client";

import { useEffect, useState } from "react";
import { useParams } from "next/navigation";
import Link from "next/link";
import { Ban, Clock, Link2 } from "lucide-react";
import { instanceService, type PasswordResetTarget } from "@/lib/api/instance.service";
import { useAuth } from "@/context/AuthContext";
import { useDocumentTitle } from "@/lib/use-document-title";
import { formatDateTime } from "@/lib/utils";
import {
    AuthCard,
    AuthColumn,
    AuthShell,
    Notice,
    authButton,
    authH1,
    authHelp,
    authInput,
    authLabel,
    authLink,
} from "@/components/shell/auth-shell";
import { resetCode } from "@/components/shell/auth";
import { PASSWORD_MIN, PASSWORD_MIN_HELP, PASSWORD_TOO_LONG, passwordTooLong } from "@/lib/password";
import { fullPageLoad } from "@/lib/full-page-load";

// Set a new password from an operator's reset link (D-56; board:
// private/design/canvas/ResetPassword.dc.html).

export default function ResetPasswordPage() {
    const params = useParams();
    const token = decodeURIComponent(String(params.token ?? ""));
    const { login } = useAuth();
    const [target, setTarget] = useState<PasswordResetTarget | null>(null);
    const [dead, setDead] = useState<{ code: string; message: string } | null>(null);
    const [loading, setLoading] = useState(true);
    const [password, setPassword] = useState("");
    const [revokeAccess, setRevokeAccess] = useState(false);
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState<string | null>(null);
    // The password is set; signing in with it failed (rate limit, network).
    const [setButNotIn, setSetButNotIn] = useState(false);
    useDocumentTitle("Set a new password · Toft");

    const fail = (err: unknown) => setDead({ code: resetCode(err), message: err instanceof Error ? err.message : "" });

    useEffect(() => {
        instanceService.getPasswordReset(token)
            .then(setTarget)
            .catch(fail)
            .finally(() => setLoading(false));
    }, [token]);

    const submit = async (e: React.FormEvent) => {
        e.preventDefault();
        if (!target) return;
        if (passwordTooLong(password)) {
            setError(PASSWORD_TOO_LONG);
            return;
        }
        setBusy(true);
        setError(null);
        try {
            await instanceService.redeemPasswordReset(token, password, revokeAccess);
        } catch (err: unknown) {
            if (resetCode(err)) fail(err);
            else setError(err instanceof Error ? err.message : "Couldn’t set the password. Try again.");
            setBusy(false);
            return;
        }
        try {
            await login(target.email, password);
            // Signed in as this account now, maybe over another one's
            // session in this browser: a full load (fullPageLoad).
            fullPageLoad("/dashboard");
        } catch {
            setSetButNotIn(true);
            setBusy(false);
        }
    };

    if (loading) {
        return (
            <AuthShell section="Password" className="pt-14">
                <AuthColumn>
                    <span className="sr-only" role="status">Checking link…</span>
                    <div className="h-[34px] w-2/3 rounded bg-surface motion-safe:animate-pulse" />
                    <div className="h-64 rounded-[10px] bg-surface motion-safe:animate-pulse" />
                </AuthColumn>
            </AuthShell>
        );
    }

    if (dead || !target) {
        return (
            <AuthShell section="Password" className="pt-14">
                <AuthColumn className="gap-3">
                    <DeadLink code={dead?.code ?? ""} message={dead?.message ?? ""} />
                </AuthColumn>
            </AuthShell>
        );
    }

    if (setButNotIn) {
        return (
            <AuthShell section="Password" className="pt-14">
                <AuthColumn className="gap-3">
                    <h1 className={authH1}>Password set</h1>
                    <p role="status" className="text-fg/70">
                        Sign in with it. <Link href="/" className={authLink}>Sign in</Link>
                    </p>
                </AuthColumn>
            </AuthShell>
        );
    }

    return (
        <AuthShell section="Password" className="pt-14">
            <AuthColumn>
                <div className="flex flex-col gap-2">
                    <h1 className={authH1}>Set a new password</h1>
                    <p className="text-fg/70">
                        For <span className="font-mono text-[13px] text-fg">{target.email}</span>. The link works once, until{" "}
                        <span className="whitespace-nowrap font-mono text-[13px] text-fg">{formatDateTime(target.expires_at)}</span>.
                        Every other session of this account ends when you set it.
                    </p>
                </div>
                <AuthCard>
                    <form onSubmit={submit} className="flex flex-col gap-4">
                        {error && <Notice kind="error" alert>{error}</Notice>}
                        <div className="flex flex-col gap-1.5">
                            <label htmlFor="reset-email" className={authLabel}>Email</label>
                            <input id="reset-email" type="email" name="email" value={target.email} readOnly autoComplete="username" className={`${authInput} text-fg/70`} />
                        </div>
                        <div className="flex flex-col gap-1.5">
                            <label htmlFor="reset-password" className={authLabel}>New password</label>
                            <input
                                id="reset-password"
                                type="password"
                                name="password"
                                value={password}
                                onChange={(e) => {
                                    setPassword(e.target.value);
                                    if (error === PASSWORD_TOO_LONG) setError(null);
                                }}
                                required
                                minLength={PASSWORD_MIN}
                                autoComplete="new-password"
                                autoFocus
                                aria-describedby="reset-password-help"
                                aria-invalid={passwordTooLong(password) || undefined}
                                className={authInput}
                            />
                            <span id="reset-password-help" className={passwordTooLong(password) ? `${authHelp} text-status-failed-fg` : authHelp}>
                                {passwordTooLong(password) ? PASSWORD_TOO_LONG : PASSWORD_MIN_HELP}
                            </span>
                        </div>
                        <label className="flex cursor-pointer items-start gap-2.5">
                            <input
                                type="checkbox"
                                checked={revokeAccess}
                                onChange={(e) => setRevokeAccess(e.target.checked)}
                                aria-describedby="reset-revoke-help"
                                className="mt-px h-[18px] w-[18px] flex-none accent-[var(--link)]"
                            />
                            <span className="flex flex-col gap-0.5">
                                <span className="font-medium">Also revoke my API keys, SSH keys and agents</span>
                                <span id="reset-revoke-help" className={authHelp}>
                                    If someone else may have used this account. Scripts, pushes and agents that use them stop working.
                                </span>
                            </span>
                        </label>
                        <button type="submit" disabled={busy} className={`${authButton} mt-1 w-full`}>
                            {busy ? "Setting…" : "Set password and sign in"}
                        </button>
                    </form>
                </AuthCard>
                <p className="text-[13px] leading-[19px] text-fg/70">
                    Didn’t ask for this? Ignore the link; it expires on its own and your password stays as it is.
                </p>
            </AuthColumn>
        </AuthShell>
    );
}

/** The expired, used and unknown views, by the API's code. */
function DeadLink({ code, message }: { code: string; message: string }) {
    const view = {
        reset_expired: {
            Icon: Clock,
            tint: "text-status-pending",
            title: "This link has expired",
            text: "Reset links work for an hour. Ask an instance operator for a new one.",
        },
        reset_used: {
            Icon: Ban,
            tint: "text-muted",
            title: "This link has already been used",
            text: "Each link works once. If you set a password with it, sign in. If not, ask an instance operator for a new one.",
        },
        reset_not_found: {
            Icon: Link2,
            tint: "text-muted",
            title: "This link doesn’t exist",
            text: null,
        },
    }[code] ?? {
        Icon: Link2,
        tint: "text-muted",
        title: "Couldn’t check this link",
        text: message ? `Toft answered: ${message}. Try the link again in a moment.` : "Try the link again in a moment.",
    };
    const { Icon } = view;
    return (
        <>
            <h1 className={`${authH1} flex items-center gap-2.5`}>
                <Icon aria-hidden="true" className={`h-[22px] w-[22px] flex-none ${view.tint}`} strokeWidth={1.9} />
                {view.title}
            </h1>
            <p role="alert" className="text-fg/70">
                {view.text ?? (
                    <>
                        Check that you copied the whole link, including the long code after{" "}
                        <span className="font-mono text-[13px] text-fg/85">/reset/</span>.
                    </>
                )}
            </p>
            <Link href="/" className={`${authLink} text-[13px]`}>Back to sign in</Link>
        </>
    );
}
