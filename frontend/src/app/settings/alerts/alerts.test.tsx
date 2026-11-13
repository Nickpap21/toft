import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import AlertsSettingsPage from "./page";

let role = "owner";
vi.mock("@/context/AuthContext", () => ({
    useAuth: () => ({ isLoading: false, user: { id: "u-1", email: "a@example.test", orgName: "Founders" } }),
    useOrgRole: () => role,
    useIsOperator: () => false,
}));
vi.mock("next/navigation", () => ({ usePathname: () => "/settings/alerts" }));
vi.mock("@/lib/api/org.service", () => ({
    orgService: {
        listMembers: async () => [{ user_id: "u-2", user_name: "admin", user_email: "admin@example.test", role: "owner" }],
        listAuditLog: async () => [],
    },
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const put = vi.fn();
let saved: unknown = null;
vi.mock("@/lib/api/alert.service", () => ({
    alertService: {
        get: async () => saved,
        put: (...args: unknown[]) => put(...args),
        rotateSecret: vi.fn(),
        test: vi.fn(),
        delete: vi.fn(),
        recent: async () => [],
    },
}));

function renderPage() {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return render(
        <QueryClientProvider client={client}>
            <AlertsSettingsPage />
        </QueryClientProvider>,
    );
}

describe("Settings → Alerts", () => {
    beforeEach(() => {
        role = "owner";
        saved = null;
        put.mockReset();
    });

    it("is for owners only", async () => {
        role = "developer";
        renderPage();
        expect(await screen.findByText(/Only workspace owners manage alerts\. Ask an owner of Founders \(admin\)/)).toBeTruthy();
        expect(screen.queryByLabelText("Webhook URL")).toBeNull();
    });

    it("creates a webhook and shows its signing secret once", async () => {
        put.mockResolvedValue({ webhook_url: "https://hooks.example.com/x", enabled: true, updated_at: "", signing_secret: "s3cr3t" });
        renderPage();

        const field = await screen.findByLabelText("Webhook URL");
        expect(screen.queryByRole("button", { name: "Send test" })).toBeNull();
        await userEvent.type(field, "https://hooks.example.com/x");
        await userEvent.click(screen.getByRole("button", { name: "Create webhook" }));

        expect(put).toHaveBeenCalledWith("https://hooks.example.com/x", true);
        expect(((await screen.findByLabelText("Signing secret")) as HTMLTextAreaElement).value).toBe("s3cr3t");
        expect(screen.getByRole("heading", { name: "Copy the signing secret" })).toBeTruthy();
    });

    it("loads a saved webhook without its secret", async () => {
        saved = { webhook_url: "https://hooks.example.com/saved", enabled: false, updated_at: "", last_delivery_at: null, last_delivery_status: null, last_delivery_error: "" };
        renderPage();
        expect(((await screen.findByDisplayValue("https://hooks.example.com/saved")) as HTMLInputElement).value).toBeTruthy();
        expect((screen.getByRole("checkbox", { name: /^Send alerts/ }) as HTMLInputElement).checked).toBe(false);
        expect(screen.queryByLabelText("Signing secret")).toBeNull();
        expect(screen.getByText("Paused")).toBeTruthy();
        expect(screen.getByRole("button", { name: "Send test" })).toBeTruthy();
    });

    it("says alerts are off without a webhook", async () => {
        renderPage();
        expect(await screen.findByText("Off")).toBeTruthy();
        expect(screen.getByText("No webhook: nothing is sent")).toBeTruthy();
        expect(screen.getByText(/Nothing is sent: Founders has no webhook/)).toBeTruthy();
    });
});
