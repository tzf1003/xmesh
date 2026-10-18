import * as fs from "node:fs";
import * as path from "node:path";
import { DEFAULT_DISPLAY, type DisplayPrefs, parseDisplay } from "../shared/display";

/** The display preferences in one small JSON file in the app's user-data directory. */
export class DisplayStore {
  private prefs: DisplayPrefs;

  constructor(private readonly file: string) {
    this.prefs = this.read();
  }

  get(): DisplayPrefs {
    return this.prefs;
  }

  /** Keeps the new value in memory even when the disk write fails, and says whether it was saved. */
  set(next: DisplayPrefs): boolean {
    this.prefs = next;
    try {
      fs.mkdirSync(path.dirname(this.file), { recursive: true });
      const tmp = `${this.file}.tmp`;
      fs.writeFileSync(tmp, JSON.stringify(next));
      fs.renameSync(tmp, this.file);
      return true;
    } catch {
      return false;
    }
  }

  private read(): DisplayPrefs {
    try {
      return parseDisplay(JSON.parse(fs.readFileSync(this.file, "utf8"))) ?? DEFAULT_DISPLAY;
    } catch {
      return DEFAULT_DISPLAY;
    }
  }
}
