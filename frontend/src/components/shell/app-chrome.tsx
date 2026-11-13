"use client";

import { usePathname } from "next/navigation";
import { isBarePath } from "@/lib/routes";
import { AppShell } from "./app-shell";

/** Public pages (sign-in, first-run set-up, invites) and the setup wizard
 *  render in their own frame; everything else gets the signed-in shell. */
export function AppChrome({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  return isBarePath(pathname) ? <>{children}</> : <AppShell>{children}</AppShell>;
}
