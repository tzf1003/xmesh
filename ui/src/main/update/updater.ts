// The update flow: check GitHub for a newer release, download its installer, verify it, stop the kernels, hand
// over to the installer script and quit. After the new app starts, `finishPending` restarts the kernels and
// reports how the update ended. Every side effect sits behind `UpdaterEnv`, so the flow runs in tests.

import { EventEmitter } from "node:events";
import * as fs from "node:fs";
import * as path from "node:path";
import { createHash } from "node:crypto";
import {
  EMPTY_UPDATE,
  type UpdateBlocked,
  type UpdateError,
  type UpdateOutcome,
  type UpdatePrefs,
  type UpdateState,
} from "../../shared/update";
import { ApplyError, type Applier } from "./apply";
import { CHECKSUM_FILE, type Candidate, type Target, expectedHash, parseChecksums, pickCandidate, targetFor } from "./releases";
import { type Progress, type UpdateSource, SourceError } from "./source";
import type { UpdateStore } from "./store";
import { sameVersion } from "../../shared/version";

export type StopResult = "stopped" | "absent";

export type UpdateNotice =
  | { kind: "available"; version: string }
  | { kind: "updated"; version: string }
  | { kind: "failed"; version: string };

export interface UpdaterEnv {
  /** `app.getVersion()` */
  version: string;
  packaged: boolean;
  platform: NodeJS.Platform;
  arch: string;
  /** The kernel the app runs is the one inside the installer. */
  kernelBundled: boolean;
  execPath: string;
  pid: number;
  /** A directory the update may fill and empty (installers, scripts, the pending note). */
  dataDir: string;
  source: UpdateSource;
  /** Undefined where nothing can replace the app. */
  applier: Applier | undefined;
  store: UpdateStore;
  /** Node names of the kernels running now. */
  runningNodes(): string[];
  /** Stops the service of a node. `absent`: it has none (a kernel started by hand). Throws when it cannot stop. */
  stopNode(node: string): Promise<StopResult>;
  startNode(node: string): Promise<void>;
  quit(): void;
  openPath(p: string): Promise<string>;
  notify(n: UpdateNotice): void;
  now?: () => Date;
  /** Delay before the first automatic check, and the interval after it. */
  firstCheckMs?: number;
  intervalMs?: number;
}

interface Pending {
  from: string;
  to: string;
  nodes: string[];
}

export interface UpdaterEvents {
  change: [state: UpdateState];
}

const PENDING_FILE = "pending.json";
const RESULT_FILE = "result.txt";
const PROGRESS_EMIT_MS = 250;
const NODE = /^[A-Za-z0-9._-]+$/;

export class Updater extends EventEmitter<UpdaterEvents> {
  private st: UpdateState;
  private candidate: Candidate | undefined;
  private abort: AbortController | undefined;
  private timers: NodeJS.Timeout[] = [];
  private lastProgress = 0;
  private readonly target: Target | undefined;

  constructor(private readonly env: UpdaterEnv) {
    super();
    this.target = targetFor(env.platform, env.arch);
    this.st = {
      ...EMPTY_UPDATE,
      current: env.version,
      kernelBundled: env.kernelBundled,
      prefs: env.store.prefs,
      blocked: this.blocked(),
    };
  }

  get state(): UpdateState {
    return this.st;
  }

  private blocked(): UpdateBlocked {
    if (!this.env.packaged) return "unpackaged";
    if (this.target === undefined || this.env.applier === undefined) return "unsupported";
    return this.env.applier.blocked();
  }

  /** Whether a check makes sense at all: a development run and an unsupported system have nothing to update. */
  private canCheck(): boolean {
    return this.st.blocked !== "unpackaged" && this.st.blocked !== "unsupported";
  }

  /** Starts the automatic checks when the preference is on. Safe to call again. */
  start(): void {
    this.stop();
    if (!this.canCheck()) return;
    const first = setTimeout(() => void this.autoCheck(), this.env.firstCheckMs ?? 20_000);
    const every = setInterval(() => void this.autoCheck(), this.env.intervalMs ?? 6 * 3600_000);
    first.unref();
    every.unref();
    this.timers = [first, every];
  }

  stop(): void {
    for (const t of this.timers) {
      clearTimeout(t);
      clearInterval(t);
    }
    this.timers = [];
  }

  private async autoCheck(): Promise<void> {
    if (this.st.prefs.auto) await this.check(false);
  }

  /** Looks for a newer release. A manual check reports a failure; an automatic one stays quiet about it. */
  async check(manual = true): Promise<void> {
    if (!this.canCheck() || this.st.phase === "checking" || this.st.phase === "downloading" || this.st.phase === "installing") return;
    const prior = this.st;
    this.set({ phase: "checking", error: undefined, outcome: undefined });
    const ctl = new AbortController();
    const timer = setTimeout(() => ctl.abort(), 30_000);
    try {
      const releases = await this.env.source.releases(ctl.signal);
      const found = pickCandidate(releases, this.env.version, this.st.prefs.prerelease, this.target!);
      this.candidate = found;
      const checkedAt = (this.env.now?.() ?? new Date()).toISOString();
      if (found === undefined) return this.set({ phase: "idle", latest: undefined, progress: undefined, checkedAt });
      this.set({ phase: "available", latest: found.release, progress: undefined, checkedAt });
      if (this.env.store.announced !== found.tag) {
        this.env.store.set({ announced: found.tag });
        if (!manual) this.env.notify({ kind: "available", version: found.release.version });
      }
    } catch (e) {
      if (manual) this.set({ phase: "error", error: this.fail(e, "network") });
      else this.set({ phase: prior.phase === "available" ? "available" : "idle", error: undefined });
    } finally {
      clearTimeout(timer);
    }
  }

  setPrefs(prefs: UpdatePrefs): void {
    this.env.store.set({ prefs });
    const changed = prefs.prerelease !== this.st.prefs.prerelease;
    this.set({ prefs });
    // The choice of channel changes which release is the newest, so the old answer is stale.
    if (changed && this.st.phase !== "downloading" && this.st.phase !== "installing") void this.check(true);
  }

  cancel(): void {
    if (this.st.phase === "downloading") this.abort?.abort();
  }

  /** Downloads and verifies the installer for the release `check` found, then hands over to it and quits. */
  async install(): Promise<{ ok: true } | { ok: false; error: UpdateError }> {
    const found = this.candidate;
    const applier = this.env.applier;
    if (this.st.phase !== "available" || found === undefined || applier === undefined || this.target === undefined) {
      return { ok: false, error: { code: "busy", message: "no update is ready to install" } };
    }
    if (this.st.blocked !== "") return { ok: false, error: { code: "blocked", message: `updates are not possible here (${this.st.blocked})` } };

    const ctl = new AbortController();
    this.abort = ctl;
    this.lastProgress = 0;
    this.set({ phase: "downloading", error: undefined, outcome: undefined, progress: { received: 0, total: found.release.size } });
    let installer: string;
    try {
      installer = await this.fetchInstaller(found, ctl.signal);
    } catch (e) {
      const error = this.fail(e, "network");
      // Cancelling is the user's own choice, not a failure to report.
      this.set({ phase: "available", error: error.code === "cancelled" ? undefined : error, progress: undefined });
      return { ok: false, error };
    } finally {
      this.abort = undefined;
    }

    this.set({ phase: "installing", progress: undefined });
    const stopped: string[] = [];
    try {
      for (const node of this.env.runningNodes()) {
        if (!NODE.test(node)) continue;
        if ((await this.env.stopNode(node)) === "stopped") stopped.push(node);
      }
    } catch (e) {
      await this.restart(stopped);
      const error = { code: "stop_failed", message: e instanceof Error ? e.message : String(e) };
      this.set({ phase: "available", error });
      return { ok: false, error };
    }

    const pending: Pending = { from: this.env.version, to: found.release.version, nodes: stopped };
    fs.writeFileSync(path.join(this.env.dataDir, PENDING_FILE), JSON.stringify(pending));
    fs.rmSync(path.join(this.env.dataDir, RESULT_FILE), { force: true });
    try {
      await applier.start({
        installer,
        execPath: this.env.execPath,
        pid: this.env.pid,
        dir: this.env.dataDir,
        resultFile: path.join(this.env.dataDir, RESULT_FILE),
      });
    } catch (e) {
      fs.rmSync(path.join(this.env.dataDir, PENDING_FILE), { force: true });
      await this.restart(stopped);
      let error: UpdateError;
      if (e instanceof ApplyError && e.code === "not_writable") {
        // No right to replace the app: show the disk image so the user can drag it over by hand.
        void this.env.openPath(installer);
        error = { code: "not_writable", message: e.message };
      } else {
        error = { code: "install_failed", message: e instanceof Error ? e.message : String(e) };
      }
      this.set({ phase: "available", error });
      return { ok: false, error };
    }
    this.env.quit();
    return { ok: true };
  }

  /** The installer file, downloaded and checked against the hash the release publishes. */
  private async fetchInstaller(found: Candidate, signal: AbortSignal): Promise<string> {
    fs.mkdirSync(this.env.dataDir, { recursive: true });
    const sums = parseChecksums(await this.env.source.text(found.tag, CHECKSUM_FILE, signal));
    const want = expectedHash(found.digest, sums, found.asset);
    if (!want.ok) {
      throw new UpdateFailure("bad_checksum", want.reason === "missing" ? "the release publishes no checksum for the installer" : "the checksums of the release disagree");
    }
    const dest = path.join(this.env.dataDir, found.asset);
    // A copy left by an earlier attempt that still has the right hash is used as it is.
    if (fs.existsSync(dest) && (await hashFile(dest)) === want.sha256) return dest;
    const part = `${dest}.part`;
    const got = await this.env.source.download(found.tag, found.asset, part, (p) => this.progress(p), signal);
    if (got.sha256 !== want.sha256) {
      fs.rmSync(part, { force: true });
      throw new UpdateFailure("bad_checksum", "the downloaded installer does not match its published checksum");
    }
    fs.renameSync(part, dest);
    return dest;
  }

  private progress(p: Progress): void {
    const now = Date.now();
    if (now - this.lastProgress < PROGRESS_EMIT_MS && p.received < p.total) return;
    this.lastProgress = now;
    this.set({ progress: { received: p.received, total: p.total > 0 ? p.total : (this.st.latest?.size ?? 0) } });
  }

  private async restart(nodes: string[]): Promise<void> {
    for (const n of nodes) await this.env.startNode(n).catch(() => {});
  }

  /**
   * Run once at start: when an update was in flight, brings the stopped kernels back and records whether the app
   * is now the version the update was heading to. Then clears what the update left behind.
   */
  async finishPending(): Promise<void> {
    const file = path.join(this.env.dataDir, PENDING_FILE);
    let pending: Pending | undefined;
    try {
      const raw = JSON.parse(fs.readFileSync(file, "utf8")) as Partial<Pending>;
      if (typeof raw.to === "string" && typeof raw.from === "string" && Array.isArray(raw.nodes)) {
        pending = { from: raw.from, to: raw.to, nodes: raw.nodes.filter((n): n is string => typeof n === "string" && NODE.test(n)) };
      }
    } catch {
      // no update in flight
    }
    let reason = "";
    try {
      reason = fs.readFileSync(path.join(this.env.dataDir, RESULT_FILE), "utf8").trim().split(/\r?\n/)[0] ?? "";
    } catch {
      // the script writes this only when it failed
    }
    this.cleanup();
    if (pending === undefined) return;
    await this.restart(pending.nodes);
    const ok = sameVersion(this.env.version, pending.to);
    const outcome: UpdateOutcome = ok ? { ok, version: pending.to } : { ok, version: pending.to, message: reason || `still ${this.env.version}` };
    this.set({ outcome });
    this.env.notify({ kind: ok ? "updated" : "failed", version: pending.to });
  }

  /** Removes installers, partial downloads, scripts and notes. Nothing in the directory is needed once the app runs. */
  private cleanup(): void {
    let names: string[];
    try {
      names = fs.readdirSync(this.env.dataDir);
    } catch {
      return;
    }
    for (const n of names) fs.rmSync(path.join(this.env.dataDir, n), { force: true, recursive: true });
  }

  private fail(e: unknown, fallback: string): UpdateError {
    if (e instanceof UpdateFailure) return { code: e.code, message: e.message };
    if (e instanceof SourceError) return { code: e.code === "cancelled" ? "cancelled" : e.code === "http" ? "http" : "network", message: e.message };
    return { code: fallback, message: e instanceof Error ? e.message : String(e) };
  }

  private set(patch: Partial<UpdateState>): void {
    const next: UpdateState = { ...this.st, ...patch };
    if (JSON.stringify(next) === JSON.stringify(this.st)) return;
    this.st = next;
    this.emit("change", next);
  }
}

class UpdateFailure extends Error {
  constructor(
    readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

async function hashFile(file: string): Promise<string> {
  const hash = createHash("sha256");
  for await (const chunk of fs.createReadStream(file)) hash.update(chunk as Buffer);
  return hash.digest("hex");
}
