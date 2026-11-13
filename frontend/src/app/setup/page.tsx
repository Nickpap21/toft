"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { useAuth, useIsOperator } from "@/context/AuthContext";
import { registrationStatusQuery } from "@/lib/api/instance.service";
import { useDocumentTitle } from "@/lib/use-document-title";
import { SetupFrame, setupCard } from "@/components/setup/setup-frame";
import { setupEntry, useSetupProgress } from "@/components/setup/use-setup";
import { SetupError } from "@/components/setup/setup-page";
import { AccountStep } from "./account";

// The setup URL the installer prints (D-61): the account step on an empty
// instance; afterwards the operator's next step, sign-in for anyone signed
// out, and Home for everyone else (setupEntry).
export default function SetupEntry() {
  const router = useRouter();
  const { user, isLoading } = useAuth();
  const isOperator = useIsOperator();
  const status = useQuery(registrationStatusQuery);
  const { setup, error, retry } = useSetupProgress();
  useDocumentTitle("Set up · Toft");
  const to = setupEntry({
    firstRun: status.isError ? false : status.data?.first_run,
    signedIn: isLoading ? undefined : !!user,
    operator: isOperator,
    setup,
  });

  useEffect(() => {
    if (to && to !== "account") router.replace(to);
  }, [to, router]);

  if (to === "account") return <AccountStep setupTokenRequired={!!status.data?.setup_token_required} />;
  return (
    <SetupFrame step="owner" title="Create your account">
      {error && !to ? (
        <SetupError retry={() => void retry()} />
      ) : (
        <div className={`${setupCard} h-64 motion-safe:animate-pulse`}>
          <span className="sr-only" role="status">Loading…</span>
        </div>
      )}
    </SetupFrame>
  );
}
