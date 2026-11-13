import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import { SharedMounts } from "./shared-mounts";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
vi.mock("next/link", () => ({ default: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a> }));

const setServiceMounts = vi.fn();
const serviceMounts = vi.fn();
vi.mock("@/lib/api/sharedvolume.service", () => ({
    sharedVolumeService: {
        serviceMounts: (...args: unknown[]) => serviceMounts(...args),
        list: async () => [
            { id: "vol-images", server_id: "srv-1", name: "images", mounts: [] },
            { id: "vol-other", server_id: "srv-2", name: "elsewhere", mounts: [] },
        ],
        setServiceMounts: (...args: unknown[]) => setServiceMounts(...args),
    },
}));

function renderMounts(canEdit: boolean) {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return render(
        <QueryClientProvider client={client}>
            <SharedMounts serviceId="svc-1" serverId="srv-1" canEdit={canEdit} />
        </QueryClientProvider>,
    );
}

const images = { shared_volume_id: "vol-images", name: "images", target: "/app/images", read_only: false };
const changed = () => new ApiError("changed", 409, { code: "changed_since_read" });

describe("SharedMounts", () => {
    beforeEach(() => {
        vi.clearAllMocks();
        serviceMounts.mockResolvedValue({ data: [], version: "v0" });
    });

    it("offers only the volumes on the service's server and saves the mount", async () => {
        setServiceMounts.mockResolvedValue({ data: [{ shared_volume_id: "vol-images", name: "images", target: "/app/images", read_only: true }], version: "v1" });
        renderMounts(true);

        await userEvent.click(await screen.findByRole("button", { name: "+ Mount a shared volume" }));
        const select = screen.getByLabelText("Shared volume");
        expect(within(select).queryByRole("option", { name: "elsewhere" })).toBeNull();
        await userEvent.selectOptions(select, "vol-images");
        await userEvent.type(screen.getByLabelText("Container path"), "/app/images");
        await userEvent.click(screen.getByLabelText("read-only"));
        await userEvent.click(screen.getByRole("button", { name: "Save shared volumes" }));

        expect(setServiceMounts).toHaveBeenCalledWith("svc-1", "v0", [{ shared_volume_id: "vol-images", target: "/app/images", read_only: true }]);
    });

    it("shows what failed, with Retry, in place of an empty list when the read failed, and nothing to save from", async () => {
        serviceMounts.mockRejectedValueOnce(new ApiError("boom", 500, {}));
        renderMounts(true);

        expect(await screen.findByRole("button", { name: /^Retry/ })).toBeTruthy();
        expect(screen.queryByRole("button", { name: "+ Mount a shared volume" })).toBeNull();

        serviceMounts.mockResolvedValue({ data: [images], version: "v1" });
        await userEvent.click(screen.getByRole("button", { name: /^Retry/ }));
        expect(await screen.findByDisplayValue("/app/images")).toBeTruthy();
        expect(screen.getByRole("button", { name: "+ Mount a shared volume" })).toBeTruthy();
    });

    it("says so when the list changed since it was read, keeps what was typed, and saves on the fresh read", async () => {
        serviceMounts.mockResolvedValue({ data: [images], version: "v1" });
        setServiceMounts.mockRejectedValueOnce(changed());
        renderMounts(true);

        await userEvent.click(await screen.findByRole("button", { name: "+ Mount a shared volume" }));
        await userEvent.selectOptions(screen.getAllByLabelText("Shared volume")[1], "vol-images");
        await userEvent.type(screen.getAllByLabelText("Container path")[1], "/typed");
        await userEvent.click(screen.getByRole("button", { name: "Save shared volumes" }));

        expect(await screen.findByRole("alert")).toBeTruthy();
        expect(screen.getByText(/changed since you opened this page/)).toBeTruthy();
        expect(screen.getByDisplayValue("/typed")).toBeTruthy();
        expect((screen.getByRole("button", { name: "Save shared volumes" }) as HTMLButtonElement).disabled).toBe(true);

        // Someone else added /added meanwhile: it comes in, and what was typed stays.
        serviceMounts.mockResolvedValue({ data: [images, { ...images, target: "/added" }], version: "v2" });
        await userEvent.click(screen.getByRole("button", { name: "Read them again" }));
        expect(await screen.findByDisplayValue("/added")).toBeTruthy();
        expect(screen.getByDisplayValue("/typed")).toBeTruthy();
        await vi.waitFor(() => expect(document.activeElement).toBe(screen.getByRole("button", { name: "Save shared volumes" })));

        setServiceMounts.mockResolvedValue({ data: [], version: "v3" });
        await userEvent.click(screen.getByRole("button", { name: "Save shared volumes" }));
        expect(setServiceMounts).toHaveBeenLastCalledWith("svc-1", "v2", expect.arrayContaining([
            expect.objectContaining({ target: "/added" }),
            expect.objectContaining({ target: "/typed" }),
            expect.objectContaining({ target: "/app/images" }),
        ]));
    });

    it("doesn't bring back a mount someone else removed when the list is read again", async () => {
        const at = (target: string) => ({ ...images, target });
        serviceMounts.mockResolvedValue({ data: [at("/a"), at("/b")], version: "v1" });
        setServiceMounts.mockRejectedValueOnce(changed());
        renderMounts(true);

        await userEvent.click(await screen.findByRole("button", { name: "+ Mount a shared volume" }));
        await userEvent.selectOptions(screen.getAllByLabelText("Shared volume")[2], "vol-images");
        await userEvent.type(screen.getAllByLabelText("Container path")[2], "/c");
        await userEvent.click(screen.getByRole("button", { name: "Save shared volumes" }));
        expect(await screen.findByRole("alert")).toBeTruthy();

        // Someone else removed /b meanwhile: it stays gone, /c stays typed.
        serviceMounts.mockResolvedValue({ data: [at("/a")], version: "v2" });
        await userEvent.click(screen.getByRole("button", { name: "Read them again" }));
        await vi.waitFor(() => expect(screen.queryByDisplayValue("/b")).toBeNull());
        expect(screen.getByDisplayValue("/c")).toBeTruthy();

        setServiceMounts.mockResolvedValue({ data: [], version: "v3" });
        await userEvent.click(screen.getByRole("button", { name: "Save shared volumes" }));
        expect(setServiceMounts).toHaveBeenLastCalledWith("svc-1", "v2", [
            at("/a"),
            { shared_volume_id: "vol-images", target: "/c", read_only: false },
        ]);
    });

    it("lets a viewer see but not change anything", async () => {
        renderMounts(false);
        expect(await screen.findByText("Shared volumes")).toBeTruthy();
        expect(screen.queryByRole("button", { name: "+ Mount a shared volume" })).toBeNull();
    });
});
