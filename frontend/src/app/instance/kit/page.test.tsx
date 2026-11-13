import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import { setupService, type Setup } from "@/lib/api/instance.service";
import InstanceKitPage from "./page";

let setup: Setup;
let setupDown = false;
let upgrade: unknown = null;
const reauth = vi.fn(async () => true);

vi.mock("@/context/AuthContext", () => ({ useAuth: () => ({ user: { email: "ana@example.com" } }) }));
vi.mock("@/components/reauth", () => ({ useReauth: () => reauth }));
vi.mock("@/components/instance/instance-frame", () => ({ InstanceFrame: ({ children }: { children: React.ReactNode }) => children }));
vi.mock("@/lib/api/instance.service", () => ({
  setupQuery: {
    queryKey: ["instance", "setup"],
    queryFn: async () => {
      if (setupDown) throw new Error("down");
      return setup;
    },
  },
  upgradeQuery: { queryKey: ["instance", "upgrade"], queryFn: async () => upgrade },
  setupService: { patch: vi.fn(), recoveryKit: vi.fn() },
}));

const patch = vi.mocked(setupService.patch);
const recoveryKit = vi.mocked(setupService.recoveryKit);

const show = () =>
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <InstanceKitPage />
    </QueryClientProvider>,
  );

beforeEach(() => {
  patch.mockReset();
  recoveryKit.mockReset().mockResolvedValue({ text: "kit", filename: "kit.txt", domain: "panel.example", fingerprint: "fp1" } as never);
  upgrade = null;
  setupDown = false;
  reauth.mockReset().mockResolvedValue(true);
  setup = {
    in_progress: false,
    domain: "panel.example",
    domain_set: true,
    awaiting_domain: false,
    role: "apps",
    role_set: true,
    kit_acknowledged_at: "2026-10-18T10:00:00Z",
    kit_out_of_date: true,
    finished_at: "2026-10-18T10:00:00Z",
    app: "",
  };
});

// D-163, board ReworkInstanceKit.
describe("Instance → Recovery kit", () => {
  it("says the stored kit is out of date and records the new one with its fingerprint", async () => {
    patch.mockResolvedValue({ ...setup, kit_acknowledged_at: "2026-11-10T09:00:00Z", kit_out_of_date: false });
    show();

    const notice = await screen.findByRole("status");
    expect(notice).toHaveTextContent("The kit stored on 2026-10-18 is out of date");
    // The old kit still restores the backups made before the change (D-164).
    expect(notice).toHaveTextContent("Store this one, and keep the old one while backups from before the change exist: restoring one of those may need it.");
    expect(reauth).toHaveBeenCalledWith(expect.any(String), { returnFocus: { current: screen.getByRole("heading", { name: "Recovery kit" }) } });
    expect(screen.getByText("Out of date")).toBeInTheDocument();
    await screen.findByRole("button", { name: "Download the kit" });
    const mark = screen.getByRole("button", { name: "Mark as stored" });
    expect(mark).toBeDisabled();
    await userEvent.click(screen.getByRole("checkbox"));
    await userEvent.click(mark);

    expect(await screen.findByText("Stored 2026-11-10")).toBeInTheDocument();
    expect(patch).toHaveBeenCalledWith({ kit_acknowledged: true, fingerprint: "fp1" });
    expect(screen.queryByRole("status")).toBeNull();
    expect(screen.queryByRole("checkbox")).toBeNull();
    expect(screen.getByText("Nothing a restore needs has changed since this kit was stored.")).toBeInTheDocument();
    // The button is gone: focus stays in the card.
    expect(screen.getByRole("heading", { name: "Recovery kit" })).toHaveFocus();
  });

  it("reads the kit again when it changed after it was read, and asks for the box again", async () => {
    patch.mockRejectedValue(new ApiError("the recovery kit changed", 409, { error: "the recovery kit changed", data: { code: "kit_changed" } }));
    show();
    await screen.findByRole("button", { name: "Download the kit" });
    await userEvent.click(screen.getByRole("checkbox"));
    await userEvent.click(screen.getByRole("button", { name: "Mark as stored" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("The kit changed while this page was open. Store the one shown now, then mark it as stored.");
    await waitFor(() => expect(recoveryKit).toHaveBeenCalledTimes(2));
    expect(screen.getByRole("checkbox")).not.toBeChecked();
  });

  it("keeps the box ticked when Toft couldn't be reached", async () => {
    patch.mockRejectedValue(new ApiError("boom", 500, null));
    show();
    await screen.findByRole("button", { name: "Download the kit" });
    await userEvent.click(screen.getByRole("checkbox"));
    await userEvent.click(screen.getByRole("button", { name: "Mark as stored" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("Couldn’t reach Toft. Try again.");
    expect(screen.getByRole("checkbox")).toBeChecked();
    expect(recoveryKit).toHaveBeenCalledTimes(1);
  });

  it("asks for nothing while the stored kit is current", async () => {
    setup = { ...setup, kit_out_of_date: false };
    show();
    expect(await screen.findByText("Stored 2026-10-18")).toBeInTheDocument();
    await screen.findByRole("button", { name: "Download the kit" });
    expect(screen.queryByRole("checkbox")).toBeNull();
    expect(screen.queryByRole("button", { name: "Mark as stored" })).toBeNull();
  });

  it("offers to store a kit that never was", async () => {
    setup = { ...setup, kit_acknowledged_at: null, kit_out_of_date: false };
    show();
    expect(await screen.findByText("Not stored yet")).toBeInTheDocument();
    expect(screen.queryByRole("status")).toBeNull();
    expect(screen.getByRole("button", { name: "Mark as stored" })).toBeDisabled();
  });

  it("offers no acknowledgement when whether the kit is stored couldn't be read", async () => {
    setupDown = true;
    show();
    expect(await screen.findByText("Couldn’t load whether the kit is stored.")).toBeInTheDocument();
    await screen.findByRole("button", { name: "Download the kit" });
    expect(screen.queryByRole("checkbox")).toBeNull();
    expect(screen.queryByRole("button", { name: "Mark as stored" })).toBeNull();
  });

  it("asks for the password again before reading a kit that didn't load", async () => {
    recoveryKit.mockRejectedValueOnce(new ApiError("confirm your password", 403, { error: "confirm your password", data: { code: "reauth_required" } }));
    show();
    await userEvent.click(await screen.findByRole("button", { name: "Try again" }));

    await screen.findByRole("button", { name: "Download the kit" });
    expect(reauth).toHaveBeenCalledTimes(2);
    expect(recoveryKit).toHaveBeenCalledTimes(2);
  });

  it("keeps the kit back until the password is confirmed", async () => {
    reauth.mockResolvedValueOnce(false);
    show();
    expect(await screen.findByText("Toft shows the kit once you confirm your password.")).toBeInTheDocument();
    expect(recoveryKit).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Show the kit…" }));

    await screen.findByRole("button", { name: "Download the kit" });
    expect(recoveryKit).toHaveBeenCalledTimes(1);
  });

  // One accent per screen (D-139): the header's Upgrade, when there is one.
  it("leaves the accent to the header's Upgrade", async () => {
    upgrade = { install: "installer", available: true, run: null };
    show();
    await waitFor(() => expect(screen.getByRole("button", { name: "Mark as stored" }).className).not.toContain("bg-accent"));
  });
});
