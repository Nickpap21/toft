import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { inboxItems } from "@/components/home/home";
import type { NeedsYouItem } from "@/lib/api/inbox.service";
import { HomeNeedsRows, NeedsYouRows } from "./needs-you";

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }));

const item = (x: Partial<NeedsYouItem>) =>
  ({ key: "no_backup:panel", kind: "no_backup", category: "servers", title: "The panel host", detail: "1 database, no backups", at: "2026-11-01T10:00:00Z", project_id: null, project: "", severity: "warning", snoozes: true, ...x }) as NeedsYouItem;

function rows(its: NeedsYouItem[], done: boolean) {
  const client = new QueryClient();
  render(<QueryClientProvider client={client}><NeedsYouRows items={inboxItems(its)} now={Date.now()} done={done} onDone={() => {}} /></QueryClientProvider>);
}

// A condition marked done is only put off (the 7 days are the backend's
// snoozeFor): the button says so, and the Done list says when it's back.
describe("Needs you rows", () => {
  it("offers to snooze a condition, and to mark an event done", () => {
    rows([item({}), item({ key: "deploy:d1", kind: "deploy_failed", title: "web", detail: "Deploy failed", snoozes: false })], false);
    expect(screen.getByRole("button", { name: "Snooze “The panel host” for 7 days" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Mark “web” done" })).toBeTruthy();
  });

  it("says when a snoozed row comes back, in the Done list", () => {
    rows([item({ snoozed_until: "2026-11-15T12:00:00Z" })], true);
    expect(screen.getByText("1 database, no backups · Back on 2026-11-15")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Move “The panel host” back to the inbox" })).toBeTruthy();
  });
});

describe("Home's Needs you rows", () => {
  // WCAG 2.5.3: someone saying what the row shows reaches its link.
  it("names the link with the row's own words, the action among them", () => {
    render(<HomeNeedsRows items={inboxItems([item({})])} now={Date.now()} />);
    expect(screen.getByRole("link", { name: /No backups.*The panel host.*Set up backups/ })).toBeTruthy();
  });
});
