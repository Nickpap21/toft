import { Lock } from "lucide-react";
import type { EnvColor } from "@/lib/api/environment.service";
import { cn } from "@/lib/utils";

type EnvLook = { chip: string; text: string; swatch: string };

/** Chip classes per environment colour: soft fill, readable text. Square
 *  corners, mono name and no dot keep it apart from a status chip (D-55).
 *  No red: it means failing (D-142). */
export const ENV_CHIP: Record<EnvColor, EnvLook> = {
  amber: { chip: "bg-env-amber-soft text-env-amber-fg", text: "text-env-amber-fg", swatch: "bg-env-amber" },
  violet: { chip: "bg-env-violet-soft text-env-violet-fg", text: "text-env-violet-fg", swatch: "bg-env-violet" },
  blue: { chip: "bg-env-blue-soft text-env-blue-fg", text: "text-env-blue-fg", swatch: "bg-env-blue" },
  teal: { chip: "bg-env-teal-soft text-env-teal-fg", text: "text-env-teal-fg", swatch: "bg-env-teal" },
  gray: { chip: "bg-env-gray-soft text-env-gray-fg", text: "text-env-gray-fg", swatch: "bg-env-gray" },
};

/** The classes for a colour; gray for anything unknown. */
export const envChip = (color: string) => ENV_CHIP[color as EnvColor] ?? ENV_CHIP.gray;

/** A protected environment draws neutral, whatever its colour: the lock
 *  says careful, so red keeps meaning failing (D-142). */
const PROTECTED: EnvLook = {
  chip: "bg-env-protected-soft text-env-protected-fg ring-1 ring-inset ring-env-protected-ring",
  text: "text-env-protected-fg",
  swatch: "bg-env-protected",
};

/** An environment's classes: neutral when protected, else its colour's. */
export const envLook = (env: { color: string; protected: boolean }) => (env.protected ? PROTECTED : envChip(env.color));

/** An environment's name as its chip; a lock marks a protected one. */
export function EnvChip({
  env,
  size = "sm",
  className,
  nameClassName,
}: {
  env: { name: string; color: EnvColor; protected: boolean };
  size?: "sm" | "md";
  className?: string;
  /** For the name alone: the icon rail hides it, keeping the lock. */
  nameClassName?: string;
}) {
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center gap-1 rounded px-1.5 font-mono",
        size === "sm" ? "h-5 text-xs" : "h-[22px] px-2 text-xs",
        envLook(env).chip,
        className,
      )}
    >
      {env.protected && <Lock className="h-2.5 w-2.5 shrink-0" aria-hidden="true" />}
      {nameClassName ? <span className={nameClassName}>{env.name}</span> : env.name}
      {env.protected && <span className="sr-only"> (protected)</span>}
    </span>
  );
}
