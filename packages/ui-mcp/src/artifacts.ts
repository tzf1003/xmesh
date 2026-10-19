import fs from "node:fs";
import path from "node:path";
import { randomUUID, createHash } from "node:crypto";
import { stripVTControlCharacters } from "node:util";
import type { Config, Artifact, RunResult } from "./types.js";
import { privateDir, inside } from "./config.js";
import { requireValue } from "./errors.js";

export function atomicJSON(file: string, value: unknown): void {
  const tmp = `${file}.${randomUUID()}.tmp`;
  fs.writeFileSync(tmp, JSON.stringify(value, null, 2), { mode: 0o600 });
  for (let attempt = 0; ; attempt++) {
    try {
      fs.renameSync(tmp, file);
      break;
    } catch (error) {
      if (
        process.platform !== "win32" ||
        !["EPERM", "EACCES", "EBUSY"].includes(
          (error as NodeJS.ErrnoException).code ?? "",
        ) ||
        attempt >= 20
      ) {
        fs.rmSync(tmp, { force: true });
        throw error;
      }
      Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 10);
    }
  }
}
export function redact<T>(value: T, secrets: string[]): T {
  let text = JSON.stringify(value);
  for (const secret of secrets
    .filter(Boolean)
    .sort((a, b) => b.length - a.length))
    text = text.split(JSON.stringify(secret).slice(1, -1)).join("[REDACTED]");
  return JSON.parse(text) as T;
}
export function bytes(dir: string): number {
  let entries: fs.Dirent[];
  try {
    entries = fs.readdirSync(dir, { withFileTypes: true });
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return 0;
    throw error;
  }
  let total = 0;
  for (const entry of entries) {
    const file = path.join(dir, entry.name);
    if (entry.isDirectory()) total += bytes(file);
    else if (entry.isFile()) {
      try {
        total += fs.statSync(file).size;
      } catch (error) {
        // Guardians atomically rename their temporary state during this walk.
        if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
      }
    }
  }
  return total;
}
export class Evidence {
  readonly dir: string;
  private files = new Map<string, string>();
  private secrets: string[];
  constructor(
    private config: Config,
    instance: string,
    run: string,
    secrets: string[],
  ) {
    this.dir = path.join(config.state_dir, "instances", instance, "runs", run);
    privateDir(this.dir);
    this.secrets = secrets;
  }
  event(event: Record<string, unknown>): void {
    const line = JSON.stringify(redact(event, this.secrets));
    fs.appendFileSync(path.join(this.dir, "events.jsonl"), `${line}\n`, {
      mode: 0o600,
    });
  }
  save(result: RunResult): void {
    atomicJSON(
      path.join(this.dir, "result.json"),
      redact(result, this.secrets),
    );
  }
  add(file: string, name: string, mime: string): Artifact {
    const source = inside(this.dir, file),
      size = fs.statSync(source).size;
    requireValue(
      bytes(this.dir) <= this.config.limits.run_bytes &&
        bytes(this.config.state_dir) <= this.config.limits.total_bytes,
      "evidence_quota",
      "evidence quota exceeded",
    );
    const id = `a_${randomUUID()}`;
    const hash = createHash("sha256");
    const fd = fs.openSync(source, "r"),
      buffer = Buffer.alloc(65536);
    try {
      for (let n; (n = fs.readSync(fd, buffer)) > 0; )
        hash.update(buffer.subarray(0, n));
    } finally {
      fs.closeSync(fd);
    }
    this.files.set(id, source);
    return {
      id,
      name,
      mime_type: mime,
      bytes: size,
      sha256: hash.digest("hex"),
    };
  }
  read(id: string, offset = 0, limit = 65536): Record<string, unknown> {
    const file = this.files.get(id);
    requireValue(file, "not_found", "unknown artifact");
    inside(this.dir, file);
    requireValue(
      Number.isSafeInteger(offset) &&
        offset >= 0 &&
        Number.isSafeInteger(limit) &&
        limit > 0 &&
        limit <= 262144,
      "invalid_args",
      "invalid artifact range",
    );
    const size = fs.statSync(file).size;
    requireValue(
      offset <= size,
      "invalid_args",
      "artifact offset exceeds length",
    );
    const buffer = Buffer.alloc(Math.min(limit, size - offset)),
      fd = fs.openSync(file, "r");
    try {
      fs.readSync(fd, buffer, 0, buffer.length, offset);
    } finally {
      fs.closeSync(fd);
    }
    return {
      artifact_id: id,
      offset,
      total_bytes: size,
      eof: offset + buffer.length === size,
      encoding: "base64",
      data: buffer.toString("base64"),
    };
  }
  reports(result: RunResult): Artifact[] {
    const safe = redact(result, this.secrets);
    const text = [
      `# UI test ${safe.run_id}`,
      `Status: ${safe.status}`,
      `Environment: ${safe.environment_id}`,
      `Driver: ${safe.driver}`,
      `Fixtures: ${safe.fixtures}`,
      `Coverage: ${safe.coverage.join(", ")}`,
      "",
      ...safe.steps.map((s) => `- ${s.id}: ${s.status}`),
      "",
      safe.error ? `Error: ${safe.error.code}\n\n${safe.error.message}` : "",
    ].join("\n");
    fs.writeFileSync(path.join(this.dir, "report.md"), text, { mode: 0o600 });
    const escape = (s: string) =>
      stripVTControlCharacters(s)
        .replace(/[\u0000-\u0008\u000B\u000C\u000E-\u001F\uFFFE\uFFFF]/g, "")
        .replace(
          /[<>&"']/g,
          (c) =>
            ({
              "<": "&lt;",
              ">": "&gt;",
              "&": "&amp;",
              '"': "&quot;",
              "'": "&apos;",
            })[c]!,
        );
    const unsuccessful = safe.status !== "passed";
    fs.writeFileSync(
      path.join(this.dir, "junit.xml"),
      `<?xml version="1.0" encoding="UTF-8"?><testsuite name="ui" tests="1" failures="${unsuccessful ? 1 : 0}"><testcase name="${escape(safe.run_id)}">${unsuccessful ? `<failure message="${escape(safe.error?.code ?? safe.status)}">${escape(safe.error?.message ?? safe.status)}</failure>` : ""}</testcase></testsuite>`,
      { mode: 0o600 },
    );
    return [
      this.add("report.md", "report.md", "text/markdown"),
      this.add("junit.xml", "junit.xml", "application/xml"),
    ];
  }
}
export function gc(config: Config): { removed: number; bytes: number } {
  const root = path.join(config.state_dir, "instances");
  if (!fs.existsSync(root)) return { removed: 0, bytes: 0 };
  const candidates: Array<{ dir: string; time: number; size: number }> = [];
  for (const i of fs
    .readdirSync(root, { withFileTypes: true })
    .filter((e) => e.isDirectory())) {
    const runs = path.join(root, i.name, "runs");
    if (!fs.existsSync(runs)) continue;
    for (const r of fs
      .readdirSync(runs, { withFileTypes: true })
      .filter((e) => e.isDirectory())) {
      const dir = path.join(runs, r.name);
      try {
        const result = JSON.parse(
          fs.readFileSync(path.join(dir, "result.json"), "utf8"),
        ) as RunResult;
        if (result.ended_at)
          candidates.push({
            dir,
            time: Date.parse(result.ended_at),
            size: bytes(dir),
          });
      } catch {
        /* Incomplete journals remain available for recovery. */
      }
    }
  }
  candidates.sort((a, b) => a.time - b.time);
  let total = bytes(config.state_dir),
    removed = 0,
    freed = 0;
  for (const item of candidates)
    if (
      item.time < Date.now() - config.limits.retention_days * 86400000 ||
      total > config.limits.total_bytes
    ) {
      fs.rmSync(item.dir, { recursive: true, force: true });
      total -= item.size;
      freed += item.size;
      removed++;
    }
  return { removed, bytes: freed };
}
