import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useRef, useState } from "react";
import { describe, expect, it } from "vitest";
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogTitle } from "./dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "./dropdown-menu";

// Controlled, with no DialogTrigger: how the app's dialogs are written.
function Controlled({ onCloseAutoFocus, removeOpener = false }: { onCloseAutoFocus?: (e: Event) => void; removeOpener?: boolean }) {
  const [open, setOpen] = useState(false);
  const [opener, setOpener] = useState(true);
  return (
    <>
      <button type="button">Before</button>
      {opener && (
        <button type="button" onClick={() => setOpen(true)}>
          Invite member
        </button>
      )}
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent onCloseAutoFocus={onCloseAutoFocus}>
          <DialogTitle>Invite</DialogTitle>
          <DialogDescription>Invite someone.</DialogDescription>
          {/* autoFocus takes focus before Radix does: the opener must be remembered first. */}
          <input aria-label="Email" autoFocus />
          {removeOpener && (
            <button type="button" onClick={() => setOpener(false)}>
              Remove opener
            </button>
          )}
          <DialogClose>Cancel</DialogClose>
        </DialogContent>
      </Dialog>
    </>
  );
}

describe("DialogContent focus return", () => {
  it("returns focus to the button that opened it on Escape", async () => {
    const user = userEvent.setup();
    render(<Controlled />);
    const opener = screen.getByRole("button", { name: "Invite member" });
    opener.focus();
    await user.keyboard("{Enter}");
    await waitFor(() => expect(screen.getByLabelText("Email")).toHaveFocus());
    await user.keyboard("{Escape}");
    await waitFor(() => expect(opener).toHaveFocus());
  });

  it("returns focus to the opener on Cancel", async () => {
    const user = userEvent.setup();
    render(<Controlled />);
    const opener = screen.getByRole("button", { name: "Invite member" });
    await user.click(opener);
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(opener).toHaveFocus());
  });

  it("leaves focus to a caller's onCloseAutoFocus that prevents default", async () => {
    const user = userEvent.setup();
    render(
      <Controlled
        onCloseAutoFocus={(e) => {
          e.preventDefault();
          screen.getByRole("button", { name: "Before" }).focus();
        }}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Invite member" }));
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.getByRole("button", { name: "Before" })).toHaveFocus());
  });

  // Opened from a mutation's onError: its button was disabled while pending,
  // and is enabled again by the time the dialog closes.
  function FromError({ returnFocus = false }: { returnFocus?: boolean }) {
    const [open, setOpen] = useState(false);
    const [pending, setPending] = useState(false);
    const deploy = useRef<HTMLButtonElement>(null);
    return (
      <>
        <button ref={deploy} type="button" disabled={pending} onClick={() => setPending(true)}>
          Deploy
        </button>
        <button type="button" onClick={() => setOpen(true)}>Fail</button>
        <button type="button" onClick={() => setPending(false)}>Settle</button>
        <Dialog open={open} onOpenChange={setOpen}>
          <DialogContent returnFocus={returnFocus ? deploy : undefined}>
            <DialogTitle>Not enough room</DialogTitle>
            <DialogDescription>Refused.</DialogDescription>
            <DialogClose>Cancel</DialogClose>
          </DialogContent>
        </Dialog>
      </>
    );
  }

  it("returns focus to an opener disabled at open once it is enabled again", async () => {
    const user = userEvent.setup();
    render(<FromError />);
    const deploy = screen.getByRole("button", { name: "Deploy" });
    await user.click(deploy);
    deploy.focus(); // a browser that keeps focus on it while disabled
    fireEvent.click(screen.getByRole("button", { name: "Fail" }));
    await screen.findByRole("dialog");
    fireEvent.click(screen.getByRole("button", { name: "Settle", hidden: true }));
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(deploy).toHaveFocus());
  });

  it("returns focus to returnFocus when nothing had focus at open", async () => {
    const user = userEvent.setup();
    render(<FromError returnFocus />);
    const deploy = screen.getByRole("button", { name: "Deploy" });
    await user.click(deploy);
    // A browser that drops focus from a disabled button to <body> (jsdom won't blur it).
    const elsewhere = document.body.appendChild(document.createElement("input"));
    elsewhere.focus();
    elsewhere.remove();
    expect(document.activeElement).toBe(document.body);
    fireEvent.click(screen.getByRole("button", { name: "Fail" }));
    await screen.findByRole("dialog");
    fireEvent.click(screen.getByRole("button", { name: "Settle", hidden: true }));
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(deploy).toHaveFocus());
  });

  // Opened from a menu: the item that had focus goes with its menu.
  function FromMenu({ returnFocus = false, removeTrigger = false }: { returnFocus?: boolean; removeTrigger?: boolean }) {
    const [open, setOpen] = useState(false);
    const [trigger, setTrigger] = useState(true);
    const header = useRef<HTMLButtonElement>(null);
    return (
      <>
        <button ref={header} type="button">Header</button>
        {trigger && (
          <DropdownMenu>
            <DropdownMenuTrigger>More actions</DropdownMenuTrigger>
            <DropdownMenuContent>
              <DropdownMenuItem onSelect={() => setOpen(true)}>Stop web…</DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
        <Dialog open={open} onOpenChange={setOpen}>
          <DialogContent returnFocus={returnFocus ? header : undefined}>
            <DialogTitle>Stop web?</DialogTitle>
            <DialogDescription>It stops.</DialogDescription>
            {removeTrigger && <button type="button" onClick={() => setTrigger(false)}>Remove trigger</button>}
            <DialogClose>Cancel</DialogClose>
          </DialogContent>
        </Dialog>
      </>
    );
  }

  async function openFromMenu(user: ReturnType<typeof userEvent.setup>) {
    screen.getByRole("button", { name: "More actions" }).focus();
    await user.keyboard("{Enter}");
    await screen.findByRole("menuitem", { name: "Stop web…" });
    await user.keyboard("{Enter}");
    await screen.findByRole("dialog");
  }

  it("returns focus to the menu's trigger when a menu item opened it", async () => {
    const user = userEvent.setup();
    render(<FromMenu />);
    const trigger = screen.getByRole("button", { name: "More actions" });
    await openFromMenu(user);
    await waitFor(() => expect(screen.getByRole("button", { name: "Cancel" })).toHaveFocus());
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await waitFor(() => expect(trigger).toHaveFocus());
  });

  it("falls back to returnFocus when the menu's trigger is gone too", async () => {
    const user = userEvent.setup();
    render(<FromMenu returnFocus removeTrigger />);
    await openFromMenu(user);
    await user.click(screen.getByRole("button", { name: "Remove trigger" }));
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Header" })).toHaveFocus());
  });

  it("does not throw when the opener is gone by the time it closes", async () => {
    const user = userEvent.setup();
    render(<Controlled removeOpener />);
    await user.click(screen.getByRole("button", { name: "Invite member" }));
    await user.click(screen.getByRole("button", { name: "Remove opener" }));
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.queryByRole("button", { name: "Invite member" })).not.toBeInTheDocument();
  });
});
