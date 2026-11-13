import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import BackupsPage from "./page";
import { PanelHost } from "./panel-host";

// A destination that couldn't be read isn't one that isn't set up.

let operator = false;
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  usePathname: () => "/settings/backups",
  useSearchParams: () => new URLSearchParams(),
}));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ isLoading: false, user: { id: "u1", orgId: "o1", orgName: "QA Lab", orgs: [] } }),
  useOrgRole: () => "owner",
  useIsOperator: () => operator,
}));

const ok = (v: unknown) => ({ status: 200, body: JSON.stringify({ data: v }) });
const boom = { status: 500, body: JSON.stringify({ error: "internal error", status: 500 }) };
const server = { id: "s1", name: "web-1", status: "active" };
type Answer = { status: number; body: string };

function serve(over: Record<string, Answer> = {}) {
  const answers: Record<string, Answer> = { "/api/v1/servers": ok([server]), ...over };
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    const a = answers[new URL(url, "http://x").pathname] ?? ok(null);
    return new Response(a.body, { status: a.status });
  }));
}
afterEach(() => {
  vi.unstubAllGlobals();
  operator = false;
});

function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><BackupsPage /></QueryClientProvider>);
}
const rowOf = async (name: string) => (await screen.findByRole("link", { name })).closest("tr")!;

describe("Settings → Backups", () => {
  it("a server's destination that failed isn't No destination or Not set up", async () => {
    serve({ "/api/v1/servers/s1/backup": boom });
    show();
    const row = await rowOf("web-1");
    expect(await within(row).findByText("Couldn’t load this server’s backup.")).toBeInTheDocument();
    expect(within(row).getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
    expect(within(row).queryByText("No destination")).toBeNull();
    expect(within(row).queryByText("Not set up")).toBeNull();
  });

  it("Retry reads it again, and the answer replaces the row", async () => {
    serve({ "/api/v1/servers/s1/backup": boom });
    show();
    const row = await rowOf("web-1");
    const retry = await within(row).findByRole("button", { name: /^Retry/ });
    serve();
    fireEvent.click(retry);
    expect(await within(row).findByText("No destination")).toBeInTheDocument();
  });

  it("the panel host's destination that failed isn't Not set up, and offers no Set up", async () => {
    operator = true;
    serve({ "/api/v1/instance/backup": boom });
    show();
    const row = await rowOf("panel");
    expect(await within(row).findByText("Couldn’t load the panel host’s backup.")).toBeInTheDocument();
    expect(within(row).queryByText("Not set up")).toBeNull();
    expect(within(row).queryByRole("button", { name: /Set up/ })).toBeNull();
  });

  it("the table's Edit opens the panel host's form filled from its destination", async () => {
    operator = true;
    serve({
      "/api/v1/instance/backup": ok({
        endpoint: "https://s3.example.com",
        bucket: "founders-backups",
        access_key: "AKIA123",
        schedule: "03:00",
        repository: "s3:https://s3.example.com/founders-backups/toft/panel",
        last_run_at: null,
        last_ok: null,
        last_ok_at: null,
        last_bytes: null,
        last_error: "",
        created_at: "2026-11-01T00:00:00Z",
      }),
    });
    show();
    const row = await rowOf("panel");
    fireEvent.click(await within(row).findByRole("button", { name: "Edit the panel host’s backup" }));
    expect(await screen.findByLabelText("Endpoint")).toHaveValue("https://s3.example.com");
    expect(screen.getByLabelText("Bucket")).toHaveValue("founders-backups");
    expect(screen.getByLabelText("Access key")).toHaveValue("AKIA123");
  });

  it("the panel host's form is filled when it comes back already being edited", () => {
    const dest = {
      endpoint: "https://s3.example.com",
      bucket: "founders-backups",
      access_key: "AKIA123",
      schedule: "03:00",
      repository: "s3:https://s3.example.com/founders-backups/toft/panel",
      last_run_at: null,
      last_ok: null,
      last_ok_at: null,
      last_bytes: null,
      last_error: "",
      created_at: "2026-11-01T00:00:00Z",
    };
    render(
      <QueryClientProvider client={new QueryClient()}>
        <PanelHost dest={dest} now={Date.now()} editing onEdit={() => {}} />
      </QueryClientProvider>,
    );
    expect(screen.getByLabelText("Endpoint")).toHaveValue("https://s3.example.com");
  });

  it("a restored panel's backups say Not running, and Resume backups sends the stored destination with no key", async () => {
    operator = true;
    const dest = {
      endpoint: "https://s3.example.com",
      bucket: "founders-backups",
      access_key: "AKIA123",
      schedule: "03:30",
      repository: "s3:https://s3.example.com/founders-backups/toft/control-plane",
      last_run_at: "2026-11-09T03:31:00Z",
      last_ok: true,
      last_ok_at: "2026-11-09T03:31:00Z",
      last_bytes: 1024,
      last_error: "",
      created_at: "2026-11-01T00:00:00Z",
    };
    serve({ "/api/v1/instance/backup": ok({ ...dest, paused: true, restored_at: "2026-11-10T12:00:00Z" }) });
    show();
    const row = await rowOf("panel");
    expect(await within(row).findByText("Not running")).toBeInTheDocument();
    expect(within(row).getByText("since the restore")).toBeInTheDocument();
    expect(screen.getByText("Backups are not running on this server")).toBeInTheDocument();
    expect(screen.getByText(/, before the restore/)).toBeInTheDocument();

    // Resumed, and this server hasn't run one yet: the read still names the restore.
    // By the clock the page reads: a last success older than 26 hours says Missed.
    const hoursAgo = (h: number) => new Date(Date.now() - h * 3_600_000).toISOString();
    serve({ "/api/v1/instance/backup": ok({ ...dest, last_run_at: hoursAgo(5), last_ok_at: hoursAgo(5), restored_at: hoursAgo(1) }) });
    fireEvent.click(screen.getByRole("button", { name: "Resume backups" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Resume backups" })).not.toBeInTheDocument());
    expect(screen.getByRole("heading", { name: "Panel host" })).toHaveFocus();
    const put = vi.mocked(fetch).mock.calls.find(([, init]) => (init as RequestInit | undefined)?.method === "PUT")!;
    expect(JSON.parse((put[1] as RequestInit).body as string)).toEqual({
      endpoint: "https://s3.example.com",
      bucket: "founders-backups",
      access_key: "AKIA123",
      secret_key: "",
      schedule: "03:30",
    });
    expect(within(row).queryByText("Not running")).not.toBeInTheDocument();
    expect(within(row).getByText("Waiting")).toBeInTheDocument();
    expect(within(row).getByText("no backup since the restore")).toBeInTheDocument();
    expect(screen.getByText("Waiting for the first backup")).toBeInTheDocument();
    expect(screen.getByText(/is from before the restore/)).toBeInTheDocument();
    expect(screen.queryByText(/backed up, no errors/)).not.toBeInTheDocument();
  });

  it("a resumed panel whose last recorded success is overdue says Missed, and still says that backup is from before the restore", async () => {
    operator = true;
    serve({
      "/api/v1/instance/backup": ok({
        endpoint: "https://s3.example.com",
        bucket: "founders-backups",
        access_key: "AKIA123",
        schedule: "03:30",
        repository: "s3:https://s3.example.com/founders-backups/toft/control-plane",
        last_run_at: "2026-11-01T03:31:00Z",
        last_ok: true,
        last_ok_at: "2026-11-01T03:31:00Z",
        last_bytes: 1024,
        last_error: "",
        created_at: "2026-10-01T00:00:00Z",
        restored_at: "2026-11-02T12:00:00Z",
      }),
    });
    show();
    const card = within(await screen.findByRole("region", { name: "Panel host" }));
    expect(await card.findByText(/is from before the restore/)).toBeInTheDocument();
    expect(card.getByText("Missed")).toBeInTheDocument();
    expect(card.queryByText(/last\.json/)).not.toBeInTheDocument();
  });

  it("a destination waiting for its first backup says when it starts and how to run one now, and can be edited or stopped", async () => {
    operator = true;
    serve({
      "/api/v1/instance/backup": ok({
        endpoint: "https://s3.example.com",
        bucket: "founders-backups",
        access_key: "AKIA123",
        schedule: "03:30",
        repository: "s3:https://s3.example.com/founders-backups/toft/control-plane",
        last_run_at: null,
        last_ok: null,
        last_ok_at: null,
        last_bytes: null,
        last_error: "",
        created_at: "2026-11-01T00:00:00Z",
      }),
    });
    show();
    expect(await screen.findByText(/^Saved and written to the panel host\. The first backup starts at/)).toBeInTheDocument();
    expect(screen.getByText(/run one on the panel host \(below, under Restoring\)/)).toBeInTheDocument();
    const card = within(screen.getByRole("region", { name: "Panel host" }));
    expect(card.queryByText("Last backup")).not.toBeInTheDocument();
    expect(card.getByText("Destination")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Stop backups…" })).toBeInTheDocument();
  });

  it("a destination that isn't set up still says so", async () => {
    serve();
    show();
    const row = await rowOf("web-1");
    expect(await within(row).findByText("No destination")).toBeInTheDocument();
    expect(within(row).getByText("Not set up")).toBeInTheDocument();
  });
});
