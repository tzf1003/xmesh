// After a config save the page asks the kernel to reload the file in place, so a change that the kernel can take
// needs no restart. A kernel without the reload route, or a reload that fails, leaves the restart the write reported.

import { getApi } from "../api";

/** What the page shows after a save: whether a restart is still needed, for which settings, and a load error. */
export interface SaveOutcome {
  restart: boolean;
  /** The settings the kernel still needs a restart for, as config pointers. */
  paths: string[];
  loadError?: string;
}

export async function applySaved(restartRequired: boolean): Promise<SaveOutcome> {
  const r = await getApi().reloadConfig();
  if (!r.ok) return { restart: restartRequired, paths: [] };
  return { restart: r.data.restart_required, paths: r.data.restart_paths, loadError: r.data.load_error ?? undefined };
}
