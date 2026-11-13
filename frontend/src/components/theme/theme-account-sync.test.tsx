import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ThemeProvider, useTheme } from "@/components/theme-provider";
import { STORAGE_KEY, type ThemePref } from "@/lib/theme/themes";
import { ThemeAccountSync } from "./theme-account-sync";

vi.mock("@/context/AuthContext", () => ({ useAuth: () => ({ token: "t" }) }));
vi.mock("@/lib/api/client", () => ({ isTokenExpired: () => false }));
let saved: ThemePref | null = null;
const setTheme = vi.fn<(p: ThemePref) => Promise<void>>(async () => {});
vi.mock("@/lib/api/user.service", () => ({
  userService: { getMe: async () => ({ theme: saved }), setTheme: (p: ThemePref) => setTheme(p) },
}));

function Picker() {
  const { setPref } = useTheme();
  return <button onClick={() => setPref("mono")}>Use Mono</button>;
}

function renderSync() {
  return render(
    <ThemeProvider>
      <QueryClientProvider client={new QueryClient()}>
        <ThemeAccountSync />
        <Picker />
      </QueryClientProvider>
    </ThemeProvider>,
  );
}

describe("ThemeAccountSync", () => {
  beforeEach(() => {
    localStorage.clear();
    setTheme.mockClear();
  });

  it("applies the account's theme over this browser's", async () => {
    localStorage.setItem(STORAGE_KEY, "paper");
    saved = "contrast";
    renderSync();
    await waitFor(() => expect(localStorage.getItem(STORAGE_KEY)).toBe("contrast"));
    expect(setTheme).not.toHaveBeenCalled();
  });

  it("uploads this browser's theme when the account has none", async () => {
    localStorage.setItem(STORAGE_KEY, "ink");
    saved = null;
    renderSync();
    await waitFor(() => expect(setTheme).toHaveBeenCalledWith("ink"));
  });

  it("saves a later change", async () => {
    localStorage.setItem(STORAGE_KEY, "graphite");
    saved = "graphite";
    renderSync();
    await waitFor(() => expect(localStorage.getItem(STORAGE_KEY)).toBe("graphite"));
    await new Promise((r) => setTimeout(r, 0));
    await userEvent.click(screen.getByRole("button", { name: "Use Mono" }));
    await waitFor(() => expect(setTheme).toHaveBeenCalledWith("mono"));
    expect(setTheme).toHaveBeenCalledTimes(1);
  });
});
