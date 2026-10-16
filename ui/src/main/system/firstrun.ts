import * as fs from "node:fs";
import * as path from "node:path";

/** Tiny persisted set of "already shown" flags in the app's user-data directory. */
export class SeenFlags {
  constructor(private readonly file: string) {}

  private read(): Record<string, true> {
    try {
      const v: unknown = JSON.parse(fs.readFileSync(this.file, "utf8"));
      return v !== null && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, true>) : {};
    } catch {
      return {};
    }
  }

  has(flag: string): boolean {
    return this.read()[flag] === true;
  }

  /** Returns true the first time a flag is set. Failing to persist counts as already seen, so a read-only profile never nags. */
  markOnce(flag: string): boolean {
    const cur = this.read();
    if (cur[flag] === true) return false;
    try {
      fs.mkdirSync(path.dirname(this.file), { recursive: true });
      fs.writeFileSync(this.file, JSON.stringify({ ...cur, [flag]: true }), { mode: 0o600 });
    } catch {
      return false;
    }
    return true;
  }
}
