import { focusManager, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ApiError, fetchAPI } from "@/lib/api/client";
import { PANEL_KEY } from "../settings/backups/panel-host";
import InstanceOverviewPage from "./page";

vi.mock("next/navigation", () => ({ useRouter: () => ({ replace: vi.fn(), push: vi.fn() }), usePathname: () => "/instance" }));
vi.mock("@/context/AuthContext", () => ({ useAuth: () => ({ isLoading: false }), useIsOperator: () => true }));
const confirm = vi.hoisted(() => vi.fn(async () => true));
vi.mock("@/components/reauth", () => ({ useReauth: () => confirm }));
vi.mock("@/lib/api/client", async (actual) => ({ ...(await actual<typeof import("@/lib/api/client")>()), fetchAPI: vi.fn(async () => null) }));

// Overview shows the panel's state: a no_backup row marked done in the Inbox
// still has to show beside Health's "Not set up".
describe("Instance Overview", () => {
  it("reads Needs you whatever the person marked", async () => {
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><InstanceOverviewPage /></QueryClientProvider>);
    await waitFor(() => expect(vi.mocked(fetchAPI).mock.calls.map((c) => c[0])).toContain("/api/v1/inbox/needs-you?done=any"));
  });

  // A failed read is not "Not set up": Needs you has no no_backup item then.
  it("shows no Backups row when the backup can't be read", async () => {
    vi.mocked(fetchAPI).mockImplementation(async (path) => {
      if (path === "/api/v1/instance/backup") throw new Error("boom");
      return path === "/api/v1/instance/panel" ? { version: "v1", healthy: true, containers: [], certificate: null, uptime_seconds: 0, shell: true } : null;
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}><InstanceOverviewPage /></QueryClientProvider>);
    await screen.findByRole("heading", { name: "Health" });
    await waitFor(() => expect(client.getQueryState(PANEL_KEY)?.status).toBe("error"));
    await act(() => new Promise((r) => setTimeout(r, 0)));
    expect(screen.queryByText("Backups")).toBeNull();
  });

  const panel = { version: "v1", healthy: true, containers: [], certificate: null, uptime_seconds: 0, shell: true };
  const host = { role: "apps", reservation: { panel_cpu: 1, panel_memory_bytes: 2 ** 30 } };
  const show = () => render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><InstanceOverviewPage /></QueryClientProvider>);

  it.each(["/api/v1/instance/setup", "/api/v1/instance/panel-host"])("Setup says so with Retry when %s fails", async (failing) => {
    vi.mocked(fetchAPI).mockImplementation(async (path) => {
      if (path === failing) throw new Error("boom");
      return path === "/api/v1/instance/panel" ? panel : path === "/api/v1/instance/panel-host" ? host : path === "/api/v1/instance/setup" ? { domain: "", role: "apps" } : null;
    });
    show();
    const card = await screen.findByRole("region", { name: "Setup" });
    expect(await within(card).findByText("Couldn’t load the panel’s setup.")).toBeInTheDocument();
    expect(within(card).getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
  });

  // D-163: the stored kit no longer restores this panel.
  it("says the recovery kit is out of date, in Needs attention and in Setup, each leading to the kit", async () => {
    vi.mocked(fetchAPI).mockImplementation(async (path) =>
      path === "/api/v1/instance/panel" ? panel : path === "/api/v1/instance/panel-host" ? host : path === "/api/v1/instance/setup" ? { domain: "", role: "apps", kit_acknowledged_at: "2026-10-18T10:00:00Z", kit_out_of_date: true } : null,
    );
    show();
    const row = (await screen.findByText("The stored recovery kit is out of date")).closest("li")!;
    expect(within(row).getByRole("link", { name: "Show kit" })).toHaveAttribute("href", "/instance/kit");
    const card = screen.getByRole("region", { name: "Setup" });
    expect(await within(card).findByText("Out of date")).toBeInTheDocument();
    expect(within(card).getByText("Stored 2026-10-18, before the key or the backup settings changed")).toBeInTheDocument();
    expect(within(card).getByRole("link", { name: "Show kit" })).toHaveAttribute("href", "/instance/kit");
  });

  // D-164: a restored panel answers on its IP until its domain reaches it, or an operator says stop.
  describe("a restored panel still on its IP", () => {
    const restored = { domain: "panel.example", role: "apps", awaiting_domain: true };
    const CHECK = "/api/v1/instance/setup/domain-check";
    const reads = (setup: object, reaches: boolean | Error = true) => async (path: string) => {
      if (path === "/api/v1/instance/setup/domain") return { ...restored, awaiting_domain: false };
      if (path === CHECK) {
        if (reaches instanceof Error) throw reaches;
        return { domain: "panel.example", addresses: [], reaches_panel: reaches, certificate: { state: "pending", expires_at: null, issuer: "" } };
      }
      return path === "/api/v1/instance/panel" ? panel : path === "/api/v1/instance/panel-host" ? host : path === "/api/v1/instance/setup" ? setup : null;
    };
    const at = (hostname: string) => {
      const assign = vi.fn();
      vi.stubGlobal("location", { ...window.location, hostname, assign });
      return assign;
    };

    it("says so in Needs attention and in Setup, and a panel that isn't waiting says neither", async () => {
      vi.mocked(fetchAPI).mockImplementation(reads(restored));
      const { unmount } = show();
      const row = (await screen.findByText("This panel also answers on its server’s IP address")).closest("li")!;
      expect(within(row).getByRole("button", { name: "Stop answering on the IP…" })).toBeInTheDocument();
      expect(await within(screen.getByRole("region", { name: "Setup" })).findByText("Also on the server’s IP address until the domain reaches this panel")).toBeInTheDocument();
      unmount();

      vi.mocked(fetchAPI).mockImplementation(reads({ ...restored, awaiting_domain: false }));
      show();
      expect(await within(await screen.findByRole("region", { name: "Setup" })).findByText("Let’s Encrypt certificate")).toBeInTheDocument();
      expect(screen.queryByText("This panel also answers on its server’s IP address")).toBeNull();
    });

    it("stops it from the domain: asks for the password, saves the same address as checked, and the row goes", async () => {
      at("panel.example");
      vi.mocked(fetchAPI).mockImplementation(reads(restored));
      show();
      (await screen.findByRole("button", { name: "Stop answering on the IP…" })).click();
      const dialog = await screen.findByRole("alertdialog", { name: "Stop answering on the IP address?" });
      expect(within(dialog).getByText("You opened this panel at its domain, so it keeps working for you.")).toBeInTheDocument();
      within(dialog).getByRole("button", { name: "Stop answering on the IP" }).click();
      await waitFor(() => expect(confirm).toHaveBeenCalledWith(expect.stringContaining("asks for your password first"), expect.anything()));
      await waitFor(() => expect(screen.queryByText("This panel also answers on its server’s IP address")).toBeNull());
      const put = vi.mocked(fetchAPI).mock.calls.find((c) => c[0] === "/api/v1/instance/setup/domain")!;
      expect(put[1]).toMatchObject({ method: "PUT", body: JSON.stringify({ domain: "panel.example", domain_checked: true }) });
      expect(screen.queryByRole("alertdialog")).toBeNull();
      // The browser on the domain is the proof: the panel is asked nothing.
      expect(vi.mocked(fetchAPI).mock.calls.some((c) => c[0] === CHECK)).toBe(false);
      vi.unstubAllGlobals();
    });

    it("on the IP, with the domain reaching this panel: says so, lets the panel check again as it stops, and moves there after", async () => {
      const assign = at("203.0.113.7");
      vi.mocked(fetchAPI).mockClear().mockImplementation(reads(restored));
      show();
      (await screen.findByRole("button", { name: "Stop answering on the IP…" })).click();
      const dialog = await screen.findByRole("alertdialog");
      expect(within(dialog).getByText("203.0.113.7")).toBeInTheDocument();
      await waitFor(() => expect(dialog).toHaveAccessibleDescription(expect.stringContaining("reaches this panel right now")));
      expect(within(dialog).queryByRole("checkbox")).toBeNull();
      within(dialog).getByRole("button", { name: "Stop answering on the IP" }).click();
      await waitFor(() => expect(assign).toHaveBeenCalledWith("https://panel.example/?next=%2Finstance"));
      const put = vi.mocked(fetchAPI).mock.calls.find((c) => c[0] === "/api/v1/instance/setup/domain")!;
      expect(put[1]).toMatchObject({ body: JSON.stringify({ domain: "panel.example" }) });
      vi.unstubAllGlobals();
    });

    // The lock-out: the domain points elsewhere and the IP is the only way in.
    it.each([
      ["the domain doesn’t reach this panel", false, "doesn’t reach this panel right now"],
      ["the check fails", new Error("boom"), "Toft couldn’t check whether"],
    ])("on the IP, when %s: says so, gives the way back, and stops only on the operator’s word", async (_, reaches, words) => {
      const assign = at("203.0.113.7");
      vi.mocked(fetchAPI).mockClear().mockImplementation(reads(restored, reaches));
      show();
      (await screen.findByRole("button", { name: "Stop answering on the IP…" })).click();
      const dialog = await screen.findByRole("alertdialog");
      // Focus starts on Cancel, not the link.
      await waitFor(() => expect(within(dialog).getByRole("button", { name: "Cancel" })).toHaveFocus());
      await waitFor(() => expect(dialog).toHaveAccessibleDescription(expect.stringContaining(words)));
      expect(dialog).toHaveAccessibleDescription(expect.stringContaining("nobody can open this panel"));
      expect(within(dialog).getByText("sudo toft answer-on-ip")).toBeInTheDocument();
      const link = within(dialog).getByRole("link", { name: /^open the panel at its domain ?\(opens in a new tab\)$/ });
      expect(link).toHaveAttribute("href", "https://panel.example/instance");
      expect(link).toHaveAttribute("target", "_blank");
      const stop = within(dialog).getByRole("button", { name: "Stop answering on the IP" });
      expect(stop).toBeDisabled();
      within(dialog).getByRole("checkbox", { name: "I can reach the server over SSH if the panel can’t be opened" }).click();
      await waitFor(() => expect(stop).toBeEnabled());
      // Back from the tab the link opened: the warning and the tick stay, nothing is asked again.
      act(() => (focusManager.setFocused(false), focusManager.setFocused(true)));
      await act(() => new Promise((r) => setTimeout(r, 0)));
      expect(vi.mocked(fetchAPI).mock.calls.filter((c) => c[0] === CHECK)).toHaveLength(1);
      expect(within(dialog).getByRole("checkbox")).toBeChecked();
      focusManager.setFocused(undefined);
      stop.click();
      await waitFor(() => expect(assign).toHaveBeenCalledWith("https://panel.example/?next=%2Finstance"));
      const put = vi.mocked(fetchAPI).mock.calls.find((c) => c[0] === "/api/v1/instance/setup/domain")!;
      expect(put[1]).toMatchObject({ body: JSON.stringify({ domain: "panel.example", domain_checked: true }) });
      vi.unstubAllGlobals();
    });

    it("a stop the panel refuses, the domain having moved since the check, becomes that warning and not an error", async () => {
      const assign = at("203.0.113.7");
      const read = reads(restored);
      vi.mocked(fetchAPI).mockClear().mockImplementation(async (path) => {
        if (path === "/api/v1/instance/setup/domain") throw new ApiError("that domain doesn't reach this panel yet", 409, { data: { code: "panel_domain_unreachable" } });
        return read(path);
      });
      show();
      (await screen.findByRole("button", { name: "Stop answering on the IP…" })).click();
      const dialog = await screen.findByRole("alertdialog");
      await waitFor(() => expect(dialog).toHaveAccessibleDescription(expect.stringContaining("reaches this panel right now")));
      within(dialog).getByRole("button", { name: "Stop answering on the IP" }).click();
      await waitFor(() => expect(dialog).toHaveAccessibleDescription(expect.stringContaining("doesn’t reach this panel right now")));
      expect(within(dialog).getByRole("checkbox")).not.toBeChecked();
      expect(within(dialog).getByRole("button", { name: "Stop answering on the IP" })).toBeDisabled();
      expect(within(dialog).queryByRole("alert")).toBeNull();
      expect(assign).not.toHaveBeenCalled();
      vi.unstubAllGlobals();
    });

    it("stops nothing when the password prompt is cancelled", async () => {
      at("panel.example");
      confirm.mockResolvedValueOnce(false);
      vi.mocked(fetchAPI).mockImplementation(reads(restored));
      show();
      (await screen.findByRole("button", { name: "Stop answering on the IP…" })).click();
      const dialog = await screen.findByRole("alertdialog");
      const asked = confirm.mock.calls.length;
      within(dialog).getByRole("button", { name: "Stop answering on the IP" }).click();
      await waitFor(() => expect(confirm.mock.calls.length).toBe(asked + 1));
      await act(() => new Promise((r) => setTimeout(r, 0)));
      // Not "Stopping…": nothing was sent.
      expect(within(dialog).getByRole("button", { name: "Stop answering on the IP" })).toBeEnabled();
      expect(vi.mocked(fetchAPI).mock.calls.some((c) => c[0] === "/api/v1/instance/setup/domain")).toBe(false);
      expect(within(dialog).queryByRole("alert")).toBeNull();
      vi.unstubAllGlobals();
    });
  });

  it("Needs attention names what it couldn't check, not that nothing needs it", async () => {
    vi.mocked(fetchAPI).mockImplementation(async (path) => {
      if (path === "/api/v1/instance/panel/updates") throw new Error("boom");
      return path === "/api/v1/instance/panel" ? panel : null;
    });
    show();
    expect(await screen.findByText("Couldn’t check updates")).toBeInTheDocument();
    expect(screen.queryByText("Nothing needs attention.")).toBeNull();
  });

  const panelOk = async (path: string) => (path === "/api/v1/instance/panel" ? panel : null);

  // Reads still going are not an answer: the all-clear waits for them.
  it("Needs attention holds its all-clear until every read has answered", async () => {
    vi.mocked(fetchAPI).mockImplementation(async (path) => {
      if (path === "/api/v1/instance/panel/updates") return new Promise(() => {});
      return panelOk(path);
    });
    show();
    expect(await screen.findByText("Checking…")).toBeInTheDocument();
    expect(screen.queryByText("Nothing needs attention.")).toBeNull();
  });

  it("gives the all-clear once they have", async () => {
    vi.mocked(fetchAPI).mockImplementation(panelOk);
    show();
    expect(await screen.findByText("Nothing needs attention.")).toBeInTheDocument();
    expect(screen.queryByText("Checking…")).toBeNull();
  });

  // A failed read that polls is pending for the fetch: it must not leave the all-clear behind.
  it("a failed read fetching again stays Couldn't check", async () => {
    vi.mocked(fetchAPI).mockImplementation(async (path) => {
      if (path === "/api/v1/instance/panel/updates") throw new Error("boom");
      return panelOk(path);
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}><InstanceOverviewPage /></QueryClientProvider>);
    expect(await screen.findByText("Couldn’t check updates")).toBeInTheDocument();
    vi.mocked(fetchAPI).mockImplementation(async (path) => (path === "/api/v1/instance/panel/updates" ? new Promise(() => {}) : panelOk(path)));
    await act(async () => void client.refetchQueries({ queryKey: ["updates", null] }));
    await act(() => new Promise((r) => setTimeout(r, 0)));
    expect(screen.getByText("Couldn’t check updates")).toBeInTheDocument();
    expect(screen.queryByText("Nothing needs attention.")).toBeNull();
  });

  // F6: the row, and the focused Retry in it, stay while it reads again.
  it("Setup's Retry keeps its button while it reads again", async () => {
    let calls = 0;
    vi.mocked(fetchAPI).mockImplementation(async (path) => {
      if (path === "/api/v1/instance/setup") {
        if (calls++ === 0) throw new Error("boom");
        return new Promise(() => {});
      }
      return path === "/api/v1/instance/panel" ? panel : path === "/api/v1/instance/panel-host" ? host : null;
    });
    show();
    const card = await screen.findByRole("region", { name: "Setup" });
    const retry = await within(card).findByRole("button", { name: /^Retry/ });
    retry.focus();
    retry.click();
    await waitFor(() => expect(calls).toBe(2));
    expect(within(screen.getByRole("region", { name: "Setup" })).getByRole("button", { name: /^Retry/ })).toBe(retry);
    expect(retry).toHaveFocus();
  });
});
