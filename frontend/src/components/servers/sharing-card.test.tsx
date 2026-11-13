import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { SharingCard } from "./sharing-card";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
vi.mock("@/context/AuthContext", () => ({
    useAuth: () => ({
        user: {
            orgId: "org-northwind",
            orgs: [
                { org_id: "org-northwind", name: "Northwind", role: "owner" },
                { org_id: "org-personal", name: "Personal Workspace", role: "owner" },
                { org_id: "org-client", name: "Client", role: "developer" },
            ],
        },
    }),
}));

const putShare = vi.fn();
vi.mock("@/lib/api/server.service", () => ({
    serverService: {
        listShares: async () => [],
        putShare: (...args: unknown[]) => putShare(...args),
        deleteShare: vi.fn(),
    },
}));

function renderCard(canManage: boolean) {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return render(
        <QueryClientProvider client={client}>
            <SharingCard serverId="srv-1" canManage={canManage} />
        </QueryClientProvider>,
    );
}

describe("SharingCard", () => {
    it("offers only the other workspaces the user owns, and sends the share in MiB", async () => {
        putShare.mockResolvedValue([{ org_id: "org-personal", org_name: "Personal Workspace", cpu_cores: 1, memory_mb: 1536, created_at: "" }]);
        renderCard(true);

        const select = await screen.findByLabelText("Workspace");
        expect(within(select).queryByRole("option", { name: "Northwind" })).toBeNull();
        expect(within(select).queryByRole("option", { name: "Client" })).toBeNull();
        await userEvent.selectOptions(select, "org-personal");
        await userEvent.click(screen.getByRole("button", { name: "Share" }));

        expect(putShare).toHaveBeenCalledWith("srv-1", "org-personal", 1, 1536);
        const listed = await screen.findByRole("row", { name: /Personal Workspace/ });
        expect(listed.textContent).toContain("Personal Workspace");
        expect(listed.textContent).toContain("1 vCPU · 1.5 GiB");
    });

    it("shows nothing to non-owners", () => {
        const { container } = renderCard(false);
        expect(container.textContent).toBe("");
    });
});
