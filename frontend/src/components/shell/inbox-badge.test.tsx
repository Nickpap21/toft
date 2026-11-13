import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { NeedsYouItem } from "@/lib/api/inbox.service";
import { needsYouQuery } from "@/lib/queries";
import { InboxIconLink } from "./project-sidebar";

vi.mock("@/context/AuthContext", () => ({ useAuth: () => ({ token: "t" }), useOrgRole: () => "owner" }));
vi.mock("next/navigation", () => ({ useParams: () => ({}), usePathname: () => "/" }));
const items = [
  { key: "no_backup:panel:false:1:0", kind: "no_backup", category: "servers", title: "The panel host", detail: "1 database, no backups", at: "2026-11-04T10:00:00Z", project_id: null, project: "", severity: "warning" },
  { key: "alert:a1", kind: "alert", alert_kind: "not_drawn_yet", category: "servers", title: "fsn1", detail: "", at: "2026-11-04T10:00:00Z", project_id: null, project: "", severity: "warning" },
] as NeedsYouItem[];
vi.mock("@/lib/api/inbox.service", () => ({ inboxService: { needsYou: async () => items } }));

// The badge counts the rows the Inbox page draws, from the page's own query:
// one for every item, a kind it doesn't name included, so it is the main
// sidebar's counts.needs_you too (D-140).
describe("Inbox badge", () => {
  it("counts every item, as the Inbox page draws one row for each", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}><InboxIconLink /></QueryClientProvider>);
    expect(await screen.findByRole("link", { name: "Inbox, 2 need you" })).toBeTruthy();
  });

  // Home polls Needs you every 30 s (needsYouQuery); the badge shares the
  // query, so both observers poll at the one interval.
  it("polls at the interval Home does", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}><InboxIconLink /></QueryClientProvider>);
    await screen.findByRole("link", { name: "Inbox, 2 need you" });
    const observer = client.getQueryCache().find({ queryKey: needsYouQuery().queryKey })!.observers[0];
    expect(observer.options.refetchInterval).toBe(needsYouQuery().refetchInterval);
    expect(observer.options.refetchInterval).toBe(30_000);
  });
});
