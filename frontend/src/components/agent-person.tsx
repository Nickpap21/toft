import type { components } from "@/lib/api/schema";
import { cn } from "@/lib/utils";

type Person = components["schemas"]["Person"];

// Work an agent made names it beside its person, everywhere the person's
// name would be (board AgentWork, D-102).

/** "Claude Code for admin" for an agent's work, else the person's name. */
export function personName(p: Person | null | undefined, fallback: string): string {
  if (!p) return fallback;
  return p.agent ? `${p.agent} for ${p.name}` : p.name;
}

/** The agent's name tinted, then "for <person>"; a person's name alone. */
export function Who({ person, fallback, className }: { person: Person | null | undefined; fallback: string; className?: string }) {
  if (!person?.agent) return <span className={className}>{person?.name ?? fallback}</span>;
  return (
    <span>
      <span className={cn(className, "text-link")}>{person.agent}</span> for {person.name}
    </span>
  );
}

/** The board's glyph for an agent: a small robot. */
export function BotGlyph({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" className={className}>
      <path d="M12 8V4H8M4 8h16v12H4zM2 14h2M20 14h2M9 13v2M15 13v2" />
    </svg>
  );
}

/** "Claude Code for admin" as a chip, with the agent's glyph. */
export function AgentChip({ label, className }: { label: string; className?: string }) {
  return (
    <span className={cn("inline-flex h-[22px] items-center gap-[5px] whitespace-nowrap rounded-full bg-link/12 px-2 text-xs font-medium text-link", className)}>
      <BotGlyph className="h-3 w-3 flex-none" />
      {label}
    </span>
  );
}
