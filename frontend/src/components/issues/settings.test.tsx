import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import { ProjectIssuesCard } from "./settings";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const settings = vi.fn();
const saveSettings = vi.fn();
const types = [
  { id: "t1", name: "Task", kind: "standard", icon: "task", color: "gray", archived: false },
  { id: "t2", name: "Bug", kind: "standard", icon: "bug", color: "red", archived: false },
  { id: "t3", name: "Feature", kind: "standard", icon: "feature", color: "blue", archived: false },
];
vi.mock("@/lib/api/issue.service", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/issue.service")>()),
  issueService: () => ({ settings, saveSettings }),
  issueTypesQuery: { queryKey: ["issue-types"], queryFn: async () => types },
}));

const read = {
  key: "SHOP",
  aliases: [],
  digits: 3,
  workspace_digits: 3,
  inherit_digits: true,
  default_type_id: null,
  next_key: "SHOP-001",
  hidden_type_ids: ["t1"],
  failed_deploy_issues: false,
  version: "v1",
};

function renderCard() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <ProjectIssuesCard projectId="p1" isOwner />
    </QueryClientProvider>,
  );
  return client;
}

const changed = () => new ApiError("changed", 409, { code: "changed_since_read" });

describe("ProjectIssuesCard", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    settings.mockResolvedValue(read);
    saveSettings.mockResolvedValue({ ...read, version: "v9" });
  });

  it("says the settings failed to load, with Retry, in place of a skeleton that never ends", async () => {
    settings.mockRejectedValueOnce(new Error("boom"));
    renderCard();
    expect(await screen.findByText("Couldn’t load the issue settings.")).toBeInTheDocument();
    expect(screen.queryByRole("status")).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: /^Retry/ }));
    expect(await screen.findByLabelText("Digits")).toBeInTheDocument();
  });

  it("sends the hidden types and the version they were read with only when they changed", async () => {
    renderCard();
    await userEvent.click(await screen.findByLabelText("Digits"));
    await userEvent.selectOptions(screen.getByLabelText("Digits"), "4");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(saveSettings).toHaveBeenCalledTimes(1);
    expect(saveSettings.mock.calls[0][0]).not.toHaveProperty("hidden_type_ids");

    // The first save answered with the new version: a second needs no re-read.
    saveSettings.mockClear();
    await userEvent.click(await screen.findByLabelText("Bug"));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(saveSettings.mock.calls[0][0]).toMatchObject({ hidden_type_ids: ["t1", "t2"], version: "v9" });
  });

  it("says so when the types changed since they were read, keeps what was chosen, and saves on the fresh read", async () => {
    saveSettings.mockRejectedValueOnce(changed());
    renderCard();

    await userEvent.click(await screen.findByLabelText("Bug"));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(screen.getByText(/changed since you opened this page/)).toBeTruthy();
    expect((screen.getByLabelText("Bug") as HTMLInputElement).checked).toBe(false);
    expect((screen.getByRole("button", { name: "Save" }) as HTMLButtonElement).disabled).toBe(true);

    // Another owner hid Feature meanwhile: it comes in, Bug stays hidden as chosen.
    settings.mockResolvedValue({ ...read, hidden_type_ids: ["t1", "t3"], version: "v2" });
    await userEvent.click(screen.getByRole("button", { name: "Read them again" }));
    await vi.waitFor(() => expect((screen.getByLabelText("Feature") as HTMLInputElement).checked).toBe(false));
    expect((screen.getByLabelText("Bug") as HTMLInputElement).checked).toBe(false);
    await vi.waitFor(() => expect(document.activeElement).toBe(screen.getByRole("button", { name: "Save" })));

    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(saveSettings).toHaveBeenLastCalledWith(expect.objectContaining({
      hidden_type_ids: expect.arrayContaining(["t1", "t2", "t3"]),
      version: "v2",
    }));
  });

  const refused = async () => {
    saveSettings.mockRejectedValueOnce(changed());
    await userEvent.click(await screen.findByLabelText("Bug"));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByRole("alert")).toBeTruthy();
  };
  const digits = () => (screen.getByLabelText("Digits") as HTMLSelectElement).value;

  it("takes another owner's changes to what wasn't touched here, and doesn't send them back", async () => {
    renderCard();
    await refused();

    settings.mockResolvedValue({
      ...read,
      key: "SHOPX",
      digits: 5,
      inherit_digits: false,
      default_type_id: "t3",
      failed_deploy_issues: true,
      version: "v2",
    });
    await userEvent.click(screen.getByRole("button", { name: "Read them again" }));
    await vi.waitFor(() => expect(digits()).toBe("5"));
    expect((screen.getByLabelText("Key") as HTMLInputElement).value).toBe("SHOPX");
    await vi.waitFor(() => expect(screen.getByText("Unsaved change to Types")).toBeTruthy());

    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    const sent = saveSettings.mock.lastCall![0];
    expect(sent).not.toHaveProperty("key");
    expect(sent).toMatchObject({
      digits: 5,
      default_type_id: "t3",
      failed_deploy_issues: true,
      hidden_type_ids: ["t1", "t2"],
      version: "v2",
    });
  });

  it("keeps a field changed here over another owner's change to it", async () => {
    renderCard();
    await userEvent.selectOptions(await screen.findByLabelText("Digits"), "4");
    await refused();

    settings.mockResolvedValue({ ...read, digits: 5, inherit_digits: false, version: "v2" });
    await userEvent.click(screen.getByRole("button", { name: "Read them again" }));
    await vi.waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
    expect(digits()).toBe("4");
  });

  it("follows the data again after Cancel", async () => {
    const client = renderCard();
    await refused();
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));

    settings.mockResolvedValue({ ...read, digits: 5, inherit_digits: false, version: "v2" });
    await act(async () => {
      await client.refetchQueries();
    });
    await vi.waitFor(() => expect(digits()).toBe("5"));
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
  });
});
