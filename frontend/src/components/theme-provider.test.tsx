import { useEffect } from "react";
import { act, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { LEGACY_KEY, STORAGE_KEY } from "@/lib/theme/themes";
import { setSystemDark } from "@/test/setup";
import { ThemeProvider, useTheme } from "./theme-provider";

let api: ReturnType<typeof useTheme>;
function Probe() {
  const value = useTheme();
  useEffect(() => {
    api = value;
  });
  return <span data-testid="theme">{value.theme ?? "none"}</span>;
}

function renderProvider() {
  render(
    <ThemeProvider>
      <Probe />
    </ThemeProvider>,
  );
  return () => screen.getByTestId("theme").textContent;
}

describe("ThemeProvider", () => {
  it("writes paas_theme, leaves the legacy key alone and updates <html>", () => {
    // OS dark, so "paper" can only come from migrating the legacy choice.
    setSystemDark(true);
    localStorage.setItem(LEGACY_KEY, "light");
    const current = renderProvider();
    expect(current()).toBe("paper");
    act(() => api.setPref("ink"));
    expect(localStorage.getItem(STORAGE_KEY)).toBe("ink");
    expect(localStorage.getItem(LEGACY_KEY)).toBe("light");
    expect(current()).toBe("ink");
    const html = document.documentElement;
    expect(html.dataset.theme).toBe("ink");
    expect(html).toHaveClass("dark");
    expect(html.style.colorScheme).toBe("dark");
  });

  it("follows a change made in another tab", () => {
    const current = renderProvider();
    act(() => {
      localStorage.setItem(STORAGE_KEY, "daylight");
      window.dispatchEvent(new StorageEvent("storage", { key: STORAGE_KEY, newValue: "daylight" }));
    });
    expect(current()).toBe("daylight");
    expect(document.documentElement.dataset.theme).toBe("daylight");
  });

  it("flips a system pair when the OS changes, but not a fixed theme", () => {
    const current = renderProvider();
    expect(current()).toBe("paper");
    act(() => setSystemDark(true));
    expect(current()).toBe("graphite");
    act(() => api.setPref("paper"));
    act(() => setSystemDark(false));
    act(() => setSystemDark(true));
    expect(current()).toBe("paper");
  });

  it("toggleMode crosses to the other side as a fixed choice", () => {
    setSystemDark(true);
    const current = renderProvider();
    expect(current()).toBe("graphite");
    act(() => api.toggleMode());
    expect(current()).toBe("paper");
    expect(localStorage.getItem(STORAGE_KEY)).toBe("paper");
  });
});
