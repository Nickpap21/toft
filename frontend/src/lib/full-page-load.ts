/**
 * Opens `to` with a full page load. Only for a change of who or where the
 * tab is: the account (signed in as someone else), its workspace (a switch,
 * a new, joined, deleted or lost one) or its rights (its own operator rights
 * revoked). Nothing of the previous one may survive: cached queries, open
 * pages, and the event, log and terminal streams the server scoped to the
 * account and workspace they connected with. A router navigation keeps all
 * three (it re-rendered the open page against the new workspace first, with
 * 403s, and kept its streams). Every other internal navigation uses the
 * router; the lint rule against location.assign stays on for those.
 */
export function fullPageLoad(to: string): void {
  window.location.assign(to);
}
