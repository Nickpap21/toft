import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { setupService, type PanelDomainCheck, type Setup } from "@/lib/api/instance.service";
import AddressStep from "./page";

const reauth = async () => true;
let setup: Setup;

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }));
vi.mock("@/components/reauth", () => ({ useReauth: () => reauth }));
vi.mock("@/components/setup/setup-page", () => ({
  SetupPage: ({ children }: { children: (s: Setup) => React.ReactNode }) => children(setup),
}));
vi.mock("@/lib/api/instance.service", () => ({
  setupQuery: { queryKey: ["setup"] },
  setupService: { setDomain: vi.fn(), checkDomain: vi.fn() },
}));

const elsewhere: PanelDomainCheck = { domain: "panel.example", addresses: ["198.51.100.7"], reaches_panel: false, certificate: { state: "none" } } as PanelDomainCheck;

function open() {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <AddressStep />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.mocked(setupService.setDomain).mockReset().mockResolvedValue({} as Setup);
  vi.mocked(setupService.checkDomain).mockReset().mockResolvedValue(elsewhere);
  setup = { in_progress: false, domain: "", domain_set: true, awaiting_domain: false, role: "apps", role_set: true, kit_acknowledged_at: null, kit_out_of_date: false, finished_at: "2026-11-09T10:00:00Z", app: "" };
});

describe("Setting the panel's domain after setup", () => {
  it("says the address it's on stops answering", async () => {
    open();
    await userEvent.click(screen.getByRole("radio", { name: "A domain" }));
    await userEvent.type(screen.getByRole("textbox", { name: "Domain" }), "panel.example");
    expect(screen.getByText(`Once the domain is set, Toft stops answering on ${window.location.hostname}. If the domain doesn’t reach this server, you can’t get back in.`)).toBeInTheDocument();
  });

  it("says nothing of the kind during setup, while the address keeps answering", async () => {
    setup = { ...setup, in_progress: true, finished_at: null };
    open();
    await userEvent.click(screen.getByRole("radio", { name: "A domain" }));
    expect(screen.queryByText(/stops answering/)).not.toBeInTheDocument();
  });

  it("use it anyway sends the operator's word; a plain save doesn't", async () => {
    open();
    await userEvent.click(screen.getByRole("radio", { name: "A domain" }));
    await userEvent.type(screen.getByRole("textbox", { name: "Domain" }), "panel.example");
    await userEvent.click(screen.getByRole("button", { name: "Check and continue" }));
    expect(setupService.setDomain).not.toHaveBeenCalled();

    await userEvent.click(await screen.findByRole("button", { name: "It points here: use it anyway" }));
    await waitFor(() => expect(setupService.setDomain).toHaveBeenCalledWith("panel.example", true));

    vi.mocked(setupService.checkDomain).mockResolvedValue({ ...elsewhere, reaches_panel: true });
    vi.mocked(setupService.setDomain).mockClear();
    await userEvent.clear(screen.getByRole("textbox", { name: "Domain" }));
    await userEvent.type(screen.getByRole("textbox", { name: "Domain" }), "other.example");
    await userEvent.click(screen.getByRole("button", { name: "Check and continue" }));
    await waitFor(() => expect(setupService.setDomain).toHaveBeenCalledWith("other.example", false));
  });
});
