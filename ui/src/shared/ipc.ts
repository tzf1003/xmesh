// The only surface the renderer gets: a fixed set of operations, each with a
// validated argument. There is deliberately no generic `invoke`, no raw
// channel access, and nothing that reaches the filesystem or a subprocess with
// renderer-chosen input.

import type { Snapshot } from "./state";

/** `ipcMain.handle` channels. The preload script hard-codes the same strings; a unit test checks they agree. */
export const CHANNELS = {
  getSnapshot: "xmesh:get-snapshot",
  drain: "xmesh:drain",
  resume: "xmesh:resume",
  selectInstance: "xmesh:select-instance",
  copy: "xmesh:copy",
  openPath: "xmesh:open-path",
  setLoginItem: "xmesh:set-login-item",
  openExternal: "xmesh:open-external",
} as const;

/** Main -> renderer push channel. */
export const SNAPSHOT_EVENT = "xmesh:snapshot";

export const COPY_KINDS = ["gatewayUrl", "claudeCodeCommand", "codexConfig"] as const;
export type CopyKind = (typeof COPY_KINDS)[number];

export const PATH_KINDS = ["config", "logs", "audit"] as const;
export type PathKind = (typeof PATH_KINDS)[number];

/** Link targets are chosen by key, never by URL, so the renderer cannot open arbitrary sites. */
export const EXTERNAL_LINKS = {
  docs: "https://github.com/tzf1003/xmesh#readme",
} as const;
export type ExternalKey = keyof typeof EXTERNAL_LINKS;

export type ActionResult = { ok: true } | { ok: false; error: { code: string; message: string } };

/** What `window.xmesh` looks like in the renderer. Method names are the whitelist. */
export interface PanelApi {
  getSnapshot(): Promise<Snapshot>;
  onSnapshot(listener: (snapshot: Snapshot) => void): () => void;
  drain(): Promise<ActionResult>;
  resume(): Promise<ActionResult>;
  selectInstance(id: string): Promise<ActionResult>;
  copy(kind: CopyKind): Promise<ActionResult>;
  openPath(kind: PathKind): Promise<ActionResult>;
  setLoginItem(enabled: boolean): Promise<ActionResult>;
  openExternal(key: ExternalKey): Promise<ActionResult>;
}

export const PANEL_API_METHODS = [
  "getSnapshot",
  "onSnapshot",
  "drain",
  "resume",
  "selectInstance",
  "copy",
  "openPath",
  "setLoginItem",
  "openExternal",
] as const satisfies readonly (keyof PanelApi)[];

export const PANEL_API_GLOBAL = "xmesh";

// Argument validators. Each returns the parsed value or undefined.

const INSTANCE_ID = /^[A-Za-z0-9._-]{1,128}$/;

export function parseInstanceId(v: unknown): string | undefined {
  return typeof v === "string" && INSTANCE_ID.test(v) && v !== "." && v !== ".." ? v : undefined;
}

export function parseCopyKind(v: unknown): CopyKind | undefined {
  return (COPY_KINDS as readonly unknown[]).includes(v) ? (v as CopyKind) : undefined;
}

export function parsePathKind(v: unknown): PathKind | undefined {
  return (PATH_KINDS as readonly unknown[]).includes(v) ? (v as PathKind) : undefined;
}

export function parseExternalKey(v: unknown): ExternalKey | undefined {
  return typeof v === "string" && Object.prototype.hasOwnProperty.call(EXTERNAL_LINKS, v)
    ? (v as ExternalKey)
    : undefined;
}

export function parseBoolean(v: unknown): boolean | undefined {
  return typeof v === "boolean" ? v : undefined;
}
