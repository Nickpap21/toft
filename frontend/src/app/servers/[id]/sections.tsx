"use client";

import { useEffect, useState } from "react";
import { cn } from "@/lib/utils";
import { card, field, h2 } from "@/app/projects/_lib/ui";

// The Maintenance and Settings tabs' frame (boards: private/design/canvas/
// ReworkServerMaintenance.dc.html, ReworkServerSettings.dc.html): their cards
// in one scroll, with an "On this page" index from 1200 px and a Jump to
// select below it, as the settings pages have.

export interface PageSection {
  /** The card's anchor, which Overview's links land on. */
  id: string;
  label: string;
  /** A count beside the label, hidden at 0: what needs action, coloured by
   *  the worst, or a plain count without a tone. */
  count?: { n: number; tone?: "bad" | "warn" };
  /** The danger zone: last, set apart and red. */
  danger?: boolean;
}

export function SectionsLayout({ sections, label = "Jump to a section", children }: { sections: PageSection[]; label?: string; children: React.ReactNode }) {
  const [current, setCurrent] = useState(sections[0]?.id ?? "");
  const jump = (id: string) => {
    const el = document.getElementById(id);
    if (!el) return;
    setCurrent(id);
    el.scrollIntoView({ block: "start" });
    // The section takes focus: Tab goes on from there.
    el.focus({ preventScroll: true });
    history.replaceState(null, "", `#${id}`);
  };
  // The card in view: the first in the band below the Jump to bar, down to
  // 45% of the window.
  const ids = sections.map((s) => s.id).join();
  useEffect(() => {
    const list = ids.split(",");
    const seen = new Map<string, boolean>();
    const io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) seen.set(e.target.id, e.isIntersecting);
        // At the page's end the last cards never reach the band: the one
        // jumped to stays marked.
        const main = document.getElementById("main");
        if (main && main.scrollTop > 0 && main.scrollTop + main.clientHeight >= main.scrollHeight - 2) return;
        const first = list.find((id) => seen.get(id));
        if (first) setCurrent(first);
      },
      { rootMargin: "-128px 0px -55% 0px" },
    );
    for (const id of list) {
      const el = document.getElementById(id);
      if (el) io.observe(el);
    }
    return () => io.disconnect();
  }, [ids]);

  return (
    <div className="flex flex-col gap-5 max-sm:gap-4">
      {/* Sticks at <main>'s top edge, above its padding (p-4, md:p-8). */}
      <label data-jump-bar className="sticky -top-4 z-10 -mt-2 flex items-center gap-2.5 border-b border-subtle bg-ground py-2 text-[13px] text-muted md:-top-8 min-[1200px]:hidden">
        Jump to
        <select aria-label={label} className={cn(field, "max-w-[320px] flex-1 text-[13px] pointer-coarse:h-11")} value={current} onChange={(e) => jump(e.target.value)}>
          {sections.map((s) => (
            <option key={s.id} value={s.id}>
              {s.count?.n ? `${s.label} (${s.count.n})` : s.label}
            </option>
          ))}
        </select>
      </label>

      <div className="grid items-start gap-6 min-[1200px]:grid-cols-[minmax(0,1fr)_240px]">
        <div className="flex min-w-0 flex-col gap-5 max-sm:gap-4">{children}</div>
        <aside className="sticky top-5 hidden min-[1200px]:block">
          <nav aria-labelledby="mnt-toc-h" className={card}>
            <div className="border-b border-subtle px-4 py-3">
              <h2 id="mnt-toc-h" className={h2}>On this page</h2>
            </div>
            <ul className="flex flex-col p-1.5">
              {sections.map((s) => (
                <li key={s.id} className={cn(s.danger && "mt-1.5 border-t border-subtle pt-1.5")}>
                  <a
                    href={`#${s.id}`}
                    aria-current={current === s.id ? "true" : undefined}
                    onClick={(e) => {
                      e.preventDefault();
                      jump(s.id);
                    }}
                    className={cn(
                      "flex min-h-[34px] items-center gap-2 rounded-lg px-2.5 text-[13px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus pointer-coarse:min-h-11",
                      current === s.id ? "bg-selected" : "hover:bg-raised",
                      s.danger ? "text-status-failed-fg" : current === s.id ? "text-fg" : "text-fg/85",
                    )}
                  >
                    {s.label}
                    {!!s.count?.n && !s.count.tone && <span className="ml-auto font-mono text-xs text-muted">{s.count.n}</span>}
                    {!!s.count?.n && s.count.tone && (
                      <span
                        className={cn(
                          "ml-auto rounded-full px-1.5 font-mono text-xs font-semibold leading-[18px]",
                          s.count.tone === "bad" ? "bg-status-failed-soft text-status-failed-fg" : "bg-status-pending-soft text-status-pending-fg",
                        )}
                      >
                        {s.count.n}
                        <span className="sr-only"> {s.count.n === 1 ? "item needs" : "items need"} action</span>
                      </span>
                    )}
                  </a>
                </li>
              ))}
            </ul>
          </nav>
        </aside>
      </div>
    </div>
  );
}
