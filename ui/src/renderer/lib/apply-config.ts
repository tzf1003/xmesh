import type { ConfigPutResponse } from "../../shared/control";
import type { ConfigPatchEdit, DataResult } from "../../shared/ipc";
import { getApi } from "../api";

/** The most edits one PATCH carries; the kernel refuses more (control API `ConfigEditRequest`). */
export const MAX_CONFIG_EDITS = 64;

/**
 * The one place the form-style settings write the kernel configuration. It sends a single PATCH
 * with the etag the edits were computed from. A settings page that needs other behaviour (for
 * example a kernel hot-reload status) replaces this function, not the callers.
 */
export function applyConfigEdits(etag: string, edits: ConfigPatchEdit[], dryRun = false): Promise<DataResult<ConfigPutResponse>> {
  return getApi().patchConfig({ edits, etag, dryRun });
}
