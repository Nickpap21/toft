// The project pages' shared classes, as the project boards draw them
// (Projects, Project, ProjectSettings, NewService, SharedServices).

/** A card. A Read directly in it, loading or failed, sits clear of its edge. */
export const card =
  "overflow-hidden rounded-[10px] border border-subtle bg-surface [&>[data-read-state=failed]]:px-4 [&>[data-read-state=failed]]:py-3 [&>[data-read-state=loading]]:m-4";
export const h2 = "text-[15px] font-semibold leading-[22px] text-fg";
export const mono = "font-mono text-xs";
/** A page's own header, full width under the shell's padding. */
export const pageHeader =
  "-mx-4 -mt-4 mb-6 flex flex-wrap items-center justify-between gap-3 border-b border-subtle px-4 py-4 md:-mx-8 md:-mt-8 md:px-8";
export const h1 = "text-xl font-semibold leading-7 tracking-[-0.01em] text-fg";
export const primary =
  "touch-target inline-flex h-9 items-center gap-1.5 rounded-lg bg-accent px-3.5 text-[13px] font-medium text-on-accent hover:bg-accent/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-50";
export const outline =
  "touch-target inline-flex h-9 items-center gap-1.5 rounded-lg border border-subtle bg-raised px-3 text-[13px] font-medium text-fg hover:bg-raised/70 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-50";
export const small =
  "touch-target inline-flex h-8 items-center gap-1.5 rounded-lg border border-subtle bg-raised px-3 text-[13px] font-medium text-fg hover:bg-raised/70 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-50";
export const iconButton =
  "touch-target pointer-coarse:min-w-11 inline-flex h-8 w-8 items-center justify-center rounded-lg border border-subtle bg-raised text-muted hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus";
export const field =
  "touch-target h-9 w-full rounded-lg border border-strong bg-ground px-2.5 text-sm text-fg placeholder:text-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-60";
export const label = "text-[13px] font-medium text-fg";
export const th = "px-4 py-2.5 text-left text-xs font-medium text-muted";
export const td = "border-t border-subtle px-4 py-3 align-top";
/** The board's .sum and .help: a card head's summary, a field's help. */
export const sum = "text-xs text-muted";
export const help = "text-xs leading-[18px] text-muted";
const focus = "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus";
/** The board's .btn.sm, .btn.sm.ghost and .btn.sm.pri: card heads and save bars. */
export const btnSm = `touch-target inline-flex h-[30px] items-center gap-1.5 whitespace-nowrap rounded-md border border-strong bg-raised px-2.5 text-xs font-medium text-fg hover:bg-raised/70 disabled:opacity-50 ${focus}`;
export const ghostSm = `touch-target inline-flex h-[30px] items-center gap-1.5 whitespace-nowrap rounded-md px-2.5 text-xs font-medium text-fg/80 hover:bg-raised hover:text-fg disabled:opacity-50 ${focus}`;
export const primarySm = `touch-target inline-flex h-[30px] items-center whitespace-nowrap rounded-md bg-accent px-2.5 text-xs font-medium text-on-accent hover:bg-accent/90 disabled:opacity-50 ${focus}`;
/** A card's save bar (the board's .cf), shown only while something changed. */
export const saveBar = "flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-t border-subtle px-4 py-2.5 text-[13px] text-muted";
