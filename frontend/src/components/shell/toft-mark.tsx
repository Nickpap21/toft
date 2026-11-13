/** The logo's geometry, from `docs/brand/`. The mark is the small-size cut of
 *  the ft ligature (the f and t under one roof); the wordmark is cropped to
 *  its letters. */
export const TOFT_MARK_PATH =
  "M52 224V158H24V124H52V96A64 64 0 0 1 116 32H124A64 64 0 0 1 188 96V124H228V158H188V168A16 16 0 0 0 204 184H228V224H204A56 56 0 0 1 148 168V158H92V224ZM92 124V96A24 24 0 0 1 116 72H124A24 24 0 0 1 148 96V124Z";
const WORDMARK_PATH =
  "M32 56H60V96H96V122H60V152A20 20 0 0 0 80 172H96V200H80A48 48 0 0 1 32 152V122H8V96H32ZM110 148A54 54 0 1 1 218 148A54 54 0 1 1 110 148ZM138 148A26 26 0 1 1 190 148A26 26 0 1 1 138 148ZM254 200V122H230V96H254V88A48 48 0 0 1 302 40H322A48 48 0 0 1 370 88V96H406V122H370V152A20 20 0 0 0 390 172H406V200H390A48 48 0 0 1 342 152V122H282V200ZM282 96V88A20 20 0 0 1 302 68H322A20 20 0 0 1 342 88V96Z";

/** The Toft mark, in the current text colour. Decorative; pair it with the
 *  name in text. */
export function ToftMark({ className = "h-8 w-8" }: { className?: string }) {
  return (
    <svg viewBox="0 0 256 256" aria-hidden="true" className={className}>
      <path d={TOFT_MARK_PATH} fill="currentColor" fillRule="evenodd" />
    </svg>
  );
}

/** The Toft logo: the drawn wordmark, in the current text colour. Never set
 *  "toft" in a font instead. Width follows height (398 × 162). */
export function ToftWordmark({ className = "h-5 w-auto" }: { className?: string }) {
  return (
    <svg viewBox="8 40 398 162" role="img" aria-label="Toft" className={className}>
      <path d={WORDMARK_PATH} fill="currentColor" fillRule="evenodd" />
    </svg>
  );
}
