"use client";

import { useEffect, useRef } from "react";
import { useQuery } from "@tanstack/react-query";
import { useAuth } from "@/context/AuthContext";
import { useTheme } from "@/components/theme-provider";
import { isTokenExpired } from "@/lib/api/client";
import { userService } from "@/lib/api/user.service";
import { THEME_PREFS, type ThemePref } from "@/lib/theme/themes";

/**
 * Keeps the theme in step with the account (D-32, D-46). The browser's
 * stored choice paints first (the head script); once /me answers, the
 * account's choice wins. An account with none takes this browser's. After
 * that, every change made here (Appearance, ⌘K, the account menu) is saved.
 */
export function ThemeAccountSync() {
  const { token } = useAuth();
  const { pref, setPref } = useTheme();
  const signedIn = !!token && !isTokenExpired(token);
  const me = useQuery({ queryKey: ["me"], queryFn: () => userService.getMe(), enabled: signedIn });
  // The choice the account and this browser agree on; undefined until /me
  // answers on this page load.
  const agreed = useRef<ThemePref | undefined>(undefined);

  useEffect(() => {
    if (!me.data || !pref) return;
    const save = (p: ThemePref) => {
      agreed.current = p;
      userService.setTheme(p).catch(() => {
        // Kept in this browser; the next change or page load tries again.
        agreed.current = undefined;
      });
    };
    if (agreed.current === undefined) {
      const saved = me.data.theme;
      if (saved && THEME_PREFS.includes(saved)) {
        agreed.current = saved;
        if (saved !== pref) setPref(saved);
      } else {
        save(pref);
      }
      return;
    }
    if (pref !== agreed.current) save(pref);
  }, [me.data, pref, setPref]);

  return null;
}
