// Wire format between the browser panel and the Linux web management server
// (src/web). The page calls the same channels as the Electron preload, over
// HTTP, after logging in with the management key.

import type { PickKind } from "./ipc";

/** Every request that changes state carries this header; a cross-site form or image cannot set it. */
export const WEB_REQUEST_HEADER = "x-xmesh-request";
export const WEB_SESSION_COOKIE = "xmesh_webui";
/** `window.xmeshWeb`: present only in the web panel, beside `window.xmesh`. */
export const WEB_SESSION_GLOBAL = "xmeshWeb";

export interface WebSessionApi {
  logout(): void;
}

export const WEB_PATHS = {
  session: "/api/session",
  login: "/api/login",
  logout: "/api/logout",
  /** `/api/invoke/<channel key>` with `{ args: unknown[] }`. */
  invoke: "/api/invoke/",
  events: "/api/events",
  answer: "/api/answer",
} as const;

/** Desktop side effects the server cannot perform for a remote browser; the page performs them after the call returns. */
export type WebEffect =
  | { type: "clipboard"; text: string }
  | { type: "open"; url: string }
  | { type: "download"; name: string; text: string };

export interface WebInvokeReply {
  result: unknown;
  effects: WebEffect[];
}

/** A question the server asks the page in the middle of a call (native dialogs on the desktop). */
export type WebAsk =
  | { id: string; kind: "confirm"; title: string; detail: string; confirmLabel: string; cancelLabel: string }
  | { id: string; kind: "path"; pick: PickKind | "upload" | "download"; suggested?: string };

export interface WebAnswer {
  id: string;
  /** `confirm`: a boolean. `path`: an absolute path on the server, or null on cancel. */
  value: boolean | string | null;
}

export interface WebSessionInfo {
  authenticated: boolean;
  /** Seconds the client must wait before the next login attempt. */
  retryAfter?: number;
}

/** SSE event names on WEB_PATHS.events. */
export const WEB_EVENTS = { snapshot: "snapshot", calls: "calls", ask: "ask", logout: "logout" } as const;

export function parseWebAnswer(v: unknown): WebAnswer | undefined {
  if (typeof v !== "object" || v === null || Array.isArray(v)) return undefined;
  const o = v as Record<string, unknown>;
  if (typeof o.id !== "string" || !/^[A-Za-z0-9_-]{8,64}$/.test(o.id)) return undefined;
  const value = o.value;
  if (value !== null && typeof value !== "boolean" && (typeof value !== "string" || value.length > 4096 || value.includes("\0"))) return undefined;
  return { id: o.id, value };
}
