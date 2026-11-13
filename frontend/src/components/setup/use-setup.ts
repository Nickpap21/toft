"use client";

import { useQuery } from "@tanstack/react-query";
import { useIsOperator } from "@/context/AuthContext";
import { setupQuery, type Setup } from "@/lib/api/instance.service";
import { SETUP_STEPS, type SetupStepId } from "./setup-frame";

/** The steps done: the account always, once there is a setup to read. */
export function finishedSteps(setup: Setup): Set<SetupStepId> {
  const done = new Set<SetupStepId>(["owner"]);
  if (setup.domain_set) done.add("address");
  if (setup.role_set) done.add("role");
  if (setup.kit_acknowledged_at) done.add("kit");
  return done;
}

/** The first step not done yet; the last one, whose Finish ends setup, when
 *  all are. */
export function firstOpenStep(done: Set<SetupStepId>): string {
  return (SETUP_STEPS.find((s) => !done.has(s.id)) ?? SETUP_STEPS[SETUP_STEPS.length - 1]).href;
}

/** The setup state and its finished steps, for an operator. */
export function useSetupProgress() {
  const isOperator = useIsOperator();
  const setup = useQuery({ ...setupQuery, enabled: isOperator });
  const done = setup.data ? finishedSteps(setup.data) : new Set<SetupStepId>();
  return { setup: setup.data, done, ready: setup.isSuccess, error: setup.error, retry: setup.refetch };
}

/**
 * Where /setup goes (7g): the account step on an empty instance; sign-in for
 * anyone signed out; Home for a signed-in non-operator and once setup is
 * finished; otherwise the operator's first open step. null while it can't
 * tell yet.
 */
export function setupEntry(s: {
  firstRun: boolean | undefined;
  signedIn: boolean | undefined;
  operator: boolean;
  setup: Setup | undefined;
}): "account" | string | null {
  if (s.firstRun) return "account";
  if (s.firstRun === undefined || s.signedIn === undefined) return null;
  if (!s.signedIn) return "/?next=/setup";
  if (!s.operator) return "/dashboard";
  if (!s.setup) return null;
  return s.setup.in_progress ? firstOpenStep(finishedSteps(s.setup)) : "/dashboard";
}
