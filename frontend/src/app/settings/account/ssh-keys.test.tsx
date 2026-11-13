import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { SSHKeys } from "./ssh-keys";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
const confirmPassword = vi.fn();
vi.mock("@/components/reauth", () => ({ useReauth: () => confirmPassword }));

let enabled = true;
const add = vi.fn();
vi.mock("@/lib/api/code.service", () => ({
    gitInfoQuery: { queryKey: ["git"], queryFn: async () => (enabled ? { enabled, ssh_port: 2222 } : { enabled }) },
    codeService: {
        sshKeys: async () => [
            { id: "k1", title: "laptop", fingerprint: "SHA256:abc", key_type: "ssh-ed25519", last_used_at: null, created_at: "2026-10-18T10:00:00Z" },
        ],
        addSSHKey: (...args: unknown[]) => add(...args),
        deleteSSHKey: vi.fn(),
    },
}));

function renderKeys() {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return render(
        <QueryClientProvider client={client}>
            <SSHKeys />
        </QueryClientProvider>,
    );
}

describe("Account → SSH keys", () => {
    beforeEach(() => {
        enabled = true;
        add.mockReset();
        confirmPassword.mockReset();
    });

    it("stacks its rows on a phone instead of scrolling them", async () => {
        renderKeys();
        const row = (await screen.findByText("laptop")).closest("tr");
        const table = screen.getByRole("table");
        expect(table.className).toContain("max-sm:block");
        expect(table.className).toContain("sm:min-w-[640px]");
        expect(row?.className).toContain("max-sm:grid");
    });

    it("lists the keys, and is absent without git", async () => {
        renderKeys();
        expect(await screen.findByText("SHA256:abc")).toBeTruthy();
        expect(screen.getByText(/git over SSH on port 2222/)).toBeTruthy();
        expect(screen.getByText("Never")).toBeTruthy();

        enabled = false;
        const { container } = renderKeys();
        await new Promise((r) => setTimeout(r, 20));
        expect(container.textContent).toBe("");
    });

    it("lets the keyboard reach the table when it scrolls sideways", async () => {
        renderKeys();
        const scroller = await screen.findByRole("group", { name: "Your SSH keys" });
        expect(scroller.getAttribute("tabindex")).toBe("0");
    });

    it("asks for the password before adding, and names the key from its comment", async () => {
        confirmPassword.mockResolvedValue(true);
        add.mockResolvedValue({ id: "k2", title: "me@desk" });
        renderKeys();
        await userEvent.click(await screen.findByRole("button", { name: "Add SSH key" }));
        await userEvent.type(screen.getByLabelText("Public key"), "ssh-ed25519 AAAAC3Nza me@desk");
        expect((screen.getByLabelText("Title") as HTMLInputElement).value).toBe("me@desk");
        await userEvent.click(screen.getByRole("button", { name: "Add key" }));
        expect(confirmPassword).toHaveBeenCalled();
        expect(add).toHaveBeenCalledWith("me@desk", "ssh-ed25519 AAAAC3Nza me@desk");
    });

    it("adds nothing when the password is cancelled", async () => {
        confirmPassword.mockResolvedValue(false);
        renderKeys();
        await userEvent.click(await screen.findByRole("button", { name: "Add SSH key" }));
        await userEvent.type(screen.getByLabelText("Public key"), "ssh-ed25519 AAAAC3Nza me@desk");
        await userEvent.click(screen.getByRole("button", { name: "Add key" }));
        expect(await screen.findByRole("alert")).toBeTruthy();
        expect(add).not.toHaveBeenCalled();
    });
});
