"use client";

import { AlertTriangle, ShieldAlert, ShieldCheck } from "lucide-react";
import type { PolicyViolation } from "@/lib/api/deploypolicy.service";

interface PolicyViolationListProps {
    violations: PolicyViolation[];
    /**
     * "refused": the deploy policy blocked the action (red).
     * "warning": reported only, in warn mode (amber).
     * "allowed": the service's exception covers it (neutral).
     */
    tone?: "refused" | "warning" | "allowed";
    title?: string;
    className?: string;
}

const TONES = {
    refused: {
        Icon: ShieldAlert,
        box: "border-status-failed/35 bg-status-failed-soft",
        heading: "text-status-failed-fg",
        title: "Refused by the deploy policy",
    },
    warning: {
        Icon: AlertTriangle,
        box: "border-status-pending/35 bg-status-pending-soft",
        heading: "text-status-pending-fg",
        title: "Deploy policy warnings",
    },
    allowed: {
        Icon: ShieldCheck,
        box: "border-subtle bg-raised",
        heading: "text-fg/85",
        title: "Allowed by this service's exception",
    },
} as const;

/**
 * Lists deploy policy violations. Each says what the rule covers, what the
 * configuration does, why the rule exists and what to do instead (the
 * rule's text comes with the violation from the API), and the setting it is
 * in. Used where services are created, where their config is saved, where a
 * deploy is refused, and in the policy report.
 */
export function PolicyViolationList({ violations, tone = "refused", title, className = "" }: PolicyViolationListProps) {
    if (violations.length === 0) return null;
    const t = TONES[tone];
    const allowed = tone === "allowed";
    return (
        <div role={tone === "refused" ? "alert" : "status"} className={`rounded-lg border p-3 text-sm ${t.box} ${className}`}>
            <p className={`flex items-center gap-2 font-medium ${t.heading}`}>
                <t.Icon className="h-4 w-4 shrink-0" aria-hidden />
                {title ?? t.title}
            </p>
            <ul className="mt-2 space-y-3">
                {violations.map((v, i) => (
                    <li key={`${v.field}-${v.rule}-${i}`} className="space-y-1 text-fg/85">
                        <p className="flex flex-wrap items-center gap-x-2 gap-y-1">
                            <span className="font-medium text-fg">{v.title || v.rule}</span>
                            <span className="rounded bg-ground/60 px-1.5 py-0.5 font-mono text-[11px] text-muted">
                                {v.rule}
                            </span>
                        </p>
                        <p>{v.message.charAt(0).toUpperCase() + v.message.slice(1)}.</p>
                        {v.why && (
                            <p>
                                <span className="font-medium">Why it matters: </span>
                                {v.why}
                            </p>
                        )}
                        {allowed ? (
                            <p>An instance operator granted this service an exception for this rule, so it deploys as it is.</p>
                        ) : (
                            v.fix && (
                                <p>
                                    <span className="font-medium">What to do: </span>
                                    {v.fix}
                                    {v.grantable && " If you trust this service, an instance operator can grant it an exception for this rule instead."}
                                </p>
                            )
                        )}
                        <p className="text-xs text-muted">
                            Setting:{" "}
                            <span className="font-mono break-all">
                                {v.service ? `${v.field} (compose service ${v.service})` : v.field}
                            </span>
                        </p>
                    </li>
                ))}
            </ul>
        </div>
    );
}
