"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";
import { useAuth, useIsOperator } from "@/context/AuthContext";
import type { Setup } from "@/lib/api/instance.service";
import { useDocumentTitle } from "@/lib/use-document-title";
import { Notice } from "@/components/shell/auth-shell";
import { SetupFrame, setupBack, setupBody, setupCard, type SetupStepId } from "./setup-frame";
import { useSetupProgress } from "./use-setup";

/**
 * One step of the wizard after the account: operators only (anyone else
 * goes Home), its card once the setup state is loaded. A finished setup's
 * steps stay open: an operator changes the address or the role from them
 * later, from Instance → This panel (D-62).
 */
export function SetupPage({ step, title, children }: {
  step: SetupStepId;
  title: string;
  children: (setup: Setup) => React.ReactNode;
}) {
  const router = useRouter();
  const { user, isLoading } = useAuth();
  const isOperator = useIsOperator();
  const { setup, ready, error, retry } = useSetupProgress();
  useDocumentTitle(`${title} · Setup · Toft`);

  useEffect(() => {
    if (!isLoading && user && !isOperator) router.replace("/dashboard");
  }, [isLoading, user, isOperator, router]);

  return (
    <SetupFrame step={step} title={title}>
      {ready && setup ? (
        children(setup)
      ) : error ? (
        <SetupError retry={() => void retry()} />
      ) : (
        <div className={`${setupCard} h-64 motion-safe:animate-pulse`}>
          <span className="sr-only" role="status">Loading…</span>
        </div>
      )}
    </SetupFrame>
  );
}

/** The setup state couldn't be read: say so, and offer to try again. */
export function SetupError({ retry }: { retry: () => void }) {
  return (
    <div className={`${setupCard} ${setupBody} items-start`}>
      <Notice kind="error">Couldn’t read where setup stands.</Notice>
      <button type="button" onClick={retry} className={setupBack}>
        Try again
      </button>
    </div>
  );
}
