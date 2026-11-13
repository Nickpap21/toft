// @mentions (D-88): a member's handle is their email's local part, lower
// case, when no other member shares it; as the server reads them.

const HANDLE = /^[a-z0-9][a-z0-9._-]{0,63}$/;

export interface Handle {
  handle: string;
  name: string;
}

export function handles(members: { user_email: string; user_name: string }[]): Handle[] {
  const seen = new Map<string, Handle[]>();
  for (const m of members) {
    const local = m.user_email.toLowerCase().split("@")[0]?.replace(/\.+$/, "") ?? "";
    if (!HANDLE.test(local)) continue;
    seen.set(local, [...(seen.get(local) ?? []), { handle: local, name: m.user_name }]);
  }
  return [...seen.values()].filter((l) => l.length === 1).map((l) => l[0]);
}

/** The @word being typed before the caret, if any. */
export function typing(text: string, caret: number): { start: number; prefix: string } | null {
  const m = /(^|[^\w@.])@([a-z0-9._-]*)$/i.exec(text.slice(0, caret));
  if (!m) return null;
  return { start: caret - m[2].length - 1, prefix: m[2].toLowerCase() };
}
