import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
    return twMerge(clsx(inputs));
}

/** Unambiguous "YYYY-MM-DD HH:mm" in the viewer's local time — avoids
 *  toLocaleString()'s locale-dependent DD/MM vs MM/DD ambiguity. */
export function formatDateTime(input: string | number | Date): string {
    const d = new Date(input);
    const pad = (n: number) => String(n).padStart(2, "0");
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

const ZONE = new Intl.DateTimeFormat("en", { timeZoneName: "short" });

/** formatDateTime with the viewer's time zone, "2026-11-09 05:31 GMT+2": for
 *  a date beside a time in another zone, as a schedule in server time. The
 *  zone stays on the time's line (a no-break space). */
export function formatDateTimeZone(input: string | number | Date): string {
    const zone = ZONE.formatToParts(new Date(input)).find((p) => p.type === "timeZoneName")?.value;
    return zone ? `${formatDateTime(input)}\u00a0${zone}` : formatDateTime(input);
}

/** "Local Admin" → "LA", "admin" → "AD", "dimitra.konstantinidou" → "DK";
 *  an email's domain is left out. */
export function initials(name: string): string {
  const words = name.trim().split("@")[0].split(/[\s._-]+/).filter(Boolean);
  if (words.length >= 2) return (words[0][0] + words[1][0]).toUpperCase();
  return (words[0] ?? "?").slice(0, 2).toUpperCase();
}

/** "acme’s", "Founders’": the possessive of a name. */
export function possessive(name: string): string {
  return /s$/i.test(name) ? `${name}’` : `${name}’s`;
}
