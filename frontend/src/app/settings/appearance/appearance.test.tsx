import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ThemeProvider } from "@/components/theme-provider";
import { LEGACY_KEY, STORAGE_KEY } from "@/lib/theme/themes";
import { setSystemDark } from "@/test/setup";
import AppearancePage from "./page";

vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ user: { id: "u-1", email: "a@example.test", name: "admin", orgName: "Founders" } }),
  useIsOperator: () => false,
}));
vi.mock("next/navigation", () => ({ usePathname: () => "/settings/appearance" }));

function renderPage() {
  return render(
    <ThemeProvider>
      <AppearancePage />
    </ThemeProvider>,
  );
}

describe("Settings → Appearance", () => {
  it("offers eight themes as radios in a labelled group", () => {
    renderPage();
    const group = screen.getByRole("group", { name: "Theme" });
    const radios = within(group).getAllByRole("radio");
    expect(radios).toHaveLength(8);
    expect(radios.map((r) => r.getAttribute("value"))).toEqual([
      "system:graphite-paper",
      "system:mono-daylight",
      "graphite",
      "paper",
      "mono",
      "daylight",
      "ink",
      "contrast",
    ]);
  });

  it("checks the Graphite and Paper pair by default", () => {
    renderPage();
    expect(screen.getByRole("radio", { name: /Graphite and Paper/ })).toBeChecked();
  });

  it("applies a choice at once and announces it", async () => {
    setSystemDark(true);
    renderPage();
    await userEvent.click(screen.getByRole("radio", { name: /^Contrast/ }));
    expect(localStorage.getItem(STORAGE_KEY)).toBe("contrast");
    expect(localStorage.getItem(LEGACY_KEY)).toBeNull();
    expect(document.documentElement.dataset.theme).toBe("contrast");
    expect(screen.getByRole("status")).toHaveTextContent("Theme set to Contrast.");
  });

  it("moves with the arrow keys like any radio group", async () => {
    renderPage();
    const first = screen.getByRole("radio", { name: /Graphite and Paper/ });
    first.focus();
    await userEvent.keyboard("{ArrowDown}");
    expect(screen.getByRole("radio", { name: /Mono and Daylight/ })).toBeChecked();
    expect(localStorage.getItem(STORAGE_KEY)).toBe("system:mono-daylight");
  });

  it("previews each theme inside its own data-theme", () => {
    const { container } = renderPage();
    const themes = [...container.querySelectorAll("[data-theme]")].map((el) => el.getAttribute("data-theme"));
    expect(new Set(themes)).toEqual(new Set(["graphite", "paper", "mono", "daylight", "ink", "contrast"]));
  });
});
