import * as fs from "node:fs";
import * as path from "node:path";
import { DEFAULT_APP_PREFERENCES, parseAppPreferences, type AppPreferences } from "../shared/preferences";

export class PreferencesStore {
  private value: AppPreferences;
  constructor(private readonly file: string, locale: "zh" | "en") {
    try { this.value = parseAppPreferences(JSON.parse(fs.readFileSync(file, "utf8"))) ?? { ...DEFAULT_APP_PREFERENCES, locale }; }
    catch { this.value = { ...DEFAULT_APP_PREFERENCES, locale }; }
  }
  get(): AppPreferences { return { ...this.value }; }
  set(next: AppPreferences): boolean {
    try {
      fs.mkdirSync(path.dirname(this.file), { recursive: true });
      fs.writeFileSync(this.file + ".tmp", JSON.stringify(next), { mode: 0o600 });
      fs.renameSync(this.file + ".tmp", this.file);
      this.value = { ...next };
      return true;
    } catch { return false; }
  }
}
