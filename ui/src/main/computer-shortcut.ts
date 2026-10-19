export const COMPUTER_EMERGENCY_SHORTCUT = "CommandOrControl+Alt+Shift+Escape";

/** Return actual OS registration so the panel can offer its takeover control. */
export function registerComputerShortcut(shortcuts: { register(key: string, callback: () => void): boolean }, stop: () => void): boolean {
  try { return shortcuts.register(COMPUTER_EMERGENCY_SHORTCUT, stop); }
  catch { return false; }
}
