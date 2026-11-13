import { useEffect } from "react";

/** The root layout's title (src/app/layout.tsx), which pages without their
 *  own keep. */
const APP_TITLE = "Toft";

/** Sets the browser tab's title while the page is open (WCAG 2.4.2), and
 *  puts the app's back after. Next writes the root layout's <title> into the
 *  head after hydration, over a title set earlier, so this re-applies it
 *  whenever the head changes. */
export function useDocumentTitle(title: string) {
  useEffect(() => {
    const apply = () => {
      if (document.title !== title) document.title = title;
    };
    apply();
    const watch = new MutationObserver(apply);
    watch.observe(document.head, { subtree: true, childList: true, characterData: true });
    return () => {
      watch.disconnect();
      document.title = APP_TITLE;
    };
  }, [title]);
}
