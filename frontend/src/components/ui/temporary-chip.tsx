import { cn } from "@/lib/utils";

/** What a temporary (sslip.io) address is for (D-150). */
export const TEMPORARY_NOTE = "For a first look; use your own domain for anything with logins.";

/** The "Temporary" chip beside an sslip.io address (D-150). */
export function TemporaryChip({ className }: { className?: string }) {
  return (
    <span
      title={TEMPORARY_NOTE}
      className={cn("inline-flex h-[22px] flex-none items-center self-start rounded-full bg-status-pending-soft px-2 text-xs font-medium text-status-pending-fg", className)}
    >
      Temporary
    </span>
  );
}

/** A temporary name is one under sslip.io: the backend's domain.IsTemporary,
 *  for the lists that carry only hostnames (the project overview). */
export const isTemporary = (hostname: string) => hostname.endsWith(".sslip.io");

/** Where a domain opens: a temporary name answers over HTTP until its
 *  certificate is issued, the others over HTTPS (D-150). */
export function domainURL(d: { hostname: string; temporary: boolean; certificate_at: string | null }): string {
  return `${d.temporary && !d.certificate_at ? "http" : "https"}://${d.hostname}`;
}

/** Whether a domain check saw a valid certificate that a list of domains
 *  still shows pending: the check records it (certificate_at, D-150), so
 *  the list is out of date. */
export function certificateRecorded(
  check: { domains: { id: string; certificate: string }[] },
  list: { id: string; certificate_at: string | null }[] | undefined,
): boolean {
  return !!list?.some((d) => !d.certificate_at && check.domains.some((f) => f.id === d.id && f.certificate === "valid"));
}
