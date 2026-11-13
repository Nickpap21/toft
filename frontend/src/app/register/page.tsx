"use client";

import { Suspense, useEffect } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { registrationStatusQuery } from "@/lib/api/instance.service";
import { useDocumentTitle } from "@/lib/use-document-title";
import { AuthColumn, AuthShell, authH1, authLink } from "@/components/shell/auth-shell";

// Registration is invite-only (D-21). This page explains how to get an
// invite; an invite link goes to its own page, and on an empty instance the
// first account is the setup wizard's first step, /setup (7g).

export default function RegisterPage() {
    useDocumentTitle("Set up · Toft");
    // useSearchParams needs a Suspense boundary to prerender this page.
    return (
        <Suspense fallback={<Loading />}>
            <RegisterContent />
        </Suspense>
    );
}

function RegisterContent() {
    const searchParams = useSearchParams();
    const inviteToken = searchParams.get("invite");
    const router = useRouter();
    const status = useQuery({ ...registrationStatusQuery, enabled: !inviteToken });
    const firstRun = !!status.data?.first_run;

    useEffect(() => {
        // Invites have their own page, which shows the workspace and role;
        // the first account is the setup wizard's first step (7g).
        if (inviteToken) router.replace(`/invite/${encodeURIComponent(inviteToken)}`);
        else if (firstRun) router.replace("/setup");
    }, [inviteToken, firstRun, router]);

    if (inviteToken || status.isPending || firstRun) return <Loading />;
    // If the status can't be read, say how accounts are made rather than
    // showing a form that will most likely be refused.
    return <InviteOnly />;
}

function InviteOnly() {
    return (
        <AuthShell>
            <AuthColumn className="gap-4">
                <h1 className={authH1}>Registration is invite-only</h1>
                <p className="text-fg/70">Accounts are created from invite links. Ask a workspace owner on this instance to send you one.</p>
                <p className="text-[13px] text-fg/70">
                    Already have an account? <Link href="/" className={authLink}>Sign in</Link>
                </p>
            </AuthColumn>
        </AuthShell>
    );
}

function Loading() {
    return (
        <AuthShell section="Setup">
            <AuthColumn>
                <span className="sr-only" role="status">Loading…</span>
                <div className="h-8 w-2/3 rounded bg-surface motion-safe:animate-pulse" />
                <div className="h-64 rounded-[10px] bg-surface motion-safe:animate-pulse" />
            </AuthColumn>
        </AuthShell>
    );
}
