"use client";

import { useEffect, useState } from "react";
import { useParams } from "next/navigation";
import Link from "next/link";
import { Ban, Clock, Link2 } from "lucide-react";
import { orgService, type InviteValidation } from "@/lib/api/org.service";
import { useAuth } from "@/context/AuthContext";
import { ApiError } from "@/lib/api/client";
import { useDocumentTitle } from "@/lib/use-document-title";
import { formatDateTime, initials } from "@/lib/utils";
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
    useInstanceHost,
    type NoticeKind,
} from "@/components/shell/auth-shell";
import { inviteCode, signInError } from "@/components/shell/auth";
import { PASSWORD_MIN, PASSWORD_MIN_HELP, PASSWORD_TOO_LONG, passwordTooLong } from "@/lib/password";
import { fullPageLoad } from "@/lib/full-page-load";

// Accept an invite (board: private/design/canvas/InviteAccept.dc.html).

/** What each role may do, in the invite's sentence. */
const ROLE_CAN: Record<string, (org: string) => string> = {
    owner: (org) => `you get full control of ${org}, including its members and deleting it`,
    developer: (org) => `you can create and deploy services in ${org}`,
    viewer: (org) => `you can see ${org}’s projects, services and logs, but not change them`,
};

export default function InvitePage() {
    const params = useParams();
    const { user, login, register, logout, switchOrg } = useAuth();
    const host = useInstanceHost();
    // useParams() can return this dynamic segment still percent-encoded (observed
    // for tokens containing "=" padding) depending on which render produced it —
    // decodeURIComponent is a safe no-op on an already-decoded token, since none
    // of base64url's alphabet (A-Z a-z 0-9 - _ =) looks like a "%XX" escape.
    const token = decodeURIComponent(params.token as string);

    const [invite, setInvite] = useState<InviteValidation | null>(null);
    // Why the link can't be used: an invite_* code, or a message for anything else.
    const [dead, setDead] = useState<{ code: string; message: string } | null>(null);
    const [joinError, setJoinError] = useState<string | null>(null);
    const [alreadyMember, setAlreadyMember] = useState(false);
    const [loading, setLoading] = useState(true);
    const [joining, setJoining] = useState(false);
    const [joined, setJoined] = useState(false);

    // Auth form state
    const [tab, setTab] = useState<"register" | "login">("register");
    const [name, setName] = useState("");
    const [email, setEmail] = useState("");
    const [password, setPassword] = useState("");
    const [authError, setAuthError] = useState<{ kind: NoticeKind; text: string } | null>(null);
    // Set when registration failed because the email already has an account.
    const [emailTaken, setEmailTaken] = useState(false);
    const [authLoading, setAuthLoading] = useState(false);

    useDocumentTitle(invite ? `Join ${invite.org_name} · Toft` : "Invite · Toft");

    const fail = (err: unknown) => setDead({ code: inviteCode(err), message: err instanceof Error ? err.message : "" });

    useEffect(() => {
        orgService.validateInvite(token)
            .then(setInvite)
            .catch((err: unknown) => setDead({ code: inviteCode(err), message: err instanceof Error ? err.message : "" }))
            .finally(() => setLoading(false));
    }, [token]);

    const acceptInvite = async () => {
        setJoining(true);
        setJoinError(null);
        try {
            const org = await orgService.acceptInvite(token);
            if (org) {
                // Sets active_org_id server-side and refreshes cached user/org list —
                // without this, an existing member of another org would join this one
                // server-side but stay pointed at their old org's dashboard. It
                // then opens the dashboard with a full page load.
                await switchOrg(org.id);
                return;
            }
            setJoined(true);
            // A full load, so AuthProvider re-initialises with the new workspace.
            setTimeout(() => fullPageLoad("/dashboard"), 1500);
        } catch (err: unknown) {
            const msg = err instanceof Error ? err.message : "";
            if (msg.includes("Session expired")) {
                // Token expired — clear stale auth and reload to show login form.
                localStorage.removeItem("paas_user");
                window.location.reload();
            } else if (err instanceof ApiError && err.status === 409) {
                setAlreadyMember(true);
            } else if (inviteCode(err)) {
                fail(err);
            } else {
                setJoinError(msg || "Failed to join the workspace. Please try again.");
            }
        } finally {
            setJoining(false);
        }
    };

    const pickTab = (t: "register" | "login") => {
        setTab(t);
        setAuthError(null);
        setEmailTaken(false);
    };

    const handleAuth = async (e: React.FormEvent) => {
        e.preventDefault();
        if (tab === "register" && passwordTooLong(password)) {
            setAuthError({ kind: "error", text: PASSWORD_TOO_LONG });
            return;
        }
        setAuthLoading(true);
        setAuthError(null);
        setEmailTaken(false);
        try {
            if (tab === "register") {
                // The account, its membership and the consumed invite are created
                // together or not at all: on any error (expired or used invite,
                // email already registered) nothing exists. On success register()
                // signs in with the session the server returned, so no second
                // /auth call can fail after the account exists.
                try {
                    await register({ name, email, password, invite_token: token });
                } catch (err: unknown) {
                    if (err instanceof ApiError && err.status === 409) {
                        setEmailTaken(true);
                        return;
                    }
                    if (inviteCode(err)) {
                        fail(err);
                        return;
                    }
                    if (err instanceof ApiError && err.status === 429) {
                        setAuthError(signInError(err));
                        return;
                    }
                    throw err;
                }
                setJoined(true);
                // A new account in a new workspace: a full load (fullPageLoad).
                setTimeout(() => fullPageLoad("/dashboard"), 1500);
                return;
            }

            // Sign-in path: the account already exists. Reuse the same acceptInvite
            // handler the "already logged in" Join button uses below, so the
            // localStorage org patch and hard reload happen consistently regardless
            // of which path got the user logged in — without this, an existing
            // member of another org would join this one server-side but stay
            // pointed at their old org's dashboard.
            try {
                await login(email, password);
            } catch (err: unknown) {
                setAuthError(signInError(err));
                return;
            }
            await acceptInvite();
        } catch (err: unknown) {
            setAuthError({ kind: "error", text: err instanceof Error ? err.message : "Authentication failed" });
        } finally {
            setAuthLoading(false);
        }
    };

    if (loading) {
        return (
            <AuthShell section="Invite" className="pt-14">
                <AuthColumn>
                    <span className="sr-only" role="status">Checking invite…</span>
                    <div className="h-[34px] w-2/3 rounded bg-surface motion-safe:animate-pulse" />
                    <div className="h-80 rounded-[10px] bg-surface motion-safe:animate-pulse" />
                </AuthColumn>
            </AuthShell>
        );
    }

    if (dead || !invite) {
        return (
            <AuthShell section="Invite" className="pt-14">
                <AuthColumn className="gap-3">
                    <DeadInvite code={dead?.code ?? ""} message={dead?.message ?? ""} />
                </AuthColumn>
            </AuthShell>
        );
    }

    const org = invite.org_name;
    const member = alreadyMember || !!user?.orgs?.some((o) => o.org_id === invite.org_id);

    if (joined) {
        return (
            <AuthShell section="Invite" className="pt-14">
                <AuthColumn className="gap-3">
                    <h1 className={authH1}>Welcome to {org}</h1>
                    <p role="status" className="text-fg/70">Opening {org}…</p>
                </AuthColumn>
            </AuthShell>
        );
    }

    const can = (ROLE_CAN[invite.role] ?? ROLE_CAN.viewer)(org);

    return (
        <AuthShell section="Invite" className="pt-14">
            <AuthColumn>
                <div className="flex flex-col gap-2">
                    <div className="flex items-center gap-3">
                        <span aria-hidden="true" className="flex h-8 w-8 flex-none items-center justify-center rounded-lg bg-accent text-[15px] font-bold text-on-accent">
                            {org.trim().charAt(0).toUpperCase()}
                        </span>
                        <h1 className={authH1}>Join {org}</h1>
                    </div>
                    <p className="text-fg/70">
                        {invite.invited_by ? `${invite.invited_by} invited you` : "You’re invited"} as{" "}
                        {invite.role === "owner" ? "an" : "a"} <span className="font-medium text-fg">{invite.role}</span>: {can}.
                        The link works once, until{" "}
                        <span className="whitespace-nowrap font-mono text-[13px] text-fg">{formatDateTime(invite.expires_at)}</span>.
                    </p>
                </div>

                {member ? (
                    <Notice kind="done">
                        You’re already a member of {org}.{" "}
                        {user?.orgId === invite.org_id ? (
                            <Link href="/dashboard" className={authLink}>Open {org}</Link>
                        ) : (
                            <button type="button" onClick={() => void switchOrg(invite.org_id)} className={authLink}>Open {org}</button>
                        )}
                    </Notice>
                ) : user ? (
                    <AuthCard>
                        {joinError && <Notice kind="error">{joinError}</Notice>}
                        <span className="flex items-center gap-2.5">
                            <span aria-hidden="true" className="flex h-7 w-7 flex-none items-center justify-center rounded-full bg-raised text-xs font-semibold">
                                {initials(user.name ?? user.email)}
                            </span>
                            <span className="flex min-w-0 flex-col">
                                <span className="text-xs text-muted">Signed in as</span>
                                <span className="truncate font-mono text-xs">{user.email}</span>
                            </span>
                        </span>
                        <button type="button" onClick={acceptInvite} disabled={joining} className={authButton}>
                            {joining ? "Joining…" : `Join ${org}`}
                        </button>
                        <span className="text-[13px] text-fg/70">
                            Not {user.name ?? user.email}?{" "}
                            <button type="button" onClick={() => logout(`/invite/${encodeURIComponent(token)}`)} className={authLink}>
                                Sign out
                            </button>{" "}
                            and use this link again.
                        </span>
                    </AuthCard>
                ) : (
                    <>
                        <AuthCard>
                            <div role="group" aria-label="Account" className="grid grid-cols-2 gap-0.5 rounded-lg border border-subtle bg-ground p-0.5">
                                {(["register", "login"] as const).map((t) => (
                                    <button
                                        key={t}
                                        type="button"
                                        aria-pressed={tab === t}
                                        onClick={() => pickTab(t)}
                                        className={`h-8 rounded-md px-2.5 text-[13px] font-medium focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-link ${
                                            tab === t ? "bg-raised text-fg" : "text-fg/70 hover:text-fg"
                                        }`}
                                    >
                                        {t === "register" ? "New account" : "I have an account"}
                                    </button>
                                ))}
                            </div>

                            <form onSubmit={handleAuth} className="flex flex-col gap-4">
                                {emailTaken && (
                                    <Notice kind="error">
                                        <span className="flex flex-col gap-1.5">
                                            <span>An account with <span className="font-mono text-xs">{email}</span> already exists.</span>
                                            <button
                                                type="button"
                                                onClick={() => pickTab("login")}
                                                className="self-start font-medium text-fg underline underline-offset-2"
                                            >
                                                Sign in with this email instead
                                            </button>
                                        </span>
                                    </Notice>
                                )}
                                {authError && <Notice kind={authError.kind} alert>{authError.text}</Notice>}
                                {tab === "register" && (
                                    <div className="flex flex-col gap-1.5">
                                        <label htmlFor="invite-name" className={authLabel}>Name</label>
                                        <input id="invite-name" name="name" value={name} onChange={(e) => setName(e.target.value)} required minLength={2} autoComplete="name" className={authInput} />
                                    </div>
                                )}
                                <div className="flex flex-col gap-1.5">
                                    <label htmlFor="invite-email" className={authLabel}>Email</label>
                                    <input
                                        id="invite-email"
                                        type="email"
                                        name="email"
                                        value={email}
                                        onChange={(e) => setEmail(e.target.value)}
                                        required
                                        autoComplete={tab === "login" ? "username" : "email"}
                                        aria-invalid={emailTaken || undefined}
                                        className={authInput}
                                    />
                                </div>
                                <div className="flex flex-col gap-1.5">
                                    <label htmlFor="invite-password" className={authLabel}>Password</label>
                                    <input
                                        id="invite-password"
                                        type="password"
                                        name="password"
                                        value={password}
                                        onChange={(e) => {
                                            setPassword(e.target.value);
                                            if (authError?.text === PASSWORD_TOO_LONG) setAuthError(null);
                                        }}
                                        required
                                        minLength={tab === "register" ? PASSWORD_MIN : undefined}
                                        autoComplete={tab === "login" ? "current-password" : "new-password"}
                                        aria-describedby={tab === "register" ? "invite-password-help" : undefined}
                                        aria-invalid={(tab === "register" && passwordTooLong(password)) || undefined}
                                        className={authInput}
                                    />
                                    {tab === "register" && (
                                        <span id="invite-password-help" className={passwordTooLong(password) ? `${authHelp} text-status-failed-fg` : authHelp}>
                                            {passwordTooLong(password) ? PASSWORD_TOO_LONG : PASSWORD_MIN_HELP}
                                        </span>
                                    )}
                                </div>
                                <button type="submit" disabled={authLoading} className={`${authButton} mt-1 w-full`}>
                                    {authLoading
                                        ? "Joining…"
                                        : tab === "login"
                                            ? "Sign in and join"
                                            : "Create account and join"}
                                </button>
                            </form>
                        </AuthCard>

                        <p className="text-[13px] leading-[19px] text-fg/70">
                            Choose “I have an account” if you already sign in to{" "}
                            <span className="font-mono text-xs text-fg/85">{host}</span> for another workspace. One account can
                            belong to several workspaces.
                        </p>
                    </>
                )}
            </AuthColumn>
        </AuthShell>
    );
}

/** The board's expired, used and unknown views, by the API's code. */
function DeadInvite({ code, message }: { code: string; message: string }) {
    const view = {
        invite_expired: {
            Icon: Clock,
            tint: "text-status-pending",
            title: "This invite has expired",
            text: "Invite links work for 7 days. Ask the person who sent it for a new one.",
            link: "Back to sign in",
        },
        invite_used: {
            Icon: Ban,
            tint: "text-muted",
            title: "This invite has already been used",
            text: "Each link works once. If you joined with it, sign in. If not, ask the person who sent it for a new one.",
            link: "Sign in",
        },
        invite_not_found: {
            Icon: Link2,
            tint: "text-muted",
            title: "This invite link doesn’t exist",
            text: null,
            link: "Back to sign in",
        },
    }[code] ?? {
        Icon: Link2,
        tint: "text-muted",
        title: "Couldn’t check this invite",
        text: message ? `Toft answered: ${message}. Try the link again in a moment.` : "Try the link again in a moment.",
        link: "Back to sign in",
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
                        <span className="font-mono text-[13px] text-fg/85">/invite/</span>.
                    </>
                )}
            </p>
            <Link href="/" className={`${authLink} text-[13px]`}>{view.link}</Link>
        </>
    );
}
