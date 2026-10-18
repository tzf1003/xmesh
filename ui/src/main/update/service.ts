import type { ActionResult } from "../../shared/ipc";
import type { UpdatePrefs } from "../../shared/update";
import type { Updater } from "./updater";

/** The update operations behind the panel IPC: the `Updater`, with its answers shaped as `ActionResult`. */
export class UpdateActions {
  constructor(private readonly updater: Updater) {}

  async check(): Promise<ActionResult> {
    await this.updater.check(true);
    const { phase, error } = this.updater.state;
    return phase === "error" && error ? { ok: false, error } : { ok: true };
  }

  async install(): Promise<ActionResult> {
    const r = await this.updater.install();
    return r.ok ? r : { ok: false, error: r.error };
  }

  async cancel(): Promise<ActionResult> {
    this.updater.cancel();
    return { ok: true };
  }

  async setPrefs(prefs: UpdatePrefs): Promise<ActionResult> {
    this.updater.setPrefs(prefs);
    return { ok: true };
  }
}
