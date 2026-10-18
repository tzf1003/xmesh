// After a config save the page asks the kernel to reload the file in place, so a change that the kernel can take
// needs no restart. A kernel without the reload route, or a reload that fails, leaves the restart the write reported.

import { getApi } from "../api";

/** What the page shows after a save: whether a restart is still needed, for which settings, and a load error. */
export interface SaveOutcome {
  restart: boolean;
  /** The settings the kernel still needs a restart for, as config pointers. */
  paths: string[];
  loadError?: string;
  /** Why a setting that is valid did not take effect in the running kernel; empty when everything did. */
  applyErrors: string[];
}

export async function applySaved(restartRequired: boolean): Promise<SaveOutcome> {
  const r = await getApi().reloadConfig();
  if (!r.ok) return { restart: restartRequired, paths: [], applyErrors: [] };
  return {
    restart: r.data.restart_required,
    paths: r.data.restart_paths,
    loadError: r.data.load_error ?? undefined,
    applyErrors: (r.data.apply_errors ?? []).map((e) => e.message),
  };
}

/** The outcome of a save that the kernel answered itself, as a gateway update does: it reports what it could not apply. */
export function outcomeOf(r: { restart_required: boolean; apply_errors?: { message: string }[] }): SaveOutcome {
  return { restart: r.restart_required, paths: [], applyErrors: (r.apply_errors ?? []).map((e) => e.message) };
}
