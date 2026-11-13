"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { orgService } from "@/lib/api/org.service";
import { cn } from "@/lib/utils";
import { handles, typing } from "./mentions";

type Props = Omit<React.TextareaHTMLAttributes<HTMLTextAreaElement>, "value" | "onChange"> & {
  id: string;
  value: string;
  onChange: (v: string) => void;
};

/** A textarea where @ completes the workspace's handles (D-88). */
export function MentionTextarea({ id, value, onChange, onKeyDown, onBlur, ...rest }: Props) {
  const [mention, setMention] = useState<{ start: number; prefix: string; index: number } | null>(null);
  const members = useQuery({ queryKey: ["org-members"], queryFn: () => orgService.listMembers(), staleTime: 60_000 });
  const choices = mention ? handles(members.data ?? []).filter((h) => h.handle.startsWith(mention.prefix)).slice(0, 6) : [];
  const insert = (h: string, el: HTMLTextAreaElement) => {
    if (!mention) return;
    const next = value.slice(0, mention.start) + "@" + h + " " + value.slice(el.selectionStart);
    onChange(next);
    setMention(null);
    const at = mention.start + h.length + 2;
    requestAnimationFrame(() => el.setSelectionRange(at, at));
  };
  return (
    <>
      <textarea
        id={id}
        value={value}
        {...rest}
        onChange={(e) => {
          onChange(e.target.value);
          const t = typing(e.target.value, e.target.selectionStart);
          setMention(t ? { ...t, index: 0 } : null);
        }}
        onBlur={(e) => {
          setMention(null);
          onBlur?.(e);
        }}
        onKeyDown={(e) => {
          if (mention && choices.length > 0) {
            if (e.key === "ArrowDown" || e.key === "ArrowUp") {
              e.preventDefault();
              const d = e.key === "ArrowDown" ? 1 : -1;
              setMention({ ...mention, index: (mention.index + d + choices.length) % choices.length });
              return;
            }
            if (e.key === "Enter" || e.key === "Tab") {
              e.preventDefault();
              insert(choices[mention.index].handle, e.currentTarget);
              return;
            }
            if (e.key === "Escape") {
              e.preventDefault();
              setMention(null);
              return;
            }
          }
          onKeyDown?.(e);
        }}
      />
      {mention && choices.length > 0 && (
        <ul role="listbox" aria-label="Mention someone" className="w-64 max-w-full overflow-hidden rounded-lg border border-strong bg-surface py-1 shadow-menu">
          {choices.map((h, i) => (
            <li key={h.handle}>
              <button
                type="button"
                role="option"
                aria-selected={i === mention.index}
                onMouseDown={(e) => {
                  e.preventDefault();
                  const el = document.getElementById(id) as HTMLTextAreaElement | null;
                  if (el) insert(h.handle, el);
                }}
                className={cn("flex w-full items-center gap-2 px-2.5 py-1.5 text-left text-[13px]", i === mention.index ? "bg-accent-soft" : "hover:bg-raised")}
              >
                <span className="font-mono text-xs text-fg">@{h.handle}</span>
                <span className="min-w-0 flex-1 truncate text-muted">{h.name}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </>
  );
}
