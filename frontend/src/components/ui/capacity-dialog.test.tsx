import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import { CapacityDialog, capacityWarningFrom, formatMemory, type CapacityWarning } from "./capacity-dialog";

// The initech case: 1.5 GiB share, 1 GiB already allocated, a 512m deploy plus a little over.
const warning: CapacityWarning = {
    code: "capacity_exceeded",
    can_bypass: true,
    effective_cpu_cores: 1,
    effective_memory_mb: 1536,
    allocated_cpu_cores: 0,
    allocated_memory_mb: 1100,
    requested_cpu_cores: 0,
    requested_memory_mb: 512,
};

describe("capacityWarningFrom", () => {
    it("reads the 409 the deploy endpoint sends", () => {
        expect(capacityWarningFrom(new ApiError("Request failed: 409", 409, { data: warning }))).toEqual(warning);
    });

    it("ignores other errors", () => {
        expect(capacityWarningFrom(new ApiError("a deployment is already running", 409, { error: "busy" }))).toBeNull();
        expect(capacityWarningFrom(new ApiError("nope", 403, { data: warning }))).toBeNull();
        expect(capacityWarningFrom(new Error("network"))).toBeNull();
    });
});

describe("CapacityDialog", () => {
    it("says what is short and lets an allowed role deploy anyway", async () => {
        const onDeployAnyway = vi.fn();
        const onCancel = vi.fn();
        render(<CapacityDialog warning={warning} serviceName="initech-migrate" onCancel={onCancel} onDeployAnyway={onDeployAnyway} />);

        expect(screen.getByRole("dialog", { name: "Not enough room to deploy initech-migrate" })).toHaveAccessibleDescription(
            /needs more memory than this workspace has left on its server\. Nothing was deployed\./,
        );
        expect(screen.getByText("needs 512 MiB, 436 MiB left of 1.5 GiB")).toBeTruthy();

        await userEvent.click(screen.getByRole("button", { name: "Deploy anyway" }));
        expect(onDeployAnyway).toHaveBeenCalledTimes(1);
        await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
        expect(onCancel).toHaveBeenCalled();
    });

    it("offers no bypass to a role that may not", () => {
        render(<CapacityDialog warning={{ ...warning, can_bypass: false }} onCancel={vi.fn()} onDeployAnyway={vi.fn()} />);
        expect(screen.queryByRole("button", { name: "Deploy anyway" })).toBeNull();
        expect(screen.getByText(/ask a workspace owner to deploy it/)).toBeTruthy();
    });

    // New service: the service doesn't exist yet, so nothing points at its settings.
    it("reads as a create from New service", async () => {
        const onDeployAnyway = vi.fn();
        render(<CapacityDialog warning={warning} serviceName="probe" create onCancel={vi.fn()} onDeployAnyway={onDeployAnyway} />);
        expect(screen.getByRole("dialog", { name: "Not enough room to deploy probe" })).toHaveAccessibleDescription(
            "This service needs more memory than this workspace has left on its server. Nothing was created.",
        );
        expect(
            screen.getByText("Lower its memory above or pick another server, or create and deploy it anyway: the server may then run short for every service on it."),
        ).toBeTruthy();
        await userEvent.click(screen.getByRole("button", { name: "Create and deploy anyway" }));
        expect(onDeployAnyway).toHaveBeenCalledTimes(1);
    });

    it("tells a role that may not bypass who can create it", () => {
        render(<CapacityDialog warning={{ ...warning, can_bypass: false }} create onCancel={vi.fn()} onDeployAnyway={vi.fn()} />);
        expect(screen.getByText("Lower its memory above or pick another server, or ask a workspace owner to create it.")).toBeTruthy();
        expect(screen.queryByText(/settings/)).toBeNull();
    });

    it("writes MiB under 1 GiB and GiB with one decimal above", () => {
        expect(formatMemory(436)).toBe("436 MiB");
        expect(formatMemory(1536)).toBe("1.5 GiB");
    });
});
