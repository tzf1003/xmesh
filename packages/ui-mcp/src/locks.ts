import fs from "node:fs";
import path from "node:path";
import { execFileSync } from "node:child_process";
import { randomUUID, createHash } from "node:crypto";
import { privateDir } from "./config.js";
import { UIError } from "./errors.js";

interface Owner {
  pid: number;
  birth: string;
  nonce: string;
  ticket: number;
}
let selfIdentity: string | undefined;
export function processIdentity(pid: number): string {
  if (pid === process.pid && selfIdentity) return selfIdentity;
  try {
    process.kill(pid, 0);
  } catch {
    return "";
  }
  try {
    const identity =
      process.platform === "win32"
        ? execFileSync(
            "powershell.exe",
            [
              "-NoProfile",
              "-NonInteractive",
              "-Command",
              `(Get-Process -Id ${pid} -ErrorAction Stop).StartTime.ToUniversalTime().Ticks`,
            ],
            {
              encoding: "utf8",
              stdio: ["ignore", "pipe", "ignore"],
              timeout: 10000,
              windowsHide: true,
            },
          ).trim()
        : execFileSync("ps", ["-p", String(pid), "-o", "lstart="], {
            encoding: "utf8",
            stdio: ["ignore", "pipe", "ignore"],
            timeout: 3000,
          }).trim();
    if (pid === process.pid) selfIdentity = identity;
    return identity;
  } catch {
    return "";
  }
}
function stale(owner: Owner): boolean {
  if (!Number.isSafeInteger(owner.pid) || owner.pid <= 0 || !owner.birth)
    return false;
  try {
    process.kill(owner.pid, 0);
  } catch (e) {
    return (e as NodeJS.ErrnoException).code === "ESRCH";
  }
  const current = processIdentity(owner.pid);
  return !!current && current !== owner.birth;
}
export class Locks {
  private owner: Owner;
  private held = new Set<string>();
  constructor(private root: string) {
    privateDir(root);
    this.owner = {
      pid: process.pid,
      birth: processIdentity(process.pid),
      nonce: randomUUID(),
      ticket: 0,
    };
    if (!this.owner.birth)
      throw new UIError(
        "preflight_failed",
        "cannot determine process identity for resource locks",
      );
  }
  acquire(resource: string): (() => void) | undefined {
    const dir = path.join(
      this.root,
      createHash("sha256").update(resource).digest("hex"),
    );
    privateDir(dir);
    // Bakery tickets use unique claim files. A stale claim is ignored, never
    // renamed over another process's newly acquired lock.
    const claim = path.join(dir, `${randomUUID()}.json`);
    fs.writeFileSync(
      `${claim}.tmp`,
      JSON.stringify({ ...this.owner, ticket: 0 }),
      {
        mode: 0o600,
        flag: "wx",
      },
    );
    fs.renameSync(`${claim}.tmp`, claim);
    const read = (): Owner[] =>
      fs
        .readdirSync(dir)
        .filter(
          (name) => name.endsWith(".json") && path.join(dir, name) !== claim,
        )
        .flatMap((name) => {
          try {
            const owner = JSON.parse(
              fs.readFileSync(path.join(dir, name), "utf8"),
            ) as Owner;
            return stale(owner) ? [] : [owner];
          } catch (error) {
            if ((error as NodeJS.ErrnoException).code === "ENOENT") return [];
            return [{ pid: 0, birth: "", nonce: "", ticket: 0 }];
          }
        });
    try {
      const ticket = 1 + Math.max(0, ...read().map((o) => o.ticket || 0));
      const temporary = `${claim}.tmp`;
      fs.writeFileSync(temporary, JSON.stringify({ ...this.owner, ticket }), {
        mode: 0o600,
      });
      fs.renameSync(temporary, claim);
      const blocked = read().some(
        (o) =>
          !o.ticket ||
          o.ticket < ticket ||
          (o.ticket === ticket && o.nonce < this.owner.nonce),
      );
      if (blocked) {
        fs.rmSync(claim, { force: true });
        return undefined;
      }
      this.held.add(claim);
      return () => this.release(claim);
    } catch (error) {
      fs.rmSync(claim, { force: true });
      throw error;
    }
  }
  private release(dir: string): void {
    try {
      const owner = JSON.parse(fs.readFileSync(dir, "utf8")) as Owner;
      if (owner.nonce === this.owner.nonce) fs.rmSync(dir, { force: true });
    } catch (e) {
      if ((e as NodeJS.ErrnoException).code !== "ENOENT") throw e;
    } finally {
      this.held.delete(dir);
    }
  }
  close(): void {
    for (const dir of [...this.held]) this.release(dir);
  }
}
