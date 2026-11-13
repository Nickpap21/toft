import { THEME_SCRIPT } from "@/lib/theme/script";

/**
 * Applies the saved theme before the first paint. A plain inline <script>
 * on purpose: next/script can defer even beforeInteractive inline scripts in
 * the App Router, which would bring back the flash of the wrong theme.
 */
export function ThemeScript({ nonce }: { nonce?: string }) {
  // The nonce is the proxy's (x-nonce), so the Content-Security-Policy lets
  // this one inline script run and refuses every other.
  return <script nonce={nonce} dangerouslySetInnerHTML={{ __html: THEME_SCRIPT }} />;
}
