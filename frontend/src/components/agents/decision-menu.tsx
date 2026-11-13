"use client";

import { ChevronDown, Lock } from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";
import { DECISION_LABEL, loosens, type Decision } from "@/app/settings/agents/view";

/** A policy cell's menu (boards Agents, ProjectAgents): the chip shows what
 *  applies; the menu offers Allow, Ask a person and Never, and for a
 *  project "As <workspace>" (the value ""), marking with a lock what
 *  loosens what applies, since that asks for the password. */
export function DecisionMenu({ applies, selected, asWorkspace, ownHint, ariaLabel, disabled, className, onPick, children }: {
  applies: Decision;
  selected: Decision | "";
  asWorkspace?: { name: string; decision: Decision };
  ownHint?: string;
  ariaLabel: string;
  disabled?: boolean;
  className: string;
  onPick: (to: Decision | "") => void;
  /** What the trigger shows instead of the label and chevron; it names
   *  the trigger, and ariaLabel goes unused. */
  children?: React.ReactNode;
}) {
  const options: (Decision | "")[] = asWorkspace ? ["allow", "ask", "never", ""] : ["allow", "ask", "never"];
  return (
    <DropdownMenu>
      <DropdownMenuTrigger aria-label={children ? undefined : `${ariaLabel}. Change`} disabled={disabled} className={cn(className, "gap-[5px] disabled:opacity-60")}>
        {children ?? (
          <>
            {DECISION_LABEL[applies]}
            <ChevronDown className="h-2.5 w-2.5" aria-hidden="true" />
          </>
        )}
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-[300px]">
        <DropdownMenuRadioGroup value={selected} onValueChange={(to) => to !== selected && onPick(to as Decision | "")}>
          {options.map((d) => {
            const label = d ? DECISION_LABEL[d] : `As ${asWorkspace?.name} (${DECISION_LABEL[asWorkspace?.decision ?? "ask"]})`;
            const needs = d !== selected && loosens(applies, d || asWorkspace?.decision || applies);
            return (
              <DropdownMenuRadioItem key={d || "workspace"} value={d}>
                <span className="flex-1">{label}</span>
                {needs ? (
                  <span className="inline-flex items-center gap-1 text-xs text-muted">
                    <Lock className="h-3 w-3" aria-hidden="true" />
                    password
                  </span>
                ) : d && d === selected && ownHint ? (
                  <span className="text-xs text-muted">{ownHint}</span>
                ) : null}
              </DropdownMenuRadioItem>
            );
          })}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
