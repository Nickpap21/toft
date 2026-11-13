import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import { DNSProviderCard } from "./dns-provider-card";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const put = vi.fn();
const del = vi.fn();
let saved: { provider: string; credential_keys: string[]; updated_at: string } | null = null;
vi.mock("@/lib/api/server.service", () => ({
    dnsProviderService: {
        get: async () => {
            if (saved) return saved;
            throw new ApiError("this server has no DNS provider", 404, null);
        },
        put: (...args: unknown[]) => put(...args),
        delete: (...args: unknown[]) => del(...args),
    },
}));

function renderCard(canManage: boolean) {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return render(
        <QueryClientProvider client={client}>
            <DNSProviderCard serverId="srv-1" serverName="fsn1-prod-01" canManage={canManage} />
        </QueryClientProvider>,
    );
}

describe("DNSProviderCard", () => {
    it("sends the provider and its named variables, and then shows only the names", async () => {
        put.mockResolvedValue({ provider: "cloudflare", credential_keys: ["CF_DNS_API_TOKEN"], updated_at: "" });
        renderCard(true);

        await userEvent.click(await screen.findByRole("button", { name: "Set up" }));
        await userEvent.type(screen.getByLabelText("Provider"), "Cloudflare");
        await userEvent.type(screen.getByLabelText("Variable 1 name"), "cf_dns_api_token");
        await userEvent.type(screen.getByLabelText("Variable 1 value"), "tok-s3cret");
        await userEvent.click(screen.getByRole("button", { name: "Add variable" })); // left empty: not sent
        await userEvent.click(screen.getByRole("button", { name: "Save" }));

        expect(put).toHaveBeenCalledWith("srv-1", "cloudflare", { CF_DNS_API_TOKEN: "tok-s3cret" });
        // Saved: the variable's name only, and "Replace".
        expect(await screen.findByRole("button", { name: "Replace" })).toBeTruthy();
        expect(screen.getAllByText("CF_DNS_API_TOKEN")).toHaveLength(1);
        expect(screen.queryByDisplayValue("tok-s3cret")).toBeNull();
    });

    it("asks before removing the provider, with Cancel focused", async () => {
        saved = { provider: "cloudflare", credential_keys: ["CF_DNS_API_TOKEN"], updated_at: "" };
        del.mockResolvedValue(undefined);
        renderCard(true);

        await userEvent.click(await screen.findByRole("button", { name: "Remove…" }));
        const dialog = await screen.findByRole("dialog", { name: "Remove Cloudflare from fsn1-prod-01?" });
        expect(dialog.textContent).toMatch(/wildcard certificates stop renewing/);
        expect(document.activeElement).toBe(screen.getByRole("button", { name: "Cancel" }));
        expect(del).not.toHaveBeenCalled();

        await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
        expect(screen.queryByRole("dialog")).toBeNull();
        expect(del).not.toHaveBeenCalled();

        await userEvent.click(screen.getByRole("button", { name: "Remove…" }));
        await userEvent.click(await screen.findByRole("button", { name: "Remove DNS provider" }));
        expect(del).toHaveBeenCalledWith("srv-1");
        saved = null;
    });

    it("is read-only for non-owners", async () => {
        renderCard(false);
        expect(await screen.findByText(/No DNS provider/)).toBeTruthy();
        expect(screen.queryByRole("button", { name: "Set up" })).toBeNull();
    });
});
