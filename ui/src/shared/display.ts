// The panel's display preferences: text size and typefaces. Stored per user on this machine, not in the kernel.

export const ZOOM_STEPS = [1, 1.1, 1.25, 1.5] as const;

export interface DisplayPrefs {
  /** Page zoom; always one of ZOOM_STEPS. */
  zoom: number;
  /** Typeface for the interface; empty means the system's. */
  uiFont: string;
  /** Typeface for code and paths; empty means the system's. */
  codeFont: string;
}

export const DEFAULT_DISPLAY: DisplayPrefs = { zoom: 1, uiFont: "", codeFont: "" };

/** Letters, digits, spaces and a few separators: enough for any installed family name, nothing that could leave a CSS string. */
const FONT_NAME = /^[\p{L}\p{N} ._-]{1,64}$/u;

export function isFontName(v: unknown): v is string {
  return typeof v === "string" && FONT_NAME.test(v);
}

/** Accepts exactly the shape the panel sends; anything else is refused, so a bad file or call changes nothing. */
export function parseDisplay(v: unknown): DisplayPrefs | undefined {
  if (typeof v !== "object" || v === null || Array.isArray(v)) return undefined;
  const o = v as Record<string, unknown>;
  if (Object.keys(o).some((k) => k !== "zoom" && k !== "uiFont" && k !== "codeFont")) return undefined;
  const zoom = o.zoom;
  if (typeof zoom !== "number" || !(ZOOM_STEPS as readonly number[]).includes(zoom)) return undefined;
  const uiFont = o.uiFont ?? "";
  const codeFont = o.codeFont ?? "";
  for (const f of [uiFont, codeFont]) if (f !== "" && !isFontName(f)) return undefined;
  return { zoom, uiFont: uiFont as string, codeFont: codeFont as string };
}

/** The next larger or smaller step, or the same one at the end of the range. */
export function stepZoom(current: number, dir: 1 | -1): number {
  const i = (ZOOM_STEPS as readonly number[]).indexOf(current);
  const at = i < 0 ? 0 : i;
  return ZOOM_STEPS[Math.min(ZOOM_STEPS.length - 1, Math.max(0, at + dir))]!;
}

/** A CSS font-family list that starts with the chosen family, or undefined for the default stack. */
export function cssFamily(name: string, fallback: string): string | undefined {
  return isFontName(name) ? `"${name}", ${fallback}` : undefined;
}

/** Ctrl/Cmd + "+", "-" and "0" step or reset the panel's text size, as in a browser; nothing else is intercepted. */
export function zoomKey(input: { type: string; key: string; control: boolean; meta: boolean; alt: boolean }): 1 | -1 | 0 | undefined {
  if (input.type !== "keyDown" || input.alt || !(input.control || input.meta)) return undefined;
  if (input.key === "+" || input.key === "=") return 1;
  if (input.key === "-" || input.key === "_") return -1;
  if (input.key === "0") return 0;
  return undefined;
}
