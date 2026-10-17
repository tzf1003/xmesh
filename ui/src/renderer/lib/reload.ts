// The hook the settings page uses to have the kernel pick up a saved config without a restart. The kernel's
// reload call lands separately; until then there is none, and every change that needs one offers a restart.

/**
 * Asks the running kernel to reload its config in place. Undefined while the kernel has no reload call, which
 * leaves the restart offer on the page. When the call exists, return its promise here: it resolves `true` when
 * the kernel took the change, and `false` sends the page back to the restart offer.
 */
export function requestReload(): Promise<boolean> | undefined {
  return undefined;
}
