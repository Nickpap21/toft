"use client";

import type { ThemeId } from "@/lib/theme/themes";

const DOTS = ["bg-status-running", "bg-status-building", "bg-status-pending", "bg-status-failed"];

/** A miniature of the interface rendered in another theme (board:
 *  Appearance). Decorative: the option's label and description carry the
 *  meaning. */
export function ThemePreview({ theme, className = "" }: { theme: ThemeId; className?: string }) {
  return (
    <span data-theme={theme} aria-hidden="true" className={`flex h-24 min-w-0 gap-2 bg-ground p-2.5 ${className}`}>
      <span className="flex w-11 flex-none flex-col gap-[5px] pt-0.5">
        <span className="h-2 rounded-xs bg-accent-soft" />
        <span className="h-2 rounded-xs bg-raised" />
        <span className="h-2 rounded-xs bg-raised" />
        <span className="h-2 rounded-xs bg-raised" />
      </span>
      <span className="flex min-w-0 flex-1 flex-col gap-[7px] rounded-md border border-subtle bg-surface px-2.5 py-[9px]">
        <span className="h-1.5 w-[70%] rounded-xs bg-fg" />
        <span className="h-1.5 w-[46%] rounded-xs bg-muted" />
        <span className="flex gap-[5px]">
          {DOTS.map((d) => (
            <span key={d} className={`h-[7px] w-[7px] rounded-full ${d}`} />
          ))}
        </span>
        <span className="mt-auto h-3.5 w-11 rounded bg-accent" />
      </span>
    </span>
  );
}

interface ThemeOptionProps {
  value: string;
  label: string;
  description: string;
  /** "Dark", "Light" or "Default", beside the label. */
  tag?: string;
  previews: ThemeId[];
  checked: boolean;
  /** Under the description while it is checked. */
  note?: string;
  onSelect: (value: string) => void;
}

/** One native radio, styled as a card. Arrow keys move between options the
 *  way they do in any radio group. */
export function ThemeOption({ value, label, description, tag, previews, checked, note, onSelect }: ThemeOptionProps) {
  const id = `theme-${value.replace(":", "-")}`;
  return (
    <label
      htmlFor={id}
      className="flex cursor-pointer flex-col overflow-hidden rounded-[10px] border border-subtle bg-surface hover:border-strong has-[:checked]:border-link has-[:checked]:ring-1 has-[:checked]:ring-link has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-offset-2 has-[:focus-visible]:outline-focus"
    >
      <span className="flex border-b border-subtle">
        {previews.map((t) => (
          <ThemePreview key={t} theme={t} className="flex-1" />
        ))}
      </span>
      <span className="flex items-start gap-2.5 px-3.5 py-3">
        <input
          id={id}
          type="radio"
          name="theme"
          value={value}
          checked={checked}
          onChange={() => onSelect(value)}
          // Named by the label and tag only; the description is a description.
          aria-labelledby={tag ? `${id}-label ${id}-tag` : `${id}-label`}
          aria-describedby={`${id}-desc`}
          // The card shows focus; the radio itself needn't draw a second ring.
          className="mt-0.5 h-4 w-4 flex-none accent-[var(--link)] focus-visible:outline-none"
        />
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="flex flex-wrap items-center gap-1.5">
            <span id={`${id}-label`} className="text-sm font-medium text-fg">
              {label}
            </span>
            {tag && (
              <span id={`${id}-tag`} className="rounded-full border border-subtle px-[7px] text-[11px] leading-[18px] text-fg/70">
                {tag}
              </span>
            )}
          </span>
          <span id={`${id}-desc`} className="text-[13px] leading-[19px] text-fg/70">
            {description}
          </span>
          {checked && note && <span className="text-xs leading-[18px] text-muted">{note}</span>}
        </span>
      </span>
    </label>
  );
}
