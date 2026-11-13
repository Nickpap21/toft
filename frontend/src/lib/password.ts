/** The API takes at most 72 bytes of a password (bcrypt's limit) and answers
 *  anything longer with 400 password_too_long. */
export const PASSWORD_TOO_LONG = "Too long: at most 72 characters, fewer with accents, non-Latin letters or emoji.";

/** A new password's minimum (7g). Sign-in checks no length: a shorter one
 *  set before still works. */
export const PASSWORD_MIN = 12;
export const PASSWORD_MIN_HELP = `At least ${PASSWORD_MIN} characters.`;

export const passwordTooLong = (password: string) => new TextEncoder().encode(password).length > 72;
