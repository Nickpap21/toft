import { useState } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";

/** A dialog a link can open: true while ?<name> is in the URL (⌘K's "New
 *  API key" opens /settings/api-keys?new), or after open(true). Closing it
 *  drops the parameter. The page needs a Suspense boundary
 *  (useSearchParams). */
export function useQueryFlag(name: string): [boolean, (open: boolean) => void] {
  const flagged = useSearchParams().has(name);
  const router = useRouter();
  const pathname = usePathname();
  const [open, setOpen] = useState(flagged);
  // A link to the page it is on adds the flag without remounting it.
  const [seen, setSeen] = useState(flagged);
  if (flagged !== seen) {
    setSeen(flagged);
    if (flagged) setOpen(true);
  }
  return [
    open,
    (next: boolean) => {
      setOpen(next);
      if (!next && flagged) router.replace(pathname, { scroll: false });
    },
  ];
}
