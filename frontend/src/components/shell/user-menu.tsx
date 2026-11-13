"use client";

import Link from "next/link";
import { LogOut, Moon, Palette, Settings, Sun } from "lucide-react";
import { useAuth } from "@/context/AuthContext";
import { useTheme } from "@/components/theme-provider";
import { otherSide } from "@/lib/theme/resolve";
import { THEMES } from "@/lib/theme/themes";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

export function initials(name?: string, email?: string): string {
  const source = name?.trim() || email?.trim() || "";
  if (!source) return "?";
  const words = source.split(/[\s@._-]+/).filter(Boolean);
  return (words.length > 1 ? words[0][0] + words[1][0] : source.slice(0, 2)).toUpperCase();
}

export function UserMenu() {
  const { user, logout } = useAuth();
  const { theme, toggleMode } = useTheme();
  const other = theme ? THEMES[otherSide(theme)] : null;

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        title={user?.email}
        className="flex min-h-11 w-full min-w-0 items-center gap-2.5 rounded-lg px-2 py-1 text-left transition-colors hover:bg-raised rail-collapsed:justify-center rail-collapsed:px-0"
      >
        <span className="sr-only">Account menu:</span>
        {/* Visible text, so voice control can say the initials. */}
        <span
          className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-raised text-xs font-semibold text-fg"
        >
          {initials(user?.name, user?.email)}
        </span>
        {/* sr-only, not hidden, in the collapsed rail: it is the button's name. */}
        <span className="flex min-w-0 flex-1 flex-col rail-collapsed:sr-only">
          <span className="truncate text-[13px] font-medium leading-[18px] text-fg">{user?.name || user?.email}</span>
          {user?.name && <span className="truncate font-mono text-xs leading-4 text-muted">{user.email}</span>}
        </span>
        {user?.role && (
          <span className="shrink-0 rounded bg-status-pending-soft px-1.5 text-[11px] font-medium capitalize leading-[18px] text-status-pending-fg rail-collapsed:sr-only">
            {user.role}
          </span>
        )}
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" side="top" className="w-60">
        <DropdownMenuLabel className="truncate font-mono font-normal">{user?.email}</DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuItem asChild>
          <Link href="/settings/appearance">
            <Palette aria-hidden="true" className="h-4 w-4 text-muted" />
            Appearance
          </Link>
        </DropdownMenuItem>
        <DropdownMenuItem asChild>
          <Link href="/settings">
            <Settings aria-hidden="true" className="h-4 w-4 text-muted" />
            Settings
          </Link>
        </DropdownMenuItem>
        {other && (
          <DropdownMenuItem onSelect={toggleMode}>
            {other.mode === "dark" ? (
              <Moon aria-hidden="true" className="h-4 w-4 text-muted" />
            ) : (
              <Sun aria-hidden="true" className="h-4 w-4 text-muted" />
            )}
            Use {other.label}
          </DropdownMenuItem>
        )}
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={() => logout()}>
          <LogOut aria-hidden="true" className="h-4 w-4 text-muted" />
          Sign out
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
