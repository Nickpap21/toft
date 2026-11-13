import { useEffect } from "react";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { reauthProof, saveReauth } from "@/lib/api/client";
import { ReauthProvider, useReauth } from "./reauth";

const token = `x.${btoa(JSON.stringify({ exp: Math.floor(Date.now() / 1000) + 3600 }))}.y`;

/** POST /auth/reauth: right for "secret", 403 wrong_password otherwise. */
function stubReauth() {
  const fetch = vi.fn<typeof globalThis.fetch>(async (_url, init) => {
    const { password } = JSON.parse(String(init?.body)) as { password: string };
    return password === "secret"
      ? new Response(JSON.stringify({ data: { token: "proof", expires_at: "", expires_in: 600 } }), { status: 200 })
      : new Response(JSON.stringify({ error: "your password is incorrect", data: { code: "wrong_password" } }), { status: 403 });
  });
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

/** The provider's confirm function, as a component would call it. */
const grabbed: { confirm: ReturnType<typeof useReauth> } = { confirm: async () => false };
const confirm = () => grabbed.confirm();
function Grab() {
  const c = useReauth();
  useEffect(() => {
    grabbed.confirm = c;
  }, [c]);
  return null;
}

function renderProvider() {
  localStorage.setItem("paas_token", token);
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <ReauthProvider>
        <Grab />
      </ReauthProvider>
    </QueryClientProvider>,
  );
}

afterEach(() => vi.unstubAllGlobals());

describe("ReauthProvider (D-53)", () => {
  it("answers at once while a confirmation lasts", async () => {
    saveReauth("proof", 600);
    const fetch = stubReauth();
    renderProvider();
    await expect(confirm()).resolves.toBe(true);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(fetch).not.toHaveBeenCalled();
  });

  it("asks once for two callers, says a wrong password, and keeps the proof", async () => {
    stubReauth();
    renderProvider();
    let first!: Promise<boolean>, second!: Promise<boolean>;
    act(() => {
      first = confirm();
      second = confirm();
    });
    const dialog = await screen.findByRole("dialog", { name: "Confirm your password" });
    const field = screen.getByLabelText("Your password");
    expect(field).toHaveAttribute("autocomplete", "current-password");

    await userEvent.type(field, "nope{Enter}");
    expect(await screen.findByRole("alert")).toHaveTextContent("Your password is incorrect.");
    expect(dialog).toBeInTheDocument();

    await userEvent.clear(field);
    await userEvent.type(field, "secret{Enter}");
    await expect(first).resolves.toBe(true);
    await expect(second).resolves.toBe(true);
    expect(reauthProof()).toBe("proof");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("resolves false when cancelled", async () => {
    stubReauth();
    renderProvider();
    let asked!: Promise<boolean>;
    act(() => {
      asked = confirm();
    });
    await userEvent.click(await screen.findByRole("button", { name: "Cancel" }));
    await expect(asked).resolves.toBe(false);
    expect(reauthProof()).toBeNull();
  });

  // A prompt that opens with its page has no button to go back to (D-164).
  it("puts focus where it was told to when nothing had focus at open", async () => {
    stubReauth();
    renderProvider();
    const heading = document.body.appendChild(Object.assign(document.createElement("h2"), { tabIndex: -1 }));
    let asked!: Promise<boolean>;
    act(() => {
      asked = grabbed.confirm(undefined, { returnFocus: { current: heading } });
    });
    await userEvent.click(await screen.findByRole("button", { name: "Cancel" }));
    await expect(asked).resolves.toBe(false);
    await waitFor(() => expect(heading).toHaveFocus());
    heading.remove();
  });
});
