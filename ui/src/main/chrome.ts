import type { BrowserWindowConstructorOptions } from "electron";

/** Page colours behind the window; they match `--bg` in renderer/styles.css. */
export const CHROME_BG = { light: "#f7f8f7", dark: "#0f1211" } as const;
const SYMBOL = { light: "#161b18", dark: "#e8ece9" } as const;

/** Height of the panel's top bar in CSS pixels; the Windows caption buttons are drawn to this height. */
export const TOPBAR_HEIGHT = 46;

/**
 * The window frame the panel asks for: the page paints its own top bar, so
 * macOS hides the title bar and keeps the traffic lights over it, and Windows
 * keeps the caption buttons but takes the bar's colour from the page. Linux
 * keeps the system frame.
 */
export function windowChrome(platform: NodeJS.Platform, dark: boolean): BrowserWindowConstructorOptions {
  const scheme = dark ? "dark" : "light";
  const base: BrowserWindowConstructorOptions = { backgroundColor: CHROME_BG[scheme] };
  if (platform === "darwin") {
    return { ...base, titleBarStyle: "hiddenInset", trafficLightPosition: { x: 16, y: 15 } };
  }
  if (platform === "win32") {
    return {
      ...base,
      titleBarStyle: "hidden",
      titleBarOverlay: { color: CHROME_BG[scheme], symbolColor: SYMBOL[scheme], height: TOPBAR_HEIGHT },
    };
  }
  return base;
}

/** Overlay colours for `win.setTitleBarOverlay` when the system theme changes (Windows only). */
export function overlayColors(dark: boolean): { color: string; symbolColor: string; height: number } {
  const scheme = dark ? "dark" : "light";
  return { color: CHROME_BG[scheme], symbolColor: SYMBOL[scheme], height: TOPBAR_HEIGHT };
}
