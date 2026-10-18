import * as fs from "node:fs";
import * as path from "node:path";
import { DEFAULT_UPDATE_PREFS, type UpdatePrefs, parseUpdatePrefs } from "../../shared/update";

interface Saved {
  prefs: UpdatePrefs;
  /** The newest tag a notification was shown for, so one release is announced once. */
  announced: string;
}

/** The update preferences and the last announced release in one small JSON file in the user-data directory. */
export class UpdateStore {
  private saved: Saved;

  constructor(private readonly file: string) {
    this.saved = this.read();
  }

  get prefs(): UpdatePrefs {
    return this.saved.prefs;
  }

  get announced(): string {
    return this.saved.announced;
  }

  /** Keeps the value in memory even when the disk write fails, and says whether it was saved. */
  set(patch: Partial<Saved>): boolean {
    this.saved = { ...this.saved, ...patch };
    try {
      fs.mkdirSync(path.dirname(this.file), { recursive: true });
      const tmp = `${this.file}.tmp`;
      fs.writeFileSync(tmp, JSON.stringify(this.saved));
      fs.renameSync(tmp, this.file);
      return true;
    } catch {
      return false;
    }
  }

  private read(): Saved {
    try {
      const raw = JSON.parse(fs.readFileSync(this.file, "utf8")) as Record<string, unknown>;
      return {
        prefs: parseUpdatePrefs(raw.prefs) ?? DEFAULT_UPDATE_PREFS,
        announced: typeof raw.announced === "string" ? raw.announced : "",
      };
    } catch {
      return { prefs: DEFAULT_UPDATE_PREFS, announced: "" };
    }
  }
}
