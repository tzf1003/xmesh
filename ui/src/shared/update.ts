// Version check and one-click update (docs/design/12 "更新"). The state below is what the tray and the panel
// render from; it lives in the Snapshot so both follow it without a channel of their own.

/** Both flags default to on while every release is a beta: a tester wants to hear about the next one. */
export interface UpdatePrefs {
  /** Look for a newer release on start and every few hours. A manual check always works. */
  auto: boolean;
  /** Offer pre-releases (v0.2.0-beta.1, -rc.1) as well as stable releases. */
  prerelease: boolean;
}

export const DEFAULT_UPDATE_PREFS: UpdatePrefs = { auto: true, prerelease: true };

/** Exactly the shape the panel sends; anything else is refused, so a bad file or call changes nothing. */
export function parseUpdatePrefs(v: unknown): UpdatePrefs | undefined {
  if (typeof v !== "object" || v === null || Array.isArray(v)) return undefined;
  const o = v as Record<string, unknown>;
  if (Object.keys(o).some((k) => k !== "auto" && k !== "prerelease")) return undefined;
  if (typeof o.auto !== "boolean" || typeof o.prerelease !== "boolean") return undefined;
  return { auto: o.auto, prerelease: o.prerelease };
}

/**
 * Why this install cannot update itself from the panel; "" when it can.
 *   unpackaged   a development run, nothing to replace
 *   unsupported  no installer is published for this system (the UI ships for Windows and macOS)
 *   translocated macOS runs the app from a read-only copy (AppTranslocation or the disk image)
 */
export type UpdateBlocked = "" | "unpackaged" | "unsupported" | "translocated";

export type UpdatePhase = "idle" | "checking" | "available" | "downloading" | "installing" | "error";

/** The newest eligible release, as far as the panel shows it. */
export interface UpdateRelease {
  /** `0.1.0-beta.3`, comparable with the running version. */
  version: string;
  /** `v0.1.0-beta.3`. */
  tag: string;
  prerelease: boolean;
  publishedAt: string;
  /** Size of the installer for this system, in bytes. */
  size: number;
  /** Release notes as plain text, cut to a length a card can show. */
  notes: string;
}

export interface UpdateError {
  /** Stable code: network, no_installer, bad_checksum, stop_failed, busy, install_failed. */
  code: string;
  message: string;
}

/** How the last update ended, shown once after the app came back. */
export interface UpdateOutcome {
  ok: boolean;
  /** The version the update was heading to. */
  version: string;
  message?: string;
  /** Nodes the update stopped that would not start again. */
  unstarted?: string[];
}

export interface UpdateState {
  blocked: UpdateBlocked;
  /** The running app's version; the kernel inside the installer has the same one. */
  current: string;
  /** False when the kernel the app runs is not the one shipped in the installer (XMESH_BIN or PATH). */
  kernelBundled: boolean;
  phase: UpdatePhase;
  /** Set while `phase` is `available`, `downloading` or `installing`. */
  latest?: UpdateRelease;
  /** When the last successful check finished (ISO time). A check that found nothing newer leaves `latest` unset. */
  checkedAt?: string;
  progress?: { received: number; total: number };
  error?: UpdateError;
  prefs: UpdatePrefs;
  outcome?: UpdateOutcome;
}

export const EMPTY_UPDATE: UpdateState = {
  blocked: "unpackaged",
  current: "",
  kernelBundled: false,
  phase: "idle",
  prefs: DEFAULT_UPDATE_PREFS,
};
