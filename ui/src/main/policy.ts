// Security policy for the panel window, as data, so tests can assert it
// without starting Electron. The Electron glue (`window.ts`, `protocol.ts`)
// applies these values and nothing else. Requirements: docs/design/12-ui-and-control.md "面板".

import * as path from "node:path";
import type { WebPreferences } from "electron";

export const PANEL_SCHEME = "app";
export const PANEL_HOST = "panel";
export const PANEL_ORIGIN = `${PANEL_SCHEME}://${PANEL_HOST}`;
export const PANEL_ENTRY = `${PANEL_ORIGIN}/index.html`;

/** Pinned renderer settings. The unit test compares this object with the live window. */
export function panelWebPreferences(preload: string, packaged: boolean): WebPreferences {
  return {
    preload,
    contextIsolation: true,
    sandbox: true,
    nodeIntegration: false,
    nodeIntegrationInWorker: false,
    nodeIntegrationInSubFrames: false,
    webSecurity: true,
    allowRunningInsecureContent: false,
    experimentalFeatures: false,
    webviewTag: false,
    enableWebSQL: false,
    spellcheck: false,
    navigateOnDragDrop: false,
    safeDialogs: true,
    devTools: !packaged,
  };
}

/**
 * Strict CSP for the bundled panel: same-origin scripts, bundled styles and
 * per-page nonce-authorized terminal styles. No inline scripts or network from the page (the main process owns every
 * connection), no framing, no plugins.
 */
export const PANEL_CSP = [
  "default-src 'none'",
  "script-src 'self'",
  "style-src 'self' 'nonce-__XMESH_STYLE_NONCE__'",
  "img-src 'self' data:",
  "font-src 'self'",
  "connect-src 'none'",
  "object-src 'none'",
  "base-uri 'none'",
  "form-action 'none'",
  "frame-ancestors 'none'",
].join("; ");

export function parseCsp(csp: string): Map<string, string[]> {
  const out = new Map<string, string[]>();
  for (const part of csp.split(";")) {
    const tokens = part.trim().split(/\s+/).filter(Boolean);
    const name = tokens.shift();
    if (name) out.set(name, tokens);
  }
  return out;
}

export function isPanelUrl(raw: string): boolean {
  try {
    const u = new URL(raw);
    return u.protocol === `${PANEL_SCHEME}:` && u.hostname === PANEL_HOST;
  } catch {
    return false;
  }
}

const MIME: Record<string, string> = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".mjs": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".ico": "image/x-icon",
  ".woff2": "font/woff2",
};

export function mimeFor(file: string): string | undefined {
  return MIME[path.extname(file).toLowerCase()];
}

/**
 * Maps an `app://panel/<path>` URL to a file under `root`, or undefined for
 * anything that is not a plain file path inside it (wrong host, `..`, encoded
 * separators, absolute paths).
 */
export function resolvePanelFile(root: string, raw: string): string | undefined {
  let u: URL;
  try {
    u = new URL(raw);
  } catch {
    return undefined;
  }
  if (u.protocol !== `${PANEL_SCHEME}:` || u.hostname !== PANEL_HOST) return undefined;
  let rel: string;
  try {
    rel = decodeURIComponent(u.pathname);
  } catch {
    return undefined;
  }
  if (rel === "/" || rel === "") rel = "/index.html";
  if (rel.includes("\0") || rel.includes("\\")) return undefined;
  const parts = rel.split("/").filter((p) => p !== "");
  if (parts.some((p) => p === ".." || p === ".")) return undefined;
  const file = path.join(root, ...parts);
  const back = path.relative(root, file);
  if (back === "" || back.startsWith("..") || path.isAbsolute(back)) return undefined;
  return file;
}

/** Separate resource root for the tray; panel origin and IPC rules remain independent. */
export function resolveUiFile(root: string, raw: string): string | undefined {
  try {
    const url = new URL(raw);
    if (url.protocol === "app:" && url.hostname === "tray" && !url.username && !url.password && !url.port) {
      url.hostname = PANEL_HOST;
      return resolvePanelFile(path.join(root, "tray"), url.href);
    }
  } catch { return undefined; }
  return resolvePanelFile(root, raw);
}

/** Headers added to every panel response. */
export function panelHeaders(contentType: string): Record<string, string> {
  return {
    "Content-Type": contentType,
    "Content-Security-Policy": PANEL_CSP,
    "X-Content-Type-Options": "nosniff",
    "Cross-Origin-Opener-Policy": "same-origin",
    "Cross-Origin-Resource-Policy": "same-origin",
    "Cache-Control": "no-store",
  };
}
