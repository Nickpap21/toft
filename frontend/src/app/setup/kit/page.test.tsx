import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import { setupService, type Setup } from "@/lib/api/instance.service";
import KitStep from "./page";

const push = vi.fn();
const reauth = async () => true;
let setup: Setup;

vi.mock("next/navigation", () => ({ useRouter: () => ({ push }) }));
vi.mock("@/context/AuthContext", () => ({ useAuth: () => ({ user: { email: "ana@example.com" } }) }));
vi.mock("@/components/reauth", () => ({ useReauth: () => reauth }));
vi.mock("@/components/setup/setup-page", () => ({
  SetupPage: ({ children }: { children: (s: Setup) => React.ReactNode }) => children(setup),
}));
vi.mock("@/lib/api/instance.service", () => ({
  setupQuery: { queryKey: ["setup"] },
  setupService: { patch: vi.fn(), recoveryKit: vi.fn() },
}));

const patch = vi.mocked(setupService.patch);
const unreachable = new ApiError("not yet", 409, { error: "not yet", data: { code: "panel_domain_unreachable" } });

async function finishSetup() {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <KitStep />
    </QueryClientProvider>,
  );
  await screen.findByRole("button", { name: "Download the kit" });
  await userEvent.click(screen.getByRole("button", { name: "Finish and open Home" }));
}

beforeEach(() => {
  push.mockClear();
  patch.mockReset();
  vi.mocked(setupService.recoveryKit).mockReset().mockResolvedValue({ text: "kit", filename: "kit.txt", domain: "panel.example", fingerprint: "fp" } as never);
  setup = {
    in_progress: true,
    domain: "panel.example",
    domain_set: true,
    awaiting_domain: false,
    role: "apps",
    role_set: true,
    kit_acknowledged_at: "2026-11-09T10:00:00Z",
    kit_out_of_date: false,
    finished_at: null,
    app: "",
  };
});

describe("Finishing setup on a domain this server can’t reach", () => {
  it("says what is at stake, naming the domain and this address", async () => {
    patch.mockRejectedValueOnce(unreachable);
    await finishSetup();

    const notice = await screen.findByRole("alert");
    expect(notice).toHaveTextContent("panel.example doesn’t answer from this server yet.");
    expect(notice).toHaveTextContent(`When setup finishes, Toft stops answering on ${window.location.host}, so if the domain doesn’t work you can’t get back in.`);
    expect(notice).toHaveTextContent("Open https://panel.example in another tab to check.");
    expect(screen.getByRole("link", { name: "https://panel.example" })).toHaveAttribute("href", "https://panel.example");
    expect(push).not.toHaveBeenCalled();
  });

  it("Change the address goes back to the address step", async () => {
    patch.mockRejectedValueOnce(unreachable);
    await finishSetup();

    await userEvent.click(await screen.findByRole("button", { name: "Change the address" }));
    expect(push).toHaveBeenCalledWith("/setup/address");
    expect(patch).toHaveBeenCalledTimes(1);
  });

  it("It opens: finish anyway resends with the override and opens Home", async () => {
    patch.mockRejectedValueOnce(unreachable).mockResolvedValueOnce({ ...setup, in_progress: false });
    await finishSetup();

    await userEvent.click(await screen.findByRole("button", { name: "It opens: finish anyway" }));
    await waitFor(() => expect(push).toHaveBeenCalledWith("/dashboard"));
    expect(patch).toHaveBeenNthCalledWith(1, { finished: true });
    expect(patch).toHaveBeenNthCalledWith(2, { finished: true, domain_checked: true });
  });

  it("any other failure shows as before, with no way past it", async () => {
    patch.mockRejectedValueOnce(new ApiError("boom", 500, null));
    await finishSetup();

    expect(await screen.findByRole("alert")).toHaveTextContent("Couldn’t reach Toft. Try again.");
    expect(screen.queryByRole("button", { name: "It opens: finish anyway" })).not.toBeInTheDocument();
  });

  it("someone already on the domain has checked it", async () => {
    setup.domain = window.location.host;
    patch.mockResolvedValueOnce({ ...setup, in_progress: false });
    await finishSetup();

    await waitFor(() => expect(push).toHaveBeenCalledWith("/dashboard"));
    expect(patch).toHaveBeenCalledWith({ finished: true, domain_checked: true });
  });
});

// D-163: the acknowledgement names the kit that was read.
describe("Storing the kit", () => {
  it.each([
    ["a kit not stored yet", { kit_acknowledged_at: null }],
    ["an out-of-date kit", { kit_out_of_date: true }],
  ])("%s is acknowledged with its fingerprint once the box is ticked", async (_, state) => {
    Object.assign(setup, state);
    patch.mockResolvedValue({ ...setup, in_progress: false });
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <KitStep />
      </QueryClientProvider>,
    );
    await screen.findByRole("button", { name: "Download the kit" });
    expect(screen.getByRole("button", { name: "Finish and open Home" })).toBeDisabled();
    await userEvent.click(screen.getByRole("checkbox"));
    await userEvent.click(screen.getByRole("button", { name: "Finish and open Home" }));

    await waitFor(() => expect(push).toHaveBeenCalledWith("/dashboard"));
    expect(patch).toHaveBeenNthCalledWith(1, { kit_acknowledged: true, fingerprint: "fp" });
  });

  it("a kit that changed after it was read is read again, and the box asked for again", async () => {
    setup.kit_acknowledged_at = null;
    patch.mockRejectedValueOnce(new ApiError("the recovery kit changed", 409, { error: "the recovery kit changed", data: { code: "kit_changed" } }));
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <KitStep />
      </QueryClientProvider>,
    );
    await screen.findByRole("button", { name: "Download the kit" });
    await userEvent.click(screen.getByRole("checkbox"));
    await userEvent.click(screen.getByRole("button", { name: "Finish and open Home" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("The kit changed while this page was open. Store the one shown now, then finish.");
    await waitFor(() => expect(setupService.recoveryKit).toHaveBeenCalledTimes(2));
    expect(screen.getByRole("checkbox")).not.toBeChecked();
    expect(push).not.toHaveBeenCalled();
  });
});
