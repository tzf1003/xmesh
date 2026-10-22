// Login for the web management panel: one management key per host, kept in a
// 0600 file, and short-lived sessions in memory. The key is generated on first
// start; `--rotate-key` replaces it. Sessions do not survive a restart.

import { createHash, randomBytes, timingSafeEqual } from "node:crypto";
import * as fs from "node:fs";
import * as path from "node:path";

/** Shorter keys are refused when read from a hand-written file. */
export const MIN_KEY_LENGTH = 16;
const MAX_KEY_LENGTH = 512;

export function generateKey(): string {
  return randomBytes(32).toString("base64url");
}

function validKey(key: string): boolean {
  return key.length >= MIN_KEY_LENGTH && key.length <= MAX_KEY_LENGTH && !/[\s\0]/.test(key);
}

/** Writes `key` to `file` with mode 0600 through a temporary file in the same directory. */
export function writeKeyFile(file: string, key: string): void {
  fs.mkdirSync(path.dirname(file), { recursive: true, mode: 0o700 });
  const tmp = `${file}.${process.pid}.${randomBytes(6).toString("hex")}.tmp`;
  try {
    fs.writeFileSync(tmp, `${key}\n`, { mode: 0o600, flag: "wx" });
    fs.renameSync(tmp, file);
  } catch (e) {
    fs.rmSync(tmp, { force: true });
    throw e;
  }
}

export interface LoadedKey {
  key: string;
  /** True when this call created the file; the caller prints the key once. */
  created: boolean;
}

/** Reads the key file, creating it with a fresh key when it is absent or `rotate` is set. */
export function loadKey(file: string, rotate = false): LoadedKey {
  if (!rotate) {
    let text: string | undefined;
    try {
      const st = fs.statSync(file);
      if (process.platform !== "win32" && (st.mode & 0o077) !== 0) {
        throw new Error(`${file} must not be readable by group or others (chmod 600)`);
      }
      text = fs.readFileSync(file, "utf8");
    } catch (e) {
      if ((e as NodeJS.ErrnoException).code !== "ENOENT") throw e;
    }
    if (text !== undefined) {
      const key = text.trim();
      if (!validKey(key)) throw new Error(`${file}: the key must be ${MIN_KEY_LENGTH}-${MAX_KEY_LENGTH} characters without spaces`);
      return { key, created: false };
    }
  }
  const key = generateKey();
  writeKeyFile(file, key);
  return { key, created: true };
}

const digest = (s: string): Buffer => createHash("sha256").update(s, "utf8").digest();

/** Constant-time comparison: both sides are hashed first, so the length does not leak either. */
export function keyMatches(expected: string, given: unknown): boolean {
  if (typeof given !== "string" || given.length > MAX_KEY_LENGTH) return false;
  return timingSafeEqual(digest(expected), digest(given));
}

export interface SessionOptions {
  /** A session unused for this long ends. */
  idleMs?: number;
  /** A session ends this long after login whatever its use. */
  maxMs?: number;
  /** At most this many live sessions; the oldest is dropped first. */
  limit?: number;
  now?: () => number;
}

interface Session {
  created: number;
  seen: number;
}

export class Sessions {
  private readonly items = new Map<string, Session>();
  private readonly idleMs: number;
  private readonly maxMs: number;
  private readonly limit: number;
  private readonly now: () => number;

  constructor(opts: SessionOptions = {}) {
    this.idleMs = opts.idleMs ?? 12 * 3600_000;
    this.maxMs = opts.maxMs ?? 7 * 24 * 3600_000;
    this.limit = opts.limit ?? 64;
    this.now = opts.now ?? Date.now;
  }

  create(): string {
    this.sweep();
    while (this.items.size >= this.limit) {
      const oldest = this.items.keys().next().value;
      if (oldest === undefined) break;
      this.items.delete(oldest);
    }
    const id = randomBytes(32).toString("base64url");
    const now = this.now();
    this.items.set(id, { created: now, seen: now });
    return id;
  }

  /** Whether `id` names a live session; a hit counts as use. */
  touch(id: string | undefined): boolean {
    if (id === undefined) return false;
    const s = this.items.get(id);
    if (!s) return false;
    const now = this.now();
    if (now - s.seen > this.idleMs || now - s.created > this.maxMs) {
      this.items.delete(id);
      return false;
    }
    s.seen = now;
    return true;
  }

  delete(id: string | undefined): void {
    if (id !== undefined) this.items.delete(id);
  }

  clear(): void {
    this.items.clear();
  }

  get size(): number {
    return this.items.size;
  }

  private sweep(): void {
    const now = this.now();
    for (const [id, s] of this.items) {
      if (now - s.seen > this.idleMs || now - s.created > this.maxMs) this.items.delete(id);
    }
  }
}

/**
 * Slows down key guessing per client address: after `free` failures each further
 * failure doubles the wait, up to `maxMs`. A success clears the address.
 */
export class LoginLimiter {
  private readonly failures = new Map<string, { count: number; until: number }>();
  private readonly free: number;
  private readonly baseMs: number;
  private readonly maxMs: number;
  private readonly now: () => number;

  constructor(opts: { free?: number; baseMs?: number; maxMs?: number; now?: () => number } = {}) {
    this.free = opts.free ?? 5;
    this.baseMs = opts.baseMs ?? 1000;
    this.maxMs = opts.maxMs ?? 15 * 60_000;
    this.now = opts.now ?? Date.now;
  }

  /** Milliseconds `client` must still wait; 0 when it may try. */
  wait(client: string): number {
    const f = this.failures.get(client);
    return f ? Math.max(0, f.until - this.now()) : 0;
  }

  fail(client: string): void {
    const f = this.failures.get(client) ?? { count: 0, until: 0 };
    f.count++;
    if (f.count > this.free) f.until = this.now() + Math.min(this.maxMs, this.baseMs * 2 ** (f.count - this.free - 1));
    this.failures.set(client, f);
    if (this.failures.size > 10_000) {
      const now = this.now();
      for (const [k, v] of this.failures) if (v.until <= now) this.failures.delete(k);
    }
  }

  succeed(client: string): void {
    this.failures.delete(client);
  }
}
