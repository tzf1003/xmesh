// The one-time notice after the gateway token appears (first launch after an upgrade, or a rotation).
// The kernel is the source of truth; what this remembers is only which modified_at the user dismissed
// the notice at, per viewer. Every storage access can throw, so each one is guarded.

import type { PatInfo } from "../../shared/control";

export const NOTICE_KEY_PREFIX = "xmesh.gatewayTokenNotice.";

export interface NoticeStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

/** The page's localStorage, or undefined where the browser refuses it (reading the accessor itself can throw). */
export function noticeStorage(): NoticeStorage | undefined {
  try {
    return (globalThis as { localStorage?: NoticeStorage }).localStorage ?? undefined;
  } catch {
    return undefined;
  }
}

export function noticeKey(patName: string): string {
  return `${NOTICE_KEY_PREFIX}${patName}`;
}

/** The modified_at the notice was last dismissed at; undefined when never, or when storage cannot be read. */
export function readSeen(store: NoticeStorage | undefined, patName: string): string | undefined {
  if (store === undefined) return undefined;
  try {
    return store.getItem(noticeKey(patName)) ?? undefined;
  } catch {
    return undefined;
  }
}

/** Returns false when the value could not be stored; the caller still hides the notice for this session. */
export function writeSeen(store: NoticeStorage | undefined, patName: string, modifiedAt: string): boolean {
  if (store === undefined) return false;
  try {
    store.setItem(noticeKey(patName), modifiedAt);
    return true;
  } catch {
    return false;
  }
}

/** Due when the PAT exists, its token file has a time, and that time is not the one dismissed. */
export function noticeDue(pat: PatInfo | undefined, seen: string | undefined): boolean {
  return pat !== undefined && pat.modified_at !== null && pat.modified_at !== seen;
}
