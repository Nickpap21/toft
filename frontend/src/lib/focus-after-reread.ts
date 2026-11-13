/** After "Read them again": the button sits in the banner the new read
 *  removes, so focus would fall to the top of the page. Move it to Save, or
 *  to the heading when Save can't be pressed. Call it after the state
 *  updates: a frame later the new read is on screen. */
export function focusAfterReread(saveId: string, headingId: string) {
  requestAnimationFrame(() => {
    const save = document.getElementById(saveId) as HTMLButtonElement | null;
    (save && !save.disabled ? save : document.getElementById(headingId))?.focus();
  });
}
