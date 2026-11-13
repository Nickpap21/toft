"use client";

import { useState } from "react";
import { useAuth } from "@/context/AuthContext";
import { useTheme } from "@/components/theme-provider";
import { ThemeOption } from "@/components/theme/theme-option";
import { SettingsFrame } from "@/components/settings/settings-frame";
import {
  DEFAULT_PREF,
  PAIR_LABELS,
  PAIRS,
  THEME_PREFS,
  THEMES,
  prefLabel,
  type PairId,
  type ThemeId,
  type ThemePref,
} from "@/lib/theme/themes";

const PAIR_PREFS = THEME_PREFS.filter((p) => p.startsWith("system:"));
const SINGLE_PREFS = THEME_PREFS.filter(
  (p) => !p.startsWith("system:"),
) as ThemeId[];

const groupHead = "text-xs font-medium leading-[18px] text-fg/70";

/** Settings → Appearance (board: Appearance). A choice applies at once; the
 *  account keeps it (ThemeAccountSync). */
export default function AppearancePage() {
  const { user } = useAuth();
  const { pref, theme, setPref } = useTheme();
  const [announcement, setAnnouncement] = useState("");

  function select(value: string) {
    const next = value as ThemePref;
    setPref(next);
    setAnnouncement(`Theme set to ${prefLabel(next)}.`);
  }

  return (
    <SettingsFrame title="Appearance" crumb={user?.name || user?.email} gap="gap-6">
      <div className="flex flex-col gap-1">
        <h2
          id="theme-h"
          className="text-[15px] font-semibold leading-[22px] text-fg"
        >
          Theme
        </h2>
        <p
          id="theme-d"
          className="max-w-[760px] text-[13px] leading-[19px] text-fg/70"
        >
          Applies as soon as you pick one, and is saved to your account, so
          every browser you sign in on uses it. Status colours keep their
          meaning in every theme: green running, blue building, amber pending or
          warning, red failed.
        </p>
      </div>

      <fieldset
        aria-describedby="theme-d"
        className="-mt-1 flex min-w-0 flex-col gap-6"
      >
        <legend className="sr-only">Theme</legend>
        <div className="flex flex-col gap-2.5">
          <h3 className={groupHead}>
            Follow your system’s light or dark setting
          </h3>
          <div className="grid grid-cols-[repeat(auto-fit,minmax(min(380px,100%),1fr))] gap-4">
            {PAIR_PREFS.map((p) => {
              const pair = PAIRS[p.slice(7) as PairId];
              return (
                <ThemeOption
                  key={p}
                  value={p}
                  label={PAIR_LABELS[p.slice(7) as PairId].label}
                  description={PAIR_LABELS[p.slice(7) as PairId].description}
                  tag={p === DEFAULT_PREF ? "Default" : undefined}
                  previews={[pair.dark, pair.light]}
                  checked={pref === p}
                  note={
                    theme
                      ? `Your system is ${THEMES[theme].mode} now, so you see ${THEMES[theme].label}.`
                      : undefined
                  }
                  onSelect={select}
                />
              );
            })}
          </div>
        </div>
        <div className="flex flex-col gap-2.5">
          <h3 className={groupHead}>Always use one theme</h3>
          <div className="grid grid-cols-[repeat(auto-fit,minmax(min(300px,100%),1fr))] gap-4">
            {SINGLE_PREFS.map((t) => (
              <ThemeOption
                key={t}
                value={t}
                label={THEMES[t].label}
                description={THEMES[t].description}
                tag={THEMES[t].mode === "dark" ? "Dark" : "Light"}
                previews={[t]}
                checked={pref === t}
                onSelect={select}
              />
            ))}
          </div>
        </div>
      </fieldset>

      <p role="status" aria-live="polite" className="sr-only">
        {announcement}
      </p>

      <section
        aria-labelledby="else-h"
        className="flex flex-col gap-2 border-t border-subtle pt-5"
      >
        <h2
          id="else-h"
          className="text-[15px] font-semibold leading-[22px] text-fg"
        >
          Elsewhere
        </h2>
        <ul className="flex list-disc flex-col gap-1 pl-[18px] text-[13px] leading-[19px] text-fg/70">
          <li>
            In{" "}
            <kbd className="rounded border border-subtle px-[5px] font-mono text-[11px] text-fg/75">
              ⌘K
            </kbd>
            , type “theme” to pick one without opening this page.
          </li>
          <li>
            The account menu at the bottom of the sidebar switches to the other
            side of a pair, for example from Graphite to Paper.
          </li>
        </ul>
      </section>
    </SettingsFrame>
  );
}
