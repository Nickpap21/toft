"use client";

import { Suspense, useRef, useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { useAuth } from "@/context/AuthContext";
import { registrationStatusQuery } from "@/lib/api/instance.service";
import { safeNext } from "@/lib/routes";
import { useDocumentTitle } from "@/lib/use-document-title";
import {
  AuthCard,
  AuthColumn,
  AuthShell,
  Notice,
  authButton,
  authH1,
  authInput,
  authLabel,
  authLink,
  type NoticeKind,
} from "@/components/shell/auth-shell";
import { signInError } from "@/components/shell/auth";

// Sign in (board: private/design/canvas/SignIn.dc.html).

export default function LoginPage() {
  useDocumentTitle("Sign in · Toft");
  // useSearchParams needs a Suspense boundary to prerender this page.
  return (
    <AuthShell>
      <AuthColumn>
        <h1 className={authH1}>Sign in to Toft</h1>
        <Suspense fallback={<AuthCard className="h-[229px]">{null}</AuthCard>}>
          <SignInForm />
        </Suspense>
        <div className="flex flex-col gap-2 text-[13px] leading-[19px] text-fg/70">
          <p>New here? Accounts are created from invite links. Ask a workspace owner to send you one.</p>
          <p>Forgot your password? Ask an instance operator for a reset link.</p>
        </div>
      </AuthColumn>
    </AuthShell>
  );
}

function SignInForm() {
  const next = useSearchParams().get("next");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [loading, setLoading] = useState(false);
  // An error from signing in is an alert; the expired session is a status.
  const [notice, setNotice] = useState<{ kind: NoticeKind; text: string; alert?: boolean } | null>(() => {
    if (typeof window !== "undefined" && sessionStorage.getItem("paas_session_expired")) {
      sessionStorage.removeItem("paas_session_expired");
      return { kind: "wait", text: "Your session expired. Sign in again to go back to where you were." };
    }
    return null;
  });
  const passwordRef = useRef<HTMLInputElement>(null);
  const router = useRouter();
  const { login } = useAuth();
  // True on an instance with no accounts yet, so the first person to arrive
  // can find the set-up form (registration is otherwise invite-only). A
  // failed read is not critical: the login form works either way.
  const { data: registration } = useQuery(registrationStatusQuery);
  const firstRun = registration?.first_run === true;

  const handleLogin = async (e: React.FormEvent) => {
    e.preventDefault();
    setLoading(true);
    setNotice(null);
    try {
      await login(email, password);
      router.push(safeNext(next));
    } catch (err: unknown) {
      setNotice({ ...signInError(err), alert: true });
      setPassword("");
      passwordRef.current?.focus();
    } finally {
      setLoading(false);
    }
  };

  return (
    <AuthCard>
      <form onSubmit={handleLogin} className="flex flex-col gap-4">
        {notice ? (
          <Notice kind={notice.kind} alert={notice.alert}>{notice.text}</Notice>
        ) : (
          next && <Notice kind="next">Sign in to open the page you followed. You’ll go straight there.</Notice>
        )}
        {firstRun && (
          <Notice kind="info">
            This instance has no accounts yet. <Link href="/register" className={authLink}>Set it up</Link>
          </Notice>
        )}
        <div className="flex flex-col gap-1.5">
          <label htmlFor="email" className={authLabel}>Email</label>
          <input
            id="email"
            type="email"
            name="email"
            autoComplete="username"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            required
            className={authInput}
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <label htmlFor="password" className={authLabel}>Password</label>
          <input
            ref={passwordRef}
            id="password"
            type="password"
            name="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
            className={authInput}
          />
        </div>
        <button type="submit" disabled={loading} className={`${authButton} mt-1 w-full`}>
          {loading ? "Signing in…" : "Sign in"}
        </button>
      </form>
    </AuthCard>
  );
}
