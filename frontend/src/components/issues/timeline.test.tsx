import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { IssueTimelineEntry } from "@/lib/api/issue.service";
import { addDays, day, fmtDay, iso, today } from "./cycles";
import { TimelineView } from "./timeline";

const after = (n: number) => iso(addDays(day(today()), n));
const update = vi.fn();
const entry = (number: number, x: Partial<IssueTimelineEntry>) =>
  ({ key: `STO-${number}`, number, title: `Issue ${number}`, status: "in_progress", priority: "high", type: { id: "t", name: "Task", kind: "standard", icon: "task", color: "blue" }, start_on: null, due_on: null, started_at: null, shipped_at: null, assignee: null, labels: [], estimate: null, progress: null, ...x }) as unknown as IssueTimelineEntry;
const container = (icon: string) => ({ id: icon, name: icon, kind: "container", icon, color: "blue" }) as IssueTimelineEntry["type"];
const timeline = (items: IssueTimelineEntry[]) => ({ cycles: [], blocks: [], undated: 0, items });
const issueTimeline = vi.fn(async () =>
  timeline([entry(1, { start_on: "2026-11-02", due_on: "2026-11-12" }), entry(2, { due_on: "2026-11-20" }), entry(3, { type: container("release"), start_on: "2026-11-02", due_on: "2026-11-25" })]),
);
vi.mock("@/lib/api/issue.service", () => ({ issueService: () => ({ issueTimeline, update }) }));
vi.mock("./issue-detail", () => ({ useIssueRefresh: () => vi.fn() }));

function open(canWrite = true) {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <TimelineView projectId="p1" filters={{} as never} canWrite={canWrite} scale={"off" as never} zoom="weeks" deps={false} />
    </QueryClientProvider>,
  );
  return screen.findByRole("button", { name: /STO-1,/ });
}

beforeEach(() => update.mockReset());

// WCAG 2.5.3: each bar's name starts with the words it shows.
it("names bars and milestones with what they show", async () => {
  issueTimeline.mockResolvedValueOnce(
    timeline([
      entry(1, { status: "todo", title: "Soon", start_on: after(-1), due_on: after(1) }),
      entry(2, { status: "todo", title: "Sized", start_on: after(-1), due_on: after(9), estimate: 3 }),
      entry(3, { status: "todo", title: "Dated", due_on: after(9) }),
    ]),
  );
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <TimelineView projectId="p1" filters={{} as never} canWrite={false} scale="fibonacci" zoom="weeks" deps={false} />
    </QueryClientProvider>,
  );
  expect(await screen.findByRole("button", { name: `due tomorrow, STO-1, Soon, from ${fmtDay(after(-1))}, due ${fmtDay(after(1))}` })).toBeTruthy();
  expect(screen.getByRole("button", { name: /^3 points, STO-2, Sized/ })).toBeTruthy();
  expect(screen.getByRole("button", { name: `due ${fmtDay(after(9))}, STO-3, Dated` })).toBeTruthy();
});

describe("the Timeline popover and focus", () => {
  it("takes focus when a bar opens it, and gives it back on Escape", async () => {
    const user = userEvent.setup();
    const bar = await open();
    bar.focus();
    await user.keyboard("{Enter}");
    const pop = await screen.findByRole("dialog", { name: "Issue 1" });
    await waitFor(() => expect(pop).toHaveFocus());
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(bar).toHaveFocus();
  });

  it.each([
    ["a due date", "STO-2", "Issue 2"],
    ["a release", "STO-3", "Issue 3"],
  ])("gives it back to %s's diamond too", async (_, key, title) => {
    const user = userEvent.setup();
    await open();
    const diamond = screen.getByRole("button", { name: new RegExp(`${key},`) });
    diamond.focus();
    await user.keyboard("{Enter}");
    await waitFor(() => expect(screen.getByRole("dialog", { name: title })).toHaveFocus());
    await user.keyboard("{Escape}");
    expect(diamond).toHaveFocus();
  });

  it("shows the dates of the bar picked last", async () => {
    const user = userEvent.setup();
    const bar = await open();
    bar.focus();
    await user.keyboard("{Enter}");
    await screen.findByRole("dialog", { name: "Issue 1" });
    screen.getByRole("button", { name: /STO-2,/ }).focus();
    await user.keyboard("{Enter}");
    await screen.findByRole("dialog", { name: "Issue 2" });
    await user.click(screen.getByRole("button", { name: "Edit dates" }));
    expect((screen.getByLabelText("Starts") as HTMLInputElement).value).toBe("");
    expect((screen.getByLabelText("Due") as HTMLInputElement).value).toBe("2026-11-20");
  });

  it.each([
    ["bar", "STO-1", "Issue 1"],
    ["diamond", "STO-2", "Issue 2"],
  ])("opens from a viewer's mouse on a %s", async (_, key, title) => {
    const user = userEvent.setup();
    await open(false);
    await user.click(screen.getByRole("button", { name: new RegExp(`${key},`) }));
    expect(await screen.findByRole("dialog", { name: title })).toBeTruthy();
  });

  it("puts focus on Starts for Edit dates, and back on the bar on Cancel", async () => {
    const user = userEvent.setup();
    const bar = await open();
    bar.focus();
    await user.keyboard("{Enter}");
    await user.click(await screen.findByRole("button", { name: "Edit dates" }));
    expect(screen.getByLabelText("Starts")).toHaveFocus();
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(bar).toHaveFocus();
  });

  it("keeps focus in the popover when Save dates closes the form", async () => {
    update.mockResolvedValue({});
    const user = userEvent.setup();
    const bar = await open();
    bar.focus();
    await user.keyboard("{Enter}");
    await user.click(await screen.findByRole("button", { name: "Edit dates" }));
    await user.click(screen.getByRole("button", { name: "Save dates" }));
    await waitFor(() => expect(screen.getByRole("dialog", { name: "Issue 1" })).toHaveFocus());
  });

  it("falls back to the Timeline when a save takes the issue off it", async () => {
    update.mockResolvedValue({});
    const user = userEvent.setup();
    const bar = await open();
    bar.focus();
    await user.keyboard("{Enter}");
    await user.click(await screen.findByRole("button", { name: "Edit dates" }));
    issueTimeline.mockResolvedValueOnce(timeline([entry(2, { due_on: "2026-11-20" })]));
    await user.click(screen.getByRole("button", { name: "Save dates" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.getByRole("group", { name: "Timeline" })).toHaveFocus();
  });

  it("falls back to the Timeline on Escape when the issue kept its row but lost its bar", async () => {
    update.mockResolvedValue({});
    const user = userEvent.setup();
    const epic = entry(4, { type: container("epic"), start_on: "2026-11-02", due_on: "2026-11-25" });
    issueTimeline.mockResolvedValueOnce(timeline([epic, entry(1, { parent: "STO-4", start_on: "2026-11-02", due_on: "2026-11-12" })]));
    const bar = await open();
    bar.focus();
    await user.keyboard("{Enter}");
    await user.click(await screen.findByRole("button", { name: "Edit dates" }));
    issueTimeline.mockResolvedValueOnce(timeline([epic, entry(1, { parent: "STO-4" })]));
    await user.click(screen.getByRole("button", { name: "Save dates" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: /STO-1,/ })).toBeNull());
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.getByRole("group", { name: "Timeline" })).toHaveFocus();
  });
});
